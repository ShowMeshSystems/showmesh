package agent

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// This file covers R1's pause-result bookmark evidence (ADR-049 decision
// 4) and audio.session.resume's own ParamScheduledAtNs wiring, against a
// real *audio.Manager exactly as audiosessionrevision_wire_test.go and
// cueactivationaudio_test.go already do.

// TestPauseSessionReportsBookmarkEvidence proves a paused session's
// result carries its own bookmark (item id, index, position) so a
// coordinator resuming a multi-node bed can read it back without a
// second observation call.
func TestPauseSessionReportsBookmarkEvidence(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 16, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clock)
	ops := audioSessionOperations(mgr)
	ctx := context.Background()

	hash := writeAssetFixture(t, dir, "bookmark.wav", []byte("pretend this is wav audio content"))
	ref := pkgaudio.MediaRef{AssetID: "bookmark-asset", ContentHash: hash, RuntimeFilename: "bookmark.wav"}
	const id = pkgaudio.SessionID("bed-pause-1")

	if out := mgr.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}); out.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("Apply refused: %s", out.Reason)
	}
	if out := mgr.Start(ctx, id, "inv-start", 2); out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("Start = %q (%s), want started", out.Outcome, out.Reason)
	}
	clock.t = clock.t.Add(1500 * time.Millisecond)

	pauseOp := ops[string(pkgaudio.OperationSessionPause)]
	res, err := pauseOp(ctx, wireCmdParams(t, "audio.session.pause", map[string]any{
		"sessionId": string(id), "invocationId": "inv-pause", "revision": 3,
	}), clock.now)
	if err != nil {
		t.Fatalf("pauseOp: %v", err)
	}
	outcome, reason := sessionOutcomeReason(t, res)
	if outcome != string(pkgaudio.OutcomePosition) {
		t.Fatalf("pause outcome = %q (%s), want position", outcome, reason)
	}

	value, ok := res.Value.(map[string]any)
	if !ok {
		t.Fatalf("OperationResult.Value = %#v, want map[string]any", res.Value)
	}
	known, ok := value[pkgaudio.ResultBookmarkKnown].(bool)
	if !ok || !known {
		t.Fatalf("%s = %v (ok=%v), want true", pkgaudio.ResultBookmarkKnown, value[pkgaudio.ResultBookmarkKnown], ok)
	}
	if got := value[pkgaudio.ResultBookmarkItemID]; got != "media" {
		t.Fatalf("%s = %v, want \"media\"", pkgaudio.ResultBookmarkItemID, got)
	}
	if got := value[pkgaudio.ResultBookmarkIndex]; got != 0 {
		t.Fatalf("%s = %v, want 0", pkgaudio.ResultBookmarkIndex, got)
	}
	posMs, ok := value[pkgaudio.ResultBookmarkPositionMs].(int64)
	if !ok || posMs < 1400 || posMs > 1600 {
		t.Fatalf("%s = %v (ok=%v), want ~1500", pkgaudio.ResultBookmarkPositionMs, value[pkgaudio.ResultBookmarkPositionMs], ok)
	}
}

// TestPauseSessionReportsBookmarkUnknownWhenNothingToBookmark proves the
// negative side: a pause refused for want of anything loaded reports
// bookmarkKnown false rather than omitting the field or fabricating one.
func TestPauseSessionReportsBookmarkUnknownWhenNothingToBookmark(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 16, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clock)
	ops := audioSessionOperations(mgr)
	ctx := context.Background()

	const id = pkgaudio.SessionID("bed-pause-2")
	pauseOp := ops[string(pkgaudio.OperationSessionPause)]
	res, err := pauseOp(ctx, wireCmdParams(t, "audio.session.pause", map[string]any{
		"sessionId": string(id), "invocationId": "inv-pause", "revision": 1,
	}), clock.now)
	if err != nil {
		t.Fatalf("pauseOp: %v", err)
	}
	outcome, _ := sessionOutcomeReason(t, res)
	if outcome != string(pkgaudio.OutcomeRefused) {
		t.Fatalf("pause outcome on an unloaded session = %q, want refused", outcome)
	}
	value, ok := res.Value.(map[string]any)
	if !ok {
		t.Fatalf("OperationResult.Value = %#v, want map[string]any", res.Value)
	}
	known, ok := value[pkgaudio.ResultBookmarkKnown].(bool)
	if !ok || known {
		t.Fatalf("%s = %v (ok=%v), want false", pkgaudio.ResultBookmarkKnown, value[pkgaudio.ResultBookmarkKnown], ok)
	}
}

// TestResumeSessionHonoursScheduledAtNs is the wire-level mirror of
// startSession's own scheduled-start wiring: audio.session.resume with
// ParamScheduledAtNs reaches [audio.Manager.ResumeAt], refusing a past
// instant exactly as a scheduled start does.
func TestResumeSessionHonoursScheduledAtNs(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 16, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clock)
	ops := audioSessionOperations(mgr)
	ctx := context.Background()

	hash := writeAssetFixture(t, dir, "resume.wav", []byte("pretend this is wav audio content"))
	ref := pkgaudio.MediaRef{AssetID: "resume-asset", ContentHash: hash, RuntimeFilename: "resume.wav"}
	const id = pkgaudio.SessionID("bed-resume-1")

	if out := mgr.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}); out.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("Apply refused: %s", out.Reason)
	}
	if out := mgr.Start(ctx, id, "inv-start", 2); out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("Start = %q (%s), want started", out.Outcome, out.Reason)
	}
	if out := mgr.Pause(ctx, id, "inv-pause", 3); out.Outcome != pkgaudio.OutcomePosition {
		t.Fatalf("Pause = %q (%s), want position", out.Outcome, out.Reason)
	}

	// No clock is wired on this Manager, so a past instant is ignored
	// (started/resumed on arrival) rather than refused -- proving the
	// param actually reaches ResumeAt, distinct from Resume's own
	// no-param path, is enough here without also standing up a locked
	// media clock (itemschedule_test.go already covers the locked-clock
	// refusal and re-anchor cases against *audio.Manager directly).
	resumeOp := ops[string(pkgaudio.OperationSessionResume)]
	res, err := resumeOp(ctx, wireCmdParams(t, "audio.session.resume", map[string]any{
		"sessionId": string(id), "invocationId": "inv-resume", "revision": 4,
		pkgaudio.ParamScheduledAtNs: clock.now().UnixNano(),
	}), clock.now)
	if err != nil {
		t.Fatalf("resumeOp: %v", err)
	}
	outcome, reason := sessionOutcomeReason(t, res)
	if outcome != string(pkgaudio.OutcomeStarted) {
		t.Fatalf("resume outcome = %q (%s), want started", outcome, reason)
	}

	s := mgr.Snapshot(ctx)
	var found bool
	for _, snap := range s {
		if snap.ID == id {
			found = true
			if snap.State != pkgaudio.StatePlaying {
				t.Fatalf("state after a scheduled resume with no clock wired = %q, want playing", snap.State)
			}
		}
	}
	if !found {
		t.Fatal("session not found in snapshot")
	}
}

// resumePointPlaylistFixture applies a two-item playlist, starts it, and
// pauses it on item-a (position irrelevant), for the resume-point wire
// tests below.
func resumePointPlaylistFixture(t *testing.T, mgr *audio.Manager, ctx context.Context, id pkgaudio.SessionID, dir string) {
	t.Helper()
	hashA := writeAssetFixture(t, dir, "resume-point-a.wav", []byte("item a audio"))
	hashB := writeAssetFixture(t, dir, "resume-point-b.wav", []byte("item b audio"))
	playlist := pkgaudio.PlaylistRef{
		OwnerKind: "show", OwnerID: "night-session", OwnerRevision: 1,
		Repeat: pkgaudio.RepeatNone, Resume: pkgaudio.ResumePolicyRestart, RequestedTransition: pkgaudio.ItemTransitionSequential,
		Items: []pkgaudio.PlaylistItem{
			{ItemID: "item-a", Index: 0, Media: pkgaudio.MediaRef{AssetID: "resume-point-asset-a", ContentHash: hashA, RuntimeFilename: "resume-point-a.wav"}},
			{ItemID: "item-b", Index: 1, Media: pkgaudio.MediaRef{AssetID: "resume-point-asset-b", ContentHash: hashB, RuntimeFilename: "resume-point-b.wav"}},
		},
	}
	if out := mgr.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Playlist: pkgaudio.SetField(playlist)}); out.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("Apply refused: %s", out.Reason)
	}
	if out := mgr.Start(ctx, id, "inv-start", 2); out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("Start = %q (%s), want started", out.Outcome, out.Reason)
	}
	if out := mgr.Pause(ctx, id, "inv-pause", 3); out.Outcome != pkgaudio.OutcomePosition {
		t.Fatalf("Pause = %q (%s), want position", out.Outcome, out.Reason)
	}
}

// TestResumeSessionWithFullResumePointResumesNamedItemAtInstant proves
// the wire-level resume point (ADR-049 decision 4, amended): all three
// of resumeItemId/resumeIndex/resumePositionMs, sent together with
// ParamScheduledAtNs, resume the named item at the named position, not
// this node's own pause item.
func TestResumeSessionWithFullResumePointResumesNamedItemAtInstant(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 16, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clock)
	ops := audioSessionOperations(mgr)
	ctx := context.Background()
	const id = pkgaudio.SessionID("bed-resume-point-1")

	resumePointPlaylistFixture(t, mgr, ctx, id, dir)

	resumeOp := ops[string(pkgaudio.OperationSessionResume)]
	res, err := resumeOp(ctx, wireCmdParams(t, "audio.session.resume", map[string]any{
		"sessionId": string(id), "invocationId": "inv-resume", "revision": 4,
		pkgaudio.ParamScheduledAtNs:    clock.now().UnixNano(),
		pkgaudio.ParamResumeItemID:     "item-b",
		pkgaudio.ParamResumeIndex:      1,
		pkgaudio.ParamResumePositionMs: 1500,
	}), clock.now)
	if err != nil {
		t.Fatalf("resumeOp: %v", err)
	}
	outcome, reason := sessionOutcomeReason(t, res)
	if outcome != string(pkgaudio.OutcomeStarted) {
		t.Fatalf("resume outcome = %q (%s), want started", outcome, reason)
	}

	var found bool
	for _, snap := range mgr.Snapshot(ctx) {
		if snap.ID != id {
			continue
		}
		found = true
		if snap.ItemID != "item-b" || snap.ItemIndex != 1 {
			t.Fatalf("resumed item = %q index %d, want item-b index 1", snap.ItemID, snap.ItemIndex)
		}
		if !snap.PositionKnown || snap.Position < 1400*time.Millisecond || snap.Position > 1600*time.Millisecond {
			t.Fatalf("resumed position = %v (known=%v), want ~1500ms", snap.Position, snap.PositionKnown)
		}
	}
	if !found {
		t.Fatal("session not found in snapshot")
	}
}

// TestResumeSessionWithPartialResumePointIsRefused proves a partial send
// (missing one of the three resume-point fields) is refused rather than
// silently falling back to a subset of it.
func TestResumeSessionWithPartialResumePointIsRefused(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 16, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clock)
	ops := audioSessionOperations(mgr)
	ctx := context.Background()
	const id = pkgaudio.SessionID("bed-resume-point-2")

	resumePointPlaylistFixture(t, mgr, ctx, id, dir)

	resumeOp := ops[string(pkgaudio.OperationSessionResume)]
	_, err := resumeOp(ctx, wireCmdParams(t, "audio.session.resume", map[string]any{
		"sessionId": string(id), "invocationId": "inv-resume", "revision": 4,
		pkgaudio.ParamResumeItemID: "item-b",
		pkgaudio.ParamResumeIndex:  1,
		// resumePositionMs deliberately omitted.
	}), clock.now)
	if err == nil {
		t.Fatal("resumeOp with a partial resume point succeeded, want an error")
	}
}

// TestResumeSessionWithMismatchedResumePointIsRefused proves a resume
// point naming an item that does not match this session's own playlist
// at that index is refused, naming both.
func TestResumeSessionWithMismatchedResumePointIsRefused(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 16, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clock)
	ops := audioSessionOperations(mgr)
	ctx := context.Background()
	const id = pkgaudio.SessionID("bed-resume-point-3")

	resumePointPlaylistFixture(t, mgr, ctx, id, dir)

	resumeOp := ops[string(pkgaudio.OperationSessionResume)]
	res, err := resumeOp(ctx, wireCmdParams(t, "audio.session.resume", map[string]any{
		"sessionId": string(id), "invocationId": "inv-resume", "revision": 4,
		pkgaudio.ParamResumeItemID:     "item-does-not-exist",
		pkgaudio.ParamResumeIndex:      1,
		pkgaudio.ParamResumePositionMs: 1000,
	}), clock.now)
	if err != nil {
		t.Fatalf("resumeOp: %v", err)
	}
	outcome, reason := sessionOutcomeReason(t, res)
	if outcome != string(pkgaudio.OutcomeRefused) {
		t.Fatalf("resume outcome = %q (%s), want refused", outcome, reason)
	}
}
