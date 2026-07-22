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
