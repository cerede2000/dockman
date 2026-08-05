package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RA341/dockman/internal/host/filesystem"
	hostmiddleware "github.com/RA341/dockman/internal/host/middleware"
	"github.com/stretchr/testify/require"
)

func testHTTPHandler(t *testing.T) http.Handler {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "demo"), 0o755))
	store := NewPlainFileStore(func(host, stackPath string) (filesystem.FileSystem, string, error) {
		require.Equal(t, "ssh-node", host)
		require.Equal(t, "compose/demo", stackPath)
		return filesystem.NewLocal(root), "demo", nil
	})
	return NewHTTPHandler(NewService(store))
}

func requestWithHost(method, target string, body []byte) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req = req.WithContext(hostmiddleware.SetHost(context.Background(), "ssh-node"))
	return req
}

func TestHTTPSecretLifecycleNeverReturnsPlaintextExceptExplicitReveal(t *testing.T) {
	handler := testHTTPHandler(t)
	payload, err := json.Marshal(writeInput{StackPath: "compose/demo", Value: "super-secret-value"})
	require.NoError(t, err)
	writeRecorder := httptest.NewRecorder()
	handler.ServeHTTP(writeRecorder, requestWithHost(http.MethodPut, "/api_token", payload))
	require.Equal(t, http.StatusOK, writeRecorder.Code)
	require.NotContains(t, writeRecorder.Body.String(), "super-secret-value")
	require.Equal(t, "no-store", writeRecorder.Header().Get("Cache-Control"))

	listRecorder := httptest.NewRecorder()
	handler.ServeHTTP(listRecorder, requestWithHost(http.MethodGet, "/?stack=compose%2Fdemo", nil))
	require.Equal(t, http.StatusOK, listRecorder.Code)
	require.Contains(t, listRecorder.Body.String(), "api_token")
	require.NotContains(t, listRecorder.Body.String(), "super-secret-value")

	readRecorder := httptest.NewRecorder()
	handler.ServeHTTP(readRecorder, requestWithHost(http.MethodGet, "/api_token?stack=compose%2Fdemo", nil))
	require.Equal(t, http.StatusOK, readRecorder.Code)
	require.NotContains(t, readRecorder.Body.String(), "super-secret-value", "explicit reveals remain base64 encoded in transport")
	require.Contains(t, readRecorder.Body.String(), "c3VwZXItc2VjcmV0LXZhbHVl")

	deleteRecorder := httptest.NewRecorder()
	handler.ServeHTTP(deleteRecorder, requestWithHost(http.MethodDelete, "/api_token?stack=compose%2Fdemo", nil))
	require.Equal(t, http.StatusNoContent, deleteRecorder.Code)
}

func TestHTTPSecretErrorsDoNotEchoSubmittedValue(t *testing.T) {
	handler := testHTTPHandler(t)
	recorder := httptest.NewRecorder()
	secret := "must-never-appear-in-error"
	body := []byte(`{"stackPath":"compose/demo","value":"` + secret + `","encoding":"invalid"}`)
	handler.ServeHTTP(recorder, requestWithHost(http.MethodPut, "/token", body))
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.False(t, strings.Contains(recorder.Body.String(), secret))
}
