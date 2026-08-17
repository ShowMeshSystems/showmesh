package agent

import (
	"sync"
	"time"
)

// multiSyncStatus is thread-safe, publishable evidence of whether this
// node's MultiSync listener currently holds a bound UDP 32320 socket —
// finding 7's second half. Before this existed, a bind failure
// (multisync.go's runMultiSyncListener) was ONLY a log line: the timeline
// stayed at multisync.StateUnknown forever, and every surface's frame
// writer drew idle output at full configured rate while everything ELSE in
// the render report (PipelineState=="running", FramesWritten climbing,
// FramesDropped==0) looked completely healthy. runMultiSyncListener writes
// this; publishOneRenderReport (renderreport.go) reads it, so the bind
// outcome reaches the same evidence stream an operator already watches
// instead of living only where a log line lives.
type multiSyncStatus struct {
	mu         sync.Mutex
	listening  bool
	reason     string
	observedAt time.Time
}

// newMultiSyncStatus returns a status starting "not yet attempted": a bind
// attempt has not happened yet at construction, and that is itself a
// distinct, stated reason (ADR-011: never default an unknown to healthy) —
// runMultiSyncListener overwrites it with the real outcome, one way or the
// other, before doing anything else. observedAt starts zero (IsZero()),
// which the coordinator's noderender collector reads as "no real evidence
// yet" — the same zero-time-as-unknown sentinel this codebase already uses
// elsewhere (e.g. renderops.go's awaitAndReport).
func newMultiSyncStatus() *multiSyncStatus {
	return &multiSyncStatus{reason: "multisync listener has not attempted to bind yet"}
}

// set records the listener's current outcome, stamping observedAt from the
// real wall clock at the moment of this actual bind attempt or failure —
// this is ADR-003 evidence about WHEN the listener's status became what it
// is, distinct from whenever the next render report happens to publish it,
// the same "evidence time is not collection time" distinction runner.
// setState draws in internal/agent/pipeline/supervisor.go. reason is
// required whenever listening is false, matching this project's Reason-
// required-when-not-healthy convention (mirrored on the wire by
// RenderPayload.MultiSyncReason).
func (s *multiSyncStatus) set(listening bool, reason string) {
	s.mu.Lock()
	s.listening = listening
	s.reason = reason
	s.observedAt = time.Now().UTC()
	s.mu.Unlock()
}

// get reports the listener's most recently recorded outcome and when it was
// recorded. observedAt is the zero time.Time until the first set call.
func (s *multiSyncStatus) get() (listening bool, reason string, observedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listening, s.reason, s.observedAt
}
