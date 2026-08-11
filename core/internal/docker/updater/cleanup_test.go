package updater

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

func testAutomationService(t *testing.T) *AutomationService {
	t.Helper()
	service, err := NewAutomationService(
		testScanStore(t),
		func(context.Context, string) ([]UpdateEnrollment, error) { return nil, nil },
		func(context.Context, string, []string) ([]ContainerUpdateCheck, error) { return nil, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown() })
	return service
}

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

// An image Docker will not remove - it is still referenced by another
// container - used to stay pending forever, and every automatic run reissued
// the same doomed ImageRemove against the daemon. Nothing bounded it: the row
// was never pruned either, because the history purge only ever touched rows
// already marked removed.
func TestAnImageDockerKeepsRefusingStopsBeingRetriedAutomatically(t *testing.T) {
	service := testAutomationService(t)
	attempts := 0
	service.SetImageCleaner(func(context.Context, string, string) (bool, string, error) {
		attempts++
		return false, "retained: still referenced by another container", nil
	})
	outcome := UpdateExecutionOutcome{
		UpdateExecutionTarget: UpdateExecutionTarget{ContainerName: "web", CleanupEnabled: true, CleanupKeep: 0},
		State:                 ExecutionUpdated, PreviousImage: "sha256:stuck",
	}
	if err := service.queueAndProcessImageCleanup(context.Background(), "local", []UpdateExecutionOutcome{outcome}); err != nil {
		t.Fatal(err)
	}

	// Ten further automatic runs, none of them queueing anything new.
	for range 10 {
		if err := service.queueAndProcessImageCleanup(context.Background(), "local", nil); err != nil {
			t.Fatal(err)
		}
	}

	if attempts > maxAutomaticCleanupAttempts {
		t.Fatalf("the daemon was asked to remove an image it refuses %d times; the automatic budget is %d",
			attempts, maxAutomaticCleanupAttempts)
	}
	rows, err := service.ImageCleanupState("local")
	if err != nil || len(rows) != 1 {
		t.Fatalf("cleanup state: rows=%#v err=%v", rows, err)
	}
	if rows[0].Attempts != attempts {
		t.Fatalf("the row does not record what was tried: attempts=%d recorded=%d", attempts, rows[0].Attempts)
	}
	if rows[0].Status != "pending" {
		t.Fatalf("an image that may become removable later must stay actionable, got %q", rows[0].Status)
	}
}

// Giving up automatically must not take the decision away from the operator:
// the explicit retry starts the budget over and tries again.
func TestAnExhaustedImageIsStillRetriedOnDemand(t *testing.T) {
	service := testAutomationService(t)
	attempts := 0
	service.SetImageCleaner(func(context.Context, string, string) (bool, string, error) {
		attempts++
		// Whatever was holding the image is gone by the time the operator asks.
		if attempts > maxAutomaticCleanupAttempts {
			return true, "removed safely", nil
		}
		return false, "retained: still referenced by another container", nil
	})
	outcome := UpdateExecutionOutcome{
		UpdateExecutionTarget: UpdateExecutionTarget{ContainerName: "web", CleanupEnabled: true, CleanupKeep: 0},
		State:                 ExecutionUpdated, PreviousImage: "sha256:stuck",
	}
	if err := service.queueAndProcessImageCleanup(context.Background(), "local", []UpdateExecutionOutcome{outcome}); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if err := service.queueAndProcessImageCleanup(context.Background(), "local", nil); err != nil {
			t.Fatal(err)
		}
	}

	if err := service.RetryImageCleanup(context.Background(), "local"); err != nil {
		t.Fatal(err)
	}

	rows, err := service.ImageCleanupState("local")
	if err != nil || len(rows) != 1 || rows[0].Status != "removed" {
		t.Fatalf("an explicit retry did not get through: rows=%#v err=%v", rows, err)
	}
}

// The table has to stay bounded whatever the statuses are. Capping only the
// removed rows left the pending ones - exactly the ones that accumulate -
// growing without limit.
func TestImageCleanupHistoryIsBoundedWhateverTheStatus(t *testing.T) {
	service := testAutomationService(t)
	service.SetImageCleaner(func(context.Context, string, string) (bool, string, error) {
		return false, "retained: still referenced by another container", nil
	})
	for i := range maxImageCleanupHistory + 40 {
		outcome := UpdateExecutionOutcome{
			UpdateExecutionTarget: UpdateExecutionTarget{
				ContainerName: fmt.Sprintf("web-%d", i), CleanupEnabled: true, CleanupKeep: 0,
			},
			State: ExecutionUpdated, PreviousImage: fmt.Sprintf("sha256:old-%d", i),
		}
		if err := service.queueAndProcessImageCleanup(context.Background(), "local", []UpdateExecutionOutcome{outcome}); err != nil {
			t.Fatal(err)
		}
	}

	var stored int64
	if err := service.store.db.Model(&UpdateImageCleanup{}).Where("host = ?", "local").Count(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored > maxImageCleanupHistory {
		t.Fatalf("the cleanup table grew to %d rows, past the %d it is meant to keep", stored, maxImageCleanupHistory)
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
