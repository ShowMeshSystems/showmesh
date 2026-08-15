package coordinator

import (
	"context"
	"testing"
	"time"
)

func TestMacroStopContext(t *testing.T) {
	deadline := time.Now().Add(10 * time.Second)
	ctx, cancel := macroStopContext(context.Background(), deadline, 3*time.Second)
	defer cancel()

	stopBy, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected a deadline")
	}
	// Bounded from BOTH sides: above, so the reserve is actually held
	// back, and below, so Stop keeps its own share. A macroStopContext
	// that returned "now" on every path (zero budget for Stop, defeating
	// the one reason Stop exists) passed this test's first version.
	wantMax := deadline.Add(-3 * time.Second)
	if stopBy.After(wantMax.Add(50 * time.Millisecond)) {
		t.Fatalf("stop deadline %v is later than expected %v", stopBy, wantMax)
	}
	if stopBy.Before(wantMax.Add(-50 * time.Millisecond)) {
		t.Fatalf("stop deadline %v is earlier than expected %v; Stop's own share of the budget was given away", stopBy, wantMax)
	}
}

func TestMacroStopContext_ClampsWhenLittleTimeRemains(t *testing.T) {
	// Deadline already inside the reserve window: stop must still get a
	// deadline at or after now, never one already in the past.
	deadline := time.Now().Add(1 * time.Second)
	ctx, cancel := macroStopContext(context.Background(), deadline, 3*time.Second)
	defer cancel()

	stopBy, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected a deadline")
	}
	if stopBy.Before(time.Now().Add(-50 * time.Millisecond)) {
		t.Fatalf("stop deadline %v is in the past", stopBy)
	}
}

// TestRunShutdown_DisconnectAlwaysGetsRemainingTime proves the defect this
// fix closes: an executor Stop that blocks past its own sub-deadline (an
// in-flight run cannot be cancelled, so Stop legitimately times out per its
// own doc comment) must not also starve afterStop's context of the time it
// needs to disconnect cleanly. Reverting runShutdown to hand stop and
// afterStop the same shared, already-expiring context makes this fail,
// because afterStop's context would then carry no remaining time.
func TestRunShutdown_DisconnectAlwaysGetsRemainingTime(t *testing.T) {
	deadline := time.Now().Add(macroExecutorStopReserve + 200*time.Millisecond)

	blockingStop := func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}

	var afterRemaining time.Duration
	afterStop := func(ctx context.Context) {
		dl, ok := ctx.Deadline()
		if !ok {
			t.Fatal("expected afterStop context to carry a deadline")
		}
		afterRemaining = time.Until(dl)
	}

	stopErr := runShutdown(context.Background(), deadline, blockingStop, afterStop)
	if stopErr == nil {
		t.Fatal("expected stop to report its own sub-deadline exceeded")
	}
	if afterRemaining < macroExecutorStopReserve/2 {
		t.Fatalf("afterStop context had only %v left, want at least %v", afterRemaining, macroExecutorStopReserve/2)
	}
}
