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
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/goccy/go-yaml"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/RA341/dockman/internal/host/filesystem"
)

const (
	maxProvisionManifestSize = 64 << 10
	maxProvisionOperations   = 128
)

var gitCommitSHA = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

func (s *Service) cleanupStaleProvisionStaging(now time.Time) {
	bindings, err := s.store.ListBindings()
	if err != nil {
		return
	}
	seen := make(map[string]struct{})
	for _, binding := range bindings {
		targetFS, root, err := s.resolveBindingStack(binding)
		if err != nil {
			continue
		}
		for _, composePath := range splitPatternLines(binding.ComposePaths) {
			directory := filepath.ToSlash(filepath.Dir(filepath.FromSlash(composePath)))
			if directory == "." {
				directory = ""
			}
			stackRoot := targetFS.Join(root, filepath.FromSlash(directory))
			key := binding.Host + "\x00" + stackRoot
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			// Staging directories are direct children of a provisioned stack.
			// Inspect only catalogued stack roots instead of recursively walking
			// the whole folder link and its potentially huge data trees.
			entries, readErr := targetFS.ReadDir(stackRoot)
			if readErr != nil {
				continue
			}
			for _, entry := range entries {
				if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".dockman-provision-staging-") {
					continue
				}
				info, infoErr := entry.Info()
				if infoErr != nil || now.Sub(info.ModTime()) < 24*time.Hour {
					continue
				}
				stale := targetFS.Join(stackRoot, entry.Name())
				if removeErr := targetFS.RemoveAll(stale); removeErr != nil {
					log.Warn().Str("path", stale).Err(removeErr).Msg("unable to remove stale Git provisioning staging directory")
					continue
				}
				log.Info().Str("path", stale).Msg("removed stale Git provisioning staging directory")
			}
		}
	}
}

type provisionManifest struct {
	Version     int                   `yaml:"version"`
	Directories []provisionDirectory  `yaml:"directories,omitempty"`
	Permissions []provisionPermission `yaml:"permissions,omitempty"`
	Remove      []provisionRemoval    `yaml:"remove,omitempty"`
}

type provisionRemoval struct {
	Path      string `yaml:"path"`
	Type      string `yaml:"type"`
	Recursive bool   `yaml:"recursive,omitempty"`
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
	remove    bool
	recursive bool
}

type provisionSnapshot struct {
	path         string
	mode         fs.FileMode
	uid          int
	gid          int
	ownerKnown   bool
	restoreMode  bool
	restoreOwner bool
}

type provisionTransaction struct {
	filesystem filesystem.FileSystem
	snapshots  []provisionSnapshot
	created    []string
	manifest   string
	operations int
	removed    []provisionRemovedPath
	staging    string
	backupID   string
}

type provisionRemovedPath struct {
	original string
	staged   string
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
	if err := validateProtectedProvisionRemovals(binding, stackDirectory, composePath, operations); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", manifestName, err)
	}
	if err := validateProvisionOperations(ctx, targetFS, stackRoot, operations); err != nil {
		return nil, fmt.Errorf("validate %s: %w", manifestName, err)
	}
	backupID, err := s.backupProvisionRemovals(ctx, binding, commit, stackDirectory, targetFS, targetRoot, operations)
	if err != nil {
		return nil, fmt.Errorf("mandatory backup before %s removal: %w", manifestName, err)
	}
	tx.backupID = backupID
	if err := tx.apply(ctx, stackRoot, operations); err != nil {
		rollbackErr := tx.Rollback()
		if rollbackErr != nil {
			return nil, fmt.Errorf("apply %s: %w; provisioning rollback failed: %v", manifestName, err, rollbackErr)
		}
		return nil, fmt.Errorf("apply %s: %w; provisioning changes rolled back", manifestName, err)
	}
	_, _ = fmt.Fprintf(logs, "[dockman] applied %s securely (%d operation(s))", manifestName, len(operations))
	if backupID != "" {
		_, _ = fmt.Fprintf(logs, "; mandatory deletion backup %s", backupID)
	}
	_, _ = fmt.Fprintln(logs)
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
	operationCount := len(manifest.Directories) + len(manifest.Permissions) + len(manifest.Remove)
	if operationCount == 0 {
		return nil, errors.New("at least one directory, permissions, or remove operation is required")
	}
	if operationCount > maxProvisionOperations {
		return nil, fmt.Errorf("at most %d operations are allowed", maxProvisionOperations)
	}
	result := make([]normalizedProvisionOperation, 0, operationCount)
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
	for _, removal := range manifest.Remove {
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(removal.Path))))
		if err := validateRelativePath(clean, false); err != nil {
			return nil, fmt.Errorf("remove: %w", err)
		}
		if _, duplicate := seen[clean]; duplicate {
			return nil, fmt.Errorf("remove: path %s is declared more than once", clean)
		}
		kind := strings.ToLower(strings.TrimSpace(removal.Type))
		if kind != "file" && kind != "directory" {
			return nil, fmt.Errorf("remove: path %s must explicitly set type to file or directory", clean)
		}
		if removal.Recursive && kind != "directory" {
			return nil, fmt.Errorf("remove: path %s can only be recursive when type is directory", clean)
		}
		seen[clean] = struct{}{}
		result = append(result, normalizedProvisionOperation{path: clean, directory: kind == "directory", remove: true, recursive: removal.Recursive})
	}
	for left := range result {
		for right := left + 1; right < len(result); right++ {
			if !result[left].remove && !result[right].remove {
				continue
			}
			if result[left].path == result[right].path || strings.HasPrefix(result[left].path, result[right].path+"/") || strings.HasPrefix(result[right].path, result[left].path+"/") {
				return nil, fmt.Errorf("paths %s and %s overlap; each provisioned path must be independent", result[left].path, result[right].path)
			}
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
		if operation.remove {
			if err := validateProvisionRemoval(tx.filesystem, root, operation); err != nil {
				return fmt.Errorf("%s: %w", operation.path, err)
			}
			continue
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
		if operation.remove {
			if _, err := tx.filesystem.Lstat(full); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return fmt.Errorf("inspect removal target %s: %w", operation.path, err)
			}
			if tx.staging == "" {
				tx.staging = tx.filesystem.Join(root, ".dockman-provision-staging-"+uuid.NewString())
				if err := tx.filesystem.MkdirAll(tx.staging, 0700); err != nil {
					return fmt.Errorf("create protected removal staging directory: %w", err)
				}
			}
			staged := tx.filesystem.Join(tx.staging, filepath.FromSlash(operation.path))
			if err := tx.filesystem.MkdirAll(filepath.Dir(staged), 0700); err != nil {
				return fmt.Errorf("prepare protected staging path for %s: %w", operation.path, err)
			}
			if err := tx.filesystem.Rename(full, staged); err != nil {
				return fmt.Errorf("stage %s for protected removal: %w", operation.path, err)
			}
			tx.removed = append(tx.removed, provisionRemovedPath{original: full, staged: staged})
			continue
		}
		info, err := tx.filesystem.Lstat(full)
		currentUID, currentGID, ownerKnown := 0, 0, false
		currentMode, modeKnown := fs.FileMode(0), false
		snapshotIndex := -1
		if errors.Is(err, os.ErrNotExist) && operation.directory {
			created, createdErr := missingProvisionDirectories(tx.filesystem, root, operation.path)
			if createdErr != nil {
				return createdErr
			}
			createMode := fs.FileMode(0755)
			if operation.setMode {
				createMode = operation.mode
			}
			if err := tx.filesystem.MkdirAll(full, createMode); err != nil {
				return fmt.Errorf("create directory %s: %w", operation.path, err)
			}
			tx.created = append(tx.created, created...)
			info, err = tx.filesystem.Lstat(full)
			if err != nil {
				return fmt.Errorf("inspect created directory %s: %w", operation.path, err)
			}
			currentMode, modeKnown = info.Mode().Perm(), true
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
			currentMode, modeKnown = snapshot.mode, true
			if operation.setOwner {
				snapshot.uid, snapshot.gid, err = tx.filesystem.Ownership(full)
				if err != nil {
					return fmt.Errorf("read owner of %s: %w", operation.path, err)
				}
				snapshot.ownerKnown = true
				currentUID, currentGID, ownerKnown = snapshot.uid, snapshot.gid, true
			}
			tx.snapshots = append(tx.snapshots, snapshot)
			snapshotIndex = len(tx.snapshots) - 1
		}
		// Apply permissions while Dockman still owns the path, then transfer
		// ownership. With a least-privilege CHOWN-only capability set, doing
		// this in the opposite order would require FOWNER just to apply the
		// requested mode to the newly transferred path.
		if operation.setMode && (!modeKnown || currentMode != operation.mode) {
			if err := tx.filesystem.Chmod(full, operation.mode); err != nil {
				info, statErr := tx.filesystem.Lstat(full)
				if statErr != nil || info.Mode().Perm() != operation.mode {
					return provisionChmodError(operation.path, currentMode, operation.mode, err)
				}
			} else if snapshotIndex >= 0 {
				tx.snapshots[snapshotIndex].restoreMode = true
			}
		}
		if operation.setOwner && (!ownerKnown || currentUID != operation.uid || currentGID != operation.gid) {
			if err := tx.filesystem.Chown(full, operation.uid, operation.gid); err != nil {
				return provisionChownError(operation.path, operation.uid, operation.gid, err)
			}
			if snapshotIndex >= 0 {
				tx.snapshots[snapshotIndex].restoreOwner = true
			}
		}
	}
	return nil
}

func validateProvisionOperations(ctx context.Context, targetFS filesystem.FileSystem, root string, operations []normalizedProvisionOperation) error {
	for _, operation := range operations {
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		if operation.remove {
			err = validateProvisionRemoval(targetFS, root, operation)
		} else {
			err = validateProvisionPath(targetFS, root, operation.path, operation.directory)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", operation.path, err)
		}
	}
	return nil
}

func validateProvisionRemoval(targetFS filesystem.FileSystem, root string, operation normalizedProvisionOperation) error {
	full := targetFS.Join(root, filepath.FromSlash(operation.path))
	info, err := targetFS.Lstat(full)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := validateProvisionPath(targetFS, root, operation.path, operation.directory); err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("symbolic links are forbidden")
	}
	if operation.directory != info.IsDir() {
		return fmt.Errorf("declared type does not match the existing %s", provisionFileType(info))
	}
	if !operation.directory && !info.Mode().IsRegular() {
		return errors.New("only regular files and directories can be removed")
	}
	if operation.directory && !operation.recursive {
		entries, err := targetFS.ReadDir(full)
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			return errors.New("directory is not empty; set recursive: true explicitly")
		}
	}
	return nil
}

func provisionFileType(info fs.FileInfo) string {
	if info.IsDir() {
		return "directory"
	}
	if info.Mode().IsRegular() {
		return "file"
	}
	return "special file"
}

func validateProtectedProvisionRemovals(binding StackBinding, stackDirectory, currentComposePath string, operations []normalizedProvisionOperation) error {
	protected := []string{"provision.yml", "provision.yaml"}
	composePaths := append(splitPatternLines(binding.ComposePaths), currentComposePath)
	for _, compose := range uniqueSortedStrings(composePaths) {
		compose = filepath.ToSlash(filepath.Clean(filepath.FromSlash(compose)))
		if stackDirectory == "" {
			protected = append(protected, compose)
		} else if relative, err := filepath.Rel(filepath.FromSlash(stackDirectory), filepath.FromSlash(compose)); err == nil {
			relative = filepath.ToSlash(relative)
			if relative != ".." && !strings.HasPrefix(relative, "../") {
				protected = append(protected, relative)
			}
		}
	}
	for _, operation := range operations {
		if !operation.remove {
			continue
		}
		if strings.HasPrefix(operation.path, ".dockman-provision-staging-") {
			return fmt.Errorf("remove path %s uses Dockman's reserved staging namespace", operation.path)
		}
		for _, candidate := range protected {
			if operation.path == candidate || strings.HasPrefix(candidate, operation.path+"/") {
				return fmt.Errorf("remove path %s would delete protected control or Compose file %s", operation.path, candidate)
			}
		}
	}
	return nil
}

func provisionChmodError(path string, current, requested fs.FileMode, err error) error {
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("cannot change permissions of %s from %04o to %04o: the underlying filesystem or Dockman/SSH account refused chmod; use the effective mode provided by the mount, fix its ownership or mount options, or run Dockman with an account that owns the path: %w", path, current.Perm(), requested.Perm(), err)
	}
	return fmt.Errorf("change permissions of %s from %04o to %04o: %w", path, current.Perm(), requested.Perm(), err)
}

func provisionChownError(path string, uid, gid int, err error) error {
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("cannot change owner of %s to %d:%d: the Dockman or SSH account is not permitted to change ownership; remove uid/gid, use Dockman's current PUID/PGID, or for deliberate cross-owner provisioning run Dockman with PUID=0 and explicitly grant CHOWN capability: %w", path, uid, gid, err)
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
	return errors.Join(tx.RollbackMetadata(), tx.RollbackRemovedPaths(), tx.RollbackCreatedDirectories())
}

func (tx *provisionTransaction) RollbackRemovedPaths() error {
	if tx == nil || tx.staging == "" {
		return nil
	}
	var rollbackErrors []error
	for index := len(tx.removed) - 1; index >= 0; index-- {
		removed := tx.removed[index]
		if _, err := tx.filesystem.Lstat(removed.original); err == nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("refusing to overwrite %s while restoring a protected removal", removed.original))
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			rollbackErrors = append(rollbackErrors, err)
			continue
		}
		if err := tx.filesystem.Rename(removed.staged, removed.original); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restore protected removal %s: %w", removed.original, err))
		}
	}
	if len(rollbackErrors) == 0 {
		if err := tx.filesystem.RemoveAll(tx.staging); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("remove provisioning staging directory: %w", err))
		}
	}
	return errors.Join(rollbackErrors...)
}

func (tx *provisionTransaction) Commit() error {
	if tx == nil || tx.staging == "" {
		return nil
	}
	if err := tx.filesystem.RemoveAll(tx.staging); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("purge protected removal staging directory: %w", err)
	}
	tx.removed = nil
	tx.staging = ""
	return nil
}

func (tx *provisionTransaction) RollbackMetadata() error {
	if tx == nil {
		return nil
	}
	var rollbackErrors []error
	for index := len(tx.snapshots) - 1; index >= 0; index-- {
		snapshot := tx.snapshots[index]
		if snapshot.restoreOwner && snapshot.ownerKnown {
			if err := tx.filesystem.Chown(snapshot.path, snapshot.uid, snapshot.gid); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("restore owner of %s: %w", snapshot.path, err))
			}
		}
		if snapshot.restoreMode {
			if err := tx.filesystem.Chmod(snapshot.path, snapshot.mode); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("restore mode of %s: %w", snapshot.path, err))
			}
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
