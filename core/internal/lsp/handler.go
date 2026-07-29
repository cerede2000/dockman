package lsp

import (
	"fmt"
	"github.com/RA341/dockman/pkg/fileutil"
	wsu "github.com/RA341/dockman/pkg/ws"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
	"net/http"
)

var DefaultUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     wsu.CheckOrigin,
}

// WebSocketHandler returns an http.Handler that upgrades the connection
// to a WebSocket and starts an LSP session.
func WebSocketHandler(up websocket.Upgrader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Debug().Msg("starting lsp")

		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to upgrade connection: %v", err), http.StatusInternalServerError)
			return
		}
		// Ensure connection is closed when function exits
		defer fileutil.Close(conn)
		defer wsu.KeepAlive(r.Context(), conn)()

		stream := &WebSocketStream{conn: conn}
		if err = StartLSP(WithStream(stream), WithZapLogger()); err != nil {
			log.Error().Err(err).Msg("Failed to start LSP server")
			// Optionally send close message with error
			_ = wsu.WriteMessage(conn,
				websocket.CloseMessage,
				websocket.FormatCloseMessage(
					websocket.CloseInternalServerErr, err.Error(),
				),
			)
			return
		}
	}
}

// WebSocketStream wraps a websocket connection to implement io.ReadWriteCloser
// The websocket.Conn satisfies the io.ReadWriteCloser interface,
// But we need to wrap it to handle message types
//
// Why: When the jsonrpc2 library calls stream.Read(),
// it would be calling the low-level websocket.Conn.Read() method
//
// This method is not guaranteed to return a complete WebSocket message. It might just return the first few bytes of the message frame.
// This would confuse the parser, which is expecting a full header and JSON body.
type WebSocketStream struct {
	conn *websocket.Conn
}

func (s *WebSocketStream) Read(p []byte) (n int, err error) {
	_, r, err := s.conn.NextReader()
	if err != nil {
		return 0, err
	}
	return r.Read(p)
}

func (s *WebSocketStream) Write(p []byte) (n int, err error) {
	err = wsu.WriteMessage(s.conn, websocket.TextMessage, p)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *WebSocketStream) Close() error {
	return s.conn.Close()
}
