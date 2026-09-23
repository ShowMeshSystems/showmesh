package api

import (
	"context"
	"errors"
	"testing"
)

// This file is the test suite for nightCheckFPPPluginReportsRefused
// (nightsessioncontrol.go): fpp-plugin-reports-refused, IDENTIFIER-
// REGISTER.md's own reservation.

func TestNightCheckFPPPluginReportsRefused_FailsWhenBoundInstanceIsRefused(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	deps := showConfigTestDeps(svc, st)
	deps.FPP = &fakeFPPLister{views: []FPPInstanceView{
		{
			InstanceID: "bench-fpp",
			PlaylistObservationRefused: &FPPPlaylistObservationRefusal{
				Reason:    "FPP reported a playlist sequence lower than the last one this coordinator accepted.",
				RefusedAt: testNow,
			},
		},
	}}
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(testNow), logger: testLogger()}

	check := h.nightCheckFPPPluginReportsRefused(context.Background(), map[string]bool{"bench-fpp": true})
	if check.name != "fpp-plugin-reports-refused" {
		t.Errorf("name = %q, want fpp-plugin-reports-refused", check.name)
	}
	if check.health != nightHealthFailed() {
		t.Errorf("health = %q, want failed", check.health)
	}
	if check.reason == "" {
		t.Error("reason is empty, want the operator-facing refusal sentence")
	}
}

func TestNightCheckFPPPluginReportsRefused_HealthyWhenNoBoundInstanceIsRefused(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	deps := showConfigTestDeps(svc, st)
	deps.FPP = &fakeFPPLister{views: []FPPInstanceView{
		{InstanceID: "bench-fpp"},
		{InstanceID: "other-fpp", PlaylistObservationRefused: &FPPPlaylistObservationRefusal{Reason: "refused", RefusedAt: testNow}},
	}}
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(testNow), logger: testLogger()}

	// "other-fpp" is refused but not bound to this session, so it must not
	// redden a check this show does not depend on.
	check := h.nightCheckFPPPluginReportsRefused(context.Background(), map[string]bool{"bench-fpp": true})
	if check.health != nightHealthHealthy() {
		t.Errorf("health = %q, want healthy (the bound instance carries no refusal, and the OTHER refused instance is not bound)", check.health)
	}
	if check.reason != "" {
		t.Errorf("reason = %q, want empty on a healthy check", check.reason)
	}
}

func TestNightCheckFPPPluginReportsRefused_UnknownOnListError(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	deps := showConfigTestDeps(svc, st)
	deps.FPP = &fakeFPPLister{err: errors.New("boom")}
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(testNow), logger: testLogger()}

	check := h.nightCheckFPPPluginReportsRefused(context.Background(), map[string]bool{"bench-fpp": true})
	if check.health != nightHealthUnknown() {
		t.Errorf("health = %q, want unknown when the FPP instance list cannot be read", check.health)
	}
}
