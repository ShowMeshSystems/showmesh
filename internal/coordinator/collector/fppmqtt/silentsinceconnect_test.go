package fppmqtt

import (
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

	silent, reason := c.SilentSinceConnect()
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

	silent, reason := c.SilentSinceConnect()
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

	if silent, reason := c.SilentSinceConnect(); silent {
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
	if silent, _ := c.SilentSinceConnect(); !silent {
		t.Fatalf("SilentSinceConnect = false before the fix under test, want true (setup precondition)")
	}

	deliver(c, "falcon/player/fpp-player/status", []byte("idle"), false)

	if silent, reason := c.SilentSinceConnect(); silent {
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

	if silent, reason := c.SilentSinceConnect(); silent {
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
	if silent, _ := c.SilentSinceConnect(); !silent {
		t.Fatalf("setup precondition failed: want silent before the reconnect")
	}

	c.setConnected(false, "mqtt broker connection lost; will retry")
	c.setConnected(true, "")
	deliver(c, "falcon/player/fpp-player/status", []byte("idle"), false)

	if silent, reason := c.SilentSinceConnect(); silent {
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
	if silent, _ := c.SilentSinceConnect(); silent {
		t.Fatalf("setup precondition failed: want not silent before the reconnect")
	}

	c.setConnected(false, "mqtt broker connection lost; will retry")
	c.setConnected(true, "")

	now = now.Add(silentSinceConnectThreshold)
	if silent, reason := c.SilentSinceConnect(); !silent {
		t.Fatalf("SilentSinceConnect = false, want true: the new connection has heard nothing for a full threshold")
	} else if reason == "" {
		t.Errorf("reason is empty, want it set alongside silent=true")
	}
}
