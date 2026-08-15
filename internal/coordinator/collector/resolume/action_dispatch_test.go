package resolume

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// This file is action.go/action_dispatch.go/action_client.go's own
// end-to-end test suite: every test here drives [ActionDispatcher.Dispatch]
// against a small in-memory fake Arena (fakeArena below), never a real
// Resolume — matching this package's own standing note that nothing in
// Track D has run against real hardware yet; every claim below is unit-test
// evidence only.
//
// Every test uses a FAKE, PAIRED clock/sleep (fixedClock + fakeSleep, both
// closed over the same *time.Time): [ActionDispatcher.pollUntilConfirmedOrDeadline]'s
// own poll loop advances simulated time on every iteration rather than
// actually sleeping, so a multi-second derived deadline (clearLayer,
// blackout) costs no real wall-clock time in this suite despite genuinely
// exercising many poll iterations.

// fakeSleep returns a func(time.Duration) that advances *now by the given
// duration instead of sleeping — paired with [fixedClock] over the same
// pointer, per [ActionDispatcherOptions.Sleep]'s own doc comment.
func fakeSleep(now *time.Time) func(time.Duration) {
	return func(d time.Duration) { *now = now.Add(d) }
}

// --- Fake Arena --------------------------------------------------------

type faLayer struct {
	bypassed        bool
	bypassedParamID ParameterID
	master          float64
	masterParamID   ParameterID
	activeClip      *ObjectID
	hasTransition   bool
	transitionSecs  float64

	// pendingClearDelay is how much simulated time must elapse after a
	// clear/disconnect-all before this layer's active_clip actually
	// becomes absent — the fake's own stand-in for capture §7.2's measured
	// transition-bounded disconnect delay.
	pendingClearDelay time.Duration
	clearPending      bool
	clearReadyAt      time.Time

	// bypassedValueless/masterValueless simulate the defect-1 hazard
	// directly: capture §17.3's headline finding that no schema in Arena's
	// own specification carries a `required` list, so a `bypassed` or
	// `master` envelope with no "value" key at all is contract-legal. When
	// true, [layerJSON] omits the "value" key from the corresponding
	// field entirely — never emitting an explicit `null`, which is a
	// DIFFERENT, already-handled case — so any test using this flag is
	// exercising the value-less-envelope path specifically, never
	// PresenceNull's.
	bypassedValueless bool
	masterValueless   bool
}

type faClip struct {
	connected  string
	ownerLayer ObjectID
}

type faColumn struct{ connected string }

type faDeck struct {
	selected bool
	name     string
}

// fakeArena is a minimal, in-memory stand-in for Arena's `/api/v1` write
// and by-id-read surface — enough of it to drive every one of
// TRACK-D-D3-SPEC.md §2's seven actions through [ActionDispatcher.Dispatch]
// without a real Resolume. now is the SAME *time.Time pointer a test's
// [fixedClock]/[fakeSleep] pair uses, so a delayed state transition
// (pendingClearDelay) resolves in step with the dispatcher's own simulated
// polling, deterministically and with no real sleep.
type fakeArena struct {
	mu  sync.Mutex
	now *time.Time

	layers  map[ObjectID]*faLayer
	clips   map[ObjectID]*faClip
	columns map[ObjectID]*faColumn
	decks   map[ObjectID]*faDeck

	requests []string // "METHOD /path", in issued order

	// answerValuelessAfterWrite simulates the defect-1 confirmation hazard:
	// when true, a PUT to a layer's bypassed/master parameter does NOT
	// apply the requested value at all — instead every subsequent by-id
	// read of that layer answers the corresponding field with its envelope
	// present but no "value" key (see faLayer.bypassedValueless/
	// masterValueless), forever. A dispatcher that still reads
	// .Param.Value unguarded would see the Go zero value (false/0.0) and,
	// whenever that happens to equal the requested value, report
	// ActionConfirmed off evidence that was never actually read.
	answerValuelessAfterWrite bool
}

func newFakeArena(now *time.Time) *fakeArena {
	return &fakeArena{
		now:     now,
		layers:  map[ObjectID]*faLayer{},
		clips:   map[ObjectID]*faClip{},
		columns: map[ObjectID]*faColumn{},
		decks:   map[ObjectID]*faDeck{},
	}
}

func layerJSON(l *faLayer) string {
	activeClipJSON := "null"
	if l.activeClip != nil {
		activeClipJSON = fmt.Sprintf(`{"id":%d}`, int64(*l.activeClip))
	}
	transitionJSON := "null"
	if l.hasTransition {
		transitionJSON = fmt.Sprintf(`{"duration":{"id":999999,"value":%v}}`, l.transitionSecs)
	}
	bypassedJSON := fmt.Sprintf(`{"id":%d,"value":%t}`, int64(l.bypassedParamID), l.bypassed)
	if l.bypassedValueless {
		bypassedJSON = fmt.Sprintf(`{"id":%d}`, int64(l.bypassedParamID))
	}
	masterJSON := fmt.Sprintf(`{"id":%d,"value":%v}`, int64(l.masterParamID), l.master)
	if l.masterValueless {
		masterJSON = fmt.Sprintf(`{"id":%d}`, int64(l.masterParamID))
	}
	return fmt.Sprintf(
		`{"id":1,"bypassed":%s,"master":%s,"active_clip":%s,"transition":%s}`,
		bypassedJSON, masterJSON, activeClipJSON, transitionJSON)
}

func clipJSON(c *faClip) string {
	return fmt.Sprintf(
		`{"id":1,"connected":{"id":1,"value":%q,"options":["Empty","Disconnected","Previewing","Connected","Connected & previewing"]}}`,
		c.connected)
}

func columnJSON(c *faColumn) string {
	return fmt.Sprintf(`{"id":1,"connected":{"id":1,"value":%q,"options":["Empty","Disconnected","Connected"]}}`, c.connected)
}

func deckJSON(dk *faDeck) string {
	return fmt.Sprintf(`{"id":1,"selected":{"id":1,"value":%t},"name":{"id":1,"value":%q}}`, dk.selected, dk.name)
}

func idFromTail(path string) ObjectID {
	parts := strings.Split(strings.TrimSuffix(path, "/"), "/")
	n, _ := strconv.ParseInt(parts[len(parts)-1], 10, 64)
	return ObjectID(n)
}

func (a *fakeArena) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, r.Method+" "+r.URL.Path)

	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/api/v1/composition/layers/by-id/"):
		id := idFromTail(path)
		l, ok := a.layers[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if l.clearPending && !a.now.Before(l.clearReadyAt) {
			l.activeClip = nil
			l.clearPending = false
		}
		w.Write([]byte(layerJSON(l)))

	case r.Method == http.MethodGet && strings.HasPrefix(path, "/api/v1/composition/clips/by-id/"):
		id := idFromTail(path)
		c, ok := a.clips[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(clipJSON(c)))

	case r.Method == http.MethodGet && strings.HasPrefix(path, "/api/v1/composition/columns/by-id/"):
		id := idFromTail(path)
		c, ok := a.columns[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(columnJSON(c)))

	case r.Method == http.MethodGet && strings.HasPrefix(path, "/api/v1/composition/decks/by-id/"):
		id := idFromTail(path)
		dk, ok := a.decks[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(deckJSON(dk)))

	case r.Method == http.MethodPost && strings.HasPrefix(path, "/api/v1/composition/clips/by-id/") && strings.HasSuffix(path, "/connect"):
		id := idFromTail(strings.TrimSuffix(path, "/connect"))
		body, _ := io.ReadAll(r.Body)
		// Defect 6 (2026-08-15): [Client.ConnectClip] now sends NO body at
		// all — the vendor's own documented "click" gesture — rather than a
		// literal `true`. An empty body is accepted here exactly like
		// `true` used to be; anything else (in particular a literal
		// `false`) is still rejected, matching capture §6.3's own measured
		// "false does nothing" finding.
		if trimmed := strings.TrimSpace(string(body)); trimmed != "" && trimmed != "true" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		c, ok := a.clips[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		c.connected = "Connected"
		if l, ok := a.layers[c.ownerLayer]; ok {
			l.activeClip = &id
			l.clearPending = false
		}
		w.WriteHeader(http.StatusNoContent)

	case r.Method == http.MethodPost && strings.HasPrefix(path, "/api/v1/composition/columns/by-id/") && strings.HasSuffix(path, "/connect"):
		id := idFromTail(strings.TrimSuffix(path, "/connect"))
		c, ok := a.columns[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		c.connected = "Connected"
		w.WriteHeader(http.StatusNoContent)

	case r.Method == http.MethodPost && strings.HasPrefix(path, "/api/v1/composition/layers/by-id/") && strings.HasSuffix(path, "/clear"):
		id := idFromTail(strings.TrimSuffix(path, "/clear"))
		l, ok := a.layers[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		l.clearPending = true
		l.clearReadyAt = a.now.Add(l.pendingClearDelay)
		w.WriteHeader(http.StatusNoContent)

	case r.Method == http.MethodPost && path == "/api/v1/composition/disconnect-all":
		for _, l := range a.layers {
			l.clearPending = true
			l.clearReadyAt = a.now.Add(l.pendingClearDelay)
		}
		w.WriteHeader(http.StatusNoContent)

	case r.Method == http.MethodPost && strings.HasPrefix(path, "/api/v1/composition/decks/by-id/") && strings.HasSuffix(path, "/select"):
		id := idFromTail(strings.TrimSuffix(path, "/select"))
		if _, ok := a.decks[id]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		for _, dk := range a.decks {
			dk.selected = false
		}
		a.decks[id].selected = true
		w.WriteHeader(http.StatusNoContent)

	case r.Method == http.MethodPut && strings.HasPrefix(path, "/api/v1/parameter/by-id/"):
		id := idFromTail(path)
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Value any `json:"value"`
		}
		_ = json.Unmarshal(body, &payload)
		found := false
		for _, l := range a.layers {
			if l.bypassedParamID == ParameterID(id) {
				if a.answerValuelessAfterWrite {
					l.bypassedValueless = true
				} else if b, ok := payload.Value.(bool); ok {
					l.bypassed = b
				}
				found = true
			}
			if l.masterParamID == ParameterID(id) {
				if a.answerValuelessAfterWrite {
					l.masterValueless = true
				} else if f, ok := payload.Value.(float64); ok {
					l.master = f
				}
				found = true
			}
		}
		if !found {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// --- Test fixture wiring -------------------------------------------------

// Object ids from pkg/resolumecomp/testdata/complete.avc (composition_test.go's
// own parseTestComposition fixture) — see that fixture's own comments for
// the full layout. Reused here rather than a second fixture: Layer 1
// (3000000000001) owns clip 6000000000001 (deck 2000000000001) and
// persistent clip 7000000000001; Layer 2 (3000000000002) owns clip
// 6000000000003 (deck 2000000000001) and persistent clip 7000000000002;
// clip 6000000000101 (deck 2000000000002) also resolves to Layer 1
// (layers are deck-independent).
const (
	testDeckOne   ObjectID = 2000000000001
	testDeckTwo   ObjectID = 2000000000002
	testLayerOne  ObjectID = 3000000000001
	testLayerTwo  ObjectID = 3000000000002
	testClipA     ObjectID = 6000000000001 // deck one, layer one, "Snowflakes"
	testClipB     ObjectID = 6000000000003 // deck one, layer two, "Clip B"
	testClipC     ObjectID = 6000000000101 // deck two, layer one, "Clip C"
	testPersistA  ObjectID = 7000000000001 // layer one
	testColumnOne ObjectID = 5000000000001 // deck one, columnIndex 0
)

// newTestActionDispatcher builds an [ActionDispatcher] over a fresh
// [Collector] whose [CompositionStore] holds the shared complete.avc
// fixture, its [Collector.LastSurveySnapshot] pre-seeded as
// [IdentityTrue] with deckOne selected (the ordinary "D-2 already confirmed
// identity" state every action test starts from unless it is specifically
// testing the identity or deck gate), and its [Client] pointed at arena.
// now/sleep are the shared fake clock pair every poll loop in the test
// advances instead of sleeping.
func newTestActionDispatcher(t *testing.T, arena *fakeArena, now *time.Time, snap SurveySnapshot) *ActionDispatcher {
	t.Helper()
	srv := httptest.NewServer(arena)
	t.Cleanup(srv.Close)

	comp := parseTestComposition(t)
	store := newTestCompositionStore(t, comp)

	c := newTestCollector(t, srv.URL, Options{Now: fixedClock(now), CompositionStore: store})
	c.recordSurveySnapshot(snap)

	return NewActionDispatcher(c, ActionDispatcherOptions{
		Now: fixedClock(now), Sleep: fakeSleep(now), PollInterval: 10 * time.Millisecond,
	})
}

func identifiedSnapshot(now time.Time) SurveySnapshot {
	return SurveySnapshot{
		SurveyRan: true, SurveyAt: now,
		IdentityKnown: true, Identity: IdentityTrue, IdentityObservedAt: now,
		SelectedDeckKnown: true, SelectedDeckID: testDeckOne, SelectedDeckName: "Deck One", SelectedDeckObservedAt: now,
	}
}

// --- launchClip ------------------------------------------------------------

func TestDispatchLaunchClipConfirms(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	arena.layers[testLayerOne] = &faLayer{bypassedParamID: 9001, masterParamID: 9002, master: 1}
	arena.clips[testClipA] = &faClip{connected: "Disconnected", ownerLayer: testLayerOne}

	d := newTestActionDispatcher(t, arena, &now, identifiedSnapshot(now))

	out, err := d.Dispatch(context.Background(), ActionLaunchClip, ActionParams{ClipID: testClipA})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State != ActionConfirmed {
		t.Fatalf("State = %q, want %q (reason: %s)", out.State, ActionConfirmed, out.Reason)
	}
	if out.Reason == "" {
		t.Error("Reason is empty on a confirmed outcome — Step 8 Finding 6's own rule applies here too")
	}
	if !out.ConfirmedAt.After(out.DispatchedAt) {
		t.Errorf("ConfirmedAt (%s) is not after DispatchedAt (%s)", out.ConfirmedAt, out.DispatchedAt)
	}

	arena.mu.Lock()
	defer arena.mu.Unlock()
	for _, req := range arena.requests {
		if req == "POST /api/v1/composition/clips/by-id/6000000000001/connect" {
			return
		}
	}
	t.Errorf("connect was never dispatched; requests = %v", arena.requests)
}

// TestDispatchLaunchClipAlreadyPlayingIsUnconfirmable is acceptance
// criterion 2: a clip already satisfying the confirming predicate before
// dispatch must report unconfirmable, never confirmed — TRACK-D-D3-SPEC.md
// §3.5/§4.2, the direct generalization of Step 7's 179-microsecond defect.
func TestDispatchLaunchClipAlreadyPlayingIsUnconfirmable(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	arena.layers[testLayerOne] = &faLayer{bypassedParamID: 9001, masterParamID: 9002, master: 1, activeClip: idPtr(testClipA)}
	arena.clips[testClipA] = &faClip{connected: "Connected", ownerLayer: testLayerOne}

	d := newTestActionDispatcher(t, arena, &now, identifiedSnapshot(now))

	out, err := d.Dispatch(context.Background(), ActionLaunchClip, ActionParams{ClipID: testClipA})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State != ActionUnconfirmable {
		t.Fatalf("State = %q, want %q (reason: %s)", out.State, ActionUnconfirmable, out.Reason)
	}
	if out.Reason == "" {
		t.Error("Reason is empty")
	}

	// The click is still dispatched — it is harmless, and refusing it
	// would be a SIXTH thing this package invented beyond what
	// TRACK-D-D3-SPEC.md's own table describes.
	arena.mu.Lock()
	defer arena.mu.Unlock()
	found := false
	for _, req := range arena.requests {
		if req == "POST /api/v1/composition/clips/by-id/6000000000001/connect" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the connect call to still be dispatched even though the outcome is unconfirmable; requests = %v", arena.requests)
	}
}

// TestDispatchLaunchClipDeckMismatchIsRefused is acceptance criterion 3.
func TestDispatchLaunchClipDeckMismatchIsRefused(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	arena.layers[testLayerOne] = &faLayer{bypassedParamID: 9001, masterParamID: 9002, master: 1}
	arena.clips[testClipA] = &faClip{connected: "Disconnected", ownerLayer: testLayerOne}

	// testClipA belongs to testDeckOne, but the snapshot reports
	// testDeckTwo selected.
	snap := identifiedSnapshot(now)
	snap.SelectedDeckID, snap.SelectedDeckName = testDeckTwo, "Deck Two"

	d := newTestActionDispatcher(t, arena, &now, snap)

	out, err := d.Dispatch(context.Background(), ActionLaunchClip, ActionParams{ClipID: testClipA})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State != ActionRefused {
		t.Fatalf("State = %q, want %q (reason: %s)", out.State, ActionRefused, out.Reason)
	}
	if !contains(out.Reason, "2000000000001") || !contains(out.Reason, "2000000000002") {
		t.Errorf("Reason = %q, want it to name both decks (2000000000001 and 2000000000002)", out.Reason)
	}
	if !out.DispatchedAt.IsZero() {
		t.Errorf("DispatchedAt = %s, want the zero time for a refusal", out.DispatchedAt)
	}

	arena.mu.Lock()
	defer arena.mu.Unlock()
	if len(arena.requests) != 0 {
		t.Errorf("requests = %v, want zero HTTP requests for a deck-mismatch refusal", arena.requests)
	}
}

// TestDispatchLaunchClipPersistentClipIgnoresDeck: a persistent clip
// carries no deck term at all (ADR-032 decision 6) and must dispatch
// regardless of which deck the snapshot reports selected.
func TestDispatchLaunchClipPersistentClipIgnoresDeck(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	arena.layers[testLayerOne] = &faLayer{bypassedParamID: 9001, masterParamID: 9002, master: 1}
	arena.clips[testPersistA] = &faClip{connected: "Disconnected", ownerLayer: testLayerOne}

	snap := identifiedSnapshot(now)
	snap.SelectedDeckID, snap.SelectedDeckName = testDeckTwo, "Deck Two"

	d := newTestActionDispatcher(t, arena, &now, snap)

	out, err := d.Dispatch(context.Background(), ActionLaunchClip, ActionParams{ClipID: testPersistA})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State != ActionConfirmed {
		t.Fatalf("State = %q, want %q (reason: %s)", out.State, ActionConfirmed, out.Reason)
	}
}

// --- The composition-identity gate (§3.6, acceptance criterion 6) ---------

func TestDispatchRefusesWhenIdentityUnknown(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	d := newTestActionDispatcher(t, arena, &now, SurveySnapshot{}) // zero value: SurveyRan false

	out, err := d.Dispatch(context.Background(), ActionBlackout, ActionParams{})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State != ActionRefused {
		t.Fatalf("State = %q, want %q", out.State, ActionRefused)
	}
	arena.mu.Lock()
	defer arena.mu.Unlock()
	if len(arena.requests) != 0 {
		t.Errorf("requests = %v, want zero", arena.requests)
	}
}

func TestDispatchRefusesWhenIdentityFalse(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	snap := identifiedSnapshot(now)
	snap.Identity = IdentityFalse

	d := newTestActionDispatcher(t, arena, &now, snap)

	out, err := d.Dispatch(context.Background(), ActionLaunchColumn, ActionParams{ColumnID: testColumnOne})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State != ActionRefused {
		t.Fatalf("State = %q, want %q", out.State, ActionRefused)
	}
}

func TestDispatchRefusesWhenIdentityDeckMismatch(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	snap := identifiedSnapshot(now)
	snap.Identity = IdentityDeckMismatch

	d := newTestActionDispatcher(t, arena, &now, snap)

	out, err := d.Dispatch(context.Background(), ActionSelectDeck, ActionParams{DeckID: testDeckOne})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State != ActionRefused {
		t.Fatalf("State = %q, want %q", out.State, ActionRefused)
	}
}

// --- clearLayer, and acceptance criterion 4 (derived deadline) -----------

func TestDispatchClearLayerConfirms(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	arena.layers[testLayerOne] = &faLayer{
		bypassedParamID: 9001, masterParamID: 9002, master: 1,
		activeClip: idPtr(testClipA), hasTransition: true, transitionSecs: 0.1,
		pendingClearDelay: 120 * time.Millisecond,
	}

	d := newTestActionDispatcher(t, arena, &now, identifiedSnapshot(now))
	out, err := d.Dispatch(context.Background(), ActionClearLayer, ActionParams{LayerID: testLayerOne})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State != ActionConfirmed {
		t.Fatalf("State = %q, want %q (reason: %s)", out.State, ActionConfirmed, out.Reason)
	}
}

// TestDispatchClearLayerDeadlineIsDerivedFromTransitionDuration is
// acceptance criterion 4: a clearLayer against a layer with a longer
// transition.duration takes longer (in the fake's own simulated time) to
// confirm than the identical action against a layer with a shorter one —
// demonstrating [deriveClearDeadline] actually drives the poll's own
// deadline rather than a constant. Both delays are set just OVER their own
// transition.duration, mirroring capture §7.2's own measured 75-113ms
// overshoot pattern.
func TestDispatchClearLayerDeadlineIsDerivedFromTransitionDuration(t *testing.T) {
	run := func(transitionSecs float64, overshoot time.Duration) time.Duration {
		now := time.Now()
		arena := newFakeArena(&now)
		arena.layers[testLayerOne] = &faLayer{
			bypassedParamID: 9001, masterParamID: 9002, master: 1,
			activeClip: idPtr(testClipA), hasTransition: true, transitionSecs: transitionSecs,
			pendingClearDelay: time.Duration(transitionSecs*float64(time.Second)) + overshoot,
		}
		d := newTestActionDispatcher(t, arena, &now, identifiedSnapshot(now))
		out, err := d.Dispatch(context.Background(), ActionClearLayer, ActionParams{LayerID: testLayerOne})
		if err != nil {
			t.Fatalf("Dispatch error = %v", err)
		}
		if out.State != ActionConfirmed {
			t.Fatalf("transitionSecs=%v: State = %q, want %q (reason: %s)", transitionSecs, out.State, ActionConfirmed, out.Reason)
		}
		return out.ConfirmedAt.Sub(out.DispatchedAt)
	}

	fast := run(0.1, 20*time.Millisecond)
	slow := run(2.5, 90*time.Millisecond)

	if !(fast < slow) {
		t.Errorf("fast-transition confirm delay (%s) is not shorter than slow-transition confirm delay (%s); "+
			"a fixed deadline would make these indistinguishable", fast, slow)
	}
}

// TestDispatchClearLayerDeadlineExpiresAsUnconfirmedNeverFailed is
// acceptance criterion 5.
func TestDispatchClearLayerDeadlineExpiresAsUnconfirmedNeverFailed(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	arena.layers[testLayerOne] = &faLayer{
		bypassedParamID: 9001, masterParamID: 9002, master: 1,
		activeClip: idPtr(testClipA), hasTransition: true, transitionSecs: 0.1,
		// pendingClearDelay left at zero's default (0), but never resolved:
		// simulate an Arena that never actually clears by disabling the
		// resolution entirely — set the delay far beyond the derived
		// deadline (0.1s + 1s margin = 1.1s).
		pendingClearDelay: time.Hour,
	}

	d := newTestActionDispatcher(t, arena, &now, identifiedSnapshot(now))
	out, err := d.Dispatch(context.Background(), ActionClearLayer, ActionParams{LayerID: testLayerOne})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State != ActionUnconfirmed {
		t.Fatalf("State = %q, want %q (reason: %s)", out.State, ActionUnconfirmed, out.Reason)
	}
	if out.Reason == "" {
		t.Error("Reason is empty")
	}
}

// --- blackout ---------------------------------------------------------

func TestDispatchBlackoutConfirmsAcrossMultipleLayers(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	arena.layers[testLayerOne] = &faLayer{
		bypassedParamID: 9001, masterParamID: 9002, master: 1,
		activeClip: idPtr(testClipA), hasTransition: true, transitionSecs: 0.1, pendingClearDelay: 20 * time.Millisecond,
	}
	arena.layers[testLayerTwo] = &faLayer{
		bypassedParamID: 9011, masterParamID: 9012, master: 1,
		activeClip: idPtr(testClipB), hasTransition: true, transitionSecs: 0.2, pendingClearDelay: 220 * time.Millisecond,
	}
	// A third tracked layer (3000000000003 per the fixture) with nothing
	// playing at all: an "already empty" layer must not block the
	// blackout on a transition it does not need.
	arena.layers[3000000000003] = &faLayer{bypassedParamID: 9021, masterParamID: 9022, master: 1}

	d := newTestActionDispatcher(t, arena, &now, identifiedSnapshot(now))
	out, err := d.Dispatch(context.Background(), ActionBlackout, ActionParams{})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State != ActionConfirmed {
		t.Fatalf("State = %q, want %q (reason: %s)", out.State, ActionConfirmed, out.Reason)
	}

	arena.mu.Lock()
	defer arena.mu.Unlock()
	found := false
	for _, req := range arena.requests {
		if req == "POST /api/v1/composition/disconnect-all" {
			found = true
		}
	}
	if !found {
		t.Errorf("disconnect-all was never dispatched; requests = %v", arena.requests)
	}
}

func TestDispatchBlackoutAlreadyEmptyIsUnconfirmable(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	arena.layers[testLayerOne] = &faLayer{bypassedParamID: 9001, masterParamID: 9002, master: 1}
	arena.layers[testLayerTwo] = &faLayer{bypassedParamID: 9011, masterParamID: 9012, master: 1}
	arena.layers[3000000000003] = &faLayer{bypassedParamID: 9021, masterParamID: 9022, master: 1}

	d := newTestActionDispatcher(t, arena, &now, identifiedSnapshot(now))
	out, err := d.Dispatch(context.Background(), ActionBlackout, ActionParams{})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State != ActionUnconfirmable {
		t.Fatalf("State = %q, want %q (reason: %s)", out.State, ActionUnconfirmable, out.Reason)
	}
}

// --- launchColumn -----------------------------------------------------

func TestDispatchLaunchColumnConfirms(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	arena.columns[testColumnOne] = &faColumn{connected: "Disconnected"}

	d := newTestActionDispatcher(t, arena, &now, identifiedSnapshot(now))
	out, err := d.Dispatch(context.Background(), ActionLaunchColumn, ActionParams{ColumnID: testColumnOne})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State != ActionConfirmed {
		t.Fatalf("State = %q, want %q (reason: %s)", out.State, ActionConfirmed, out.Reason)
	}
}

func TestDispatchLaunchColumnUnknownIDIsRefused(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)

	d := newTestActionDispatcher(t, arena, &now, identifiedSnapshot(now))
	out, err := d.Dispatch(context.Background(), ActionLaunchColumn, ActionParams{ColumnID: 999999999})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State != ActionRefused {
		t.Fatalf("State = %q, want %q", out.State, ActionRefused)
	}
	arena.mu.Lock()
	defer arena.mu.Unlock()
	if len(arena.requests) != 0 {
		t.Errorf("requests = %v, want zero for an id absent from the stored composition", arena.requests)
	}
}

// --- selectDeck -------------------------------------------------------

func TestDispatchSelectDeckConfirms(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	arena.decks[testDeckOne] = &faDeck{selected: false, name: "Deck One"}
	arena.decks[testDeckTwo] = &faDeck{selected: true, name: "Deck Two"}

	d := newTestActionDispatcher(t, arena, &now, identifiedSnapshot(now))
	out, err := d.Dispatch(context.Background(), ActionSelectDeck, ActionParams{DeckID: testDeckOne})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State != ActionConfirmed {
		t.Fatalf("State = %q, want %q (reason: %s)", out.State, ActionConfirmed, out.Reason)
	}
}

func TestDispatchSelectDeckAlreadySelectedIsUnconfirmable(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	arena.decks[testDeckOne] = &faDeck{selected: true, name: "Deck One"}

	d := newTestActionDispatcher(t, arena, &now, identifiedSnapshot(now))
	out, err := d.Dispatch(context.Background(), ActionSelectDeck, ActionParams{DeckID: testDeckOne})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State != ActionUnconfirmable {
		t.Fatalf("State = %q, want %q (reason: %s)", out.State, ActionUnconfirmable, out.Reason)
	}
}

// --- setLayerBypass / setLayerMaster ------------------------------------

func TestDispatchSetLayerBypassConfirms(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	arena.layers[testLayerOne] = &faLayer{bypassed: false, bypassedParamID: 9001, masterParamID: 9002, master: 1}

	d := newTestActionDispatcher(t, arena, &now, identifiedSnapshot(now))
	out, err := d.Dispatch(context.Background(), ActionSetLayerBypass, ActionParams{LayerID: testLayerOne, Bypassed: true})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State != ActionConfirmed {
		t.Fatalf("State = %q, want %q (reason: %s)", out.State, ActionConfirmed, out.Reason)
	}

	arena.mu.Lock()
	defer arena.mu.Unlock()
	if got := arena.layers[testLayerOne].bypassed; !got {
		t.Errorf("layer bypassed = %v, want true", got)
	}
	found := false
	for _, req := range arena.requests {
		if req == "PUT /api/v1/parameter/by-id/9001" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a PUT to the LIVE bypassed parameter id (9001); requests = %v", arena.requests)
	}
}

func TestDispatchSetLayerBypassAlreadyEqualIsUnconfirmable(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	arena.layers[testLayerOne] = &faLayer{bypassed: true, bypassedParamID: 9001, masterParamID: 9002, master: 1}

	d := newTestActionDispatcher(t, arena, &now, identifiedSnapshot(now))
	out, err := d.Dispatch(context.Background(), ActionSetLayerBypass, ActionParams{LayerID: testLayerOne, Bypassed: true})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State != ActionUnconfirmable {
		t.Fatalf("State = %q, want %q (reason: %s)", out.State, ActionUnconfirmable, out.Reason)
	}
}

func TestDispatchSetLayerMasterConfirms(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	arena.layers[testLayerOne] = &faLayer{bypassedParamID: 9001, masterParamID: 9002, master: 1.0}

	d := newTestActionDispatcher(t, arena, &now, identifiedSnapshot(now))
	out, err := d.Dispatch(context.Background(), ActionSetLayerMaster, ActionParams{LayerID: testLayerOne, Master: 0.25})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State != ActionConfirmed {
		t.Fatalf("State = %q, want %q (reason: %s)", out.State, ActionConfirmed, out.Reason)
	}
	arena.mu.Lock()
	defer arena.mu.Unlock()
	if got := arena.layers[testLayerOne].master; !floatsNearlyEqual(got, 0.25, layerMasterEpsilon) {
		t.Errorf("layer master = %v, want 0.25", got)
	}
}

// --- Defect 1 (2026-08-15): a value-less parameter envelope must never
// produce a false ActionConfirmed on the darkening direction -------------
//
// Both tests below use want == false / want == 0.0 deliberately: these are
// exactly CLAUDE.md's own named "blackout-adjacent values" — the case
// where a value-less envelope's Go zero value happens to equal the
// desired value, so a bare .Param.Value read would report ActionConfirmed
// for evidence that was never actually collected.

// TestDispatchSetLayerBypassValuelessBaselineIsRefused covers the
// PRE-DISPATCH half: if the baseline read's own bypassed envelope has no
// "value" key, this seam cannot know the layer's current state, so the
// command is refused before any write reaches Arena — never dispatched on
// an assumed (and possibly wrong) current value.
//
// Before trusting this test: reverted dispatchSetLayerBypass's baseline
// check to read baseLayer.Bypassed.Param.Value directly (the pre-fix
// shape), which decodes the value-less envelope's Go zero value (false)
// and reads it as the CURRENT state — with want=false, that makes
// alreadySatisfied true, and the command reports ActionUnconfirmable
// instead of ActionRefused, having dispatched a write against a layer
// whose actual current bypass state was never known. Reran: this test
// failed, State = "unconfirmable", want "refused". Restored afterward.
func TestDispatchSetLayerBypassValuelessBaselineIsRefused(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	arena.layers[testLayerOne] = &faLayer{bypassedParamID: 9001, masterParamID: 9002, master: 1, bypassedValueless: true}

	d := newTestActionDispatcher(t, arena, &now, identifiedSnapshot(now))
	out, err := d.Dispatch(context.Background(), ActionSetLayerBypass, ActionParams{LayerID: testLayerOne, Bypassed: false})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State != ActionRefused {
		t.Fatalf("State = %q, want %q (reason: %s)", out.State, ActionRefused, out.Reason)
	}

	arena.mu.Lock()
	defer arena.mu.Unlock()
	for _, req := range arena.requests {
		if req == "PUT /api/v1/parameter/by-id/9001" {
			t.Errorf("a write reached Arena (requests = %v) — a refused command must dispatch nothing", arena.requests)
		}
	}
}

// TestDispatchSetLayerBypassValuelessConfirmationNeverReportsConfirmed
// covers the CONFIRMATION half: the baseline reads a real value (true, so
// want=false is NOT already satisfied and the poll loop actually runs),
// the write is dispatched successfully, but every post-dispatch read of
// the bypassed parameter answers with no "value" key at all (Arena's own
// specification: this is schema-legal). The outcome must be
// ActionUnconfirmed when the deadline expires — NEVER ActionConfirmed.
//
// Before trusting this test: reverted the confirmation closure to read
// layer.Bypassed.Param.Value directly instead of through Bool(). Reran:
// the test failed immediately, State = "confirmed" — the exact false
// confirmation on the darkening direction this defect names, produced in
// well under the 2s deadline because the value-less envelope's Go zero
// value (false) equals want (false) on the very first poll. Restored
// afterward.
func TestDispatchSetLayerBypassValuelessConfirmationNeverReportsConfirmed(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	arena.layers[testLayerOne] = &faLayer{bypassed: true, bypassedParamID: 9001, masterParamID: 9002, master: 1}
	arena.answerValuelessAfterWrite = true

	d := newTestActionDispatcher(t, arena, &now, identifiedSnapshot(now))
	out, err := d.Dispatch(context.Background(), ActionSetLayerBypass, ActionParams{LayerID: testLayerOne, Bypassed: false})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State == ActionConfirmed {
		t.Fatalf("State = %q — a value-less confirmation read must NEVER report confirmed (reason: %s)", out.State, out.Reason)
	}
	if out.State != ActionUnconfirmed {
		t.Fatalf("State = %q, want %q (reason: %s)", out.State, ActionUnconfirmed, out.Reason)
	}
}

// TestDispatchSetLayerMasterValuelessConfirmationNeverReportsConfirmed is
// the identical scenario for setLayerMaster's own darkening-direction
// value, want == 0.0 — see the bypass sibling test's own doc comment for
// the shape and the reasoning.
func TestDispatchSetLayerMasterValuelessConfirmationNeverReportsConfirmed(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	arena.layers[testLayerOne] = &faLayer{bypassedParamID: 9001, masterParamID: 9002, master: 1.0}
	arena.answerValuelessAfterWrite = true

	d := newTestActionDispatcher(t, arena, &now, identifiedSnapshot(now))
	out, err := d.Dispatch(context.Background(), ActionSetLayerMaster, ActionParams{LayerID: testLayerOne, Master: 0.0})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State == ActionConfirmed {
		t.Fatalf("State = %q — a value-less confirmation read must NEVER report confirmed (reason: %s)", out.State, out.Reason)
	}
	if out.State != ActionUnconfirmed {
		t.Fatalf("State = %q, want %q (reason: %s)", out.State, ActionUnconfirmed, out.Reason)
	}
}

// --- Dispatch failure: a definite non-2xx becomes ActionFailed -----------

func TestDispatchLaunchClipDefiniteFailureIsFailedNeverConfirmed(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	arena.layers[testLayerOne] = &faLayer{bypassedParamID: 9001, masterParamID: 9002, master: 1}
	arena.clips[testClipA] = &faClip{connected: "Disconnected", ownerLayer: testLayerOne}

	// No entry in arena.clips under a DIFFERENT id used for dispatch would
	// 404 the connect call. Simplest reproduction: remove the clip's own
	// registration right before dispatch would be racy in a real server;
	// instead exercise it directly by pointing the action at an id that
	// resolves against the stored composition (so it passes the resolve
	// step) but has no fake-server clip registered (so ConnectClip 404s).
	delete(arena.clips, testClipA)

	d := newTestActionDispatcher(t, arena, &now, identifiedSnapshot(now))
	out, err := d.Dispatch(context.Background(), ActionLaunchClip, ActionParams{ClipID: testClipA})
	// The pre-dispatch baseline read (readClip) also 404s here, which this
	// package treats as "could not read a baseline" -> refused, not
	// failed. That is the CORRECT and distinct outcome for this input
	// shape; see TestDispatchLaunchClipDispatchFailureAfterBaseline below
	// for the "baseline read succeeds, dispatch itself fails" case, which
	// is what ActionFailed actually covers.
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State != ActionRefused {
		t.Fatalf("State = %q, want %q (a missing baseline refuses rather than dispatching blind); reason: %s", out.State, ActionRefused, out.Reason)
	}
}

// TestDispatchLaunchClipDispatchFailureAfterBaseline: the baseline read
// succeeds, but the write itself is rejected — capture §2.5's own "a
// command against a target that does not exist does fail loudly." This
// must report ActionFailed, never ActionConfirmed or ActionUnconfirmed.
func TestDispatchLaunchClipDispatchFailureAfterBaseline(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	arena.layers[testLayerOne] = &faLayer{bypassedParamID: 9001, masterParamID: 9002, master: 1}
	arena.clips[testClipA] = &faClip{connected: "Disconnected", ownerLayer: testLayerOne}

	// A custom handler wraps the fake so the baseline GET succeeds
	// normally, but the connect POST specifically 500s.
	failingConnect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/connect") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		arena.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(failingConnect)
	t.Cleanup(srv.Close)

	comp := parseTestComposition(t)
	store := newTestCompositionStore(t, comp)
	c := newTestCollector(t, srv.URL, Options{Now: fixedClock(&now), CompositionStore: store})
	c.recordSurveySnapshot(identifiedSnapshot(now))
	d := NewActionDispatcher(c, ActionDispatcherOptions{Now: fixedClock(&now), Sleep: fakeSleep(&now), PollInterval: 10 * time.Millisecond})

	out, err := d.Dispatch(context.Background(), ActionLaunchClip, ActionParams{ClipID: testClipA})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State != ActionFailed {
		t.Fatalf("State = %q, want %q (reason: %s)", out.State, ActionFailed, out.Reason)
	}
	if out.Reason == "" {
		t.Error("Reason is empty")
	}
}

// --- Unrecognized action name ---------------------------------------------

func TestDispatchUnrecognizedActionReturnsError(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	d := newTestActionDispatcher(t, arena, &now, identifiedSnapshot(now))

	if _, err := d.Dispatch(context.Background(), ActionName("notAnAction"), ActionParams{}); err == nil {
		t.Fatal("Dispatch error = nil, want an error for an unrecognized action name")
	}
}

func idPtr(id ObjectID) *ObjectID { return &id }
