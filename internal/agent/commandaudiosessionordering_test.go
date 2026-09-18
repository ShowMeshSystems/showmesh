package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file pins, at the command dispatch layer, the same guarantee
// internal/agent/audio/crosssessionordering_test.go pins at the Manager
// layer: a slow audio.session.prepare on one session must not hold up an
// audio.session.pause command for a different session on the same node.
// mqtt.go dispatches every inbound PUBLISH by spawning
// [CommandHandler.HandleMessage] on its own goroutine
// (registerCommandHandling's "go cmdHandler.HandleMessage(...)"), so this
// test reproduces that exact shape rather than calling HandleMessage
// synchronously.

func audioSessionCmd(action, sessionID, invocationID string, revision int, extraParams map[string]any) mqttproto.CmdPayload {
	params := map[string]any{
		"sessionId":    sessionID,
		"invocationId": invocationID,
		"revision":     revision,
	}
	for k, v := range extraParams {
		params[k] = v
	}
	return mqttproto.CmdPayload{
		CommandID:          "cmd-" + invocationID,
		IdempotencyKey:     "idem-" + invocationID,
		Action:             action,
		Target:             mqttproto.CmdTarget{Kind: "node", ID: testNodeID},
		Params:             params,
		Issuer:             mqttproto.CmdIssuer{PrincipalID: "principal-1", PrincipalName: "operator"},
		ConfirmationMethod: confirmationMethodEvidence,
	}
}

func mediaRefParams(ref pkgaudio.MediaRef) map[string]any {
	return map[string]any{
		"media": map[string]any{
			"assetId":     ref.AssetID,
			"contentHash": ref.ContentHash,
			"sizeBytes":   float64(ref.SizeBytes),
			"filename":    ref.RuntimeFilename,
		},
	}
}

// waitForPublish blocks until pub has recorded at least n calls, failing
// the test after 5s -- used for the setup commands this test issues
// sequentially, before the concurrent phase it actually measures.
func waitForPublish(t *testing.T, pub *fakePublisher, n int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if len(pub.snapshot()) >= n {
			return
		}
		select {
		case <-pub.notify:
		case <-deadline:
			t.Fatalf("timed out waiting for %d publish(es)", n)
		}
	}
}

func TestHandleMessagePrepareOnOneSessionDoesNotDelayPauseResultForAnother(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	dir := t.TempDir()
	engine := audio.NewFakeEngine(clock.now)
	mgr := audio.NewManager(engine, audio.NewFileSessionStore(dir), dir, fakeAssetDecoder{}, clock.now, nil)
	h := newCommandHandler(testNodeID, dir, "", nil, nil, nil, mgr, nil, nil, nil, nil, nil, clock.now, discardLogger())

	assetA := writeTestAudioAsset(t, dir, "a.wav", "asset-a", []byte("aaaaaaaa"))
	assetB := writeTestAudioAsset(t, dir, "b.wav", "asset-b", []byte("bbbbbbbb"))

	// Matches mqtt.go's registerCommandHandling: one goroutine per inbound
	// PUBLISH, never called synchronously.
	send := func(cmd mqttproto.CmdPayload, pub *fakePublisher) {
		topic, payload := buildCmdMessage(t, clock, cmd)
		go h.HandleMessage(context.Background(), pub, topic, payload)
	}

	applyPubA := newFakePublisher()
	send(audioSessionCmd(string(pkgaudio.OperationSessionApply), "session-a", "apply-a", 1, mediaRefParams(assetA)), applyPubA)
	waitForPublish(t, applyPubA, 1)

	applyPubB := newFakePublisher()
	send(audioSessionCmd(string(pkgaudio.OperationSessionApply), "session-b", "apply-b", 1, mediaRefParams(assetB)), applyPubB)
	waitForPublish(t, applyPubB, 1)

	prepPubB := newFakePublisher()
	send(audioSessionCmd(string(pkgaudio.OperationSessionPrepare), "session-b", "prep-b", 2, nil), prepPubB)
	waitForPublish(t, prepPubB, 1)

	startPubB := newFakePublisher()
	send(audioSessionCmd(string(pkgaudio.OperationSessionStart), "session-b", "start-b", 3, nil), startPubB)
	waitForPublish(t, startPubB, 1)

	entered := make(chan struct{})
	unblock := make(chan struct{})
	engine.LoadHook = func(handle audio.EngineHandle) {
		if !strings.HasPrefix(string(handle), "session-a/") {
			return
		}
		close(entered)
		<-unblock
	}

	prepPubA := newFakePublisher()
	send(audioSessionCmd(string(pkgaudio.OperationSessionPrepare), "session-a", "prep-a", 2, nil), prepPubA)

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("session A's prepare never reached its slow Load")
	}

	pausePubB := newFakePublisher()
	send(audioSessionCmd(string(pkgaudio.OperationSessionPause), "session-b", "pause-b", 4, nil), pausePubB)

	select {
	case <-pausePubB.notify:
	case <-time.After(2 * time.Second):
		t.Fatal("audio.session.pause result for session B did not publish while session A's prepare was still in flight -- cross-session blocking")
	}

	close(unblock)
	select {
	case <-prepPubA.notify:
	case <-time.After(5 * time.Second):
		t.Fatal("session A's prepare never completed after being unblocked")
	}
}
