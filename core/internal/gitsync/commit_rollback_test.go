package gitsync

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	gitclient "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/require"
)

func TestCommitRollbackPreviewsAndRestoresLocallyWithoutDeployment(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	composePath := filepath.Join(stackRoot, "app", "compose.yml")
	configPath := filepath.Join(stackRoot, "app", "config.yml")
	require.NoError(t, os.MkdirAll(filepath.Dir(composePath), 0o755))
	require.NoError(t, os.WriteFile(composePath, []byte("services:\n  app:\n    image: alpine:3.22\n"), 0o644))
	require.NoError(t, os.WriteFile(configPath, []byte("version: one\n"), 0o644))
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks"})
	require.NoError(t, err)
	establishBindingBaseline(t, service, binding.ID)

	repo, err := service.openRepository(repository)
	require.NoError(t, err)
	oldReference, err := repo.Reference(plumbing.NewBranchReferenceName(repository.DefaultBranch), true)
	require.NoError(t, err)
	oldCommit := oldReference.Hash().String()
	temporary, temporaryPath, cleanup := compactTestCheckout(t, repo, repository.DefaultBranch)
	commitTestFile(t, temporary, temporaryPath, "stacks/app/compose.yml", "services:\n  app:\n    image: alpine:3.23\n")
	commitTestFile(t, temporary, temporaryPath, "stacks/app/config.yml", "version: two\n")
	commitTestFile(t, temporary, temporaryPath, "stacks/app/new.yml", "created: true\n")
	cleanup()
	require.NoError(t, repo.Push(&gitclient.PushOptions{}))
	importPreview, err := service.PreviewBinding(binding.ID, "repository_to_stack", TransferInput{})
	require.NoError(t, err)
	_, err = service.ImportBinding(context.Background(), binding.ID, TransferInput{PreviewToken: importPreview.PreviewToken})
	require.NoError(t, err)

	deployCalls := 0
	service.ConfigureDeployment(
		func(context.Context, string, string) error { deployCalls++; return nil },
		func(context.Context, string, string, io.Writer) error { deployCalls++; return nil },
		func(context.Context, string, string, io.Writer) error { deployCalls++; return nil },
		func(context.Context, string, string, io.Writer) error { deployCalls++; return nil },
		func(context.Context, string, string, io.Writer) error { deployCalls++; return nil },
		func(string, string) (func(), bool) { return func() {}, true },
	)
	commits, err := service.ListBindingCommits(binding.ID, 20)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(commits), 2)
	require.Contains(t, commitSHAs(commits), oldCommit)

	preview, err := service.PreviewCommitRollback(binding.ID, CommitRollbackInput{CommitSHA: oldCommit})
	require.NoError(t, err)
	require.Equal(t, []string{"app/compose.yml"}, preview.ComposePaths)
	require.Equal(t, 3, preview.Changed)
	require.Equal(t, 2, preview.Restores)
	require.Equal(t, 1, preview.Removals)
	require.Empty(t, preview.ComposeErrors)
	comparison, err := service.CompareCommitRollbackFile(binding.ID, CommitRollbackInput{CommitSHA: oldCommit}, "app/config.yml")
	require.NoError(t, err)
	require.True(t, comparison.Comparable)
	require.Contains(t, comparison.Dockman.Content, "two")
	require.Contains(t, comparison.Git.Content, "one")

	result, err := service.ApplyCommitRollback(context.Background(), binding.ID, CommitRollbackInput{
		CommitSHA: oldCommit, PreviewToken: preview.Token,
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.SafetyBackupID)
	require.Equal(t, []string{"app/compose.yml"}, result.PausedStacks)
	require.Zero(t, deployCalls)
	compose, err := os.ReadFile(composePath)
	require.NoError(t, err)
	require.Contains(t, string(compose), "alpine:3.22")
	config, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(config), "one")
	require.NoFileExists(t, filepath.Join(stackRoot, "app", "new.yml"))
	statuses, err := service.store.GitStackStatuses(binding.ID)
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	require.Equal(t, stackSyncLocalChanges, statuses[0].State)
	require.True(t, statuses[0].AutomationPaused)
	require.Equal(t, stackPauseRecovery, statuses[0].PauseReason)
	require.Contains(t, statuses[0].ErrorMessage, "Local rollback waiting")
	backup, err := service.store.GetBackup(result.SafetyBackupID)
	require.NoError(t, err)
	require.Equal(t, "pre_commit_rollback", backup.Kind)
	require.Equal(t, oldCommit, backup.CommitSHA)
}

func commitSHAs(commits []BindingCommitView) []string {
	result := make([]string, 0, len(commits))
	for _, commit := range commits {
		result = append(result, commit.SHA)
	}
	return result
}
