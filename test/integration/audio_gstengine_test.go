//go:build integration

package integration

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is Track C phase 1b's vertical slice: a real HTTP-shaped
// command (here, dispatched directly over MQTT, matching every other
// test in this package's own convention — the coordinator's HTTP
// dispatch is proven separately in internal/coordinator/api) travels
// through a real Mosquitto broker, into a real showmesh-agent
// subprocess, into the real gstengine backend behind
// internal/agent/audio.SwitchableEngine, and produces a playback
// outcome that is NOT "unconfirmable". It uses [envGstAudioSinkOverride]
// ("fakesink") so no real audio device is opened, per this repository's
// own SHOWMESH_GST_DISCOVERER precedent.

// audioNodeConfigureCmd builds "audio.node.configure" exactly as
// internal/coordinator/audioconfigpush's own publish would, targeting a
// non-hardware sink: programRoute/ltcRoute name no real device (the
// fakesink override never reads them), only the channel/revision shape
// matters to the agent's own decode and to gstengine.Config.Validate.
func audioNodeConfigureCmd(nodeID, commandID string, revision int64) mqttproto.CmdPayload {
	return mqttproto.CmdPayload{
		CommandID:      commandID,
		IdempotencyKey: commandID,
		Action:         "audio.node.configure",
		Target:         mqttproto.CmdTarget{Kind: "node", ID: nodeID},
		Params: map[string]any{
			"programRoute":          "test-route",
			"ltcRoute":              "test-route",
			"programChannels":       []int{1, 2},
			"ltcChannel":            3,
			"clockDomain":           "test-domain",
			"clockDomainProvenance": "single interface, both routes on it",
			"revision":              revision,
		},
		Issuer:             mqttproto.CmdIssuer{PrincipalID: "test-principal", PrincipalName: "integration-test"},
		ConfirmationMethod: "evidence",
	}
}

// TestRealCommandReachesRealAudioEngine proves the vertical slice end to
// end: audio.node.configure delivers this node's output binding, which
// rebuilds internal/agent/audio.SwitchableEngine against the real
// gstengine backend; a subsequent audio.session.apply/start pair then
// reaches that real engine (never the fake one — see
// TestProductionNeverConstructsFakeEngine) and produces a genuine
// "confirmed" outcome, not "unconfirmable".
func TestRealCommandReachesRealAudioEngine(t *testing.T) {
	startIsolatedAudioBroker(t)
	t.Setenv(envGstAudioSinkOverride, "fakesink")

	assetDir := t.TempDir()
	const filename = "clip.wav"
	content, contentHash := writeShortWAV(t, filepath.Join(assetDir, filename), 2.0)

	nodeID := "audio-node-" + uniqueSuffix()
	agent := startAgent(t, agentConfig{nodeID: nodeID, assetDir: assetDir})

	cli, w := startCmdClient(t, nodeID)
	awaitAgentReceivingCommands(t, cli, w, nodeID)

	// Before any audio.node.configure, the real engine is wired but
	// unbound: node.audio.node_config_revision has never been reported
	// and the deliverable's own gate ("no binding yet, Available() false")
	// is exercised by internal/agent/audio's own unit tests
	// (TestSwitchableEngineUnboundReportsNoBinding); this test's job is
	// what happens once a binding arrives.
	configureCmdID := "cmd-audio-node-configure-" + uniqueSuffix()
	dispatchCmd(t, cli, nodeID, audioNodeConfigureCmd(nodeID, configureCmdID, 1))
	configureResult := waitForResult(t, w, configureCmdID, 15*time.Second)
	if configureResult.Outcome != mqttproto.OutcomeConfirmed {
		t.Fatalf("audio.node.configure outcome = %q, reason %q, want %q — agent log:\n%s",
			configureResult.Outcome, configureResult.Reason, mqttproto.OutcomeConfirmed, agent.logs.String())
	}

	// Replaying the SAME revision must be idempotent and still confirmed
	// — Deliverable 1's own requirement, exercised here against the real
	// running agent rather than only the unit-level audioBinding tests.
	replayCmdID := "cmd-audio-node-configure-replay-" + uniqueSuffix()
	dispatchCmd(t, cli, nodeID, audioNodeConfigureCmd(nodeID, replayCmdID, 1))
	replayResult := waitForResult(t, w, replayCmdID, 15*time.Second)
	if replayResult.Outcome != mqttproto.OutcomeConfirmed {
		t.Fatalf("replayed audio.node.configure (same revision) outcome = %q, reason %q, want %q",
			replayResult.Outcome, replayResult.Reason, mqttproto.OutcomeConfirmed)
	}

	const sessionID = "vertical-slice-session"
	applyCmdID := "cmd-apply-" + uniqueSuffix()
	dispatchCmd(t, cli, nodeID, applySessionCmd(nodeID, applyCmdID, sessionID, "clip-1", contentHash, filename, int64(len(content))))
	applyResult := waitForResult(t, w, applyCmdID, 10*time.Second)
	if applyResult.Outcome == mqttproto.OutcomeFailed || applyResult.Outcome == mqttproto.OutcomeRefused {
		t.Fatalf("audio.session.apply outcome = %q, reason %q, want a non-failure outcome — agent log:\n%s",
			applyResult.Outcome, applyResult.Reason, agent.logs.String())
	}

	startCmdID := "cmd-start-" + uniqueSuffix()
	dispatchCmd(t, cli, nodeID, startSessionCmd(nodeID, startCmdID, sessionID))
	startResult := waitForResult(t, w, startCmdID, 10*time.Second)

	// This is the deliverable's own bar: NOT unconfirmable. The real
	// gstengine backend against fakesink is expected to genuinely confirm
	// (Available() true, a real state transition to Playing), so this
	// also asserts the stronger, actually-observed outcome and fails with
	// the full evidence value and agent log if that stronger claim does
	// not hold, rather than silently accepting a weaker pass.
	if startResult.Outcome == mqttproto.OutcomeUnconfirmed {
		t.Fatalf("audio.session.start outcome = %q (unconfirmable-shaped), reason %q — a real command did NOT reach a working real engine; agent log:\n%s",
			startResult.Outcome, startResult.Reason, agent.logs.String())
	}
	if startResult.Outcome != mqttproto.OutcomeConfirmed {
		t.Fatalf("audio.session.start outcome = %q, reason %q, evidence %+v, want %q — agent log:\n%s",
			startResult.Outcome, startResult.Reason, startResult.Evidence, mqttproto.OutcomeConfirmed, agent.logs.String())
	}
	if startResult.Evidence == nil {
		t.Fatal("audio.session.start result carries no Evidence, want the real engine's own read-back")
	}
	value, ok := startResult.Evidence.Value.(map[string]any)
	if !ok {
		t.Fatalf("audio.session.start Evidence.Value = %#v (%T), want a map carrying \"outcome\"", startResult.Evidence.Value, startResult.Evidence.Value)
	}
	if value["outcome"] != "started" {
		t.Fatalf(`audio.session.start Evidence.Value["outcome"] = %v, want "started"`, value["outcome"])
	}

	t.Logf("real command reached the real audio engine: audio.session.start outcome=%q evidence=%+v", startResult.Outcome, startResult.Evidence)
}
