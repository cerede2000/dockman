package gitsync

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDisabledHandlerOnlyExposesStatus(t *testing.T) {
	service, _ := testService(t, false)
	handler := NewHTTPHandler(service)

	status := httptest.NewRecorder()
	handler.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/status", nil))
	require.Equal(t, http.StatusOK, status.Code)
	require.Contains(t, status.Body.String(), `"enabled":false`)
	require.Contains(t, status.Body.String(), `"repositorySyncAvailable":false`)

	credentials := httptest.NewRecorder()
	handler.ServeHTTP(credentials, httptest.NewRequest(http.MethodGet, "/credentials", nil))
	require.Equal(t, http.StatusNotFound, credentials.Code)
}

func TestEnabledStatusAdvertisesManualRepositorySync(t *testing.T) {
	service, _ := testService(t, true)
	response := httptest.NewRecorder()
	NewHTTPHandler(service).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/status", nil))
	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), `"phase":"manual_repository_sync"`)
	require.Contains(t, response.Body.String(), `"repositorySyncAvailable":true`)
}

func TestCredentialAPIResponseDoesNotContainSecret(t *testing.T) {
	service, _ := testService(t, true)
	handler := NewHTTPHandler(service)
	body := `{"name":"github","authType":"https_token","token":"test-token-never-return-me"}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/credentials", strings.NewReader(body)))
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.NotContains(t, response.Body.String(), "test-token-never-return-me")
	var view CredentialView
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &view))
	require.True(t, view.HasSecret)

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/credentials", nil))
	require.Equal(t, http.StatusOK, list.Code)
	require.NotContains(t, list.Body.String(), "test-token-never-return-me")
}
