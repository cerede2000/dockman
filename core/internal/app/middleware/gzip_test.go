package middleware

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func gzipRequest(t *testing.T, handler http.Handler, accept string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/index.html", nil)
	if accept != "" {
		request.Header.Set("Accept-Encoding", accept)
	}
	recorder := httptest.NewRecorder()
	Gzip(handler).ServeHTTP(recorder, request)
	return recorder
}

func TestGzipCompressesTextAndDecodesBack(t *testing.T) {
	body := strings.Repeat("dockman ", 200)
	recorder := gzipRequest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, body)
	}), "gzip")

	require.Equal(t, "gzip", recorder.Header().Get("Content-Encoding"))
	require.Equal(t, "Accept-Encoding", recorder.Header().Get("Vary"))
	require.Empty(t, recorder.Header().Get("Content-Length"),
		"the compressed length is not the one the handler announced")

	reader, err := gzip.NewReader(recorder.Body)
	require.NoError(t, err)
	decoded, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, body, string(decoded))
	require.Less(t, recorder.Body.Len(), len(body))
}

// Announcing an encoding the body does not carry is how a page arrives as
// binary noise. A response with no body at all must not be announced as gzip.
func TestGzipLeavesBodylessResponsesAlone(t *testing.T) {
	// 206 is here for a different reason: the range it answers is a range of
	// the original entity, so compressing it returns bytes that no longer match
	// the Content-Range describing them.
	for _, status := range []int{http.StatusNoContent, http.StatusNotModified, http.StatusPartialContent} {
		recorder := gzipRequest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}), "gzip")
		require.Empty(t, recorder.Header().Get("Content-Encoding"), "status %d", status)
		require.Equal(t, "Accept-Encoding", recorder.Header().Get("Vary"),
			"the response still varies by the header, whether or not it was compressed")
	}
}

// Recompressing a PNG or a woff2 spends CPU to make the payload very slightly
// bigger. The SPA serves both out of assets/.
func TestGzipSkipsAlreadyCompressedTypes(t *testing.T) {
	for _, contentType := range []string{"image/png", "font/woff2", "image/webp", "video/mp4", "application/zip"} {
		recorder := gzipRequest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", contentType)
			_, _ = w.Write([]byte("payload"))
		}), "gzip")
		require.Empty(t, recorder.Header().Get("Content-Encoding"), contentType)
		require.Equal(t, "payload", recorder.Body.String(), contentType)
	}
}

func TestGzipCompressesSVGWhichIsText(t *testing.T) {
	recorder := gzipRequest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = io.WriteString(w, strings.Repeat("<rect/>", 100))
	}), "gzip")
	require.Equal(t, "gzip", recorder.Header().Get("Content-Encoding"))
}

func TestGzipPassesThroughWithoutTheHeader(t *testing.T) {
	recorder := gzipRequest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "plain")
	}), "")
	require.Empty(t, recorder.Header().Get("Content-Encoding"))
	require.Equal(t, "plain", recorder.Body.String())
}

// A handler that flushes expects its bytes to have left. Without a Flush that
// reaches the gzip writer first, they sit in its buffer and a streaming
// response stalls with the client waiting on data already written.
func TestGzipFlushReachesTheClient(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/logs", nil)
	request.Header.Set("Accept-Encoding", "gzip")

	// What the client had received at the moment the handler flushed. A gzip
	// header alone would satisfy a mere "not empty", so the check below decodes
	// this prefix: the chunk has either left the compressor or it has not.
	var atFlush []byte
	Gzip(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "first chunk")
		flusher, ok := w.(http.Flusher)
		require.True(t, ok)
		flusher.Flush()
		atFlush = append([]byte(nil), recorder.Body.Bytes()...)
	})).ServeHTTP(recorder, request)

	require.True(t, recorder.Flushed)

	reader, err := gzip.NewReader(bytes.NewReader(atFlush))
	require.NoError(t, err)
	decoded, err := io.ReadAll(reader)
	// The stream is deliberately truncated mid-response, so an unexpected EOF
	// is the expected shape; what matters is the payload that came before it.
	if err != nil {
		require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	}
	require.Equal(t, "first chunk", string(decoded),
		"the chunk sat in the compressor's buffer instead of going out")
}

// A response that already carries an encoding must not be wrapped in a second
// one.
func TestGzipDoesNotDoubleEncode(t *testing.T) {
	recorder := gzipRequest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "br")
		_, _ = w.Write([]byte("already compressed"))
	}), "gzip")
	require.Equal(t, "br", recorder.Header().Get("Content-Encoding"))
	require.Equal(t, "already compressed", recorder.Body.String())
}
