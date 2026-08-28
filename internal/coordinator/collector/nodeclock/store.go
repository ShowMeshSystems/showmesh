package nodeclock

import (
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// report is the latest clock payload received for one node, plus the
// coordinator's own receipt time — [buildValue]'s CollectedAt, never
// ObservedAt (see that function's doc comment), matching
// nodeaudio.report's identical shape.
type report struct {
	payload    mqttproto.ClockPayload
	receivedAt time.Time
}

// Store holds, for each node that has ever published a clock report, the
// most recently received one. The zero value is not usable; construct
// with [NewStore]. Mirrors nodeaudio.Store, minus that package's
// ClockDomainSource: node.clock has no analogous coordinator-declared
// value a node's own report needs merged in — every field this package
// reports comes straight from the node's own evidence.
type Store struct {
	mu   sync.Mutex
	data map[string]report
}

// NewStore builds an empty Store.
func NewStore() *Store {
	return &Store{data: make(map[string]report)}
}

// Put records payload as the latest clock report for nodeID, replacing
// whatever was previously stored. receivedAt is when this process
// actually observed the delivery — bookkeeping, never evidence of the
// node's own state (see [buildValue]). Safe for concurrent use.
func (s *Store) Put(nodeID string, payload mqttproto.ClockPayload, receivedAt time.Time) {
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
