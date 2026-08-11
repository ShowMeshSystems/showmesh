package inventory

import (
	"fmt"
	"os"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// HeartbeatInterval and StalenessWindow are SHOWMESH HYPOTHESES, NOT
// DERIVED FROM ADR-008 OR FROM ANY MEASUREMENT. Per the Step 2 round 2
// shared design contract, they are labeled as such here the way
// pkg/multisync/timeline.go separates FPP-derived thresholds from
// ShowMesh-chosen ones, and are expected to change once RES-009 failure
// testing produces real evidence.
//
// StalenessWindow is the coordinator's own policy, not an assumption that
// it equals the agent's HeartbeatInterval: this package never hardcodes
// "the agent publishes every HeartbeatInterval, so anything older than
// that is stale" — it only ever compares a stored ObservedAt against
// StalenessWindow, which is deliberately set wider than one heartbeat
// (three missed heartbeats) so a single delayed or dropped delivery does
// not flip a live node to unknown.
const (
	// HeartbeatInterval is how often a node agent is expected to publish a
	// health heartbeat. It exists here as documentation of the assumption
	// StalenessWindow is sized against; this package does not use it in any
	// computation.
	HeartbeatInterval = 10 * time.Second
)

// envStalenessWindowOverride is a TEST-SUPPORT-ONLY environment variable
// (Step 2 round 2 Task E) that lets the integration test harness in
// /test/integration compress StalenessWindow, so a test does not have to
// wait out the production 30 second window. It is read exactly once, at
// package initialization (see StalenessWindow below), which means it can
// only take effect if it is already set in the process environment before
// this package is first used — e.g. by the harness's `make
// test-integration` target exporting it before running `go test` — never
// by calling code after startup. It must never become a documented
// production tuning surface: unset in every real deployment, it has no
// effect and StalenessWindow is exactly the 30 second value below.
const envStalenessWindowOverride = "SHOWMESH_TEST_STALENESS_WINDOW"

// StalenessWindow is how old a live health observation may be before this
// package stops treating it as proof of life. Three missed heartbeats at
// HeartbeatInterval.
//
// A package-level var, not a const, ONLY so [envStalenessWindowOverride]
// can override it for integration tests; see that constant's doc comment
// for why this must not be read as an invitation to change it any other
// way. deriveLiveness and every caller in this package continue to read it
// as if it were the fixed constant it was before Task E.
var StalenessWindow = resolveStalenessWindow()

// resolveStalenessWindow returns the envStalenessWindowOverride value when
// it is set to a valid positive duration, and the production default
// otherwise. Invalid or non-positive overrides are silently ignored in
// favor of the default rather than failing package initialization, since a
// malformed test-only environment variable must never be able to crash
// coordinator startup.
func resolveStalenessWindow() time.Duration {
	const def = 30 * time.Second
	if raw := os.Getenv(envStalenessWindowOverride); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return def
}

// Liveness is a node's derived online/offline/unknown verdict. It is
// always computed fresh from stored evidence plus the current time (see
// deriveLiveness) — never stored itself; see the store package doc
// comment for why.
type Liveness string

const (
	// LivenessOnline means the node's last-will evidence says online AND a
	// live health heartbeat has been observed within StalenessWindow.
	LivenessOnline Liveness = "online"

	// LivenessOffline means the node's last-will evidence says offline, and
	// nothing else currently contradicts it. This is unconditional on
	// freshness — a stale "offline" is still a true "not proven online
	// right now" — but it IS conditional on there being no live,
	// StalenessWindow-fresh heartbeat contradicting it; see
	// deriveLiveness's doc comment for why a disagreement of that specific
	// kind demotes to LivenessUnknown instead.
	LivenessOffline Liveness = "offline"

	// LivenessUnknown covers every other case: no evidence yet, only
	// retained evidence with unknown age, evidence that has aged past
	// StalenessWindow, or last-will and health evidence that directly
	// contradict each other. Per ADR-011, stale, insufficient, OR
	// CONTRADICTORY evidence must never be reported as a confident
	// classification (healthy or otherwise) — this is the ADR-011 default,
	// not a soft LivenessOnline, and must never be treated as healthy by
	// any downstream consumer.
	LivenessUnknown Liveness = "unknown"
)

// deriveLiveness computes a node's Liveness and a short human-readable
// reason from its stored evidence, per the Step 2 round 2 shared design
// contract's "Liveness derivation" section:
//
//   - offline: the node's LWT evidence says online:false, AND there is no
//     health evidence that DISAGREES with it (see "DISAGREEMENT IS ABOUT
//     ORDER, NOT FRESHNESS" below). Absent a disagreement, this check wins
//     unconditionally on freshness — before health is even looked at —
//     because the shared contract treats an offline declaration as
//     trustworthy in both directions (a broker-fired Will on an unclean
//     disconnect, or the agent's own graceful retained publish before a
//     clean one) regardless of how old that evidence is. A stale
//     "offline" with no disagreeing evidence is still a true "not proven
//     online right now"; the risk this package guards against is the
//     opposite direction (stale evidence reading as healthy), which an
//     undisputed offline can never produce.
//
//   - online: LWT evidence says online:true AND a live (non-retained)
//     health heartbeat has been observed within StalenessWindow. Both
//     conditions are required; see "Note why LWT alone is insufficient" in
//     the shared contract — an LWT online:true observation can itself be
//     stale retained evidence (e.g. immediately after a coordinator
//     restart), so a fresh heartbeat is the thing that actually proves
//     life.
//
//   - unknown: every other case, explicitly including: no LWT evidence at
//     all (e.g. only a retained hello has ever been seen — hello is a
//     capability advertisement, not proof of presence); LWT says online
//     but no health evidence exists yet; LWT says online but the only
//     health evidence is a retained delivery with unknown age; LWT says
//     online but the live health evidence has aged past StalenessWindow;
//     and the disagreement case below.
//
// DISAGREEMENT IS ABOUT ORDER, NOT FRESHNESS. An earlier version of this
// function treated "last-will offline plus a StalenessWindow-fresh live
// heartbeat" as an automatic disagreement. That is wrong, and it broke the
// exact scenario the disagreement check exists to protect: a clean
// shutdown. An agent's last deliberate act before disconnecting is to
// publish its own retained "online: false" (see ADR-008 and the shared
// contract's "Clean shutdown" section) — and the heartbeat immediately
// before that shutdown is, by construction, only seconds old and therefore
// still inside the staleness window. Treating that as a disagreement holds
// the node at LivenessUnknown for up to the full staleness window after it
// announced its own death in exactly the way it was designed to, which
// defeats the entire point of publishing that announcement.
//
// A heartbeat that arrived BEFORE an offline last will is not conflicting
// evidence: it is history. "I was alive, then I said I'm stopping" is a
// sequence of events, not two things being true at once. A heartbeat that
// arrives AFTER an offline last will is different in kind: the node said it
// was going offline and then kept proving it was alive, which is the
// genuine zombie case — a will fired (or was replayed) and the node either
// never received it or came back without republishing "online: true". That
// is the only case this function must call a disagreement.
//
// So the rule turns on which piece of evidence is NEWER, using each
// evidence's own ObservedAt (nil for a retained delivery — age unknown, see
// classify in inventory.go — never the coordinator's current-record
// bookkeeping):
//
//   - Both the last will and the health heartbeat have a known observation
//     time (both were live deliveries): disagreement only if the health
//     observation is STRICTLY newer than the last-will observation AND
//     still within StalenessWindow of now. An offline last will observed
//     at or after the last heartbeat — the clean-shutdown and unclean-kill
//     shapes — is not a disagreement; it wins immediately, however fresh
//     the earlier heartbeat still looks.
//   - The last will's observation time is nil (a retained replay — e.g.
//     what a just-restarted coordinator sees on subscribe) AND there is a
//     fresh LIVE health observation: this counts as a disagreement. A
//     retained delivery's age is unknown, so it can never be proven newer
//     than a live heartbeat that is, by construction, happening right now;
//     this is the "node came back and has not yet republished its online
//     will" case, and — like every other LivenessUnknown here — it is
//     expected to self-correct within about one heartbeat interval once
//     the agent republishes its retained online will on reconnect.
//   - Every other combination — no health evidence, a retained (unknown-
//     age) health delivery, or a live health observation that is stale or
//     not newer than the last will — is not a disagreement: LivenessOffline.
//
// A RETAINED heartbeat never creates a disagreement on its own (it falls
// into "no health evidence" above once its unknown age is accounted for):
// that is exactly the retained-freshness trap the rest of this package
// guards against, dressed up as "but the heartbeat looks recent".
//
// Reporting a confident LivenessOffline during a genuine disagreement would
// present a contradiction as a settled fact, which is exactly what
// ADR-011's "insufficient or contradictory evidence becomes unknown, not a
// confident classification" exists to prevent, so this function reports
// LivenessUnknown with a reason that says plainly that the two topics
// disagree, rather than a generic staleness-shaped message — legibility
// matters here because a genuine disagreement is itself an anomaly worth
// someone's attention (message loss on one topic, or an agent heartbeating
// without republishing its online last will after a will fired). Do not
// "fix" a transient occurrence of this by preferring one topic over the
// other — that is precisely the shortcut this function now avoids.
func deriveLiveness(rec store.NodeRecord, now time.Time) (Liveness, string) {
	if rec.LWT != nil && !rec.LWT.Online {
		if reason, disagrees := offlineDisagreesWithHealth(rec, now); disagrees {
			return LivenessUnknown, reason
		}
		return LivenessOffline, "last-will evidence reports offline"
	}
	if rec.LWT == nil {
		return LivenessUnknown, "no last-will evidence of an online state"
	}
	// rec.LWT != nil && rec.LWT.Online == true from here on.

	if rec.Health == nil {
		return LivenessUnknown, "last-will reports online but no health heartbeat has been observed yet"
	}
	if rec.Health.ObservedAt == nil {
		return LivenessUnknown, "last-will reports online but the only health evidence is a retained delivery of unknown age"
	}

	age := now.Sub(*rec.Health.ObservedAt)
	if age > StalenessWindow {
		return LivenessUnknown, fmt.Sprintf("health evidence is %s old, past the %s staleness window", age.Round(time.Second), StalenessWindow)
	}

	return LivenessOnline, ""
}

// offlineDisagreesWithHealth reports whether rec's health evidence
// disagrees with an offline last will, per deriveLiveness's "DISAGREEMENT
// IS ABOUT ORDER, NOT FRESHNESS" doc section: a disagreement requires a
// health observation that is not older than the last-will observation, not
// merely one that happens to still be fresh. Callers must only invoke this
// when rec.LWT != nil && !rec.LWT.Online.
func offlineDisagreesWithHealth(rec store.NodeRecord, now time.Time) (reason string, disagrees bool) {
	if rec.Health == nil || rec.Health.ObservedAt == nil {
		// No health evidence, or only a retained (unknown-age) delivery:
		// nothing current enough to disagree with an offline last will.
		return "", false
	}

	healthAt := *rec.Health.ObservedAt
	age := now.Sub(healthAt)
	if age > StalenessWindow {
		// A stale live heartbeat is not current evidence either, ordering
		// aside.
		return "", false
	}

	if rec.LWT.ObservedAt == nil {
		// The last will is a retained replay: its age is unknown, so it can
		// never be proven newer than a heartbeat that is, by construction,
		// live right now. See deriveLiveness's doc comment for why this is
		// the "node came back and has not republished its online will"
		// case.
		return fmt.Sprintf(
			"last-will reports offline (retained delivery, age unknown) but a live health heartbeat %s old disagrees; treating as unknown rather than trusting either topic alone",
			age.Round(time.Second)), true
	}

	if !healthAt.After(*rec.LWT.ObservedAt) {
		// The offline last will is newer than, or exactly as old as, the
		// heartbeat: this is history, not a disagreement — e.g. a clean
		// shutdown's deliberate final "online: false" publish arriving
		// after the last heartbeat it sent while still running. See
		// deriveLiveness's doc comment.
		return "", false
	}

	return fmt.Sprintf(
		"last-will reports offline but a live health heartbeat %s old, observed after the last will, disagrees; treating as unknown rather than trusting either topic alone",
		age.Round(time.Second)), true
}
