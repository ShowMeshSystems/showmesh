package fppmqtt

import (
	"sync"
	"time"
)

// message is the latest payload observed on one MQTT topic suffix for one
// configured FPP instance, plus the metadata [render.go] needs to decide
// how to model its evidence age. retained and receivedAt are captured at
// the moment the subscription callback ran (see mqttclient.go's
// newPublishHandler), never recomputed later: contract section 4.2
// requires that once a topic has delivered a live message, later polls
// keep reporting THAT message's original receivedAt so it ages into stale
// naturally, rather than the act of polling refreshing it. Poll (render.go)
// never writes to the store; it only ever reads a [messageStore.snapshot].
type message struct {
	payload    []byte
	retained   bool
	receivedAt time.Time
}

// messageStore holds, for each configured FPP instance, the latest message
// received on each topic suffix under that instance's subtree. It is the
// mechanism behind this collector's push-to-poll shape (contract section
// 4.1): the MQTT subscription callback (push, see mqttclient.go) writes
// here via put; Poll (pull, see render.go) reads a stable snapshot via
// snapshot.
//
// messageStore never drops a topic's message except by a newer message on
// the SAME topic superseding it — state-topic semantics, not an event log,
// per contract section 4.1: "No message is dropped between polls except by
// being superseded on its own topic."
type messageStore struct {
	mu sync.Mutex
	// data is instanceID -> topic suffix -> latest message for that topic.
	data map[string]map[string]message
}

func newMessageStore() *messageStore {
	return &messageStore{data: make(map[string]map[string]message)}
}

// put records m as the latest message for (instanceID, suffix), replacing
// whatever was previously stored there. Safe for concurrent use.
func (s *messageStore) put(instanceID, suffix string, m message) {
	s.mu.Lock()
	defer s.mu.Unlock()

	byTopic, ok := s.data[instanceID]
	if !ok {
		byTopic = make(map[string]message)
		s.data[instanceID] = byTopic
	}
	byTopic[suffix] = m
}

// snapshot returns a shallow copy of every message currently stored for
// instanceID, keyed by topic suffix, safe for the caller to range over
// without further locking. A never-stored instanceID returns an empty,
// non-nil map.
func (s *messageStore) snapshot(instanceID string) map[string]message {
	s.mu.Lock()
	defer s.mu.Unlock()

	byTopic := s.data[instanceID]
	out := make(map[string]message, len(byTopic))
	for k, v := range byTopic {
		out[k] = v
	}
	return out
}

// latestReceivedAt reports the most recent receivedAt across every topic
// suffix ever stored for instanceID, and whether instanceID has ever had
// any message stored for it at all. everPublished answers a whole-process
// question, independent of any MQTT reconnect: put never removes an entry
// except by a newer message superseding it on the SAME topic (see the
// message/messageStore doc comments), so an instance that has published at
// least once keeps a row here for the life of the collector, even across a
// broker disconnect and reconnect. A never-stored instanceID reports the
// zero Time and everPublished=false.
func (s *messageStore) latestReceivedAt(instanceID string) (t time.Time, everPublished bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	byTopic, ok := s.data[instanceID]
	if !ok || len(byTopic) == 0 {
		return time.Time{}, false
	}
	for _, m := range byTopic {
		if m.receivedAt.After(t) {
			t = m.receivedAt
		}
	}
	return t, true
}
