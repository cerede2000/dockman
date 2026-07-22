package gitsync

import (
	"archive/tar"
	"bytes"
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
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/RA341/dockman/internal/host/filesystem"
	"github.com/bmatcuk/doublestar/v4"
	gitclient "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/goccy/go-yaml"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

const (
	maxBindingFiles          = 20_000
	maxBindingFileSize       = 100 << 20
	maxBindingTotalSize      = 2 << 30
	transferBufferSize       = 64 << 10
	maxIgnoreFileSize        = 64 << 10
	maxIgnoreRules           = 1000
	maxComparisonFileSize    = 2 << 20
	sensitiveConfirmText     = "INCLUDE SENSITIVE FILES"
	syncProfileComposeConfig = "compose_config"
	syncProfileAllFiles      = "all_files"
)

var transferBufferPool = sync.Pool{New: func() any { return make([]byte, transferBufferSize) }}

type BindingInput struct {
	RepositoryID string `json:"repositoryId"`
	Host         string `json:"host"`
	StackPath    string `json:"stackPath"`
	SubPath      string `json:"subPath"`
}

type BindingView struct {
	ID                      string     `json:"id"`
	RepositoryID            string     `json:"repositoryId"`
	RepositoryName          string     `json:"repositoryName"`
	Host                    string     `json:"host"`
	StackPath               string     `json:"stackPath"`
	SubPath                 string     `json:"subPath"`
	ComposePaths            []string   `json:"composePaths"`
	SyncProfile             string     `json:"syncProfile"`
	IncludePatterns         []string   `json:"includePatterns"`
	ExcludePatterns         []string   `json:"excludePatterns"`
	Enabled                 bool       `json:"enabled"`
	AutoSyncEnabled         bool       `json:"autoSyncEnabled"`
	AutoSyncIntervalMinutes int        `json:"autoSyncIntervalMinutes"`
	AutoSyncState           string     `json:"autoSyncState"`
	AutoSyncError           string     `json:"autoSyncError,omitempty"`
	LastAutoSyncAt          *time.Time `json:"lastAutoSyncAt,omitempty"`
	LastAutoSyncSuccessAt   *time.Time `json:"lastAutoSyncSuccessAt,omitempty"`
	CreatedAt               time.Time  `json:"createdAt"`
	UpdatedAt               time.Time  `json:"updatedAt"`
}

type BindingPolicyInput struct {
	Profile         string   `json:"profile"`
	IncludePatterns []string `json:"includePatterns"`
	ExcludePatterns []string `json:"excludePatterns"`
}

type BindingExclusionInput struct {
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
}

type BindingExclusionsInput struct {
	Entries []BindingExclusionInput `json:"entries"`
}

type BindingInclusionsInput struct {
	Paths []string `json:"paths"`
}

type StackTarget struct {
	Host         string   `json:"host"`
	Path         string   `json:"path"`
	ComposePaths []string `json:"composePaths"`
	Scope        string   `json:"scope"`
	StackCount   int      `json:"stackCount"`
}

type TransferInput struct {
	IncludeSensitive      bool     `json:"includeSensitive"`
	SensitiveConfirmation string   `json:"sensitiveConfirmation"`
	CommitMessage         string   `json:"commitMessage"`
	PreviewToken          string   `json:"previewToken"`
	ResolvedPaths         []string `json:"resolvedPaths"`
	SelectedPaths         []string `json:"selectedPaths"`
}

type PreviewEntry struct {
	Path         string `json:"path"`
	Status       string `json:"status"`
	SourceSHA    string `json:"sourceSha,omitempty"`
	TargetSHA    string `json:"targetSha,omitempty"`
	Size         int64  `json:"size,omitempty"`
	Sensitive    bool   `json:"sensitive,omitempty"`
	Directory    bool   `json:"directory,omitempty"`
	ConflictKind string `json:"conflictKind,omitempty"`
}

type TransferPreview struct {
	BindingID    string         `json:"bindingId"`
	Direction    string         `json:"direction"`
	Entries      []PreviewEntry `json:"entries"`
	Changed      int            `json:"changed"`
	Unchanged    int            `json:"unchanged"`
	Skipped      int            `json:"skipped"`
	Conflicts    int            `json:"conflicts"`
	DeletionMode string         `json:"deletionMode"`
	PreviewToken string         `json:"previewToken"`
}

type TransferResult struct {
	Preview   TransferPreview `json:"preview"`
	CommitSHA string          `json:"commitSha,omitempty"`
	Backup    string          `json:"backup,omitempty"`
	Message   string          `json:"message"`
}

type ComparisonInput struct {
	TransferInput
	Path string `json:"path"`
}

type ComparisonSide struct {
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	Content string `json:"content,omitempty"`
}

type FileComparison struct {
	Path       string         `json:"path"`
	Dockman    ComparisonSide `json:"dockman"`
	Git        ComparisonSide `json:"git"`
	Comparable bool           `json:"comparable"`
	Reason     string         `json:"reason,omitempty"`
}

type transferFile struct {
	path       string
	mode       fs.FileMode
	size       int64
	sha        string
	sensitive  bool
	skipReason string
	directory  bool
	open       func() (io.ReadCloser, error)
}

type ignoreRule struct {
	pattern   string
	directory bool
	basename  bool
}

type syncPolicy struct {
	profile  string
	includes []ignoreRule
	excludes []ignoreRule
	compose  map[string]struct{}
}

var composeConfigRules = mustRules([]string{
	"*.yml", "*.yaml", "*.json", "*.toml", "*.conf", "*.config", "*.cfg", "*.ini",
	"*.properties", "*.xml", "*.tmpl", "*.tpl", "*.j2", "*.sh", "*.bash", "*.sql",
	"*.txt", "*.md", "*.crt", ".env", ".env.*", ".gitignore", ".dockerignore",
	".dockmanignore", "Dockerfile*", "Containerfile*", "Caddyfile", "Makefile",
})

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
	archived, archiveErr := s.store.ArchivedBinding(clean.Host, clean.StackPath)
	if archiveErr == nil {
		if archived.RepositoryUUID == clean.RepositoryID && archived.SubPath == clean.SubPath {
			archived.ComposePaths = strings.Join(compose, "\n")
			if err := s.store.RestoreBinding(&archived); err != nil {
				return BindingView{}, err
			}
			return s.bindingView(archived)
		}
		if err := s.store.DeleteBinding(archived.UUID, true); err != nil {
			return BindingView{}, err
		}
	} else if !errors.Is(archiveErr, gorm.ErrRecordNotFound) {
		return BindingView{}, archiveErr
	}
	row := StackBinding{
		UUID: uuid.NewString(), RepositoryUUID: clean.RepositoryID, Host: clean.Host,
		StackPath: clean.StackPath, SubPath: clean.SubPath,
		ComposePaths: strings.Join(compose, "\n"), SyncProfile: syncProfileComposeConfig, Enabled: true,
		AutoSyncIntervalMinutes: defaultAutoSyncIntervalMinutes, AutoSyncState: "disabled",
	}
	if err := s.store.SaveBinding(&row); err != nil {
		return BindingView{}, err
	}
	return s.bindingView(row)
}

func (s *Service) UpdateBindingPolicy(id string, input BindingPolicyInput) (BindingView, error) {
	row, err := s.store.GetBinding(id)
	if err != nil {
		return BindingView{}, err
	}
	policy, includes, excludes, err := normalizeBindingPolicy(input)
	if err != nil {
		return BindingView{}, err
	}
	row.SyncProfile = policy
	row.IncludePatterns = strings.Join(includes, "\n")
	row.ExcludePatterns = strings.Join(excludes, "\n")
	if err := s.store.SaveBinding(&row); err != nil {
		return BindingView{}, err
	}
	return s.bindingView(row)
}

func (s *Service) AddBindingExclusion(id string, input BindingExclusionInput) (BindingView, error) {
	return s.AddBindingExclusions(id, []BindingExclusionInput{input})
}

func (s *Service) AddBindingExclusions(id string, inputs []BindingExclusionInput) (BindingView, error) {
	if len(inputs) == 0 {
		return BindingView{}, errors.New("at least one exclusion is required")
	}
	if len(inputs) > 100 {
		return BindingView{}, errors.New("at most 100 exclusions can be added at once")
	}
	row, err := s.store.GetBinding(id)
	if err != nil {
		return BindingView{}, err
	}
	excludes := splitPatternLines(row.ExcludePatterns)
	existingPatterns := make(map[string]struct{}, len(excludes)+len(inputs))
	for _, existing := range excludes {
		existingPatterns[existing] = struct{}{}
	}
	composePaths := make(map[string]struct{}, len(splitPatternLines(row.ComposePaths)))
	for _, compose := range splitPatternLines(row.ComposePaths) {
		composePaths[filepath.ToSlash(filepath.Clean(filepath.FromSlash(compose)))] = struct{}{}
	}
	for _, input := range inputs {
		relative := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(input.Path))))
		if err := validateRelativePath(relative, false); err != nil {
			return BindingView{}, fmt.Errorf("invalid exclusion path %q: %w", input.Path, err)
		}
		if _, compose := composePaths[relative]; compose && !input.Directory {
			return BindingView{}, fmt.Errorf("Compose files cannot be excluded from synchronization: %q", relative)
		}
		pattern := escapeGlobLiteral(relative)
		if input.Directory {
			pattern += "/"
		}
		if _, exists := existingPatterns[pattern]; exists {
			continue
		}
		existingPatterns[pattern] = struct{}{}
		excludes = append(excludes, pattern)
	}
	profile, includes, excludes, err := normalizeBindingPolicy(BindingPolicyInput{
		Profile: row.SyncProfile, IncludePatterns: splitPatternLines(row.IncludePatterns), ExcludePatterns: excludes,
	})
	if err != nil {
		return BindingView{}, err
	}
	row.SyncProfile = profile
	row.IncludePatterns = strings.Join(includes, "\n")
	row.ExcludePatterns = strings.Join(excludes, "\n")
	if err := s.store.SaveBinding(&row); err != nil {
		return BindingView{}, err
	}
	return s.bindingView(row)
}

func (s *Service) AddBindingInclusions(id string, paths []string) (BindingView, error) {
	if len(paths) == 0 {
		return BindingView{}, errors.New("at least one inclusion is required")
	}
	if len(paths) > 100 {
		return BindingView{}, errors.New("at most 100 inclusions can be added at once")
	}
	row, err := s.store.GetBinding(id)
	if err != nil {
		return BindingView{}, err
	}
	includes := splitPatternLines(row.IncludePatterns)
	existingPatterns := make(map[string]struct{}, len(includes)+len(paths))
	for _, existing := range includes {
		existingPatterns[existing] = struct{}{}
	}
	for _, path := range paths {
		relative := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(path))))
		if err := validateRelativePath(relative, false); err != nil {
			return BindingView{}, fmt.Errorf("invalid inclusion path %q: %w", path, err)
		}
		pattern := escapeGlobLiteral(relative)
		if _, exists := existingPatterns[pattern]; exists {
			continue
		}
		existingPatterns[pattern] = struct{}{}
		includes = append(includes, pattern)
	}
	profile, includes, excludes, err := normalizeBindingPolicy(BindingPolicyInput{
		Profile: row.SyncProfile, IncludePatterns: includes, ExcludePatterns: splitPatternLines(row.ExcludePatterns),
	})
	if err != nil {
		return BindingView{}, err
	}
	row.SyncProfile = profile
	row.IncludePatterns = strings.Join(includes, "\n")
	row.ExcludePatterns = strings.Join(excludes, "\n")
	if err := s.store.SaveBinding(&row); err != nil {
		return BindingView{}, err
	}
	return s.bindingView(row)
}

func (s *Service) DeleteBinding(id string, forget bool) error {
	automationLock := s.repositoryLock("automation:" + id)
	automationLock.Lock()
	defer automationLock.Unlock()
	row, err := s.store.GetBinding(id)
	if err != nil {
		return err
	}
	row.AutoSyncEnabled = false
	row.AutoSyncState = "disabled"
	row.AutoSyncError = ""
	if err := s.store.SaveBinding(&row); err != nil {
		return err
	}
	return s.store.DeleteBinding(id, forget)
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
	baseline, err := s.store.BindingBaseline(binding.UUID)
	if err != nil {
		return TransferPreview{}, err
	}
	return buildPreview(binding.UUID, direction, source, target, baseline), nil
}

func (s *Service) CompareBindingFile(id, direction string, input ComparisonInput) (FileComparison, error) {
	input.Path = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(input.Path))))
	if err := validateRelativePath(input.Path, false); err != nil {
		return FileComparison{}, fmt.Errorf("invalid comparison path: %w", err)
	}
	binding, source, target, err := s.loadTransferTrees(id, direction, input.TransferInput)
	if err != nil {
		return FileComparison{}, err
	}
	baseline, err := s.store.BindingBaseline(binding.UUID)
	if err != nil {
		return FileComparison{}, err
	}
	preview := buildPreview(binding.UUID, direction, source, target, baseline)
	conflict := false
	for _, entry := range preview.Entries {
		if entry.Path == input.Path && entry.Status == "conflict" {
			conflict = true
			break
		}
	}
	if !conflict {
		return FileComparison{}, errors.New("the requested file is not a current conflict")
	}
	sourceFile, sourceExists := source[input.Path]
	targetFile, targetExists := target[input.Path]
	if !sourceExists || !targetExists || sourceFile.open == nil || targetFile.open == nil {
		return FileComparison{}, errors.New("both file versions must be available for comparison")
	}
	sourceSide, sourceComparable, sourceReason, err := comparisonSide(sourceFile)
	if err != nil {
		return FileComparison{}, err
	}
	targetSide, targetComparable, targetReason, err := comparisonSide(targetFile)
	if err != nil {
		return FileComparison{}, err
	}
	result := FileComparison{Path: input.Path, Comparable: sourceComparable && targetComparable}
	if direction == "stack_to_repository" {
		result.Dockman, result.Git = sourceSide, targetSide
	} else {
		result.Dockman, result.Git = targetSide, sourceSide
	}
	if !result.Comparable {
		result.Reason = strings.TrimSpace(strings.Join([]string{sourceReason, targetReason}, " "))
	}
	return result, nil
}

func comparisonSide(file transferFile) (ComparisonSide, bool, string, error) {
	side := ComparisonSide{SHA256: file.sha, Size: file.size}
	if file.size > maxComparisonFileSize {
		return side, false, fmt.Sprintf("File exceeds the %d MiB comparison limit.", maxComparisonFileSize>>20), nil
	}
	reader, err := file.open()
	if err != nil {
		return side, false, "", err
	}
	data, readErr := io.ReadAll(io.LimitReader(reader, maxComparisonFileSize+1))
	closeErr := reader.Close()
	if readErr != nil {
		return side, false, "", readErr
	}
	if closeErr != nil {
		return side, false, "", closeErr
	}
	if len(data) > maxComparisonFileSize {
		return side, false, fmt.Sprintf("File exceeds the %d MiB comparison limit.", maxComparisonFileSize>>20), nil
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return side, false, "Binary files cannot be displayed as text.", nil
	}
	side.Content = string(data)
	return side, true, "", nil
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
		baseline, err := s.store.BindingBaseline(binding.UUID)
		if err != nil {
			return err
		}
		result.Preview = buildPreview(binding.UUID, "stack_to_repository", source, target, baseline)
		if err := validatePreviewToken(input.PreviewToken, result.Preview.PreviewToken); err != nil {
			return err
		}
		if result.Preview.Changed == 0 {
			if err := s.store.ReplaceBindingBaseline(binding.UUID, baselineFromSource(source)); err != nil {
				return err
			}
			result.Message = "Repository already matches the stack"
			return nil
		}
		selected, pendingConflicts, err := selectedTransferFiles(result.Preview, source, input.ResolvedPaths, input.SelectedPaths)
		if err != nil {
			return err
		}
		if len(selected) == 0 {
			return errors.New("no transferable file was selected; resolve at least one conflict or leave this transfer pending")
		}
		repoPath, err := s.repositoryPath(binding.RepositoryUUID)
		if err != nil {
			return err
		}
		if err := writeRepositoryFiles(repoPath, binding.SubPath, selected); err != nil {
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
		compactRepositoryObjects(repo, binding.RepositoryUUID)
		if err := s.store.ReplaceBindingBaseline(binding.UUID, baselineAfterTransfer(baseline, source, target, selected)); err != nil {
			return err
		}
		result.CommitSHA, result.Message = hash.String(), "Stack exported, committed, and pushed"
		if pendingConflicts > 0 {
			result.Message += fmt.Sprintf("; %d conflict(s) remain pending", pendingConflicts)
		}
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
		baseline, err := s.store.BindingBaseline(binding.UUID)
		if err != nil {
			return err
		}
		result.Preview = buildPreview(binding.UUID, "repository_to_stack", source, target, baseline)
		if err := validatePreviewToken(input.PreviewToken, result.Preview.PreviewToken); err != nil {
			return err
		}
		if result.Preview.Changed == 0 {
			if err := s.store.ReplaceBindingBaseline(binding.UUID, baselineFromSource(source)); err != nil {
				return err
			}
			result.Message = "Stack already matches the repository"
			return nil
		}
		selected, pendingConflicts, err := selectedTransferFiles(result.Preview, source, input.ResolvedPaths, input.SelectedPaths)
		if err != nil {
			return err
		}
		if len(selected) == 0 {
			return errors.New("no transferable file was selected; resolve at least one conflict or leave this transfer pending")
		}
		targetFS, targetRoot, err := s.resolveBindingStack(binding)
		if err != nil {
			return err
		}
		backup, err := s.backupChangedFiles(binding, targetFS, targetRoot, selected, target)
		if err != nil {
			return err
		}
		if err := writeStackFiles(targetFS, targetRoot, selected); err != nil {
			return err
		}
		if err := s.store.ReplaceBindingBaseline(binding.UUID, baselineAfterTransfer(baseline, source, target, selected)); err != nil {
			return err
		}
		result.Backup, result.Message = backup, "Repository files imported with a backup; the stack was not deployed"
		if pendingConflicts > 0 {
			result.Message += fmt.Sprintf("; %d conflict(s) remain pending", pendingConflicts)
		}
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
	profile := row.SyncProfile
	if profile == "" {
		profile = syncProfileComposeConfig
	}
	return BindingView{
		ID: row.UUID, RepositoryID: row.RepositoryUUID, RepositoryName: repository.Name,
		Host: row.Host, StackPath: row.StackPath, SubPath: row.SubPath, ComposePaths: compose,
		SyncProfile: profile, IncludePatterns: splitPatternLines(row.IncludePatterns),
		ExcludePatterns: splitPatternLines(row.ExcludePatterns), Enabled: row.Enabled,
		AutoSyncEnabled: row.AutoSyncEnabled, AutoSyncIntervalMinutes: row.AutoSyncIntervalMinutes,
		AutoSyncState: row.AutoSyncState, AutoSyncError: row.AutoSyncError,
		LastAutoSyncAt: row.LastAutoSyncAt, LastAutoSyncSuccessAt: row.LastAutoSyncSuccessAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
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
	policy, err := policyFromBinding(binding)
	if err != nil {
		return binding, nil, nil, err
	}
	targetFS, targetRoot, err := s.resolveBindingStack(binding)
	if err != nil {
		return binding, nil, nil, err
	}
	stackFiles, err := collectStackFiles(targetFS, targetRoot, input.IncludeSensitive, policy)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return binding, nil, nil, fmt.Errorf("read stack files: %w", err)
	}
	repositoryRoot, err := s.repositoryPath(binding.RepositoryUUID)
	if err != nil {
		return binding, nil, nil, err
	}
	repositoryFiles, err := collectRepositoryFiles(repositoryRoot, binding.SubPath, input.IncludeSensitive, policy)
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

func collectStackFiles(targetFS filesystem.FileSystem, root string, includeSensitive bool, policies ...syncPolicy) (map[string]transferFile, error) {
	result := map[string]transferFile{}
	policy := defaultSyncPolicy()
	if len(policies) > 0 {
		policy = policies[0]
	}
	ignoreRules, err := loadStackIgnoreRules(targetFS, root)
	if err != nil {
		return nil, err
	}
	excludeRules := append(append([]ignoreRule(nil), policy.excludes...), ignoreRules...)
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
			if matchesIgnoreRule(excludeRules, childRel, entry.IsDir()) && !policy.protectsCompose(childRel) {
				if entry.IsDir() && policy.containsCompose(childRel) {
					child := targetFS.Join(dir, entry.Name())
					if err := walk(child, childRel); err != nil {
						return err
					}
					continue
				}
				result[childRel] = transferFile{path: childRel, skipReason: "excluded", directory: entry.IsDir()}
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
			if len(result)+1 > maxBindingFiles {
				return fmt.Errorf("stack contains more than %d files; exclude generated folders with .dockmanignore", maxBindingFiles)
			}
			sensitive := isSensitivePath(childRel)
			if sensitive && !includeSensitive {
				result[childRel] = transferFile{path: childRel, size: info.Size(), sensitive: true, skipReason: "sensitive"}
				continue
			}
			if info.Size() > maxBindingFileSize {
				log.Warn().Str("file", childRel).Int64("size_bytes", info.Size()).Int64("limit_bytes", maxBindingFileSize).Msg("Git stack sync skipped oversized file")
				result[childRel] = transferFile{path: childRel, size: info.Size(), skipReason: "oversized"}
				continue
			}
			if !policy.includesFile(childRel) {
				result[childRel] = transferFile{path: childRel, size: info.Size(), skipReason: "type"}
				continue
			}
			if total+info.Size() > maxBindingTotalSize {
				return fmt.Errorf("stack files exceed the %d MiB total limit at %s (%d MiB accumulated); exclude this file or a generated folder with .dockmanignore", maxBindingTotalSize>>20, childRel, (total+info.Size())>>20)
			}
			if err := checkTransferLimit(len(result)+1, info.Size(), total+info.Size()); err != nil {
				return err
			}
			total += info.Size()
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

func collectRepositoryFiles(repositoryRoot, subPath string, includeSensitive bool, policies ...syncPolicy) (map[string]transferFile, error) {
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
	policy := defaultSyncPolicy()
	if len(policies) > 0 {
		policy = policies[0]
	}
	ignoreRules, err := loadRepositoryIgnoreRules(safeRoot, baseRelative)
	if err != nil {
		return nil, err
	}
	excludeRules := append(append([]ignoreRule(nil), policy.excludes...), ignoreRules...)
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
		if matchesIgnoreRule(excludeRules, rel, entry.IsDir()) && !policy.protectsCompose(rel) {
			if entry.IsDir() && policy.containsCompose(rel) {
				return nil
			}
			result[rel] = transferFile{path: rel, skipReason: "excluded", directory: entry.IsDir()}
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
		if len(result)+1 > maxBindingFiles {
			return fmt.Errorf("repository folder contains more than %d files; exclude generated folders with .dockmanignore", maxBindingFiles)
		}
		sensitive := isSensitivePath(rel)
		if sensitive && !includeSensitive {
			result[rel] = transferFile{path: rel, size: info.Size(), sensitive: true, skipReason: "sensitive"}
			return nil
		}
		if info.Size() > maxBindingFileSize {
			log.Warn().Str("file", rel).Int64("size_bytes", info.Size()).Int64("limit_bytes", maxBindingFileSize).Msg("Git stack sync skipped oversized file")
			result[rel] = transferFile{path: rel, size: info.Size(), skipReason: "oversized"}
			return nil
		}
		if !policy.includesFile(rel) {
			result[rel] = transferFile{path: rel, size: info.Size(), skipReason: "type"}
			return nil
		}
		if total+info.Size() > maxBindingTotalSize {
			return fmt.Errorf("repository files exceed the %d MiB total limit at %s (%d MiB accumulated); exclude this file or a generated folder with .dockmanignore", maxBindingTotalSize>>20, rel, (total+info.Size())>>20)
		}
		if err := checkTransferLimit(len(result)+1, info.Size(), total+info.Size()); err != nil {
			return err
		}
		total += info.Size()
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

func defaultSyncPolicy() syncPolicy {
	return syncPolicy{profile: syncProfileComposeConfig}
}

func policyFromBinding(binding StackBinding) (syncPolicy, error) {
	profile := binding.SyncProfile
	if profile == "" {
		profile = syncProfileComposeConfig
	}
	_, includes, excludes, err := normalizeBindingPolicy(BindingPolicyInput{
		Profile: profile, IncludePatterns: splitPatternLines(binding.IncludePatterns), ExcludePatterns: splitPatternLines(binding.ExcludePatterns),
	})
	if err != nil {
		return syncPolicy{}, fmt.Errorf("invalid stack link policy: %w", err)
	}
	includeRules, err := rulesFromPatterns(includes)
	if err != nil {
		return syncPolicy{}, err
	}
	excludeRules, err := rulesFromPatterns(excludes)
	if err != nil {
		return syncPolicy{}, err
	}
	compose := make(map[string]struct{})
	for _, relative := range splitPatternLines(binding.ComposePaths) {
		compose[filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative)))] = struct{}{}
	}
	return syncPolicy{profile: profile, includes: includeRules, excludes: excludeRules, compose: compose}, nil
}

func normalizeBindingPolicy(input BindingPolicyInput) (string, []string, []string, error) {
	profile := strings.TrimSpace(input.Profile)
	if profile == "" {
		profile = syncProfileComposeConfig
	}
	if profile != syncProfileComposeConfig && profile != syncProfileAllFiles {
		return "", nil, nil, errors.New("sync profile must be compose_config or all_files")
	}
	includes, _, err := normalizePatterns(input.IncludePatterns)
	if err != nil {
		return "", nil, nil, fmt.Errorf("invalid include patterns: %w", err)
	}
	excludes, _, err := normalizePatterns(input.ExcludePatterns)
	if err != nil {
		return "", nil, nil, fmt.Errorf("invalid exclude patterns: %w", err)
	}
	return profile, includes, excludes, nil
}

func splitPatternLines(value string) []string {
	if strings.TrimSpace(value) == "" {
		return []string{}
	}
	return strings.Split(value, "\n")
}

func normalizePatterns(values []string) ([]string, []ignoreRule, error) {
	normalized := make([]string, 0, len(values))
	rules := make([]ignoreRule, 0, len(values))
	total := 0
	for index, raw := range values {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "!") {
			return nil, nil, fmt.Errorf("line %d: negation rules are not supported", index+1)
		}
		directory := strings.HasSuffix(line, "/")
		line = strings.Trim(line, "/")
		if err := validateRelativePath(line, false); err != nil {
			return nil, nil, fmt.Errorf("line %d: %w", index+1, err)
		}
		if !doublestar.ValidatePattern(line) {
			return nil, nil, fmt.Errorf("line %d: invalid glob pattern", index+1)
		}
		total += len(line)
		if total > maxIgnoreFileSize {
			return nil, nil, fmt.Errorf("patterns exceed %d KiB", maxIgnoreFileSize>>10)
		}
		normalizedLine := line
		if directory {
			normalizedLine += "/"
		}
		normalized = append(normalized, normalizedLine)
		rules = append(rules, ignoreRule{pattern: line, directory: directory, basename: !strings.Contains(line, "/")})
		if len(rules) > maxIgnoreRules {
			return nil, nil, fmt.Errorf("more than %d rules", maxIgnoreRules)
		}
	}
	return normalized, rules, nil
}

func rulesFromPatterns(patterns []string) ([]ignoreRule, error) {
	_, rules, err := normalizePatterns(patterns)
	return rules, err
}

func escapeGlobLiteral(relative string) string {
	var escaped strings.Builder
	escaped.Grow(len(relative))
	for _, character := range relative {
		if strings.ContainsRune(`\\*?[]{}!`, character) {
			escaped.WriteByte('\\')
		}
		escaped.WriteRune(character)
	}
	return escaped.String()
}

func mustRules(patterns []string) []ignoreRule {
	rules, err := rulesFromPatterns(patterns)
	if err != nil {
		panic(err)
	}
	return rules
}

func (policy syncPolicy) includesFile(relative string) bool {
	if matchesIgnoreRule(policy.includes, relative, false) {
		return true
	}
	if policy.profile == syncProfileAllFiles {
		return true
	}
	return matchesIgnoreRule(composeConfigRules, relative, false)
}

func (policy syncPolicy) protectsCompose(relative string) bool {
	if !isComposePath(relative) {
		return false
	}
	if len(policy.compose) == 0 {
		return true
	}
	_, protected := policy.compose[filepath.ToSlash(relative)]
	return protected
}

func (policy syncPolicy) containsCompose(directory string) bool {
	directory = strings.Trim(filepath.ToSlash(directory), "/")
	for compose := range policy.compose {
		if strings.HasPrefix(compose, directory+"/") {
			return true
		}
	}
	return false
}

func loadStackIgnoreRules(targetFS filesystem.FileSystem, root string) ([]ignoreRule, error) {
	ignorePath := targetFS.Join(root, ".dockmanignore")
	info, err := targetFS.Stat(ignorePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("inspect .dockmanignore: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New(".dockmanignore must be a regular file")
	}
	reader, err := targetFS.OpenFile(ignorePath, os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("open .dockmanignore: %w", err)
	}
	return parseIgnoreRules(reader)
}

func loadRepositoryIgnoreRules(root *os.Root, base string) ([]ignoreRule, error) {
	reader, err := root.Open(filepath.Join(base, ".dockmanignore"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open .dockmanignore: %w", err)
	}
	return parseIgnoreRules(reader)
}

func parseIgnoreRules(reader io.ReadCloser) ([]ignoreRule, error) {
	defer reader.Close()
	contents, err := io.ReadAll(io.LimitReader(reader, maxIgnoreFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("read .dockmanignore: %w", err)
	}
	if len(contents) > maxIgnoreFileSize {
		return nil, fmt.Errorf(".dockmanignore exceeds %d KiB", maxIgnoreFileSize>>10)
	}
	_, rules, err := normalizePatterns(strings.Split(string(contents), "\n"))
	if err != nil {
		return nil, fmt.Errorf("invalid .dockmanignore: %w", err)
	}
	return rules, nil
}

func matchesIgnoreRule(rules []ignoreRule, relative string, directory bool) bool {
	relative = filepath.ToSlash(strings.TrimPrefix(relative, "./"))
	for _, rule := range rules {
		if rule.directory && (relative == rule.pattern || strings.HasPrefix(relative, rule.pattern+"/")) {
			return true
		}
		if directory && strings.HasSuffix(rule.pattern, "/**") && relative == strings.TrimSuffix(rule.pattern, "/**") {
			return true
		}
		candidate := relative
		if rule.basename {
			candidate = path.Base(relative)
		}
		matched, err := doublestar.Match(rule.pattern, candidate)
		if err == nil && matched && (!rule.directory || directory) {
			return true
		}
	}
	return false
}

func isTransferFile(mode fs.FileMode) bool {
	return mode.IsRegular()
}

func isComposePath(relative string) bool {
	switch strings.ToLower(filepath.Base(relative)) {
	case "compose.yml", "compose.yaml", "docker-compose.yml", "docker-compose.yaml":
		return true
	default:
		return false
	}
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

func buildPreview(bindingID, direction string, source, target map[string]transferFile, baselines ...map[string]string) TransferPreview {
	preview := TransferPreview{BindingID: bindingID, Direction: direction, Entries: []PreviewEntry{}, DeletionMode: "non_destructive"}
	baseline := map[string]string{}
	if len(baselines) > 0 && baselines[0] != nil {
		baseline = baselines[0]
	}
	paths := make([]string, 0, len(source))
	for path := range source {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		src := source[path]
		if src.open == nil {
			reason := src.skipReason
			if reason == "" {
				reason = "unavailable"
			}
			preview.Entries = append(preview.Entries, PreviewEntry{Path: path, Status: "skipped_" + reason, Size: src.size, Sensitive: src.sensitive, Directory: src.directory})
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
			baseSHA, tracked := baseline[path]
			if !tracked {
				entry.Status = "conflict"
				entry.ConflictKind = "no_baseline"
				preview.Conflicts++
			} else if dst.sha != baseSHA && src.sha != dst.sha {
				entry.Status = "conflict"
				entry.ConflictKind = "destination_changed"
				preview.Conflicts++
			}
		}
		preview.Entries = append(preview.Entries, entry)
		preview.Changed++
	}
	preview.PreviewToken = previewToken(preview)
	return preview
}

func baselineFromSource(source map[string]transferFile) map[string]string {
	result := make(map[string]string, len(source))
	for path, file := range source {
		if file.open != nil && file.sha != "" {
			result[path] = file.sha
		}
	}
	return result
}

func previewToken(preview TransferPreview) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\x00", preview.BindingID, preview.Direction, preview.DeletionMode)
	for _, entry := range preview.Entries {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%t\x00", entry.Path, entry.Status, entry.ConflictKind, entry.SourceSHA, entry.TargetSHA, entry.Size, entry.Sensitive)
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

func selectedTransferFiles(preview TransferPreview, source map[string]transferFile, resolvedPaths, selectedPaths []string) (map[string]transferFile, int, error) {
	if len(resolvedPaths) > maxBindingFiles || len(selectedPaths) > maxBindingFiles {
		return nil, 0, errors.New("too many conflict resolutions")
	}
	conflicts := make(map[string]struct{}, preview.Conflicts)
	transferable := make(map[string]struct{}, preview.Changed)
	for _, entry := range preview.Entries {
		if entry.Status == "add" || entry.Status == "modify" || entry.Status == "conflict" {
			transferable[entry.Path] = struct{}{}
		}
		if entry.Status == "conflict" {
			conflicts[entry.Path] = struct{}{}
		}
	}
	resolved := make(map[string]struct{}, len(resolvedPaths))
	for _, candidate := range resolvedPaths {
		candidate = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(candidate))))
		if err := validateRelativePath(candidate, false); err != nil {
			return nil, 0, fmt.Errorf("invalid resolved conflict path: %w", err)
		}
		if _, conflict := conflicts[candidate]; !conflict {
			return nil, 0, fmt.Errorf("%s is not a current conflict; refresh the preview", candidate)
		}
		resolved[candidate] = struct{}{}
	}
	limited := len(selectedPaths) > 0
	selectedSet := make(map[string]struct{}, len(selectedPaths))
	for _, candidate := range selectedPaths {
		candidate = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(candidate))))
		if err := validateRelativePath(candidate, false); err != nil {
			return nil, 0, fmt.Errorf("invalid selected transfer path: %w", err)
		}
		if _, exists := transferable[candidate]; !exists {
			return nil, 0, fmt.Errorf("%s is not a current transferable file; refresh the preview", candidate)
		}
		selectedSet[candidate] = struct{}{}
	}
	if limited {
		for candidate := range resolved {
			if _, selected := selectedSet[candidate]; !selected {
				return nil, 0, fmt.Errorf("resolved conflict %s must also be selected for transfer", candidate)
			}
		}
	}
	selected := make(map[string]transferFile)
	for _, entry := range preview.Entries {
		if limited {
			if _, approved := selectedSet[entry.Path]; !approved {
				continue
			}
		}
		if entry.Status != "add" && entry.Status != "modify" {
			if entry.Status != "conflict" {
				continue
			}
			if _, approved := resolved[entry.Path]; !approved {
				continue
			}
		}
		if file, exists := source[entry.Path]; exists && file.open != nil {
			selected[entry.Path] = file
		}
	}
	return selected, len(conflicts) - len(resolved), nil
}

func baselineAfterTransfer(current map[string]string, source, target, selected map[string]transferFile) map[string]string {
	result := make(map[string]string, len(current)+len(selected))
	for path, sha := range current {
		result[path] = sha
	}
	for path, sourceFile := range source {
		if sourceFile.open == nil || sourceFile.sha == "" {
			continue
		}
		if _, applied := selected[path]; applied {
			result[path] = sourceFile.sha
			continue
		}
		if targetFile, exists := target[path]; exists && targetFile.open != nil && targetFile.sha == sourceFile.sha {
			result[path] = sourceFile.sha
		}
	}
	return result
}

func validateComposeFiles(files map[string]transferFile) error {
	found := false
	for path, file := range files {
		if isComposePath(path) {
			found = true
			if file.open == nil {
				return fmt.Errorf("compose file %s was skipped (%s); Compose files cannot be excluded from synchronization", path, file.skipReason)
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
