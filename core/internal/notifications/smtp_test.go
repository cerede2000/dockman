package notifications

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
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

func TestNotifyExecutionGroupsSuccessAndRollback(t *testing.T) {
	service, _, sender := testService(t)
	if _, err := service.Save("local", validInput()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	run := updater.UpdateExecutionRun{Host: "local", Schedule: "0 4 * * *", Targets: 2, Updated: 1, RolledBack: 1, CompletedAt: &now}
	outcomes := []updater.UpdateExecutionOutcome{
		{UpdateExecutionTarget: updater.UpdateExecutionTarget{ContainerName: "web", Image: "example/web:latest"}, State: updater.ExecutionUpdated},
		{UpdateExecutionTarget: updater.UpdateExecutionTarget{ContainerName: "worker", Image: "example/worker:latest"}, State: updater.ExecutionRolledBack, Message: "health check failed"},
	}
	if err := service.NotifyExecution(context.Background(), run, outcomes); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 1 || !strings.Contains(sender.messages[0].Body, "web") || !strings.Contains(sender.messages[0].Body, "health check failed") {
		t.Fatalf("unexpected execution notification: %#v", sender.messages)
	}
}

func TestFormatSMTPMessageMatchesMinimalOperationalFormat(t *testing.T) {
	sentAt := time.Date(2026, time.August, 4, 12, 30, 0, 0, time.UTC)
	payload, err := formatSMTPMessage(SMTPMessage{
		Config:     SMTPConfig{FromAddress: "Dockman <dockman@example.com>"},
		Recipients: []string{"ops@example.com"}, Subject: "[Dockman] 2 updates · local",
		Body: "Updated:\n- web\n- worker",
	}, sentAt)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"Date: Tue, 04 Aug 2026 12:30:00 +0000\r\n",
		"From: \"Dockman\" <dockman@example.com>\r\n",
		"Content-Type: text/plain; charset=\"UTF-8\"\r\n",
		"Updated:\r\n- web\r\n- worker\r\n",
	} {
		if !strings.Contains(payload, expected) {
			t.Fatalf("SMTP payload missing %q:\n%s", expected, payload)
		}
	}
	for _, unwanted := range []string{"Message-ID:", "Content-Transfer-Encoding:", "Auto-Submitted:", "X-Mailer:"} {
		if strings.Contains(payload, unwanted) {
			t.Fatalf("minimal SMTP payload must not contain %q", unwanted)
		}
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

func TestSMTPTLSConfigAppendsMountedPrivateCA(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Dockman SMTP test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(t.TempDir(), "smtp-ca.crt")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}

	config, err := smtpTLSConfig("smtp.internal.example", caFile, true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, subject := range config.RootCAs.Subjects() {
		if bytes.Equal(subject, certificate.RawSubject) {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("mounted SMTP CA was not appended to the system trust pool")
	}
	if config.MinVersion != tls.VersionTLS12 || config.ServerName != "smtp.internal.example" {
		t.Fatalf("TLS verification settings were weakened: %#v", config)
	}
}

func TestSMTPTLSConfigRejectsInvalidOrRequiredMissingCA(t *testing.T) {
	invalid := filepath.Join(t.TempDir(), "invalid.crt")
	if err := os.WriteFile(invalid, []byte("not a PEM certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := smtpTLSConfig("smtp.internal.example", invalid, true); err == nil || !strings.Contains(err.Error(), "no valid PEM") {
		t.Fatalf("invalid CA was not rejected: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing.crt")
	if _, err := smtpTLSConfig("smtp.internal.example", missing, true); err == nil || !strings.Contains(err.Error(), "open custom SMTP CA") {
		t.Fatalf("required missing CA was not rejected: %v", err)
	}
	if _, err := smtpTLSConfig("smtp.public.example", missing, false); err != nil {
		t.Fatalf("optional default CA must preserve public PKI behavior: %v", err)
	}
}

func TestSMTPCAFileEnvironmentOverride(t *testing.T) {
	t.Setenv("DOCKMAN_SMTP_CA_FILE", "/run/secrets/smtp-root.pem")
	path, required := smtpCAFileFromEnvironment()
	if path != "/run/secrets/smtp-root.pem" || !required {
		t.Fatalf("unexpected SMTP CA override: %q required=%v", path, required)
	}
}
