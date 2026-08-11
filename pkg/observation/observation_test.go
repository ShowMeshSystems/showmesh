package observation

import (
	"errors"
	"testing"
	"time"
)

func testResource() ResourceRef {
	return ResourceRef{Kind: ResourceFPP, ID: "fpp-front"}
}

// TestStateAtTable enumerates every combination of (value present/absent) x
// (observedAt known/unknown) x (validFor zero/elapsed/not elapsed) x
// (absence set/unset) that StateAt's derivation switches on, built directly
// against the struct rather than through a constructor. This is the test
// that would catch a future "helpful" default of ObservedAt = CollectedAt:
// if StateAt ever treated a nil ObservedAt as "now", the unknownAge cases
// below would silently start reporting current instead.
func TestStateAtTable(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-1 * time.Second)
	old := now.Add(-1 * time.Hour)

	tests := []struct {
		name       string
		value      any
		observedAt *time.Time
		validFor   time.Duration
		absence    State
		want       State
	}{
		// Absence set wins regardless of value/observedAt/validFor: this
		// is StateAt's first branch, and every one of the three real
		// absence states must round-trip through it unchanged.
		{name: "absence unsupported, no value", value: nil, absence: StateUnsupported, want: StateUnsupported},
		{name: "absence not_collected, no value", value: nil, absence: StateNotCollected, want: StateNotCollected},
		{name: "absence collection_failed, no value", value: nil, absence: StateCollectionFailed, want: StateCollectionFailed},

		// No value, no absence: the defensive branch. Validate rejects
		// this combination, but StateAt must still answer honestly
		// rather than panic on a struct that skipped Validate.
		{name: "no value, no absence", value: nil, want: StateNotCollected},

		// Value present, observedAt unknown: the retained-MQTT case.
		// Never StateCurrent no matter what ValidFor says.
		{name: "value present, observedAt nil, validFor zero", value: int64(5), observedAt: nil, validFor: 0, want: StateUnknownAge},
		{name: "value present, observedAt nil, validFor set", value: int64(5), observedAt: nil, validFor: time.Minute, want: StateUnknownAge},

		// Value present, observedAt known, ValidFor zero: never expires.
		{name: "value present, observedAt fresh, validFor zero", value: int64(5), observedAt: &fresh, validFor: 0, want: StateCurrent},
		{name: "value present, observedAt old, validFor zero", value: int64(5), observedAt: &old, validFor: 0, want: StateCurrent},

		// Value present, observedAt known, ValidFor set: aged in or out.
		{name: "value present, observedAt fresh, within validFor", value: int64(5), observedAt: &fresh, validFor: time.Minute, want: StateCurrent},
		{name: "value present, observedAt old, past validFor", value: int64(5), observedAt: &old, validFor: time.Minute, want: StateStale},
		{name: "value present, observedAt exactly at validFor boundary", value: int64(5), observedAt: &fresh, validFor: 1 * time.Second, want: StateCurrent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := Observation{
				Resource:    testResource(),
				Signal:      "test.signal",
				Value:       tt.value,
				ObservedAt:  tt.observedAt,
				CollectedAt: now,
				ValidFor:    tt.validFor,
				Absence:     tt.absence,
			}
			if got := o.StateAt(now); got != tt.want {
				t.Errorf("StateAt() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestStateCurrentRequiresValueAndKnownObservedAt asserts, directly, the
// property the table test above already exercises across many cases: there
// is no way to reach StateCurrent without both a non-nil Value and a
// non-nil ObservedAt.
func TestStateCurrentRequiresValueAndKnownObservedAt(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

	noValue := Observation{Resource: testResource(), Signal: "s", CollectedAt: now, Absence: StateNotCollected, Reason: "x"}
	if got := noValue.StateAt(now); got == StateCurrent {
		t.Errorf("StateAt() = current for an observation with no value, want anything else")
	}

	unknownAge := Observation{Resource: testResource(), Signal: "s", Value: int64(1), ObservedAt: nil, CollectedAt: now}
	if got := unknownAge.StateAt(now); got == StateCurrent {
		t.Errorf("StateAt() = current for an observation with a nil ObservedAt, want anything else")
	}
}

func TestMeasuredSetsObservedAtAndDefaults(t *testing.T) {
	observedAt := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	o, err := Measured(testResource(), "fpp.multisync.enabled", false, observedAt)
	if err != nil {
		t.Fatalf("Measured() error = %v, want nil", err)
	}
	if o.ObservedAt == nil || !o.ObservedAt.Equal(observedAt) {
		t.Errorf("ObservedAt = %v, want %v", o.ObservedAt, observedAt)
	}
	if o.CollectedAt.IsZero() {
		t.Errorf("CollectedAt is zero, want defaulted to time.Now()")
	}
	if o.Quality != QualityDirect {
		t.Errorf("Quality = %q, want default QualityDirect", o.Quality)
	}
	if err := o.Validate(); err != nil {
		t.Errorf("Measured() produced an Observation that fails Validate: %v", err)
	}
}

func TestMeasuredRejectsInvalidValueType(t *testing.T) {
	_, err := Measured(testResource(), "s", []byte("nope"), time.Now())
	if !errors.Is(err, ErrObservationInvalidValueType) {
		t.Fatalf("Measured() error = %v, want errors.Is(err, ErrObservationInvalidValueType)", err)
	}
}

func TestMeasuredUnknownAgeLeavesObservedAtNil(t *testing.T) {
	o, err := MeasuredUnknownAge(testResource(), "node.agent.hello", true, WithSource("mqtt-inventory"))
	if err != nil {
		t.Fatalf("MeasuredUnknownAge() error = %v, want nil", err)
	}
	if o.ObservedAt != nil {
		t.Errorf("ObservedAt = %v, want nil", o.ObservedAt)
	}
	if o.StateAt(time.Now()) != StateUnknownAge {
		t.Errorf("StateAt() = %q, want unknown_age", o.StateAt(time.Now()))
	}
	if o.Source != "mqtt-inventory" {
		t.Errorf("Source = %q, want mqtt-inventory", o.Source)
	}
}

func TestAbsenceConstructors(t *testing.T) {
	tests := []struct {
		name    string
		build   func() (Observation, error)
		wantAbs State
	}{
		{name: "Unsupported", build: func() (Observation, error) { return Unsupported(testResource(), "s", "fpp too old") }, wantAbs: StateUnsupported},
		{name: "NotCollected", build: func() (Observation, error) { return NotCollected(testResource(), "s", "collector disabled") }, wantAbs: StateNotCollected},
		{name: "CollectionFailed", build: func() (Observation, error) { return CollectionFailed(testResource(), "s", "http 503") }, wantAbs: StateCollectionFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, err := tt.build()
			if err != nil {
				t.Fatalf("build() error = %v, want nil", err)
			}
			if o.Absence != tt.wantAbs {
				t.Errorf("Absence = %q, want %q", o.Absence, tt.wantAbs)
			}
			if o.Value != nil {
				t.Errorf("Value = %v, want nil", o.Value)
			}
			if o.Reason == "" {
				t.Errorf("Reason is empty, want the reason passed in")
			}
			if err := o.Validate(); err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestAbsenceConstructorsRejectEmptyReason(t *testing.T) {
	if _, err := Unsupported(testResource(), "s", ""); !errors.Is(err, ErrObservationMissingReason) {
		t.Errorf("Unsupported() error = %v, want errors.Is(err, ErrObservationMissingReason)", err)
	}
	if _, err := NotCollected(testResource(), "s", ""); !errors.Is(err, ErrObservationMissingReason) {
		t.Errorf("NotCollected() error = %v, want errors.Is(err, ErrObservationMissingReason)", err)
	}
	if _, err := CollectionFailed(testResource(), "s", ""); !errors.Is(err, ErrObservationMissingReason) {
		t.Errorf("CollectionFailed() error = %v, want errors.Is(err, ErrObservationMissingReason)", err)
	}
}

func TestOptionsApply(t *testing.T) {
	collectedAt := time.Date(2026, 8, 10, 11, 0, 0, 0, time.UTC)
	o, err := Measured(testResource(), "fpp.multisync.enabled", true, time.Now(),
		WithCollectedAt(collectedAt),
		WithSource("fpp-rest"),
		WithQuality(QualityDerived),
		WithUnit("volts"),
		WithValidFor(15*time.Second),
	)
	if err != nil {
		t.Fatalf("Measured() error = %v, want nil", err)
	}
	if !o.CollectedAt.Equal(collectedAt) {
		t.Errorf("CollectedAt = %v, want %v", o.CollectedAt, collectedAt)
	}
	if o.Source != "fpp-rest" {
		t.Errorf("Source = %q, want fpp-rest", o.Source)
	}
	if o.Quality != QualityDerived {
		t.Errorf("Quality = %q, want derived", o.Quality)
	}
	if o.Unit != "volts" {
		t.Errorf("Unit = %q, want volts", o.Unit)
	}
	if o.ValidFor != 15*time.Second {
		t.Errorf("ValidFor = %v, want 15s", o.ValidFor)
	}
}

// TestValidateRejects exercises every rule Validate's doc comment names,
// one violation at a time, against errors.Is of the specific sentinel —
// not just "an error occurred" — so a change that swaps in the wrong
// sentinel for the wrong rule is caught.
func TestValidateRejects(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	valid := func() Observation {
		return Observation{
			Resource:    testResource(),
			Signal:      "fpp.multisync.enabled",
			Value:       true,
			ObservedAt:  &now,
			CollectedAt: now,
		}
	}

	tests := []struct {
		name    string
		mutate  func(o *Observation)
		wantErr error
	}{
		{
			name:    "value and absence both set",
			mutate:  func(o *Observation) { o.Absence = StateNotCollected; o.Reason = "x" },
			wantErr: ErrObservationValueAndAbsence,
		},
		{
			name:    "no value and no absence",
			mutate:  func(o *Observation) { o.Value = nil; o.ObservedAt = nil },
			wantErr: ErrObservationNoValueOrAbsence,
		},
		{
			name:    "invalid value type",
			mutate:  func(o *Observation) { o.Value = []int{1} },
			wantErr: ErrObservationInvalidValueType,
		},
		{
			name:    "zero CollectedAt",
			mutate:  func(o *Observation) { o.CollectedAt = time.Time{} },
			wantErr: ErrObservationMissingCollectedAt,
		},
		{
			name: "absence set with empty reason",
			mutate: func(o *Observation) {
				o.Value = nil
				o.ObservedAt = nil
				o.Absence = StateUnsupported
				o.Reason = ""
			},
			wantErr: ErrObservationMissingReason,
		},
		{
			name: "absence set to a derived-only state",
			mutate: func(o *Observation) {
				o.Value = nil
				o.ObservedAt = nil
				o.Absence = StateStale
				o.Reason = "x"
			},
			wantErr: ErrObservationDerivedAbsence,
		},
		{
			name:    "empty signal",
			mutate:  func(o *Observation) { o.Signal = "" },
			wantErr: ErrObservationMissingSignal,
		},
		{
			name:    "empty resource ID",
			mutate:  func(o *Observation) { o.Resource.ID = "" },
			wantErr: ErrObservationMissingResourceID,
		},
		{
			name:    "negative ValidFor",
			mutate:  func(o *Observation) { o.ValidFor = -1 * time.Second },
			wantErr: ErrObservationNegativeValidFor,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := valid()
			tt.mutate(&o)
			err := o.Validate()
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Validate() error = %v, want errors.Is(err, %v)", err, tt.wantErr)
			}
		})
	}
}

// TestValidateSignalIDSyntax pins contract section 7's rule directly
// against ValidateSignalID, and against Validate itself so the wiring
// (not just the standalone function) is proven. This is the test that
// would have caught the Step 3 wiring session's real defect: the FPP
// collector emitting "fpp.uptimeSeconds" (camelCase) while a consumer
// elsewhere guessed a different, snake_cased name for the same idea, with
// nothing catching the mismatch until someone read both sides by hand.
func TestValidateSignalIDSyntax(t *testing.T) {
	valid := []SignalID{
		"s",
		"fpp.multisync.enabled",
		"node.control_plane.last_will",
		"a.b.c.d",
		"node123.signal_2",
	}
	for _, id := range valid {
		if err := ValidateSignalID(id); err != nil {
			t.Errorf("ValidateSignalID(%q) = %v, want nil", id, err)
		}
	}

	invalid := []SignalID{
		"",
		".",
		".fpp.reachable",
		"fpp.reachable.",
		"fpp..reachable",
		"fpp.multiSync.enabled",  // camelCase segment
		"FPP.reachable",          // uppercase
		"fpp.multisync enabled",  // space
		"fpp.multi-sync.enabled", // hyphen
	}
	for _, id := range invalid {
		if err := ValidateSignalID(id); !errors.Is(err, ErrInvalidSignalID) {
			t.Errorf("ValidateSignalID(%q) error = %v, want errors.Is(err, ErrInvalidSignalID)", id, err)
		}
	}

	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	bad := Observation{
		Resource:    testResource(),
		Signal:      "fpp.multiSync.enabled",
		Value:       true,
		ObservedAt:  &now,
		CollectedAt: now,
	}
	if err := bad.Validate(); !errors.Is(err, ErrInvalidSignalID) {
		t.Errorf("Validate() error = %v, want errors.Is(err, ErrInvalidSignalID) for a camelCase signal", err)
	}
}

func TestValidateAcceptsAWellFormedObservation(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	o := Observation{
		Resource:    testResource(),
		Signal:      "fpp.multisync.enabled",
		Value:       true,
		ObservedAt:  &now,
		CollectedAt: now,
	}
	if err := o.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// TestValidateAllowsDerivedOnlyStateAtOutput is a guard against confusing
// StateAt's return value with a legal Absence: current, stale, and
// unknown_age are things StateAt derives, never things a caller sets on
// Absence, and the two vocabularies must never be conflated even though
// they share the same Go type.
func TestValidateAllowsDerivedOnlyStateAtOutput(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	o := Observation{
		Resource:    testResource(),
		Signal:      "fpp.multisync.enabled",
		Value:       true,
		ObservedAt:  &now,
		CollectedAt: now,
	}
	if err := o.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	if got := o.StateAt(now); got != StateCurrent {
		t.Fatalf("StateAt() = %q, want current", got)
	}
}
