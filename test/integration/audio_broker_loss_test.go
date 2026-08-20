//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// waitForBrokerTCPUp blocks until brokerURL accepts a bare TCP connection,
// or fails t after timeout. `docker start` returns as soon as the
// container process is launched, not once Mosquitto is actually listening
// again, so a dial issued immediately after it can still race a listener
// that has not bound yet.
func waitForBrokerTCPUp(t *testing.T, timeout time.Duration) {
	t.Helper()
	u, err := url.Parse(brokerURL)
	if err != nil {
		t.Fatalf("parse broker URL %q: %v", brokerURL, err)
	}
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", u.Host, time.Second)
		if err == nil {
			_ = conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("broker at %s did not accept a TCP connection within %s: %v", u.Host, timeout, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// isolatedAudioBrokerImage is pinned to match deploy/docker-compose.yml's
// mosquitto.image and scripts/test-integration.sh's own pin, so this
// broker never drifts onto a version the reference deployment does not
// run.
const isolatedAudioBrokerImage = "eclipse-mosquitto:2.0.22"

// startIsolatedAudioBroker gives this test its own Mosquitto container and
// port, seeded with the exact shipped deploy/mosquitto/mosquitto.conf and
// acl.conf (ADR-024 decision 10), rather than the shared broker
// scripts/test-integration.sh starts for the rest of this package. That
// shared broker is depended on by every other test in this package; this
// one needs to stop and start a broker container for real, and doing that
// to the shared instance is what quarantined this test in the first place
// (see this test's own doc comment).
//
// It overrides the package-level brokerURL/mosquittoContainer/
// testMQTTCoordinatorUsername/testMQTTCoordinatorPassword for the rest of
// this test's run, so every existing helper (startAgent, startCmdClient,
// stopBroker, startBroker, subscribeAudioReports, ...) drives this
// container without any change of its own, and restores the previous
// values via t.Cleanup so no later test in this package (which does not
// run in parallel with this one — see provisionMu's doc comment) is
// affected.
func startIsolatedAudioBroker(t *testing.T) {
	t.Helper()
	requireBroker(t)
	if err := exec.Command("docker", "info").Run(); err != nil {
		skipOrFatalDependency(t, depBroker, "docker is not usable, so this test cannot start its own isolated broker: %v", err)
	}

	root, err := moduleRoot()
	if err != nil {
		t.Fatalf("moduleRoot: %v", err)
	}

	containerName := "showmesh-test-mosquitto-audio-brokerloss-" + uniqueSuffix()
	port := findFreePort(t)
	const username = "coordinator"
	password := "test-" + username + "-" + uniqueSuffix()

	// In case a previous run of this test was interrupted before its own
	// cleanup ran, matching scripts/test-integration.sh's identical
	// precaution for the shared container.
	_ = exec.Command("docker", "rm", "-f", containerName).Run()

	createOut, err := exec.Command("docker", "create",
		"--name", containerName,
		"-p", fmt.Sprintf("%d:1883", port),
		"-v", root+"/deploy/mosquitto/mosquitto.conf:/mosquitto/config/mosquitto.conf:ro",
		isolatedAudioBrokerImage,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("docker create %s: %v\n%s", containerName, err, createOut)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", containerName).Run() })

	// password_file is required (mosquitto refuses to start with
	// allow_anonymous false and no password_file present), so it is seeded
	// via `docker cp` into the created-but-not-started container, exactly
	// like scripts/test-integration.sh's identical "seed via docker cp
	// before start" technique.
	seedPasswd, err := os.CreateTemp("", "showmesh-audio-brokerloss-passwd-*")
	if err != nil {
		t.Fatalf("create temp passwd seed file: %v", err)
	}
	seedPasswdPath := seedPasswd.Name()
	_ = seedPasswd.Close()
	defer os.Remove(seedPasswdPath)

	if out, err := exec.Command("docker", "run", "--rm",
		"-v", seedPasswdPath+":/out/passwd",
		isolatedAudioBrokerImage,
		"mosquitto_passwd", "-b", "-c", "/out/passwd", username, password,
	).CombinedOutput(); err != nil {
		t.Fatalf("mosquitto_passwd -b -c ... %s: %v\n%s", username, err, out)
	}
	if out, err := exec.Command("docker", "cp", seedPasswdPath, containerName+":/mosquitto/config/passwd").CombinedOutput(); err != nil {
		t.Fatalf("docker cp passwd into %s: %v\n%s", containerName, err, out)
	}

	// The committed acl.conf, unedited: this test only ever authenticates
	// as the fixed "coordinator" role and its own provisioned per-node
	// agent credential (via provisionAgentCredential, unchanged), so it
	// needs no test-only ACL stanza the way the shared broker's burst
	// publisher does.
	if out, err := exec.Command("docker", "cp", root+"/deploy/mosquitto/acl.conf", containerName+":/mosquitto/config/acl.generated.conf").CombinedOutput(); err != nil {
		t.Fatalf("docker cp acl.conf into %s: %v\n%s", containerName, err, out)
	}

	if out, err := exec.Command("docker", "start", containerName).CombinedOutput(); err != nil {
		t.Fatalf("docker start %s: %v\n%s", containerName, err, out)
	}

	prevBrokerURL, prevContainer := brokerURL, mosquittoContainer
	prevUsername, prevPassword := testMQTTCoordinatorUsername, testMQTTCoordinatorPassword
	brokerURL = fmt.Sprintf("tcp://localhost:%d", port)
	mosquittoContainer = containerName
	testMQTTCoordinatorUsername = username
	testMQTTCoordinatorPassword = password
	t.Cleanup(func() {
		brokerURL, mosquittoContainer = prevBrokerURL, prevContainer
		testMQTTCoordinatorUsername, testMQTTCoordinatorPassword = prevUsername, prevPassword
	})

	waitForBrokerTCPUp(t, 15*time.Second)
}

// TestLocalAudioSessionSurvivesBrokerLoss proves ADR-019's failure-containment intent: a
// running local session must continue through coordinator/broker loss
// for media already present, and the FIRST evidence available once the
// broker returns must already show what happened locally while it was
// gone — not a resumed-from-scratch session, not a session frozen at the
// moment of the outage.
func TestLocalAudioSessionSurvivesBrokerLoss(t *testing.T) {
	startIsolatedAudioBroker(t)

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
	agent := startAgent(t, agentConfig{nodeID: nodeID, assetDir: assetDir})

	cli, w := startCmdClient(t, nodeID)
	// The agent's SUBSCRIBE to its own cmd topic races this function's
	// return (see awaitAgentReceivingCommands's doc comment); without this
	// barrier, apply/start below can land in that window and be silently
	// dropped, never reaching the agent at all.
	awaitAgentReceivingCommands(t, cli, w, nodeID)
	audioSub := subscribeAudioReports(t, nodeID)

	const sessionID = "show-session-1"
	applyCmdID := "cmd-apply-" + uniqueSuffix()
	dispatchCmd(t, cli, nodeID, applySessionCmd(nodeID, applyCmdID, sessionID, "clip-1", contentHash, filename, int64(len(content))))
	// Each inbound command runs in its own goroutine (mqtt.go's
	// registerCommandHandling), so start must not fire until apply's own
	// result confirms it has already run.
	waitForResult(t, w, applyCmdID, 10*time.Second)
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

	// The audio path does not stop merely because MQTT disconnected: the
	// agent process is still running (nothing in internal/agent's report
	// loops or RunWatcher exits or panics on a broker outage), and its own
	// log shows runAudioReport's ticker kept firing and kept trying to
	// publish, failing on every attempt only because the broker is
	// unreachable (audioreport.go's publishAudioPayload logs exactly this
	// and lets the ticker continue rather than stopping).
	select {
	case <-agent.waitDone:
		t.Fatalf("agent process for node %s exited during the broker outage; the audio path must survive broker loss", nodeID)
	default:
	}
	if !strings.Contains(agent.logs.String(), "audio report publish failed; will retry next tick") {
		t.Fatalf("agent %s produced no evidence of a continued audio report attempt during the broker outage; log:\n%s", nodeID, agent.logs.String())
	}

	startBroker(t)
	waitForBrokerTCPUp(t, 15*time.Second)

	// audioSub's own raw client (rawConnect) is a bare TCP session with no
	// reconnect logic of its own — unlike the agent under test, which
	// reconnects via autopaho. Stopping the broker container severs that
	// TCP session for good, so the ORIGINAL audioSub can never observe
	// anything published after the restart: reusing it here would make
	// this test block until its own 30s timeout regardless of what the
	// agent does, which proved nothing about the agent and everything
	// about a stale observer. A fresh subscription, opened only once the
	// broker is confirmed reachable again, is what actually watches for
	// the evidence this test is about.
	audioSub = subscribeAudioReports(t, nodeID)

	// The first report to arrive after the broker returns must already
	// show Completed — proving the transition happened locally, during
	// the outage, since nothing else could have driven it while the
	// broker was down. This is also "current telemetry is present again":
	// this subscription was only opened after the broker was confirmed
	// reachable, so any report it receives at all is fresh, post-outage
	// evidence, never a stale or replayed one.
	var final mqttproto.AudioSessionReport
	waitFor(t, 30*time.Second, 200*time.Millisecond, func() bool {
		p, ok := audioSub.latestFor(sessionID)
		if !ok || p.State != "completed" {
			return false
		}
		final = p
		return true
	}, "session to already report state \"completed\" on the first report after the broker returns")

	// Desired and observed state reconcile: the start command carried
	// revision 2 (startSessionCmd), and the completed session's own
	// DesiredRevision must show the agent settled on it rather than being
	// stuck on the outage-time value.
	if final.DesiredRevision != 2 {
		t.Fatalf("completed session reports DesiredRevision = %d, want 2 (desired/observed did not reconcile)", final.DesiredRevision)
	}
}
