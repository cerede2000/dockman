package gitsync

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func testService(t *testing.T, enabled bool) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Credential{}, &Repository{}, &StackBinding{}, &Operation{}, &Deployment{}))
	vault, err := NewVault(bytes.Repeat([]byte{0x13}, 32))
	require.NoError(t, err)
	return NewService(enabled, NewStore(db), vault), db
}

func TestCredentialSecretEncryptedAndNeverReturned(t *testing.T) {
	service, db := testService(t, true)
	view, err := service.CreateCredential(CredentialInput{Name: "github", AuthType: AuthHTTPSToken, Token: "test-token-plaintext"})
	require.NoError(t, err)
	require.True(t, view.HasSecret)
	require.NotContains(t, view.SecretHint, "plaintext")

	var stored Credential
	require.NoError(t, db.Where("uuid = ?", view.ID).First(&stored).Error)
	require.NotContains(t, string(stored.EncryptedPayload), "test-token-plaintext")

	updated, err := service.UpdateCredential(view.ID, CredentialInput{Name: "github-renamed", AuthType: AuthHTTPSToken})
	require.NoError(t, err)
	require.Equal(t, "github-renamed", updated.Name)
	payload, err := service.decryptPayload(storedCredential(t, db, view.ID))
	require.NoError(t, err)
	require.Equal(t, "test-token-plaintext", payload.Token, "empty update must preserve the encrypted secret")
}

func TestPublicCredentialHasNoSecret(t *testing.T) {
	service, _ := testService(t, true)
	view, err := service.CreateCredential(CredentialInput{Name: "public", AuthType: AuthPublic, Token: "must-be-discarded"})
	require.NoError(t, err)
	require.False(t, view.HasSecret)
}

func TestDeleteCredentialPurgesSecretRow(t *testing.T) {
	service, db := testService(t, true)
	view, err := service.CreateCredential(CredentialInput{Name: "temporary", AuthType: AuthHTTPSToken, Token: "remove-me"})
	require.NoError(t, err)
	require.NoError(t, service.DeleteCredential(view.ID))
	var count int64
	require.NoError(t, db.Unscoped().Model(&Credential{}).Where("uuid = ?", view.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestInterruptedOperationsAreRecovered(t *testing.T) {
	service, db := testService(t, true)
	now := time.Now().UTC()
	require.NoError(t, db.Create(&Operation{UUID: "running", OperationType: "fetch", State: "running", StartedAt: &now}).Error)
	require.NoError(t, db.Create(&Operation{UUID: "queued", OperationType: "pull", State: "queued"}).Error)
	require.NoError(t, db.Create(&Operation{UUID: "done", OperationType: "push", State: "success", FinishedAt: &now}).Error)

	count, err := service.RecoverInterruptedOperations()
	require.NoError(t, err)
	require.EqualValues(t, 2, count)

	var rows []Operation
	require.NoError(t, db.Order("uuid").Find(&rows).Error)
	for _, row := range rows {
		if row.UUID == "done" {
			require.Equal(t, "success", row.State)
			continue
		}
		require.Equal(t, "failed", row.State)
		require.Contains(t, row.ErrorMessage, "restarted")
		require.NotNil(t, row.FinishedAt)
	}
}

func TestRepositoryOperationsArePersisted(t *testing.T) {
	service, db := testService(t, true)
	want := errors.New("fetch failed")
	err := service.RunRepositoryOperation(context.Background(), "repo-1", "fetch", func(context.Context) error { return want })
	require.ErrorIs(t, err, want)
	var row Operation
	require.NoError(t, db.First(&row).Error)
	require.Equal(t, "failed", row.State)
	require.Equal(t, want.Error(), row.ErrorMessage)
}

func storedCredential(t *testing.T, db *gorm.DB, id string) Credential {
	t.Helper()
	var row Credential
	require.NoError(t, db.Where("uuid = ?", id).First(&row).Error)
	return row
}
