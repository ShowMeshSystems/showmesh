package coordinator

import (
	"os"
	"strings"
	"testing"
)

// The fallback hold service counts its 45 second start hold, and a
// plugin's silence, from MarkStarted. Run must call it once the loops are
// spawned and before the listener starts, or both count from process
// start and part of the hold is spent starting up. Run also has to give
// the service its reading of the player and its audit log, or no hold ever
// ends without its plugin and an automatic clear leaves no record.
func TestRunWiresTheFallbackHoldServiceAndMarksItStartedBeforeListening(t *testing.T) {
	raw, err := os.ReadFile("coordinator.go")
	if err != nil {
		t.Fatalf("read coordinator.go: %v", err)
	}
	src := string(raw)
	position := func(call string) int {
		t.Helper()
		if n := strings.Count(src, call); n != 1 {
			t.Fatalf("coordinator.go has %d occurrences of %q, want exactly one", n, call)
		}
		return strings.Index(src, call)
	}
	loops := position("fallbackHolds.Run(ctx)")
	nightLoop := position("nightLoop.Run(ctx)")
	cueLoop := position("cueActivationLoop.Run(ctx)")
	marked := position("fallbackHolds.MarkStarted(time.Now())")
	listen := position("srv.ListenAndServe()")
	for name, at := range map[string]int{"the hold service's own loop": loops, "the night loop": nightLoop, "the Cue loop": cueLoop} {
		if at > marked {
			t.Errorf("MarkStarted is called before %s is spawned", name)
		}
	}
	if marked > listen {
		t.Error("MarkStarted is called after the HTTP listener starts")
	}
	position("fallbackHolds.SetPlayerReader(api.NewFallbackPlayerReader(apiDeps.Observations))")
	position("fallbackHolds.SetAudit(identitySvc)")
	position("fallbackHolds.SetNudge(cueActivationLoop.Nudge)")
}
