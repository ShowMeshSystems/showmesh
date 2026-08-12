package api

import (
	"reflect"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file tests [preferObservation] and [ResolveObservations] against
// Step 5 contract section 5.2's precedence rule, including every pair of
// tiers in BOTH orders — section 8's acceptance criterion 4 — and every
// named consequence section 5.2 calls out explicitly as "worth stating,
// because each is a test."

var precedenceRes = observation.ResourceRef{Kind: observation.ResourceFPP, ID: "player-01"}

const precedenceSignal = observation.SignalID("fpp.test.signal")

// tier1 builds a tier-1 candidate: a value with a known ObservedAt.
func tier1(t *testing.T, source string, observedAt time.Time, value any) observation.Observation {
	t.Helper()
	o, err := observation.Measured(precedenceRes, precedenceSignal, value, observedAt,
		observation.WithSource(source), observation.WithCollectedAt(observedAt))
	if err != nil {
		t.Fatalf("build tier-1 observation: %v", err)
	}
	return o
}

// tier2 builds a tier-2 candidate: a value with an unknown ObservedAt (the
// retained-MQTT case).
func tier2(t *testing.T, source string, value any) observation.Observation {
	t.Helper()
	o, err := observation.MeasuredUnknownAge(precedenceRes, precedenceSignal, value,
		observation.WithSource(source), observation.WithCollectedAt(time.Now()))
	if err != nil {
		t.Fatalf("build tier-2 observation: %v", err)
	}
	return o
}

// tier3 builds a tier-3 candidate: an absence, of the given state.
func tier3(t *testing.T, source string, absence observation.State) observation.Observation {
	t.Helper()
	reason := "test reason for " + string(absence)
	var (
		o   observation.Observation
		err error
	)
	switch absence {
	case observation.StateUnsupported:
		o, err = observation.Unsupported(precedenceRes, precedenceSignal, reason, observation.WithSource(source))
	case observation.StateCollectionFailed:
		o, err = observation.CollectionFailed(precedenceRes, precedenceSignal, reason, observation.WithSource(source))
	case observation.StateNotCollected:
		o, err = observation.NotCollected(precedenceRes, precedenceSignal, reason, observation.WithSource(source))
	default:
		t.Fatalf("tier3: unhandled absence state %q", absence)
	}
	if err != nil {
		t.Fatalf("build tier-3 observation: %v", err)
	}
	return o
}

// sameObservation reports whether a and b are the identical observation
// value (not merely "in the same tier/from the same source") — used so a
// test can assert exactly which of two distinct candidates
// [preferObservation] picked, not just that it picked "one of them".
func sameObservation(a, b observation.Observation) bool {
	return reflect.DeepEqual(a, b)
}

// TestPreferObservationEveryTierPairBothOrders is section 8's acceptance
// criterion 4: a table covering every pair of tiers (including a tier
// against itself), each case checked in both argument orders, proving
// [preferObservation] is symmetric — preferObservation(a, b) and
// preferObservation(b, a) always agree on which of the two wins,
// regardless of which happened to be seen first by [ResolveObservations]'
// grouping loop.
func TestPreferObservationEveryTierPairBothOrders(t *testing.T) {
	t0 := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	later := t0.Add(5 * time.Second)

	tests := []struct {
		name string
		a, b observation.Observation
		want string // "a" or "b"
	}{
		// --- tier 1 vs tier 1 ---
		{
			name: "tier1 vs tier1: later ObservedAt wins regardless of source",
			a:    tier1(t, "fpp-rest", t0, "older-rest"),
			b:    tier1(t, "fpp-mqtt", later, "newer-mqtt"),
			want: "b",
		},
		{
			name: "tier1 vs tier1: equal ObservedAt breaks on source, fpp-rest beats fpp-mqtt",
			a:    tier1(t, "fpp-mqtt", t0, "mqtt-value"),
			b:    tier1(t, "fpp-rest", t0, "rest-value"),
			want: "b",
		},
		// --- tier 1 vs tier 2 ---
		{
			name: "tier1 vs tier2: tier1 always wins",
			a:    tier1(t, "fpp-mqtt", t0, "live-mqtt-value"),
			b:    tier2(t, "fpp-rest", "retained-rest-value"),
			want: "a",
		},
		// --- tier 1 vs tier 3 ---
		{
			name: "tier1 vs tier3: a live MQTT value beats a REST collection_failed",
			a:    tier1(t, "fpp-mqtt", t0, true),
			b:    tier3(t, "fpp-rest", observation.StateCollectionFailed),
			want: "a",
		},
		{
			name: "tier1 vs tier3: fresh REST value beats stale-looking MQTT absence too, tier1 always over tier3",
			a:    tier3(t, "fpp-mqtt", observation.StateNotCollected),
			b:    tier1(t, "fpp-rest", t0, "reachable"),
			want: "b",
		},
		// --- tier 2 vs tier 2 ---
		{
			name: "tier2 vs tier2: no time to compare, source tie-break decides",
			a:    tier2(t, "fpp-mqtt", "mqtt-retained"),
			b:    tier2(t, "fpp-rest", "rest-retained"),
			want: "b",
		},
		// --- tier 2 vs tier 3 ---
		{
			name: "tier2 vs tier3: a retained-only MQTT value beats a REST collection_failed",
			a:    tier2(t, "fpp-mqtt", true),
			b:    tier3(t, "fpp-rest", observation.StateCollectionFailed),
			want: "a",
		},
		// --- tier 3 vs tier 3 ---
		{
			name: "tier3 vs tier3: unsupported beats collection_failed",
			a:    tier3(t, "fpp-rest", observation.StateUnsupported),
			b:    tier3(t, "fpp-mqtt", observation.StateCollectionFailed),
			want: "a",
		},
		{
			name: "tier3 vs tier3: REST unsupported beats MQTT not_collected",
			a:    tier3(t, "fpp-rest", observation.StateUnsupported),
			b:    tier3(t, "fpp-mqtt", observation.StateNotCollected),
			want: "a",
		},
		{
			name: "tier3 vs tier3: collection_failed beats not_collected",
			a:    tier3(t, "fpp-rest", observation.StateCollectionFailed),
			b:    tier3(t, "fpp-mqtt", observation.StateNotCollected),
			want: "a",
		},
		{
			name: "tier3 vs tier3: equal absence rank breaks on source, fpp-rest beats fpp-mqtt",
			a:    tier3(t, "fpp-mqtt", observation.StateCollectionFailed),
			b:    tier3(t, "fpp-rest", observation.StateCollectionFailed),
			want: "b",
		},
		// --- tier3 vs tier3, SAME source: Step 5 review finding 5. Every
		// rank-differentiated case above happens to also put the expected
		// winner on fpp-rest, so mutating absenceRank to a constant (deleting
		// "unsupported beats collection_failed beats not_collected" entirely)
		// leaves every case above green: the static source tie-break alone
		// reproduces the same answer every time, and absenceRank is never
		// actually the deciding factor. These same-source pairs remove the
		// source tie-break from the equation entirely — only absenceRank can
		// decide them.
		{
			name: "tier3 vs tier3, SAME source (fpp-rest): unsupported beats collection_failed with no source tie-break available",
			a:    tier3(t, "fpp-rest", observation.StateUnsupported),
			b:    tier3(t, "fpp-rest", observation.StateCollectionFailed),
			want: "a",
		},
		{
			name: "tier3 vs tier3, SAME source (fpp-mqtt): collection_failed beats not_collected with no source tie-break available",
			a:    tier3(t, "fpp-mqtt", observation.StateCollectionFailed),
			b:    tier3(t, "fpp-mqtt", observation.StateNotCollected),
			want: "a",
		},
		{
			name: "tier3 vs tier3, SAME source (fpp-rest): unsupported beats not_collected with no source tie-break available",
			a:    tier3(t, "fpp-rest", observation.StateUnsupported),
			b:    tier3(t, "fpp-rest", observation.StateNotCollected),
			want: "a",
		},
		// --- tier3 vs tier3, source REVERSED from the differentiated cases
		// above: proves absenceRank is the actual deciding factor, not merely
		// correlated with which source happens to win — the weaker absence
		// (by rank) sits on fpp-rest here, and the rank must still win over
		// the source tie-break for the winner to land on fpp-mqtt.
		{
			name: "tier3 vs tier3, REVERSED source: fpp-mqtt unsupported still beats fpp-rest collection_failed",
			a:    tier3(t, "fpp-mqtt", observation.StateUnsupported),
			b:    tier3(t, "fpp-rest", observation.StateCollectionFailed),
			want: "a",
		},
		{
			name: "tier3 vs tier3, REVERSED source: fpp-mqtt unsupported still beats fpp-rest not_collected",
			a:    tier3(t, "fpp-mqtt", observation.StateUnsupported),
			b:    tier3(t, "fpp-rest", observation.StateNotCollected),
			want: "a",
		},
		{
			name: "tier3 vs tier3, REVERSED source: fpp-mqtt collection_failed still beats fpp-rest not_collected",
			a:    tier3(t, "fpp-mqtt", observation.StateCollectionFailed),
			b:    tier3(t, "fpp-rest", observation.StateNotCollected),
			want: "a",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wantObs := tc.a
			if tc.want == "b" {
				wantObs = tc.b
			}

			gotForward := preferObservation(tc.a, tc.b)
			if !sameObservation(gotForward, wantObs) {
				t.Errorf("preferObservation(a, b) = %+v, want %s (%+v)", gotForward, tc.want, wantObs)
			}

			gotReverse := preferObservation(tc.b, tc.a)
			if !sameObservation(gotReverse, wantObs) {
				t.Errorf("preferObservation(b, a) = %+v, want %s (%+v) — result must not depend on argument order", gotReverse, tc.want, wantObs)
			}
		})
	}
}

// TestPreferObservationFreshVsStaleRestOnly proves "a fresh REST value beats
// a stale REST value and vice versa, purely on ObservedAt" for the specific
// same-source case contract section 5.2 calls out, independent of the
// source tie-break (both candidates share a source here, so only the clock
// comparison can be deciding it).
func TestPreferObservationFreshVsStaleRestOnly(t *testing.T) {
	older := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	fresher := older.Add(time.Minute)

	stale := tier1(t, "fpp-rest", older, "stale-value")
	fresh := tier1(t, "fpp-rest", fresher, "fresh-value")

	if got := preferObservation(stale, fresh); !sameObservation(got, fresh) {
		t.Errorf("preferObservation(stale, fresh) = %+v, want the fresher observation", got)
	}
	if got := preferObservation(fresh, stale); !sameObservation(got, fresh) {
		t.Errorf("preferObservation(fresh, stale) = %+v, want the fresher observation", got)
	}
}

// TestPreferObservationUnrankedSourcesAreDeterministic proves the "a source
// not listed here ranks 0" fallback (sourcePrecedence's doc comment) is
// still a total, deterministic order rather than a coin flip: two
// candidates from equally-unranked sources must resolve the same way
// regardless of which is passed first.
func TestPreferObservationUnrankedSourcesAreDeterministic(t *testing.T) {
	a := tier2(t, "some-future-source", "a-value")
	b := tier2(t, "another-future-source", "b-value")

	forward := preferObservation(a, b)
	reverse := preferObservation(b, a)
	if !sameObservation(forward, reverse) {
		t.Errorf("preferObservation is not deterministic for two equally-unranked sources: forward=%+v reverse=%+v", forward, reverse)
	}
}

// TestResolveObservationsGroupsByResourceAndSignalIndependently proves
// ResolveObservations does not conflate different signals or different
// resources: a real multi-instance, multi-signal observation set must come
// back with exactly one entry per distinct (resource, signal), each
// resolved on its own.
func TestResolveObservationsGroupsByResourceAndSignalIndependently(t *testing.T) {
	t0 := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	res2 := observation.ResourceRef{Kind: observation.ResourceFPP, ID: "player-02"}

	mustObservation := func(o observation.Observation, err error) observation.Observation {
		t.Helper()
		if err != nil {
			t.Fatalf("build observation: %v", err)
		}
		return o
	}

	obs := []observation.Observation{
		tier1(t, "fpp-rest", t0, "player-01-reachable"),
		mustObservation(observation.Measured(precedenceRes, "fpp.other.signal", "player-01-other", t0, observation.WithSource("fpp-rest"))),
		mustObservation(observation.Measured(res2, precedenceSignal, "player-02-reachable", t0, observation.WithSource("fpp-rest"))),
	}

	resolved := ResolveObservations(obs)
	if len(resolved) != 3 {
		t.Fatalf("len(resolved) = %d, want 3: distinct (resource, signal) pairs must not be merged", len(resolved))
	}
}

// TestResolveObservationsNoOpForSingleSource proves the common case (every
// signal still has exactly one source) is a pass-through: ResolveObservations
// must not alter or drop an observation that has no competing candidate.
func TestResolveObservationsNoOpForSingleSource(t *testing.T) {
	t0 := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	only := tier1(t, "fpp-rest", t0, "only-value")

	resolved := ResolveObservations([]observation.Observation{only})
	if len(resolved) != 1 || !sameObservation(resolved[0], only) {
		t.Fatalf("ResolveObservations([only]) = %+v, want [only] unchanged", resolved)
	}
}

// TestResolveObservationsRetainedMQTTNeverBeatsLiveRestByStaleness proves
// contract section 5.2's "do not fold staleness into the ranking" holds
// through the actual entry point, not just preferObservation in isolation:
// a REST value that is old enough to have gone StateAt-stale by the
// caller's clock must still win tier-1-over-tier-2 against a retained MQTT
// value, because tier membership (does ObservedAt exist at all) — not
// current-vs-stale — is what this resolution step compares. Staleness is
// then correctly visible only after resolution, via StateAt against now,
// exactly as mapEvidence already does.
func TestResolveObservationsRetainedMQTTNeverBeatsLiveRestByStaleness(t *testing.T) {
	veryOld := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	restStale := tier1(t, "fpp-rest", veryOld, "old-but-a-real-timestamp")
	mqttRetained := tier2(t, "fpp-mqtt", "retained-value")

	resolved := ResolveObservations([]observation.Observation{restStale, mqttRetained})
	if len(resolved) != 1 {
		t.Fatalf("len(resolved) = %d, want 1", len(resolved))
	}
	if !sameObservation(resolved[0], restStale) {
		t.Errorf("resolved = %+v, want the tier-1 (known ObservedAt) REST value even though it is chronologically old", resolved[0])
	}
	// And the resolved value's own state, computed at a "now" far past the
	// old ObservedAt, is legitimately stale — this test is not claiming the
	// resolved value reads as current, only that it was the one chosen.
	now := time.Now()
	if state := resolved[0].StateAt(now); state != observation.StateStale && state != observation.StateCurrent {
		t.Errorf("resolved[0].StateAt(now) = %q, want stale or current (a real tier-1 value), never an absence state", state)
	}
}
