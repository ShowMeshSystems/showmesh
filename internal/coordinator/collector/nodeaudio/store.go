package nodeaudio

import (
	"context"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// report is the latest audio payload received for one node, plus the
// coordinator's own receipt time — [buildValue]'s CollectedAt, never
// ObservedAt (see that function's doc comment).
type report struct {
	payload    mqttproto.AudioPayload
	receivedAt time.Time
}

// ClockDomainSource reads nodeID's operator-declared audio.node clock
// domain configuration (ADR-039, config.AudioNodeConfigKind) — the ONLY
// source [Store] ever reports node.audio.clock.domain/provenance from. A
// node cannot supply its own clock domain: no software call proves two
// outputs share a hardware clock, so a node reporting one would be
// reporting a guess as if it were a reading. *store.Store already
// satisfies this directly, no adapter needed, matching
// [internal/coordinator/api.ConfigStore]'s identical precedent.
type ClockDomainSource interface {
	GetConfigObject(ctx context.Context, kind, id string) (store.ConfigObjectRecord, error)
	GetConfigRevision(ctx context.Context, kind, id string, revision int64) (store.ConfigRevisionRecord, error)
}

// Store holds, for each node that has ever published an audio report, the
// most recently received one. The zero value is not usable; construct with
// [NewStore]. Mirrors noderender.Store, plus the clock-domain source
// [ClockDomainSource] every observation this package builds reads live.
type Store struct {
	mu       sync.Mutex
	data     map[string]report
	clockSrc ClockDomainSource
}

// StoreOption configures [NewStore].
type StoreOption func(*Store)

// WithClockDomainSource wires clockSrc as the coordinator config store
// [Store] reads node.audio.clock.domain/provenance from, live, on every
// Poll and every [Store.NodeAudioObservations] call. Omitting this option
// leaves clockSrc nil, under which every node reports
// [observation.StateNotCollected] for both signals, naming the missing
// wiring — never "undeclared" presented as a reading.
func WithClockDomainSource(src ClockDomainSource) StoreOption {
	return func(s *Store) { s.clockSrc = src }
}

// NewStore builds an empty Store.
func NewStore(opts ...StoreOption) *Store {
	s := &Store{data: make(map[string]report)}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Put records payload as the latest audio report for nodeID, replacing
// whatever was previously stored. receivedAt is when this process actually
// observed the delivery — bookkeeping, never evidence of the node's own
// state (see [buildValue]). Safe for concurrent use.
func (s *Store) Put(nodeID string, payload mqttproto.AudioPayload, receivedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[nodeID] = report{payload: payload, receivedAt: receivedAt}
}

// snapshot returns a shallow copy of every node's latest report, safe for
// the caller to range over without further locking.
func (s *Store) snapshot() map[string]report {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make(map[string]report, len(s.data))
	for k, v := range s.data {
		out[k] = v
	}
	return out
}

// get returns nodeID's latest stored report, or ok=false if none has ever
// been received.
func (s *Store) get(nodeID string) (report, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.data[nodeID]
	return r, ok
}
