package ws

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
)

type validatedOriginContextKey struct{}

// WithValidatedOrigin records that Dockman's global origin allow-list accepted
// the request. It lets WebSocket upgraders keep a second, local boundary while
// still supporting explicitly configured administration origins.
func WithValidatedOrigin(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), validatedOriginContextKey{}, true))
}

// CheckOrigin accepts non-browser clients without an Origin header, direct
// same-origin browser requests, and origins previously approved by the global
// policy. A handler accidentally mounted without that policy fails closed for
// foreign browser origins.
func CheckOrigin(r *http.Request) bool {
	origin := strings.TrimSuffix(strings.TrimSpace(r.Header.Get("Origin")), "/")
	if origin == "" {
		return true
	}
	if validated, _ := r.Context().Value(validatedOriginContextKey{}).(bool); validated {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Scheme != "" && parsed.Host != "" && strings.EqualFold(parsed.Host, r.Host)
}

type WsWriter struct {
	ws *websocket.Conn
}

const writeTimeout = 15 * time.Second

const (
	pongWait   = 90 * time.Second
	pingPeriod = 30 * time.Second
)

func WriteMessage(ws *websocket.Conn, messageType int, data []byte) error {
	if err := ws.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	return ws.WriteMessage(messageType, data)
}

// LimitClientMessages bounds browser-to-server frames without constraining
// server-to-browser log and terminal streams. Terminal input, resize events
// and file-search queries are all tiny; 1 MiB leaves ample compatibility
// headroom while preventing a single frame from exhausting server memory.
func LimitClientMessages(ws *websocket.Conn) {
	ws.SetReadLimit(1 << 20)
}

// KeepAlive detects browsers and network paths which disappeared without a
// WebSocket close frame. Protocol control writes are safe alongside the
// existing stream writer; pong frames extend the read deadline without being
// exposed to terminal, log, search, or LSP consumers.
func KeepAlive(ctx context.Context, ws *websocket.Conn) func() {
	keepAliveCtx, cancel := context.WithCancel(ctx)
	_ = ws.SetReadDeadline(time.Now().Add(pongWait))
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(pongWait))
	})
	go func() {
		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-keepAliveCtx.Done():
				return
			case <-ticker.C:
				if err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout)); err != nil {
					_ = ws.Close()
					return
				}
			}
		}
	}()
	return cancel
}

func NewWsWriter(ws *websocket.Conn) *WsWriter {
	return &WsWriter{
		ws: ws,
	}
}

func (w *WsWriter) Write(p []byte) (n int, err error) {
	err = WriteMessage(w.ws, websocket.TextMessage, p)
	if err != nil {
		log.Warn().Err(err).Msg("Unable to write to websocket")
		return len(p), err
	}

	return len(p), nil
}

const (
	AnsiGreen  = "\x1b[32m"
	AnsiRed    = "\x1b[31m"
	AnsiReset  = "\x1b[0m"
	AnsiYellow = "\x1b[33m"
	newLine    = "\r\n"
)

func WWrn(ws *websocket.Conn, message string) {
	message = formatTermMessage(message, AnsiYellow)
	WsMustWrite(ws, message)
}

func WInf(ws *websocket.Conn, message string) {
	message = formatTermMessage(message, AnsiGreen)
	WsMustWrite(ws, message)
}

func WErr(ws *websocket.Conn, errMessage error) {
	message := formatTermMessage(errMessage.Error(), AnsiRed)
	WsMustWrite(ws, message)
}

func formatTermMessage(message string, color string) string {
	message = color + message + AnsiReset + newLine
	return message
}

func WsMustWrite(ws *websocket.Conn, message string) {
	err := WriteMessage(ws, websocket.TextMessage, []byte(message))
	if err != nil {
		log.Warn().Err(err).Msg("Failed to write to socket")
		return
	}
}
