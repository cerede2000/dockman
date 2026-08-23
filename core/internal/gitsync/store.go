package gitsync

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

type Store struct{ db *gorm.DB }

func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

func (s *Store) ListCredentials() ([]Credential, error) {
	var rows []Credential
	err := s.db.Order("name COLLATE NOCASE ASC").Find(&rows).Error
	return rows, err
}

func (s *Store) GetCredential(id string) (Credential, error) {
	var row Credential
	err := s.db.Where("uuid = ?", id).First(&row).Error
	return row, err
}

func (s *Store) SaveCredential(row *Credential) error { return s.db.Save(row).Error }

func (s *Store) DeleteCredential(id string) error {
	// Credentials contain encrypted secret material. Purge the row instead of
	// retaining it through GORM soft deletion once reference checks have passed.
	result := s.db.Unscoped().Where("uuid = ?", id).Delete(&Credential{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (s *Store) CredentialInUse(id string) (bool, error) {
	var count int64
	err := s.db.Model(&Repository{}).Where("credential_uuid = ?", id).Count(&count).Error
	return count > 0, err
}

func (s *Store) ListRepositories() ([]Repository, error) {
	var rows []Repository
	err := s.db.Order("name COLLATE NOCASE ASC").Find(&rows).Error
	return rows, err
}

func (s *Store) GetRepository(id string) (Repository, error) {
	var row Repository
	err := s.db.Where("uuid = ?", id).First(&row).Error
	return row, err
}

func (s *Store) RepositoriesByIDs(ids []string) ([]Repository, error) {
	if len(ids) == 0 {
		return []Repository{}, nil
	}
	var rows []Repository
	err := s.db.Where("uuid IN ?", ids).Find(&rows).Error
	return rows, err
}

func (s *Store) SaveRepository(row *Repository) error { return s.db.Save(row).Error }

func (s *Store) DeleteRepository(id string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var webhookIDs []string
		if err := tx.Unscoped().Model(&RepositoryWebhook{}).Where("repository_uuid = ?", id).Pluck("uuid", &webhookIDs).Error; err != nil {
			return err
		}
		if len(webhookIDs) > 0 {
			if err := tx.Unscoped().Where("webhook_uuid IN ?", webhookIDs).Delete(&WebhookDelivery{}).Error; err != nil {
				return err
			}
			if err := tx.Unscoped().Where("repository_uuid = ?", id).Delete(&RepositoryWebhook{}).Error; err != nil {
				return err
			}
		}
		var bindingIDs []string
		if err := tx.Unscoped().Model(&StackBinding{}).Where("repository_uuid = ?", id).Pluck("uuid", &bindingIDs).Error; err != nil {
			return err
		}
		if len(bindingIDs) > 0 {
			if err := tx.Where("binding_uuid IN ?", bindingIDs).Delete(&GitStackStatus{}).Error; err != nil {
				return err
			}
			if err := tx.Where("binding_uuid IN ?", bindingIDs).Delete(&BindingBaseline{}).Error; err != nil {
				return err
			}
			if err := tx.Unscoped().Where("uuid IN ?", bindingIDs).Delete(&StackBinding{}).Error; err != nil {
				return err
			}
		}
		result := tx.Unscoped().Where("uuid = ?", id).Delete(&Repository{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		if err := tx.Where("repository_uuid = ?", id).Delete(&Operation{}).Error; err != nil {
			return err
		}
		return tx.Where("repository_uuid = ?", id).Delete(&Deployment{}).Error
	})
}

func (s *Store) RepositoryHasBindings(id string) (bool, error) {
	var count int64
	err := s.db.Model(&StackBinding{}).Where("repository_uuid = ?", id).Count(&count).Error
	return count > 0, err
}

func (s *Store) RepositoryBindingIDs(id string) ([]string, error) {
	var ids []string
	err := s.db.Unscoped().Model(&StackBinding{}).Where("repository_uuid = ?", id).Pluck("uuid", &ids).Error
	return ids, err
}

func (s *Store) ListBindings() ([]StackBinding, error) {
	var rows []StackBinding
	err := s.db.Order("host COLLATE NOCASE ASC, stack_path COLLATE NOCASE ASC").Find(&rows).Error
	return rows, err
}

func (s *Store) ListBindingsForHost(host string) ([]StackBinding, error) {
	if host == "" {
		return s.ListBindings()
	}
	var rows []StackBinding
	err := s.db.Where("host = ?", host).Order("stack_path COLLATE NOCASE ASC").Find(&rows).Error
	return rows, err
}

func (s *Store) GetBinding(id string) (StackBinding, error) {
	var row StackBinding
	err := s.db.Where("uuid = ?", id).First(&row).Error
	return row, err
}

func (s *Store) ListAutoSyncBindings() ([]StackBinding, error) {
	var rows []StackBinding
	err := s.db.Where("auto_sync_enabled = ? AND auto_sync_paused = ?", true, false).
		Order("last_auto_sync_at ASC, created_at ASC").Find(&rows).Error
	return rows, err
}

// SaveBinding persists mutable Folder Link state while protecting its two
// endpoints. Changing either endpoint turns the same binding into a different
// synchronization contract and can silently copy a repository-root link into
// a generated folder such as stacks/compose. Such a move must be expressed as
// an explicit unlink followed by a new link, never as a side effect of saving
// policy, automation, catalog, or runtime state.
func (s *Store) SaveBinding(row *StackBinding) error {
	if strings.TrimSpace(row.UUID) == "" {
		return errors.New("folder link UUID is required")
	}
	var existing StackBinding
	err := s.db.Unscoped().Select("id", "uuid", "repository_uuid", "host", "stack_path", "sub_path", "deleted_at").
		Where("uuid = ?", row.UUID).Take(&existing).Error
	if err == nil {
		if existing.RepositoryUUID != row.RepositoryUUID || existing.Host != row.Host ||
			existing.StackPath != row.StackPath || existing.SubPath != row.SubPath {
			return fmt.Errorf("folder link target is immutable; unlink and create a new link to change repository, host, source folder, or Git destination")
		}
		// Unlinking without "forget" only soft-deletes the row. Anything that
		// held the binding in memory - an auto-sync run, a deployment, a
		// status reconciliation - still saves its state afterwards, and Save
		// writes the zero DeletedAt of that in-memory copy straight over the
		// deletion. The link came back to life, re-enrolled in whatever
		// automation it carried, pointing at a repository the user had just
		// disconnected. Refusing is the only safe answer: the caller is
		// writing to something that no longer exists.
		if existing.DeletedAt.Valid {
			return fmt.Errorf("folder link %s was unlinked; its state can no longer be saved: %w", row.UUID, gorm.ErrRecordNotFound)
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return s.db.Save(row).Error
}

func (s *Store) ReconcileGitStackStatuses(binding StackBinding, composePaths []string, initialState string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var existing []GitStackStatus
		if err := tx.Where("binding_uuid = ?", binding.UUID).Find(&existing).Error; err != nil {
			return err
		}
		keep := make(map[string]struct{}, len(composePaths))
		known := make(map[string]struct{}, len(existing))
		for _, row := range existing {
			known[row.ComposePath] = struct{}{}
		}
		for _, composePath := range composePaths {
			keep[composePath] = struct{}{}
			if _, ok := known[composePath]; ok {
				continue
			}
			row := GitStackStatus{BindingUUID: binding.UUID, ComposePath: composePath, State: initialState, DeployState: "disabled"}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		remove := make([]string, 0)
		for _, row := range existing {
			if _, ok := keep[row.ComposePath]; !ok {
				remove = append(remove, row.ComposePath)
			}
		}
		if len(remove) > 0 {
			return tx.Where("binding_uuid = ? AND compose_path IN ?", binding.UUID, remove).Delete(&GitStackStatus{}).Error
		}
		return nil
	})
}

func (s *Store) ListGitStackStatuses(host string) ([]GitStackStatus, error) {
	var rows []GitStackStatus
	query := s.db.Table("git_stack_statuses").
		Joins("JOIN git_stack_bindings ON git_stack_bindings.uuid = git_stack_statuses.binding_uuid AND git_stack_bindings.deleted_at IS NULL")
	if host != "" {
		query = query.Where("git_stack_bindings.host = ?", host)
	}
	err := query.Select("git_stack_statuses.*").Order("git_stack_statuses.binding_uuid, git_stack_statuses.compose_path").Scan(&rows).Error
	return rows, err
}

func (s *Store) GitStackStatuses(bindingID string) ([]GitStackStatus, error) {
	var rows []GitStackStatus
	err := s.db.Where("binding_uuid = ?", bindingID).Order("compose_path").Find(&rows).Error
	return rows, err
}

func (s *Store) GitStackStatus(bindingID, composePath string) (GitStackStatus, error) {
	var row GitStackStatus
	err := s.db.Where("binding_uuid = ? AND compose_path = ?", bindingID, composePath).First(&row).Error
	return row, err
}

func (s *Store) PausedComposePaths(bindingID string) ([]string, error) {
	var paths []string
	err := s.db.Model(&GitStackStatus{}).Where("binding_uuid = ? AND automation_paused = ?", bindingID, true).Pluck("compose_path", &paths).Error
	sort.Strings(paths)
	return paths, err
}

func (s *Store) SetGitStackPause(bindingID, composePath string, paused bool) error {
	reason := ""
	if paused {
		reason = stackPauseManual
	}
	return s.SetGitStackPauseReason(bindingID, composePath, paused, reason)
}

func (s *Store) SetGitStackPauseReason(bindingID, composePath string, paused bool, reason string) error {
	if !paused {
		reason = ""
	} else if reason != stackPauseManual && reason != stackPauseRecovery {
		return errors.New("invalid Git stack pause reason")
	}
	result := s.db.Model(&GitStackStatus{}).Where("binding_uuid = ? AND compose_path = ?", bindingID, composePath).
		Updates(map[string]any{"automation_paused": paused, "pause_reason": reason})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (s *Store) UpdateGitStackStatuses(bindingID string, composePaths []string, updates map[string]any) error {
	query := s.db.Model(&GitStackStatus{}).Where("binding_uuid = ?", bindingID)
	if composePaths != nil {
		// A non-nil empty selection means that every stack is paused. It must
		// never fall through to an update of all rows for the binding.
		if len(composePaths) == 0 {
			return nil
		}
		query = query.Where("compose_path IN ?", composePaths)
	}
	return query.Updates(updates).Error
}

func (s *Store) UpdateGitStackStatusesExcept(bindingID string, composePaths []string, excludedStates []string, updates map[string]any) error {
	if composePaths != nil && len(composePaths) == 0 {
		return nil
	}
	query := s.db.Model(&GitStackStatus{}).Where("binding_uuid = ?", bindingID)
	if composePaths != nil {
		query = query.Where("compose_path IN ?", composePaths)
	}
	if len(excludedStates) > 0 {
		query = query.Where("state NOT IN ?", excludedStates)
	}
	return query.Updates(updates).Error
}

func (s *Store) SaveDeployment(row *Deployment) error { return s.db.Save(row).Error }

func (s *Store) ListDeployments(bindingID string, limit int) ([]Deployment, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	var rows []Deployment
	err := s.db.Where("binding_uuid = ?", bindingID).Order("created_at DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (s *Store) UpdateBindingAutoDeployState(id, state, message string, at *time.Time) error {
	updates := map[string]any{"auto_deploy_state": state, "auto_deploy_error": message}
	if at != nil {
		updates["last_auto_deploy_at"] = at
	}
	return s.db.Model(&StackBinding{}).Where("uuid = ?", id).Updates(updates).Error
}

func (s *Store) UpdateBindingAutoSyncState(id, state, message, commit string, attemptedAt, succeededAt *time.Time) error {
	updates := map[string]any{"auto_sync_state": state, "auto_sync_error": message}
	if attemptedAt != nil {
		updates["last_auto_sync_at"] = attemptedAt
	}
	if succeededAt != nil {
		updates["last_auto_sync_success_at"] = succeededAt
	}
	if commit != "" {
		updates["last_auto_sync_commit"] = commit
	}
	return s.db.Model(&StackBinding{}).Where("uuid = ?", id).Updates(updates).Error
}

// ClearStaleAutoSyncBlocks forgets the last synchronized commit of every link
// stopped on a conflict or a local deletion, so the next automatic cycle runs a
// full scan instead of taking the "no new Git commit" shortcut.
//
// Releases before per-stack isolation aborted the whole link on either of those:
// the untouched stacks kept a computed-but-never-applied "remote changes" state,
// and when Git had not moved since, the shortcut skipped the scan that would
// have repaired them - the link stayed stuck until someone checked by hand.
// Running this once at startup is what makes upgrading enough to recover.
//
// Preserved Git deletions are excluded: remembering the commit there is
// deliberate, it keeps the interval fetch-only until Git actually moves.
func (s *Store) ClearStaleAutoSyncBlocks() (int64, error) {
	tx := s.db.Model(&StackBinding{}).
		Where("auto_sync_state IN ?", []string{"blocked", "conflict"}).
		Where("last_auto_sync_commit IS NOT NULL AND last_auto_sync_commit != ''").
		Where("auto_sync_error IS NULL OR auto_sync_error NOT LIKE ?", "%Git deletion%").
		Update("last_auto_sync_commit", "")
	return tx.RowsAffected, tx.Error
}

func (s *Store) UpdateBindingInitialSyncState(id, state, message string, at *time.Time) error {
	updates := map[string]any{"initial_sync_state": state, "initial_sync_error": message}
	if at != nil {
		updates["initial_sync_at"] = at
	}
	return s.db.Model(&StackBinding{}).Where("uuid = ?", id).Updates(updates).Error
}

func (s *Store) ArchivedBinding(host, stackPath string) (StackBinding, error) {
	var row StackBinding
	err := s.db.Unscoped().Where("host = ? AND stack_path = ? AND deleted_at IS NOT NULL", host, stackPath).First(&row).Error
	return row, err
}

func (s *Store) RestoreBinding(row *StackBinding) error {
	return s.db.Unscoped().Model(&StackBinding{}).Where("uuid = ?", row.UUID).Updates(map[string]any{
		"deleted_at": nil, "compose_paths": row.ComposePaths, "enabled": true,
		"compose_selection_mode": row.ComposeSelectionMode, "selected_compose_paths": row.SelectedComposePaths,
		"auto_sync_selection_mode": row.AutoSyncSelectionMode, "auto_sync_compose_paths": row.AutoSyncComposePaths,
		"auto_reconcile_enabled": row.AutoReconcileEnabled, "initial_sync_state": row.InitialSyncState,
		"initial_sync_error": row.InitialSyncError,
	}).Error
}

func (s *Store) DeleteBinding(id string, forget bool) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		query := tx.Where("uuid = ?", id)
		if forget {
			query = query.Unscoped()
		}
		result := query.Delete(&StackBinding{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		if forget {
			if err := tx.Where("binding_uuid = ?", id).Delete(&GitStackStatus{}).Error; err != nil {
				return err
			}
			return tx.Where("binding_uuid = ?", id).Delete(&BindingBaseline{}).Error
		}
		return nil
	})
}

func (s *Store) BindingBaseline(id string) (map[string]string, error) {
	var rows []BindingBaseline
	if err := s.db.Where("binding_uuid = ?", id).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[string]string, len(rows))
	for _, row := range rows {
		result[row.Path] = row.SHA256
	}
	return result, nil
}

func (s *Store) ReplaceBindingBaseline(id string, hashes map[string]string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("binding_uuid = ?", id).Delete(&BindingBaseline{}).Error; err != nil {
			return err
		}
		rows := make([]BindingBaseline, 0, len(hashes))
		for path, sha := range hashes {
			rows = append(rows, BindingBaseline{BindingUUID: id, Path: path, SHA256: sha})
		}
		if len(rows) == 0 {
			return nil
		}
		return tx.CreateInBatches(rows, 250).Error
	})
}

func (s *Store) ListOperations(repositoryID string, limit int) ([]Operation, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	var rows []Operation
	query := s.db.Order("created_at DESC").Limit(limit)
	if repositoryID != "" {
		query = query.Where("repository_uuid = ?", repositoryID)
	}
	err := query.Find(&rows).Error
	return rows, err
}

func (s *Store) ListBindingOperations(bindingID string, limit int, offsets ...int) ([]Operation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := 0
	if len(offsets) > 0 && offsets[0] > 0 && offsets[0] <= 100_000 {
		offset = offsets[0]
	}
	var rows []Operation
	err := s.db.Where("binding_uuid = ?", bindingID).Order("created_at DESC").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, err
}

func (s *Store) StartOperation(row *Operation) error { return s.db.Create(row).Error }

func (s *Store) SaveBackup(row *GitBackup) error { return s.db.Save(row).Error }

func (s *Store) GetBackup(id string) (GitBackup, error) {
	var row GitBackup
	err := s.db.Where("uuid = ?", id).First(&row).Error
	return row, err
}

func (s *Store) ListBindingBackups(bindingID string, limit int) ([]GitBackup, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows []GitBackup
	err := s.db.Where("binding_uuid = ?", bindingID).Order("created_at DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (s *Store) DeleteBackup(id string) error {
	result := s.db.Where("uuid = ?", id).Delete(&GitBackup{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (s *Store) DeleteBindingBackups(bindingID string) error {
	return s.db.Where("binding_uuid = ?", bindingID).Delete(&GitBackup{}).Error
}

func (s *Store) ExpiredBackups(cutoff time.Time) ([]GitBackup, error) {
	var rows []GitBackup
	err := s.db.Where("expires_at IS NOT NULL AND expires_at < ?", cutoff).Order("created_at ASC").Find(&rows).Error
	return rows, err
}

func (s *Store) PruneGitHistory(cutoff time.Time) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("created_at < ?", cutoff).Delete(&Operation{}).Error; err != nil {
			return err
		}
		return tx.Where("created_at < ?", cutoff).Delete(&Deployment{}).Error
	})
}

func (s *Store) FinishOperation(id, state, message string) error {
	now := time.Now().UTC()
	return s.db.Model(&Operation{}).Where("uuid = ?", id).Updates(map[string]any{
		"state": state, "error_message": message, "finished_at": &now,
	}).Error
}

func (s *Store) MarkInterruptedOperations() (int64, error) {
	now := time.Now().UTC()
	result := s.db.Model(&Operation{}).
		Where("state IN ?", []string{"queued", "running"}).
		Updates(map[string]any{
			"state": "failed", "error_message": "Dockman restarted while the operation was in progress", "finished_at": &now,
		})
	return result.RowsAffected, result.Error
}

func (s *Store) MarkInterruptedDeployments() (int64, error) {
	result := s.db.Model(&Deployment{}).Where("state IN ?", []string{"validating", "dry_run", "deploying"}).Updates(map[string]any{
		"state": "failed", "result": "Dockman stopped before the controlled deployment completed",
	})
	return result.RowsAffected, result.Error
}

func isNotFound(err error) bool { return errors.Is(err, gorm.ErrRecordNotFound) }

// RenameBindingHost re-points every folder link of one host at a new name.
//
// This deliberately bypasses SaveBinding's immutability guard, and it is the
// only thing allowed to. That guard exists because changing an endpoint turns
// a link into a different synchronization contract - it could silently move a
// repository-root link into a generated folder. A host rename changes none of
// that: the same machine, the same directory, the same Git destination, under
// a new label. Refusing it is what left links pointing at a name nothing
// answered to, with unlink-and-relink and a full baseline rebuild as the only
// way out.
//
// Both names are required and must differ, so this can never be used to blank
// a host out. It returns how many links were rewritten.
func (s *Store) RenameBindingHost(previousName, newName string) (int, error) {
	previousName, newName = strings.TrimSpace(previousName), strings.TrimSpace(newName)
	if previousName == "" || newName == "" {
		return 0, errors.New("both the previous and the new host name are required")
	}
	if previousName == newName {
		return 0, nil
	}
	result := s.db.Model(&StackBinding{}).
		Where("host = ?", previousName).
		Update("host", newName)
	if result.Error != nil {
		return 0, result.Error
	}
	return int(result.RowsAffected), nil
}
