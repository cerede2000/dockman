package updater

import (
	"context"
	"strings"
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

// The socket proxy keeps a source of its own. Collapsing it into Dockman's
// "protected" hid the very button that makes it updatable, because the
// interface hides the protected update for rows Dockman self-updates.
func TestSocketExposingContainerGetsItsOwnProtectedSource(t *testing.T) {
	service := testPolicyService(t)
	rows, err := service.Inventory(t.Context(), "local", []container.Summary{
		{ID: "proxy", Names: []string{"/socket-proxy"}, Mounts: []container.MountPoint{
			{Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock"},
		}},
		{ID: "dockman", Names: []string{"/dockman"}, Labels: map[string]string{DockmanContainerLabel: "true"}},
		{ID: "app", Names: []string{"/app"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	bySource := map[string]string{}
	for _, row := range rows {
		bySource[row.ContainerName] = row.Source
		if row.Enrolled {
			t.Fatalf("%s must not be enrolled", row.ContainerName)
		}
	}
	if bySource["socket-proxy"] != SourceProtectedInfrastructure {
		t.Fatalf("socket proxy source = %q, want %q", bySource["socket-proxy"], SourceProtectedInfrastructure)
	}
	if bySource["dockman"] != "protected" {
		t.Fatalf("Dockman must keep its own classification, got %q", bySource["dockman"])
	}
	if bySource["app"] != "none" {
		t.Fatalf("ordinary container source = %q, want none", bySource["app"])
	}
}

// The explicit label stays the last word, above the socket protection.
func TestExplicitOptInOverridesTheSocketProtection(t *testing.T) {
	service := testPolicyService(t)
	rows, err := service.Inventory(t.Context(), "local", []container.Summary{
		{ID: "proxy", Names: []string{"/socket-proxy"},
			Labels: map[string]string{DockmanOptInUpdateLabel: "true"},
			Mounts: []container.MountPoint{{Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !rows[0].Enrolled || rows[0].Source != "label" {
		t.Fatalf("the explicit label must win over the socket protection, got %#v", rows)
	}
}

// strconv.ParseBool rejects yes/no/on/off, and the old caller turned that
// rejection into a plain false. So dockman.update=yes disabled the container
// while the interface claimed the label read false, and
// dockman.update.rollback=yes switched the rollback off - the opposite of what
// was written.
func TestBooleanLabelsAcceptCommonSpellings(t *testing.T) {
	for _, value := range []string{"true", "TRUE", "1", "t", "y", "yes", "on", "enabled", ""} {
		got, present, valid := parseBoolLabel(map[string]string{"k": value}, "k")
		if !present || !valid || !got {
			t.Fatalf("%q should read as true, got value=%v present=%v valid=%v", value, got, present, valid)
		}
	}
	for _, value := range []string{"false", "FALSE", "0", "f", "n", "no", "off", "disabled"} {
		got, present, valid := parseBoolLabel(map[string]string{"k": value}, "k")
		if !present || !valid || got {
			t.Fatalf("%q should read as false, got value=%v present=%v valid=%v", value, got, present, valid)
		}
	}
	for _, value := range []string{"maybe", "2", "oui"} {
		_, present, valid := parseBoolLabel(map[string]string{"k": value}, "k")
		if !present || valid {
			t.Fatalf("%q should be reported as unreadable, got present=%v valid=%v", value, present, valid)
		}
	}
	if _, present, valid := parseBoolLabel(map[string]string{}, "k"); present || !valid {
		t.Fatal("an absent label is neither present nor invalid")
	}
}

// An unreadable label must never be resolved into the dangerous direction: a
// disable that cannot be read still disables, and a rollback that cannot be
// read stays on.
func TestUnreadableLabelsFallToTheSafeSide(t *testing.T) {
	service := testPolicyService(t)
	rows, err := service.Inventory(t.Context(), "local", []container.Summary{
		{ID: "a", Names: []string{"/held-back"}, Labels: map[string]string{DockmanUpdateDisableLabel: "yes"}},
		{ID: "b", Names: []string{"/opted-in"}, Labels: map[string]string{DockmanOptInUpdateLabel: "yes"}},
		{ID: "c", Names: []string{"/nonsense"}, Labels: map[string]string{DockmanOptInUpdateLabel: "maybe"}},
		{ID: "d", Names: []string{"/kept-rollback"}, Labels: map[string]string{
			DockmanOptInUpdateLabel: "true", UpdateRollbackLabel: "oui",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]UpdateEnrollment{}
	for _, row := range rows {
		byName[row.ContainerName] = row
	}
	if byName["held-back"].Enrolled || byName["held-back"].Source != "disabled-label" {
		t.Fatalf("dockman.update.disable=yes must hold the container back, got %#v", byName["held-back"])
	}
	if !byName["opted-in"].Enrolled {
		t.Fatalf("dockman.update=yes must enroll, got %#v", byName["opted-in"])
	}
	if byName["nonsense"].Enrolled || !strings.Contains(byName["nonsense"].Reason, `"maybe"`) {
		t.Fatalf("an unreadable value must be reported as written, got %#v", byName["nonsense"])
	}
	if !byName["kept-rollback"].Rollback {
		t.Fatalf("an unreadable rollback label must leave rollback enabled, got %#v", byName["kept-rollback"])
	}
}
