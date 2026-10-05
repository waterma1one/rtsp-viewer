package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"
)

// State is the lifecycle state reported to viewers.
type State string

const (
	StateConnecting   State = "connecting"
	StateLive         State = "live"
	StateReconnecting State = "reconnecting"
	// StateFailed is terminal: retrying cannot help (e.g. unsupported codec).
	StateFailed State = "failed"
)

type Status struct {
	State   State  `json:"state"`
	Message string `json:"message,omitempty"`
}

type MessageKind int

const (
	MsgStatus MessageKind = iota
	// MsgInit carries an init segment plus its codec string. Viewers must
	// reset their decoder on every MsgInit: a restarted source restarts
	// timestamps and may change resolution.
	MsgInit
	MsgMedia
)

type Message struct {
	Kind   MessageKind
	Status Status
	Codec  string
	Data   []byte
}

var errStalled = errors.New("stream stalled")

// Tuning knobs, overridable in tests.
type Options struct {
	StallTimeout   time.Duration // kill a source that produces nothing for this long
	SlowClientKick time.Duration // drop a viewer that cannot keep up for this long
	BackoffMin     time.Duration
	BackoffMax     time.Duration
	HealthyRun     time.Duration // a run this long resets the backoff
	SubscriberBuf  int
}

func DefaultOptions() Options {
	return Options{
		// With frag_keyframe a segment is emitted only at the next keyframe,
		// so this must exceed the longest GOP a camera may use.
		StallTimeout:   25 * time.Second,
		SlowClientKick: 10 * time.Second,
		BackoffMin:     time.Second,
		BackoffMax:     30 * time.Second,
		HealthyRun:     30 * time.Second,
		SubscriberBuf:  16,
	}
}

// Stream owns one source process and fans its segments out to subscribers.
type Stream struct {
	url    string
	source Source
	opts   Options
	log    *slog.Logger
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	subs   map[*Subscriber]struct{}
	status Status
	init   *Message // cached for late joiners
	last   *Message // latest media segment, starts on a keyframe
	idle   *time.Timer
	// idleGen identifies the current idle timer, so a timer that fires
	// after being superseded can recognise itself as stale.
	idleGen uint64
}

func newStream(url string, source Source, opts Options, log *slog.Logger) *Stream {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Stream{
		url:    url,
		source: source,
		opts:   opts,
		log:    log,
		cancel: cancel,
		done:   make(chan struct{}),
		subs:   make(map[*Subscriber]struct{}),
		status: Status{State: StateConnecting},
	}
	go s.run(ctx)
	return s
}

// run restarts the source with exponential backoff until cancelled.
func (s *Stream) run(ctx context.Context) {
	defer close(s.done)
	backoff := s.opts.BackoffMin
	for attempt := 1; ; attempt++ {
		started := time.Now()
		err := s.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		msg := FriendlyError(err, s.url)
		if errors.Is(err, errStalled) {
			msg = fmt.Sprintf("No video received for %s", s.opts.StallTimeout)
		}
		if errors.Is(err, ErrUnsupportedCodec) {
			s.log.Warn("stream failed permanently", "err", msg)
			s.setStatus(Status{State: StateFailed, Message: msg})
			return
		}
		if time.Since(started) >= s.opts.HealthyRun {
			backoff, attempt = s.opts.BackoffMin, 1
		}
		s.log.Info("source ended, retrying", "err", msg, "attempt", attempt, "backoff", backoff)
		s.setStatus(Status{State: StateReconnecting, Message: msg})

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, s.opts.BackoffMax)
	}
}

func (s *Stream) runOnce(ctx context.Context) (err error) {
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	feed, err := s.source(runCtx, s.url)
	if err != nil {
		return err
	}
	defer func() {
		// Stop the process before waiting: on a parse or codec error it is
		// still running. Cancelling first keeps a stall cause if one was set.
		cancel(nil)
		waitErr := feed.Wait()
		if cause := context.Cause(runCtx); errors.Is(cause, errStalled) {
			err = errStalled
		} else if err == nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			// A read error after the process died is a symptom; the exit
			// status and stderr explain the cause.
			if waitErr != nil {
				err = waitErr
			}
		}
		s.clearCache()
	}()

	// Watchdog: some cameras accept the connection and then send nothing.
	watchdog := time.AfterFunc(s.opts.StallTimeout, func() { cancel(errStalled) })
	defer watchdog.Stop()

	reader := NewSegmentReader(feed)
	for {
		seg, err := reader.Next()
		if err != nil {
			return err
		}
		watchdog.Reset(s.opts.StallTimeout)

		switch seg.Kind {
		case InitSegment:
			codec, err := CodecString(seg.Data)
			if err != nil {
				return err
			}
			s.publishInit(&Message{Kind: MsgInit, Codec: codec, Data: seg.Data})
		case MediaSegment:
			s.publishMedia(&Message{Kind: MsgMedia, Data: seg.Data})
		}
	}
}

func (s *Stream) publishInit(m *Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init, s.last = m, nil
	s.broadcastLocked(m)
}

func (s *Stream) publishMedia(m *Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.State != StateLive {
		s.status = Status{State: StateLive}
		s.broadcastLocked(&Message{Kind: MsgStatus, Status: s.status})
	}
	s.last = m
	s.broadcastLocked(m)
}

func (s *Stream) setStatus(st Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = st
	s.broadcastLocked(&Message{Kind: MsgStatus, Status: st})
}

func (s *Stream) clearCache() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init, s.last = nil, nil
}

// broadcastLocked never blocks: a viewer that cannot keep up misses
// segments instead of stalling everyone else. Because every media segment
// starts on a keyframe, a skipped segment shows as a brief jump, not
// corruption. A viewer stuck for SlowClientKick is disconnected.
func (s *Stream) broadcastLocked(m *Message) {
	now := time.Now()
	for sub := range s.subs {
		select {
		case sub.ch <- m:
			sub.stalledSince = time.Time{}
		default:
			if m.Kind == MsgInit {
				// Media after a missed init cannot be decoded; drop the
				// viewer so its client reconnects cleanly.
				sub.err = ErrSlowClient
				s.removeLocked(sub)
				continue
			}
			if sub.stalledSince.IsZero() {
				sub.stalledSince = now
			} else if now.Sub(sub.stalledSince) >= s.opts.SlowClientKick {
				s.log.Info("disconnecting slow viewer")
				sub.err = ErrSlowClient
				s.removeLocked(sub)
			}
		}
	}
}

// addLocked registers sub and queues the current state so a late joiner
// starts playing from the most recent keyframe immediately.
func (s *Stream) addLocked(sub *Subscriber) {
	if s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}
	s.subs[sub] = struct{}{}
	sub.ch <- &Message{Kind: MsgStatus, Status: s.status}
	if s.init != nil {
		sub.ch <- s.init
		if s.last != nil {
			sub.ch <- s.last
		}
	}
}

func (s *Stream) removeLocked(sub *Subscriber) {
	if _, ok := s.subs[sub]; !ok {
		return
	}
	delete(s.subs, sub)
	close(sub.ch)
}

// finished reports whether the run loop has exited for good.
func (s *Stream) finished() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *Stream) stop() {
	s.cancel()
	<-s.done
}

// ErrSlowClient is reported to a viewer that was dropped for falling behind.
var ErrSlowClient = errors.New("connection too slow to keep up with the stream")

// Subscriber is one viewer's handle on a stream.
type Subscriber struct {
	ch           chan *Message
	stream       *Stream
	mgr          *Manager
	once         sync.Once
	stalledSince time.Time // guarded by stream.mu
	err          error     // guarded by stream.mu; set before ch is closed
}

// C delivers messages until the subscription ends; it is closed when the
// viewer unsubscribes or is dropped.
func (sub *Subscriber) C() <-chan *Message { return sub.ch }

// Err explains why C was closed by the server, or nil.
func (sub *Subscriber) Err() error {
	sub.stream.mu.Lock()
	defer sub.stream.mu.Unlock()
	return sub.err
}

// Close unsubscribes; safe to call more than once.
func (sub *Subscriber) Close() {
	sub.once.Do(func() { sub.mgr.unsubscribe(sub) })
}
