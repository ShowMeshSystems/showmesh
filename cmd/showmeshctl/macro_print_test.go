package main

import (
	"bytes"
	"encoding/json"
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

// TestShowActionIdempotentLabelDistinguishesTriState proves
// showActionIdempotentLabel's three renderings (true, false, never
// declared) are pairwise distinct -- in particular that "not declared"
// never reads as "false" -- since collapsing those two is exactly the claim
// about an operator's wiring the system cannot see (api/openapi.yaml's
// ConfigShowAction.idempotent doc comment).
func TestShowActionIdempotentLabelDistinguishesTriState(t *testing.T) {
	tru, fls := true, false

	declaredTrue := showActionIdempotentLabel(&tru)
	declaredFalse := showActionIdempotentLabel(&fls)
	notDeclared := showActionIdempotentLabel(nil)

	renderings := map[string]string{"declared true": declaredTrue, "declared false": declaredFalse, "not declared": notDeclared}
	seen := make(map[string]string, len(renderings))
	for name, rendering := range renderings {
		if other, dup := seen[rendering]; dup {
			t.Errorf("showActionIdempotentLabel(%s) and showActionIdempotentLabel(%s) render identically as %q; "+
				"all three states must be legible as different facts", name, other, rendering)
		}
		seen[rendering] = name
	}
}

// TestPrintShowActionDetailRendersIdempotentTriState proves the tri-state
// reaches the operator-facing "action get" detail text, not just the
// labeling helper in isolation.
func TestPrintShowActionDetailRendersIdempotentTriState(t *testing.T) {
	base := showActionConfigResponse{
		ID: "hush-background", Revision: 1,
		Payload: showAction{Show: "s1", Label: "l1", SafetyClass: "stop"},
	}

	tru, fls := true, false
	cases := []struct {
		name string
		v    *bool
		want string
	}{
		{"declared true", &tru, "true"},
		{"declared false", &fls, "false"},
		{"not declared", nil, "not declared"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := base
			resp.Payload.Idempotent = c.v
			var buf bytes.Buffer
			printShowActionDetail(&buf, resp)
			out := buf.String()
			if !strings.Contains(out, "Idempotent:   "+c.want) {
				t.Errorf("printShowActionDetail(idempotent=%v) missing %q in output:\n%s", c.v, c.want, out)
			}
		})
	}
}

// TestShowActionIdempotentSurvivesDecodeAsTriState is this fix's own
// mutation-proof: showAction.Idempotent must be *bool, not bool, because a
// declared-false action and an action that never declared idempotent both
// decode from JSON with no error and no other signal to tell them apart.
// json.Unmarshal leaves a plain bool at its zero value both when the key is
// absent and when the value is `null` (encoding/json's own documented
// "null has no effect on the value" rule) -- exactly the collapse
// api/openapi.yaml's ConfigShowAction.idempotent doc comment forbids. With
// *bool, absent/null decode to a nil pointer and `false` decodes to a
// non-nil pointer to false, so this test distinguishes all three wire
// shapes. Changing the field back to a plain bool makes the first
// assertion below fail: Idempotent stops being comparable to nil at all
// (a compile failure), and forcing it to compare against the zero value
// instead collapses "absent" and "false" together, which the
// wantNilForAbsent/wantNilForNull assertions catch.
func TestShowActionIdempotentSurvivesDecodeAsTriState(t *testing.T) {
	cases := []struct {
		name    string
		json    string
		wantNil bool
		wantVal bool
	}{
		{"declared true", `{"idempotent":true}`, false, true},
		{"declared false", `{"idempotent":false}`, false, false},
		{"explicit null", `{"idempotent":null}`, true, false},
		{"absent", `{}`, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var a showAction
			if err := json.Unmarshal([]byte(c.json), &a); err != nil {
				t.Fatalf("json.Unmarshal(%s): %v", c.json, err)
			}
			if c.wantNil {
				if a.Idempotent != nil {
					t.Errorf("json.Unmarshal(%s): Idempotent = %v, want nil (never declared)", c.json, *a.Idempotent)
				}
				return
			}
			if a.Idempotent == nil {
				t.Fatalf("json.Unmarshal(%s): Idempotent = nil, want a declared value", c.json)
			}
			if *a.Idempotent != c.wantVal {
				t.Errorf("json.Unmarshal(%s): Idempotent = %v, want %v", c.json, *a.Idempotent, c.wantVal)
			}
		})
	}

	// The property that actually matters: "absent" and "declared false"
	// must decode to DIFFERENT Go values, not both to the zero value.
	var absent, declaredFalse showAction
	if err := json.Unmarshal([]byte(`{}`), &absent); err != nil {
		t.Fatalf("json.Unmarshal({}): %v", err)
	}
	if err := json.Unmarshal([]byte(`{"idempotent":false}`), &declaredFalse); err != nil {
		t.Fatalf("json.Unmarshal({idempotent:false}): %v", err)
	}
	if absent.Idempotent == declaredFalse.Idempotent {
		t.Fatalf("absent and declared-false both decoded to Idempotent=%v; a plain bool field would collapse "+
			"these two distinct wire states into the same Go value", absent.Idempotent)
	}
	if absent.Idempotent != nil {
		t.Errorf("absent payload decoded Idempotent = %v, want nil", *absent.Idempotent)
	}
	if declaredFalse.Idempotent == nil || *declaredFalse.Idempotent != false {
		t.Errorf("declared-false payload decoded Idempotent = %v, want a non-nil pointer to false", declaredFalse.Idempotent)
	}
}
