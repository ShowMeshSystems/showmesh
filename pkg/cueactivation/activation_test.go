package cueactivation

import (
	"encoding/json"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/cueauth"
)

func baseActivation() Activation {
	return Activation{
		Runner:          "fpp",
		RunnerInstance:  "fpp-01",
		ActivationID:    "act-1",
		Show:            "halloween-2026",
		Generation:      3,
		CatalogRevision: "rev-a",
		Playlist:        "main",
		EntryID:         "entry-1",
		CueID:           "cue-1",
		CueRevision:     2,
		PositionMS:      1500,
		EvidenceAt:      time.Date(2026, 8, 23, 20, 0, 0, 0, time.UTC),
	}
}

func TestTupleProjectsAuthorizationFields(t *testing.T) {
	a := baseActivation()
	got := a.Tuple()
	want := cueauth.AuthorizationTuple{
		Show: "halloween-2026", Generation: 3, CatalogRevision: "rev-a",
		CueID: "cue-1", CueRevision: 2,
	}
	if got != want {
		t.Fatalf("Tuple() = %+v, want %+v", got, want)
	}
}

func TestValidateAccepts(t *testing.T) {
	if err := baseActivation().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestValidateRejectsMissingFields(t *testing.T) {
	cases := []func(a *Activation){
		func(a *Activation) { a.Runner = "" },
		func(a *Activation) { a.RunnerInstance = "" },
		func(a *Activation) { a.ActivationID = "" },
		func(a *Activation) { a.Show = "" },
		func(a *Activation) { a.Generation = 0 },
		func(a *Activation) { a.CatalogRevision = "" },
		func(a *Activation) { a.CueID = "" },
		func(a *Activation) { a.CueRevision = 0 },
		func(a *Activation) { a.PositionMS = -1 },
		func(a *Activation) { a.EvidenceAt = time.Time{} },
	}
	for i, mutate := range cases {
		a := baseActivation()
		mutate(&a)
		if err := a.Validate(); err == nil {
			t.Fatalf("case %d: Validate() accepted an invalid Activation: %+v", i, a)
		}
	}
}

func TestDecodeParamsRoundTrips(t *testing.T) {
	params := map[string]any{
		"runner": "fpp", "runnerInstance": "fpp-01", "activationId": "act-1",
		"show": "halloween-2026", "generation": float64(3), "catalogRevision": "rev-a",
		"playlist": "main", "playlistRevision": float64(1), "entryId": "entry-1",
		"cueId": "cue-1", "cueRevision": float64(2), "positionMs": float64(1500),
		"evidenceAt": "2026-08-23T20:00:00Z",
	}
	got, err := DecodeParams(params)
	if err != nil {
		t.Fatalf("DecodeParams: %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("decoded Activation failed Validate: %v", err)
	}
	if got.CueID != "cue-1" || got.PositionMS != 1500 || got.Generation != 3 {
		t.Fatalf("DecodeParams produced unexpected Activation: %+v", got)
	}
}

func TestDecodeParamsRejectsUnmarshalableValue(t *testing.T) {
	params := map[string]any{"generation": make(chan int)}
	if _, err := DecodeParams(params); err == nil {
		t.Fatalf("DecodeParams accepted a value json.Marshal cannot encode")
	}
}

// TestScheduledAtNsRoundTripsExactly proves a start instant near 1.79e18
// (nanosecond-scale, past float64's exact integer range) survives
// DecodeParams exactly: the identical exact-integer round trip
// mqttproto's own exactIntegerParams gives audio.session.start's
// scheduledAtNs param, since this field deliberately shares that wire
// name (see [Activation.ScheduledAtNs]'s own doc comment).
func TestScheduledAtNsRoundTripsExactly(t *testing.T) {
	const exact = "1789012345678901234"
	params := map[string]any{
		"runner": "fpp", "runnerInstance": "fpp-01", "activationId": "act-1",
		"show": "halloween-2026", "generation": float64(3), "catalogRevision": "rev-a",
		"cueId": "cue-1", "cueRevision": float64(2), "positionMs": float64(1500),
		"evidenceAt":    "2026-08-23T20:00:00Z",
		"scheduledAtNs": json.Number(exact),
	}
	got, err := DecodeParams(params)
	if err != nil {
		t.Fatalf("DecodeParams: %v", err)
	}
	if got.ScheduledAtNs == nil {
		t.Fatalf("ScheduledAtNs = nil, want %s", exact)
	}
	if *got.ScheduledAtNs != 1789012345678901234 {
		t.Errorf("ScheduledAtNs = %d, want 1789012345678901234: the value was rounded", *got.ScheduledAtNs)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("decoded Activation with ScheduledAtNs failed Validate: %v", err)
	}
}

// TestScheduledAtNsWireKeyMatchesAudioParamScheduledAtNs pins the reason
// the exact round trip above works at all: mqttproto's own
// exactIntegerParams treats this exact string as an exact-integer param,
// so a drift in either name would silently stop preserving precision.
func TestScheduledAtNsWireKeyMatchesAudioParamScheduledAtNs(t *testing.T) {
	at := int64(5)
	raw, err := json.Marshal(Activation{ScheduledAtNs: &at})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := m[pkgaudio.ParamScheduledAtNs]; !ok {
		t.Fatalf("Activation's JSON has no key %q; ScheduledAtNs's json tag must match pkgaudio.ParamScheduledAtNs", pkgaudio.ParamScheduledAtNs)
	}
}

// TestScheduledAtNsOmittedWhenNil proves an activation with no scheduled
// instant carries no scheduledAtNs key at all, never a JSON null: an
// agent decoding params with a present-but-null key would take a
// different path than one where the key is simply absent.
func TestScheduledAtNsOmittedWhenNil(t *testing.T) {
	raw, err := json.Marshal(baseActivation())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := m["scheduledAtNs"]; ok {
		t.Errorf("scheduledAtNs present in JSON with ScheduledAtNs nil, want omitted")
	}
	if _, ok := m["unalignedReason"]; ok {
		t.Errorf("unalignedReason present in JSON with UnalignedReason empty, want omitted")
	}
}

// TestScheduleProbeSessionIDIsNeverAnotherWellKnownSessionID guards the
// identity ADR-049 decision 3's coordinator-side reading round depends on:
// a fresh per-node clock reading must never be taken against the live show
// session (which may already be Playing the preceding Cue) or the
// prepare-ahead staging session (which a concurrent tick may already be
// using for a different, later Cue), see [ScheduleProbeSessionID]'s own
// doc comment.
func TestScheduleProbeSessionIDIsNeverAnotherWellKnownSessionID(t *testing.T) {
	for _, other := range []string{AudioSessionID, AnnouncementSessionID, PrepareStagingSessionID, BackgroundSessionID} {
		if ScheduleProbeSessionID == other {
			t.Fatalf("ScheduleProbeSessionID (%q) must never equal %q", ScheduleProbeSessionID, other)
		}
	}
}

// TestScheduleProbeSessionRevisionIsAdditiveNotMultiplicative guards the
// exact overflow finding 5 exists to close: a timestamp multiplied by a
// step count (rather than added to it) overflows uint64 well before this
// century ends, and a wrap-around would put the probe session's own
// revision out of reach of any plain nanosecond value a later real
// activity might present.
func TestScheduleProbeSessionRevisionIsAdditiveNotMultiplicative(t *testing.T) {
	now := time.Date(2026, 8, 23, 20, 0, 0, 0, time.UTC)
	apply := ScheduleProbeSessionRevision(now, ScheduleProbeSessionStepApply)
	prepare := ScheduleProbeSessionRevision(now, ScheduleProbeSessionStepPrepare)
	clear := ScheduleProbeSessionRevision(now, ScheduleProbeSessionStepClear)

	base := uint64(now.UnixNano())
	if apply != base || prepare != base+1 || clear != base+2 {
		t.Fatalf("revisions = (%d, %d, %d), want (%d, %d, %d): the derivation must add the step, never multiply the timestamp by it",
			apply, prepare, clear, base, base+1, base+2)
	}
	if apply >= prepare || prepare >= clear {
		t.Fatalf("revisions are not strictly increasing: apply=%d prepare=%d clear=%d", apply, prepare, clear)
	}
}

// TestPrepareStagingSessionIDIsNeverTheShowSessionID guards the identity
// [audio.Manager.Promote]'s whole design depends on: a coordinator-scheduled
// prepare-ahead must load media under a genuinely separate session from the
// one currently playing the preceding Cue, never the same one — staging
// under [AudioSessionID] itself would mean the "prepare ahead" call tears
// down whatever is already playing, the exact audible cutoff this feature
// exists to prevent (see [PrepareStagingSessionID]'s own doc comment).
func TestPrepareStagingSessionIDIsNeverTheShowSessionID(t *testing.T) {
	if PrepareStagingSessionID == AudioSessionID {
		t.Fatalf("PrepareStagingSessionID (%q) must never equal AudioSessionID (%q)", PrepareStagingSessionID, AudioSessionID)
	}
	if PrepareStagingSessionID == AnnouncementSessionID {
		t.Fatalf("PrepareStagingSessionID (%q) must never equal AnnouncementSessionID (%q)", PrepareStagingSessionID, AnnouncementSessionID)
	}
}
