package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is the test suite for nightCheckAudioAlignment/
// nightCheckAudioAlignmentForNode (nightaudioreadiness.go): a measured
// alignment beyond audio.settings' driftIgnoreThresholdMs warns on the
// night-session readiness read, and never blocks start-night.

// seedAudioNodeConfigObject activates an "audio.node" config object
// directly through st, bypassing the PUT API's own placement-evidence
// validation (audionode_test.go), which this file's checks do not
// exercise.
func seedAudioNodeConfigObject(t *testing.T, st *store.Store, id string) {
	t.Helper()
	rev, err := st.CreateConfigRevision(context.Background(), store.ConfigRevisionRecord{
		Kind: config.AudioNodeConfigKind, ObjectID: id, Revision: 1,
		PayloadJSON: validAudioNodeBody, Source: config.AudioSettingsSourceAPI,
	})
	if err != nil {
		t.Fatalf("create audio.node config revision: %v", err)
	}
	if _, err := st.ActivateConfigRevision(context.Background(), config.AudioNodeConfigKind, id, rev.Revision); err != nil {
		t.Fatalf("activate audio.node config revision: %v", err)
	}
}

// alignmentStateObs builds a node.audio.clock.alignment.state observation
// current as of now.
func alignmentStateObs(nodeID, value string, now time.Time) observation.Observation {
	observedAt := now
	return observation.Observation{
		Resource: observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID},
		Signal:   "node.audio.clock.alignment.state", Value: value,
		ObservedAt: &observedAt, CollectedAt: now, ValidFor: time.Minute,
		Source: "node-audio:" + nodeID, Quality: observation.QualityDirect,
	}
}

// alignmentValueObs builds the sibling node.audio.clock.alignment
// observation (the measured offset in ms) current as of now.
func alignmentValueObs(nodeID string, offsetMs int64, now time.Time) observation.Observation {
	observedAt := now
	return observation.Observation{
		Resource: observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID},
		Signal:   "node.audio.clock.alignment", Value: offsetMs,
		ObservedAt: &observedAt, CollectedAt: now, ValidFor: time.Minute,
		Source: "node-audio:" + nodeID, Quality: observation.QualityDirect,
	}
}

func TestNightCheckAudioAlignment_BeyondThresholdReportsDegradedNamingBothNumbers(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	deps := showConfigTestDeps(svc, st)
	audio := &fakeNodeAudioLister{}
	deps.Audio = audio
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(testNow), logger: testLogger()}

	seedAudioNodeConfigObject(t, st, "audio-01")
	audio.setObservations("audio-01", []observation.Observation{
		alignmentStateObs("audio-01", "beyond_threshold", testNow),
		alignmentValueObs("audio-01", 60, testNow),
	})

	checks := h.nightCheckAudioAlignment(context.Background(), testNow)
	if len(checks) != 1 {
		t.Fatalf("checks = %d, want 1", len(checks))
	}
	c := checks[0]
	if c.name != "audio:alignment:audio-01" {
		t.Errorf("check name = %q, want audio:alignment:audio-01", c.name)
	}
	if c.health != nightHealthDegraded() {
		t.Errorf("health = %q, want degraded (a warning, never a failure)", c.health)
	}
	if !strings.Contains(c.reason, "60ms") {
		t.Errorf("reason = %q, want it to name the measured offset 60ms", c.reason)
	}
	if !strings.Contains(c.reason, "40ms") {
		t.Errorf("reason = %q, want it to name the shipped default threshold 40ms", c.reason)
	}
	if outcome := nightOutcomeFromChecks(checks); outcome != "ready_with_warnings" {
		t.Errorf("nightOutcomeFromChecks = %q, want ready_with_warnings", outcome)
	}
}

func TestNightCheckAudioAlignment_WithinThresholdReportsHealthy(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	deps := showConfigTestDeps(svc, st)
	audio := &fakeNodeAudioLister{}
	deps.Audio = audio
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(testNow), logger: testLogger()}

	seedAudioNodeConfigObject(t, st, "audio-01")
	audio.setObservations("audio-01", []observation.Observation{
		alignmentStateObs("audio-01", "within_threshold", testNow),
		alignmentValueObs("audio-01", 12, testNow),
	})

	checks := h.nightCheckAudioAlignment(context.Background(), testNow)
	if len(checks) != 1 {
		t.Fatalf("checks = %d, want 1", len(checks))
	}
	if checks[0].health != nightHealthHealthy() {
		t.Errorf("health = %q, want healthy", checks[0].health)
	}
	if outcome := nightOutcomeFromChecks(checks); outcome != "ready" {
		t.Errorf("nightOutcomeFromChecks = %q, want ready", outcome)
	}
}

// TestNightCheckAudioAlignment_NoObservationReportsUnknown proves a
// configured audio.node that has never reported node.audio.clock.
// alignment.state at all reads unknown, not not_verifiable: missing
// evidence, unlike an explicit not_collected reading, is not a legitimate
// non-claim.
func TestNightCheckAudioAlignment_NoObservationReportsUnknown(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	deps := showConfigTestDeps(svc, st)
	audio := &fakeNodeAudioLister{}
	deps.Audio = audio
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(testNow), logger: testLogger()}

	seedAudioNodeConfigObject(t, st, "audio-01")

	checks := h.nightCheckAudioAlignment(context.Background(), testNow)
	if len(checks) != 1 {
		t.Fatalf("checks = %d, want 1", len(checks))
	}
	if checks[0].health != nightHealthUnknown() {
		t.Errorf("health = %q, want unknown", checks[0].health)
	}
	if outcome := nightOutcomeFromChecks(checks); outcome != "unknown" {
		t.Errorf("nightOutcomeFromChecks = %q, want unknown", outcome)
	}
}

// TestNightCheckAudioAlignment_NotCollectedIsNotVerifiableAndDoesNotAffectOutcome
// proves a configured audio.node whose alignment.state observation exists
// but carries StateNotCollected (the underlying alignment sample itself
// was never measured, a legitimate non-claim per RES-019) reads
// not_verifiable, excluded from the aggregate.
func TestNightCheckAudioAlignment_NotCollectedIsNotVerifiableAndDoesNotAffectOutcome(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	deps := showConfigTestDeps(svc, st)
	audio := &fakeNodeAudioLister{}
	deps.Audio = audio
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(testNow), logger: testLogger()}

	seedAudioNodeConfigObject(t, st, "audio-01")
	audio.setObservations("audio-01", []observation.Observation{
		{
			Resource:    observation.ResourceRef{Kind: observation.ResourceNode, ID: "audio-01"},
			Signal:      "node.audio.clock.alignment.state",
			Absence:     observation.StateNotCollected,
			Reason:      "program branch has not rendered up to its presented position; underrun suspected",
			CollectedAt: testNow,
			Source:      "node-audio:audio-01", Quality: observation.QualityDirect,
		},
	})

	checks := h.nightCheckAudioAlignment(context.Background(), testNow)
	if len(checks) != 1 {
		t.Fatalf("checks = %d, want 1", len(checks))
	}
	if checks[0].health != nightCheckStateNotVerifiable {
		t.Errorf("health = %q, want not_verifiable", checks[0].health)
	}
	if outcome := nightOutcomeFromChecks(checks); outcome != "ready" {
		t.Errorf("nightOutcomeFromChecks = %q, want ready (not_verifiable excluded from the aggregate)", outcome)
	}
}

// TestNightCheckAudioAlignment_StaleReportsUnknown proves an
// alignment.state observation that has aged past its ValidFor reads
// unknown, never the not_verifiable a legitimate non-claim gets: the
// node was reporting and stopped, which is not the same as never having
// made a claim.
func TestNightCheckAudioAlignment_StaleReportsUnknown(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	deps := showConfigTestDeps(svc, st)
	audio := &fakeNodeAudioLister{}
	deps.Audio = audio
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(testNow), logger: testLogger()}

	seedAudioNodeConfigObject(t, st, "audio-01")
	observedAt := testNow.Add(-time.Hour)
	audio.setObservations("audio-01", []observation.Observation{
		{
			Resource:    observation.ResourceRef{Kind: observation.ResourceNode, ID: "audio-01"},
			Signal:      "node.audio.clock.alignment.state",
			Value:       "within_threshold",
			ObservedAt:  &observedAt,
			CollectedAt: testNow, ValidFor: time.Minute,
			Source: "node-audio:audio-01", Quality: observation.QualityDirect,
		},
	})

	checks := h.nightCheckAudioAlignment(context.Background(), testNow)
	if len(checks) != 1 {
		t.Fatalf("checks = %d, want 1", len(checks))
	}
	if checks[0].health != nightHealthUnknown() {
		t.Errorf("health = %q, want unknown", checks[0].health)
	}
	if outcome := nightOutcomeFromChecks(checks); outcome != "unknown" {
		t.Errorf("nightOutcomeFromChecks = %q, want unknown", outcome)
	}
}

// TestNightCheckAudioAlignment_NoConfiguredAudioNodesReportsNoChecks
// proves this check adds nothing when no audio.node object is configured
// at all, matching how every other configured-collection readiness check
// in this package behaves.
func TestNightCheckAudioAlignment_NoConfiguredAudioNodesReportsNoChecks(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	deps := showConfigTestDeps(svc, st)
	deps.Audio = &fakeNodeAudioLister{}
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(testNow), logger: testLogger()}

	if checks := h.nightCheckAudioAlignment(context.Background(), testNow); len(checks) != 0 {
		t.Fatalf("checks = %d, want 0 with no audio.node configured", len(checks))
	}
}

// TestNightSession_AudioAlignmentBeyondThresholdWarnsButStartNightProceeds
// runs through the real night-session command lifecycle: with the
// threshold at its default and a measured alignment beyond it,
// run-readiness reports ready_with_warnings AND start-night proceeds.
func TestNightSession_AudioAlignmentBeyondThresholdWarnsButStartNightProceeds(t *testing.T) {
	advanceFn, now := mutableClock(testNow)
	svc, st, _ := newTestIdentityServiceWithStore(t, now)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	opToken := mustIssueToken(t, svc, operator.ID)

	deps, obs := nightControlTestDeps(svc, st)
	audio := &fakeNodeAudioLister{}
	deps.Audio = audio
	deps.FPP = nightWireFPPForReadiness(t)
	backend := nightTestAssetBackend(t)
	deps.AssetBackend = backend

	api := New(deps, Options{Clock: now, Logger: testLogger(), NightReadinessMaxAge: time.Hour})

	mustPutShow(t, api, adminToken, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutShowAction(t, api, adminToken, "lighting-fade-out", validShowActionFPPBody)
	mustCreateNightSessionFSEQAsset(t, st, backend, "halloween-2026", "resting-loop", "player-01")
	mustPutNightSession(t, api, adminToken, "halloween-main", validNightSessionBody)
	mustActivateNightSession(t, api, adminToken, "halloween-main")

	seedAudioNodeConfigObject(t, st, "audio-01")
	audio.setObservations("audio-01", []observation.Observation{
		alignmentStateObs("audio-01", "beyond_threshold", now()),
		alignmentValueObs("audio-01", 60, now()),
	})

	setHealthyFPPReachable(obs, now())
	mustNightCommand(t, api, opToken, "prepare-site")
	readiness := mustNightCommand(t, api, opToken, "run-readiness")
	if readiness.Session.Readiness.Outcome != "ready_with_warnings" {
		t.Fatalf("readiness outcome = %q, want ready_with_warnings", readiness.Session.Readiness.Outcome)
	}
	var sawAlignmentWarning bool
	for _, c := range readiness.Session.Readiness.Checks {
		if c.Name == "audio:alignment:audio-01" {
			sawAlignmentWarning = c.State == "degraded"
		}
	}
	if !sawAlignmentWarning {
		t.Fatalf("readiness checks = %+v, want a degraded audio:alignment:audio-01 check", readiness.Session.Readiness.Checks)
	}

	mustNightCommand(t, api, opToken, "start-preshow")
	out := mustNightCommand(t, api, opToken, "start-night")
	if out.Session.State != "transition-to-show" {
		t.Fatalf("start-night with a degraded audio alignment warning: state = %q, want transition-to-show (a warning must never stop the show)", out.Session.State)
	}
	_ = advanceFn
}
