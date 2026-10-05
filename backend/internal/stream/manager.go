package stream

import (
	"errors"
	"log/slog"
	"sync"
	"time"
)

// ErrTooManyStreams is returned when the server is already running its
// maximum number of distinct sources.
var ErrTooManyStreams = errors.New("server is at its stream limit")

// Manager runs at most one source per URL, however many viewers watch it.
// Sources start with the first viewer and stop IdleGrace after the last one
// leaves, so a page refresh or a quick pause does not restart ffmpeg.
//
// Lock order: Manager.mu before Stream.mu.
type Manager struct {
	source     Source
	opts       Options
	maxStreams int
	idleGrace  time.Duration
	log        *slog.Logger

	mu      sync.Mutex
	streams map[string]*Stream
	closed  bool
}

func NewManager(source Source, maxStreams int, idleGrace time.Duration, opts Options, log *slog.Logger) *Manager {
	return &Manager{
		source:     source,
		opts:       opts,
		maxStreams: maxStreams,
		idleGrace:  idleGrace,
		log:        log,
		streams:    make(map[string]*Stream),
	}
}

// Subscribe attaches a viewer to url, starting its source if needed. The
// url must already be validated and normalised.
func (m *Manager) Subscribe(url string) (*Subscriber, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("server is shutting down")
	}

	s, ok := m.streams[url]
	if ok && s.finished() {
		// A terminal failure (e.g. unsupported codec) ended the source.
		// A new viewer, such as a "Try again", gets a fresh attempt.
		delete(m.streams, url)
		s.mu.Lock()
		if s.idle != nil {
			s.idle.Stop()
			s.idle = nil
		}
		s.mu.Unlock()
		ok = false
	}
	if !ok {
		if len(m.streams) >= m.maxStreams {
			return nil, ErrTooManyStreams
		}
		s = newStream(url, m.source, m.opts, m.log.With("url", RedactURL(url)))
		m.streams[url] = s
		m.log.Info("stream started", "active", len(m.streams))
	}

	// +3 so addLocked can always queue status, init and last segment.
	sub := &Subscriber{ch: make(chan *Message, m.opts.SubscriberBuf+3), stream: s, mgr: m}
	s.mu.Lock()
	s.addLocked(sub)
	s.mu.Unlock()
	return sub, nil
}

func (m *Manager) unsubscribe(sub *Subscriber) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := sub.stream
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeLocked(sub)
	if len(s.subs) > 0 || s.idle != nil || m.streams[s.url] != s {
		return
	}
	s.idleGen++
	gen := s.idleGen
	s.idle = time.AfterFunc(m.idleGrace, func() { m.reapIfIdle(s, gen) })
}

func (m *Manager) reapIfIdle(s *Stream, gen uint64) {
	m.mu.Lock()
	s.mu.Lock()
	// A viewer may have joined, or a newer timer replaced this one.
	idle := len(s.subs) == 0 && s.idle != nil && s.idleGen == gen && m.streams[s.url] == s
	if idle {
		delete(m.streams, s.url)
		s.idle = nil
	}
	active := len(m.streams)
	s.mu.Unlock()
	m.mu.Unlock()

	if idle {
		s.stop()
		m.log.Info("stream stopped after idle grace", "active", active)
	}
}

// ActiveStreams reports how many sources are running.
func (m *Manager) ActiveStreams() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.streams)
}

// Shutdown stops every source and waits for them to exit.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.closed = true
	streams := m.streams
	m.streams = map[string]*Stream{}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, s := range streams {
		wg.Go(s.stop)
	}
	wg.Wait()
}
