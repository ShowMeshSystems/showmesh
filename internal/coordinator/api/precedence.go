package api

import "github.com/showmeshsystems/showmesh/pkg/observation"

// This file implements the Step 5 contract's section 5.2 precedence rule:
// the one place this API resolves more than one collector source's
// evidence for the same (resource, signal) into the single [observation.Observation]
// every other rendering path (mapFPPInstance's Health and Observations,
// handleObservations' flat list, and — because mapFPPInstance is the one
// function every fpp.changed stream event and every FPP-instance response
// goes through — the SSE stream too) treats as authoritative.
//
// schemaV4 (internal/coordinator/store/migrations.go) widened the
// observations table's primary key to include source specifically so a
// second collector source (internal/coordinator/collector/fppmqtt, "source"
// "fpp-mqtt") reporting the same signal internal/coordinator/collector/fpp
// ("fpp-rest") already reports does not silently overwrite it — both rows
// persist. [Store.ListObservations] therefore now legitimately returns more
// than one [observation.Observation] for one (resource, signal) pair.
// [ResolveObservations] is the pure function that turns that multi-source
// slice back into one Observation per (resource, signal), so every caller
// downstream of it — API handlers, the SSE hub, health derivation — never
// has to reason about more than one collector source at once. Resolving
// once here, rather than at every call site independently, is what makes
// this rule testable in isolation and impossible to apply inconsistently
// between endpoints.

// resolutionKey identifies one (resource, signal) group for
// [ResolveObservations]. Unexported: it is this file's own grouping detail,
// never something a caller constructs.
type resolutionKey struct {
	kind   observation.ResourceKind
	id     string
	signal observation.SignalID
}

func resolutionKeyOf(o observation.Observation) resolutionKey {
	return resolutionKey{kind: o.Resource.Kind, id: o.Resource.ID, signal: o.Signal}
}

// sourcePrecedenceRank is the static, source-name tie-break contract
// section 5.2 requires when two candidates land in the same tier and (for
// tier 1) carry the identical ObservedAt: "fpp-rest beats fpp-mqtt,
// because a REST value came from a round trip this coordinator initiated
// in the last few seconds, while an MQTT value we cannot time is a
// replay." Higher wins. A source not listed here (e.g. "mqtt-inventory",
// which never collides with another source for the same signal today, or
// any future source) ranks 0 — below every explicitly-ranked source — as
// an arbitrary but deterministic last resort; nothing in this codebase
// currently relies on that fallback actually being reached.
var sourcePrecedenceRank = map[string]int{
	"fpp-mqtt": 1,
	"fpp-rest": 2,
}

func sourcePrecedence(source string) int { return sourcePrecedenceRank[source] }

// observationTier classifies o into contract section 5.2's three tiers.
// Lower is higher-precedence: tier 1 (a value with a known observation
// time) always outranks tier 2 (a value with an unknown observation time),
// which always outranks tier 3 (no value at all). This mirrors
// [observation.Observation.StateAt]'s own ordering of concerns (Absence,
// then ObservedAt, then ValidFor) but deliberately stops short of
// StateAt's staleness check: contract section 5.2 says outright "do not
// fold staleness into the ranking... folding it in here would make the
// resolution depend on when it ran," so this function takes no `now` and
// cannot even ask whether a tier-1 value has aged past its ValidFor — a
// stale tier-1 value still outranks a current tier-3 absence, exactly as a
// fresh one would, and the caller's own [observation.Observation.StateAt]
// call at render time is what turns "resolved to a stale tier-1 value"
// into the wire's "stale" state, same as it always has.
func observationTier(o observation.Observation) int {
	if o.Value != nil {
		if o.ObservedAt != nil {
			return 1
		}
		return 2
	}
	return 3
}

// absenceRank orders tier 3 (no value at all) candidates against each
// other: "unsupported beats collection_failed beats not_collected"
// (contract section 5.2) — unsupported is the strongest statement (a
// source positively knows it cannot answer this signal), not_collected the
// weakest (nothing has even been attempted yet). Higher wins.
//
// A State this function does not recognize ranks 0, below every named
// absence state — the same conservative-default posture
// [observation.severityRank] documents for an unrecognized Health: this
// function is fed obs.Absence values that ultimately came from SQLite rows
// or a synthesized placeholder, not from a fresh, just-validated
// construction, so an unrecognized value here is stored or transmitted
// data, not a programming error to panic on. Ranking it lowest rather than
// panicking keeps ResolveObservations answering (conservatively) for the
// (resource, signal) group it belongs to instead of taking the whole
// request down with it.
func absenceRank(a observation.State) int {
	switch a {
	case observation.StateUnsupported:
		return 3
	case observation.StateCollectionFailed:
		return 2
	case observation.StateNotCollected:
		return 1
	default:
		return 0
	}
}

// preferObservation returns whichever of a and b (both already known to
// share a (resource, signal) pair) contract section 5.2's precedence rule
// picks. It is symmetric — preferObservation(a, b) and preferObservation(b,
// a) always pick the same one of the two — which is what makes
// [ResolveObservations]' result independent of input order; see this
// file's table-driven test for the both-orders proof section 5.2's
// acceptance criterion asks for.
func preferObservation(a, b observation.Observation) observation.Observation {
	ta, tb := observationTier(a), observationTier(b)
	if ta != tb {
		if ta < tb {
			return a
		}
		return b
	}

	switch ta {
	case 1:
		// Tier 1: later ObservedAt wins, purely on the clock — "a fresh REST
		// value beats a stale MQTT value and vice versa" (contract section
		// 5.2). Equal ObservedAt falls through to the source tie-break
		// below.
		if a.ObservedAt.After(*b.ObservedAt) {
			return a
		}
		if b.ObservedAt.After(*a.ObservedAt) {
			return b
		}
	case 2:
		// Tier 2: both observation times are unknown, so there is nothing
		// to compare but the source tie-break below.
	case 3:
		// Tier 3: rank by the strength of the absence claim.
		ra, rb := absenceRank(a.Absence), absenceRank(b.Absence)
		if ra != rb {
			if ra > rb {
				return a
			}
			return b
		}
	}

	// Tie-break 1: static source precedence (contract section 5.2:
	// "fpp-rest beats fpp-mqtt").
	if pa, pb := sourcePrecedence(a.Source), sourcePrecedence(b.Source); pa != pb {
		if pa > pb {
			return a
		}
		return b
	}

	// Tie-break 2: two sources this codebase has not explicitly ranked
	// (sourcePrecedence's "ranks 0" fallback) both land here with an equal
	// rank, which the first tie-break above cannot resolve. Comparing the
	// source names themselves, lexicographically, keeps the result a
	// function of the two candidates' own content rather than of which one
	// the caller happened to pass as a vs. b — a naive "always keep a"
	// fallback here would make preferObservation(a, b) and
	// preferObservation(b, a) disagree whenever a and b are genuinely tied,
	// which would make [ResolveObservations]' result depend on the
	// caller-supplied slice's own iteration order, exactly the dependency
	// this function's doc comment promises it does not have. This branch
	// existed once as "return a always" and TestPreferObservationUnrankedSourcesAreDeterministic
	// caught the resulting asymmetry directly.
	if a.Source != b.Source {
		if a.Source > b.Source {
			return a
		}
		return b
	}

	// Both candidates share every ranking signal this function knows about,
	// including the exact same source name. Real input never reaches this:
	// [Store.ListObservations] holds at most one row per (resource, signal,
	// source) since schemaV4 made that the primary key, so two same-source
	// candidates for one group cannot both exist. Returning a is safe
	// precisely because that precondition means a and b are indistinguishable
	// by anything this function is asked to compare.
	return a
}

// ResolveObservations is the single, documented, pure function contract
// section 5.2 asks for: it groups obs by (Resource.Kind, Resource.ID,
// Signal) and returns exactly one [observation.Observation] per group,
// chosen by [preferObservation]. It takes no clock and looks at nothing
// but obs itself — see [observationTier]'s doc comment for why staleness
// deliberately plays no part in the ranking.
//
// The result is unordered (each group's winner appears in the position its
// first-seen candidate occupied in obs); callers that need a stable
// display order — every caller in this package does — apply
// [sortObservations] to the result exactly as they already do to a raw,
// single-source slice.
//
// An observation whose (Resource, Signal) has no competing candidate
// passes through unchanged: ResolveObservations is a no-op for the
// overwhelming majority of this coordinator's evidence, which still comes
// from exactly one source per signal, and only actually resolves anything
// where two sources genuinely overlap.
func ResolveObservations(obs []observation.Observation) []observation.Observation {
	if len(obs) == 0 {
		return obs
	}

	winners := make(map[resolutionKey]observation.Observation, len(obs))
	order := make([]resolutionKey, 0, len(obs))
	for _, o := range obs {
		k := resolutionKeyOf(o)
		cur, ok := winners[k]
		if !ok {
			winners[k] = o
			order = append(order, k)
			continue
		}
		winners[k] = preferObservation(o, cur)
	}

	out := make([]observation.Observation, 0, len(order))
	for _, k := range order {
		out = append(out, winners[k])
	}
	return out
}
