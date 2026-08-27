package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file tests [deriveInstanceHealth] against Step 5 contract section
// 5.3: the fix for the defect Step 5 would otherwise create in
// [mapFPPInstance]'s health verdict — every observation used to be a
// critical member with any value read as healthy, which would have made
// Step 5's new unsupported signals drag every real host to unknown forever
// AND kept mapping fpp.power.bad == true to healthy because the old mapper
// never looked at the value at all.

var healthRes = observation.ResourceRef{Kind: observation.ResourceFPP, ID: "player-01"}

// healthMustObs panics on error, matching this package's other
// _test.go helpers (mustObs in fixtures_test.go) for concise fixture
// construction.
func healthMustObs(o observation.Observation, err error) observation.Observation {
	if err != nil {
		panic("build health test fixture: " + err.Error())
	}
	return o
}

func healthCurrentObs(t *testing.T, signal observation.SignalID, value any, now time.Time) observation.Observation {
	t.Helper()
	return healthMustObs(observation.Measured(healthRes, signal, value, now, observation.WithSource("fpp-rest")))
}

func healthUnsupportedObs(t *testing.T, signal observation.SignalID, reason string) observation.Observation {
	t.Helper()
	return healthMustObs(observation.Unsupported(healthRes, signal, reason, observation.WithSource("fpp-rest")))
}

// TestDeriveInstanceHealthNoCriticalEvidenceIsUnknown proves an instance
// with zero observations at all — and, separately, one with observations
// but none of them health-critical — both report HealthUnknown, matching
// the Step 3 decision this task's spec says still stands.
func TestDeriveInstanceHealthNoCriticalEvidenceIsUnknown(t *testing.T) {
	now := time.Now()

	if got := deriveInstanceHealth(nil, now); got != observation.HealthUnknown {
		t.Errorf("deriveInstanceHealth(nil) = %q, want %q", got, observation.HealthUnknown)
	}

	onlyInformational := []observation.Observation{
		healthCurrentObs(t, "fpp.version", "9.4", now),
		healthCurrentObs(t, "fpp.uptime.seconds", int64(120), now),
	}
	if got := deriveInstanceHealth(onlyInformational, now); got != observation.HealthUnknown {
		t.Errorf("deriveInstanceHealth(only informational signals) = %q, want %q: informational signals must not manufacture a healthy verdict", got, observation.HealthUnknown)
	}
}

// TestDeriveInstanceHealthAllCriticalSignalsHealthyIsHealthy is the
// baseline positive case: all three named health-critical signals current
// and reporting their healthy value.
func TestDeriveInstanceHealthAllCriticalSignalsHealthyIsHealthy(t *testing.T) {
	now := time.Now()
	obs := []observation.Observation{
		healthCurrentObs(t, "fpp.reachable", true, now),
		healthCurrentObs(t, "fpp.fppd.state", "running", now),
		healthCurrentObs(t, "fpp.power.bad", false, now),
	}
	if got := deriveInstanceHealth(obs, now); got != observation.HealthHealthy {
		t.Errorf("deriveInstanceHealth(all healthy) = %q, want %q", got, observation.HealthHealthy)
	}
}

// TestDeriveInstanceHealthPowerBadTrueIsDegraded is the literal Step 5
// defect this map exists to fix: fpp.power.bad == true used to contribute
// HealthHealthy because the old mapper never looked at the value at all.
func TestDeriveInstanceHealthPowerBadTrueIsDegraded(t *testing.T) {
	now := time.Now()
	obs := []observation.Observation{
		healthCurrentObs(t, "fpp.reachable", true, now),
		healthCurrentObs(t, "fpp.fppd.state", "running", now),
		healthCurrentObs(t, "fpp.power.bad", true, now),
	}
	got := deriveInstanceHealth(obs, now)
	if got != observation.HealthDegraded {
		t.Errorf("deriveInstanceHealth(power.bad=true) = %q, want %q — a boolean fault signal must not read as fine when it fires", got, observation.HealthDegraded)
	}
}

// TestDeriveInstanceHealthReachableFalseIsFailed proves fpp.reachable's
// "otherwise failed" branch (not merely degraded — an unreachable FPP is
// the strongest of the three signals).
func TestDeriveInstanceHealthReachableFalseIsFailed(t *testing.T) {
	now := time.Now()
	obs := []observation.Observation{
		healthCurrentObs(t, "fpp.reachable", false, now),
	}
	if got := deriveInstanceHealth(obs, now); got != observation.HealthFailed {
		t.Errorf("deriveInstanceHealth(reachable=false) = %q, want %q", got, observation.HealthFailed)
	}
}

// TestDeriveInstanceHealthFPPDStateNotRunningIsDegraded proves
// fpp.fppd.state's "anything other than running is degraded" branch.
func TestDeriveInstanceHealthFPPDStateNotRunningIsDegraded(t *testing.T) {
	now := time.Now()
	obs := []observation.Observation{
		healthCurrentObs(t, "fpp.fppd.state", "starting", now),
	}
	if got := deriveInstanceHealth(obs, now); got != observation.HealthDegraded {
		t.Errorf("deriveInstanceHealth(fppd.state=starting) = %q, want %q", got, observation.HealthDegraded)
	}
}

// TestDeriveInstanceHealthUnsupportedCriticalSignalContributesNothing is
// Step 5's headline regression case: a real, deployed host (a remote in
// remote-mode, or any instance whose fpp.power.bad this source cannot
// report for whatever reason) whose health-critical signals are legitimately
// unsupported must not be dragged to HealthUnknown by that unsupported
// evidence — it contributes nothing, exactly like an informational signal
// would, which is different from (and better than) contributing a critical
// HealthUnknown member.
func TestDeriveInstanceHealthUnsupportedCriticalSignalContributesNothing(t *testing.T) {
	now := time.Now()

	// fpp.power.bad unsupported, but fpp.reachable and fpp.fppd.state are
	// both healthy: the aggregate must still read healthy, proving the
	// unsupported critical signal was excluded rather than counted as an
	// unknown critical member (which would have forced the whole aggregate
	// to HealthUnknown per observation.AggregateHealth's own
	// critical-unknown rule).
	obs := []observation.Observation{
		healthCurrentObs(t, "fpp.reachable", true, now),
		healthCurrentObs(t, "fpp.fppd.state", "running", now),
		healthUnsupportedObs(t, "fpp.power.bad", "this source does not report a power-fault flag"),
	}
	if got := deriveInstanceHealth(obs, now); got != observation.HealthHealthy {
		t.Errorf("deriveInstanceHealth(power.bad unsupported, others healthy) = %q, want %q: an unsupported critical signal must contribute nothing, not drag the aggregate to unknown", got, observation.HealthHealthy)
	}
}

// TestDeriveInstanceHealthManyUnsupportedSignalsStillResolvesHealthy is the
// realistic Step 5 fleet scenario named directly in this task's spec: many
// legitimately-unsupported signals (remote-mode playback fields,
// smart-receiver current, an absent pixelCount) alongside the three real
// health-critical signals, all healthy. Before this fix, every one of these
// unsupported observations was a critical member and dragged the instance
// to HealthUnknown forever.
func TestDeriveInstanceHealthManyUnsupportedSignalsStillResolvesHealthy(t *testing.T) {
	now := time.Now()
	obs := []observation.Observation{
		healthCurrentObs(t, "fpp.reachable", true, now),
		healthCurrentObs(t, "fpp.fppd.state", "running", now),
		healthCurrentObs(t, "fpp.power.bad", false, now),
		healthUnsupportedObs(t, "fpp.scheduler.next_playlist", "host is in remote mode; FPP does not report a scheduler"),
		healthUnsupportedObs(t, "fpp.playlist.repeat_mode", "host is in remote mode; FPP does not report a scheduler"),
		healthUnsupportedObs(t, "fpp.port.port_17.current_ma", "smart receiver position: pre-V5 receivers report no per-port current"),
		healthUnsupportedObs(t, "fpp.port.port_17.pixel_count", "pixelCount absent from this FPP's port document; the pixel-count operation has never been run on this host"),
	}
	if got := deriveInstanceHealth(obs, now); got != observation.HealthHealthy {
		t.Errorf("deriveInstanceHealth(healthy criticals + many unsupported signals) = %q, want %q — this is the Step 5 regression this fix exists to prevent", got, observation.HealthHealthy)
	}
}

// TestDeriveInstanceHealthWarningsNeverDriveHealth proves fpp.warnings.* is
// deliberately excluded from healthCriticalSignals: a large warnings count
// and a scary-looking warnings summary must neither drag a healthy instance
// down nor, on their own with no other evidence, produce anything but
// unknown — ADR-011 forbids classifying FPP's own warning strings into a
// verdict this coordinator does not have structured evidence for.
func TestDeriveInstanceHealthWarningsNeverDriveHealth(t *testing.T) {
	now := time.Now()

	// Warnings alongside healthy criticals: still healthy.
	withHealthyCriticals := []observation.Observation{
		healthCurrentObs(t, "fpp.reachable", true, now),
		healthCurrentObs(t, "fpp.fppd.state", "running", now),
		healthCurrentObs(t, "fpp.power.bad", false, now),
		healthCurrentObs(t, "fpp.warnings.count", int64(3), now),
		healthCurrentObs(t, "fpp.warnings.summary", "Cannot Ping ArtNet Channel Data Target; A Log Level is set to Debug", now),
	}
	if got := deriveInstanceHealth(withHealthyCriticals, now); got != observation.HealthHealthy {
		t.Errorf("deriveInstanceHealth(healthy criticals + warnings) = %q, want %q: warnings must not override healthy criticals", got, observation.HealthHealthy)
	}

	// Warnings alone, no critical evidence at all: unknown, not healthy and
	// not degraded — proves warnings are not silently treated as
	// health-critical themselves.
	onlyWarnings := []observation.Observation{
		healthCurrentObs(t, "fpp.warnings.count", int64(3), now),
		healthCurrentObs(t, "fpp.warnings.summary", "Cannot Ping ArtNet Channel Data Target", now),
	}
	if got := deriveInstanceHealth(onlyWarnings, now); got != observation.HealthUnknown {
		t.Errorf("deriveInstanceHealth(only warnings) = %q, want %q: warnings alone must never synthesize a health verdict", got, observation.HealthUnknown)
	}
}

// TestDeriveInstanceHealthNonCurrentCriticalSignalIsUnknown proves a
// health-critical signal that IS in healthCriticalSignals but is not
// StateCurrent (here, collection_failed) still contributes HealthUnknown,
// not HealthHealthy and not "excluded like unsupported" — collection_failed
// is missing evidence, a fundamentally different claim from unsupported's
// positive "this source cannot answer this", and [observation.DeriveHealth]
// is what enforces that distinction structurally.
func TestDeriveInstanceHealthNonCurrentCriticalSignalIsUnknown(t *testing.T) {
	now := time.Now()
	failed := healthMustObs(observation.CollectionFailed(healthRes, "fpp.reachable", "connection refused", observation.WithSource("fpp-rest")))

	got := deriveInstanceHealth([]observation.Observation{failed}, now)
	if got != observation.HealthUnknown {
		t.Errorf("deriveInstanceHealth(reachable collection_failed) = %q, want %q", got, observation.HealthUnknown)
	}
}

// TestDeriveInstanceHealthUnreachableInstanceReportsUnknown is Step 5
// review finding 4(a) pinned by name: the REAL production shape of "this
// FPP cannot be reached" is observation.CollectionFailed on fpp.reachable
// — internal/coordinator/collector/fpp never calls
// observation.Measured(false, ...) for this signal (see
// healthCriticalSignals' "fpp.reachable" doc comment) — and that shape
// must derive to HealthUnknown, never HealthFailed. HealthFailed is a name
// the healthCriticalSignals map CAN produce, but only for a hand-built
// Measured(false) observation
// (TestDeriveInstanceHealthReachableFalseIsFailed, above) that no real
// collector poll can ever construct; this test is functionally the same
// case as TestDeriveInstanceHealthNonCurrentCriticalSignalIsUnknown above
// but named to match the exact review finding it closes, per the finding's
// own instruction to "add a test pinning 'unreachable instance reports
// unknown'".
func TestDeriveInstanceHealthUnreachableInstanceReportsUnknown(t *testing.T) {
	now := time.Now()
	unreachable := healthMustObs(observation.CollectionFailed(healthRes, "fpp.reachable", "connection refused", observation.WithSource("fpp-rest")))

	got := deriveInstanceHealth([]observation.Observation{unreachable}, now)
	if got != observation.HealthUnknown {
		t.Errorf("deriveInstanceHealth(unreachable) = %q, want %q — an unreachable instance must report unknown, never failed (Step 3's decision, confirmed by Step 5 review finding 4(a))", got, observation.HealthUnknown)
	}
}

// TestDeriveInstanceHealthUnknownAgeCriticalSignalIsUnknown closes Step 5
// review finding 4(c): this file previously only exercised
// collection_failed as a non-current critical signal
// (TestDeriveInstanceHealthNonCurrentCriticalSignalIsUnknown above).
// Acceptance criterion 5 ("a retained-only MQTT host can never read
// healthy") survived only via a pre-existing pkg/observation-level test
// with no coverage at THIS package's own aggregation layer — exactly the
// Step 3/4 failure shape of testing an acceptance criterion one layer away
// from where it must actually hold, applied to the fpp-ghost ghost's specific
// case (a retained fpp.reachable, unknown_age, sourced fpp-mqtt).
func TestDeriveInstanceHealthUnknownAgeCriticalSignalIsUnknown(t *testing.T) {
	now := time.Now()
	unknownAge := healthMustObs(observation.MeasuredUnknownAge(healthRes, "fpp.reachable", true, observation.WithSource("fpp-mqtt")))

	got := deriveInstanceHealth([]observation.Observation{unknownAge}, now)
	if got != observation.HealthUnknown {
		t.Errorf("deriveInstanceHealth(reachable unknown_age) = %q, want %q — a retained/unknown-age critical signal must never read healthy", got, observation.HealthUnknown)
	}
}

// TestDeriveInstanceHealthHealthyRequiresFPPDStatePresentAndRunning pins
// Step 5 review finding 4(b)'s decision: fpp.reachable and fpp.power.bad
// alone are not enough for a healthy verdict. Before this fix, an instance
// whose source never reports fpp.fppd.state at all — not a hypothetical,
// but a real gap any source implementing only reachability and the
// power-fault flag has today — read fully healthy from two of three
// critical signals, with zero evidence the player daemon itself was ever
// running: "the HTTP server answered" is not evidence the player is
// healthy, and ADR-011 forbids a confident verdict the evidence does not
// support.
func TestDeriveInstanceHealthHealthyRequiresFPPDStatePresentAndRunning(t *testing.T) {
	now := time.Now()

	t.Run("fppd.state entirely absent caps healthy to unknown", func(t *testing.T) {
		obs := []observation.Observation{
			healthCurrentObs(t, "fpp.reachable", true, now),
			healthCurrentObs(t, "fpp.power.bad", false, now),
		}
		if got := deriveInstanceHealth(obs, now); got != observation.HealthUnknown {
			t.Errorf("deriveInstanceHealth(reachable+power.bad healthy, fppd.state never observed) = %q, want %q", got, observation.HealthUnknown)
		}
	})

	t.Run("fppd.state unsupported caps healthy to unknown", func(t *testing.T) {
		obs := []observation.Observation{
			healthCurrentObs(t, "fpp.reachable", true, now),
			healthCurrentObs(t, "fpp.power.bad", false, now),
			healthUnsupportedObs(t, "fpp.fppd.state", "this source does not report a daemon-state field"),
		}
		if got := deriveInstanceHealth(obs, now); got != observation.HealthUnknown {
			t.Errorf("deriveInstanceHealth(fppd.state unsupported, others healthy) = %q, want %q", got, observation.HealthUnknown)
		}
	})

	t.Run("fppd.state present, current, and running allows healthy", func(t *testing.T) {
		obs := []observation.Observation{
			healthCurrentObs(t, "fpp.reachable", true, now),
			healthCurrentObs(t, "fpp.power.bad", false, now),
			healthCurrentObs(t, "fpp.fppd.state", "running", now),
		}
		if got := deriveInstanceHealth(obs, now); got != observation.HealthHealthy {
			t.Errorf("deriveInstanceHealth(all three healthy, fppd.state present and running) = %q, want %q", got, observation.HealthHealthy)
		}
	})
}

// TestEvidenceReasonForStaleDoesNotEmbedAComputedAge is Step 5 review
// finding 2 pinned directly at its own source: evidenceReason (this file)
// used to build the stale-state reason with fmt.Sprintf against
// now.Sub(*o.ObservedAt) rounded to the second — a PRECOMPUTED AGE INSIDE
// A PAYLOAD, which ADR-020 forbids in as many words. Rendering the
// IDENTICAL stale observation at two very different render clock times
// must produce byte-identical JSON: any difference means something in the
// envelope is silently encoding "when this was rendered" rather than
// "what is true about the subject", which is exactly what defeated
// Hub.updateRendered's diff (contract section 6.5) and re-broadcast every
// stale FPP signal forever at tick rate — measured against the real fleet
// at roughly 43 KB/s per connected browser on an otherwise idle system.
func TestEvidenceReasonForStaleDoesNotEmbedAComputedAge(t *testing.T) {
	observedAt := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	o := healthMustObs(observation.Measured(healthRes, "fpp.uptime.seconds", int64(120), observedAt,
		observation.WithSource("fpp-rest"), observation.WithValidFor(30*time.Second)))

	now1 := observedAt.Add(5 * time.Minute)
	now2 := observedAt.Add(45 * time.Minute) // a very different render time, but still stale either way

	ev1 := mapEvidence(o, now1)
	ev2 := mapEvidence(o, now2)
	if ev1.State != "stale" || ev2.State != "stale" {
		t.Fatalf("both renderings must be stale for this test to prove anything: got %q and %q", ev1.State, ev2.State)
	}

	b1, err := json.Marshal(ev1)
	if err != nil {
		t.Fatalf("marshal ev1: %v", err)
	}
	b2, err := json.Marshal(ev2)
	if err != nil {
		t.Fatalf("marshal ev2: %v", err)
	}
	if string(b1) != string(b2) {
		t.Errorf("mapEvidence(same stale observation) rendered differently at two different clock times, want byte-identical JSON:\n  now=%s: %s\n  now=%s: %s", now1, b1, now2, b2)
	}
}

// TestMapEvidenceDeliversAuthoredReasonEvenWhenStateIsCurrent is a
// regression test: mapEvidence used to deliver Reason ONLY when state was
// not "current", which silently dropped
// every authored o.Reason on an observation whose freshness state IS
// current — exactly rendersuperseded.go's applySupersededVerdict, which
// overrides Value to "superseded" and sets Reason, but deliberately never
// touches ObservedAt/ValidFor (its own doc comment), so state keeps
// reading "current" (the node's own evidence really is fresh; only what
// the coordinator says the VALUE means changed). Before this fix, an
// operator's GET /api/v1/nodes/{id} carried the superseded verdict with no
// explanation at all.
func TestMapEvidenceDeliversAuthoredReasonEvenWhenStateIsCurrent(t *testing.T) {
	observedAt := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	o := healthMustObs(observation.Measured(healthRes, "surface.pipeline.state", "superseded", observedAt,
		observation.WithSource("node-render:render-01"), observation.WithValidFor(45*time.Second)))
	o.Reason = "this surface is holding a render authorized by show \"halloween-2026\" generation 1; the coordinator's currently active show is \"lane14-other\" generation 2"
	o.Quality = observation.QualityDerived

	ev := mapEvidence(o, observedAt)
	if ev.State != "current" {
		t.Fatalf("State = %q, want %q for this test to prove anything (a superseded verdict's freshness state stays current)", ev.State, "current")
	}
	if ev.Reason == nil || *ev.Reason != o.Reason {
		t.Errorf("Reason = %v, want %q delivered on the wire even though State is \"current\"", ev.Reason, o.Reason)
	}
}

// TestMapFPPInstanceResolvesMultiSourceObservations is Step 5 review
// finding 1's most direct wiring-layer gap: deleting `resolved :=
// ResolveObservations(fv.Observations)` from [mapFPPInstance] left the
// ENTIRE Go suite green, because nothing before this test fed two rows
// sharing one (resource, signal) pair with different sources through this
// function. Two candidates for the identical signal, one from each Step 5
// collector source, must collapse to exactly one [v1.Evidence] entry on the
// wire — never two — per contract section 5.2 ("resolution happens once,
// at read"). Every FPP-instance rendering path (GET /api/v1/fpp, GET
// /api/v1/fpp/{id}, the snapshot, and every fpp.changed stream event) goes
// through this exact function, so this one test is what the rest of Step
// 5's finding-1 coverage (handlers_test.go, stream_test.go) builds on.
func TestMapFPPInstanceResolvesMultiSourceObservations(t *testing.T) {
	now := time.Now()
	rest := healthMustObs(observation.Measured(healthRes, "fpp.status", "idle", now, observation.WithSource("fpp-rest")))
	mqtt := healthMustObs(observation.Measured(healthRes, "fpp.status", "idle", now.Add(-time.Second), observation.WithSource("fpp-mqtt")))

	fv := FPPInstanceView{
		InstanceID:   "player-01",
		Endpoint:     "http://10.0.1.20",
		Observations: []observation.Observation{rest, mqtt},
	}
	inst := mapFPPInstance(fv, now)

	count := 0
	for _, ev := range inst.Observations {
		if ev.Signal == "fpp.status" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("mapFPPInstance emitted %d Evidence rows for fpp.status from two collector sources, want exactly 1 — ResolveObservations must run in mapFPPInstance (mapping.go)", count)
	}
}

// --- deriveResolumeHealth ---
//
// This section is [TestDeriveInstanceHealth*]'s Resolume sibling, against
// [resolumeHealthCriticalSignals]/[deriveResolumeHealth] instead of
// [healthCriticalSignals]/[deriveInstanceHealth] — same aggregator
// (observation.AggregateHealth/DeriveHealth), a different, smaller
// two-signal critical map with no third fppdState-shaped cap.

var resolumeHealthRes = observation.ResourceRef{Kind: observation.ResourceResolume, ID: "resolume"}

func resolumeHealthCurrentObs(t *testing.T, signal observation.SignalID, value any, now time.Time) observation.Observation {
	t.Helper()
	return healthMustObs(observation.Measured(resolumeHealthRes, signal, value, now, observation.WithSource("resolume-rest")))
}

// TestDeriveResolumeHealthNoCriticalEvidenceIsUnknown mirrors
// TestDeriveInstanceHealthNoCriticalEvidenceIsUnknown: zero observations, and
// separately only informational ones, both report unknown.
func TestDeriveResolumeHealthNoCriticalEvidenceIsUnknown(t *testing.T) {
	now := time.Now()
	if got := deriveResolumeHealth(nil, now); got != observation.HealthUnknown {
		t.Errorf("deriveResolumeHealth(nil) = %q, want %q", got, observation.HealthUnknown)
	}
	informational := []observation.Observation{
		resolumeHealthCurrentObs(t, "resolume.product", "Arena 7.23.2", now),
	}
	if got := deriveResolumeHealth(informational, now); got != observation.HealthUnknown {
		t.Errorf("deriveResolumeHealth(only informational) = %q, want %q", got, observation.HealthUnknown)
	}
}

// TestDeriveResolumeHealthReachableTrueAndIdentifiedIsHealthy is the
// baseline positive case.
func TestDeriveResolumeHealthReachableTrueAndIdentifiedIsHealthy(t *testing.T) {
	now := time.Now()
	obs := []observation.Observation{
		resolumeHealthCurrentObs(t, "resolume.reachable", true, now),
		resolumeHealthCurrentObs(t, "resolume.composition.identified", "identified", now),
	}
	if got := deriveResolumeHealth(obs, now); got != observation.HealthHealthy {
		t.Errorf("deriveResolumeHealth(reachable=true, identified) = %q, want %q", got, observation.HealthHealthy)
	}
}

// TestDeriveResolumeHealthReachableFalseIsFailed proves
// resolume.reachable's "otherwise failed" branch — this seam's spec table
// pins this exactly, mirroring fpp.reachable's identical rule.
func TestDeriveResolumeHealthReachableFalseIsFailed(t *testing.T) {
	now := time.Now()
	obs := []observation.Observation{resolumeHealthCurrentObs(t, "resolume.reachable", false, now)}
	if got := deriveResolumeHealth(obs, now); got != observation.HealthFailed {
		t.Errorf("deriveResolumeHealth(reachable=false) = %q, want %q", got, observation.HealthFailed)
	}
}

// TestDeriveResolumeHealthCompositionNotIdentifiedIsDegraded is acceptance
// criterion 7: a "not_identified" reading rolls the instance up to degraded,
// never failed — the operator may legitimately have loaded a different
// composition, which is a thing to surface, not a system failure.
func TestDeriveResolumeHealthCompositionNotIdentifiedIsDegraded(t *testing.T) {
	now := time.Now()
	obs := []observation.Observation{
		resolumeHealthCurrentObs(t, "resolume.reachable", true, now),
		resolumeHealthCurrentObs(t, "resolume.composition.identified", "not_identified: missing 2 sampled clips", now),
	}
	got := deriveResolumeHealth(obs, now)
	if got != observation.HealthDegraded {
		t.Errorf("deriveResolumeHealth(composition.identified=not_identified) = %q, want %q, not %q", got, observation.HealthDegraded, observation.HealthFailed)
	}
}

// TestDeriveResolumeHealthCompositionDeckMismatchIsUnknown is finding 1's
// second regression guard (owner review, 2026-08-16), companion to
// TestIdentityObservationEmitsForDeckMismatch in the collector package: a
// deck_mismatch reading means the sampled clips did not resolve because
// the selected deck changed mid-check — an absence of evidence about
// identity, never a finding about the composition — so it must roll up to
// neither of the two different wrong answers.
func TestDeriveResolumeHealthCompositionDeckMismatchIsUnknown(t *testing.T) {
	now := time.Now()
	obs := []observation.Observation{
		resolumeHealthCurrentObs(t, "resolume.reachable", true, now),
		resolumeHealthCurrentObs(t, "resolume.composition.identified",
			"deck_mismatch: the selected deck changed while this identity check was running (expected deck id 2000000000001, now selected Deck Two (id 2000000000002))", now),
	}
	got := deriveResolumeHealth(obs, now)
	if got == observation.HealthHealthy {
		t.Errorf("deriveResolumeHealth(deck_mismatch) = healthy: that deletes the fact that identity could not be determined")
	}
	if got == observation.HealthDegraded {
		t.Errorf("deriveResolumeHealth(deck_mismatch) = degraded: that asserts a fault in the composition ShowMesh cannot support — it could not check, which is unknown")
	}
}

// TestDeriveResolumeHealthCompositionUnknownDuringLoadWindowIsUnknown proves
// the third value in the spec table's row: "unknown: ..." (the post-connect
// load window) reads unknown, distinct from both healthy and degraded.
func TestDeriveResolumeHealthCompositionUnknownDuringLoadWindowIsUnknown(t *testing.T) {
	now := time.Now()
	obs := []observation.Observation{
		resolumeHealthCurrentObs(t, "resolume.reachable", true, now),
		resolumeHealthCurrentObs(t, "resolume.composition.identified", "unknown: still within the post-connect load window", now),
	}
	if got := deriveResolumeHealth(obs, now); got != observation.HealthUnknown {
		t.Errorf("deriveResolumeHealth(composition.identified=unknown) = %q, want %q", got, observation.HealthUnknown)
	}
}

// TestDeriveResolumeHealthAgedOutReachableIsUnknown is acceptance criterion
// 6: an aged-out (stale) resolume.reachable observation rolls the instance
// up to unknown — three separate assertions, because "healthy" and "failed"
// are wrong in opposite directions and either would hide the fact that
// nothing current is known.
func TestDeriveResolumeHealthAgedOutReachableIsUnknown(t *testing.T) {
	// Pin map membership first: without this, deleting "resolume.reachable"
	// from resolumeHealthCriticalSignals entirely would also make this test
	// pass, because zero critical members aggregates to unknown too — the
	// assertion below would then be meaningless.
	if _, ok := resolumeHealthCriticalSignals["resolume.reachable"]; !ok {
		t.Fatalf(`resolumeHealthCriticalSignals is missing "resolume.reachable"`)
	}

	observedAt := time.Now().Add(-time.Hour)
	now := time.Now() // now is well past observedAt+ValidFor, below
	agedOut := healthMustObs(observation.Measured(resolumeHealthRes, "resolume.reachable", true, observedAt,
		observation.WithSource("resolume-rest"), observation.WithValidFor(30*time.Second)))

	got := deriveResolumeHealth([]observation.Observation{agedOut}, now)
	if got != observation.HealthUnknown {
		t.Errorf("deriveResolumeHealth(aged-out reachable=true) = %q, want %q", got, observation.HealthUnknown)
	}
}

// TestDeriveResolumeHealthUnsupportedCriticalSignalContributesNothing mirrors
// TestDeriveInstanceHealthUnsupportedCriticalSignalContributesNothing: a
// legitimately unsupported critical signal must not drag the aggregate to
// unknown.
func TestDeriveResolumeHealthUnsupportedCriticalSignalContributesNothing(t *testing.T) {
	now := time.Now()
	obs := []observation.Observation{
		resolumeHealthCurrentObs(t, "resolume.reachable", true, now),
		healthMustObs(observation.Unsupported(resolumeHealthRes, "resolume.composition.identified",
			"no composition has been uploaded to this coordinator yet", observation.WithSource("resolume-survey"))),
	}
	if got := deriveResolumeHealth(obs, now); got != observation.HealthHealthy {
		t.Errorf("deriveResolumeHealth(composition.identified unsupported, reachable healthy) = %q, want %q", got, observation.HealthHealthy)
	}
}

// TestMapResolumeInstanceResolvesMultiSourceObservations proves
// mapResolumeInstance runs ResolveObservations exactly once, mirroring
// TestMapFPPInstanceResolvesMultiSourceObservations.
func TestMapResolumeInstanceResolvesMultiSourceObservations(t *testing.T) {
	now := time.Now()
	a := healthMustObs(observation.Measured(resolumeHealthRes, "resolume.product", "Arena 7.23.2", now, observation.WithSource("resolume-rest")))
	b := healthMustObs(observation.Measured(resolumeHealthRes, "resolume.product", "Arena 7.23.2", now.Add(-time.Second), observation.WithSource("resolume-rest")))

	rv := ResolumeInstanceView{InstanceID: "resolume", Observations: []observation.Observation{a, b}}
	inst := mapResolumeInstance(rv, nil, now)

	count := 0
	for _, ev := range inst.Observations {
		if ev.Signal == "resolume.product" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("mapResolumeInstance emitted %d Evidence rows for resolume.product, want exactly 1 — ResolveObservations must run in mapResolumeInstance (mapping.go)", count)
	}
	if inst.Composition != nil {
		t.Errorf("Composition = %+v, want nil when the caller passed nil", inst.Composition)
	}
}
