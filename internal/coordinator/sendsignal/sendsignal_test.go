package sendsignal

import (
	"context"
	"testing"
)

func TestSentRunsTheHookOnceThroughDerivedContexts(t *testing.T) {
	calls := 0
	ctx := WithHook(context.Background(), func() { calls++ })
	derived, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	Sent(derived)
	Sent(ctx)
	if calls != 1 {
		t.Fatalf("hook ran %d times, want 1", calls)
	}
	Sent(context.Background())
}
