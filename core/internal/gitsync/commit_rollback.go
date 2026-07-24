package gitsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	gitclient "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const maxRollbackCommitWalk = 10_000

type BindingCommitView struct {
	SHA        string    `json:"sha"`
	ShortSHA   string    `json:"shortSha"`
	Message    string    `json:"message"`
	AuthorName string    `json:"authorName"`
	AuthorMail string    `json:"authorEmail,omitempty"`
	AuthoredAt time.Time `json:"authoredAt"`
}

type CommitRollbackInput struct {
	CommitSHA             string   `json:"commitSha"`
	ComposePaths          []string `json:"composePaths"`
	SelectedPaths         []string `json:"selectedPaths"`
	IncludeSensitive      bool     `json:"includeSensitive"`
	SensitiveConfirmation string   `json:"sensitiveConfirmation"`
	PreviewToken          string   `json:"previewToken"`
}

type CommitRollbackEntry struct {
	Path        string `json:"path"`
	ComposePath string `json:"composePath"`
	Action      string `json:"action"`
	CurrentSHA  string `json:"currentSha,omitempty"`
	TargetSHA   string `json:"targetSha,omitempty"`
	Size        int64  `json:"size,omitempty"`
	Sensitive   bool   `json:"sensitive,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

type CommitRollbackPreview struct {
	Commit              BindingCommitView     `json:"commit"`
	ComposePaths        []string              `json:"composePaths"`
	Entries             []CommitRollbackEntry `json:"entries"`
	Changed             int                   `json:"changed"`
	Restores            int                   `json:"restores"`
	Removals            int                   `json:"removals"`
	Skipped             int                   `json:"skipped"`
	MissingComposePaths []string              `json:"missingComposePaths,omitempty"`
	ComposeErrors       map[string]string     `json:"composeErrors,omitempty"`
	Token               string                `json:"token"`
}

type CommitRollbackResult struct {
	CommitSHA      string   `json:"commitSha"`
	SafetyBackupID string   `json:"safetyBackupId"`
	RestoredPaths  []string `json:"restoredPaths"`
	PausedStacks   []string `json:"pausedStacks"`
	Message        string   `json:"message"`
}

func (s *Service) ListBindingCommits(bindingID string, limit int) ([]BindingCommitView, error) {
	binding, err := s.store.GetBinding(bindingID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	lock := s.repositoryLock(binding.RepositoryUUID)
	lock.Lock()
	defer lock.Unlock()
	repository, err := s.store.GetRepository(binding.RepositoryUUID)
	if err != nil {
		return nil, err
	}
	repo, err := s.openRepository(repository)
	if err != nil {
		return nil, err
	}
	reference, err := repo.Reference(plumbing.NewBranchReferenceName(repository.DefaultBranch), true)
	if err != nil {
		return nil, fmt.Errorf("resolve repository branch: %w", err)
	}
	iterator, err := repo.Log(&gitclient.LogOptions{From: reference.Hash(), PathFilter: rollbackPathFilter(binding.SubPath)})
	if err != nil {
		return nil, err
	}
	defer iterator.Close()
	result := make([]BindingCommitView, 0, limit)
	for len(result) < limit {
		commit, nextErr := iterator.Next()
		if errors.Is(nextErr, io.EOF) || errors.Is(nextErr, os.ErrNotExist) || errors.Is(nextErr, plumbing.ErrObjectNotFound) {
			break
		}
		if nextErr != nil {
			return nil, nextErr
		}
		result = append(result, bindingCommitView(commit.Hash.String(), commit.Message, commit.Author.Name, commit.Author.Email, commit.Author.When))
	}
	return result, nil
}

func rollbackPathFilter(subPath string) func(string) bool {
	root := strings.Trim(filepath.ToSlash(filepath.Clean(filepath.FromSlash(subPath))), "/")
	if root == "." {
		root = ""
	}
	return func(candidate string) bool {
		candidate = strings.Trim(filepath.ToSlash(candidate), "/")
		return root == "" || candidate == root || strings.HasPrefix(candidate, root+"/")
	}
}

func bindingCommitView(sha, message, author, mail string, authoredAt time.Time) BindingCommitView {
	message = strings.TrimSpace(strings.SplitN(message, "\n", 2)[0])
	if len(message) > 300 {
		message = message[:300]
	}
	short := sha
	if len(short) > 12 {
		short = short[:12]
	}
	return BindingCommitView{SHA: sha, ShortSHA: short, Message: message, AuthorName: author, AuthorMail: mail, AuthoredAt: authoredAt}
}

func (s *Service) PreviewCommitRollback(bindingID string, input CommitRollbackInput) (CommitRollbackPreview, error) {
	if err := validateSensitiveOptIn(TransferInput{IncludeSensitive: input.IncludeSensitive, SensitiveConfirmation: input.SensitiveConfirmation}); err != nil {
		return CommitRollbackPreview{}, err
	}
	binding, err := s.store.GetBinding(bindingID)
	if err != nil {
		return CommitRollbackPreview{}, err
	}
	lock := s.repositoryLock(binding.RepositoryUUID)
	lock.Lock()
	defer lock.Unlock()
	return s.previewCommitRollbackLocked(binding, input)
}

func (s *Service) previewCommitRollbackLocked(binding StackBinding, input CommitRollbackInput) (CommitRollbackPreview, error) {
	preview, _, _, err := s.rollbackTreesLocked(binding, input)
	return preview, err
}

func reachableBindingCommit(repo *gitclient.Repository, branch, requested string) (*object.Commit, error) {
	if len(requested) != 40 {
		return nil, errors.New("a complete 40-character commit SHA is required")
	}
	if _, err := hex.DecodeString(requested); err != nil {
		return nil, errors.New("invalid commit SHA")
	}
	reference, err := repo.Reference(plumbing.NewBranchReferenceName(branch), true)
	if err != nil {
		return nil, err
	}
	iterator, err := repo.Log(&gitclient.LogOptions{From: reference.Hash()})
	if err != nil {
		return nil, err
	}
	defer iterator.Close()
	for walked := 0; walked < maxRollbackCommitWalk; walked++ {
		commit, nextErr := iterator.Next()
		if nextErr != nil {
			break
		}
		if commit.Hash.String() == requested {
			return commit, nil
		}
	}
	return nil, errors.New("commit is not reachable from the configured repository branch")
}

func rollbackComposeSelection(binding StackBinding, requested []string) ([]string, error) {
	available := selectedComposePaths(binding)
	if len(requested) == 0 {
		if len(available) == 0 {
			return nil, errors.New("this folder link has no synchronized stack selected")
		}
		return available, nil
	}
	if len(requested) > len(available) {
		return nil, errors.New("too many rollback stack selections")
	}
	result := make([]string, 0, len(requested))
	for _, candidate := range requested {
		candidate = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(candidate))))
		if err := validateRelativePath(candidate, false); err != nil || !stringInSlice(candidate, available) {
			return nil, fmt.Errorf("stack %q is not selected for synchronization", candidate)
		}
		result = append(result, candidate)
	}
	return uniqueSortedStrings(result), nil
}

func buildCommitRollbackPreview(commit BindingCommitView, composePaths []string, target, current map[string]transferFile) CommitRollbackPreview {
	all := make(map[string]struct{}, len(target)+len(current))
	for path := range target {
		all[path] = struct{}{}
	}
	for path := range current {
		all[path] = struct{}{}
	}
	paths := make([]string, 0, len(all))
	for path := range all {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	preview := CommitRollbackPreview{Commit: commit, ComposePaths: composePaths}
	for _, path := range paths {
		owners := composePathsForFile(composePaths, path)
		if len(owners) == 0 {
			continue
		}
		targetFile, targetExists := target[path]
		currentFile, currentExists := current[path]
		entry := CommitRollbackEntry{Path: path, ComposePath: owners[0], Sensitive: targetFile.sensitive || currentFile.sensitive}
		if targetExists {
			entry.TargetSHA, entry.Size = targetFile.sha, targetFile.size
		}
		if currentExists {
			entry.CurrentSHA = currentFile.sha
			if !targetExists {
				entry.Size = currentFile.size
			}
		}
		switch {
		case targetExists && targetFile.open == nil:
			entry.Action, entry.Reason = "skipped", "target commit file is protected: "+targetFile.skipReason
		case currentExists && currentFile.open == nil:
			entry.Action, entry.Reason = "skipped", "current file is protected: "+currentFile.skipReason
		case targetExists && currentExists && targetFile.sha == currentFile.sha:
			entry.Action = "noop"
		case targetExists:
			entry.Action = "restore"
			preview.Changed++
			preview.Restores++
		case currentExists:
			entry.Action = "remove"
			preview.Changed++
			preview.Removals++
		default:
			continue
		}
		if entry.Action == "skipped" {
			preview.Skipped++
		}
		preview.Entries = append(preview.Entries, entry)
	}
	return preview
}

func commitRollbackToken(preview CommitRollbackPreview) string {
	raw, _ := json.Marshal(struct {
		Commit  string                `json:"commit"`
		Stacks  []string              `json:"stacks"`
		Entries []CommitRollbackEntry `json:"entries"`
	}{preview.Commit.SHA, preview.ComposePaths, preview.Entries})
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func (s *Service) CompareCommitRollbackFile(bindingID string, input CommitRollbackInput, path string) (FileComparison, error) {
	path = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(path))))
	if err := validateRelativePath(path, false); err != nil {
		return FileComparison{}, err
	}
	binding, err := s.store.GetBinding(bindingID)
	if err != nil {
		return FileComparison{}, err
	}
	lock := s.repositoryLock(binding.RepositoryUUID)
	lock.Lock()
	defer lock.Unlock()
	preview, target, current, err := s.rollbackTreesLocked(binding, input)
	if err != nil {
		return FileComparison{}, err
	}
	allowed := false
	for _, entry := range preview.Entries {
		if entry.Path == path && entry.Action == "restore" {
			allowed = true
			break
		}
	}
	if !allowed {
		return FileComparison{}, errors.New("only changed files available on both sides can be compared")
	}
	targetFile, targetOK := target[path]
	currentFile, currentOK := current[path]
	if !targetOK || !currentOK || targetFile.open == nil || currentFile.open == nil {
		return FileComparison{}, errors.New("both file versions must be available for comparison")
	}
	dockman, dockmanComparable, dockmanReason, err := comparisonSide(currentFile)
	if err != nil {
		return FileComparison{}, err
	}
	git, gitComparable, gitReason, err := comparisonSide(targetFile)
	if err != nil {
		return FileComparison{}, err
	}
	result := FileComparison{Path: path, Dockman: dockman, Git: git, Comparable: dockmanComparable && gitComparable}
	if !result.Comparable {
		result.Reason = strings.TrimSpace(strings.Join([]string{dockmanReason, gitReason}, " "))
	}
	return result, nil
}

func (s *Service) rollbackTreesLocked(binding StackBinding, input CommitRollbackInput) (CommitRollbackPreview, map[string]transferFile, map[string]transferFile, error) {
	if err := validateSensitiveOptIn(TransferInput{IncludeSensitive: input.IncludeSensitive, SensitiveConfirmation: input.SensitiveConfirmation}); err != nil {
		return CommitRollbackPreview{}, nil, nil, err
	}
	repository, err := s.store.GetRepository(binding.RepositoryUUID)
	if err != nil {
		return CommitRollbackPreview{}, nil, nil, err
	}
	repo, err := s.openRepository(repository)
	if err != nil {
		return CommitRollbackPreview{}, nil, nil, err
	}
	commit, err := reachableBindingCommit(repo, repository.DefaultBranch, input.CommitSHA)
	if err != nil {
		return CommitRollbackPreview{}, nil, nil, err
	}
	composePaths, err := rollbackComposeSelection(binding, input.ComposePaths)
	if err != nil {
		return CommitRollbackPreview{}, nil, nil, err
	}
	rollbackBinding := binding
	rollbackBinding.ComposeSelectionMode = composeSelectionSelected
	rollbackBinding.SelectedComposePaths = strings.Join(composePaths, "\n")
	rollbackBinding.AutoDeployEnabled = false
	rollbackBinding.AutoDeployNewStacks = false
	policy, err := policyFromBinding(rollbackBinding, repository)
	if err != nil {
		return CommitRollbackPreview{}, nil, nil, err
	}
	target, err := collectRepositoryFilesAtCommit(repo, commit.Hash, binding.SubPath, input.IncludeSensitive, policy)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return CommitRollbackPreview{}, nil, nil, fmt.Errorf("read target commit files: %w", err)
	}
	targetFS, targetRoot, err := s.resolveBindingStack(binding)
	if err != nil {
		return CommitRollbackPreview{}, nil, nil, err
	}
	current, err := collectStackFiles(targetFS, targetRoot, input.IncludeSensitive, policy)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return CommitRollbackPreview{}, nil, nil, fmt.Errorf("read current stack files: %w", err)
	}
	if target == nil {
		target = map[string]transferFile{}
	}
	if current == nil {
		current = map[string]transferFile{}
	}
	preview := buildCommitRollbackPreview(bindingCommitView(commit.Hash.String(), commit.Message, commit.Author.Name, commit.Author.Email, commit.Author.When), composePaths, target, current)
	preview.ComposeErrors, _ = composeFileErrors(target)
	for _, composePath := range composePaths {
		file, exists := target[composePath]
		if !exists || file.open == nil {
			preview.MissingComposePaths = append(preview.MissingComposePaths, composePath)
		}
	}
	preview.Token = commitRollbackToken(preview)
	return preview, target, current, nil
}

func (s *Service) ApplyCommitRollback(ctx context.Context, bindingID string, input CommitRollbackInput) (CommitRollbackResult, error) {
	_ = ctx
	binding, err := s.store.GetBinding(bindingID)
	if err != nil {
		return CommitRollbackResult{}, err
	}
	automationLock := s.repositoryLock("automation:" + bindingID)
	if !automationLock.TryLock() {
		return CommitRollbackResult{}, errors.New("automatic synchronization is currently running; retry when it finishes")
	}
	defer automationLock.Unlock()
	repositoryLock := s.repositoryLock(binding.RepositoryUUID)
	if !repositoryLock.TryLock() {
		return CommitRollbackResult{}, errors.New("a Git operation is currently running; retry when it finishes")
	}
	defer repositoryLock.Unlock()
	result, applyErr := s.applyCommitRollbackLocked(binding, input)
	activity := ActivityRecord{RepositoryID: binding.RepositoryUUID, BindingID: binding.UUID, Type: "commit_rollback", Trigger: "manual", CommitSHA: input.CommitSHA, BackupID: result.SafetyBackupID,
		Details: ActivityDetails{Action: "restore_commit", Paths: result.RestoredPaths, Message: result.Message}}
	if applyErr != nil {
		activity.State, activity.Error = "failed", safeGitError(applyErr)
	}
	s.recordActivity(activity)
	return result, applyErr
}

func (s *Service) applyCommitRollbackLocked(binding StackBinding, input CommitRollbackInput) (CommitRollbackResult, error) {
	preview, target, current, err := s.rollbackTreesLocked(binding, input)
	if err != nil {
		return CommitRollbackResult{}, err
	}
	if input.PreviewToken == "" || input.PreviewToken != preview.Token {
		return CommitRollbackResult{}, errors.New("commit rollback preview changed; review it again")
	}
	if len(preview.ComposeErrors) > 0 {
		return CommitRollbackResult{}, errors.New(firstMapValue(preview.ComposeErrors))
	}
	selected := make(map[string]struct{})
	if len(input.SelectedPaths) == 0 {
		for _, entry := range preview.Entries {
			if entry.Action == "restore" || entry.Action == "remove" {
				selected[entry.Path] = struct{}{}
			}
		}
	} else {
		for _, path := range input.SelectedPaths {
			path = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(path))))
			if err := validateRelativePath(path, false); err != nil {
				return CommitRollbackResult{}, err
			}
			selected[path] = struct{}{}
		}
	}
	actions := make(map[string]CommitRollbackEntry)
	for _, entry := range preview.Entries {
		if entry.Action == "restore" || entry.Action == "remove" {
			actions[entry.Path] = entry
		}
	}
	for path := range selected {
		if _, ok := actions[path]; !ok {
			return CommitRollbackResult{}, fmt.Errorf("%s is no longer safely rollbackable", path)
		}
	}
	if len(selected) == 0 {
		return CommitRollbackResult{}, errors.New("no rollback file was selected")
	}
	affected := make(map[string]struct{})
	for path := range selected {
		affected[actions[path].ComposePath] = struct{}{}
	}
	affectedStacks := make([]string, 0, len(affected))
	for path := range affected {
		affectedStacks = append(affectedStacks, path)
	}
	sort.Strings(affectedStacks)
	if s.stackHasAnyDirtyEditor(binding, affectedStacks) {
		return CommitRollbackResult{}, errors.New("commit rollback refused while an affected stack has an unsaved editor")
	}
	desired, currentSelected := make(map[string]transferFile), make(map[string]transferFile)
	for path := range selected {
		if actions[path].Action == "restore" {
			desired[path] = target[path]
		} else {
			desired[path] = transferFile{path: path}
		}
		if file, ok := current[path]; ok {
			currentSelected[path] = file
		}
	}
	backupID, err := s.backupChangedFiles(binding, desired, currentSelected, "pre_commit_rollback", preview.Commit.SHA)
	if err != nil {
		return CommitRollbackResult{}, fmt.Errorf("create rollback safety backup: %w", err)
	}
	// Pause first: once local files start changing, no automatic import may race
	// with or silently undo the explicit rollback decision.
	for _, composePath := range affectedStacks {
		if err := s.store.SetGitStackPause(binding.UUID, composePath, true); err != nil {
			return CommitRollbackResult{SafetyBackupID: backupID}, fmt.Errorf("pause stack automation before rollback: %w", err)
		}
	}
	targetFS, targetRoot, err := s.resolveBindingStack(binding)
	if err != nil {
		return CommitRollbackResult{}, err
	}
	if err := writeStackFiles(targetFS, targetRoot, desired); err != nil {
		return CommitRollbackResult{SafetyBackupID: backupID}, err
	}
	for path := range selected {
		if actions[path].Action != "remove" {
			continue
		}
		if err := targetFS.RemoveAll(targetFS.Join(targetRoot, filepath.FromSlash(path))); err != nil {
			return CommitRollbackResult{SafetyBackupID: backupID}, err
		}
	}
	paths := make([]string, 0, len(selected))
	for path := range selected {
		paths = append(paths, path)
		if s.fileChangeNotify != nil {
			s.fileChangeNotify(binding.Host, filepath.ToSlash(filepath.Join(binding.StackPath, path)))
		}
	}
	sort.Strings(paths)
	now := time.Now().UTC()
	for _, composePath := range affectedStacks {
		state := stackSyncLocalChanges
		if !bindingComposeExistsLocally(s, binding, composePath) {
			state = stackSyncLocalDeleted
		}
		if err := s.store.UpdateGitStackStatuses(binding.UUID, []string{composePath}, map[string]any{"state": state, "error_message": "Local rollback waiting; review before pushing or importing", "conflict_count": 0, "last_checked_at": &now}); err != nil {
			return CommitRollbackResult{CommitSHA: preview.Commit.SHA, SafetyBackupID: backupID, RestoredPaths: paths, PausedStacks: affectedStacks}, fmt.Errorf("record rollback stack state: %w", err)
		}
	}
	message := fmt.Sprintf("%d file(s) restored locally from commit %s; %d affected stack(s) paused; no Compose or Docker action was run", len(paths), preview.Commit.ShortSHA, len(affectedStacks))
	return CommitRollbackResult{CommitSHA: preview.Commit.SHA, SafetyBackupID: backupID, RestoredPaths: paths, PausedStacks: affectedStacks, Message: message}, nil
}

func firstMapValue(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return "Compose validation failed"
	}
	return values[keys[0]]
}
