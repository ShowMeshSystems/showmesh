package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Track F seam F3 tests for nightloop.go's state-machine advance
// functions, driven directly (no HTTP) against a real *store.Store, per
// the seam's own emphasis: rule 4 (completion evidence, never
// graceful-stop acceptance) is the most load-bearing behavior in this
// file.

func nightLoopTestHandlers(t *testing.T, now func() time.Time, obs *fakeObservationLister) (*handlers, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "db"), nil, store.WithClock(now))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	deps := Dependencies{NightSessions: st, Observations: obs}.withDefaults()
	return &handlers{deps: deps, clock: now, logger: testLogger()}, st
}

func liveSessionAnchor(instanceID, playlist, item string, dispatchedAt time.Time) nightContentAnchor {
	return nightContentAnchor{
		Purpose: nightAnchorPurposeShow, FPPInstanceID: instanceID, Playlist: playlist, Item: item,
		DispatchedAt: dispatchedAt, ObservedAt: dispatchedAt.Add(time.Second),
	}
}

func mustCreateLiveSession(t *testing.T, st *store.Store, now time.Time, anchor nightContentAnchor) store.NightSessionRecord {
	t.Helper()
	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateLive, StateEnteredAt: now, ContentAnchorJSON: encodeNightContentAnchor(anchor),
	}
	if err := st.CreateNightSession(context.Background(), rec, now); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	return rec
}

func statusObservation(instanceID, status string, collectedAt time.Time) observation.Observation {
	observedAt := collectedAt
	return observation.Observation{
		Resource: observation.ResourceRef{Kind: observation.ResourceFPP, ID: instanceID},
		Signal:   observation.SignalID(fppStatusSignal), Value: status,
		ObservedAt: &observedAt, CollectedAt: collectedAt, Source: "fpp-rest",
		Quality: observation.QualityDirect, ValidFor: time.Minute,
	}
}

func playlistNameObservation(instanceID, name string, collectedAt time.Time) observation.Observation {
	observedAt := collectedAt
	return observation.Observation{
		Resource: observation.ResourceRef{Kind: observation.ResourceFPP, ID: instanceID},
		Signal:   observation.SignalID(fppPlaylistNameSignal), Value: name,
		ObservedAt: &observedAt, CollectedAt: collectedAt, Source: "fpp-rest",
		Quality: observation.QualityDirect, ValidFor: time.Minute,
	}
}

func repeatModeObservation(instanceID string, repeat bool, collectedAt time.Time) observation.Observation {
	observedAt := collectedAt
	return observation.Observation{
		Resource: observation.ResourceRef{Kind: observation.ResourceFPP, ID: instanceID},
		Signal:   observation.SignalID(nightSignalPlaylistRepeatMode), Value: repeat,
		ObservedAt: &observedAt, CollectedAt: collectedAt, Source: "fpp-rest",
		Quality: observation.QualityDirect, ValidFor: time.Minute,
	}
}

func positionMSObservation(instanceID string, ms int64, collectedAt time.Time) observation.Observation {
	observedAt := collectedAt
	return observation.Observation{
		Resource: observation.ResourceRef{Kind: observation.ResourceFPP, ID: instanceID},
		Signal:   observation.SignalID(nightSignalPositionElapsedMS), Value: ms,
		ObservedAt: &observedAt, CollectedAt: collectedAt, Source: "fpp-rest",
		Quality: observation.QualityDirect, ValidFor: time.Minute,
	}
}

// Rule 4: "Graceful-stop acceptance is not completion... the return
// transition begins only on observed evidence that the playlist actually
// completed." A "stopping gracefully" status, however long it persists,
// must never advance live -> transition-to-resting.
func TestNightAdvanceLive_Rule4_StoppingGracefullyIsNotCompletion(t *testing.T) {
	dispatchedAt := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	now := dispatchedAt.Add(10 * time.Second)
	obs := &fakeObservationLister{obs: []observation.Observation{
		statusObservation("player-01", fppStatusValueStoppingGracefully, now),
		playlistNameObservation("player-01", "halloween-show", now),
	}}
	h, st := nightLoopTestHandlers(t, func() time.Time { return now }, obs)
	anchor := liveSessionAnchor("player-01", "halloween-show", "halloween-show.fseq", dispatchedAt)
	mustCreateLiveSession(t, st, dispatchedAt, anchor)

	h.nightAdvanceLive(context.Background(), now, mustGetCurrentSession(t, st))

	got := mustGetCurrentSession(t, st)
	if got.State != nightStateLive {
		t.Fatalf("state = %q, want still %q (stopping gracefully must never be read as completion)", got.State, nightStateLive)
	}
}

// Rule 4, the positive case: status idle AND current_playlist cleared (F0
// §3's own captured completion shape) DOES advance to transition-to-resting.
func TestNightAdvanceLive_Rule4_IdleWithClearedPlaylistIsCompletion(t *testing.T) {
	dispatchedAt := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	now := dispatchedAt.Add(20 * time.Second)
	obs := &fakeObservationLister{obs: []observation.Observation{
		statusObservation("player-01", fppStatusValueIdle, now),
		playlistNameObservation("player-01", "", now),
	}}
	h, st := nightLoopTestHandlers(t, func() time.Time { return now }, obs)
	anchor := liveSessionAnchor("player-01", "halloween-show", "halloween-show.fseq", dispatchedAt)
	mustCreateLiveSession(t, st, dispatchedAt, anchor)

	h.nightAdvanceLive(context.Background(), now, mustGetCurrentSession(t, st))

	got := mustGetCurrentSession(t, st)
	if got.State != nightStateTransitionToResting {
		t.Fatalf("state = %q, want %q", got.State, nightStateTransitionToResting)
	}
}

// Rule 4: idle alone, with current_playlist NOT yet cleared, is also not
// completion — F0 §3's own two-part condition, checked as a conjunction.
func TestNightAdvanceLive_Rule4_IdleWithPlaylistStillNamedIsNotCompletion(t *testing.T) {
	dispatchedAt := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	now := dispatchedAt.Add(20 * time.Second)
	obs := &fakeObservationLister{obs: []observation.Observation{
		statusObservation("player-01", fppStatusValueIdle, now),
		playlistNameObservation("player-01", "halloween-show", now),
	}}
	h, st := nightLoopTestHandlers(t, func() time.Time { return now }, obs)
	anchor := liveSessionAnchor("player-01", "halloween-show", "halloween-show.fseq", dispatchedAt)
	mustCreateLiveSession(t, st, dispatchedAt, anchor)

	h.nightAdvanceLive(context.Background(), now, mustGetCurrentSession(t, st))

	got := mustGetCurrentSession(t, st)
	if got.State != nightStateLive {
		t.Fatalf("state = %q, want still %q", got.State, nightStateLive)
	}
}

// mutableObservationLister lets a test simulate the collector's next poll
// landing mid-flow: the httptest command server mutates it after
// "receiving" a dispatch, so a confirmation loop that polls
// ObservationLister repeatedly sees the change exactly like a real
// collector poll would.
type mutableObservationLister struct {
	mu  sync.Mutex
	obs []observation.Observation
}

func (m *mutableObservationLister) set(obs []observation.Observation) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.obs = obs
}

func (m *mutableObservationLister) add(obs ...observation.Observation) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.obs = append(m.obs, obs...)
}

func (m *mutableObservationLister) ListObservations(context.Context, ObservationFilter) ([]observation.Observation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]observation.Observation, len(m.obs))
	copy(out, m.obs)
	return out, nil
}

func mustGetCurrentSession(t *testing.T, st *store.Store) store.NightSessionRecord {
	t.Helper()
	rec, ok, err := st.GetCurrentNightSession(context.Background())
	if err != nil || !ok {
		t.Fatalf("get current night session: ok=%v err=%v", ok, err)
	}
	return rec
}

// Pre-show starts the resting playlist with repeat enabled
// (RESTING-MODE.md §5's reference operating day gives ~25 minutes of dark
// house otherwise). This proves the actual dispatch carries repeat=true
// and the persisted anchor's own Purpose is the repeat one (never
// resting-oneshot, which would trip nightBoundaryContradicted's
// repeat-mode-active check on the very next tick).
func TestNightAdvancePreshow_StartsRestingWithRepeatEnabled(t *testing.T) {
	var gotArgs []string
	cmdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Command == "Start Playlist" {
			gotArgs = body.Args
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Playlist Starting"))
	}))
	defer cmdSrv.Close()

	now := time.Date(2026, 10, 31, 16, 30, 0, 0, time.UTC)
	dispatchedAt := now
	obs := &fakeObservationLister{obs: []observation.Observation{
		statusObservation("player-01", fppStatusValuePlaying, dispatchedAt),
		playlistNameObservation("player-01", "halloween-resting", dispatchedAt),
		repeatModeObservation("player-01", true, dispatchedAt),
		positionMSObservation("player-01", 0, dispatchedAt),
	}}
	svc, st, _ := newTestIdentityServiceWithStore(t, func() time.Time { return now })
	deps := Dependencies{
		NightSessions: st, Observations: obs, Identity: svc, Config: st,
		FPP: &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: cmdSrv.URL}}},
	}.withDefaults()
	opts := Options{}.withDefaults()
	h := &handlers{
		deps: deps, clock: func() time.Time { return now }, logger: testLogger(),
		fppCommandConfirmDeadline: opts.FPPCommandConfirmDeadline, fppCommandPollInterval: opts.FPPCommandPollInterval,
	}

	rec := store.NightSessionRecord{ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1, State: nightStatePreshow, StateEnteredAt: now}
	if err := st.CreateNightSession(context.Background(), rec, now); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	recordNightIssuer("sess-1", FPPCommandIssuer{PrincipalID: "operator-1", PrincipalName: "operator-1"})
	t.Cleanup(func() { forgetNightIssuer("sess-1") })

	payload := config.NightSessionPayload{
		Show:         "halloween-2026",
		Label:        "test",
		ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "player-01", Playlist: "halloween-show"},
		Resting:      config.NightSessionResting{FPPInstanceID: "player-01", Playlist: "halloween-resting", EndOfNightPlaylist: "halloween-resting"},
	}
	payloadJSON, err := config.EncodeNightSessionPayload(payload)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	if _, err := st.CreateConfigObject(context.Background(), config.NightSessionConfigKind, "halloween-main"); err != nil {
		t.Fatalf("create config object: %v", err)
	}
	if _, err := st.CreateConfigRevision(context.Background(), store.ConfigRevisionRecord{
		Kind: config.NightSessionConfigKind, ObjectID: "halloween-main", Revision: 1,
		PayloadJSON: payloadJSON, Source: "api",
	}); err != nil {
		t.Fatalf("create config revision: %v", err)
	}

	h.nightAdvancePreshow(context.Background(), now, rec)

	if len(gotArgs) < 2 {
		t.Fatalf("Start Playlist args = %v, want at least [name, repeat]", gotArgs)
	}
	if gotArgs[1] != "true" {
		t.Fatalf("Start Playlist repeat arg = %q, want %q", gotArgs[1], "true")
	}

	got := mustGetCurrentSession(t, st)
	anchor, has := decodeNightContentAnchor(got.ContentAnchorJSON)
	if !has {
		t.Fatal("expected a content anchor to have been persisted")
	}
	if anchor.Purpose != nightAnchorPurposeRestingRepeat {
		t.Fatalf("anchor.Purpose = %q, want %q", anchor.Purpose, nightAnchorPurposeRestingRepeat)
	}
	if !anchor.RepeatMode {
		t.Fatal("anchor.RepeatMode = false, want true")
	}
}

// setupTransitionToShowTest builds a session already in transition-to-show
// (blackout hold already elapsed) with the given pre-seeded observations,
// a capturing fake "Start Playlist" command server, and the standard
// halloween-main config. gotArgs records the args of every dispatched
// "Start Playlist" call — empty means no dispatch was ever attempted
// (refused before the HTTP request, by the primitive's own
// PreDispatchCheck).
func setupTransitionToShowTest(t *testing.T, obsRecords []observation.Observation) (h *handlers, st *store.Store, rec store.NightSessionRecord, gotArgs *[]string, now time.Time) {
	t.Helper()
	gotArgs = new([]string)
	now = time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	obs := &mutableObservationLister{}
	obs.set(obsRecords)
	cmdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Command == "Start Playlist" && len(body.Args) > 0 {
			*gotArgs = body.Args
			// Simulate the collector's next poll landing: the requested
			// playlist is now confirmed playing, with evidence collected
			// at (not before) this fixed test clock's "now" — the exact
			// fence [resolveConfirmationEvidence] checks.
			obs.add(
				statusObservation("player-01", fppStatusValuePlaying, now),
				playlistNameObservation("player-01", body.Args[0], now),
				positionMSObservation("player-01", 0, now),
			)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Playlist Starting"))
	}))
	t.Cleanup(cmdSrv.Close)

	svc, storeInst, _ := newTestIdentityServiceWithStore(t, func() time.Time { return now })
	deps := Dependencies{
		NightSessions: storeInst, Observations: obs, Identity: svc, Config: storeInst,
		FPP: &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: cmdSrv.URL}}},
	}.withDefaults()
	opts := Options{}.withDefaults()
	h = &handlers{
		deps: deps, clock: func() time.Time { return now }, logger: testLogger(),
		fppCommandConfirmDeadline: opts.FPPCommandConfirmDeadline, fppCommandPollInterval: opts.FPPCommandPollInterval,
	}

	// StateEnteredAt well before now: enterShow.blackoutHoldMs (6000 in
	// validShowActionBody's own fixture shape) has already elapsed. E is
	// pinned to the same instant (no enterShow cues are configured in
	// this harness, so the lead is zero and E == StateEnteredAt exactly);
	// LastTickAt is "now" so the clock-jump guard sees a normal gap.
	enteredAt := now.Add(-time.Minute)
	rec = store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateTransitionToShow, StateEnteredAt: enteredAt,
		BoundaryJSON: encodeNightBoundary(nightBoundary{State: nightBoundaryStateArmed, ExpectedAt: &enteredAt, LastTickAt: &now}),
	}
	if err := storeInst.CreateNightSession(context.Background(), rec, now); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	recordNightIssuer("sess-1", FPPCommandIssuer{PrincipalID: "operator-1", PrincipalName: "operator-1"})
	t.Cleanup(func() { forgetNightIssuer("sess-1") })

	payload := config.NightSessionPayload{
		Show:         "halloween-2026",
		Label:        "test",
		ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "player-01", Playlist: "halloween-show"},
		Resting:      config.NightSessionResting{FPPInstanceID: "player-01", Playlist: "halloween-resting", EndOfNightPlaylist: "halloween-resting"},
		EnterShow:    config.NightSessionEnterShow{BlackoutHoldMs: 6000},
	}
	payloadJSON, err := config.EncodeNightSessionPayload(payload)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	if _, err := storeInst.CreateConfigObject(context.Background(), config.NightSessionConfigKind, "halloween-main"); err != nil {
		t.Fatalf("create config object: %v", err)
	}
	if _, err := storeInst.CreateConfigRevision(context.Background(), store.ConfigRevisionRecord{
		Kind: config.NightSessionConfigKind, ObjectID: "halloween-main", Revision: 1,
		PayloadJSON: payloadJSON, Source: "api",
	}); err != nil {
		t.Fatalf("create config revision: %v", err)
	}
	return h, storeInst, rec, gotArgs, now
}

// Replacement is conditional on IDENTITY. Our own
// resting playlist, currently playing, is the one case where replace is
// safe and the transition to live must succeed.
func TestNightAdvanceTransitionToShow_OwnRestingPlaylistRunning_Replaces(t *testing.T) {
	now0 := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	h, st, rec, gotArgs, now := setupTransitionToShowTest(t, []observation.Observation{
		statusObservation("player-01", fppStatusValuePlaying, now0),
		playlistNameObservation("player-01", "halloween-resting", now0),
	})

	h.nightAdvanceTransitionToShow(context.Background(), now, rec)

	if len(*gotArgs) < 1 || (*gotArgs)[0] != "halloween-show" {
		t.Fatalf("Start Playlist args = %v, want [halloween-show, ...] (dispatch must have happened)", *gotArgs)
	}
	got := mustGetCurrentSession(t, st)
	if got.State != nightStateLive {
		t.Fatalf("state = %q, want %q", got.State, nightStateLive)
	}
}

// The case that matters: an UNRELATED playlist is running. Rule 5 forbids
// replacing it silently — this must refuse, name what was observed, and
// never advance out of transition-to-show.
func TestNightAdvanceTransitionToShow_UnrelatedPlaylistRunning_Refuses(t *testing.T) {
	now0 := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	h, st, rec, gotArgs, now := setupTransitionToShowTest(t, []observation.Observation{
		statusObservation("player-01", fppStatusValuePlaying, now0),
		playlistNameObservation("player-01", "someone-elses-playlist", now0),
	})

	h.nightAdvanceTransitionToShow(context.Background(), now, rec)

	if len(*gotArgs) != 0 {
		t.Fatalf("Start Playlist args = %v, want no dispatch at all (refused before FPP was ever contacted)", *gotArgs)
	}
	got := mustGetCurrentSession(t, st)
	if got.State != nightStateTransitionToShow {
		t.Fatalf("state = %q, want still %q (must never cross into live)", got.State, nightStateTransitionToShow)
	}
	boundary, has := decodeNightBoundary(got.BoundaryJSON)
	if !has || boundary.Reason == "" {
		t.Fatal("expected a stated refusal reason")
	}
	if !nightContainsAll(boundary.Reason, "someone-elses-playlist") {
		t.Fatalf("reason %q does not name the observed playlist", boundary.Reason)
	}
}

// No current evidence at all: identity cannot be established, so this
// must refuse exactly like the unrelated-playlist case, with its own
// reason, and never advance.
func TestNightAdvanceTransitionToShow_NoCurrentEvidence_Refuses(t *testing.T) {
	h, st, rec, gotArgs, now := setupTransitionToShowTest(t, nil)

	h.nightAdvanceTransitionToShow(context.Background(), now, rec)

	if len(*gotArgs) != 0 {
		t.Fatalf("Start Playlist args = %v, want no dispatch at all", *gotArgs)
	}
	got := mustGetCurrentSession(t, st)
	if got.State != nightStateTransitionToShow {
		t.Fatalf("state = %q, want still %q", got.State, nightStateTransitionToShow)
	}
	boundary, has := decodeNightBoundary(got.BoundaryJSON)
	if !has || boundary.Reason == "" {
		t.Fatal("expected a stated refusal reason")
	}
}

// setupRestingIntershowTest builds a session in resting-intershow with a
// COMPLETE anchor (ObservedAt set) for the given duration/position, so
// nightAdvanceRestingIntershow's own "anchor already complete" branch is
// what runs.
func setupRestingIntershowTest(t *testing.T, obsRecords func(now time.Time) []observation.Observation, anchor nightContentAnchor, boundaryJSON string) (h *handlers, st *store.Store, rec store.NightSessionRecord, now time.Time) {
	t.Helper()
	now = time.Date(2026, 10, 31, 20, 5, 0, 0, time.UTC)
	obs := &fakeObservationLister{obs: obsRecords(now)}
	svc, storeInst, _ := newTestIdentityServiceWithStore(t, func() time.Time { return now })
	deps := Dependencies{NightSessions: storeInst, Observations: obs, Identity: svc, Config: storeInst}.withDefaults()
	opts := Options{}.withDefaults()
	h = &handlers{
		deps: deps, clock: func() time.Time { return now }, logger: testLogger(),
		fppCommandConfirmDeadline: opts.FPPCommandConfirmDeadline, fppCommandPollInterval: opts.FPPCommandPollInterval,
	}
	rec = store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateRestingIntershow, StateEnteredAt: now.Add(-time.Minute),
		ContentAnchorJSON: encodeNightContentAnchor(anchor), BoundaryJSON: boundaryJSON,
	}
	if err := storeInst.CreateNightSession(context.Background(), rec, now); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	payload := config.NightSessionPayload{
		Show: "halloween-2026", Label: "test",
		ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "player-01", Playlist: "halloween-show"},
		Resting:      config.NightSessionResting{FPPInstanceID: "player-01", Playlist: "halloween-resting", EndOfNightPlaylist: "halloween-resting"},
	}
	payloadJSON, err := config.EncodeNightSessionPayload(payload)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	if _, err := storeInst.CreateConfigObject(context.Background(), config.NightSessionConfigKind, "halloween-main"); err != nil {
		t.Fatalf("create config object: %v", err)
	}
	if _, err := storeInst.CreateConfigRevision(context.Background(), store.ConfigRevisionRecord{
		Kind: config.NightSessionConfigKind, ObjectID: "halloween-main", Revision: 1, PayloadJSON: payloadJSON, Source: "api",
	}); err != nil {
		t.Fatalf("create config revision: %v", err)
	}
	return h, storeInst, rec, now
}

// Finding 1 (blocking): an anchor already marked invalid (persisted
// BoundaryJSON.State) must never be recomputed from on a later tick, even
// when fresh evidence no longer contradicts it — the wall clock has
// already moved past the stale E by the time evidence agrees again, and
// recomputing would arm and fire the show launch off the wrong boundary.
func TestNightAdvanceRestingIntershow_InvalidatedBoundaryNeverRecomputed(t *testing.T) {
	dispatchedAt := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	anchor := nightContentAnchor{
		Purpose: nightAnchorPurposeRestingOneShot, FPPInstanceID: "player-01", Playlist: "halloween-resting",
		Item: "halloween-resting.fseq", DurationMS: 300000, PositionSeconds: 2, PositionMS: 2000, PositionMSKnown: true,
		DispatchedAt: dispatchedAt, ObservedAt: dispatchedAt.Add(time.Second),
	}
	// now (t+5m) is already far past this anchor's own E (~t+298s) — if
	// recomputed, this would arm and immediately fire.
	persistedInvalid := encodeNightBoundary(nightBoundary{State: nightBoundaryStateInvalid, Reason: "playback is paused"})
	h, st, rec, now := setupRestingIntershowTest(t, func(now time.Time) []observation.Observation {
		return []observation.Observation{
			statusObservation("player-01", fppStatusValuePlaying, now),
			playlistNameObservation("player-01", "halloween-resting", now),
			positionMSObservation("player-01", 2000, now),
		}
	}, anchor, persistedInvalid)

	h.nightAdvanceRestingIntershow(context.Background(), now, rec)

	got := mustGetCurrentSession(t, st)
	if got.State != nightStateRestingIntershow {
		t.Fatalf("state = %q, want still %q (an invalidated boundary must never silently re-arm)", got.State, nightStateRestingIntershow)
	}
}

// Finding 1: a contradiction found THIS tick must invalidate the ANCHOR
// (clear its observed half), not only the boundary — otherwise the very
// next tick, seeing agreeing evidence again, recomputes from the same
// stale anchor.
func TestNightAdvanceRestingIntershow_ContradictionInvalidatesAnchorNotOnlyBoundary(t *testing.T) {
	dispatchedAt := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	anchor := nightContentAnchor{
		Purpose: nightAnchorPurposeRestingOneShot, FPPInstanceID: "player-01", Playlist: "halloween-resting",
		Item: "halloween-resting.fseq", DurationMS: 300000, PositionSeconds: 2, PositionMS: 2000, PositionMSKnown: true,
		DispatchedAt: dispatchedAt, ObservedAt: dispatchedAt.Add(time.Second),
	}
	h, st, rec, now := setupRestingIntershowTest(t, func(now time.Time) []observation.Observation {
		return []observation.Observation{
			statusObservation("player-01", fppStatusValuePaused, now),
			playlistNameObservation("player-01", "halloween-resting", now),
		}
	}, anchor, "")

	h.nightAdvanceRestingIntershow(context.Background(), now, rec)

	got := mustGetCurrentSession(t, st)
	boundary, has := decodeNightBoundary(got.BoundaryJSON)
	if !has || boundary.State != nightBoundaryStateInvalid {
		t.Fatalf("boundary state = %+v, want invalid", boundary)
	}
	gotAnchor, hasAnchor := decodeNightContentAnchor(got.ContentAnchorJSON)
	if !hasAnchor {
		t.Fatal("expected the anchor to still be persisted (invalidated, not deleted)")
	}
	if !gotAnchor.ObservedAt.IsZero() {
		t.Fatalf("anchor.ObservedAt = %v, want zero (the anchor itself must be invalidated, not just the boundary)", gotAnchor.ObservedAt)
	}
	if !gotAnchor.DispatchedAt.Equal(dispatchedAt) {
		t.Fatalf("anchor.DispatchedAt = %v, want unchanged %v (no new dispatch)", gotAnchor.DispatchedAt, dispatchedAt)
	}
}

// Finding 2: two additional scenarios the review proved reach replace
// without a positive identification.
func TestNightShowLaunchIfBusy_DifferentInstanceSameNameRefuses(t *testing.T) {
	now := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	obs := &fakeObservationLister{obs: []observation.Observation{
		statusObservation("player-02", fppStatusValuePlaying, now),
		playlistNameObservation("player-02", "halloween-resting", now),
		positionMSObservation("player-02", 1000, now),
	}}
	deps := Dependencies{Observations: obs}.withDefaults()
	h := &handlers{deps: deps, clock: func() time.Time { return now }, logger: testLogger()}
	payload := config.NightSessionPayload{
		ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "player-02", Playlist: "halloween-show"},
		Resting:      config.NightSessionResting{FPPInstanceID: "player-01", Playlist: "halloween-resting"},
	}
	if got := h.nightShowLaunchIfBusy(context.Background(), now, payload); got != fppIfBusyRefuse {
		t.Fatalf("ifBusy = %q, want %q (resting and show run on different FPP instances)", got, fppIfBusyRefuse)
	}
}

func TestNightShowLaunchIfBusy_StaleEvidenceRefuses(t *testing.T) {
	now := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	staleAt := now.Add(-44 * time.Second) // inside fpp.DefaultValidFor (45s), outside our own tighter bound.
	obs := &fakeObservationLister{obs: []observation.Observation{
		statusObservation("player-01", fppStatusValuePlaying, staleAt),
		playlistNameObservation("player-01", "halloween-resting", staleAt),
	}}
	deps := Dependencies{Observations: obs}.withDefaults()
	h := &handlers{deps: deps, clock: func() time.Time { return now }, logger: testLogger()}
	payload := config.NightSessionPayload{
		ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "player-01", Playlist: "halloween-show"},
		Resting:      config.NightSessionResting{FPPInstanceID: "player-01", Playlist: "halloween-resting"},
	}
	if got := h.nightShowLaunchIfBusy(context.Background(), now, payload); got != fppIfBusyRefuse {
		t.Fatalf("ifBusy = %q, want %q (44s-old identity evidence must not license replace)", got, fppIfBusyRefuse)
	}
}

// Finding 3 (blocking): status idle and CURRENT, but no playlist-name
// evidence at all, must not read as completion — absent must never decode
// as F0's genuine idle "".
func TestNightAdvanceLive_Rule4_AbsentPlaylistEvidenceIsNotCompletion(t *testing.T) {
	dispatchedAt := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	now := dispatchedAt.Add(20 * time.Second)
	obs := &fakeObservationLister{obs: []observation.Observation{
		statusObservation("player-01", fppStatusValueIdle, now),
		// No playlist-name observation seeded at all.
	}}
	h, st := nightLoopTestHandlers(t, func() time.Time { return now }, obs)
	anchor := liveSessionAnchor("player-01", "halloween-show", "halloween-show.fseq", dispatchedAt)
	mustCreateLiveSession(t, st, dispatchedAt, anchor)

	h.nightAdvanceLive(context.Background(), now, mustGetCurrentSession(t, st))

	got := mustGetCurrentSession(t, st)
	if got.State != nightStateLive {
		t.Fatalf("state = %q, want still %q (absent playlist evidence must never read as completion)", got.State, nightStateLive)
	}
}

// Finding 7 (blocking): an autonomous dispatch with no attributed
// principal must be refused, not sent with a zero issuer.
func TestNightEnsureAnchor_RefusesDispatchWithNoAttributedPrincipal(t *testing.T) {
	var dispatched bool
	cmdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dispatched = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Playlist Starting"))
	}))
	defer cmdSrv.Close()

	now := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	svc, st, _ := newTestIdentityServiceWithStore(t, func() time.Time { return now })
	obs := &fakeObservationLister{obs: []observation.Observation{
		statusObservation("player-01", fppStatusValueIdle, now),
	}}
	deps := Dependencies{
		NightSessions: st, Observations: obs, Identity: svc,
		FPP: &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: cmdSrv.URL}}},
	}.withDefaults()
	opts := Options{}.withDefaults()
	h := &handlers{
		deps: deps, clock: func() time.Time { return now }, logger: testLogger(),
		fppCommandConfirmDeadline: opts.FPPCommandConfirmDeadline, fppCommandPollInterval: opts.FPPCommandPollInterval,
	}
	rec := store.NightSessionRecord{ID: "no-issuer-sess", State: nightStatePreshow, StateEnteredAt: now}
	// Deliberately never call recordNightIssuer for this session id.

	anchor, ready, changed := h.nightEnsureAnchor(context.Background(), now, rec, nightAnchorPurposeRestingRepeat, "player-01", "halloween-resting", true, 0, fppIfBusyRefuse)
	if dispatched {
		t.Fatal("dispatched startPlaylist with no attributed principal")
	}
	if ready || !changed {
		t.Fatalf("ready=%v changed=%v, want ready=false changed=true (refused, recorded)", ready, changed)
	}
	if anchor.Source == "" {
		t.Fatal("expected a stated refusal reason")
	}
}

// Finding 6: resting.endOfNightRepeat must be read, not hardcoded true.
func TestNightAdvanceTransitionToResting_RespectsEndOfNightRepeatFalse(t *testing.T) {
	var gotArgs []string
	cmdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Command == "Start Playlist" {
			gotArgs = body.Args
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Playlist Starting"))
	}))
	defer cmdSrv.Close()

	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	obs := &fakeObservationLister{obs: []observation.Observation{
		statusObservation("player-01", fppStatusValuePlaying, now),
		playlistNameObservation("player-01", "halloween-resting", now),
		positionMSObservation("player-01", 0, now),
	}}
	svc, st, _ := newTestIdentityServiceWithStore(t, func() time.Time { return now })
	deps := Dependencies{
		NightSessions: st, Observations: obs, Identity: svc, Config: st,
		FPP: &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: cmdSrv.URL}}},
	}.withDefaults()
	opts := Options{}.withDefaults()
	h := &handlers{
		deps: deps, clock: func() time.Time { return now }, logger: testLogger(),
		fppCommandConfirmDeadline: opts.FPPCommandConfirmDeadline, fppCommandPollInterval: opts.FPPCommandPollInterval,
	}
	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateTransitionToResting, StateEnteredAt: now.Add(-time.Minute), FinalShowRequested: true,
	}
	if err := st.CreateNightSession(context.Background(), rec, now); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	recordNightIssuer("sess-1", FPPCommandIssuer{PrincipalID: "operator-1", PrincipalName: "operator-1"})
	t.Cleanup(func() { forgetNightIssuer("sess-1") })

	payload := config.NightSessionPayload{
		Show: "halloween-2026", Label: "test",
		ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "player-01", Playlist: "halloween-show"},
		Resting: config.NightSessionResting{
			FPPInstanceID: "player-01", Playlist: "halloween-resting", EndOfNightPlaylist: "halloween-resting",
			EndOfNightRepeat: false,
		},
	}
	payloadJSON, err := config.EncodeNightSessionPayload(payload)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	if _, err := st.CreateConfigObject(context.Background(), config.NightSessionConfigKind, "halloween-main"); err != nil {
		t.Fatalf("create config object: %v", err)
	}
	if _, err := st.CreateConfigRevision(context.Background(), store.ConfigRevisionRecord{
		Kind: config.NightSessionConfigKind, ObjectID: "halloween-main", Revision: 1, PayloadJSON: payloadJSON, Source: "api",
	}); err != nil {
		t.Fatalf("create config revision: %v", err)
	}

	h.nightAdvanceTransitionToResting(context.Background(), now, rec)

	if len(gotArgs) < 2 {
		t.Fatalf("Start Playlist args = %v, want at least [name, repeat]", gotArgs)
	}
	if gotArgs[1] != "false" {
		t.Fatalf("Start Playlist repeat arg = %q, want %q (resting.endOfNightRepeat: false)", gotArgs[1], "false")
	}
}

// Finding 10: Run must not return while a tick is still in flight, or the
// coordinator's shutdown sequence can close the store out from under a
// goroutine still writing to it.
func TestNightLoopRun_WaitsForInFlightTickBeforeReturning(t *testing.T) {
	l := &NightLoop{interval: time.Hour, inFlight: make(chan struct{}, 1)}
	l.inFlight <- struct{}{} // simulate a tick already in flight.

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		l.Run(ctx)
		close(done)
	}()
	cancel()

	select {
	case <-done:
		t.Fatal("Run returned while a tick was still in flight")
	case <-time.After(100 * time.Millisecond):
	}

	<-l.inFlight // the simulated tick "finishes."

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the in-flight tick released")
	}
}

// Finding 11: a backward clock step beyond nightClockBackstepTolerance
// invalidates an armed boundary rather than leaving it to misfire once
// wall time settles.
func TestNightAdvanceRestingIntershow_BackwardClockStepInvalidates(t *testing.T) {
	dispatchedAt := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	observedAt := dispatchedAt.Add(time.Second)
	anchor := nightContentAnchor{
		Purpose: nightAnchorPurposeRestingOneShot, FPPInstanceID: "player-01", Playlist: "halloween-resting",
		Item: "halloween-resting.fseq", DurationMS: 300000, PositionSeconds: 2, PositionMS: 2000, PositionMSKnown: true,
		DispatchedAt: dispatchedAt, ObservedAt: observedAt,
	}
	// now is BEFORE observedAt by more than nightClockBackstepTolerance —
	// a clock correction, not measurement noise.
	backward := observedAt.Add(-nightClockBackstepTolerance - time.Second)
	h, st, rec, _ := setupRestingIntershowTest(t, func(now time.Time) []observation.Observation {
		return []observation.Observation{
			statusObservation("player-01", fppStatusValuePlaying, now),
			playlistNameObservation("player-01", "halloween-resting", now),
			positionMSObservation("player-01", 2000, now),
		}
	}, anchor, "")

	h.nightAdvanceRestingIntershow(context.Background(), backward, rec)

	got := mustGetCurrentSession(t, st)
	if got.State != nightStateRestingIntershow {
		t.Fatalf("state = %q, want still %q", got.State, nightStateRestingIntershow)
	}
	boundary, has := decodeNightBoundary(got.BoundaryJSON)
	if !has || boundary.State != nightBoundaryStateInvalid {
		t.Fatalf("boundary = %+v, want invalid", boundary)
	}
}
