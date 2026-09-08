package agent

import (
	"context"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// wireCmdParams round-trips params through a real [mqttproto.CmdPayload]
// encode/decode, exactly as a command arrives at a node over MQTT, rather
// than a Go map handed to an op directly, which never exercises the
// wire's own JSON number decoding at all.
func wireCmdParams(t *testing.T, action string, params map[string]any) map[string]any {
	t.Helper()
	cmd := mqttproto.CmdPayload{
		CommandID:          "cmd-" + action,
		IdempotencyKey:     "idem-" + action,
		Action:             action,
		Target:             mqttproto.CmdTarget{Kind: "node", ID: "node-1"},
		Params:             params,
		Issuer:             mqttproto.CmdIssuer{PrincipalID: "coordinator"},
		ConfirmationMethod: "evidence",
	}
	env, err := mqttproto.NewCmdEnvelope(time.Now, "node-1", cmd)
	if err != nil {
		t.Fatalf("NewCmdEnvelope(%s): %v", action, err)
	}
	decoded, err := mqttproto.DecodeCmdPayload(env)
	if err != nil {
		t.Fatalf("DecodeCmdPayload(%s): %v", action, err)
	}
	return decoded.Params
}

func sessionOutcomeReason(t *testing.T, res OperationResult) (outcome, reason string) {
	t.Helper()
	m, ok := res.Value.(map[string]any)
	if !ok {
		t.Fatalf("OperationResult.Value = %#v, want map[string]any", res.Value)
	}
	outcome, _ = m["outcome"].(string)
	reason, _ = m["reason"].(string)
	return outcome, reason
}

// TestPrepareAheadAudioAcceptsAdjacentNanosecondRevisions is the
// prepare-ahead audio staging regression test.
//
// pkg/cueactivation.AudioSessionRevision derives the prepare-ahead
// apply/prepare pair as consecutive nanosecond-epoch integers around
// 1.8e18, past float64's 2^53 exact-integer ceiling, where consecutive
// float64 values are 256 apart. 1788886007157000005 and 1788886007157000006
// (the values observed on rehearsal-stack main 6dfecdd's commands table)
// both decode to the same float64 off the wire before the fix, so the
// prepare that must exceed the apply's revision compared equal instead
// and was refused stale_revision even though the coordinator sent a
// strictly greater value.
func TestPrepareAheadAudioAcceptsAdjacentNanosecondRevisions(t *testing.T) {
	const (
		applyRevision   = uint64(1788886007157000005)
		prepareRevision = uint64(1788886007157000006)
		staging         = "cue-activation:prepare-staging"
	)
	if applyRevision == prepareRevision {
		t.Fatal("test setup: the two revisions must be distinct")
	}

	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 23, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clock)
	ops := audioSessionOperations(mgr)

	hash := writeAssetFixture(t, dir, "prepare-ahead.wav", []byte("pretend this is wav audio content"))

	applyOp := ops[string(pkgaudio.OperationSessionApply)]
	applyParams := wireCmdParams(t, "audio.session.apply", map[string]any{
		"sessionId": staging, "invocationId": "act-1:prepare-ahead-apply", "revision": applyRevision,
		"sourceRole": string(pkgaudio.SourceRoleShow),
		"media":      map[string]any{"assetId": "prepare-ahead-asset", "contentHash": hash, "filename": "prepare-ahead.wav"},
	})
	applyResult, err := applyOp(context.Background(), applyParams, clock.now)
	if err != nil {
		t.Fatalf("audio.session.apply: %v", err)
	}
	if outcome, reason := sessionOutcomeReason(t, applyResult); outcome != string(pkgaudio.OutcomePosition) {
		t.Fatalf("audio.session.apply outcome = %q reason = %q, want %q", outcome, reason, pkgaudio.OutcomePosition)
	}

	prepareOp := ops[string(pkgaudio.OperationSessionPrepare)]
	prepareParams := wireCmdParams(t, "audio.session.prepare", map[string]any{
		"sessionId": staging, "invocationId": "act-1:prepare-ahead-prepare", "revision": prepareRevision,
	})
	prepareResult, err := prepareOp(context.Background(), prepareParams, clock.now)
	if err != nil {
		t.Fatalf("audio.session.prepare: %v", err)
	}
	outcome, reason := sessionOutcomeReason(t, prepareResult)
	if outcome == string(pkgaudio.OutcomeRefused) && reason == pkgaudio.ReasonStaleRevision {
		t.Fatalf("audio.session.prepare(revision=%d) was refused stale_revision against apply(revision=%d): "+
			"the prepare's genuinely-greater revision collided with apply's onto the same float64 off the wire",
			prepareRevision, applyRevision)
	}
	if outcome != string(pkgaudio.OutcomePosition) {
		t.Fatalf("audio.session.prepare outcome = %q reason = %q, want %q", outcome, reason, pkgaudio.OutcomePosition)
	}
}
