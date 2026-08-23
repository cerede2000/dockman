package gitsync

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/RA341/dockman/internal/host/filesystem"
	"github.com/google/uuid"
)

type OrphanActionInput struct {
	Action       string `json:"action"`
	Confirmation string `json:"confirmation"`
}

type OrphanActionResult struct {
	Action      string `json:"action"`
	ComposePath string `json:"composePath"`
	Backup      string `json:"backup,omitempty"`
	Message     string `json:"message"`
}

// ResolveGitOrphan applies an explicit decision to a whole stack directory
// that existed at the synchronization baseline and has since disappeared
// completely from Git. It never invokes Compose or removes Docker volumes.
func (s *Service) ResolveGitOrphan(ctx context.Context, bindingID, composePath string, input OrphanActionInput) (OrphanActionResult, error) {
	composePath = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(composePath))))
	if err := validateRelativePath(composePath, false); err != nil {
		return OrphanActionResult{}, fmt.Errorf("invalid Compose path: %w", err)
	}
	action := strings.ToLower(strings.TrimSpace(input.Action))
	if action != "restore" && action != "archive" && action != "delete" {
		return OrphanActionResult{}, errors.New("orphan action must be restore, archive, or delete")
	}
	if (action == "archive" || action == "delete") && input.Confirmation != typedConfirmationText {
		return OrphanActionResult{}, fmt.Errorf("type %q to confirm the local stack removal", typedConfirmationText)
	}

	automationLock := s.repositoryLock("automation:" + bindingID)
	if !waitForLock(automationLock, decisionLockBudget) {
		return OrphanActionResult{}, errors.New("automatic synchronization is still running for this Folder Link; pause its automation if you need to decide now")
	}
	defer automationLock.Unlock()

	binding, err := s.store.GetBinding(bindingID)
	if err != nil {
		return OrphanActionResult{}, err
	}
	if !stringInSlice(composePath, selectedComposePaths(binding)) {
		return OrphanActionResult{}, errors.New("stack is not selected for Git synchronization")
	}
	if _, err := s.PullRepository(ctx, binding.RepositoryUUID); err != nil {
		return OrphanActionResult{}, fmt.Errorf("refresh repository before orphan action: %w", err)
	}
	// Destructive decisions keep the repository snapshot stable from the proof
	// below until the local backup and removal have completed. Restore delegates
	// to ExportBinding, which owns the same lock and performs its own fresh token
	// validation.
	if action != "restore" {
		repositoryLock := s.repositoryLock(binding.RepositoryUUID)
		repositoryLock.Lock()
		defer repositoryLock.Unlock()
	}
	binding, repositoryFiles, stackFiles, err := s.loadTransferTrees(bindingID, "repository_to_stack", TransferInput{})
	if err != nil {
		return OrphanActionResult{}, err
	}
	baseline, err := s.store.BindingBaseline(binding.UUID)
	if err != nil {
		return OrphanActionResult{}, err
	}
	localCompose, localExists := stackFiles[composePath]
	if !localExists || localCompose.open == nil {
		return OrphanActionResult{}, errors.New("local orphan no longer exists; refresh the preview")
	}
	if _, tracked := baseline[composePath]; !tracked {
		return OrphanActionResult{}, errors.New("stack has no common Git baseline and cannot be treated as a Git deletion")
	}
	allCompose := splitPatternLines(binding.ComposePaths)
	for path := range repositoryFiles {
		if stringInSlice(composePath, composePathsForFile(allCompose, path)) {
			return OrphanActionResult{}, fmt.Errorf("orphan action refused: Git still contains %s for this stack", path)
		}
	}

	if action == "restore" {
		result, err := s.PushGitStack(ctx, bindingID, composePath)
		if err != nil {
			return OrphanActionResult{}, err
		}
		if err := s.settleBindingAfterOrphanDecision(bindingID); err != nil {
			return OrphanActionResult{}, fmt.Errorf("stack restored but synchronization state could not be refreshed: %w", err)
		}
		s.recordActivity(ActivityRecord{RepositoryID: binding.RepositoryUUID, BindingID: binding.UUID,
			ComposePath: composePath, Type: "orphan_resolve", Trigger: "manual",
			Details: ActivityDetails{Action: action, Message: result.Message, Paths: []string{composePath}}})
		return OrphanActionResult{Action: action, ComposePath: composePath, Message: result.Message}, nil
	}

	directory := filepath.ToSlash(filepath.Dir(filepath.FromSlash(composePath)))
	if directory == "." || directory == "" {
		return OrphanActionResult{}, errors.New("local removal is refused for a stack at the folder-link root; move it to a dedicated subfolder or remove it manually after backup")
	}
	for _, candidate := range allCompose {
		if candidate == composePath {
			continue
		}
		candidateDir := filepath.ToSlash(filepath.Dir(filepath.FromSlash(candidate)))
		if candidateDir == directory || strings.HasPrefix(candidateDir, directory+"/") {
			return OrphanActionResult{}, fmt.Errorf("local removal refused: stack directory also contains %s", candidate)
		}
	}
	if s.stackHasDirtyEditor(binding, composePath) {
		return OrphanActionResult{}, errors.New("local removal refused while this stack has an unsaved editor")
	}
	unlock := func() {}
	if s.lockCompose != nil {
		var locked bool
		unlock, locked = s.lockCompose(binding.Host, filepath.ToSlash(filepath.Join(binding.StackPath, composePath)))
		if !locked {
			return OrphanActionResult{}, errors.New("stack currently has another action in progress")
		}
	}
	defer unlock()

	targetFS, targetRoot, err := s.resolveBindingStack(binding)
	if err != nil {
		return OrphanActionResult{}, err
	}
	snapshot, err := collectOrphanSnapshot(targetFS, targetRoot, directory)
	if err != nil {
		return OrphanActionResult{}, err
	}
	backup, err := s.backupOrphanSnapshot(binding, composePath, snapshot, action == "archive")
	if err != nil {
		return OrphanActionResult{}, err
	}
	currentSnapshot, err := collectOrphanSnapshot(targetFS, targetRoot, directory)
	if err != nil {
		return OrphanActionResult{}, err
	}
	if !sameTransferSnapshot(snapshot, currentSnapshot) {
		return OrphanActionResult{}, errors.New("local stack changed while its backup was being created; nothing was removed")
	}
	if err := targetFS.RemoveAll(targetFS.Join(targetRoot, filepath.FromSlash(directory))); err != nil {
		return OrphanActionResult{}, fmt.Errorf("remove local orphan after backup: %w", err)
	}
	if err := s.forgetOrphanedStack(binding, composePath, baseline); err != nil {
		return OrphanActionResult{}, fmt.Errorf("update synchronization state after local removal: %w", err)
	}
	if err := s.settleBindingAfterOrphanDecision(bindingID); err != nil {
		return OrphanActionResult{}, fmt.Errorf("local orphan removed after backup but synchronization state could not be refreshed: %w", err)
	}
	if s.fileChangeNotify != nil {
		s.fileChangeNotify(binding.Host, filepath.ToSlash(filepath.Join(binding.StackPath, composePath)))
	}
	message := "Local orphan deleted after backup; no Docker action was run"
	if action == "archive" {
		message = "Local orphan archived and removed; no Docker action was run"
	}
	s.recordActivity(ActivityRecord{RepositoryID: binding.RepositoryUUID, BindingID: binding.UUID,
		ComposePath: composePath, Type: "orphan_resolve", Trigger: "manual", BackupID: backup,
		Details: ActivityDetails{Action: action, Message: message, Paths: []string{composePath}}})
	return OrphanActionResult{Action: action, ComposePath: composePath, Backup: backup, Message: message}, nil
}

// settleBindingAfterOrphanDecision recomputes the link state after the user
// has dealt with one orphan. This prevents a stale blocked checkpoint from
// surviving forever, while retaining a warning if another orphan or conflict
// still requires a decision.
func (s *Service) settleBindingAfterOrphanDecision(bindingID string) error {
	binding, err := s.store.GetBinding(bindingID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if !binding.AutoSyncEnabled {
		binding.AutoSyncState = "disabled"
		binding.AutoSyncError = ""
		return s.store.SaveBinding(&binding)
	}

	remotePreview, err := s.PreviewBinding(bindingID, "repository_to_stack", TransferInput{})
	if err != nil {
		return err
	}
	switch {
	case remotePreview.Conflicts > 0:
		binding.AutoSyncState = "conflict"
		binding.AutoSyncError = fmt.Sprintf("%d conflict(s) require a manual decision", remotePreview.Conflicts)
		binding.LastAutoSyncCommit = ""
	case remotePreview.Preserved > 0:
		binding.AutoSyncState = "blocked"
		binding.AutoSyncError = fmt.Sprintf("%d Git deletion(s) preserved locally; choose restore, archive, or explicit local deletion", remotePreview.Preserved)
		binding.LastAutoSyncCommit = ""
	case remotePreview.Changed > 0:
		binding.AutoSyncState = "watching"
		binding.AutoSyncError = fmt.Sprintf("%d Git change(s) still waiting for synchronization", remotePreview.Changed)
		binding.LastAutoSyncCommit = ""
	default:
		localPreview, previewErr := s.PreviewBinding(bindingID, "stack_to_repository", TransferInput{})
		if previewErr != nil {
			return previewErr
		}
		if localPreview.Conflicts > 0 {
			binding.AutoSyncState = "conflict"
			binding.AutoSyncError = fmt.Sprintf("%d conflict(s) require a manual decision", localPreview.Conflicts)
			binding.LastAutoSyncCommit = ""
		} else if localPreview.Changed > 0 {
			binding.AutoSyncState = "watching"
			binding.AutoSyncError = fmt.Sprintf("%d local change(s) waiting to be pushed to Git", localPreview.Changed)
		} else {
			status, statusErr := s.RepositoryStatus(binding.RepositoryUUID)
			if statusErr != nil {
				return statusErr
			}
			binding.AutoSyncState = "up_to_date"
			binding.AutoSyncError = ""
			binding.LastAutoSyncCommit = status.Head
			binding.LastAutoSyncSuccessAt = &now
		}
	}
	binding.LastAutoSyncAt = &now
	return s.store.SaveBinding(&binding)
}

func collectOrphanSnapshot(targetFS filesystem.FileSystem, bindingRoot, directory string) (map[string]transferFile, error) {
	stackRoot := targetFS.Join(bindingRoot, filepath.FromSlash(directory))
	absoluteBindingRoot, err := targetFS.Abs(bindingRoot)
	if err != nil {
		return nil, err
	}
	absoluteStackRoot, err := targetFS.Abs(stackRoot)
	if err != nil {
		return nil, err
	}
	result := map[string]transferFile{}
	var total int64
	err = targetFS.WalkDir(absoluteStackRoot, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if filepath.Clean(current) == filepath.Clean(absoluteStackRoot) {
			return nil
		}
		relative, err := filepath.Rel(absoluteBindingRoot, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if err := validateRelativePath(relative, false); err != nil {
			return fmt.Errorf("unsafe local orphan path %q: %w", relative, err)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("local orphan contains symbolic link %s; removal refused because it cannot be archived safely", relative)
		}
		if entry.IsDir() {
			if strings.EqualFold(entry.Name(), ".git") {
				return fmt.Errorf("local orphan contains nested Git metadata at %s; removal refused", relative)
			}
			return nil
		}
		info, err := targetFS.Stat(current)
		if err != nil {
			return err
		}
		if !isTransferFile(info.Mode()) {
			return fmt.Errorf("local orphan contains unsupported special file %s; removal refused", relative)
		}
		if info.Size() > maxBindingFileSize {
			return fmt.Errorf("local orphan file %s exceeds the %d MiB safe backup limit; removal refused", relative, maxBindingFileSize>>20)
		}
		if err := checkTransferLimit(len(result)+1, info.Size(), total+info.Size()); err != nil {
			return fmt.Errorf("local orphan cannot be fully backed up: %w", err)
		}
		total += info.Size()
		path := current
		file := transferFile{path: relative, size: info.Size(), mode: info.Mode().Perm(), sensitive: isSensitivePath(relative), open: func() (io.ReadCloser, error) {
			return targetFS.OpenFile(path, os.O_RDONLY, 0)
		}}
		file.sha, err = hashTransferFile(file)
		if err != nil {
			return err
		}
		result[relative] = file
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, errors.New("local stack snapshot is empty; nothing was removed")
	}
	return result, nil
}

func sameTransferSnapshot(left, right map[string]transferFile) bool {
	if len(left) != len(right) {
		return false
	}
	for path, file := range left {
		other, ok := right[path]
		if !ok || file.sha != other.sha || file.size != other.size || safeFileMode(file.mode) != safeFileMode(other.mode) {
			return false
		}
	}
	return true
}

func (s *Service) stackHasDirtyEditor(binding StackBinding, composePath string) bool {
	if s.dirtyEditorPaths == nil {
		return false
	}
	root := strings.Trim(filepath.ToSlash(filepath.Clean(filepath.FromSlash(binding.StackPath))), "/")
	directory := filepath.ToSlash(filepath.Dir(filepath.FromSlash(composePath)))
	for _, openPath := range s.dirtyEditorPaths(binding.Host) {
		openPath = strings.Trim(filepath.ToSlash(filepath.Clean(filepath.FromSlash(openPath))), "/")
		relative := openPath
		if root != "." && root != "" {
			if !strings.HasPrefix(openPath, root+"/") {
				continue
			}
			relative = strings.TrimPrefix(openPath, root+"/")
		}
		if relative == directory || strings.HasPrefix(relative, directory+"/") {
			return true
		}
	}
	return false
}

func (s *Service) forgetOrphanedStack(binding StackBinding, composePath string, baseline map[string]string) error {
	allCompose := splitPatternLines(binding.ComposePaths)
	remainingCompose := make([]string, 0, len(allCompose)-1)
	for _, candidate := range allCompose {
		if candidate != composePath {
			remainingCompose = append(remainingCompose, candidate)
		}
	}
	binding.ComposePaths = strings.Join(remainingCompose, "\n")
	binding.SelectedComposePaths = strings.Join(withoutString(splitPatternLines(binding.SelectedComposePaths), composePath), "\n")
	binding.AutoDeployComposePaths = strings.Join(withoutString(splitPatternLines(binding.AutoDeployComposePaths), composePath), "\n")
	if err := s.store.SaveBinding(&binding); err != nil {
		return err
	}
	for path := range baseline {
		if stringInSlice(composePath, composePathsForFile(allCompose, path)) {
			delete(baseline, path)
		}
	}
	if err := s.store.ReplaceBindingBaseline(binding.UUID, baseline); err != nil {
		return err
	}
	return s.reconcileGitStackStatuses(binding)
}

func withoutString(values []string, target string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}

func (s *Service) backupOrphanSnapshot(binding StackBinding, composePath string, files map[string]transferFile, archived bool) (string, error) {
	if err := os.MkdirAll(s.backupRoot, 0o700); err != nil {
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
	directory := binding.UUID
	if archived {
		directory = filepath.Join("archives", binding.UUID)
	}
	if err := backupFS.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	backupID := uuid.NewString()
	createdAt := time.Now().UTC()
	name := createdAt.Format("20060102T150405.000000000Z") + "-orphan.tar.gz"
	relativePath := filepath.Join(directory, name)
	handle, err := backupFS.OpenFile(relativePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	kind := "pre_orphan_delete"
	if archived {
		kind = "orphan_archive"
	}
	manifest := backupManifest{Version: 2, BackupID: backupID, BindingID: binding.UUID,
		RepositoryID: binding.RepositoryUUID, Kind: kind, CreatedAt: createdAt,
		ComposePaths: []string{composePath}}
	for _, file := range sortedTransferFiles(files) {
		manifest.Files = append(manifest.Files, backupManifestFile{Path: file.path, BeforeSHA: file.sha,
			BeforeExists: true, AfterExists: false, BeforeMode: uint32(safeFileMode(file.mode))})
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		_ = handle.Close()
		_ = backupFS.Remove(relativePath)
		return "", err
	}
	gzipWriter := gzip.NewWriter(handle)
	tarWriter := tar.NewWriter(gzipWriter)
	writeErr := func() error {
		header := &tar.Header{Name: backupManifestName, Mode: 0o600, Size: int64(len(manifestJSON)), ModTime: createdAt}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if _, err := tarWriter.Write(manifestJSON); err != nil {
			return err
		}
		for _, file := range sortedTransferFiles(files) {
			header := &tar.Header{Name: file.path, Mode: int64(safeFileMode(file.mode)), Size: file.size, ModTime: createdAt}
			if err := tarWriter.WriteHeader(header); err != nil {
				return err
			}
			if err := streamTransferFile(file, tarWriter); err != nil {
				return err
			}
		}
		return nil
	}()
	errorsToCheck := []error{writeErr, tarWriter.Close(), gzipWriter.Close(), handle.Close()}
	for _, candidate := range errorsToCheck {
		if candidate != nil {
			_ = backupFS.Remove(relativePath)
			return "", fmt.Errorf("write orphan backup: %w", candidate)
		}
	}
	row, err := s.registerBackup(binding, backupID, kind, relativePath, "", manifest)
	if err != nil {
		_ = backupFS.Remove(relativePath)
		return "", err
	}
	return row.UUID, nil
}

func detectOrphanedComposePaths(repositoryFiles, stackFiles map[string]transferFile, baseline map[string]string, composePaths []string) []string {
	result := make([]string, 0)
	for _, composePath := range composePaths {
		localCompose, local := stackFiles[composePath]
		_, tracked := baseline[composePath]
		if !local || localCompose.open == nil || !tracked {
			continue
		}
		if remoteCompose, exists := repositoryFiles[composePath]; exists && remoteCompose.open != nil {
			continue
		}
		hasRemoteStackFile := false
		for path := range repositoryFiles {
			if stringInSlice(composePath, composePathsForFile(composePaths, path)) {
				hasRemoteStackFile = true
				break
			}
		}
		if !hasRemoteStackFile {
			result = append(result, composePath)
		}
	}
	sort.Strings(result)
	return result
}
