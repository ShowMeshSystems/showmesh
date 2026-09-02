package audio

import (
	"testing"
	"time"
)

// startingState carries a playlist (never both a playlist and media at
// once — see [SessionDesiredState.Validate]).
func startingState() SessionDesiredState {
	gain := Gain(0.6)
	ceiling := Ceiling(0.9)
	fade := Fade{Curve: FadeCurveLinear, TargetGain: 0.5, Duration: 1}
	mix := MixPolicyDuck
	outputs := []string{"local"}
	playlist := validPlaylist()
	role := SourceRoleBackground
	bookmark := Bookmark{PlaylistRevision: playlist.OwnerRevision, ItemID: "item-1"}
	expiry := time.Unix(1_700_000_000, 0)
	return SessionDesiredState{
		SourceRole: &role,
		Playlist:   &playlist,
		Gain:       &gain,
		Ceiling:    &ceiling,
		Fade:       &fade,
		MixPolicy:  &mix,
		Outputs:    &outputs,
		Bookmark:   &bookmark,
		Expiry:     &expiry,
	}
}

func mustMerge(t *testing.T, req ApplyRequest, s SessionDesiredState) (SessionDesiredState, MergeReport) {
	t.Helper()
	after, report, err := req.Merge(s)
	if err != nil {
		t.Fatalf("Merge: got err %v, want nil", err)
	}
	return after, report
}

func TestApplyRequestOmittedFieldLeavesUnchanged(t *testing.T) {
	before := startingState()
	after, _ := mustMerge(t, ApplyRequest{}, before)

	if after.SourceRole != before.SourceRole {
		t.Error("omitted SourceRole: pointer changed, want unchanged")
	}
	if after.Playlist != before.Playlist {
		t.Error("omitted Playlist: pointer changed, want unchanged")
	}
	if after.Media != before.Media {
		t.Error("omitted Media: pointer changed, want unchanged")
	}
	if after.Gain != before.Gain {
		t.Error("omitted Gain: pointer changed, want unchanged")
	}
	if after.Ceiling != before.Ceiling {
		t.Error("omitted Ceiling: pointer changed, want unchanged")
	}
	if after.Fade != before.Fade {
		t.Error("omitted Fade: pointer changed, want unchanged")
	}
	if after.MixPolicy != before.MixPolicy {
		t.Error("omitted MixPolicy: pointer changed, want unchanged")
	}
	if after.Outputs != before.Outputs {
		t.Error("omitted Outputs: pointer changed, want unchanged")
	}
	if after.Bookmark != before.Bookmark {
		t.Error("omitted Bookmark: pointer changed, want unchanged")
	}
	if after.Expiry != before.Expiry {
		t.Error("omitted Expiry: pointer changed, want unchanged")
	}
}

func TestApplyRequestNullFieldClears(t *testing.T) {
	before := startingState()
	req := ApplyRequest{
		SourceRole: NullField[SourceRole](),
		Playlist:   NullField[PlaylistRef](),
		Gain:       NullField[Gain](),
		Ceiling:    NullField[Ceiling](),
		Fade:       NullField[Fade](),
		MixPolicy:  NullField[MixPolicy](),
		Outputs:    NullField[[]string](),
		Bookmark:   NullField[Bookmark](),
		Expiry:     NullField[time.Time](),
	}
	after, _ := mustMerge(t, req, before)

	if after.SourceRole != nil {
		t.Error("null SourceRole: got non-nil, want cleared")
	}
	if after.Playlist != nil {
		t.Error("null Playlist: got non-nil, want cleared")
	}
	if after.Gain != nil {
		t.Error("null Gain: got non-nil, want cleared")
	}
	if after.Ceiling != nil {
		t.Error("null Ceiling: got non-nil, want cleared")
	}
	if after.Fade != nil {
		t.Error("null Fade: got non-nil, want cleared")
	}
	if after.MixPolicy != nil {
		t.Error("null MixPolicy: got non-nil, want cleared")
	}
	if after.Outputs != nil {
		t.Error("null Outputs: got non-nil, want cleared")
	}
	if after.Bookmark != nil {
		t.Error("null Bookmark: got non-nil, want cleared")
	}
	if after.Expiry != nil {
		t.Error("null Expiry: got non-nil, want cleared")
	}
}

func TestApplyRequestSetExpiryReplacesAbsoluteValue(t *testing.T) {
	before := startingState()
	newExpiry := time.Unix(1_800_000_000, 0)
	after, _ := mustMerge(t, ApplyRequest{Expiry: SetField(newExpiry)}, before)

	if after.Expiry == before.Expiry {
		t.Fatal("set Expiry: pointer unchanged, want replaced")
	}
	if !after.Expiry.Equal(newExpiry) {
		t.Errorf("set Expiry: got %v, want %v", *after.Expiry, newExpiry)
	}
	if after.Gain != before.Gain {
		t.Error("set Expiry must not affect Gain")
	}
}

// TestApplyRequestExpiryAbsentNeverExpires documents the legacy-record
// guarantee restore-time retirement depends on: a state with no Expiry
// ever set decodes and merges with Expiry nil, never a zero time.Time
// that a restore check could mistake for "already expired".
func TestApplyRequestExpiryAbsentNeverExpires(t *testing.T) {
	var legacy SessionDesiredState
	after, _ := mustMerge(t, ApplyRequest{}, legacy)
	if after.Expiry != nil {
		t.Fatalf("legacy state with no Expiry ever set: got %v, want nil", after.Expiry)
	}
}

func TestApplyRequestSetFieldReplaces(t *testing.T) {
	before := startingState()
	newGain := Gain(0.1)
	after, _ := mustMerge(t, ApplyRequest{Gain: SetField(newGain)}, before)

	if after.Gain == before.Gain {
		t.Fatal("set Gain: pointer unchanged, want replaced")
	}
	if *after.Gain != newGain {
		t.Errorf("set Gain: got %v, want %v", *after.Gain, newGain)
	}
	if after.Ceiling != before.Ceiling {
		t.Error("set Gain must not affect Ceiling")
	}
}

func TestApplyRequestMixedOmitSetNull(t *testing.T) {
	before := startingState()
	newMix := MixPolicyInterrupt
	req := ApplyRequest{
		MixPolicy: SetField(newMix),
		Fade:      NullField[Fade](),
	}
	after, _ := mustMerge(t, req, before)

	if after.MixPolicy == before.MixPolicy || *after.MixPolicy != newMix {
		t.Error("set MixPolicy did not take effect")
	}
	if after.Fade != nil {
		t.Error("null Fade did not clear")
	}
	if after.Gain != before.Gain {
		t.Error("omitted Gain changed")
	}
	if after.Playlist != before.Playlist {
		t.Error("omitted Playlist changed")
	}
}

func TestSessionDesiredStateValidateRejectsBothMediaAndPlaylist(t *testing.T) {
	media := MediaRef{AssetID: "a1", ContentHash: "h1"}
	playlist := validPlaylist()
	s := SessionDesiredState{Media: &media, Playlist: &playlist}
	if err := s.Validate(); err == nil {
		t.Fatal("Validate(media and playlist both set): got nil, want error")
	}
}

// Setting Media while explicitly nulling Playlist is the legal source
// switch AUDIO-ENGINE section 3 describes.
func TestApplyRequestSwitchesFromPlaylistToMedia(t *testing.T) {
	before := startingState()
	media := MediaRef{AssetID: "a9", ContentHash: "h9"}
	req := ApplyRequest{Media: SetField(media), Playlist: NullField[PlaylistRef]()}
	after, _ := mustMerge(t, req, before)

	if after.Playlist != nil {
		t.Error("switch to media: Playlist not cleared")
	}
	if after.Media == nil || after.Media.AssetID != "a9" {
		t.Errorf("switch to media: got Media=%+v, want asset a9", after.Media)
	}
}

func TestApplyRequestMergeRejectsMediaAgainstExistingPlaylist(t *testing.T) {
	before := startingState() // Playlist already set, Media nil.
	media := MediaRef{AssetID: "a9", ContentHash: "h9"}
	_, _, err := ApplyRequest{Media: SetField(media)}.Merge(before)
	if err == nil {
		t.Fatal("setting Media without clearing an existing Playlist: got nil error, want ErrSessionHasBothMediaAndPlaylist")
	}
}

func TestApplyRequestMergeReportsCeilingClamp(t *testing.T) {
	before := startingState() // Ceiling 0.9.
	over := Gain(2.0)
	after, report := mustMerge(t, ApplyRequest{Gain: SetField(over)}, before)

	if report.Ceiling == nil || !report.Ceiling.Clamped {
		t.Fatalf("MergeReport = %+v, want a clamped Ceiling result", report)
	}
	if after.Gain == nil || *after.Gain != Gain(0.9) {
		t.Fatalf("merged Gain = %v, want clamped to ceiling 0.9", after.Gain)
	}
}

func TestApplyRequestMergePropagatesInvalidGain(t *testing.T) {
	before := startingState()
	_, _, err := ApplyRequest{Gain: SetField(Gain(-1))}.Merge(before)
	if err == nil {
		t.Fatal("Merge with negative gain: got nil error, want one")
	}
}

func TestApplyRequestMergeDeepCopiesPlaylistItems(t *testing.T) {
	items := []PlaylistItem{{ItemID: "x", Media: MediaRef{AssetID: "a1", ContentHash: "h1"}}}
	playlist := PlaylistRef{OwnerKind: "night_session", OwnerID: "ns-1", OwnerRevision: 1, Repeat: RepeatNone, Resume: ResumePolicyRestart, RequestedTransition: ItemTransitionSequential, Items: items}

	after, _, err := ApplyRequest{Playlist: SetField(playlist)}.Merge(SessionDesiredState{})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}

	items[0].ItemID = "mutated"
	if after.Playlist.Items[0].ItemID != "x" {
		t.Fatalf("pinned playlist mutated via caller's backing array: got %q, want unaffected \"x\"", after.Playlist.Items[0].ItemID)
	}
}

func TestApplyRequestMergeRejectsInvalidGainWithNoCeiling(t *testing.T) {
	_, _, err := ApplyRequest{Gain: SetField(Gain(-1))}.Merge(SessionDesiredState{})
	if err == nil {
		t.Fatal("negative gain with no ceiling present: got nil error, want one")
	}
}

func TestApplyRequestMergeRejectsInvalidCeilingWithNoGain(t *testing.T) {
	_, _, err := ApplyRequest{Ceiling: SetField(Ceiling(0))}.Merge(SessionDesiredState{})
	if err == nil {
		t.Fatal("zero ceiling with no gain present: got nil error, want one")
	}
}

func TestApplyRequestMergeRejectsInvalidFade(t *testing.T) {
	bad := Fade{Curve: "sine", Duration: 1, TargetGain: 0.5}
	_, _, err := ApplyRequest{Fade: SetField(bad)}.Merge(SessionDesiredState{})
	if err == nil {
		t.Fatal("fade with unknown curve: got nil error, want one")
	}
}

func TestApplyRequestMergeRejectsInvalidMixPolicy(t *testing.T) {
	_, _, err := ApplyRequest{MixPolicy: SetField(MixPolicy("blend"))}.Merge(SessionDesiredState{})
	if err == nil {
		t.Fatal("unknown mix policy: got nil error, want one")
	}
}

func TestApplyRequestMergeRejectsInvalidSourceRole(t *testing.T) {
	_, _, err := ApplyRequest{SourceRole: SetField(SourceRole("narrator"))}.Merge(SessionDesiredState{})
	if err == nil {
		t.Fatal("unknown source role: got nil error, want one")
	}
}

func TestApplyRequestMergeRejectsInvalidPlaylist(t *testing.T) {
	bad := validPlaylist()
	bad.Items = nil
	_, _, err := ApplyRequest{Playlist: SetField(bad)}.Merge(SessionDesiredState{})
	if err == nil {
		t.Fatal("playlist that fails its own Validate: got nil error, want one")
	}
}

func TestApplyRequestMergeRejectsEmptyBookmarkItemID(t *testing.T) {
	bad := Bookmark{PlaylistRevision: 1, ItemID: ""}
	_, _, err := ApplyRequest{Bookmark: SetField(bad)}.Merge(SessionDesiredState{})
	if err == nil {
		t.Fatal("bookmark with empty item id: got nil error, want one")
	}
}

func TestApplyRequestMergeAcceptsFullyValidState(t *testing.T) {
	before := startingState()
	after, _, err := ApplyRequest{}.Merge(before)
	if err != nil {
		t.Fatalf("Merge(valid state, no-op request): got err %v, want nil", err)
	}
	if after.Playlist == nil || after.SourceRole == nil || after.Gain == nil {
		t.Fatalf("Merge(valid state, no-op request): got %+v, want fields preserved", after)
	}
}

func TestApplyRequestMergeDeepCopiesOutputs(t *testing.T) {
	outputs := []string{"local"}
	after, _, err := ApplyRequest{Outputs: SetField(outputs)}.Merge(SessionDesiredState{})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}

	outputs[0] = "mutated"
	if (*after.Outputs)[0] != "local" {
		t.Fatalf("pinned outputs mutated via caller's backing array: got %q, want unaffected \"local\"", (*after.Outputs)[0])
	}
}
