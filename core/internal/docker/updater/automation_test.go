package updater

import (
	"context"
	"errors"
	"slices"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func testScanStore(t *testing.T) *ScanStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&UpdateScanResult{}, &UpdateScanRun{}); err != nil {
		t.Fatal(err)
	}
	return NewScanStore(db)
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
