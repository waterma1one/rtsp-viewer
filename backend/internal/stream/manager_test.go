package stream

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSource hands out pipes the test writes fMP4 boxes into.
type fakeSource struct {
	starts atomic.Int32
	feeds  chan *fakeFeed
}

type fakeFeed struct {
	*io.PipeReader
	w       *io.PipeWriter
	waitErr error
	ctx     context.Context
}

func (f *fakeFeed) Wait() error { return f.waitErr }

func newFakeSource() *fakeSource { return &fakeSource{feeds: make(chan *fakeFeed, 8)} }

func (fs *fakeSource) Source(ctx context.Context, _ string) (Feed, error) {
	fs.starts.Add(1)
	r, w := io.Pipe()
	f := &fakeFeed{PipeReader: r, w: w, ctx: ctx}
	// Like a killed process: cancellation ends the output.
	go func() { <-ctx.Done(); w.CloseWithError(io.EOF) }()
	fs.feeds <- f
	return f, nil
}

func (fs *fakeSource) next(t *testing.T) *fakeFeed {
	t.Helper()
	select {
	case f := <-fs.feeds:
		return f
	case <-time.After(2 * time.Second):
		t.Fatal("source was not started")
		return nil
	}
}

func testOptions() Options {
	o := DefaultOptions()
	o.BackoffMin = 10 * time.Millisecond
	o.BackoffMax = 20 * time.Millisecond
	o.SubscriberBuf = 4
	return o
}

func newTestManager(fs *fakeSource, max int, grace time.Duration, o Options) *Manager {
	return NewManager(fs.Source, max, grace, o, slog.New(slog.DiscardHandler))
}

var (
	testInit  = initSegment("avc1", 0x4d, 0x40, 0x1e)
	testMedia = append(box("moof", []byte("m")), box("mdat", []byte("frame"))...)
)

// recv returns the next message that is not a status update.
func recv(t *testing.T, sub *Subscriber) *Message {
	t.Helper()
	for {
		select {
		case m, ok := <-sub.C():
			if !ok {
				t.Fatal("subscription closed")
			}
			if m.Kind != MsgStatus {
				return m
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for message")
		}
	}
}

func recvStatus(t *testing.T, sub *Subscriber, want State) Status {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case m, ok := <-sub.C():
			if !ok {
				t.Fatalf("subscription closed while waiting for %s", want)
			}
			if m.Kind == MsgStatus && m.Status.State == want {
				return m.Status
			}
		case <-deadline:
			t.Fatalf("timed out waiting for status %s", want)
		}
	}
}

func write(t *testing.T, f *fakeFeed, b []byte) {
	t.Helper()
	if _, err := f.w.Write(b); err != nil {
		t.Fatal(err)
	}
}

func TestFanOutSharesOneSource(t *testing.T) {
	fs := newFakeSource()
	m := newTestManager(fs, 4, time.Minute, testOptions())
	defer m.Shutdown()

	a, err := m.Subscribe("rtsp://cam/1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Subscribe("rtsp://cam/1")
	if err != nil {
		t.Fatal(err)
	}
	feed := fs.next(t)
	write(t, feed, testInit)
	write(t, feed, testMedia)

	for _, sub := range []*Subscriber{a, b} {
		if init := recv(t, sub); init.Kind != MsgInit || init.Codec != "avc1.4d401e" {
			t.Fatalf("want init with codec, got kind %v codec %q", init.Kind, init.Codec)
		}
		if media := recv(t, sub); media.Kind != MsgMedia {
			t.Fatalf("want media, got %v", media.Kind)
		}
	}
	if n := fs.starts.Load(); n != 1 {
		t.Fatalf("source started %d times, want 1", n)
	}
	if m.ActiveStreams() != 1 {
		t.Fatalf("active streams %d, want 1", m.ActiveStreams())
	}
}

func TestLateJoinerGetsInitAndLatestKeyframe(t *testing.T) {
	fs := newFakeSource()
	m := newTestManager(fs, 4, time.Minute, testOptions())
	defer m.Shutdown()

	first, _ := m.Subscribe("rtsp://cam/1")
	feed := fs.next(t)
	write(t, feed, testInit)
	older := append(box("moof", []byte("old")), box("mdat", []byte("1"))...)
	newer := append(box("moof", []byte("new")), box("mdat", []byte("2"))...)
	write(t, feed, older)
	write(t, feed, newer)
	recv(t, first)
	recv(t, first)
	recv(t, first) // all published before the late join

	late, _ := m.Subscribe("rtsp://cam/1")
	if st := (<-late.C()).Status.State; st != StateLive {
		t.Fatalf("late joiner first status %q, want live", st)
	}
	if m := recv(t, late); m.Kind != MsgInit {
		t.Fatal("late joiner did not get init first")
	}
	if m := recv(t, late); string(m.Data) != string(newer) {
		t.Fatal("late joiner should start at the latest segment")
	}
}

func TestStreamLimit(t *testing.T) {
	fs := newFakeSource()
	m := newTestManager(fs, 2, time.Minute, testOptions())
	defer m.Shutdown()

	for _, u := range []string{"rtsp://cam/1", "rtsp://cam/2", "rtsp://cam/1"} {
		if _, err := m.Subscribe(u); err != nil {
			t.Fatalf("%s: %v", u, err)
		}
	}
	if _, err := m.Subscribe("rtsp://cam/3"); !errors.Is(err, ErrTooManyStreams) {
		t.Fatalf("want ErrTooManyStreams, got %v", err)
	}
}

func TestIdleGraceStopsAndReusesSource(t *testing.T) {
	fs := newFakeSource()
	m := newTestManager(fs, 4, 50*time.Millisecond, testOptions())
	defer m.Shutdown()

	sub, _ := m.Subscribe("rtsp://cam/1")
	feed := fs.next(t)
	sub.Close()
	sub.Close() // idempotent

	// Rejoin inside the grace period reuses the running source.
	sub, _ = m.Subscribe("rtsp://cam/1")
	time.Sleep(100 * time.Millisecond)
	if n := fs.starts.Load(); n != 1 {
		t.Fatalf("source restarted on quick rejoin: %d starts", n)
	}

	sub.Close()
	select {
	case <-feed.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("source not stopped after idle grace")
	}
	if m.ActiveStreams() != 0 {
		t.Fatalf("active streams %d after grace, want 0", m.ActiveStreams())
	}
}

func TestSourceFailureReconnects(t *testing.T) {
	fs := newFakeSource()
	m := newTestManager(fs, 4, time.Minute, testOptions())
	defer m.Shutdown()

	sub, _ := m.Subscribe("rtsp://cam/1")
	feed := fs.next(t)
	feed.waitErr = &SourceError{Detail: "rtsp://cam/1: Connection refused"}
	feed.w.Close()

	st := recvStatus(t, sub, StateReconnecting)
	if st.Message != "Connection refused by the RTSP server" {
		t.Fatalf("message %q", st.Message)
	}
	feed = fs.next(t)
	write(t, feed, testInit)
	if m := recv(t, sub); m.Kind != MsgInit {
		t.Fatal("want fresh init after reconnect")
	}
}

func TestUnsupportedCodecIsTerminal(t *testing.T) {
	fs := newFakeSource()
	m := newTestManager(fs, 4, time.Minute, testOptions())
	defer m.Shutdown()

	sub, _ := m.Subscribe("rtsp://cam/1")
	write(t, fs.next(t), initSegment("hvc1", 0, 0, 0))

	st := recvStatus(t, sub, StateFailed)
	if st.Message == "" {
		t.Fatal("failed status needs a message")
	}
	time.Sleep(50 * time.Millisecond)
	if n := fs.starts.Load(); n != 1 {
		t.Fatalf("terminal failure must not retry, got %d starts", n)
	}

	// "Try again" within the idle grace must probe the camera again rather
	// than replay the cached failure.
	sub.Close()
	retry, err := m.Subscribe("rtsp://cam/1")
	if err != nil {
		t.Fatal(err)
	}
	fs.next(t)
	if n := fs.starts.Load(); n != 2 {
		t.Fatalf("retry after terminal failure should restart the source, got %d starts", n)
	}
	retry.Close()
}

func TestViewerMissingInitIsDropped(t *testing.T) {
	fs := newFakeSource()
	o := testOptions()
	o.SlowClientKick = time.Hour // isolate the init rule from the stall rule
	m := newTestManager(fs, 4, time.Minute, o)
	defer m.Shutdown()

	sub, _ := m.Subscribe("rtsp://cam/1")
	feed := fs.next(t)
	write(t, feed, testInit)
	for range o.SubscriberBuf + 3 {
		write(t, feed, testMedia) // fill the never-read buffer
	}
	// The source restarts and the new process sends a fresh init.
	feed.w.Close()
	write(t, fs.next(t), testInit)

	deadline := time.Now().Add(2 * time.Second)
	for sub.Err() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !errors.Is(sub.Err(), ErrSlowClient) {
		t.Fatalf("viewer that missed an init should be dropped, got %v", sub.Err())
	}
}

func TestStallWatchdog(t *testing.T) {
	fs := newFakeSource()
	o := testOptions()
	o.StallTimeout = 50 * time.Millisecond
	m := newTestManager(fs, 4, time.Minute, o)
	defer m.Shutdown()

	sub, _ := m.Subscribe("rtsp://cam/1")
	fs.next(t) // connected, never sends
	st := recvStatus(t, sub, StateReconnecting)
	if st.Message != "No video received for 50ms" {
		t.Fatalf("message %q", st.Message)
	}
}

func TestSlowViewerIsDroppedWithoutBlockingOthers(t *testing.T) {
	fs := newFakeSource()
	o := testOptions()
	o.SlowClientKick = 30 * time.Millisecond
	m := newTestManager(fs, 4, time.Minute, o)
	defer m.Shutdown()

	slow, _ := m.Subscribe("rtsp://cam/1")
	fast, _ := m.Subscribe("rtsp://cam/1")
	feed := fs.next(t)
	write(t, feed, testInit)

	var wg sync.WaitGroup
	wg.Go(func() {
		for range fast.C() {
		}
	})

	// slow never reads: its buffer fills, then it stays stalled past the kick.
	deadline := time.Now().Add(2 * time.Second)
	for slow.Err() == nil && time.Now().Before(deadline) {
		write(t, feed, testMedia)
		time.Sleep(5 * time.Millisecond)
	}
	if !errors.Is(slow.Err(), ErrSlowClient) {
		t.Fatalf("slow viewer not dropped: %v", slow.Err())
	}
	for range slow.C() { // channel must be closed after draining
	}
	if fast.Err() != nil {
		t.Fatalf("fast viewer affected: %v", fast.Err())
	}
	fast.Close()
	wg.Wait()
}

func TestShutdownStopsSources(t *testing.T) {
	fs := newFakeSource()
	m := newTestManager(fs, 4, time.Minute, testOptions())
	m.Subscribe("rtsp://cam/1")
	feed := fs.next(t)
	m.Shutdown()
	if feed.ctx.Err() == nil {
		t.Fatal("source context not cancelled on shutdown")
	}
	if _, err := m.Subscribe("rtsp://cam/1"); err == nil {
		t.Fatal("subscribe after shutdown should fail")
	}
}
