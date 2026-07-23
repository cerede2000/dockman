package gitsync

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	gitclient "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/require"
)

func TestBindingAutomationConfigurationIsOptInAndBounded(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "compose.yaml"), []byte("services: {}\n"), 0o644))
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
	require.NoError(t, err)
	require.False(t, binding.AutoSyncEnabled)
	require.Equal(t, defaultAutoSyncIntervalMinutes, binding.AutoSyncIntervalMinutes)

	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, IntervalMinutes: 4})
	require.ErrorContains(t, err, "between 5 and 1440")
	updated, err := service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, IntervalMinutes: 10})
	require.NoError(t, err)
	require.True(t, updated.AutoSyncEnabled)
	require.Equal(t, "watching", updated.AutoSyncState)
	require.NoError(t, service.DeleteBinding(binding.ID, false))
	archived, err := service.store.ArchivedBinding("local", "compose/app")
	require.NoError(t, err)
	require.False(t, archived.AutoSyncEnabled, "relinking must never resume automation implicitly")
	require.Equal(t, "disabled", archived.AutoSyncState)
}

func TestAutoSyncReconcilesIdenticalTreesBeforeCommitShortcut(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	contents := "services: {}\n"
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "compose.yaml"), []byte(contents), 0o644))
	remoteChange(t, repository.RemoteURL, "stacks/app/compose.yaml", contents)
	autoReconcile := false
	binding, err := service.CreateBindingContext(context.Background(), BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app", AutoReconcile: &autoReconcile})
	require.NoError(t, err)
	require.Equal(t, "pending", binding.InitialSyncState)
	autoReconcile = true
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, AutoReconcile: &autoReconcile, IntervalMinutes: 5})
	require.NoError(t, err)

	result, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "up_to_date", result.State)
	require.Contains(t, result.Message, "baseline established automatically")
	baseline, err := service.store.BindingBaseline(binding.ID)
	require.NoError(t, err)
	require.NotEmpty(t, baseline["compose.yaml"])
}

func TestNextAutoSyncDelayTracksExactDeadline(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "compose.yaml"), []byte("services: {}\n"), 0o644))
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
	require.NoError(t, err)
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, IntervalMinutes: 5})
	require.NoError(t, err)
	now := time.Now().UTC()
	attempted := now.Add(-4*time.Minute - 50*time.Second)
	require.NoError(t, service.store.UpdateBindingAutoSyncState(binding.ID, "watching", "", "", &attempted, nil))
	delay := service.nextAutoSyncDelay(now)
	require.InDelta(t, 10*time.Second, delay, float64(100*time.Millisecond))
}

func TestBindingAutomationImportsRemoteChangesWithBackup(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	stackPath := filepath.Join(stackRoot, "app", "compose.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(stackPath), 0o755))
	require.NoError(t, os.WriteFile(stackPath, []byte("services:\n  app:\n    image: alpine:3.22\n"), 0o644))
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
	require.NoError(t, err)
	establishBindingBaseline(t, service, binding.ID)
	remoteChange(t, repository.RemoteURL, "stacks/app/compose.yaml", "services:\n  app:\n    image: alpine:3.23\n")
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, IntervalMinutes: 5})
	require.NoError(t, err)

	result, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "up_to_date", result.State)
	require.Equal(t, 1, result.Changed)
	require.NotEmpty(t, result.Backup)
	contents, err := os.ReadFile(stackPath)
	require.NoError(t, err)
	require.Contains(t, string(contents), "alpine:3.23")
	updated, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.NotNil(t, updated.LastAutoSyncSuccessAt)
	require.NotEmpty(t, updated.LastAutoSyncCommit)

	second, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "No new Git commit; stack scan skipped", second.Message)
}

func TestBindingAutomationLeavesAllFilesUntouchedOnConflict(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	stackPath := filepath.Join(stackRoot, "app", "compose.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(stackPath), 0o755))
	require.NoError(t, os.WriteFile(stackPath, []byte("services:\n  app:\n    image: alpine:3.22\n"), 0o644))
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
	require.NoError(t, err)
	establishBindingBaseline(t, service, binding.ID)
	require.NoError(t, os.WriteFile(stackPath, []byte("services:\n  app:\n    image: local-change\n"), 0o644))
	remoteChange(t, repository.RemoteURL, "stacks/app/compose.yaml", "services:\n  app:\n    image: remote-change\n")
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, IntervalMinutes: 5})
	require.NoError(t, err)

	result, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "conflict", result.State)
	require.Equal(t, 1, result.Conflicts)
	contents, err := os.ReadFile(stackPath)
	require.NoError(t, err)
	require.Contains(t, string(contents), "local-change")
}

func TestBindingAutomationDiscoversAndDeploysNewGitStack(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks"})
	require.NoError(t, err)
	binding, err = service.UpdateBindingComposeSelection(binding.ID, BindingComposeSelectionInput{Mode: composeSelectionSelected})
	require.NoError(t, err)
	remoteChange(t, repository.RemoteURL, "stacks/new-stack/compose.yml", "services:\n  app:\n    image: alpine:3.23\n")
	var actions []string
	service.ConfigureDeployment(
		func(_ context.Context, _, filename string) error {
			actions = append(actions, "validate:"+filename)
			return nil
		},
		func(_ context.Context, _, filename string, _ io.Writer) error {
			actions = append(actions, "dry-run:"+filename)
			return nil
		},
		func(_ context.Context, _, filename string, _ io.Writer) error {
			actions = append(actions, "deploy:"+filename)
			return nil
		},
		func(_, _ string) (func(), bool) { return func() {}, true },
	)
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{
		Enabled: true, IntervalMinutes: 5, DeployEnabled: true, DeployNewStacks: true,
	})
	require.NoError(t, err)
	result, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"new-stack/compose.yml"}, result.Deployed)
	require.Equal(t, []string{
		"validate:compose/new-stack/compose.yml",
		"dry-run:compose/new-stack/compose.yml",
		"deploy:compose/new-stack/compose.yml",
	}, actions)
	_, err = os.Stat(filepath.Join(stackRoot, "new-stack", "compose.yml"))
	require.NoError(t, err)
	updated, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Contains(t, splitPatternLines(updated.ComposePaths), "new-stack/compose.yml")
	require.Contains(t, splitPatternLines(updated.SelectedComposePaths), "new-stack/compose.yml")
	require.Contains(t, splitPatternLines(updated.AutoDeployComposePaths), "new-stack/compose.yml")
}

func establishBindingBaseline(t *testing.T, service *Service, bindingID string) {
	t.Helper()
	preview, err := service.PreviewBinding(bindingID, "stack_to_repository", TransferInput{})
	require.NoError(t, err)
	_, err = service.ExportBinding(context.Background(), bindingID, TransferInput{PreviewToken: preview.PreviewToken})
	require.NoError(t, err)
}

func remoteChange(t *testing.T, remoteURL, name, contents string) {
	t.Helper()
	root := t.TempDir()
	repo, err := gitclient.PlainClone(root, false, &gitclient.CloneOptions{
		URL: remoteURL, ReferenceName: plumbing.NewBranchReferenceName("main"), SingleBranch: true,
	})
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755))
	commitTestFile(t, repo, root, name, contents)
	require.NoError(t, repo.Push(&gitclient.PushOptions{}))
}
