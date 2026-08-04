package updater

import (
	"context"
	"testing"

	"github.com/moby/moby/api/types/container"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func testPolicyService(t *testing.T) *PolicyService {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&UpdatePolicy{}); err != nil {
		t.Fatal(err)
	}
	return NewPolicyService(NewPolicyStore(db))
}

func TestPolicySaveUpsertsAndValidatesSchedule(t *testing.T) {
	service := testPolicyService(t)
	policy := UpdatePolicy{Host: "local", TargetType: UpdateTargetContainer, TargetKey: "web", TargetName: "web", Enabled: true, RollbackEnabled: true}
	if err := service.Save(&policy); err != nil {
		t.Fatal(err)
	}
	policy.Schedule = "0 4 * * *"
	policy.RollbackEnabled = false
	if err := service.Save(&policy); err != nil {
		t.Fatal(err)
	}
	rows, err := service.List("local")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Schedule != "0 4 * * *" || rows[0].RollbackEnabled {
		t.Fatalf("unexpected upsert result: %#v", rows)
	}
	policy.Schedule = "not cron"
	if err := service.Save(&policy); err == nil {
		t.Fatal("expected invalid cron schedule to be rejected")
	}
}

func TestPolicyVersionDiscoveryDefaultsAndValidation(t *testing.T) {
	service := testPolicyService(t)
	policy := UpdatePolicy{Host: "local", TargetType: UpdateTargetContainer, TargetKey: "web", TargetName: "web", Enabled: true}
	if err := service.Save(&policy); err != nil {
		t.Fatal(err)
	}
	if policy.VersionPolicy != VersionPolicyOff {
		t.Fatalf("default version policy = %q", policy.VersionPolicy)
	}
	policy.VersionPolicy = VersionPolicyMinor
	policy.VersionPrerelease = true
	if err := service.Save(&policy); err != nil {
		t.Fatal(err)
	}
	rows, err := service.List("local")
	if err != nil || len(rows) != 1 || rows[0].VersionPolicy != VersionPolicyMinor || !rows[0].VersionPrerelease {
		t.Fatalf("version policy was not persisted: rows=%#v err=%v", rows, err)
	}
	policy.VersionPolicy = "anything"
	if err := service.Save(&policy); err == nil {
		t.Fatal("unsupported version policy was accepted")
	}
}

func TestInventoryPolicyPrecedence(t *testing.T) {
	service := testPolicyService(t)
	stackLabels := map[string]string{composeProjectLabel: "demo", composeFilesLabel: "/stacks/demo/compose.yml"}
	_, stackKey := stackIdentity(stackLabels)
	if err := service.Save(&UpdatePolicy{
		Host: "local", TargetType: UpdateTargetStack, TargetKey: stackKey, TargetName: "demo",
		Enabled: true, Schedule: "0 4 * * *", RollbackEnabled: true, CleanupEnabled: true, CleanupKeep: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.Save(&UpdatePolicy{
		Host: "local", TargetType: UpdateTargetContainer, TargetKey: "disabled-by-ui", TargetName: "disabled-by-ui",
		Enabled: false, RollbackEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	containers := []container.Summary{
		{ID: "1", Names: []string{"/stack-web"}, Image: "example/web:latest", Labels: stackLabels},
		{ID: "2", Names: []string{"/labelled"}, Image: "example/labelled:latest", Labels: map[string]string{
			DockmanOptInUpdateLabel: "true", UpdateScheduleLabel: "30 2 * * *", UpdateRollbackLabel: "false", UpdateCleanupLabel: "true", UpdateCleanupKeepLabel: "0",
		}},
		{ID: "3", Names: []string{"/disabled-label"}, Labels: map[string]string{
			DockmanOptInUpdateLabel: "true", DockmanUpdateDisableLabel: "true",
		}},
		{ID: "4", Names: []string{"/disabled-by-ui"}, Labels: stackLabels},
	}
	rows, err := service.Inventory(context.Background(), "local", containers)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]UpdateEnrollment, len(rows))
	for _, row := range rows {
		byName[row.ContainerName] = row
	}
	if row := byName["stack-web"]; !row.Enrolled || row.PolicyTarget != UpdateTargetStack || row.Schedule != "0 4 * * *" || !row.CleanupEnabled || row.CleanupKeep != 2 {
		t.Fatalf("stack policy not applied: %#v", row)
	}
	if row := byName["labelled"]; !row.Enrolled || row.Source != "label" || row.Rollback || row.Schedule != "30 2 * * *" || !row.CleanupEnabled || row.CleanupKeep != 0 {
		t.Fatalf("label policy not applied: %#v", row)
	}
	if row := byName["disabled-label"]; row.Enrolled || row.Source != "disabled-label" {
		t.Fatalf("hard-disable label did not win: %#v", row)
	}
	if row := byName["disabled-by-ui"]; row.Enrolled || row.Source != "interface" {
		t.Fatalf("container exclusion not retained: %#v", row)
	}
}

func TestDeletePolicy(t *testing.T) {
	service := testPolicyService(t)
	policy := UpdatePolicy{Host: "local", TargetType: UpdateTargetContainer, TargetKey: "web", TargetName: "web", Enabled: true}
	if err := service.Save(&policy); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete("local", UpdateTargetContainer, "web"); err != nil {
		t.Fatal(err)
	}
	rows, err := service.List("local")
	if err != nil || len(rows) != 0 {
		t.Fatalf("policy was not deleted: rows=%#v err=%v", rows, err)
	}
}
