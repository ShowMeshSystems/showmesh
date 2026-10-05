package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/heldcatalog"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
	"github.com/showmeshsystems/showmesh/pkg/fallbackactivation"
)

// These tests drive the fallback ingress with the shared fixtures in
// test/fixtures/fallback-activation, the same files the plugin copies.

const fallbackFixtureDir = "../../test/fixtures/fallback-activation"

type fallbackFixtureCases struct {
	NodeID      string `json:"nodeId"`
	NodeCatalog struct {
		Show            string `json:"show"`
		Generation      int64  `json:"generation"`
		CatalogRevision string `json:"catalogRevision"`
		Cues            []struct {
			CueID       string `json:"cueId"`
			CueRevision int64  `json:"cueRevision"`
		} `json:"cues"`
	} `json:"nodeCatalog"`
	Activations []struct {
		Name            string   `json:"name"`
		Installed       []string `json:"installed"`
		Now             string   `json:"now"`
		Body            string   `json:"body"`
		RepeatBody      bool     `json:"repeatBody"`
		ExpectedStatus  int      `json:"expectedStatus"`
		ExpectedOutcome string   `json:"expectedOutcome"`
	} `json:"activations"`
	Programs []struct {
		Name            string   `json:"name"`
		Installed       []string `json:"installed"`
		Now             string   `json:"now"`
		PathHost        string   `json:"pathFppInstanceUuid"`
		Document        string   `json:"document"`
		ExpectedStatus  int      `json:"expectedStatus"`
		ExpectedOutcome string   `json:"expectedOutcome"`
	} `json:"programs"`
}

func readFallbackFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fallbackFixtureDir, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func loadFallbackFixtureCases(t *testing.T) fallbackFixtureCases {
	t.Helper()
	var cases fallbackFixtureCases
	if err := json.Unmarshal(readFallbackFixture(t, "cases.json"), &cases); err != nil {
		t.Fatalf("decode cases.json: %v", err)
	}
	return cases
}

func fallbackFixtureCoordinatorKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	var keys struct {
		CoordinatorPublicKey string `json:"coordinatorPublicKey"`
	}
	if err := json.Unmarshal(readFallbackFixture(t, "keys.json"), &keys); err != nil {
		t.Fatalf("decode keys.json: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(keys.CoordinatorPublicKey)
	if err != nil {
		t.Fatalf("decode coordinator key: %v", err)
	}
	return ed25519.PublicKey(raw)
}

// fallbackTestNode is one node under test: a real ingress over a real
// cue.activate operation and held catalog, with a clock the test moves.
type fallbackTestNode struct {
	t       *testing.T
	dir     string
	key     ed25519.PublicKey
	nodeID  string
	handler http.Handler
	ingress *fallbackIngress

	mu          sync.Mutex
	now         time.Time
	activations []map[string]any
}

func (n *fallbackTestNode) clock() time.Time {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.now
}

func (n *fallbackTestNode) setNow(t time.Time) {
	n.mu.Lock()
	n.now = t
	n.mu.Unlock()
}

func (n *fallbackTestNode) activationCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.activations)
}

// start builds the ingress from what is on disk, as an agent start does.
func (n *fallbackTestNode) start() {
	op := &cueActivationOperation{assetDir: n.dir, catalogStore: heldcatalog.NewFileStore(n.dir), nodeID: n.nodeID}
	n.ingress = newFallbackIngress(n.nodeID, n.dir, n.key, n.clock, discardLogger())
	n.ingress.setActivate(func(ctx context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
		n.mu.Lock()
		n.activations = append(n.activations, params)
		n.mu.Unlock()
		return op.activate(ctx, params, now)
	})
	n.handler = n.ingress.wrap(http.NotFoundHandler())
}

func newFallbackTestNode(t *testing.T, cases fallbackFixtureCases, key ed25519.PublicKey, now time.Time) *fallbackTestNode {
	t.Helper()
	n := &fallbackTestNode{t: t, dir: t.TempDir(), key: key, nodeID: cases.NodeID, now: now}
	entries := make([]cuecatalog.Entry, 0, len(cases.NodeCatalog.Cues))
	for _, cue := range cases.NodeCatalog.Cues {
		entries = append(entries, cuecatalog.Entry{CueID: cue.CueID, CueRevision: cue.CueRevision})
	}
	if err := heldcatalog.NewFileStore(n.dir).Save(heldcatalog.HeldCatalog{
		Show: cases.NodeCatalog.Show, Generation: cases.NodeCatalog.Generation, Node: cases.NodeID,
		Revision: cases.NodeCatalog.CatalogRevision, Entries: entries, ReceivedAt: now,
	}); err != nil {
		t.Fatalf("save held catalog: %v", err)
	}
	n.start()
	return n
}

type fallbackResponseForTest struct {
	Accepted     bool   `json:"accepted"`
	Outcome      string `json:"outcome"`
	Reason       string `json:"reason"`
	ExecutionID  string `json:"executionId"`
	FirstOutcome string `json:"firstOutcome"`
}

func (n *fallbackTestNode) do(method, path, body string) (int, fallbackResponseForTest) {
	n.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "192.0.2.10:40000"
	rec := httptest.NewRecorder()
	n.handler.ServeHTTP(rec, req)
	var out fallbackResponseForTest
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		n.t.Fatalf("%s %s: response is not the fixed JSON shape: %v; body: %s", method, path, err, rec.Body.String())
	}
	return rec.Code, out
}

func (n *fallbackTestNode) putProgram(host, fixture string) (int, fallbackResponseForTest) {
	n.t.Helper()
	return n.do(http.MethodPut, fallbackactivation.ProgramPathPrefix+host, string(readFallbackFixture(n.t, fixture)))
}

func (n *fallbackTestNode) postActivation(body string) (int, fallbackResponseForTest) {
	n.t.Helper()
	return n.do(http.MethodPost, fallbackactivation.ActivationPath, body)
}

// install delivers each named program under the FPP player it names.
func (n *fallbackTestNode) install(fixtures []string) {
	n.t.Helper()
	for _, fixture := range fixtures {
		var doc struct {
			Program struct {
				FPPInstanceUUID string `json:"fppInstanceUuid"`
			} `json:"program"`
		}
		if err := json.Unmarshal(readFallbackFixture(n.t, fixture), &doc); err != nil {
			n.t.Fatalf("decode %s: %v", fixture, err)
		}
		if status, resp := n.putProgram(doc.Program.FPPInstanceUUID, fixture); status != http.StatusOK || resp.Outcome != "installed" {
			n.t.Fatalf("install %s: status %d outcome %q, want it installed", fixture, status, resp.Outcome)
		}
	}
}

// lastDecision is the newest entry of the node's own report.
func (n *fallbackTestNode) lastDecisionOutcome() string {
	report := n.ingress.report()
	if len(report.Decisions) == 0 {
		return ""
	}
	return report.Decisions[len(report.Decisions)-1].Outcome
}

func mustParseFixtureTime(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse fixture time %q: %v", s, err)
	}
	return parsed
}

// fallbackFixtureInstallTime is inside every fixture program's validity.
func fallbackFixtureInstallTime(t *testing.T, cases fallbackFixtureCases) time.Time {
	return mustParseFixtureTime(t, cases.Activations[0].Now)
}

func TestFallbackActivationFixtureCases(t *testing.T) {
	cases := loadFallbackFixtureCases(t)
	key := fallbackFixtureCoordinatorKey(t)
	want := map[string]bool{
		"authorized": false, "signature-invalid": false, "program-not-installed": false, "wrong-target": false,
		"stale-generation": false, "stale-catalog": false, "unknown-entry": false, "program-expired": false,
		"replayed-execution": false, "cue-not-authorized": false, "malformed-request": false,
		"program-not-current": false, "executor-not-enrolled": false,
	}
	for _, tc := range cases.Activations {
		t.Run(tc.Name, func(t *testing.T) {
			node := newFallbackTestNode(t, cases, key, fallbackFixtureInstallTime(t, cases))
			node.install(tc.Installed)
			node.setNow(mustParseFixtureTime(t, tc.Now))
			wantRuns := 0
			if tc.RepeatBody {
				if status, resp := node.postActivation(tc.Body); status != http.StatusOK || resp.Outcome != "authorized" {
					t.Fatalf("first send: status %d outcome %q, want it authorized", status, resp.Outcome)
				}
				wantRuns = 1
			}

			status, resp := node.postActivation(tc.Body)
			if status != tc.ExpectedStatus || resp.Outcome != tc.ExpectedOutcome {
				t.Fatalf("status %d outcome %q, want %d %q (reason: %s)", status, resp.Outcome, tc.ExpectedStatus, tc.ExpectedOutcome, resp.Reason)
			}
			if resp.Accepted != (tc.ExpectedOutcome == "authorized") {
				t.Fatalf("accepted = %v for outcome %q", resp.Accepted, resp.Outcome)
			}
			if resp.Reason == "" {
				t.Fatalf("outcome %q carried no reason", resp.Outcome)
			}
			if tc.ExpectedOutcome == "authorized" {
				wantRuns = 1
			}
			if got := node.activationCount(); got != wantRuns {
				t.Fatalf("the Cue activation ran %d times, want %d", got, wantRuns)
			}
			if got := node.lastDecisionOutcome(); got != tc.ExpectedOutcome {
				t.Fatalf("the node's report ends with outcome %q, want %q", got, tc.ExpectedOutcome)
			}
			want[tc.ExpectedOutcome] = true
		})
	}
	for outcome, seen := range want {
		if !seen {
			t.Errorf("no fixture case answers %q", outcome)
		}
	}
}

func TestFallbackProgramFixtureCases(t *testing.T) {
	cases := loadFallbackFixtureCases(t)
	key := fallbackFixtureCoordinatorKey(t)
	for _, tc := range cases.Programs {
		t.Run(tc.Name, func(t *testing.T) {
			node := newFallbackTestNode(t, cases, key, fallbackFixtureInstallTime(t, cases))
			node.install(tc.Installed)
			node.setNow(mustParseFixtureTime(t, tc.Now))

			status, resp := node.putProgram(tc.PathHost, tc.Document)
			if status != tc.ExpectedStatus || resp.Outcome != tc.ExpectedOutcome {
				t.Fatalf("status %d outcome %q, want %d %q (reason: %s)", status, resp.Outcome, tc.ExpectedStatus, tc.ExpectedOutcome, resp.Reason)
			}
			if got := node.lastDecisionOutcome(); got != tc.ExpectedOutcome {
				t.Fatalf("the node's report ends with outcome %q, want %q", got, tc.ExpectedOutcome)
			}
		})
	}
}

// The valid request must reach cue.activate as the envelope a coordinator
// dispatch carries, built only from the program and the signed request.
func TestFallbackValidRequestRunsTheNormalCueActivation(t *testing.T) {
	cases := loadFallbackFixtureCases(t)
	node := newFallbackTestNode(t, cases, fallbackFixtureCoordinatorKey(t), fallbackFixtureInstallTime(t, cases))
	node.install(cases.Activations[0].Installed)

	if status, resp := node.postActivation(cases.Activations[0].Body); status != http.StatusOK || !resp.Accepted {
		t.Fatalf("valid request: status %d outcome %q", status, resp.Outcome)
	}
	if node.activationCount() != 1 {
		t.Fatalf("the Cue activation ran %d times, want 1", node.activationCount())
	}
	got := node.activations[0]
	want := map[string]any{
		"runner": "fpp-fallback", "runnerInstance": "22222222-2222-4222-8222-222222222222",
		"activationId": "00000000-0000-4000-8000-000000000001", "show": "halloween",
		"generation": float64(7), "catalogRevision": "cat-rev-1", "cueId": "cue-a", "cueRevision": float64(3),
		"positionMs": float64(0),
	}
	for name, value := range want {
		if got[name] != value {
			t.Errorf("activation %s = %#v, want %#v", name, got[name], value)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok && name != "evidenceAt" {
			t.Errorf("activation carries %q, which no fallback request can supply", name)
		}
	}
}

// A node whose own held catalog has moved on refuses through the normal
// Cue activation, with that activation's own outcome word.
func TestFallbackRequestIsCheckedAgainstTheNodesOwnCatalog(t *testing.T) {
	cases := loadFallbackFixtureCases(t)
	node := newFallbackTestNode(t, cases, fallbackFixtureCoordinatorKey(t), fallbackFixtureInstallTime(t, cases))
	node.install(cases.Activations[0].Installed)
	if err := heldcatalog.NewFileStore(node.dir).Save(heldcatalog.HeldCatalog{
		Show: "halloween", Generation: 7, Node: node.nodeID, Revision: "cat-rev-2",
		Entries: []cuecatalog.Entry{{CueID: "cue-a", CueRevision: 3}}, ReceivedAt: node.clock(),
	}); err != nil {
		t.Fatalf("save newer held catalog: %v", err)
	}

	status, resp := node.postActivation(cases.Activations[0].Body)
	if status != http.StatusConflict || resp.Outcome != "stale-catalog" || resp.Accepted {
		t.Fatalf("status %d outcome %q accepted %v, want 409 stale-catalog refused", status, resp.Outcome, resp.Accepted)
	}
}

func TestFallbackReplayFenceAndProgramSurviveARestart(t *testing.T) {
	cases := loadFallbackFixtureCases(t)
	node := newFallbackTestNode(t, cases, fallbackFixtureCoordinatorKey(t), fallbackFixtureInstallTime(t, cases))
	node.install(cases.Activations[0].Installed)
	valid := cases.Activations[0].Body
	if status, resp := node.postActivation(valid); status != http.StatusOK || resp.Outcome != "authorized" {
		t.Fatalf("before restart: status %d outcome %q, want authorized", status, resp.Outcome)
	}

	node.start()

	status, resp := node.postActivation(valid)
	if status != http.StatusConflict || resp.Outcome != "replayed-execution" {
		t.Fatalf("after restart: status %d outcome %q, want 409 replayed-execution", status, resp.Outcome)
	}
	if resp.FirstOutcome != "authorized" {
		t.Fatalf("after restart: firstOutcome = %q, want authorized", resp.FirstOutcome)
	}
	if node.activationCount() != 1 {
		t.Fatalf("the Cue activation ran %d times across a restart, want 1", node.activationCount())
	}
}

// An id claimed just before a crash must stay claimed, even though no
// outcome was ever written for it.
func TestFallbackFenceKeepsAnIDClaimedBeforeACrash(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	fence, err := openFallbackFence(dir, now)
	if err != nil {
		t.Fatalf("open fence: %v", err)
	}
	if _, replayed, err := fence.claim("id-1", now.Add(time.Minute)); err != nil || replayed {
		t.Fatalf("first claim: replayed %v, err %v", replayed, err)
	}
	path := filepath.Join(dir, fallbackStateSubdir, fallbackFenceFile)
	torn, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open fence file: %v", err)
	}
	if _, err := torn.WriteString(`{"executionId":"id-2","expi`); err != nil {
		t.Fatalf("write torn line: %v", err)
	}
	_ = torn.Close()

	reopened, err := openFallbackFence(dir, now)
	if err != nil {
		t.Fatalf("reopen fence: %v", err)
	}
	first, replayed, err := reopened.claim("id-1", now.Add(time.Minute))
	if err != nil || !replayed || first != fallbackFenceOutcomeUnknown {
		t.Fatalf("claim after restart: first %q replayed %v err %v, want an unknown first outcome replayed", first, replayed, err)
	}
	if _, replayed, err := reopened.claim("id-2", now.Add(time.Minute)); err != nil || replayed {
		t.Fatalf("an id whose record was torn mid-write read as processed: replayed %v err %v", replayed, err)
	}
}

func TestFallbackFenceForgetsAnIDOnlyAfterItsProgramExpired(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	fence, err := openFallbackFence(dir, now)
	if err != nil {
		t.Fatalf("open fence: %v", err)
	}
	if _, _, err := fence.claim("id-1", now.Add(time.Minute)); err != nil {
		t.Fatalf("claim: %v", err)
	}

	stillHeld, err := openFallbackFence(dir, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("reopen fence: %v", err)
	}
	if _, replayed, _ := stillHeld.claim("id-1", now.Add(time.Minute)); !replayed {
		t.Fatal("an id was forgotten inside its retention")
	}
	forgotten, err := openFallbackFence(dir, now.Add(time.Minute).Add(fallbackFenceRetention).Add(time.Second))
	if err != nil {
		t.Fatalf("reopen fence past retention: %v", err)
	}
	if len(forgotten.entries) != 0 {
		t.Fatalf("the fence kept %d ids past retention", len(forgotten.entries))
	}
}

// A node with no pinned coordinator key must refuse both routes, say why,
// and record that it did.
func TestFallbackNodeWithNoCoordinatorKeyRefusesAndReports(t *testing.T) {
	cases := loadFallbackFixtureCases(t)
	node := newFallbackTestNode(t, cases, nil, fallbackFixtureInstallTime(t, cases))

	status, resp := node.putProgram("22222222-2222-4222-8222-222222222222", "program.json")
	if status != http.StatusServiceUnavailable || resp.Outcome != "no-coordinator-key" || resp.Accepted {
		t.Fatalf("program route: status %d outcome %q accepted %v, want 503 no-coordinator-key refused", status, resp.Outcome, resp.Accepted)
	}
	status, resp = node.postActivation(cases.Activations[0].Body)
	if status != http.StatusServiceUnavailable || resp.Outcome != "no-coordinator-key" || resp.Accepted {
		t.Fatalf("activation route: status %d outcome %q accepted %v, want 503 no-coordinator-key refused", status, resp.Outcome, resp.Accepted)
	}
	if !strings.Contains(resp.Reason, "no coordinator key") {
		t.Fatalf("reason %q does not say the node has no coordinator key", resp.Reason)
	}
	if node.activationCount() != 0 {
		t.Fatalf("a node with no coordinator key ran the Cue activation %d times", node.activationCount())
	}
	report := node.ingress.report()
	if report.CoordinatorKeyLoaded || report.Refused != 2 || len(report.Decisions) != 2 || len(report.Programs) != 0 {
		t.Fatalf("report = key loaded %v, refused %d, %d decisions, %d programs; want no key, 2 refusals recorded, no program",
			report.CoordinatorKeyLoaded, report.Refused, len(report.Decisions), len(report.Programs))
	}
}

// A program stored on disk is never trusted on a later start under a
// different coordinator key.
func TestFallbackStoredProgramIsReverifiedOnStart(t *testing.T) {
	cases := loadFallbackFixtureCases(t)
	node := newFallbackTestNode(t, cases, fallbackFixtureCoordinatorKey(t), fallbackFixtureInstallTime(t, cases))
	node.install([]string{"program.json"})

	otherKey := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	node.key = otherKey
	node.start()

	if status, resp := node.postActivation(cases.Activations[0].Body); status != http.StatusConflict || resp.Outcome != "program-not-installed" {
		t.Fatalf("status %d outcome %q, want 409 program-not-installed", status, resp.Outcome)
	}
}

func TestFallbackActivationRateLimitIsPerCallerAndReportedOnce(t *testing.T) {
	cases := loadFallbackFixtureCases(t)
	node := newFallbackTestNode(t, cases, fallbackFixtureCoordinatorKey(t), fallbackFixtureInstallTime(t, cases))

	for i := 0; i < fallbackActivationsPerMinute; i++ {
		if status, _ := node.postActivation(`{}`); status != http.StatusBadRequest {
			t.Fatalf("request %d: status %d, want 400 under the limit", i+1, status)
		}
	}
	for i := 0; i < 5; i++ {
		if status, resp := node.postActivation(`{}`); status != http.StatusTooManyRequests || resp.Outcome != "rate-limited" {
			t.Fatalf("request past the limit: status %d outcome %q, want 429 rate-limited", status, resp.Outcome)
		}
	}

	other := httptest.NewRequest(http.MethodPost, fallbackactivation.ActivationPath, strings.NewReader(`{}`))
	other.RemoteAddr = "192.0.2.99:40000"
	rec := httptest.NewRecorder()
	node.handler.ServeHTTP(rec, other)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a second caller was limited by the first: status %d", rec.Code)
	}

	report := node.ingress.report()
	limited := 0
	for _, d := range report.Decisions {
		if d.Outcome == "rate-limited" {
			limited++
			if d.Count != 5 {
				t.Fatalf("the rate-limited entry counts %d refusals, want 5", d.Count)
			}
		}
	}
	if limited != 1 {
		t.Fatalf("the report carries %d rate-limited entries, want one with a count", limited)
	}
	if report.Refused != int64(fallbackActivationsPerMinute)+6 {
		t.Fatalf("report counts %d refusals, want every one of %d", report.Refused, fallbackActivationsPerMinute+6)
	}
}

func TestFallbackBodiesOverTheLimitAreRefused(t *testing.T) {
	cases := loadFallbackFixtureCases(t)
	node := newFallbackTestNode(t, cases, fallbackFixtureCoordinatorKey(t), fallbackFixtureInstallTime(t, cases))

	if status, resp := node.postActivation(strings.Repeat("a", fallbackactivation.MaxActivationBodyBytes+1)); status != http.StatusRequestEntityTooLarge || resp.Outcome != "too-large" {
		t.Fatalf("activation: status %d outcome %q, want 413 too-large", status, resp.Outcome)
	}
	status, resp := node.do(http.MethodPut, fallbackactivation.ProgramPathPrefix+"x", strings.Repeat("a", fallbackactivation.MaxProgramBodyBytes+1))
	if status != http.StatusRequestEntityTooLarge || resp.Outcome != "too-large" {
		t.Fatalf("program: status %d outcome %q, want 413 too-large", status, resp.Outcome)
	}
}

// The ingress adds two routes and takes nothing else from the listener.
func TestFallbackIngressLeavesEveryOtherRequestAlone(t *testing.T) {
	cases := loadFallbackFixtureCases(t)
	node := newFallbackTestNode(t, cases, fallbackFixtureCoordinatorKey(t), fallbackFixtureInstallTime(t, cases))
	for _, probe := range []struct{ method, path string }{
		{http.MethodGet, fallbackactivation.ActivationPath},
		{http.MethodPost, fallbackactivation.ProgramPathPrefix + "x"},
		{http.MethodGet, "/api/system/info"},
		{http.MethodPost, "/showmesh/v1/fallback/commands"},
	} {
		rec := httptest.NewRecorder()
		node.handler.ServeHTTP(rec, httptest.NewRequest(probe.method, probe.path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: status %d, want it passed through to the listener's own 404", probe.method, probe.path, rec.Code)
		}
	}
	if report := node.ingress.report(); len(report.Decisions) != 0 {
		t.Fatalf("a request for another route was recorded as a fallback decision: %+v", report.Decisions)
	}
}

func fallbackFencePath(dir string) string {
	return filepath.Join(dir, fallbackStateSubdir, fallbackFenceFile)
}

// A damaged record cannot say which requests already ran, so the node
// must stop accepting any rather than start from an empty record.
func TestFallbackDamagedExecutionRecordRefusesEveryActivation(t *testing.T) {
	cases := loadFallbackFixtureCases(t)
	node := newFallbackTestNode(t, cases, fallbackFixtureCoordinatorKey(t), fallbackFixtureInstallTime(t, cases))
	node.install(cases.Activations[0].Installed)
	valid := cases.Activations[0].Body
	if status, resp := node.postActivation(valid); status != http.StatusOK || resp.Outcome != "authorized" {
		t.Fatalf("before the damage: status %d outcome %q, want authorized", status, resp.Outcome)
	}
	if err := os.WriteFile(fallbackFencePath(node.dir), []byte("not a record at all\n\x00\x01\n"), 0o644); err != nil {
		t.Fatalf("overwrite the execution record: %v", err)
	}

	node.start()

	status, resp := node.postActivation(valid)
	if status != http.StatusServiceUnavailable || resp.Outcome != "storage-unavailable" || resp.Accepted {
		t.Fatalf("replay of an applied request over a damaged record: status %d outcome %q accepted %v, want 503 storage-unavailable refused", status, resp.Outcome, resp.Accepted)
	}
	if !strings.Contains(resp.Reason, "is damaged") {
		t.Fatalf("reason %q does not say the record is damaged", resp.Reason)
	}
	if node.activationCount() != 1 {
		t.Fatalf("the Cue activation ran %d times, want only the one before the damage", node.activationCount())
	}
	if problem := node.ingress.report().ExecutionRecordProblem; !strings.Contains(problem, "is damaged") {
		t.Fatalf("the node's report says %q about its execution record, want it to say the record is damaged", problem)
	}
}

func TestFallbackFenceOpenFailsOnAnUnreadableLineThatIsNotATornTail(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	good := `{"executionId":"id-1","expiresAt":"2026-10-05T12:15:00Z"}`
	for name, content := range map[string]string{
		"garbage only":             "garbage\n",
		"garbage before a record":  "garbage\n" + good + "\n",
		"garbage between records":  good + "\ngarbage\n" + good + "\n",
		"a record with no id":      `{"expiresAt":"2026-10-05T12:15:00Z"}` + "\n",
		"an empty line":            good + "\n\n",
		"two records on one line":  good + good + "\n",
		"a partial line then more": `{"executionId":"id-0","expi` + good + "\n",
	} {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, fallbackStateSubdir), 0o755); err != nil {
			t.Fatalf("%s: mkdir: %v", name, err)
		}
		if err := os.WriteFile(fallbackFencePath(dir), []byte(content), 0o644); err != nil {
			t.Fatalf("%s: write: %v", name, err)
		}
		if fence, err := openFallbackFence(dir, now); err == nil {
			t.Errorf("%s: the fence opened with %d ids, want the open refused", name, len(fence.entries))
		}
	}
}

// A write that stops partway must not corrupt the next id: the id written
// after it has to be on its own line and still be claimed after a restart.
func TestFallbackFenceShortWriteDoesNotSwallowTheNextID(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	expires := now.Add(time.Minute)
	fence, err := openFallbackFence(dir, now)
	if err != nil {
		t.Fatalf("open fence: %v", err)
	}

	realWrite := fallbackFenceWrite
	fallbackFenceWrite = func(file *os.File, line []byte, offset int64) error {
		_, _ = file.WriteAt(line[:len(line)/2], offset)
		return errors.New("disk full")
	}
	_, _, err = fence.claim("id-a", expires)
	fallbackFenceWrite = realWrite
	if err == nil {
		t.Fatal("a claim whose write stopped partway reported success")
	}
	if !fence.failed() {
		t.Fatal("the fence does not report its failed write")
	}
	if _, replayed, _ := fence.claim("id-a", expires); replayed {
		t.Fatal("an id whose write failed reads as processed")
	}
	if _, replayed, err := fence.claim("id-b", expires); err != nil || replayed {
		t.Fatalf("claim after the short write: replayed %v err %v", replayed, err)
	}
	if fence.failed() {
		t.Fatal("the fence still reports a failed write after a good one")
	}

	reopened, err := openFallbackFence(dir, now)
	if err != nil {
		t.Fatalf("reopen after a short write: %v", err)
	}
	for _, id := range []string{"id-a", "id-b"} {
		if _, replayed, err := reopened.claim(id, expires); err != nil || !replayed {
			t.Errorf("%s after restart: replayed %v err %v, want it still claimed", id, replayed, err)
		}
	}
}

// A torn tail left on disk by a crash is cut off before the next id is
// written, even when no restart came in between.
func TestFallbackFenceCutsOffATornTailBeforeAppending(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	expires := now.Add(time.Minute)
	fence, err := openFallbackFence(dir, now)
	if err != nil {
		t.Fatalf("open fence: %v", err)
	}
	if _, _, err := fence.claim("id-1", expires); err != nil {
		t.Fatalf("claim id-1: %v", err)
	}
	torn, err := os.OpenFile(fallbackFencePath(dir), os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open fence file: %v", err)
	}
	_, _ = torn.WriteString(`{"executionId":"id-torn","expi`)
	_ = torn.Close()

	if _, replayed, err := fence.claim("id-2", expires); err != nil || replayed {
		t.Fatalf("claim id-2 over a torn tail: replayed %v err %v", replayed, err)
	}
	reopened, err := openFallbackFence(dir, now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(reopened.entries) != 2 {
		t.Fatalf("the reopened fence holds %d ids, want id-1 and id-2", len(reopened.entries))
	}
}

func TestFallbackFenceRefusesToWriteToARecordThatShrank(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	fence, err := openFallbackFence(dir, now)
	if err != nil {
		t.Fatalf("open fence: %v", err)
	}
	if _, _, err := fence.claim("id-1", now.Add(time.Minute)); err != nil {
		t.Fatalf("claim id-1: %v", err)
	}
	if err := os.Truncate(fallbackFencePath(dir), 0); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, _, err := fence.claim("id-2", now.Add(time.Minute)); err == nil {
		t.Fatal("a claim succeeded on a record that lost what this node wrote")
	}
}

// When the node cannot even check the Cue, the answer must not say the
// Cue was allowed. The execution id is still spent.
func TestFallbackUncheckableCueIsNotReportedAsAllowed(t *testing.T) {
	cases := loadFallbackFixtureCases(t)
	node := newFallbackTestNode(t, cases, fallbackFixtureCoordinatorKey(t), fallbackFixtureInstallTime(t, cases))
	node.install(cases.Activations[0].Installed)
	catalogs, _ := filepath.Glob(filepath.Join(node.dir, "cue-catalog-state", "*.json"))
	if len(catalogs) != 1 {
		t.Fatalf("found %d held catalog files, want 1", len(catalogs))
	}
	if err := os.WriteFile(catalogs[0], []byte("{not json"), 0o644); err != nil {
		t.Fatalf("damage the held catalog: %v", err)
	}

	status, resp := node.postActivation(cases.Activations[0].Body)
	if status != http.StatusConflict || resp.Outcome != "cue-check-failed" || resp.Accepted {
		t.Fatalf("status %d outcome %q accepted %v, want 409 cue-check-failed refused", status, resp.Outcome, resp.Accepted)
	}
	if strings.Contains(resp.Reason, "allowed") {
		t.Fatalf("reason %q says the Cue was allowed", resp.Reason)
	}
	status, resp = node.postActivation(cases.Activations[0].Body)
	if status != http.StatusConflict || resp.Outcome != "replayed-execution" || resp.FirstOutcome != "cue-check-failed" {
		t.Fatalf("second send: status %d outcome %q first %q, want replayed-execution first cue-check-failed", status, resp.Outcome, resp.FirstOutcome)
	}
}

func TestFallbackNodeThatCannotRunCuesSaysSo(t *testing.T) {
	cases := loadFallbackFixtureCases(t)
	node := newFallbackTestNode(t, cases, fallbackFixtureCoordinatorKey(t), fallbackFixtureInstallTime(t, cases))
	node.install(cases.Activations[0].Installed)
	valid := cases.Activations[0].Body

	node.ingress.setActivate(nil)
	status, resp := node.postActivation(valid)
	if status != http.StatusServiceUnavailable || resp.Outcome != "not-ready" || !strings.Contains(resp.Reason, "still starting") {
		t.Fatalf("before cue.activate is wired: status %d outcome %q reason %q, want 503 not-ready still starting", status, resp.Outcome, resp.Reason)
	}

	node.ingress.setNoCueActivation()
	status, resp = node.postActivation(valid)
	if status != http.StatusServiceUnavailable || resp.Outcome != "not-ready" || !strings.Contains(resp.Reason, "cannot run Cues") {
		t.Fatalf("with no cue.activate at all: status %d outcome %q reason %q, want 503 not-ready saying it cannot run Cues", status, resp.Outcome, resp.Reason)
	}
	if node.activationCount() != 0 {
		t.Fatalf("the Cue activation ran %d times", node.activationCount())
	}
}
