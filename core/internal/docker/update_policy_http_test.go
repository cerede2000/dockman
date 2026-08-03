package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RA341/dockman/internal/docker/updater"
	hostMid "github.com/RA341/dockman/internal/host/middleware"
	"github.com/RA341/dockman/internal/notifications"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type testNotificationSender struct {
	messages []notifications.SMTPMessage
}

func (s *testNotificationSender) Send(_ context.Context, message notifications.SMTPMessage) error {
	s.messages = append(s.messages, message)
	return nil
}

func testUpdatePolicyHandler(t *testing.T) http.Handler {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&updater.UpdatePolicy{}); err != nil {
		t.Fatal(err)
	}
	service := updater.NewPolicyService(updater.NewPolicyStore(db))
	return NewHandlerHttp(nil, false, service)
}

func policyRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request = request.WithContext(hostMid.SetHost(context.Background(), "local"))
	return request
}

func TestUpdatePolicyHTTPRoundTrip(t *testing.T) {
	handler := testUpdatePolicyHandler(t)
	save := httptest.NewRecorder()
	handler.ServeHTTP(save, policyRequest(http.MethodPut, "/updates/policies", `{
		"targetType":"container","targetKey":"web","targetName":"web",
		"enabled":true,"schedule":"0 4 * * *","rollbackEnabled":true
	}`))
	if save.Code != http.StatusNoContent {
		t.Fatalf("save status %d: %s", save.Code, save.Body.String())
	}

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, policyRequest(http.MethodGet, "/updates/policies", ""))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"targetKey":"web"`) {
		t.Fatalf("unexpected list response %d: %s", list.Code, list.Body.String())
	}

	remove := httptest.NewRecorder()
	handler.ServeHTTP(remove, policyRequest(http.MethodDelete, "/updates/policies?targetType=container&targetKey=web", ""))
	if remove.Code != http.StatusNoContent {
		t.Fatalf("delete status %d: %s", remove.Code, remove.Body.String())
	}
}

func TestUpdatePolicyHTTPRejectsInvalidSchedule(t *testing.T) {
	handler := testUpdatePolicyHandler(t)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, policyRequest(http.MethodPut, "/updates/policies", `{
		"targetType":"container","targetKey":"web","targetName":"web",
		"enabled":true,"schedule":"invalid","rollbackEnabled":true
	}`))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid image check schedule") {
		t.Fatalf("unexpected response %d: %s", response.Code, response.Body.String())
	}
}

func TestEnrolledUpdateScanHTTP(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&updater.UpdateScanResult{}, &updater.UpdateScanRun{}); err != nil {
		t.Fatal(err)
	}
	automation, err := updater.NewAutomationService(
		updater.NewScanStore(db),
		func(context.Context, string) ([]updater.UpdateEnrollment, error) {
			return []updater.UpdateEnrollment{{ContainerID: "web-id", ContainerName: "web", Enrolled: true}}, nil
		},
		func(context.Context, string, []string) ([]updater.ContainerUpdateCheck, error) {
			return []updater.ContainerUpdateCheck{{ContainerID: "web-id", ContainerName: "web", Status: updater.ContainerUpdateCurrent}}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = automation.Shutdown() })
	handler := NewHandlerHttpWithUpdates(nil, false, nil, automation)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, policyRequest(http.MethodPost, "/updates/scan", ""))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"current":1`) {
		t.Fatalf("unexpected scan response %d: %s", response.Code, response.Body.String())
	}
}

func TestSMTPNotificationHTTPNeverReturnsPassword(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&notifications.SMTPConfig{}, &notifications.NotificationState{}, &notifications.Delivery{}); err != nil {
		t.Fatal(err)
	}
	vault, err := notifications.NewVault([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	sender := &testNotificationSender{}
	service := notifications.NewServiceWithSender(db, vault, sender)
	handler := NewHandlerHttpWithUpdates(nil, false, nil, nil, service)

	save := httptest.NewRecorder()
	handler.ServeHTTP(save, policyRequest(http.MethodPut, "/updates/notifications/smtp", `{
		"enabled":true,"server":"smtp.example.com","port":587,"security":"starttls",
		"username":"dockman","password":"top-secret","fromAddress":"dockman@example.com",
		"recipients":"ops@example.com","notifyUpdates":true,"notifyErrors":true
	}`))
	if save.Code != http.StatusOK || strings.Contains(save.Body.String(), "top-secret") || strings.Contains(save.Body.String(), `"password"`) {
		t.Fatalf("save leaked the SMTP password (%d): %s", save.Code, save.Body.String())
	}

	get := httptest.NewRecorder()
	handler.ServeHTTP(get, policyRequest(http.MethodGet, "/updates/notifications/smtp", ""))
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"hasPassword":true`) || strings.Contains(get.Body.String(), "top-secret") || strings.Contains(get.Body.String(), `"password"`) {
		t.Fatalf("get leaked or lost the SMTP password state (%d): %s", get.Code, get.Body.String())
	}

	test := httptest.NewRecorder()
	handler.ServeHTTP(test, policyRequest(http.MethodPost, "/updates/notifications/smtp/test", ""))
	if test.Code != http.StatusNoContent || len(sender.messages) != 1 || sender.messages[0].Password != "top-secret" {
		t.Fatalf("unexpected SMTP test result (%d): messages=%#v body=%s", test.Code, sender.messages, test.Body.String())
	}
}
