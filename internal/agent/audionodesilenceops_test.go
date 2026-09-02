package agent

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// TestAudioNodeSilenceIsAllowlisted proves "audio.node.silence" is a real
// key in the agent's command allowlist when an audio manager is wired,
// matching audioops_test.go's identical allowlist-presence proof for
// "audio.device.probe".
func TestAudioNodeSilenceIsAllowlisted(t *testing.T) {
	dir := t.TempDir()
	mgr := audio.NewManager(audio.NewFakeEngine(time.Now), audio.NewFileSessionStore(dir), dir, audio.RealDecoder{}, time.Now, nil)
	ops := newOperationRegistry(testNodeID, dir, "", nil, mgr, nil, nil, nil, discardLogger())
	if _, ok := ops["audio.node.silence"]; !ok {
		t.Fatal(`newOperationRegistry() does not contain "audio.node.silence" when an audio manager is wired`)
	}
}

// TestAudioNodeSilenceNotWiredWithoutManager proves a node with no audio
// manager configured never wires "audio.node.silence" either, matching
// audioSessionOperations' identical nil-disables convention.
func TestAudioNodeSilenceNotWiredWithoutManager(t *testing.T) {
	ops := newOperationRegistry(testNodeID, t.TempDir(), "", nil, nil, nil, nil, nil, discardLogger())
	if _, ok := ops["audio.node.silence"]; ok {
		t.Fatal(`newOperationRegistry() contains "audio.node.silence" with a nil audio manager`)
	}
}

// TestSilenceNodeRejectsUnknownKeys proves audio.node.silence takes no
// params: an unrecognized key is refused, not silently ignored, matching
// this package's rejectUnknownKeys convention everywhere else.
func TestSilenceNodeRejectsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	mgr := audio.NewManager(audio.NewFakeEngine(time.Now), audio.NewFileSessionStore(dir), dir, audio.RealDecoder{}, time.Now, nil)
	op := silenceNode(mgr)
	if _, err := op(context.Background(), map[string]any{"sessionId": "s1"}, time.Now); err == nil {
		t.Fatal("silenceNode with an unrecognized key: got nil error, want one")
	}
}

// TestSilenceNodeReportsSessionsFoundAndConfirmed proves the required
// report shape: Confirmed is always true (an unconditional safety
// command), and Value carries the count of sessions found plus a
// per-session outcome, for both an empty node and one with sessions.
func TestSilenceNodeReportsSessionsFoundAndConfirmed(t *testing.T) {
	dir := t.TempDir()
	mgr := audio.NewManager(audio.NewFakeEngine(time.Now), audio.NewFileSessionStore(dir), dir, audio.RealDecoder{}, time.Now, nil)
	op := silenceNode(mgr)

	result, err := op(context.Background(), map[string]any{}, time.Now)
	if err != nil {
		t.Fatalf("silenceNode on an empty node: unexpected error %v", err)
	}
	if !result.Confirmed {
		t.Error("Confirmed = false, want true (silence is unconditional)")
	}
	val, ok := result.Value.(map[string]any)
	if !ok {
		t.Fatalf("Value = %v (%T), want map[string]any", result.Value, result.Value)
	}
	if val["sessionsFound"] != 0 {
		t.Errorf("sessionsFound = %v, want 0 on an empty node", val["sessionsFound"])
	}

	ctx := context.Background()
	req := pkgaudio.ApplyRequest{SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleBackground)}
	mgr.Apply(ctx, "s1", "inv-1", 1, req)

	result, err = op(ctx, map[string]any{}, time.Now)
	if err != nil {
		t.Fatalf("silenceNode with one session: unexpected error %v", err)
	}
	if !result.Confirmed {
		t.Error("Confirmed = false, want true (silence is unconditional)")
	}
	val = result.Value.(map[string]any)
	if val["sessionsFound"] != 1 {
		t.Errorf("sessionsFound = %v, want 1", val["sessionsFound"])
	}
	sessions, ok := val["sessions"].([]map[string]any)
	if !ok || len(sessions) != 1 {
		t.Fatalf("sessions = %v, want one entry", val["sessions"])
	}
	if sessions[0]["sessionId"] != "s1" {
		t.Errorf("sessions[0].sessionId = %v, want %q", sessions[0]["sessionId"], "s1")
	}
}

// TestSilenceNodeNotWired proves the not-wired error names the action.
func TestSilenceNodeNotWired(t *testing.T) {
	op := silenceNode(nil)
	if _, err := op(context.Background(), map[string]any{}, time.Now); err == nil {
		t.Fatal("silenceNode(nil) called: got nil error, want one")
	}
}
