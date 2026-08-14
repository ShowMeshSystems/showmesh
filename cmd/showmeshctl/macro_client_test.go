package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- exitCodeForMacroRun ---

func TestExitCodeForMacroRunConfirmed(t *testing.T) {
	tru := true
	run := macroRun{State: "finished", Completed: &tru, Confirmed: &tru}
	if got := exitCodeForMacroRun(run); got != exitOK {
		t.Errorf("exitCodeForMacroRun(completed=true,confirmed=true) = %d, want exitOK (%d)", got, exitOK)
	}
}

func TestExitCodeForMacroRunCompletedNotConfirmed(t *testing.T) {
	tru, fls := true, false
	run := macroRun{State: "finished", Completed: &tru, Confirmed: &fls}
	if got := exitCodeForMacroRun(run); got != exitCommandUnconfirmed {
		t.Errorf("exitCodeForMacroRun(completed=true,confirmed=false) = %d, want exitCommandUnconfirmed (%d)", got, exitCommandUnconfirmed)
	}
}

func TestExitCodeForMacroRunAborted(t *testing.T) {
	fls := false
	run := macroRun{State: "finished", Completed: &fls, Confirmed: &fls}
	if got := exitCodeForMacroRun(run); got != exitMacroRunAborted {
		t.Errorf("exitCodeForMacroRun(completed=false) = %d, want exitMacroRunAborted (%d)", got, exitMacroRunAborted)
	}
}

// TestExitCodeForMacroRunAbortedTakesPrecedenceOverConfirmed proves
// completed=false is checked BEFORE confirmed, matching STEP-9-SPEC.md
// section 2.3's own table ("failed" sets completed false; confirmed is
// whatever it had earned by then, and can be either value at abort time) —
// an aborted run must always report as aborted (exit 12), never silently
// downgraded to "just unconfirmed" (exit 9) because confirmed also
// happened to be false.
func TestExitCodeForMacroRunAbortedTakesPrecedenceOverConfirmed(t *testing.T) {
	fls := false
	run := macroRun{State: "finished", Completed: &fls, Confirmed: &fls}
	if got := exitCodeForMacroRun(run); got != exitMacroRunAborted {
		t.Errorf("exitCodeForMacroRun = %d, want exitMacroRunAborted (%d), not exitCommandUnconfirmed", got, exitMacroRunAborted)
	}
}

// --- effectiveMacroClientTimeout / noteMacroTimeoutFloorIfRaised ---

func TestEffectiveMacroClientTimeoutRaisesBelowFloor(t *testing.T) {
	got := effectiveMacroClientTimeout(1 * time.Second)
	if got != minMacroClientTimeout {
		t.Errorf("effectiveMacroClientTimeout(1s) = %s, want the floor %s", got, minMacroClientTimeout)
	}
}

func TestEffectiveMacroClientTimeoutHonorsLargerExplicitValue(t *testing.T) {
	explicit := minMacroClientTimeout + 30*time.Second
	got := effectiveMacroClientTimeout(explicit)
	if got != explicit {
		t.Errorf("effectiveMacroClientTimeout(%s) = %s, want it honored verbatim (already above the floor)", explicit, got)
	}
}

func TestNoteMacroTimeoutFloorIfRaisedPrintsOnlyWhenActuallyRaised(t *testing.T) {
	var buf bytes.Buffer
	noteMacroTimeoutFloorIfRaised(&buf, "macro run", 1*time.Second, minMacroClientTimeout)
	if !strings.Contains(buf.String(), "1s") || !strings.Contains(buf.String(), minMacroClientTimeout.String()) {
		t.Errorf("expected a note naming both values, got %q", buf.String())
	}

	buf.Reset()
	noteMacroTimeoutFloorIfRaised(&buf, "macro run", minMacroClientTimeout, minMacroClientTimeout)
	if buf.Len() != 0 {
		t.Errorf("expected no note when the floor did not fire, got %q", buf.String())
	}
}

// --- followMacroRun ---

// macroRunFixture builds a minimal, valid JSON body for GET
// /macro-runs/{runId} with the given state/completed/confirmed/reason.
// completed/confirmed are omitted (null) when nil, matching how the real
// server renders an in-flight run (ADR-031 decision 3).
func macroRunFixture(id, state string, completed, confirmed *bool, reason string) string {
	boolOrNull := func(b *bool) string {
		if b == nil {
			return "null"
		}
		if *b {
			return "true"
		}
		return "false"
	}
	return fmt.Sprintf(`{"serverTime":"2026-08-14T21:00:00Z","run":{
		"id":%q,"macroObjectId":"m1","macroRevision":1,"show":"s1","trigger":"cli",
		"issuerPrincipalId":"p1","issuerPrincipalName":"admin","createdAt":"2026-08-14T21:00:00Z",
		"finishedAt":null,"state":%q,"completed":%s,"confirmed":%s,"reason":%q,
		"attributionDegraded":false,"steps":[]
	}}`, id, state, boolOrNull(completed), boolOrNull(confirmed), reason)
}

func jsonServer(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ShowMesh-API-Version", "1")
	_, _ = fmt.Fprint(w, body)
}

// TestFollowMacroRunNeverIdlesOutWhileTheCoordinatorKeepsAnswering is this
// file's own proof of the central requirement: a run that legitimately
// keeps saying "still running" is followed for as long as it keeps
// answering, and stopping is driven by the IDLE window, never by a total
// duration — "the idle window resets on every ... successful poll." The
// server here answers successfully on EVERY single poll and idleTimeout is
// set far shorter than this test's own outer deadline; if a total-duration
// cap (or an off-by-one in the reset logic) had crept in, this would exit
// via exitFollowStillWatching well before outerDeadline fires. It must
// instead keep going until THIS TEST cancels it (mirroring Ctrl+C),
// exiting exitOK, having polled many times.
//
// Break-first-verify (see report): commenting out the
// `deadline = now.Add(idleTimeout)` reset on a successful poll (i.e.
// reverting to resetting the deadline only once, at loop entry) makes this
// test fail fast with exitFollowStillWatching well before outerDeadline,
// confirming the assertion actually exercises the per-success reset rather
// than merely "does it eventually return".
func TestFollowMacroRunNeverIdlesOutWhileTheCoordinatorKeepsAnswering(t *testing.T) {
	var pollCount int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&pollCount, 1)
		jsonServer(w, macroRunFixture("run-1", "running", nil, nil, ""))
	}))
	defer ts.Close()

	c, err := newClient(ts.URL, "", &http.Client{})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	g := &globalFlags{timeout: minMacroClientTimeout, output: outputText}

	const pollInterval = 10 * time.Millisecond
	const idleTimeout = 50 * time.Millisecond    // deliberately tiny
	const outerDeadline = 300 * time.Millisecond // several multiples of idleTimeout

	ctx, cancel := context.WithTimeout(context.Background(), outerDeadline)
	defer cancel()

	var stdout, stderr bytes.Buffer
	start := time.Now()
	code := followMacroRun(ctx, c, g, "run show", "run-1", pollInterval, idleTimeout, &stdout, &stderr, time.Now)
	elapsed := time.Since(start)

	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK (ctx cancellation, mirroring Ctrl+C) — got a different code, which "+
			"means something ended this loop BEFORE the outer deadline despite the coordinator answering every "+
			"single poll; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	// Must have run close to the FULL outer deadline, not stopped early at
	// (or near) idleTimeout.
	if elapsed < outerDeadline/2 {
		t.Errorf("elapsed = %s, want close to the outer deadline %s — an idle timeout this small (%s) must never "+
			"fire while every poll succeeds", elapsed, outerDeadline, idleTimeout)
	}
	if got := atomic.LoadInt64(&pollCount); got < 10 {
		t.Errorf("pollCount = %d, want at least 10 — the loop must have kept polling for the full outer duration", got)
	}
}

// TestFollowMacroRunIdleTimeoutFiresOnGenuineSilence is the companion case:
// nothing is listening at all (every poll fails at the transport level, the
// way a downed coordinator or an unplugged network genuinely would), and
// this loop must stop watching once the idle window elapses — cleanly,
// never printing something that reads as a transport failure or a Go
// error (STEP-9-SPEC.md section 9's own requirement for this exact exit).
func TestFollowMacroRunIdleTimeoutFiresOnGenuineSilence(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonServer(w, macroRunFixture("run-1", "running", nil, nil, ""))
	}))
	ts.Close() // closed before the first request: every poll fails immediately

	c, err := newClient(ts.URL, "", &http.Client{})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	g := &globalFlags{timeout: minMacroClientTimeout, output: outputText}

	var stdout, stderr bytes.Buffer
	const pollInterval = 10 * time.Millisecond
	const idleTimeout = 150 * time.Millisecond
	start := time.Now()
	code := followMacroRun(context.Background(), c, g, "run show", "run-1", pollInterval, idleTimeout, &stdout, &stderr, time.Now)
	elapsed := time.Since(start)

	if code != exitFollowStillWatching {
		t.Fatalf("exit code = %d, want exitFollowStillWatching (%d); stdout=%q stderr=%q", code, exitFollowStillWatching, stdout.String(), stderr.String())
	}
	if elapsed < idleTimeout/2 || elapsed > idleTimeout*6 {
		t.Errorf("elapsed = %s, want roughly the idle timeout (%s)", elapsed, idleTimeout)
	}

	out := stdout.String()
	if !strings.Contains(out, "run-1") {
		t.Errorf("stdout = %q, want it to name the run id", out)
	}
	if !strings.Contains(out, "in progress") && !strings.Contains(out, "stopped watching") {
		t.Errorf("stdout = %q, want it to say the run may still be in progress / that this command stopped watching", out)
	}
	// STEP-9-SPEC.md section 9: "never reports anything that reads as a
	// transport failure... must not print a Go error" on the terminal
	// message (stdout) — even though every single poll genuinely failed at
	// the transport level along the way.
	for _, bad := range []string{"context deadline exceeded", "dial tcp", "EOF", "connection refused"} {
		if strings.Contains(out, bad) {
			t.Errorf("stdout contains %q, which reads as a transport failure; STEP-9-SPEC.md section 9 forbids that on an idle-timeout exit: %q", bad, out)
		}
	}
}

// TestFollowMacroRunFinishesConfirmedExitsOK proves the OTHER path out of
// the loop: a run that reaches "finished" is reported immediately, without
// waiting for the idle window, and a fully confirmed run exits 0.
func TestFollowMacroRunFinishesConfirmedExitsOK(t *testing.T) {
	tru := true
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonServer(w, macroRunFixture("run-2", "finished", &tru, &tru, ""))
	}))
	defer ts.Close()

	c, err := newClient(ts.URL, "", &http.Client{})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	g := &globalFlags{timeout: minMacroClientTimeout, output: outputText}

	var stdout, stderr bytes.Buffer
	start := time.Now()
	code := followMacroRun(context.Background(), c, g, "run show", "run-2", 10*time.Millisecond, 10*time.Second, &stdout, &stderr, time.Now)
	elapsed := time.Since(start)

	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if elapsed > 2*time.Second {
		t.Errorf("elapsed = %s, want this to return almost immediately on the FIRST poll (finished already), not wait out the 10s idle window", elapsed)
	}
}

// TestFollowMacroRunFinishesAbortedNamesReason proves an aborted run's exit
// code and that its reason reaches the operator.
func TestFollowMacroRunFinishesAbortedNamesReason(t *testing.T) {
	fls := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonServer(w, macroRunFixture("run-3", "finished", &fls, &fls, "step \"start\" failed: FPP refused the command"))
	}))
	defer ts.Close()

	c, err := newClient(ts.URL, "", &http.Client{})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	g := &globalFlags{timeout: minMacroClientTimeout, output: outputText}

	var stdout, stderr bytes.Buffer
	code := followMacroRun(context.Background(), c, g, "run show", "run-3", 10*time.Millisecond, 10*time.Second, &stdout, &stderr, time.Now)
	if code != exitMacroRunAborted {
		t.Fatalf("exit code = %d, want exitMacroRunAborted (%d); stdout=%q", code, exitMacroRunAborted, stdout.String())
	}
	if !strings.Contains(stdout.String(), "FPP refused the command") {
		t.Errorf("stdout = %q, want it to carry the run's own reason", stdout.String())
	}
}

// TestFollowMacroRunStopsImmediatelyOnDefinitiveNotFound proves a 404 (the
// run does not exist — a healthy coordinator's definitive answer) is
// reported right away rather than absorbed as silence and waited out.
func TestFollowMacroRunStopsImmediatelyOnDefinitiveNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/resource-not-found","title":"Resource not found","status":404,"detail":"no such run"}`)
	}))
	defer ts.Close()

	c, err := newClient(ts.URL, "", &http.Client{})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	g := &globalFlags{timeout: minMacroClientTimeout, output: outputText}

	var stdout, stderr bytes.Buffer
	const idleTimeout = 5 * time.Second
	start := time.Now()
	code := followMacroRun(context.Background(), c, g, "run show", "nope", 10*time.Millisecond, idleTimeout, &stdout, &stderr, time.Now)
	elapsed := time.Since(start)

	if code != exitNotFound {
		t.Fatalf("exit code = %d, want exitNotFound (%d); stderr=%q", code, exitNotFound, stderr.String())
	}
	if elapsed > idleTimeout/2 {
		t.Errorf("elapsed = %s, want this reported almost immediately, not after waiting toward the %s idle window", elapsed, idleTimeout)
	}
}

// TestFollowMacroRunSurvivesTransientTransportFailureThenIdlesOut proves a
// dropped connection is treated as SILENCE (retried, counted toward the
// idle window) rather than an immediate fatal error — Step 7's own lesson
// ("a client that gives up before the server answers deletes an outcome
// from existence") applied to a follow loop rather than a single request.
// The server here answers successfully a few times, then the listener is
// closed: every poll after that fails at the transport level, and this
// loop must keep trying rather than reporting a transport failure outright,
// eventually reaching the SAME clean idle-timeout exit as the
// always-healthy case.
func TestFollowMacroRunSurvivesTransientTransportFailureThenIdlesOut(t *testing.T) {
	var pollCount int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&pollCount, 1)
		jsonServer(w, macroRunFixture("run-4", "running", nil, nil, ""))
	}))
	// Close the listener shortly after starting, well before the idle
	// window elapses, so most polls in this test observe a transport
	// failure rather than a real response.
	go func() {
		time.Sleep(30 * time.Millisecond)
		ts.Close()
	}()

	c, err := newClient(ts.URL, "", &http.Client{})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	g := &globalFlags{timeout: minMacroClientTimeout, output: outputText}

	var stdout, stderr bytes.Buffer
	const idleTimeout = 200 * time.Millisecond
	code := followMacroRun(context.Background(), c, g, "run show", "run-4", 10*time.Millisecond, idleTimeout, &stdout, &stderr, time.Now)

	if code != exitFollowStillWatching {
		t.Fatalf("exit code = %d, want exitFollowStillWatching (%d) — a transport failure must not become a reported error here; stdout=%q stderr=%q",
			code, exitFollowStillWatching, stdout.String(), stderr.String())
	}
	// The FINAL message to the operator (stdout) must still read clean,
	// even though this run genuinely experienced transport failures along
	// the way (which may be noted on stderr, but never as the terminal
	// verdict on stdout).
	for _, bad := range []string{"context deadline exceeded", "dial tcp", "EOF", "connection refused"} {
		if strings.Contains(stdout.String(), bad) {
			t.Errorf("stdout contains %q; a transport failure must never be the reported terminal message: %q", bad, stdout.String())
		}
	}
}
