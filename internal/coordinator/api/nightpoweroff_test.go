package api

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// The night's shutdown sequence runtime (nightpoweroff.go), against an
// in-memory store and fakeResolumeActionDispatcher, never real power.

type nightPowerOffFixture struct {
	h   *handlers
	st  *store.Store
	res *fakeResolumeActionDispatcher
}

func powerCut(action string) config.ShutdownStep {
	return config.ShutdownStep{Action: action, PowerDomain: config.NightPowerDomainPresentation, DomainProvenance: config.NightDomainProvenanceOperatorDeclared}
}

func newNightPowerOffFixture(t *testing.T, now *time.Time, actions *config.NightSiteActions, results map[string]ResolumeActionResult, actionIDsToVerbs map[string]string) *nightPowerOffFixture {
	t.Helper()
	dir := t.TempDir()
	st, err := openTestStore(context.Background(), filepath.Join(dir, "db"), nil, store.WithClock(func() time.Time { return *now }))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	res := &fakeResolumeActionDispatcher{results: results}
	deps := Dependencies{NightSessions: st, Config: st, ResolumeActions: res}.withDefaults()
	h := &handlers{deps: deps, clock: func() time.Time { return *now }, logger: testLogger()}

	for id, verb := range actionIDsToVerbs {
		putNightAction(t, st, id, config.ShowActionPayload{
			Show: "halloween-2026", Label: id, SafetyClass: config.ShowSafetyClassNone,
			Target: config.ShowActionTarget{Integration: config.ShowActionIntegrationResolume, Action: verb, Ref: map[string]any{}},
		})
	}

	payload := nightShutdownPayload()
	payload.SiteActions = actions
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

	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateStopped, StateEnteredAt: *now, Cycle: 1,
		AdmissionClosed: true, ShutdownIntent: "power-down", PowerPhase: nightPowerPhaseConfiguredNotDispatched,
	}
	if err := st.CreateNightSession(context.Background(), rec, *now); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	return &nightPowerOffFixture{h: h, st: st, res: res}
}

func nightOnlySequences(seqs ...config.ShutdownSequence) *config.NightSiteActions {
	return &config.NightSiteActions{PowerDown: &config.NightSitePowerDown{AdditionalSequences: seqs}}
}

func resolumeConfirmed(reason string, at time.Time) ResolumeActionResult {
	t := at
	return ResolumeActionResult{Outcome: ResolumeOutcomeConfirmed, Dispatched: true, DispatchedAt: &t, Reason: reason}
}

func resolumeUnconfirmed(reason string, at time.Time) ResolumeActionResult {
	t := at
	return ResolumeActionResult{Outcome: ResolumeOutcomeUnconfirmed, Dispatched: true, DispatchedAt: &t, Reason: reason}
}

func projectorSequence() config.ShutdownSequence {
	return config.ShutdownSequence{ID: "projectors", Label: "Projectors", Steps: []config.ShutdownStep{
		{Action: "lamps-off", RequireConfirmation: true},
		{DelayMs: 5000},
		powerCut("power-off-action"),
	}}
}

// Steps run in order, the wait holds the power cut back, and a resolved
// sequence is never sent again.
func TestNightPowerOff_SequenceRunsStepsInOrderThenCutsPowerLast(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	f := newNightPowerOffFixture(t, &now, nightOnlySequences(projectorSequence()), map[string]ResolumeActionResult{
		"launchClip": resolumeConfirmed("projectors confirmed off", now),
		"blackout":   resolumeConfirmed("power relay confirmed open", now),
	}, map[string]string{"lamps-off": "launchClip", "power-off-action": "blackout"})

	f.h.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, f.st))
	if got := f.res.callCount(); got != 1 {
		t.Fatalf("resolume calls after the first step = %d, want 1 (the wait must hold the power cut back)", got)
	}
	f.h.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, f.st))
	if got := f.res.callCount(); got != 1 {
		t.Fatalf("resolume calls before the wait ends = %d, want still 1", got)
	}

	now = now.Add(5 * time.Second)
	f.h.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, f.st))
	if last, ok := f.res.lastCall(); f.res.callCount() != 2 || !ok || last.action != "blackout" {
		t.Fatalf("calls = %d, last = %+v, want the power cut sent last", f.res.callCount(), last)
	}
	if final := mustGetCurrentSession(t, f.st); final.PowerPhase != "Power down finished." {
		t.Fatalf("PowerPhase = %q, want Power down finished.", final.PowerPhase)
	}
	f.h.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, f.st))
	if got := f.res.callCount(); got != 2 {
		t.Fatalf("resolume calls after the sequence resolved = %d, want still 2", got)
	}
}

// Two sequences start together: one waiting on its cooldown never holds
// the other back.
func TestNightPowerOff_SequencesRunSideBySide(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	fm := config.ShutdownSequence{ID: "fm", Label: "FM transmitter", Steps: []config.ShutdownStep{powerCut("fm-off")}}
	f := newNightPowerOffFixture(t, &now, nightOnlySequences(projectorSequence(), fm), map[string]ResolumeActionResult{
		"launchClip":   resolumeConfirmed("projectors confirmed off", now),
		"blackout":     resolumeConfirmed("power relay confirmed open", now),
		"launchColumn": resolumeConfirmed("fm off", now),
	}, map[string]string{"lamps-off": "launchClip", "power-off-action": "blackout", "fm-off": "launchColumn"})

	f.h.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, f.st))
	sent := map[string]bool{}
	for _, c := range f.res.calls {
		sent[c.action] = true
	}
	if !sent["launchColumn"] || !sent["launchClip"] || sent["blackout"] {
		t.Fatalf("first tick sent %v, want the FM cut and the lamp step, not the projector power cut", sent)
	}
}

// A step that must confirm and does not stops its sequence before the
// power cut and says which sequence stopped.
func TestNightPowerOff_UnconfirmedStepStopsTheSequenceBeforeThePowerCut(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	f := newNightPowerOffFixture(t, &now, nightOnlySequences(projectorSequence()), map[string]ResolumeActionResult{
		"launchClip": resolumeUnconfirmed("no evidence arrived before the deadline", now),
		"blackout":   resolumeConfirmed("power relay confirmed open", now),
	}, map[string]string{"lamps-off": "launchClip", "power-off-action": "blackout"})

	f.h.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, f.st))
	for _, call := range f.res.calls {
		if call.action == "blackout" {
			t.Fatalf("the power cut was sent even though the lamp step never confirmed")
		}
	}
	final := mustGetCurrentSession(t, f.st)
	if !strings.Contains(final.PowerPhase, "Projectors") || !strings.Contains(final.PowerPhase, "Check it by hand") {
		t.Fatalf("PowerPhase = %q, want it to name the stopped sequence and the manual check", final.PowerPhase)
	}
}

// A restart between steps resumes without repeating a finished step.
func TestNightPowerOff_RestartDoesNotRepeatAFinishedStep(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	f := newNightPowerOffFixture(t, &now, nightOnlySequences(projectorSequence()), map[string]ResolumeActionResult{
		"launchClip": resolumeConfirmed("projectors confirmed off", now),
		"blackout":   resolumeConfirmed("power relay confirmed open", now),
	}, map[string]string{"lamps-off": "launchClip", "power-off-action": "blackout"})
	f.h.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, f.st))

	freshRes := &fakeResolumeActionDispatcher{results: map[string]ResolumeActionResult{
		"launchClip": resolumeConfirmed("projectors confirmed off", now),
		"blackout":   resolumeConfirmed("power relay confirmed open", now),
	}}
	freshH := &handlers{deps: Dependencies{NightSessions: f.st, Config: f.st, ResolumeActions: freshRes}.withDefaults(), clock: func() time.Time { return now }, logger: testLogger()}
	now = now.Add(5 * time.Second)
	freshH.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, f.st))
	if last, ok := freshRes.lastCall(); freshRes.callCount() != 1 || !ok || last.action != "blackout" {
		t.Fatalf("calls after restart = %d, last = %+v, want only the power cut", freshRes.callCount(), last)
	}
}

// A night that runs no sequence records not_configured.
func TestNightPowerOff_NoSequencesRecordsNotConfigured(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	f := newNightPowerOffFixture(t, &now, nil, nil, nil)
	f.h.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, f.st))
	if got := mustGetCurrentSession(t, f.st).PowerPhase; got != nightShutdownPowerPhaseNotConfigured {
		t.Fatalf("PowerPhase = %q, want %q", got, nightShutdownPowerPhaseNotConfigured)
	}
}

// A step carrying an environmental domain, never producible through
// validation, is refused at run time and never sent.
func TestNightPowerOff_NonPresentationStepIsRefused(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	seq := config.ShutdownSequence{ID: "heat", Label: "Heat", Steps: []config.ShutdownStep{{Action: "power-off-action", PowerDomain: config.NightPowerDomainEnvironmental, DomainProvenance: config.NightDomainProvenanceOperatorDeclared}}}
	f := newNightPowerOffFixture(t, &now, nightOnlySequences(seq), map[string]ResolumeActionResult{
		"blackout": resolumeConfirmed("power relay confirmed open", now),
	}, map[string]string{"power-off-action": "blackout"})
	f.h.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, f.st))
	if got := f.res.callCount(); got != 0 {
		t.Fatalf("resolume calls for an environmental step = %d, want 0", got)
	}
	if final := mustGetCurrentSession(t, f.st); !strings.Contains(final.PowerPhase, "Heat") {
		t.Fatalf("PowerPhase = %q, want it to name the stopped sequence", final.PowerPhase)
	}
}

// A crash after a step was marked sent but before its outcome was recorded
// lands on ambiguous and stops the sequence, never retried or assumed.
func TestNightPowerOff_CrashMidDispatchIsAmbiguousNotGuessed(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	f := newNightPowerOffFixture(t, &now, nightOnlySequences(projectorSequence()), map[string]ResolumeActionResult{
		"launchClip": resolumeConfirmed("projectors confirmed off", now),
		"blackout":   resolumeConfirmed("power relay confirmed open", now),
	}, map[string]string{"lamps-off": "launchClip", "power-off-action": "blackout"})
	dispatchedAt := now
	cueName := nightShutdownStepName("night", "projectors", 0)
	if err := f.st.InsertNightCueOutboxRow(context.Background(), store.NightCueOutboxRecord{
		ID: uuid.NewString(), SessionID: "sess-1", Cycle: 1, Phase: nightPhaseShutdownStep, CueName: cueName,
		ActionRevision: 1, State: nightCueStateDispatched, DispatchedAt: &dispatchedAt,
	}, now); err != nil {
		t.Fatalf("insert dispatched outbox row: %v", err)
	}
	f.h.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, f.st))
	if got := f.res.callCount(); got != 0 {
		t.Fatalf("resolume calls after an ambiguous row = %d, want 0", got)
	}
	row, err := f.st.GetNightCueOutboxRow(context.Background(), "sess-1", 1, nightPhaseShutdownStep, cueName)
	if err != nil || row.State != nightCueStateAmbiguous {
		t.Fatalf("row = %+v, err = %v, want ambiguous", row, err)
	}
	if final := mustGetCurrentSession(t, f.st); !strings.Contains(final.PowerPhase, "Projectors") {
		t.Fatalf("PowerPhase = %q, want the stopped sequence named", final.PowerPhase)
	}
}

// End session actions run once the power down has finished, and a hard
// stop's marker keeps them from running at all.
func TestNightEndSessionActionsRunAfterPowerDownAndNotAfterHardStop(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	actions := &config.NightSiteActions{EndSession: []string{"standby"}}
	f := newNightPowerOffFixture(t, &now, actions, map[string]ResolumeActionResult{
		"launchClip": resolumeConfirmed("standby", now),
	}, map[string]string{"standby": "launchClip"})

	f.h.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, f.st))
	if got := f.res.callCount(); got != 0 {
		t.Fatalf("calls on the tick that resolves the power phase = %d, want 0", got)
	}
	f.h.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, f.st))
	f.h.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, f.st))
	if got := f.res.callCount(); got != 1 {
		t.Fatalf("end session action calls = %d, want exactly 1", got)
	}

	g := newNightPowerOffFixture(t, &now, actions, map[string]ResolumeActionResult{
		"launchClip": resolumeConfirmed("standby", now),
	}, map[string]string{"standby": "launchClip"})
	if err := g.h.nightSkipEndSessionActions(context.Background(), now, mustGetCurrentSession(t, g.st)); err != nil {
		t.Fatalf("skip: %v", err)
	}
	g.h.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, g.st))
	g.h.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, g.st))
	if got := g.res.callCount(); got != 0 {
		t.Fatalf("end session action calls after a hard stop = %d, want 0", got)
	}
}

func putSingletonConfig(t *testing.T, st *store.Store, kind, payload string) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.CreateConfigObject(ctx, kind, "default"); err != nil {
		t.Fatalf("create %s: %v", kind, err)
	}
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{Kind: kind, ObjectID: "default", Revision: 1, PayloadJSON: payload, Source: "api"}); err != nil {
		t.Fatalf("create %s revision: %v", kind, err)
	}
	if _, err := st.ActivateConfigRevision(ctx, kind, "default", 1); err != nil {
		t.Fatalf("activate %s: %v", kind, err)
	}
}

// A power down forced by emergency stop level 2 uses that level's skip
// list in place of the night's.
func TestNightPowerOff_EmergencyLevelTwoPicksTheSequences(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	f := newNightPowerOffFixture(t, &now, &config.NightSiteActions{PowerDown: &config.NightSitePowerDown{SkipSequences: []string{"lights"}}}, map[string]ResolumeActionResult{
		"launchClip":   resolumeConfirmed("fm off", now),
		"launchColumn": resolumeConfirmed("lights off", now),
	}, map[string]string{"fm-off": "launchClip", "lights-off": "launchColumn"})
	putSingletonConfig(t, f.st, config.ShowSitePowerConfigKind, `{"shutdownSequences":[{"id":"fm","label":"FM","steps":[{"action":"fm-off"}]},{"id":"lights","label":"Lights","steps":[{"action":"lights-off"}]}],"powerOnActions":[]}`)
	putSingletonConfig(t, f.st, config.ShowEmergencyStopConfigKind, `{"stop":{"actions":[]},"stopPowerDown":{"actions":[],"skipSequences":["fm"]},"hardStop":{"actions":[]}}`)

	rec := mustGetCurrentSession(t, f.st)
	if err := f.h.nightMarkEmergencySelection(context.Background(), now, rec); err != nil {
		t.Fatal(err)
	}
	f.h.nightAdvancePowerOff(context.Background(), now, rec)
	if last, ok := f.res.lastCall(); f.res.callCount() != 1 || !ok || last.action != "launchColumn" {
		t.Fatalf("calls = %d, last = %+v, want only the lights sequence, which level 2 keeps and the night skips", f.res.callCount(), last)
	}
}
