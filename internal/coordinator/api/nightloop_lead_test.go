package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Track F seam F4 review fix: RESTING-MODE.md §7.1's pre-boundary lead.
// transition-to-show must be entered at E minus the largest negative
// enterShow cue offset, and every cue offset — plus the show-launch hold —
// must run relative to E, never to this state's own (earlier) entry time.
// These tests pin both halves and were verified to fail against the
// pre-fix behavior (see this seam's own report).

// TestNightAdvanceRestingIntershow_EntersTransitionToShowAtLeadTime
// defends the FIRST half: with a 20-second lead cue configured, the state
// must not advance a tick before E-20s, and must advance once at or past
// it — never waiting for E itself.
func TestNightAdvanceRestingIntershow_EntersTransitionToShowAtLeadTime(t *testing.T) {
	e := time.Date(2026, 10, 31, 20, 5, 0, 0, time.UTC)
	dispatchedAt := e.Add(-300 * time.Second)
	anchor := nightContentAnchor{
		Purpose: nightAnchorPurposeRestingOneShot, FPPInstanceID: "player-01", Playlist: "halloween-resting",
		Item: "halloween-resting.fseq", DurationMS: 300000, PositionSeconds: 0, PositionMS: 0, PositionMSKnown: true,
		DispatchedAt: dispatchedAt, ObservedAt: dispatchedAt,
	}

	run := func(t *testing.T, now time.Time) store.NightSessionRecord {
		t.Helper()
		obs := &fakeObservationLister{obs: []observation.Observation{
			statusObservation("player-01", fppStatusValuePlaying, now),
			playlistNameObservation("player-01", "halloween-resting", now),
			positionMSObservation("player-01", 0, now),
		}}
		svc, storeInst, _ := newTestIdentityServiceWithStore(t, func() time.Time { return now })
		deps := Dependencies{NightSessions: storeInst, Observations: obs, Identity: svc, Config: storeInst}.withDefaults()
		h := &handlers{deps: deps, clock: func() time.Time { return now }, logger: testLogger()}
		rec := store.NightSessionRecord{
			ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
			State: nightStateRestingIntershow, StateEnteredAt: dispatchedAt,
			ContentAnchorJSON: encodeNightContentAnchor(anchor),
		}
		if err := storeInst.CreateNightSession(context.Background(), rec, now); err != nil {
			t.Fatalf("create night session: %v", err)
		}
		payload := config.NightSessionPayload{
			Show: "halloween-2026", Label: "test",
			ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "player-01", Playlist: "halloween-show"},
			Resting:      config.NightSessionResting{FPPInstanceID: "player-01", Playlist: "halloween-resting", EndOfNightPlaylist: "halloween-resting"},
			EnterShow:    config.NightSessionEnterShow{Cues: []config.NightSessionCue{{Name: "lights", Role: config.NightSessionCueRoleLighting, Action: "act-1", OffsetMs: -20000, OnFailure: config.NightSessionCueOnFailureContinue}}},
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
		h.nightAdvanceRestingIntershow(context.Background(), now, rec)
		got, ok, err := storeInst.GetCurrentNightSession(context.Background())
		if err != nil || !ok {
			t.Fatalf("get current session: ok=%v err=%v", ok, err)
		}
		return got
	}

	t.Run("before E-20s: still resting-intershow", func(t *testing.T) {
		got := run(t, e.Add(-21*time.Second))
		if got.State != nightStateRestingIntershow {
			t.Fatalf("state = %q, want still %q (the 20s lead has not elapsed)", got.State, nightStateRestingIntershow)
		}
	})

	t.Run("at E-19s: already in transition-to-show, and E itself is preserved", func(t *testing.T) {
		got := run(t, e.Add(-19*time.Second))
		if got.State != nightStateTransitionToShow {
			t.Fatalf("state = %q, want %q (the 20s lead has elapsed, 19s < 20s before E)", got.State, nightStateTransitionToShow)
		}
		b, ok := decodeNightBoundary(got.BoundaryJSON)
		if !ok || b.ExpectedAt == nil {
			t.Fatal("BoundaryJSON does not carry E")
		}
		if !b.ExpectedAt.Equal(e) {
			t.Fatalf("persisted E = %v, want %v — the state entered EARLY but E itself must be unchanged", *b.ExpectedAt, e)
		}
	})
}

// TestNightAdvanceTransitionToShow_CueAndLaunchAreRelativeToE_NotEntryTime
// defends the SECOND half against the exact regression this seam's
// review found: with the state entered 20s before E (a 20s lead cue), the
// cue must fire immediately at entry (offset -20000ms from E), and the
// show launch must wait for E+blackoutHoldMs — NOT
// StateEnteredAt+blackoutHoldMs, which arrives 20s earlier and would
// launch the show before the resting FSEQ has even finished.
func TestNightAdvanceTransitionToShow_CueAndLaunchAreRelativeToE_NotEntryTime(t *testing.T) {
	e := time.Date(2026, 10, 31, 20, 5, 0, 0, time.UTC)
	enteredAt := e.Add(-20 * time.Second) // the 20s lead.
	now := enteredAt

	obs := &mutableObservationLister{}
	gotArgs := new([]string)
	cmdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Command == "Start Playlist" && len(body.Args) > 0 {
			*gotArgs = body.Args
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
	dispatcher := &fakeResolumeActionDispatcher{
		results: map[string]ResolumeActionResult{config.ShowActionResolumeBlackout: {Outcome: ResolumeOutcomeConfirmed, Dispatched: true, Reason: "dark"}},
	}
	deps := Dependencies{
		NightSessions: storeInst, Observations: obs, Identity: svc, Config: storeInst,
		FPP:             &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: cmdSrv.URL}}},
		ResolumeActions: dispatcher,
	}.withDefaults()
	opts := Options{}.withDefaults()
	h := &handlers{
		deps: deps, clock: func() time.Time { return now }, logger: testLogger(),
		fppCommandConfirmDeadline: opts.FPPCommandConfirmDeadline, fppCommandPollInterval: opts.FPPCommandPollInterval,
	}

	putNightAction(t, storeInst, "act-blackout", blackoutResolumeAction())

	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateTransitionToShow, StateEnteredAt: enteredAt, Cycle: 1,
		BoundaryJSON: encodeNightBoundary(nightBoundary{State: nightBoundaryStateArmed, ExpectedAt: &e}),
	}
	if err := storeInst.CreateNightSession(context.Background(), rec, now); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	recordNightIssuer("sess-1", FPPCommandIssuer{PrincipalID: "operator-1", PrincipalName: "operator-1"})
	t.Cleanup(func() { forgetNightIssuer("sess-1") })

	payload := config.NightSessionPayload{
		Show: "halloween-2026", Label: "test",
		ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "player-01", Playlist: "halloween-show"},
		Resting:      config.NightSessionResting{FPPInstanceID: "player-01", Playlist: "halloween-resting", EndOfNightPlaylist: "halloween-resting"},
		EnterShow: config.NightSessionEnterShow{
			BlackoutHoldMs: 6000,
			Cues:           []config.NightSessionCue{{Name: "lights", Role: config.NightSessionCueRoleLighting, Action: "act-blackout", OffsetMs: -20000, Barrier: true, OnFailure: config.NightSessionCueOnFailureContinue}},
		},
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

	// Step 1: at entry (now == enteredAt == E-20s), the lead cue is due
	// immediately (its offset -20000ms exactly matches E-now), and the
	// show must NOT launch (hold has not elapsed from E in any reading).
	h.nightAdvanceTransitionToShow(context.Background(), now, mustGetCurrentSession(t, storeInst))
	if n := dispatcher.callCount(); n != 1 {
		t.Fatalf("resolume dispatcher called %d times at entry, want exactly 1 (the lead cue must fire immediately)", n)
	}
	if len(*gotArgs) != 0 {
		t.Fatalf("Start Playlist args = %v, want no dispatch yet", *gotArgs)
	}

	// Step 2: 6s after ENTRY (enteredAt+6s == E-14s). Under the pre-fix
	// behavior (hold measured from StateEnteredAt) this satisfies
	// blackoutHoldMs and would launch the show 14 SECONDS BEFORE the
	// resting FSEQ itself ends. It must not launch here.
	now = enteredAt.Add(6 * time.Second)
	h.nightAdvanceTransitionToShow(context.Background(), now, mustGetCurrentSession(t, storeInst))
	if len(*gotArgs) != 0 {
		t.Fatalf("Start Playlist args = %v at E-14s, want no dispatch — the hold must be measured from E, not from this state's entry time", *gotArgs)
	}
	got := mustGetCurrentSession(t, storeInst)
	if got.State != nightStateTransitionToShow {
		t.Fatalf("state = %q at E-14s, want still %q", got.State, nightStateTransitionToShow)
	}

	// Step 3: at E+6s, the hold measured from E has now elapsed, and the
	// (already-resolved, barrier) cue does not block it. The show launches.
	// The resting FSEQ has finished by E, so the player is idle — the
	// same identity evidence a real handoff would have.
	now = e.Add(6 * time.Second)
	obs.set([]observation.Observation{
		statusObservation("player-01", fppStatusValueIdle, now),
	})
	h.nightAdvanceTransitionToShow(context.Background(), now, mustGetCurrentSession(t, storeInst))
	if len(*gotArgs) < 1 || (*gotArgs)[0] != "halloween-show" {
		t.Fatalf("Start Playlist args = %v at E+6s, want [halloween-show, ...]", *gotArgs)
	}
	got = mustGetCurrentSession(t, storeInst)
	if got.State != nightStateLive {
		t.Fatalf("state = %q at E+6s, want %q", got.State, nightStateLive)
	}
	if n := dispatcher.callCount(); n != 1 {
		t.Errorf("resolume dispatcher called %d times total, want exactly 1 (no re-dispatch of an already-resolved cue)", n)
	}
}

// --- barrier deadline (orchestrator review fix): an unbounded barrier
// wait means the show never launches when an adapter never reports
// completion (Track C's own audio-session defect is the evidence this
// review cites — ADR-035/ADR-031 decision 2/ADR-029 decision 4 all agree a
// monitoring gap must never stop a show). These tests exercise the real
// end-to-end path, including a genuinely stuck fpp command (a command row
// whose own ResultJSON was never written — the "replay observed
// mid-flight" case dispatchFPPCommand's own doc comment names), not a
// simplified stand-in.

// stalledFPPBarrierTest builds a transition-to-show session with one
// barrier cue whose fpp command is stuck: night_cue_outbox holds it
// nightCueStateDispatched, and the underlying commands row it points at
// (same idempotency key) has an empty ResultJSON — so every replay
// (nightResumeCueRow's own retry-by-identity path) finds it, not FPP,
// and comes back with Outcome=="" forever, exactly the shape a
// permanently wedged confirmation predicate produces. onFailure sets the
// stalled cue's own failure policy.
func stalledFPPBarrierTest(t *testing.T, onFailure string) (h *handlers, storeInst *store.Store, e time.Time, now *time.Time, obs *mutableObservationLister, gotArgs *[]string) {
	t.Helper()
	e = time.Date(2026, 10, 31, 20, 5, 0, 0, time.UTC)
	now = new(time.Time)
	*now = e

	obs = &mutableObservationLister{}
	gotArgs = new([]string)
	cmdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Command == "Start Playlist" && len(body.Args) > 0 {
			*gotArgs = body.Args
			obs.add(
				statusObservation("player-01", fppStatusValuePlaying, *now),
				playlistNameObservation("player-01", body.Args[0], *now),
				positionMSObservation("player-01", 0, *now),
			)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Playlist Starting"))
	}))
	t.Cleanup(cmdSrv.Close)

	svc, storeInst, _ := newTestIdentityServiceWithStore(t, func() time.Time { return *now })
	deps := Dependencies{
		NightSessions: storeInst, Observations: obs, Identity: svc, Config: storeInst,
		FPP: &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: cmdSrv.URL}}},
	}.withDefaults()
	opts := Options{}.withDefaults()
	h = &handlers{
		deps: deps, clock: func() time.Time { return *now }, logger: testLogger(),
		fppCommandConfirmDeadline: opts.FPPCommandConfirmDeadline, fppCommandPollInterval: opts.FPPCommandPollInterval,
	}

	putNightAction(t, storeInst, "act-stop", config.ShowActionPayload{
		Show: "halloween", Label: "Stop", SafetyClass: config.ShowSafetyClassStop,
		Target: config.ShowActionTarget{Integration: config.ShowActionIntegrationFPP, InstanceID: "player-01", Primitive: "stopPlaylist", Params: map[string]any{}},
	})

	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateTransitionToShow, StateEnteredAt: e, Cycle: 1, ShowCommitted: true,
		BoundaryJSON: encodeNightBoundary(nightBoundary{State: nightBoundaryStateArmed, ExpectedAt: &e}),
	}
	if err := storeInst.CreateNightSession(context.Background(), rec, *now); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	recordNightIssuer("sess-1", FPPCommandIssuer{PrincipalID: "operator-1", PrincipalName: "operator-1"})
	t.Cleanup(func() { forgetNightIssuer("sess-1") })

	payload := config.NightSessionPayload{
		Show: "halloween-2026", Label: "test",
		ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "player-01", Playlist: "halloween-show"},
		Resting:      config.NightSessionResting{FPPInstanceID: "player-01", Playlist: "halloween-resting", EndOfNightPlaylist: "halloween-resting"},
		EnterShow: config.NightSessionEnterShow{
			BlackoutHoldMs: 6000,
			Cues:           []config.NightSessionCue{{Name: "stop-something", Role: config.NightSessionCueRoleOther, Action: "act-stop", OffsetMs: 0, Barrier: true, OnFailure: onFailure}},
		},
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

	idemKey := nightCueIdempotencyKey(rec.ID, rec.Cycle, nightPhaseEnterShow, "stop-something")
	if err := storeInst.InsertNightCueOutboxRow(context.Background(), store.NightCueOutboxRecord{
		ID: "outbox-1", SessionID: rec.ID, Cycle: rec.Cycle, Phase: nightPhaseEnterShow, CueName: "stop-something",
		ActionRevision: 1, State: nightCueStateDispatched, DispatchedAt: &e,
	}, e); err != nil {
		t.Fatalf("seed outbox row: %v", err)
	}
	paramsJSON, err := canonicalParamsJSON(map[string]any{})
	if err != nil {
		t.Fatalf("canonicalParamsJSON: %v", err)
	}
	if _, err := storeInst.InsertCommand(context.Background(), store.CommandRecord{
		ID: "cmd-1", IdempotencyKey: idemKey, Action: auditActionFPPStopPlaylist,
		TargetKind: "fpp", TargetID: "player-01", ParamsJSON: paramsJSON,
		IssuerPrincipalID: "operator-1", IssuerPrincipalName: "operator-1",
		ConfirmationMethod: "evidence", State: "dispatched", DispatchedAt: &e,
		// ResultJSON deliberately empty: this is the "replay observed
		// mid-flight" shape — no outcome was ever recorded for this
		// command, matching a crash mid-dispatchFPPCommand.
	}); err != nil {
		t.Fatalf("seed stuck command row: %v", err)
	}

	// One tick well before the deadline: nothing should change except the
	// stuck cue being (harmlessly) re-checked via replay.
	h.nightAdvanceTransitionToShow(context.Background(), *now, mustGetCurrentSession(t, storeInst))
	if len(*gotArgs) != 0 {
		t.Fatalf("Start Playlist args = %v before the deadline, want none", *gotArgs)
	}
	row, err := storeInst.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, nightPhaseEnterShow, "stop-something")
	if err != nil {
		t.Fatalf("GetNightCueOutboxRow: %v", err)
	}
	if row.State != nightCueStateDispatched {
		t.Fatalf("outbox row state before the deadline = %q, want still %q (replay must not have resolved it)", row.State, nightCueStateDispatched)
	}
	return h, storeInst, e, now, obs, gotArgs
}

// TestNightAdvanceTransitionToShow_StalledBarrierCueTimesOutAndShowLaunches
// is the orchestrator's own acceptance case: a barrier cue that never
// resolves must not hold the show forever. Past nightBarrierResolutionDeadline
// the cue resolves unconfirmed (recorded, not silently dropped) and,
// with onFailure=continue, the show launches anyway.
func TestNightAdvanceTransitionToShow_StalledBarrierCueTimesOutAndShowLaunches(t *testing.T) {
	h, storeInst, e, now, obs, gotArgs := stalledFPPBarrierTest(t, config.NightSessionCueOnFailureContinue)

	*now = e.Add(nightBarrierResolutionDeadline + time.Second)
	obs.set([]observation.Observation{statusObservation("player-01", fppStatusValueIdle, *now)})

	h.nightAdvanceTransitionToShow(context.Background(), *now, mustGetCurrentSession(t, storeInst))

	if len(*gotArgs) < 1 || (*gotArgs)[0] != "halloween-show" {
		t.Fatalf("Start Playlist args = %v past the deadline, want [halloween-show, ...] — a stalled barrier cue must not hold the show forever", *gotArgs)
	}
	got := mustGetCurrentSession(t, storeInst)
	if got.State != nightStateLive {
		t.Fatalf("state = %q, want %q", got.State, nightStateLive)
	}
	row, err := storeInst.GetNightCueOutboxRow(context.Background(), "sess-1", 1, nightPhaseEnterShow, "stop-something")
	if err != nil {
		t.Fatalf("GetNightCueOutboxRow: %v", err)
	}
	if row.State != nightCueStateResolved || row.Outcome != nightCueOutcomeUnconfirmed {
		t.Errorf("stalled cue row = state %q outcome %q, want resolved/unconfirmed (recorded, not silently dropped)", row.State, row.Outcome)
	}
	if !nightContainsAll(row.OutcomeReason, "stop-something") {
		t.Errorf("outcome reason %q does not name the stalled cue", row.OutcomeReason)
	}
}

// TestNightAdvanceTransitionToShow_StalledBarrierCueWithAbort_NeverLaunches
// is the same stalled cue with onFailure=abort: the deadline still
// resolves the cue (it is recorded, not left hanging forever), but an
// explicit abort policy means that resolution does not satisfy the
// barrier, and the show must not launch.
func TestNightAdvanceTransitionToShow_StalledBarrierCueWithAbort_NeverLaunches(t *testing.T) {
	h, storeInst, e, now, obs, gotArgs := stalledFPPBarrierTest(t, config.NightSessionCueOnFailureAbort)

	*now = e.Add(nightBarrierResolutionDeadline + time.Second)
	obs.set([]observation.Observation{statusObservation("player-01", fppStatusValueIdle, *now)})

	h.nightAdvanceTransitionToShow(context.Background(), *now, mustGetCurrentSession(t, storeInst))

	if len(*gotArgs) != 0 {
		t.Fatalf("Start Playlist args = %v, want none — onFailure=abort must hold the launch", *gotArgs)
	}
	got := mustGetCurrentSession(t, storeInst)
	if got.State != nightStateTransitionToShow {
		t.Fatalf("state = %q, want still %q", got.State, nightStateTransitionToShow)
	}
	row, err := storeInst.GetNightCueOutboxRow(context.Background(), "sess-1", 1, nightPhaseEnterShow, "stop-something")
	if err != nil {
		t.Fatalf("GetNightCueOutboxRow: %v", err)
	}
	if row.State != nightCueStateResolved || row.Outcome != nightCueOutcomeUnconfirmed {
		t.Errorf("stalled cue row = state %q outcome %q, want resolved/unconfirmed even though it still blocks", row.State, row.Outcome)
	}
	boundary, ok := decodeNightBoundary(got.BoundaryJSON)
	if !ok || !nightContainsAll(boundary.Reason, "abort") {
		t.Errorf("boundary reason %+v does not name the abort policy blocking the launch", boundary)
	}
}

// --- review round 2: findings 7 and 8 ---

// contradictionSupervisionTest builds a transition-to-show session whose
// ContentAnchorJSON still carries the resting-oneshot anchor that armed
// E, with contradicting evidence (the anchor implies playing; the
// observation reports paused) queued up. showCommitted controls whether
// finding 7's pre-commit supervision is reachable at all.
func contradictionSupervisionTest(t *testing.T, showCommitted bool) (h *handlers, st *store.Store, rec store.NightSessionRecord, now time.Time) {
	t.Helper()
	now = time.Date(2026, 10, 31, 20, 5, 0, 0, time.UTC)
	dispatchedAt := now.Add(-4 * time.Minute)
	anchor := nightContentAnchor{
		Purpose: nightAnchorPurposeRestingOneShot, FPPInstanceID: "player-01", Playlist: "halloween-resting",
		Item: "halloween-resting.fseq", DurationMS: 300000, PositionSeconds: 2, PositionMS: 2000, PositionMSKnown: true,
		DispatchedAt: dispatchedAt, ObservedAt: dispatchedAt.Add(time.Second),
	}
	obs := &fakeObservationLister{obs: []observation.Observation{
		statusObservation("player-01", fppStatusValuePaused, now),
	}}
	svc, storeInst, _ := newTestIdentityServiceWithStore(t, func() time.Time { return now })
	deps := Dependencies{NightSessions: storeInst, Observations: obs, Identity: svc, Config: storeInst}.withDefaults()
	h = &handlers{deps: deps, clock: func() time.Time { return now }, logger: testLogger()}

	e := now.Add(time.Hour) // far enough out that hold never elapses.
	rec = store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateTransitionToShow, StateEnteredAt: now, Cycle: 1, ShowCommitted: showCommitted,
		ContentAnchorJSON: encodeNightContentAnchor(anchor),
		BoundaryJSON:      encodeNightBoundary(nightBoundary{State: nightBoundaryStateArmed, ExpectedAt: &e, LastTickAt: &now}),
	}
	if err := storeInst.CreateNightSession(context.Background(), rec, now); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	recordNightIssuer("sess-1", FPPCommandIssuer{PrincipalID: "operator-1", PrincipalName: "operator-1"})
	t.Cleanup(func() { forgetNightIssuer("sess-1") })

	payload := config.NightSessionPayload{
		Show: "halloween-2026", Label: "test",
		ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "player-01", Playlist: "halloween-show"},
		Resting:      config.NightSessionResting{FPPInstanceID: "player-01", Playlist: "halloween-resting", EndOfNightPlaylist: "halloween-resting"},
		EnterShow:    config.NightSessionEnterShow{BlackoutHoldMs: 3600000},
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

// TestNightAdvanceTransitionToShow_ContradictionBeforeCommitCancels
// defends finding 7: the resting playback stays supervised through the
// lead window. Contradicting evidence found before the first cue commits
// cancels the armed boundary and reverts to resting-intershow, exactly
// as it would have there. Mutation-checked: gating this check on
// rec.ShowCommitted with the condition inverted, or removing it, makes
// this fail (state stays transition-to-show).
func TestNightAdvanceTransitionToShow_ContradictionBeforeCommitCancels(t *testing.T) {
	h, st, rec, now := contradictionSupervisionTest(t, false)

	h.nightAdvanceTransitionToShow(context.Background(), now, rec)

	got := mustGetCurrentSession(t, st)
	if got.State != nightStateRestingIntershow {
		t.Fatalf("state = %q, want %q — a contradiction before commit must cancel the armed boundary", got.State, nightStateRestingIntershow)
	}
	anchor, has := decodeNightContentAnchor(got.ContentAnchorJSON)
	if !has || !anchor.ObservedAt.IsZero() {
		t.Errorf("anchor = %+v, want invalidated (ObservedAt zero)", anchor)
	}
	boundary, has := decodeNightBoundary(got.BoundaryJSON)
	if !has || boundary.State != nightBoundaryStateInvalid {
		t.Errorf("boundary = %+v, want invalid", boundary)
	}
}

// TestNightAdvanceTransitionToShow_ContradictionAfterCommitDoesNotReverse
// is finding 7's other half: once the first cue has committed, the SAME
// contradicting evidence must never reverse a transition the audience may
// already have seen (§7.1.1). Mutation-checked: removing the
// `!rec.ShowCommitted` guard makes this fail (state reverts anyway).
func TestNightAdvanceTransitionToShow_ContradictionAfterCommitDoesNotReverse(t *testing.T) {
	h, st, rec, now := contradictionSupervisionTest(t, true)

	h.nightAdvanceTransitionToShow(context.Background(), now, rec)

	got := mustGetCurrentSession(t, st)
	if got.State != nightStateTransitionToShow {
		t.Fatalf("state = %q, want still %q — a committed show must never reverse", got.State, nightStateTransitionToShow)
	}
}

// TestNightAdvanceTransitionToShow_ClockJumpResyncsInsteadOfActingEarly
// defends finding 8: a forward clock discontinuity must not simultaneously
// satisfy the hold and time out every dispatched barrier cue. Instead of
// launching, the checkpoint resyncs; the FOLLOWING tick, with a normal
// gap, evaluates normally and launches once genuinely ready. Mutation-
// checked: removing the forward half of the guard condition makes the
// first assertion fail (the show launches immediately on the jump).
func TestNightAdvanceTransitionToShow_ClockJumpResyncsInsteadOfActingEarly(t *testing.T) {
	e := time.Date(2026, 10, 31, 20, 5, 0, 0, time.UTC)
	now := new(time.Time)
	*now = e

	obs := &mutableObservationLister{}
	gotArgs := new([]string)
	cmdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Command == "Start Playlist" && len(body.Args) > 0 {
			*gotArgs = body.Args
			obs.add(
				statusObservation("player-01", fppStatusValuePlaying, *now),
				playlistNameObservation("player-01", body.Args[0], *now),
				positionMSObservation("player-01", 0, *now),
			)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Playlist Starting"))
	}))
	t.Cleanup(cmdSrv.Close)

	svc, storeInst, _ := newTestIdentityServiceWithStore(t, func() time.Time { return *now })
	deps := Dependencies{
		NightSessions: storeInst, Observations: obs, Identity: svc, Config: storeInst,
		FPP: &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: cmdSrv.URL}}},
	}.withDefaults()
	opts := Options{}.withDefaults()
	h := &handlers{
		deps: deps, clock: func() time.Time { return *now }, logger: testLogger(),
		fppCommandConfirmDeadline: opts.FPPCommandConfirmDeadline, fppCommandPollInterval: opts.FPPCommandPollInterval,
	}

	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateTransitionToShow, StateEnteredAt: e, Cycle: 1, ShowCommitted: true,
		BoundaryJSON: encodeNightBoundary(nightBoundary{State: nightBoundaryStateArmed, ExpectedAt: &e, LastTickAt: &e}),
	}
	if err := storeInst.CreateNightSession(context.Background(), rec, e); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	recordNightIssuer("sess-1", FPPCommandIssuer{PrincipalID: "operator-1", PrincipalName: "operator-1"})
	t.Cleanup(func() { forgetNightIssuer("sess-1") })

	payload := config.NightSessionPayload{
		Show: "halloween-2026", Label: "test",
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
		Kind: config.NightSessionConfigKind, ObjectID: "halloween-main", Revision: 1, PayloadJSON: payloadJSON, Source: "api",
	}); err != nil {
		t.Fatalf("create config revision: %v", err)
	}

	// A forward jump well past both the hold and the barrier deadline, in
	// one step.
	*now = e.Add(5 * time.Minute)
	h.nightAdvanceTransitionToShow(context.Background(), *now, mustGetCurrentSession(t, storeInst))
	if len(*gotArgs) != 0 {
		t.Fatalf("Start Playlist args = %v on the jump tick, want none — a clock jump must not act early", *gotArgs)
	}
	got := mustGetCurrentSession(t, storeInst)
	if got.State != nightStateTransitionToShow {
		t.Fatalf("state = %q after the jump, want still %q", got.State, nightStateTransitionToShow)
	}
	b, ok := decodeNightBoundary(got.BoundaryJSON)
	if !ok || b.LastTickAt == nil || !b.LastTickAt.Equal(*now) {
		t.Fatalf("LastTickAt = %+v, want resynced to %v", b.LastTickAt, *now)
	}

	// The NEXT tick, one ordinary interval later, evaluates normally and
	// launches.
	*now = now.Add(time.Second)
	obs.set([]observation.Observation{statusObservation("player-01", fppStatusValueIdle, *now)})
	h.nightAdvanceTransitionToShow(context.Background(), *now, mustGetCurrentSession(t, storeInst))
	if len(*gotArgs) < 1 || (*gotArgs)[0] != "halloween-show" {
		t.Fatalf("Start Playlist args = %v on the next ordinary tick, want [halloween-show, ...]", *gotArgs)
	}
	got = mustGetCurrentSession(t, storeInst)
	if got.State != nightStateLive {
		t.Fatalf("state = %q, want %q", got.State, nightStateLive)
	}
}
