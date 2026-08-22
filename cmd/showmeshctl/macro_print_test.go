package main

import (
	"bytes"
	"strings"
	"testing"
)

// This file is the protection Step 9 wave 3 review finding 1 named
// missing: macro_print.go's rendering had no test at all, and the
// reviewer proved that by mutating it three separate ways — collapsing
// "completed but not confirmed" onto "confirmed" (STEP-9-SPEC.md section
// 2.3's own forbidden collapse), deleting the attributionDegraded warning
// line, and blanking the per-step outcome reason column — and the entire
// suite stayed green for all three. Each test below is written against
// that failure mode specifically: see each test's own doc comment for
// which mutation it exists to catch, and this file's report entry for
// which mutations were actually run to confirm these tests bite.

// --- macroRunOutcomeGlyph: the three finished renderings are pairwise
// distinct ---

// TestMacroRunOutcomeGlyphFinishedRenderingsArePairwiseDistinct is this
// file's direct answer to the review's core finding: it asserts all three
// (four, counting the nil-evidence case fixed alongside this test) of
// macroRunOutcomeGlyph's finished-state outputs are DIFFERENT FROM EACH
// OTHER, not merely non-empty. A future edit that merges "confirmed" and
// "completed, not confirmed" into one label — exactly the mutation the
// reviewer applied — makes two of these equal and this test fails.
func TestMacroRunOutcomeGlyphFinishedRenderingsArePairwiseDistinct(t *testing.T) {
	tru, fls := true, false

	confirmed := macroRunOutcomeGlyph("finished", &tru, &tru, "")
	completedNotConfirmed := macroRunOutcomeGlyph("finished", &tru, &fls, "")
	aborted := macroRunOutcomeGlyph("finished", &fls, &fls, "")
	outcomeNotReported := macroRunOutcomeGlyph("finished", nil, nil, "")

	renderings := map[string]string{
		"confirmed":                confirmed,
		"completed, not confirmed": completedNotConfirmed,
		"aborted":                  aborted,
		"outcome not reported":     outcomeNotReported,
	}

	seen := make(map[string]string, len(renderings))
	for name, rendering := range renderings {
		if other, dup := seen[rendering]; dup {
			t.Errorf("macroRunOutcomeGlyph(%s) and macroRunOutcomeGlyph(%s) render identically as %q; "+
				"STEP-9-SPEC.md section 2.3 requires every one of these to be legible as a DIFFERENT fact",
				name, other, rendering)
		}
		seen[rendering] = name
	}

	// Also pin the specific case the reviewer's mutation collided:
	// "confirmed" must never contain the word this label uses for the
	// unconfirmed case, and vice versa.
	if strings.Contains(strings.ToUpper(confirmed), "NOT CONFIRMED") {
		t.Errorf("confirmed rendering = %q, must not read as unconfirmed", confirmed)
	}
	if !strings.Contains(strings.ToUpper(completedNotConfirmed), "NOT CONFIRMED") {
		t.Errorf("completed-not-confirmed rendering = %q, want it to say so", completedNotConfirmed)
	}
	if !strings.Contains(strings.ToUpper(aborted), "ABORTED") {
		t.Errorf("aborted rendering = %q, want it to say ABORTED", aborted)
	}
}

// TestMacroRunOutcomeGlyphShowsReasonWheneverEitherBooleanIsFalse proves
// STEP-9-SPEC.md section 2.3's "whenever either is false the run carries
// a reason naming the step and the cause" reaches the rendered text, for
// every combination where at least one of completed/confirmed is false —
// this is the day-0 "projectors-on" macro's own case (an MQTT step with
// no expected response reports completed=true, confirmed=false on every
// correct run, per the task spec's own framing).
func TestMacroRunOutcomeGlyphShowsReasonWheneverEitherBooleanIsFalse(t *testing.T) {
	tru, fls := true, false
	const reason = `step "projectors" produced no confirming evidence: MQTT action declares no expected response`

	cases := []struct {
		name                 string
		completed, confirmed *bool
	}{
		{"completed true, confirmed false", &tru, &fls},
		{"completed false, confirmed true", &fls, &tru},
		{"completed false, confirmed false", &fls, &fls},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := macroRunOutcomeGlyph("finished", c.completed, c.confirmed, reason)
			if !strings.Contains(got, reason) {
				t.Errorf("macroRunOutcomeGlyph(%s, reason=%q) = %q, want the reason included", c.name, reason, got)
			}
		})
	}

	// The fully-clean case must NOT force a reason to be shown when there
	// isn't one — confirming this doesn't degenerate into always printing
	// something reason-shaped.
	if got := macroRunOutcomeGlyph("finished", &tru, &tru, ""); strings.Contains(got, "(") {
		t.Errorf("macroRunOutcomeGlyph(completed=true, confirmed=true, reason=\"\") = %q, want no parenthetical reason", got)
	}
}

// --- printMacroRunDetail: the attributionDegraded warning ---

// TestPrintMacroRunDetailShowsAttributionDegradedWarning is this file's
// direct catch for the reviewer's second mutation (deleting the
// attributionDegraded warning line): it requires the warning text to be
// present when the flag is true and ABSENT when it is false, so a mutant
// that always prints (or never prints) the line fails either half.
func TestPrintMacroRunDetailShowsAttributionDegradedWarning(t *testing.T) {
	tru := true
	base := macroRun{
		ID: "run-1", MacroObjectID: "m1", MacroRevision: 1, Show: "s1", Trigger: "cli",
		IssuerPrincipalName: "admin", State: "finished", Completed: &tru, Confirmed: &tru,
	}

	degraded := base
	degraded.AttributionDegraded = true
	var buf bytes.Buffer
	printMacroRunDetail(&buf, degraded)
	if !strings.Contains(strings.ToUpper(buf.String()), "DEGRADED") {
		t.Errorf("printMacroRunDetail(attributionDegraded=true) output missing a degraded-attribution warning:\n%s", buf.String())
	}

	clean := base
	clean.AttributionDegraded = false
	buf.Reset()
	printMacroRunDetail(&buf, clean)
	if strings.Contains(strings.ToUpper(buf.String()), "DEGRADED") {
		t.Errorf("printMacroRunDetail(attributionDegraded=false) output contains a degraded-attribution warning it should not:\n%s", buf.String())
	}
}

// --- printMacroRunDetail: the per-step outcome reason column ---

// TestPrintMacroRunDetailRendersPerStepOutcomeReason is this file's direct
// catch for the reviewer's third mutation (blanking the per-step outcome
// reason column): a step whose OutcomeReason is a distinctive string must
// have that string appear somewhere in the rendered output.
func TestPrintMacroRunDetailRendersPerStepOutcomeReason(t *testing.T) {
	tru := true
	const reason = "collection_failed: the FPP collector could not reach bench-fpp-1 during the confirmation window"
	run := macroRun{
		ID: "run-1", MacroObjectID: "m1", MacroRevision: 1, Show: "s1", Trigger: "cli",
		IssuerPrincipalName: "admin", State: "finished", Completed: &tru, Confirmed: &tru,
		Steps: []macroRunStep{
			{StepIndex: 0, StepID: "stop", State: "resolved", Outcome: "unconfirmed", OutcomeReason: reason},
		},
	}
	var buf bytes.Buffer
	printMacroRunDetail(&buf, run)
	if !strings.Contains(buf.String(), reason) {
		t.Errorf("printMacroRunDetail output missing step outcome reason %q:\n%s", reason, buf.String())
	}
}

// --- printMacroRunDetail: step command evidence (review finding 9) ---

// TestPrintMacroRunDetailRendersStepCommandEvidence proves
// macroRunStepCommand's state/id/reason — decoded off the wire per this
// API's "absent evidence is stated, never omitted" rule — actually reach
// the operator in the default text renderer, for all three command
// states, not just decoded and discarded.
func TestPrintMacroRunDetailRendersStepCommandEvidence(t *testing.T) {
	tru := true
	id := "cmd-123"
	run := macroRun{
		ID: "run-1", MacroObjectID: "m1", MacroRevision: 1, Show: "s1", Trigger: "cli",
		IssuerPrincipalName: "admin", State: "finished", Completed: &tru, Confirmed: &tru,
		Steps: []macroRunStep{
			{StepIndex: 0, StepID: "notify", Outcome: "unconfirmable",
				Command: macroRunStepCommand{State: "none", Reason: "mqtt action declares no expected response"}},
			{StepIndex: 1, StepID: "stop", Outcome: "confirmed",
				Command: macroRunStepCommand{State: "retained", ID: &id}},
			{StepIndex: 2, StepID: "start", Outcome: "confirmed",
				Command: macroRunStepCommand{State: "not_retained", ID: &id, Reason: "commands row pruned by retention"}},
		},
	}
	var buf bytes.Buffer
	printMacroRunDetail(&buf, run)
	out := buf.String()

	for _, want := range []string{
		"mqtt action declares no expected response",
		"cmd-123",
		"commands row pruned by retention",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("printMacroRunDetail output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(strings.ToUpper(out), "NOT RETAINED") {
		t.Errorf("printMacroRunDetail output missing a NOT RETAINED marker for the pruned step's command:\n%s", out)
	}
}

// TestPrintMacroRunDetailRendersOutcomeState proves macroRunStep's own
// OutcomeState (the six-value evidence-state vocabulary a step's outcome
// was decided from) reaches the operator, not merely Outcome/OutcomeReason.
func TestPrintMacroRunDetailRendersOutcomeState(t *testing.T) {
	tru := true
	run := macroRun{
		ID: "run-1", MacroObjectID: "m1", MacroRevision: 1, Show: "s1", Trigger: "cli",
		IssuerPrincipalName: "admin", State: "finished", Completed: &tru, Confirmed: &tru,
		Steps: []macroRunStep{
			{StepIndex: 0, StepID: "stop", Outcome: "unconfirmed", OutcomeState: "collection_failed"},
		},
	}
	var buf bytes.Buffer
	printMacroRunDetail(&buf, run)
	if !strings.Contains(buf.String(), "collection_failed") {
		t.Errorf("printMacroRunDetail output missing outcomeState %q:\n%s", "collection_failed", buf.String())
	}
}

// TestPrintShowActionDetailRendersAudioTarget defends the fourth
// show.action.target.integration (Track F seam F5): the operator-facing
// "action get" detail must show the audio node/session/action rather than
// falling into the "(unrecognized integration)" default branch.
// Mutation-checked: deleting the "audio" case from printShowActionDetail's
// switch makes this fail on the "  Node:" assertion.
func TestPrintShowActionDetailRendersAudioTarget(t *testing.T) {
	resp := showActionConfigResponse{
		ID: "hush-background", Revision: 3,
		Payload: showAction{
			Show: "halloween-2026", Label: "Hush resting background audio", SafetyClass: "stop",
			Target: showActionTarget{
				Integration: "audio", AudioNodeID: "node-a", AudioSessionID: "resting-bg",
				AudioAction: "audio.session.stop",
			},
		},
	}
	var buf bytes.Buffer
	printShowActionDetail(&buf, resp)
	out := buf.String()
	for _, want := range []string{"node-a", "resting-bg", "audio.session.stop"} {
		if !strings.Contains(out, want) {
			t.Errorf("printShowActionDetail output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "unrecognized integration") {
		t.Errorf("printShowActionDetail fell into the unrecognized-integration default for \"audio\":\n%s", out)
	}
}
