package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// End-to-end proof for the night controller's announcement sequence,
// driven through THIS package's own wire-param parser into a real
// audio.Manager. Nothing here asserts on a recorded param map: every
// assertion reads the Manager's own reported session state after the
// exact commands the coordinator dispatches.
//
// The sequence under test is the one IDENTIFIER-REGISTER.md specifies:
// audio.session.apply carrying source role "announcement" and a declared
// mix policy, FOLLOWED BY audio.session.start. Apply alone is not enough
// - internal/agent/audio's Manager resolves duck and interrupt at Start,
// never at Apply - and that gap is exactly what these tests exist to
// keep closed.

type nightStaticDecoder struct{ duration time.Duration }

func (d nightStaticDecoder) Decode(_ context.Context, _ string) audio.DecodeResult {
	return audio.DecodeResult{
		Available: true, TypeIdentified: true, MIMEType: "audio/wav", Decoded: true,
		Discoverer: audio.DiscovererEvidence{Ran: true, Duration: d.duration},
	}
}

// nightTestNode is a Manager wired to a FakeEngine plus this package's
// own audio.session.* operation table, which is what a real node runs.
type nightTestNode struct {
	mgr *audio.Manager
	ops map[string]OperationFunc
	dir string
}

func newNightTestNode(t *testing.T) *nightTestNode {
	t.Helper()
	dir := t.TempDir()
	mgr := audio.NewManager(audio.NewFakeEngine(time.Now), audio.NewFileSessionStore(dir), dir, nightStaticDecoder{duration: 5 * time.Second}, time.Now, nil)
	ops := audioSessionOperations(mgr)
	for name, op := range audioGainOperations(mgr) {
		ops[name] = op
	}
	return &nightTestNode{mgr: mgr, ops: ops, dir: dir}
}

// asset writes a file and returns the media params the coordinator sends
// for it.
func (n *nightTestNode) asset(t *testing.T, filename, assetID string) map[string]any {
	t.Helper()
	content := []byte(assetID)
	if err := os.WriteFile(filepath.Join(n.dir, filename), content, 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}
	sum := sha256.Sum256(content)
	return map[string]any{
		"assetId": assetID, "contentHash": "sha256:" + hex.EncodeToString(sum[:]),
		"filename": filename, "sizeBytes": float64(len(content)),
	}
}

// run dispatches one command exactly the way the coordinator's audio
// dispatch does: the action string, plus sessionId, invocationId and
// revision merged into the target's own params.
func (n *nightTestNode) run(t *testing.T, action, sessionID, invocation string, revision int64, params map[string]any) OperationResult {
	t.Helper()
	op, ok := n.ops[action]
	if !ok {
		t.Fatalf("no operation %q", action)
	}
	full := make(map[string]any, len(params)+3)
	for k, v := range params {
		full[k] = v
	}
	full["sessionId"] = sessionID
	full["invocationId"] = invocation
	full["revision"] = float64(revision)
	res, err := op(context.Background(), full, time.Now)
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	return res
}

// nightOutcome and nightReason read one OperationResult's own reported
// value map, which is what a real coordinator receives over the wire.
func nightOutcome(res OperationResult) string { return nightValue(res, "outcome") }
func nightReason(res OperationResult) string  { return nightValue(res, "reason") }

func nightValue(res OperationResult, key string) string {
	m, ok := res.Value.(map[string]any)
	if !ok {
		return ""
	}
	v, _ := m[key].(string)
	return v
}

func (n *nightTestNode) report(t *testing.T, id pkgaudio.SessionID) audio.SessionSnapshot {
	t.Helper()
	for _, s := range n.mgr.Snapshot(context.Background()) {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no session %q in the manager's report", id)
	return audio.SessionSnapshot{}
}

// nightConfiguredGain mirrors nightBackgroundCeilingGain's arithmetic for
// a configured maxGainDb. Duplicated rather than imported: this package
// must not depend on the coordinator.
func nightConfiguredGain(maxGainDb float64) float64 {
	return math.Pow(10, maxGainDb/20)
}

// mutation target: the coordinator's own audio.session.start step
// (nightAdvanceAnnouncementStart). Delete that step and the announcement
// is applied and never started, which is exactly the state this test's
// first half asserts is NOT sufficient: the session stays ready, nothing
// plays, and the bed is untouched at full configured gain.
func TestNightAnnouncementApplyThenStartIsWhatActuallyDucks(t *testing.T) {
	n := newNightTestNode(t)
	bedMedia := n.asset(t, "bed.wav", "bed-asset")
	annMedia := n.asset(t, "ann.wav", "ann-asset")
	configured := nightConfiguredGain(-10)

	// Background audio, in the controller's own order: apply, gain, start.
	n.run(t, "audio.session.apply", "night-bg", "bg-apply", 1, map[string]any{
		"sourceRole": "background", "mixPolicy": "mix", "media": bedMedia,
	})
	n.run(t, "audio.gain.set", "night-bg", "bg-gain", 2, map[string]any{"gain": configured})
	n.run(t, "audio.session.start", "night-bg", "bg-start", 3, nil)
	if got := n.report(t, "night-bg"); got.State != pkgaudio.StatePlaying {
		t.Fatalf("bed state = %q, want playing", got.State)
	}

	// The announcement's own apply: the cue's bound show.action, carrying
	// the declaration the coordinator injects.
	n.run(t, "audio.session.apply", "announcement-1", "ann-apply", 1, map[string]any{
		"sourceRole": "announcement", "mixPolicy": "duck", "media": annMedia,
	})
	afterApply := n.report(t, "announcement-1")
	if afterApply.State == pkgaudio.StatePlaying {
		t.Fatalf("announcement state after apply alone = %q; this test's premise is that apply never plays", afterApply.State)
	}
	if bed := n.report(t, "night-bg"); bed.Ducked {
		t.Fatal("apply alone ducked the bed; the whole reason the controller owns a start step is that it does not")
	}

	// The start the controller owns. THIS is what plays and what ducks.
	n.run(t, "audio.session.start", "announcement-1", "ann-start", 2, nil)
	if got := n.report(t, "announcement-1"); got.State != pkgaudio.StatePlaying {
		t.Fatalf("announcement state after start = %q, want playing", got.State)
	}
	bed := n.report(t, "night-bg")
	if !bed.Ducked || bed.DuckedBy != "announcement-1" {
		t.Fatalf("bed after the announcement started: ducked = %v, duckedBy = %q; want ducked by announcement-1", bed.Ducked, bed.DuckedBy)
	}
	if bed.Gain != 0 {
		t.Fatalf("bed gain under the announcement = %v, want 0", bed.Gain)
	}

	// And it releases on stop, back to the configured gain.
	n.run(t, "audio.session.stop", "announcement-1", "ann-stop", 3, nil)
	bed = n.report(t, "night-bg")
	if bed.Ducked {
		t.Fatal("bed still ducked after the announcement stopped")
	}
	if bed.Gain != pkgaudio.Gain(configured) {
		t.Fatalf("bed gain after the announcement = %v, want the configured %v", bed.Gain, configured)
	}
}

// mutation target: nightAnnouncementRevisions' floor+1/floor+2 pair, and
// the coordinator's clear step. This is the second-night case: the
// announcement's own apply is dispatched at its show.action's PINNED
// config revision, which does not change between cycles, so without the
// clear that deletes the session (and its RevisionState with it) every
// later cycle's apply is refused as stale and the announcement degrades.
func TestNightAnnouncementClearMakesThePinnedApplyLandAgainNextCycle(t *testing.T) {
	n := newNightTestNode(t)
	annMedia := n.asset(t, "ann.wav", "ann-asset")
	apply := map[string]any{"sourceRole": "announcement", "mixPolicy": "duck", "media": annMedia}

	// Cycle 1: clear (nothing there yet), apply at the pinned revision 1,
	// start at 3.
	n.run(t, "audio.session.clear", "announcement-1", "c1-clear", 2, nil)
	if res := n.run(t, "audio.session.apply", "announcement-1", "c1-apply", 1, apply); nightOutcome(res) == "refused" {
		t.Fatalf("cycle 1 apply refused: %s", nightReason(res))
	}
	n.run(t, "audio.session.start", "announcement-1", "c1-start", 3, nil)
	if got := n.report(t, "announcement-1"); got.State != pkgaudio.StatePlaying {
		t.Fatalf("cycle 1 announcement state = %q, want playing", got.State)
	}

	// Cycle 2: the same pinned apply revision. Without the clear first it
	// would be refused; with it, the session is gone and revision 1 is
	// accepted again.
	n.run(t, "audio.session.clear", "announcement-1", "c2-clear", 4, nil)
	res := n.run(t, "audio.session.apply", "announcement-1", "c2-apply", 1, apply)
	if nightOutcome(res) == "refused" {
		t.Fatalf("cycle 2 apply at the same pinned revision was refused (%v); the clear is what has to make this land", nightReason(res))
	}
	n.run(t, "audio.session.start", "announcement-1", "c2-start", 5, nil)
	if got := n.report(t, "announcement-1"); got.State != pkgaudio.StatePlaying {
		t.Fatalf("cycle 2 announcement state = %q, want playing", got.State)
	}
}

// The same second night WITHOUT the clear, so the failure the clear
// exists to prevent is on the record rather than asserted from reading.
func TestNightAnnouncementWithoutAClearThePinnedApplyGoesStaleNextCycle(t *testing.T) {
	n := newNightTestNode(t)
	annMedia := n.asset(t, "ann.wav", "ann-asset")
	apply := map[string]any{"sourceRole": "announcement", "mixPolicy": "duck", "media": annMedia}

	n.run(t, "audio.session.apply", "announcement-1", "c1-apply", 1, apply)
	n.run(t, "audio.session.start", "announcement-1", "c1-start", 3, nil)

	res := n.run(t, "audio.session.apply", "announcement-1", "c2-apply", 1, apply)
	if nightOutcome(res) != "refused" {
		t.Fatalf("a second cycle's apply at the same pinned revision reported %v; this test pins that it is refused as stale, which is what the clear step exists to avoid", res.Value)
	}
}
