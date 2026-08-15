package macro

import (
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

// TestNormalizeStepPoliciesFillsEmptyWithDocumentedDefaults pins the two
// defaults at the point resolveMacro actually reads them, not only at the
// point the write surface applies them. See normalizeStepPolicies' own doc
// comment for why an empty value reaching this function is a state worth
// deciding rather than a state worth assuming cannot happen.
func TestNormalizeStepPoliciesFillsEmptyWithDocumentedDefaults(t *testing.T) {
	steps := []config.ShowMacroStep{{ID: "one"}, {ID: "two"}}
	if err := normalizeStepPolicies(steps, "m", 1); err != nil {
		t.Fatalf("normalizeStepPolicies() error = %v, want nil", err)
	}
	for _, st := range steps {
		if st.OnFailure != config.ShowMacroOnFailureContinue {
			t.Errorf("step %q OnFailure = %q, want %q: a run always runs every step (owner decision 2026-08-14)", st.ID, st.OnFailure, config.ShowMacroOnFailureContinue)
		}
		if st.OnUnconfirmed != config.ShowMacroOnUnconfirmedContinue {
			t.Errorf("step %q OnUnconfirmed = %q, want %q: a monitoring gap must never stop a show", st.ID, st.OnUnconfirmed, config.ShowMacroOnUnconfirmedContinue)
		}
	}
}

// TestNormalizeStepPoliciesKeepsExplicitValues confirms normalization only
// fills a gap and never overwrites what the operator wrote down.
func TestNormalizeStepPoliciesKeepsExplicitValues(t *testing.T) {
	steps := []config.ShowMacroStep{{
		ID:            "one",
		OnFailure:     config.ShowMacroOnFailureContinue,
		OnUnconfirmed: config.ShowMacroOnUnconfirmedAbort,
	}}
	if err := normalizeStepPolicies(steps, "m", 1); err != nil {
		t.Fatalf("normalizeStepPolicies() error = %v, want nil", err)
	}
	if steps[0].OnFailure != config.ShowMacroOnFailureContinue {
		t.Errorf("OnFailure = %q, want the explicit %q left untouched", steps[0].OnFailure, config.ShowMacroOnFailureContinue)
	}
	if steps[0].OnUnconfirmed != config.ShowMacroOnUnconfirmedAbort {
		t.Errorf("OnUnconfirmed = %q, want the explicit %q left untouched", steps[0].OnUnconfirmed, config.ShowMacroOnUnconfirmedAbort)
	}
}

// TestNormalizeStepPoliciesRefusesUnrecognizedValue confirms a stored value
// outside either enum refuses the submission rather than being coerced to a
// default this coordinator invented and would then report as the operator's
// own recorded choice.
func TestNormalizeStepPoliciesRefusesUnrecognizedValue(t *testing.T) {
	for _, c := range []struct {
		name string
		step config.ShowMacroStep
		want string
	}{
		{"onFailure", config.ShowMacroStep{ID: "one", OnFailure: "halt"}, "onFailure"},
		{"onUnconfirmed", config.ShowMacroStep{ID: "one", OnUnconfirmed: "retry"}, "onUnconfirmed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := normalizeStepPolicies([]config.ShowMacroStep{c.step}, "m", 3)
			if err == nil {
				t.Fatalf("normalizeStepPolicies() error = nil, want a refusal naming %s", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %q, want it to name %s", err, c.want)
			}
		})
	}
}
