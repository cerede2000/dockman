package gitsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/goccy/go-yaml"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

const (
	maxBindingFiles          = 20_000
	maxAutoDirectoryFiles    = 2_000
	maxBindingFileSize       = 100 << 20
	maxBindingTotalSize      = 2 << 30
	transferBufferSize       = 64 << 10
	maxIgnoreFileSize        = 64 << 10
	maxIgnoreRules           = 1000
	maxComparisonFileSize    = 2 << 20
	gitBackupRetention       = 10
	sensitiveConfirmText     = "INCLUDE SENSITIVE FILES"
	syncProfileComposeOnly   = "compose_only"
	syncProfileComposeConfig = "compose_config"
	syncProfileAllFiles      = "all_files"
	composeSelectionAll      = "all"
	composeSelectionSelected = "selected"
)

var transferBufferPool = sync.Pool{New: func() any { return make([]byte, transferBufferSize) }}

type BindingInput struct {
	RepositoryID         string   `json:"repositoryId"`
	Host                 string   `json:"host"`
	StackPath            string   `json:"stackPath"`
	SubPath              string   `json:"subPath"`
	SyncProfile          string   `json:"syncProfile"`
	AutoReconcile        *bool    `json:"autoReconcile"`
	InitialSync          string   `json:"initialSync"`
	ComposeSelectionMode string   `json:"composeSelectionMode"`
	SelectedComposePaths []string `json:"selectedComposePaths"`
}

type BindingView struct {
	ID                        string     `json:"id"`
	RepositoryID              string     `json:"repositoryId"`
	RepositoryName            string     `json:"repositoryName"`
	Host                      string     `json:"host"`
	StackPath                 string     `json:"stackPath"`
	SubPath                   string     `json:"subPath"`
	ComposePaths              []string   `json:"composePaths"`
	ComposeSelectionMode      string     `json:"composeSelectionMode"`
	SelectedComposePaths      []string   `json:"selectedComposePaths"`
	SyncProfile               string     `json:"syncProfile"`
	IncludePatterns           []string   `json:"includePatterns"`
	ExcludePatterns           []string   `json:"excludePatterns"`
	Enabled                   bool       `json:"enabled"`
	AutoSyncEnabled           bool       `json:"autoSyncEnabled"`
	AutoSyncPaused            bool       `json:"autoSyncPaused"`
	AutoSyncSelectionMode     string     `json:"autoSyncSelectionMode"`
	AutoSyncComposePaths      []string   `json:"autoSyncComposePaths"`
	AutoSyncIntervalMinutes   int        `json:"autoSyncIntervalMinutes"`
	AutoSyncState             string     `json:"autoSyncState"`
	AutoSyncError             string     `json:"autoSyncError,omitempty"`
	LastAutoSyncAt            *time.Time `json:"lastAutoSyncAt,omitempty"`
	LastAutoSyncSuccessAt     *time.Time `json:"lastAutoSyncSuccessAt,omitempty"`
	AutoDeployEnabled         bool       `json:"autoDeployEnabled"`
	AutoDeployNewStacks       bool       `json:"autoDeployNewStacks"`
	AutoDeployRollbackEnabled bool       `json:"autoDeployRollbackEnabled"`
	AutoDeployComposePaths    []string   `json:"autoDeployComposePaths"`
	AutoDeployState           string     `json:"autoDeployState"`
	AutoDeployError           string     `json:"autoDeployError,omitempty"`
	LastAutoDeployAt          *time.Time `json:"lastAutoDeployAt,omitempty"`
	AutoReconcileEnabled      bool       `json:"autoReconcileEnabled"`
	InitialSyncState          string     `json:"initialSyncState"`
	InitialSyncError          string     `json:"initialSyncError,omitempty"`
	InitialSyncAt             *time.Time `json:"initialSyncAt,omitempty"`
	CreatedAt                 time.Time  `json:"createdAt"`
	UpdatedAt                 time.Time  `json:"updatedAt"`
}

type BindingPolicyInput struct {
	Profile         string   `json:"profile"`
	IncludePatterns []string `json:"includePatterns"`
	ExcludePatterns []string `json:"excludePatterns"`
}

type BindingComposeSelectionInput struct {
	Mode         string   `json:"mode"`
	ComposePaths []string `json:"composePaths"`
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

type RepositoryStackCatalog struct {
	RepositoryID string   `json:"repositoryId"`
	Branch       string   `json:"branch"`
	ComposePaths []string `json:"composePaths"`
}

type TransferInput struct {
	IncludeSensitive      bool     `json:"includeSensitive"`
	SensitiveConfirmation string   `json:"sensitiveConfirmation"`
	CommitMessage         string   `json:"commitMessage"`
	PreviewToken          string   `json:"previewToken"`
	ResolvedPaths         []string `json:"resolvedPaths"`
	SelectedPaths         []string `json:"selectedPaths"`
	compactResult         bool
	automation            bool
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
	BindingID            string            `json:"bindingId"`
	Direction            string            `json:"direction"`
	Entries              []PreviewEntry    `json:"entries"`
	Changed              int               `json:"changed"`
	Unchanged            int               `json:"unchanged"`
	Skipped              int               `json:"skipped"`
	Conflicts            int               `json:"conflicts"`
	Preserved            int               `json:"preserved"`
	LocalDeletions       int               `json:"localDeletions"`
	OrphanedComposePaths []string          `json:"orphanedComposePaths,omitempty"`
	ComposeErrors        map[string]string `json:"composeErrors,omitempty"`
	DeletionMode         string            `json:"deletionMode"`
	automation           bool
	PreviewToken         string `json:"previewToken"`
}

type TransferResult struct {
	Preview        TransferPreview `json:"preview"`
	CommitSHA      string          `json:"commitSha,omitempty"`
	Backup         string          `json:"backup,omitempty"`
	Message        string          `json:"message"`
	EditorBlocked  []string        `json:"editorBlocked,omitempty"`
	ComposeBlocked []string        `json:"composeBlocked,omitempty"`
	ReadBlocked    []string        `json:"readBlocked,omitempty"`
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
	profile            string
	includes           []ignoreRule
	excludes           []ignoreRule
	repositoryExcludes []ignoreRule
	repositorySubPath  string
	compose            map[string]struct{}
	composeDirectories map[string]struct{}
	selectedRoots      map[string]struct{}
	selectionEnabled   bool
	selectNewCompose   bool
}

var composeConfigRules = mustRules([]string{
	"*.yml", "*.yaml", "*.json", "*.toml", "*.conf", "*.config", "*.cfg", "*.ini",
	"*.properties", "*.xml", "*.tmpl", "*.tpl", "*.j2", "*.sh", "*.bash", "*.sql",
	"*.txt", "*.md", "*.crt", ".env", ".env.*", ".gitignore", ".dockerignore",
	".dockmanignore", "Dockerfile*", "Containerfile*", "Caddyfile", "Makefile",
})

var composeOnlyRules = mustRules([]string{
	".env.example", ".env.*.example", ".env.sample", ".env.*.sample",
	".env.template", ".env.*.template", ".env.dist", ".env.*.dist",
})

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
	return s.CreateBindingContext(context.Background(), input)
}

func (s *Service) CreateBindingContext(ctx context.Context, input BindingInput) (BindingView, error) {
	if !s.enabled {
		return BindingView{}, errors.New("Git synchronization is disabled")
	}
	ownershipLock := s.repositoryLock("binding-ownership:" + strings.TrimSpace(input.Host))
	ownershipLock.Lock()
	defer ownershipLock.Unlock()
	clean, targetFS, targetRoot, err := s.validateBindingInput(input)
	if err != nil {
		return BindingView{}, err
	}
	localCompose := discoverComposeFiles(targetFS, targetRoot)
	repository, err := s.store.GetRepository(clean.RepositoryID)
	if err != nil {
		return BindingView{}, err
	}
	// A Git-first link must validate the requested Compose selection against
	// the current remote branch, not against a possibly stale local catalog.
	// The initialization phase pulls again under the transfer lock before any
	// write, preserving the normal preview-token race protection.
	if strings.TrimSpace(clean.InitialSync) == "repository_to_stack" {
		if _, err := s.PullRepository(ctx, clean.RepositoryID); err != nil {
			return BindingView{}, fmt.Errorf("refresh repository before Git stack import: %w", err)
		}
		repository, err = s.store.GetRepository(clean.RepositoryID)
		if err != nil {
			return BindingView{}, err
		}
	}
	remoteCompose, err := s.repositoryComposeCatalog(repository, clean.SubPath)
	if err != nil {
		return BindingView{}, fmt.Errorf("discover repository Compose catalog: %w", err)
	}
	// Catalog both sides at link creation. This keeps an initially empty local
	// folder safe in Compose-only mode while still allowing an existing Git
	// stack to be imported without a separate manual refresh.
	compose := uniqueSortedStrings(append(localCompose, remoteCompose...))
	selectionMode, selectedCompose, err := normalizeComposeSelection(compose, clean.ComposeSelectionMode, clean.SelectedComposePaths, composeSelectionAll)
	if err != nil {
		return BindingView{}, err
	}
	syncProfile := strings.TrimSpace(clean.SyncProfile)
	if syncProfile == "" {
		syncProfile = syncProfileComposeConfig
	}
	if syncProfile != syncProfileComposeOnly && syncProfile != syncProfileComposeConfig && syncProfile != syncProfileAllFiles {
		return BindingView{}, errors.New("sync profile must be compose_only, compose_config or all_files")
	}
	autoReconcile := true
	if clean.AutoReconcile != nil {
		autoReconcile = *clean.AutoReconcile
	}
	initialSync := strings.TrimSpace(clean.InitialSync)
	if initialSync == "" {
		initialSync = "none"
	}
	if initialSync != "none" && initialSync != "stack_to_repository" && initialSync != "repository_to_stack" {
		return BindingView{}, errors.New("initial sync must be none, stack_to_repository, or repository_to_stack")
	}
	archived, archiveErr := s.store.ArchivedBinding(clean.Host, clean.StackPath)
	if archiveErr == nil {
		if archived.RepositoryUUID == clean.RepositoryID && archived.SubPath == clean.SubPath {
			archived.ComposePaths = strings.Join(compose, "\n")
			if strings.TrimSpace(clean.ComposeSelectionMode) != "" {
				archived.ComposeSelectionMode = selectionMode
				archived.SelectedComposePaths = strings.Join(selectedCompose, "\n")
			} else if normalizedComposeSelectionMode(archived.ComposeSelectionMode) == composeSelectionSelected {
				// A legacy relink keeps its previous choice, limited to stacks that still exist.
				available := make(map[string]struct{}, len(compose))
				for _, relative := range compose {
					available[relative] = struct{}{}
				}
				preserved := make([]string, 0)
				for _, relative := range splitPatternLines(archived.SelectedComposePaths) {
					if _, ok := available[relative]; ok {
						preserved = append(preserved, relative)
					}
				}
				archived.SelectedComposePaths = strings.Join(preserved, "\n")
			}
			if strings.TrimSpace(clean.SyncProfile) != "" {
				archived.SyncProfile = syncProfile
			}
			archived.AutoReconcileEnabled = autoReconcile
			archived.InitialSyncState = "checking"
			archived.InitialSyncError = ""
			if err := s.store.RestoreBinding(&archived); err != nil {
				return BindingView{}, err
			}
			if err := s.reconcileGitStackStatuses(archived); err != nil {
				return BindingView{}, err
			}
			return s.initializeBinding(ctx, archived, initialSync)
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
		ComposePaths: strings.Join(compose, "\n"), ComposeSelectionMode: selectionMode, SelectedComposePaths: strings.Join(selectedCompose, "\n"), SyncProfile: syncProfile, Enabled: true,
		AutoSyncIntervalMinutes: defaultAutoSyncIntervalMinutes, AutoSyncState: "disabled",
		AutoReconcileEnabled: autoReconcile, InitialSyncState: "checking",
	}
	if err := s.store.SaveBinding(&row); err != nil {
		return BindingView{}, err
	}
	if err := s.reconcileGitStackStatuses(row); err != nil {
		return BindingView{}, err
	}
	return s.initializeBinding(ctx, row, initialSync)
}

func (s *Service) initializeBinding(ctx context.Context, row StackBinding, direction string) (BindingView, error) {
	state, message := "pending", ""
	if !row.AutoReconcileEnabled && direction == "none" {
		// Link only: leave both sides untouched until the user previews a direction.
	} else if _, err := s.PullRepository(ctx, row.RepositoryUUID); err != nil {
		state, message = "error", safeGitError(err)
	} else if refreshed, _, refreshErr := s.refreshBindingComposeCatalogLocked(row); refreshErr != nil {
		state, message = "error", "refresh Compose catalog after pull: "+safeGitError(refreshErr)
	} else {
		if len(splitPatternLines(row.ComposePaths)) == 0 &&
			normalizedComposeSelectionMode(row.ComposeSelectionMode) == composeSelectionAll &&
			len(splitPatternLines(refreshed.ComposePaths)) > 0 {
			// This is still the initial link operation, not a later discovery.
			// Preserve the operator's initial "All stacks" choice after the pull
			// instead of turning newly visible remote stacks into an empty explicit
			// selection and falsely reconciling two empty inventories.
			refreshed.ComposeSelectionMode = composeSelectionAll
			refreshed.SelectedComposePaths = ""
			if saveErr := s.store.SaveBinding(&refreshed); saveErr != nil {
				return BindingView{}, saveErr
			}
			if reconcileErr := s.reconcileGitStackStatuses(refreshed); reconcileErr != nil {
				return BindingView{}, reconcileErr
			}
		}
		row = refreshed
		identical := false
		var reconcileErr error
		if row.AutoReconcileEnabled {
			identical, reconcileErr = s.reconcileBindingIfIdentical(row.UUID)
		}
		if reconcileErr != nil {
			state, message = "error", safeGitError(reconcileErr)
		} else if identical && row.AutoReconcileEnabled {
			state = "reconciled"
		} else if direction != "none" {
			preview, previewErr := s.PreviewBinding(row.UUID, direction, TransferInput{})
			if previewErr != nil {
				state, message = "error", safeGitError(previewErr)
			} else {
				resolved := make([]string, 0, preview.Conflicts)
				for _, entry := range preview.Entries {
					if entry.Status == "conflict" {
						resolved = append(resolved, entry.Path)
					}
				}
				input := TransferInput{PreviewToken: preview.PreviewToken, ResolvedPaths: resolved}
				var transferErr error
				if direction == "stack_to_repository" {
					input.CommitMessage = "chore(stack): initialize " + row.StackPath + " from Dockman"
					_, transferErr = s.ExportBinding(ctx, row.UUID, input)
					state = "exported"
				} else {
					_, transferErr = s.ImportBinding(ctx, row.UUID, input)
					state = "imported"
				}
				if transferErr != nil {
					state, message = "error", safeGitError(transferErr)
				}
			}
		}
	}
	now := time.Now().UTC()
	if err := s.store.UpdateBindingInitialSyncState(row.UUID, state, message, &now); err != nil {
		return BindingView{}, err
	}
	stackState := stackSyncPending
	success := false
	switch state {
	case "reconciled", "imported", "exported":
		stackState, success = stackSyncUpToDate, true
	}
	// The initial operation can fail before a per-stack preview exists (clone,
	// fetch, credentials, branch). Keep that error on the Folder Link instead of
	// copying it to every selected stack. Exact preview errors are recorded by
	// recordPreviewStackStatuses when they can be attributed safely.
	stackMessage := ""
	updates := map[string]any{"state": stackState, "error_message": stackMessage, "last_checked_at": &now}
	if success {
		updates["last_success_at"] = &now
	}
	_ = s.store.UpdateGitStackStatuses(row.UUID, selectedComposePaths(row), updates)
	updated, err := s.store.GetBinding(row.UUID)
	if err != nil {
		return BindingView{}, err
	}
	return s.bindingView(updated)
}

func (s *Service) reconcileBindingIfIdentical(id string) (bool, error) {
	binding, stackFiles, repositoryFiles, err := s.loadTransferTrees(id, "stack_to_repository", TransferInput{})
	if err != nil {
		return false, err
	}
	stackHashes := baselineFromSource(stackFiles)
	repositoryHashes := baselineFromSource(repositoryFiles)
	if len(stackHashes) != len(repositoryHashes) {
		return false, nil
	}
	for filePath, stackSHA := range stackHashes {
		if repositoryHashes[filePath] != stackSHA {
			return false, nil
		}
	}
	if err := s.store.ReplaceBindingBaseline(binding.UUID, stackHashes); err != nil {
		return false, err
	}
	return true, nil
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
	if policy == syncProfileComposeOnly && len(splitPatternLines(row.ComposePaths)) == 0 {
		refreshed, refreshErr := s.RefreshBindingComposeCatalog(id)
		if refreshErr != nil {
			return BindingView{}, fmt.Errorf("refresh Compose catalog before enabling Docker Compose only: %w", refreshErr)
		}
		row, err = s.store.GetBinding(refreshed.ID)
		if err != nil {
			return BindingView{}, err
		}
		if len(splitPatternLines(row.ComposePaths)) == 0 {
			return BindingView{}, composeOnlyCatalogError()
		}
	}
	// Serialize the final policy update with the automatic worker. The optional
	// catalog refresh above takes this same lock internally, so acquire it only
	// after that operation has completed.
	automationLock := s.repositoryLock("automation:" + id)
	automationLock.Lock()
	defer automationLock.Unlock()
	row, err = s.store.GetBinding(id)
	if err != nil {
		return BindingView{}, err
	}
	currentPolicy := row.SyncProfile
	if currentPolicy == "" {
		currentPolicy = syncProfileComposeConfig
	}
	policyChanged := currentPolicy != policy || row.IncludePatterns != strings.Join(includes, "\n") || row.ExcludePatterns != strings.Join(excludes, "\n")
	row.SyncProfile = policy
	row.IncludePatterns = strings.Join(includes, "\n")
	row.ExcludePatterns = strings.Join(excludes, "\n")
	ownershipLock := s.repositoryLock("binding-ownership:" + row.Host)
	ownershipLock.Lock()
	defer ownershipLock.Unlock()
	if err := s.validateBindingOwnership(row, row.UUID); err != nil {
		return BindingView{}, err
	}
	if policyChanged {
		// A prior error/conflict may belong to a file that this policy no longer
		// selects. Invalidate the commit shortcut and mark the projection pending
		// until the next bounded preview computes the new authoritative state.
		row.LastAutoSyncCommit = ""
		row.AutoSyncError = ""
		if row.AutoSyncEnabled {
			row.AutoSyncState = "watching"
		} else {
			row.AutoSyncState = "disabled"
		}
	}
	if err := s.store.SaveBinding(&row); err != nil {
		return BindingView{}, err
	}
	if policyChanged {
		if err := s.store.UpdateGitStackStatuses(row.UUID, selectedComposePaths(row), map[string]any{
			"state": stackSyncPending, "error_message": "", "conflict_count": 0,
		}); err != nil {
			return BindingView{}, err
		}
	}
	return s.bindingView(row)
}

func (s *Service) UpdateBindingComposeSelection(id string, input BindingComposeSelectionInput) (BindingView, error) {
	automationLock := s.repositoryLock("automation:" + id)
	automationLock.Lock()
	defer automationLock.Unlock()
	row, err := s.store.GetBinding(id)
	if err != nil {
		return BindingView{}, err
	}
	previousAutoTargets := strings.Join(autoSyncComposePaths(row), "\n")
	ownershipLock := s.repositoryLock("binding-ownership:" + row.Host)
	ownershipLock.Lock()
	defer ownershipLock.Unlock()
	repositoryLock := s.repositoryLock(row.RepositoryUUID)
	repositoryLock.Lock()
	defer repositoryLock.Unlock()
	var added []string
	row, added, err = s.refreshBindingComposeCatalogLocked(row)
	if err != nil {
		return BindingView{}, err
	}
	if len(added) > 0 && input.Mode == composeSelectionAll {
		// The caller built its "all" choice from an older catalog. Keep the
		// just-discovered paths outside that stale selection.
		input.Mode = composeSelectionSelected
	}
	mode, selected, err := normalizeComposeSelection(splitPatternLines(row.ComposePaths), input.Mode, input.ComposePaths, composeSelectionSelected)
	if err != nil {
		return BindingView{}, err
	}
	row.ComposeSelectionMode = mode
	row.SelectedComposePaths = strings.Join(selected, "\n")
	if err := s.validateBindingOwnership(row, row.UUID); err != nil {
		return BindingView{}, err
	}
	// A deselected stack must never remain authorized for automatic polling or deployment.
	if mode == composeSelectionSelected {
		allowed := make(map[string]struct{}, len(selected))
		for _, relative := range selected {
			allowed[relative] = struct{}{}
		}
		deploy := make([]string, 0)
		for _, relative := range splitPatternLines(row.AutoDeployComposePaths) {
			if _, ok := allowed[relative]; ok {
				deploy = append(deploy, relative)
			}
		}
		row.AutoDeployComposePaths = strings.Join(deploy, "\n")
		if normalizedComposeSelectionMode(row.AutoSyncSelectionMode) == composeSelectionSelected {
			row.AutoSyncComposePaths = strings.Join(keepCataloguedPaths(splitPatternLines(row.AutoSyncComposePaths), allowed), "\n")
		}
		if row.AutoDeployEnabled && len(deploy) == 0 && !row.AutoDeployNewStacks {
			row.AutoDeployEnabled = false
			row.AutoDeployRollbackEnabled = false
			row.AutoDeployState = "disabled"
			row.AutoDeployError = ""
		}
	}
	if strings.Join(autoSyncComposePaths(row), "\n") != previousAutoTargets {
		row.LastAutoSyncCommit = ""
	}
	if err := s.store.SaveBinding(&row); err != nil {
		return BindingView{}, err
	}
	if err := s.reconcileGitStackStatuses(row); err != nil {
		return BindingView{}, err
	}
	return s.bindingView(row)
}

// RefreshBindingComposeCatalog discovers Compose files added locally after a
// folder link was created. New stacks are deliberately catalogued as
// unselected so a local directory can never enter Git synchronization merely
// by appearing on disk.
func (s *Service) RefreshBindingComposeCatalog(id string) (BindingView, error) {
	automationLock := s.repositoryLock("automation:" + id)
	automationLock.Lock()
	defer automationLock.Unlock()
	row, err := s.store.GetBinding(id)
	if err != nil {
		return BindingView{}, err
	}
	repositoryLock := s.repositoryLock(row.RepositoryUUID)
	repositoryLock.Lock()
	defer repositoryLock.Unlock()
	row, _, err = s.refreshBindingComposeCatalogLocked(row)
	if err != nil {
		return BindingView{}, err
	}
	return s.bindingView(row)
}

func (s *Service) refreshBindingComposeCatalogLocked(row StackBinding) (StackBinding, []string, error) {
	targetFS, targetRoot, err := s.resolveBindingStack(row)
	if err != nil {
		return row, nil, err
	}
	if info, statErr := targetFS.Stat(targetRoot); statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return row, nil, fmt.Errorf("inspect linked stack folder: %w", statErr)
	} else if statErr == nil && !info.IsDir() {
		return row, nil, errors.New("linked stack path is no longer a directory")
	}
	localCompose := discoverComposeFiles(targetFS, targetRoot)
	repository, err := s.store.GetRepository(row.RepositoryUUID)
	if err != nil {
		return row, nil, err
	}
	remoteCompose, err := s.repositoryComposeCatalog(repository, row.SubPath)
	if err != nil {
		return row, nil, err
	}
	discovered := uniqueSortedStrings(append(localCompose, remoteCompose...))
	known := splitPatternLines(row.ComposePaths)
	knownSet := make(map[string]struct{}, len(known))
	for _, path := range known {
		knownSet[path] = struct{}{}
	}
	added := make([]string, 0)
	for _, path := range discovered {
		if _, exists := knownSet[path]; exists {
			continue
		}
		added = append(added, path)
	}
	discoveredSet := make(map[string]struct{}, len(discovered))
	for _, path := range discovered {
		discoveredSet[path] = struct{}{}
	}
	removed := make([]string, 0)
	for _, path := range known {
		if _, exists := discoveredSet[path]; !exists {
			removed = append(removed, path)
		}
	}
	if len(added) == 0 && len(removed) == 0 {
		s.recordMissingLocalComposeStatuses(row, localCompose)
		return row, nil, nil
	}
	// Capture the effective selection before extending the catalog. An old
	// "all" selection therefore becomes an explicit selection of the stacks
	// the user already approved, leaving every newly found stack grey.
	selected := selectedComposePaths(row)
	selected = keepCataloguedPaths(selected, discoveredSet)
	row.ComposePaths = strings.Join(discovered, "\n")
	if len(added) > 0 {
		row.ComposeSelectionMode = composeSelectionSelected
	}
	if len(selected) == len(discovered) && len(added) == 0 {
		row.ComposeSelectionMode = composeSelectionAll
	}
	row.SelectedComposePaths = strings.Join(uniqueSortedStrings(selected), "\n")
	row.AutoSyncComposePaths = strings.Join(keepCataloguedPaths(splitPatternLines(row.AutoSyncComposePaths), discoveredSet), "\n")
	row.AutoDeployComposePaths = strings.Join(keepCataloguedPaths(splitPatternLines(row.AutoDeployComposePaths), discoveredSet), "\n")
	if err := s.store.SaveBinding(&row); err != nil {
		return row, nil, err
	}
	if len(removed) > 0 {
		baseline, baselineErr := s.store.BindingBaseline(row.UUID)
		if baselineErr != nil {
			return row, nil, baselineErr
		}
		for path := range baseline {
			for _, removedCompose := range removed {
				if stringInSlice(removedCompose, composePathsForFile(known, path)) {
					delete(baseline, path)
					break
				}
			}
		}
		if err := s.store.ReplaceBindingBaseline(row.UUID, baseline); err != nil {
			return row, nil, err
		}
	}
	if err := s.reconcileGitStackStatuses(row); err != nil {
		return row, nil, err
	}
	s.recordMissingLocalComposeStatuses(row, localCompose)
	return row, added, nil
}

func (s *Service) recordMissingLocalComposeStatuses(binding StackBinding, localCompose []string) {
	local := make(map[string]struct{}, len(localCompose))
	for _, path := range localCompose {
		local[path] = struct{}{}
	}
	baseline, _ := s.store.BindingBaseline(binding.UUID)
	rows, _ := s.store.GitStackStatuses(binding.UUID)
	current := make(map[string]string, len(rows))
	for _, row := range rows {
		current[row.ComposePath] = row.State
	}
	now := time.Now().UTC()
	locallyDeleted := make([]string, 0)
	remoteOnly := make([]string, 0)
	for _, composePath := range selectedComposePaths(binding) {
		if _, exists := local[composePath]; exists {
			continue
		}
		if current[composePath] == stackSyncConflict || current[composePath] == stackSyncError {
			continue
		}
		if _, tracked := baseline[composePath]; tracked {
			locallyDeleted = append(locallyDeleted, composePath)
		} else {
			remoteOnly = append(remoteOnly, composePath)
		}
	}
	for state, paths := range map[string][]string{stackSyncLocalDeleted: locallyDeleted, stackSyncRemoteChanges: remoteOnly} {
		_ = s.store.UpdateGitStackStatuses(binding.UUID, paths, map[string]any{
			"state": state, "error_message": "", "conflict_count": 0, "last_checked_at": &now,
		})
	}
}

func keepCataloguedPaths(paths []string, catalog map[string]struct{}) []string {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if _, exists := catalog[path]; exists {
			result = append(result, path)
		}
	}
	return uniqueSortedStrings(result)
}

func (s *Service) repositoryComposeCatalog(repository Repository, subPath string) ([]string, error) {
	repo, err := s.openRepository(repository)
	if err != nil {
		return nil, err
	}
	tree, err := repositoryCommitTree(repo, repository.DefaultBranch)
	if err != nil {
		return nil, err
	}
	tree, err = repositorySubtree(tree, subPath)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	return discoverRepositoryComposeFiles(tree), nil
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
		pattern := "/" + escapeGlobLiteral(relative)
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
	repositoryLock := s.repositoryLock(row.RepositoryUUID)
	repositoryLock.Lock()
	defer repositoryLock.Unlock()
	row.AutoSyncEnabled = false
	row.AutoSyncPaused = false
	row.AutoSyncState = "disabled"
	row.AutoSyncError = ""
	row.AutoDeployEnabled = false
	row.AutoDeployNewStacks = false
	row.AutoDeployRollbackEnabled = false
	row.AutoDeployState = "disabled"
	row.AutoDeployError = ""
	if err := s.store.SaveBinding(&row); err != nil {
		return err
	}
	if err := s.removeBindingBackups(row.UUID); err != nil {
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
		// Derive every nested source-folder choice from the single bounded root
		// catalog. Re-scanning each directory would become quadratic on large
		// trees and previously exposed only first-level folders, forcing distinct
		// nested groups to share one broad source root and then fail overlap checks.
		groups := make(map[string][]string)
		for _, composePath := range composeRoot {
			directory := filepath.ToSlash(filepath.Dir(filepath.FromSlash(composePath)))
			for directory != "." && directory != "" && directory != "/" {
				relative := strings.TrimPrefix(composePath, strings.Trim(directory, "/")+"/")
				groups[directory] = append(groups[directory], relative)
				directory = filepath.ToSlash(filepath.Dir(filepath.FromSlash(directory)))
			}
		}
		directories := make([]string, 0, len(groups))
		for directory := range groups {
			directories = append(directories, directory)
		}
		sort.Strings(directories)
		for _, directory := range directories {
			compose := uniqueSortedStrings(groups[directory])
			result = append(result, StackTarget{Host: host, Path: filepath.ToSlash(filepath.Join("compose", directory)), ComposePaths: compose, Scope: "folder", StackCount: countComposeFolders(compose)})
		}
	}
	return result, nil
}

// RepositoryStackCatalog lists Compose manifests from the managed branch
// without touching the live stacks filesystem. It powers the Git-first import
// flow where no local directory exists yet; the normal binding import remains
// responsible for fetching, validating, backing up and writing the files.
func (s *Service) RepositoryStackCatalog(repositoryID string) (RepositoryStackCatalog, error) {
	if !s.enabled {
		return RepositoryStackCatalog{}, errors.New("Git synchronization is disabled")
	}
	repository, err := s.store.GetRepository(strings.TrimSpace(repositoryID))
	if err != nil {
		return RepositoryStackCatalog{}, err
	}
	compose, err := s.repositoryComposeCatalog(repository, ".")
	if err != nil {
		return RepositoryStackCatalog{}, fmt.Errorf("discover repository Compose catalog: %w", err)
	}
	return RepositoryStackCatalog{RepositoryID: repository.UUID, Branch: repository.DefaultBranch, ComposePaths: compose}, nil
}

func (s *Service) PreviewBinding(id, direction string, input TransferInput) (TransferPreview, error) {
	if !input.automation {
		binding, err := s.store.GetBinding(id)
		if err != nil {
			return TransferPreview{}, err
		}
		if binding.SyncProfile == syncProfileComposeOnly && len(splitPatternLines(binding.ComposePaths)) == 0 {
			if _, err := s.RefreshBindingComposeCatalog(id); err != nil {
				return TransferPreview{}, fmt.Errorf("refresh empty Compose catalog: %w", err)
			}
		}
	}
	binding, source, target, err := s.loadTransferTrees(id, direction, input)
	if err != nil {
		return TransferPreview{}, err
	}
	baseline, err := s.store.BindingBaseline(binding.UUID)
	if err != nil {
		return TransferPreview{}, err
	}
	preview := buildPreview(binding.UUID, direction, source, target, baseline)
	if direction == "repository_to_stack" {
		preview.OrphanedComposePaths = detectOrphanedComposePaths(source, target, baseline, selectedComposePaths(binding))
		if input.automation {
			preview.ComposeErrors, _ = composeFileErrors(source)
		}
	}
	preview.automation = input.automation
	s.recordPreviewStackStatuses(binding, preview)
	return preview, nil
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
	var exportedPaths []string
	pendingConflicts := 0
	trigger := "manual"
	if input.automation {
		trigger = "automation"
	}
	err = s.runBindingOperationWithTrigger(ctx, binding.RepositoryUUID, binding.UUID, "stack_export", trigger, func(ctx context.Context) error {
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
		result.Preview.automation = input.automation
		result.ReadBlocked = previewProtectedLocalPaths(result.Preview)
		s.recordPreviewStackStatuses(binding, result.Preview)
		if err := validatePreviewToken(input.PreviewToken, result.Preview.PreviewToken); err != nil {
			return err
		}
		if result.Preview.Changed == 0 {
			if err := s.store.ReplaceBindingBaseline(binding.UUID, baselineFromSourcePreservingControl(source, baseline)); err != nil {
				return err
			}
			result.Message = "Repository already matches the readable stack files"
			if len(result.ReadBlocked) > 0 {
				result.Message += fmt.Sprintf("; %d protected local item(s) skipped without blocking other stacks", len(result.ReadBlocked))
			}
			return nil
		}
		selected, remainingConflicts, err := selectedTransferFiles(result.Preview, source, input.ResolvedPaths, input.SelectedPaths)
		if err != nil {
			return err
		}
		pendingConflicts = remainingConflicts
		if len(selected) == 0 {
			return errors.New("no transferable file was selected; resolve at least one conflict or leave this transfer pending")
		}
		exportedPaths = make([]string, 0, len(selected))
		for path := range selected {
			exportedPaths = append(exportedPaths, path)
		}
		sort.Strings(exportedPaths)
		row, err := s.store.GetRepository(binding.RepositoryUUID)
		if err != nil {
			return err
		}
		repo, err := s.openRepository(row)
		if err != nil {
			return err
		}
		temporaryRepo, checkoutPath, cleanup, err := temporaryRepositoryWorktree(repo, s.workspaceRoot)
		if err != nil {
			return err
		}
		defer cleanup()
		worktree, err := temporaryRepo.Worktree()
		if err != nil {
			return err
		}
		if err := worktree.Checkout(&gitclient.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(row.DefaultBranch), Force: true}); err != nil {
			return fmt.Errorf("prepare temporary Git checkout: %w", err)
		}
		if err := writeRepositoryFiles(checkoutPath, binding.SubPath, selected); err != nil {
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
		message = s.commitMessageWithProvenance(message, &binding)
		hash, err := worktree.Commit(message, s.bindingCommitOptions(row, &binding, time.Now()))
		if err != nil {
			return fmt.Errorf("commit stack export: %w", err)
		}
		auth, err := s.authForRepository(ctx, row)
		if err != nil {
			return err
		}
		pushCtx, cancelPush := gitNetworkContext(ctx)
		defer cancelPush()
		if err := repo.PushContext(pushCtx, &gitclient.PushOptions{RemoteName: "origin", Auth: auth}); err != nil && !errors.Is(err, gitclient.NoErrAlreadyUpToDate) {
			return fmt.Errorf("push stack export: %w", err)
		}
		compactRepositoryObjects(repo, binding.RepositoryUUID)
		if err := s.store.ReplaceBindingBaseline(binding.UUID, baselineAfterTransfer(baseline, source, target, selected)); err != nil {
			return err
		}
		result.CommitSHA, result.Message = hash.String(), "Stack exported, committed, and pushed"
		if len(result.ReadBlocked) > 0 {
			result.Message += fmt.Sprintf("; %d protected local item(s) skipped without blocking other stacks", len(result.ReadBlocked))
		}
		if pendingConflicts > 0 {
			result.Message += fmt.Sprintf("; %d conflict(s) remain pending", pendingConflicts)
		}
		return nil
	})
	if err == nil {
		if stateErr := s.clearResolvedManualConflictState(binding, pendingConflicts); stateErr != nil {
			return result, fmt.Errorf("stack was pushed but its synchronization state could not be refreshed: %w", stateErr)
		}
		if result.Preview.Conflicts == 0 {
			paths := selectedComposePaths(binding)
			if input.automation {
				paths = s.activeAutomationComposePaths(binding)
			}
			now := time.Now().UTC()
			paths = excludeStringValues(paths, composePathsForFiles(paths, result.ReadBlocked))
			_ = s.store.UpdateGitStackStatuses(binding.UUID, paths, map[string]any{"state": stackSyncUpToDate, "error_message": "", "conflict_count": 0, "last_checked_at": &now, "last_success_at": &now, "last_commit": result.CommitSHA})
		}
		if !input.automation && len(exportedPaths) > 0 {
			completeStacks := fullyExportedComposePaths(binding, result.Preview, exportedPaths)
			if clearErr := s.clearResolvedDeploymentStates(binding, completeStacks); clearErr != nil {
				return result, fmt.Errorf("stack was pushed but its previous deployment warning could not be cleared: %w", clearErr)
			}
			if resumeErr := s.resumeRecoveryPausesAfterExport(binding, completeStacks); resumeErr != nil {
				return result, fmt.Errorf("stack was pushed but automatic synchronization could not be resumed: %w", resumeErr)
			}
		}
	}
	return result, err
}

func fullyExportedComposePaths(binding StackBinding, preview TransferPreview, exportedPaths []string) map[string]struct{} {
	complete := make(map[string]struct{})
	exported := make(map[string]struct{}, len(exportedPaths))
	composePaths := selectedComposePaths(binding)
	for _, path := range exportedPaths {
		exported[path] = struct{}{}
		for _, composePath := range composePathsForFile(composePaths, path) {
			complete[composePath] = struct{}{}
		}
	}
	// A settings transfer may intentionally select only part of a stack. Resume
	// a recovery pause only when every remaining local/conflicting change owned
	// by that stack was part of the successful export.
	for _, entry := range preview.Entries {
		if entry.Status != "add" && entry.Status != "modify" && entry.Status != "conflict" && entry.Status != "deleted_locally" {
			continue
		}
		_, wasExported := exported[entry.Path]
		for _, composePath := range composePathsForFile(composePaths, entry.Path) {
			if _, candidate := complete[composePath]; candidate && (!wasExported || entry.Status == "deleted_locally") {
				delete(complete, composePath)
			}
		}
	}
	return complete
}

func (s *Service) resumeRecoveryPausesAfterExport(binding StackBinding, complete map[string]struct{}) error {
	statuses, err := s.store.GitStackStatuses(binding.UUID)
	if err != nil {
		return err
	}
	for _, status := range statuses {
		if _, ok := complete[status.ComposePath]; !ok || !status.AutomationPaused || status.PauseReason != stackPauseRecovery {
			continue
		}
		if err := s.store.SetGitStackPause(binding.UUID, status.ComposePath, false); err != nil {
			return err
		}
	}
	return nil
}

// A successful complete push makes the rollback incident historical: the
// corrected local state is now the Git baseline. Clear only the stacks fully
// covered by that push and retain failures belonging to every other stack.
func (s *Service) clearResolvedDeploymentStates(binding StackBinding, complete map[string]struct{}) error {
	if !binding.AutoDeployEnabled || len(complete) == 0 {
		return nil
	}
	statuses, err := s.store.GitStackStatuses(binding.UUID)
	if err != nil {
		return err
	}
	cleared := make(map[string]struct{})
	for _, status := range statuses {
		if _, ok := complete[status.ComposePath]; !ok {
			continue
		}
		switch status.DeployState {
		case "failed", "rolled_back", "rollback_failed":
			cleared[status.ComposePath] = struct{}{}
		}
	}
	paths := make([]string, 0, len(cleared))
	for path := range cleared {
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return nil
	}
	if err := s.store.UpdateGitStackStatuses(binding.UUID, paths, map[string]any{"deploy_state": "idle", "deploy_error": ""}); err != nil {
		return err
	}

	state, message := "watching", ""
	attention := 0
	for _, status := range statuses {
		if _, ok := cleared[status.ComposePath]; ok {
			continue
		}
		if !stringInSlice(status.ComposePath, splitPatternLines(binding.AutoDeployComposePaths)) {
			continue
		}
		switch status.DeployState {
		case "rollback_failed", "failed":
			state = "failed"
			attention++
		case "rolled_back":
			if state != "failed" {
				state = "partial"
			}
			attention++
		case "pending", "validating", "dry_run", "deploying", "rolling_back":
			if state == "watching" {
				state = "pending"
			}
		}
	}
	if attention > 0 {
		message = fmt.Sprintf("%d stack(s) still require deployment attention", attention)
	}
	return s.store.UpdateBindingAutoDeployState(binding.UUID, state, message, nil)
}

// A new Git commit can intentionally restore the exact file contents that the
// automatic rollback already put back locally. No transfer or redeploy is
// needed in that case, but the former rolled-back incident is now resolved and
// must stop keeping the link partial. Hard failures remain untouched.
func (s *Service) clearIdenticalRollbackStates(binding StackBinding) error {
	statuses, err := s.store.GitStackStatuses(binding.UUID)
	if err != nil {
		return err
	}
	complete := make(map[string]struct{})
	for _, status := range statuses {
		if status.DeployState == "rolled_back" && stringInSlice(status.ComposePath, splitPatternLines(binding.AutoDeployComposePaths)) {
			complete[status.ComposePath] = struct{}{}
		}
	}
	return s.clearResolvedDeploymentStates(binding, complete)
}

// A manual transfer can resolve the last conflict before the automatic worker
// gets another turn. Do not leave the folder link painted as conflicted when
// the transfer preview confirms that no decision remains pending. The next
// regular check still performs the complete Git -> Dockman reconciliation;
// this only removes the obsolete incident state and never deploys anything.
func (s *Service) clearResolvedManualConflictState(binding StackBinding, pendingConflicts int) error {
	if !binding.AutoSyncEnabled || binding.AutoSyncState != "conflict" || pendingConflicts > 0 {
		return nil
	}
	return s.store.UpdateBindingAutoSyncState(binding.UUID, "watching", "", "", nil, nil)
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
	pendingConflicts := 0
	trigger := "manual"
	if input.automation {
		trigger = "automation"
	}
	err = s.runBindingOperationWithTrigger(ctx, binding.RepositoryUUID, binding.UUID, "stack_import", trigger, func(ctx context.Context) error {
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
		baseline, err := s.store.BindingBaseline(binding.UUID)
		if err != nil {
			return err
		}
		composeErrors, composeFound := composeFileErrors(source)
		if validationErr := firstComposeValidationError(composeErrors, composeFound); validationErr != nil && !input.automation {
			if !hasTrackedDeletedCompose(source, target, baseline, selectedComposePaths(binding)) {
				return validationErr
			}
		}
		if !composeFound && !hasTrackedDeletedCompose(source, target, baseline, selectedComposePaths(binding)) {
			return errors.New("repository path contains no compose file")
		}
		result.Preview = buildPreview(binding.UUID, "repository_to_stack", source, target, baseline)
		result.Preview.OrphanedComposePaths = detectOrphanedComposePaths(source, target, baseline, selectedComposePaths(binding))
		result.Preview.automation = input.automation
		if input.automation {
			result.Preview.ComposeErrors = composeErrors
		}
		result.ReadBlocked = previewProtectedLocalPaths(result.Preview)
		s.recordPreviewStackStatuses(binding, result.Preview)
		if err := validatePreviewToken(input.PreviewToken, result.Preview.PreviewToken); err != nil {
			return err
		}
		if result.Preview.Changed == 0 {
			if err := s.store.ReplaceBindingBaseline(binding.UUID, baselineFromSource(source)); err != nil {
				return err
			}
			result.Message = "Readable stack files already match the repository"
			if len(result.ReadBlocked) > 0 {
				result.Message += fmt.Sprintf("; %d protected local item(s) skipped without blocking other stacks", len(result.ReadBlocked))
			}
			return nil
		}
		selected, remainingConflicts, err := selectedTransferFiles(result.Preview, source, input.ResolvedPaths, input.SelectedPaths)
		if err != nil {
			return err
		}
		pendingConflicts = remainingConflicts
		if len(selected) == 0 {
			if result.Preview.Preserved > 0 && pendingConflicts == 0 {
				result.Message = fmt.Sprintf("%d Git deletion(s) preserved locally; choose an explicit orphan action", result.Preview.Preserved)
				return nil
			}
			return errors.New("no transferable file was selected; resolve at least one conflict or leave this transfer pending")
		}
		selected, result.EditorBlocked = s.excludeDirtyEditorStacks(binding, selected)
		if input.automation && len(composeErrors) > 0 {
			selected, result.ComposeBlocked = excludeInvalidComposeStacks(selected, source, composeErrors)
		}
		if len(selected) == 0 {
			if len(result.ComposeBlocked) > 0 {
				result.Message = fmt.Sprintf("%d stack(s) kept unchanged because their Compose file is invalid", len(result.ComposeBlocked))
			} else {
				result.Message = "Synchronization paused for the stack currently being edited; no file was overwritten"
			}
			return nil
		}
		targetFS, targetRoot, err := s.resolveBindingStack(binding)
		if err != nil {
			return err
		}
		stackFiles, controlFiles := splitProvisionControlFiles(selected)
		backup := ""
		if len(stackFiles) > 0 {
			backup, err = s.backupChangedFiles(binding, stackFiles, target, "pre_import", status.Head)
			if err != nil {
				return err
			}
		}
		if err := writeStackFiles(targetFS, targetRoot, stackFiles); err != nil {
			return err
		}
		if s.fileChangeNotify != nil {
			for path := range stackFiles {
				s.fileChangeNotify(binding.Host, filepath.ToSlash(filepath.Join(binding.StackPath, path)))
			}
		}
		if err := s.store.ReplaceBindingBaseline(binding.UUID, baselineAfterTransfer(baseline, source, target, selected)); err != nil {
			return err
		}
		selectedPaths := make([]string, 0, len(selected))
		for path := range selected {
			selectedPaths = append(selectedPaths, path)
		}
		if binding.AutoDeployEnabled && len(deploymentTargetsForChanges(binding, selectedPaths)) > 0 {
			_ = s.store.UpdateBindingAutoDeployState(binding.UUID, "pending", "Imported changes are waiting for controlled deployment", nil)
		}
		result.Backup, result.Message = backup, "Repository files imported with a backup; the stack was not deployed"
		if len(controlFiles) > 0 && len(stackFiles) == 0 {
			result.Message = "Provisioning manifest synchronized; it will be applied by the controlled deployment"
		}
		if len(result.ComposeBlocked) > 0 {
			result.Message = fmt.Sprintf("%d invalid Compose stack(s) kept unchanged; other safe repository files were imported with a backup", len(result.ComposeBlocked))
		}
		if len(result.ReadBlocked) > 0 {
			result.Message += fmt.Sprintf("; %d protected local item(s) skipped without blocking other stacks", len(result.ReadBlocked))
		}
		if pendingConflicts > 0 {
			result.Message += fmt.Sprintf("; %d conflict(s) remain pending", pendingConflicts)
		}
		return nil
	})
	if err == nil {
		if stateErr := s.clearResolvedManualConflictState(binding, pendingConflicts); stateErr != nil {
			return result, fmt.Errorf("repository files were imported but the synchronization state could not be refreshed: %w", stateErr)
		}
	}
	if err == nil && result.Preview.Conflicts == 0 && result.Preview.Preserved == 0 && len(result.EditorBlocked) == 0 {
		paths := selectedComposePaths(binding)
		if input.automation {
			paths = s.activeAutomationComposePaths(binding)
		}
		now := time.Now().UTC()
		safePaths := excludeStringValues(paths, result.ComposeBlocked)
		_ = s.store.UpdateGitStackStatuses(binding.UUID, safePaths, map[string]any{"state": stackSyncUpToDate, "error_message": "", "conflict_count": 0, "last_checked_at": &now, "last_success_at": &now})
		for _, composePath := range result.ComposeBlocked {
			message := result.Preview.ComposeErrors[composePath]
			if message == "" {
				message = "Compose validation failed"
			}
			_ = s.store.UpdateGitStackStatuses(binding.UUID, []string{composePath}, map[string]any{"state": stackSyncError, "error_message": message, "last_checked_at": &now})
		}
	}
	if input.compactResult {
		result.Preview.Entries = nil
	}
	return result, err
}

func (s *Service) excludeDirtyEditorStacks(binding StackBinding, selected map[string]transferFile) (map[string]transferFile, []string) {
	if s.dirtyEditorPaths == nil || len(selected) == 0 {
		return selected, nil
	}
	root := strings.Trim(filepath.ToSlash(filepath.Clean(filepath.FromSlash(binding.StackPath))), "/")
	composePaths := selectedComposePaths(binding)
	blockedDirs := map[string]struct{}{}
	blockedCompose := map[string]struct{}{}
	for _, openPath := range s.dirtyEditorPaths(binding.Host) {
		openPath = strings.Trim(filepath.ToSlash(filepath.Clean(filepath.FromSlash(openPath))), "/")
		relative := openPath
		if root != "." && root != "" {
			if openPath == root {
				relative = "."
			} else if strings.HasPrefix(openPath, root+"/") {
				relative = strings.TrimPrefix(openPath, root+"/")
			} else {
				continue
			}
		}
		bestCompose, bestDir := "", ""
		for _, composePath := range composePaths {
			dir := filepath.ToSlash(filepath.Dir(filepath.FromSlash(composePath)))
			if dir == "." {
				dir = ""
			}
			if (dir == "" || relative == dir || strings.HasPrefix(relative, dir+"/")) && len(dir) >= len(bestDir) {
				bestCompose, bestDir = composePath, dir
			}
		}
		if bestCompose != "" {
			blockedCompose[bestCompose] = struct{}{}
			blockedDirs[bestDir] = struct{}{}
		}
	}
	if len(blockedDirs) == 0 {
		return selected, nil
	}
	filtered := make(map[string]transferFile, len(selected))
	for path, file := range selected {
		blocked := false
		for dir := range blockedDirs {
			if dir == "" {
				// A root stack owns root files only; nested stacks remain independent.
				blocked = !strings.Contains(path, "/")
			} else {
				blocked = path == dir || strings.HasPrefix(path, dir+"/")
			}
			if blocked {
				break
			}
		}
		if !blocked {
			filtered[path] = file
		}
	}
	blocked := make([]string, 0, len(blockedCompose))
	for path := range blockedCompose {
		blocked = append(blocked, path)
	}
	sort.Strings(blocked)
	return filtered, blocked
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
	candidate := StackBinding{
		RepositoryUUID: input.RepositoryID, Host: input.Host, StackPath: input.StackPath, SubPath: input.SubPath, ComposeSelectionMode: input.ComposeSelectionMode,
		SelectedComposePaths: strings.Join(input.SelectedComposePaths, "\n"),
	}
	if err := s.validateBindingOwnership(candidate, ""); err != nil {
		return input, nil, "", err
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

type bindingOwnership struct {
	path      string
	recursive bool
}

func (s *Service) validateBindingOwnership(candidate StackBinding, ignoreID string) error {
	if err := s.validateBindingRepositoryOwnership(candidate, ignoreID); err != nil {
		return err
	}
	return s.validateBindingLocalOwnership(candidate, ignoreID)
}

// Repository ownership follows the same selection-aware rules as the live
// stack filesystem. Two Folder Links on the same managed repository/branch may
// use nested Git paths when their selected Compose roots are disjoint. An All
// stacks link, a duplicate stack selection, or an explicit include reaching
// the other subtree still fails closed.
func (s *Service) validateBindingRepositoryOwnership(candidate StackBinding, ignoreID string) error {
	rows, err := s.store.ListBindings()
	if err != nil {
		return err
	}
	candidateRepository := repositoryOwnershipBinding(candidate)
	for _, existing := range rows {
		if existing.UUID == ignoreID || existing.RepositoryUUID != candidate.RepositoryUUID || !pathsOverlap(existing.SubPath, candidate.SubPath) {
			continue
		}
		existingRepository := repositoryOwnershipBinding(existing)
		if bindingOwnershipsOverlap(existingRepository, candidateRepository) || bindingIncludeMayReach(existingRepository, candidateRepository.StackPath) || bindingIncludeMayReach(candidateRepository, existingRepository.StackPath) {
			return fmt.Errorf("repository path overlaps files selected by existing link %s on host %s; choose unlinked Git stacks or deselect them from the other link first", existing.StackPath, existing.Host)
		}
	}
	return nil
}

func repositoryOwnershipBinding(binding StackBinding) StackBinding {
	binding.StackPath = binding.SubPath
	if strings.TrimSpace(binding.StackPath) == "" {
		binding.StackPath = "."
	}
	return binding
}

// validateBindingLocalOwnership permits nested Folder Links only when their
// effective Compose selections are disjoint. A link in "all" mode reserves
// its complete source tree; a selected link owns only the directories of its
// approved Compose manifests. This lets sibling subfolders target independent
// branches without ever giving two links authority over the same live files.
func (s *Service) validateBindingLocalOwnership(candidate StackBinding, ignoreID string) error {
	rows, err := s.store.ListBindings()
	if err != nil {
		return err
	}
	for _, existing := range rows {
		if existing.UUID == ignoreID || existing.Host != candidate.Host || !pathsOverlap(existing.StackPath, candidate.StackPath) {
			continue
		}
		if bindingOwnershipsOverlap(existing, candidate) || bindingIncludeMayReach(existing, candidate.StackPath) || bindingIncludeMayReach(candidate, existing.StackPath) {
			return fmt.Errorf("source folder overlaps files selected by existing link %s on host %s; choose a disjoint subfolder or deselect those stacks first", existing.StackPath, existing.Host)
		}
	}
	return nil
}

func bindingOwnershipsOverlap(left, right StackBinding) bool {
	for _, leftOwnership := range bindingOwnerships(left) {
		for _, rightOwnership := range bindingOwnerships(right) {
			if ownershipsOverlap(leftOwnership, rightOwnership) {
				return true
			}
		}
	}
	return false
}

func bindingOwnerships(binding StackBinding) []bindingOwnership {
	root := strings.Trim(filepath.ToSlash(filepath.Clean(filepath.FromSlash(binding.StackPath))), "/")
	if normalizedComposeSelectionMode(binding.ComposeSelectionMode) == composeSelectionAll {
		return []bindingOwnership{{path: root, recursive: true}}
	}
	result := make([]bindingOwnership, 0)
	seen := make(map[bindingOwnership]struct{})
	for _, composePath := range selectedComposePaths(binding) {
		directory := filepath.ToSlash(filepath.Dir(filepath.FromSlash(composePath)))
		ownership := bindingOwnership{path: root, recursive: false}
		if directory != "." && directory != "" {
			ownership.path = strings.Trim(filepath.ToSlash(filepath.Join(root, directory)), "/")
			ownership.recursive = true
		}
		if _, exists := seen[ownership]; !exists {
			seen[ownership] = struct{}{}
			result = append(result, ownership)
		}
	}
	return result
}

func ownershipsOverlap(left, right bindingOwnership) bool {
	if left.path == right.path {
		return true
	}
	return left.recursive && strings.HasPrefix(right.path, left.path+"/") ||
		right.recursive && strings.HasPrefix(left.path, right.path+"/")
}

func bindingIncludeMayReach(binding StackBinding, targetRoot string) bool {
	bindingRoot := strings.Trim(filepath.ToSlash(filepath.Clean(filepath.FromSlash(binding.StackPath))), "/")
	targetRoot = strings.Trim(filepath.ToSlash(filepath.Clean(filepath.FromSlash(targetRoot))), "/")
	if targetRoot == bindingRoot || !strings.HasPrefix(targetRoot, bindingRoot+"/") {
		return false
	}
	relative := strings.TrimPrefix(targetRoot, bindingRoot+"/")
	rules, err := rulesFromPatterns(splitPatternLines(binding.IncludePatterns))
	if err != nil {
		return true
	}
	for _, rule := range rules {
		if !rule.basename && (matchesIgnoreRule([]ignoreRule{rule}, relative, false) || includeRuleCanMatchBelowDirectory(rule, relative)) {
			return true
		}
	}
	return false
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
		ComposeSelectionMode: normalizedComposeSelectionMode(row.ComposeSelectionMode), SelectedComposePaths: selectedComposePaths(row),
		SyncProfile: profile, IncludePatterns: splitPatternLines(row.IncludePatterns),
		ExcludePatterns: splitPatternLines(row.ExcludePatterns), Enabled: row.Enabled,
		AutoSyncEnabled: row.AutoSyncEnabled, AutoSyncPaused: row.AutoSyncPaused, AutoSyncIntervalMinutes: row.AutoSyncIntervalMinutes,
		AutoSyncSelectionMode: normalizedComposeSelectionMode(row.AutoSyncSelectionMode), AutoSyncComposePaths: autoSyncComposePaths(row),
		AutoSyncState: row.AutoSyncState, AutoSyncError: row.AutoSyncError,
		LastAutoSyncAt: row.LastAutoSyncAt, LastAutoSyncSuccessAt: row.LastAutoSyncSuccessAt,
		AutoDeployEnabled: row.AutoDeployEnabled, AutoDeployNewStacks: row.AutoDeployNewStacks,
		AutoDeployRollbackEnabled: row.AutoDeployRollbackEnabled, AutoDeployComposePaths: splitPatternLines(row.AutoDeployComposePaths),
		AutoDeployState: row.AutoDeployState, AutoDeployError: row.AutoDeployError, LastAutoDeployAt: row.LastAutoDeployAt,
		AutoReconcileEnabled: row.AutoReconcileEnabled, InitialSyncState: row.InitialSyncState,
		InitialSyncError: row.InitialSyncError, InitialSyncAt: row.InitialSyncAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

func discoverComposeFiles(targetFS filesystem.FileSystem, root string) []string {
	names := make([]string, 0)
	type pendingDirectory struct {
		path     string
		relative string
		depth    int
	}

	// Breadth-first discovery is important here. A stack may contain a large
	// data/cache tree; a depth-first walk can exhaust the bounded directory
	// budget inside that first tree and never inspect its sibling stacks. Keep
	// the same limits, but inspect every shallower folder before descending.
	queue := []pendingDirectory{{path: root}}
	visited := 0
	for len(queue) > 0 && visited < 1000 && len(names) < 500 {
		current := queue[0]
		queue = queue[1:]
		if current.depth > 8 {
			continue
		}
		visited++
		entries, err := targetFS.ReadDir(current.path)
		if err != nil {
			continue
		}
		children := make([]pendingDirectory, 0)
		for _, entry := range entries {
			childRelative := filepath.ToSlash(filepath.Join(current.relative, entry.Name()))
			if entry.Type()&os.ModeSymlink != 0 || shouldSkipPath(childRelative, entry.IsDir()) {
				continue
			}
			if entry.IsDir() {
				children = append(children, pendingDirectory{
					path:     targetFS.Join(current.path, entry.Name()),
					relative: childRelative,
					depth:    current.depth + 1,
				})
				continue
			}
			switch strings.ToLower(entry.Name()) {
			case "compose.yml", "compose.yaml", "docker-compose.yml", "docker-compose.yaml":
				names = append(names, childRelative)
			}
		}
		// Bound the pending work as well as the number of filesystem calls. This
		// prevents a single very wide directory from increasing memory usage.
		remaining := 1000 - visited - len(queue)
		if remaining > len(children) {
			remaining = len(children)
		}
		if remaining > 0 {
			queue = append(queue, children[:remaining]...)
		}
	}
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
	if input.automation {
		active := s.activeAutomationComposePaths(binding)
		binding.ComposeSelectionMode = composeSelectionSelected
		binding.SelectedComposePaths = strings.Join(active, "\n")
	}
	repositoryRow, err := s.store.GetRepository(binding.RepositoryUUID)
	if err != nil {
		return binding, nil, nil, err
	}
	policy, err := policyFromBinding(binding, repositoryRow)
	if err != nil {
		return binding, nil, nil, err
	}
	// Automatic new-stack deployment may legitimately start with an empty
	// catalog. Authorize only Compose paths newly discovered in the current Git
	// tree for this cycle; normal Compose-only transfers still fail closed, and
	// successful deployment registers the paths through the existing workflow.
	if input.automation && policy.selectNewCompose {
		remoteCompose, catalogErr := s.repositoryComposeCatalog(repositoryRow, binding.SubPath)
		if catalogErr != nil {
			return binding, nil, nil, fmt.Errorf("discover automatic Git stack candidates: %w", catalogErr)
		}
		if policy.selectedRoots == nil {
			policy.selectedRoots = make(map[string]struct{})
		}
		for _, relative := range remoteCompose {
			if _, known := policy.compose[relative]; known {
				continue
			}
			policy.compose[relative] = struct{}{}
			policy.selectedRoots[filepath.ToSlash(filepath.Dir(filepath.FromSlash(relative)))] = struct{}{}
		}
	}
	targetFS, targetRoot, err := s.resolveBindingStack(binding)
	if err != nil {
		return binding, nil, nil, err
	}
	stackFiles, err := collectStackFiles(targetFS, targetRoot, input.IncludeSensitive, policy)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return binding, nil, nil, fmt.Errorf("read stack files: %w", err)
	}
	repository, err := s.openRepository(repositoryRow)
	if err != nil {
		return binding, nil, nil, err
	}
	repositoryFiles, err := collectRepositoryFiles(repository, repositoryRow.DefaultBranch, binding.SubPath, input.IncludeSensitive, policy)
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
	if err := validateComposeOnlyCatalog(policy); err != nil {
		return nil, err
	}
	policy = policy.withComposeDirectoryIndex()
	ignoreRules, err := loadStackIgnoreRules(targetFS, root)
	if err != nil {
		return nil, err
	}
	var total int64
	var walk func(string, string) (int, error)
	walk = func(dir, rel string) (int, error) {
		entries, err := targetFS.ReadDir(dir)
		if err != nil {
			if rel != "" && errors.Is(err, fs.ErrPermission) {
				log.Warn().Str("path", rel).Err(err).Msg("Git stack sync skipped unreadable directory")
				result[rel] = transferFile{path: rel, skipReason: "permission", directory: true}
				return 0, nil
			}
			return 0, err
		}
		observed := 0
		for _, entry := range entries {
			childRel := filepath.ToSlash(filepath.Join(rel, entry.Name()))
			// Provision manifests are Git-side control files. They are never
			// exported from or copied into the live stack directory.
			if !entry.IsDir() && isProvisionControlPath(childRel) {
				continue
			}
			selected, traverse := policy.selectsPath(childRel, entry.IsDir())
			if !selected {
				if entry.IsDir() && traverse {
					childCount, err := walk(targetFS.Join(dir, entry.Name()), childRel)
					if err != nil {
						return 0, err
					}
					observed += childCount
				}
				continue
			}
			if shouldSkipPath(childRel, entry.IsDir()) {
				continue
			}
			if policy.excludesPath(childRel, entry.IsDir(), ignoreRules) && !policy.explicitlyIncludes(childRel, entry.IsDir()) && !policy.protectsCompose(childRel) && !policy.protectsProvision(childRel) {
				if entry.IsDir() && policy.containsCompose(childRel) {
					child := targetFS.Join(dir, entry.Name())
					childCount, err := walk(child, childRel)
					if err != nil {
						return 0, err
					}
					observed += childCount
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
				// Compose-only is a strict allow-list. Once the Compose catalog is
				// known, do not even open unrelated application-data directories:
				// they may contain secrets, sockets, locked files, or very large
				// trees and none of them belongs to this synchronization profile.
				if policy.profile == syncProfileComposeOnly && !policy.traversesComposeOnlyDirectory(childRel) {
					continue
				}
				childCount, err := walk(child, childRel)
				if err != nil {
					return 0, err
				}
				observed += childCount
				if autoExcludeLargeDirectory(result, policy, rel, observed) {
					return observed, nil
				}
				continue
			}
			// Compose-only is an allow-list profile: unrelated files are outside
			// the synchronization inventory altogether. In particular, do not
			// stat, hash, count, or tokenise mutable application data merely to
			// report it as skipped. Explicit include rules still opt individual
			// files back into the normal protected transfer path below.
			if policy.profile == syncProfileComposeOnly && !policy.includesFile(childRel) {
				continue
			}
			info, err := targetFS.Stat(child)
			if err != nil {
				if errors.Is(err, fs.ErrPermission) {
					log.Warn().Str("file", childRel).Err(err).Msg("Git stack sync skipped unreadable file")
					result[childRel] = transferFile{path: childRel, skipReason: "permission"}
					continue
				}
				return 0, err
			}
			if !isTransferFile(info.Mode()) {
				continue
			}
			observed++
			if autoExcludeLargeDirectory(result, policy, rel, observed) {
				return observed, nil
			}
			if len(result)+1 > maxBindingFiles {
				return 0, stackInventoryLimitError(policy, childRel, maxBindingFiles)
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
			if !policy.includesFile(childRel) && !policy.protectsProvision(childRel) {
				result[childRel] = transferFile{path: childRel, size: info.Size(), skipReason: "type"}
				continue
			}
			if total+info.Size() > maxBindingTotalSize {
				return 0, fmt.Errorf("%s files exceed the %d MiB total limit while scanning %s (%d MiB accumulated); exclude its data directory with .dockmanignore", stackInventoryOwner(policy, childRel), maxBindingTotalSize>>20, childRel, (total+info.Size())>>20)
			}
			if err := checkTransferLimit(len(result)+1, info.Size(), total+info.Size()); err != nil {
				return 0, fmt.Errorf("%s at %s: %w", stackInventoryOwner(policy, childRel), childRel, err)
			}
			total += info.Size()
			childPath := child
			file := transferFile{path: childRel, size: info.Size(), mode: info.Mode().Perm(), sensitive: sensitive, open: func() (io.ReadCloser, error) {
				return targetFS.OpenFile(childPath, os.O_RDONLY, 0)
			}}
			file.sha, err = hashTransferFile(file)
			if err != nil {
				if errors.Is(err, fs.ErrPermission) {
					log.Warn().Str("file", childRel).Err(err).Msg("Git stack sync skipped unreadable file")
					result[childRel] = transferFile{path: childRel, size: info.Size(), mode: info.Mode().Perm(), skipReason: "permission"}
					continue
				}
				return 0, err
			}
			result[childRel] = file
		}
		return observed, nil
	}
	_, err = walk(root, "")
	return result, err
}

func autoExcludeLargeDirectory(result map[string]transferFile, policy syncPolicy, relative string, observed int) bool {
	if relative == "" || observed <= maxAutoDirectoryFiles || policy.containsCompose(relative) {
		return false
	}
	prefix := strings.TrimSuffix(filepath.ToSlash(relative), "/") + "/"
	for candidate := range result {
		if candidate == relative || strings.HasPrefix(candidate, prefix) {
			delete(result, candidate)
		}
	}
	result[relative] = transferFile{path: relative, skipReason: "large_directory", directory: true}
	log.Warn().Str("directory", relative).Int("observed_files", observed).Int("automatic_limit", maxAutoDirectoryFiles).Msg("Git stack sync automatically skipped a large data directory")
	return true
}

func stackInventoryOwner(policy syncPolicy, relative string) string {
	compose := make([]string, 0, len(policy.compose))
	for candidate := range policy.compose {
		compose = append(compose, candidate)
	}
	owners := composePathsForFile(compose, relative)
	if len(owners) == 0 {
		return "folder link"
	}
	return "stack " + strings.Join(owners, ", ")
}

func stackInventoryLimitError(policy syncPolicy, relative string, limit int) error {
	return fmt.Errorf("%s exceeds the %d-item synchronization inventory while scanning %s; large data directories above %d files are skipped automatically, so this path needs an explicit folder exclusion in .dockmanignore or the folder-link settings", stackInventoryOwner(policy, relative), limit, relative, maxAutoDirectoryFiles)
}

func collectRepositoryFiles(repo *gitclient.Repository, branch, subPath string, includeSensitive bool, policies ...syncPolicy) (map[string]transferFile, error) {
	tree, err := repositoryCommitTree(repo, branch)
	if err != nil {
		return nil, err
	}
	return collectRepositoryTreeFiles(repo, tree, subPath, includeSensitive, policies...)
}

func collectRepositoryFilesAtCommit(repo *gitclient.Repository, commit plumbing.Hash, subPath string, includeSensitive bool, policies ...syncPolicy) (map[string]transferFile, error) {
	tree, err := repositoryCommitTreeAtHash(repo, commit)
	if err != nil {
		return nil, err
	}
	return collectRepositoryTreeFiles(repo, tree, subPath, includeSensitive, policies...)
}

func collectRepositoryTreeFiles(repo *gitclient.Repository, tree *object.Tree, subPath string, includeSensitive bool, policies ...syncPolicy) (map[string]transferFile, error) {
	var err error
	tree, err = repositorySubtree(tree, subPath)
	if err != nil {
		return nil, err
	}
	result := map[string]transferFile{}
	policy := defaultSyncPolicy()
	if len(policies) > 0 {
		policy = policies[0]
	}
	if policy.selectionEnabled && policy.selectNewCompose {
		if policy.selectedRoots == nil {
			policy.selectedRoots = make(map[string]struct{})
		}
		for _, relative := range discoverRepositoryComposeFiles(tree) {
			if _, known := policy.compose[relative]; !known {
				policy.compose[relative] = struct{}{}
				policy.selectedRoots[filepath.ToSlash(filepath.Dir(filepath.FromSlash(relative)))] = struct{}{}
			}
		}
	}
	if err := validateComposeOnlyCatalog(policy); err != nil {
		return nil, err
	}
	policy = policy.withComposeDirectoryIndex()
	ignoreRules, err := loadRepositoryTreeIgnoreRules(tree)
	if err != nil {
		return nil, err
	}
	var total int64
	var walk func(*object.Tree, string) (int, error)
	walk = func(current *object.Tree, parent string) (int, error) {
		observed := 0
		for _, entry := range current.Entries {
			if entry.Name == "" || entry.Name == "." || entry.Name == ".." || strings.ContainsAny(entry.Name, `/\\`) {
				return 0, fmt.Errorf("repository contains unsafe Git tree entry %q", entry.Name)
			}
			rel := path.Join(parent, entry.Name)
			if err := validateRelativePath(rel, false); err != nil {
				return 0, fmt.Errorf("repository contains unsafe Git tree path %q: %w", rel, err)
			}
			isDirectory := entry.Mode == filemode.Dir
			selected, traverse := policy.selectsPath(rel, isDirectory)
			if !selected {
				if isDirectory && traverse {
					subtree, err := current.Tree(entry.Name)
					if err != nil {
						return 0, err
					}
					childCount, err := walk(subtree, rel)
					if err != nil {
						return 0, err
					}
					observed += childCount
				}
				continue
			}
			if shouldSkipPath(rel, isDirectory) {
				continue
			}
			if policy.excludesPath(rel, isDirectory, ignoreRules) && !policy.explicitlyIncludes(rel, isDirectory) && !policy.protectsCompose(rel) && !policy.protectsProvision(rel) {
				if isDirectory && policy.containsCompose(rel) {
					subtree, err := current.Tree(entry.Name)
					if err != nil {
						return 0, err
					}
					childCount, err := walk(subtree, rel)
					if err != nil {
						return 0, err
					}
					observed += childCount
					continue
				}
				result[rel] = transferFile{path: rel, skipReason: "excluded", directory: isDirectory}
				continue
			}
			if isDirectory {
				// Keep Git-side Compose-only traversal symmetrical with the live
				// stack collector. An unrelated tree is outside the inventory, so
				// do not load its Git tree object or enumerate any of its entries.
				if policy.profile == syncProfileComposeOnly && !policy.traversesComposeOnlyDirectory(rel) {
					continue
				}
				subtree, err := current.Tree(entry.Name)
				if err != nil {
					return 0, err
				}
				childCount, err := walk(subtree, rel)
				if err != nil {
					return 0, err
				}
				observed += childCount
				if autoExcludeLargeDirectory(result, policy, parent, observed) {
					return observed, nil
				}
				continue
			}
			if entry.Mode == filemode.Symlink || entry.Mode == filemode.Submodule || !entry.Mode.IsFile() {
				continue
			}
			// Keep the Compose-only inventory genuinely bounded to its allow
			// list. This mirrors the live-stack collector and avoids loading Git
			// blobs which cannot participate in this synchronization.
			if policy.profile == syncProfileComposeOnly && !policy.includesFile(rel) && !policy.protectsProvision(rel) {
				continue
			}
			blob, err := repo.BlobObject(entry.Hash)
			if err != nil {
				return 0, err
			}
			size := blob.Size
			observed++
			if autoExcludeLargeDirectory(result, policy, parent, observed) {
				return observed, nil
			}
			if len(result)+1 > maxBindingFiles {
				return 0, stackInventoryLimitError(policy, rel, maxBindingFiles)
			}
			sensitive := isSensitivePath(rel)
			if sensitive && !includeSensitive {
				result[rel] = transferFile{path: rel, size: size, sensitive: true, skipReason: "sensitive"}
				continue
			}
			if size > maxBindingFileSize {
				log.Warn().Str("file", rel).Int64("size_bytes", size).Int64("limit_bytes", maxBindingFileSize).Msg("Git stack sync skipped oversized file")
				result[rel] = transferFile{path: rel, size: size, skipReason: "oversized"}
				continue
			}
			if !policy.includesFile(rel) && !policy.protectsProvision(rel) {
				result[rel] = transferFile{path: rel, size: size, skipReason: "type"}
				continue
			}
			if total+size > maxBindingTotalSize {
				return 0, fmt.Errorf("%s repository files exceed the %d MiB total limit while scanning %s (%d MiB accumulated); exclude its data directory", stackInventoryOwner(policy, rel), maxBindingTotalSize>>20, rel, (total+size)>>20)
			}
			if err := checkTransferLimit(len(result)+1, size, total+size); err != nil {
				return 0, fmt.Errorf("%s at %s: %w", stackInventoryOwner(policy, rel), rel, err)
			}
			total += size
			file := transferFile{path: rel, size: size, mode: gitFileMode(entry.Mode), sensitive: sensitive, open: gitBlobOpener(repo, entry.Hash)}
			file.sha, err = hashTransferFile(file)
			if err != nil {
				return 0, err
			}
			result[rel] = file
		}
		return observed, nil
	}
	_, err = walk(tree, "")
	return result, err
}

func discoverRepositoryComposeFiles(tree *object.Tree) []string {
	result := make([]string, 0)
	visited := 0
	var walk func(*object.Tree, string, int)
	walk = func(current *object.Tree, parent string, depth int) {
		if depth > 8 || visited >= 1000 || len(result) >= 500 {
			return
		}
		visited++
		for _, entry := range current.Entries {
			relative := path.Join(parent, entry.Name)
			if entry.Mode == filemode.Dir {
				subtree, err := current.Tree(entry.Name)
				if err == nil {
					walk(subtree, relative, depth+1)
				}
				continue
			}
			if isComposeDeploymentFile(relative) {
				result = append(result, relative)
			}
		}
	}
	walk(tree, "", 0)
	sort.Strings(result)
	return result
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

func policyFromBinding(binding StackBinding, repositories ...Repository) (syncPolicy, error) {
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
	policy := syncPolicy{profile: profile, includes: includeRules, excludes: excludeRules, compose: compose, repositorySubPath: binding.SubPath}
	if normalizedComposeSelectionMode(binding.ComposeSelectionMode) == composeSelectionSelected {
		policy.selectionEnabled = true
		policy.selectNewCompose = binding.AutoDeployEnabled && binding.AutoDeployNewStacks
		policy.selectedRoots = make(map[string]struct{})
		for _, relative := range selectedComposePaths(binding) {
			root := filepath.ToSlash(filepath.Dir(filepath.FromSlash(relative)))
			policy.selectedRoots[root] = struct{}{}
		}
	}
	if len(repositories) > 0 {
		policy.repositoryExcludes, err = rulesFromPatterns(splitPatternLines(repositories[0].ExcludePatterns))
		if err != nil {
			return syncPolicy{}, fmt.Errorf("invalid repository policy: %w", err)
		}
	}
	return policy, nil
}

func normalizedComposeSelectionMode(mode string) string {
	if mode == composeSelectionSelected {
		return composeSelectionSelected
	}
	return composeSelectionAll
}

func normalizeComposeSelection(availablePaths []string, mode string, requestedPaths []string, defaultMode string) (string, []string, error) {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = defaultMode
	}
	if mode != composeSelectionAll && mode != composeSelectionSelected {
		return "", nil, errors.New("Compose selection mode must be all or selected")
	}
	if mode == composeSelectionAll {
		return mode, nil, nil
	}
	available := make(map[string]struct{}, len(availablePaths))
	for _, raw := range availablePaths {
		relative := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(raw))))
		available[relative] = struct{}{}
	}
	selected := make([]string, 0, len(requestedPaths))
	seen := make(map[string]struct{}, len(requestedPaths))
	for _, raw := range requestedPaths {
		relative := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(raw))))
		if err := validateRelativePath(relative, false); err != nil {
			return "", nil, fmt.Errorf("invalid Compose selection %q: %w", raw, err)
		}
		if _, ok := available[relative]; !ok {
			return "", nil, fmt.Errorf("Compose file is no longer available in this folder link: %s", relative)
		}
		if _, ok := seen[relative]; ok {
			continue
		}
		seen[relative] = struct{}{}
		selected = append(selected, relative)
	}
	sort.Strings(selected)
	return mode, selected, nil
}

func selectedComposePaths(binding StackBinding) []string {
	if normalizedComposeSelectionMode(binding.ComposeSelectionMode) == composeSelectionAll {
		return splitPatternLines(binding.ComposePaths)
	}
	return splitPatternLines(binding.SelectedComposePaths)
}

// autoSyncComposePaths returns the persistent automatic polling policy. It is
// intentionally independent from the folder-link selection: a stack may stay
// linked for safe manual transfers while being excluded from background Git
// polling. Legacy links default to all selected stacks.
func autoSyncComposePaths(binding StackBinding) []string {
	selected := selectedComposePaths(binding)
	if normalizedComposeSelectionMode(binding.AutoSyncSelectionMode) == composeSelectionAll {
		return selected
	}
	allowed := make(map[string]struct{}, len(selected))
	for _, path := range selected {
		allowed[path] = struct{}{}
	}
	return keepCataloguedPaths(splitPatternLines(binding.AutoSyncComposePaths), allowed)
}

func (policy syncPolicy) selectsPath(relative string, directory bool) (selected, traverse bool) {
	if !policy.selectionEnabled {
		return true, true
	}
	relative = strings.Trim(filepath.ToSlash(relative), "/")
	// An explicit path inclusion is an operator decision and may extend a root
	// Compose selection into a supporting subdirectory. Basename-only rules are
	// deliberately excluded from traversal: searching every subtree for one
	// filename would reopen secrets, sockets and application-data directories.
	if policy.explicitlyIncludes(relative, directory) {
		return true, true
	}
	if directory && policy.explicitIncludeCanMatchBelow(relative) {
		traverse = true
	}
	for root := range policy.selectedRoots {
		root = strings.Trim(root, "/")
		if root == "." {
			// A Compose manifest at the binding root owns root files only. It
			// must not implicitly re-select every nested stack that the operator
			// explicitly deselected.
			if !directory && !strings.Contains(relative, "/") {
				return true, true
			}
			continue
		}
		if relative == root || strings.HasPrefix(relative, root+"/") {
			return true, true
		}
		if directory && strings.HasPrefix(root, relative+"/") {
			traverse = true
		}
	}
	return false, traverse
}

func normalizeBindingPolicy(input BindingPolicyInput) (string, []string, []string, error) {
	profile := strings.TrimSpace(input.Profile)
	if profile == "" {
		profile = syncProfileComposeConfig
	}
	if profile != syncProfileComposeOnly && profile != syncProfileComposeConfig && profile != syncProfileAllFiles {
		return "", nil, nil, errors.New("sync profile must be compose_only, compose_config or all_files")
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
		anchored := strings.HasPrefix(line, "/")
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
		if anchored {
			normalizedLine = "/" + normalizedLine
		}
		if directory {
			normalizedLine += "/"
		}
		normalized = append(normalized, normalizedLine)
		rules = append(rules, ignoreRule{pattern: line, directory: directory, basename: !anchored && !strings.Contains(line, "/")})
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
	if policy.explicitlyIncludes(relative, false) {
		return true
	}
	if policy.profile == syncProfileComposeOnly {
		if isComposePath(relative) {
			return policy.catalogsCompose(relative)
		}
		return matchesIgnoreRule(composeOnlyRules, relative, false) && policy.hasComposeInFileDirectory(relative)
	}
	if policy.profile == syncProfileAllFiles {
		return true
	}
	return matchesIgnoreRule(composeConfigRules, relative, false)
}

func composeOnlyCatalogError() error {
	return errors.New("no Compose stack is catalogued for this folder link; refresh the Compose catalog before synchronizing")
}

func validateComposeOnlyCatalog(policy syncPolicy) error {
	if policy.profile == syncProfileComposeOnly && len(policy.compose) == 0 {
		return composeOnlyCatalogError()
	}
	return nil
}

func (policy syncPolicy) catalogsCompose(relative string) bool {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative)))
	_, catalogued := policy.compose[clean]
	return catalogued
}

func (policy syncPolicy) hasComposeInFileDirectory(relative string) bool {
	directory := path.Dir(filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative))))
	for compose := range policy.compose {
		if path.Dir(filepath.ToSlash(compose)) == directory {
			return true
		}
	}
	return false
}

func (policy syncPolicy) explicitlyIncludes(relative string, directory bool) bool {
	return matchesIgnoreRule(policy.includes, relative, directory)
}

func (policy syncPolicy) excludesPath(relative string, directory bool, localRules []ignoreRule) bool {
	if matchesIgnoreRule(policy.excludes, relative, directory) || matchesIgnoreRule(localRules, relative, directory) {
		return true
	}
	repositoryRelative := relative
	if subPath := strings.Trim(policy.repositorySubPath, "/"); subPath != "" && subPath != "." {
		repositoryRelative = path.Join(subPath, relative)
	}
	return matchesIgnoreRule(policy.repositoryExcludes, repositoryRelative, directory)
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

func (policy syncPolicy) protectsProvision(relative string) bool {
	if !isProvisionControlPath(relative) {
		return false
	}
	directory := filepath.ToSlash(filepath.Dir(filepath.FromSlash(relative)))
	if len(policy.compose) == 0 {
		return true
	}
	for compose := range policy.compose {
		if filepath.ToSlash(filepath.Dir(filepath.FromSlash(compose))) == directory {
			return true
		}
	}
	return false
}

func (policy syncPolicy) containsCompose(directory string) bool {
	directory = strings.Trim(filepath.ToSlash(directory), "/")
	if policy.composeDirectories != nil {
		_, found := policy.composeDirectories[directory]
		return found
	}
	for compose := range policy.compose {
		if strings.HasPrefix(compose, directory+"/") {
			return true
		}
	}
	return false
}

func (policy syncPolicy) withComposeDirectoryIndex() syncPolicy {
	policy.composeDirectories = make(map[string]struct{}, len(policy.compose))
	for compose := range policy.compose {
		directory := path.Dir(filepath.ToSlash(compose))
		for directory != "." && directory != "" && directory != "/" {
			policy.composeDirectories[strings.Trim(directory, "/")] = struct{}{}
			directory = path.Dir(directory)
		}
	}
	return policy
}

// traversesComposeOnlyDirectory reports whether an allow-listed file can exist
// below directory. Built-in environment templates are intentionally scoped to
// directories that contain a known Compose manifest. Extra trees are visited
// only when an explicit include rule can target them.
func (policy syncPolicy) traversesComposeOnlyDirectory(directory string) bool {
	if policy.containsCompose(directory) {
		return true
	}
	directory = strings.Trim(filepath.ToSlash(directory), "/")
	return policy.explicitIncludeCanMatchBelow(directory)
}

func (policy syncPolicy) explicitIncludeCanMatchBelow(directory string) bool {
	for _, rule := range policy.includes {
		if !rule.basename && includeRuleCanMatchBelowDirectory(rule, directory) {
			return true
		}
	}
	return false
}

func includeRuleCanMatchBelowDirectory(rule ignoreRule, directory string) bool {
	patterns := strings.Split(strings.Trim(rule.pattern, "/"), "/")
	directories := strings.Split(strings.Trim(directory, "/"), "/")
	type state struct{ pattern, directory int }
	visited := map[state]bool{}
	var matches func(int, int) bool
	matches = func(patternIndex, directoryIndex int) bool {
		current := state{pattern: patternIndex, directory: directoryIndex}
		if visited[current] {
			return false
		}
		visited[current] = true
		if directoryIndex == len(directories) {
			// At least one pattern component must remain below this directory.
			return patternIndex < len(patterns)
		}
		if patternIndex == len(patterns) {
			return false
		}
		if patterns[patternIndex] == "**" {
			return matches(patternIndex+1, directoryIndex) || matches(patternIndex, directoryIndex+1)
		}
		matched, err := doublestar.Match(patterns[patternIndex], directories[directoryIndex])
		return err == nil && matched && matches(patternIndex+1, directoryIndex+1)
	}
	return matches(0, 0)
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

func loadRepositoryTreeIgnoreRules(tree *object.Tree) ([]ignoreRule, error) {
	file, err := tree.File(".dockmanignore")
	if err != nil {
		if errors.Is(err, object.ErrFileNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("open .dockmanignore from Git tree: %w", err)
	}
	reader, err := file.Reader()
	if err != nil {
		return nil, fmt.Errorf("open .dockmanignore from Git tree: %w", err)
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

func isProvisionControlPath(relative string) bool {
	switch strings.ToLower(filepath.Base(filepath.FromSlash(relative))) {
	case "provision.yml", "provision.yaml":
		return true
	default:
		return false
	}
}

func shouldSkipPath(path string, directory bool) bool {
	base := strings.ToLower(filepath.Base(path))
	return directory && (base == ".git" || base == ".dockman-backups" || strings.HasPrefix(base, ".dockman-provision-staging-"))
}

func isSensitivePath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	ext := strings.ToLower(filepath.Ext(base))
	if ((base == ".env" || strings.HasPrefix(base, ".env.")) && !isEnvironmentTemplate(base)) || base == "id_rsa" || base == "id_ed25519" {
		return true
	}
	if ext == ".pem" || ext == ".key" || ext == ".p12" || ext == ".pfx" {
		return true
	}
	return strings.Contains(base, "secret") || strings.Contains(base, "credential")
}

// Environment templates document variable names and safe example values. Keep
// the sensitive-file guard for real environment files, but allow conventional
// templates to be selected explicitly by a synchronization policy.
func isEnvironmentTemplate(base string) bool {
	if !strings.HasPrefix(base, ".env.") {
		return false
	}
	suffix := base[strings.LastIndexByte(base, '.')+1:]
	switch suffix {
	case "example", "sample", "template", "dist":
		return true
	default:
		return false
	}
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
		if isProvisionControlPath(path) {
			if direction == "stack_to_repository" {
				continue
			}
			entry := PreviewEntry{Path: path, Status: "add", SourceSHA: src.sha, Size: src.size}
			if previous, tracked := baseline[path]; tracked {
				if previous == src.sha {
					preview.Unchanged++
					continue
				}
				entry.Status = "modify"
			}
			preview.Entries = append(preview.Entries, entry)
			preview.Changed++
			continue
		}
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
		if !exists && direction == "repository_to_stack" {
			dst, exists = blockedTargetDirectory(target, path)
		}
		entry := PreviewEntry{Path: path, Status: "add", SourceSHA: src.sha, Size: src.size, Sensitive: src.sensitive}
		if exists && dst.open == nil && (dst.skipReason == "permission" || dst.skipReason == "large_directory") {
			entry.Status = "skipped_" + dst.skipReason
			entry.TargetSHA = dst.sha
			entry.Size = dst.size
			entry.Directory = dst.directory
			preview.Entries = append(preview.Entries, entry)
			preview.Skipped++
			continue
		}
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
		} else if direction == "repository_to_stack" {
			if baseSHA, tracked := baseline[path]; tracked && baseSHA == src.sha {
				entry.ConflictKind = "destination_deleted"
				preview.LocalDeletions++
			}
		}
		preview.Entries = append(preview.Entries, entry)
		preview.Changed++
	}
	// Stack -> Git must expose tracked files that disappeared locally. They
	// remain on Git until an explicit stack-level decision is made; a regular
	// export can therefore never turn a local deletion into a remote deletion.
	if direction == "stack_to_repository" {
		for path, dst := range target {
			if _, exists := source[path]; exists || dst.open == nil || isProvisionControlPath(path) {
				continue
			}
			baseSHA, tracked := baseline[path]
			if !tracked {
				continue
			}
			entry := PreviewEntry{Path: path, Status: "deleted_locally", TargetSHA: dst.sha, Size: dst.size, Sensitive: dst.sensitive}
			if dst.sha != baseSHA {
				entry.Status = "conflict"
				entry.ConflictKind = "source_deleted_destination_changed"
				preview.Conflicts++
			}
			preview.Entries = append(preview.Entries, entry)
			preview.Changed++
			preview.LocalDeletions++
		}
		sort.SliceStable(preview.Entries, func(i, j int) bool { return preview.Entries[i].Path < preview.Entries[j].Path })
	}
	// A Git -> Dockman preview must also expose files that existed at the
	// common baseline but disappeared from Git. They are preserved locally by
	// default and are never fed to the regular transfer writer.
	if direction == "repository_to_stack" {
		preservedPaths := make([]string, 0)
		for path, dst := range target {
			if _, exists := source[path]; exists || dst.open == nil || isProvisionControlPath(path) {
				continue
			}
			baseSHA, tracked := baseline[path]
			if !tracked {
				continue
			}
			entry := PreviewEntry{Path: path, Status: "deleted_on_git", TargetSHA: dst.sha, Size: dst.size, Sensitive: dst.sensitive}
			if dst.sha != baseSHA {
				entry.Status = "conflict"
				entry.ConflictKind = "source_deleted_destination_changed"
				preview.Conflicts++
			}
			preservedPaths = append(preservedPaths, path)
			preview.Entries = append(preview.Entries, entry)
			preview.Changed++
			preview.Preserved++
		}
		if len(preservedPaths) > 0 {
			sort.SliceStable(preview.Entries, func(i, j int) bool { return preview.Entries[i].Path < preview.Entries[j].Path })
		}
		for path := range baseline {
			if !isProvisionControlPath(path) {
				continue
			}
			if _, exists := source[path]; exists {
				continue
			}
			preview.Entries = append(preview.Entries, PreviewEntry{Path: path, Status: "remove_control"})
			preview.Changed++
		}
		sort.SliceStable(preview.Entries, func(i, j int) bool { return preview.Entries[i].Path < preview.Entries[j].Path })
	}
	preview.PreviewToken = previewToken(preview)
	return preview
}

func blockedTargetDirectory(target map[string]transferFile, relative string) (transferFile, bool) {
	current := filepath.ToSlash(filepath.Dir(filepath.FromSlash(relative)))
	for current != "." && current != "" {
		if blocker, exists := target[current]; exists && blocker.directory && blocker.open == nil && (blocker.skipReason == "permission" || blocker.skipReason == "large_directory") {
			return blocker, true
		}
		current = filepath.ToSlash(filepath.Dir(filepath.FromSlash(current)))
	}
	return transferFile{}, false
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

func baselineFromSourcePreservingControl(source map[string]transferFile, current map[string]string) map[string]string {
	result := baselineFromSource(source)
	for path, sha := range current {
		if isProvisionControlPath(path) {
			result[path] = sha
		}
	}
	return result
}

func splitProvisionControlFiles(files map[string]transferFile) (map[string]transferFile, map[string]transferFile) {
	stackFiles := make(map[string]transferFile, len(files))
	controlFiles := make(map[string]transferFile)
	for path, file := range files {
		if isProvisionControlPath(path) {
			controlFiles[path] = file
			continue
		}
		stackFiles[path] = file
	}
	return stackFiles, controlFiles
}

func previewToken(preview TransferPreview) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\x00", preview.BindingID, preview.Direction, preview.DeletionMode)
	for _, entry := range preview.Entries {
		// Skipped entries are deliberately non-transferable and require no
		// user decision. Mutable logs, caches, excluded files, or unreadable
		// data must therefore not invalidate confirmation of an otherwise
		// unchanged Compose transfer. If such a path becomes transferable its
		// status changes and it is included in the next token automatically.
		if strings.HasPrefix(entry.Status, "skipped_") {
			continue
		}
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
			if errors.Is(err, fs.ErrPermission) {
				if fallbackErr := writeExistingStackFile(targetFS, destination, file, err); fallbackErr != nil {
					return fallbackErr
				}
				continue
			}
			return fmt.Errorf("create temporary replacement for %s: %w", file.path, err)
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
			if errors.Is(err, fs.ErrPermission) {
				if fallbackErr := writeExistingStackFile(targetFS, destination, file, err); fallbackErr != nil {
					return fallbackErr
				}
				continue
			}
			return fmt.Errorf("replace %s: %w", file.path, err)
		}
	}
	return nil
}

// writeExistingStackFile is the narrow compatibility path for mounts where an
// existing file is writable but its parent directory does not allow creating
// or renaming entries. The regular Git import path remains atomic. Imports
// create a safety backup before reaching this function, and this fallback only
// applies to an existing regular file: it never creates a missing path or
// follows a symlink.
func writeExistingStackFile(targetFS filesystem.FileSystem, destination string, file transferFile, atomicErr error) error {
	info, err := targetFS.Lstat(destination)
	if err != nil {
		return fmt.Errorf("cannot replace %s atomically and no writable regular file is available for an in-place update: %w", file.path, atomicErr)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("cannot replace %s atomically and refusing an in-place update of a non-regular file: %w", file.path, atomicErr)
	}

	handle, err := targetFS.OpenFile(destination, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return fmt.Errorf("cannot replace %s atomically or update the existing file directly: %w", file.path, errors.Join(atomicErr, err))
	}
	writeErr := streamTransferFile(file, handle)
	closeErr := handle.Close()
	if writeErr != nil {
		return fmt.Errorf("update existing file %s after atomic replacement was denied: %w", file.path, writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close existing file %s after atomic replacement was denied: %w", file.path, closeErr)
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
		if entry.Status == "add" || entry.Status == "modify" || entry.Status == "conflict" || entry.Status == "remove_control" {
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
		if entry.Status != "add" && entry.Status != "modify" && entry.Status != "remove_control" {
			if entry.Status != "conflict" {
				continue
			}
			if _, approved := resolved[entry.Path]; !approved {
				continue
			}
		}
		if entry.Status == "remove_control" {
			selected[entry.Path] = transferFile{path: entry.Path}
		} else if file, exists := source[entry.Path]; exists && file.open != nil {
			selected[entry.Path] = file
		}
	}
	return selected, len(conflicts) - len(resolved), nil
}

func previewPathsWithStatus(preview TransferPreview, status string) []string {
	paths := make([]string, 0)
	for _, entry := range preview.Entries {
		if entry.Status == status {
			paths = append(paths, entry.Path)
		}
	}
	return uniqueSortedStrings(paths)
}

func previewProtectedLocalPaths(preview TransferPreview) []string {
	return uniqueSortedStrings(append(
		previewPathsWithStatus(preview, "skipped_permission"),
		previewPathsWithStatus(preview, "skipped_large_directory")...,
	))
}

func composePathsForFiles(composePaths, filePaths []string) []string {
	result := make([]string, 0)
	for _, filePath := range filePaths {
		result = append(result, composePathsForFile(composePaths, filePath)...)
	}
	return uniqueSortedStrings(result)
}

func baselineAfterTransfer(current map[string]string, source, target, selected map[string]transferFile) map[string]string {
	result := make(map[string]string, len(current)+len(selected))
	for path, sha := range current {
		result[path] = sha
	}
	for path, file := range selected {
		if isProvisionControlPath(path) && file.open == nil {
			delete(result, path)
		}
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
	errorsByPath, found := composeFileErrors(files)
	return firstComposeValidationError(errorsByPath, found)
}

func composeFileErrors(files map[string]transferFile) (map[string]string, bool) {
	errorsByPath := make(map[string]string)
	found := false
	for path, file := range files {
		if isComposePath(path) {
			found = true
			if file.open == nil {
				errorsByPath[path] = fmt.Sprintf("compose file %s was skipped (%s); Compose files cannot be excluded from synchronization", path, file.skipReason)
				continue
			}
			reader, err := file.open()
			if err != nil {
				errorsByPath[path] = fmt.Sprintf("open compose YAML %s: %v", path, err)
				continue
			}
			var value any
			decodeErr := yaml.NewDecoder(io.LimitReader(reader, file.size+1)).Decode(&value)
			closeErr := reader.Close()
			if decodeErr != nil {
				errorsByPath[path] = fmt.Sprintf("invalid compose YAML %s: %v", path, decodeErr)
				continue
			}
			if closeErr != nil {
				errorsByPath[path] = fmt.Sprintf("close compose YAML %s: %v", path, closeErr)
				continue
			}
			if value == nil {
				errorsByPath[path] = fmt.Sprintf("invalid compose YAML %s: document is empty", path)
			}
		}
	}
	return errorsByPath, found
}

func firstComposeValidationError(errorsByPath map[string]string, found bool) error {
	if !found {
		return errors.New("repository path contains no compose file")
	}
	paths := make([]string, 0, len(errorsByPath))
	for path := range errorsByPath {
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return nil
	}
	sort.Strings(paths)
	return errors.New(errorsByPath[paths[0]])
}

func excludeInvalidComposeStacks(selected, allFiles map[string]transferFile, composeErrors map[string]string) (map[string]transferFile, []string) {
	allCompose := make([]string, 0)
	for path := range allFiles {
		if isComposePath(path) {
			allCompose = append(allCompose, path)
		}
	}
	sort.Strings(allCompose)
	blocked := make(map[string]struct{})
	filtered := make(map[string]transferFile, len(selected))
	for path, file := range selected {
		invalidOwners := false
		for _, composePath := range composePathsForFile(allCompose, path) {
			if _, invalid := composeErrors[composePath]; invalid {
				blocked[composePath] = struct{}{}
				invalidOwners = true
			}
		}
		if !invalidOwners {
			filtered[path] = file
		}
	}
	blockedPaths := make([]string, 0, len(blocked))
	for composePath := range blocked {
		blockedPaths = append(blockedPaths, composePath)
	}
	sort.Strings(blockedPaths)
	return filtered, blockedPaths
}

func excludeStringValues(values, excluded []string) []string {
	excludedSet := make(map[string]struct{}, len(excluded))
	for _, value := range excluded {
		excludedSet[value] = struct{}{}
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, skip := excludedSet[value]; !skip {
			result = append(result, value)
		}
	}
	return result
}

func hasTrackedDeletedCompose(source, target map[string]transferFile, baseline map[string]string, composePaths []string) bool {
	for _, composePath := range composePaths {
		if sourceFile, exists := source[composePath]; exists && sourceFile.open != nil {
			return false
		}
		targetFile, local := target[composePath]
		_, tracked := baseline[composePath]
		if local && targetFile.open != nil && tracked {
			return true
		}
	}
	return false
}

func (s *Service) removeBindingBackups(bindingID string) error {
	if s.backupRoot == "" {
		return s.store.DeleteBindingBackups(bindingID)
	}
	if _, err := uuid.Parse(bindingID); err != nil {
		return errors.New("invalid binding identifier for backup cleanup")
	}
	info, err := os.Lstat(s.backupRoot)
	if errors.Is(err, os.ErrNotExist) {
		return s.store.DeleteBindingBackups(bindingID)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Git stack backup root must be a real directory")
	}
	backupFS, err := os.OpenRoot(s.backupRoot)
	if err != nil {
		return fmt.Errorf("open backup directory: %w", err)
	}
	defer backupFS.Close()
	if err := backupFS.RemoveAll(bindingID); err != nil {
		return fmt.Errorf("remove binding backups: %w", err)
	}
	if err := backupFS.RemoveAll(filepath.Join("archives", bindingID)); err != nil {
		return fmt.Errorf("remove binding orphan archives: %w", err)
	}
	return s.store.DeleteBindingBackups(bindingID)
}
