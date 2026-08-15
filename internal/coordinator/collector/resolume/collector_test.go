package resolume

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
	"github.com/showmeshsystems/showmesh/pkg/resolumecomp"
)

func newTestCollector(t *testing.T, baseURL string, opts Options) *Collector {
	t.Helper()
	c, err := New("resolume-main", baseURL, opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return c
}

func TestNewRejectsInvalidID(t *testing.T) {
	if _, err := New("Not Valid!", "http://127.0.0.1:9080", Options{}); err == nil {
		t.Fatalf("New() error = nil, want an error for an id that fails mqttproto.ValidateNodeID")
	}
}

func TestNewRejectsBadBaseURL(t *testing.T) {
	if _, err := New("resolume-main", "http://127.0.0.1:9080/composition", Options{}); err == nil {
		t.Fatalf("New() error = nil, want an error for a base URL carrying a path")
	}
}

func TestCollectorIDReturnsConfiguredID(t *testing.T) {
	c := newTestCollector(t, "http://127.0.0.1:9080", Options{})
	if got := c.ID(); got != "resolume-main" {
		t.Errorf("ID() = %q, want %q", got, "resolume-main")
	}
}

func TestPollSuccessProducesBothSignals(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/product" {
			t.Errorf("unexpected request path %q; with nothing ever uploaded, even the defect-3 startup-transition survey primed below issues zero by-id requests", r.URL.Path)
		}
		_, _ = w.Write(loadTestdata(t, "product.json"))
	}))
	defer srv.Close()

	now := time.Now()
	c := newTestCollector(t, srv.URL, Options{Now: fixedClock(&now)})

	// Defect 3 (TRACK-D-D2-SPEC.md's Show Mode gap): the very first
	// successful liveness poll a Collector ever performs is itself a
	// reachability transition (never-observed -> reachable) and
	// legitimately queues one survey — see
	// TestTransitionToReachableTriggersSurveyInTheSamePoll and
	// TestSteadyStateIsExactlyOneProductRequestPerInterval's own priming
	// for this exact behavior tested directly and in isolation. Prime past
	// it here so this test measures an ordinary steady-state poll, which is
	// what "exactly 2 observations" is actually asserting.
	c.Poll(context.Background())
	now = now.Add(c.Footprint().PollInterval())

	obs, complete := c.Poll(context.Background())
	if !complete {
		t.Errorf("Poll() complete = false, want true always (see Poll's doc comment)")
	}
	if len(obs) != 2 {
		t.Fatalf("len(Poll() observations) = %d, want exactly 2 (SignalReachable, SignalProduct)", len(obs))
	}

	reachable := findSignal(t, obs, SignalReachable)
	if reachable.StateAt(now) != observation.StateCurrent {
		t.Errorf("resolume.reachable.StateAt(now) = %q, want current", reachable.StateAt(now))
	}
	if reachable.Value != true {
		t.Errorf("resolume.reachable.Value = %v, want true", reachable.Value)
	}
	if reachable.Quality != observation.QualityDerived {
		t.Errorf("resolume.reachable.Quality = %q, want derived (reachability is derived from the request succeeding, not a field Resolume reports)", reachable.Quality)
	}
	if reachable.Source != sourceName {
		t.Errorf("resolume.reachable.Source = %q, want %q", reachable.Source, sourceName)
	}

	product := findSignal(t, obs, SignalProduct)
	if product.StateAt(now) != observation.StateCurrent {
		t.Errorf("resolume.product.StateAt(now) = %q, want current", product.StateAt(now))
	}
	wantProduct := "Arena 7.23.2 (r51094)"
	if product.Value != wantProduct {
		t.Errorf("resolume.product.Value = %v, want %q", product.Value, wantProduct)
	}
	if product.Quality != observation.QualityDirect {
		t.Errorf("resolume.product.Quality = %q, want direct", product.Quality)
	}
}

// --- Required test 2: connection refused produces collection_failed --------

// TestPollConnectionRefusedProducesCollectionFailedOnBothSignals is the
// required reproduction: an unreachable Resolume must degrade BOTH
// signals to collection_failed with a classified reason, and StateAt must
// yield collection_failed — never a value, never healthy. Mirrors
// internal/coordinator/collector/fpp's own
// TestPollUnreachableProducesCollectionFailedNeverFabricatedFalse.
//
// Before trusting this test, Poll's error branch was temporarily changed
// to still emit resolume.reachable as a measured `false` (the fabrication
// CLAUDE.md's "absent evidence is stated, never omitted" rule forbids)
// and this test's Absence assertion was confirmed to fail — a measured
// false observation has Absence == "" by construction, so the check
// failed loudly rather than silently. Reverted afterward.
func TestPollConnectionRefusedProducesCollectionFailedOnBothSignals(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing listening on this port for the rest of the test

	now := time.Now()
	c := newTestCollector(t, url, Options{Now: fixedClock(&now)})

	obs, complete := c.Poll(context.Background())
	if !complete {
		t.Errorf("Poll() complete = false, want true always")
	}

	reachable := findSignal(t, obs, SignalReachable)
	if reachable.Absence != observation.StateCollectionFailed {
		t.Errorf("resolume.reachable Absence = %q, want collection_failed", reachable.Absence)
	}
	if reachable.Value != nil {
		t.Errorf("resolume.reachable Value = %v, want nil — never a fabricated false", reachable.Value)
	}
	if reachable.Reason == "" {
		t.Errorf("resolume.reachable Reason is empty, want a classified failure reason")
	}
	if got := reachable.StateAt(now); got != observation.StateCollectionFailed {
		t.Errorf("resolume.reachable.StateAt(now) = %q, want collection_failed — never a value, never healthy", got)
	}

	product := findSignal(t, obs, SignalProduct)
	if product.Absence != observation.StateCollectionFailed {
		t.Errorf("resolume.product Absence = %q, want collection_failed", product.Absence)
	}
	if product.Value != nil {
		t.Errorf("resolume.product Value = %v, want nil", product.Value)
	}
	if got := product.StateAt(now); got != observation.StateCollectionFailed {
		t.Errorf("resolume.product.StateAt(now) = %q, want collection_failed", got)
	}

	if reachable.Reason != product.Reason {
		t.Errorf("resolume.reachable Reason (%q) and resolume.product Reason (%q) differ; a single failed /product request should classify identically for both", reachable.Reason, product.Reason)
	}
}

func TestPollHTTPStatusErrorProducesCollectionFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("unavailable"))
	}))
	defer srv.Close()

	now := time.Now()
	c := newTestCollector(t, srv.URL, Options{Now: fixedClock(&now)})

	obs, _ := c.Poll(context.Background())
	reachable := findSignal(t, obs, SignalReachable)
	if reachable.Absence != observation.StateCollectionFailed {
		t.Errorf("resolume.reachable Absence = %q, want collection_failed", reachable.Absence)
	}
	if !contains(reachable.Reason, "503") {
		t.Errorf("resolume.reachable Reason = %q, want it to mention the 503 status", reachable.Reason)
	}
}

func TestPollDecodeErrorProducesCollectionFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	now := time.Now()
	c := newTestCollector(t, srv.URL, Options{Now: fixedClock(&now)})

	obs, _ := c.Poll(context.Background())
	reachable := findSignal(t, obs, SignalReachable)
	if reachable.Absence != observation.StateCollectionFailed {
		t.Errorf("resolume.reachable Absence = %q, want collection_failed", reachable.Absence)
	}
	if reachable.Reason != "decode error" {
		t.Errorf("resolume.reachable Reason = %q, want %q", reachable.Reason, "decode error")
	}
}

// TestPollNeverRequestsComposition proves the D-1/D-2 boundary at the
// wire level: Poll must issue exactly one request, to /product, and never
// touch /composition — composition semantics are out of scope for this
// seam's Collector (see this package's doc comment: GET /composition is
// forbidden outright, not merely out of scope for Poll specifically).
func TestPollNeverRequestsComposition(t *testing.T) {
	requested := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested[r.URL.Path]++
		_, _ = w.Write(loadTestdata(t, "product.json"))
	}))
	defer srv.Close()

	now := time.Now()
	c := newTestCollector(t, srv.URL, Options{Now: fixedClock(&now)})
	c.Poll(context.Background())

	if requested["/api/v1/composition"] != 0 {
		t.Errorf("Poll() requested /composition %d time(s); this Collector must never read composition state", requested["/api/v1/composition"])
	}
	if requested["/api/v1/product"] != 1 {
		t.Errorf("Poll() requested /product %d time(s), want exactly 1", requested["/api/v1/product"])
	}
}

// TestPollNeverEmitsAnySignalOtherThanTheTwoOnSteadyState is seam D-1's
// original boundary test, renamed (was
// TestPollNeverEmitsAnySignalOtherThanTheTwo) to make explicit what changed
// in Track D seam D-2/C: with no [Collector.RequestSurvey] ever called,
// Poll's steady-state behavior is completely unchanged from D-1 — only
// resolume.reachable and resolume.product ever appear.
func TestPollNeverEmitsAnySignalOtherThanTheTwoOnSteadyState(t *testing.T) {
	allowed := map[observation.SignalID]bool{
		SignalReachable: true,
		SignalProduct:   true,
	}
	check := func(t *testing.T, obs []observation.Observation) {
		t.Helper()
		for _, o := range obs {
			if !allowed[o.Signal] {
				t.Errorf("Poll() emitted signal %q with no survey requested; steady state may only ever emit resolume.reachable and resolume.product", o.Signal)
			}
		}
	}

	// "success": a plain successful poll, once the one-time defect-3
	// startup-transition survey (see TestPollSuccessProducesBothSignals's
	// identical priming) is out of the way, must never emit anything beyond
	// the two liveness signals.
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(loadTestdata(t, "product.json"))
		}))
		defer srv.Close()
		now := time.Now()
		c := newTestCollector(t, srv.URL, Options{Now: fixedClock(&now)})

		c.Poll(context.Background()) // the startup transition's one legitimate survey
		now = now.Add(c.Footprint().PollInterval())

		obs, _ := c.Poll(context.Background())
		check(t, obs)
	})

	// "failure": a poll that never even reaches a successful liveness
	// result is the FIRST poll this Collector ever performs, but there is
	// no reachable transition to trigger anything — Product() itself
	// errored, so Poll's failure branch runs, never the success branch the
	// defect-3 triggers live in. No priming needed or possible.
	t.Run("failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		now := time.Now()
		c := newTestCollector(t, srv.URL, Options{Now: fixedClock(&now)})
		obs, _ := c.Poll(context.Background())
		check(t, obs)
	})
}

// --- Track D seam D-2/C acceptance criterion 9: steady-state traffic ------

// TestSteadyStateIsExactlyOneProductRequestPerInterval is
// TRACK-D-D2-SPEC.md acceptance criterion 9's own named test: with no
// [Collector.RequestSurvey] ever called, driving [Collector.Poll] across
// several dynamic poll intervals (a fake clock advanced by exactly
// [FootprintControls.PollInterval] between calls, the same shape Runner's
// own loop drives Poll in production — see collector.go's own doc comment
// on why Runner's fixed registration interval is NOT what governs this)
// must produce EXACTLY one GET /product per interval and no other request
// path at all. A survey appearing on the timer is exactly the regression
// this criterion exists to catch, per its own text: "the natural way to
// add a signal is to add it to the poll."
//
// Before trusting this test: [Collector.Poll]'s survey branch
// (`if hasPendingSurvey { obs = append(obs, c.survey(...)...) }`) was
// temporarily changed to run unconditionally on every call, and this test
// failed immediately — see this task's own report for the exact output.
// Reverted afterward.
func TestSteadyStateIsExactlyOneProductRequestPerInterval(t *testing.T) {
	var requested []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.Path)
		_, _ = w.Write(loadTestdata(t, "product.json"))
	}))
	defer srv.Close()

	now := time.Now()
	c := newTestCollector(t, srv.URL, Options{Now: fixedClock(&now)})

	// Defect 3 (TRACK-D-D2-SPEC.md's Show Mode gap): the very first
	// successful liveness poll after construction is itself a reachability
	// transition (never-observed -> reachable) and legitimately queues ONE
	// survey, so the dashboard repopulates on a coordinator restart even
	// with the WebSocket held closed (ADR-033 Show Mode) — see
	// TestTransitionToReachableTriggersSurveyInTheSamePoll and
	// TestSteadyStateAfterInitialTransitionNeverSurveysAgain for that exact
	// behavior tested directly and in isolation. That is real, desired
	// traffic and must not be mistaken for the timer-driven survey THIS
	// criterion actually forbids — prime it here, before measuring "steady
	// state", exactly as an operator's dashboard would see one extra burst
	// right after the coordinator comes up and then nothing until something
	// actually changes. It costs zero EXTRA requests either way (nothing is
	// uploaded to this Collector's CompositionStore, so its one survey
	// issues no by-id reads at all) — only the request-count assertion
	// below is what this priming keeps honest.
	if _, complete := c.Poll(context.Background()); !complete {
		t.Fatalf("priming Poll() complete = false, want true")
	}
	now = now.Add(c.Footprint().PollInterval())
	requested = nil

	const intervals = 5
	for i := 0; i < intervals; i++ {
		obs, complete := c.Poll(context.Background())
		if !complete {
			t.Fatalf("interval %d: Poll() complete = false, want true (this poll was due)", i)
		}
		if len(obs) != 2 {
			t.Fatalf("interval %d: Poll() returned %d observation(s), want exactly 2 (reachable, product) — a survey ran with no request for one", i, len(obs))
		}
		now = now.Add(c.Footprint().PollInterval())
	}

	if len(requested) != intervals {
		t.Fatalf("total requests after priming = %d, want exactly %d (one per interval)", len(requested), intervals)
	}
	for i, path := range requested {
		if path != "/api/v1/product" {
			t.Errorf("request %d = %q, want /api/v1/product — steady state must never touch any other path", i, path)
		}
	}
}

// TestSteadyStateAfterADispatchStillExactlyOneProductRequestPerInterval
// extends the test above to a case it cannot catch: it never constructs an
// [ActionDispatcher], so a future seam that wired a post-dispatch
// [Collector.RequestSurvey] call (or any other extra steady-state traffic)
// would only show up in the poll intervals AFTER a real dispatch — never
// in a suite that never dispatches anything. This drives one genuinely
// CONFIRMED action (selectDeck) against the SAME [Collector] the polling
// loop below measures, over a server that answers both /api/v1/product
// (for Poll) and [fakeArena]'s by-id/write surface (for Dispatch), then
// asserts steady state is unchanged afterward — the same one-request-per-
// interval, product-only assertion the sibling test makes, without
// weakening it.
func TestSteadyStateAfterADispatchStillExactlyOneProductRequestPerInterval(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	arena.decks[testDeckOne] = &faDeck{selected: false, name: "Deck One"}
	arena.decks[testDeckTwo] = &faDeck{selected: true, name: "Deck Two"}

	var requested []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.Path)
		if r.URL.Path == "/api/v1/product" {
			_, _ = w.Write(loadTestdata(t, "product.json"))
			return
		}
		arena.ServeHTTP(w, r)
	}))
	defer srv.Close()

	store := newTestCompositionStore(t, parseTestComposition(t))
	c := newTestCollector(t, srv.URL, Options{Now: fixedClock(&now), CompositionStore: store})

	// Prime past the reachability-transition survey exactly like the
	// sibling test above — real, desired traffic this test does not
	// measure either. What that survey's identity check concludes does
	// not matter here: recordSurveySnapshot below overrides it with the
	// confirmed snapshot selectDeck's own identity gate needs.
	if _, complete := c.Poll(context.Background()); !complete {
		t.Fatalf("priming Poll() complete = false, want true")
	}
	now = now.Add(c.Footprint().PollInterval())
	c.recordSurveySnapshot(identifiedSnapshot(now))
	requested = nil

	d := NewActionDispatcher(c, ActionDispatcherOptions{
		Now: fixedClock(&now), Sleep: fakeSleep(&now), PollInterval: 10 * time.Millisecond,
	})
	out, err := d.Dispatch(context.Background(), ActionSelectDeck, ActionParams{DeckID: testDeckOne})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State != ActionConfirmed {
		t.Fatalf("State = %q, want %q (reason: %s) — this test needs a genuinely CONFIRMED dispatch to prove anything about what happens after one", out.State, ActionConfirmed, out.Reason)
	}
	requested = nil // discard the dispatch's own baseline/write/confirm traffic; only what follows is steady state

	const intervals = 5
	for i := 0; i < intervals; i++ {
		obs, complete := c.Poll(context.Background())
		if !complete {
			t.Fatalf("interval %d: Poll() complete = false, want true (this poll was due)", i)
		}
		if len(obs) != 2 {
			t.Fatalf("interval %d: Poll() returned %d observation(s), want exactly 2 (reachable, product) — a dispatch must never leave a pending survey behind", i, len(obs))
		}
		now = now.Add(c.Footprint().PollInterval())
	}

	if len(requested) != intervals {
		t.Fatalf("total requests after the dispatch = %d, want exactly %d (one per interval) — a dispatch must not grow steady-state traffic", len(requested), intervals)
	}
	for i, path := range requested {
		if path != "/api/v1/product" {
			t.Errorf("request %d after the dispatch = %q, want /api/v1/product — steady state must never touch another path after a dispatch either", i, path)
		}
	}
}

// TestPollSkipsWhenNotYetDueUnderDynamicInterval proves the OTHER half of
// the self-throttle collector.go's own doc comment describes: calling Poll
// again before FootprintControls.PollInterval has elapsed must issue NO
// request at all, returning the documented skip shape (nil, false) rather
// than a second /product call.
func TestPollSkipsWhenNotYetDueUnderDynamicInterval(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write(loadTestdata(t, "product.json"))
	}))
	defer srv.Close()

	now := time.Now()
	c := newTestCollector(t, srv.URL, Options{Now: fixedClock(&now)})

	if _, complete := c.Poll(context.Background()); !complete {
		t.Fatalf("first Poll() complete = false, want true")
	}
	if requests != 1 {
		t.Fatalf("requests after first Poll() = %d, want 1", requests)
	}

	// No time advanced at all: the very next call must skip.
	obs, complete := c.Poll(context.Background())
	if complete {
		t.Errorf("second immediate Poll() complete = true, want false (skip)")
	}
	if obs != nil {
		t.Errorf("second immediate Poll() observations = %v, want nil", obs)
	}
	if requests != 1 {
		t.Errorf("requests after second (skipped) Poll() = %d, want still 1", requests)
	}
}

// TestFootprintControlsPollIntervalChangeTakesEffectWithoutReconstruction
// is ADR-033/TRACK-D-D2-SPEC.md §3.3's own shape requirement, proved
// directly: shrinking [FootprintControls.PollInterval] on the SAME
// *Collector, with no reconstruction of anything, makes an
// otherwise-too-soon Poll() call due.
func TestFootprintControlsPollIntervalChangeTakesEffectWithoutReconstruction(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write(loadTestdata(t, "product.json"))
	}))
	defer srv.Close()

	now := time.Now()
	c := newTestCollector(t, srv.URL, Options{Now: fixedClock(&now)})

	c.Poll(context.Background())
	if requests != 1 {
		t.Fatalf("requests after first Poll() = %d, want 1", requests)
	}

	// Advance the clock by less than DefaultPollInterval but more than a
	// much shorter interval this same Collector is now reconfigured to —
	// with no new *Collector, no new *Client, no restart of anything.
	now = now.Add(50 * time.Millisecond)
	c.Footprint().SetPollInterval(10 * time.Millisecond)

	if _, complete := c.Poll(context.Background()); !complete {
		t.Fatalf("Poll() after shrinking the interval complete = false, want true (due under the new, smaller interval)")
	}
	if requests != 2 {
		t.Fatalf("requests after interval change = %d, want 2", requests)
	}
}

// TestRequestSurveyBypassesLivenessThrottle proves a pending survey makes
// Poll due regardless of how much of the liveness interval remains — a
// confirmed reconnect must not wait out however much of the dynamic
// interval happens to be left.
func TestRequestSurveyBypassesLivenessThrottle(t *testing.T) {
	requested := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested[r.URL.Path]++
		if r.URL.Path == "/api/v1/product" {
			_, _ = w.Write(loadTestdata(t, "product.json"))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	now := time.Now()
	c := newTestCollector(t, srv.URL, Options{Now: fixedClock(&now)})

	c.Poll(context.Background()) // first poll is always due
	if requested["/api/v1/product"] != 1 {
		t.Fatalf("/product requests after first Poll() = %d, want 1", requested["/api/v1/product"])
	}

	c.RequestSurvey(true)
	// No time advanced: an ordinary Poll() would skip, but a pending
	// survey must still make this call due.
	obs, complete := c.Poll(context.Background())
	if !complete {
		t.Fatalf("Poll() with a pending survey complete = false, want true")
	}
	if requested["/api/v1/product"] != 2 {
		t.Errorf("/product requests after the survey-triggered Poll() = %d, want 2", requested["/api/v1/product"])
	}
	if len(obs) <= 2 {
		t.Errorf("Poll() with a pending survey returned only %d observation(s), want more than the 2 liveness signals (a survey ran, even though it found nothing uploaded)", len(obs))
	}
}

// --- operator-facing string guard -----------------------------------------

// operatorFacingStringGuardFiles is every file in this package whose
// string literals can reach an [observation.Observation] value or reason
// — readiness.go and identity.go's own doc comments both promise this
// guard exists; collector.go is the third, since every UnknownReason and
// formatted Value string this package produces is built there.
var operatorFacingStringGuardFiles = []string{
	"readiness.go",
	"identity.go",
	"collector.go",
	// Track D seam D-3: every ActionOutcome.Reason string this package
	// produces is built in these three files, and the identical operator-
	// facing rule applies to them unchanged — see action.go's own doc
	// comment.
	"action.go",
	"action_dispatch.go",
	"action_client.go",
}

// operatorFacingStringForbiddenPattern mirrors
// internal/coordinator/api/fppcommand_copy_guard_test.go's own
// forbiddenCopyPattern (CLAUDE.md's rule, restated in this task's own
// brief): a repo path, a doc/spec filename, an ADR or research-record
// number, or "section" followed by a digit are all internal citations that
// must never reach an operator.
var operatorFacingStringForbiddenPattern = regexp.MustCompile(
	`docs/|\.md\b|ADR-\d+|RES-\d{3}|(?i)\bsection\s+\d`,
)

// TestReadinessAndIdentityStringsCarryNoInternalCitation is the guard
// readiness.go's and identity.go's own doc comments name. Parses SOURCE
// only (an *ast.BasicLit walk, exactly like
// guardfullcomposition_test.go's own AST walk in this same package) —
// comments are never visited, so a citation in a `//` comment (this
// package's own convention for citing TRACK-D-D2-SPEC.md by section, as
// this very file does throughout) never trips it.
//
// Before trusting this test: temporarily changed
// boolTermHoldsWhenFalse's PresenceNull branch (readiness.go) to return
// UnknownReason: fieldLabel + " was null (see TRACK-D-D2-SPEC.md section 4)"
// and reran — failed immediately, naming the file, line, and offending
// substring. Reverted afterward.
// --- Track D seam D-2/C: Survey end to end -------------------------------

// newTestCompositionStore builds a *CompositionStore already loaded with
// comp, via [fakeCompositionConfigReader] and [CompositionStore.Refresh]
// (idmap_test.go's own pattern), so this file never needs a *store.Store
// or the coordinator wiring layer to exercise [Collector.survey].
func newTestCompositionStore(t *testing.T, comp *resolumecomp.Composition) *CompositionStore {
	t.Helper()
	var s CompositionStore
	reader := &fakeCompositionConfigReader{}
	reader.setRevision(t, 1, comp)
	if err := s.Refresh(context.Background(), reader); err != nil {
		t.Fatalf("CompositionStore.Refresh: %v", err)
	}
	return &s
}

// TestSurveyNoCompositionUploadedReportsNotCollected is TRACK-D-D2-SPEC.md
// §9's D-2/C row's own "no composition uploaded" case: a survey against a
// Collector with nothing ever loaded into its CompositionStore must report
// every composition-level signal as StateNotCollected with a reason —
// never an empty composition, never a silent absence of signals, and
// crucially, no request beyond /product at all (there is nothing to
// survey).
func TestSurveyNoCompositionUploadedReportsNotCollected(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		_, _ = w.Write(loadTestdata(t, "product.json"))
	}))
	defer srv.Close()

	now := time.Now()
	c := newTestCollector(t, srv.URL, Options{Now: fixedClock(&now)})
	c.RequestSurvey(true)

	obs, complete := c.Poll(context.Background())
	if !complete {
		t.Fatalf("Poll() complete = false, want true")
	}

	for _, sig := range []observation.SignalID{SignalCompositionName, SignalCompositionIdentified, SignalCompositionDecks, SignalCompositionSelectedDeck} {
		o := findSignal(t, obs, sig)
		if o.Absence != observation.StateNotCollected {
			t.Errorf("%s Absence = %q, want %q", sig, o.Absence, observation.StateNotCollected)
		}
		if o.Reason == "" {
			t.Errorf("%s Reason is empty, want a reason naming that nothing has been uploaded", sig)
		}
	}

	if len(seen) != 1 || seen[0] != "/api/v1/product" {
		t.Errorf("requests = %v, want exactly one /api/v1/product request (no composition to survey)", seen)
	}
}

// TestSurveyEndToEndAgainstFixtureComposition drives [Collector.Poll] (via
// [Collector.RequestSurvey]) against a fake Arena serving every by-id
// endpoint the operator's own testdata/complete.avc fixture composition
// names, and checks the produced observations against what that
// composition actually contains — proving the wiring from HTTP response
// through [CheckIdentity]/[LayerReady] to a stamped [observation.Observation]
// end to end, not just each piece in isolation.
func TestSurveyEndToEndAgainstFixtureComposition(t *testing.T) {
	comp := parseTestComposition(t)
	store := newTestCompositionStore(t, comp)

	paramState := func(id int, value string) string {
		return `{"valuetype":"ParamState","id":` + itoa(id) + `,"value":"` + value + `","options":["Empty","Disconnected","Previewing","Connected","Connected & previewing"]}`
	}
	clipJSON := func(id int64, paramID int, name, connected string) []byte {
		return []byte(`{"id":` + itoa64(id) + `,"name":{"valuetype":"ParamString","id":` + itoa(paramID) + `,"value":"` + name + `"},` +
			`"connected":` + paramState(paramID+1, connected) + `,` +
			`"transport":{"position":{"valuetype":"ParamRange","id":` + itoa(paramID+2) + `,"value":0},"controls":null},` +
			`"transporttype":{"valuetype":"ParamChoice","id":` + itoa(paramID+3) + `,"value":"Timeline","options":["Timeline","BPM Sync","SMPTE 1","SMPTE 2","Denon DJ","Pioneer DJ"]}}`)
	}

	responses := map[string][]byte{
		"/api/v1/product": loadTestdata(t, "product.json"),

		// No /api/v1/composition/bypassed, /master, or /name entries: the
		// composition-level parameter ladder was deleted (defect 2,
		// 2026-08-15) because no such path exists in Arena's own OpenAPI
		// specification — see client.go's own doc comment. If this
		// package ever issues one of those requests again, the fake server
		// below answers with a 404 (not in this map), which is exactly the
		// signal a regression should produce.

		"/api/v1/composition/decks/by-id/2000000000001": []byte(`{"id":2000000000001,"name":{"valuetype":"ParamString","id":1,"value":"Deck One"},"selected":{"valuetype":"ParamBoolean","id":2,"value":true}}`),
		"/api/v1/composition/decks/by-id/2000000000002": []byte(`{"id":2000000000002,"name":{"valuetype":"ParamString","id":3,"value":"Deck Two"},"selected":{"valuetype":"ParamBoolean","id":4,"value":false}}`),

		"/api/v1/composition/layers/by-id/3000000000001": []byte(`{"id":3000000000001,"bypassed":{"valuetype":"ParamBoolean","id":10,"value":false},"master":{"valuetype":"ParamRange","id":11,"value":1.0},"video":{"opacity":{"valuetype":"ParamRange","id":12,"value":1.0}},"active_clip":{"id":6000000000001}}`),
		"/api/v1/composition/layers/by-id/3000000000002": []byte(`{"id":3000000000002,"bypassed":{"valuetype":"ParamBoolean","id":20,"value":true},"master":{"valuetype":"ParamRange","id":21,"value":1.0},"video":{"opacity":{"valuetype":"ParamRange","id":22,"value":1.0}},"active_clip":null}`),
		"/api/v1/composition/layers/by-id/3000000000003": []byte(`{"id":3000000000003,"bypassed":{"valuetype":"ParamBoolean","id":30,"value":false},"master":{"valuetype":"ParamRange","id":31,"value":1.0},"video":{"opacity":{"valuetype":"ParamRange","id":32,"value":1.0}},"active_clip":{"id":6000000000003}}`),

		"/api/v1/composition/layergroups/by-id/4000000000001": []byte(`{"id":4000000000001,"bypassed":{"valuetype":"ParamBoolean","id":40,"value":false},"master":{"valuetype":"ParamRange","id":41,"value":1.0}}`),
		"/api/v1/composition/layergroups/by-id/4000000000002": []byte(`{"id":4000000000002,"bypassed":{"valuetype":"ParamBoolean","id":42,"value":false},"master":{"valuetype":"ParamRange","id":43,"value":1.0}}`),

		"/api/v1/composition/clips/by-id/6000000000001": clipJSON(6000000000001, 50, "Snowflakes", "Connected"),
		"/api/v1/composition/clips/by-id/6000000000003": clipJSON(6000000000003, 60, "Clip B", "Disconnected"),
		"/api/v1/composition/clips/by-id/7000000000001": clipJSON(7000000000001, 70, "Persistent A", "Disconnected"),
		"/api/v1/composition/clips/by-id/7000000000002": clipJSON(7000000000002, 80, "Persistent B", "Disconnected"),
	}

	var mu sync.Mutex
	seen := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path]++
		mu.Unlock()
		body, ok := responses[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	now := time.Now()
	c, err := New("resolume-main", srv.URL, Options{Now: fixedClock(&now), CompositionStore: store})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	c.RequestSurvey(true)
	obs, complete := c.Poll(context.Background())
	if !complete {
		t.Fatalf("Poll() complete = false, want true")
	}

	byID := make(map[observation.SignalID]observation.Observation, len(obs))
	for _, o := range obs {
		byID[o.Signal] = o
	}

	// Identity: deck one is selected, both its non-empty clips and both
	// persistent clips resolve -> identified, and the load window closes
	// in this same survey (afterReconnect=true, but a determinate result).
	identified := byID[SignalCompositionIdentified]
	if identified.Value != "identified" {
		t.Errorf("%s = %v, want %q", SignalCompositionIdentified, identified.Value, "identified")
	}

	// resolume.composition.name is permanently unavailable (defect 2,
	// 2026-08-15): there is no path this package may use to read it, and
	// it must never be backfilled from the uploaded composition file.
	name := byID[SignalCompositionName]
	if name.Absence != observation.StateUnsupported {
		t.Errorf("%s Absence = %q, want %q", SignalCompositionName, name.Absence, observation.StateUnsupported)
	}
	if name.Reason == "" {
		t.Errorf("%s Reason is empty, want a reason", SignalCompositionName)
	}

	decks := byID[SignalCompositionDecks]
	if decks.Value != int64(2) {
		t.Errorf("%s = %v, want 2", SignalCompositionDecks, decks.Value)
	}
	selectedDeck := byID[SignalCompositionSelectedDeck]
	if s, ok := selectedDeck.Value.(string); !ok || !contains(s, "Deck One") {
		t.Errorf("%s = %v, want it to name Deck One", SignalCompositionSelectedDeck, selectedDeck.Value)
	}

	// Layer 3000000000001: every LAYER/GROUP term is known and true, but
	// composition.bypassed/composition.master are permanently unavailable
	// (defect 2, 2026-08-15) — this composition can never report "ready"
	// through this seam, only "unknown" naming the two composition-level
	// terms. That is the honest, unconditional rung-2 behaviour.
	ready1 := byID[LayerReadySignal(3000000000001)]
	if s, ok := ready1.Value.(string); !ok || !contains(s, "unknown") || !contains(s, "composition.bypassed") || !contains(s, "composition.master") {
		t.Errorf("layer 3000000000001 ready = %v, want unknown naming composition.bypassed and composition.master", ready1.Value)
	}

	// Layer 3000000000002: bypassed -> not_ready naming the term, and
	// Kleene AND means this definite failure wins even though the two
	// composition-level terms are also unknown here. This is acceptance
	// criterion 2 exercised through the FULL survey pipeline, not
	// LayerReady in isolation.
	ready2 := byID[LayerReadySignal(3000000000002)]
	if s, ok := ready2.Value.(string); !ok || !contains(s, "not_ready") || !contains(s, "layer.bypassed") {
		t.Errorf("layer 3000000000002 ready = %v, want it to name layer.bypassed as not_ready", ready2.Value)
	}

	// Layer 3000000000003 has no layer group in the fixture -> both group
	// terms are unknown, on top of the two permanently-unavailable
	// composition-level terms.
	ready3 := byID[LayerReadySignal(3000000000003)]
	if s, ok := ready3.Value.(string); !ok || !contains(s, "unknown") || !contains(s, "layergroup") {
		t.Errorf("layer 3000000000003 ready = %v, want unknown naming a layergroup term", ready3.Value)
	}

	connected1 := byID[ClipConnectedSignal(6000000000001)]
	if connected1.Value != "Connected" {
		t.Errorf("clip 6000000000001 connected = %v, want %q", connected1.Value, "Connected")
	}

	// Layer 3000000000002 has no active clip (JSON null) -> a genuinely
	// measured, explicit, non-empty "none" value (defect 1's fix,
	// 2026-08-14) — never an absence, and never the blank string a
	// PresenceAbsent case would produce instead. See
	// TestLayerActiveClipObservationThreeWayPresence for all three Presence
	// outcomes tested directly.
	activeClip2, ok := byID[LayerActiveClipSignal(3000000000002)]
	if !ok {
		t.Fatalf("no active_clip observation for layer 3000000000002")
	}
	if activeClip2.Absence != "" {
		t.Errorf("layer 3000000000002 active_clip Absence = %q, want empty", activeClip2.Absence)
	}
	if activeClip2.Value != activeClipNoneValue {
		t.Errorf("layer 3000000000002 active_clip Value = %v, want %q", activeClip2.Value, activeClipNoneValue)
	}

	// Every survey-sourced observation must carry surveySourceName, never
	// sourceName — see that constant's own doc comment for why.
	for sig, o := range byID {
		if sig == SignalReachable || sig == SignalProduct {
			continue
		}
		if o.Source != surveySourceName {
			t.Errorf("%s Source = %q, want %q", sig, o.Source, surveySourceName)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	// Deck Two's own clip (on the non-selected deck, and never anyone's
	// active_clip) must never be requested — §3.4's "deliberately never
	// read" rule.
	if n := seen["/api/v1/composition/clips/by-id/6000000000101"]; n != 0 {
		t.Errorf("clip 6000000000101 was requested %d time(s), want 0", n)
	}
	if n := seen["/api/v1/composition"]; n != 0 {
		t.Errorf("GET /composition was requested %d time(s), want 0 — forbidden on every path", n)
	}
	// The deleted composition-level parameter ladder must never be
	// reconstructed: this package issues no request to any of these three
	// paths, because none of them exists in Arena's own specification
	// (defect 2, 2026-08-15).
	for _, p := range []string{"/api/v1/composition/bypassed", "/api/v1/composition/master", "/api/v1/composition/name"} {
		if n := seen[p]; n != 0 {
			t.Errorf("%s requested %d time(s), want 0 — this path does not exist on Arena's own API", p, n)
		}
	}
}

func itoa(n int) string     { return strconv.Itoa(n) }
func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

// --- Defect 1 (2026-08-14): null-vs-absent on active_clip, connected, and
// transporttype -------------------------------------------------------------

// TestLayerActiveClipObservationThreeWayPresence drives
// [Collector.layerActiveClipObservation] directly against all three
// [Presence] outcomes. Before trusting this test: reverted to the
// pre-fix body (PresencePresent -> formatRef, everything else -> a
// measured ""), and reran — the PresenceNull case failed immediately
// (Value = "", want a non-empty explicit value) and the PresenceAbsent
// case failed immediately too (Absence = "", want collection_failed).
// Restored afterward.
func TestLayerActiveClipObservationThreeWayPresence(t *testing.T) {
	now := time.Now()
	c := newTestCollector(t, "http://127.0.0.1:9080", Options{Now: fixedClock(&now)})

	present := Layer{ActiveClip: ActiveClipField{Presence: PresencePresent, Clip: &ActiveClip{ID: 42}}}
	o := c.layerActiveClipObservation(1, present, now)
	if o.Absence != "" {
		t.Errorf("present Absence = %q, want empty", o.Absence)
	}
	if s, ok := o.Value.(string); !ok || !contains(s, "42") {
		t.Errorf("present Value = %v, want it to name clip id 42", o.Value)
	}

	null := Layer{ActiveClip: ActiveClipField{Presence: PresenceNull}}
	o = c.layerActiveClipObservation(2, null, now)
	if o.Absence != "" {
		t.Errorf("null Absence = %q, want empty — a null active_clip is a real measured fact (nothing playing), not an absence of evidence", o.Absence)
	}
	s, ok := o.Value.(string)
	if !ok || s == "" {
		t.Fatalf("null Value = %v, want a non-empty explicit string value — CLAUDE.md's \"a missing field renders as blank\" rule", o.Value)
	}
	if contains(s, "id ") {
		t.Errorf("null Value = %q looks like formatRef's clip-reference shape; it must be unmistakably NOT a clip", s)
	}

	absent := Layer{ActiveClip: ActiveClipField{Presence: PresenceAbsent}}
	o = c.layerActiveClipObservation(3, absent, now)
	if o.Absence != observation.StateCollectionFailed {
		t.Errorf("absent Absence = %q, want collection_failed — an attempt was made (the layer itself decoded) and this one field could not be obtained", o.Absence)
	}
	if o.Value != nil {
		t.Errorf("absent Value = %v, want nil", o.Value)
	}
	if o.Reason == "" {
		t.Errorf("absent Reason is empty, want a reason naming the field")
	}
}

// TestClipConnectedAndTransportTypeObservationThreeWayPresence is the
// SAME defect-1 pattern, found and fixed in the two other places this task
// found it: [Collector.clipConnectedObservation] and
// [Collector.clipTransportTypeObservation]. Before trusting this test:
// reverted both methods to the pre-fix body (PresencePresent -> the real
// value, everything else -> a measured ""), and reran — every null/absent
// case below failed immediately (Absence = "", want collection_failed).
// Restored afterward.
func TestClipConnectedAndTransportTypeObservationThreeWayPresence(t *testing.T) {
	now := time.Now()
	c := newTestCollector(t, "http://127.0.0.1:9080", Options{Now: fixedClock(&now)})

	present := Clip{
		Connected:     ParamStateField{Presence: PresencePresent, Param: &ParamState{Value: "Connected", ValuePresence: PresencePresent}},
		TransportType: ParamChoiceField{Presence: PresencePresent, Param: &ParamChoice{Value: "Timeline", ValuePresence: PresencePresent}},
	}
	connected := c.clipConnectedObservation(1, present, now)
	if connected.Absence != "" || connected.Value != "Connected" {
		t.Errorf("present connected = %+v, want a measured %q", connected, "Connected")
	}
	tt := c.clipTransportTypeObservation(1, present, now)
	if tt.Absence != "" || tt.Value != "Timeline" {
		t.Errorf("present transporttype = %+v, want a measured %q", tt, "Timeline")
	}

	null := Clip{
		Connected:     ParamStateField{Presence: PresenceNull},
		TransportType: ParamChoiceField{Presence: PresenceNull},
	}
	nullConnected := c.clipConnectedObservation(2, null, now)
	if nullConnected.Absence != observation.StateCollectionFailed {
		t.Errorf("null connected Absence = %q, want collection_failed — a null value must never read as an empty measured string", nullConnected.Absence)
	}
	if nullConnected.Value != nil {
		t.Errorf("null connected Value = %v, want nil", nullConnected.Value)
	}
	if nullConnected.Reason == "" {
		t.Errorf("null connected Reason is empty, want a reason naming the field")
	}
	nullTT := c.clipTransportTypeObservation(2, null, now)
	if nullTT.Absence != observation.StateCollectionFailed {
		t.Errorf("null transporttype Absence = %q, want collection_failed", nullTT.Absence)
	}

	absent := Clip{} // zero value: Connected/TransportType both PresenceAbsent
	absentConnected := c.clipConnectedObservation(3, absent, now)
	if absentConnected.Absence != observation.StateCollectionFailed {
		t.Errorf("absent connected Absence = %q, want collection_failed", absentConnected.Absence)
	}
	absentTT := c.clipTransportTypeObservation(3, absent, now)
	if absentTT.Absence != observation.StateCollectionFailed {
		t.Errorf("absent transporttype Absence = %q, want collection_failed", absentTT.Absence)
	}

	// Null and absent must stay distinguishable from EACH OTHER, not only
	// from the present case — "Resolume said null" and "ShowMesh never saw
	// the field" are different facts (this file's own top comment on
	// [layerActiveClipObservation]'s sibling fix).
	if nullConnected.Reason == absentConnected.Reason {
		t.Errorf("null and absent connected share one reason (%q); an operator cannot tell the two apart", nullConnected.Reason)
	}
}

// --- Defect 2 (2026-08-15): the composition-level ladder is deleted, not
// disabled --------------------------------------------------------------

// TestCompositionLevelTermsAndNameArePermanentlyUnavailable is defect 2's
// own regression test: composition.bypassed and composition.master must
// always be Known=false, and resolume.composition.name must always be
// [observation.StateUnsupported], with NO HTTP request issued to get
// there — never conditioned on a configuration flag, never attempted, and
// never backfilled from anywhere, because no path exists on Arena's own
// API to read any of the three (client.go's own doc comment).
func TestCompositionLevelTermsAndNameArePermanentlyUnavailable(t *testing.T) {
	bypassedTerm := compositionLevelReadinessTerm(string(ReadinessTermCompositionBypassed))
	if bypassedTerm.Known {
		t.Errorf("composition.bypassed term Known = true, want false — this term is permanently unavailable")
	}
	if bypassedTerm.UnknownReason == "" {
		t.Errorf("composition.bypassed UnknownReason is empty, want a stated reason")
	}

	masterTerm := compositionLevelReadinessTerm(string(ReadinessTermCompositionMaster))
	if masterTerm.Known {
		t.Errorf("composition.master term Known = true, want false — this term is permanently unavailable")
	}

	c := newTestCollector(t, "http://127.0.0.1:1", Options{})
	nameObs := c.compositionNameObservation(time.Now())
	if nameObs.Value != nil {
		t.Errorf("composition name Value = %v, want nil (an absence)", nameObs.Value)
	}
	if nameObs.Absence != observation.StateUnsupported {
		t.Errorf("composition name Absence = %q, want %q", nameObs.Absence, observation.StateUnsupported)
	}
	if nameObs.Reason == "" {
		t.Errorf("composition name Reason is empty, want a stated reason")
	}
}

// --- Defect 3 (2026-08-14): a survey trigger that does not depend on the
// WebSocket ------------------------------------------------------------------

// TestTransitionToReachableTriggersSurveyInTheSamePoll proves BOTH of
// TRACK-D-D2-SPEC.md's new triggers with one collector: the very first
// successful liveness poll ever (never-observed -> reachable), and a
// later down -> up transition. Both must run their survey in the SAME
// [Collector.Poll] call that observed the transition, with no
// [Collector.RequestSurvey] ever called.
//
// Before trusting this test: temporarily made [Collector.noteLivenessAndCheckTransition]
// always return false, and reran — failed immediately at the FIRST
// assertion ("startup transition poll returned only 2 observation(s),
// want more than 2"), a t.Fatalf that halts the test before the later
// down->up assertion can even run. Restored afterward.
func TestTransitionToReachableTriggersSurveyInTheSamePoll(t *testing.T) {
	var mu sync.Mutex
	up := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		curUp := up
		mu.Unlock()
		if !curUp {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(loadTestdata(t, "product.json"))
	}))
	defer srv.Close()

	now := time.Now()
	c := newTestCollector(t, srv.URL, Options{Now: fixedClock(&now)})

	// The startup transition (never-observed -> reachable): one survey, in
	// this same first Poll() call, with no RequestSurvey ever called.
	obs, complete := c.Poll(context.Background())
	if !complete {
		t.Fatalf("startup Poll() complete = false, want true")
	}
	if len(obs) <= 2 {
		t.Fatalf("startup transition poll returned only %d observation(s), want more than 2 (its own survey ran)", len(obs))
	}
	// Advance well past DefaultTransitionSurveyMinInterval so the
	// down->up transition below is a genuinely SEPARATE, un-rate-limited
	// event, not a second transition arriving too soon after the first
	// (that specific case is TestFlappingReachabilityRateLimitsTransitionSurveys'
	// own job to prove).
	now = now.Add(DefaultTransitionSurveyMinInterval + time.Second)

	// Go down: no survey while unreachable (nothing to survey).
	mu.Lock()
	up = false
	mu.Unlock()
	obs, _ = c.Poll(context.Background())
	if len(obs) != 2 {
		t.Fatalf("down poll returned %d observation(s), want exactly 2 (both collection_failed)", len(obs))
	}
	now = now.Add(c.Footprint().PollInterval())

	// Come back up: a down -> up transition, well outside the rate-limit
	// window of the startup survey, must ALSO trigger a survey, in this
	// same Poll call.
	mu.Lock()
	up = true
	mu.Unlock()
	obs, complete = c.Poll(context.Background())
	if !complete {
		t.Fatalf("recovery Poll() complete = false, want true")
	}
	if len(obs) <= 2 {
		t.Errorf("recovery poll (down -> up) returned only %d observation(s), want more than 2 — a reachability transition must trigger a survey", len(obs))
	}
}

// TestSteadyStateAfterInitialTransitionNeverSurveysAgain is
// TRACK-D-D2-SPEC.md's own required companion test: once the one
// legitimate startup-transition survey has run, a steady succession of
// ordinary successful polls must NEVER survey again — the requirement
// this task's own brief names as priority 1 (acceptance criterion 9 must
// still hold).
func TestSteadyStateAfterInitialTransitionNeverSurveysAgain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(loadTestdata(t, "product.json"))
	}))
	defer srv.Close()

	now := time.Now()
	c := newTestCollector(t, srv.URL, Options{Now: fixedClock(&now)})

	obs, _ := c.Poll(context.Background())
	if len(obs) <= 2 {
		t.Fatalf("startup transition poll returned only %d observation(s), want more than 2 (its own survey ran)", len(obs))
	}
	now = now.Add(c.Footprint().PollInterval())

	for i := 0; i < 10; i++ {
		obs, complete := c.Poll(context.Background())
		if !complete {
			t.Fatalf("poll %d complete = false, want true", i)
		}
		if len(obs) != 2 {
			t.Fatalf("poll %d after the startup transition returned %d observation(s), want exactly 2 — a steady succession of successful polls must never re-survey", i, len(obs))
		}
		now = now.Add(c.Footprint().PollInterval())
	}
}

// TestFlappingReachabilityRateLimitsTransitionSurveys is priority-2 of this
// task's own brief: a flapping Arena (reachable, unreachable, reachable,
// ...) must not turn every recovery into another ~24-36-request survey.
// Drives the collector against a real fixture composition (so a survey
// that DOES run produces real by-id traffic to count) and flaps
// reachability four more times, all well within
// [DefaultTransitionSurveyMinInterval] of the one legitimate startup
// survey — an unrated implementation would issue a fresh burst of by-id
// requests on every recovery; this asserts the count never moves past what
// the startup survey alone produced.
//
// Before trusting this test: temporarily removed the rate-limit check from
// [Collector.noteLivenessAndCheckTransition] (always allowing a transition
// through) and reran — by-id requests after flapping = 27, want unchanged
// at 9 (three transitions' worth of this fixture's own by-id traffic,
// instead of the one the rate limit should have allowed). Restored
// afterward.
func TestFlappingReachabilityRateLimitsTransitionSurveys(t *testing.T) {
	comp := parseTestComposition(t)
	store := newTestCompositionStore(t, comp)

	var mu sync.Mutex
	up := true
	byIDRequests := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/product" {
			mu.Lock()
			curUp := up
			mu.Unlock()
			if !curUp {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write(loadTestdata(t, "product.json"))
			return
		}
		mu.Lock()
		byIDRequests++
		mu.Unlock()
		http.NotFound(w, r) // content does not matter; only HOW OFTEN a survey's worth of by-id traffic lands
	}))
	defer srv.Close()

	now := time.Now()
	c := newTestCollector(t, srv.URL, Options{Now: fixedClock(&now), CompositionStore: store})
	c.Footprint().SetPollInterval(time.Second) // short, so every flap below is "due" without waiting out DefaultTransitionSurveyMinInterval on its own

	poll := func(reachable bool) {
		mu.Lock()
		up = reachable
		mu.Unlock()
		c.Poll(context.Background())
		now = now.Add(c.Footprint().PollInterval())
	}

	poll(true) // startup transition: the one survey this test allows
	mu.Lock()
	afterStartupSurvey := byIDRequests
	mu.Unlock()
	if afterStartupSurvey == 0 {
		t.Fatalf("the startup reachability transition issued no by-id traffic at all; want its one survey to have run")
	}

	// Flap down/up twice more, all within DefaultTransitionSurveyMinInterval
	// of the startup survey (total elapsed: 4 x 1s poll interval = 4s).
	poll(false)
	poll(true)
	poll(false)
	poll(true)

	mu.Lock()
	got := byIDRequests
	mu.Unlock()
	if got != afterStartupSurvey {
		t.Errorf("by-id requests after flapping = %d, want unchanged at %d — a reachability transition within DefaultTransitionSurveyMinInterval of the last transition-triggered survey must not queue another one", got, afterStartupSurvey)
	}
}

// TestTransitionTriggeredSurveyAppliesTheLoadWindow is priority-3 of this
// task's own brief: TRACK-D-D2-SPEC.md §7's post-restart load window must
// apply to a transition-triggered survey exactly as it already does to a
// WebSocket-reconnect-triggered one.
//
// This deliberately does NOT just start a Collector fresh and run one
// survey against 404ing clips — [Collector.identityConfirmed] starts at
// its Go zero value (false) already, so a Collector that has NEVER
// confirmed anything would report the load window correctly EVEN IF the
// transition trigger forgot to pass afterReconnect=true, and the test
// would prove nothing about that wiring. Instead: phase 1 runs a survey
// against a fully resolving fixture, confirming identity for real
// (identityConfirmed becomes true — layer 3000000000001 measurably reports
// "ready"). Phase 2 then simulates Arena restarting — down, then back up
// answering /product again, but with every clip now 404ing, matching
// capture §10.1's measured ~1.2s post-restart shape (deck/layer/group
// objects still resolve; the real composition has not finished loading).
// Only a survey that correctly reopens the load window (afterReconnect
// true) can turn phase 1's "ready" back into "unknown" for the identical
// layer; a survey that does not would leave the stale confirmed identity
// in place and let real-looking (but pre-restart) deck/layer/group
// evidence compute a definite, wrong "ready" verdict.
//
// Before trusting this test: temporarily removed the
// `surveyAfterReconnect = true` line from the transition-trigger branch in
// [Collector.Poll] (collector.go) and reran — PASSED with the WEAKER
// version of this test that used to exist here (which started a fresh
// Collector straight into the load-window scenario, so identityConfirmed
// was already false regardless of the bug, and the test proved nothing).
// That false pass is exactly why this test was rewritten to phase 1 /
// phase 2. Rerunning the identical mutation against the version below: the
// layer readiness assertion failed exactly as expected (layer
// 3000000000001 stayed "ready" instead of reopening to "unknown"). The
// composition.identified assertion did NOT fail — CheckIdentity's own
// "nothing resolved" branch (identity.go) independently returns
// IdentityUnknown whenever every sampled clip id fails to resolve,
// regardless of loadWindow, so that signal has its own defense-in-depth
// against this exact bug. Layer readiness has no equivalent: loadWindow is
// the ONLY thing standing between it and computing a full, wrong,
// definite verdict off stale-but-still-200-OK deck/layer/group evidence —
// which is exactly what this test caught. Restored afterward.
func TestTransitionTriggeredSurveyAppliesTheLoadWindow(t *testing.T) {
	comp := parseTestComposition(t)
	store := newTestCompositionStore(t, comp)

	paramState := func(id int, value string) string {
		return `{"valuetype":"ParamState","id":` + itoa(id) + `,"value":"` + value + `","options":["Empty","Disconnected","Previewing","Connected","Connected & previewing"]}`
	}
	clipJSON := func(id int64, paramID int, name, connected string) []byte {
		return []byte(`{"id":` + itoa64(id) + `,"name":{"valuetype":"ParamString","id":` + itoa(paramID) + `,"value":"` + name + `"},` +
			`"connected":` + paramState(paramID+1, connected) + `,` +
			`"transport":{"position":{"valuetype":"ParamRange","id":` + itoa(paramID+2) + `,"value":0},"controls":null},` +
			`"transporttype":{"valuetype":"ParamChoice","id":` + itoa(paramID+3) + `,"value":"Timeline","options":["Timeline","BPM Sync","SMPTE 1","SMPTE 2","Denon DJ","Pioneer DJ"]}}`)
	}

	deckLayerGroupResponses := map[string][]byte{
		// No /api/v1/composition/bypassed, /master, or /name entries: the
		// composition-level parameter ladder was deleted (defect 2,
		// 2026-08-15) — see client.go's own doc comment.

		"/api/v1/composition/decks/by-id/2000000000001": []byte(`{"id":2000000000001,"name":{"valuetype":"ParamString","id":1,"value":"Deck One"},"selected":{"valuetype":"ParamBoolean","id":2,"value":true}}`),
		"/api/v1/composition/decks/by-id/2000000000002": []byte(`{"id":2000000000002,"name":{"valuetype":"ParamString","id":3,"value":"Deck Two"},"selected":{"valuetype":"ParamBoolean","id":4,"value":false}}`),

		"/api/v1/composition/layers/by-id/3000000000001": []byte(`{"id":3000000000001,"bypassed":{"valuetype":"ParamBoolean","id":10,"value":false},"master":{"valuetype":"ParamRange","id":11,"value":1.0},"video":{"opacity":{"valuetype":"ParamRange","id":12,"value":1.0}},"active_clip":{"id":6000000000001}}`),
		"/api/v1/composition/layers/by-id/3000000000002": []byte(`{"id":3000000000002,"bypassed":{"valuetype":"ParamBoolean","id":20,"value":false},"master":{"valuetype":"ParamRange","id":21,"value":1.0},"video":{"opacity":{"valuetype":"ParamRange","id":22,"value":1.0}},"active_clip":null}`),
		"/api/v1/composition/layers/by-id/3000000000003": []byte(`{"id":3000000000003,"bypassed":{"valuetype":"ParamBoolean","id":30,"value":false},"master":{"valuetype":"ParamRange","id":31,"value":1.0},"video":{"opacity":{"valuetype":"ParamRange","id":32,"value":1.0}},"active_clip":{"id":6000000000003}}`),

		"/api/v1/composition/layergroups/by-id/4000000000001": []byte(`{"id":4000000000001,"bypassed":{"valuetype":"ParamBoolean","id":40,"value":false},"master":{"valuetype":"ParamRange","id":41,"value":1.0}}`),
		"/api/v1/composition/layergroups/by-id/4000000000002": []byte(`{"id":4000000000002,"bypassed":{"valuetype":"ParamBoolean","id":42,"value":false},"master":{"valuetype":"ParamRange","id":43,"value":1.0}}`),
	}
	clipResponses := map[string][]byte{
		"/api/v1/composition/clips/by-id/6000000000001": clipJSON(6000000000001, 50, "Snowflakes", "Connected"),
		"/api/v1/composition/clips/by-id/6000000000003": clipJSON(6000000000003, 60, "Clip B", "Disconnected"),
		"/api/v1/composition/clips/by-id/7000000000001": clipJSON(7000000000001, 70, "Persistent A", "Disconnected"),
		"/api/v1/composition/clips/by-id/7000000000002": clipJSON(7000000000002, 80, "Persistent B", "Disconnected"),
	}

	var mu sync.Mutex
	up := true
	clipsResolve := true // toggled false to simulate the ~1.2s load window: deck/layer/group answer, clips do not

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/product" {
			mu.Lock()
			curUp := up
			mu.Unlock()
			if !curUp {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write(loadTestdata(t, "product.json"))
			return
		}
		if body, ok := deckLayerGroupResponses[r.URL.Path]; ok {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
			return
		}
		mu.Lock()
		resolve := clipsResolve
		mu.Unlock()
		if resolve {
			if body, ok := clipResponses[r.URL.Path]; ok {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(body)
				return
			}
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	now := time.Now()
	c := newTestCollector(t, srv.URL, Options{Now: fixedClock(&now), CompositionStore: store})

	// Phase 1: everything resolves. The startup transition's own survey
	// confirms identity for real (IdentityTrue), closing the load window.
	// Layer readiness cannot reach "ready" here — composition.bypassed and
	// composition.master are permanently unavailable (defect 2,
	// 2026-08-15) — so phase 1's own baseline is "unknown" naming those two
	// terms specifically, distinct from phase 2's load-window "unknown"
	// below.
	obs, complete := c.Poll(context.Background())
	if !complete {
		t.Fatalf("phase 1 Poll() complete = false, want true")
	}
	if identified := findSignal(t, obs, SignalCompositionIdentified); identified.Value != "identified" {
		t.Fatalf("phase 1 resolume.composition.identified = %v, want %q — the test setup must start with a confirmed identity, or phase 2 proves nothing", identified.Value, "identified")
	}
	ready1 := findSignal(t, obs, LayerReadySignal(3000000000001))
	if s, ok := ready1.Value.(string); !ok || !contains(s, "composition.bypassed") || !contains(s, "composition.master") {
		t.Fatalf("phase 1 layer 3000000000001 readiness = %v, want unknown naming composition.bypassed and composition.master", ready1.Value)
	}
	now = now.Add(DefaultTransitionSurveyMinInterval + time.Second) // clear of the rate limit before phase 2's own transition

	// Phase 2: Arena goes down, then comes back up — but every clip id now
	// 404s, matching capture §10.1's measured post-restart shape.
	mu.Lock()
	up = false
	clipsResolve = false
	mu.Unlock()
	obs, _ = c.Poll(context.Background())
	if len(obs) != 2 {
		t.Fatalf("down poll returned %d observation(s), want exactly 2 (both collection_failed)", len(obs))
	}
	now = now.Add(c.Footprint().PollInterval())

	mu.Lock()
	up = true
	mu.Unlock()
	obs, complete = c.Poll(context.Background())
	if !complete {
		t.Fatalf("recovery Poll() complete = false, want true")
	}

	identified := findSignal(t, obs, SignalCompositionIdentified)
	s, ok := identified.Value.(string)
	if !ok || !contains(s, "unknown") {
		t.Errorf("resolume.composition.identified after the transition = %v, want it to report unknown during the REOPENED load window — a previously confirmed identity must not paper over Resolume answering out of a stale/partial composition", identified.Value)
	}

	// Phase 1's own "unknown" named composition.bypassed/composition.master
	// specifically. Proving the state genuinely moved on reconnect — not
	// merely staying at the SAME unknown reason from phase 1 — means
	// checking for the load-window's own distinct wording, not just the
	// word "unknown" again.
	ready := findSignal(t, obs, LayerReadySignal(3000000000001))
	rs, ok := ready.Value.(string)
	if !ok || !contains(rs, "load window") {
		t.Errorf("layer 3000000000001 readiness after the transition = %v, want it to name the reopened load window — the SAME layer must not silently carry phase 1's unrelated unknown reason forward as if nothing changed", ready.Value)
	}
}

// TestCompositionUploadTriggersSurveyWithoutWaitingForAnythingElse is the
// third defect-3 trigger: a fresh upload landing in [CompositionStore]
// must reach the dashboard on its own, with no reachability transition and
// no explicit [Collector.RequestSurvey] — otherwise a survey against the
// OLD id map after a NEW upload is a dashboard describing the previous
// show (this task's own brief, quoting TRACK-D-D2-SPEC.md's own framing).
//
// Before trusting this test: temporarily made
// [Collector.compositionRevisionChanged] always return false, and reran —
// the post-upload assertion failed (len(obs) == 2, want more than 2).
// Restored afterward.
func TestCompositionUploadTriggersSurveyWithoutWaitingForAnythingElse(t *testing.T) {
	var store CompositionStore
	reader := &fakeCompositionConfigReader{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/product" {
			_, _ = w.Write(loadTestdata(t, "product.json"))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	now := time.Now()
	c := newTestCollector(t, srv.URL, Options{Now: fixedClock(&now), CompositionStore: &store})

	// Prime past the startup transition so what follows is unambiguously
	// due to the composition-revision trigger alone.
	c.Poll(context.Background())
	now = now.Add(c.Footprint().PollInterval())

	obs, _ := c.Poll(context.Background())
	if len(obs) != 2 {
		t.Fatalf("poll before any upload returned %d observation(s), want exactly 2 — no transition, no upload, nothing to survey", len(obs))
	}
	now = now.Add(c.Footprint().PollInterval())

	// An upload lands: the composition config store's revision moves from
	// 0 (nothing uploaded) to 1.
	comp := parseTestComposition(t)
	reader.setRevision(t, 1, comp)
	if err := store.Refresh(context.Background(), reader); err != nil {
		t.Fatalf("CompositionStore.Refresh: %v", err)
	}

	obs, complete := c.Poll(context.Background())
	if !complete {
		t.Fatalf("Poll() after upload complete = false, want true")
	}
	if len(obs) <= 2 {
		t.Errorf("Poll() after a composition upload returned only %d observation(s), want more than the 2 liveness signals — an upload must trigger a survey without waiting for a reconnect or an explicit refresh", len(obs))
	}
	identified := findSignal(t, obs, SignalCompositionIdentified)
	if identified.Absence == observation.StateNotCollected {
		t.Errorf("resolume.composition.identified Absence = %q after an upload landed, want a real survey result, not still not_collected", identified.Absence)
	}
}

func TestReadinessAndIdentityStringsCarryNoInternalCitation(t *testing.T) {
	for _, path := range operatorFacingStringGuardFiles {
		path := path
		t.Run(path, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parsing %s: %v", path, err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				if loc := operatorFacingStringForbiddenPattern.FindString(lit.Value); loc != "" {
					pos := fset.Position(lit.Pos())
					t.Errorf("%s:%d: string literal carries an internal citation (%q): %s\n"+
						"move the citation into a // comment; the string itself must read as if no internal doc, ADR, or research record existed",
						path, pos.Line, loc, lit.Value)
				}
				return true
			})
		})
	}
}
