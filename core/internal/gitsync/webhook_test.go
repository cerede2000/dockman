package gitsync

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func signedWebhookRequest(t *testing.T, path, event, delivery, secret, payload string) *http.Request {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	_, err := mac.Write([]byte(payload))
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", delivery)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return req
}

func TestGitHubWebhookValidatesSignatureRepositoryBranchAndReplay(t *testing.T) {
	service, _ := testService(t, true)
	repository := Repository{
		UUID: uuid.NewString(), Name: "webhook-repository", Provider: "github",
		RemoteURL: "https://github.com/cerede2000/dockman.git", DefaultBranch: "integration", Mode: "managed", Status: "ready",
	}
	require.NoError(t, service.store.SaveRepository(&repository))
	configured, err := service.ConfigureRepositoryWebhook(repository.UUID, RepositoryWebhookInput{Enabled: true})
	require.NoError(t, err)
	require.NotEmpty(t, configured.Secret)

	payload := `{"ref":"refs/heads/integration","deleted":false,"repository":{"full_name":"cerede2000/dockman"}}`
	handler := NewWebhookHandler(service)
	response := httptest.NewRecorder()
	handlerPath := strings.TrimPrefix(configured.Path, "/api/git-webhooks")
	handler.ServeHTTP(response, signedWebhookRequest(t, handlerPath, "push", "delivery-1", configured.Secret, payload))
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	require.Contains(t, service.webhookPending, repository.UUID)

	replay := httptest.NewRecorder()
	handler.ServeHTTP(replay, signedWebhookRequest(t, handlerPath, "push", "delivery-1", configured.Secret, payload))
	require.Equal(t, http.StatusConflict, replay.Code)

	badSignature := signedWebhookRequest(t, handlerPath, "push", "delivery-2", "wrong-secret", payload)
	badResponse := httptest.NewRecorder()
	handler.ServeHTTP(badResponse, badSignature)
	require.Equal(t, http.StatusUnauthorized, badResponse.Code)
}

func TestGitHubWebhookIgnoresOtherBranches(t *testing.T) {
	service, _ := testService(t, true)
	repository := Repository{
		UUID: uuid.NewString(), Name: "branch-webhook", Provider: "github",
		RemoteURL: "git@github.com:cerede2000/dockman.git", DefaultBranch: "integration", Mode: "managed", Status: "ready",
	}
	require.NoError(t, service.store.SaveRepository(&repository))
	configured, err := service.ConfigureRepositoryWebhook(repository.UUID, RepositoryWebhookInput{Enabled: true})
	require.NoError(t, err)
	payload := `{"ref":"refs/heads/main","deleted":false,"repository":{"full_name":"cerede2000/dockman"}}`
	response := httptest.NewRecorder()
	handlerPath := strings.TrimPrefix(configured.Path, "/api/git-webhooks")
	NewWebhookHandler(service).ServeHTTP(response, signedWebhookRequest(t, handlerPath, "push", "delivery-other", configured.Secret, payload))
	require.Equal(t, http.StatusAccepted, response.Code)
	require.NotContains(t, service.webhookPending, repository.UUID)
	view, err := service.RepositoryWebhook(repository.UUID)
	require.NoError(t, err)
	require.Equal(t, "ignored", view.LastStatus)
}
