package coordinator

import (
	"testing"
	"time"
)

// fakeClock lets tests drive BrokerManager's timestamps deterministically,
// without real sleeps.
type fakeClock struct {
	t time.Time
}

func (c *fakeClock) now() time.Time { return c.t }

func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// newTestBrokerManager builds a BrokerManager whose state-tracking logic
// (setConnected/State) can be exercised directly, without dialing a real
// broker: cm is left nil, which is fine since these tests never call
// anything that touches it.
func newTestBrokerManager(clock *fakeClock) *BrokerManager {
	initAt := clock.now()
	return &BrokerManager{
		now:   clock.now,
		state: BrokerState{Connected: false, Since: initAt, ObservedAt: initAt},
	}
}

func TestBrokerManagerSetConnectedMovesSinceOnlyOnChange(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)}
	bm := newTestBrokerManager(clock)

	initial := bm.State()

	// Re-confirm the same value (false) at a later time: ObservedAt should
	// advance, Since should not move.
	clock.advance(5 * time.Second)
	bm.setConnected(false)

	afterReconfirm := bm.State()
	if !afterReconfirm.Since.Equal(initial.Since) {
		t.Errorf("Since = %v after re-confirming the same value, want unchanged %v", afterReconfirm.Since, initial.Since)
	}
	if !afterReconfirm.ObservedAt.Equal(clock.now()) {
		t.Errorf("ObservedAt = %v, want %v", afterReconfirm.ObservedAt, clock.now())
	}
	if afterReconfirm.ObservedAt.Equal(initial.ObservedAt) {
		t.Errorf("ObservedAt did not advance on re-confirmation")
	}

	// Now actually change the value: Since must move to the new time.
	clock.advance(5 * time.Second)
	bm.setConnected(true)

	afterChange := bm.State()
	if !afterChange.Connected {
		t.Fatalf("Connected = false, want true")
	}
	if !afterChange.Since.Equal(clock.now()) {
		t.Errorf("Since = %v after a real transition, want %v", afterChange.Since, clock.now())
	}
	if !afterChange.ObservedAt.Equal(clock.now()) {
		t.Errorf("ObservedAt = %v, want %v", afterChange.ObservedAt, clock.now())
	}

	// Re-confirm the new value (true): Since should stay at the transition
	// time, only ObservedAt should move.
	sinceAfterChange := afterChange.Since
	clock.advance(5 * time.Second)
	bm.setConnected(true)

	afterSecondReconfirm := bm.State()
	if !afterSecondReconfirm.Since.Equal(sinceAfterChange) {
		t.Errorf("Since = %v after re-confirming true, want unchanged %v", afterSecondReconfirm.Since, sinceAfterChange)
	}
	if !afterSecondReconfirm.ObservedAt.Equal(clock.now()) {
		t.Errorf("ObservedAt = %v, want %v", afterSecondReconfirm.ObservedAt, clock.now())
	}
}

func TestBrokerManagerStateInitiallyDisconnected(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)}
	bm := newTestBrokerManager(clock)

	state := bm.State()
	if state.Connected {
		t.Errorf("Connected = true before any observation, want false")
	}
	if state.ObservedAt.IsZero() {
		t.Errorf("ObservedAt is zero, want an initial timestamp so freshness is well-defined from construction")
	}
}
