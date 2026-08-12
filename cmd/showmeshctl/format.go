package main

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"
)

// clockSkewWarnThreshold is the point past which showmeshctl warns that its
// local clock and the coordinator's serverTime disagree. This is a
// ShowMesh-chosen display threshold, not a measured value and not part of
// the wire contract (contract §3.4 requires exactly this labeling for any
// interval this program invents) — five seconds was picked as "clearly
// more than ordinary NTP jitter" with no bench evidence behind it.
const clockSkewWarnThreshold = 5 * time.Second

// newTabWriter returns a tabwriter configured the same way at every call
// site, so every table in this program lines up the same way whether it is
// rendering nodes, FPP instances, or events.
func newTabWriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
}

// stateGlyph renders an evidence state (contract §4 / §6.3) so that
// anything other than "current" is visually distinct in a plain-text
// table, without relying on colour: task spec §3 is explicit that this
// tool is run over SSH, piped, and redirected to a file, so an ANSI escape
// is not a substitute for a textual marker. "current" renders bare; every
// other state renders in shout case with the reason appended when present,
// so a piped `grep -v OK` (there is no bare OK; see below) style filter has
// something unambiguous to match against.
func stateGlyph(st string, reason *string) string {
	var label string
	switch st {
	case stateCurrent:
		return "current"
	case stateStale:
		label = "STALE"
	case stateUnknownAge:
		label = "AGE-UNKNOWN"
	case stateNotCollected:
		label = "NOT-COLLECTED"
	case stateCollectionFailed:
		label = "COLLECTION-FAILED"
	case stateUnsupported:
		label = "UNSUPPORTED"
	default:
		// An additive future state this build predates (contract §6.2).
		// Render it loudly rather than mapping it to something that looks
		// fine.
		label = "UNRECOGNIZED-STATE(" + st + ")"
	}
	if reason != nil && *reason != "" {
		return fmt.Sprintf("%s (%s)", label, *reason)
	}
	return label
}

// ageAgainst renders how long ago observedAt was, computed against
// serverTime rather than the local clock (task spec §3: "the CLI computes
// ages against serverTime, not against the local clock, so a skewed
// laptop clock does not silently misreport how fresh a show's evidence
// is"). A nil observedAt (contract §3.3: retained MQTT deliveries, and
// every absence state) is rendered as "age unknown", never as "0s ago" or
// any other value that would misreport a delivery with no valid
// observation time as fresh.
func ageAgainst(observedAt *time.Time, serverTime time.Time) string {
	if observedAt == nil {
		return "age unknown"
	}
	d := serverTime.Sub(*observedAt)
	if d < 0 {
		// observedAt after serverTime: clock skew between coordinator and
		// whatever produced the evidence, or a client clock problem
		// upstream of this CLI. Say so plainly rather than printing a
		// nonsensical negative duration.
		return fmt.Sprintf("%s in the future", roundDuration(-d))
	}
	return roundDuration(d) + " ago"
}

// roundDuration renders a duration at a human granularity (whole seconds
// under an hour, whole minutes at or beyond it) instead of Go's default
// full-precision String(), which is unreadable in a table column.
func roundDuration(d time.Duration) string {
	if d < time.Hour {
		return d.Round(time.Second).String()
	}
	return d.Round(time.Minute).String()
}

// controlPlaneColumn renders node.controlPlane per contract §3.2 and task
// spec §3: never a bare "OFFLINE" that reads as "the show is down". The
// field itself is control-plane connectivity, not liveness, and this
// function's whole job is to keep that distinction visible in a table
// column an operator will skim.
func controlPlaneColumn(cp controlPlane) string {
	switch cp.State {
	case "online":
		return "control-plane: online"
	case "offline":
		reason := "control-plane: offline (node may still be running the show)"
		if cp.Reason != nil && *cp.Reason != "" {
			reason = fmt.Sprintf("control-plane: offline — %s (node may still be running the show)", *cp.Reason)
		}
		return reason
	case "unknown":
		return "control-plane: unknown"
	default:
		return "control-plane: " + cp.State + " (unrecognized)"
	}
}

// clockSkewWarning returns a non-empty warning line when the local clock
// and the coordinator's serverTime disagree by more than
// clockSkewWarnThreshold, and "" otherwise. Callers print it to stderr once
// per invocation (or once per snapshot refetch, for watch) rather than
// spamming it per row.
func clockSkewWarning(serverTime, localNow time.Time) string {
	skew := localNow.Sub(serverTime)
	if skew < 0 {
		skew = -skew
	}
	if skew <= clockSkewWarnThreshold {
		return ""
	}
	return fmt.Sprintf(
		"warning: local clock differs from coordinator serverTime by %s; ages below are computed against serverTime, not the local clock",
		roundDuration(skew),
	)
}

// stringOrDash renders an optional string field for a text table: "-" when
// nil, matching contract's null (§6.10: label/platform/agentVersion/etc.
// are null when no hello has ever been observed), never an empty cell that
// could be misread as an empty string value.
func stringOrDash(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}

// emptyOrDash renders a plain (non-pointer) string field for a text table
// as "-" when empty. Unlike stringOrDash, this is for fields the API
// declares always-present (audit entries: openapi.yaml marks every
// AuditEntry field required with no null variant) but that can still be
// the empty string on the wire (e.g. clientAddr when no trusted proxy is
// configured — ADR-024 decision 11's "an audit entry records who and, by
// default, cannot record from where"). "-" here means "empty value", not
// "absent field" — there is no absent-vs-empty distinction to preserve for
// a field that is never a pointer.
func emptyOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// scopesStateGlyph renders SessionResponse.scopesState (ADR-024 decision
// 12). "unknown" must read as loudly distinct as every other
// non-"current" state this program renders (stateGlyph, healthGlyph):
// decision 12 requires a client treat "unknown" exactly like an empty
// scope list, never as permissive, and a bare "unknown" in a column an
// operator skims would not carry that warning on its own.
func scopesStateGlyph(s string) string {
	switch s {
	case "current":
		return "current"
	case "not_applicable":
		return "not-applicable (not authenticated)"
	case "unknown":
		return "UNKNOWN — treat as no scopes, never as permissive (ADR-024 decision 12)"
	default:
		return "UNRECOGNIZED-STATE(" + s + ")"
	}
}

// auditKindGlyph renders auditEntry.kind (ADR-024 decision 11's five
// values). "replay" and "auth_failure" are made visually loud for the same
// reason severityGlyph makes "warning"/"critical" loud: decision 11 is
// explicit that a replay "is precisely the case an investigator wants to
// see... it means the operator did not get their response," so it must not
// blend into a column of ordinary dispatch/outcome rows.
func auditKindGlyph(k string) string {
	switch k {
	case "dispatch":
		return "dispatch"
	case "outcome":
		return "outcome"
	case "admin":
		return "admin"
	case "replay":
		return "REPLAY"
	case "auth_failure":
		return "AUTH-FAILURE"
	default:
		return "UNRECOGNIZED(" + k + ")"
	}
}

// timeOrDash renders an optional timestamp for a text table.
func timeOrDash(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Format(time.RFC3339)
}
