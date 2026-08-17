package agent

import "sync"

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
	mu        sync.Mutex
	listening bool
	reason    string
}

// newMultiSyncStatus returns a status starting "not yet attempted": a bind
// attempt has not happened yet at construction, and that is itself a
// distinct, stated reason (ADR-011: never default an unknown to healthy) —
// runMultiSyncListener overwrites it with the real outcome, one way or the
// other, before doing anything else.
func newMultiSyncStatus() *multiSyncStatus {
	return &multiSyncStatus{reason: "multisync listener has not attempted to bind yet"}
}

// set records the listener's current outcome. reason is required whenever
// listening is false, matching this project's Reason-required-when-not-
// healthy convention (mirrored on the wire by RenderPayload.MultiSyncReason).
func (s *multiSyncStatus) set(listening bool, reason string) {
	s.mu.Lock()
	s.listening = listening
	s.reason = reason
	s.mu.Unlock()
}

// get reports the listener's most recently recorded outcome.
func (s *multiSyncStatus) get() (listening bool, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listening, s.reason
}
