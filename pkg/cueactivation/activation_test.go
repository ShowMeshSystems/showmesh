package cueactivation

import (
	"testing"
	"time"

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
