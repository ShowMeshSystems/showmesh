//go:build integration

package integration

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/paho"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file proves ADR-053 (weather delay) end to end across process
// boundaries: a real showmesh-coordinator binary, a real showmesh-agent
// binary (built with cgo, driven with the "fakesink" gstengine override,
// see envGstAudioSinkOverride's own doc comment one file over, never a
// build tag; there is no separate fake-engine build in this repository), a
// real Mosquitto broker, and [weatherDelayFPPStub], a small stand-in HTTP
// server playing an FPP player and its ShowMesh plugin's weather gate
// route. Every piece under test here (the coordinator's delay state and
// enforcement loop, the FPP plugin's weather gate contract, the node
// agent's alert session, the signed direct-HTTP start path, the power
// group heartbeat, and the night/cue refusals) was previously built and
// unit-tested in isolation on its own branch. Nothing before this file
// proved the pieces are wired together against real processes.
//
// Scenario 6 exercises POST /api/v1/weather-delay/cancel-night and the
// graceful night shutdown it schedules behind its own alert.

// weatherDelayAlertSessionIDForTest mirrors internal/agent/weatherdelayops.go's
// unexported weatherDelayAlertSessionIDForKind(weatherdelay.KindDelay)
// ("weatherdelay:alert:delay"). It cannot be imported (package agent, not
// exported), so this is the literal string a test observes on the wire via
// the node's own observed/audio report. Every scenario but 6 (cancel
// night) only ever exercises the delay kind, so this name stays the
// default; scenario 6 also uses weatherDelayCancelAlertSessionIDForTest for
// the cancel kind's own, separate session.
const weatherDelayAlertSessionIDForTest = "weatherdelay:alert:delay"

// weatherDelayCancelAlertSessionIDForTest mirrors
// weatherDelayAlertSessionIDForKind(weatherdelay.KindCancelNight)
// ("weatherdelay:alert:cancelNight"), the cancel-night alert's own session,
// distinct from the delay alert's: ADR-053 decision 1's delay-to-cancel
// change in place must never replace a playlist already loaded and playing
// on the delay alert's session.
const weatherDelayCancelAlertSessionIDForTest = "weatherdelay:alert:cancelNight"

// --- weatherDelayFPPStub: a stand-in FPP player plus its ShowMesh plugin ---

// weatherDelayFPPStub records every command it receives and answers the
// three routes the coordinator's FPP path actually calls: GET
// /api/fppd/status (the status collector), POST /api/command (the native
// FPP command dispatch, "Stop Now" among others), and the plugin's own
// weather gate route (fppcommand.WeatherGatePath) for both the write and
// the read. It never talks to a real fppd.
type weatherDelayFPPStub struct {
	srv *httptest.Server

	mu           sync.Mutex
	statusName   string
	gateClosed   bool
	gateRevision int64
	gateReads    int
	commands     []string
}

func newWeatherDelayFPPStub(t *testing.T) *weatherDelayFPPStub {
	t.Helper()
	s := &weatherDelayFPPStub{statusName: "idle"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/fppd/status", s.handleStatus)
	mux.HandleFunc("POST /api/command", s.handleCommand)
	mux.HandleFunc("POST /api/plugin-apis/showmesh/brightness/weather-gate", s.handleGateWrite)
	mux.HandleFunc("GET /api/plugin-apis/showmesh/brightness/weather-gate", s.handleGateRead)
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *weatherDelayFPPStub) url() string { return s.srv.URL }

func (s *weatherDelayFPPStub) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	statusName := s.statusName
	s.mu.Unlock()
	jsonEncode(w, map[string]any{
		"status_name": statusName,
		"mode_name":   "player",
		"multisync":   false,
		"volume":      70,
		"current_playlist": map[string]any{
			"playlist": "", "index": "0", "count": "0", "type": "",
		},
	})
}

func (s *weatherDelayFPPStub) handleCommand(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	s.commands = append(s.commands, body.Command)
	if body.Command == "Stop Now" {
		s.statusName = "idle"
	}
	s.mu.Unlock()
	jsonEncode(w, map[string]any{"status": "ok"})
}

// weatherGateRequest/weatherGateResponse mirror
// internal/coordinator/fppcommand/weathergate.go's own unexported wire
// types field for field, matching how the real plugin's contract composes
// (FPP-PLUGIN-COORDINATOR-CONTRACTS.md section 2.5): the plugin always
// applies a coordinator write, storing max(stored+1, revision), and
// effectiveOutputPercent is 0 whenever the gate is closed.
type weatherGateRequest struct {
	Closed   bool  `json:"closed"`
	Revision int64 `json:"revision"`
}

func (s *weatherDelayFPPStub) handleGateWrite(w http.ResponseWriter, r *http.Request) {
	var req weatherGateRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	s.mu.Lock()
	next := s.gateRevision + 1
	if req.Revision > next {
		next = req.Revision
	}
	s.gateRevision = next
	s.gateClosed = req.Closed
	closed, rev := s.gateClosed, s.gateRevision
	s.mu.Unlock()
	s.writeGateResponse(w, closed, rev)
}

func (s *weatherDelayFPPStub) handleGateRead(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.gateReads++
	closed, rev := s.gateClosed, s.gateRevision
	s.mu.Unlock()
	s.writeGateResponse(w, closed, rev)
}

func (s *weatherDelayFPPStub) writeGateResponse(w http.ResponseWriter, closed bool, rev int64) {
	percent := 100
	if closed {
		percent = 0
	}
	jsonEncode(w, map[string]any{
		"weatherGateClosed": closed, "weatherGateRevision": rev, "effectiveOutputPercent": percent,
	})
}

func jsonEncode(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// setStatusName flips what GET /api/fppd/status reports, simulating FPP's
// own scheduler (or an operator) starting or stopping playback outside
// ShowMesh's own accounting.
func (s *weatherDelayFPPStub) setStatusName(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statusName = name
}

func (s *weatherDelayFPPStub) currentStatusName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusName
}

// setGateClosed lets a test seed the gate's starting state directly
// (scenario 7, "held player": a gate that already reads closed before any
// delay this coordinator knows about).
func (s *weatherDelayFPPStub) setGateClosed(closed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gateRevision++
	s.gateClosed = closed
}

func (s *weatherDelayFPPStub) gateState() (closed bool, revision int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gateClosed, s.gateRevision
}

// gateReadCount is how many times the coordinator has READ the gate. A
// "never wrote" assertion needs it: without a read the coordinator had no
// occasion to write either, so silence over a window with no read proves
// nothing.
func (s *weatherDelayFPPStub) gateReadCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gateReads
}

func (s *weatherDelayFPPStub) commandCount(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.commands {
		if c == name {
			n++
		}
	}
	return n
}

// --- shared setup helpers ---

// nonLoopbackHost returns a real, non-loopback IPv4 address this host owns.
// internal/agent/advertise.go's resolveInboundListener deliberately refuses
// to report a loopback or link-local address as this node's own inbound
// listener (a coordinator on a different host could never dial it back),
// so the direct-HTTP fallback path (scenario 3) needs the agent's inbound
// listener bound to an address that survives that check, even though
// coordinator and agent are really the same host here.
func nonLoopbackHost(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatalf("list interface addresses: %v", err)
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() {
			continue
		}
		if ip4 := ipnet.IP.To4(); ip4 != nil {
			return ip4.String()
		}
	}
	t.Skip("no non-loopback IPv4 interface found on this host; the weather delay direct-HTTP fallback path needs one so the agent's own inbound-listener address is not rejected as loopback")
	return ""
}

// coordinatorSigningPublicKeyFile reads the coordinator's own persisted
// Ed25519 signing key from dataDir (internal/coordinator/signingkey.FileName,
// a 64-byte raw private key written after the coordinator's first
// successful start), derives the public half, and writes it base64-encoded
// to a fresh file a node agent can be pointed at via
// SHOWMESH_WEATHERDELAY_COORDINATOR_PUBLIC_KEY_PATH. There is no API or CLI
// surface that exports this key; a real deployment provisions it out of
// band (ADR-025), and this is this test's own equivalent of that step.
func coordinatorSigningPublicKeyFile(t *testing.T, dataDir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dataDir, "coordinator-signing-key"))
	if err != nil {
		t.Fatalf("read coordinator signing key: %v", err)
	}
	if len(raw) != ed25519.PrivateKeySize {
		t.Fatalf("coordinator signing key is %d bytes, want %d", len(raw), ed25519.PrivateKeySize)
	}
	pub := ed25519.PrivateKey(raw).Public().(ed25519.PublicKey)
	path := filepath.Join(t.TempDir(), "coordinator-public-key.b64")
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(pub)), 0o600); err != nil {
		t.Fatalf("write coordinator public key file: %v", err)
	}
	return path
}

// weatherDelayAgentConfig starts a real showmesh-agent subprocess with its
// inbound HTTP listener bound to a non-loopback address (so the coordinator
// can dial it for the direct-HTTP fallback), pointed at the coordinator's
// own signing public key, and using the "fakesink" gstengine backend (no
// real audio device, no separate build). assetDir is reused across a
// restart by callers that need the agent's own locally persisted delay
// state (scenario 5) to survive one.
func startWeatherDelayAgent(t *testing.T, nodeID, assetDir, coordinatorPublicKeyPath string) *testAgent {
	t.Helper()
	t.Setenv(envGstAudioSinkOverride, "fakesink")
	// A fast report cadence, matching audio_broker_loss_test.go's own
	// identical override: the 15s production default would make every
	// "alert session reports playing" wait in this file race its own
	// generous-but-bounded timeout for no reason.
	t.Setenv(envAudioReportInterval, "500ms")
	listenAddr := fmt.Sprintf("%s:%d", nonLoopbackHost(t), findFreePort(t))
	return startAgent(t, agentConfig{
		nodeID: nodeID, assetDir: assetDir,
		extraEnv: []string{
			"SHOWMESH_FPPCONNECT_LISTEN_ADDR=" + listenAddr,
			"SHOWMESH_WEATHERDELAY_COORDINATOR_PUBLIC_KEY_PATH=" + coordinatorPublicKeyPath,
		},
	})
}

// putWeatherDelayConfig PUTs show.weatherdelay and requires success.
func putWeatherDelayConfig(t *testing.T, coord *testCoordinator, token string, payload v1.ConfigWeatherDelayPayload) {
	t.Helper()
	status, body := putRawWithToken(t, coord, "/api/v1/config/show.weatherdelay", token, payload)
	if status != http.StatusOK {
		t.Fatalf("PUT /api/v1/config/show.weatherdelay: status = %d, want 200; body: %s", status, body)
	}
}

// normalizeRepeatCount maps 0 (this file's own "leave the default in
// effect" shorthand) to the plan default; the wire validation itself
// requires an explicit value in [1, 50], with no "0 means unset" allowance
// (config.WeatherDelayAlertPayload's identical comment describes the
// internal domain type only, decoded well after this validation runs).
func normalizeRepeatCount(repeatCount int) int {
	if repeatCount == 0 {
		return 10
	}
	return repeatCount
}

// weatherDelayDefaultTriggers mirrors
// config.WeatherDelayDefaultPayload.Triggers: like repeatCount and the
// power group's list fields, the wire validation requires explicit values
// in range, with no "0 means the production default" leniency.
func weatherDelayDefaultTriggers() v1.ConfigWeatherDelayTriggersPayload {
	return v1.ConfigWeatherDelayTriggersPayload{AnswerWindowSeconds: 30, CancelAnswerWindowSeconds: 180, RestartMinutes: 15}
}

// weatherDelayPowerGroupPayload builds one valid power group payload
// naming fppID, with every other list field explicit (empty, never nil):
// the config decoder refuses a null resolumeInstanceIds/renderNodeIds
// rather than treating it as "no selection."
func weatherDelayPowerGroupPayload(groupID, fppID string) v1.ConfigWeatherDelayPowerGroupPayload {
	return v1.ConfigWeatherDelayPowerGroupPayload{
		ID: groupID, Label: "Test power group",
		FPPInstanceIDs: []string{fppID}, ResolumeInstanceIDs: []string{}, RenderNodeIDs: []string{},
		Heartbeat: v1.ConfigWeatherDelayHeartbeatPayload{Enabled: true, IntervalSeconds: 1},
	}
}

// weatherDelayState fetches and decodes GET /api/v1/weather-delay, via
// coord.getRaw so it carries whatever bearer token coord was started with
// (this file always starts a coordinator with bearerToken set to a real
// admin token, see newWeatherDelayCoordinator).
func weatherDelayState(t *testing.T, coord *testCoordinator) v1.WeatherDelayStateResponse {
	t.Helper()
	status, body := coord.getRaw(t, "/api/v1/weather-delay")
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/weather-delay: status = %d, want 200; body: %s", status, body)
	}
	var resp v1.WeatherDelayStateResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode GET /api/v1/weather-delay: %v; body: %s", err, body)
	}
	return resp
}

// weatherDelayStartAction POSTs weather-delay/start and requires success.
func weatherDelayStartAction(t *testing.T, coord *testCoordinator, token string) v1.WeatherDelayActionResponse {
	t.Helper()
	return weatherDelayAction(t, coord, token, "/api/v1/weather-delay/start")
}

func weatherDelayResumeAction(t *testing.T, coord *testCoordinator, token string) v1.WeatherDelayActionResponse {
	t.Helper()
	return weatherDelayAction(t, coord, token, "/api/v1/weather-delay/resume")
}

func weatherDelayAction(t *testing.T, coord *testCoordinator, token, path string) v1.WeatherDelayActionResponse {
	t.Helper()
	status, body := postRawWithToken(t, coord, path, token, map[string]string{"idempotencyKey": "wd-" + uniqueSuffix()})
	if status != http.StatusOK {
		t.Fatalf("POST %s: status = %d, want 200; body: %s", path, status, body)
	}
	var resp v1.WeatherDelayActionResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode %s response: %v; body: %s", path, err, body)
	}
	return resp
}

// uploadWeatherDelayAlertAsset uploads and syncs a small alert audio asset
// (durationSeconds long) to nodeID, and returns its asset id for use as
// show.weatherdelay's alert.delayAssetId/cancelNightAssetId.
func uploadWeatherDelayAlertAsset(t *testing.T, coord *testCoordinator, token, showID, sequence, nodeID string, durationSeconds float64) string {
	t.Helper()
	filePath := filepath.Join(t.TempDir(), sequence+".wav")
	writeShortWAV(t, filePath, durationSeconds)
	asset := uploadNodeAsset(t, coord, token, showID, sequence, "audio", nodeID, filePath)
	return asset.ID
}

// subscribeWeatherDelayDark subscribes a raw "observer"-credentialed client
// (deploy/mosquitto/acl.conf's own read-only "showmesh/#" role; the
// "coordinator" role only has WRITE on showmesh/events/#, matching ADR-053
// decision 10's own "an installation's power controller points at the
// heartbeat" reader, never the publisher itself) to one power group's dark
// heartbeat topic and records every message received along with whether the
// broker delivered it retained. Scenario 1 requires this topic is published
// live, never retained (mqttproto.WeatherDelayDarkDeliveryPolicy).
type weatherDelayDarkSubscriber struct {
	mu       sync.Mutex
	messages []weatherDelayDarkObservation
}

type weatherDelayDarkObservation struct {
	msg      mqttproto.WeatherDelayDarkMessage
	retained bool
}

func subscribeWeatherDelayDark(t *testing.T, groupID string) *weatherDelayDarkSubscriber {
	t.Helper()
	username, password := provisionBrokerCredential(t, "observer")
	cli := rawConnect(t, username, password)
	sub := &weatherDelayDarkSubscriber{}
	cli.AddOnPublishReceived(func(pr paho.PublishReceived) (bool, error) {
		if pr.Packet == nil {
			return true, nil
		}
		msg, err := mqttproto.DecodeWeatherDelayDarkMessage(pr.Packet.Payload)
		if err != nil {
			return true, nil
		}
		sub.mu.Lock()
		sub.messages = append(sub.messages, weatherDelayDarkObservation{msg: msg, retained: pr.Packet.Retain})
		sub.mu.Unlock()
		return true, nil
	})
	topic, err := mqttproto.WeatherDelayDarkTopic(groupID)
	if err != nil {
		t.Fatalf("WeatherDelayDarkTopic(%q): %v", groupID, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sa, err := cli.Subscribe(ctx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: topic, QoS: 1}}})
	if err != nil {
		t.Fatalf("SUBSCRIBE %s: %v", topic, err)
	}
	for i, rc := range sa.Reasons {
		if rc >= 0x80 {
			t.Fatalf("SUBSCRIBE %s rejected: index %d, reason code %d", topic, i, rc)
		}
	}
	return sub
}

func (s *weatherDelayDarkSubscriber) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.messages)
}

func (s *weatherDelayDarkSubscriber) any(cond func(weatherDelayDarkObservation) bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.messages {
		if cond(m) {
			return true
		}
	}
	return false
}

// weatherDelayStateSubscriber observes the retained weather-delay state
// topic itself (as "observer", the same read-only role
// subscribeWeatherDelayDark uses), recording the latest plan seen. Used by
// scenario 3 to confirm the retained, plan-carrying broadcast has actually
// reached the broker (and, since the node subscribed to this topic long
// before, the node too) BEFORE stopping the broker: none of the other two
// weatherdelay.start delivery paths (the MQTT command's params, the signed
// direct-HTTP body) carry the alert asset reference, only this topic does.
type weatherDelayStateSubscriber struct {
	mu   sync.Mutex
	last mqttproto.WeatherDelayMessage
	has  bool
}

func subscribeWeatherDelayState(t *testing.T) *weatherDelayStateSubscriber {
	t.Helper()
	username, password := provisionBrokerCredential(t, "observer")
	cli := rawConnect(t, username, password)
	sub := &weatherDelayStateSubscriber{}
	cli.AddOnPublishReceived(func(pr paho.PublishReceived) (bool, error) {
		if pr.Packet == nil {
			return true, nil
		}
		msg, err := mqttproto.DecodeWeatherDelayMessage(pr.Packet.Payload)
		if err != nil {
			return true, nil
		}
		sub.mu.Lock()
		sub.last, sub.has = msg, true
		sub.mu.Unlock()
		return true, nil
	})
	topic := mqttproto.WeatherDelayTopic()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sa, err := cli.Subscribe(ctx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: topic, QoS: 1}}})
	if err != nil {
		t.Fatalf("SUBSCRIBE %s: %v", topic, err)
	}
	for i, rc := range sa.Reasons {
		if rc >= 0x80 {
			t.Fatalf("SUBSCRIBE %s rejected: index %d, reason code %d", topic, i, rc)
		}
	}
	return sub
}

// hasDelayAsset reports whether the latest state message seen carries
// assetID as its delay alert.
func (s *weatherDelayStateSubscriber) hasDelayAsset(assetID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.has && s.last.Plan.Delay != nil && s.last.Plan.Delay.AssetID == assetID
}

// newWeatherDelayCoordinator starts a coordinator subprocess configured
// with fppInstanceID pointing at stub.url(), its own asset store with sync
// enabled (assetContentBaseURL pointed at its own address, matching
// assets_test.go's startAssetCoordinator), and returns it with a freshly
// minted admin token and the dataDir it used (for scenario 5's own restart
// over the same database).
func newWeatherDelayCoordinator(t *testing.T, dataDir, clientID, fppInstanceID string, stub *weatherDelayFPPStub) (coord *testCoordinator, token string) {
	t.Helper()
	token = createAdminAndIssueToken(t, dataDir, "admin-1", "a-strong-password-1")
	assetDir := filepath.Join(dataDir, "assets")
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		t.Fatalf("mkdir coordinator asset dir %s: %v", assetDir, err)
	}
	httpAddr := fmt.Sprintf("127.0.0.1:%d", findFreePort(t))
	coord = startCoordinatorWithConfig(t, coordinatorConfig{
		dataDir: dataDir, clientID: clientID,
		bearerToken: token, httpAddr: httpAddr, fppEndpoints: fppInstanceID + "=" + stub.url(),
		assetDir: assetDir, assetContentBaseURL: "http://" + httpAddr,
		assetSyncInterval: 500 * time.Millisecond, assetInventoryInterval: 200 * time.Millisecond,
	})
	return coord, token
}

// restartWeatherDelayCoordinator rebuilds a coordinator over the SAME
// dataDir, httpAddr and client id an earlier newWeatherDelayCoordinator (or
// a previous restartWeatherDelayCoordinator) used: scenario 5's own "kill
// and restart over the same database", reusing coordinatorConfig's own
// httpAddr override the way restart_test.go's coordinator-restart tests do.
func restartWeatherDelayCoordinator(t *testing.T, dataDir, httpAddr, clientID, fppInstanceID, token string, stub *weatherDelayFPPStub) *testCoordinator {
	t.Helper()
	assetDir := filepath.Join(dataDir, "assets")
	return startCoordinatorWithConfig(t, coordinatorConfig{
		dataDir: dataDir, clientID: clientID, httpAddr: httpAddr,
		bearerToken: token, fppEndpoints: fppInstanceID + "=" + stub.url(),
		assetDir: assetDir, assetContentBaseURL: "http://" + httpAddr,
		assetSyncInterval: 500 * time.Millisecond, assetInventoryInterval: 200 * time.Millisecond,
	})
}

// waitForFPPGateSupported waits until the coordinator's own idle-gate read
// (or the enforcement loop, once active) has reached the stub at least
// once, observable as a non-zero gate revision once the coordinator has
// written to it, or simply that the coordinator's FPP poll has landed.
// Used before scenario 7's own pre-seeded gate assertion to make sure the
// coordinator is actually talking to the stub before relying on its
// silence (never writing to an already-closed gate).
func waitForCoordinatorReachesFPP(t *testing.T, coord *testCoordinator, token, instanceID string) {
	t.Helper()
	waitFor(t, 30*time.Second, 200*time.Millisecond, func() bool {
		status, body := coord.getRaw(t, "/api/v1/fpp/"+instanceID)
		return status == http.StatusOK && strings.Contains(string(body), `"signal":"fpp.status"`) && !strings.Contains(string(body), `"fpp.status","value":null`)
	}, "the FPP collector's first poll against the stand-in FPP stub to land through the coordinator")
}

// weatherDelayFixture is the common rig every scenario in this file starts
// from: a coordinator with its own asset store, a stand-in FPP stub, one
// node agent (fakesink, inbound HTTP listener enabled, pointed at the
// coordinator's own signing key), a small alert asset uploaded and synced
// to that node, and one power group containing the stub.
type weatherDelayFixture struct {
	coord      *testCoordinator
	token      string
	dataDir    string
	httpAddr   string
	clientID   string
	nodeID     string
	assetDir   string
	fppID      string
	stub       *weatherDelayFPPStub
	agent      *testAgent
	pubKeyPath string
	groupID    string
	showID     string
	assetID    string
}

// configureLongWeatherDelayAlert re-points show.weatherdelay at a freshly
// uploaded alert asset durationSeconds long, repeated repeatCount times, and
// waits for it to reach the node. Scenarios that must observe an alert still
// PLAYING some seconds after they started it need one: the fixture's own
// 0.3s asset repeated ten times runs for about three seconds, which a
// "still playing" or "stopped early" assertion can outlive by accident.
func configureLongWeatherDelayAlert(t *testing.T, f *weatherDelayFixture, sequence string, durationSeconds float64, repeatCount int) string {
	t.Helper()
	assetID := uploadWeatherDelayAlertAsset(t, f.coord, f.token, f.showID, sequence, f.nodeID, durationSeconds)
	putWeatherDelayConfig(t, f.coord, f.token, v1.ConfigWeatherDelayPayload{
		Alert:       v1.ConfigWeatherDelayAlertPayload{DelayAssetID: assetID, RepeatCount: repeatCount, NodeIDs: []string{f.nodeID}},
		PowerGroups: []v1.ConfigWeatherDelayPowerGroupPayload{weatherDelayPowerGroupPayload(f.groupID, f.fppID)},
		Triggers:    weatherDelayDefaultTriggers(),
	})
	waitFor(t, 30*time.Second, 200*time.Millisecond, func() bool {
		return weatherDelayAssetPresent(t, f.coord, f.nodeID, assetID)
	}, "the longer delay alert asset to sync to the node once show.weatherdelay names it")
	return assetID
}

// weatherDelayAssetPresent reports whether GET /api/v1/weather-delay's own
// asset readiness (h.weatherDelayAssetReadiness) shows assetID present and
// hash-verified on nodeID.
func weatherDelayAssetPresent(t *testing.T, coord *testCoordinator, nodeID, assetID string) bool {
	t.Helper()
	resp := weatherDelayState(t, coord)
	for _, na := range resp.Assets {
		if na.NodeID != nodeID {
			continue
		}
		return na.DelayAsset != nil && na.DelayAsset.AssetID == assetID && na.DelayAsset.Present
	}
	return false
}

// newWeatherDelayFixture builds the rig. repeatCount 0 leaves the plan's
// own default (10) in effect.
func newWeatherDelayFixture(t *testing.T, repeatCount int) *weatherDelayFixture {
	t.Helper()
	dataDir := t.TempDir()
	clientID := "coord-" + uniqueSuffix()
	stub := newWeatherDelayFPPStub(t)
	fppID := "wdfpp-" + uniqueSuffix()
	coord, token := newWeatherDelayCoordinator(t, dataDir, clientID, fppID, stub)
	waitForCoordinatorReachesFPP(t, coord, token, fppID)

	nodeID := "wdnode-" + uniqueSuffix()
	mustCtl(t, coord, token, []string{"declare", "--label", "Weather delay test node"}, nodeID)

	pubKeyPath := coordinatorSigningPublicKeyFile(t, dataDir)
	assetDir := filepath.Join(t.TempDir(), "assets")
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		t.Fatalf("mkdir agent asset dir %s: %v", assetDir, err)
	}
	agent := startWeatherDelayAgent(t, nodeID, assetDir, pubKeyPath)

	// The alert plays over the same real gstengine backend every other
	// audio session does (audio_gstengine_test.go's own doc comment): the
	// engine is wired but unbound until audio.node.configure delivers an
	// output binding, so weather delay's own start sequence produces no
	// audible session on a freshly started agent without this first, exactly
	// as it would not in production without an operator having configured
	// this node's audio output at least once.
	cli, w := startCmdClient(t, nodeID)
	awaitAgentReceivingCommands(t, cli, w, nodeID)
	configureCmdID := "cmd-audio-node-configure-" + uniqueSuffix()
	dispatchCmd(t, cli, nodeID, audioNodeConfigureCmd(nodeID, configureCmdID, 1))
	waitForResult(t, w, configureCmdID, 15*time.Second)

	showID := "wdshow-" + uniqueSuffix()
	ensureShow(t, coord, token, showID, "Weather Delay Test Show")
	assetID := uploadWeatherDelayAlertAsset(t, coord, token, showID, "delay-alert", nodeID, 0.3)

	// Weather delay's own asset push (internal/coordinator/weatherdelay.go's
	// pushWeatherDelayAssets) is driven by the SAME republish loop that
	// carries the retained state, on weatherDelayReconcileInterval (a
	// private 5s var, no test override, like the enforcement loop's own
	// interval). It only nudges an asset once show.weatherdelay actually
	// names it, so the config PUT must happen BEFORE waiting for presence,
	// not after.
	groupID := "wdgroup-" + uniqueSuffix()
	putWeatherDelayConfig(t, coord, token, v1.ConfigWeatherDelayPayload{
		Alert:       v1.ConfigWeatherDelayAlertPayload{DelayAssetID: assetID, RepeatCount: normalizeRepeatCount(repeatCount), NodeIDs: []string{nodeID}},
		PowerGroups: []v1.ConfigWeatherDelayPowerGroupPayload{weatherDelayPowerGroupPayload(groupID, fppID)},
		Triggers:    weatherDelayDefaultTriggers(),
	})
	waitFor(t, 30*time.Second, 200*time.Millisecond, func() bool {
		return weatherDelayAssetPresent(t, coord, nodeID, assetID)
	}, "the delay alert asset to sync to the node once show.weatherdelay names it")

	return &weatherDelayFixture{
		coord: coord, token: token, dataDir: dataDir, httpAddr: coord.httpAddr, clientID: clientID,
		nodeID: nodeID, assetDir: assetDir, fppID: fppID, stub: stub, agent: agent, pubKeyPath: pubKeyPath,
		groupID: groupID, showID: showID, assetID: assetID,
	}
}

// waitForCountToSettle polls count and fails t if it has not gone quiet (no
// change) for a full settle window before deadline elapses. Used where a
// value may legitimately keep changing for a while (a stale evidence window
// this file does not control the length of) before it is expected to stop,
// so a fixed short observation window would be racy in either direction.
func waitForCountToSettle(t *testing.T, count func() int, deadline, settle time.Duration, msg string) {
	t.Helper()
	end := time.Now().Add(deadline)
	last := count()
	stableSince := time.Now()
	for {
		if time.Since(stableSince) >= settle {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("timed out after %s waiting for: %s (count still changing, last value %d)", deadline, msg, last)
		}
		time.Sleep(300 * time.Millisecond)
		if cur := count(); cur != last {
			last = cur
			stableSince = time.Now()
		}
	}
}

// waitForNodeAlertSession polls the node's own observed/audio report for
// the delay alert's session reaching wantState with the announcement role.
func waitForNodeAlertSession(t *testing.T, sub *audioReportSubscriber, wantState string) {
	t.Helper()
	waitForNodeAlertSessionID(t, sub, weatherDelayAlertSessionIDForTest, wantState)
}

// waitForNodeAlertSessionID is waitForNodeAlertSession against a specific
// alert session id, for scenario 6, which exercises both kinds' sessions.
func waitForNodeAlertSessionID(t *testing.T, sub *audioReportSubscriber, sessionID, wantState string) {
	t.Helper()
	waitFor(t, 20*time.Second, 200*time.Millisecond, func() bool {
		p, ok := sub.latestFor(sessionID)
		return ok && p.State == wantState && p.HasSourceRole && p.SourceRole == "announcement"
	}, fmt.Sprintf("the weather delay alert session (%s) to report state %q with the announcement role", sessionID, wantState))
}

// --- Scenario 1: one press ---

// TestWeatherDelayOnePress proves ADR-053's basic path: POST
// weather-delay/start reaches the stand-in FPP player (Stop Now, then a
// gate close carrying a revision), reaches the node (the alert session
// playing under the announcement role), and the power group is reported
// confirmed dark once the player reports idle and closed, with its
// heartbeat published live, never retained.
func TestWeatherDelayOnePress(t *testing.T) {
	f := newWeatherDelayFixture(t, 0)
	audioSub := subscribeAudioReports(t, f.nodeID)
	dark := subscribeWeatherDelayDark(t, f.groupID)

	f.stub.setStatusName("playing")

	result := weatherDelayStartAction(t, f.coord, f.token)
	if !result.Result.Active {
		t.Fatalf("weather-delay/start: Active = false, want true")
	}

	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		return f.stub.commandCount("Stop Now") >= 1
	}, "the stand-in FPP player to receive Stop Now")

	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		closed, rev := f.stub.gateState()
		return closed && rev > 0
	}, "the stand-in FPP plugin's weather gate to close, carrying a revision")

	waitForNodeAlertSession(t, audioSub, "playing")

	waitFor(t, 20*time.Second, 200*time.Millisecond, func() bool {
		return f.stub.currentStatusName() == "idle"
	}, "the stand-in FPP player to report idle after Stop Now")

	waitFor(t, 20*time.Second, 200*time.Millisecond, func() bool {
		resp := weatherDelayState(t, f.coord)
		if !resp.Active {
			return false
		}
		for _, pg := range resp.PowerGroups {
			if pg.ID == f.groupID && pg.ConfirmedDark {
				return true
			}
		}
		return false
	}, "the power group to be reported confirmed dark once the player reports idle and closed")

	waitFor(t, 10*time.Second, 200*time.Millisecond, func() bool {
		return dark.count() > 0
	}, "the power group's dark heartbeat to be published")

	// A subscription opened BEFORE a publish always sees RETAIN cleared,
	// whatever the publisher asked for, so only a subscriber opened after a
	// heartbeat has already been published can tell live from retained.
	fresh := subscribeWeatherDelayDark(t, f.groupID)
	waitFor(t, 20*time.Second, 200*time.Millisecond, func() bool {
		return fresh.count() > 0
	}, "the fresh dark-heartbeat subscriber to see a heartbeat at all, so its retained/live verdict rests on an observation")
	if fresh.any(func(o weatherDelayDarkObservation) bool { return o.retained }) {
		t.Fatalf("the power group dark heartbeat was delivered retained to a subscriber that joined after it was published; ADR-053 decision 10 requires it live, never retained (a stale message must not linger)")
	}
}

// --- Scenario 2: held ---

// TestWeatherDelayHeld proves ADR-053 decision 3/4 while a delay is
// active: when the stand-in player reports playing again (as if FPP's own
// schedule had started something), the enforcement loop re-sends Stop Now
// within about two of its own ticks; a cue activation and a night-start
// command are both refused with the operator-facing sentence; the power
// group flips to not dark while the player is playing, its heartbeat
// stops, and both resume once the player reports idle and closed again.
func TestWeatherDelayHeld(t *testing.T) {
	f := newWeatherDelayFixture(t, 0)
	dark := subscribeWeatherDelayDark(t, f.groupID)

	weatherDelayStartAction(t, f.coord, f.token)
	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		return f.stub.commandCount("Stop Now") >= 1
	}, "the stand-in FPP player to receive the initial Stop Now")
	waitFor(t, 20*time.Second, 200*time.Millisecond, func() bool {
		return dark.count() > 0
	}, "the power group's dark heartbeat to start once the player is confirmed idle and closed")

	stopNowBefore := f.stub.commandCount("Stop Now")
	f.stub.setStatusName("playing")

	// weatherDelayEnforceInterval is a private 5s Go var with no test
	// override (production never overrides it, see that var's own doc
	// comment); this waits generously for at least two ticks.
	waitFor(t, 25*time.Second, 200*time.Millisecond, func() bool {
		return f.stub.commandCount("Stop Now") > stopNowBefore
	}, "the enforcement loop to re-send Stop Now once the player reports playing again")

	status, body := postRawWithToken(t, f.coord, "/api/v1/cues/no-such-cue/activate", f.token, nil)
	if status != http.StatusConflict {
		t.Fatalf("POST /api/v1/cues/{id}/activate while active: status = %d, want 409; body: %s", status, body)
	}
	if !strings.Contains(string(body), "A weather delay is active. Resume the show to activate Cues.") {
		t.Fatalf("cue activation refusal body missing the operator sentence: %s", body)
	}

	status, body = postRawWithToken(t, f.coord, "/api/v1/night/commands/start-night", f.token, map[string]string{"idempotencyKey": "wd-" + uniqueSuffix()})
	if status != http.StatusConflict {
		t.Fatalf("POST /api/v1/night/commands/start-night while active: status = %d, want 409; body: %s", status, body)
	}
	if !strings.Contains(string(body), "A weather delay is active. Resume the show to start it.") {
		t.Fatalf("night-start refusal body missing the operator sentence: %s", body)
	}

	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		resp := weatherDelayState(t, f.coord)
		for _, pg := range resp.PowerGroups {
			if pg.ID == f.groupID {
				return !pg.ConfirmedDark
			}
		}
		return false
	}, "the power group to flip to not dark while the player reports playing")

	// The heartbeat must eventually stop, but "eventually" is bounded by the
	// FPP status collector's own poll cadence (fpp.DefaultPollInterval, 15s,
	// not the 5s enforcement tick): the enforcement tick computes darkness
	// from that collector's evidence, so it can keep seeing "idle" (and keep
	// heartbeating) for one or more ticks after the GET above already saw a
	// fresher read. Rather than assert a small fixed bound against that real
	// staleness, this polls until the count goes quiet for a full enforcement
	// interval, with a deadline wide enough to cover the collector's own
	// worst-case poll gap.
	waitForCountToSettle(t, dark.count, 45*time.Second, 6*time.Second,
		"the power group's dark heartbeat to stop publishing once the group is confirmed not dark")

	f.stub.setStatusName("idle")
	waitFor(t, 20*time.Second, 200*time.Millisecond, func() bool {
		closed, _ := f.stub.gateState()
		return closed
	}, "the gate to be re-closed by the enforcement loop once the player reports idle")

	darkCountBeforeResume := dark.count()
	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		resp := weatherDelayState(t, f.coord)
		for _, pg := range resp.PowerGroups {
			if pg.ID == f.groupID {
				return pg.ConfirmedDark
			}
		}
		return false
	}, "the power group to be reported confirmed dark again once the player reports idle and closed")
	waitFor(t, 10*time.Second, 200*time.Millisecond, func() bool {
		return dark.count() > darkCountBeforeResume
	}, "the power group's dark heartbeat to resume")
}

// --- Scenario 3: broker down ---

// TestWeatherDelayBrokerDown proves ADR-053 decision 8: with the broker
// stopped, POST weather-delay/start still reaches the node over the
// direct-HTTP fallback, the node still plays the alert, and the player is
// still stopped and closed via the coordinator's own direct FPP HTTP
// dispatch (which never depended on the broker in the first place).
// Restarting the broker afterward proves node and coordinator converge and
// the node stays delayed.
func TestWeatherDelayBrokerDown(t *testing.T) {
	// This test's own broker, isolated from the shared one every other test
	// in this file (and this package) depends on: audio_broker_loss_test.go's
	// startIsolatedAudioBroker doc comment explains why (stopping/starting
	// the SHARED container out from under concurrently-relevant state is
	// what quarantined that test in the first place). It overrides the
	// package-level broker globals for the rest of this test's run, so
	// newWeatherDelayFixture's own use of startAgent/stopBroker/startBroker
	// below drives this isolated container transparently.
	startIsolatedAudioBroker(t)

	f := newWeatherDelayFixture(t, 0)
	audioSub := subscribeAudioReports(t, f.nodeID)

	// An alert long enough to still be playing after the broker comes back,
	// so the report below is evidence about what the node did while the
	// broker was DOWN and not about a start replayed once it returned.
	longAssetID := configureLongWeatherDelayAlert(t, f, "delay-alert-long", 2, 50)

	// The node's own copy of the alert plan (which asset to play) only ever
	// arrives on the retained state topic; neither the MQTT command nor the
	// signed direct-HTTP start below carries it. Confirm the broker has
	// already carried a plan-bearing publish (the node, subscribed since it
	// started, gets it live at the same time) before taking the broker down,
	// so this test proves the direct-HTTP fallback's own claim rather than a
	// race against the coordinator's 5s republish loop.
	stateSub := subscribeWeatherDelayState(t)
	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		return stateSub.hasDelayAsset(longAssetID)
	}, "the retained weather delay state to carry the configured delay asset before the broker goes down")

	stopBroker(t)
	t.Cleanup(func() { startBroker(t) })

	result := weatherDelayStartAction(t, f.coord, f.token)
	var nodeOutcome *v1.WeatherDelayTargetOutcome
	for i := range result.Result.Targets {
		if result.Result.Targets[i].TargetKind == v1.WeatherDelayTargetKindNodeCommand {
			nodeOutcome = &result.Result.Targets[i]
		}
	}
	if nodeOutcome == nil {
		t.Fatalf("weather-delay/start response carries no node-command target outcome; targets: %+v", result.Result.Targets)
	}
	if nodeOutcome.DeliveredVia != "http" {
		t.Fatalf("node-command DeliveredVia = %q, want %q (the broker is down; only the direct-HTTP path can have reached it)", nodeOutcome.DeliveredVia, "http")
	}
	if nodeOutcome.Outcome != "confirmed" {
		t.Fatalf("node-command outcome = %q, want confirmed; reason: %s", nodeOutcome.Outcome, nodeOutcome.OutcomeReason)
	}

	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		return f.stub.commandCount("Stop Now") >= 1
	}, "the stand-in FPP player to receive Stop Now while the broker is down (the coordinator's FPP dispatch never depends on the node's own broker)")
	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		closed, _ := f.stub.gateState()
		return closed
	}, "the stand-in FPP plugin's weather gate to close while the broker is down")

	brokerBackAt := time.Now()
	startBroker(t)
	waitForBrokerReady(t, 20*time.Second)

	// The audio report subscription was opened before the broker went
	// down; a fresh one (matching audio_broker_loss_test.go's own
	// reasoning) is what can observe anything published after it returns.
	audioSub = subscribeAudioReports(t, f.nodeID)
	waitForNodeAlertSession(t, audioSub, "playing")

	// The alert playlist's revision is the UnixNano the node stamped when it
	// built the alert (internal/agent/weatherdelayops.go's startAlert), so it
	// dates the node's own decision to play. A node that answered the signed
	// HTTP start 200 and did nothing, and only started once MQTT redelivered
	// the state, stamps a revision AFTER the broker came back.
	p, ok := audioSub.latestFor(weatherDelayAlertSessionIDForTest)
	if !ok {
		t.Fatalf("no alert session report for %s after the broker returned", weatherDelayAlertSessionIDForTest)
	}
	if !p.HasPlaylist {
		t.Fatalf("the alert session reports no playlist, so nothing dates when the node started it")
	}
	startedAt := time.Unix(0, int64(p.PlaylistRevision))
	if !startedAt.Before(brokerBackAt) {
		t.Fatalf("the node started the alert at %s, after the broker came back at %s; ADR-053 decision 8 requires the signed direct-HTTP start to have reached it while the broker was down, not a redelivered MQTT message afterwards", startedAt, brokerBackAt)
	}

	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		resp := weatherDelayState(t, f.coord)
		return resp.Active
	}, "the coordinator to still report the delay active once the broker returns")
}

// --- Scenario 4: repeat and resume ---

// TestWeatherDelayRepeatAndResume proves ADR-053 decision 7 (the alert
// plays a configured number of times and stops on its own) and decisions
// 1/11 (resume ends an alert immediately, re-opens the gate with a higher
// revision, and re-admits cue activation).
func TestWeatherDelayRepeatAndResume(t *testing.T) {
	f := newWeatherDelayFixture(t, 3)
	audioSub := subscribeAudioReports(t, f.nodeID)

	weatherDelayStartAction(t, f.coord, f.token)
	waitForNodeAlertSession(t, audioSub, "playing")

	// repeatCount 3 against a ~0.3s asset: comfortably done within 10s.
	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		p, ok := audioSub.latestFor(weatherDelayAlertSessionIDForTest)
		return ok && p.State == "completed"
	}, "the alert session to advance through its configured repeat count and complete on its own")

	weatherDelayResumeAction(t, f.coord, f.token)
	waitFor(t, 10*time.Second, 200*time.Millisecond, func() bool {
		resp := weatherDelayState(t, f.coord)
		return !resp.Active
	}, "the coordinator to report the delay no longer active after the first resume")

	// Start again with an alert that runs for about a hundred seconds, so
	// this resume demonstrably stops it mid-flight. Ten repeats of the
	// fixture's own 0.3s asset run for three seconds, which the steps
	// between the start and the resume below can outlast, leaving the alert
	// to finish on its own and the "resume stopped it" assertion true for
	// the wrong reason.
	configureLongWeatherDelayAlert(t, f, "delay-alert-long", 2, 50)

	_, beforeRevision := f.stub.gateState()
	weatherDelayStartAction(t, f.coord, f.token)
	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		closed, rev := f.stub.gateState()
		return closed && rev > beforeRevision
	}, "the gate to close again on the second start")
	audioSub = subscribeAudioReports(t, f.nodeID)
	waitForNodeAlertSession(t, audioSub, "playing")

	_, closeRevision := f.stub.gateState()
	if p, ok := audioSub.latestFor(weatherDelayAlertSessionIDForTest); !ok || p.State != "playing" {
		t.Fatalf("the alert was not playing at the moment resume was sent (report present: %v, state %q), so a later stop would prove nothing", ok, p.State)
	}
	weatherDelayResumeAction(t, f.coord, f.token)

	waitFor(t, 10*time.Second, 200*time.Millisecond, func() bool {
		p, ok := audioSub.latestFor(weatherDelayAlertSessionIDForTest)
		return ok && p.State != "playing"
	}, "the alert to stop promptly on resume rather than finishing its own repeat count")

	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		closed, rev := f.stub.gateState()
		return !closed && rev > closeRevision
	}, "resume to open the gate again with a higher revision than the close it is undoing")

	status, body := postRawWithToken(t, f.coord, "/api/v1/cues/no-such-cue/activate", f.token, nil)
	if status == http.StatusConflict {
		t.Fatalf("cue activation still refused as if a weather delay were active after resume; body: %s", body)
	}
}

// --- Scenario 5: restart ---

// nodeWeatherDelayStateActive reads the record the node agent persists under
// its asset directory (internal/agent/weatherdelay.go's
// weatherDelayStateSubdir/weatherDelayStateFile) and reports whether it says
// a delay is active. Read while the agent is stopped, it is the node's own
// evidence, independent of anything the broker would redeliver.
func nodeWeatherDelayStateActive(t *testing.T, assetDir string) bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(assetDir, "weather-delay-state", "state.json"))
	if err != nil {
		t.Fatalf("read the node's persisted weather delay state: %v", err)
	}
	var rec struct {
		Active bool `json:"active"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("decode the node's persisted weather delay state: %v; body: %s", err, raw)
	}
	return rec.Active
}

// TestWeatherDelayRestart proves ADR-053 decision 2 (the state survives a
// coordinator restart): killing and restarting both the coordinator process
// (over the same database) and the node agent process (over the same
// asset/state directory) leaves both delayed, with enforcement resuming and
// nothing started. It also confirms the node wrote the delay to its own
// disk while it was stopped. It does NOT prove decision 9's other half, that
// a node reads that file back and stays delayed with no coordinator: the
// coordinator is up here and its retained state would redeliver the delay
// on its own. internal/agent/weatherdelay_test.go covers the read-back.
func TestWeatherDelayRestart(t *testing.T) {
	f := newWeatherDelayFixture(t, 0)
	audioSub := subscribeAudioReports(t, f.nodeID)

	weatherDelayStartAction(t, f.coord, f.token)
	waitForNodeAlertSession(t, audioSub, "playing")
	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		closed, _ := f.stub.gateState()
		return closed
	}, "the gate to close before the restart")

	f.coord.shutdown()
	f.agent.sigkill(t)

	if !nodeWeatherDelayStateActive(t, f.assetDir) {
		t.Fatalf("the node's own persisted weather delay state does not say active while the agent is stopped; ADR-053 decision 2 requires a node to come back delayed from its own disk")
	}

	f.coord = restartWeatherDelayCoordinator(t, f.dataDir, f.httpAddr, f.clientID, f.fppID, f.token, f.stub)
	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		resp := weatherDelayState(t, f.coord)
		return resp.Active
	}, "the restarted coordinator to still report the delay active from its own database")

	f.agent = startWeatherDelayAgent(t, f.nodeID, f.assetDir, f.pubKeyPath)
	audioSub = subscribeAudioReports(t, f.nodeID)
	waitForNodeAlertSession(t, audioSub, "playing")

	// Enforcement resumes and nothing starts: the stub never sees a Start
	// Playlist, and stays reachable for a fresh Stop Now if it reports
	// playing again.
	f.stub.setStatusName("playing")
	waitFor(t, 20*time.Second, 200*time.Millisecond, func() bool {
		return f.stub.commandCount("Stop Now") >= 1
	}, "the restarted enforcement loop to send Stop Now to a player that reports playing again")
	if n := f.stub.commandCount("Start Playlist"); n != 0 {
		t.Fatalf("stand-in FPP player received %d Start Playlist command(s) across a weather delay restart, want 0", n)
	}
}

// --- Scenario 7: held player ---

// TestWeatherDelayHeldPlayer proves ADR-053 decision 10's own held-player
// reporting: with no delay active, a player whose gate already reads
// closed (as if left over from a previous delay, or closed by hand) is
// listed under heldPlayers, the coordinator never writes to its gate while
// idle-only reading it, and resume releases it.
func TestWeatherDelayHeldPlayer(t *testing.T) {
	f := newWeatherDelayFixture(t, 0)

	f.stub.setGateClosed(true)
	_, revisionBefore := f.stub.gateState()
	readsBefore := f.stub.gateReadCount()

	waitFor(t, 40*time.Second, 500*time.Millisecond, func() bool {
		resp := weatherDelayState(t, f.coord)
		if resp.Active {
			return false
		}
		for _, hp := range resp.HeldPlayers {
			if hp.InstanceID == f.fppID {
				return true
			}
		}
		return false
	}, "the player to be listed under heldPlayers while its gate reads closed and no delay is active")

	// The idle-gate READ runs on its own 30s interval
	// (weatherDelayIdleGateReadInterval), so a short fixed sleep can end with
	// no read having happened at all and prove nothing. Wait for a read that
	// lands after the close above, then confirm that read did not WRITE: a
	// write always advances the revision (see weatherGateRequest's own
	// comment: max(stored+1, revision)).
	waitFor(t, 60*time.Second, 500*time.Millisecond, func() bool {
		return f.stub.gateReadCount() > readsBefore
	}, "the coordinator's idle gate read to reach the stand-in player again after its gate was closed")
	_, revisionAfter := f.stub.gateState()
	if revisionAfter != revisionBefore {
		t.Fatalf("the coordinator wrote to an already-closed gate while no delay is active and none was requested (revision %d -> %d); ADR-053 decision 10 forbids the coordinator ever opening a gate on its own, and a write of any kind here is not idle reading", revisionBefore, revisionAfter)
	}

	weatherDelayResumeAction(t, f.coord, f.token)
	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		closed, rev := f.stub.gateState()
		return !closed && rev > revisionBefore
	}, "resume to release the held player by opening its gate")

	resp := weatherDelayState(t, f.coord)
	for _, hp := range resp.HeldPlayers {
		if hp.InstanceID == f.fppID {
			t.Fatalf("player %s still listed under heldPlayers after resume", f.fppID)
		}
	}
}

// --- Scenario 6: cancel night ---

// weatherDelayParseTime parses an RFC 3339 timestamp the API answered with.
func weatherDelayParseTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("parse timestamp %q: %v", value, err)
	}
	return parsed
}

// weatherDelayLatestEventSeq reads GET /api/v1/events' own latestSeq, so a
// later read can ask only for events appended after this moment.
func weatherDelayLatestEventSeq(t *testing.T, coord *testCoordinator) uint64 {
	t.Helper()
	_, latest := weatherDelayEventsSince(t, coord, 0)
	return latest
}

// weatherDelayEventsSince returns every event summary appended after since,
// with the newest latestSeq the coordinator reports.
func weatherDelayEventsSince(t *testing.T, coord *testCoordinator, since uint64) (summaries []string, latest uint64) {
	t.Helper()
	status, body := coord.getRaw(t, fmt.Sprintf("/api/v1/events?since=%d", since))
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/events?since=%d: status = %d, want 200; body: %s", since, status, body)
	}
	var resp struct {
		Events    []v1.Event `json:"events"`
		LatestSeq uint64     `json:"latestSeq"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode GET /api/v1/events: %v; body: %s", err, body)
	}
	for _, e := range resp.Events {
		summaries = append(summaries, e.Summary)
	}
	return summaries, resp.LatestSeq
}

// weatherDelayNightShutdownRan reports whether the graceful night shutdown
// cancel-night runs after its alert has appended its own event. Both of its
// outcomes count: this rig has no night session running, so the shutdown
// reports that there was nothing to shut down.
func weatherDelayNightShutdownRan(t *testing.T, coord *testCoordinator, since uint64) bool {
	t.Helper()
	summaries, _ := weatherDelayEventsSince(t, coord, since)
	for _, s := range summaries {
		if strings.Contains(s, "no show was running") || strings.Contains(s, "shutting down for the night") {
			return true
		}
	}
	return false
}

// alertPlaylistRevision returns the alert session's current playlist
// revision, which the node stamps afresh every time it builds an alert. A
// change means the node loaded a DIFFERENT alert, not that it kept playing
// the one it had.
func alertPlaylistRevision(t *testing.T, sub *audioReportSubscriber) uint64 {
	t.Helper()
	return alertPlaylistRevisionForSession(t, sub, weatherDelayAlertSessionIDForTest)
}

func alertPlaylistRevisionForSession(t *testing.T, sub *audioReportSubscriber, sessionID string) uint64 {
	t.Helper()
	p, ok := sub.latestFor(sessionID)
	if !ok || !p.HasPlaylist {
		t.Fatalf("no alert session playlist reported for %s", sessionID)
	}
	return p.PlaylistRevision
}

// observeAlertPlayback waits until sessionID's own reports at wantRevision
// leave the playing state (or deadline elapses since the call), then reads
// sub's FULL history for that session — never a live poll loop's own
// timing — and returns how long it was reported playing (first playing
// observation to last) plus the set of item indexes it saw playing.
// sub's background MQTT callback timestamps each report the instant its
// own goroutine receives it, so this stays accurate even when the test's
// own foreground code was blocked elsewhere for a while (a cancel-night
// POST can itself take several seconds to return; nothing about that
// coordinator-side latency may be mistaken for the alert's own truncation).
// Used to prove an alert played in full rather than trusting a single
// point-in-time read, against the case where it replaced a DIFFERENT
// alert already loaded and playing on another session (ADR-053 decision
// 7): before the fix, the audio manager's own Apply-over-a-loaded-
// playlist defect (recorded, skipped, in internal/agent/audio) meant
// weather delay's shared alert session reported a played-through defect,
// not an absent one.
func observeAlertPlayback(t *testing.T, sub *audioReportSubscriber, sessionID string, wantRevision uint64, deadline time.Duration) (playedFor time.Duration, indexesSeen map[int64]bool) {
	t.Helper()
	waitFor(t, deadline, 200*time.Millisecond, func() bool {
		p, ok := sub.latestFor(sessionID)
		return ok && p.HasPlaylist && p.PlaylistRevision == wantRevision && p.State != "playing"
	}, fmt.Sprintf("the alert session %s at playlist revision %d to leave the playing state", sessionID, wantRevision))

	indexesSeen = map[int64]bool{}
	var first, last time.Time
	for _, obs := range sub.historyFor(sessionID) {
		if !obs.report.HasPlaylist || obs.report.PlaylistRevision != wantRevision || obs.report.State != "playing" {
			continue
		}
		if first.IsZero() {
			first = obs.at
		}
		last = obs.at
		if obs.report.HasItem {
			indexesSeen[obs.report.ItemIndex] = true
		}
	}
	if first.IsZero() {
		return 0, indexesSeen
	}
	return last.Sub(first), indexesSeen
}

// TestWeatherDelayCancelNight proves ADR-053 decisions 1 and 11's
// cancel-night path against the real coordinator: a delay changes to a
// cancel in place with its start time kept, the cancel alert plays in full
// on its OWN session even though it replaces a delay alert already loaded
// and playing on a different one, a night start is refused with the
// cancelled sentence, a delay start does not downgrade the cancellation,
// clearing it starts nothing, and the graceful night shutdown runs only
// once the cancel alert has ended.
func TestWeatherDelayCancelNight(t *testing.T) {
	f := newWeatherDelayFixture(t, 0)

	// A cancel alert of about twelve seconds. The shutdown watcher polls
	// every two seconds, so a shorter alert would let the shutdown run
	// before this test could observe that it had not yet.
	cancelAssetID := uploadWeatherDelayAlertAsset(t, f.coord, f.token, f.showID, "cancel-alert", f.nodeID, 4)
	putWeatherDelayConfig(t, f.coord, f.token, v1.ConfigWeatherDelayPayload{
		Alert: v1.ConfigWeatherDelayAlertPayload{
			DelayAssetID: f.assetID, CancelNightAssetID: cancelAssetID, RepeatCount: 3, NodeIDs: []string{f.nodeID},
		},
		PowerGroups: []v1.ConfigWeatherDelayPowerGroupPayload{weatherDelayPowerGroupPayload(f.groupID, f.fppID)},
		Triggers:    weatherDelayDefaultTriggers(),
	})
	waitFor(t, 30*time.Second, 200*time.Millisecond, func() bool {
		resp := weatherDelayState(t, f.coord)
		for _, na := range resp.Assets {
			if na.NodeID == f.nodeID {
				return na.CancelAsset != nil && na.CancelAsset.AssetID == cancelAssetID && na.CancelAsset.Present
			}
		}
		return false
	}, "the cancel-night alert asset to sync to the node once show.weatherdelay names it")

	audioSub := subscribeAudioReports(t, f.nodeID)
	startResp := weatherDelayStartAction(t, f.coord, f.token)
	waitForNodeAlertSession(t, audioSub, "playing")
	delayAlertRevision := alertPlaylistRevision(t, audioSub)

	cancelResp := weatherDelayAction(t, f.coord, f.token, "/api/v1/weather-delay/cancel-night")
	if cancelResp.Result.Kind != "cancelNight" {
		t.Fatalf("cancel-night: Kind = %q, want %q", cancelResp.Result.Kind, "cancelNight")
	}
	// Compared as instants, not as strings: start answers from the record it
	// just built, in this coordinator's local zone, and cancel-night answers
	// from the same record read back out of the database, in UTC. Same
	// moment, two renderings.
	if !weatherDelayParseTime(t, cancelResp.Result.StartedAt).Equal(weatherDelayParseTime(t, startResp.Result.StartedAt)) {
		t.Fatalf("cancel-night changed startedAt: was %q, now %q, want the same moment (ADR-053 decision 1 changes a delay in place)", startResp.Result.StartedAt, cancelResp.Result.StartedAt)
	}
	if cancelResp.Result.Revision <= startResp.Result.Revision {
		t.Fatalf("cancel-night revision = %d, want greater than the delay's %d so nodes act on it", cancelResp.Result.Revision, startResp.Result.Revision)
	}

	// The node starts the cancel alert on its OWN session, never the delay
	// alert's: a delay-to-cancel change in place must not Apply the cancel
	// alert's playlist onto a session the delay alert already has loaded
	// and playing.
	waitFor(t, 20*time.Second, 200*time.Millisecond, func() bool {
		p, ok := audioSub.latestFor(weatherDelayCancelAlertSessionIDForTest)
		return ok && p.HasPlaylist && strings.HasPrefix(p.ItemID, "cancelNight-")
	}, "the node to start the cancel-night alert on its own session rather than the delay alert's")
	cancelPlaylistRevision := alertPlaylistRevisionForSession(t, audioSub, weatherDelayCancelAlertSessionIDForTest)
	if p, ok := audioSub.latestFor(weatherDelayAlertSessionIDForTest); ok && p.HasPlaylist && p.PlaylistRevision != delayAlertRevision {
		t.Fatalf("the delay alert's own session playlist revision changed from %d to %d after the night was cancelled; the cancel alert must never Apply its playlist onto the delay alert's session", delayAlertRevision, p.PlaylistRevision)
	}

	// The cancel alert, replacing a delay alert already loaded and playing
	// on a different session, must still play in full: repeatCount 3
	// against a 4s asset is about 12s. Before the fix, a delay-to-cancel
	// change in place Applied the cancel alert's playlist onto the delay
	// alert's OWN loaded session, and the audio manager's Apply-over-a-
	// loaded-playlist defect truncated it to well under a second.
	sinceCancelSeq := weatherDelayLatestEventSeq(t, f.coord)
	playedFor, indexesSeen := observeAlertPlayback(t, audioSub, weatherDelayCancelAlertSessionIDForTest, cancelPlaylistRevision, 20*time.Second)
	if playedFor < 10*time.Second {
		t.Fatalf("the cancel alert reported playing state for only %s after replacing a loaded delay alert, want at least 10s (repeatCount 3 x a 4s asset); ADR-053 decision 7 requires it to play in full", playedFor)
	}
	for _, idx := range []int64{0, 1, 2} {
		if !indexesSeen[idx] {
			t.Fatalf("the cancel alert never reported item index %d playing after replacing a loaded delay alert; observed indexes: %v", idx, indexesSeen)
		}
	}
	if weatherDelayNightShutdownRan(t, f.coord, sinceCancelSeq) {
		t.Fatalf("the graceful night shutdown ran before the cancel alert, replacing a loaded delay alert, finished playing")
	}

	status, body := postRawWithToken(t, f.coord, "/api/v1/night/commands/start-night", f.token, map[string]string{"idempotencyKey": "wd-" + uniqueSuffix()})
	if status != http.StatusConflict {
		t.Fatalf("night start after cancel-night: status = %d, want 409; body: %s", status, body)
	}
	if !strings.Contains(string(body), "Tonight's show was cancelled for weather. Clear the cancellation to start a show.") {
		t.Fatalf("night-start refusal after cancel-night missing the cancelled sentence: %s", body)
	}

	// A delay start while the night is cancelled re-affirms the cancel and
	// never downgrades it.
	again := weatherDelayStartAction(t, f.coord, f.token)
	if again.Result.Kind != "cancelNight" {
		t.Fatalf("a delay start while cancelled returned kind %q, want %q; ADR-053 decision 11 keeps the cancellation set", again.Result.Kind, "cancelNight")
	}
	if !strings.Contains(again.Result.Message, "The night is already cancelled") {
		t.Fatalf("a delay start while cancelled returned message %q, want the already-cancelled sentence", again.Result.Message)
	}
	if st := weatherDelayState(t, f.coord); !st.Active || st.Kind != "cancelNight" {
		t.Fatalf("stored state after a delay start while cancelled: active = %v, kind = %q, want active cancelNight", st.Active, st.Kind)
	}

	// Clearing the cancellation starts nothing: the player is never told to
	// play and never reports playing again on its own.
	startPlaylistBefore := f.stub.commandCount("Start Playlist")
	weatherDelayResumeAction(t, f.coord, f.token)
	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		return !weatherDelayState(t, f.coord).Active
	}, "the coordinator to report nothing active once the cancellation is cleared")
	time.Sleep(3 * time.Second)
	if n := f.stub.commandCount("Start Playlist"); n != startPlaylistBefore {
		t.Fatalf("the stand-in FPP player received %d Start Playlist command(s) after the cancellation was cleared, want %d; ADR-053 decision 11 says clearing starts nothing", n, startPlaylistBefore)
	}
	if got := f.stub.currentStatusName(); got != "idle" {
		t.Fatalf("the stand-in FPP player reports %q after the cancellation was cleared, want idle; clearing starts nothing", got)
	}

	// The graceful night shutdown waits for the cancel alert. The clear above
	// stopped the alert session, so this cancel's own alert plays in full,
	// starting fresh on its own session (nothing was loaded on it before).
	sinceSeq := weatherDelayLatestEventSeq(t, f.coord)
	weatherDelayAction(t, f.coord, f.token, "/api/v1/weather-delay/cancel-night")
	waitForNodeAlertSessionID(t, audioSub, weatherDelayCancelAlertSessionIDForTest, "playing")
	if weatherDelayNightShutdownRan(t, f.coord, sinceSeq) {
		t.Fatalf("the graceful night shutdown ran while the cancel alert was still playing; ADR-053 decision 11 runs it only after the alert ends")
	}
	if p, ok := audioSub.latestFor(weatherDelayCancelAlertSessionIDForTest); !ok || p.State != "playing" {
		t.Fatalf("the cancel alert was no longer playing (report present: %v, state %q) when the shutdown was checked, so that check proved nothing", ok, p.State)
	}
	waitFor(t, 60*time.Second, 200*time.Millisecond, func() bool {
		p, ok := audioSub.latestFor(weatherDelayCancelAlertSessionIDForTest)
		return ok && p.State != "playing"
	}, "the cancel-night alert to finish its own repeat count")
	waitFor(t, 60*time.Second, 500*time.Millisecond, func() bool {
		return weatherDelayNightShutdownRan(t, f.coord, sinceSeq)
	}, "the graceful night shutdown to run once the cancel alert has ended")

	weatherDelayResumeAction(t, f.coord, f.token)
}
