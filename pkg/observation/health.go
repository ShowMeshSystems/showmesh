package observation

import "time"

// Health is the evidence-derived condition of one resource or aggregate,
// per OBSERVABILITY section 4.2. There are exactly five states; do not add
// a sixth without updating OBSERVABILITY and every switch in this package
// that is written to be exhaustive over it.
type Health string

const (
	// HealthHealthy means current evidence meets expectations.
	HealthHealthy Health = "healthy"

	// HealthDegraded means service continues outside its desired
	// operating range.
	HealthDegraded Health = "degraded"

	// HealthFailed means a required result is absent or unsafe.
	HealthFailed Health = "failed"

	// HealthUnknown means evidence is missing, stale, contradictory, or
	// insufficient.
	HealthUnknown Health = "unknown"

	// HealthSuppressed means the condition is expected under a
	// maintenance or lifecycle policy. See [AggregateHealth]'s doc
	// comment for how this package treats Suppressed in an aggregate:
	// as a policy overlay, not a severity.
	HealthSuppressed Health = "suppressed"
)

// DeriveHealth is the one place ADR-011's core rule lives: an observation
// that is not [StateCurrent] as of now can never be reported [HealthHealthy]
// — or any other Health more specific than [HealthUnknown]. whenCurrent is
// not even called unless o.StateAt(now) == StateCurrent, so a caller cannot
// accidentally smuggle a confident verdict out of stale, unknown-age, or
// absent evidence by writing a whenCurrent that is too permissive: the gate
// is structural, not a convention whenCurrent has to remember.
//
// whenCurrent is called only in the current case and receives o.Value
// (guaranteed non-nil there); it decides between [HealthHealthy],
// [HealthDegraded], and [HealthFailed] by comparing the value to whatever
// expectation the caller holds. Step 3 has very little basis for degraded
// or failed — see BUILD-PLAN and the contract's section 4 closing note —
// so most callers' whenCurrent should stay a narrow present/absent or
// equality check rather than a threshold model this package has no
// evidence to justify. whenCurrent returning [HealthUnknown] or
// [HealthSuppressed] is allowed and passed through unchanged; this helper
// only ever forces the unknown-when-not-current direction, never the
// reverse.
func DeriveHealth(o Observation, now time.Time, whenCurrent func(value any) Health) Health {
	if o.StateAt(now) != StateCurrent {
		return HealthUnknown
	}
	return whenCurrent(o.Value)
}

// AggregateMember is one child's contribution to an [AggregateHealth] call:
// its own Health, and whether it is critical to the parent resource.
type AggregateMember struct {
	Health Health

	// Critical marks a child whose HealthUnknown must block the
	// aggregate from reporting HealthHealthy, per OBSERVABILITY section
	// 4.2: "an aggregate may not report healthy when a critical child is
	// unknown." A non-critical child's HealthUnknown does not, by
	// itself, prevent a healthy aggregate — see AggregateHealth's doc
	// comment for the reasoning.
	Critical bool
}

// severityRank orders the four severities AggregateHealth compares:
// failed beats degraded beats unknown beats healthy. HealthSuppressed has
// no rank here — AggregateHealth filters it out before this is ever
// consulted; see that function's doc comment for why.
func severityRank(h Health) int {
	switch h {
	case HealthFailed:
		return 3
	case HealthDegraded:
		return 2
	case HealthUnknown:
		return 1
	case HealthHealthy:
		return 0
	default:
		// A Health this package does not know about (a bug, or a future
		// sixth state introduced without updating this function) is
		// treated as at least as bad as unknown: reporting it healthy by
		// accident would be the ADR-011 violation this whole package
		// exists to prevent, and unknown is the conservative default.
		return 1
	}
}

// AggregateHealth derives one Health for a resource with several children,
// implementing OBSERVABILITY section 4.2's rule: "an aggregate may not
// report healthy when a critical child is unknown."
//
// The precedence among the members that count towards the result is
// HealthFailed beats HealthDegraded beats HealthUnknown beats HealthHealthy
// — the aggregate takes the worst severity present. A non-critical child's
// HealthUnknown is the one exception: it does not, by itself, drag the
// aggregate down, because OBSERVABILITY's rule names "critical" child
// explicitly and a design that treated every unknown as equally blocking
// would make a single optional, unreachable sensor able to hide a healthy
// aggregate behind "unknown" forever. HealthFailed and HealthDegraded from
// a non-critical child still count at full weight: those are informative
// results (something WAS observed and found wanting), not an absence of
// evidence, and OBSERVABILITY's rule has no carve-out for them.
//
// HealthSuppressed is not a severity in this ordering; it is a policy
// overlay, and a suppressed child is excluded from the computation
// entirely rather than assigned a rank. OBSERVABILITY section 4.2 defines
// suppressed as "the condition is expected under a maintenance or
// lifecycle policy" — a statement about why the underlying evidence
// shouldn't count against this resource right now, not a claim about how
// bad that evidence is. Ranking it (worse than degraded? better than
// unknown?) would be a judgment call ADR-011 and OBSERVABILITY do not make,
// and Step 3 has no lifecycle or maintenance model yet to make it correctly
// (that is OBSERVABILITY section 11, a later phase) — so this package does
// not manufacture one.
//
// An aggregate with no members, or whose only members are all suppressed
// (or all excluded as non-critical-unknown, which amounts to the same
// thing: nothing counted), is HealthUnknown, never HealthHealthy. An
// earlier version of this function returned HealthHealthy for both cases,
// reasoning that "no evidence against it" is vacuously fine — but this
// function's entire stated job is to make ADR-011's "never healthy without
// current evidence" structural, and zero members that actually counted
// towards the result IS "no current evidence", the textbook case
// OBSERVABILITY section 4.2 and ADR-011 both name as unknown, not healthy.
// A caller that previously special-cased an empty child list itself, to
// avoid relying on this function's old vacuously-healthy behavior, gets
// the same honest answer either way now and can drop its own guard.
func AggregateHealth(members []AggregateMember) Health {
	worst := HealthHealthy
	counted := 0
	for _, m := range members {
		if m.Health == HealthSuppressed {
			continue
		}
		if m.Health == HealthUnknown && !m.Critical {
			continue
		}
		counted++
		if severityRank(m.Health) > severityRank(worst) {
			worst = m.Health
		}
	}
	if counted == 0 {
		return HealthUnknown
	}
	return worst
}
