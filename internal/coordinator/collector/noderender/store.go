package noderender

import (
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// report is the latest render payload received for one node, plus the
// metadata [buildValue] needs to decide evidence age — the exact shape of
// fppmqtt's own message type, one field renamed for this package's single
// per-node (not per-topic) cache.
type report struct {
	payload    mqttproto.RenderPayload
	retained   bool
	receivedAt time.Time
}

// Store holds, for each node that has ever published a render report, the
// most recently received one. It is the mechanism behind this package's
// push-to-poll shape (see the package doc comment): internal/coordinator/
// inventory calls [Store.Put] from its MQTT message-arrival path (push);
// [Collector.Poll] and [Store.NodeRenderObservations] both read a stable
// snapshot (pull). The zero value is not usable; construct with [NewStore].
type Store struct {
	mu   sync.Mutex
	data map[string]report
}

// NewStore builds an empty Store.
func NewStore() *Store {
	return &Store{data: make(map[string]report)}
}

// Put records payload as the latest render report for nodeID, replacing
// whatever was previously stored. retained and receivedAt are the same pair
// [Manager.classify]-adjacent callers already compute for every other
// inventory topic: retained marks a broker replay of unknown age;
// receivedAt is when THIS process actually observed the delivery, used as
// CollectedAt regardless of retained (bookkeeping, never evidence of the
// subject's own state — see [buildValue]). Safe for concurrent use.
func (s *Store) Put(nodeID string, payload mqttproto.RenderPayload, retained bool, receivedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[nodeID] = report{payload: payload, retained: retained, receivedAt: receivedAt}
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
