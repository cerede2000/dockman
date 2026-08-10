package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// The wrapper used to embed http.Flusher as an anonymous field and never set
// it. The method set therefore advertised Flush, the assertion below succeeded,
// and calling it dereferenced a nil interface.
//
// This is not hypothetical: GET /events in internal/files is a Server-Sent
// Events endpoint that asserts http.Flusher and flushes on every message, and
// it sits under /api, which this middleware wraps whenever LOG_HTTP is on. The
// stream died on its first flush, the browser reconnected, and it died again.
func TestLoggingMiddlewareFlushReachesTheUnderlyingWriter(t *testing.T) {
	flushed := false
	handler := LoggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		require.True(t, ok, "a streaming handler asks for this before writing anything")
		_, _ = io.WriteString(w, ": connected\n\n")
		require.NotPanics(t, flusher.Flush, "the wrapper must not advertise a flush it cannot perform")
		flushed = true
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/files/events", nil))

	require.True(t, flushed)
	require.True(t, recorder.Flushed, "the flush has to reach the real writer, not stop at the wrapper")
	require.Equal(t, ": connected\n\n", recorder.Body.String())
}

// http.ResponseController is the modern way to reach a flusher, and it needs
// Unwrap to see through the wrapper.
func TestLoggingMiddlewareSupportsResponseController(t *testing.T) {
	handler := LoggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, http.NewResponseController(w).Flush())
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/files/events", nil))

	require.True(t, recorder.Flushed)
}

// The status code is the only thing this wrapper exists to capture.
func TestLoggingMiddlewareRecordsTheStatusCode(t *testing.T) {
	handler := LoggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/anything", nil))

	require.Equal(t, http.StatusTeapot, recorder.Code)
}
