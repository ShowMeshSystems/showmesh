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

// LocalClockSource reads nodeID's active audio.node configuration
// (ADR-039, config.AudioNodeConfigKind): the ONLY source [Store] ever
// reports node.audio.clock.local and its source from. The local clock is
// the interface that configuration's program route names, or the
// operator's own localClockOverride: a node cannot see two interfaces
// sharing external word clock, so a node reporting an override would be
// reporting a guess as if it were a reading. *store.Store already
// satisfies this directly, no adapter needed, matching
// [internal/coordinator/api.ConfigStore]'s identical precedent.
type LocalClockSource interface {
	GetConfigObject(ctx context.Context, kind, id string) (store.ConfigObjectRecord, error)
	GetConfigRevision(ctx context.Context, kind, id string, revision int64) (store.ConfigRevisionRecord, error)
}

// ClockStatusSource reads nodeID's most recent PTP clock report, the
// node's own evidence behind node.clock.ptp.*, through
// nodeclock.Store's NodeClockStatus. [Store] reads it to say what this
// node's local clock follows and how far off it is, so node.audio.sync.*
// is built from the node's own reading rather than from a second
// measurement of the same thing. ok is false for a node that has never
// published a clock report.
type ClockStatusSource interface {
	NodeClockStatus(nodeID string) (payload mqttproto.ClockPayload, ok bool)
}

// Store holds, for each node that has ever published an audio report, the
// most recently received one. The zero value is not usable; construct with
// [NewStore]. Mirrors noderender.Store, plus the two live sources every
// observation this package builds reads: [LocalClockSource] and
// [ClockStatusSource].
type Store struct {
	mu        sync.Mutex
	data      map[string]report
	clockSrc  LocalClockSource
	clockStat ClockStatusSource
}

// StoreOption configures [NewStore].
type StoreOption func(*Store)

// WithLocalClockSource wires clockSrc as the coordinator config store
// [Store] reads node.audio.clock.local and its source from, live, on
// every Poll and every [Store.NodeAudioObservations] call. Omitting this
// option leaves clockSrc nil, under which every node reports
// [observation.StateNotCollected] for both signals, naming the missing
// wiring, never an unconfigured node presented as a reading.
func WithLocalClockSource(src LocalClockSource) StoreOption {
	return func(s *Store) { s.clockSrc = src }
}

// WithClockStatusSource wires src as the PTP clock report cache [Store]
// reads node.audio.sync.* from, live, on the same calls. Omitting it
// leaves src nil, under which every node reports
// [observation.StateNotCollected] for all four sync signals rather than
// a free-running claim this coordinator never checked.
func WithClockStatusSource(src ClockStatusSource) StoreOption {
	return func(s *Store) { s.clockStat = src }
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
