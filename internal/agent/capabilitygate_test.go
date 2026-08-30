package agent

import (
	"testing"
	"time"
)

// TestCapabilityDetectionGateCoalescesAndDropsStaleResults is the
// mechanism-level proof behind scheduleCapabilityDetection's own
// single-flight and generation-stamping: a trigger that arrives while
// one run is in flight is coalesced into exactly one follow-up carrying
// the LATEST generation (never one follow-up per trigger, and never the
// generation an intermediate trigger captured), and a run whose
// generation is no longer current by the time it finishes never
// publishes at all.
//
// run is a controllable stand-in for runCapabilityDetection's own body:
// it reports it has started (so the test can synchronize), blocks until
// the test releases it (simulating a real detection's up-to-120s probe
// window), checks gate.isCurrent itself exactly as runCapabilityDetection
// does before it would otherwise store/publish, then reports it has
// finished. Every assertion the test makes on shared state (published)
// happens only after receiving that finished signal for the relevant
// generation, so there is a real happens-before relationship and no
// data race between the run goroutine and the test goroutine.
func TestCapabilityDetectionGateCoalescesAndDropsStaleResults(t *testing.T) {
	gate := &capabilityDetectionGate{}

	started := make(chan uint64, 8)
	finished := make(chan uint64, 8)
	release := make(chan struct{})
	var published []uint64

	run := func(gen uint64) {
		started <- gen
		<-release
		if gate.isCurrent(gen) {
			published = append(published, gen)
		}
		finished <- gen
	}

	mustReceive := func(t *testing.T, ch chan uint64, label string) uint64 {
		t.Helper()
		select {
		case gen := <-ch:
			return gen
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: timed out waiting", label)
			return 0
		}
	}

	gate.trigger(run)
	if gen := mustReceive(t, started, "first run start"); gen != 1 {
		t.Fatalf("first run started with generation %d, want 1", gen)
	}

	// Two more triggers arrive while generation 1 is still blocked
	// in-flight: single-flight means neither starts a second run, they
	// only bump the generation and mark a follow-up pending.
	gate.trigger(run)
	gate.trigger(run)

	// Release generation 1's run and wait for it to actually finish
	// deciding whether to publish: its own generation (1) is no longer
	// current (the gate is now at 3), so it must NOT have published.
	release <- struct{}{}
	if gen := mustReceive(t, finished, "first run finish"); gen != 1 {
		t.Fatalf("first run to finish carried generation %d, want 1", gen)
	}

	// The coalesced follow-up must start with generation 3 directly,
	// never generation 2 (which was superseded before it ever got a
	// chance to run at all).
	if gen := mustReceive(t, started, "coalesced follow-up start"); gen != 3 {
		t.Fatalf("coalesced follow-up started with generation %d, want 3", gen)
	}

	// Release generation 3's run and wait for it to finish: nothing has
	// superseded it, so it must publish.
	release <- struct{}{}
	if gen := mustReceive(t, finished, "second run finish"); gen != 3 {
		t.Fatalf("second run to finish carried generation %d, want 3", gen)
	}

	select {
	case gen := <-started:
		t.Fatalf("a third run started (generation %d); only two runs (generation 1, dropped, then generation 3, published) should ever have executed", gen)
	case <-time.After(200 * time.Millisecond):
	}

	if len(published) != 1 || published[0] != 3 {
		t.Fatalf("published = %v, want exactly [3]: generation 1 must be dropped as stale, generation 2 must never run at all, and generation 3 must be the only one actually published", published)
	}
}

// TestCapabilityDetectionGateRunsSequentiallyNotConcurrently proves the
// single-flight guarantee directly: even with several triggers arriving
// back to back, run's body is never invoked a second time before its
// first invocation has returned, and the resulting coalescing means
// three rapid triggers produce exactly two actual runs, not three, one
// per trigger.
func TestCapabilityDetectionGateRunsSequentiallyNotConcurrently(t *testing.T) {
	gate := &capabilityDetectionGate{}

	var active, maxActive int
	finished := make(chan uint64, 8)

	run := func(gen uint64) {
		active++
		if active > maxActive {
			maxActive = active
		}
		time.Sleep(2 * time.Millisecond)
		active--
		finished <- gen
	}

	gate.trigger(run)
	gate.trigger(run)
	gate.trigger(run)

	var runs int
loop:
	for {
		select {
		case <-finished:
			runs++
			if runs == 2 {
				break loop
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d run(s) completed in time, want 2 (the initial run plus one coalesced follow-up for the two extra triggers)", runs)
		}
	}

	select {
	case <-finished:
		t.Fatal("a third run completed; three rapid triggers must coalesce into exactly two runs, not one run per trigger")
	case <-time.After(200 * time.Millisecond):
	}

	if maxActive > 1 {
		t.Fatalf("maxActive = %d, want 1: two run bodies executed concurrently", maxActive)
	}
}
