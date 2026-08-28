// Package clock is Track I seam I1's node media clock: a [Provider]
// abstraction over three concrete PTP sources (ShowMesh-managed linuxptp,
// an externally-owned linuxptp, and an FPP 10 host's own AES67/PTP status),
// a [Tracker] that turns any Provider's raw, un-opinionated reading into
// RES-019 section 9's state machine (acquiring/locked/holdover/failed,
// holdover ageing into unsynchronized, a grandmaster change counted as a
// step), and [ReadPHC] for reading the media clock itself off a PHC device.
//
// Nothing in this package ever touches playback. A node with no configured
// provider never constructs a Tracker at all — see the agent's own wiring
// (clockreport.go) — and reports "unsynchronized" by construction, exactly
// as an unconfigured node did before this seam existed.
package clock

import (
	"context"
	"time"
)

// State is this node's reported PTP synchronization state — RES-019
// section 9's exact five-member closed vocabulary
// (docs/build/IDENTIFIER-REGISTER.md's node.clock.ptp.state reservation).
type State string

const (
	// StateUnsynchronized is what a node with no configured provider
	// reports (never produced by [Tracker] itself — see this package's
	// own doc comment), and what a Tracker reports once a holdover
	// episode exceeds its configured limit with no recovery.
	StateUnsynchronized State = "unsynchronized"

	// StateAcquiring is startup before the first lock, and what a Tracker
	// reports immediately after recovering from StateFailed (RES-019
	// section 9: "the ptp4l owner stopping or restarting it is failed
	// then acquiring").
	StateAcquiring State = "acquiring"

	// StateLocked is a fresh, usable reading — self-referentially true
	// for a node acting as its domain's own grandmaster (RoleGrandmaster)
	// exactly as much as for a node following one (RoleFollower). See
	// [Status.Mismatch] for "locked, but not to the declared domain or
	// grandmaster".
	StateLocked State = "locked"

	// StateHoldover is a temporary loss of sync following a prior lock,
	// aged by [Status.HoldoverAge] against the Tracker's configured
	// holdover limit.
	StateHoldover State = "holdover"

	// StateFailed is interface or link loss, or the owning ptp4l process
	// itself being gone (stopped or mid-restart) — RES-019 section 9:
	// ShowMesh never starts a competing ptp4l in response.
	StateFailed State = "failed"
)

// Role is which PTP role this node's port currently occupies, IEEE 1588's
// vocabulary narrowed to the four RES-019 section 5.2 names.
type Role string

const (
	RoleGrandmaster Role = "grandmaster"
	RoleFollower    Role = "follower"
	RolePassive     Role = "passive"
	RoleListening   Role = "listening"
)

// ProviderKind names which of the three concrete providers produced a
// [Status] — docs/build/IDENTIFIER-REGISTER.md's node.clock.ptp.provider
// reservation. ProviderNone is reported directly by the agent's report
// loop for a node with no node.clock configuration; no [Provider]
// implementation in this package ever returns it.
type ProviderKind string

const (
	ProviderManaged  ProviderKind = "managed"
	ProviderExternal ProviderKind = "external"
	ProviderFPP      ProviderKind = "fpp"
	ProviderNone     ProviderKind = "none"
)

// Timescale is the media clock's own epoch vocabulary (RES-019: "the media
// clock is a PTP-domain clock, never wall time; its timescale may be
// arbitrary"). TimescaleUnknown means genuinely undetermined, never a
// guessed default.
type Timescale string

const (
	TimescalePTP     Timescale = "ptp"
	TimescaleArb     Timescale = "arb"
	TimescaleUnknown Timescale = "unknown"
)

// Timestamping is which timestamping mode the owning ptp4l instance is
// running under.
type Timestamping string

const (
	TimestampingHardware Timestamping = "hardware"
	TimestampingSoftware Timestamping = "software"
)

// RawStatus is what one [Provider] reports on a single poll: its own
// data source's current reading, before [Tracker] applies RES-019 section
// 9's time-based state machine (holdover ageing, step detection, locked
// seconds). A field with a paired "Known" bool is reported false — never a
// zero-looking value — when this provider's own source did not supply it
// this poll; see each Provider's own doc comment for when that happens.
type RawStatus struct {
	// Reachable is false for RES-019 section 9's immediate-failure cases:
	// interface/link loss, or the owning ptp4l process itself gone. A
	// Tracker maps this straight to StateFailed regardless of every
	// other field below. Reason is required whenever Reachable is false.
	Reachable bool
	Reason    string

	// Locked is this provider's own claim, for THIS poll only, that its
	// underlying PTP port is usably synchronized right now: portState
	// SLAVE with a fresh master_offset, or (self-referentially)
	// portState MASTER — this node acting as the domain's own
	// grandmaster is a locked reading with RoleGrandmaster, not a
	// pending one.
	Locked bool

	Role      Role
	RoleKnown bool

	Domain      int
	DomainKnown bool

	GrandmasterIdentity string
	GMKnown             bool

	OffsetNs    int64
	OffsetKnown bool

	ClockClass      int
	ClockClassKnown bool

	// Timescale is always reported (TimescaleUnknown when undeterminable),
	// unlike the other optional fields here — RES-019 requires every
	// caller of Now() to know which epoch they are reading, so a
	// consumer must never be able to mistake "unknown" for an omitted
	// field.
	Timescale Timescale

	Timestamping      Timestamping
	TimestampingKnown bool

	// Owner names which component this provider observed running ptp4l
	// on Interface: "showmesh" for the managed provider's own supervised
	// process, or the external/FPP provider's best evidence of who else
	// owns it (e.g. "fpp" or "external (unidentified)").
	Owner string
}

// MediaTime is one reading of the media clock (RES-019: "now() in the
// media clock, with an error bound and a validity flag").
type MediaTime struct {
	// Time is the media clock's own reading. Its epoch is [Status.
	// Timescale] — never assume it is wall time.
	Time time.Time

	// ErrorBoundNs is this reading's own stated error bound in
	// nanoseconds, when the source can supply one; ErrorBoundKnown false
	// means it cannot, never a fabricated zero (a zero bound is itself a
	// meaningful claim of exactness that no source here can make).
	ErrorBoundNs    int64
	ErrorBoundKnown bool

	// Valid is false whenever this reading must not be trusted for
	// scheduling media against (the provider is not locked, or the PHC
	// read itself failed) — Reason is required whenever Valid is false.
	Valid  bool
	Reason string
}

// Interface is the minimal read surface a [Provider] must also satisfy to
// serve the node's media clock (RES-019 section 1: clock_gettime on the
// PHC-derived POSIX clock id, CLOCK_REALTIME fallback in software
// timestamping mode). Kept separate from [Provider] itself so a provider
// that cannot ever serve media time (a provider with no PHC access at
// all) can still implement Provider and report Now's failure honestly
// through the same MediaTime.Valid=false path every other failure uses,
// rather than a second, parallel error type.
type MediaClock interface {
	Now(ctx context.Context) MediaTime
}

// Provider is one of the three concrete PTP status/time sources this seam
// ships (see managed.go, external.go, fpp.go). A Provider never starts or
// changes playback; it is read-only status and time evidence.
type Provider interface {
	MediaClock

	// Kind reports which concrete implementation this is —
	// [Status.Provider]'s source.
	Kind() ProviderKind

	// Interface names the network interface this provider observes.
	Interface() string

	// Poll returns this provider's current raw reading. Called on the
	// agent's own report cadence by [Tracker.Poll]; must not block for
	// long (a shell-out to pmc or an HTTP GET, both already
	// context-bounded) and must never itself start, stop, or reconfigure
	// playback.
	Poll(ctx context.Context) RawStatus

	// Close releases whatever this provider owns (a supervised ptp4l
	// process, an idle HTTP client) — see each implementation's own
	// Close for what that means concretely. Safe to call once; the
	// managed provider's [ManagedProvider.Close] is the only
	// implementation that does real work.
	Close() error
}

// Status is this node's fully-derived clock status — RES-019 section 5.2
// and IDENTIFIER-REGISTER.md's node.clock.ptp.* signal set, one struct
// per report tick. Produced only by [Tracker.Poll] (or directly by the
// agent's report loop for a node with no provider configured at all, which
// reports [StatusUnconfigured]).
type Status struct {
	State  State
	Reason string // required whenever State != StateLocked

	Role      Role
	RoleKnown bool

	Provider  ProviderKind
	Owner     string
	Interface string

	Domain      int
	DomainKnown bool

	GrandmasterIdentity string
	GMKnown             bool

	Timescale Timescale

	OffsetNs    int64
	OffsetKnown bool

	ClockClass      int
	ClockClassKnown bool

	Timestamping      Timestamping
	TimestampingKnown bool

	// LockedSeconds is how long the current lock episode has held,
	// measured from when [Tracker] most recently transitioned into
	// StateLocked. Meaningful only while State is StateLocked or
	// StateHoldover (a holdover episode still reports how long the LOCK
	// before it lasted); LockedSecondsKnown false for every other state.
	LockedSeconds      int64
	LockedSecondsKnown bool

	// HoldoverAge is how long the current holdover episode has run,
	// meaningful only while State is StateHoldover.
	HoldoverAge      time.Duration
	HoldoverAgeKnown bool

	// LastStepAt/LastStepNs are the most recent step [Tracker] has
	// detected (a grandmaster change — RES-019 section 9) since this
	// process started, never reset by an ordinary poll. Both unset until
	// the first step is observed.
	LastStepAt    time.Time
	LastStepNs    int64
	LastStepKnown bool

	// Mismatch is RES-019 section 9's "locked, but not to the declared
	// domain or grandmaster" — operator-visible, no automatic action.
	// Only ever true while State is StateLocked.
	Mismatch       bool
	MismatchReason string

	ObservedAt time.Time
}

// StatusUnconfigured is what a node with no node.clock configuration
// reports — see this package's own doc comment. Provider is
// [ProviderNone]; every other evidence field is left at its zero/unknown
// value, matching this package's "unset means genuinely not evidence"
// convention throughout.
func StatusUnconfigured(now time.Time) Status {
	return Status{
		State:      StateUnsynchronized,
		Reason:     "no node.clock configuration is active for this node",
		Provider:   ProviderNone,
		Timescale:  TimescaleUnknown,
		ObservedAt: now,
	}
}
