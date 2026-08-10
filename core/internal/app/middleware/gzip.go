package middleware

import (
	"bufio"
	"compress/gzip"
	"errors"
	"net"
	"net/http"
	"strings"
)

// gzipResponseWriter compresses the body when it is worth compressing.
//
// The decision cannot be made before the handler runs, because it depends on
// the status code and the content type the handler sets. The previous version
// announced Content-Encoding: gzip up front and wrapped everything: a 204 or a
// 304 was labelled as carrying a gzip body it does not have, an already
// encoded response was encoded twice, and every PNG and woff2 the SPA serves
// was recompressed to come out marginally larger.
type gzipResponseWriter struct {
	http.ResponseWriter
	writer      *gzip.Writer
	wroteHeader bool
}

// compressible reports whether a body of this type is worth compressing.
// Images, fonts, audio and video are already compressed; running them through
// gzip spends CPU on both ends to grow the payload.
func compressible(contentType string) bool {
	mediaType := strings.TrimSpace(strings.ToLower(contentType))
	if semicolon := strings.IndexByte(mediaType, ';'); semicolon >= 0 {
		mediaType = strings.TrimSpace(mediaType[:semicolon])
	}
	switch {
	case mediaType == "":
		// No type declared: the SPA's own files all carry one, so this is an
		// unusual response and compressing it blind is not worth the risk.
		return false
	case mediaType == "image/svg+xml":
		// An SVG is markup, whatever its top-level type says.
		return true
	case strings.HasPrefix(mediaType, "image/"),
		strings.HasPrefix(mediaType, "video/"),
		strings.HasPrefix(mediaType, "audio/"),
		strings.HasPrefix(mediaType, "font/"):
		return false
	case mediaType == "application/zip",
		mediaType == "application/gzip",
		mediaType == "application/x-gzip",
		mediaType == "application/zstd",
		mediaType == "application/x-7z-compressed",
		mediaType == "application/vnd.ms-fontobject",
		mediaType == "application/font-woff",
		mediaType == "application/font-woff2":
		return false
	}
	return true
}

func (w *gzipResponseWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	header := w.Header()
	// A body-less status has nothing to compress, and a response that already
	// carries an encoding must not be wrapped in a second one.
	//
	// 206 is excluded for a different reason: the byte range it answers is a
	// range of the original entity, so compressing it would return the right
	// bytes under a Content-Range that no longer describes them. http.ServeFileFS
	// serves the SPA and answers Range requests, so this is reachable.
	bodyless := code == http.StatusNoContent || code == http.StatusNotModified ||
		code == http.StatusPartialContent || code < http.StatusOK
	if !bodyless && header.Get("Content-Encoding") == "" && compressible(header.Get("Content-Type")) {
		header.Set("Content-Encoding", "gzip")
		// The handler announced the length of the uncompressed body.
		header.Del("Content-Length")
		w.writer = gzip.NewWriter(w.ResponseWriter)
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.writer != nil {
		return w.writer.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

// Flush pushes the compressor's buffer out before flushing the connection.
// Flushing only the connection leaves the bytes sitting in the gzip buffer, so
// a streaming handler that believes it has sent a chunk has sent nothing.
func (w *gzipResponseWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.writer != nil {
		_ = w.writer.Flush()
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Hijack lets a connection upgrade - a WebSocket, say - pass through. There is
// no response body to compress once the connection is taken over.
func (w *gzipResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("the underlying writer does not support hijacking")
	}
	return hijacker.Hijack()
}

func (w *gzipResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *gzipResponseWriter) Close() error {
	if w.writer == nil {
		return nil
	}
	return w.writer.Close()
}

func Gzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The response depends on this header whether or not it ends up
		// compressed, so it is announced either way: a cache that stored one
		// form and served it to a client expecting the other would hand out a
		// body that client cannot read.
		w.Header().Add("Vary", "Accept-Encoding")

		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}

		gzw := &gzipResponseWriter{ResponseWriter: w}
		defer func() {
			_ = gzw.Close()
		}()
		next.ServeHTTP(gzw, r)
	})
}
