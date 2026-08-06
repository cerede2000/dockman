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
	require.NoError(t, db.AutoMigrate(&Credential{}, &Repository{}, &StackBinding{}, &BindingBaseline{}, &Operation{}, &GitBackup{}, &Deployment{}, &GitStackStatus{}, &RepositoryWebhook{}, &WebhookDelivery{}))
	vault, err := NewVault(bytes.Repeat([]byte{0x13}, 32))
	require.NoError(t, err)
	return NewService(enabled, NewStore(db), vault, filepath.Join(t.TempDir(), "repositories")), db
}

func requireGitFileContent(t *testing.T, repo *gitclient.Repository, branch, name, expected string) {
	t.Helper()
	tree, err := repositoryCommitTree(repo, branch)
	require.NoError(t, err)
	file, err := tree.File(filepath.ToSlash(name))
	require.NoError(t, err)
	contents, err := file.Contents()
	require.NoError(t, err)
	require.Equal(t, expected, contents)
}

func compactTestCheckout(t *testing.T, repo *gitclient.Repository, branch string) (*gitclient.Repository, string, func()) {
	t.Helper()
	temporary, path, cleanup, err := temporaryRepositoryWorktree(repo, t.TempDir())
	require.NoError(t, err)
	worktree, err := temporary.Worktree()
	require.NoError(t, err)
	require.NoError(t, worktree.Checkout(&gitclient.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(branch), Force: true}))
	return temporary, path, cleanup
}

func TestAutomationRecoveryEmitsSuccessNotification(t *testing.T) {
	service, _ := testService(t, true)
	binding := StackBinding{
		UUID: uuid.NewString(), RepositoryUUID: uuid.NewString(), Host: "local", StackPath: "compose/demo",
		SubPath: "demo", Enabled: true, AutoSyncEnabled: true, AutoSyncState: "up_to_date",
	}
	require.NoError(t, service.store.SaveBinding(&binding))

	var events []AutomationEvent
	service.ConfigureEventNotifier(func(event AutomationEvent) { events = append(events, event) })
	previous := binding
	previous.AutoSyncState = "error"
	previous.AutoSyncError = "temporary fetch failure"
	service.notifyAutomationResult(binding.UUID, AutoSyncResult{
		BindingID: binding.UUID, State: "up_to_date", Message: "No synchronization change detected",
	}, nil, false, &previous)

	require.Len(t, events, 1)
	require.Equal(t, "git.sync.success", events[0].Kind)
}

func TestFolderLinkTargetCannotChangeDuringLaterSaves(t *testing.T) {
	service, _ := testService(t, true)
	binding := StackBinding{
		UUID: uuid.NewString(), RepositoryUUID: uuid.NewString(), Host: "local",
		StackPath: "compose", SubPath: ".", Enabled: true,
	}
	require.NoError(t, service.store.SaveBinding(&binding))

	// Normal policy/runtime updates keep a repository-root target intact.
	binding.SyncProfile = syncProfileComposeOnly
	binding.AutoSyncState = "watching"
	require.NoError(t, service.store.SaveBinding(&binding))
	stored, err := service.store.GetBinding(binding.UUID)
	require.NoError(t, err)
	require.Equal(t, ".", stored.SubPath)

	// A stale form/default must not be able to move it to stacks/compose.
	binding.SubPath = "stacks/compose"
	err = service.store.SaveBinding(&binding)
	require.ErrorContains(t, err, "folder link target is immutable")
	stored, err = service.store.GetBinding(binding.UUID)
	require.NoError(t, err)
	require.Equal(t, ".", stored.SubPath)
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
	workspace, err := service.repositoryPath(row.UUID)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(workspace, ".git", compactStorageMarker))
	require.NoFileExists(t, filepath.Join(workspace, "README.md"))

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

	local, err := gitclient.PlainOpen(workspace)
	require.NoError(t, err)
	temporary, temporaryPath, cleanup := compactTestCheckout(t, local, row.DefaultBranch)
	commitTestFile(t, temporary, temporaryPath, "local.txt", "local change")
	cleanup()
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

func TestResetRepositoryToRemoteRecoversDivergedHistory(t *testing.T) {
	service, _ := testService(t, true)
	remotePath, _ := createTestRemote(t)
	row := Repository{
		UUID: uuid.NewString(), Name: "diverged-repository", Provider: "test", RemoteURL: remotePath,
		DefaultBranch: "main", Mode: "managed", Status: "cloning",
	}
	require.NoError(t, service.store.SaveRepository(&row))
	require.NoError(t, service.cloneRepository(context.Background(), row))
	row.Status = "ready"
	require.NoError(t, service.store.SaveRepository(&row))

	workspace, err := service.repositoryPath(row.UUID)
	require.NoError(t, err)
	local, err := gitclient.PlainOpen(workspace)
	require.NoError(t, err)
	temporary, temporaryPath, cleanup := compactTestCheckout(t, local, row.DefaultBranch)
	commitTestFile(t, temporary, temporaryPath, "local-only.txt", "discard me")
	cleanup()

	externalPath := t.TempDir()
	external, err := gitclient.PlainClone(externalPath, false, &gitclient.CloneOptions{
		URL: remotePath, ReferenceName: plumbing.NewBranchReferenceName("main"), SingleBranch: true,
	})
	require.NoError(t, err)
	commitTestFile(t, external, externalPath, "remote-only.txt", "keep me")
	require.NoError(t, external.Push(&gitclient.PushOptions{}))

	status, err := service.FetchRepository(context.Background(), row.UUID)
	require.NoError(t, err)
	require.True(t, status.Diverged)
	require.Positive(t, status.Ahead)
	require.Positive(t, status.Behind)

	status, err = service.ResetRepositoryToRemote(context.Background(), row.UUID)
	require.NoError(t, err)
	require.Equal(t, "up-to-date", status.State)
	require.Zero(t, status.Ahead)
	require.Zero(t, status.Behind)
	require.Equal(t, status.RemoteHead, status.Head)
	local, err = service.openRepository(row)
	require.NoError(t, err)
	requireGitFileContent(t, local, row.DefaultBranch, "remote-only.txt", "keep me")
	tree, err := repositoryCommitTree(local, row.DefaultBranch)
	require.NoError(t, err)
	_, err = tree.File("local-only.txt")
	require.Error(t, err)
}

func TestInspectAndCreateMissingRemoteBranch(t *testing.T) {
	service, _ := testService(t, true)
	remotePath, _ := createTestRemote(t)
	row := Repository{
		UUID: uuid.NewString(), Name: "missing-branch", Provider: "test", RemoteURL: remotePath,
		DefaultBranch: "dockman", Mode: "managed", Status: "cloning",
	}

	state, err := service.inspectRemoteBranch(context.Background(), row)
	require.NoError(t, err)
	require.False(t, state.exists)
	require.Equal(t, "main", state.sourceBranch)

	require.NoError(t, service.createRemoteBranchFromDefault(context.Background(), row, state.sourceBranch))
	state, err = service.inspectRemoteBranch(context.Background(), row)
	require.NoError(t, err)
	require.True(t, state.exists)

	checkout := t.TempDir()
	_, err = gitclient.PlainClone(checkout, false, &gitclient.CloneOptions{
		URL: remotePath, ReferenceName: plumbing.NewBranchReferenceName("dockman"), SingleBranch: true,
	})
	require.NoError(t, err)
	contents, err := os.ReadFile(filepath.Join(checkout, "README.md"))
	require.NoError(t, err)
	require.Equal(t, "test repository", string(contents))
}

func TestCreateIndependentEmptyRemoteBranch(t *testing.T) {
	service, _ := testService(t, true)
	remotePath, seedPath := createTestRemote(t)
	seed, err := gitclient.PlainOpen(seedPath)
	require.NoError(t, err)
	mainReference, err := seed.Reference(plumbing.NewBranchReferenceName("main"), true)
	require.NoError(t, err)
	row := Repository{
		UUID: uuid.NewString(), Name: "empty-branch", Provider: "test", RemoteURL: remotePath,
		DefaultBranch: "dockman-empty", Mode: "managed", Status: "cloning",
	}

	require.NoError(t, service.createEmptyRemoteBranch(context.Background(), row))
	state, err := service.inspectRemoteBranch(context.Background(), row)
	require.NoError(t, err)
	require.True(t, state.exists)

	checkout := t.TempDir()
	repository, err := gitclient.PlainClone(checkout, false, &gitclient.CloneOptions{
		URL: remotePath, ReferenceName: plumbing.NewBranchReferenceName("dockman-empty"), SingleBranch: true,
	})
	require.NoError(t, err)
	emptyReference, err := repository.Reference(plumbing.NewBranchReferenceName("dockman-empty"), true)
	require.NoError(t, err)
	require.NotEqual(t, mainReference.Hash(), emptyReference.Hash())
	commit, err := repository.CommitObject(emptyReference.Hash())
	require.NoError(t, err)
	require.Equal(t, 0, commit.NumParents())
	tree, err := commit.Tree()
	require.NoError(t, err)
	require.Empty(t, tree.Entries)
	entries, err := os.ReadDir(checkout)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, ".git", entries[0].Name())
}

func TestRemoteDefaultBranchPrefersRemoteHead(t *testing.T) {
	mainHash := plumbing.NewHash("1111111111111111111111111111111111111111")
	developHash := plumbing.NewHash("2222222222222222222222222222222222222222")
	require.Equal(t, "develop", remoteDefaultBranch(map[string]plumbing.Hash{
		"main": mainHash, "develop": developHash,
	}, developHash))
	require.Equal(t, "main", remoteDefaultBranch(map[string]plumbing.Hash{
		"main": mainHash, "release": mainHash,
	}, mainHash))
	require.Empty(t, remoteDefaultBranch(nil, plumbing.ZeroHash))
}

func TestRepositoryInputRejectsInvalidGitReference(t *testing.T) {
	service, _ := testService(t, true)
	for _, branch := range []string{"release/", "release//next", "release.lock", "feature."} {
		_, err := service.validateRepositoryInput(RepositoryInput{
			Name: "repository", RemoteURL: "owner/repository", DefaultBranch: branch,
		})
		require.ErrorContains(t, err, "invalid default branch name", branch)
	}
	_, err := service.validateRepositoryInput(RepositoryInput{
		Name: "repository", RemoteURL: "owner/repository", DefaultBranch: "main", BranchCreationMode: "unknown",
	})
	require.ErrorContains(t, err, "invalid branch creation mode")
}

func TestRepositoryPullRefusesDirtyWorkspace(t *testing.T) {
	service, _ := testService(t, true)
	remotePath, _ := createTestRemote(t)
	row := Repository{UUID: uuid.NewString(), Name: "dirty", Provider: "test", RemoteURL: remotePath, DefaultBranch: "main", Mode: "managed", Status: "ready"}
	require.NoError(t, service.store.SaveRepository(&row))
	workspace, err := service.repositoryPath(row.UUID)
	require.NoError(t, err)
	_, err = gitclient.PlainClone(workspace, false, &gitclient.CloneOptions{URL: remotePath, ReferenceName: plumbing.NewBranchReferenceName("main"), SingleBranch: true})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "dirty.txt"), []byte("not committed"), 0o600))
	_, err = service.PullRepository(context.Background(), row.UUID)
	require.ErrorContains(t, err, "migration refused")
	require.FileExists(t, filepath.Join(workspace, "dirty.txt"))
}

func TestCompactMigrationPreservesIgnoredUntrackedData(t *testing.T) {
	service, _ := testService(t, true)
	remotePath, seedPath := createTestRemote(t)
	seed, err := gitclient.PlainOpen(seedPath)
	require.NoError(t, err)
	commitTestFile(t, seed, seedPath, ".gitignore", "ignored.tmp\n")
	require.NoError(t, seed.Push(&gitclient.PushOptions{}))
	row := Repository{UUID: uuid.NewString(), Name: "ignored", Provider: "test", RemoteURL: remotePath, DefaultBranch: "main", Mode: "managed", Status: "ready"}
	require.NoError(t, service.store.SaveRepository(&row))
	workspace, err := service.repositoryPath(row.UUID)
	require.NoError(t, err)
	_, err = gitclient.PlainClone(workspace, false, &gitclient.CloneOptions{URL: remotePath, ReferenceName: plumbing.NewBranchReferenceName("main"), SingleBranch: true})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "ignored.tmp"), []byte("must survive"), 0o600))

	_, err = service.openRepository(row)
	require.ErrorContains(t, err, "untracked or ignored data")
	require.FileExists(t, filepath.Join(workspace, "ignored.tmp"))
	require.NoFileExists(t, filepath.Join(workspace, ".git", compactStorageMarker))
}

func TestLegacyRepositoryMigratesWithoutLosingGitData(t *testing.T) {
	service, _ := testService(t, true)
	remotePath, _ := createTestRemote(t)
	row := Repository{UUID: uuid.NewString(), Name: "legacy", Provider: "test", RemoteURL: remotePath, DefaultBranch: "main", Mode: "managed", Status: "ready"}
	require.NoError(t, service.store.SaveRepository(&row))
	workspace, err := service.repositoryPath(row.UUID)
	require.NoError(t, err)
	_, err = gitclient.PlainClone(workspace, false, &gitclient.CloneOptions{URL: remotePath, ReferenceName: plumbing.NewBranchReferenceName("main"), SingleBranch: true})
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(workspace, "README.md"))

	repo, err := service.openRepository(row)
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(workspace, "README.md"))
	require.FileExists(t, filepath.Join(workspace, ".git", compactStorageMarker))
	requireGitFileContent(t, repo, "main", "README.md", "test repository")
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

func TestBindingBaselineIsReplacedAndRemovedWithBinding(t *testing.T) {
	service, _ := testService(t, true)
	binding := StackBinding{
		UUID: uuid.NewString(), RepositoryUUID: uuid.NewString(), Host: "local",
		StackPath: "compose/test", SubPath: "stacks/test", Enabled: true,
	}
	require.NoError(t, service.store.SaveBinding(&binding))
	require.NoError(t, service.store.ReplaceBindingBaseline(binding.UUID, map[string]string{
		"compose.yaml": "first",
		"config.json":  "second",
	}))

	baseline, err := service.store.BindingBaseline(binding.UUID)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"compose.yaml": "first", "config.json": "second"}, baseline)

	require.NoError(t, service.store.ReplaceBindingBaseline(binding.UUID, map[string]string{"compose.yaml": "updated"}))
	baseline, err = service.store.BindingBaseline(binding.UUID)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"compose.yaml": "updated"}, baseline)

	require.NoError(t, service.store.DeleteBinding(binding.UUID, true))
	baseline, err = service.store.BindingBaseline(binding.UUID)
	require.NoError(t, err)
	require.Empty(t, baseline)
}

func TestBindingBaselineSurvivesUnlinkAndRestore(t *testing.T) {
	service, _ := testService(t, true)
	binding := StackBinding{
		UUID: uuid.NewString(), RepositoryUUID: uuid.NewString(), Host: "local",
		StackPath: "compose/restorable", SubPath: "stacks/restorable", Enabled: true,
	}
	require.NoError(t, service.store.SaveBinding(&binding))
	require.NoError(t, service.store.ReplaceBindingBaseline(binding.UUID, map[string]string{"compose.yaml": "baseline"}))
	require.NoError(t, service.store.DeleteBinding(binding.UUID, false))

	_, err := service.store.GetBinding(binding.UUID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	archived, err := service.store.ArchivedBinding(binding.Host, binding.StackPath)
	require.NoError(t, err)
	require.Equal(t, binding.UUID, archived.UUID)
	require.NoError(t, service.store.RestoreBinding(&archived))

	baseline, err := service.store.BindingBaseline(binding.UUID)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"compose.yaml": "baseline"}, baseline)
}

func TestValidateGitHubRepositoryURL(t *testing.T) {
	for _, value := range []string{
		"https://github.com/owner/repository.git",
		"https://github.com/owner/repository",
		"owner/repository",
		"git@github.com:owner/repository.git",
		"git@github.com:owner/repository",
		"ssh://git@github.com/owner/repository.git",
		"ssh://git@github.com/owner/repository",
		"ssh://git@github.com:22/owner/repository.git",
	} {
		require.NoError(t, validateGitHubURL(value, true), value)
	}
	for _, value := range []string{
		"https://token@github.com/owner/repository.git",
		"https://github.com:8443/owner/repository.git",
		"https://api.github.com/owner/repository.git",
		"https://github.com/owner/nested/repository.git",
		"https://github.com/owner/repository.git?token=secret",
		"https://github.com/owner%2Frepository.git",
		"git@github.com:owner/nested/repository.git",
		"ssh://git@github.com:2222/owner/repository.git",
	} {
		require.Error(t, validateGitHubURL(value, true), value)
	}
}

func TestNormalizeGitHubRepositoryURL(t *testing.T) {
	tests := map[string]string{
		"owner/repository":                        "https://github.com/owner/repository.git",
		"https://github.com/owner/repository":     "https://github.com/owner/repository.git",
		"https://github.com/owner/repository.git": "https://github.com/owner/repository.git",
		"git@github.com:owner/repository":         "git@github.com:owner/repository.git",
		"ssh://git@github.com/owner/repository":   "ssh://git@github.com/owner/repository.git",
	}
	for input, expected := range tests {
		actual, err := normalizeGitHubURL(input, true)
		require.NoError(t, err, input)
		require.Equal(t, expected, actual, input)
	}
	sshShorthand, err := normalizeGitHubURL("owner/repository", true, true)
	require.NoError(t, err)
	require.Equal(t, "git@github.com:owner/repository.git", sshShorthand)
}

func TestDuplicateRepositoryIdentityIsRejectedPerBranch(t *testing.T) {
	service, _ := testService(t, true)
	require.NoError(t, service.store.SaveRepository(&Repository{
		UUID: uuid.NewString(), Name: "existing", Provider: "github",
		RemoteURL: "git@github.com:Owner/Repository.git", DefaultBranch: "main", Mode: "managed", Status: "ready",
	}))
	identity, err := githubRepositoryIdentity("https://github.com/owner/repository")
	require.NoError(t, err)
	require.ErrorContains(t, service.ensureRepositoryUnique(identity, "main"), "already registered")
	require.NoError(t, service.ensureRepositoryUnique(identity, "develop"))
}

func TestUpdateRepositoryPolicyConfiguresIdentityAndPreservesItForLegacyClients(t *testing.T) {
	service, _ := testService(t, true)
	repository := Repository{
		UUID: uuid.NewString(), Name: "policy", Provider: "test", RemoteURL: "https://github.com/example/policy.git",
		DefaultBranch: "main", CommitAuthorName: "Dockman Git Sync", CommitAuthorEmail: "dockman@localhost.invalid",
		Mode: "managed", Status: "ready",
	}
	require.NoError(t, service.store.SaveRepository(&repository))
	name, email := "Production Dockman", "dockman@example.test"
	view, err := service.UpdateRepositoryPolicy(repository.UUID, RepositoryPolicyInput{
		ExcludePatterns: []string{"/README.md"}, CommitAuthorName: &name, CommitAuthorEmail: &email,
	})
	require.NoError(t, err)
	require.Equal(t, name, view.CommitAuthorName)
	require.Equal(t, email, view.CommitAuthorEmail)

	view, err = service.UpdateRepositoryPolicy(repository.UUID, RepositoryPolicyInput{ExcludePatterns: []string{"/docs/**"}})
	require.NoError(t, err)
	require.Equal(t, name, view.CommitAuthorName)
	require.Equal(t, email, view.CommitAuthorEmail)
	require.Equal(t, []string{"/docs/**"}, view.ExcludePatterns)
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
