package macro

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// recordPriorFailures is STEP-9-SPEC.md section 8.3 path 2's coordinator-
// side landing point: "The plugin buffers degraded outcomes and attaches
// them to the next successful authenticated call, in an additive
// priorFailures array on the run request body." This method is where that
// array is actually recorded, as one or more operator-visible events.
//
// Coalesced, never appended one per attempt (macro_seam.go's own doc
// comment on [api.MacroSubmitRequest.PriorFailures]: "an unbounded write
// on a failure path is an eviction primitive aimed at your own evidence,
// and events are under retention"). Grouped by [api.MacroPriorFailure.Class]
// (refused/rejected/unreachable are semantically distinct — folding them
// into one event would answer "something failed" without answering which
// of the three, which is the exact ambiguity STEP-9-SPEC.md section 8.2
// exists to remove) and written as at most one event per class present in
// this call, each carrying a count and a first-seen/last-seen time rather
// than one row per buffered attempt.
//
// Best-effort: a failure to write one of these events is logged, never
// returned — this is reporting about a caller's own past trouble, and
// failing the run submission itself over it would be a second failure on
// top of the first, for a call that has already been accepted.
// group is one class's coalesced window across one submission.
type group struct {
	count     int
	firstSeen time.Time
	lastSeen  time.Time
}

func (e *Executor) recordPriorFailures(ctx context.Context, req api.MacroSubmitRequest) {
	if len(req.PriorFailures) == 0 && req.PriorFailuresDropped == 0 {
		return
	}

	failures := req.PriorFailures
	if len(failures) > e.maxPriorFailuresPerSubmit {
		e.logWarn("macro run submission carried more priorFailures than this coordinator will act on; excess entries ignored",
			"macroId", req.MacroObjectID, "received", len(failures), "limit", e.maxPriorFailuresPerSubmit)
		failures = failures[:e.maxPriorFailuresPerSubmit]
	}

	// The class is caller-supplied, and it is the coalescing key, so an
	// unvalidated one turns this bounded write into an unbounded one: a
	// caller sending 50 entries with 50 distinct class strings would
	// write 50 events per submission instead of at most three. Events
	// are under retention, so that is an eviction primitive aimed at
	// this coordinator's own evidence, which is the shape LESSONS.md
	// records. Anything outside STEP-9-SPEC.md section 8.2's three
	// classes is folded into "unclassified" rather than dropped: what a
	// caller reported still happened, and the count still matters.
	byClass := make(map[string]*group)
	for _, f := range failures {
		class := f.Class
		switch class {
		case priorFailureClassRefused, priorFailureClassRejected, priorFailureClassUnreachable:
		default:
			class = priorFailureClassUnclassified
		}
		g, ok := byClass[class]
		if !ok {
			g = &group{count: 0, firstSeen: f.At, lastSeen: f.At}
			byClass[class] = g
		}
		g.count++
		if f.At.Before(g.firstSeen) {
			g.firstSeen = f.At
		}
		if f.At.After(g.lastSeen) {
			g.lastSeen = f.At
		}
	}

	// A caller can legitimately report only that it dropped entries,
	// with none surviving to send. Writing nothing for that case would
	// silently truncate a truncation report, which is the smaller
	// version of the lie the buffer exists to prevent, so it gets its
	// own event rather than falling out of an empty grouping loop.
	if len(byClass) == 0 && req.PriorFailuresDropped > 0 {
		e.appendPriorFailureEvent(ctx, req.MacroObjectID, priorFailureClassDroppedOnly, nil, req.PriorFailuresDropped)
		return
	}

	// droppedByCaller is reported once, on one event, rather than copied
	// onto every class: it is a property of the caller's buffer, not of
	// any one class, and repeating it three times would read as three
	// separate losses.
	dropped := req.PriorFailuresDropped
	for class, g := range byClass {
		e.appendPriorFailureEvent(ctx, req.MacroObjectID, class, g, dropped)
		dropped = 0
	}
}

// Prior-failure classes, STEP-9-SPEC.md section 8.2's three plus two this
// package needs and the wire does not define: unclassified, for a class
// string a caller sent that is none of the three, and dropped-only, for a
// report that carries nothing but a count of what the caller discarded.
const (
	priorFailureClassRefused      = "refused"
	priorFailureClassRejected     = "rejected"
	priorFailureClassUnreachable  = "unreachable"
	priorFailureClassUnclassified = "unclassified"
	priorFailureClassDroppedOnly  = "dropped-only"
)

// appendPriorFailureEvent writes one coalesced event. g is nil for the
// dropped-only case, which has no attempts to count and no window to
// report, only the number the caller discarded.
func (e *Executor) appendPriorFailureEvent(ctx context.Context, macroObjectID, class string, g *group, dropped int) {
	detail := map[string]any{"class": class, "droppedByCaller": dropped}
	count := 0
	if g != nil {
		count = g.count
		detail["count"] = count
		detail["firstSeen"] = g.firstSeen.UTC().Format(time.RFC3339Nano)
		detail["lastSeen"] = g.lastSeen.UTC().Format(time.RFC3339Nano)
	}
	details, _ := json.Marshal(detail)
	if _, err := e.store.AppendEvent(ctx, store.EventRecord{
		Source:   "macro-executor",
		Resource: observation.ResourceRef{Kind: observation.ResourceKind("macro"), ID: macroObjectID},
		Category: "macro.run.prior_failure",
		Severity: "warning",
		Summary:  macroPriorFailureSummary(macroObjectID, class, count),
		Details:  details,
	}); err != nil {
		e.logWarn("failed to record buffered prior-failure event", "macroId", macroObjectID, "class", class, "error", err)
	}
}

func macroPriorFailureSummary(macroObjectID, class string, count int) string {
	switch class {
	case priorFailureClassRefused:
		return "a caller reported " + pluralAttempts(count) + " refused while trying to run this macro, buffered until it could reach this coordinator"
	case priorFailureClassRejected:
		return "a caller reported " + pluralAttempts(count) + " rejected while trying to run this macro, buffered until it could reach this coordinator"
	case priorFailureClassUnreachable:
		return "a caller reported " + pluralAttempts(count) + " unable to reach this coordinator while trying to run this macro"
	default:
		return "a caller reported " + pluralAttempts(count) + " that did not succeed while trying to run this macro"
	}
}

func pluralAttempts(n int) string {
	if n == 1 {
		return "1 attempt"
	}
	return strconv.Itoa(n) + " attempts"
}
