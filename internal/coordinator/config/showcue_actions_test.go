package config

import (
	"reflect"
	"testing"
)

func TestDecodeShowCueActionsOnlyIsAccepted(t *testing.T) {
	p, verr := DecodeShowCuePayload(`{"show":"halloween-2026","name":"Song one","outputs":{"actions":["col-1","col-2"]}}`, alwaysTrueShowExists, alwaysTrueAudioNodeExists, nil)
	if verr != nil {
		t.Fatalf("unexpected refusal: %+v", verr)
	}
	if !reflect.DeepEqual(p.Outputs.Actions, []string{"col-1", "col-2"}) {
		t.Fatalf("actions = %v, want the declared order", p.Outputs.Actions)
	}
	claims, err := DeriveShowCueClaims(p, ShowCueClaimContext{})
	if err != nil || len(claims) != 0 {
		t.Fatalf("claims = %v, err = %v; want none for an actions-only cue", claims, err)
	}
}

func TestDecodeShowCueEmptyActionsIsAbsent(t *testing.T) {
	p, verr := DecodeShowCuePayload(`{"show":"halloween-2026","name":"x","outputs":{"render":{"sequence":"s"},"actions":[]}}`, alwaysTrueShowExists, alwaysTrueAudioNodeExists, nil)
	if verr != nil {
		t.Fatalf("unexpected refusal: %+v", verr)
	}
	if p.Outputs.Actions != nil {
		t.Fatalf("actions = %v, want nil", p.Outputs.Actions)
	}
	encoded, err := EncodeShowCuePayload(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if want := `{"show":"halloween-2026","name":"x","outputs":{"render":{"sequence":"s"}}}`; encoded != want {
		t.Fatalf("encoded = %s, want %s", encoded, want)
	}
	if _, verr := DecodeShowCuePayload(`{"show":"halloween-2026","name":"x","outputs":{"actions":[]}}`, alwaysTrueShowExists, alwaysTrueAudioNodeExists, nil); verr == nil || verr.Field != "outputs" {
		t.Fatalf("an empty actions list as the only output was accepted or refused on the wrong field: %+v", verr)
	}
}

func TestDecodeShowCueActionsRefusals(t *testing.T) {
	cases := map[string]struct{ actions, field, code string }{
		"duplicate": {`["a","a"]`, "outputs.actions[1]", ValidationCodeFieldInvalid},
		"empty id":  {`["a",""]`, "outputs.actions[1]", ValidationCodeFieldEmpty},
		"null":      {`null`, "outputs.actions", ValidationCodeFieldNull},
		"not array": {`"a"`, "outputs.actions", ValidationCodeFieldInvalid},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, verr := DecodeShowCuePayload(`{"show":"halloween-2026","name":"x","outputs":{"actions":`+c.actions+`}}`, alwaysTrueShowExists, alwaysTrueAudioNodeExists, nil)
			if verr == nil || verr.Field != c.field || verr.Code != c.code {
				t.Fatalf("verr = %+v, want %s on %s", verr, c.code, c.field)
			}
		})
	}
}

func TestValidateShowCueActions(t *testing.T) {
	actions := map[string]string{"col-1": "halloween-2026", "sleigh": "christmas-2026"}
	lookup := func(id string) (string, bool) { show, ok := actions[id]; return show, ok }
	p := ShowCuePayload{Show: "halloween-2026", Outputs: ShowCueOutputs{Actions: []string{"col-1"}}}
	if verr := ValidateShowCueActions(p, lookup); verr != nil {
		t.Fatalf("same-show action refused: %+v", verr)
	}
	p.Outputs.Actions = []string{"col-1", "ghost"}
	if verr := ValidateShowCueActions(p, lookup); verr == nil || verr.Code != ValidationCodeFieldUnknownReference || verr.Field != "outputs.actions[1]" {
		t.Fatalf("unknown action: verr = %+v", verr)
	}
	p.Outputs.Actions = []string{"sleigh"}
	if verr := ValidateShowCueActions(p, lookup); verr == nil || verr.Code != ValidationCodeCrossShowReference {
		t.Fatalf("other-show action: verr = %+v", verr)
	}
}
