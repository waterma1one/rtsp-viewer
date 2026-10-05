package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/waterma1one/rtsp-viewer/backend/internal/stream"
)

// fixtureSource replays real ffmpeg output, then blocks like a live feed.
func fixtureSource(t *testing.T) stream.Source {
	data, err := os.ReadFile("../stream/testdata/cam1.fmp4")
	if err != nil {
		t.Fatal(err)
	}
	return func(ctx context.Context, _ string) (stream.Feed, error) {
		r, w := io.Pipe()
		go func() {
			w.Write(data)
			<-ctx.Done()
			w.CloseWithError(io.EOF)
		}()
		return pipeFeed{r}, nil
	}
}

type pipeFeed struct{ io.Reader }

func (pipeFeed) Wait() error { return nil }

func newTestServer(t *testing.T, maxStreams int) *httptest.Server {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	mgr := stream.NewManager(fixtureSource(t), maxStreams, time.Minute, stream.DefaultOptions(), log)
	srv := &Server{
		Manager:        mgr,
		Validator:      &URLValidator{AllowedHostPorts: map[string]bool{"127.0.0.1:8554": true}},
		Demos:          []DemoStream{{Name: "cam1", URL: "rtsp://127.0.0.1:8554/cam1"}},
		AllowedOrigins: []string{"app.example.com"},
		Log:            log,
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); mgr.Shutdown() })
	return ts
}

func dial(t *testing.T, ts *httptest.Server, rtsp string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	u := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws?url=" + url.QueryEscape(rtsp)
	c, _, err := websocket.Dial(ctx, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.SetReadLimit(4 << 20)
	t.Cleanup(func() { c.CloseNow() })
	return c
}

func read(t *testing.T, c *websocket.Conn) (websocket.MessageType, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typ, b, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return typ, b
}

func readJSON(t *testing.T, c *websocket.Conn) wireMsg {
	t.Helper()
	typ, b := read(t, c)
	if typ != websocket.MessageText {
		t.Fatalf("want text message, got binary (%d bytes)", len(b))
	}
	var m wireMsg
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestWebSocketStreamsInitThenMedia(t *testing.T) {
	ts := newTestServer(t, 4)
	c := dial(t, ts, "rtsp://127.0.0.1:8554/cam1")

	if m := readJSON(t, c); m.Type != "status" || m.State != "connecting" {
		t.Fatalf("first message %+v", m)
	}
	if m := readJSON(t, c); m.Type != "init" || m.Codec != "avc1.4d401e" {
		t.Fatalf("want init with codec, got %+v", m)
	}
	if typ, b := read(t, c); typ != websocket.MessageBinary || string(b[4:8]) != "ftyp" {
		t.Fatal("init segment should follow the init message")
	}
	if m := readJSON(t, c); m.State != "live" {
		t.Fatalf("want live status before first media, got %+v", m)
	}
	if typ, b := read(t, c); typ != websocket.MessageBinary || string(b[4:8]) != "moof" {
		t.Fatal("want binary media segment")
	}
}

func TestWebSocketRejectsInvalidURL(t *testing.T) {
	ts := newTestServer(t, 4)
	c := dial(t, ts, "rtsp://10.0.0.1/cam")

	m := readJSON(t, c)
	if m.Type != "error" || !strings.Contains(m.Message, "private network address") {
		t.Fatalf("want private-address error, got %+v", m)
	}
	_, _, err := c.Read(context.Background())
	if websocket.CloseStatus(err) != closeInvalidURL {
		t.Fatalf("want close %d, got %v", closeInvalidURL, err)
	}
}

func TestWebSocketStreamLimit(t *testing.T) {
	ts := newTestServer(t, 1)
	first := dial(t, ts, "rtsp://127.0.0.1:8554/cam1")
	readJSON(t, first) // first stream holds the only slot

	// A second viewer of the same URL shares the source, so it is allowed.
	same := dial(t, ts, "rtsp://127.0.0.1:8554/cam1")
	if m := readJSON(t, same); m.Type != "status" {
		t.Fatalf("same-url viewer rejected: %+v", m)
	}

	other := dial(t, ts, "rtsp://127.0.0.1:8554/cam2")
	m := readJSON(t, other)
	if m.Type != "error" || !strings.Contains(m.Message, "limit") {
		t.Fatalf("want stream limit error, got %+v", m)
	}
	_, _, err := other.Read(context.Background())
	if websocket.CloseStatus(err) != closeLimit {
		t.Fatalf("want close %d, got %v", closeLimit, err)
	}
}

func TestOriginCheck(t *testing.T) {
	ts := newTestServer(t, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	u := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws?url=x"
	_, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {"https://evil.example.com"}},
	})
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin dial should get 403, got err=%v resp=%v", err, resp)
	}
}

func TestValidateEndpointAndCORS(t *testing.T) {
	ts := newTestServer(t, 4)
	req, _ := http.NewRequest("GET", ts.URL+"/api/validate?url="+url.QueryEscape("http://x"), nil)
	req.Header.Set("Origin", "https://app.example.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Fatal("missing CORS header for allowed origin")
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	if body["error"] != "The URL must start with rtsp:// or rtsps://." {
		t.Fatalf("error message %q", body["error"])
	}
}

func TestOriginPatternWithScheme(t *testing.T) {
	s := &Server{AllowedOrigins: []string{"https://rtsp-viewer.onrender.com"}}
	for origin, want := range map[string]bool{
		"https://rtsp-viewer.onrender.com":      true,
		"http://rtsp-viewer.onrender.com":       false,
		"https://rtsp-viewer-evil.onrender.com": false,
		"https://rtsp-viewer.onrender.com.evil": false,
		"":                                      false,
	} {
		if got := s.originAllowed(origin); got != want {
			t.Errorf("originAllowed(%q) = %v, want %v", origin, got, want)
		}
	}
}
