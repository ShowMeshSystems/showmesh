package previewgateway

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"
)

const (
	failureMemory = 30 * time.Second
	stallAfter    = 5 * time.Second
)

// Stream states reported by GET /v1/streams.
const (
	StateLive       = "live"
	StateIdle       = "idle"
	StateConnecting = "connecting"
	StateNotFound   = "not_found"
	StateError      = "error"
)

// StreamStatus is one row of GET /v1/streams.
type StreamStatus struct {
	Key       string `json:"key"`
	State     string `json:"state"`
	Message   string `json:"message"`
	Consumers int    `json:"consumers"`
}

type entry struct {
	spec     StreamSpec
	id       string
	source   string
	applied  bool
	failedAt time.Time

	lastBytes  float64
	advancedAt time.Time
}

// Manager holds the desired stream set and keeps go2rtc equal to it.
type Manager struct {
	engine  Engine
	sources SourceProvider
	feed    string
	log     *slog.Logger
	now     func() time.Time

	applyMu sync.Mutex // serialises engine writes
	mu      sync.Mutex // guards desired and its entries
	desired map[string]*entry
}

// NewManager builds a Manager. feed is the loopback URL prefix under which
// the gateway serves each NDI stream's video to go2rtc.
func NewManager(engine Engine, sources SourceProvider, feed string, log *slog.Logger) *Manager {
	return &Manager{engine: engine, sources: sources, feed: feed, log: log, now: time.Now, desired: map[string]*entry{}}
}

// NDIName returns the NDI source name of the desired NDI stream with this id.
func (m *Manager) NDIName(id string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.desired {
		if e.id == id && e.spec.Kind == KindNDI {
			return e.spec.NDIName, true
		}
	}
	return "", false
}

func (m *Manager) sourceFor(s StreamSpec) string {
	if s.Kind == KindNDI {
		return m.feed + "/" + streamID(s.Key)
	}
	return s.URL
}

// Set makes specs the desired set. A stream whose spec is unchanged is left
// running; one whose spec changed is removed from the engine and added again.
func (m *Manager) Set(ctx context.Context, specs []StreamSpec) {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	var drop []string
	m.mu.Lock()
	next := make(map[string]*entry, len(specs))
	for _, s := range specs {
		e := &entry{spec: s, id: streamID(s.Key), source: m.sourceFor(s)}
		if old, ok := m.desired[s.Key]; ok {
			if old.spec == s {
				e.applied, e.failedAt = old.applied, old.failedAt
			} else if old.applied {
				drop = append(drop, old.id)
			}
		}
		next[s.Key] = e
	}
	for key, old := range m.desired {
		if _, kept := next[key]; !kept {
			drop = append(drop, old.id)
		}
	}
	m.desired = next
	m.mu.Unlock()
	for _, id := range drop {
		if err := m.engine.Delete(ctx, id); err != nil {
			m.log.Warn("Removing a stream failed.", "id", id, "error", err)
		}
	}
	m.applyPending(ctx)
}

// Retry applies every stream the engine has not accepted yet.
func (m *Manager) Retry(ctx context.Context) {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	m.applyPending(ctx)
}

// Resync forgets what the engine holds, for use after the engine restarts.
func (m *Manager) Resync(ctx context.Context) {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	m.mu.Lock()
	for _, e := range m.desired {
		e.applied = false
	}
	m.mu.Unlock()
	m.applyPending(ctx)
}

// applyPending calls the engine without holding m.mu, so a slow engine never
// blocks status or lookups. The caller holds applyMu.
func (m *Manager) applyPending(ctx context.Context) {
	m.mu.Lock()
	var pending []*entry
	for _, e := range m.desired {
		if !e.applied {
			pending = append(pending, e)
		}
	}
	m.mu.Unlock()
	for _, e := range pending {
		if err := m.engine.Put(ctx, e.id, e.source); err != nil {
			m.log.Warn("Adding a stream failed.", "id", e.id, "error", err)
			continue
		}
		m.mu.Lock()
		e.applied = true
		m.mu.Unlock()
	}
}

// Lookup returns the engine id and kind for a key.
func (m *Manager) Lookup(key string) (id, kind string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.desired[key]
	if !ok {
		return "", "", false
	}
	return e.id, e.spec.Kind, true
}

// NoteResult records whether the last request for a frame or an answer
// succeeded, so the stream reports a camera that refused.
func (m *Manager) NoteResult(key string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, found := m.desired[key]; found {
		if ok {
			e.failedAt = time.Time{}
		} else {
			e.failedAt = m.now()
		}
	}
}

// Streams reports every desired stream, ordered by key.
func (m *Manager) Streams(ctx context.Context) []StreamStatus {
	info, listErr := m.engine.List(ctx)
	m.mu.Lock()
	entries := make([]entry, 0, len(m.desired))
	for _, e := range m.desired {
		if listErr == nil {
			m.trackBytes(e, info[e.id])
		}
		entries = append(entries, *e)
	}
	m.mu.Unlock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].spec.Key < entries[j].spec.Key })

	announced, discoveryOK := m.sources.Announced()
	out := make([]StreamStatus, 0, len(entries))
	for _, e := range entries {
		st := StreamStatus{Key: e.spec.Key}
		switch {
		case !e.applied || listErr != nil:
			st.State = StateError
			st.Message = "The video engine is not running this stream. The gateway restarts it automatically."
		default:
			gi := info[e.id]
			st.Consumers = len(gi.Consumers)
			st.State, st.Message = m.classify(e, gi, announced, discoveryOK)
		}
		out = append(out, st)
	}
	return out
}

// trackBytes remembers when a stream's received byte count last grew, so that
// a source that stops sending stops reporting live.
func (m *Manager) trackBytes(e *entry, gi Go2RTCInfo) {
	n := producerBytes(gi)
	if n != e.lastBytes {
		e.lastBytes, e.advancedAt = n, m.now()
	}
}

func (m *Manager) classify(e entry, gi Go2RTCInfo, announced map[string]bool, discoveryOK bool) (string, string) {
	if producerBytes(gi) > 0 && m.now().Sub(e.advancedAt) < stallAfter {
		return StateLive, ""
	}
	if e.spec.Kind == KindNDI {
		if !discoveryOK {
			msg := m.sources.Unavailable()
			if msg == "" {
				msg = msgNoNDIRuntime
			}
			return StateError, msg
		}
		if !announced[e.spec.NDIName] {
			return StateNotFound, "The NDI source is not announced on the network. Start the sender or check the network."
		}
	}
	if !e.failedAt.IsZero() && m.now().Sub(e.failedAt) < failureMemory {
		if e.spec.Kind == KindRTSP {
			return StateNotFound, "The camera did not send video. Check its address and that it is powered on."
		}
		return StateError, "The source did not send video. Check that the sender is running."
	}
	if len(gi.Consumers) > 0 || producerConnected(gi) {
		return StateConnecting, "Waiting for the first frame."
	}
	return StateIdle, ""
}
