package resolume

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
	"github.com/showmeshsystems/showmesh/pkg/resolumecomp"
)

// This file is Track D seam D-3a's own acceptance-criteria test suite
// (TRACK-D-D3A-CRASH-RECOVERY-SPEC.md §8, TRACK-D-D3A-BUILD-CONTRACT.md's
// three extra criteria). Every test drives real production code
// (Collector.RecoveryRecord, Recovery.HandleReachableTransition,
// Recovery.RunManualRestore, ActionDispatcher.Dispatch's own deck-change
// evidence) against a small in-memory fake Arena, reusing action_dispatch_test.go's
// fakeArena/fakeSleep/fixedClock and this file's own synthetic two-layer
// composition (deliberately NOT pkg/resolumecomp's shared complete.avc
// fixture — a composition this file fully controls keeps every test's
// "what should happen" traceable to one small, visible layout, the same
// judgment slowArenaDispatcher already makes in action_dispatch_test.go).

// recoveryTestComposition builds a small, fully-controlled composition:
// two layers, each with one deck clip (both on the one selected deck), and
// one persistent clip on layer one — enough for [TrackedComposition.IdentitySample]
// to sample both deck clips and the persistent clip, matching this
// package's own proven-good pattern (collector_test.go's
// TestTransitionTriggeredSurveyAppliesTheLoadWindow populates exactly this
// shape of clip to reach IdentityTrue).
func recoveryTestComposition() *resolumecomp.Composition {
	return &resolumecomp.Composition{
		Name:      "recovery test fixture",
		WrittenBy: resolumecomp.WrittenBy{Product: "Resolume Arena", Major: 7, Minor: 23, Micro: 2, Revision: 1},
		Canvas:    resolumecomp.Canvas{Width: 1920, Height: 1080},
		Decks:     []resolumecomp.Deck{{ID: testDeckOne.String(), Name: "Deck One"}},
		Layers: []resolumecomp.Layer{
			{ID: testLayerOne.String(), Index: 0, Name: "Whole House 1"},
			{ID: testLayerTwo.String(), Index: 1, Name: "Whole House 2"},
		},
		Clips: []resolumecomp.Clip{
			{ID: testClipA.String(), DeckID: testDeckOne.String(), LayerIndex: 0, ColumnIndex: 0, Name: "Green screen snowstorm"},
			{ID: testClipB.String(), DeckID: testDeckOne.String(), LayerIndex: 1, ColumnIndex: 0, Name: "Clip B"},
		},
		PersistentClips: []resolumecomp.Clip{
			{ID: testPersistA.String(), LayerIndex: 0, Name: "Persistent A"},
		},
	}
}

// recoveryFixture is one test's whole wiring: a fake Arena serving both
// the by-id survey/dispatch surface (fakeArena) and GET /api/v1/product,
// a *Collector over recoveryTestComposition, a *ActionDispatcher, and a
// *Recovery over both.
type recoveryFixture struct {
	t           *testing.T
	arena       *fakeArena
	now         *time.Time
	up          bool
	collector   *Collector
	dispatcher  *ActionDispatcher
	recovery    *Recovery
	settle      time.Duration
	autoEnabled func(ctx context.Context) (bool, error)
	reports     []RestoreReport

	// intercept, when non-nil, is checked before every request reaches
	// arena.ServeHTTP — a test's own escape hatch for a response shape
	// fakeArena/layerJSON cannot produce (e.g. finding 8's absent
	// active_clip key). Returns true when it fully handled the request.
	intercept func(w http.ResponseWriter, r *http.Request) bool
}

func newRecoveryFixture(t *testing.T, settle time.Duration) *recoveryFixture {
	t.Helper()
	now := time.Now()
	arena := newFakeArena(&now)
	arena.layers[testLayerOne] = &faLayer{bypassedParamID: 9001, masterParamID: 9002, master: 1}
	arena.layers[testLayerTwo] = &faLayer{bypassedParamID: 9003, masterParamID: 9004, master: 1}
	arena.clips[testClipA] = &faClip{connected: "Disconnected", ownerLayer: testLayerOne}
	arena.clips[testClipB] = &faClip{connected: "Disconnected", ownerLayer: testLayerTwo}
	arena.clips[testPersistA] = &faClip{connected: "Disconnected", ownerLayer: testLayerOne}

	f := &recoveryFixture{t: t, arena: arena, now: &now, up: true, settle: settle}
	f.autoEnabled = func(context.Context) (bool, error) { return true, nil }

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.intercept != nil && f.intercept(w, r) {
			return
		}
		if r.URL.Path == "/api/v1/product" {
			// Recorded into arena.requests directly (rather than
			// delegating to arena.ServeHTTP, which has no /product case
			// of its own — action_dispatch_test.go's fakeArena never
			// drives Collector.Poll, only ActionDispatcher.Dispatch): the
			// footprint tests below (criteria 8, 17) count THIS list.
			arena.mu.Lock()
			arena.requests = append(arena.requests, r.Method+" "+r.URL.Path)
			up := f.up
			arena.mu.Unlock()
			if !up {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(loadTestdata(t, "product.json"))
			return
		}
		arena.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	store := newTestCompositionStore(t, recoveryTestComposition())

	// recoveryPtr is the same late-binding indirection coordinator.go's own
	// atomic.Pointer[resolume.Recovery] wiring uses (resolumeRecoveryHolder):
	// the Collector's OnUnreachableTransition callback must be supplied at
	// construction, before f.recovery exists to call into. Single-goroutine
	// test code, so a bare closure over this pointer is safe without an
	// atomic — f.recovery is assigned below, before anything calls Poll.
	var recoveryPtr *Recovery
	f.collector = newTestCollector(t, srv.URL, Options{
		Now: fixedClock(&now), CompositionStore: store,
		OnUnreachableTransition: func(at time.Time) {
			if recoveryPtr != nil {
				recoveryPtr.captureCrashTarget(at)
			}
		},
	})
	f.dispatcher = NewActionDispatcher(f.collector, ActionDispatcherOptions{
		Now: fixedClock(&now), Sleep: fakeSleep(&now), PollInterval: 5 * time.Millisecond,
	})
	f.recovery = NewRecovery(f.collector, f.dispatcher, RecoveryOptions{
		Now: fixedClock(&now), Sleep: fakeSleep(&now), Settle: settle,
		AutoRestoreEnabled: func(ctx context.Context) (bool, error) { return f.autoEnabled(ctx) },
		OnRestoreComplete:  func(r RestoreReport) { f.reports = append(f.reports, r) },
	})
	recoveryPtr = f.recovery

	// Establish a real, confirmed identity before any test body runs —
	// every test in this file that dispatches starts from "D-2 already
	// confirmed identity", matching action_dispatch_test.go's own
	// newTestActionDispatcher fixture contract.
	snap := f.collector.SurveyNow(context.Background(), true)
	if snap.Identity != IdentityTrue {
		t.Fatalf("fixture setup: SurveyNow identity = %q, want %q (reason: %s) — the fixture's own composition/arena must resolve", snap.Identity, IdentityTrue, snap.IdentityObservedAt)
	}
	return f
}

// simulateCrash drives a REAL reachable->unreachable->(collector-side)
// transition through f.collector.Poll — the production path that captures
// Recovery's own crash-target snapshot — rather than relying on a live
// re-read. Every test that calls
// HandleReachableTransition to simulate a return and expects a restore
// target must call this first, or [Recovery.takeCrashTarget] has nothing
// captured and the restore is correctly empty. Leaves f.up == true (ready
// for whatever the test does next) and advances *f.now by roughly two poll
// intervals — callers that assert precise elapsed time do so AFTER this
// call, never around it.
func (f *recoveryFixture) simulateCrash(t *testing.T) {
	t.Helper()
	// A down-transition is only detected relative to a PRIOR known-
	// reachable state (finding 6) — establish that first if this is the
	// first Poll call this fixture has ever made. Advancing the clock
	// before EACH Poll call (not only between them) is what keeps a
	// second simulateCrash call due under Poll's own PollInterval
	// throttle, since [Collector.lastLivenessPollAt] persists across
	// calls on the same fixture.
	*f.now = f.now.Add(f.collector.Footprint().PollInterval())
	if _, complete := f.collector.Poll(context.Background()); !complete {
		t.Fatalf("simulateCrash: warm-up Poll() complete = false")
	}
	*f.now = f.now.Add(f.collector.Footprint().PollInterval())
	f.up = false
	if _, complete := f.collector.Poll(context.Background()); !complete {
		t.Fatalf("simulateCrash: crash Poll() complete = false")
	}
	f.up = true
}

// dispatchAndConfirm dispatches name against f's own dispatcher and fails
// the test unless it confirms — the seeding step every test that needs a
// PRE-EXISTING, source=action record entry uses.
func (f *recoveryFixture) dispatchAndConfirm(name ActionName, params ActionParams) ActionOutcome {
	f.t.Helper()
	out, err := f.dispatcher.Dispatch(context.Background(), name, params)
	if err != nil {
		f.t.Fatalf("Dispatch(%s) error = %v", name, err)
	}
	if out.State != ActionConfirmed {
		f.t.Fatalf("Dispatch(%s) State = %q, want %q (reason: %s)", name, out.State, ActionConfirmed, out.Reason)
	}
	return out
}

// requestCount returns how many requests arena has served matching prefix
// (or all, if prefix is "").
func (f *recoveryFixture) requestCount(prefix string) int {
	f.arena.mu.Lock()
	defer f.arena.mu.Unlock()
	if prefix == "" {
		return len(f.arena.requests)
	}
	n := 0
	for _, r := range f.arena.requests {
		if strings.Contains(r, prefix) {
			n++
		}
	}
	return n
}

// findRestoreLayer locates layer's own row in report, failing the test if
// absent.
func findRestoreLayer(t *testing.T, report RestoreReport, layer string) RestoreLayerResult {
	t.Helper()
	for _, l := range report.Layers {
		if l.Layer == layer {
			return l
		}
	}
	t.Fatalf("no layer %q in restore report layers %+v", layer, report.Layers)
	return RestoreLayerResult{}
}

func findRecoveryEntry(t *testing.T, record []RecoveryLayerRecord, layer string) RecoveryLayerRecord {
	t.Helper()
	for _, e := range record {
		if e.Layer == layer {
			return e
		}
	}
	t.Fatalf("no layer %q in recovery record %+v", layer, record)
	return RecoveryLayerRecord{}
}

// --- Criterion 1: Arena gone, reported promptly with reason+provenance --

// TestPollFailureReportsReachableCollectionFailedWithReason is criterion
// 1: this seam adds the operator-visible statement, D-2 already produces
// the detection (TRACK-D-D3A-CRASH-RECOVERY-SPEC.md §5, "Gone"). Breaking:
// production line broken was collector.go's `c.failed(SignalReachable,
// reason, now)` call replaced with `c.measured(SignalReachable, false,
// now)` — confirmed this test goes red (reason becomes empty / state
// becomes "current" rather than "collection_failed"), then restored.
func TestPollFailureReportsReachableCollectionFailedWithReason(t *testing.T) {
	f := newRecoveryFixture(t, 0)
	f.up = false

	obs, complete := f.collector.Poll(context.Background())
	if !complete {
		t.Fatalf("Poll() complete = false, want true")
	}
	reachable := findSignal(t, obs, SignalReachable)
	if reachable.Absence != observation.StateCollectionFailed {
		t.Fatalf("resolume.reachable Absence = %q, want %q", reachable.Absence, observation.StateCollectionFailed)
	}
	if reachable.Reason == "" {
		t.Error("resolume.reachable Reason is empty — a bare false, not a stated reason")
	}
}

// --- Criteria 2, 3, 11, 12: the gate --------------------------------------

// TestHandleReachableTransitionRestoresConfirmedLayer is criterion 2: a
// layer ShowMesh explicitly drove before the crash is playing again after
// the gate opens. Breaking: production line broken was
// Recovery.restoreLayer's `case ActionConfirmed: row.Result =
// RestoreResultRestored` changed to always report skipped — confirmed
// this test goes red, then restored.
func TestHandleReachableTransitionRestoresConfirmedLayer(t *testing.T) {
	f := newRecoveryFixture(t, 0)
	f.dispatchAndConfirm(ActionLaunchClip, ActionParams{ClipID: testClipA})
	f.simulateCrash(t)

	// Simulate the crash-and-return: nothing external to touch here since
	// the fake Arena never actually went anywhere, but layer one's clip
	// is CLEARED to model "Arena comes back with nothing playing" (§2).
	f.arena.clips[testClipA].connected = "Disconnected"
	f.arena.layers[testLayerOne].activeClip = nil

	returnedAt := *f.now
	*f.now = f.now.Add(time.Second)
	f.recovery.HandleReachableTransition(context.Background(), returnedAt)

	if len(f.reports) != 1 {
		t.Fatalf("OnRestoreComplete fired %d times, want 1", len(f.reports))
	}
	report := f.reports[0]
	layerOne := findRestoreLayer(t, report, "Whole House 1")
	if layerOne.Result != RestoreResultRestored {
		t.Fatalf("layer one Result = %q, want %q (reason: %s)", layerOne.Result, RestoreResultRestored, layerOne.Reason)
	}
	if f.arena.layers[testLayerOne].activeClip == nil || *f.arena.layers[testLayerOne].activeClip != testClipA {
		t.Errorf("arena layer one active clip not restored to %v", testClipA)
	}
}

// TestGateRefusesWhenSurveyPredatesReturn is criterion 3's first half:
// no restore write is issued when the confirming survey's own SurveyAt
// does not postdate the observed return. Breaking: removed the `if
// !snap.SurveyAt.After(returnedAt)` gate term entirely — confirmed this
// test goes red (a write would have been issued), then restored.
func TestGateRefusesWhenSurveyPredatesReturn(t *testing.T) {
	f := newRecoveryFixture(t, 0)
	f.dispatchAndConfirm(ActionLaunchClip, ActionParams{ClipID: testClipA})
	f.simulateCrash(t)
	f.arena.clips[testClipA].connected = "Disconnected"
	f.arena.layers[testLayerOne].activeClip = nil
	before := f.requestCount("/connect")

	// returnedAt is AHEAD of the fake clock the settle wait/survey will
	// run on — the survey's own SurveyAt can never postdate it.
	returnedAt := f.now.Add(time.Hour)
	f.recovery.HandleReachableTransition(context.Background(), returnedAt)

	if got := f.requestCount("/connect") - before; got != 0 {
		t.Fatalf("issued %d connect request(s); the gate must not write when the survey predates the return", got)
	}
	if len(f.reports) != 1 || f.reports[0].Outcome != RestoreOutcomeNothingToDo {
		t.Fatalf("reports = %+v, want exactly one nothing_to_do report", f.reports)
	}
}

// TestGateRefusesOnIdentityNotTrue is criterion 3's second half, over the
// three non-IdentityTrue outcomes named individually per build contract
// §2.4 ("write a test per outcome; three separate assertions, because
// 'the wrong composition is loaded' and 'we could not tell' are different
// facts"). describeIdentityGateRefusal is exercised directly (pure
// function) for IdentityFalse/IdentityDeckMismatch, whose HTTP-driven
// setup this fixture does not build; IdentityUnknown is driven end to end
// through the gate itself.
//
// Mutation-tested honestly: removing recovery.go's own `if snap.Identity
// != IdentityTrue` branch does NOT turn unknown_end_to_end red, because
// ActionDispatcher.Dispatch carries its OWN identity gate (§3.6,
// action.go's identityGate) checked against the SAME just-completed
// survey, so the write is still refused one layer down. That redundancy
// is real defense-in-depth, not a wasted check: recovery.go's own gate is
// what TestGateRefusesWhenSurveyPredatesReturn actually isolates — a
// stale-but-still-fresh-enough cached IdentityTrue reading from BEFORE the
// crash would satisfy Dispatch's own MaxIdentityEvidenceAge check while
// still being wrong for THIS return, which only the "survey postdates the
// return" term catches. Recorded here rather than silently claiming this
// subtest proves something it does not.
func TestGateRefusesOnIdentityNotTrue(t *testing.T) {
	t.Run("false", func(t *testing.T) {
		got := describeIdentityGateRefusal(SurveySnapshot{Identity: IdentityFalse})
		if !strings.Contains(got, "wrong composition") {
			t.Errorf("describeIdentityGateRefusal(False) = %q, want it to name the wrong composition", got)
		}
	})
	t.Run("deck_mismatch", func(t *testing.T) {
		got := describeIdentityGateRefusal(SurveySnapshot{Identity: IdentityDeckMismatch})
		if !strings.Contains(got, "deck") {
			t.Errorf("describeIdentityGateRefusal(DeckMismatch) = %q, want it to name the deck change", got)
		}
	})
	t.Run("unknown_end_to_end", func(t *testing.T) {
		f := newRecoveryFixture(t, 0)
		f.dispatchAndConfirm(ActionLaunchClip, ActionParams{ClipID: testClipA})
		f.simulateCrash(t)
		// Layer one goes genuinely dark (§2: "Arena comes back with
		// nothing playing") AND every clip id 404s: the identity sample
		// cannot resolve anything, matching §2's own "Resolume may not
		// have finished loading" post-restart shape. Clearing activeClip
		// too is load-bearing for this test: without it, layer one would
		// already read "playing the recorded clip" and skip for THAT
		// reason regardless of identity, which would let a broken
		// identity gate hide behind a coincidentally-correct skip.
		f.arena.layers[testLayerOne].activeClip = nil
		delete(f.arena.clips, testClipA)
		delete(f.arena.clips, testClipB)
		delete(f.arena.clips, testPersistA)
		before := f.requestCount("/connect")

		returnedAt := *f.now
		*f.now = f.now.Add(time.Second)
		f.recovery.HandleReachableTransition(context.Background(), returnedAt)

		if got := f.requestCount("/connect") - before; got != 0 {
			t.Fatalf("issued %d connect request(s) with identity unknown", got)
		}
		if len(f.reports) != 1 || f.reports[0].Outcome != RestoreOutcomeNothingToDo {
			t.Fatalf("reports = %+v, want exactly one nothing_to_do report", f.reports)
		}
	})
}

// TestGateWaitsSettleDelayBeforeSurveying is criterion 11: no request
// beyond the liveness probe is issued inside the settle delay. Drives the
// gate with a REAL (small, bounded) sleep rather than the fake-clock
// pair, so wall-clock ordering is genuinely observed rather than assumed:
// a background goroutine checks request counts before returning control,
// and the settle wait's own r.sleep is production's real time.Sleep via a
// dedicated fixture. Breaking: removed the `if !r.wait(ctx, r.settle)`
// call entirely (settle skipped) — confirmed the assertion at t=0 failed
// (a request had already been issued), then restored.
func TestGateWaitsSettleDelayBeforeSurveying(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	arena.layers[testLayerOne] = &faLayer{bypassedParamID: 9001, masterParamID: 9002, master: 1}
	arena.clips[testClipA] = &faClip{connected: "Disconnected", ownerLayer: testLayerOne}

	requested := make(chan struct{}, 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/product" {
			requested <- struct{}{}
		}
		arena.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	store := newTestCompositionStore(t, recoveryTestComposition())
	collector := newTestCollector(t, srv.URL, Options{CompositionStore: store}) // real clock
	dispatcher := NewActionDispatcher(collector, ActionDispatcherOptions{})
	settle := 80 * time.Millisecond
	recovery := NewRecovery(collector, dispatcher, RecoveryOptions{Settle: settle})

	go recovery.HandleReachableTransition(context.Background(), time.Now())

	select {
	case <-requested:
		t.Fatal("a by-id request was issued before the settle delay elapsed")
	case <-time.After(settle / 2):
		// Good: nothing issued halfway through the settle window.
	}

	select {
	case <-requested:
		// Good: the survey ran once settle elapsed.
	case <-time.After(2 * time.Second):
		t.Fatal("no request was ever issued after the settle delay — the gate appears to have stalled")
	}
}

// TestSecondCrashInsideThrottleWindowStillRestores is criterion 12: a
// second HandleReachableTransition call inside DefaultTransitionSurveyMinInterval
// still restores, because the gate's own survey bypasses that throttle
// (SurveyNow, never the ordinary Poll-driven trigger). Breaking:
// production line broken was Collector.SurveyNow changed to call
// c.Poll(ctx) instead of c.survey(...) directly (reintroducing the
// throttle) — confirmed the second restore's layer read zero connect
// requests where one was expected, then restored.
func TestSecondCrashInsideThrottleWindowStillRestores(t *testing.T) {
	f := newRecoveryFixture(t, 0)
	f.dispatchAndConfirm(ActionLaunchClip, ActionParams{ClipID: testClipA})
	f.simulateCrash(t)
	f.arena.clips[testClipA].connected = "Disconnected"
	f.arena.layers[testLayerOne].activeClip = nil

	returnedAt1 := *f.now
	*f.now = f.now.Add(time.Second)
	f.recovery.HandleReachableTransition(context.Background(), returnedAt1)
	if len(f.reports) != 1 || f.reports[0].Outcome != RestoreOutcomeRestored {
		t.Fatalf("first restore = %+v, want a single \"restored\" report", f.reports)
	}

	// A second crash 36s later — inside DefaultTransitionSurveyMinInterval
	// (1 minute) — the measured real crash interval this bypass exists
	// for. The first restore's own confirmed launchClip already
	// re-established layer one's action-sourced entry, so this crash's
	// own capture reflects THAT — the same real sequence this restore's
	// own applyConfirmedActionToRecoveryRecord hook runs in production.
	f.simulateCrash(t)
	f.arena.clips[testClipA].connected = "Disconnected"
	f.arena.layers[testLayerOne].activeClip = nil
	*f.now = f.now.Add(36 * time.Second)
	returnedAt2 := *f.now
	*f.now = f.now.Add(time.Second)
	f.recovery.HandleReachableTransition(context.Background(), returnedAt2)

	if len(f.reports) != 2 {
		t.Fatalf("reports = %+v, want 2 (the second crash's own restore must not be dropped by the throttle)", f.reports)
	}
	if f.reports[1].Outcome != RestoreOutcomeRestored {
		t.Fatalf("second restore outcome = %q, want %q", f.reports[1].Outcome, RestoreOutcomeRestored)
	}
}

// --- Criteria 4, 5, 6: the restore's own skip rules -----------------------

// TestRestoreLeavesLayerAlreadyRelaunchedByHand is criterion 4: a layer
// the operator has already relaunched by hand (a DIFFERENT clip than
// recorded) is left alone and reported. Breaking: production line broken
// was the `case !layerActiveClipAbsent(live):` branch in
// Recovery.restoreLayer deleted, falling through to dispatch — confirmed
// this test goes red (a connect request was issued for the recorded
// clip), then restored.
func TestRestoreLeavesLayerAlreadyRelaunchedByHand(t *testing.T) {
	f := newRecoveryFixture(t, 0)
	f.dispatchAndConfirm(ActionLaunchClip, ActionParams{ClipID: testClipA})

	// The operator has already launched clip B on layer one by hand.
	f.arena.clips[testClipA].connected = "Disconnected"
	f.arena.clips[testClipB].connected = "Connected"
	f.arena.layers[testLayerOne].activeClip = idPtr(testClipB)
	before := f.requestCount("clips/by-id/" + testClipA.String() + "/connect")

	report := f.recovery.RunManualRestore(context.Background())
	layerOne := findRestoreLayer(t, report, "Whole House 1")
	if layerOne.Result != RestoreResultSkipped {
		t.Fatalf("layer one Result = %q, want %q", layerOne.Result, RestoreResultSkipped)
	}
	if layerOne.Reason == "" {
		t.Error("skipped layer has no reason")
	}
	connectA := f.requestCount("clips/by-id/"+testClipA.String()+"/connect") - before
	if connectA != 0 {
		t.Errorf("issued %d connect request(s) for clip A; the operator's own choice must not be overwritten", connectA)
	}
}

// TestRecoveryRecordReportsNeverObservedForUnestablishedLayer is criterion
// 5: a layer with no record entry at all is reported unknown, never
// restored and never dark. Deliberately built WITHOUT going through
// newRecoveryFixture: that helper's own SurveyNow call at construction
// gives every layer a source=survey entry immediately (itself reclassified
// to unknown by criterion 14's own rule), which would make the "!known"
// branch this test targets unreachable — proven by mutating it and
// watching a fixture-based version of this test stay green. This
// Collector never surveys at all, so RecoveryRecord's "no entry" branch is
// the ONLY branch that can produce an entry for either layer. Breaking:
// production line broken was buildRecoveryLayerRecord's `if !known`
// branch changed to `rec.State = RecoveryLayerDark` — confirmed this test
// goes red, then restored.
func TestRecoveryRecordReportsNeverObservedForUnestablishedLayer(t *testing.T) {
	arena := newFakeArena(new(time.Time))
	srv := httptest.NewServer(arena)
	t.Cleanup(srv.Close)

	store := newTestCompositionStore(t, recoveryTestComposition())
	collector := newTestCollector(t, srv.URL, Options{CompositionStore: store})
	dispatcher := NewActionDispatcher(collector, ActionDispatcherOptions{})
	recovery := NewRecovery(collector, dispatcher, RecoveryOptions{})

	record := collector.RecoveryRecord()
	if len(record) != 2 {
		t.Fatalf("RecoveryRecord() len = %d, want 2 (this test's own composition has two layers)", len(record))
	}
	layerTwo := findRecoveryEntry(t, record, "Whole House 2")
	if layerTwo.State != RecoveryLayerUnknown {
		t.Fatalf("layer two State = %q, want %q", layerTwo.State, RecoveryLayerUnknown)
	}
	if layerTwo.Reason == "" {
		t.Error("unknown entry has no reason")
	}
	if !layerTwo.EstablishedAt.IsZero() {
		t.Errorf("EstablishedAt = %v, want zero — nothing has ever been established for this layer", layerTwo.EstablishedAt)
	}

	report := recovery.RunManualRestore(context.Background())
	layerTwoRestore := findRestoreLayer(t, report, "Whole House 2")
	if layerTwoRestore.Result != RestoreResultSkipped {
		t.Fatalf("layer two restore Result = %q, want %q (never restored on unknown evidence)", layerTwoRestore.Result, RestoreResultSkipped)
	}
}

// TestRestoreReportsPartialWithReasonEveryTimeSomethingIsSkipped is
// criterion 6: a partial restore reports as partial, per layer, with a
// reason on every skip. Layer one is confirmed and genuinely dark on
// return (restored); layer two is left "already playing something else"
// (skipped). Breaking: production line broken was restore()'s outcome
// computation changed to report "restored" whenever restoredCount > 0
// regardless of skips — confirmed this test goes red (Outcome ==
// "restored"), then restored.
func TestRestoreReportsPartialWithReasonEveryTimeSomethingIsSkipped(t *testing.T) {
	f := newRecoveryFixture(t, 0)
	f.dispatchAndConfirm(ActionLaunchClip, ActionParams{ClipID: testClipA})
	f.dispatchAndConfirm(ActionLaunchClip, ActionParams{ClipID: testClipB})

	// Layer one goes dark (eligible to restore); layer two is already
	// playing something else by the time restore runs — but there is
	// only one clip per layer in this fixture, so "something else" here
	// means still B, which is already-satisfied (skip: already playing).
	f.arena.clips[testClipA].connected = "Disconnected"
	f.arena.layers[testLayerOne].activeClip = nil

	report := f.recovery.RunManualRestore(context.Background())
	if report.Outcome != RestoreOutcomePartial {
		t.Fatalf("Outcome = %q, want %q", report.Outcome, RestoreOutcomePartial)
	}
	layerOne := findRestoreLayer(t, report, "Whole House 1")
	if layerOne.Result != RestoreResultRestored {
		t.Fatalf("layer one Result = %q, want %q", layerOne.Result, RestoreResultRestored)
	}
	if layerOne.Reason != "" {
		t.Errorf("restored layer has a reason %q, want none", layerOne.Reason)
	}
	layerTwo := findRestoreLayer(t, report, "Whole House 2")
	if layerTwo.Result != RestoreResultSkipped || layerTwo.Reason == "" {
		t.Fatalf("layer two = %+v, want skipped with a reason", layerTwo)
	}
}

// --- Criterion 8: steady-state traffic is unchanged -----------------------

// TestSteadyStatePollTrafficIsOneProductRequestPerCycle is criterion 8:
// wiring a Recovery in does not add to the ordinary liveness poll's own
// footprint — SurveyNow/recoveryUpdateFromSurvey are only ever invoked
// from RunManualRestore/HandleReachableTransition, never from Poll's own
// steady-state path. Breaking: production line broken was Poll's own
// steady-state branch changed to call c.recoveryUpdateFromSurvey
// unconditionally — confirmed a request count assertion here failed
// (extra by-id requests appeared where none should), then restored: no
// code path in Poll itself was actually touched, so this test's job is
// proving Poll's own request count with a Recovery wired stays 1
// GET /product, matching the pre-existing footprint tests exactly.
func TestSteadyStatePollTrafficIsOneProductRequestPerCycle(t *testing.T) {
	f := newRecoveryFixture(t, 0)
	f.collector.footprint.SetPollInterval(time.Millisecond)

	// The very first Poll call ever on this Collector is itself a
	// transition (livenessKnown starts false) and legitimately runs one
	// survey alongside it — consumed here so the assertion below measures
	// genuine STEADY-STATE traffic, the second and any later cycle.
	*f.now = f.now.Add(time.Second)
	if _, complete := f.collector.Poll(context.Background()); !complete {
		t.Fatalf("warm-up Poll() complete = false")
	}

	before := f.requestCount("")
	*f.now = f.now.Add(time.Second)
	_, complete := f.collector.Poll(context.Background())
	if !complete {
		t.Fatalf("Poll() complete = false")
	}
	if got := f.requestCount("") - before; got != 1 {
		t.Fatalf("Poll served %d request(s) for one ordinary cycle, want exactly 1 (GET /product)", got)
	}
}

// --- Criterion 10: toggle off issues no write ------------------------------

// TestAutomaticRestoreWithToggleOffIssuesNoWriteButStillReports is
// criterion 10: with the toggle off, a crash and return produce the
// report and no write. Breaking: production line broken was
// restoreLayer's `if trigger == RestoreTriggerAutomatic && !autoEnabled`
// branch deleted — confirmed this test goes red (a connect request was
// issued while the toggle read false), then restored.
func TestAutomaticRestoreWithToggleOffIssuesNoWriteButStillReports(t *testing.T) {
	f := newRecoveryFixture(t, 0)
	f.dispatchAndConfirm(ActionLaunchClip, ActionParams{ClipID: testClipA})
	f.simulateCrash(t)
	f.arena.clips[testClipA].connected = "Disconnected"
	f.arena.layers[testLayerOne].activeClip = nil
	f.autoEnabled = func(context.Context) (bool, error) { return false, nil }
	before := f.requestCount("/connect")

	returnedAt := *f.now
	*f.now = f.now.Add(time.Second)
	f.recovery.HandleReachableTransition(context.Background(), returnedAt)

	if got := f.requestCount("/connect") - before; got != 0 {
		t.Fatalf("issued %d connect request(s) with the toggle off", got)
	}
	if len(f.reports) != 1 {
		t.Fatalf("reports = %+v, want exactly one report even with the toggle off", f.reports)
	}
	layerOne := findRestoreLayer(t, f.reports[0], "Whole House 1")
	if layerOne.Result != RestoreResultSkipped || !strings.Contains(layerOne.Reason, "disabled") {
		t.Fatalf("layer one = %+v, want skipped naming the toggle as disabled", layerOne)
	}
}

// --- Criterion 14: stale survey-only evidence ------------------------------

// TestRecoveryRecordReclassifiesSurveyEvidenceAsNeverObserved is criterion
// 14: a layer whose only evidence is a survey reports never observed,
// with the age and the source stated — never dark, even when the survey
// itself read the layer as dark. Breaking: production line broken was
// buildRecoveryLayerRecord's `if entry.source == RecoverySourceSurvey`
// branch deleted, so a survey-sourced dark reading rendered as State ==
// RecoveryLayerDark — confirmed this test goes red, then restored.
func TestRecoveryRecordReclassifiesSurveyEvidenceAsNeverObserved(t *testing.T) {
	f := newRecoveryFixture(t, 0)
	f.collector.SurveyNow(context.Background(), true)

	record := f.collector.RecoveryRecord()
	layerOne := findRecoveryEntry(t, record, "Whole House 1")
	if layerOne.State != RecoveryLayerUnknown {
		t.Fatalf("State = %q, want %q — a survey-sourced reading must never report as dark or clip", layerOne.State, RecoveryLayerUnknown)
	}
	if !strings.Contains(layerOne.Reason, "survey") {
		t.Errorf("Reason = %q, want it to state the source (survey)", layerOne.Reason)
	}
	if layerOne.EstablishedAt.IsZero() {
		t.Error("EstablishedAt is zero — the age this reason states must be traceable to a real timestamp")
	}
}

// --- Criteria 15, 16, 17: deck-change evidence -----------------------------

// TestDispatchLaunchClipReportsSelectedDeckChangedTrue is criterion 15: a
// launchClip confirmed after the selected deck changed reports
// selectedDeckChanged true and still reports its real action outcome.
// Breaking: production line broken was dispatchLaunchClip's own
// deckChangedSinceDecision call deleted — confirmed this test's non-nil
// assertion failed, then restored.
func TestDispatchLaunchClipReportsSelectedDeckChangedTrue(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	arena.layers[testLayerOne] = &faLayer{bypassedParamID: 9001, masterParamID: 9002, master: 1}
	arena.clips[testClipA] = &faClip{connected: "Disconnected", ownerLayer: testLayerOne}
	arena.decks[testDeckTwo] = &faDeck{selected: false, name: "Deck Two"}

	// A custom handler flips the selected deck away from testDeckOne the
	// instant the clip connect POST lands — simulating the operator
	// switching decks between decision and confirmation.
	deckFlip := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/connect") {
			arena.mu.Lock()
			arena.decks[testDeckOne].selected = false
			arena.decks[testDeckTwo].selected = true
			arena.mu.Unlock()
		}
		arena.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(deckFlip)
	t.Cleanup(srv.Close)

	store := newTestCompositionStore(t, recoveryTestComposition())
	c := newTestCollector(t, srv.URL, Options{Now: fixedClock(&now), CompositionStore: store})
	c.recordSurveySnapshot(identifiedSnapshotFor(now, testDeckOne))
	d := NewActionDispatcher(c, ActionDispatcherOptions{Now: fixedClock(&now), Sleep: fakeSleep(&now), PollInterval: 5 * time.Millisecond})

	out, err := d.Dispatch(context.Background(), ActionLaunchClip, ActionParams{ClipID: testClipA})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State != ActionConfirmed {
		t.Fatalf("State = %q, want %q (reason: %s)", out.State, ActionConfirmed, out.Reason)
	}
	if out.SelectedDeckChanged == nil {
		t.Fatal("SelectedDeckChanged is nil, want a non-nil true")
	}
	if !*out.SelectedDeckChanged {
		t.Error("SelectedDeckChanged = false, want true — the deck changed after dispatch")
	}
}

// TestDispatchLaunchClipReportsSelectedDeckChangedNilOnUnreadableDeck is
// criterion 16: a launchClip whose confirmation-time deck read fails
// reports selectedDeckChanged null, never false. Breaking: production
// line broken was deckChangedSinceDecision's `if err != nil { return nil
// }` changed to `return boolPtr(false)` — confirmed this test goes red,
// then restored.
func TestDispatchLaunchClipReportsSelectedDeckChangedNilOnUnreadableDeck(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	arena.layers[testLayerOne] = &faLayer{bypassedParamID: 9001, masterParamID: 9002, master: 1}
	arena.clips[testClipA] = &faClip{connected: "Disconnected", ownerLayer: testLayerOne}

	// The deck object disappears the instant the connect lands — the
	// confirmation-time re-read of it then 404s.
	deckVanishes := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/connect") {
			arena.mu.Lock()
			delete(arena.decks, testDeckOne)
			arena.mu.Unlock()
		}
		arena.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(deckVanishes)
	t.Cleanup(srv.Close)

	store := newTestCompositionStore(t, recoveryTestComposition())
	c := newTestCollector(t, srv.URL, Options{Now: fixedClock(&now), CompositionStore: store})
	c.recordSurveySnapshot(identifiedSnapshotFor(now, testDeckOne))
	d := NewActionDispatcher(c, ActionDispatcherOptions{Now: fixedClock(&now), Sleep: fakeSleep(&now), PollInterval: 5 * time.Millisecond})

	out, err := d.Dispatch(context.Background(), ActionLaunchClip, ActionParams{ClipID: testClipA})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State != ActionConfirmed {
		t.Fatalf("State = %q, want %q (reason: %s)", out.State, ActionConfirmed, out.Reason)
	}
	if out.SelectedDeckChanged != nil {
		t.Errorf("SelectedDeckChanged = %v, want nil (never false) when the confirmation-time deck read fails", *out.SelectedDeckChanged)
	}
}

// TestDeckChangeEvidenceDoesNotAffectIdleTraffic is criterion 17: idle
// traffic is still exactly one GET /product per interval with a
// launchClip driven through the fake — the deck-change read costs one
// extra by-id read on the ACTION path, never the timer.
func TestDeckChangeEvidenceDoesNotAffectIdleTraffic(t *testing.T) {
	f := newRecoveryFixture(t, 0)
	f.collector.footprint.SetPollInterval(time.Millisecond)

	// Consume the first-ever-Poll transition survey before measuring —
	// see TestSteadyStatePollTrafficIsOneProductRequestPerCycle's
	// identical note.
	*f.now = f.now.Add(time.Second)
	if _, complete := f.collector.Poll(context.Background()); !complete {
		t.Fatalf("warm-up Poll() complete = false")
	}

	f.dispatchAndConfirm(ActionLaunchClip, ActionParams{ClipID: testClipA})

	before := f.requestCount("")
	*f.now = f.now.Add(time.Second)
	_, complete := f.collector.Poll(context.Background())
	if !complete {
		t.Fatalf("Poll() complete = false")
	}
	after := f.requestCount("")
	if after-before != 1 {
		t.Fatalf("Poll after a confirmed launchClip issued %d request(s), want exactly 1 (GET /product) — "+
			"the deck-change read must never appear on the timer", after-before)
	}
}

// identifiedSnapshotFor mirrors action_dispatch_test.go's identifiedSnapshot,
// parameterized on the selected deck for tests that need a deck other
// than testDeckOne.
func identifiedSnapshotFor(now time.Time, deck ObjectID) SurveySnapshot {
	return SurveySnapshot{
		SurveyRan: true, SurveyAt: now,
		IdentityKnown: true, Identity: IdentityTrue, IdentityObservedAt: now,
		SelectedDeckKnown: true, SelectedDeckID: deck, SelectedDeckName: "Deck One", SelectedDeckObservedAt: now,
	}
}

// --- Review 2026-08-16 findings ------------------------------------------

// TestCrashRecoverySequenceEndToEnd is the realistic sequence: dispatch and
// confirm, a survey between the confirmed action and the crash, a real
// crash (down transition), the gate's own confirming survey between the
// crash and the return, then the return — proving the restore target
// survives both surveys intact. Breaking: HandleReachableTransition's
// target reverted to a live r.collector.RecoveryRecord() read instead of
// r.takeCrashTarget() — confirmed this test goes red (layer one was never
// restored, since the intervening surveys had already reclassified the
// live record to unknown by the time the return ran), then restored.
func TestCrashRecoverySequenceEndToEnd(t *testing.T) {
	f := newRecoveryFixture(t, 0)
	f.dispatchAndConfirm(ActionLaunchClip, ActionParams{ClipID: testClipA})

	// A survey between the confirmed action and the crash — Arena still
	// genuinely shows clip A, so this survey AGREES with the action-sourced
	// entry and must not destroy it.
	f.collector.SurveyNow(context.Background(), false)

	f.simulateCrash(t)

	// Arena comes back with nothing playing (§2), and a survey races
	// AHEAD of the gate itself — modeling the WebSocket reconnect
	// resolving before the gate's own goroutine gets scheduled (this
	// file's own top comment). This survey DISAGREES with the
	// action-sourced entry (still standing from before the crash) and
	// overwrites the LIVE record — the exact corruption a live re-read at
	// gate entry would see, and the exact thing the crash-time snapshot
	// must be immune to.
	f.arena.clips[testClipA].connected = "Disconnected"
	f.arena.layers[testLayerOne].activeClip = nil
	f.collector.SurveyNow(context.Background(), true)

	returnedAt := *f.now
	*f.now = f.now.Add(time.Second)
	f.recovery.HandleReachableTransition(context.Background(), returnedAt)

	if len(f.reports) != 1 {
		t.Fatalf("reports = %+v, want exactly one", f.reports)
	}
	report := f.reports[0]
	if report.Outcome != RestoreOutcomeRestored {
		t.Fatalf("Outcome = %q, want %q", report.Outcome, RestoreOutcomeRestored)
	}
	layerOne := findRestoreLayer(t, report, "Whole House 1")
	if layerOne.Result != RestoreResultRestored {
		t.Fatalf("layer one Result = %q, want %q (reason: %s)", layerOne.Result, RestoreResultRestored, layerOne.Reason)
	}
	if f.arena.layers[testLayerOne].activeClip == nil || *f.arena.layers[testLayerOne].activeClip != testClipA {
		t.Errorf("arena layer one active clip not restored to %v", testClipA)
	}
}

// TestSurveyAgreeingWithActionEntryKeepsActionProvenance is finding 1(b): a
// survey that reads the same value an action already established keeps
// that entry action-sourced, so RecoveryRecord's own "survey evidence is
// never a restore target" rule (criterion 14) does not discard it.
// Breaking: recoverySurveyAgreesWithAction changed to always return false —
// confirmed this test goes red (State became unknown instead of clip), then
// restored.
func TestSurveyAgreeingWithActionEntryKeepsActionProvenance(t *testing.T) {
	f := newRecoveryFixture(t, 0)
	f.dispatchAndConfirm(ActionLaunchClip, ActionParams{ClipID: testClipA})

	// Arena still genuinely shows clip A — a survey here should AGREE.
	f.collector.SurveyNow(context.Background(), false)

	record := f.collector.RecoveryRecord()
	layerOne := findRecoveryEntry(t, record, "Whole House 1")
	if layerOne.State != RecoveryLayerClip {
		t.Fatalf("State = %q, want %q — an agreeing survey must not discard the action-sourced entry", layerOne.State, RecoveryLayerClip)
	}
	if layerOne.Source != RecoverySourceAction {
		t.Fatalf("Source = %q, want %q — provenance must stay action-sourced when the survey agrees", layerOne.Source, RecoverySourceAction)
	}
	if layerOne.ClipID != testClipA {
		t.Errorf("ClipID = %v, want %v", layerOne.ClipID, testClipA)
	}
}

// TestSurveyDisagreeingWithActionEntryOverwritesIt is finding 1(b)'s other
// half: a survey that finds something DIFFERENT than what an action
// established replaces the entry, becoming survey-sourced — something else
// changed it. Breaking: recoverySurveyAgreesWithAction changed to always
// return true (never overwrite) — confirmed this test goes red (the entry
// stayed action-sourced despite disagreeing), then restored.
func TestSurveyDisagreeingWithActionEntryOverwritesIt(t *testing.T) {
	f := newRecoveryFixture(t, 0)
	f.dispatchAndConfirm(ActionLaunchClip, ActionParams{ClipID: testClipA})

	// The operator relaunches clip B on layer one by hand before any
	// survey runs.
	f.arena.clips[testClipA].connected = "Disconnected"
	f.arena.clips[testClipB].connected = "Connected"
	f.arena.layers[testLayerOne].activeClip = idPtr(testClipB)

	f.collector.SurveyNow(context.Background(), false)

	record := f.collector.RecoveryRecord()
	layerOne := findRecoveryEntry(t, record, "Whole House 1")
	// Criterion 14 always renders State as unknown for a survey-sourced
	// entry, so the moved provenance is checked via Source instead.
	if layerOne.Source != RecoverySourceSurvey {
		t.Fatalf("Source = %q, want %q — a disagreeing survey must overwrite the action-sourced entry", layerOne.Source, RecoverySourceSurvey)
	}
}

// TestRestoreAllUsableLayersRefusedIsNeverNothingToDo is finding 2: when
// every usable-target layer is refused rather than genuinely absent, the
// outcome must never read as nothing_to_do — a refusal means a layer HAD a
// usable entry (build contract §1.4). Breaking: restore()'s outcome switch
// changed its usableCount==0 case to the pre-fix attemptedCount==0 —
// confirmed this test goes red (Outcome became nothing_to_do), then
// restored.
func TestRestoreAllUsableLayersRefusedIsNeverNothingToDo(t *testing.T) {
	f := newRecoveryFixture(t, 0)
	f.dispatchAndConfirm(ActionLaunchClip, ActionParams{ClipID: testClipA})
	f.dispatchAndConfirm(ActionLaunchClip, ActionParams{ClipID: testClipB})

	// Both layers go dark on return, but the selected deck changes — the
	// clip's own deck read at restore time refuses both via the deck term.
	f.arena.clips[testClipA].connected = "Disconnected"
	f.arena.layers[testLayerOne].activeClip = nil
	f.arena.clips[testClipB].connected = "Disconnected"
	f.arena.layers[testLayerTwo].activeClip = nil
	f.arena.decks[testDeckOne].selected = false

	report := f.recovery.RunManualRestore(context.Background())
	if report.Outcome != RestoreOutcomeFailed {
		t.Fatalf("Outcome = %q, want %q — every usable layer was refused, not absent", report.Outcome, RestoreOutcomeFailed)
	}
	layerOne := findRestoreLayer(t, report, "Whole House 1")
	if layerOne.Result != RestoreResultSkipped || layerOne.Reason == "" {
		t.Fatalf("layer one = %+v, want skipped with a reason (refused)", layerOne)
	}
}

// TestFirstEverPollNeverFiresTheGate is finding 6: a coordinator started
// while Resolume is already reachable must not report a phantom automatic
// restore for a crash that never happened. Breaking:
// noteLivenessAndCheckTransition's gateTransition condition weakened back
// to isTransition's own test (!wasKnown || !wasReachable) — confirmed this
// test goes red (the gate fired on the very first poll), then restored.
func TestFirstEverPollNeverFiresTheGate(t *testing.T) {
	var gateFired bool
	now := time.Now()
	arena := newFakeArena(&now)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/product" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(loadTestdata(t, "product.json"))
			return
		}
		arena.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	store := newTestCompositionStore(t, recoveryTestComposition())
	collector := newTestCollector(t, srv.URL, Options{
		Now: fixedClock(&now), CompositionStore: store,
		OnReachableTransition: func(time.Time) { gateFired = true },
	})

	if _, complete := collector.Poll(context.Background()); !complete {
		t.Fatalf("Poll() complete = false")
	}
	// OnReachableTransition is spawned in its own goroutine — give it a
	// moment before asserting a negative.
	time.Sleep(50 * time.Millisecond)
	if gateFired {
		t.Error("the gate fired on this Collector's very first liveness result — a coordinator start is not a return")
	}
}

// TestRestoreAbandonsRemainingLayersOnceSuperseded is finding 7:
// supersession is checked BETWEEN layers, not only at the gate's own
// checkpoints before restore() is called — a restore can run for minutes,
// so without a per-layer check a second gate could start a second restore
// while this one is still mid-flight. Breaking: removed the `if superseded
// != nil && superseded()` check inside restore()'s per-layer loop —
// confirmed this test goes red (both layers were processed instead of only
// the first), then restored.
func TestRestoreAbandonsRemainingLayersOnceSuperseded(t *testing.T) {
	f := newRecoveryFixture(t, 0)
	f.dispatchAndConfirm(ActionLaunchClip, ActionParams{ClipID: testClipA})
	f.dispatchAndConfirm(ActionLaunchClip, ActionParams{ClipID: testClipB})
	f.arena.clips[testClipA].connected = "Disconnected"
	f.arena.layers[testLayerOne].activeClip = nil
	f.arena.clips[testClipB].connected = "Disconnected"
	f.arena.layers[testLayerTwo].activeClip = nil

	target := f.collector.RecoveryRecord()
	calls := 0
	superseded := func() bool {
		calls++
		return calls > 1 // superseded starting from the second layer checked
	}
	report := f.recovery.restore(context.Background(), target, RestoreTriggerManual, *f.now, superseded)

	layerOne := findRestoreLayer(t, report, "Whole House 1")
	if layerOne.Result != RestoreResultRestored {
		t.Fatalf("layer one Result = %q, want %q — the first layer must still complete before supersession is noticed", layerOne.Result, RestoreResultRestored)
	}
	layerTwo := findRestoreLayer(t, report, "Whole House 2")
	if layerTwo.Result != RestoreResultSkipped || !strings.Contains(layerTwo.Reason, "superseded") {
		t.Fatalf("layer two = %+v, want skipped naming supersession", layerTwo)
	}
	if f.arena.layers[testLayerTwo].activeClip != nil {
		t.Errorf("layer two was dispatched despite supersession")
	}
}

// TestRestoreDistinguishesUnreadableFromDifferentClip is finding 8: a
// layer whose active_clip key is ABSENT from Resolume's response is
// reported as unreadable, never as "playing a different clip" — opposite
// facts an operator would act on oppositely. Breaking: restoreLayer's
// switch on live.ActiveClip.Presence collapsed back to the single
// `!layerActiveClipAbsent(live)` branch — confirmed this test goes red (the
// unreadable case claimed a different clip was playing), then restored.
func TestRestoreDistinguishesUnreadableFromDifferentClip(t *testing.T) {
	f := newRecoveryFixture(t, 0)
	f.dispatchAndConfirm(ActionLaunchClip, ActionParams{ClipID: testClipA})

	// The pre-restore read of layer one answers with active_clip missing
	// entirely — never produced by faLayer/layerJSON, so a hand-crafted
	// response is used here.
	f.intercept = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "layers/by-id/"+testLayerOne.String()) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":1,"bypassed":{"id":9001,"value":false},"master":{"id":9002,"value":1},"transition":null}`))
			return true
		}
		return false
	}

	report := f.recovery.RunManualRestore(context.Background())
	layerOne := findRestoreLayer(t, report, "Whole House 1")
	if layerOne.Result != RestoreResultSkipped {
		t.Fatalf("layer one Result = %q, want %q", layerOne.Result, RestoreResultSkipped)
	}
	if strings.Contains(layerOne.Reason, "different clip") {
		t.Errorf("layer one Reason = %q, must not claim a different clip is playing when the field was merely absent", layerOne.Reason)
	}
	if !strings.Contains(layerOne.Reason, "absent") {
		t.Errorf("layer one Reason = %q, want it to say the field was absent", layerOne.Reason)
	}
}

// TestRecoveryRecordStatesCompositionStoreErrorWithReason is finding 9: a
// composition-store error (nothing ever uploaded) renders as a stated row
// with a reason, never a bare empty list indistinguishable from "uploaded
// with zero layers." Breaking: RecoveryRecord's composition-error branch
// changed to `return nil` — confirmed this test goes red (the record was
// empty with no reason), then restored.
func TestRecoveryRecordStatesCompositionStoreErrorWithReason(t *testing.T) {
	arena := newFakeArena(new(time.Time))
	srv := httptest.NewServer(arena)
	t.Cleanup(srv.Close)

	// No CompositionStore option supplied — it defaults to a fresh,
	// never-refreshed *CompositionStore, whose Current() always answers
	// ErrCompositionNotUploaded.
	collector := newTestCollector(t, srv.URL, Options{})

	record := collector.RecoveryRecord()
	if len(record) != 1 {
		t.Fatalf("RecoveryRecord() len = %d, want 1 (a stated row, not a bare empty list)", len(record))
	}
	if record[0].Reason == "" {
		t.Error("the composition-store-error row has no reason")
	}
	if record[0].State != RecoveryLayerUnknown {
		t.Errorf("State = %q, want %q", record[0].State, RecoveryLayerUnknown)
	}
}

// TestRecoveryRecordSurveySourcedReasonDoesNotTickWithClock is finding 4:
// the survey-sourced "never observed" reason must not embed a precomputed
// age, or this resource would re-broadcast on the change stream on every
// render tick for as long as any layer carries one — EstablishedAt is
// already on the wire; a client computes its own age. Breaking:
// buildRecoveryLayerRecord's survey-sourced Reason changed back to embed
// now.Sub(entry.establishedAt) — confirmed this test goes red (the two
// reads produced different Reason strings), then restored.
func TestRecoveryRecordSurveySourcedReasonDoesNotTickWithClock(t *testing.T) {
	f := newRecoveryFixture(t, 0)
	f.collector.SurveyNow(context.Background(), true)

	first := findRecoveryEntry(t, f.collector.RecoveryRecord(), "Whole House 1")
	*f.now = f.now.Add(5 * time.Second)
	second := findRecoveryEntry(t, f.collector.RecoveryRecord(), "Whole House 1")

	if first.Reason != second.Reason {
		t.Fatalf("Reason changed as the clock advanced with nothing else different:\n  first:  %q\n  second: %q", first.Reason, second.Reason)
	}
}

// TestRestoreSkipReasonsCoverAllFiveSectionSixRules is contract §1.4: every
// one of §6's five skip rules has its own distinguishable sentence, tested
// together so a future edit that collapses two of them back into one
// generic reason is caught. Breaking (rule 5 specifically): restoreLayer's
// ActionRefused branch reverted to `row.Reason = outcome.Reason` alone
// (dropping the restore's own prefix) — confirmed the deck_not_selected
// subtest goes red (Reason no longer named the restore's own deck rule),
// then restored.
func TestRestoreSkipReasonsCoverAllFiveSectionSixRules(t *testing.T) {
	t.Run("dark", func(t *testing.T) {
		f := newRecoveryFixture(t, 0)
		f.dispatchAndConfirm(ActionLaunchClip, ActionParams{ClipID: testClipA})
		f.dispatchAndConfirm(ActionClearLayer, ActionParams{LayerID: testLayerOne})
		report := f.recovery.RunManualRestore(context.Background())
		layer := findRestoreLayer(t, report, "Whole House 1")
		if layer.Result != RestoreResultSkipped || !strings.Contains(layer.Reason, "recorded dark") {
			t.Fatalf("layer = %+v, want skipped naming the dark rule", layer)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		// Deliberately NOT newRecoveryFixture: its own SurveyNow warm-up
		// call establishes a survey-sourced entry for every layer
		// immediately, which criterion 14 reclassifies to unknown but
		// with THAT reason, not the "never established" one this subtest
		// needs — see TestRecoveryRecordReportsNeverObservedForUnestablishedLayer's
		// identical note.
		arena := newFakeArena(new(time.Time))
		srv := httptest.NewServer(arena)
		t.Cleanup(srv.Close)
		store := newTestCompositionStore(t, recoveryTestComposition())
		collector := newTestCollector(t, srv.URL, Options{CompositionStore: store})
		dispatcher := NewActionDispatcher(collector, ActionDispatcherOptions{})
		recovery := NewRecovery(collector, dispatcher, RecoveryOptions{})

		report := recovery.RunManualRestore(context.Background())
		layer := findRestoreLayer(t, report, "Whole House 1")
		if layer.Result != RestoreResultSkipped || !strings.Contains(layer.Reason, "ever been established") {
			t.Fatalf("layer = %+v, want skipped naming the unknown rule", layer)
		}
	})
	t.Run("already_playing", func(t *testing.T) {
		f := newRecoveryFixture(t, 0)
		f.dispatchAndConfirm(ActionLaunchClip, ActionParams{ClipID: testClipA})
		report := f.recovery.RunManualRestore(context.Background())
		layer := findRestoreLayer(t, report, "Whole House 1")
		if layer.Result != RestoreResultSkipped || !strings.Contains(layer.Reason, "already playing the recorded clip") {
			t.Fatalf("layer = %+v, want skipped naming the already-playing rule", layer)
		}
	})
	t.Run("playing_something_else", func(t *testing.T) {
		f := newRecoveryFixture(t, 0)
		f.dispatchAndConfirm(ActionLaunchClip, ActionParams{ClipID: testClipA})
		f.arena.clips[testClipA].connected = "Disconnected"
		f.arena.clips[testClipB].connected = "Connected"
		f.arena.layers[testLayerOne].activeClip = idPtr(testClipB)
		report := f.recovery.RunManualRestore(context.Background())
		layer := findRestoreLayer(t, report, "Whole House 1")
		if layer.Result != RestoreResultSkipped || !strings.Contains(layer.Reason, "different clip") {
			t.Fatalf("layer = %+v, want skipped naming the different-clip rule", layer)
		}
	})
	t.Run("deck_not_selected", func(t *testing.T) {
		f := newRecoveryFixture(t, 0)
		f.dispatchAndConfirm(ActionLaunchClip, ActionParams{ClipID: testClipA})
		f.arena.clips[testClipA].connected = "Disconnected"
		f.arena.layers[testLayerOne].activeClip = nil
		f.arena.decks[testDeckOne].selected = false
		report := f.recovery.RunManualRestore(context.Background())
		layer := findRestoreLayer(t, report, "Whole House 1")
		if layer.Result != RestoreResultSkipped || !strings.Contains(layer.Reason, "never selects a deck") {
			t.Fatalf("layer = %+v, want skipped naming the restore's own deck rule", layer)
		}
	})
}

// TestGateRefusesOnIdentityUnknownReportsTheGateOwnSentence strengthens
// TestGateRefusesOnIdentityNotTrue's unknown_end_to_end subtest per finding
// 11: recovery.go's own identity check was previously proven only by
// connect-request-count and Outcome, both of which ActionDispatcher's own
// redundant identity gate (one layer down) satisfies identically — so a
// mutation to recovery.go's OWN check alone did not turn that subtest red.
// This asserts the report's own layer rows carry
// describeIdentityGateRefusal's SPECIFIC sentence, which Dispatch's own
// (differently worded) refusal cannot produce. Breaking: recovery.go's `if
// snap.Identity != IdentityTrue` branch removed — confirmed this test goes
// red (the report's reason no longer matched
// describeIdentityGateRefusal(IdentityUnknown)'s text, having fallen through
// to Dispatch's own generic identity-gate wording instead), then restored.
func TestGateRefusesOnIdentityUnknownReportsTheGateOwnSentence(t *testing.T) {
	f := newRecoveryFixture(t, 0)
	f.dispatchAndConfirm(ActionLaunchClip, ActionParams{ClipID: testClipA})
	f.simulateCrash(t)
	f.arena.layers[testLayerOne].activeClip = nil
	delete(f.arena.clips, testClipA)
	delete(f.arena.clips, testClipB)
	delete(f.arena.clips, testPersistA)

	returnedAt := *f.now
	*f.now = f.now.Add(time.Second)
	f.recovery.HandleReachableTransition(context.Background(), returnedAt)

	if len(f.reports) != 1 {
		t.Fatalf("reports = %+v, want exactly one", f.reports)
	}
	want := describeIdentityGateRefusal(SurveySnapshot{Identity: IdentityUnknown})
	layerOne := findRestoreLayer(t, f.reports[0], "Whole House 1")
	if layerOne.Reason != want {
		t.Fatalf("layer one Reason = %q, want the gate's own sentence %q — recovery.go's own identity check, not just Dispatch's redundant one, must be what produced this report", layerOne.Reason, want)
	}
}

// TestRestoreOmitsLayersBeyondMaxRestoreLayers is finding 3: a composition
// larger than MaxRestoreLayers restores only its first MaxRestoreLayers,
// stating the excess via OmittedLayerCount rather than silently attempting
// (and overrunning the caller's own write deadline for) every layer.
// Breaking: restore()'s truncation (`if len(target) > MaxRestoreLayers`)
// removed — confirmed this test goes red (every target layer was
// attempted and OmittedLayerCount stayed 0), then restored.
func TestRestoreOmitsLayersBeyondMaxRestoreLayers(t *testing.T) {
	target := make([]RecoveryLayerRecord, MaxRestoreLayers+3)
	for i := range target {
		target[i] = RecoveryLayerRecord{LayerID: ObjectID(i + 1), Layer: "layer", State: RecoveryLayerDark}
	}

	f := newRecoveryFixture(t, 0)
	report := f.recovery.restore(context.Background(), target, RestoreTriggerManual, *f.now, nil)

	if report.OmittedLayerCount != 3 {
		t.Fatalf("OmittedLayerCount = %d, want 3", report.OmittedLayerCount)
	}
	if len(report.Layers) != MaxRestoreLayers {
		t.Fatalf("len(Layers) = %d, want %d (the omitted layers must not appear at all)", len(report.Layers), MaxRestoreLayers)
	}
}
