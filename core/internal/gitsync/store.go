package gitsync

import (
	"errors"
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

func (s *Store) SaveRepository(row *Repository) error { return s.db.Save(row).Error }

func (s *Store) DeleteRepository(id string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var bindingIDs []string
		if err := tx.Unscoped().Model(&StackBinding{}).Where("repository_uuid = ?", id).Pluck("uuid", &bindingIDs).Error; err != nil {
			return err
		}
		if len(bindingIDs) > 0 {
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

func (s *Store) GetBinding(id string) (StackBinding, error) {
	var row StackBinding
	err := s.db.Where("uuid = ?", id).First(&row).Error
	return row, err
}

func (s *Store) ListAutoSyncBindings() ([]StackBinding, error) {
	var rows []StackBinding
	err := s.db.Where("auto_sync_enabled = ?", true).
		Order("last_auto_sync_at ASC, created_at ASC").Find(&rows).Error
	return rows, err
}

func (s *Store) SaveBinding(row *StackBinding) error { return s.db.Save(row).Error }

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
		updates["last_auto_sync_commit"] = commit
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

func (s *Store) StartOperation(row *Operation) error { return s.db.Create(row).Error }

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
