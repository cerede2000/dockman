package middleware

import (
	"net/http"
	"time"

	"github.com/rs/zerolog/log"
)

func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		subLog := log.With().
			Str("url", r.URL.String()).
			Str("method", r.Method).
			Logger()

		if r.Header.Get("Connect-Protocol-Version") != "" {
			subLog.Debug().Str("url", r.URL.String()).
				Msg("connect rpc")
			next.ServeHTTP(w, r)
			return
		}

		// WebSocket request, don't
		// wrap the writer to avoid hijacking errs
		if r.Header.Get("Upgrade") == "websocket" {
			subLog.Debug().Str("url", r.URL.String()).
				Msg("websocket connection")
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		//subLog.Debug().Msg("Started request")

		next.ServeHTTP(wrapped, r)

		subLog.Debug().
			//Int("status", wrapped.statusCode).
			Dur("elapsed", time.Since(start)).
			Msg("Completed request")
	})
}

// responseWriter records the status code for the log line.
//
// http.Flusher used to be embedded here as an anonymous interface and was never
// assigned. That published a Flush method backed by a nil interface: a handler
// asking `w.(http.Flusher)` got ok=true and panicked as soon as it flushed.
// Streaming handlers do exactly that - GET /events in internal/files is a
// Server-Sent Events endpoint sitting under /api, which this middleware wraps
// whenever LOG_HTTP is on.
//
// Flush is now delegated explicitly, and Unwrap lets http.ResponseController
// reach past the wrapper for everything else, hijacking included.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

// Unwrap exposes the wrapped writer to http.ResponseController, so a capability
// this type does not implement itself is still reachable through it.
func (rw *responseWriter) Unwrap() http.ResponseWriter { return rw.ResponseWriter }

// Flush forwards to the real writer. Every net/http response writer supports
// flushing, so the no-op branch only guards against an exotic wrapper further
// down the chain rather than any real deployment.
func (rw *responseWriter) Flush() {
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// Optional: If Write is called without calling WriteHeader,
// Go defaults to 200 OK. We should account for that.
func (rw *responseWriter) Write(b []byte) (int, error) {
	if rw.statusCode == 0 {
		rw.statusCode = http.StatusOK
	}
	return rw.ResponseWriter.Write(b)
}
