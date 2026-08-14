package main

import (
	"strings"
	"testing"
	"time"
)

func fixtureMacroConfig(revision int, label string, steps ...configShowMacroStep) showMacroConfigResponse {
	return showMacroConfigResponse{
		Revision: revision,
		Payload:  configShowMacro{Label: label, Steps: steps},
	}
}

func TestLocalPolicyStatementNoCache(t *testing.T) {
	dir := t.TempDir()
	got := localPolicyStatement(dir, "unknown-macro", time.Now())
	if !strings.Contains(got, "unknown-macro") {
		t.Errorf("statement %q does not name the macro", got)
	}
	if !strings.Contains(got, "unknown") {
		t.Errorf("statement %q does not say the policy is unknown", got)
	}
	// The critical property this test protects: with no cache, the
	// statement must NEVER claim a specific class (none/coordinator-
	// required/silence) for any step, because that would be inventing an
	// answer the definition never gave this program a way to read.
	for _, forbidden := range []string{"coordinator-required", "\"none\"", "silence"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("statement %q names a specific local-fallback class with no cache present — this must never happen", got)
		}
	}
}

func TestUpdateMacroCacheThenLocalPolicyStatement(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 14, 17, 0, 0, 0, time.UTC)
	cfg := fixtureMacroConfig(4, "Begin Set",
		configShowMacroStep{ID: "projectors", LocalFallback: configShowMacroLocalFallback{
			Class: "coordinator-required", Reason: "projector control runs on the coordinator",
		}},
		configShowMacroStep{ID: "start", LocalFallback: configShowMacroLocalFallback{
			Class: "none", Reason: "no local delivery path exists for this step",
		}},
	)
	if err := updateMacroCache(dir, "projectors-on", cfg, now); err != nil {
		t.Fatal(err)
	}

	later := now.Add(90 * time.Minute)
	got := localPolicyStatement(dir, "projectors-on", later)

	if !strings.Contains(got, "Begin Set") {
		t.Errorf("statement does not name the macro's own LABEL (the prose an operator reads, not just its id): %q", got)
	}
	if !strings.Contains(got, "projectors-on") {
		t.Errorf("statement does not name the macro id: %q", got)
	}
	if !strings.Contains(got, "revision 4") {
		t.Errorf("statement does not name the cached revision: %q", got)
	}
	if !strings.Contains(got, `"projectors"`) || !strings.Contains(got, "cannot run locally") {
		t.Errorf("statement does not describe the coordinator-required step: %q", got)
	}
	if !strings.Contains(got, "projector control runs on the coordinator") {
		t.Errorf("statement does not carry the DEFINITION'S OWN reason text for the coordinator-required step: %q", got)
	}
	if !strings.Contains(got, `"start"`) || !strings.Contains(got, "nothing runs locally") {
		t.Errorf("statement does not describe the none-fallback step: %q", got)
	}
	if !strings.Contains(got, "no local delivery path exists for this step") {
		t.Errorf("statement does not carry the definition's own reason text for the none-fallback step: %q", got)
	}
}

func TestLocalPolicyStatementFallsBackToMacroIDWhenLabelIsEmpty(t *testing.T) {
	// A definition with an empty label (allowed on the wire — ConfigShowMacro
	// requires the key but not a non-empty value) must not render as a
	// literal empty string where the macro's name should read.
	dir := t.TempDir()
	now := time.Now()
	cfg := fixtureMacroConfig(1, "", configShowMacroStep{ID: "s1", LocalFallback: configShowMacroLocalFallback{Class: "silence", Reason: "r"}})
	if err := updateMacroCache(dir, "macro-x", cfg, now); err != nil {
		t.Fatal(err)
	}
	got := localPolicyStatement(dir, "macro-x", now)
	if !strings.Contains(got, "macro-x") {
		t.Errorf("statement should fall back to the macro id when label is empty: %q", got)
	}
}

func TestLocalPolicyStatementDoesNotConfuseDifferentMacros(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	cfg := fixtureMacroConfig(1, "Macro A", configShowMacroStep{ID: "s1", LocalFallback: configShowMacroLocalFallback{Class: "silence", Reason: "r"}})
	if err := updateMacroCache(dir, "macro-a", cfg, now); err != nil {
		t.Fatal(err)
	}

	got := localPolicyStatement(dir, "macro-b", now)
	if strings.Contains(got, "silence") {
		t.Errorf("statement for macro-b leaked macro-a's cached policy: %q", got)
	}
	if !strings.Contains(got, "unknown") {
		t.Errorf("statement for an uncached macro-b should say unknown: %q", got)
	}
}

func TestCachedRevisionFor(t *testing.T) {
	dir := t.TempDir()
	if _, ok := cachedRevisionFor(dir, "macro-a"); ok {
		t.Fatal("expected no cached revision before anything is cached")
	}
	cfg := fixtureMacroConfig(9, "Macro A")
	if err := updateMacroCache(dir, "macro-a", cfg, time.Now()); err != nil {
		t.Fatal(err)
	}
	rev, ok := cachedRevisionFor(dir, "macro-a")
	if !ok || rev != 9 {
		t.Errorf("cachedRevisionFor = (%d, %v), want (9, true)", rev, ok)
	}
	if _, ok := cachedRevisionFor(dir, "macro-b"); ok {
		t.Error("expected no cached revision for a different, never-cached macro id")
	}
}
