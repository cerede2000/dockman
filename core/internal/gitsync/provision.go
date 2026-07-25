package gitsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/goccy/go-yaml"

	"github.com/RA341/dockman/internal/host/filesystem"
)

const (
	maxProvisionManifestSize = 64 << 10
	maxProvisionOperations   = 128
)

var gitCommitSHA = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

type provisionManifest struct {
	Version     int                   `yaml:"version"`
	Directories []provisionDirectory  `yaml:"directories,omitempty"`
	Permissions []provisionPermission `yaml:"permissions,omitempty"`
}

type provisionDirectory struct {
	Path string `yaml:"path"`
	Mode string `yaml:"mode,omitempty"`
	UID  *int   `yaml:"uid,omitempty"`
	GID  *int   `yaml:"gid,omitempty"`
}

type provisionPermission struct {
	Path string `yaml:"path"`
	Mode string `yaml:"mode,omitempty"`
	UID  *int   `yaml:"uid,omitempty"`
	GID  *int   `yaml:"gid,omitempty"`
}

type normalizedProvisionOperation struct {
	path      string
	directory bool
	mode      fs.FileMode
	setMode   bool
	uid       int
	gid       int
	setOwner  bool
}

type provisionSnapshot struct {
	path       string
	mode       fs.FileMode
	uid        int
	gid        int
	ownerKnown bool
}

type provisionTransaction struct {
	filesystem filesystem.FileSystem
	snapshots  []provisionSnapshot
	created    []string
	manifest   string
	operations int
}

func (s *Service) applyStackProvisioning(ctx context.Context, binding StackBinding, commit, composePath string, logs io.Writer) (*provisionTransaction, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	manifestName, manifest, err := s.loadProvisionManifest(binding, commit, composePath)
	if err != nil || manifest == nil {
		return nil, err
	}
	operations, err := normalizeProvisionManifest(*manifest)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", manifestName, err)
	}
	targetFS, targetRoot, err := s.resolveBindingStack(binding)
	if err != nil {
		return nil, err
	}
	stackDirectory := filepath.ToSlash(filepath.Dir(filepath.FromSlash(composePath)))
	if stackDirectory == "." {
		stackDirectory = ""
	}
	if err := validateProvisionStackRoot(targetFS, targetRoot, stackDirectory); err != nil {
		return nil, fmt.Errorf("validate stack directory for %s: %w", manifestName, err)
	}
	stackRoot := targetFS.Join(targetRoot, filepath.FromSlash(stackDirectory))
	tx := &provisionTransaction{filesystem: targetFS, manifest: manifestName, operations: len(operations)}
	if err := tx.apply(ctx, stackRoot, operations); err != nil {
		rollbackErr := tx.Rollback()
		if rollbackErr != nil {
			return nil, fmt.Errorf("apply %s: %w; provisioning rollback failed: %v", manifestName, err, rollbackErr)
		}
		return nil, fmt.Errorf("apply %s: %w; provisioning changes rolled back", manifestName, err)
	}
	_, _ = fmt.Fprintf(logs, "[dockman] applied %s securely (%d operation(s))\n", manifestName, len(operations))
	return tx, nil
}

func (s *Service) loadProvisionManifest(binding StackBinding, commit, composePath string) (string, *provisionManifest, error) {
	// Controlled synchronization always supplies an immutable commit SHA. Unit
	// callers and legacy rows without one simply have no provisioning input.
	if !gitCommitSHA.MatchString(commit) {
		return "", nil, nil
	}
	row, err := s.store.GetRepository(binding.RepositoryUUID)
	if err != nil {
		return "", nil, err
	}
	repository, err := s.openRepository(row)
	if err != nil {
		return "", nil, err
	}
	tree, err := repositoryCommitTreeAtHash(repository, plumbing.NewHash(commit))
	if err != nil {
		return "", nil, err
	}
	tree, err = repositorySubtree(tree, binding.SubPath)
	if err != nil {
		return "", nil, err
	}
	directory := filepath.ToSlash(filepath.Dir(filepath.FromSlash(composePath)))
	if directory == "." {
		directory = ""
	}
	candidates := []string{filepath.ToSlash(filepath.Join(directory, "provision.yml")), filepath.ToSlash(filepath.Join(directory, "provision.yaml"))}
	var found *object.File
	name := ""
	for _, candidate := range candidates {
		file, fileErr := tree.File(candidate)
		if errors.Is(fileErr, object.ErrFileNotFound) {
			continue
		}
		if fileErr != nil {
			return "", nil, fmt.Errorf("read provisioning manifest %s: %w", candidate, fileErr)
		}
		if found != nil {
			return "", nil, fmt.Errorf("stack %s contains both provision.yml and provision.yaml; keep exactly one", composePath)
		}
		found, name = file, candidate
	}
	if found == nil {
		return "", nil, nil
	}
	reader, err := found.Reader()
	if err != nil {
		return "", nil, fmt.Errorf("open %s: %w", name, err)
	}
	defer reader.Close()
	limited := io.LimitReader(reader, maxProvisionManifestSize+1)
	contents, err := io.ReadAll(limited)
	if err != nil {
		return "", nil, fmt.Errorf("read %s: %w", name, err)
	}
	if len(contents) > maxProvisionManifestSize {
		return "", nil, fmt.Errorf("%s exceeds %d KiB", name, maxProvisionManifestSize>>10)
	}
	var manifest provisionManifest
	decoder := yaml.NewDecoder(strings.NewReader(string(contents)), yaml.Strict())
	if err := decoder.Decode(&manifest); err != nil {
		return "", nil, fmt.Errorf("decode %s: %w", name, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != nil && !errors.Is(err, io.EOF) {
		return "", nil, fmt.Errorf("decode %s: %w", name, err)
	} else if err == nil && extra != nil {
		return "", nil, fmt.Errorf("%s must contain exactly one YAML document", name)
	}
	return name, &manifest, nil
}

func normalizeProvisionManifest(manifest provisionManifest) ([]normalizedProvisionOperation, error) {
	if manifest.Version != 1 {
		return nil, errors.New("version must be 1")
	}
	if len(manifest.Directories)+len(manifest.Permissions) == 0 {
		return nil, errors.New("at least one directory or permissions operation is required")
	}
	if len(manifest.Directories)+len(manifest.Permissions) > maxProvisionOperations {
		return nil, fmt.Errorf("at most %d operations are allowed", maxProvisionOperations)
	}
	result := make([]normalizedProvisionOperation, 0, len(manifest.Directories)+len(manifest.Permissions))
	seen := make(map[string]struct{})
	appendOperation := func(path, rawMode string, uid, gid *int, directory bool) error {
		path = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(path))))
		if err := validateRelativePath(path, false); err != nil {
			return err
		}
		if _, duplicate := seen[path]; duplicate {
			return fmt.Errorf("path %s is declared more than once", path)
		}
		seen[path] = struct{}{}
		operation := normalizedProvisionOperation{path: path, directory: directory}
		if rawMode != "" {
			mode, err := parseProvisionMode(rawMode)
			if err != nil {
				return fmt.Errorf("path %s: %w", path, err)
			}
			operation.mode, operation.setMode = mode, true
		} else if directory {
			operation.mode, operation.setMode = 0755, true
		}
		if (uid == nil) != (gid == nil) {
			return fmt.Errorf("path %s: uid and gid must be specified together", path)
		}
		if uid != nil {
			if *uid < 0 || *gid < 0 || *uid > 2147483647 || *gid > 2147483647 {
				return fmt.Errorf("path %s: uid and gid must be between 0 and 2147483647", path)
			}
			operation.uid, operation.gid, operation.setOwner = *uid, *gid, true
		}
		if !operation.setMode && !operation.setOwner {
			return fmt.Errorf("path %s: permissions must set mode and/or uid/gid", path)
		}
		result = append(result, operation)
		return nil
	}
	for _, directory := range manifest.Directories {
		if err := appendOperation(directory.Path, directory.Mode, directory.UID, directory.GID, true); err != nil {
			return nil, fmt.Errorf("directory: %w", err)
		}
	}
	for _, permission := range manifest.Permissions {
		if err := appendOperation(permission.Path, permission.Mode, permission.UID, permission.GID, false); err != nil {
			return nil, fmt.Errorf("permissions: %w", err)
		}
	}
	return result, nil
}

func parseProvisionMode(value string) (fs.FileMode, error) {
	value = strings.TrimSpace(value)
	if len(value) != 3 && len(value) != 4 {
		return 0, errors.New("mode must be a quoted octal string such as \"0750\"")
	}
	parsed, err := strconv.ParseUint(value, 8, 12)
	if err != nil || parsed > 0777 {
		return 0, errors.New("mode must contain only permission bits from 0000 to 0777")
	}
	return fs.FileMode(parsed), nil
}

func (tx *provisionTransaction) apply(ctx context.Context, root string, operations []normalizedProvisionOperation) error {
	// Validate every existing path before the first mutation. This avoids a
	// partially applied manifest for all schema and confinement failures.
	for _, operation := range operations {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := validateProvisionPath(tx.filesystem, root, operation.path, operation.directory); err != nil {
			return fmt.Errorf("%s: %w", operation.path, err)
		}
	}
	for _, operation := range operations {
		if err := ctx.Err(); err != nil {
			return err
		}
		full := tx.filesystem.Join(root, filepath.FromSlash(operation.path))
		info, err := tx.filesystem.Lstat(full)
		currentUID, currentGID, ownerKnown := 0, 0, false
		if errors.Is(err, os.ErrNotExist) && operation.directory {
			created, createdErr := missingProvisionDirectories(tx.filesystem, root, operation.path)
			if createdErr != nil {
				return createdErr
			}
			if err := tx.filesystem.MkdirAll(full, 0755); err != nil {
				return fmt.Errorf("create directory %s: %w", operation.path, err)
			}
			tx.created = append(tx.created, created...)
			if operation.setOwner {
				currentUID, currentGID, err = tx.filesystem.Ownership(full)
				if err != nil {
					return fmt.Errorf("read owner of created directory %s: %w", operation.path, err)
				}
				ownerKnown = true
			}
		} else if err != nil {
			return fmt.Errorf("inspect %s: %w", operation.path, err)
		} else {
			snapshot := provisionSnapshot{path: full, mode: info.Mode().Perm()}
			if operation.setOwner {
				snapshot.uid, snapshot.gid, err = tx.filesystem.Ownership(full)
				if err != nil {
					return fmt.Errorf("read owner of %s: %w", operation.path, err)
				}
				snapshot.ownerKnown = true
				currentUID, currentGID, ownerKnown = snapshot.uid, snapshot.gid, true
			}
			tx.snapshots = append(tx.snapshots, snapshot)
		}
		if operation.setOwner && (!ownerKnown || currentUID != operation.uid || currentGID != operation.gid) {
			if err := tx.filesystem.Chown(full, operation.uid, operation.gid); err != nil {
				return provisionChownError(operation.path, operation.uid, operation.gid, err)
			}
		}
		if operation.setMode {
			if err := tx.filesystem.Chmod(full, operation.mode); err != nil {
				return fmt.Errorf("chmod %s: %w", operation.path, err)
			}
		}
	}
	return nil
}

func provisionChownError(path string, uid, gid int, err error) error {
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("cannot change owner of %s to %d:%d: the Dockman or SSH account is not permitted to change ownership; remove uid/gid, use its current uid/gid, or explicitly grant CHOWN capability: %w", path, uid, gid, err)
	}
	return fmt.Errorf("change owner of %s to %d:%d: %w", path, uid, gid, err)
}

func validateProvisionPath(targetFS filesystem.FileSystem, root, relative string, allowMissing bool) error {
	parts := strings.Split(filepath.ToSlash(relative), "/")
	for index := range parts {
		candidate := targetFS.Join(root, filepath.FromSlash(strings.Join(parts[:index+1], "/")))
		info, err := targetFS.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) && allowMissing {
			return nil
		}
		if errors.Is(err, os.ErrNotExist) {
			missing := strings.Join(parts[:index+1], "/")
			if index < len(parts)-1 {
				return fmt.Errorf("parent directory %q does not exist after Git synchronization; permissions targets must already exist", missing)
			}
			return errors.New("target does not exist after Git synchronization; permissions can only be applied to existing files or directories")
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symbolic links are forbidden")
		}
		if index < len(parts)-1 && !info.IsDir() {
			return errors.New("a parent component is not a directory")
		}
		if index == len(parts)-1 {
			if allowMissing && !info.IsDir() {
				return errors.New("directory operation target is not a directory")
			}
			if !allowMissing && !info.IsDir() && !info.Mode().IsRegular() {
				return errors.New("only regular files and directories can be provisioned")
			}
		}
	}
	return nil
}

func validateProvisionStackRoot(targetFS filesystem.FileSystem, targetRoot, relative string) error {
	if relative != "" {
		if err := validateProvisionPath(targetFS, targetRoot, relative, false); err != nil {
			return err
		}
	}
	root := targetFS.Join(targetRoot, filepath.FromSlash(relative))
	info, err := targetFS.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("stack root must be a real directory")
	}
	return nil
}

func missingProvisionDirectories(targetFS filesystem.FileSystem, root, relative string) ([]string, error) {
	parts := strings.Split(filepath.ToSlash(relative), "/")
	created := make([]string, 0, len(parts))
	for index := range parts {
		candidate := targetFS.Join(root, filepath.FromSlash(strings.Join(parts[:index+1], "/")))
		info, err := targetFS.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			created = append(created, candidate)
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, errors.New("directory path contains an unsafe component")
		}
	}
	return created, nil
}

func (tx *provisionTransaction) Rollback() error {
	if tx == nil {
		return nil
	}
	return errors.Join(tx.RollbackMetadata(), tx.RollbackCreatedDirectories())
}

func (tx *provisionTransaction) RollbackMetadata() error {
	if tx == nil {
		return nil
	}
	var rollbackErrors []error
	for index := len(tx.snapshots) - 1; index >= 0; index-- {
		snapshot := tx.snapshots[index]
		if snapshot.ownerKnown {
			if err := tx.filesystem.Chown(snapshot.path, snapshot.uid, snapshot.gid); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("restore owner of %s: %w", snapshot.path, err))
			}
		}
		if err := tx.filesystem.Chmod(snapshot.path, snapshot.mode); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restore mode of %s: %w", snapshot.path, err))
		}
	}
	return errors.Join(rollbackErrors...)
}

func (tx *provisionTransaction) RollbackCreatedDirectories() error {
	if tx == nil {
		return nil
	}
	var rollbackErrors []error
	sort.Slice(tx.created, func(i, j int) bool { return len(tx.created[i]) > len(tx.created[j]) })
	for _, path := range tx.created {
		entries, err := tx.filesystem.ReadDir(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("inspect created directory %s: %w", path, err))
			continue
		}
		if len(entries) != 0 {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("refusing to remove non-empty provisioned directory %s", path))
			continue
		}
		if err := tx.filesystem.RemoveAll(path); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("remove provisioned directory %s: %w", path, err))
		}
	}
	return errors.Join(rollbackErrors...)
}
