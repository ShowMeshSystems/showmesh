//go:build cgo

package gstengine

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestAwaitElementsStateReportsDeadlineOnTie pits an already-expired ctx
// against a done channel that already holds a successful result, so both
// select cases are ready at once. Without the ctx.Err() re-check, select's
// random tie-break lets the done case win and report nil past the deadline;
// 200 trials make that tie land on both orderings.
func TestAwaitElementsStateReportsDeadlineOnTie(t *testing.T) {
	b := &branch{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	const trials = 200
	for i := 0; i < trials; i++ {
		b.pendingStateChanges.Add(1)
		done := make(chan error, 1)
		done <- nil

		err := b.awaitElementsState(ctx, done)

		if !errors.Is(err, context.Canceled) {
			t.Fatalf("trial %d: awaitElementsState returned %v, want context.Canceled even though done also succeeded", i, err)
		}
		deadline := time.Now().Add(time.Second)
		for b.pendingStateChanges.Load() != 0 {
			if time.Now().After(deadline) {
				t.Fatalf("trial %d: pendingStateChanges did not return to 0", i)
			}
			time.Sleep(time.Millisecond)
		}
	}
}
