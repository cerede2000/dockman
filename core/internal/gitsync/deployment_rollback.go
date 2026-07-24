package gitsync

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
)

// deploymentHadPreviousCompose distinguishes an update from the first
// deployment of a newly discovered stack. A new stack must be taken down while
// its imported Compose file still exists; an update is repaired by redeploying
// the restored Compose file instead.
func (s *Service) deploymentHadPreviousCompose(binding StackBinding, backupID, composePath string) (bool, error) {
	if backupID == "" {
		return false, errors.New("no pre-import backup is available for automatic rollback")
	}
	row, err := s.authorizedBackup(binding.UUID, backupID)
	if err != nil {
		return false, err
	}
	if row.Kind != "pre_import" || !row.Restorable {
		return false, errors.New("the deployment backup is not eligible for automatic rollback")
	}
	manifest, err := s.loadBackupManifest(row)
	if err != nil {
		return false, err
	}
	if err := validateBackupManifest(manifest, row); err != nil {
		return false, err
	}
	for _, record := range manifest.Files {
		if record.Path == composePath {
			return record.BeforeExists, nil
		}
	}
	// A config-only change does not add the unchanged Compose file to the
	// backup manifest, therefore the stack necessarily existed before import.
	return true, nil
}

// rollbackDeploymentFiles restores only the files owned by one failed stack
// from the pre-import backup. Every current file must still match the imported
// SHA, otherwise an external/editor change wins and rollback is refused.
func (s *Service) rollbackDeploymentFiles(binding StackBinding, backupID, composePath string) ([]string, error) {
	if backupID == "" {
		return nil, errors.New("no pre-import backup is available for automatic rollback")
	}
	row, err := s.authorizedBackup(binding.UUID, backupID)
	if err != nil {
		return nil, err
	}
	if row.Kind != "pre_import" || !row.Restorable {
		return nil, errors.New("the deployment backup is not eligible for automatic rollback")
	}
	manifest, err := s.loadBackupManifest(row)
	if err != nil {
		return nil, err
	}
	if err := validateBackupManifest(manifest, row); err != nil {
		return nil, err
	}
	targetFS, targetRoot, err := s.resolveBindingStack(binding)
	if err != nil {
		return nil, err
	}
	if s.stackHasAnyDirtyEditor(binding, []string{composePath}) {
		return nil, errors.New("automatic rollback refused while the failed stack has an unsaved editor")
	}

	selected := make(map[string]struct{})
	actions := make(map[string]BackupRestoreEntry)
	beforeHashes := make(map[string]*string)
	for _, record := range manifest.Files {
		if !stringInSlice(composePath, composePathsForFile(manifest.ComposePaths, record.Path)) {
			continue
		}
		current, exists, currentErr := currentBackupFile(targetFS, targetRoot, record.Path)
		if currentErr != nil {
			return nil, fmt.Errorf("verify %s before automatic rollback: %w", record.Path, currentErr)
		}
		action := BackupRestoreEntry{Path: record.Path, BeforeSHA: record.BeforeSHA, AfterSHA: record.AfterSHA}
		switch {
		case exists && record.BeforeExists && record.AfterExists && current.sha == record.BeforeSHA:
			action.Action = "noop"
		case exists && record.BeforeExists && record.AfterExists && current.sha == record.AfterSHA:
			action.Action = "restore"
		case exists && !record.BeforeExists && record.AfterExists && current.sha == record.AfterSHA:
			action.Action = "remove"
		case !exists && record.BeforeExists && !record.AfterExists:
			action.Action = "restore"
		case !exists && !record.BeforeExists:
			action.Action = "noop"
		default:
			return nil, fmt.Errorf("%s changed after import; automatic rollback was refused", record.Path)
		}
		if record.BeforeExists {
			sha := record.BeforeSHA
			beforeHashes[record.Path] = &sha
		} else {
			beforeHashes[record.Path] = nil
		}
		if action.Action == "restore" || action.Action == "remove" {
			selected[record.Path] = struct{}{}
			actions[record.Path] = action
		}
	}
	if len(beforeHashes) == 0 {
		return nil, errors.New("the pre-import backup contains no file for this stack")
	}
	if len(selected) > 0 {
		if err := s.applyBackupArchive(row, manifest, selected, actions, targetFS, targetRoot); err != nil {
			return nil, err
		}
	}

	baseline, err := s.store.BindingBaseline(binding.UUID)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(beforeHashes))
	for path, sha := range beforeHashes {
		paths = append(paths, path)
		if sha == nil {
			delete(baseline, path)
		} else {
			baseline[path] = *sha
		}
		if s.fileChangeNotify != nil {
			s.fileChangeNotify(binding.Host, filepath.ToSlash(filepath.Join(binding.StackPath, path)))
		}
	}
	if err := s.store.ReplaceBindingBaseline(binding.UUID, baseline); err != nil {
		return nil, fmt.Errorf("restore synchronization baseline after deployment rollback: %w", err)
	}
	sort.Strings(paths)
	return paths, nil
}
