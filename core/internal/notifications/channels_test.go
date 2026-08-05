package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/RA341/dockman/internal/docker/updater"
)

type recordingChannelSender struct {
	events []ChannelEvent
	rows   []ChannelConfig
	errFor map[string]error
}

func (s *recordingChannelSender) Send(_ context.Context, row ChannelConfig, _ channelSecrets, event ChannelEvent) error {
	s.events = append(s.events, event)
	s.rows = append(s.rows, row)
	return s.errFor[row.Name]
}

func TestChannelConfigurationEncryptsSecretsAndPublicViewIsRedacted(t *testing.T) {
	service, db, smtp := testService(t)
	channels := &recordingChannelSender{errFor: map[string]error{}}
	service.sender = smtp
	service.channelSender = channels
	view, err := service.SaveChannel("local", ChannelInput{
		Name: "Gotify home", Type: ChannelGotify, Enabled: true,
		URL: "https://gotify.example.com", Token: "super-secret-token", Priority: 5,
		NotifyUpdates: true, NotifyErrors: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !view.Configured || !view.HasToken || strings.Contains(view.Target, "super-secret") {
		t.Fatalf("unexpected public channel view: %#v", view)
	}
	var row ChannelConfig
	if err := db.First(&row, view.ID).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(row.EncryptedConfig), "super-secret-token") || row.SecretKey == "" {
		t.Fatal("notification channel secret was not encrypted")
	}
	views, err := service.ListChannels("local")
	if err != nil || len(views) != 1 || views[0].Target != "https://gotify.example.com" {
		t.Fatalf("unexpected channel list: %#v, %v", views, err)
	}
	encoded, _ := json.Marshal(views)
	if strings.Contains(string(encoded), "super-secret-token") {
		t.Fatal("channel API view leaked the token")
	}
}

func TestNotificationFanoutIsIndependentAndFailedChannelRetries(t *testing.T) {
	base, db, smtp := testService(t)
	channels := &recordingChannelSender{errFor: map[string]error{"broken": errors.New("endpoint rejected secret-value")}}
	service := base
	service.sender = smtp
	service.channelSender = channels
	for _, name := range []string{"working", "broken"} {
		if _, err := service.SaveChannel("local", ChannelInput{Name: name, Type: ChannelWebhook, Enabled: true, URL: "https://hooks.example.com/dockman", Token: "secret-value", NotifyUpdates: true, NotifyErrors: true}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	run := updater.UpdateScanRun{Host: "local", Trigger: "scheduled", Schedule: "0 4 * * *", CompletedAt: &now}
	checks := []updater.ContainerUpdateCheck{{ContainerName: "web", Image: "web:latest", Status: updater.ContainerUpdateAvailable}}
	firstErr := service.NotifyScan(context.Background(), run, checks)
	if firstErr == nil {
		t.Fatal("expected the broken channel error")
	}
	if strings.Contains(firstErr.Error(), "secret-value") || !strings.Contains(firstErr.Error(), "[redacted]") {
		t.Fatalf("notification error leaked a credential: %v", firstErr)
	}
	if len(channels.events) != 2 {
		t.Fatalf("fan-out stopped after one channel: %d", len(channels.events))
	}
	channels.errFor["broken"] = nil
	if err := service.NotifyScan(context.Background(), run, checks); err != nil {
		t.Fatal(err)
	}
	if len(channels.events) != 3 || channels.rows[2].Name != "broken" {
		t.Fatalf("successful channel was duplicated or failed channel did not retry: %#v", channels.rows)
	}
	var deliveries []Delivery
	if err := db.Order("id ASC").Find(&deliveries).Error; err != nil {
		t.Fatal(err)
	}
	failedError := ""
	for _, delivery := range deliveries {
		if !delivery.Success {
			failedError = delivery.Error
		}
	}
	if len(deliveries) != 3 || strings.Contains(failedError, "secret-value") || !strings.Contains(failedError, "[redacted]") {
		t.Fatalf("delivery audit did not safely record fan-out: %#v", deliveries)
	}
}

func TestChannelPayloadsAndEndpointHardening(t *testing.T) {
	event := ChannelEvent{Kind: "update", Host: "local", Title: "Updated", Message: "web updated", Severity: "success", Time: time.Now()}
	endpoint, _, body, err := channelRequest(ChannelConfig{Type: ChannelGotify, Priority: 4}, channelSecrets{URL: "https://gotify.example.com", Token: "token"}, event)
	if err != nil || endpoint != "https://gotify.example.com/message?token=token" || !strings.Contains(string(body), `"priority":4`) {
		t.Fatalf("unexpected Gotify request: %q %s %v", endpoint, body, err)
	}
	endpoint, headers, body, err := channelRequest(ChannelConfig{Type: ChannelNtfy, Topic: "dockman", Tags: "docker,update"}, channelSecrets{URL: "https://ntfy.sh", Token: "token"}, event)
	if err != nil || endpoint != "https://ntfy.sh/dockman" || headers["Authorization"] != "Bearer token" || !strings.Contains(string(body), "docker") {
		t.Fatalf("unexpected ntfy request: %q %#v %s %v", endpoint, headers, body, err)
	}
	for _, raw := range []string{"http://example.com/hook", "https://localhost/hook", "https://169.254.169.254/latest/meta-data"} {
		if _, err := secureNotificationClient(raw, false, time.Second); err == nil {
			t.Fatalf("unsafe endpoint accepted: %s", raw)
		}
	}
}

func TestChannelEventSubscriptionsAreIndependent(t *testing.T) {
	service, _, _ := testService(t)
	sender := &recordingChannelSender{errFor: map[string]error{}}
	service.channelSender = sender
	_, err := service.SaveChannel("local", ChannelInput{Name: "builds", Type: ChannelWebhook, Enabled: true, URL: "https://hooks.example.com/builds", Events: []string{EventBuildSuccess}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.SaveChannel("local", ChannelInput{Name: "git", Type: ChannelWebhook, Enabled: true, URL: "https://hooks.example.com/git", Events: []string{EventGitSyncFailure}})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Publish(context.Background(), ChannelEvent{Kind: EventBuildSuccess, Host: "local", Title: "Built"}); err != nil {
		t.Fatal(err)
	}
	if len(sender.rows) != 1 || sender.rows[0].Name != "builds" {
		t.Fatalf("event was delivered to the wrong subscriptions: %#v", sender.rows)
	}
	if _, err := service.SaveChannel("local", ChannelInput{Name: "invalid", Type: ChannelWebhook, Enabled: true, URL: "https://hooks.example.com/invalid", Events: []string{"unknown.event"}}); err == nil {
		t.Fatal("unknown event subscription was accepted")
	}
}

func TestMultipleSMTPChannelsAndLegacyMigration(t *testing.T) {
	service, db, _ := testService(t)
	legacyPassword, err := service.vault.Encrypt([]byte("legacy-secret"), "local")
	if err != nil {
		t.Fatal(err)
	}
	legacy := SMTPConfig{Host: "local", Enabled: true, Server: "smtp.legacy.example", Port: 587, Security: SecuritySTARTTLS, Username: "dockman", EncryptedPassword: legacyPassword, FromAddress: "dockman@example.com", Recipients: "ops@example.com", NotifyUpdates: true, NotifyErrors: true}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.MigrateLegacySMTPConfigs(); err != nil {
		t.Fatal(err)
	}
	second, err := service.SaveChannel("local", ChannelInput{Name: "SMTP backup", Type: ChannelSMTP, Enabled: true, Server: "smtp.backup.example", Port: 465, Security: SecurityTLS, FromAddress: "dockman@example.com", Recipients: "backup@example.com", Events: []string{EventBuildFailure}})
	if err != nil {
		t.Fatal(err)
	}
	views, err := service.ListChannels("local")
	if err != nil || len(views) != 2 || second.Type != ChannelSMTP {
		t.Fatalf("multiple SMTP channels were not preserved: %#v, %v", views, err)
	}
	var legacyCount int64
	if err := db.Model(&SMTPConfig{}).Count(&legacyCount).Error; err != nil || legacyCount != 0 {
		t.Fatalf("legacy SMTP row still active: count=%d err=%v", legacyCount, err)
	}
	// A second startup is idempotent and cannot recreate or duplicate SMTP.
	if err := service.MigrateLegacySMTPConfigs(); err != nil {
		t.Fatal(err)
	}
	views, err = service.ListChannels("local")
	if err != nil || len(views) != 2 {
		t.Fatalf("SMTP migration was not idempotent: %#v, %v", views, err)
	}
}

func TestUpdateNotificationSubscriptionsDoNotCrossDeliver(t *testing.T) {
	service, _, _ := testService(t)
	sender := &recordingChannelSender{errFor: map[string]error{}}
	service.channelSender = sender
	_, err := service.SaveChannel("local", ChannelInput{Name: "success-only", Type: ChannelWebhook, Enabled: true, URL: "https://hooks.example.com/success", Events: []string{EventUpdateSuccess}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := service.NotifyScan(context.Background(), updater.UpdateScanRun{Host: "local", Trigger: "scheduled", Schedule: "0 4 * * *", CompletedAt: &now}, []updater.ContainerUpdateCheck{{ContainerName: "web", Image: "web:latest", Status: updater.ContainerUpdateAvailable}}); err != nil {
		t.Fatal(err)
	}
	if len(sender.events) != 0 {
		t.Fatalf("success-only channel received an availability scan: %#v", sender.events)
	}
	if err := service.NotifyExecution(context.Background(), updater.UpdateExecutionRun{Host: "local", Updated: 1, CompletedAt: &now}, []updater.UpdateExecutionOutcome{{UpdateExecutionTarget: updater.UpdateExecutionTarget{ContainerName: "web", Image: "web:latest"}, State: updater.ExecutionUpdated}}); err != nil {
		t.Fatal(err)
	}
	if len(sender.events) != 1 || sender.events[0].Kind != EventUpdateSuccess {
		t.Fatalf("success event was not delivered exactly once: %#v", sender.events)
	}
}
