package gitsync

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/RA341/dockman/internal/host/filesystem"
	gitclient "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/goccy/go-yaml"
	"github.com/google/uuid"
)

const (
	maxBindingFiles      = 20_000
	maxBindingFileSize   = 100 << 20
	maxBindingTotalSize  = 2 << 30
	transferBufferSize   = 64 << 10
	sensitiveConfirmText = "INCLUDE SENSITIVE FILES"
)

var transferBufferPool = sync.Pool{New: func() any { return make([]byte, transferBufferSize) }}

type BindingInput struct {
	RepositoryID string `json:"repositoryId"`
	Host         string `json:"host"`
	StackPath    string `json:"stackPath"`
	SubPath      string `json:"subPath"`
}

type BindingView struct {
	ID             string    `json:"id"`
	RepositoryID   string    `json:"repositoryId"`
	RepositoryName string    `json:"repositoryName"`
	Host           string    `json:"host"`
	StackPath      string    `json:"stackPath"`
	SubPath        string    `json:"subPath"`
	ComposePaths   []string  `json:"composePaths"`
	Enabled        bool      `json:"enabled"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type StackTarget struct {
	Host         string   `json:"host"`
	Path         string   `json:"path"`
	ComposePaths []string `json:"composePaths"`
	Scope        string   `json:"scope"`
	StackCount   int      `json:"stackCount"`
}

type TransferInput struct {
	IncludeSensitive      bool   `json:"includeSensitive"`
	SensitiveConfirmation string `json:"sensitiveConfirmation"`
	CommitMessage         string `json:"commitMessage"`
	PreviewToken          string `json:"previewToken"`
}

type PreviewEntry struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	SourceSHA string `json:"sourceSha,omitempty"`
	TargetSHA string `json:"targetSha,omitempty"`
	Size      int64  `json:"size,omitempty"`
	Sensitive bool   `json:"sensitive,omitempty"`
}

type TransferPreview struct {
	BindingID    string         `json:"bindingId"`
	Direction    string         `json:"direction"`
	Entries      []PreviewEntry `json:"entries"`
	Changed      int            `json:"changed"`
	Unchanged    int            `json:"unchanged"`
	Skipped      int            `json:"skipped"`
	DeletionMode string         `json:"deletionMode"`
	PreviewToken string         `json:"previewToken"`
}

type TransferResult struct {
	Preview   TransferPreview `json:"preview"`
	CommitSHA string          `json:"commitSha,omitempty"`
	Backup    string          `json:"backup,omitempty"`
	Message   string          `json:"message"`
}

type transferFile struct {
	path      string
	mode      fs.FileMode
	size      int64
	sha       string
	sensitive bool
	open      func() (io.ReadCloser, error)
}

type rootedReadCloser struct {
	file *os.File
	root *os.Root
}

func (r *rootedReadCloser) Read(p []byte) (int, error) { return r.file.Read(p) }
func (r *rootedReadCloser) Close() error {
	fileErr := r.file.Close()
	rootErr := r.root.Close()
	if fileErr != nil {
		return fileErr
	}
	return rootErr
}

func (s *Service) ListBindings() ([]BindingView, error) {
	rows, err := s.store.ListBindings()
	if err != nil {
		return nil, err
	}
	out := make([]BindingView, 0, len(rows))
	for _, row := range rows {
		view, err := s.bindingView(row)
		if err != nil {
			return nil, err
		}
		out = append(out, view)
	}
	return out, nil
}

func (s *Service) CreateBinding(input BindingInput) (BindingView, error) {
	if !s.enabled {
		return BindingView{}, errors.New("Git synchronization is disabled")
	}
	clean, targetFS, targetRoot, err := s.validateBindingInput(input)
	if err != nil {
		return BindingView{}, err
	}
	compose := discoverComposeFiles(targetFS, targetRoot)
	row := StackBinding{
		UUID: uuid.NewString(), RepositoryUUID: clean.RepositoryID, Host: clean.Host,
		StackPath: clean.StackPath, SubPath: clean.SubPath,
		ComposePaths: strings.Join(compose, "\n"), Enabled: true,
	}
	if err := s.store.SaveBinding(&row); err != nil {
		return BindingView{}, err
	}
	return s.bindingView(row)
}

func (s *Service) DeleteBinding(id string) error {
	if _, err := s.store.GetBinding(id); err != nil {
		return err
	}
	return s.store.DeleteBinding(id)
}

func (s *Service) ListStackTargets() ([]StackTarget, error) {
	if s.stackResolver == nil || s.hostLister == nil {
		return nil, errors.New("stack filesystem access is not configured")
	}
	hosts := append([]string(nil), s.hostLister()...)
	sort.Strings(hosts)
	result := make([]StackTarget, 0)
	for _, host := range hosts {
		targetFS, root, err := s.stackResolver(host, "compose")
		if err != nil {
			continue
		}
		composeRoot := discoverComposeFiles(targetFS, root)
		result = append(result, StackTarget{Host: host, Path: "compose", ComposePaths: composeRoot, Scope: "all_stacks", StackCount: countComposeFolders(composeRoot)})
		entries, err := targetFS.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			rel := targetFS.Join(root, entry.Name())
			compose := discoverComposeFiles(targetFS, rel)
			if len(compose) > 0 {
				result = append(result, StackTarget{Host: host, Path: filepath.ToSlash(filepath.Join("compose", entry.Name())), ComposePaths: compose, Scope: "folder", StackCount: countComposeFolders(compose)})
			}
		}
	}
	return result, nil
}

func (s *Service) PreviewBinding(id, direction string, input TransferInput) (TransferPreview, error) {
	binding, source, target, err := s.loadTransferTrees(id, direction, input)
	if err != nil {
		return TransferPreview{}, err
	}
	return buildPreview(binding.UUID, direction, source, target), nil
}

func (s *Service) ExportBinding(ctx context.Context, id string, input TransferInput) (TransferResult, error) {
	binding, err := s.store.GetBinding(id)
	if err != nil {
		return TransferResult{}, err
	}
	lock := s.repositoryLock(binding.RepositoryUUID)
	lock.Lock()
	defer lock.Unlock()

	var result TransferResult
	err = s.runBindingOperation(ctx, binding.RepositoryUUID, binding.UUID, "stack_export", func(ctx context.Context) error {
		if _, err := s.fetchRepositoryLocked(ctx, binding.RepositoryUUID); err != nil {
			return err
		}
		status, err := s.RepositoryStatus(binding.RepositoryUUID)
		if err != nil {
			return err
		}
		if !status.Clean || status.Behind > 0 || status.Diverged {
			return errors.New("export refused: pull remote changes and resolve repository state first")
		}
		_, source, target, err := s.loadTransferTrees(binding.UUID, "stack_to_repository", input)
		if err != nil {
			return err
		}
		result.Preview = buildPreview(binding.UUID, "stack_to_repository", source, target)
		if err := validatePreviewToken(input.PreviewToken, result.Preview.PreviewToken); err != nil {
			return err
		}
		if result.Preview.Changed == 0 {
			result.Message = "Repository already matches the stack"
			return nil
		}
		repoPath, err := s.repositoryPath(binding.RepositoryUUID)
		if err != nil {
			return err
		}
		if err := writeRepositoryFiles(repoPath, binding.SubPath, changedTransferFiles(source, target)); err != nil {
			return err
		}
		row, err := s.store.GetRepository(binding.RepositoryUUID)
		if err != nil {
			return err
		}
		repo, err := s.openRepository(row)
		if err != nil {
			return err
		}
		worktree, err := repo.Worktree()
		if err != nil {
			return err
		}
		stagePath := binding.SubPath
		if stagePath == "." {
			stagePath = ""
		}
		if err := worktree.AddWithOptions(&gitclient.AddOptions{Path: stagePath}); err != nil {
			return fmt.Errorf("stage stack files: %w", err)
		}
		message := strings.TrimSpace(input.CommitMessage)
		if message == "" {
			message = "chore(stack): sync " + binding.StackPath + " from Dockman"
		}
		if len(message) > 300 || strings.ContainsAny(message, "\r\n") {
			return errors.New("commit message must be one line and at most 300 characters")
		}
		hash, err := worktree.Commit(message, &gitclient.CommitOptions{Author: &object.Signature{Name: "Dockman Git Sync", Email: "dockman@localhost.invalid", When: time.Now().UTC()}})
		if err != nil {
			return fmt.Errorf("commit stack export: %w", err)
		}
		auth, err := s.authForRepository(ctx, row)
		if err != nil {
			return err
		}
		if err := repo.PushContext(ctx, &gitclient.PushOptions{RemoteName: "origin", Auth: auth}); err != nil && !errors.Is(err, gitclient.NoErrAlreadyUpToDate) {
			return fmt.Errorf("push stack export: %w", err)
		}
		result.CommitSHA, result.Message = hash.String(), "Stack exported, committed, and pushed"
		return nil
	})
	return result, err
}

func (s *Service) ImportBinding(ctx context.Context, id string, input TransferInput) (TransferResult, error) {
	binding, err := s.store.GetBinding(id)
	if err != nil {
		return TransferResult{}, err
	}
	lock := s.repositoryLock(binding.RepositoryUUID)
	lock.Lock()
	defer lock.Unlock()

	var result TransferResult
	err = s.runBindingOperation(ctx, binding.RepositoryUUID, binding.UUID, "stack_import", func(ctx context.Context) error {
		if _, err := s.fetchRepositoryLocked(ctx, binding.RepositoryUUID); err != nil {
			return err
		}
		status, err := s.RepositoryStatus(binding.RepositoryUUID)
		if err != nil {
			return err
		}
		if !status.Clean || status.Ahead > 0 || status.Behind > 0 || status.Diverged {
			return errors.New("import refused: pull remote changes and resolve repository state first")
		}
		_, source, target, err := s.loadTransferTrees(binding.UUID, "repository_to_stack", input)
		if err != nil {
			return err
		}
		if err := validateComposeFiles(source); err != nil {
			return err
		}
		result.Preview = buildPreview(binding.UUID, "repository_to_stack", source, target)
		if err := validatePreviewToken(input.PreviewToken, result.Preview.PreviewToken); err != nil {
			return err
		}
		if result.Preview.Changed == 0 {
			result.Message = "Stack already matches the repository"
			return nil
		}
		targetFS, targetRoot, err := s.resolveBindingStack(binding)
		if err != nil {
			return err
		}
		backup, err := s.backupChangedFiles(binding, targetFS, targetRoot, source, target)
		if err != nil {
			return err
		}
		if err := writeStackFiles(targetFS, targetRoot, changedTransferFiles(source, target)); err != nil {
			return err
		}
		result.Backup, result.Message = backup, "Repository files imported with a backup; the stack was not deployed"
		return nil
	})
	return result, err
}

func (s *Service) validateBindingInput(input BindingInput) (BindingInput, filesystem.FileSystem, string, error) {
	input.RepositoryID = strings.TrimSpace(input.RepositoryID)
	input.Host = strings.TrimSpace(input.Host)
	input.StackPath = strings.TrimSpace(filepath.ToSlash(input.StackPath))
	input.SubPath = strings.TrimSpace(filepath.ToSlash(input.SubPath))
	if input.SubPath == "" {
		input.SubPath = "."
	}
	repository, err := s.store.GetRepository(input.RepositoryID)
	if err != nil {
		return input, nil, "", err
	}
	if _, err := s.openRepository(repository); err != nil {
		return input, nil, "", fmt.Errorf("repository workspace is not ready: %w", err)
	}
	if input.Host == "" || len(input.Host) > 255 {
		return input, nil, "", errors.New("host is required")
	}
	if err := validateRelativePath(input.StackPath, false); err != nil {
		return input, nil, "", fmt.Errorf("invalid stack path: %w", err)
	}
	if err := validateRelativePath(input.SubPath, true); err != nil {
		return input, nil, "", fmt.Errorf("invalid repository subpath: %w", err)
	}
	if s.stackResolver == nil {
		return input, nil, "", errors.New("stack filesystem access is not configured")
	}
	rows, err := s.store.ListBindings()
	if err != nil {
		return input, nil, "", err
	}
	for _, row := range rows {
		if row.Host == input.Host && pathsOverlap(row.StackPath, input.StackPath) {
			return input, nil, "", fmt.Errorf("source folder overlaps existing link %s on host %s; remove the narrower link before linking its parent", row.StackPath, row.Host)
		}
		if row.RepositoryUUID == input.RepositoryID && pathsOverlap(row.SubPath, input.SubPath) {
			return input, nil, "", fmt.Errorf("repository path overlaps stack link %s on host %s", row.StackPath, row.Host)
		}
	}
	targetFS, targetRoot, err := s.stackResolver(input.Host, input.StackPath)
	if err != nil {
		return input, nil, "", fmt.Errorf("resolve stack path: %w", err)
	}
	if info, err := targetFS.Stat(targetRoot); err != nil && !errors.Is(err, os.ErrNotExist) {
		return input, nil, "", fmt.Errorf("inspect stack path: %w", err)
	} else if err == nil && !info.IsDir() {
		return input, nil, "", errors.New("stack path must be a directory")
	}
	return input, targetFS, targetRoot, nil
}

func pathsOverlap(left, right string) bool {
	left = strings.Trim(filepath.ToSlash(filepath.Clean(filepath.FromSlash(left))), "/")
	right = strings.Trim(filepath.ToSlash(filepath.Clean(filepath.FromSlash(right))), "/")
	if left == "." || right == "." {
		return true
	}
	return left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}

func validateRelativePath(value string, allowDot bool) error {
	if value == "" || len(value) > 500 || strings.ContainsRune(value, '\x00') {
		return errors.New("path is empty or too long")
	}
	if filepath.IsAbs(value) || strings.HasPrefix(value, "/") {
		return errors.New("absolute paths are forbidden")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if clean == "." && allowDot {
		return nil
	}
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return errors.New("path traversal is forbidden")
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".git" {
			return errors.New(".git paths are forbidden")
		}
	}
	return nil
}

func (s *Service) bindingView(row StackBinding) (BindingView, error) {
	repository, err := s.store.GetRepository(row.RepositoryUUID)
	if err != nil {
		return BindingView{}, err
	}
	compose := []string{}
	if row.ComposePaths != "" {
		compose = strings.Split(row.ComposePaths, "\n")
	}
	return BindingView{ID: row.UUID, RepositoryID: row.RepositoryUUID, RepositoryName: repository.Name, Host: row.Host, StackPath: row.StackPath, SubPath: row.SubPath, ComposePaths: compose, Enabled: row.Enabled, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, nil
}

func discoverComposeFiles(targetFS filesystem.FileSystem, root string) []string {
	names := make([]string, 0)
	visited := 0
	var walk func(string, string, int)
	walk = func(directory, relative string, depth int) {
		if depth > 8 || visited >= 1000 || len(names) >= 500 {
			return
		}
		visited++
		entries, err := targetFS.ReadDir(directory)
		if err != nil {
			return
		}
		for _, entry := range entries {
			childRelative := filepath.ToSlash(filepath.Join(relative, entry.Name()))
			if entry.Type()&os.ModeSymlink != 0 || shouldSkipPath(childRelative, entry.IsDir()) {
				continue
			}
			if entry.IsDir() {
				walk(targetFS.Join(directory, entry.Name()), childRelative, depth+1)
				continue
			}
			switch strings.ToLower(entry.Name()) {
			case "compose.yml", "compose.yaml", "docker-compose.yml", "docker-compose.yaml":
				names = append(names, childRelative)
			}
		}
	}
	walk(root, "", 0)
	sort.Strings(names)
	return names
}

func countComposeFolders(paths []string) int {
	folders := make(map[string]struct{}, len(paths))
	for _, composePath := range paths {
		folders[filepath.ToSlash(filepath.Dir(composePath))] = struct{}{}
	}
	return len(folders)
}

func (s *Service) resolveBindingStack(binding StackBinding) (filesystem.FileSystem, string, error) {
	if s.stackResolver == nil {
		return nil, "", errors.New("stack filesystem access is not configured")
	}
	return s.stackResolver(binding.Host, binding.StackPath)
}

func (s *Service) loadTransferTrees(id, direction string, input TransferInput) (StackBinding, map[string]transferFile, map[string]transferFile, error) {
	if err := validateSensitiveOptIn(input); err != nil {
		return StackBinding{}, nil, nil, err
	}
	binding, err := s.store.GetBinding(id)
	if err != nil {
		return StackBinding{}, nil, nil, err
	}
	targetFS, targetRoot, err := s.resolveBindingStack(binding)
	if err != nil {
		return binding, nil, nil, err
	}
	stackFiles, err := collectStackFiles(targetFS, targetRoot, input.IncludeSensitive)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return binding, nil, nil, fmt.Errorf("read stack files: %w", err)
	}
	repositoryRoot, err := s.repositoryPath(binding.RepositoryUUID)
	if err != nil {
		return binding, nil, nil, err
	}
	repositoryFiles, err := collectRepositoryFiles(repositoryRoot, binding.SubPath, input.IncludeSensitive)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return binding, nil, nil, fmt.Errorf("read repository files: %w", err)
	}
	switch direction {
	case "stack_to_repository":
		return binding, stackFiles, repositoryFiles, nil
	case "repository_to_stack":
		return binding, repositoryFiles, stackFiles, nil
	default:
		return binding, nil, nil, errors.New("direction must be stack_to_repository or repository_to_stack")
	}
}

func validateSensitiveOptIn(input TransferInput) error {
	if input.IncludeSensitive && input.SensitiveConfirmation != sensitiveConfirmText {
		return fmt.Errorf("sensitive file inclusion requires confirmation %q", sensitiveConfirmText)
	}
	return nil
}

func collectStackFiles(targetFS filesystem.FileSystem, root string, includeSensitive bool) (map[string]transferFile, error) {
	result := map[string]transferFile{}
	var total int64
	var walk func(string, string) error
	walk = func(dir, rel string) error {
		entries, err := targetFS.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			childRel := filepath.ToSlash(filepath.Join(rel, entry.Name()))
			if shouldSkipPath(childRel, entry.IsDir()) {
				continue
			}
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			child := targetFS.Join(dir, entry.Name())
			if entry.IsDir() {
				if err := walk(child, childRel); err != nil {
					return err
				}
				continue
			}
			info, err := targetFS.Stat(child)
			if err != nil {
				return err
			}
			if !isTransferFile(info.Mode()) {
				continue
			}
			if err := checkTransferLimit(len(result)+1, info.Size(), total+info.Size()); err != nil {
				return err
			}
			total += info.Size()
			sensitive := isSensitivePath(childRel)
			if sensitive && !includeSensitive {
				result[childRel] = transferFile{path: childRel, size: info.Size(), sensitive: true}
				continue
			}
			childPath := child
			file := transferFile{path: childRel, size: info.Size(), mode: info.Mode().Perm(), sensitive: sensitive, open: func() (io.ReadCloser, error) {
				return targetFS.OpenFile(childPath, os.O_RDONLY, 0)
			}}
			file.sha, err = hashTransferFile(file)
			if err != nil {
				return err
			}
			result[childRel] = file
		}
		return nil
	}
	return result, walk(root, "")
}

func collectRepositoryFiles(repositoryRoot, subPath string, includeSensitive bool) (map[string]transferFile, error) {
	safeRoot, err := os.OpenRoot(repositoryRoot)
	if err != nil {
		return nil, err
	}
	defer safeRoot.Close()
	baseRelative := "."
	if subPath != "." {
		baseRelative = filepath.FromSlash(subPath)
	}
	result := map[string]transferFile{}
	var total int64
	err = fs.WalkDir(safeRoot.FS(), filepath.ToSlash(baseRelative), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(filepath.ToSlash(baseRelative), path)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		if shouldSkipPath(rel, entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !isTransferFile(info.Mode()) {
			return nil
		}
		if err := checkTransferLimit(len(result)+1, info.Size(), total+info.Size()); err != nil {
			return err
		}
		total += info.Size()
		sensitive := isSensitivePath(rel)
		if sensitive && !includeSensitive {
			result[rel] = transferFile{path: rel, size: info.Size(), sensitive: true}
			return nil
		}
		repositoryRelative := filepath.Join(baseRelative, filepath.FromSlash(rel))
		file := transferFile{path: rel, size: info.Size(), mode: info.Mode().Perm(), sensitive: sensitive, open: repositoryFileOpener(repositoryRoot, repositoryRelative)}
		file.sha, err = hashTransferFile(file)
		if err != nil {
			return err
		}
		result[rel] = file
		return nil
	})
	return result, err
}

func repositoryFileOpener(repositoryRoot, relativePath string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) {
		root, err := os.OpenRoot(repositoryRoot)
		if err != nil {
			return nil, err
		}
		file, err := root.Open(relativePath)
		if err != nil {
			_ = root.Close()
			return nil, err
		}
		return &rootedReadCloser{file: file, root: root}, nil
	}
}

func hashTransferFile(file transferFile) (string, error) {
	if file.open == nil {
		return "", nil
	}
	reader, err := file.open()
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	buffer := transferBufferPool.Get().([]byte)
	n, copyErr := io.CopyBuffer(hash, io.LimitReader(reader, file.size+1), buffer)
	transferBufferPool.Put(buffer)
	closeErr := reader.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if n != file.size {
		return "", fmt.Errorf("file %s changed while it was being hashed; retry the preview", file.path)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func checkTransferLimit(files int, fileSize, totalSize int64) error {
	if files > maxBindingFiles {
		return fmt.Errorf("stack contains more than %d files", maxBindingFiles)
	}
	if fileSize > maxBindingFileSize {
		return fmt.Errorf("a stack file exceeds the %d MiB limit", maxBindingFileSize>>20)
	}
	if totalSize > maxBindingTotalSize {
		return fmt.Errorf("stack files exceed the %d MiB total limit", maxBindingTotalSize>>20)
	}
	return nil
}

func isTransferFile(mode fs.FileMode) bool {
	return mode.IsRegular()
}

func shouldSkipPath(path string, directory bool) bool {
	base := strings.ToLower(filepath.Base(path))
	return directory && (base == ".git" || base == ".dockman-backups")
}

func isSensitivePath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	ext := strings.ToLower(filepath.Ext(base))
	if base == ".env" || strings.HasPrefix(base, ".env.") || base == "id_rsa" || base == "id_ed25519" {
		return true
	}
	if ext == ".pem" || ext == ".key" || ext == ".p12" || ext == ".pfx" {
		return true
	}
	return strings.Contains(base, "secret") || strings.Contains(base, "credential")
}

func buildPreview(bindingID, direction string, source, target map[string]transferFile) TransferPreview {
	preview := TransferPreview{BindingID: bindingID, Direction: direction, Entries: []PreviewEntry{}, DeletionMode: "non_destructive"}
	paths := make([]string, 0, len(source))
	for path := range source {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		src := source[path]
		if src.sensitive && src.open == nil {
			preview.Entries = append(preview.Entries, PreviewEntry{Path: path, Status: "skipped_sensitive", Size: src.size, Sensitive: true})
			preview.Skipped++
			continue
		}
		dst, exists := target[path]
		entry := PreviewEntry{Path: path, Status: "add", SourceSHA: src.sha, Size: src.size, Sensitive: src.sensitive}
		if exists && dst.open != nil {
			entry.TargetSHA = dst.sha
			if entry.SourceSHA == entry.TargetSHA {
				entry.Status = "unchanged"
				preview.Unchanged++
				continue
			}
			entry.Status = "modify"
		}
		preview.Entries = append(preview.Entries, entry)
		preview.Changed++
	}
	preview.PreviewToken = previewToken(preview)
	return preview
}

func previewToken(preview TransferPreview) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\x00", preview.BindingID, preview.Direction, preview.DeletionMode)
	for _, entry := range preview.Entries {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%s\x00%d\x00%t\x00", entry.Path, entry.Status, entry.SourceSHA, entry.TargetSHA, entry.Size, entry.Sensitive)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func validatePreviewToken(expected, actual string) error {
	if expected == "" {
		return errors.New("transfer requires a fresh preview confirmation")
	}
	if expected != actual {
		return errors.New("stack or repository files changed after the preview; review the updated differences before retrying")
	}
	return nil
}

func writeRepositoryFiles(repositoryRoot, subPath string, files map[string]transferFile) error {
	root, err := os.OpenRoot(repositoryRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	base := ""
	if subPath != "." {
		base = filepath.FromSlash(subPath)
	}
	for _, file := range sortedTransferFiles(files) {
		if file.open == nil {
			continue
		}
		destination := filepath.Join(base, filepath.FromSlash(file.path))
		if err := root.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return err
		}
		temporary := destination + ".dockman-git-" + uuid.NewString() + ".tmp"
		handle, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, safeFileMode(file.mode))
		if err != nil {
			return err
		}
		writeErr := streamTransferFile(file, handle)
		closeErr := handle.Close()
		if writeErr != nil || closeErr != nil {
			_ = root.Remove(temporary)
		}
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		if err := root.Rename(temporary, destination); err != nil {
			_ = root.Remove(temporary)
			return fmt.Errorf("replace %s: %w", file.path, err)
		}
	}
	return nil
}

func writeStackFiles(targetFS filesystem.FileSystem, root string, files map[string]transferFile) error {
	for _, file := range sortedTransferFiles(files) {
		if file.open == nil {
			continue
		}
		destination := targetFS.Join(root, filepath.FromSlash(file.path))
		if err := targetFS.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return err
		}
		temporary := destination + ".dockman-git-" + uuid.NewString() + ".tmp"
		handle, err := targetFS.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, safeFileMode(file.mode))
		if err != nil {
			return err
		}
		writeErr := streamTransferFile(file, handle)
		closeErr := handle.Close()
		if writeErr != nil || closeErr != nil {
			_ = targetFS.RemoveAll(temporary)
			if writeErr != nil {
				return writeErr
			}
			return closeErr
		}
		if err := targetFS.Rename(temporary, destination); err != nil {
			_ = targetFS.RemoveAll(temporary)
			return fmt.Errorf("replace %s: %w", file.path, err)
		}
	}
	return nil
}

func streamTransferFile(file transferFile, destination io.Writer) error {
	reader, err := file.open()
	if err != nil {
		return err
	}
	buffer := transferBufferPool.Get().([]byte)
	hash := sha256.New()
	written, copyErr := io.CopyBuffer(io.MultiWriter(destination, hash), io.LimitReader(reader, file.size+1), buffer)
	transferBufferPool.Put(buffer)
	closeErr := reader.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != file.size {
		return fmt.Errorf("file %s changed during transfer; retry from a new preview", file.path)
	}
	if currentSHA := hex.EncodeToString(hash.Sum(nil)); currentSHA != file.sha {
		return fmt.Errorf("file %s changed after the preview; retry from a new preview", file.path)
	}
	return nil
}

func safeFileMode(mode fs.FileMode) fs.FileMode {
	mode &= 0777
	if mode == 0 {
		return 0644
	}
	return mode
}

func sortedTransferFiles(files map[string]transferFile) []transferFile {
	result := make([]transferFile, 0, len(files))
	for _, file := range files {
		result = append(result, file)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].path < result[j].path })
	return result
}

func changedTransferFiles(source, target map[string]transferFile) map[string]transferFile {
	changed := make(map[string]transferFile)
	for path, sourceFile := range source {
		if sourceFile.open == nil {
			continue
		}
		targetFile, exists := target[path]
		if !exists || targetFile.open == nil || sourceFile.sha != targetFile.sha {
			changed[path] = sourceFile
		}
	}
	return changed
}

func validateComposeFiles(files map[string]transferFile) error {
	found := false
	for path, file := range files {
		switch strings.ToLower(filepath.Base(path)) {
		case "compose.yml", "compose.yaml", "docker-compose.yml", "docker-compose.yaml":
			found = true
			if file.open == nil {
				return fmt.Errorf("compose file %s is sensitive and was skipped", path)
			}
			reader, err := file.open()
			if err != nil {
				return fmt.Errorf("open compose YAML %s: %w", path, err)
			}
			var value any
			decodeErr := yaml.NewDecoder(io.LimitReader(reader, file.size+1)).Decode(&value)
			closeErr := reader.Close()
			if decodeErr != nil {
				return fmt.Errorf("invalid compose YAML %s: %w", path, decodeErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close compose YAML %s: %w", path, closeErr)
			}
			if value == nil {
				return fmt.Errorf("invalid compose YAML %s: document is empty", path)
			}
		}
	}
	if !found {
		return errors.New("repository path contains no compose file")
	}
	return nil
}

func (s *Service) backupChangedFiles(binding StackBinding, _ filesystem.FileSystem, _ string, source, target map[string]transferFile) (string, error) {
	if s.backupRoot == "" {
		return "", errors.New("Git stack backup directory is not configured")
	}
	if err := os.MkdirAll(s.backupRoot, 0700); err != nil {
		return "", fmt.Errorf("create backup directory: %w", err)
	}
	rootInfo, err := os.Lstat(s.backupRoot)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("Git stack backup root must be a real directory")
	}
	backupFS, err := os.OpenRoot(s.backupRoot)
	if err != nil {
		return "", fmt.Errorf("open backup directory: %w", err)
	}
	defer backupFS.Close()
	if err := backupFS.MkdirAll(binding.UUID, 0700); err != nil {
		return "", fmt.Errorf("create binding backup directory: %w", err)
	}
	name := time.Now().UTC().Format("20060102T150405.000000000Z") + ".tar.gz"
	relativePath := filepath.Join(binding.UUID, name)
	handle, err := backupFS.OpenFile(relativePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	gzipWriter := gzip.NewWriter(handle)
	tarWriter := tar.NewWriter(gzipWriter)
	writeErr := func() error {
		created := make([]string, 0)
		for _, sourceFile := range sortedTransferFiles(source) {
			targetFile, exists := target[sourceFile.path]
			if !exists {
				if sourceFile.open != nil {
					created = append(created, sourceFile.path)
				}
				continue
			}
			if targetFile.open == nil || sourceFile.sha == targetFile.sha {
				continue
			}
			header := &tar.Header{Name: sourceFile.path, Mode: int64(safeFileMode(targetFile.mode)), Size: targetFile.size, ModTime: time.Now().UTC()}
			if err := tarWriter.WriteHeader(header); err != nil {
				return err
			}
			if err := streamTransferFile(targetFile, tarWriter); err != nil {
				return err
			}
		}
		manifest, err := json.Marshal(struct {
			Version      int      `json:"version"`
			BindingID    string   `json:"bindingId"`
			CreatedFiles []string `json:"createdFiles"`
		}{Version: 1, BindingID: binding.UUID, CreatedFiles: created})
		if err != nil {
			return err
		}
		header := &tar.Header{Name: ".dockman-backup-manifest.json", Mode: 0600, Size: int64(len(manifest)), ModTime: time.Now().UTC()}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if _, err := tarWriter.Write(manifest); err != nil {
			return err
		}
		return nil
	}()
	closeTarErr := tarWriter.Close()
	closeGzipErr := gzipWriter.Close()
	closeFileErr := handle.Close()
	for _, candidate := range []error{writeErr, closeTarErr, closeGzipErr, closeFileErr} {
		if candidate != nil {
			_ = backupFS.Remove(relativePath)
			return "", fmt.Errorf("write stack backup: %w", candidate)
		}
	}
	return filepath.ToSlash(relativePath), nil
}
