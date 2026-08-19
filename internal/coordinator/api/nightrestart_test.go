package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Startup reconciliation: RESTING-MODE.md §11's three resumable cases
// resume from fresh evidence, and only genuinely ambiguous evidence
// degrades.

func nightRestartFixture(t *testing.T, now time.Time, rec store.NightSessionRecord, obs []observation.Observation) (*store.Store, Dependencies) {
	t.Helper()
	_, st, _ := newTestIdentityServiceWithStore(t, func() time.Time { return now })
	if err := st.CreateNightSession(context.Background(), rec, now); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	deps := Dependencies{
		NightSessions: st, Config: st,
		Observations: &fakeObservationLister{obs: obs},
	}.withDefaults()
	return st, deps
}

func nightPlayingObservations(instanceID, playlist string, at time.Time) []observation.Observation {
	return []observation.Observation{
		statusObservation(instanceID, fppStatusValuePlaying, at),
		playlistNameObservation(instanceID, playlist, at),
		positionMSObservation(instanceID, 30000, at),
	}
}

func nightRestartAnchor(purpose, playlist string, dispatchedAt time.Time) nightContentAnchor {
	return nightContentAnchor{
		Purpose: purpose, FPPInstanceID: "player-01", Playlist: playlist,
		Item: playlist + ".fseq", DurationMS: 300000,
		PositionMS: 10000, PositionMSKnown: true,
		DispatchedAt: dispatchedAt, ObservedAt: dispatchedAt.Add(time.Second),
	}
}

func mustReconcile(t *testing.T, deps Dependencies, now time.Time) {
	t.Helper()
	if err := ReconcileNightSessionOnStartup(context.Background(), deps, func() time.Time { return now }, testLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

// A restart while the show playlist is still playing observes without
// interrupting it: the session stays live and nothing is dispatched.
func TestReconcileOnStartup_LivePlaybackResumesObservation(t *testing.T) {
	started := time.Date(2026, 10, 31, 21, 0, 0, 0, time.UTC)
	now := started.Add(2 * time.Minute)
	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateLive, StateEnteredAt: started, Cycle: 1,
		ContentAnchorJSON: encodeNightContentAnchor(nightRestartAnchor(nightAnchorPurposeShow, "halloween-show", started)),
	}
	st, deps := nightRestartFixture(t, now, rec, nightPlayingObservations("player-01", "halloween-show", now))

	mustReconcile(t, deps, now)

	got := mustGetCurrentSession(t, st)
	if got.Degraded {
		t.Fatalf("a restart during undisturbed live playback degraded the session: %q", got.DegradedReason)
	}
	if got.State != nightStateLive {
		t.Fatalf("state = %q, want still live", got.State)
	}
}

// A restart that finds the show already finished is not a contradiction:
// the session stays resumable and the loop's own completion check advances
// it on the next tick.
func TestReconcileOnStartup_LivePlaybackCompletedDuringTheRestartAdvances(t *testing.T) {
	started := time.Date(2026, 10, 31, 21, 0, 0, 0, time.UTC)
	now := started.Add(2 * time.Minute)
	anchor := nightRestartAnchor(nightAnchorPurposeShow, "halloween-show", started)
	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateLive, StateEnteredAt: started, Cycle: 1,
		ContentAnchorJSON: encodeNightContentAnchor(anchor),
	}
	obs := []observation.Observation{
		statusObservation("player-01", fppStatusValueIdle, now),
		playlistNameObservation("player-01", "", now),
	}
	st, deps := nightRestartFixture(t, now, rec, obs)

	mustReconcile(t, deps, now)
	if got := mustGetCurrentSession(t, st); got.Degraded {
		t.Fatalf("observed completion during a restart degraded the session: %q", got.DegradedReason)
	}

	h := &handlers{deps: deps, clock: func() time.Time { return now }, logger: testLogger()}
	h.nightAdvanceLive(context.Background(), now, mustGetCurrentSession(t, st))
	if got := mustGetCurrentSession(t, st); got.State != nightStateTransitionToResting {
		t.Fatalf("state = %q after the post-restart tick, want transition-to-resting", got.State)
	}
}

// A restart during the exact one-shot resting item keeps the session and
// launches nothing; the boundary is reconstructed by the ordinary tick.
func TestReconcileOnStartup_ExactRestingItemResumes(t *testing.T) {
	started := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)
	now := started.Add(30 * time.Second)
	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateRestingIntershow, StateEnteredAt: started, Cycle: 1,
		ContentAnchorJSON: encodeNightContentAnchor(nightRestartAnchor(nightAnchorPurposeRestingOneShot, "halloween-resting", started)),
	}
	st, deps := nightRestartFixture(t, now, rec, nightPlayingObservations("player-01", "halloween-resting", now))

	mustReconcile(t, deps, now)

	got := mustGetCurrentSession(t, st)
	if got.Degraded {
		t.Fatalf("a restart during the exact resting item degraded the session: %q", got.DegradedReason)
	}
	if got.State != nightStateRestingIntershow {
		t.Fatalf("state = %q, want still resting-intershow", got.State)
	}
}

// A restart during end-of-night repeat restores observation and starts
// nothing.
func TestReconcileOnStartup_EndOfNightRepeatResumesWithoutStartingAShow(t *testing.T) {
	started := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	now := started.Add(time.Minute)
	anchor := nightRestartAnchor(nightAnchorPurposeRestingRepeat, "halloween-resting", started)
	anchor.RepeatMode = true
	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateEndOfNightResting, StateEnteredAt: started, Cycle: 2,
		FinalShowRequested: true, ContentAnchorJSON: encodeNightContentAnchor(anchor),
	}
	obs := append(nightPlayingObservations("player-01", "halloween-resting", now),
		repeatModeObservation("player-01", true, now))
	st, deps := nightRestartFixture(t, now, rec, obs)

	mustReconcile(t, deps, now)

	got := mustGetCurrentSession(t, st)
	if got.Degraded {
		t.Fatalf("a restart during end-of-night repeat degraded the session: %q", got.DegradedReason)
	}
	if got.State != nightStateEndOfNightResting {
		t.Fatalf("state = %q, want still end-of-night-resting", got.State)
	}
	if got.ArmedShowID != "" {
		t.Fatalf("armedShowId = %q after reconcile; end-of-night resting must arm no show", got.ArmedShowID)
	}
}

// Ambiguity degrades with a reason naming the recovery action.
func TestReconcileOnStartup_AmbiguousEvidenceDegradesWithRecoveryAction(t *testing.T) {
	started := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)
	now := started.Add(30 * time.Second)

	cases := []struct {
		name string
		rec  store.NightSessionRecord
		obs  []observation.Observation
		want string
	}{
		{
			name: "a different playlist is playing than the anchor's",
			rec: store.NightSessionRecord{
				ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
				State: nightStateRestingIntershow, StateEnteredAt: started, Cycle: 1,
				ContentAnchorJSON: encodeNightContentAnchor(nightRestartAnchor(nightAnchorPurposeRestingOneShot, "halloween-resting", started)),
			},
			obs:  nightPlayingObservations("player-01", "something-else", now),
			want: "contradicts",
		},
		{
			name: "no current status evidence at all",
			rec: store.NightSessionRecord{
				ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
				State: nightStateEndOfNightResting, StateEnteredAt: started, Cycle: 1,
				ContentAnchorJSON: encodeNightContentAnchor(nightRestartAnchor(nightAnchorPurposeRestingRepeat, "halloween-resting", started)),
			},
			obs:  nil,
			want: "no current fpp.status evidence",
		},
		{
			name: "no usable anchor for the persisted state",
			rec: store.NightSessionRecord{
				ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
				State: nightStateLive, StateEnteredAt: started, Cycle: 1,
			},
			obs:  nightPlayingObservations("player-01", "halloween-show", now),
			want: "no usable content anchor",
		},
		{
			name: "caught mid-transition",
			rec: store.NightSessionRecord{
				ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
				State: nightStateTransitionToResting, StateEnteredAt: started, Cycle: 1,
			},
			obs:  nightPlayingObservations("player-01", "halloween-resting", now),
			want: "mid-transition",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, deps := nightRestartFixture(t, now, tc.rec, tc.obs)
			mustReconcile(t, deps, now)

			got := mustGetCurrentSession(t, st)
			if !got.Degraded {
				t.Fatalf("ambiguous evidence did not degrade the session: %+v", got)
			}
			if !strings.Contains(got.DegradedReason, tc.want) {
				t.Fatalf("degradedReason = %q, want it to contain %q", got.DegradedReason, tc.want)
			}
			if !strings.Contains(got.DegradedReason, "end-session") || !strings.Contains(got.DegradedReason, "prepare-site") {
				t.Fatalf("degradedReason = %q, want it to name the recovery action", got.DegradedReason)
			}
		})
	}
}

// A cue left mid-dispatch by the restart, whose action carries no stable
// retry identity, resolves terminally ambiguous before any boundary work.
func TestReconcileOnStartup_StrandedNonRetryableCueBecomesAmbiguous(t *testing.T) {
	started := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)
	now := started.Add(30 * time.Second)
	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateRestingIntershow, StateEnteredAt: started, Cycle: 1,
		ContentAnchorJSON: encodeNightContentAnchor(nightRestartAnchor(nightAnchorPurposeRestingOneShot, "halloween-resting", started)),
	}
	st, deps := nightRestartFixture(t, now, rec, nightPlayingObservations("player-01", "halloween-resting", now))

	putNightAction(t, st, "act-blackout", blackoutResolumeAction())
	payload := nightShutdownPayload()
	payload.EnterShow = config.NightSessionEnterShow{Cues: []config.NightSessionCue{
		{Name: "blackout", Role: config.NightSessionCueRoleProjection, Action: "act-blackout", OnFailure: config.NightSessionCueOnFailureContinue},
	}}
	seedNightSessionRevision(t, st, payload)

	dispatchedAt := started.Add(5 * time.Second)
	if err := st.InsertNightCueOutboxRow(context.Background(), store.NightCueOutboxRecord{
		ID: "row-1", SessionID: rec.ID, Cycle: rec.Cycle, Phase: nightPhaseEnterShow, CueName: "blackout",
		ActionRevision: 1, State: nightCueStateDispatched, DispatchedAt: &dispatchedAt,
	}, dispatchedAt); err != nil {
		t.Fatalf("seed dispatched row: %v", err)
	}

	mustReconcile(t, deps, now)

	row, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, nightPhaseEnterShow, "blackout")
	if err != nil {
		t.Fatalf("GetNightCueOutboxRow: %v", err)
	}
	if row.State != nightCueStateAmbiguous || row.Outcome != nightCueOutcomeAmbiguous {
		t.Fatalf("row state/outcome = %q/%q, want ambiguous/ambiguous", row.State, row.Outcome)
	}
	if row.ResolvedAt == nil {
		t.Fatal("an ambiguous row carries no resolvedAt")
	}
}

// Authorization is persisted with the session, so the principal who
// authorized the night is still nameable after a restart.
func TestNightAttribution_SurvivesARestart(t *testing.T) {
	api, st, token, _, obs := setupNightControlFixture(t, time.Hour)
	runToPreshow(t, api, token, obs, testNow)

	rec := mustGetCurrentSession(t, st)
	if rec.Issuer.IsZero() {
		t.Fatal("no authorizing principal was persisted with the session")
	}
	if rec.Issuer.Command == "" {
		t.Fatal("no lifecycle command was persisted with the attribution")
	}
	if rec.Issuer.RecordedAt == nil {
		t.Fatal("attribution carries no recordedAt")
	}

	// A fresh process holds no in-memory registry; the record alone must
	// still name who authorized this session.
	reread, ok, err := st.GetCurrentNightSession(context.Background())
	if err != nil || !ok {
		t.Fatalf("re-read session: ok=%v err=%v", ok, err)
	}
	if reread.Issuer.PrincipalID != rec.Issuer.PrincipalID {
		t.Fatalf("re-read principal = %q, want %q", reread.Issuer.PrincipalID, rec.Issuer.PrincipalID)
	}

	got := mustGetNightSession(t, api)
	if got.Session.Authorization.State != "recorded" {
		t.Fatalf("authorization.state = %q, want recorded", got.Session.Authorization.State)
	}
	if got.Session.Authorization.PrincipalName == "" {
		t.Fatal("authorization names no principal")
	}
}

func seedNightSessionRevision(t *testing.T, st *store.Store, payload config.NightSessionPayload) {
	t.Helper()
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
}
