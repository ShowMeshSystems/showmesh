//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/paho"
	"github.com/google/uuid"

	"github.com/showmeshsystems/showmesh/pkg/coordsig"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
	"github.com/showmeshsystems/showmesh/pkg/fallbackactivation"
	"github.com/showmeshsystems/showmesh/pkg/fallbackprogram"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// Track J step J3's acceptance against a real showmesh-agent process and
// broker. The test plays both parties the node trusts: it signs the program
// with the coordinator key the node pinned, and each request as the executor.

const (
	fallbackTestShow       = "fallback-show"
	fallbackTestGeneration = int64(4)
	fallbackTestFPPHost    = "22222222-2222-4222-8222-222222222222"
	fallbackTestOtherHost  = "33333333-3333-4333-8333-333333333333"
)

type fallbackNode struct {
	t           *testing.T
	nodeID      string
	assetDir    string
	listenAddr  string
	keyPath     string
	coordinator ed25519.PrivateKey
	executor    ed25519.PrivateKey
	agent       *testAgent
	cmd         *paho.Client
	results     *resultWatcher
}

func newFallbackKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func fallbackPublicB64(key ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
}

func (n *fallbackNode) startAgent() {
	n.t.Helper()
	n.agent = startAgent(n.t, agentConfig{
		nodeID: n.nodeID, assetDir: n.assetDir,
		extraEnv: []string{
			"SHOWMESH_FPPCONNECT_LISTEN_ADDR=" + n.listenAddr,
			"SHOWMESH_WEATHERDELAY_COORDINATOR_PUBLIC_KEY_PATH=" + n.keyPath,
		},
	})
	n.cmd, n.results = startCmdClient(n.t, n.nodeID)
	awaitAgentReceivingCommands(n.t, n.cmd, n.results, n.nodeID)
	waitFor(n.t, 15*time.Second, 100*time.Millisecond, func() bool {
		resp, err := http.Get("http://" + n.listenAddr + "/showmesh/v1/fallback/activations")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return true
	}, "the node's inbound listener to accept connections")
}

// newFallbackNode starts a real agent that pinned the test's coordinator
// key, or no key at all when pinKey is false.
func newFallbackNode(t *testing.T, pinKey bool) *fallbackNode {
	t.Helper()
	requireBroker(t)
	n := &fallbackNode{
		t: t, nodeID: "node-" + uniqueSuffix(), assetDir: writeTempDirHelper(t),
		listenAddr:  fmt.Sprintf("127.0.0.1:%d", findFreePort(t)),
		coordinator: newFallbackKey(t), executor: newFallbackKey(t),
	}
	if pinKey {
		n.keyPath = filepath.Join(t.TempDir(), "coordinator-public.key")
		if err := os.WriteFile(n.keyPath, []byte(fallbackPublicB64(n.coordinator)), 0o600); err != nil {
			t.Fatalf("write coordinator public key: %v", err)
		}
	}
	n.startAgent()
	return n
}

// command sends one command over MQTT, as a coordinator dispatch does,
// and returns the node's result.
func (n *fallbackNode) command(action string, params map[string]any) mqttproto.ResultPayload {
	n.t.Helper()
	id := "cmd-" + uniqueSuffix()
	dispatchCmd(n.t, n.cmd, n.nodeID, mqttproto.CmdPayload{
		CommandID: id, IdempotencyKey: id, Action: action,
		Target: mqttproto.CmdTarget{Kind: "node", ID: n.nodeID}, Params: params,
		Issuer:             mqttproto.CmdIssuer{PrincipalID: "test-principal", PrincipalName: "integration-test"},
		ConfirmationMethod: "evidence",
	})
	return waitForResult(n.t, n.results, id, 15*time.Second)
}

// deployCatalog gives the node a held Cue catalog over the normal command
// path and returns its revision. cue-plain has no output; cue-file needs a
// file this node does not have.
func (n *fallbackNode) deployCatalog(marker string) string {
	n.t.Helper()
	entries := []cuecatalog.Entry{
		{CueID: "cue-file", CueRevision: 1, Outputs: cuecatalog.Outputs{
			Render: &cuecatalog.RenderOutput{Sequence: marker, Filename: marker + ".fseq", AssetHashes: []string{"0000"}},
		}},
		{CueID: "cue-plain", CueRevision: 1},
	}
	revision, err := cuecatalog.ComputeRevision(cuecatalog.RevisionInput{
		Show: fallbackTestShow, Generation: fallbackTestGeneration, Node: n.nodeID, Entries: entries,
	})
	if err != nil {
		n.t.Fatalf("compute catalog revision: %v", err)
	}
	raw, err := json.Marshal(map[string]any{
		"show": fallbackTestShow, "generation": fallbackTestGeneration, "revision": revision, "entries": entries,
	})
	if err != nil {
		n.t.Fatalf("marshal catalog: %v", err)
	}
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		n.t.Fatalf("decode catalog params: %v", err)
	}
	if result := n.command("cuecatalog.deploy", params); result.Outcome != "confirmed" {
		n.t.Fatalf("cuecatalog.deploy outcome = %q (%s), want confirmed", result.Outcome, result.Reason)
	}
	return revision
}

// dispatchCue runs one Cue through the normal coordinator dispatch and
// returns the outcome word the node reported.
func (n *fallbackNode) dispatchCue(cueID, catalogRevision string) string {
	n.t.Helper()
	result := n.command("cue.activate", map[string]any{
		"runner": "showmesh", "runnerInstance": "integration-test", "activationId": uuid.NewString(),
		"show": fallbackTestShow, "generation": fallbackTestGeneration, "catalogRevision": catalogRevision,
		"cueId": cueID, "cueRevision": 1, "positionMs": 0, "evidenceAt": time.Now().UTC().Format(time.RFC3339Nano),
	})
	if result.Evidence == nil {
		n.t.Fatalf("cue.activate for %s carried no evidence: outcome %q, reason %q", cueID, result.Outcome, result.Reason)
	}
	value, _ := result.Evidence.Value.(map[string]any)
	outcome, _ := value["outcome"].(string)
	if outcome == "" {
		n.t.Fatalf("cue.activate for %s reported no outcome word: %+v", cueID, result.Evidence.Value)
	}
	return outcome
}

// program builds what the coordinator would publish for host: entry-plain
// and entry-file target this node, entry-elsewhere targets another node.
func (n *fallbackNode) program(host, catalogRevision string, executor ed25519.PrivateKey, validFor time.Duration) fallbackprogram.Program {
	compiledAt := time.Now().UTC().Truncate(time.Millisecond)
	render := &fallbackprogram.RenderActivation{Sequence: "seq", Filename: "seq.fseq", AssetHashes: []string{"0000"}}
	here := []fallbackprogram.NodeTarget{{NodeID: n.nodeID, Address: n.listenAddr, Render: render}}
	return fallbackprogram.Program{
		SchemaVersion: fallbackprogram.SchemaVersion, PackageID: uuid.NewString(), Revision: "rev-" + uniqueSuffix(),
		ExpiresAt: compiledAt.Add(validFor), CompiledAt: compiledAt,
		FPPInstanceUUID: host, ExecutorPublicKey: fallbackPublicB64(executor),
		Show: fallbackTestShow, Generation: fallbackTestGeneration,
		PlaylistRevisions: map[string]int64{"pl-main": 1},
		CatalogRevisions:  map[string]string{n.nodeID: catalogRevision, "node-elsewhere": "other"},
		Entries: []fallbackprogram.EntryMapping{
			{EntryKey: "entry-elsewhere", CueID: "cue-plain", CueRevision: 1, Targets: []fallbackprogram.NodeTarget{{NodeID: "node-elsewhere", Render: render}}},
			{EntryKey: "entry-file", CueID: "cue-file", CueRevision: 1, Targets: here},
			{EntryKey: "entry-plain", CueID: "cue-plain", CueRevision: 1, Targets: here},
		},
		Rules: fallbackprogram.FixedRules,
	}
}

type fallbackAnswerForTest struct {
	Accepted     bool   `json:"accepted"`
	Outcome      string `json:"outcome"`
	Reason       string `json:"reason"`
	FirstOutcome string `json:"firstOutcome"`
}

func (n *fallbackNode) send(method, path string, body []byte) (int, fallbackAnswerForTest) {
	n.t.Helper()
	req, err := http.NewRequest(method, "http://"+n.listenAddr+path, bytes.NewReader(body))
	if err != nil {
		n.t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		n.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		n.t.Fatalf("%s %s: read response: %v", method, path, err)
	}
	var answer fallbackAnswerForTest
	if err := json.Unmarshal(raw, &answer); err != nil {
		n.t.Fatalf("%s %s: status %d, response is not the fixed JSON shape: %s", method, path, resp.StatusCode, raw)
	}
	return resp.StatusCode, answer
}

// deliver signs p with signer and hands it to the node under p's FPP host.
func (n *fallbackNode) deliver(p fallbackprogram.Program, signer ed25519.PrivateKey) (int, fallbackAnswerForTest) {
	n.t.Helper()
	canonical, err := p.CanonicalBytes()
	if err != nil {
		n.t.Fatalf("canonicalize program: %v", err)
	}
	document, err := json.Marshal(struct {
		Program   json.RawMessage    `json:"program"`
		Signature coordsig.Signature `json:"signature"`
	}{Program: canonical, Signature: ed25519.Sign(signer, canonical)})
	if err != nil {
		n.t.Fatalf("marshal signed program: %v", err)
	}
	return n.send(http.MethodPut, fallbackactivation.ProgramPathPrefix+p.FPPInstanceUUID, document)
}

func (n *fallbackNode) mustDeliver(p fallbackprogram.Program) {
	n.t.Helper()
	if status, answer := n.deliver(p, n.coordinator); status != http.StatusOK || answer.Outcome != "installed" {
		n.t.Fatalf("deliver program for %s: status %d outcome %q (%s), want it installed", p.FPPInstanceUUID, status, answer.Outcome, answer.Reason)
	}
}

func fallbackRequestFor(p fallbackprogram.Program, nodeID, entryKey, cueID string) fallbackactivation.Request {
	return fallbackactivation.Request{
		SchemaVersion: fallbackactivation.SchemaVersion, ExecutionID: uuid.NewString(),
		FPPInstanceUUID: p.FPPInstanceUUID, PackageID: p.PackageID, PackageRevision: p.Revision,
		ProgramExpiresAt: p.ExpiresAt, Generation: p.Generation, CatalogRevision: p.CatalogRevisions[nodeID],
		EntryKey: entryKey, CueID: cueID, CueRevision: 1, NodeID: nodeID,
	}
}

func (n *fallbackNode) signedBody(r fallbackactivation.Request, signer ed25519.PrivateKey) []byte {
	n.t.Helper()
	body, err := fallbackactivation.Sign(r, signer)
	if err != nil {
		n.t.Fatalf("sign request: %v", err)
	}
	return body
}

func (n *fallbackNode) activate(r fallbackactivation.Request, signer ed25519.PrivateKey) (int, fallbackAnswerForTest) {
	n.t.Helper()
	return n.send(http.MethodPost, fallbackactivation.ActivationPath, n.signedBody(r, signer))
}

// fallbackReportSubscriber keeps the node's latest retained fallback
// report, read with the coordinator's own broker credential.
type fallbackReportSubscriber struct {
	mu     sync.Mutex
	latest *mqttproto.FallbackPayload
}

func subscribeFallbackReports(t *testing.T, nodeID string) *fallbackReportSubscriber {
	t.Helper()
	cli := rawConnect(t, testMQTTCoordinatorUsername, testMQTTCoordinatorPassword)
	sub := &fallbackReportSubscriber{}
	cli.AddOnPublishReceived(func(pr paho.PublishReceived) (bool, error) {
		env, err := mqttproto.DecodeEnvelope(pr.Packet.Payload)
		if err != nil {
			return false, nil
		}
		payload, err := mqttproto.DecodeFallbackPayload(env)
		if err != nil {
			return false, nil
		}
		sub.mu.Lock()
		sub.latest = &payload
		sub.mu.Unlock()
		return true, nil
	})
	topic, err := mqttproto.ObservedTopic(nodeID, mqttproto.ObservedSubpathFallback)
	if err != nil {
		t.Fatalf("ObservedTopic: %v", err)
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

func (s *fallbackReportSubscriber) outcomes() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	if s.latest != nil {
		for _, d := range s.latest.Decisions {
			seen[d.Outcome] = true
		}
	}
	return seen
}

func TestFallbackActivationRefusalsAgainstRealAgent(t *testing.T) {
	n := newFallbackNode(t, true)
	reports := subscribeFallbackReports(t, n.nodeID)
	catalogRevision := n.deployCatalog("first")
	program := n.program(fallbackTestFPPHost, catalogRevision, n.executor, 10*time.Minute)
	n.mustDeliver(program)
	otherExecutor := newFallbackKey(t)
	n.mustDeliver(n.program(fallbackTestOtherHost, catalogRevision, otherExecutor, 10*time.Minute))

	base := func() fallbackactivation.Request {
		return fallbackRequestFor(program, n.nodeID, "entry-plain", "cue-plain")
	}
	cases := []struct {
		name    string
		signer  ed25519.PrivateKey
		mutate  func(*fallbackactivation.Request)
		outcome string
	}{
		{"invalid signature", newFallbackKey(t), func(*fallbackactivation.Request) {}, "signature-invalid"},
		{"wrong FPP host, program held", n.executor, func(r *fallbackactivation.Request) { r.FPPInstanceUUID = fallbackTestOtherHost }, "signature-invalid"},
		{"wrong FPP host, no program held", n.executor, func(r *fallbackactivation.Request) { r.FPPInstanceUUID = "44444444-4444-4444-8444-444444444444" }, "program-not-installed"},
		{"wrong target node", n.executor, func(r *fallbackactivation.Request) { r.NodeID = "node-elsewhere" }, "wrong-target"},
		{"entry for another node", n.executor, func(r *fallbackactivation.Request) { r.EntryKey = "entry-elsewhere" }, "wrong-target"},
		{"stale generation", n.executor, func(r *fallbackactivation.Request) { r.Generation = fallbackTestGeneration - 1 }, "stale-generation"},
		{"stale catalog", n.executor, func(r *fallbackactivation.Request) { r.CatalogRevision = "an-older-catalog" }, "stale-catalog"},
		{"unknown entry", n.executor, func(r *fallbackactivation.Request) { r.EntryKey = "entry-not-listed" }, "unknown-entry"},
		{"arbitrary Cue", n.executor, func(r *fallbackactivation.Request) { r.CueID = "cue-file" }, "cue-not-authorized"},
	}
	for _, tc := range cases {
		r := base()
		tc.mutate(&r)
		status, answer := n.activate(r, tc.signer)
		if answer.Outcome != tc.outcome || answer.Accepted || status < 400 {
			t.Errorf("%s: status %d outcome %q accepted %v, want a refusal with %q (reason: %s)", tc.name, status, answer.Outcome, answer.Accepted, tc.outcome, answer.Reason)
		}
	}

	// An arbitrary command: a correctly signed request with a member the
	// route does not name is refused before anything reads it.
	withAction := map[string]any{}
	raw, _ := json.Marshal(base())
	_ = json.Unmarshal(raw, &withAction)
	withAction["action"] = "render.surface.blackout"
	signedRaw, _ := json.Marshal(withAction)
	arbitrary, _ := json.Marshal(map[string]any{
		"request": json.RawMessage(signedRaw), "signature": coordsig.Signature(ed25519.Sign(n.executor, signedRaw)),
	})
	if status, answer := n.send(http.MethodPost, fallbackactivation.ActivationPath, arbitrary); status != http.StatusBadRequest || answer.Outcome != "malformed-request" {
		t.Errorf("arbitrary command member: status %d outcome %q, want 400 malformed-request", status, answer.Outcome)
	}

	// Expired program: a third FPP host's program that lapses in seconds.
	const expiringHost = "55555555-5555-4555-8555-555555555555"
	expiring := n.program(expiringHost, catalogRevision, n.executor, 3*time.Second)
	n.mustDeliver(expiring)
	time.Sleep(time.Until(expiring.ExpiresAt) + 300*time.Millisecond)
	if status, answer := n.activate(fallbackRequestFor(expiring, n.nodeID, "entry-plain", "cue-plain"), n.executor); status != http.StatusConflict || answer.Outcome != "program-expired" {
		t.Errorf("expired program: status %d outcome %q, want 409 program-expired", status, answer.Outcome)
	}

	// A program the coordinator did not sign is never installed.
	if status, answer := n.deliver(n.program("66666666-6666-4666-8666-666666666666", catalogRevision, n.executor, time.Minute), newFallbackKey(t)); status != http.StatusForbidden || answer.Outcome != "program-signature-invalid" {
		t.Errorf("program signed by another key: status %d outcome %q, want 403 program-signature-invalid", status, answer.Outcome)
	}

	// Every refusal reaches the broker in the node's retained report, read
	// here with the coordinator's own credential.
	want := []string{"signature-invalid", "program-not-installed", "wrong-target", "stale-generation", "stale-catalog",
		"unknown-entry", "cue-not-authorized", "malformed-request", "program-expired", "program-signature-invalid"}
	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		seen := reports.outcomes()
		for _, outcome := range want {
			if !seen[outcome] {
				return false
			}
		}
		return true
	}, "the node's fallback report to carry every refusal")
}

func TestFallbackActivationTakesTheNormalCuePathAndSurvivesRestart(t *testing.T) {
	n := newFallbackNode(t, true)
	catalogRevision := n.deployCatalog("first")
	program := n.program(fallbackTestFPPHost, catalogRevision, n.executor, 10*time.Minute)
	n.mustDeliver(program)

	// A Cue the node can run: both paths authorize it.
	plain := fallbackRequestFor(program, n.nodeID, "entry-plain", "cue-plain")
	status, answer := n.activate(plain, n.executor)
	if status != http.StatusOK || answer.Outcome != "authorized" || !answer.Accepted {
		t.Fatalf("valid fallback request: status %d outcome %q (%s), want 200 authorized", status, answer.Outcome, answer.Reason)
	}
	if normal := n.dispatchCue("cue-plain", catalogRevision); normal != answer.Outcome {
		t.Fatalf("normal dispatch of the same Cue answered %q, the fallback request answered %q", normal, answer.Outcome)
	}

	// A Cue whose file the node lacks: the program allows it, so only the
	// node's own Cue check can refuse it, and both paths give its word.
	_, fileAnswer := n.activate(fallbackRequestFor(program, n.nodeID, "entry-file", "cue-file"), n.executor)
	if normal := n.dispatchCue("cue-file", catalogRevision); normal != "asset-missing" || fileAnswer.Outcome != normal {
		t.Fatalf("Cue with a missing file: normal dispatch answered %q, the fallback request answered %q, want asset-missing from both", normal, fileAnswer.Outcome)
	}

	// Replay, then a real restart of the agent on the same disk.
	body := n.signedBody(plain, n.executor)
	if status, replay := n.send(http.MethodPost, fallbackactivation.ActivationPath, body); status != http.StatusConflict || replay.Outcome != "replayed-execution" || replay.FirstOutcome != "authorized" {
		t.Fatalf("replay before restart: status %d outcome %q first %q, want 409 replayed-execution first authorized", status, replay.Outcome, replay.FirstOutcome)
	}
	n.agent.sigkill(t)
	n.agent.waitForExit(t, 15*time.Second)
	n.startAgent()
	if status, replay := n.send(http.MethodPost, fallbackactivation.ActivationPath, body); status != http.StatusConflict || replay.Outcome != "replayed-execution" || replay.FirstOutcome != "authorized" {
		t.Fatalf("replay after restart: status %d outcome %q first %q, want 409 replayed-execution first authorized", status, replay.Outcome, replay.FirstOutcome)
	}

	// The restarted node still holds the program: a new execution is
	// authorized with no second delivery.
	if status, again := n.activate(fallbackRequestFor(program, n.nodeID, "entry-plain", "cue-plain"), n.executor); status != http.StatusOK || again.Outcome != "authorized" {
		t.Fatalf("new execution after restart: status %d outcome %q (%s), want 200 authorized", status, again.Outcome, again.Reason)
	}

	// The node's catalog moves on while the program stands still: the
	// request still matches the program, and both paths refuse it alike.
	n.deployCatalog("second")
	_, staleAnswer := n.activate(fallbackRequestFor(program, n.nodeID, "entry-plain", "cue-plain"), n.executor)
	if normal := n.dispatchCue("cue-plain", catalogRevision); normal != "stale-catalog" || staleAnswer.Outcome != normal {
		t.Fatalf("after the node's catalog changed: normal dispatch answered %q, the fallback request answered %q, want stale-catalog from both", normal, staleAnswer.Outcome)
	}
}

func TestFallbackActivationRealAgentWithNoCoordinatorKeyRefuses(t *testing.T) {
	n := newFallbackNode(t, false)
	catalogRevision := n.deployCatalog("first")
	program := n.program(fallbackTestFPPHost, catalogRevision, n.executor, 10*time.Minute)

	if status, answer := n.deliver(program, n.coordinator); status != http.StatusServiceUnavailable || answer.Outcome != "no-coordinator-key" || answer.Reason == "" {
		t.Fatalf("program delivery: status %d outcome %q reason %q, want 503 no-coordinator-key with a reason", status, answer.Outcome, answer.Reason)
	}
	if status, answer := n.activate(fallbackRequestFor(program, n.nodeID, "entry-plain", "cue-plain"), n.executor); status != http.StatusServiceUnavailable || answer.Outcome != "no-coordinator-key" || answer.Accepted {
		t.Fatalf("activation: status %d outcome %q accepted %v, want 503 no-coordinator-key refused", status, answer.Outcome, answer.Accepted)
	}
	// The agent is still up and still serves the normal path.
	if normal := n.dispatchCue("cue-plain", catalogRevision); normal != "authorized" {
		t.Fatalf("normal dispatch on a node with no coordinator key answered %q, want authorized", normal)
	}
}
