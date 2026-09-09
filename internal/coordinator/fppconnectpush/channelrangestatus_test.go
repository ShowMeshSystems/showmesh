package fppconnectpush

import (
	"testing"
	"time"
)

// TestStatusStoreNeverRecordedReturnsNil proves a node with no recorded
// push status renders as nil (an empty array on the wire, mapNode's own
// "absent evidence is stated, never omitted" rule), matching
// noderender.Store.NodeRenderObservations' identical never-reported case.
func TestStatusStoreNeverRecordedReturnsNil(t *testing.T) {
	s := NewStatusStore()
	if obs := s.NodeFPPConnectObservations("node-1"); obs != nil {
		t.Errorf("NodeFPPConnectObservations for an unrecorded node = %v, want nil", obs)
	}
}

// TestStatusStoreNilReceiverIsSafe proves a nil *StatusStore (an unwired
// api.Dependencies.FPPConnectStatus) never panics on read, matching
// [record]'s identical nil-receiver safety on write.
func TestStatusStoreNilReceiverIsSafe(t *testing.T) {
	var s *StatusStore
	if obs := s.NodeFPPConnectObservations("node-1"); obs != nil {
		t.Errorf("nil *StatusStore.NodeFPPConnectObservations = %v, want nil", obs)
	}
	s.record("node-1", ChannelRangeStateDropped, "boom", time.Now())
}

// TestStatusStoreRecordOverwritesPreviousStatus proves a second record
// call for the same node replaces the first, rather than accumulating —
// only the MOST RECENT push's outcome is ever visible, matching this
// package's push-time-only, never-persisted-history posture.
func TestStatusStoreRecordOverwritesPreviousStatus(t *testing.T) {
	s := NewStatusStore()
	t1 := time.Now()
	s.record("node-1", ChannelRangeStateDropped, "first failure", t1)
	t2 := t1.Add(time.Minute)
	s.record("node-1", ChannelRangeStateFormatted, "", t2)

	obs := s.NodeFPPConnectObservations("node-1")
	if len(obs) != 2 {
		t.Fatalf("NodeFPPConnectObservations returned %d entries, want 2", len(obs))
	}
	if obs[0].Value != ChannelRangeStateFormatted {
		t.Errorf("state = %v, want %q (the second, most recent record)", obs[0].Value, ChannelRangeStateFormatted)
	}
	if obs[1].Value != "" {
		t.Errorf("reason = %v, want empty string (the second record's own reason)", obs[1].Value)
	}
}

// TestStatusStoreObservationsAreScopedPerNode proves one node's recorded
// status never leaks into another node's read — resource kind/id is
// always the node itself (the node.multisync.* precedent), never shared
// across nodes.
func TestStatusStoreObservationsAreScopedPerNode(t *testing.T) {
	s := NewStatusStore()
	s.record("node-1", ChannelRangeStateDropped, "node-1's own failure", time.Now())

	if obs := s.NodeFPPConnectObservations("node-2"); obs != nil {
		t.Errorf("NodeFPPConnectObservations for node-2 (never recorded) = %v, want nil", obs)
	}
}
