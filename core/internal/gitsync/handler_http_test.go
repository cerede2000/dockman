package gitsync

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestEnabledStatusAdvertisesSafeAutomation(t *testing.T) {
	service, _ := testService(t, true)
	response := httptest.NewRecorder()
	NewHTTPHandler(service).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/status", nil))
	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), `"phase":"safe_automation"`)
	require.Contains(t, response.Body.String(), `"repositorySyncAvailable":true`)
	require.Contains(t, response.Body.String(), `"stackSyncAvailable":true`)
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

func TestGitStackStatusAPIListsAndPausesNestedStack(t *testing.T) {
	service, _, binding := prepareMultiStackBinding(t)
	handler := NewHTTPHandler(service)

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/stack-statuses?host=local", nil))
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	var statuses []GitStackStatusView
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &statuses))
	require.Len(t, statuses, 2)

	pause := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/bindings/"+binding.ID+"/stack-status/alpha/compose.yml", strings.NewReader(`{"paused":true}`))
	handler.ServeHTTP(pause, request)
	require.Equal(t, http.StatusOK, pause.Code, pause.Body.String())
	var updated GitStackStatusView
	require.NoError(t, json.Unmarshal(pause.Body.Bytes(), &updated))
	require.Equal(t, "alpha/compose.yml", updated.ComposePath)
	require.True(t, updated.AutomationPaused)
}

func TestBindingAutomationAPIAcceptsPerStackTargets(t *testing.T) {
	service, _, binding := prepareMultiStackBinding(t)
	handler := NewHTTPHandler(service)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/bindings/"+binding.ID+"/automation", strings.NewReader(`{
		"enabled":true,
		"intervalMinutes":5,
		"autoSyncSelectionMode":"selected",
		"autoSyncComposePaths":["beta/compose.yml"]
	}`))
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	var updated BindingView
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &updated))
	require.Equal(t, composeSelectionSelected, updated.AutoSyncSelectionMode)
	require.Equal(t, []string{"beta/compose.yml"}, updated.AutoSyncComposePaths)
	require.Equal(t, []string{"alpha/compose.yml", "beta/compose.yml"}, updated.SelectedComposePaths)
}

func TestBindingPolicyTreeAPIUsesUnsavedPolicy(t *testing.T) {
	service, stackRoot, binding := prepareMultiStackBinding(t)
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "alpha", "debug.log"), []byte("debug\n"), 0o644))
	handler := NewHTTPHandler(service)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/bindings/"+binding.ID+"/policy-tree", strings.NewReader(`{
		"directory":"alpha",
		"profile":"compose_config",
		"includePatterns":["/alpha/debug.log"],
		"excludePatterns":[]
	}`))
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var view BindingPolicyTreeView
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &view))
	require.Equal(t, "included", policyTreeEntryMap(view.Entries)["alpha/debug.log"].State)
}
