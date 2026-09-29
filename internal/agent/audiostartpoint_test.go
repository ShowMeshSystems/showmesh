package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// audio.session.start's optional start point (pkg/audio's
// ParamStartItemID/ParamStartIndex/ParamStartPositionMs) is what lets the
// coordinator give an audio node the night bed back where the other nodes
// are playing it, after the node's own session is gone. These tests prove
// the three cases that matter: the point is honoured, a partial point is
// refused rather than half-applied, and a command carrying no point at
// all behaves exactly as it did before the keys existed.

// startPointPlaylistFixture applies a two-item playlist and leaves it
// unstarted, which is the state a rejoining node is in right after the
// coordinator re-applies the bed to it.
func startPointPlaylistFixture(t *testing.T, mgr *audio.Manager, ctx context.Context, id pkgaudio.SessionID, dir string) {
	t.Helper()
	hashA := writeAssetFixture(t, dir, "start-point-a.wav", []byte("item a audio"))
	hashB := writeAssetFixture(t, dir, "start-point-b.wav", []byte("item b audio"))
	playlist := pkgaudio.PlaylistRef{
		OwnerKind: "show", OwnerID: "night-session", OwnerRevision: 1,
		Repeat: pkgaudio.RepeatPlaylist, Resume: pkgaudio.ResumePolicyResume, RequestedTransition: pkgaudio.ItemTransitionSequential,
		Items: []pkgaudio.PlaylistItem{
			{ItemID: "item-a", Index: 0, Media: pkgaudio.MediaRef{AssetID: "start-point-asset-a", ContentHash: hashA, RuntimeFilename: "start-point-a.wav"}},
			{ItemID: "item-b", Index: 1, Media: pkgaudio.MediaRef{AssetID: "start-point-asset-b", ContentHash: hashB, RuntimeFilename: "start-point-b.wav"}},
		},
	}
	if out := mgr.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Playlist: pkgaudio.SetField(playlist)}); out.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("Apply refused: %s", out.Reason)
	}
}

// startPointSnapshot returns id's own current item, index and position.
func startPointSnapshot(t *testing.T, mgr *audio.Manager, ctx context.Context, id pkgaudio.SessionID) (itemID string, index int, position time.Duration, positionKnown bool) {
	t.Helper()
	for _, snap := range mgr.Snapshot(ctx) {
		if snap.ID == id {
			return snap.ItemID, snap.ItemIndex, snap.Position, snap.PositionKnown
		}
	}
	t.Fatal("session not found in snapshot")
	return "", 0, 0, false
}

// TestStartSessionWithFullStartPointStartsNamedItemAtPosition is the
// rejoin case: a freshly applied session sits on its first item, and a
// start carrying the point begins the NAMED item at the named position
// instead.
func TestStartSessionWithFullStartPointStartsNamedItemAtPosition(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 17, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clock)
	ops := audioSessionOperations(mgr)
	ctx := context.Background()
	const id = pkgaudio.SessionID("bed-start-point-1")

	startPointPlaylistFixture(t, mgr, ctx, id, dir)

	startOp := ops[string(pkgaudio.OperationSessionStart)]
	res, err := startOp(ctx, wireCmdParams(t, "audio.session.start", map[string]any{
		"sessionId": string(id), "invocationId": "inv-start", "revision": 2,
		pkgaudio.ParamStartItemID:     "item-b",
		pkgaudio.ParamStartIndex:      1,
		pkgaudio.ParamStartPositionMs: 1500,
	}), clock.now)
	if err != nil {
		t.Fatalf("startOp: %v", err)
	}
	outcome, reason := sessionOutcomeReason(t, res)
	if outcome != string(pkgaudio.OutcomeStarted) {
		t.Fatalf("start outcome = %q (%s), want started", outcome, reason)
	}
	itemID, index, position, known := startPointSnapshot(t, mgr, ctx, id)
	if itemID != "item-b" || index != 1 {
		t.Fatalf("started item = %q index %d, want item-b index 1", itemID, index)
	}
	if !known || position < 1400*time.Millisecond || position > 1600*time.Millisecond {
		t.Fatalf("started position = %v (known=%v), want ~1500ms", position, known)
	}
}

// TestStartSessionWithStartPointAndScheduledInstantStartsThere proves the
// point and ParamScheduledAtNs travel together: a scheduled start is
// still scheduled when it also names where to begin.
func TestStartSessionWithStartPointAndScheduledInstantStartsThere(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 17, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clock)
	ops := audioSessionOperations(mgr)
	ctx := context.Background()
	const id = pkgaudio.SessionID("bed-start-point-2")

	startPointPlaylistFixture(t, mgr, ctx, id, dir)

	startOp := ops[string(pkgaudio.OperationSessionStart)]
	res, err := startOp(ctx, wireCmdParams(t, "audio.session.start", map[string]any{
		"sessionId": string(id), "invocationId": "inv-start", "revision": 2,
		pkgaudio.ParamScheduledAtNs:   clock.now().UnixNano(),
		pkgaudio.ParamStartItemID:     "item-b",
		pkgaudio.ParamStartIndex:      1,
		pkgaudio.ParamStartPositionMs: 2500,
	}), clock.now)
	if err != nil {
		t.Fatalf("startOp: %v", err)
	}
	outcome, reason := sessionOutcomeReason(t, res)
	if outcome != string(pkgaudio.OutcomeStarted) {
		t.Fatalf("start outcome = %q (%s), want started", outcome, reason)
	}
	itemID, index, position, known := startPointSnapshot(t, mgr, ctx, id)
	if itemID != "item-b" || index != 1 {
		t.Fatalf("started item = %q index %d, want item-b index 1", itemID, index)
	}
	if !known || position < 2400*time.Millisecond || position > 2600*time.Millisecond {
		t.Fatalf("started position = %v (known=%v), want ~2500ms", position, known)
	}
}

// TestStartSessionWithPartialStartPointIsRefused proves a partial send is
// refused rather than half-applied: a position without its item would
// play the wrong track at the right offset.
func TestStartSessionWithPartialStartPointIsRefused(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 17, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clock)
	ops := audioSessionOperations(mgr)
	ctx := context.Background()
	const id = pkgaudio.SessionID("bed-start-point-3")

	startPointPlaylistFixture(t, mgr, ctx, id, dir)

	startOp := ops[string(pkgaudio.OperationSessionStart)]
	_, err := startOp(ctx, wireCmdParams(t, "audio.session.start", map[string]any{
		"sessionId": string(id), "invocationId": "inv-start", "revision": 2,
		pkgaudio.ParamStartPositionMs: 1500,
	}), clock.now)
	if err == nil {
		t.Fatal("startOp accepted a start point missing its item id and index, want an error")
	}
	if !strings.Contains(err.Error(), pkgaudio.ParamStartItemID) {
		t.Fatalf("error = %v, want it to name %s", err, pkgaudio.ParamStartItemID)
	}
}

// TestStartSessionWithNoStartPointStartsTheFirstItemAtZero is the
// backward-compatibility proof: a start carrying none of the three new
// keys behaves exactly as it did before they existed.
func TestStartSessionWithNoStartPointStartsTheFirstItemAtZero(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 17, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clock)
	ops := audioSessionOperations(mgr)
	ctx := context.Background()
	const id = pkgaudio.SessionID("bed-start-point-4")

	startPointPlaylistFixture(t, mgr, ctx, id, dir)

	startOp := ops[string(pkgaudio.OperationSessionStart)]
	res, err := startOp(ctx, wireCmdParams(t, "audio.session.start", map[string]any{
		"sessionId": string(id), "invocationId": "inv-start", "revision": 2,
	}), clock.now)
	if err != nil {
		t.Fatalf("startOp: %v", err)
	}
	outcome, reason := sessionOutcomeReason(t, res)
	if outcome != string(pkgaudio.OutcomeStarted) {
		t.Fatalf("start outcome = %q (%s), want started", outcome, reason)
	}
	itemID, index, position, known := startPointSnapshot(t, mgr, ctx, id)
	if itemID != "item-a" || index != 0 {
		t.Fatalf("started item = %q index %d, want item-a index 0", itemID, index)
	}
	if known && position > 200*time.Millisecond {
		t.Fatalf("started position = %v, want the item's beginning", position)
	}
}

// TestStartSessionWithZeroStartPositionStartsTheNamedItemAtItsBeginning
// proves a zero position is honoured as a real value, not read as an
// absent one: the named item still wins, from its own start.
func TestStartSessionWithZeroStartPositionStartsTheNamedItemAtItsBeginning(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 17, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clock)
	ops := audioSessionOperations(mgr)
	ctx := context.Background()
	const id = pkgaudio.SessionID("bed-start-point-5")

	startPointPlaylistFixture(t, mgr, ctx, id, dir)

	startOp := ops[string(pkgaudio.OperationSessionStart)]
	res, err := startOp(ctx, wireCmdParams(t, "audio.session.start", map[string]any{
		"sessionId": string(id), "invocationId": "inv-start", "revision": 2,
		pkgaudio.ParamStartItemID:     "item-b",
		pkgaudio.ParamStartIndex:      1,
		pkgaudio.ParamStartPositionMs: 0,
	}), clock.now)
	if err != nil {
		t.Fatalf("startOp: %v", err)
	}
	outcome, reason := sessionOutcomeReason(t, res)
	if outcome != string(pkgaudio.OutcomeStarted) {
		t.Fatalf("start outcome = %q (%s), want started", outcome, reason)
	}
	itemID, index, position, known := startPointSnapshot(t, mgr, ctx, id)
	if itemID != "item-b" || index != 1 {
		t.Fatalf("started item = %q index %d, want item-b index 1", itemID, index)
	}
	if known && position > 200*time.Millisecond {
		t.Fatalf("started position = %v, want the named item's beginning", position)
	}
}

// TestStartSessionWithAStartPointNamingAnItemThePlaylistDoesNotHaveIsRefused
// keeps a mismatched point from being approximated: the coordinator's own
// idea of the playlist can be one revision behind the node's.
func TestStartSessionWithAStartPointNamingAnItemThePlaylistDoesNotHaveIsRefused(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 17, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clock)
	ops := audioSessionOperations(mgr)
	ctx := context.Background()
	const id = pkgaudio.SessionID("bed-start-point-6")

	startPointPlaylistFixture(t, mgr, ctx, id, dir)

	startOp := ops[string(pkgaudio.OperationSessionStart)]
	res, err := startOp(ctx, wireCmdParams(t, "audio.session.start", map[string]any{
		"sessionId": string(id), "invocationId": "inv-start", "revision": 2,
		pkgaudio.ParamStartItemID:     "item-that-left",
		pkgaudio.ParamStartIndex:      1,
		pkgaudio.ParamStartPositionMs: 1500,
	}), clock.now)
	if err != nil {
		t.Fatalf("startOp: %v", err)
	}
	outcome, reason := sessionOutcomeReason(t, res)
	if outcome != string(pkgaudio.OutcomeRefused) {
		t.Fatalf("start outcome = %q (%s), want refused", outcome, reason)
	}
}

// TestStartSessionWithAStartPositionPastTheItemIsAcceptedHere records
// where the guard against an over-run position actually lives, which is
// NOT this agent: a position past the end of the item is accepted and the
// session reports playing.
//
// Against the real GStreamer engine this is worse than it looks here: its
// own Start reaches EOS during prepare and returns without ever going to
// PLAYING (internal/agent/audio/gstengine/methods.go), while
// [audio.Manager.start] records StatePlaying from a non-error return
// regardless, so the session claims to be playing while presenting
// nothing. READ FROM THE CODE, NOT MEASURED: the fake engine models no
// EOS at all, so nothing here can prove the real engine's behaviour, and
// this test deliberately does not claim to.
//
// The coordinator is what keeps that case from arising: it will not
// extrapolate a peer's reported position it has held for too long
// (nightBedPositionEvidenceMaxAge), because it has no item duration to
// clamp against.
func TestStartSessionWithAStartPositionPastTheItemIsAcceptedHere(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 17, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clock)
	ops := audioSessionOperations(mgr)
	ctx := context.Background()
	const id = pkgaudio.SessionID("bed-start-point-7")

	startPointPlaylistFixture(t, mgr, ctx, id, dir)

	startOp := ops[string(pkgaudio.OperationSessionStart)]
	res, err := startOp(ctx, wireCmdParams(t, "audio.session.start", map[string]any{
		"sessionId": string(id), "invocationId": "inv-start", "revision": 2,
		pkgaudio.ParamStartItemID:     "item-b",
		pkgaudio.ParamStartIndex:      1,
		pkgaudio.ParamStartPositionMs: 86_400_000,
	}), clock.now)
	if err != nil {
		t.Fatalf("startOp: %v", err)
	}
	outcome, reason := sessionOutcomeReason(t, res)
	if outcome != string(pkgaudio.OutcomeStarted) {
		t.Fatalf("start outcome = %q (%s), want started: this agent does not bound the position", outcome, reason)
	}
}
