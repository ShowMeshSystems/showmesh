package fppmqtt

import (
	"strings"
	"testing"
	"time"
)

// TestSilentSinceConnectFiresAfterThreshold: connected, nothing ever
// received, silentSinceConnectThreshold has fully elapsed since connect.
func TestSilentSinceConnectFiresAfterThreshold(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	c := newTestCollector(t, map[string]string{"main": "fpp-player"}, &now)
	c.setConnected(true, "")

	now = now.Add(silentSinceConnectThreshold)

	silent, reason := c.SilentSinceConnect("main")
	if !silent {
		t.Fatalf("SilentSinceConnect = false, want true after %s with no message received", silentSinceConnectThreshold)
	}
	if reason == "" {
		t.Errorf("reason is empty, want it set alongside silent=true")
	}
}

// TestSilentSinceConnectDoesNotFireBeforeThreshold: identical setup, one
// tick short of the threshold: this is the "just connected, has not heard
// its first publish yet" case the threshold exists to protect against a
// false alarm on.
func TestSilentSinceConnectDoesNotFireBeforeThreshold(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	c := newTestCollector(t, map[string]string{"main": "fpp-player"}, &now)
	c.setConnected(true, "")

	now = now.Add(silentSinceConnectThreshold - time.Nanosecond)

	silent, reason := c.SilentSinceConnect("main")
	if silent {
		t.Fatalf("SilentSinceConnect = true, want false one nanosecond short of the threshold")
	}
	if reason != "" {
		t.Errorf("reason = %q, want empty when silent is false", reason)
	}
}

// TestSilentSinceConnectDoesNotFireWhenMessageArrived: a message on some
// subscribed topic arrives well before the threshold; the condition must
// never fire even once the threshold has since elapsed. This is the
// positive control this issue's brief calls for: a collector that HAS
// received a message must not report the condition.
func TestSilentSinceConnectDoesNotFireWhenMessageArrived(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	c := newTestCollector(t, map[string]string{"main": "fpp-player"}, &now)
	c.setConnected(true, "")

	deliver(c, "falcon/player/fpp-player/status", []byte("idle"), false)

	now = now.Add(silentSinceConnectThreshold)

	if silent, reason := c.SilentSinceConnect("main"); silent {
		t.Fatalf("SilentSinceConnect = true (reason %q), want false: a message was received since connect", reason)
	}
}

// TestSilentSinceConnectClearsOnMessageAfterFiring: the condition has
// already fired; a single message arriving after that must clear it
// immediately, with no wait-out window on the clear side.
func TestSilentSinceConnectClearsOnMessageAfterFiring(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	c := newTestCollector(t, map[string]string{"main": "fpp-player"}, &now)
	c.setConnected(true, "")

	now = now.Add(silentSinceConnectThreshold)
	if silent, _ := c.SilentSinceConnect("main"); !silent {
		t.Fatalf("SilentSinceConnect = false before the fix under test, want true (setup precondition)")
	}

	deliver(c, "falcon/player/fpp-player/status", []byte("idle"), false)

	if silent, reason := c.SilentSinceConnect("main"); silent {
		t.Fatalf("SilentSinceConnect = true (reason %q), want false immediately after one message arrives", reason)
	}
}

// TestSilentSinceConnectNeverFiresWhileDisconnected: Poll already reports
// collection_failed on every signal while disconnected (render_test.go);
// this condition must not duplicate that with a second, less specific
// claim.
func TestSilentSinceConnectNeverFiresWhileDisconnected(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	c := newTestCollector(t, map[string]string{"main": "fpp-player"}, &now)
	c.setConnected(true, "")

	now = now.Add(silentSinceConnectThreshold * 10)
	c.setConnected(false, "mqtt broker connection lost; will retry")

	if silent, reason := c.SilentSinceConnect("main"); silent {
		t.Fatalf("SilentSinceConnect = true (reason %q), want false while disconnected", reason)
	}
}

// TestSilentSinceConnectReconnectClearsPriorSilence: a connection was
// silent past the threshold, then drops and reconnects, and the new
// connection hears a message right away. The condition must clear: a
// reconnect must not let a previous connection's silence outlive it.
func TestSilentSinceConnectReconnectClearsPriorSilence(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	c := newTestCollector(t, map[string]string{"main": "fpp-player"}, &now)
	c.setConnected(true, "")

	now = now.Add(silentSinceConnectThreshold)
	if silent, _ := c.SilentSinceConnect("main"); !silent {
		t.Fatalf("setup precondition failed: want silent before the reconnect")
	}

	c.setConnected(false, "mqtt broker connection lost; will retry")
	c.setConnected(true, "")
	deliver(c, "falcon/player/fpp-player/status", []byte("idle"), false)

	if silent, reason := c.SilentSinceConnect("main"); silent {
		t.Fatalf("SilentSinceConnect = true (reason %q), want false: the new connection already heard a message", reason)
	}
}

// TestSilentSinceConnectReconnectRefiresOnRenewedSilence: a connection was
// healthy (heard a message), then drops and reconnects, and the new
// connection hears nothing for a full threshold. The condition must fire
// again: a prior connection's success must not carry forward and mask a
// new connection's silence.
func TestSilentSinceConnectReconnectRefiresOnRenewedSilence(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	c := newTestCollector(t, map[string]string{"main": "fpp-player"}, &now)
	c.setConnected(true, "")
	deliver(c, "falcon/player/fpp-player/status", []byte("idle"), false)

	now = now.Add(silentSinceConnectThreshold)
	if silent, _ := c.SilentSinceConnect("main"); silent {
		t.Fatalf("setup precondition failed: want not silent before the reconnect")
	}

	c.setConnected(false, "mqtt broker connection lost; will retry")
	c.setConnected(true, "")

	now = now.Add(silentSinceConnectThreshold)
	if silent, reason := c.SilentSinceConnect("main"); !silent {
		t.Fatalf("SilentSinceConnect = false, want true: the new connection has heard nothing for a full threshold")
	} else if reason == "" {
		t.Errorf("reason is empty, want it set alongside silent=true")
	}
}

// TestSilentSinceConnectOneHostAmongSeveralIsIndividuallyIdentifiable: with
// two hosts on one connection, one goes silent and the other keeps
// publishing. The silent host must be identifiable by its own id, and the
// publishing host must never be reported silent just because a different
// host on the same connection is.
func TestSilentSinceConnectOneHostAmongSeveralIsIndividuallyIdentifiable(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	c := newTestCollector(t, map[string]string{"quiet": "fpp-quiet", "loud": "fpp-loud"}, &now)
	c.setConnected(true, "")

	deliver(c, "falcon/player/fpp-loud/status", []byte("idle"), false)

	now = now.Add(silentSinceConnectThreshold)
	deliver(c, "falcon/player/fpp-loud/status", []byte("idle"), false)

	if silent, reason := c.SilentSinceConnect("quiet"); !silent {
		t.Fatalf("SilentSinceConnect(%q) = false, want true: this host has never published", "quiet")
	} else if reason == "" {
		t.Errorf("reason is empty, want it set alongside silent=true")
	}
	if silent, reason := c.SilentSinceConnect("loud"); silent {
		t.Fatalf("SilentSinceConnect(%q) = true (reason %q), want false: this host just published", "loud", reason)
	}
}

// TestSilentSinceConnectNeverPublishedDiffersFromWentQuiet: a host that has
// never published a single message (misconfiguration candidate) and a
// host that published once and then went quiet (failed-fast-path
// candidate) both report silent=true, with the same [api.CollectorRunState]
// either way, since this package adds no new one. But their reason text
// must never read the same, so an operator reading the row can tell the
// two situations apart. This is an operator-readable distinction only:
// reason is free-form prose, not a machine contract.
func TestSilentSinceConnectNeverPublishedDiffersFromWentQuiet(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	c := newTestCollector(t, map[string]string{"never": "fpp-never", "went-quiet": "fpp-went-quiet"}, &now)
	c.setConnected(true, "")

	deliver(c, "falcon/player/fpp-went-quiet/status", []byte("idle"), false)

	// The per-host latch only clears on a reconnect (see
	// TestSilentSinceConnectReconnectRefiresOnRenewedSilence), so a host
	// that published once and then falls silent within the SAME
	// connection never re-fires: that is deliberate, unchanged behavior.
	// A genuine went-quiet host is one whose latch was cleared by a
	// reconnect and has not published again since.
	c.setConnected(false, "mqtt broker connection lost; will retry")
	c.setConnected(true, "")

	now = now.Add(silentSinceConnectThreshold)

	neverSilent, neverReason := c.SilentSinceConnect("never")
	if !neverSilent {
		t.Fatalf("SilentSinceConnect(%q) = false, want true", "never")
	}
	quietSilent, quietReason := c.SilentSinceConnect("went-quiet")
	if !quietSilent {
		t.Fatalf("SilentSinceConnect(%q) = false, want true", "went-quiet")
	}

	if neverReason == quietReason {
		t.Fatalf("both hosts report the identical reason %q, want a never-published host distinguishable from a went-quiet one", neverReason)
	}
	if !strings.Contains(neverReason, "never received") {
		t.Errorf("never-published reason = %q, want it to say it has never received a message", neverReason)
	}
	if strings.Contains(quietReason, "never received") {
		t.Errorf("went-quiet reason = %q, want it to name when the last message arrived, not claim it never received one", quietReason)
	}
}

// TestSilentSinceConnectMissingBookkeepingNeverReadsHealthyPastThreshold
// exercises the grace-window boundary directly: an instance id with no
// bookkeeping at all (never in the message store, a pure map miss) must
// read running only inside the grace window, and never past the threshold
// this same package already establishes for every other host.
func TestSilentSinceConnectMissingBookkeepingNeverReadsHealthyPastThreshold(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	c := newTestCollector(t, map[string]string{"ghost": "fpp-ghost"}, &now)
	c.setConnected(true, "")

	if silent, _ := c.SilentSinceConnect("ghost"); silent {
		t.Fatalf("SilentSinceConnect(%q) = true, want false inside the grace window (deliberate, per the merged threshold behavior)", "ghost")
	}

	now = now.Add(silentSinceConnectThreshold)
	if silent, reason := c.SilentSinceConnect("ghost"); !silent {
		t.Fatalf("SilentSinceConnect(%q) = false (reason %q), want true: absent bookkeeping past the threshold must never read as healthy", "ghost", reason)
	}
}
