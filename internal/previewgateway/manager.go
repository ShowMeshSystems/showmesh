package previewgateway

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"
)

const failureMemory = 30 * time.Second

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
}

// Manager holds the desired stream set and keeps go2rtc equal to it.
type Manager struct {
	engine  Engine
	sources SourceProvider
	self    string
	log     *slog.Logger
	now     func() time.Time

	mu      sync.Mutex
	desired map[string]*entry
}

// NewManager builds a Manager. self is the path of the gateway binary that
// go2rtc runs to start an NDI pipeline.
func NewManager(engine Engine, sources SourceProvider, self string, log *slog.Logger) *Manager {
	return &Manager{engine: engine, sources: sources, self: self, log: log, now: time.Now, desired: map[string]*entry{}}
}

func (m *Manager) sourceFor(s StreamSpec) string {
	if s.Kind == KindNDI {
		return "exec:" + m.self + " ndi-pipeline " + EncodeNDIName(s.NDIName)
	}
	return s.URL
}

// Set makes specs the desired set. An unchanged stream is left running.
func (m *Manager) Set(ctx context.Context, specs []StreamSpec) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := make(map[string]*entry, len(specs))
	for _, s := range specs {
		e := &entry{spec: s, id: streamID(s.Key), source: m.sourceFor(s)}
		if old, ok := m.desired[s.Key]; ok && old.source == e.source {
			e.applied, e.failedAt = old.applied, old.failedAt
		}
		next[s.Key] = e
	}
	for key, old := range m.desired {
		if _, kept := next[key]; !kept {
			if err := m.engine.Delete(ctx, old.id); err != nil {
				m.log.Warn("Removing a stream failed.", "id", old.id, "error", err)
			}
		}
	}
	m.desired = next
	m.applyPending(ctx)
}

// Retry applies every stream the engine has not accepted yet.
func (m *Manager) Retry(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.applyPending(ctx)
}

// Resync forgets what the engine holds, for use after the engine restarts.
func (m *Manager) Resync(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.desired {
		e.applied = false
	}
	m.applyPending(ctx)
}

func (m *Manager) applyPending(ctx context.Context) {
	for _, e := range m.desired {
		if e.applied {
			continue
		}
		if err := m.engine.Put(ctx, e.id, e.source); err != nil {
			m.log.Warn("Adding a stream failed.", "id", e.id, "error", err)
			continue
		}
		e.applied = true
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
	m.mu.Lock()
	entries := make([]entry, 0, len(m.desired))
	for _, e := range m.desired {
		entries = append(entries, *e)
	}
	m.mu.Unlock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].spec.Key < entries[j].spec.Key })

	info, listErr := m.engine.List(ctx)
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

func (m *Manager) classify(e entry, gi Go2RTCInfo, announced map[string]bool, discoveryOK bool) (string, string) {
	if producerLive(gi) {
		return StateLive, ""
	}
	if e.spec.Kind == KindNDI {
		if !discoveryOK {
			return StateError, "NDI is not available on this host. Mount the NDI runtime and restart the gateway."
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
	if len(gi.Consumers) > 0 || len(gi.Producers) > 0 && producerConnecting(gi) {
		return StateConnecting, "Waiting for the first frame."
	}
	return StateIdle, ""
}
