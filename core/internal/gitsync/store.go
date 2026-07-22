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

func (s *Store) SaveBinding(row *StackBinding) error { return s.db.Save(row).Error }

func (s *Store) DeleteBinding(id string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Unscoped().Where("uuid = ?", id).Delete(&StackBinding{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return tx.Where("binding_uuid = ?", id).Delete(&BindingBaseline{}).Error
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

func isNotFound(err error) bool { return errors.Is(err, gorm.ErrRecordNotFound) }
