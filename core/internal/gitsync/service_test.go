package gitsync

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	gitclient "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/uuid"
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
	return NewService(enabled, NewStore(db), vault, filepath.Join(t.TempDir(), "repositories")), db
}

func TestRepositoryManualFetchPullAndPush(t *testing.T) {
	service, _ := testService(t, true)
	remotePath, _ := createTestRemote(t)
	row := Repository{
		UUID: uuid.NewString(), Name: "test-repository", Provider: "test", RemoteURL: remotePath,
		DefaultBranch: "main", Mode: "managed", Status: "cloning",
	}
	require.NoError(t, service.store.SaveRepository(&row))
	require.NoError(t, service.cloneRepository(context.Background(), row))
	row.Status = "ready"
	require.NoError(t, service.store.SaveRepository(&row))

	status, err := service.RepositoryStatus(row.UUID)
	require.NoError(t, err)
	require.Equal(t, "up-to-date", status.State)
	require.True(t, status.Clean)

	externalPath := t.TempDir()
	external, err := gitclient.PlainClone(externalPath, false, &gitclient.CloneOptions{
		URL: remotePath, ReferenceName: plumbing.NewBranchReferenceName("main"), SingleBranch: true,
	})
	require.NoError(t, err)
	commitTestFile(t, external, externalPath, "remote.txt", "remote change")
	require.NoError(t, external.Push(&gitclient.PushOptions{}))

	status, err = service.FetchRepository(context.Background(), row.UUID)
	require.NoError(t, err)
	require.Equal(t, "behind", status.State)
	require.Equal(t, 1, status.Behind)

	status, err = service.PullRepository(context.Background(), row.UUID)
	require.NoError(t, err)
	require.Equal(t, "up-to-date", status.State)

	workspace, err := service.repositoryPath(row.UUID)
	require.NoError(t, err)
	local, err := gitclient.PlainOpen(workspace)
	require.NoError(t, err)
	commitTestFile(t, local, workspace, "local.txt", "local change")
	status, err = service.RepositoryStatus(row.UUID)
	require.NoError(t, err)
	require.Equal(t, "ahead", status.State)

	status, err = service.PushRepository(context.Background(), row.UUID)
	require.NoError(t, err)
	require.Equal(t, "up-to-date", status.State)

	checkPath := t.TempDir()
	_, err = gitclient.PlainClone(checkPath, false, &gitclient.CloneOptions{
		URL: remotePath, ReferenceName: plumbing.NewBranchReferenceName("main"), SingleBranch: true,
	})
	require.NoError(t, err)
	contents, err := os.ReadFile(filepath.Join(checkPath, "local.txt"))
	require.NoError(t, err)
	require.Equal(t, "local change", string(contents))
}

func TestRepositoryPullRefusesDirtyWorkspace(t *testing.T) {
	service, _ := testService(t, true)
	remotePath, _ := createTestRemote(t)
	row := Repository{UUID: uuid.NewString(), Name: "dirty", Provider: "test", RemoteURL: remotePath, DefaultBranch: "main", Mode: "managed", Status: "ready"}
	require.NoError(t, service.store.SaveRepository(&row))
	require.NoError(t, service.cloneRepository(context.Background(), row))
	workspace, err := service.repositoryPath(row.UUID)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "dirty.txt"), []byte("not committed"), 0o600))
	_, err = service.PullRepository(context.Background(), row.UUID)
	require.ErrorContains(t, err, "uncommitted changes")
}

func TestRepositoryPathAndDeleteAreBounded(t *testing.T) {
	service, db := testService(t, true)
	_, err := service.repositoryPath("../../escape")
	require.Error(t, err)
	row := Repository{UUID: uuid.NewString(), Name: "delete-me", Provider: "test", RemoteURL: "https://github.com/example/example.git", DefaultBranch: "main", Mode: "managed", Status: "ready"}
	require.NoError(t, service.store.SaveRepository(&row))
	path, err := service.repositoryPath(row.UUID)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(path, 0o700))
	require.NoError(t, service.DeleteRepository(row.UUID))
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
	var count int64
	require.NoError(t, db.Unscoped().Model(&Repository{}).Where("uuid = ?", row.UUID).Count(&count).Error)
	require.Zero(t, count)
}

func TestRepositoryWorkspaceSymlinkIsRefused(t *testing.T) {
	service, _ := testService(t, true)
	row := Repository{UUID: uuid.NewString(), Name: "linked", Provider: "test", RemoteURL: "https://github.com/example/example.git", DefaultBranch: "main", Mode: "managed", Status: "ready"}
	require.NoError(t, service.store.SaveRepository(&row))
	path, err := service.repositoryPath(row.UUID)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.Symlink(t.TempDir(), path))
	_, err = service.openRepository(row)
	require.ErrorContains(t, err, "symbolic link")
}

func TestValidateGitHubRepositoryURL(t *testing.T) {
	for _, value := range []string{
		"https://github.com/owner/repository.git",
		"git@github.com:owner/repository.git",
		"ssh://git@github.com/owner/repository.git",
		"ssh://git@github.com:22/owner/repository.git",
	} {
		require.NoError(t, validateGitHubURL(value, true), value)
	}
	for _, value := range []string{
		"https://token@github.com/owner/repository.git",
		"https://github.com:8443/owner/repository.git",
		"https://api.github.com/owner/repository.git",
		"https://github.com/owner/repository",
		"https://github.com/owner/nested/repository.git",
		"https://github.com/owner/repository.git?token=secret",
		"https://github.com/owner%2Frepository.git",
		"git@github.com:owner/nested/repository.git",
		"ssh://git@github.com:2222/owner/repository.git",
	} {
		require.Error(t, validateGitHubURL(value, true), value)
	}
}

func createTestRemote(t *testing.T) (remotePath, seedPath string) {
	t.Helper()
	remotePath = filepath.Join(t.TempDir(), "remote.git")
	_, err := gitclient.PlainInit(remotePath, true)
	require.NoError(t, err)
	seedPath = t.TempDir()
	seed, err := gitclient.PlainInitWithOptions(seedPath, &gitclient.PlainInitOptions{InitOptions: gitclient.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")}})
	require.NoError(t, err)
	commitTestFile(t, seed, seedPath, "README.md", "test repository")
	_, err = seed.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{remotePath}})
	require.NoError(t, err)
	require.NoError(t, seed.Push(&gitclient.PushOptions{RefSpecs: []config.RefSpec{"refs/heads/main:refs/heads/main"}}))
	return remotePath, seedPath
}

func commitTestFile(t *testing.T, repo *gitclient.Repository, root, name, contents string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(contents), 0o600))
	worktree, err := repo.Worktree()
	require.NoError(t, err)
	_, err = worktree.Add(name)
	require.NoError(t, err)
	_, err = worktree.Commit("test commit", &gitclient.CommitOptions{Author: &object.Signature{Name: "Dockman Test", Email: "dockman-test@example.invalid", When: time.Now().UTC()}})
	require.NoError(t, err)
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
