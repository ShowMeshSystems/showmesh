package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// enterEndOfNight replaces enterResting's cues after the final show only.

func endOfNightTestPayload(eon *config.NightSessionEnterEndOfNight) config.NightSessionPayload {
	return config.NightSessionPayload{
		Show: "halloween", Label: "test",
		ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "player-01", Playlist: "halloween-show"},
		Resting:      config.NightSessionResting{FPPInstanceID: "player-01", Playlist: "halloween-resting", EndOfNightPlaylist: "halloween-goodnight"},
		EnterResting: config.NightSessionEnterResting{
			Cues:                []config.NightSessionCue{{Name: "rest-look", Role: config.NightSessionCueRoleProjection, Action: "act-rest", OnFailure: config.NightSessionCueOnFailureContinue}},
			BlackoutAfterShowMs: 1000,
		},
		EnterEndOfNight: eon,
	}
}

func customEndOfNight(blackoutMs *int) *config.NightSessionEnterEndOfNight {
	return &config.NightSessionEnterEndOfNight{
		Mode:                config.NightSessionEndOfNightModeCustom,
		Cues:                []config.NightSessionCue{{Name: "goodnight-look", Role: config.NightSessionCueRoleProjection, Action: "act-goodnight", OnFailure: config.NightSessionCueOnFailureContinue}},
		BlackoutAfterShowMs: blackoutMs,
	}
}

func TestNightEnterRestingCues_ChoosesByFinalShowAndMode(t *testing.T) {
	five := 5000
	cases := []struct {
		name      string
		final     bool
		eon       *config.NightSessionEnterEndOfNight
		wantPhase string
		wantCue   string
		wantHold  int
	}{
		{"not final, custom set", false, customEndOfNight(&five), nightPhaseEnterResting, "rest-look", 1000},
		{"final, block omitted", true, nil, nightPhaseEnterResting, "rest-look", 1000},
		{"final, resting mode", true, &config.NightSessionEnterEndOfNight{Mode: config.NightSessionEndOfNightModeResting}, nightPhaseEnterResting, "rest-look", 1000},
		{"final, none", true, &config.NightSessionEnterEndOfNight{Mode: config.NightSessionEndOfNightModeNone}, nightPhaseEnterResting, "", 1000},
		{"final, custom", true, customEndOfNight(nil), nightPhaseEnterEndOfNight, "goodnight-look", 1000},
		{"final, custom with own blackout", true, customEndOfNight(&five), nightPhaseEnterEndOfNight, "goodnight-look", 5000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := store.NightSessionRecord{FinalShowRequested: tc.final}
			phase, cues, hold := nightEnterRestingCues(rec, endOfNightTestPayload(tc.eon))
			if phase != tc.wantPhase || hold != tc.wantHold {
				t.Fatalf("phase, hold = %q, %d; want %q, %d", phase, hold, tc.wantPhase, tc.wantHold)
			}
			if tc.wantCue == "" {
				if len(cues) != 0 {
					t.Fatalf("cues = %+v, want none", cues)
				}
				return
			}
			if len(cues) != 1 || cues[0].Name != tc.wantCue {
				t.Fatalf("cues = %+v, want only %q", cues, tc.wantCue)
			}
		})
	}
}

// The transition tick sends only the end-of-night cue after the final show.
func TestNightAdvanceTransitionToResting_FinalShowRunsEndOfNightCuesOnly(t *testing.T) {
	for _, tc := range []struct {
		name       string
		eon        *config.NightSessionEnterEndOfNight
		wantCalls  int
		wantPhase  string
		wantCue    string
		absentCues []string
	}{
		{"custom", customEndOfNight(nil), 1, nightPhaseEnterEndOfNight, "goodnight-look", []string{"rest-look"}},
		{"none", &config.NightSessionEnterEndOfNight{Mode: config.NightSessionEndOfNightModeNone}, 0, "", "", []string{"rest-look", "goodnight-look"}},
		{"omitted", nil, 1, nightPhaseEnterResting, "rest-look", []string{"goodnight-look"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, st := nightCueTestHandlers(t)
			dispatcher := h.deps.ResolumeActions.(*fakeResolumeActionDispatcher)
			dispatcher.results = map[string]ResolumeActionResult{
				config.ShowActionResolumeBlackout: {Outcome: ResolumeOutcomeConfirmed, Dispatched: true, Reason: "ok"},
			}
			putNightAction(t, st, "act-rest", blackoutResolumeAction())
			putNightAction(t, st, "act-goodnight", blackoutResolumeAction())

			payload := endOfNightTestPayload(tc.eon)
			payload.EnterResting.BlackoutAfterShowMs = int(time.Hour / time.Millisecond)
			mustPinNightSessionPayload(t, st, payload)
			rec := store.NightSessionRecord{
				ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1, Cycle: 1,
				State: nightStateTransitionToResting, StateEnteredAt: testNow.Add(-time.Second), FinalShowRequested: true,
			}
			if err := st.CreateNightSession(context.Background(), rec, testNow); err != nil {
				t.Fatalf("create night session: %v", err)
			}

			h.nightAdvanceTransitionToResting(context.Background(), testNow, rec)

			if n := dispatcher.callCount(); n != tc.wantCalls {
				t.Fatalf("dispatcher called %d times, want %d", n, tc.wantCalls)
			}
			if tc.wantCue != "" {
				if _, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, tc.wantPhase, tc.wantCue); err != nil {
					t.Fatalf("no outbox row for %s/%s: %v", tc.wantPhase, tc.wantCue, err)
				}
			}
			for _, phase := range []string{nightPhaseEnterResting, nightPhaseEnterEndOfNight} {
				for _, name := range tc.absentCues {
					if _, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, phase, name); err == nil {
						t.Fatalf("cue %s/%s was sent, want it skipped", phase, name)
					}
				}
			}
		})
	}
}

func TestNightCheckEndOfNightCues_SaysWhichCuesRun(t *testing.T) {
	for _, tc := range []struct {
		eon  *config.NightSessionEnterEndOfNight
		want string
	}{
		{nil, "the resting cues run"},
		{&config.NightSessionEnterEndOfNight{Mode: config.NightSessionEndOfNightModeNone}, "no cues run"},
		{customEndOfNight(nil), "the 1 end of night cues run"},
	} {
		c := nightCheckEndOfNightCues(endOfNightTestPayload(tc.eon))
		if !strings.Contains(c.reason, tc.want) {
			t.Errorf("reason = %q, want it to contain %q", c.reason, tc.want)
		}
		if c.health != nightHealthHealthy() {
			t.Errorf("health = %v, want healthy", c.health)
		}
	}
}

func mustPinNightSessionPayload(t *testing.T, st *store.Store, payload config.NightSessionPayload) {
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
