//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/paho"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// ADR-008 constraint 6 ("the coordinator is never in
// the real-time timing or media path... a running show must survive
// coordinator loss — and broker loss") applies to a running local audio
// session exactly as it applies to a running FPP playlist. This file
// proves it against a real showmesh-agent subprocess and a real
// Mosquitto container that is actually stopped mid-session, not merely
// inspected from the source: internal/agent/audio.Manager.RunWatcher has
// no MQTT dependency by construction, and that is what turns the claim
// into evidence rather than an architecture diagram.
//
// The proof shape: start a session against a short real asset, confirm
// it reaches Playing, stop the broker container entirely, wait past the
// asset's own duration (so natural completion — driven only by
// RunWatcher's local ticker and the wall clock — can happen with no
// broker reachable), restart the broker, and read the FIRST audio report
// the agent publishes afterward. If that report already shows the
// session Completed, the transition happened locally, during the
// outage, because nothing else could have driven it.

// writeShortWAV writes a real, valid, always-non-silent one-channel PCM
// WAV file of durationSeconds length and returns its content and the
// sha256 identity ProbeAsset's checkIdentity compares against
// pkg/audio.MediaRef.ContentHash (ADR-028).
func writeShortWAV(t *testing.T, path string, durationSeconds float64) (content []byte, contentHash string) {
	t.Helper()
	const sampleRate = 8000
	const bitsPerSample = 16
	const channels = 1
	numSamples := int(durationSeconds * sampleRate)
	byteRate := sampleRate * channels * bitsPerSample / 8
	blockAlign := channels * bitsPerSample / 8
	data := make([]byte, numSamples*blockAlign)
	for i := 0; i < numSamples; i++ {
		v := int16(((i * 440 * 4) % sampleRate) - sampleRate/2)
		data[2*i] = byte(v)
		data[2*i+1] = byte(v >> 8)
	}

	buf := make([]byte, 0, 44+len(data))
	buf = append(buf, []byte("RIFF")...)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(36+len(data)))
	buf = append(buf, []byte("WAVEfmt ")...)
	buf = binary.LittleEndian.AppendUint32(buf, 16)
	buf = binary.LittleEndian.AppendUint16(buf, 1)
	buf = binary.LittleEndian.AppendUint16(buf, uint16(channels))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(sampleRate))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(byteRate))
	buf = binary.LittleEndian.AppendUint16(buf, uint16(blockAlign))
	buf = binary.LittleEndian.AppendUint16(buf, bitsPerSample)
	buf = append(buf, []byte("data")...)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(data)))
	buf = append(buf, data...)

	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("write WAV fixture: %v", err)
	}
	sum := sha256.Sum256(buf)
	return buf, "sha256:" + hex.EncodeToString(sum[:])
}

func applySessionCmd(nodeID, commandID, sessionID, assetID, contentHash, filename string, sizeBytes int64) mqttproto.CmdPayload {
	return mqttproto.CmdPayload{
		CommandID:      commandID,
		IdempotencyKey: commandID,
		Action:         "audio.session.apply",
		Target:         mqttproto.CmdTarget{Kind: "node", ID: nodeID},
		Params: map[string]any{
			"sessionId":    sessionID,
			"invocationId": commandID,
			"revision":     1,
			"media": map[string]any{
				"assetId":     assetID,
				"contentHash": contentHash,
				"filename":    filename,
				"sizeBytes":   sizeBytes,
			},
		},
		Issuer:             mqttproto.CmdIssuer{PrincipalID: "test-principal", PrincipalName: "integration-test"},
		ConfirmationMethod: "evidence",
	}
}

func startSessionCmd(nodeID, commandID, sessionID string) mqttproto.CmdPayload {
	return mqttproto.CmdPayload{
		CommandID:      commandID,
		IdempotencyKey: commandID,
		Action:         "audio.session.start",
		Target:         mqttproto.CmdTarget{Kind: "node", ID: nodeID},
		Params: map[string]any{
			"sessionId":    sessionID,
			"invocationId": commandID,
			"revision":     2,
		},
		Issuer:             mqttproto.CmdIssuer{PrincipalID: "test-principal", PrincipalName: "integration-test"},
		ConfirmationMethod: "evidence",
	}
}

// audioReportSubscriber records every showmesh.node.audio/v1 message a
// subscribed raw client receives and exposes the latest
// [mqttproto.AudioSessionReport] per session id — the surface this test
// reads to tell whether a transition happened locally during the outage.
type audioReportSubscriber struct {
	mu     sync.Mutex
	latest map[string]mqttproto.AudioSessionReport
}

func newAudioReportSubscriber() *audioReportSubscriber {
	return &audioReportSubscriber{latest: make(map[string]mqttproto.AudioSessionReport)}
}

func (w *audioReportSubscriber) onPublish(pr paho.PublishReceived) (bool, error) {
	if pr.Packet == nil {
		return true, nil
	}
	env, err := mqttproto.DecodeEnvelope(pr.Packet.Payload)
	if err != nil || env.Schema != mqttproto.SchemaNodeAudioV1 {
		return true, nil
	}
	p, err := mqttproto.DecodeAudioPayload(env)
	if err != nil {
		return true, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, s := range p.Sessions {
		w.latest[s.SessionID] = s
	}
	return true, nil
}

func (w *audioReportSubscriber) latestFor(sessionID string) (mqttproto.AudioSessionReport, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s, ok := w.latest[sessionID]
	return s, ok
}

// subscribeAudioReports connects a raw client as "coordinator" (the same
// broker role startCmdClient uses, which reads every node's observed/*
// topics) and subscribes it to nodeID's observed/audio topic.
func subscribeAudioReports(t *testing.T, nodeID string) *audioReportSubscriber {
	t.Helper()
	if testMQTTCoordinatorUsername == "" {
		t.Fatalf("no MQTT broker credential available (%s is unset) — run via `make test-integration`", envTestMQTTCoordinatorUsername)
	}
	cli := rawConnect(t, testMQTTCoordinatorUsername, testMQTTCoordinatorPassword)

	w := newAudioReportSubscriber()
	cli.AddOnPublishReceived(w.onPublish)

	topic, err := mqttproto.ObservedTopic(nodeID, "audio")
	if err != nil {
		t.Fatalf("ObservedTopic: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sa, err := cli.Subscribe(ctx, &paho.Subscribe{
		Subscriptions: []paho.SubscribeOptions{{Topic: topic, QoS: 1}},
	})
	if err != nil {
		t.Fatalf("SUBSCRIBE %s: %v", topic, err)
	}
	for i, rc := range sa.Reasons {
		if rc >= 0x80 {
			t.Fatalf("SUBSCRIBE %s rejected: index %d, reason code %d", topic, i, rc)
		}
	}
	return w
}

// TestLocalAudioSessionSurvivesBrokerLoss proves ADR-019's failure-containment intent: a
// running local session must continue through coordinator/broker loss
// for media already present, and the FIRST evidence available once the
// broker returns must already show what happened locally while it was
// gone — not a resumed-from-scratch session, not a session frozen at the
// moment of the outage.
func TestLocalAudioSessionSurvivesBrokerLoss(t *testing.T) {
	requireBroker(t)
	if mosquittoContainer == "" {
		t.Skipf("%s is not set, so this test cannot stop/start the broker container; run via `make test-integration`", envMosquittoContainer)
	}

	// A fast report cadence so "the next report after the broker returns"
	// is a bounded wait rather than the 15s production default.
	t.Setenv(envAudioReportInterval, "1s")

	assetDir := t.TempDir()
	const filename = "clip.wav"
	// Short enough that natural completion happens well within the
	// broker-down window below, long enough that the pre-outage report
	// still observes it Playing rather than already Completed.
	content, contentHash := writeShortWAV(t, filepath.Join(assetDir, filename), 2.0)

	nodeID := "audio-node-" + uniqueSuffix()
	startAgent(t, agentConfig{nodeID: nodeID, assetDir: assetDir})

	cli, _ := startCmdClient(t, nodeID)
	audioSub := subscribeAudioReports(t, nodeID)

	const sessionID = "show-session-1"
	dispatchCmd(t, cli, nodeID, applySessionCmd(nodeID, "cmd-apply-"+uniqueSuffix(), sessionID, "clip-1", contentHash, filename, int64(len(content))))
	dispatchCmd(t, cli, nodeID, startSessionCmd(nodeID, "cmd-start-"+uniqueSuffix(), sessionID))

	// Confirm the session actually reached Playing before the broker ever
	// goes away — otherwise a later "Completed" would prove nothing.
	waitFor(t, 10*time.Second, 200*time.Millisecond, func() bool {
		p, ok := audioSub.latestFor(sessionID)
		return ok && p.State == "playing"
	}, "session to reach state \"playing\" before the broker is stopped")

	stopBroker(t)
	t.Cleanup(func() { startBroker(t) })

	// The asset is 2s; RunWatcher ticks every 500ms with no MQTT
	// dependency (internal/agent/agent.go's audioSessionWatchInterval).
	// 4s with the broker down covers natural completion with margin,
	// entirely on the agent's own local clock.
	time.Sleep(4 * time.Second)

	startBroker(t)

	// The first report to arrive after the broker returns must already
	// show Completed — proving the transition happened locally, during
	// the outage, since nothing else could have driven it while the
	// broker was down.
	waitFor(t, 30*time.Second, 200*time.Millisecond, func() bool {
		p, ok := audioSub.latestFor(sessionID)
		return ok && p.State == "completed"
	}, "session to already report state \"completed\" on the first report after the broker returns")
}
