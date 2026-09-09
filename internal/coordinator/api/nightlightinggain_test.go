package api

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppcommand"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// A night lighting cue writes its brightness transition gain
// through the cue outbox, AS WELL AS its own action and never instead of
// it, on the side of the dispatch nightLightingFade.BeforeAction names.

// nightGainRig records the ORDER of the two outward effects a lighting
// cue has: the fake FPP server appends "action" when the cue's own
// primitive is dispatched, and the substituted gain writer appends
// "gain". Ordering asserted from one shared log, never from two
// independent "did it happen" flags.
type nightGainRig struct {
	h   *handlers
	rec store.NightSessionRecord

	mu         sync.Mutex
	events     []string
	requestIDs []string
	gains      []nightLightingFade
}

func (r *nightGainRig) record(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *nightGainRig) observed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

// newNightGainRig builds a night session whose cue action is a real FPP
// primitive against a fake FPP instance. gainErr, when non-nil, is what
// the gain write returns instead of an outcome.
func newNightGainRig(t *testing.T, gainErr error) *nightGainRig {
	t.Helper()
	rig := &nightGainRig{}

	fppSrv, _ := newFakeFPPCommandServerWithSideEffect(t, 200, "Stopped", func() { rig.record("action") })
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})

	deps := setup.deps()
	deps.NightSessions = setup.st
	deps.Config = setup.st
	deps = deps.withDefaults()

	rig.h = &handlers{
		deps: deps, clock: fixedClock(testNow), logger: testLogger(),
		fppCommandConfirmDeadline: 200 * time.Millisecond, fppCommandPollInterval: 10 * time.Millisecond,
		nightGainWriter: func(_ context.Context, _ config.ShowActionTarget, fade nightLightingFade, requestID string) (fppcommand.TransitionGainOutcome, error) {
			rig.record("gain")
			rig.mu.Lock()
			rig.requestIDs = append(rig.requestIDs, requestID)
			rig.gains = append(rig.gains, fade)
			rig.mu.Unlock()
			if gainErr != nil {
				return fppcommand.TransitionGainOutcome{}, gainErr
			}
			return fppcommand.TransitionGainOutcome{Applied: true, GainTarget: fade.TargetPercent, FadeSeconds: fade.FadeSeconds, Ceiling: 80, EffectiveOutput: 40}, nil
		},
	}

	putNightAction(t, setup.st, "act-lighting", config.ShowActionPayload{
		Show: "halloween", Label: "Stop", SafetyClass: config.ShowSafetyClassStop,
		Target: config.ShowActionTarget{Integration: config.ShowActionIntegrationFPP, InstanceID: "bench-fpp", Primitive: "stopPlaylist", Params: map[string]any{}},
	})
	rig.rec = mustCreateTransitionToShowSession(t, setup.st, "sess-1", 1, testNow)
	return rig
}

// nightLightingCue builds a lighting cue against the rig's FPP action.
// fadeMs nil is "this cue asks for no fade".
func nightLightingCue(name string, fadeMs *int) config.NightSessionCue {
	return config.NightSessionCue{
		Name: name, Role: config.NightSessionCueRoleLighting, Action: "act-lighting",
		FadeDurationMs: fadeMs, OnFailure: config.NightSessionCueOnFailureContinue,
	}
}

func nightFadeMs(v int) *int { return &v }

// TestNightLightingCueGainOrdering defends the ONE principle behind
// nightLightingFade.BeforeAction: the content change happens while the
// display is dark. Entering a show the gain goes down first; entering
// resting the action runs first and the gain comes up after. Both cases
// also assert the action still dispatched, which is the "as well as,
// never instead of" property.
func TestNightLightingCueGainOrdering(t *testing.T) {
	cases := []struct {
		name          string
		phase         string
		isFirst       bool
		fadeMs        *int
		wantEvents    []string
		wantGainWrite *nightLightingFade
	}{
		{
			name: "enterShow fades before the action", phase: nightPhaseEnterShow, isFirst: true,
			fadeMs: nightFadeMs(3000), wantEvents: []string{"gain", "action"},
			wantGainWrite: &nightLightingFade{TargetPercent: 0, FadeSeconds: 3, BeforeAction: true},
		},
		{
			name: "enterResting fades after the action", phase: nightPhaseEnterResting, isFirst: false,
			fadeMs: nightFadeMs(2400), wantEvents: []string{"action", "gain"},
			wantGainWrite: &nightLightingFade{TargetPercent: 100, FadeSeconds: 2, BeforeAction: false},
		},
		{
			name: "a cue with no fade writes no gain at all", phase: nightPhaseEnterShow, isFirst: true,
			fadeMs: nil, wantEvents: []string{"action"},
		},
		{
			name: "a phase with no defined direction writes no gain", phase: nightPhaseFadeOut, isFirst: false,
			fadeMs: nightFadeMs(3000), wantEvents: []string{"action"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newNightGainRig(t, nil)
			cue := nightLightingCue("dim", tc.fadeMs)

			row, err := rig.h.nightRunCue(context.Background(), testNow, rig.rec, tc.phase, cue, testIssuer, tc.isFirst)
			if err != nil {
				t.Fatalf("nightRunCue: %v", err)
			}
			if got := rig.observed(); !equalStrings(got, tc.wantEvents) {
				t.Fatalf("observed order = %v, want %v", got, tc.wantEvents)
			}
			if row.State != nightCueStateResolved {
				t.Fatalf("row state = %q, want %q", row.State, nightCueStateResolved)
			}

			rig.mu.Lock()
			gains := append([]nightLightingFade(nil), rig.gains...)
			ids := append([]string(nil), rig.requestIDs...)
			rig.mu.Unlock()

			if tc.wantGainWrite == nil {
				if len(gains) != 0 {
					t.Fatalf("gain writes = %v, want none", gains)
				}
				return
			}
			if len(gains) != 1 || gains[0] != *tc.wantGainWrite {
				t.Fatalf("gain writes = %v, want exactly one %+v", gains, *tc.wantGainWrite)
			}

			// The requestId and the outbox idempotency key are the SAME
			// value, not two values that happen to agree: a resume after a
			// restart regenerates this string from cue identity alone, and
			// the plugin then declines to restart the fade.
			wantKey := nightCueIdempotencyKey(rig.rec.ID, rig.rec.Cycle, tc.phase, cue.Name)
			if len(ids) != 1 || ids[0] != wantKey {
				t.Fatalf("gain requestId = %v, want [%q]", ids, wantKey)
			}
			if !strings.Contains(row.OutcomeReason, "transition gain") {
				t.Errorf("outbox reason %q does not record the gain outcome", row.OutcomeReason)
			}
			if !strings.Contains(row.OutcomeReason, "applied=true") ||
				!strings.Contains(row.OutcomeReason, "ceiling 80") ||
				!strings.Contains(row.OutcomeReason, "effective output 40") {
				t.Errorf("outbox reason %q does not carry applied/ceiling/effectiveOutput", row.OutcomeReason)
			}
		})
	}
}

// TestNightLightingCueGainRecordsIdempotentRepeat defends the resumed
// transition: applied=false with the gain unchanged is the key working as
// designed, not a missing outcome, so the row must still record it.
func TestNightLightingCueGainRecordsIdempotentRepeat(t *testing.T) {
	rig := newNightGainRig(t, nil)
	rig.h.nightGainWriter = func(_ context.Context, _ config.ShowActionTarget, fade nightLightingFade, _ string) (fppcommand.TransitionGainOutcome, error) {
		rig.record("gain")
		return fppcommand.TransitionGainOutcome{Applied: false, GainTarget: fade.TargetPercent, Ceiling: 70, EffectiveOutput: 0}, nil
	}
	cue := nightLightingCue("dim", nightFadeMs(3000))

	row, err := rig.h.nightRunCue(context.Background(), testNow, rig.rec, nightPhaseEnterShow, cue, testIssuer, true)
	if err != nil {
		t.Fatalf("nightRunCue: %v", err)
	}
	if row.Outcome != nightCueOutcomeConfirmed {
		t.Errorf("outcome = %q, want %q: an idempotent repeat is a success", row.Outcome, nightCueOutcomeConfirmed)
	}
	for _, want := range []string{"applied=false", "ceiling 70", "effective output 0"} {
		if !strings.Contains(row.OutcomeReason, want) {
			t.Errorf("outbox reason %q does not contain %q", row.OutcomeReason, want)
		}
	}
}

// TestNightLightingCueGainFailureSurfaces defends the rule that a failed
// gain write never reads as a clean success: the cue's own action
// confirmed, and the row still resolves failed with the gain error in its
// reason, because a lighting cue whose fade did not happen did not do
// what it was authored to do.
func TestNightLightingCueGainFailureSurfaces(t *testing.T) {
	rig := newNightGainRig(t, errors.New("fpp plugin unreachable"))
	cue := nightLightingCue("dim", nightFadeMs(3000))

	row, err := rig.h.nightRunCue(context.Background(), testNow, rig.rec, nightPhaseEnterShow, cue, testIssuer, true)
	if err != nil {
		t.Fatalf("nightRunCue: %v", err)
	}
	if got := rig.observed(); !equalStrings(got, []string{"gain", "action"}) {
		t.Fatalf("observed order = %v, want [gain action]: a failed gain write never cancels the action", got)
	}
	if row.Outcome != nightCueOutcomeFailed {
		t.Errorf("outcome = %q, want %q", row.Outcome, nightCueOutcomeFailed)
	}
	if !strings.Contains(row.OutcomeReason, "fpp plugin unreachable") {
		t.Errorf("outbox reason %q does not name the gain failure", row.OutcomeReason)
	}
}

// TestNightLightingCueGainSkipsNonFPPTarget defends the endpoint rule:
// the gain lives in the FPP plugin, so a lighting cue whose action
// targets anything else writes no gain and still runs its action.
func TestNightLightingCueGainSkipsNonFPPTarget(t *testing.T) {
	h, st := nightCueTestHandlers(t)
	gainCalls := 0
	h.nightGainWriter = func(context.Context, config.ShowActionTarget, nightLightingFade, string) (fppcommand.TransitionGainOutcome, error) {
		gainCalls++
		return fppcommand.TransitionGainOutcome{}, nil
	}
	putNightAction(t, st, "act-blackout", blackoutResolumeAction())
	rec := mustCreateTransitionToShowSession(t, st, "sess-1", 1, testNow)
	cue := config.NightSessionCue{
		Name: "dim", Role: config.NightSessionCueRoleLighting, Action: "act-blackout",
		FadeDurationMs: nightFadeMs(3000), OnFailure: config.NightSessionCueOnFailureContinue,
	}

	row, err := h.nightRunCue(context.Background(), testNow, rec, nightPhaseEnterShow, cue, testIssuer, true)
	if err != nil {
		t.Fatalf("nightRunCue: %v", err)
	}
	if gainCalls != 0 {
		t.Errorf("gain writes = %d, want 0 for a non-FPP target", gainCalls)
	}
	if row.State != nightCueStateResolved {
		t.Errorf("row state = %q, want %q: the cue's action still runs", row.State, nightCueStateResolved)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
