package registry

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

// Credential is one registry login. The secret never leaves this row in
// clear: it is sealed by the vault, under a scope bound to the row's UUID.
type Credential struct {
	gorm.Model
	UUID             string `gorm:"not null;uniqueIndex"`
	Registry         string `gorm:"not null;uniqueIndex"`
	Username         string `gorm:"not null"`
	EncryptedPayload []byte `gorm:"type:blob"`
	SecretHint       string
	LastTestedAt     *time.Time
	LastTestError    string
}

func (Credential) TableName() string { return "registry_credentials" }

type Store interface {
	List() ([]Credential, error)
	Get(uuid string) (Credential, error)
	GetByRegistry(host string) (Credential, error)
	Save(credential *Credential) error
	Delete(uuid string) error
}

type GormStore struct{ db *gorm.DB }

func NewStore(db *gorm.DB) *GormStore { return &GormStore{db: db} }

func (s *GormStore) List() ([]Credential, error) {
	var rows []Credential
	err := s.db.Order("registry asc").Find(&rows).Error
	return rows, err
}

func (s *GormStore) Get(uuid string) (Credential, error) {
	var row Credential
	err := s.db.Where("uuid = ?", uuid).First(&row).Error
	return row, err
}

func (s *GormStore) GetByRegistry(host string) (Credential, error) {
	var row Credential
	err := s.db.Where("registry = ?", NormalizeHost(host)).First(&row).Error
	return row, err
}

func (s *GormStore) Save(credential *Credential) error {
	return s.db.Save(credential).Error
}

// Delete purges the row instead of marking it deleted: it holds secret
// material, and a soft-deleted row would keep that material in the database
// while the credential is gone from every screen. The Git credentials are
// removed the same way.
func (s *GormStore) Delete(uuid string) error {
	result := s.db.Unscoped().Where("uuid = ?", uuid).Delete(&Credential{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func isNotFound(err error) bool { return errors.Is(err, gorm.ErrRecordNotFound) }
