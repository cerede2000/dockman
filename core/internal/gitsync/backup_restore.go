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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/RA341/dockman/internal/host/filesystem"
)

type BackupRestoreEntry struct {
	Path       string `json:"path"`
	Action     string `json:"action"`
	BeforeSHA  string `json:"beforeSha,omitempty"`
	AfterSHA   string `json:"afterSha,omitempty"`
	CurrentSHA string `json:"currentSha,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type BackupRestorePreview struct {
	Backup     BackupView           `json:"backup"`
	Entries    []BackupRestoreEntry `json:"entries"`
	Restorable int                  `json:"restorable"`
	Conflicts  int                  `json:"conflicts"`
	Token      string               `json:"token"`
}

type BackupRestoreInput struct {
	PreviewToken  string   `json:"previewToken"`
	SelectedPaths []string `json:"selectedPaths"`
}

type BackupRestoreResult struct {
	BackupID       string   `json:"backupId"`
	SafetyBackupID string   `json:"safetyBackupId,omitempty"`
	RestoredPaths  []string `json:"restoredPaths"`
	Message        string   `json:"message"`
}

func (s *Service) PreviewBackupRestore(bindingID, backupID string) (BackupRestorePreview, error) {
	row, err := s.authorizedBackup(bindingID, backupID)
	if err != nil {
		return BackupRestorePreview{}, err
	}
	if !row.Restorable {
		return BackupRestorePreview{}, errors.New("this legacy backup can be downloaded but cannot be restored automatically")
	}
	binding, err := s.store.GetBinding(bindingID)
	if err != nil {
		return BackupRestorePreview{}, err
	}
	manifest, err := s.loadBackupManifest(row)
	if err != nil {
		return BackupRestorePreview{}, err
	}
	if err := validateBackupManifest(manifest, row); err != nil {
		return BackupRestorePreview{}, err
	}
	targetFS, targetRoot, err := s.resolveBindingStack(binding)
	if err != nil {
		return BackupRestorePreview{}, err
	}
	preview := BackupRestorePreview{Backup: backupView(row)}
	for _, record := range manifest.Files {
		current, exists, currentErr := currentBackupFile(targetFS, targetRoot, record.Path)
		entry := BackupRestoreEntry{Path: record.Path, BeforeSHA: record.BeforeSHA, AfterSHA: record.AfterSHA}
		if currentErr != nil {
			entry.Action, entry.Reason = "conflict", currentErr.Error()
		} else if exists {
			entry.CurrentSHA = current.sha
			switch {
			case record.BeforeExists && current.sha == record.BeforeSHA:
				entry.Action = "noop"
			case record.AfterExists && current.sha == record.AfterSHA && record.BeforeExists:
				entry.Action = "restore"
			case record.AfterExists && current.sha == record.AfterSHA && !record.BeforeExists:
				entry.Action = "remove"
			default:
				entry.Action, entry.Reason = "conflict", "the current file changed after this backup"
			}
		} else {
			switch {
			case record.BeforeExists && !record.AfterExists:
				entry.Action = "restore"
			case record.BeforeExists && record.AfterExists:
				entry.Action, entry.Reason = "conflict", "the synchronized file is now missing"
			default:
				entry.Action = "noop"
			}
		}
		if entry.Action == "restore" || entry.Action == "remove" {
			preview.Restorable++
		}
		if entry.Action == "conflict" {
			preview.Conflicts++
		}
		preview.Entries = append(preview.Entries, entry)
	}
	preview.Token = backupPreviewToken(row.UUID, preview.Entries)
	return preview, nil
}

func (s *Service) RestoreBackup(ctx context.Context, bindingID, backupID string, input BackupRestoreInput) (BackupRestoreResult, error) {
	_ = ctx
	preview, err := s.PreviewBackupRestore(bindingID, backupID)
	if err != nil {
		return BackupRestoreResult{}, err
	}
	if input.PreviewToken == "" || input.PreviewToken != preview.Token {
		return BackupRestoreResult{}, errors.New("backup restore preview changed; review it again")
	}
	selectedSet := make(map[string]struct{})
	if len(input.SelectedPaths) == 0 {
		for _, entry := range preview.Entries {
			if entry.Action == "restore" || entry.Action == "remove" {
				selectedSet[entry.Path] = struct{}{}
			}
		}
	} else {
		for _, path := range input.SelectedPaths {
			path = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(path))))
			if err := validateRelativePath(path, false); err != nil {
				return BackupRestoreResult{}, err
			}
			selectedSet[path] = struct{}{}
		}
	}
	allowed := make(map[string]BackupRestoreEntry)
	for _, entry := range preview.Entries {
		if entry.Action == "restore" || entry.Action == "remove" {
			allowed[entry.Path] = entry
		}
	}
	for path := range selectedSet {
		if _, ok := allowed[path]; !ok {
			return BackupRestoreResult{}, fmt.Errorf("%s is no longer safely restorable", path)
		}
	}
	if len(selectedSet) == 0 {
		return BackupRestoreResult{}, errors.New("no safely restorable file was selected")
	}

	row, err := s.authorizedBackup(bindingID, backupID)
	if err != nil {
		return BackupRestoreResult{}, err
	}
	binding, err := s.store.GetBinding(bindingID)
	if err != nil {
		return BackupRestoreResult{}, err
	}
	manifest, err := s.loadBackupManifest(row)
	if err != nil {
		return BackupRestoreResult{}, err
	}
	// Restoring a backup is the most urgent explicit action there is: something
	// went wrong and the operator wants their files back. Refusing it the
	// instant a scheduled cycle happens to hold the lock is the wrong answer.
	automationLock := s.repositoryLock("automation:" + bindingID)
	if !waitForLock(automationLock, decisionLockBudget) {
		return BackupRestoreResult{}, errors.New("automatic synchronization is still running for this Folder Link; pause its automation if you need to restore now")
	}
	defer automationLock.Unlock()
	repositoryLock := s.repositoryLock(binding.RepositoryUUID)
	if !waitForLock(repositoryLock, decisionLockBudget) {
		return BackupRestoreResult{}, errors.New("a Git operation is still running on this repository; retry in a moment")
	}
	defer repositoryLock.Unlock()
	lockedPreview, err := s.PreviewBackupRestore(bindingID, backupID)
	if err != nil {
		return BackupRestoreResult{}, err
	}
	if lockedPreview.Token != input.PreviewToken {
		return BackupRestoreResult{}, errors.New("stack files changed after the restore preview; review the backup again")
	}
	allowed = make(map[string]BackupRestoreEntry)
	for _, entry := range lockedPreview.Entries {
		if entry.Action == "restore" || entry.Action == "remove" {
			allowed[entry.Path] = entry
		}
	}
	for path := range selectedSet {
		if _, ok := allowed[path]; !ok {
			return BackupRestoreResult{}, fmt.Errorf("%s is no longer safely restorable", path)
		}
	}
	targetFS, targetRoot, err := s.resolveBindingStack(binding)
	if err != nil {
		return BackupRestoreResult{}, err
	}
	if s.stackHasAnyDirtyEditor(binding, manifest.ComposePaths) {
		return BackupRestoreResult{}, errors.New("backup restore refused while an affected stack has an unsaved editor")
	}

	desired, current := map[string]transferFile{}, map[string]transferFile{}
	for _, record := range manifest.Files {
		if _, ok := selectedSet[record.Path]; !ok {
			continue
		}
		if record.BeforeExists {
			desired[record.Path] = transferFile{path: record.Path, sha: record.BeforeSHA, mode: os.FileMode(record.BeforeMode), open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("")), nil }}
		} else {
			desired[record.Path] = transferFile{path: record.Path}
		}
		file, exists, currentErr := currentBackupFile(targetFS, targetRoot, record.Path)
		if currentErr != nil {
			return BackupRestoreResult{}, currentErr
		}
		if exists {
			current[record.Path] = file
		}
	}
	safetyBackup, err := s.backupChangedFiles(binding, desired, current, "pre_restore", row.CommitSHA, row.UUID)
	if err != nil {
		return BackupRestoreResult{}, fmt.Errorf("create restore safety backup: %w", err)
	}
	affectedSet := make(map[string]struct{})
	for path := range selectedSet {
		for _, composePath := range composePathsForFile(manifest.ComposePaths, path) {
			affectedSet[composePath] = struct{}{}
		}
	}
	affectedStacks := make([]string, 0, len(affectedSet))
	for composePath := range affectedSet {
		affectedStacks = append(affectedStacks, composePath)
	}
	sort.Strings(affectedStacks)
	// As with commit rollback, pause before the first local write. This keeps a
	// successful recovery from being immediately overwritten by auto-sync.
	for _, composePath := range affectedStacks {
		if err := s.store.SetGitStackPauseReason(binding.UUID, composePath, true, stackPauseRecovery); err != nil {
			return BackupRestoreResult{SafetyBackupID: safetyBackup}, fmt.Errorf("pause stack automation before restore: %w", err)
		}
	}
	if err := s.applyBackupArchive(row, manifest, selectedSet, allowed, targetFS, targetRoot); err != nil {
		return BackupRestoreResult{}, err
	}
	paths := make([]string, 0, len(selectedSet))
	for path := range selectedSet {
		paths = append(paths, path)
		if s.fileChangeNotify != nil {
			s.fileChangeNotify(binding.Host, filepath.ToSlash(filepath.Join(binding.StackPath, path)))
		}
	}
	sort.Strings(paths)
	message := "Backup restored locally; no Compose or Docker action was run"
	knownComposePaths := splitPatternLines(binding.ComposePaths)
	needsCatalogRefresh := false
	for _, composePath := range manifest.ComposePaths {
		if !stringInSlice(composePath, knownComposePaths) {
			needsCatalogRefresh = true
			break
		}
	}
	// An orphan archive can recreate a complete stack directory. Refresh the
	// compact compose index once after this explicit action so Files and Monitor
	// can expose it again without adding any background scan. A missing Git
	// workspace must not turn already-restored local files into a failed restore.
	if needsCatalogRefresh {
		refreshedBinding, _, refreshErr := s.refreshBindingComposeCatalogLocked(binding)
		if refreshErr != nil {
			message += "; compose catalog refresh is pending: " + safeGitError(refreshErr)
		} else {
			binding = refreshedBinding
		}
	}
	now := time.Now().UTC()
	if err := s.store.UpdateGitStackStatuses(binding.UUID, affectedStacks, map[string]any{
		"state": stackSyncLocalChanges, "error_message": "Backup restored locally; review before pushing or importing", "last_checked_at": &now,
	}); err != nil {
		return BackupRestoreResult{BackupID: row.UUID, SafetyBackupID: safetyBackup, RestoredPaths: paths}, fmt.Errorf("record restored stack state: %w", err)
	}
	s.recordActivity(ActivityRecord{RepositoryID: binding.RepositoryUUID, BindingID: binding.UUID,
		Type: "backup_restore", Trigger: "manual", BackupID: row.UUID, CommitSHA: row.CommitSHA,
		Details: ActivityDetails{Action: "restore", Paths: paths, Message: message}})
	return BackupRestoreResult{BackupID: row.UUID, SafetyBackupID: safetyBackup, RestoredPaths: paths,
		Message: message}, nil
}

func (s *Service) loadBackupManifest(row GitBackup) (backupManifest, error) {
	handle, _, err := s.OpenBackup(row.BindingUUID, row.UUID)
	if err != nil {
		return backupManifest{}, err
	}
	defer handle.Close()
	return readBackupManifest(handle)
}

func backupPreviewToken(backupID string, entries []BackupRestoreEntry) string {
	raw, _ := json.Marshal(struct {
		BackupID string               `json:"backupId"`
		Entries  []BackupRestoreEntry `json:"entries"`
	}{backupID, entries})
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func currentBackupFile(targetFS filesystem.FileSystem, root, path string) (transferFile, bool, error) {
	full := targetFS.Join(root, filepath.FromSlash(path))
	info, err := targetFS.Stat(full)
	if errors.Is(err, os.ErrNotExist) {
		return transferFile{}, false, nil
	}
	if err != nil {
		return transferFile{}, false, err
	}
	if !isTransferFile(info.Mode()) || info.Size() > maxBindingFileSize {
		return transferFile{}, false, errors.New("current backup target is not a supported regular file")
	}
	file := transferFile{path: path, size: info.Size(), mode: info.Mode().Perm(), open: func() (io.ReadCloser, error) {
		return targetFS.OpenFile(full, os.O_RDONLY, 0)
	}}
	file.sha, err = hashTransferFile(file)
	return file, true, err
}

func (s *Service) applyBackupArchive(row GitBackup, _ backupManifest, selected map[string]struct{}, actions map[string]BackupRestoreEntry, targetFS filesystem.FileSystem, targetRoot string) error {
	handle, _, err := s.OpenBackup(row.BindingUUID, row.UUID)
	if err != nil {
		return err
	}
	defer handle.Close()
	gzipReader, err := gzip.NewReader(handle)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	restored := make(map[string]struct{})
	tarReader := tar.NewReader(gzipReader)
	buffer := transferBufferPool.Get().([]byte)
	defer transferBufferPool.Put(buffer)
	var restoredBytes int64
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		path := filepath.ToSlash(filepath.Clean(filepath.FromSlash(header.Name)))
		if path == backupManifestName {
			continue
		}
		if err := validateRelativePath(path, false); err != nil || header.Typeflag != tar.TypeReg {
			return errors.New("backup archive contains an unsafe entry")
		}
		if _, ok := selected[path]; !ok || actions[path].Action != "restore" {
			continue
		}
		if header.Size < 0 || header.Size > maxBindingFileSize {
			return errors.New("backup archive entry exceeds the safe restore limit")
		}
		restoredBytes += header.Size
		if restoredBytes > maxBindingTotalSize {
			return errors.New("selected backup content exceeds the safe restore total-size limit")
		}
		full := targetFS.Join(targetRoot, filepath.FromSlash(path))
		if err := targetFS.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			return err
		}
		mode := os.FileMode(header.Mode).Perm()
		if mode == 0 {
			mode = 0o600
		}
		temporary := full + ".dockman-restore-" + row.UUID + ".tmp"
		writer, err := targetFS.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		hash := sha256.New()
		written, copyErr := io.CopyBuffer(io.MultiWriter(writer, hash), io.LimitReader(tarReader, header.Size), buffer)
		closeErr := writer.Close()
		if copyErr != nil || closeErr != nil || written != header.Size || hex.EncodeToString(hash.Sum(nil)) != actions[path].BeforeSHA {
			_ = targetFS.RemoveAll(temporary)
			if copyErr != nil || closeErr != nil {
				return errors.Join(copyErr, closeErr)
			}
			return fmt.Errorf("backup content for %s failed its integrity check", path)
		}
		if err := targetFS.Rename(temporary, full); err != nil {
			_ = targetFS.RemoveAll(temporary)
			return fmt.Errorf("replace %s: %w", path, err)
		}
		restored[path] = struct{}{}
	}
	for path := range selected {
		action := actions[path].Action
		if action == "restore" {
			if _, ok := restored[path]; !ok {
				return fmt.Errorf("backup content for %s is missing", path)
			}
			continue
		}
		if action == "remove" {
			if err := targetFS.RemoveAll(targetFS.Join(targetRoot, filepath.FromSlash(path))); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) stackHasAnyDirtyEditor(binding StackBinding, composePaths []string) bool {
	for _, composePath := range composePaths {
		if s.stackHasDirtyEditor(binding, composePath) {
			return true
		}
	}
	return false
}
