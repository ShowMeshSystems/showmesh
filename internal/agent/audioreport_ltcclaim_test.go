package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/nodeaudio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// TestSecondShowSessionLTCRefusalIsVisibleOnACoordinatorSurface reproduces
// the defect this seam fixes: [ltcOwner.claim] (internal/agent/audio/
// ltclifecycle.go) correctly refuses a second show session that asks for
// this node's one LTC run while the first still holds it, but until now
// that refusal's only evidence was a warn-level log line on the node --
// nothing reached any coordinator surface, so an operator watching
// show-b would see a healthy, playing session and no indication it is
// not the session actually driving LTC.
//
// This test drives a real [*audio.Manager] with two concurrent show-role
// sessions (FakeEngine, no real hardware -- container/unit-bench
// evidence only), builds the session reports the way runAudioReport
// actually does, and feeds them through the coordinator's own nodeaudio
// collector -- the same GET /api/v1/observations?resourceKind=
// audio_session surface showmeshctl and the UI both read generically.
// It fails against the unmodified tree (show-b's refused claim was
// nowhere in the built observations) and passes once the refused
// session's own audio_session.ltc.claim.state/.reason evidence flows
// through unchanged.
func TestSecondShowSessionLTCRefusalIsVisibleOnACoordinatorSurface(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 28, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clock)
	ctx := context.Background()

	refA := writeAudioClaimTestAsset(t, dir, "show-a.wav", "asset-a", []byte("show-a audio"))
	refB := writeAudioClaimTestAsset(t, dir, "show-b.wav", "asset-b", []byte("show-b audio"))

	if r := mgr.Apply(ctx, "show-a", "apply-a", 1, pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleShow),
		Media:      pkgaudio.SetField(refA),
	}); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply show-a refused: %+v", r)
	}
	if r := mgr.Start(ctx, "show-a", "start-a", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start show-a refused: %+v", r)
	}

	if r := mgr.Apply(ctx, "show-b", "apply-b", 1, pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleShow),
		Media:      pkgaudio.SetField(refB),
	}); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply show-b refused: %+v", r)
	}
	// show-b's own Start is never refused: the SESSION starts playing
	// fine (a real defect this seam does not touch -- only the LTC
	// claim underneath it is turned away). See ltclifecycle.go's
	// startLTCLocked doc comment: an LTC claim refusal is never
	// propagated into the session's own outcome.
	if r := mgr.Start(ctx, "show-b", "start-b", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start show-b refused: %+v", r)
	}

	sessions, truncated := buildAudioSessionReports(ctx, mgr)
	if truncated {
		t.Fatal("sessions truncated unexpectedly")
	}
	if len(sessions) != 2 {
		t.Fatalf("session reports = %d, want 2", len(sessions))
	}

	// Every field below this test's own sessions/ObservedAt is filler: this
	// test exercises the audio_session.* signals only. Node-level reasons
	// still must be non-empty wherever their own paired "available"/
	// "enumerated" boolean is false, matching [mqttproto.AudioPayload.
	// Validate]'s own rule -- left unset here they are not evidence, they
	// are a panic in nodeObservations.
	now := clock.now()
	payload := mqttproto.AudioPayload{
		Routes:                   []mqttproto.AudioRouteReport{},
		Sessions:                 sessions,
		ObservedAt:               &now,
		HardwareEnumeratedReason: "not exercised by this test",
		EngineReason:             "not exercised by this test",
		DeviceReason:             "not exercised by this test",
		ProgramReason:            "not exercised by this test",
		LTCReason:                "not exercised by this test",
		LTCGeneratorState:        "unsupported",
		LTCGeneratorReason:       "not exercised by this test",
	}

	store := nodeaudio.NewStore()
	store.Put("node-01", payload, now)
	collector := nodeaudio.New(store)
	obs, complete := collector.Poll(ctx)
	if !complete {
		t.Fatal("collector.Poll reported incomplete")
	}

	stateB, reasonB, ok := findLTCClaim(obs, "show-b")
	if !ok {
		t.Fatal("no audio_session.ltc.claim.state observation found for show-b")
	}
	if stateB != "refused" {
		t.Fatalf("show-b's coordinator-surface LTC claim state = %v, want %q -- the refusal is not visible off the node's own log", stateB, "refused")
	}
	reasonStr, ok := reasonB.(string)
	if !ok || reasonStr == "" {
		t.Fatalf("show-b's LTC claim reason = %v (%T), want a non-empty string naming the holding session", reasonB, reasonB)
	}
	if !strings.Contains(reasonStr, "show-a") {
		t.Fatalf("show-b's LTC claim reason = %q, want it to name the holding session show-a", reasonStr)
	}

	stateA, _, ok := findLTCClaim(obs, "show-a")
	if !ok {
		t.Fatal("no audio_session.ltc.claim.state observation found for show-a")
	}
	if stateA != "held" {
		t.Fatalf("show-a's coordinator-surface LTC claim state = %v, want %q", stateA, "held")
	}
}

// audioSessionLTCClaimStateSignal and audioSessionLTCClaimReasonSignal are
// this test's own independent transcription of the reserved spellings
// docs/build/IDENTIFIER-REGISTER.md pins for Lane 18a (nodeaudio.
// SignalSessionLTCClaimState/Reason once shipped) — literal, not the
// package constant, so this test still COMPILES against the unmodified
// tree (where that constant does not exist yet) and fails on the
// assertion instead of a build error, which is the more direct
// reproduction of "the refusal never reaches this surface."
const (
	audioSessionLTCClaimStateSignal  = observation.SignalID("audio_session.ltc.claim.state")
	audioSessionLTCClaimReasonSignal = observation.SignalID("audio_session.ltc.claim.reason")
)

// findLTCClaim locates sessionID's audio_session.ltc.claim.state/.reason
// among obs, returning the state value, the reason value (nil if the
// signal was not_collected), and whether the state observation was
// found at all.
func findLTCClaim(obs []observation.Observation, sessionID string) (state any, reason any, found bool) {
	var stateVal, reasonVal any
	stateFound := false
	for _, o := range obs {
		if o.Resource.Kind != observation.ResourceAudioSession || o.Resource.ID != sessionID {
			continue
		}
		switch o.Signal {
		case audioSessionLTCClaimStateSignal:
			stateVal = o.Value
			stateFound = true
		case audioSessionLTCClaimReasonSignal:
			reasonVal = o.Value
		}
	}
	return stateVal, reasonVal, stateFound
}

// writeAudioClaimTestAsset writes a real asset file this test's
// [fixedAudioDecoder] can be pointed at, returning a [pkgaudio.MediaRef]
// with a genuine content hash -- mirrors internal/agent/audio's own
// writeTestAsset helper, reproduced here because that one is unexported
// to package audio.
func writeAudioClaimTestAsset(t *testing.T, dir, filename, assetID string, content []byte) pkgaudio.MediaRef {
	t.Helper()
	hash := writeAssetFixture(t, dir, filename, content)
	return pkgaudio.MediaRef{
		AssetID: assetID, ContentHash: hash,
		SizeBytes: int64(len(content)), RuntimeFilename: filename,
	}
}
