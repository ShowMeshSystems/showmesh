package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file proves, rather than assumes, the owner's ruling that MQTT is
// the preferred source for fpp.playlist.name (the night launch guard's own
// evidence) and REST is only the fallback — and works out, with an actual
// test rather than an argument, what nightShowLaunchIfBusy does right
// after a broker reconnect, when the only MQTT evidence is a retained
// replay (observation.MeasuredUnknownAge, per ResolveObservations tier 2)
// and the REST value is older than nightShowLaunchEvidenceMaxAge but still
// within its own 45s ValidFor (observation.StateCurrent).

// mqttPlaylistNameRetained mirrors internal/coordinator/collector/fppmqtt's
// render.go buildObservation for a RETAINED delivery: CollectedAt is the
// receipt time, ObservedAt is nil (observation time genuinely unknown) —
// tier 2 under ResolveObservations, regardless of how recently it was
// actually received.
func mqttPlaylistNameRetained(instanceID, name string, receivedAt time.Time) observation.Observation {
	return mustObs(observation.MeasuredUnknownAge(
		observation.ResourceRef{Kind: observation.ResourceFPP, ID: instanceID},
		observation.SignalID(fppPlaylistNameSignal), name,
		observation.WithSource("fpp-mqtt"), observation.WithCollectedAt(receivedAt),
	))
}

// mqttPlaylistNameLive mirrors the same buildObservation for a LIVE
// (non-retained) delivery: a real ObservedAt, 30s ValidFor
// (fppmqtt.DefaultValidFor) — tier 1 under ResolveObservations, so it
// competes with a REST row on ObservedAt recency, not merely on tier.
func mqttPlaylistNameLive(instanceID, name string, receivedAt time.Time) observation.Observation {
	return mustObs(observation.Measured(
		observation.ResourceRef{Kind: observation.ResourceFPP, ID: instanceID},
		observation.SignalID(fppPlaylistNameSignal), name, receivedAt,
		observation.WithSource("fpp-mqtt"), observation.WithCollectedAt(receivedAt), observation.WithValidFor(30*time.Second),
	))
}

func nightLaunchPayload() config.NightSessionPayload {
	return config.NightSessionPayload{
		ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "player-01", Playlist: "halloween-show"},
		Resting:      config.NightSessionResting{FPPInstanceID: "player-01", Playlist: "halloween-resting"},
	}
}

// TestNightShowLaunchIfBusy_ReconnectRetainedLosesToStaleRESTTier1 is the
// case the owner's ruling names by hand: right after a reconnect, the only
// MQTT evidence is the retained replay (tier 2), and REST's own row is 14s
// old — well past nightShowLaunchEvidenceMaxAge's 5s, but still inside
// fpp.DefaultValidFor's 45s, so it reads observation.StateCurrent.
//
// ResolveObservations' tier rule (contract section 5.2, "do not fold
// staleness into the ranking") picks the REST row regardless: tier 1
// always beats tier 2, however fresh the tier-2 delivery actually was in
// real time. The launch guard therefore evaluates REST's age, not MQTT's,
// and refuses via the SAME staleEvidenceReason branch
// TestNightShowLaunchIfBusy_StaleEvidenceRefuses already covers — this is
// not a silent fallback to stale evidence being treated as fresh; it is a
// correctly-explained refusal, using REST's own age.
func TestNightShowLaunchIfBusy_ReconnectRetainedLosesToStaleRESTTier1(t *testing.T) {
	now := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	restStaleAt := now.Add(-14 * time.Second)        // >5s (our window), <45s (fpp.DefaultValidFor): StateCurrent.
	mqttRetainedAt := now.Add(-1 * time.Millisecond) // arrived essentially now, but retained.

	obs := &fakeObservationLister{obs: []observation.Observation{
		statusObservation("player-01", fppStatusValuePlaying, restStaleAt),
		playlistNameObservation("player-01", "halloween-resting", restStaleAt),     // fpp-rest, tier 1
		mqttPlaylistNameRetained("player-01", "halloween-resting", mqttRetainedAt), // fpp-mqtt, tier 2
	}}
	deps := Dependencies{Observations: obs}.withDefaults()
	h := &handlers{deps: deps, clock: func() time.Time { return now }, logger: testLogger()}

	got, reason := h.nightShowLaunchIfBusy(context.Background(), now, nightLaunchPayload())
	if got != fppIfBusyRefuse {
		t.Fatalf("ifBusy = %q, want %q (REST's own 14s-old row must not license replace)", got, fppIfBusyRefuse)
	}
	if reason == "" || !strings.Contains(reason, "old, past") {
		t.Fatalf("reason = %q, want the stale-coordinator-evidence explanation naming REST's own age, not empty and not a generic mismatch", reason)
	}
}

// TestNightShowLaunchIfBusy_LiveMQTTBeatsStaleRESTOnRecency proves the
// other half of the ruling: once a LIVE (non-retained) MQTT delivery has
// landed, it is tier 1 exactly like REST, and [preferObservation]'s "later
// ObservedAt wins" rule (not the static source tie-break, which only
// applies to an exact ObservedAt tie) picks the fresher MQTT value over a
// REST row that is 14s old — proving MQTT is the preferred source once it
// has anything live to offer, with REST as the fallback only while MQTT
// does not.
func TestNightShowLaunchIfBusy_LiveMQTTBeatsStaleRESTOnRecency(t *testing.T) {
	now := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	restStaleAt := now.Add(-14 * time.Second)
	mqttFreshAt := now.Add(-200 * time.Millisecond)

	obs := &fakeObservationLister{obs: []observation.Observation{
		statusObservation("player-01", fppStatusValuePlaying, restStaleAt),
		playlistNameObservation("player-01", "halloween-resting", restStaleAt), // fpp-rest, 14s old
		mqttPlaylistNameLive("player-01", "halloween-resting", mqttFreshAt),    // fpp-mqtt, 200ms old
	}}
	deps := Dependencies{Observations: obs}.withDefaults()
	h := &handlers{deps: deps, clock: func() time.Time { return now }, logger: testLogger()}

	got, reason := h.nightShowLaunchIfBusy(context.Background(), now, nightLaunchPayload())
	if got != fppIfBusyReplace {
		t.Fatalf("ifBusy = %q, want %q (a live MQTT reading 200ms old must win over a 14s-old REST reading and pass the 5s freshness window)", got, fppIfBusyReplace)
	}
	if reason != "" {
		t.Fatalf("staleEvidenceReason = %q, want empty on a genuine replace", reason)
	}
}
