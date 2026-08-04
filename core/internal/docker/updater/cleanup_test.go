package updater

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestImageCleanupRetentionRemovesOnlyOlderExactImages(t *testing.T) {
	service, err := NewAutomationService(testScanStore(t), func(context.Context, string) ([]UpdateEnrollment, error) { return nil, nil }, func(context.Context, string, []string) ([]ContainerUpdateCheck, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown() })
	var removed []string
	service.SetImageCleaner(func(_ context.Context, _, imageID string) (bool, string, error) {
		removed = append(removed, imageID)
		return true, "removed safely", nil
	})
	target := UpdateExecutionTarget{ContainerName: "web", CleanupEnabled: true, CleanupKeep: 1}
	first := UpdateExecutionOutcome{UpdateExecutionTarget: target, State: ExecutionUpdated, PreviousImage: "sha256:old-one"}
	if err := service.queueAndProcessImageCleanup(context.Background(), "local", []UpdateExecutionOutcome{first}); err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 {
		t.Fatalf("newest rollback image was removed despite retention: %v", removed)
	}
	time.Sleep(time.Millisecond)
	second := UpdateExecutionOutcome{UpdateExecutionTarget: target, State: ExecutionUpdated, PreviousImage: "sha256:old-two"}
	if err := service.queueAndProcessImageCleanup(context.Background(), "local", []UpdateExecutionOutcome{second}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(removed, []string{"sha256:old-one"}) {
		t.Fatalf("cleanup did not remove only the oldest exact image: %v", removed)
	}
}

func TestImageCleanupNeverRunsForRolledBackStack(t *testing.T) {
	service, err := NewAutomationService(testScanStore(t), func(context.Context, string) ([]UpdateEnrollment, error) { return nil, nil }, func(context.Context, string, []string) ([]ContainerUpdateCheck, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown() })
	service.SetImageCleaner(func(context.Context, string, string) (bool, string, error) {
		t.Fatal("cleanup ran for a failed stack transaction")
		return false, "", nil
	})
	base := UpdateExecutionTarget{TargetType: UpdateTargetStack, StackKey: "demo|compose.yml", CleanupEnabled: true, CleanupKeep: 0}
	outcomes := []UpdateExecutionOutcome{
		{UpdateExecutionTarget: base, State: ExecutionUpdated, PreviousImage: "sha256:old-web"},
		{UpdateExecutionTarget: base, State: ExecutionRolledBack, PreviousImage: "sha256:old-db"},
	}
	if err := service.queueAndProcessImageCleanup(context.Background(), "local", outcomes); err != nil {
		t.Fatal(err)
	}
	rows, err := service.ImageCleanupState("local")
	if err != nil || len(rows) != 0 {
		t.Fatalf("failed stack queued image cleanup: rows=%#v err=%v", rows, err)
	}
}

func TestRetainedImageCanBeRetriedWithoutForce(t *testing.T) {
	service, err := NewAutomationService(testScanStore(t), func(context.Context, string) ([]UpdateEnrollment, error) { return nil, nil }, func(context.Context, string, []string) ([]ContainerUpdateCheck, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown() })
	attempts := 0
	service.SetImageCleaner(func(context.Context, string, string) (bool, string, error) {
		attempts++
		if attempts == 1 {
			return false, "retained: still referenced by one stopped container", nil
		}
		return true, "removed safely", nil
	})
	outcome := UpdateExecutionOutcome{UpdateExecutionTarget: UpdateExecutionTarget{ContainerName: "web", CleanupEnabled: true, CleanupKeep: 0}, State: ExecutionUpdated, PreviousImage: "sha256:old"}
	if err := service.queueAndProcessImageCleanup(context.Background(), "local", []UpdateExecutionOutcome{outcome}); err != nil {
		t.Fatal(err)
	}
	if err := service.RetryImageCleanup(context.Background(), "local"); err != nil {
		t.Fatal(err)
	}
	rows, err := service.ImageCleanupState("local")
	if err != nil || len(rows) != 1 || rows[0].Status != "removed" || attempts != 2 {
		t.Fatalf("retained image retry failed: rows=%#v attempts=%d err=%v", rows, attempts, err)
	}
}
