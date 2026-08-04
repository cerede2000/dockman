package updater

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func testScanStore(t *testing.T) *ScanStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&UpdateScanResult{}, &UpdateScanRun{}, &UpdateExecutionRun{}, &UpdateExecutionResult{}, &UpdateExecutionBlock{}, &UpdateAutomationControl{}, &UpdateImageCleanup{}); err != nil {
		t.Fatal(err)
	}
	return NewScanStore(db)
}

func TestInterruptedExecutionIsRecoveredAtServiceStartup(t *testing.T) {
	store := testScanStore(t)
	run := UpdateExecutionRun{Host: "local", Schedule: DefaultUpdateSchedule, StartedAt: time.Now(), Targets: 2}
	if err := store.db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	service, err := NewAutomationService(store, func(context.Context, string) ([]UpdateEnrollment, error) { return nil, nil }, func(context.Context, string, []string) ([]ContainerUpdateCheck, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown() })
	runs, _, _, err := service.ExecutionState("local")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].CompletedAt == nil || !strings.Contains(runs[0].Error, "restarted") {
		t.Fatalf("interrupted execution was not made explicit: %#v", runs)
	}
	control, err := service.Control("local")
	if err != nil || !control.Paused {
		t.Fatalf("interrupted execution did not fail closed: control=%#v err=%v", control, err)
	}
}

func TestAutomationPauseAndManualExecution(t *testing.T) {
	service, err := NewAutomationService(
		testScanStore(t),
		func(context.Context, string) ([]UpdateEnrollment, error) {
			return []UpdateEnrollment{{ContainerID: "web", ContainerName: "web", Image: "example/web", Enrolled: true}}, nil
		},
		func(context.Context, string, []string) ([]ContainerUpdateCheck, error) {
			return []ContainerUpdateCheck{{ContainerID: "web", ContainerName: "web", Image: "example/web", Status: ContainerUpdateAvailable, RemoteDigest: "new"}}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown() })
	executions := 0
	service.SetExecutor(func(_ context.Context, _ string, targets []UpdateExecutionTarget) []UpdateExecutionOutcome {
		executions++
		return []UpdateExecutionOutcome{{UpdateExecutionTarget: targets[0], State: ExecutionUpdated}}
	})
	if _, err := service.SaveControl("local", true, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.RunAutomaticNow(context.Background(), "local"); err == nil {
		t.Fatal("paused host accepted a manual automatic execution")
	}
	if _, err := service.SaveControl("local", false, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.RunAutomaticNow(context.Background(), "local"); err != nil {
		t.Fatal(err)
	}
	if executions != 1 {
		t.Fatalf("manual automatic executions = %d", executions)
	}
}

func TestExecutionGroupLimitNeverSplitsAStack(t *testing.T) {
	targets := []UpdateExecutionTarget{
		{ContainerID: "one", TargetType: UpdateTargetStack, StackKey: "stack-a"},
		{ContainerID: "two", TargetType: UpdateTargetStack, StackKey: "stack-a"},
		{ContainerID: "three"},
	}
	limited := limitExecutionGroups(targets, 1)
	if len(limited) != 2 || limited[0].ContainerID != "one" || limited[1].ContainerID != "two" {
		t.Fatalf("stack transaction was split by the execution limit: %#v", limited)
	}
}

func TestNormalizeUpdateSchedule(t *testing.T) {
	if got, err := NormalizeUpdateSchedule(""); err != nil || got != DefaultUpdateSchedule {
		t.Fatalf("unexpected default schedule %q: %v", got, err)
	}
	if got, err := NormalizeUpdateSchedule("*/15 * * * *"); err != nil || got != "*/15 * * * *" {
		t.Fatalf("unexpected normalized schedule %q: %v", got, err)
	}
	if _, err := NormalizeUpdateSchedule("* * * * *"); err == nil {
		t.Fatal("expected one-minute registry schedule to be rejected")
	}
}

func TestRunNowScansOnlyEnrolledContainersAndPersistsState(t *testing.T) {
	var scanned []string
	store := testScanStore(t)
	if err := store.db.Create(&UpdateScanResult{Host: "local", ContainerID: "removed", ContainerName: "removed", Image: "old", Status: "current"}).Error; err != nil {
		t.Fatal(err)
	}
	service, err := NewAutomationService(
		store,
		func(context.Context, string) ([]UpdateEnrollment, error) {
			return []UpdateEnrollment{
				{ContainerID: "enabled", ContainerName: "enabled", Enrolled: true},
				{ContainerID: "disabled", ContainerName: "disabled", Enrolled: false},
			}, nil
		},
		func(_ context.Context, _ string, ids []string) ([]ContainerUpdateCheck, error) {
			scanned = append(scanned, ids...)
			return []ContainerUpdateCheck{{ContainerID: "enabled", ContainerName: "enabled", Image: "example:latest", Status: ContainerUpdateAvailable}}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown() })

	run, checks, err := service.RunNow(context.Background(), "local")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(scanned, []string{"enabled"}) || len(checks) != 1 || run.Targets != 1 || run.Available != 1 {
		t.Fatalf("unexpected scan: ids=%v checks=%#v run=%#v", scanned, checks, run)
	}
	results, runs, _, err := service.State("local")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Status != string(ContainerUpdateAvailable) || len(runs) != 1 {
		t.Fatalf("unexpected persisted state: results=%#v runs=%#v", results, runs)
	}
}

func TestRunNowDiscoversAndPersistsNewerVersionWithoutChangingDigestStatus(t *testing.T) {
	store := testScanStore(t)
	service, err := NewAutomationService(
		store,
		func(context.Context, string) ([]UpdateEnrollment, error) {
			return []UpdateEnrollment{{
				ContainerID: "web", ContainerName: "web", Image: "example/web:v3.1.1",
				Enrolled: true, VersionPolicy: VersionPolicyMinor,
			}}, nil
		},
		func(context.Context, string, []string) ([]ContainerUpdateCheck, error) {
			return []ContainerUpdateCheck{{
				ContainerID: "web", ContainerName: "web", Image: "example/web:v3.1.1",
				Status: ContainerUpdateCurrent, CurrentDigest: "sha256:same", RemoteDigest: "sha256:same",
			}}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown() })
	service.SetVersionDiscoverer(func(_ context.Context, host string, targets []VersionDiscoveryTarget) []VersionDiscoveryResult {
		if host != "local" || len(targets) != 1 || targets[0].Policy != VersionPolicyMinor {
			t.Fatalf("unexpected discovery request: host=%q targets=%#v", host, targets)
		}
		return []VersionDiscoveryResult{{
			ContainerID: "web", CurrentTag: "v3.1.1", LatestTag: "v3.2.0",
			Policy: VersionPolicyMinor, Available: true, Reason: "newer minor version tag available",
		}}
	})

	run, checks, err := service.RunNow(context.Background(), "local")
	if err != nil {
		t.Fatal(err)
	}
	if run.Versions != 1 || run.Available != 0 || run.Current != 1 || len(checks) != 1 {
		t.Fatalf("version discovery changed digest scan semantics: run=%#v checks=%#v", run, checks)
	}
	if checks[0].Status != ContainerUpdateCurrent || !checks[0].VersionAvailable || checks[0].LatestTag != "v3.2.0" {
		t.Fatalf("unexpected enriched check: %#v", checks[0])
	}
	results, runs, _, err := service.State("local")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].VersionAvailable || results[0].LatestTag != "v3.2.0" || len(runs) != 1 || runs[0].Versions != 1 {
		t.Fatalf("version discovery was not persisted: results=%#v runs=%#v", results, runs)
	}
}

func TestScheduledRunUsesOnlyMatchingSchedule(t *testing.T) {
	var scanned []string
	service, err := NewAutomationService(
		testScanStore(t),
		func(context.Context, string) ([]UpdateEnrollment, error) {
			return []UpdateEnrollment{
				{ContainerID: "daily", ContainerName: "daily", Enrolled: true, Schedule: "0 4 * * *"},
				{ContainerID: "quarter-hour", ContainerName: "quarter-hour", Enrolled: true, Schedule: "*/15 * * * *"},
			}, nil
		},
		func(_ context.Context, _ string, ids []string) ([]ContainerUpdateCheck, error) {
			scanned = append(scanned, ids...)
			return []ContainerUpdateCheck{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown() })

	if _, _, err := service.run(context.Background(), "local", "0 4 * * *", "scheduled"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(scanned, []string{"daily"}) {
		t.Fatalf("scheduled scan crossed policy windows: %v", scanned)
	}
}

func TestScheduledRunExecutesAvailableTargetsButManualRunDoesNot(t *testing.T) {
	service, err := NewAutomationService(
		testScanStore(t),
		func(context.Context, string) ([]UpdateEnrollment, error) {
			return []UpdateEnrollment{{ContainerID: "web", ContainerName: "web", Image: "example/web:latest", State: "running", Enrolled: true, Schedule: DefaultUpdateSchedule, Rollback: true, PolicyTarget: UpdateTargetStack, StackName: "demo", StackKey: "demo|compose.yml", ServiceName: "web", DependsOn: "db:service_started:false"}}, nil
		},
		func(context.Context, string, []string) ([]ContainerUpdateCheck, error) {
			return []ContainerUpdateCheck{{ContainerID: "web", ContainerName: "web", Image: "example/web:latest", Status: ContainerUpdateAvailable, RemoteDigest: "new"}}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown() })
	executions := 0
	notifications := 0
	service.SetExecutor(func(_ context.Context, _ string, targets []UpdateExecutionTarget) []UpdateExecutionOutcome {
		executions++
		if targets[0].TargetType != UpdateTargetStack || targets[0].StackName != "demo" || targets[0].ServiceName != "web" || targets[0].DependsOn == "" {
			t.Fatalf("stack execution metadata was lost: %#v", targets[0])
		}
		return []UpdateExecutionOutcome{{UpdateExecutionTarget: targets[0], State: ExecutionUpdated, Message: "ok"}}
	})
	service.SetExecutionNotifier(func(_ context.Context, run UpdateExecutionRun, outcomes []UpdateExecutionOutcome) error {
		notifications++
		if run.Updated != 1 || len(outcomes) != 1 || outcomes[0].State != ExecutionUpdated {
			t.Fatalf("unexpected execution notification: %#v %#v", run, outcomes)
		}
		return nil
	})
	if _, _, err := service.RunNow(context.Background(), "local"); err != nil {
		t.Fatal(err)
	}
	if executions != 0 {
		t.Fatal("manual read-only scan executed an update")
	}
	if notifications != 0 {
		t.Fatal("manual read-only scan sent an execution notification")
	}
	if _, _, err := service.run(context.Background(), "local", DefaultUpdateSchedule, "scheduled"); err != nil {
		t.Fatal(err)
	}
	if executions != 1 {
		t.Fatalf("scheduled update executions = %d", executions)
	}
	if notifications != 1 {
		t.Fatalf("scheduled update notifications = %d", notifications)
	}
	runs, results, blocks, err := service.ExecutionState("local")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Updated != 1 || len(results) != 1 || results[0].TargetType != UpdateTargetStack || results[0].StackName != "demo" || len(blocks) != 0 {
		t.Fatalf("unexpected execution state: runs=%#v results=%#v blocks=%#v", runs, results, blocks)
	}
}

func TestFailedDigestIsBlockedUntilAcknowledgedOrChanged(t *testing.T) {
	store := testScanStore(t)
	service, err := NewAutomationService(
		store,
		func(context.Context, string) ([]UpdateEnrollment, error) {
			return []UpdateEnrollment{{ContainerID: "web", ContainerName: "web", Image: "example/web:latest", State: "running", Enrolled: true, Schedule: DefaultUpdateSchedule, Rollback: true}}, nil
		},
		func(context.Context, string, []string) ([]ContainerUpdateCheck, error) {
			return []ContainerUpdateCheck{{ContainerID: "web", ContainerName: "web", Image: "example/web:latest", Status: ContainerUpdateAvailable, RemoteDigest: "broken"}}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown() })
	executions := 0
	service.SetExecutor(func(_ context.Context, _ string, targets []UpdateExecutionTarget) []UpdateExecutionOutcome {
		executions++
		return []UpdateExecutionOutcome{{UpdateExecutionTarget: targets[0], State: ExecutionRolledBack, Message: "health check failed"}}
	})
	for range 2 {
		if _, _, err := service.run(context.Background(), "local", DefaultUpdateSchedule, "scheduled"); err != nil {
			t.Fatal(err)
		}
	}
	if executions != 1 {
		t.Fatalf("blocked digest retried %d times", executions)
	}
	if err := service.ClearExecutionBlock("local", "web"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.run(context.Background(), "local", DefaultUpdateSchedule, "scheduled"); err != nil {
		t.Fatal(err)
	}
	if executions != 2 {
		t.Fatalf("acknowledged digest was not retried: %d", executions)
	}
}

func TestBlockedStackMemberPreventsPartialStackRetry(t *testing.T) {
	store := testScanStore(t)
	stackKey := "demo|/stacks/demo/compose.yml"
	service, err := NewAutomationService(
		store,
		func(context.Context, string) ([]UpdateEnrollment, error) {
			return []UpdateEnrollment{
				{ContainerID: "db", ContainerName: "db", Image: "example/db:latest", State: "running", Enrolled: true, Schedule: DefaultUpdateSchedule, Rollback: true, PolicyTarget: UpdateTargetStack, StackName: "demo", StackKey: stackKey},
				{ContainerID: "web", ContainerName: "web", Image: "example/web:latest", State: "running", Enrolled: true, Schedule: DefaultUpdateSchedule, Rollback: true, PolicyTarget: UpdateTargetStack, StackName: "demo", StackKey: stackKey},
			}, nil
		},
		func(context.Context, string, []string) ([]ContainerUpdateCheck, error) {
			return []ContainerUpdateCheck{
				{ContainerID: "db", ContainerName: "db", Image: "example/db:latest", Status: ContainerUpdateAvailable, RemoteDigest: "db-new"},
				{ContainerID: "web", ContainerName: "web", Image: "example/web:latest", Status: ContainerUpdateAvailable, RemoteDigest: "web-broken"},
			}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown() })
	executions := 0
	service.SetExecutor(func(_ context.Context, _ string, targets []UpdateExecutionTarget) []UpdateExecutionOutcome {
		executions++
		outcomes := make([]UpdateExecutionOutcome, 0, len(targets))
		for _, target := range targets {
			outcomes = append(outcomes, UpdateExecutionOutcome{UpdateExecutionTarget: target, State: ExecutionRolledBack, Message: "stack rolled back"})
		}
		return outcomes
	})
	for range 2 {
		if _, _, err := service.run(context.Background(), "local", DefaultUpdateSchedule, "scheduled"); err != nil {
			t.Fatal(err)
		}
	}
	if executions != 1 {
		t.Fatalf("blocked stack was retried partially: executions=%d", executions)
	}
	if err := service.ClearExecutionBlock("local", "db"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.run(context.Background(), "local", DefaultUpdateSchedule, "scheduled"); err != nil {
		t.Fatal(err)
	}
	if executions != 2 {
		t.Fatalf("stack retry did not clear the whole transaction: executions=%d", executions)
	}
}

func TestScheduledNotificationFailureDoesNotFailScan(t *testing.T) {
	service, err := NewAutomationService(
		testScanStore(t),
		func(context.Context, string) ([]UpdateEnrollment, error) {
			return []UpdateEnrollment{{ContainerID: "web", ContainerName: "web", Enrolled: true, Schedule: DefaultUpdateSchedule}}, nil
		},
		func(context.Context, string, []string) ([]ContainerUpdateCheck, error) {
			return []ContainerUpdateCheck{{ContainerID: "web", ContainerName: "web", Status: ContainerUpdateAvailable}}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown() })
	called := 0
	service.SetNotifier(func(_ context.Context, run UpdateScanRun, checks []ContainerUpdateCheck) error {
		called++
		if run.Trigger != "scheduled" || len(checks) != 1 {
			t.Fatalf("unexpected notification payload: %#v %#v", run, checks)
		}
		return errors.New("SMTP unavailable")
	})
	if _, _, err := service.run(context.Background(), "local", DefaultUpdateSchedule, "scheduled"); err != nil {
		t.Fatalf("notification failure failed the image scan: %v", err)
	}
	if called != 1 {
		t.Fatalf("notifier called %d times", called)
	}
}

func TestReconcileInventoryGroupsJobsWithoutPolling(t *testing.T) {
	service, err := NewAutomationService(
		testScanStore(t),
		func(context.Context, string) ([]UpdateEnrollment, error) { return nil, nil },
		func(context.Context, string, []string) ([]ContainerUpdateCheck, error) { return nil, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown() })
	rows := []UpdateEnrollment{
		{ContainerID: "one", Enrolled: true},
		{ContainerID: "two", Enrolled: true, Schedule: DefaultUpdateSchedule},
		{ContainerID: "three", Enrolled: true, Schedule: "*/15 * * * *"},
		{ContainerID: "off", Enrolled: false},
	}
	if err := service.ReconcileInventory("local", rows); err != nil {
		t.Fatal(err)
	}
	_, _, schedules, err := service.State("local")
	if err != nil {
		t.Fatal(err)
	}
	if len(schedules) != 2 || schedules[0].NextRun.IsZero() || schedules[1].NextRun.IsZero() {
		t.Fatalf("unexpected schedules: %#v", schedules)
	}
	counts := map[string]int{}
	for _, schedule := range schedules {
		counts[schedule.Schedule] = schedule.Targets
	}
	if counts[DefaultUpdateSchedule] != 2 || counts["*/15 * * * *"] != 1 {
		t.Fatalf("targets were not grouped by schedule: %#v", counts)
	}
}
