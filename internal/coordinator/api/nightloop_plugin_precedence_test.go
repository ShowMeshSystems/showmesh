package api

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file checks, rather than assumes, the one claim the owner's ruling
// on fpp-plugin's ValidFor (45s, deliberately the same as fpp.DefaultValidFor
// rather than a number derived from any measured plugin tick cadence) rests
// on: a plugin that reported "playing" and then died without ever posting
// "stop" must not keep the launch guard refusing forever. Its own tier-1
// observation stays current for up to 45s, but a fresher REST poll (every
// 15s) supersedes it on [preferObservation]'s "later ObservedAt wins" rule,
// the identical mechanism nightloop_mqtt_precedence_test.go already proves
// for fpp-mqtt. If this were NOT true, the ValidFor ruling itself would be
// wrong, per that ruling's own stated condition.

// pluginPlaylistNameObservation mirrors mqttPlaylistNameLive
// (nightloop_mqtt_precedence_test.go) for the fpp-plugin source: a real
// ObservedAt (tier 1, never unknown-age, since the collector always
// stamps one from the coordinator's own clock at push time) and a 45s
// ValidFor, matching fpp.DefaultValidFor per the owner's ruling.
func pluginPlaylistNameObservation(instanceID, name string, observedAt time.Time) observation.Observation {
	return mustObs(observation.Measured(
		observation.ResourceRef{Kind: observation.ResourceFPP, ID: instanceID},
		observation.SignalID(fppPlaylistNameSignal), name, observedAt,
		observation.WithSource("fpp-plugin"), observation.WithCollectedAt(observedAt), observation.WithValidFor(45*time.Second),
	))
}

func pluginStatusObservation(instanceID, status string, observedAt time.Time) observation.Observation {
	return mustObs(observation.Measured(
		observation.ResourceRef{Kind: observation.ResourceFPP, ID: instanceID},
		observation.SignalID(fppStatusSignal), status, observedAt,
		observation.WithSource("fpp-plugin"), observation.WithCollectedAt(observedAt), observation.WithValidFor(45*time.Second),
	))
}

// TestNightShowLaunchIfBusy_FresherRESTBeatsAgingPluginClaim is the check
// the owner's ruling explicitly asked for: a plugin observation that
// stopped arriving (dead, or the plugin uninstalled) without ever posting
// "stop" stays at its last reported value for up to its own 45s ValidFor.
// A REST poll landing more recently, even one that is itself past the 5s
// launch-guard window, must still outrank the stale plugin claim on
// [preferObservation]'s tier-1 recency rule — never on the static source
// tie-break, which only decides an exact ObservedAt tie. If REST did NOT
// win here, a dead plugin would wedge the launch guard refused
// indefinitely, which is exactly the regression the ValidFor ruling's own
// "worst case degrades to today's behavior" argument depends on not
// happening.
func TestNightShowLaunchIfBusy_FresherRESTBeatsAgingPluginClaim(t *testing.T) {
	now := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	pluginStaleAt := now.Add(-40 * time.Second) // within its own 45s ValidFor, so still StateCurrent
	restFreshAt := now.Add(-3 * time.Second)    // within nightShowLaunchEvidenceMaxAge's 5s window

	obs := &fakeObservationLister{obs: []observation.Observation{
		// The plugin's own last-known state, unchanged since a dead plugin
		// stopped ticking: still reports "playing" the old resting playlist.
		pluginStatusObservation("player-01", fppStatusValuePlaying, pluginStaleAt),
		pluginPlaylistNameObservation("player-01", "halloween-resting", pluginStaleAt),
		// A fresh REST poll has since observed the resting playlist ended.
		statusObservation("player-01", fppStatusValueIdle, restFreshAt),
		playlistNameObservation("player-01", "", restFreshAt),
	}}
	deps := Dependencies{Observations: obs}.withDefaults()
	h := &handlers{deps: deps, clock: func() time.Time { return now }, logger: testLogger()}

	got, reason := h.nightShowLaunchIfBusy(context.Background(), now, nightLaunchPayload())
	if got != fppIfBusyRefuse {
		t.Fatalf("ifBusy = %q, want %q (fresh REST evidence says idle; a stale plugin claim must not override it)", got, fppIfBusyRefuse)
	}
	if reason != "" {
		t.Fatalf("staleEvidenceReason = %q, want empty: this is FPP genuinely idle per the winning (REST) evidence, not a coordinator-side staleness refusal", reason)
	}
}

// TestNightShowLaunchIfBusy_LivePluginBeatsStaleRESTOnRecency is the other
// half: while the plugin is actively ticking, its evidence is both fresher
// and, being event-driven, arrives well inside nightShowLaunchEvidenceMaxAge
// even though the REST row backing the same signal is already stale by
// that window — proving the plugin is the source that actually lets a
// launch proceed sub-second, which is the entire point of building it.
func TestNightShowLaunchIfBusy_LivePluginBeatsStaleRESTOnRecency(t *testing.T) {
	now := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	restStaleAt := now.Add(-14 * time.Second)
	pluginFreshAt := now.Add(-200 * time.Millisecond)

	obs := &fakeObservationLister{obs: []observation.Observation{
		statusObservation("player-01", fppStatusValuePlaying, restStaleAt),
		playlistNameObservation("player-01", "halloween-resting", restStaleAt),
		pluginStatusObservation("player-01", fppStatusValuePlaying, pluginFreshAt),
		pluginPlaylistNameObservation("player-01", "halloween-resting", pluginFreshAt),
	}}
	deps := Dependencies{Observations: obs}.withDefaults()
	h := &handlers{deps: deps, clock: func() time.Time { return now }, logger: testLogger()}

	got, reason := h.nightShowLaunchIfBusy(context.Background(), now, nightLaunchPayload())
	if got != fppIfBusyReplace {
		t.Fatalf("ifBusy = %q, want %q (a live plugin reading 200ms old must win over a 14s-old REST reading and pass the 5s freshness window)", got, fppIfBusyReplace)
	}
	if reason != "" {
		t.Fatalf("staleEvidenceReason = %q, want empty on a genuine replace", reason)
	}
}
