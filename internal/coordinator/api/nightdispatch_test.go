package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Dispatch truthfulness: a refusal that never reached the wire is not a
// dispatch, transient refusals retry across ticks under one identity, and
// a barrier cue with no outbox row still resolves against its deadline.

// A busy host refuses startPlaylist before anything reaches FPP. The
// anchor must not record a dispatch, and a later tick must retry under the
// same anchor once the host frees up. This drives three ticks.
func TestNightEnsureAnchor_TransientRefusalRetriesOnALaterTick(t *testing.T) {
	now := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	var starts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Command string `json:"command"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		if body.Command == "Start Playlist" {
			starts++
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Playlist Starting"))
	}))
	t.Cleanup(srv.Close)

	obs := &mutableObservationLister{}
	svc, st, _ := newTestIdentityServiceWithStore(t, func() time.Time { return now })
	deps := Dependencies{
		NightSessions: st, Observations: obs, Identity: svc, Config: st,
		FPP: &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: srv.URL}}},
	}.withDefaults()
	h := &handlers{
		deps: deps, clock: func() time.Time { return now }, logger: testLogger(),
		fppCommandConfirmDeadline: 50 * time.Millisecond, fppCommandPollInterval: 10 * time.Millisecond,
	}
	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStatePreshow, StateEnteredAt: now, Cycle: 1,
		Issuer: store.NightSessionIssuer{PrincipalID: "p-1", PrincipalName: "operator-1"},
	}
	if err := st.CreateNightSession(context.Background(), rec, now); err != nil {
		t.Fatalf("create night session: %v", err)
	}

	// Tick 1: a different playlist is running, so startPlaylist refuses
	// before the wire.
	obs.set([]observation.Observation{
		statusObservation("player-01", fppStatusValuePlaying, now),
		playlistNameObservation("player-01", "someone-elses-show", now),
	})
	anchor, ready, changed := h.nightEnsureAnchor(context.Background(), now, rec, nightAnchorPurposeRestingRepeat, "player-01", "halloween-resting", true, 0, fppIfBusyRefuse)
	if ready || !changed {
		t.Fatalf("ready=%v changed=%v, want ready=false changed=true", ready, changed)
	}
	if !anchor.DispatchedAt.IsZero() {
		t.Fatalf("DispatchedAt = %v for a refusal that never reached FPP, want zero", anchor.DispatchedAt)
	}
	if anchor.AttemptedAt.IsZero() || anchor.FirstAttemptAt.IsZero() {
		t.Fatalf("the refused attempt was not recorded: %+v", anchor)
	}
	if anchor.RefusalTerminal {
		t.Fatal("a busy host was classified as a terminal refusal; it must be retried")
	}
	if starts != 0 {
		t.Fatalf("Start Playlist reached FPP %d times despite the pre-dispatch refusal", starts)
	}
	rec.ContentAnchorJSON = encodeNightContentAnchor(anchor)

	// Tick 2: inside the backoff. No new attempt.
	now = now.Add(nightDispatchRetryBackoff / 2)
	_, _, changed = h.nightEnsureAnchor(context.Background(), now, rec, nightAnchorPurposeRestingRepeat, "player-01", "halloween-resting", true, 0, fppIfBusyRefuse)
	if changed {
		t.Fatal("a retry fired inside its own backoff window")
	}
	if starts != 0 {
		t.Fatalf("Start Playlist reached FPP %d times inside the backoff window", starts)
	}

	// Tick 3: past the backoff, and the host is now idle. The retry lands.
	now = now.Add(nightDispatchRetryBackoff)
	obs.set([]observation.Observation{statusObservation("player-01", fppStatusValueIdle, now)})
	next, _, changed := h.nightEnsureAnchor(context.Background(), now, rec, nightAnchorPurposeRestingRepeat, "player-01", "halloween-resting", true, 0, fppIfBusyRefuse)
	if !changed {
		t.Fatal("the retry did not run past its backoff window")
	}
	if starts != 1 {
		t.Fatalf("Start Playlist reached FPP %d times, want exactly 1", starts)
	}
	if next.DispatchedAt.IsZero() {
		t.Fatalf("the successful retry recorded no DispatchedAt: %+v", next)
	}
}

// A refusal retrying cannot fix (no such FPP instance) is terminal: it is
// never retried, and it is visible.
func TestNightEnsureAnchor_PermanentRefusalIsTerminalAndVisible(t *testing.T) {
	now := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)
	obs := &fakeObservationLister{}
	svc, st, _ := newTestIdentityServiceWithStore(t, func() time.Time { return now })
	deps := Dependencies{
		NightSessions: st, Observations: obs, Identity: svc, Config: st,
		FPP: &fakeFPPLister{views: nil},
	}.withDefaults()
	h := &handlers{
		deps: deps, clock: func() time.Time { return now }, logger: testLogger(),
		fppCommandConfirmDeadline: 50 * time.Millisecond, fppCommandPollInterval: 10 * time.Millisecond,
	}
	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStatePreshow, StateEnteredAt: now, Cycle: 1,
		Issuer: store.NightSessionIssuer{PrincipalID: "p-1", PrincipalName: "operator-1"},
	}
	if err := st.CreateNightSession(context.Background(), rec, now); err != nil {
		t.Fatalf("create night session: %v", err)
	}

	anchor, _, changed := h.nightEnsureAnchor(context.Background(), now, rec, nightAnchorPurposeRestingRepeat, "missing-instance", "halloween-resting", true, 0, fppIfBusyRefuse)
	if !changed {
		t.Fatal("the refusal was not recorded")
	}
	if !anchor.RefusalTerminal {
		t.Fatalf("a missing FPP instance was classified as retryable: %+v", anchor)
	}
	if !strings.Contains(anchor.Source, "refused") {
		t.Fatalf("anchor.Source = %q, want a stated refusal", anchor.Source)
	}

	// Well past the backoff: a terminal refusal is still not retried.
	rec.ContentAnchorJSON = encodeNightContentAnchor(anchor)
	now = now.Add(10 * nightDispatchRetryBackoff)
	if _, _, changed := h.nightEnsureAnchor(context.Background(), now, rec, nightAnchorPurposeRestingRepeat, "missing-instance", "halloween-resting", true, 0, fppIfBusyRefuse); changed {
		t.Fatal("a terminal refusal was retried")
	}
}

// A transient refusal that never clears degrades the session rather than
// retrying silently for the rest of the night.
func TestNightEnsureAnchor_RefusalPastTheRetryWindowDegrades(t *testing.T) {
	now := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)
	obs := &fakeObservationLister{}
	svc, st, _ := newTestIdentityServiceWithStore(t, func() time.Time { return now })
	deps := Dependencies{
		NightSessions: st, Observations: obs, Identity: svc, Config: st,
		FPP: &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: "http://127.0.0.1:1"}}},
	}.withDefaults()
	h := &handlers{deps: deps, clock: func() time.Time { return now }, logger: testLogger()}

	firstAttempt := now.Add(-nightDispatchRetryWindow - time.Minute)
	anchor := nightContentAnchor{
		Purpose: nightAnchorPurposeRestingRepeat, FPPInstanceID: "player-01", Playlist: "halloween-resting",
		FirstAttemptAt: firstAttempt, AttemptedAt: now.Add(-time.Minute),
		Source: "refused: the host is busy with another playlist",
	}
	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStatePreshow, StateEnteredAt: firstAttempt, Cycle: 1,
		ContentAnchorJSON: encodeNightContentAnchor(anchor),
		Issuer:            store.NightSessionIssuer{PrincipalID: "p-1", PrincipalName: "operator-1"},
	}
	if err := st.CreateNightSession(context.Background(), rec, now); err != nil {
		t.Fatalf("create night session: %v", err)
	}

	h.nightEnsureAnchor(context.Background(), now, rec, nightAnchorPurposeRestingRepeat, "player-01", "halloween-resting", true, 0, fppIfBusyRefuse)

	got := mustGetCurrentSession(t, st)
	if !got.Degraded {
		t.Fatalf("a refusal lasting the whole retry window did not degrade the session: %+v", got)
	}
	if !strings.Contains(got.DegradedReason, "could not be started") {
		t.Fatalf("degradedReason = %q, want it to name the failure", got.DegradedReason)
	}
}

func mqttCueAction(kind string, value *string) config.ShowActionPayload {
	return config.ShowActionPayload{
		Show: "halloween", Label: "House lights",
		Target: config.ShowActionTarget{
			Integration: config.ShowActionIntegrationMQTT, Broker: "home-automation",
			Publish: &config.ShowActionMQTTPublish{Topic: "house/lights/set", Payload: "off", QoS: 1},
			Expect:  &config.ShowActionMQTTExpect{Kind: kind, Topic: "house/lights/state", Value: value, DeadlineSeconds: 5},
		},
	}
}

// An mqtt cue publishes through the shared adapter and persists whatever
// it concluded, rather than failing without transmitting anything.
func TestNightRunCue_MQTTPublishesAndPersistsItsOutcome(t *testing.T) {
	h, st := nightCueTestHandlers(t)
	brokers := &fakeMQTTBrokerRegistry{msg: broker.Message{Payload: []byte("off")}}
	h.deps.MQTTBrokers = brokers

	putNightAction(t, st, "act-house-lights", mqttCueAction(config.MQTTExpectKindMatch, strp("off")))
	rec := mustCreateTransitionToShowSession(t, st, "sess-1", 1, testNow)
	cue := config.NightSessionCue{Name: "house-lights", Role: config.NightSessionCueRoleLighting, Action: "act-house-lights", OnFailure: config.NightSessionCueOnFailureContinue}

	row, err := h.nightRunCue(context.Background(), testNow, rec, nightPhaseEnterShow, cue, testIssuer, false)
	if err != nil {
		t.Fatalf("nightRunCue: %v", err)
	}
	if brokers.publishCount() == 0 {
		t.Fatal("nothing was published; the mqtt cue must reach the broker")
	}
	if row.State != nightCueStateResolved || row.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("row state/outcome = %q/%q, want resolved/confirmed", row.State, row.Outcome)
	}
	if row.DispatchedAt == nil {
		t.Fatal("a published mqtt cue recorded no dispatchedAt")
	}

	stored, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, nightPhaseEnterShow, "house-lights")
	if err != nil {
		t.Fatalf("GetNightCueOutboxRow: %v", err)
	}
	if stored.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("persisted outcome = %q, want confirmed", stored.Outcome)
	}

	// A second tick must not re-publish: mqtt carries no retry identity.
	before := brokers.publishCount()
	if _, err := h.nightRunCue(context.Background(), testNow, rec, nightPhaseEnterShow, cue, testIssuer, false); err != nil {
		t.Fatalf("second nightRunCue: %v", err)
	}
	if brokers.publishCount() != before {
		t.Fatalf("the resolved mqtt cue was published again: %d then %d", before, brokers.publishCount())
	}
}

// A barrier cue whose outbox row could never be created still resolves
// against the deadline, and its own onFailure decides the launch.
func TestNightBarrierSatisfied_MissingRowHonoursOnFailureAfterTheDeadline(t *testing.T) {
	for _, tc := range []struct {
		name      string
		onFailure string
		wantOK    bool
	}{
		{"continue releases the barrier", config.NightSessionCueOnFailureContinue, true},
		{"abort keeps it blocked", config.NightSessionCueOnFailureAbort, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, st := nightCueTestHandlers(t)
			rec := mustCreateTransitionToShowSession(t, st, "sess-1", 1, testNow)
			cues := []config.NightSessionCue{{Name: "ghost", Barrier: true, Action: "act-missing", OnFailure: tc.onFailure}}
			referenceE := testNow

			// Before the deadline: blocked, and nothing recorded yet.
			ok, reason, err := h.nightBarrierSatisfied(context.Background(), referenceE.Add(time.Second), referenceE, rec, nightPhaseEnterShow, cues)
			if err != nil {
				t.Fatalf("nightBarrierSatisfied: %v", err)
			}
			if ok {
				t.Fatal("the barrier released before its deadline with no row at all")
			}
			if !strings.Contains(reason, "has not been dispatched yet") {
				t.Fatalf("reason = %q, want the not-yet-dispatched reason", reason)
			}

			// Past the deadline: the absence is recorded durably.
			after := referenceE.Add(nightBarrierResolutionDeadline + time.Second)
			ok, reason, err = h.nightBarrierSatisfied(context.Background(), after, referenceE, rec, nightPhaseEnterShow, cues)
			if err != nil {
				t.Fatalf("nightBarrierSatisfied past deadline: %v", err)
			}
			if ok != tc.wantOK {
				t.Fatalf("barrier satisfied = %v, want %v (reason %q)", ok, tc.wantOK, reason)
			}

			row, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, nightPhaseEnterShow, "ghost")
			if err != nil {
				t.Fatalf("the missing barrier cue was never recorded: %v", err)
			}
			if row.State != nightCueStateResolved || row.Outcome != nightCueOutcomeFailed {
				t.Fatalf("recorded row state/outcome = %q/%q, want resolved/failed", row.State, row.Outcome)
			}
			if !tc.wantOK && !strings.Contains(reason, "onFailure is abort") {
				t.Fatalf("blocked reason = %q, want it to name the abort policy", reason)
			}
		})
	}
}
