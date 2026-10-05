package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	neturl "net/url"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Feed is a running source of fragmented MP4 bytes.
type Feed interface {
	io.Reader
	// Wait releases resources and returns why the feed ended. It must be
	// called exactly once, after reading stops.
	Wait() error
}

// Source starts a feed for a URL. Cancelling ctx must terminate the feed.
type Source func(ctx context.Context, url string) (Feed, error)

// FFmpegSource remuxes an RTSP stream to fragmented MP4 without re-encoding.
func FFmpegSource(bin string) Source {
	return func(ctx context.Context, url string) (Feed, error) {
		cmd := exec.CommandContext(ctx, bin,
			"-hide_banner", "-loglevel", "error", "-nostdin",
			// TCP interleaving survives NAT and cloud firewalls that drop UDP.
			"-rtsp_transport", "tcp",
			// Socket I/O timeout in microseconds: fail fast on dead hosts.
			"-timeout", "5000000",
			"-i", url,
			"-map", "0:v:0", "-an",
			"-c:v", "copy",
			"-f", "mp4",
			// One fragment per GOP so every media segment starts on a keyframe.
			"-movflags", "frag_keyframe+empty_moov+default_base_moof",
			"pipe:1",
		)
		// Ask ffmpeg to stop cleanly first; kill if it ignores us.
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		cmd.WaitDelay = 3 * time.Second

		stderr := &tailBuffer{max: 2048}
		cmd.Stderr = stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("start ffmpeg: %w", err)
		}
		return &ffmpegFeed{Reader: stdout, cmd: cmd, stderr: stderr}, nil
	}
}

type ffmpegFeed struct {
	io.Reader
	cmd    *exec.Cmd
	stderr *tailBuffer
}

func (f *ffmpegFeed) Wait() error {
	err := f.cmd.Wait()
	if msg := f.stderr.LastLine(); msg != "" {
		return &SourceError{Detail: msg, Err: err}
	}
	return err
}

// SourceError carries the last line ffmpeg printed, which is usually the
// actionable part ("Connection refused", "401 Unauthorized", ...).
type SourceError struct {
	Detail string
	Err    error
}

func (e *SourceError) Error() string { return e.Detail }
func (e *SourceError) Unwrap() error { return e.Err }

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = t.buf[over:]
	}
	return len(p), nil
}

func (t *tailBuffer) LastLine() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(t.buf)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// FriendlyError turns a feed failure into a short message for the viewer.
// The raw URL may contain credentials, so it is never echoed back.
func FriendlyError(err error, url string) string {
	if err == nil {
		return "Stream ended"
	}
	if errors.Is(err, ErrUnsupportedCodec) {
		return err.Error()
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "401") || strings.Contains(msg, "unauthorized"):
		return "Authentication failed: check the username and password"
	case strings.Contains(msg, "404") || strings.Contains(msg, "not found"):
		return "Stream path not found on the server"
	case strings.Contains(msg, "connection refused"):
		return "Connection refused by the RTSP server"
	case strings.Contains(msg, "timed out") || strings.Contains(msg, "timeout"):
		return "Timed out connecting to the RTSP server"
	case strings.Contains(msg, "no route") || strings.Contains(msg, "unreachable") ||
		strings.Contains(msg, "name or service not known") || strings.Contains(msg, "resolve"):
		return "RTSP server is unreachable"
	case strings.Contains(msg, "executable file not found"):
		return "Server misconfigured: ffmpeg is not installed"
	}
	detail := strings.ReplaceAll(err.Error(), url, "<stream>")
	if len(detail) > 160 {
		detail = detail[:160] + "…"
	}
	return "Stream error: " + detail
}

// RedactURL strips credentials so URLs can be logged safely.
func RedactURL(raw string) string {
	u, err := neturl.Parse(raw)
	if err != nil {
		return "<unparseable url>"
	}
	if u.User != nil {
		u.User = neturl.User("redacted")
	}
	return u.String()
}
