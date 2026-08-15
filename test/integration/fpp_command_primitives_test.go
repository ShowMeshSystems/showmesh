//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
)

// This file is Step 8's own "verified against the running stack" pass:
// docs/bench/fpp-command-vocabulary.md section 4's eight-primitive table,
// proven against the real, containerized bench fppd. The bench container
// is the only default write target: every test here calls
// requireLiveFPPForWrites (fpp_write_guard_test.go), which refuses,
// loudly and before any probe, to dispatch anything against a
// non-loopback host unless SHOWMESH_TEST_FPP_ALLOW_NONLOCAL_WRITES names
// that exact host. That is what keeps
// this file off the deployed fleet; every test also registers a cleanup
// that leaves the bench idle. BUILD-PLAN's own acceptance criteria for
// this step are what each test function below is named after; see this
// task's own report for the full mapping.
//
// Every test in this file starts its OWN coordinator subprocess against
// its OWN temp data directory (matching this package's existing
// convention — see fpp_command_test.go), and every test resets the bench
// to idle BEFORE establishing its own starting state rather than trusting
// a previous test's cleanup to have already run: tests in this package
// are not declared with t.Parallel(), but ordering across files is not a
// contract this file relies on either.

// --- Bench-FPP-direct helpers -----------------------------------------
//
// These bypass ShowMesh entirely and talk straight to the bench fppd named
// by requireLiveFPP/testFPPURL. Used two ways: as an INDEPENDENT ground
// truth to read back a command's real effect (never trusting ShowMesh's
// own observation of itself), and — TestFPPCommandReplayOnParameterizedCommandDispatchesNothingAuditsAsReplayAndRefusesParamConflict
// in fpp_command_test.go is the one caller — to move FPP's own state
// OUTSIDE ShowMesh's accounting, so "a replay dispatches nothing" can be
// proven structurally (if it had dispatched, the independently-set value
// would have been overwritten) rather than merely inferred from a 200
// response. Every one of these still only ever talks to the bench
// container this task's ABSOLUTE TARGET RULE names.

func fetchBenchFPPStatus(t *testing.T, fppURL string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fppURL+"/api/fppd/status", nil)
	if err != nil {
		t.Fatalf("build bench fppd status request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s/api/fppd/status: %v", fppURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var doc map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("decode bench fppd status: %v", err)
	}
	return doc
}

// benchStatusName reads status_name directly off a bench fppd status
// document — capture section 3.1's complete vocabulary ("idle",
// "playing", "paused", "stopping gracefully", ...).
func benchStatusName(doc map[string]any) string {
	s, _ := doc["status_name"].(string)
	return s
}

// benchVolume reads "volume" — a JSON number on every bench capture this
// task read, decoded here as float64 (encoding/json's generic numeric
// type for map[string]any), with a string fallback only for defensiveness
// since this project has shipped a wrong-assumed-type bug before.
func benchVolume(doc map[string]any) (int, bool) {
	switch v := doc["volume"].(type) {
	case float64:
		return int(v), true
	case string:
		n, err := strconv.Atoi(v)
		return n, err == nil
	default:
		return 0, false
	}
}

// benchPlaylistNameAndIndex reads current_playlist.playlist and
// current_playlist.index — both strings on the wire (capture section
// 3's own examples), including index, which is a stringified integer.
func benchPlaylistNameAndIndex(doc map[string]any) (name, index string) {
	cp, _ := doc["current_playlist"].(map[string]any)
	if cp == nil {
		return "", ""
	}
	name, _ = cp["playlist"].(string)
	index, _ = cp["index"].(string)
	return name, index
}

// dispatchRawFPPCommand POSTs directly to the bench fppd's own
// /api/command — capture section 1.2's canonical form, {"command":
// ...,"args":[...]}, args always present and always an array (capture
// section 1.4: FPP treats an absent args identically to an empty one for
// arity purposes, so every call site here passes an explicit, possibly
// empty, slice rather than relying on that equivalence).
func dispatchRawFPPCommand(t *testing.T, fppURL, command string, args []string) (status int, body string) {
	t.Helper()
	if args == nil {
		args = []string{}
	}
	payload, err := json.Marshal(map[string]any{"command": command, "args": args})
	if err != nil {
		t.Fatalf("encode raw fpp command %s: %v", command, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fppURL+"/api/command", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatalf("build raw fpp command request %s: %v", command, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s/api/command (%s): %v", fppURL, command, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read raw fpp command response (%s): %v", command, err)
	}
	return resp.StatusCode, string(b)
}

// waitForBenchStatus polls fetchBenchFPPStatus until cond is satisfied or
// timeout elapses — this package's usual waitFor shape (harness_test.go),
// specialized to the bench's own raw status document rather than the
// coordinator's API, since several tests in this file need to observe an
// effect INDEPENDENTLY of ShowMesh's own collector cadence.
func waitForBenchStatus(t *testing.T, fppURL string, timeout time.Duration, cond func(map[string]any) bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond(fetchBenchFPPStatus(t, fppURL)) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for: %s", timeout, msg)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// resetBenchToIdle issues a direct, ShowMesh-bypassing "Stop Now" and
// waits for the bench's own status_name to actually read "idle" (capture
// section 3.3: Stop Now's effect is immediate, but this confirms rather
// than assumes it). Every test in this file calls this BEFORE
// establishing its own starting state, and again via t.Cleanup, so the
// bench is left idle when this file's tests finish — win or lose, and
// regardless of what a previous test in this package left behind.
func resetBenchToIdle(t *testing.T, fppURL string) {
	t.Helper()
	dispatchRawFPPCommand(t, fppURL, "Stop Now", []string{})
	waitForBenchStatus(t, fppURL, 10*time.Second, func(doc map[string]any) bool {
		return benchStatusName(doc) == "idle"
	}, "bench fppd status_name to read \"idle\" after a direct Stop Now")
}

// --- ShowMesh-side helpers ---------------------------------------------

// waitForFirstFPPPoll is this file's own copy of the wait every test in
// fpp_command_test.go and fpp_e2e_test.go opens with — duplicated here
// rather than factored into harness_test.go because this task's own seam
// is this file plus fpp_command_test.go plus the script, and
// harness_test.go belongs to a concurrently-running seam.
func waitForFirstFPPPoll(t *testing.T, coord *testCoordinator, instanceID string) {
	t.Helper()
	waitFor(t, 30*time.Second, 200*time.Millisecond, func() bool {
		status, body := coord.getRaw(t, "/api/v1/fpp/"+instanceID)
		return status == http.StatusOK && strings.Contains(string(body), `"signal":"fpp.status"`) && !strings.Contains(string(body), `"fpp.status","value":null`)
	}, "the FPP collector's first fpp.status poll to land through the coordinator")
}

// dispatchFPPCommand POSTs action/params/idempotencyKey through the real
// coordinator (never directly at FPP) and requires a 200 — the shape
// every "this primitive confirms" assertion in this file is built on. A
// caller that expects a NON-200 (a validation failure, an ifBusy refusal,
// a replay conflict) calls postRawWithToken directly instead.
func dispatchFPPCommand(t *testing.T, coord *testCoordinator, token, instanceID, action string, params map[string]any, idempotencyKey string) v1.FPPCommandResult {
	t.Helper()
	body := map[string]any{"action": action, "idempotencyKey": idempotencyKey}
	if params != nil {
		body["params"] = params
	}
	status, respBody := postRawWithToken(t, coord, "/api/v1/fpp/"+instanceID+"/commands", token, body)
	if status != http.StatusOK {
		t.Fatalf("dispatch %s: status = %d, want 200; body: %s", action, status, respBody)
	}
	var resp v1.FPPCommandResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		t.Fatalf("decode dispatch %s response: %v; body: %s", action, err, respBody)
	}
	return resp.Command
}

// requireConfirmed fails t unless res reports outcome "confirmed" — ADR-003
// applied at this file's own call sites: a bare 200 from this endpoint is
// not success, and every assertion built on top of this one is only
// meaningful once this has already held.
func requireConfirmed(t *testing.T, action string, res v1.FPPCommandResult) {
	t.Helper()
	if res.Outcome != "confirmed" {
		t.Fatalf("%s: outcome = %q, want \"confirmed\"; outcomeState=%q outcomeReason=%q", action, res.Outcome, res.OutcomeState, res.OutcomeReason)
	}
}

// newFPPCoordinatorForTest is this file's own shared setup: a fresh admin
// principal/token, a fresh instance id, a coordinator subprocess pointed
// at fppURL, and a first-poll wait — the sequence every test function in
// this file (and TestFPPCommandReplayOnParameterizedCommandDispatchesNothingAuditsAsReplayAndRefusesParamConflict
// in fpp_command_test.go) opens with.
func newFPPCoordinatorForTest(t *testing.T, fppURL, namePrefix string) (coord *testCoordinator, adminToken, instanceID string) {
	t.Helper()
	dataDir := t.TempDir()
	adminToken = createAdminAndIssueToken(t, dataDir, "admin-1", "a-strong-password-1")
	instanceID = namePrefix + "-" + uniqueSuffix()
	coord = startCoordinatorWithConfig(t, coordinatorConfig{
		dataDir: dataDir, clientID: "coord-" + uniqueSuffix(),
		bearerToken: adminToken, fppEndpoints: instanceID + "=" + fppURL,
	})
	waitForFirstFPPPoll(t, coord, instanceID)
	return coord, adminToken, instanceID
}

// --- Criterion 1 (remaining primitives) and criterion 4 (argument
// encoding proven by FPP's own readback) ---------------------------------

// TestRemainingFPPPrimitivesConfirmAgainstBenchFPP proves pausePlaylist,
// resumePlaylist, nextPlaylistItem (the non-terminal, index-moved case —
// TestNextPlaylistItemAtLastItemEndsPlaylistAndConfirms covers the
// terminal one separately), prevPlaylistItem, and setVolume all confirm
// through real dispatch against the real bench fppd, each checked against
// FPP's OWN /api/fppd/status readback rather than only ShowMesh's own
// observation of itself. startPlaylist and stopPlaylist are exercised
// here too (as the natural setup/teardown for the others), and are also
// proven independently and under more adversarial conditions elsewhere in
// this file.
func TestRemainingFPPPrimitivesConfirmAgainstBenchFPP(t *testing.T) {
	requireBroker(t)
	fppURL := requireLiveFPPForWrites(t)
	resetBenchToIdle(t, fppURL)
	t.Cleanup(func() { resetBenchToIdle(t, fppURL) })

	coord, adminToken, instanceID := newFPPCoordinatorForTest(t, fppURL, "bench-fpp-prims")

	startRes := dispatchFPPCommand(t, coord, adminToken, instanceID, "startPlaylist",
		map[string]any{"playlist": "showmesh-bench-3item"}, "key-start-"+uniqueSuffix())
	requireConfirmed(t, "startPlaylist", startRes)
	rawAfterStart := fetchBenchFPPStatus(t, fppURL)
	if name, _ := benchPlaylistNameAndIndex(rawAfterStart); name != "showmesh-bench-3item" {
		t.Fatalf("startPlaylist confirmed but bench fppd's OWN current_playlist.playlist = %q, want "+
			"\"showmesh-bench-3item\" — ShowMesh's own confirmation must not outrun FPP's own truth", name)
	}
	if got := benchStatusName(rawAfterStart); got != "playing" {
		t.Fatalf("startPlaylist confirmed but bench fppd status_name = %q, want \"playing\"", got)
	}

	pauseRes := dispatchFPPCommand(t, coord, adminToken, instanceID, "pausePlaylist", nil, "key-pause-"+uniqueSuffix())
	requireConfirmed(t, "pausePlaylist", pauseRes)
	if got := benchStatusName(fetchBenchFPPStatus(t, fppURL)); got != "paused" {
		t.Fatalf("pausePlaylist confirmed but bench fppd status_name = %q, want \"paused\"", got)
	}

	resumeRes := dispatchFPPCommand(t, coord, adminToken, instanceID, "resumePlaylist", nil, "key-resume-"+uniqueSuffix())
	requireConfirmed(t, "resumePlaylist", resumeRes)
	if got := benchStatusName(fetchBenchFPPStatus(t, fppURL)); got != "playing" {
		t.Fatalf("resumePlaylist confirmed but bench fppd status_name = %q, want \"playing\"", got)
	}

	_, indexBefore := benchPlaylistNameAndIndex(fetchBenchFPPStatus(t, fppURL))
	nextRes := dispatchFPPCommand(t, coord, adminToken, instanceID, "nextPlaylistItem", nil, "key-next-"+uniqueSuffix())
	requireConfirmed(t, "nextPlaylistItem", nextRes)
	_, indexAfterNext := benchPlaylistNameAndIndex(fetchBenchFPPStatus(t, fppURL))
	if indexAfterNext == indexBefore {
		t.Fatalf("nextPlaylistItem confirmed but bench fppd's OWN current_playlist.index is still %q (unchanged)", indexAfterNext)
	}

	prevRes := dispatchFPPCommand(t, coord, adminToken, instanceID, "prevPlaylistItem", nil, "key-prev-"+uniqueSuffix())
	requireConfirmed(t, "prevPlaylistItem", prevRes)
	_, indexAfterPrev := benchPlaylistNameAndIndex(fetchBenchFPPStatus(t, fppURL))
	if indexAfterPrev == indexAfterNext {
		t.Fatalf("prevPlaylistItem confirmed but bench fppd's OWN current_playlist.index is still %q (unchanged)", indexAfterPrev)
	}
	if indexAfterPrev != indexBefore {
		t.Errorf("prevPlaylistItem confirmed and moved the index to %q, want it back at the pre-next baseline %q", indexAfterPrev, indexBefore)
	}

	volRes := dispatchFPPCommand(t, coord, adminToken, instanceID, "setVolume", map[string]any{"volume": 42}, "key-vol-"+uniqueSuffix())
	requireConfirmed(t, "setVolume", volRes)
	if v, ok := benchVolume(fetchBenchFPPStatus(t, fppURL)); !ok || v != 42 {
		t.Fatalf("setVolume(42) confirmed but bench fppd's OWN volume = %v (ok=%v), want 42", v, ok)
	}

	stopRes := dispatchFPPCommand(t, coord, adminToken, instanceID, "stopPlaylist", nil, "key-stop-"+uniqueSuffix())
	requireConfirmed(t, "stopPlaylist", stopRes)
	if got := benchStatusName(fetchBenchFPPStatus(t, fppURL)); got != "idle" {
		t.Fatalf("stopPlaylist confirmed but bench fppd status_name = %q, want \"idle\"", got)
	}
}

// --- Criteria 1 and 10: stopPlaylistGracefully ---------------------------

// TestStopPlaylistGracefullyConfirmsWhileShowStillRunning is BUILD-PLAN's
// own named test for stopPlaylistGracefully: capture section 3.3 measured
// a graceful stop's terminal state as bounded by show content (a
// 120-second item held "stopping gracefully" indefinitely), so this
// primitive's own predicate (evaluateFPPStopGracefullyEvidence) confirms
// on ENTERING a stopping state, never only on reaching idle. This test
// exists specifically to stop someone "fixing" that predicate to require
// idle: it asserts CONFIRMED while the bench fppd itself, checked
// directly and independently, is STILL non-idle, and that the outcome
// reason says so in words rather than letting "confirmed" alone be
// mistaken for "the show has stopped".
func TestStopPlaylistGracefullyConfirmsWhileShowStillRunning(t *testing.T) {
	requireBroker(t)
	fppURL := requireLiveFPPForWrites(t)
	resetBenchToIdle(t, fppURL)
	t.Cleanup(func() { resetBenchToIdle(t, fppURL) })

	coord, adminToken, instanceID := newFPPCoordinatorForTest(t, fppURL, "bench-fpp-gracestop")

	// showmesh-test: the single 120-second-pause-item bench playlist —
	// capture section 3.3's own measurement used exactly this shape, so
	// there is ample runway for a graceful stop to still be winding down
	// when this handler's own confirmation resolves.
	startRes := dispatchFPPCommand(t, coord, adminToken, instanceID, "startPlaylist",
		map[string]any{"playlist": "showmesh-test"}, "key-start-"+uniqueSuffix())
	requireConfirmed(t, "startPlaylist", startRes)

	graceRes := dispatchFPPCommand(t, coord, adminToken, instanceID, "stopPlaylistGracefully",
		map[string]any{"afterLoop": false}, "key-grace-"+uniqueSuffix())
	requireConfirmed(t, "stopPlaylistGracefully", graceRes)

	rawNow := fetchBenchFPPStatus(t, fppURL)
	gotStatus := benchStatusName(rawNow)
	if gotStatus == "idle" {
		t.Fatalf("stopPlaylistGracefully confirmed and bench fppd is ALREADY idle — this test needs the show still "+
			"winding down to prove anything (a 120-second item should still have runway left); outcomeReason=%q", graceRes.OutcomeReason)
	}
	if gotStatus != "stopping gracefully" && gotStatus != "stopping gracefully after loop" {
		t.Fatalf("bench fppd status_name = %q at the moment stopPlaylistGracefully confirmed, want a stopping state "+
			"(capture section 3.1)", gotStatus)
	}
	if !strings.Contains(graceRes.OutcomeReason, "has NOT stopped") {
		t.Fatalf("stopPlaylistGracefully confirmed with outcomeReason = %q, want it to say plainly that the show has "+
			"NOT stopped yet — an operator reading \"confirmed\" alone must not be able to conclude playback has "+
			"ended", graceRes.OutcomeReason)
	}
	t.Logf("stopPlaylistGracefully confirmed while bench fppd status_name=%q: outcomeReason=%q", gotStatus, graceRes.OutcomeReason)
}

// --- Criterion 9: nextPlaylistItem at the last item -----------------------

// TestNextPlaylistItemAtLastItemEndsPlaylistAndConfirms is capture section
// 3.5's own hazard, proven live: on a ONE-item playlist, a single Next
// Playlist Item ends the playlist entirely (status idle, index reset), and
// this endpoint's own predicate (evaluateNextItemEvidence) accepts that as
// confirmation of the command's largest possible effect rather than
// reporting unconfirmed for the one case where the command actually did
// the most.
//
// evaluateNextItemEvidence checks fpp.playlist.index BEFORE fpp.status —
// measured live against the bench, ending a one-item playlist moves BOTH
// signals in the SAME poll (index resets from its pre-dispatch baseline to
// "0" at the same moment status_name becomes "idle"), so the index-moved
// branch resolves this case in practice; the status/"ends the playlist"
// branch this predicate also carries exists for the case where index
// alone does not move. This test asserts the load-bearing claim BUILD-PLAN
// actually names — confirmed, and FPP's own status_name genuinely idle —
// rather than which of the predicate's two valid branches produced the
// reason text, since the real system may legitimately take either.
func TestNextPlaylistItemAtLastItemEndsPlaylistAndConfirms(t *testing.T) {
	requireBroker(t)
	fppURL := requireLiveFPPForWrites(t)
	resetBenchToIdle(t, fppURL)
	t.Cleanup(func() { resetBenchToIdle(t, fppURL) })

	coord, adminToken, instanceID := newFPPCoordinatorForTest(t, fppURL, "bench-fpp-nextend")

	startRes := dispatchFPPCommand(t, coord, adminToken, instanceID, "startPlaylist",
		map[string]any{"playlist": "showmesh-test"}, "key-start-"+uniqueSuffix())
	requireConfirmed(t, "startPlaylist", startRes)

	nextRes := dispatchFPPCommand(t, coord, adminToken, instanceID, "nextPlaylistItem", nil, "key-next-"+uniqueSuffix())
	requireConfirmed(t, "nextPlaylistItem", nextRes)

	rawNow := fetchBenchFPPStatus(t, fppURL)
	if got := benchStatusName(rawNow); got != "idle" {
		t.Fatalf("nextPlaylistItem past the LAST item confirmed, but bench fppd status_name = %q, want \"idle\" "+
			"(docs/bench/fpp-command-vocabulary.md section 3.5: Next Playlist Item at the last item ends the "+
			"playlist)", got)
	}
	if name, index := benchPlaylistNameAndIndex(rawNow); name != "" || index != "0" {
		t.Errorf("bench fppd current_playlist = (name=%q, index=%q) after the playlist ended, want (\"\", \"0\") "+
			"per capture section 3.5", name, index)
	}
	// Assert the load-bearing FRAGMENT of each branch's wording rather than a
	// whole sentence: this reason is operator-facing copy and was rewritten
	// once already (the first version cited a repo path at the operator), so
	// pinning a full sentence here would break the suite on every wording pass
	// while proving nothing extra. What must remain true is that the reason
	// names WHICH of evaluateNextItemEvidence's two valid branches confirmed:
	// the playlist ending, or the item position advancing.
	endedBranch := strings.Contains(nextRes.OutcomeReason, "ends the playlist") ||
		strings.Contains(nextRes.OutcomeReason, "playlist ended")
	advancedBranch := strings.Contains(nextRes.OutcomeReason, "advanced") ||
		strings.Contains(nextRes.OutcomeReason, "fpp.playlist.index")
	if !endedBranch && !advancedBranch {
		t.Errorf("nextPlaylistItem's outcomeReason = %q, want it to name which branch confirmed — the playlist "+
			"ending, or the item position advancing (evaluateNextItemEvidence's two valid branches)", nextRes.OutcomeReason)
	}
	t.Logf("nextPlaylistItem at the last item: confirmed, status_name=idle, outcomeReason=%q", nextRes.OutcomeReason)
}

// --- Criterion 2: unconfirmed, structurally --------------------------------

// TestUnconfirmedFPPCommandReportsStatedReasonNeverSuccessful is BUILD-PLAN's
// own named criterion, built structurally rather than by shrinking a
// timeout: capture sections 2 and 3.4 measured "Resume Playlist" while
// genuinely idle as a 200 ("Playlist Restarted") that changes NOTHING —
// status_name stays "idle". resumePlaylist's own predicate
// (evaluateFPPStatusEvidence, wanting fpp.status=="playing") therefore
// cannot ever confirm here: this is not a race against the collector's own
// poll cadence, it is FPP's own documented no-op. This dispatch runs out
// its own confirmation deadline (the coordinator's default, 20s — this
// package has no environment override for it), and this test asserts the
// resolved outcome is "unconfirmed" with a stated (non-empty) evidence
// state and reason, never blank and never "confirmed".
func TestUnconfirmedFPPCommandReportsStatedReasonNeverSuccessful(t *testing.T) {
	requireBroker(t)
	fppURL := requireLiveFPPForWrites(t)
	resetBenchToIdle(t, fppURL)
	t.Cleanup(func() { resetBenchToIdle(t, fppURL) })

	coord, adminToken, instanceID := newFPPCoordinatorForTest(t, fppURL, "bench-fpp-unconfirmed")

	before := time.Now()
	res := dispatchFPPCommand(t, coord, adminToken, instanceID, "resumePlaylist", nil, "key-resume-noop-"+uniqueSuffix())
	elapsed := time.Since(before)

	if res.Outcome != "unconfirmed" {
		t.Fatalf("resumePlaylist against a genuinely idle host: outcome = %q, want \"unconfirmed\" — capture "+
			"section 2 (\"FPP's 200 means nothing\") must never be reported as this endpoint's own success", res.Outcome)
	}
	if res.OutcomeState == "" {
		t.Errorf("outcomeState is empty on an unconfirmed result, want a stated evidence state (ADR-020)")
	}
	if res.OutcomeReason == "" {
		t.Errorf("outcomeReason is empty on an unconfirmed result, want a stated reason (ADR-020: absent evidence " +
			"is stated, never omitted)")
	}
	if got := benchStatusName(fetchBenchFPPStatus(t, fppURL)); got != "idle" {
		t.Errorf("bench fppd status_name = %q after an unconfirmed resumePlaylist, want it to have genuinely stayed "+
			"\"idle\" (this scenario is unconfirmed because FPP itself never moved, not because of a timing race)", got)
	}
	t.Logf("resumePlaylist against an idle host correctly reported unconfirmed after %s (the coordinator's own "+
		"confirm deadline default is 20s): outcomeState=%q outcomeReason=%q", elapsed, res.OutcomeState, res.OutcomeReason)
}

// --- Criterion 3: confirmed only on POST-dispatch evidence, timed --------

// minPlausiblePostDispatchConfirmWait is this test's own floor.
// confirmFPPCommand (internal/coordinator/api/fppcommand_handler.go)
// checks evidence ONCE, synchronously, immediately after dispatch, and
// only THEN begins waiting on its own poll ticker
// (h.fppCommandPollInterval — 500ms by default, and this package has no
// environment override for it, so every coordinator subprocess this file
// starts runs on that literal default). A CORRECT implementation's
// notBefore fence (Step 7 seam C review defect 2) means that first
// synchronous check can never succeed against evidence that predates
// dispatch, so a correct confirmation can never resolve faster than one
// tick of that ticker. Step 7's own measured regression resolved in 179
// MICROSECONDS — about four orders of magnitude below this floor — so
// this is not a tight race against the FPP collector's own ~15-second
// poll cadence (fpp.DefaultPollInterval), whose exact phase relative to
// dispatch this package does not control and does not assert against
// directly; it is a floor wide enough to convict the EXACT defect shape
// this criterion exists to catch, without being so tight that ordinary
// scheduling jitter could trip a correct implementation.
const minPlausiblePostDispatchConfirmWait = 400 * time.Millisecond

// TestStartAndStopPlaylistConfirmOnlyOnPostDispatchEvidenceTimed is
// BUILD-PLAN's own explicitly-named "criterion most likely to pass
// falsely": a start (or stop) issued against a host ALREADY in the target
// state must be confirmed only on evidence that post-dates ITS OWN
// dispatch, never on a stale reading that merely happens to already
// agree — the exact mirror of Step 7's 179-microsecond defect. Verified by
// TIMING the confirmation against the dispatch (wall-clock, independent of
// h.now()), not by reading the code, for both startPlaylist (against a
// host already playing that exact playlist) and stopPlaylist (against an
// already-idle host).
func TestStartAndStopPlaylistConfirmOnlyOnPostDispatchEvidenceTimed(t *testing.T) {
	requireBroker(t)
	fppURL := requireLiveFPPForWrites(t)
	resetBenchToIdle(t, fppURL)
	t.Cleanup(func() { resetBenchToIdle(t, fppURL) })

	coord, adminToken, instanceID := newFPPCoordinatorForTest(t, fppURL, "bench-fpp-timing")

	t.Run("startPlaylist against a host ALREADY playing that exact playlist", func(t *testing.T) {
		first := dispatchFPPCommand(t, coord, adminToken, instanceID, "startPlaylist",
			map[string]any{"playlist": "showmesh-test"}, "key-start-first-"+uniqueSuffix())
		requireConfirmed(t, "startPlaylist (first)", first)

		dispatchedAt := time.Now()
		second := dispatchFPPCommand(t, coord, adminToken, instanceID, "startPlaylist",
			map[string]any{"playlist": "showmesh-test"}, "key-start-second-"+uniqueSuffix())
		elapsed := time.Since(dispatchedAt)
		requireConfirmed(t, "startPlaylist (second, already playing)", second)

		if elapsed < minPlausiblePostDispatchConfirmWait {
			t.Fatalf("startPlaylist against an ALREADY-playing instance confirmed in %s (floor %s) — this is exactly "+
				"Step 7's 179-microsecond defect shape: a confirmation resting on stale, pre-dispatch evidence "+
				"rather than waiting for a NEW post-dispatch observation; outcomeReason=%q",
				elapsed, minPlausiblePostDispatchConfirmWait, second.OutcomeReason)
		}
		t.Logf("startPlaylist re-dispatched against an already-playing instance: confirmed after %s (floor was %s)", elapsed, minPlausiblePostDispatchConfirmWait)
	})

	t.Run("stopPlaylist against an ALREADY idle host", func(t *testing.T) {
		resetBenchToIdle(t, fppURL)
		first := dispatchFPPCommand(t, coord, adminToken, instanceID, "stopPlaylist", nil, "key-stop-first-"+uniqueSuffix())
		requireConfirmed(t, "stopPlaylist (first)", first)

		dispatchedAt := time.Now()
		second := dispatchFPPCommand(t, coord, adminToken, instanceID, "stopPlaylist", nil, "key-stop-second-"+uniqueSuffix())
		elapsed := time.Since(dispatchedAt)
		requireConfirmed(t, "stopPlaylist (second, already idle)", second)

		if elapsed < minPlausiblePostDispatchConfirmWait {
			t.Fatalf("stopPlaylist against an ALREADY-idle instance confirmed in %s (floor %s) — the same "+
				"179-microsecond defect shape; outcomeReason=%q", elapsed, minPlausiblePostDispatchConfirmWait, second.OutcomeReason)
		}
		t.Logf("stopPlaylist re-dispatched against an already-idle instance: confirmed after %s (floor was %s)", elapsed, minPlausiblePostDispatchConfirmWait)
	})
}

// --- Criterion 6: ifBusy -----------------------------------------------

// TestIfBusyRefusesReplacesAndAllowsSamePlaylist is capture section 5's
// own decision, proven live: startPlaylist's default (absent) ifBusy
// refuses to interrupt a DIFFERENT playlist that is currently playing
// (409, and — checked against the bench fppd directly — the running show
// is untouched), ifBusy="replace" does interrupt it, and requesting the
// playlist that is ALREADY the one running, with the default ifBusy, is
// never "busy" and is not refused.
func TestIfBusyRefusesReplacesAndAllowsSamePlaylist(t *testing.T) {
	requireBroker(t)
	fppURL := requireLiveFPPForWrites(t)
	resetBenchToIdle(t, fppURL)
	t.Cleanup(func() { resetBenchToIdle(t, fppURL) })

	coord, adminToken, instanceID := newFPPCoordinatorForTest(t, fppURL, "bench-fpp-ifbusy")

	startA := dispatchFPPCommand(t, coord, adminToken, instanceID, "startPlaylist",
		map[string]any{"playlist": "showmesh-bench-3item"}, "key-a-"+uniqueSuffix())
	requireConfirmed(t, "startPlaylist A", startA)

	status, body := postRawWithToken(t, coord, "/api/v1/fpp/"+instanceID+"/commands", adminToken, map[string]any{
		"action": "startPlaylist", "idempotencyKey": "key-b-refuse-" + uniqueSuffix(),
		"params": map[string]any{"playlist": "showmesh-test"},
	})
	if status != http.StatusConflict {
		t.Fatalf("startPlaylist for B with default ifBusy while A is playing: status = %d, want 409; body: %s", status, body)
	}
	if !strings.Contains(string(body), "showmesh-bench-3item") {
		t.Errorf("409 body does not name the currently-playing playlist (showmesh-bench-3item); body: %s", body)
	}
	rawAfterRefusal := fetchBenchFPPStatus(t, fppURL)
	if name, _ := benchPlaylistNameAndIndex(rawAfterRefusal); name != "showmesh-bench-3item" {
		t.Fatalf("after a REFUSED startPlaylist for B, bench fppd's OWN current_playlist.playlist = %q, want it "+
			"still \"showmesh-bench-3item\" (A) — B must never have been dispatched", name)
	}

	startBReplace := dispatchFPPCommand(t, coord, adminToken, instanceID, "startPlaylist",
		map[string]any{"playlist": "showmesh-test", "ifBusy": "replace"}, "key-b-replace-"+uniqueSuffix())
	requireConfirmed(t, "startPlaylist B (replace)", startBReplace)
	rawAfterReplace := fetchBenchFPPStatus(t, fppURL)
	if name, _ := benchPlaylistNameAndIndex(rawAfterReplace); name != "showmesh-test" {
		t.Fatalf("after ifBusy=replace, bench fppd's OWN current_playlist.playlist = %q, want \"showmesh-test\" (B)", name)
	}

	startBAgain := dispatchFPPCommand(t, coord, adminToken, instanceID, "startPlaylist",
		map[string]any{"playlist": "showmesh-test"}, "key-b-again-"+uniqueSuffix())
	requireConfirmed(t, "startPlaylist B (already running, default ifBusy)", startBAgain)
	rawFinal := fetchBenchFPPStatus(t, fppURL)
	if name, _ := benchPlaylistNameAndIndex(rawFinal); name != "showmesh-test" {
		t.Fatalf("re-requesting the ALREADY-running playlist B with default ifBusy: bench fppd's OWN "+
			"current_playlist.playlist = %q, want it to still be \"showmesh-test\"", name)
	}
}

// --- Criterion 7: absent/null/empty, end to end ----------------------------

// fppParamShapeCase is one wire shape TestFPPCommandParamsAbsentNullEmptyDistinctionEndToEnd
// exercises against the real, running server. hasParams distinguishes
// "the top-level params key is entirely absent" from "params is present"
// (params itself may then be nil, which Go's encoding/json renders as the
// JSON literal null, or a populated map).
type fppParamShapeCase struct {
	name       string
	action     string
	hasParams  bool
	params     any
	wantStatus int
	wantSubstr string
}

// TestFPPCommandParamsAbsentNullEmptyDistinctionEndToEnd proves, against
// the real running server rather than a handler unit test, that
// decodeFPPCommandParams's absent/null/empty rule (section 2 of this
// step's own spec, and CLAUDE.md's own repeatedly-shipped-bug rule) holds
// on the actual wire: params entirely absent, params: {}, params: null, a
// required param absent (with another optional param present), a required
// param present as null, a required STRING param present as an empty
// string, an OPTIONAL param present as null, an unknown param key, and a
// non-empty params object against a zero-parameter action are all DIFFERENT
// 400s naming what is actually wrong. A final positive case proves absent
// optional params really do take their documented defaults, echoed back on
// the wire and confirmed against the bench fppd's own readback (tying into
// criterion 4). Every failure case is also checked, in aggregate, to have
// dispatched NOTHING to FPP — read back directly from the bench, not
// inferred from the 400 status code alone.
func TestFPPCommandParamsAbsentNullEmptyDistinctionEndToEnd(t *testing.T) {
	requireBroker(t)
	fppURL := requireLiveFPPForWrites(t)
	resetBenchToIdle(t, fppURL)
	t.Cleanup(func() { resetBenchToIdle(t, fppURL) })

	coord, adminToken, instanceID := newFPPCoordinatorForTest(t, fppURL, "bench-fpp-paramshapes")

	cases := []fppParamShapeCase{
		{
			name:       "params entirely absent on a REQUIRED-param action",
			action:     "setVolume",
			hasParams:  false,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "params.volume is required and was not provided",
		},
		{
			name:       "params: {} on a REQUIRED-param action",
			action:     "setVolume",
			hasParams:  true,
			params:     map[string]any{},
			wantStatus: http.StatusBadRequest,
			wantSubstr: "params.volume is required and was not provided",
		},
		{
			name:       "params: null",
			action:     "setVolume",
			hasParams:  true,
			params:     nil,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "must not be null for action",
		},
		{
			name:       "a required param (playlist) absent while another optional param IS present",
			action:     "startPlaylist",
			hasParams:  true,
			params:     map[string]any{"repeat": true},
			wantStatus: http.StatusBadRequest,
			wantSubstr: "params.playlist is required and was not provided",
		},
		{
			name:       "a required param present as explicit null",
			action:     "startPlaylist",
			hasParams:  true,
			params:     map[string]any{"playlist": nil},
			wantStatus: http.StatusBadRequest,
			wantSubstr: "params.playlist is required and must not be null",
		},
		{
			name:       "a required STRING param present as an empty string",
			action:     "startPlaylist",
			hasParams:  true,
			params:     map[string]any{"playlist": ""},
			wantStatus: http.StatusBadRequest,
			wantSubstr: "must not be an empty string",
		},
		{
			name:       "an OPTIONAL param present as explicit null",
			action:     "startPlaylist",
			hasParams:  true,
			params:     map[string]any{"playlist": "showmesh-test", "repeat": nil},
			wantStatus: http.StatusBadRequest,
			wantSubstr: "params.repeat must not be null",
		},
		{
			name:       "an unknown param key",
			action:     "setVolume",
			hasParams:  true,
			params:     map[string]any{"volume": 50, "bogus": 1},
			wantStatus: http.StatusBadRequest,
			wantSubstr: "unrecognized key",
		},
		{
			name:       "a non-empty params object for a ZERO-parameter action",
			action:     "stopPlaylist",
			hasParams:  true,
			params:     map[string]any{"foo": 1},
			wantStatus: http.StatusBadRequest,
			wantSubstr: "takes no parameters",
		},
	}

	rawBefore := fetchBenchFPPStatus(t, fppURL)

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := map[string]any{"action": c.action, "idempotencyKey": "key-shape-" + uniqueSuffix()}
			if c.hasParams {
				body["params"] = c.params
			}
			status, respBody := postRawWithToken(t, coord, "/api/v1/fpp/"+instanceID+"/commands", adminToken, body)
			if status != c.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", status, c.wantStatus, respBody)
			}
			if !strings.Contains(string(respBody), c.wantSubstr) {
				t.Errorf("body does not contain %q; body: %s", c.wantSubstr, respBody)
			}
		})
	}

	rawAfterFailures := fetchBenchFPPStatus(t, fppURL)
	if benchStatusName(rawBefore) != benchStatusName(rawAfterFailures) {
		t.Errorf("bench fppd status_name changed from %q to %q across %d rejected requests, want it unchanged — "+
			"none of them should have dispatched anything", benchStatusName(rawBefore), benchStatusName(rawAfterFailures), len(cases))
	}
	nameBefore, idxBefore := benchPlaylistNameAndIndex(rawBefore)
	nameAfter, idxAfter := benchPlaylistNameAndIndex(rawAfterFailures)
	if nameBefore != nameAfter || idxBefore != idxAfter {
		t.Errorf("bench fppd's current_playlist changed from (%q,%q) to (%q,%q) across %d rejected requests, "+
			"want it unchanged", nameBefore, idxBefore, nameAfter, idxAfter, len(cases))
	}
	if volBefore, okBefore := benchVolume(rawBefore); okBefore {
		if volAfter, okAfter := benchVolume(rawAfterFailures); !okAfter || volBefore != volAfter {
			t.Errorf("bench fppd volume changed from %v to %v (ok=%v) across %d rejected requests, want it unchanged",
				volBefore, volAfter, okAfter, len(cases))
		}
	}

	t.Run("valid request: absent optional params take their documented defaults", func(t *testing.T) {
		res := dispatchFPPCommand(t, coord, adminToken, instanceID, "startPlaylist",
			map[string]any{"playlist": "showmesh-test"}, "key-shape-ok-"+uniqueSuffix())
		requireConfirmed(t, "startPlaylist (defaults)", res)
		if repeat, _ := res.Params["repeat"].(bool); repeat {
			t.Errorf("Params[\"repeat\"] = %v, want false (the documented default, applied because the field was omitted)", res.Params["repeat"])
		}
		if ifBusy, _ := res.Params["ifBusy"].(string); ifBusy != "refuse" {
			t.Errorf("Params[\"ifBusy\"] = %v, want \"refuse\" (the documented default)", res.Params["ifBusy"])
		}
		rawNow := fetchBenchFPPStatus(t, fppURL)
		if name, _ := benchPlaylistNameAndIndex(rawNow); name != "showmesh-test" {
			t.Errorf("bench fppd's OWN current_playlist.playlist = %q, want \"showmesh-test\"", name)
		}
	})
}

// --- Criterion 8: the collector's read-only posture, proven not assumed --

// recordedRequest is one HTTP request startRecordingProxy observed.
type recordedRequest struct {
	Method string
	Path   string
}

// requestRecorder is a concurrency-safe log of recordedRequest values —
// concurrency-safe because the coordinator's own HTTP server can, in
// principle, have the collector's background poll and this endpoint's
// dispatch in flight against the proxy at the same moment.
type requestRecorder struct {
	mu   sync.Mutex
	hits []recordedRequest
}

func (r *requestRecorder) record(method, path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hits = append(r.hits, recordedRequest{Method: method, Path: path})
}

func (r *requestRecorder) snapshot() []recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedRequest, len(r.hits))
	copy(out, r.hits)
	return out
}

// startRecordingProxy runs a real, local HTTP reverse proxy in front of
// target (the bench fppd), recording every request's method and path
// before forwarding it UNMODIFIED — used only by
// TestCollectorReadOnlyPostureUnchangedByCommandSurface, to observe from
// OUTSIDE the coordinator which HTTP methods it actually issues against
// FPP while the collector's own background polling and this endpoint's
// command dispatch are both live against the SAME configured instance.
// Every byte still reaches the real bench fppd through this proxy —
// nothing about FPP's own behavior is faked, only observed.
func startRecordingProxy(t *testing.T, target string) (proxyURL string, rec *requestRecorder, cleanup func()) {
	t.Helper()
	targetURL, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse proxy target %q: %v", target, err)
	}
	rec = &requestRecorder{}
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.Method, r.URL.Path)
		proxy.ServeHTTP(w, r)
	}))
	return srv.URL, rec, srv.Close
}

// TestCollectorReadOnlyPostureUnchangedByCommandSurface is BUILD-PLAN's own
// named criterion, proven structurally rather than assumed from
// internal/coordinator/collector/fpp's own unit-level GET-only guarantee
// (redirect_test.go, importgraph_test.go — a different package this
// task's seam does not touch): a real coordinator subprocess is pointed at
// a real, local reverse proxy in front of the real bench fppd, so every
// HTTP request the coordinator issues to FPP — both the collector's own
// background polling AND this endpoint's own command dispatches — passes
// through one observable point. After letting the collector poll at least
// once and dispatching two commands (setVolume, stopPlaylist) through
// ShowMesh, every recorded request must be EITHER a GET (the collector's
// own polling) OR a POST to exactly "/api/command" (an explicit ShowMesh
// command dispatch, capture section 1.2's canonical form) — proving the
// widened Step 8 command surface introduced no other write-shaped traffic
// alongside the collector's own reads.
func TestCollectorReadOnlyPostureUnchangedByCommandSurface(t *testing.T) {
	requireBroker(t)
	fppURL := requireLiveFPPForWrites(t)
	resetBenchToIdle(t, fppURL)
	t.Cleanup(func() { resetBenchToIdle(t, fppURL) })

	proxyURL, rec, cleanupProxy := startRecordingProxy(t, fppURL)
	defer cleanupProxy()

	coord, adminToken, instanceID := newFPPCoordinatorForTest(t, proxyURL, "bench-fpp-readonly")

	volRes := dispatchFPPCommand(t, coord, adminToken, instanceID, "setVolume", map[string]any{"volume": 61}, "key-ro-vol-"+uniqueSuffix())
	requireConfirmed(t, "setVolume (through proxy)", volRes)
	stopRes := dispatchFPPCommand(t, coord, adminToken, instanceID, "stopPlaylist", nil, "key-ro-stop-"+uniqueSuffix())
	requireConfirmed(t, "stopPlaylist (through proxy)", stopRes)

	hits := rec.snapshot()
	var sawGET, sawCommandPOST int
	for _, h := range hits {
		switch {
		case h.Method == http.MethodGet:
			sawGET++
		case h.Method == http.MethodPost && h.Path == "/api/command":
			sawCommandPOST++
		default:
			t.Errorf("recorded %s %s through the proxy — only GET (the collector's own polling) or POST "+
				"/api/command (an explicit ShowMesh command dispatch) are expected; the widened Step 8 command "+
				"surface must not have changed the collector's own read-only posture", h.Method, h.Path)
		}
	}
	if sawGET == 0 {
		t.Errorf("no GET request was ever recorded through the proxy — the collector never polled through it, so " +
			"this test proved nothing")
	}
	if sawCommandPOST == 0 {
		t.Errorf("no POST /api/command request was ever recorded through the proxy — the command dispatch never " +
			"reached FPP through the SAME channel the collector uses, so this test proved nothing about the two " +
			"coexisting safely")
	}
	t.Logf("recorded %d requests through the proxy: %d GET, %d POST /api/command, %d other", len(hits), sawGET, sawCommandPOST, len(hits)-sawGET-sawCommandPOST)
}
