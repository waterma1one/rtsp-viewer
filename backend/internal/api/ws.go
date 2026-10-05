package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/waterma1one/rtsp-viewer/backend/internal/stream"
)

// Wire protocol, server to client:
//
//	text   {"type":"status","state":"connecting|live|reconnecting|failed","message":"..."}
//	text   {"type":"init","codec":"avc1.4d401e"}  followed by one binary init segment
//	binary media segment (moof+mdat), appended as-is to the SourceBuffer
//	text   {"type":"error","message":"..."}       then the socket closes
//
// The client sends nothing; closing the socket unsubscribes.

const (
	writeTimeout = 10 * time.Second
	pingInterval = 20 * time.Second
)

// Application close codes (4000-4999 are free for private use).
const (
	closeInvalidURL  websocket.StatusCode = 4400
	closeLimit       websocket.StatusCode = 4429
	closeSlowClient  websocket.StatusCode = 4408
	closeStreamEnded websocket.StatusCode = 4410
)

type wireMsg struct {
	Type    string `json:"type"`
	State   string `json:"state,omitempty"`
	Message string `json:"message,omitempty"`
	Codec   string `json:"codec,omitempty"`
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.AllowedOrigins})
	if err != nil {
		return // Accept already wrote the HTTP error
	}
	defer conn.CloseNow()

	// Browsers cannot read the body of a failed upgrade, so errors are
	// reported over the socket where the UI can show them.
	url, err := s.Validator.Validate(r.Context(), r.URL.Query().Get("url"))
	if err != nil {
		closeWithError(r.Context(), conn, closeInvalidURL, userMessage(err))
		return
	}
	sub, err := s.Manager.Subscribe(url)
	if err != nil {
		code := websocket.StatusTryAgainLater
		if errors.Is(err, stream.ErrTooManyStreams) {
			code = closeLimit
		}
		closeWithError(r.Context(), conn, code, err.Error()+": try again later")
		return
	}
	defer sub.Close()

	// We never read client data; CloseRead handles control frames and
	// cancels ctx when the client goes away.
	ctx := conn.CloseRead(r.Context())
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		case m, ok := <-sub.C():
			if !ok {
				if errors.Is(sub.Err(), stream.ErrSlowClient) {
					closeWithError(ctx, conn, closeSlowClient, sub.Err().Error())
				} else {
					closeWithError(ctx, conn, closeStreamEnded, "stream closed by server")
				}
				return
			}
			if err := writeMessage(ctx, conn, m); err != nil {
				return
			}
		}
	}
}

func writeMessage(ctx context.Context, conn *websocket.Conn, m *stream.Message) error {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	switch m.Kind {
	case stream.MsgStatus:
		return writeJSONMsg(ctx, conn, wireMsg{Type: "status", State: string(m.Status.State), Message: m.Status.Message})
	case stream.MsgInit:
		if err := writeJSONMsg(ctx, conn, wireMsg{Type: "init", Codec: m.Codec}); err != nil {
			return err
		}
		return conn.Write(ctx, websocket.MessageBinary, m.Data)
	default:
		return conn.Write(ctx, websocket.MessageBinary, m.Data)
	}
}

func writeJSONMsg(ctx context.Context, conn *websocket.Conn, v wireMsg) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, b)
}

func closeWithError(ctx context.Context, conn *websocket.Conn, code websocket.StatusCode, msg string) {
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	_ = writeJSONMsg(wctx, conn, wireMsg{Type: "error", Message: msg})
	// Close reasons are limited to 123 bytes; the full text went above.
	reason := msg
	if len(reason) > 120 {
		reason = reason[:120]
	}
	_ = conn.Close(code, reason)
}
