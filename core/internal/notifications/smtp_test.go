package notifications

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/RA341/dockman/internal/docker/updater"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type recordingSender struct {
	messages []SMTPMessage
	err      error
}

func (s *recordingSender) Send(_ context.Context, message SMTPMessage) error {
	s.messages = append(s.messages, message)
	return s.err
}

func testService(t *testing.T) (*Service, *gorm.DB, *recordingSender) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&SMTPConfig{}, &NotificationState{}, &Delivery{}); err != nil {
		t.Fatal(err)
	}
	vault, err := NewVault([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	sender := &recordingSender{}
	return NewServiceWithSender(db, vault, sender), db, sender
}

func validInput() ConfigInput {
	return ConfigInput{
		Enabled: true, Server: "smtp.example.com", Port: 587, Security: SecuritySTARTTLS,
		Username: "dockman", Password: "top-secret", FromAddress: "Dockman <dockman@example.com>",
		Recipients: "ops@example.com, admin@example.com", NotifyUpdates: true, NotifyErrors: true,
	}
}

func TestSaveEncryptsPasswordAndNeverReturnsIt(t *testing.T) {
	service, db, _ := testService(t)
	view, err := service.Save("local", validInput())
	if err != nil {
		t.Fatal(err)
	}
	if !view.HasPassword || view.Password != "" {
		t.Fatalf("password leaked or was not stored: %#v", view)
	}
	var row SMTPConfig
	if err := db.Where("host = ?", "local").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if len(row.EncryptedPassword) == 0 || strings.Contains(string(row.EncryptedPassword), "top-secret") {
		t.Fatal("SMTP password was not encrypted at rest")
	}
	original := append([]byte(nil), row.EncryptedPassword...)

	input := validInput()
	input.Password = ""
	input.Recipients = "new@example.com"
	if _, err := service.Save("local", input); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("host = ?", "local").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if string(original) != string(row.EncryptedPassword) {
		t.Fatal("blank password did not preserve the encrypted credential")
	}
	got, _, err := service.Get("local")
	if err != nil || !got.HasPassword || got.Password != "" || got.Recipients != "new@example.com" {
		t.Fatalf("unexpected public configuration: %#v, %v", got, err)
	}
}

func TestNotifyScanIsScheduledGroupedAndChangeOnly(t *testing.T) {
	service, _, sender := testService(t)
	if _, err := service.Save("local", validInput()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	run := updater.UpdateScanRun{Host: "local", Trigger: "scheduled", Schedule: "0 4 * * *", Targets: 2, CompletedAt: &now}
	checks := []updater.ContainerUpdateCheck{
		{ContainerName: "web", Image: "example/web:latest", Status: updater.ContainerUpdateAvailable, RemoteDigest: "sha256:new"},
		{ContainerName: "worker", Image: "example/worker:latest", Status: updater.ContainerUpdateError, Reason: "registry unavailable"},
	}
	if err := service.NotifyScan(context.Background(), run, checks); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 1 || !strings.Contains(sender.messages[0].Body, "web") || !strings.Contains(sender.messages[0].Body, "worker") {
		t.Fatalf("scheduled events were not grouped: %#v", sender.messages)
	}
	if err := service.NotifyScan(context.Background(), run, checks); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 1 {
		t.Fatal("unchanged scheduled events generated duplicate mail")
	}

	resolved := []updater.ContainerUpdateCheck{{ContainerName: "web", Image: "example/web:latest", Status: updater.ContainerUpdateCurrent}}
	if err := service.NotifyScan(context.Background(), run, resolved); err != nil {
		t.Fatal(err)
	}
	if err := service.NotifyScan(context.Background(), run, checks); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 2 {
		t.Fatal("resolved event did not become eligible for a future notification")
	}

	run.Trigger = "manual"
	if err := service.NotifyScan(context.Background(), run, checks); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 2 {
		t.Fatal("manual scan generated an automatic notification")
	}
}

func TestNotifyScanKeepsSchedulesIndependent(t *testing.T) {
	service, _, sender := testService(t)
	if _, err := service.Save("local", validInput()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	checks := []updater.ContainerUpdateCheck{{ContainerName: "web", Image: "web:latest", Status: updater.ContainerUpdateAvailable}}
	for _, schedule := range []string{"0 4 * * *", "0 8 * * *", "0 4 * * *", "0 8 * * *"} {
		run := updater.UpdateScanRun{Host: "local", Trigger: "scheduled", Schedule: schedule, Targets: 1, CompletedAt: &now}
		if err := service.NotifyScan(context.Background(), run, checks); err != nil {
			t.Fatal(err)
		}
	}
	if len(sender.messages) != 2 {
		t.Fatalf("schedule fingerprints interfered with each other: %d messages", len(sender.messages))
	}
}

func TestNotifyScanRespectsIndependentCategories(t *testing.T) {
	service, _, sender := testService(t)
	input := validInput()
	input.NotifyUpdates = false
	if _, err := service.Save("local", input); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	run := updater.UpdateScanRun{Host: "local", Trigger: "scheduled", Schedule: "0 4 * * *", Targets: 2, CompletedAt: &now}
	checks := []updater.ContainerUpdateCheck{
		{ContainerName: "web", Image: "web:latest", Status: updater.ContainerUpdateAvailable},
		{ContainerName: "worker", Image: "worker:latest", Status: updater.ContainerUpdateError, Reason: "registry unavailable"},
	}
	if err := service.NotifyScan(context.Background(), run, checks); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 1 || strings.Contains(sender.messages[0].Body, "Updates available") || !strings.Contains(sender.messages[0].Body, "Scan errors") {
		t.Fatalf("notification categories leaked into each other: %#v", sender.messages)
	}
}

func TestFailedDeliveryIsRecordedAndRetried(t *testing.T) {
	service, _, sender := testService(t)
	if _, err := service.Save("local", validInput()); err != nil {
		t.Fatal(err)
	}
	sender.err = errors.New("SMTP rejected top-secret")
	now := time.Now()
	run := updater.UpdateScanRun{Host: "local", Trigger: "scheduled", Targets: 1, CompletedAt: &now}
	checks := []updater.ContainerUpdateCheck{{ContainerName: "web", Image: "web:latest", Status: updater.ContainerUpdateAvailable}}
	if err := service.NotifyScan(context.Background(), run, checks); err == nil {
		t.Fatal("expected delivery failure")
	}
	_, deliveries, err := service.Get("local")
	if err != nil || len(deliveries) != 1 || deliveries[0].Success || !strings.Contains(deliveries[0].Error, "[redacted]") || strings.Contains(deliveries[0].Error, "top-secret") {
		t.Fatalf("delivery failure was not recorded: %#v, %v", deliveries, err)
	}
	sender.err = nil
	if err := service.NotifyScan(context.Background(), run, checks); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 2 {
		t.Fatal("failed delivery incorrectly suppressed the retry")
	}
}

func TestValidationRejectsCredentialsWithoutEncryption(t *testing.T) {
	service, _, _ := testService(t)
	input := validInput()
	input.Security = SecurityNone
	if _, err := service.Save("local", input); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("unexpected validation result: %v", err)
	}
}
