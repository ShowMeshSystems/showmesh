package remoteoutput

import "sync"

type evidenceKey struct {
	destination Destination
	contentHash string
}

// EvidenceStore holds the current [ProvisioningRecord] per (Destination,
// content hash) pair. It is the durability-agnostic core a real
// implementation persists; [FakeDestination] uses one directly.
type EvidenceStore struct {
	mu      sync.Mutex
	records map[evidenceKey]ProvisioningRecord
}

// NewEvidenceStore returns an empty EvidenceStore.
func NewEvidenceStore() *EvidenceStore {
	return &EvidenceStore{records: make(map[evidenceKey]ProvisioningRecord)}
}

// Record upserts rec, keyed by its Destination and ContentHash. A record
// older than the one already stored (by ObservedAt) is dropped rather
// than applied: a delayed, out-of-order status report must not regress
// evidence a fresher report already advanced.
func (s *EvidenceStore) Record(rec ProvisioningRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := evidenceKey{destination: rec.Destination, contentHash: rec.ContentHash}
	if existing, ok := s.records[key]; ok && rec.ObservedAt.Before(existing.ObservedAt) {
		return
	}
	s.records[key] = rec
}

// Get returns the current record for dest and contentHash, or
// [ProvisioningNotAttempted] with ok false when no record has ever been
// stored for that exact pair.
func (s *EvidenceStore) Get(dest Destination, contentHash string) (ProvisioningRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[evidenceKey{destination: dest, contentHash: contentHash}]
	return rec, ok
}
