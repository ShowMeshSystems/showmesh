package agent

import (
	"sync"
	"time"
)

// fppConnectHTTPStatus is thread-safe, publishable evidence of whether this
// node's FPP Connect HTTP compatibility listener (ADR-044) currently holds
// its bound socket, mirroring multiSyncStatus's exact shape and reasoning:
// a bind failure must reach the same render report an operator already
// watches, not just a log line. runFPPConnectHTTPListener writes this;
// publishOneRenderReport reads it.
//
// listening stays true while the socket is bound but the listener is
// administratively disabled (the pushed enabled flag is false): the socket
// itself is still open, only the routes' behavior changed, so this is not a
// bind failure and must not be reported as one. reason still carries the
// disabled explanation in that case, the same "Reason required whenever not
// straightforwardly healthy" convention multiSyncStatus follows.
type fppConnectHTTPStatus struct {
	mu         sync.Mutex
	listening  bool
	reason     string
	observedAt time.Time
}

// newFPPConnectHTTPStatus returns a status starting "not yet attempted",
// matching newMultiSyncStatus's identical ADR-011 reasoning: a bind attempt
// has not happened yet at construction, and that is itself a distinct,
// stated reason.
func newFPPConnectHTTPStatus() *fppConnectHTTPStatus {
	return &fppConnectHTTPStatus{reason: "fppconnect http listener has not attempted to bind yet"}
}

// set records the listener's current outcome, stamping observedAt from the
// real wall clock at the moment of this actual bind attempt, failure, or
// disabled-state transition — see multiSyncStatus.set's identical doc
// comment for why this is evidence time, not collection time.
func (s *fppConnectHTTPStatus) set(listening bool, reason string) {
	s.mu.Lock()
	s.listening = listening
	s.reason = reason
	s.observedAt = time.Now().UTC()
	s.mu.Unlock()
}

// get reports the listener's most recently recorded outcome and when it was
// recorded. observedAt is the zero time.Time until the first set call.
func (s *fppConnectHTTPStatus) get() (listening bool, reason string, observedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listening, s.reason, s.observedAt
}
