// Package audiosched picks the one start instant an aligned multi-node
// audio start sends to every target (RES-019 section 6's T0).
//
// It is deliberately pure: readiness in, an instant or a stated refusal
// out, no clock of its own and no I/O. The coordinator does not hold a
// media clock, so every instant here is derived from a reading one node
// reported, and this package never invents one.
package audiosched

import (
	"fmt"
	"strings"
)

// Readiness is one target node's audio.session.prepare result, decoded
// into the terms a start instant is chosen from. It is request-scoped
// evidence: it comes from a prepare dispatched for this very start, never
// from a retained observation, because a retained media-clock reading
// would be served from whenever that node last published while looking
// exactly like a current one (pkg/audio's own doc comment on the result
// media-clock fields).
type Readiness struct {
	NodeID string

	// HoldsMediaClock is true for the node carrying the program plus LTC
	// role, the one node RES-019 section 6 takes the shared clock from.
	HoldsMediaClock bool

	// MediaClockValid false means this node could not take a usable
	// reading, and every field below it is meaningless. Reason says why.
	// Never treat an invalid reading's NowNs as an instant: a zero that
	// looks like a time is the failure this whole design is shaped to
	// avoid.
	MediaClockValid  bool
	MediaClockReason string
	MediaClockNowNs  int64

	// ErrorBoundKnown false means the bound is UNKNOWN, not zero. A
	// source that cannot state one says so rather than claiming an
	// exactness it cannot have.
	ErrorBoundKnown bool
	ErrorBoundNs    int64

	// PrerollKnown false means this node reported no preroll latency at
	// all, which is not the same as reporting zero.
	PrerollKnown bool
	PrerollMs    int64
}

// Selection is a chosen start instant and the evidence behind it.
type Selection struct {
	// ScheduledAtNs is the instant every target is started at, verbatim.
	// The same number goes to every node: one instant on one shared
	// clock is the entire point of the seam, and a per-node value would
	// mean each node started at a different time by construction.
	//
	// It is read on the media clock of ClockNodeID. Every other target
	// interprets the same digits on its OWN clock, which is sound only
	// because those clocks are disciplined to one PTP domain; that is
	// the assumption the whole seam rests on, and a node whose provider
	// is not locked is not aligned by this and keeps start-on-arrival.
	ScheduledAtNs int64

	// ClockNodeID is the node whose reading this instant is derived
	// from.
	ClockNodeID string

	// LeadNs is how far ScheduledAtNs sits past the reading it was
	// derived from: the sum of the terms below.
	LeadNs int64

	// PrerollNs is the largest preroll any target actually reported, and
	// PrerollReportedBy how many did. Zero with PrerollReportedBy zero
	// means nobody reported one, which is not the same as everybody
	// reporting zero.
	PrerollNs         int64
	PrerollReportedBy int

	DeliveryBoundNs int64
	MarginNs        int64

	// ClockErrorBoundNs is the clock-uncertainty allowance folded into
	// LeadNs, and ClockErrorBoundKnown whether there was one to fold. An
	// unknown bound contributes NOTHING rather than a zero: zero would
	// be a claim that the reading is exact, which no source here can
	// make. When it is unknown this instant carries no allowance for
	// clock uncertainty at all, and callers should say so rather than
	// imply the uncertainty was accounted for.
	ClockErrorBoundKnown bool
	ClockErrorBoundNs    int64
}

// ErrNoUsableClock is returned when no target could supply a media-clock
// reading to schedule against. It is not a transport failure and not a
// bug: a node whose clock provider is not locked reports exactly this,
// and RES-019 section 6 and the shipped node behaviour both say such a
// node keeps today's start-on-arrival. A caller starts unscheduled and
// reports this reason; it must never substitute an instant of its own.
type ErrNoUsableClock struct {
	Reason string
}

func (e *ErrNoUsableClock) Error() string { return e.Reason }

const msToNs = int64(1_000_000)

// Select picks the instant every target starts at.
//
//	T0 = ready + deliveryBound + margin (+ the clock error bound, when known)
//
// where ready is the media-clock reading of the node holding the shared
// clock, advanced by the largest preroll any target reported: the start
// must not name an instant that arrives before the slowest node can be
// playing at it.
//
// deliveryBoundMs and marginMs come from audio.settings and are both
// operator-settable guesses, not measurements.
//
// It returns *ErrNoUsableClock, never a fabricated instant, when no
// target holds a valid reading.
func Select(readiness []Readiness, deliveryBoundMs, marginMs int) (Selection, error) {
	if len(readiness) == 0 {
		return Selection{}, &ErrNoUsableClock{Reason: "no target nodes were given, so there is no clock to schedule against"}
	}
	if deliveryBoundMs < 0 || marginMs < 0 {
		return Selection{}, fmt.Errorf("audiosched: delivery bound (%d ms) and margin (%d ms) must not be negative", deliveryBoundMs, marginMs)
	}

	clock, err := pickClock(readiness)
	if err != nil {
		return Selection{}, err
	}

	sel := Selection{
		ClockNodeID:          clock.NodeID,
		DeliveryBoundNs:      int64(deliveryBoundMs) * msToNs,
		MarginNs:             int64(marginMs) * msToNs,
		ClockErrorBoundKnown: clock.ErrorBoundKnown,
	}
	if clock.ErrorBoundKnown {
		// A known bound is real uncertainty about where the reading sits,
		// so the lead time has to cover it or the instant can land in the
		// node's past. An UNKNOWN bound adds nothing here, deliberately:
		// see Selection.ClockErrorBoundNs.
		sel.ClockErrorBoundNs = clock.ErrorBoundNs
	}

	for _, r := range readiness {
		if !r.PrerollKnown {
			continue
		}
		sel.PrerollReportedBy++
		if ns := r.PrerollMs * msToNs; ns > sel.PrerollNs {
			sel.PrerollNs = ns
		}
	}

	sel.LeadNs = sel.PrerollNs + sel.DeliveryBoundNs + sel.MarginNs + sel.ClockErrorBoundNs
	sel.ScheduledAtNs = clock.MediaClockNowNs + sel.LeadNs
	if sel.ScheduledAtNs < clock.MediaClockNowNs {
		// int64 nanoseconds overflow only on an absurd reading or an
		// absurd setting, but an overflowed instant would be in the
		// node's past and refused as scheduled_start_in_past, which
		// would read as a clock fault rather than as bad input.
		return Selection{}, fmt.Errorf(
			"audiosched: start instant overflows int64 nanoseconds (reading %d plus lead %d on node %s)",
			clock.MediaClockNowNs, sel.LeadNs, clock.NodeID)
	}
	return sel, nil
}

// pickClock returns the reading the instant is derived from: the node
// holding the program plus LTC role when it has a valid one, since that
// is the node RES-019 section 6 names.
//
// A valid reading on some OTHER target is not silently substituted. The
// readings are on different nodes' clocks, and while those clocks are
// meant to be disciplined to one PTP domain, "meant to be" is the thing
// under test here; quietly deriving a show's start instant from whichever
// node happened to answer would hide exactly the misconfiguration this
// seam exists to expose.
func pickClock(readiness []Readiness) (Readiness, error) {
	var holder *Readiness
	for i := range readiness {
		if readiness[i].HoldsMediaClock {
			holder = &readiness[i]
			break
		}
	}
	if holder == nil {
		return Readiness{}, &ErrNoUsableClock{
			Reason: "no target node carries the program plus LTC role, so no target holds the shared media clock this start would be scheduled against",
		}
	}
	if !holder.MediaClockValid {
		reason := holder.MediaClockReason
		if reason == "" {
			reason = "the node reported no reason"
		}
		return Readiness{}, &ErrNoUsableClock{
			Reason: fmt.Sprintf("node %s holds the program plus LTC role but could not read its media clock: %s", holder.NodeID, reason),
		}
	}
	return *holder, nil
}

// DescribeUnscheduled is the sentence a caller reports when Select
// refused and the start went out unscheduled. Kept here so the coordinator
// and its tests share one wording for the one outcome an operator is most
// likely to misread as success.
func DescribeUnscheduled(err error) string {
	var reason string
	if err != nil {
		reason = err.Error()
	}
	if strings.TrimSpace(reason) == "" {
		reason = "no reason was reported"
	}
	return "started on arrival, NOT aligned to a shared instant: " + reason
}
