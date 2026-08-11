package observation

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// SignalID is a namespaced, dot-separated signal identifier, e.g.
// "fpp.multisync.enabled", "node.agent.uptime". Per the Step 3 contract
// section 7, signal IDs are lowercase and namespaced by resource kind
// (fpp.*, node.*, coordinator.*).
//
// This package DOES enforce that syntax, via [ValidateSignalID], called
// from [Observation.Validate]. It did not, originally — the doc comment
// this replaces argued that "no vocabulary or collector is fixed yet at
// this layer" — and that absence is exactly why the Step 3 parallel build
// wave shipped a real divergence: the FPP collector emitted
// "fpp.uptimeSeconds" (camelCase) while the API layer's test fixtures
// independently guessed "fpp.status.uptime_seconds" for a differently-named
// signal, and nothing caught it until the wiring pass read both sides by
// hand. A build-time failure would have caught it in minutes. See
// [ValidateSignalID] for the exact rule.
type SignalID string

// signalIDSegmentPattern is one dot-separated segment of a [SignalID]:
// lowercase ASCII letters, digits, and underscores, at least one
// character. Splitting on "." and requiring every resulting piece to match
// this is what rejects an empty segment (so also a leading dot, a trailing
// dot, and a doubled dot) without a more elaborate regexp.
//
// Underscores are permitted even though contract section 7's prose only
// says "lowercase, dot-separated" with no explicit mention of them: this
// codebase's own node-evidence signals (e.g. "node.control_plane.last_will",
// in internal/coordinator/api/mapping.go) already use them, predating this
// validator, and forbidding them here would invent a stricter rule than the
// contract actually states rather than enforcing the one it does.
var signalIDSegmentPattern = regexp.MustCompile(`^[a-z0-9_]+$`)

// ErrInvalidSignalID is returned by [ValidateSignalID] (and, wrapped, by
// [Observation.Validate]) when id does not satisfy contract section 7's
// syntax rule: lowercase letters, digits, and underscores, in one or more
// dot-separated non-empty segments.
var ErrInvalidSignalID = errors.New("observation: SignalID does not satisfy the lowercase dot-separated syntax rule")

// ValidateSignalID enforces contract section 7's SignalID syntax rule. It
// is called from [Observation.Validate] (only once id is already known
// non-empty — see [ErrObservationMissingSignal]) but is exported so a
// collector can reject a malformed signal name of its own choosing before
// ever building an Observation with it, e.g. at package initialization
// against every constant it defines.
func ValidateSignalID(id SignalID) error {
	for _, segment := range strings.Split(string(id), ".") {
		if !signalIDSegmentPattern.MatchString(segment) {
			return fmt.Errorf("%w: %q", ErrInvalidSignalID, id)
		}
	}
	return nil
}

// ResourceKind is the class of thing an observation is about.
type ResourceKind string

const (
	ResourceNode        ResourceKind = "node"
	ResourceFPP         ResourceKind = "fpp"
	ResourceCoordinator ResourceKind = "coordinator"
)

// ResourceRef identifies the subject of an observation.
type ResourceRef struct {
	Kind ResourceKind
	ID   string
}

// Quality distinguishes direct device telemetry from derived, inferred, and
// operator-supplied values, per OBSERVABILITY section 4.1.
type Quality string

const (
	QualityDirect   Quality = "direct"
	QualityDerived  Quality = "derived"
	QualityInferred Quality = "inferred"
	QualityOperator Quality = "operator"
)

// State is the evidence state of one observation. It is the vocabulary
// BUILD-PLAN Step 3 requires when it says the API must distinguish "not
// supported" from "not collected" from "collection failed", extended with
// the two freshness cases ADR-011 requires. See the package doc comment
// for why there are exactly six members.
type State string

const (
	// StateCurrent means a value was obtained, its observation time is
	// known, and it has not aged past ValidFor.
	StateCurrent State = "current"

	// StateStale means a value was obtained and has aged past ValidFor.
	// The value is still carried for context and must never be rendered
	// as healthy.
	StateStale State = "stale"

	// StateUnknownAge means a value exists but its observation time is
	// unknown. The retained-MQTT case (contract section 3.3). Never
	// healthy.
	StateUnknownAge State = "unknown_age"

	// StateNotCollected means no attempt has been made. The collector is
	// not configured, is disabled, or has not completed its first poll.
	StateNotCollected State = "not_collected"

	// StateCollectionFailed means an attempt was made and failed. Reason
	// carries the failure class.
	StateCollectionFailed State = "collection_failed"

	// StateUnsupported means this source cannot provide this signal at
	// all.
	StateUnsupported State = "unsupported"
)

// derivedOnlyStates are the [State] values [Observation.StateAt] computes
// from the *absence* of a fabricated timestamp or an aged-out ValidFor.
// They describe evidence that DOES have a value; nothing may construct an
// Observation with one of these as its stored Absence, because Absence
// exists to record why there is NO value (see [Observation.Absence] and
// [Observation.Validate]).
var derivedOnlyStates = map[State]bool{
	StateCurrent:    true,
	StateStale:      true,
	StateUnknownAge: true,
}

// Observation is one timestamped piece of evidence about one resource. See
// the package doc comment for why ObservedAt is a pointer and what nil
// means, and [Observation.Validate] for the invariants a hand-built value
// must satisfy.
//
// Do not construct one by hand outside a test: use [Measured],
// [MeasuredUnknownAge], [Unsupported], [NotCollected], or
// [CollectionFailed]. Those constructors are what make the invariants
// unbreakable in practice; Validate is the backstop for the value that
// still gets hand-built anyway, because the struct is exported.
type Observation struct {
	Resource ResourceRef
	Signal   SignalID

	// Value is nil when no value was obtained. When non-nil it is one of
	// bool, string, int64, float64 — validated, so an arbitrary struct
	// can never reach the wire through this field.
	Value any
	Unit  string

	// ObservedAt is when the measured condition was true, according to
	// the best evidence available. It is nil when unknown — a retained
	// MQTT delivery, or any absence state. NEVER default it to
	// CollectedAt. See the package doc comment.
	ObservedAt *time.Time

	// CollectedAt is when this observation was recorded by the collector.
	// Always set. It is bookkeeping, not evidence of the subject's
	// state.
	CollectedAt time.Time

	// Source names the collector that produced this, e.g. "fpp-rest",
	// "mqtt-inventory".
	Source  string
	Quality Quality

	// ValidFor is how long a value stays current after ObservedAt. Zero
	// means the value does not expire on its own.
	ValidFor time.Duration

	// Absence is the stored State for an observation carrying no value.
	// It is empty when Value != nil, and one of StateNotCollected,
	// StateCollectionFailed or StateUnsupported otherwise.
	Absence State

	// Reason is a short human-readable explanation. Required whenever
	// there is no current value.
	Reason string
}

// StateAt derives this observation's State at now, against the caller's
// clock:
//
//	Absence != ""             -> Absence
//	Value == nil              -> StateNotCollected (defensive; Validate rejects)
//	ObservedAt == nil         -> StateUnknownAge
//	ValidFor > 0 && aged out  -> StateStale
//	otherwise                 -> StateCurrent
//
// StateCurrent is unreachable unless both Value is non-nil and ObservedAt
// is non-nil: every other branch returns first.
func (o Observation) StateAt(now time.Time) State {
	if o.Absence != "" {
		return o.Absence
	}
	if o.Value == nil {
		// Defensive: Validate rejects an Observation with no value and no
		// Absence, so a value built through the constructors never
		// reaches this branch. A hand-built struct that skipped Validate
		// still gets an honest answer rather than a panic.
		return StateNotCollected
	}
	if o.ObservedAt == nil {
		return StateUnknownAge
	}
	if o.ValidFor > 0 && now.Sub(*o.ObservedAt) > o.ValidFor {
		return StateStale
	}
	return StateCurrent
}

// Option adjusts a field on an Observation under construction that is not
// part of a constructor's required arguments: CollectedAt, Source,
// Quality, Unit, and ValidFor all need to be settable, but only ObservedAt
// (or its deliberate absence) is load-bearing enough to be a required
// parameter — see [Measured] and [MeasuredUnknownAge].
type Option func(*Observation)

// WithCollectedAt overrides the default CollectedAt (time.Now() at
// construction) with an explicit value. Intended for tests and for a
// collector replaying evidence it collected earlier and is only recording
// now.
func WithCollectedAt(t time.Time) Option {
	return func(o *Observation) { o.CollectedAt = t }
}

// WithSource sets the name of the collector that produced this
// observation, e.g. "fpp-rest", "mqtt-inventory".
func WithSource(source string) Option {
	return func(o *Observation) { o.Source = source }
}

// WithQuality sets Quality. Constructors default to QualityDirect, the
// most common case for a collector reading device telemetry; override it
// for a derived, inferred, or operator-supplied value.
func WithQuality(q Quality) Option {
	return func(o *Observation) { o.Quality = q }
}

// WithUnit sets Unit, e.g. "seconds", "volts". Empty (the default) means
// unitless or not applicable.
func WithUnit(unit string) Option {
	return func(o *Observation) { o.Unit = unit }
}

// WithValidFor sets ValidFor, how long the value stays current after
// ObservedAt. The zero value (the default) means the value never expires
// on its own.
func WithValidFor(d time.Duration) Option {
	return func(o *Observation) { o.ValidFor = d }
}

func newObservation(res ResourceRef, sig SignalID, opts []Option) Observation {
	o := Observation{
		Resource:    res,
		Signal:      sig,
		CollectedAt: time.Now(),
		Quality:     QualityDirect,
	}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// Measured builds an Observation carrying value, whose observation time is
// known to be observedAt. This is the common case: a collector read a
// value and knows when the condition it describes was true.
//
// It returns an error rather than a partially-built Observation when value
// is not one of the types [Observation.Validate] accepts, or when res or
// sig fail their own required-field checks — see Validate for the full
// rule set. The returned Observation, when err is nil, already satisfies
// Validate.
func Measured(res ResourceRef, sig SignalID, value any, observedAt time.Time, opts ...Option) (Observation, error) {
	o := newObservation(res, sig, opts)
	o.Value = value
	o.ObservedAt = &observedAt
	if err := o.Validate(); err != nil {
		return Observation{}, err
	}
	return o, nil
}

// MeasuredUnknownAge builds an Observation carrying value whose observation
// time is genuinely unknown — the retained-MQTT case (contract section
// 3.3) and the whole reason ObservedAt is a pointer. Never call this to
// paper over an observation time you could have supplied; use [Measured]
// whenever the observation time is known, including "known to be roughly
// now" for a live poll.
func MeasuredUnknownAge(res ResourceRef, sig SignalID, value any, opts ...Option) (Observation, error) {
	o := newObservation(res, sig, opts)
	o.Value = value
	o.ObservedAt = nil
	if err := o.Validate(); err != nil {
		return Observation{}, err
	}
	return o, nil
}

func absenceObservation(res ResourceRef, sig SignalID, absence State, reason string, opts []Option) (Observation, error) {
	o := newObservation(res, sig, opts)
	o.Absence = absence
	o.Reason = reason
	if err := o.Validate(); err != nil {
		return Observation{}, err
	}
	return o, nil
}

// Unsupported builds an absence Observation recording that this source
// cannot provide this signal at all — e.g. an FPP instance too old for a
// REST field the collector needs. reason is required and becomes Reason.
func Unsupported(res ResourceRef, sig SignalID, reason string, opts ...Option) (Observation, error) {
	return absenceObservation(res, sig, StateUnsupported, reason, opts)
}

// NotCollected builds an absence Observation recording that no attempt has
// been made yet: the collector is not configured, is disabled, or has not
// completed its first poll. reason is required and becomes Reason.
func NotCollected(res ResourceRef, sig SignalID, reason string, opts ...Option) (Observation, error) {
	return absenceObservation(res, sig, StateNotCollected, reason, opts)
}

// CollectionFailed builds an absence Observation recording that an attempt
// was made and failed. reason is required, becomes Reason, and should
// carry the failure class (e.g. "http 503", "dial tcp: connection
// refused") rather than a full error dump.
func CollectionFailed(res ResourceRef, sig SignalID, reason string, opts ...Option) (Observation, error) {
	return absenceObservation(res, sig, StateCollectionFailed, reason, opts)
}

// Sentinel errors wrapped by [Observation.Validate]. Each is returned
// (wrapped with the specific value that violated it) rather than a bare
// string, so a caller can errors.Is against the specific rule that failed.
var (
	// ErrObservationValueAndAbsence is returned when both Value and
	// Absence are set: an observation cannot simultaneously carry
	// evidence and record why there is none.
	ErrObservationValueAndAbsence = errors.New("observation: has a value and a non-empty Absence")

	// ErrObservationNoValueOrAbsence is returned when neither Value nor
	// Absence is set: an observation with no value must say why.
	ErrObservationNoValueOrAbsence = errors.New("observation: has no value and no Absence")

	// ErrObservationInvalidValueType is returned when Value is set but is
	// not one of bool, string, int64, float64.
	ErrObservationInvalidValueType = errors.New("observation: Value is not bool, string, int64, or float64")

	// ErrObservationMissingCollectedAt is returned when CollectedAt is
	// the zero time.
	ErrObservationMissingCollectedAt = errors.New("observation: CollectedAt is zero")

	// ErrObservationMissingReason is returned when Absence is set but
	// Reason is empty.
	ErrObservationMissingReason = errors.New("observation: Absence is set but Reason is empty")

	// ErrObservationDerivedAbsence is returned when Absence is set to a
	// state [Observation.StateAt] only ever derives (current, stale,
	// unknown_age) rather than one of the three states that record an
	// actual absence of a value.
	ErrObservationDerivedAbsence = errors.New("observation: Absence is set to a derived-only State")

	// ErrObservationMissingSignal is returned when Signal is empty.
	ErrObservationMissingSignal = errors.New("observation: Signal is empty")

	// ErrObservationMissingResourceID is returned when Resource.ID is
	// empty.
	ErrObservationMissingResourceID = errors.New("observation: Resource.ID is empty")

	// ErrObservationNegativeValidFor is returned when ValidFor is
	// negative.
	ErrObservationNegativeValidFor = errors.New("observation: ValidFor is negative")
)

// Validate reports whether o satisfies the invariants this package exists
// to enforce. The constructors ([Measured], [MeasuredUnknownAge],
// [Unsupported], [NotCollected], [CollectionFailed]) already call this and
// never return a value that fails it; Validate is exported because
// Observation is an exported struct and any caller can build one by hand,
// deliberately or by an incomplete refactor.
//
// Validate rejects:
//   - a Value together with a non-empty Absence ([ErrObservationValueAndAbsence]);
//   - no Value together with an empty Absence ([ErrObservationNoValueOrAbsence]);
//   - a Value that is not bool, string, int64, or float64 ([ErrObservationInvalidValueType]);
//   - a zero CollectedAt ([ErrObservationMissingCollectedAt]);
//   - an empty Reason on any absence ([ErrObservationMissingReason]);
//   - an Absence set to a derived-only state: current, stale, unknown_age
//     ([ErrObservationDerivedAbsence]);
//   - an empty Signal ([ErrObservationMissingSignal]);
//   - a non-empty Signal that fails [ValidateSignalID] ([ErrInvalidSignalID]);
//   - an empty Resource.ID ([ErrObservationMissingResourceID]);
//   - a negative ValidFor ([ErrObservationNegativeValidFor]).
//
// Note what Validate deliberately does NOT check: it does not require
// ObservedAt to be set when Value is present (MeasuredUnknownAge's whole
// purpose is a value with no known observation time), and it does not
// reject Absence != "" combined with a non-nil ObservedAt-less Value,
// because that combination cannot arise from any constructor — the value
// vs. absence check above already covers it.
func (o Observation) Validate() error {
	hasValue := o.Value != nil
	hasAbsence := o.Absence != ""

	if hasValue && hasAbsence {
		return fmt.Errorf("%w: signal %q", ErrObservationValueAndAbsence, o.Signal)
	}
	if !hasValue && !hasAbsence {
		return fmt.Errorf("%w: signal %q", ErrObservationNoValueOrAbsence, o.Signal)
	}
	if hasValue {
		switch o.Value.(type) {
		case bool, string, int64, float64:
		default:
			return fmt.Errorf("%w: signal %q: %T", ErrObservationInvalidValueType, o.Signal, o.Value)
		}
	}
	if hasAbsence {
		if derivedOnlyStates[o.Absence] {
			return fmt.Errorf("%w: signal %q: %q", ErrObservationDerivedAbsence, o.Signal, o.Absence)
		}
		if o.Reason == "" {
			return fmt.Errorf("%w: signal %q", ErrObservationMissingReason, o.Signal)
		}
	}
	if o.CollectedAt.IsZero() {
		return fmt.Errorf("%w: signal %q", ErrObservationMissingCollectedAt, o.Signal)
	}
	if o.Signal == "" {
		return ErrObservationMissingSignal
	}
	if err := ValidateSignalID(o.Signal); err != nil {
		return err
	}
	if o.Resource.ID == "" {
		return fmt.Errorf("%w: signal %q", ErrObservationMissingResourceID, o.Signal)
	}
	if o.ValidFor < 0 {
		return fmt.Errorf("%w: signal %q: %s", ErrObservationNegativeValidFor, o.Signal, o.ValidFor)
	}
	return nil
}
