package config

import "testing"

func alwaysResolvesEmergencyStopAction(string) bool { return true }
func neverResolvesEmergencyStopAction(string) bool  { return false }

func fullEmergencyStopBody(stop, stopPowerDown, hardStop string) string {
	return `{"stop":{"actions":[` + stop + `]},"stopPowerDown":{"actions":[` + stopPowerDown + `]},"hardStop":{"actions":[` + hardStop + `]}}`
}

func TestDecodeEmergencyStopPayloadAcceptsAllLevelsEmpty(t *testing.T) {
	got, verr := DecodeEmergencyStopPayload(fullEmergencyStopBody("", "", ""), alwaysResolvesEmergencyStopAction)
	if verr != nil {
		t.Fatalf("DecodeEmergencyStopPayload returned %v", verr)
	}
	if len(got.Stop.Actions) != 0 || len(got.StopPowerDown.Actions) != 0 || len(got.HardStop.Actions) != 0 {
		t.Fatalf("got non-empty actions from an all-empty body: %+v", got)
	}
}

func TestDecodeEmergencyStopPayloadAcceptsConfiguredActions(t *testing.T) {
	got, verr := DecodeEmergencyStopPayload(fullEmergencyStopBody(`"worklights-on"`, `"worklights-on","projectors-off"`, `"kill-all-power"`), alwaysResolvesEmergencyStopAction)
	if verr != nil {
		t.Fatalf("DecodeEmergencyStopPayload returned %v", verr)
	}
	if len(got.Stop.Actions) != 1 || got.Stop.Actions[0] != "worklights-on" {
		t.Fatalf("Stop.Actions = %v", got.Stop.Actions)
	}
	if len(got.StopPowerDown.Actions) != 2 {
		t.Fatalf("StopPowerDown.Actions = %v", got.StopPowerDown.Actions)
	}
	if len(got.HardStop.Actions) != 1 || got.HardStop.Actions[0] != "kill-all-power" {
		t.Fatalf("HardStop.Actions = %v", got.HardStop.Actions)
	}
}

// Every one of the three level keys is required on a full-replacement PUT,
// even when its own actions list would be empty. This is the identical
// absent-key-is-refused rule ConfigFPPEndpointsPayload.endpoints and
// ShowModePayload.mode both already state for their own kind.
func TestDecodeEmergencyStopPayloadRefusesAbsentLevelKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"missing stop", `{"stopPowerDown":{"actions":[]},"hardStop":{"actions":[]}}`, "stop"},
		{"missing stopPowerDown", `{"stop":{"actions":[]},"hardStop":{"actions":[]}}`, "stopPowerDown"},
		{"missing hardStop", `{"stop":{"actions":[]},"stopPowerDown":{"actions":[]}}`, "hardStop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, verr := DecodeEmergencyStopPayload(tc.body, alwaysResolvesEmergencyStopAction)
			if verr == nil {
				t.Fatalf("DecodeEmergencyStopPayload(%s) accepted a body missing %q", tc.body, tc.want)
			}
			if verr.Field != tc.want {
				t.Fatalf("validation error names field %q, want %q", verr.Field, tc.want)
			}
		})
	}
}

// A null actions list is refused by name: an empty array, not null, is how
// "no follow-up actions" is configured for a level.
func TestDecodeEmergencyStopPayloadRefusesNullActions(t *testing.T) {
	_, verr := DecodeEmergencyStopPayload(`{"stop":{"actions":null},"stopPowerDown":{"actions":[]},"hardStop":{"actions":[]}}`, alwaysResolvesEmergencyStopAction)
	if verr == nil {
		t.Fatal("DecodeEmergencyStopPayload accepted a null actions list")
	}
	if verr.Field != "stop.actions" {
		t.Fatalf("validation error names field %q, want stop.actions", verr.Field)
	}
}

func TestDecodeEmergencyStopPayloadRejectsUnknownAction(t *testing.T) {
	_, verr := DecodeEmergencyStopPayload(fullEmergencyStopBody(`"nonexistent-action"`, "", ""), neverResolvesEmergencyStopAction)
	if verr == nil {
		t.Fatal("DecodeEmergencyStopPayload accepted an action id the resolver does not recognize")
	}
	if verr.Code != ValidationCodeFieldUnknownReference {
		t.Fatalf("validation error code = %q, want %q", verr.Code, ValidationCodeFieldUnknownReference)
	}
}

func TestDecodeEmergencyStopPayloadRejectsDuplicateActionWithinOneLevel(t *testing.T) {
	_, verr := DecodeEmergencyStopPayload(fullEmergencyStopBody(`"worklights-on","worklights-on"`, "", ""), alwaysResolvesEmergencyStopAction)
	if verr == nil {
		t.Fatal("DecodeEmergencyStopPayload accepted a level with the same action id twice")
	}
	if verr.Code != ValidationCodeEmergencyStopActionDuplicate {
		t.Fatalf("validation error code = %q, want %q", verr.Code, ValidationCodeEmergencyStopActionDuplicate)
	}
}

// The SAME action id is explicitly allowed to appear in more than one
// level's own list, the shared-pool design this kind documents.
func TestDecodeEmergencyStopPayloadAllowsSameActionAcrossLevels(t *testing.T) {
	got, verr := DecodeEmergencyStopPayload(fullEmergencyStopBody(`"worklights-on"`, `"worklights-on"`, `"worklights-on"`), alwaysResolvesEmergencyStopAction)
	if verr != nil {
		t.Fatalf("DecodeEmergencyStopPayload returned %v", verr)
	}
	if got.Stop.Actions[0] != "worklights-on" || got.StopPowerDown.Actions[0] != "worklights-on" || got.HardStop.Actions[0] != "worklights-on" {
		t.Fatalf("expected the same action id reusable across all three levels, got %+v", got)
	}
}

func TestDecodeEmergencyStopPayloadRejectsEmptyActionID(t *testing.T) {
	_, verr := DecodeEmergencyStopPayload(fullEmergencyStopBody(`""`, "", ""), alwaysResolvesEmergencyStopAction)
	if verr == nil {
		t.Fatal("DecodeEmergencyStopPayload accepted an empty-string action id")
	}
	if verr.Code != ValidationCodeFieldEmpty {
		t.Fatalf("validation error code = %q, want %q", verr.Code, ValidationCodeFieldEmpty)
	}
}

func TestDecodeEmergencyStopPayloadRejectsUnknownTopLevelKeys(t *testing.T) {
	_, verr := DecodeEmergencyStopPayload(`{"stop":{"actions":[]},"stopPowerDown":{"actions":[]},"hardStop":{"actions":[]},"level4":{}}`, alwaysResolvesEmergencyStopAction)
	if verr == nil {
		t.Fatal("DecodeEmergencyStopPayload accepted an unknown top-level key")
	}
}

func TestDecodeEmergencyStopPayloadRejectsUnknownLevelKeys(t *testing.T) {
	_, verr := DecodeEmergencyStopPayload(`{"stop":{"actions":[],"extra":true},"stopPowerDown":{"actions":[]},"hardStop":{"actions":[]}}`, alwaysResolvesEmergencyStopAction)
	if verr == nil {
		t.Fatal("DecodeEmergencyStopPayload accepted an unknown key inside a level object")
	}
	if verr.Field != "stop" {
		t.Fatalf("validation error names field %q, want stop", verr.Field)
	}
}

func TestDecodeEmergencyStopPayloadRejectsTooManyActions(t *testing.T) {
	actions := ""
	for i := 0; i < emergencyStopActionsMaxCount+1; i++ {
		if i > 0 {
			actions += ","
		}
		actions += `"action-` + string(rune('a'+i%26)) + `-` + string(rune('0'+i%10)) + `"`
	}
	_, verr := DecodeEmergencyStopPayload(fullEmergencyStopBody(actions, "", ""), alwaysResolvesEmergencyStopAction)
	if verr == nil {
		t.Fatal("DecodeEmergencyStopPayload accepted a level with more than the maximum actions")
	}
}

func TestDecodeEmergencyStopPayloadRejectsNonObjectBody(t *testing.T) {
	for _, raw := range []string{``, `[]`, `"stop"`, `null`, `{`} {
		if _, verr := DecodeEmergencyStopPayload(raw, alwaysResolvesEmergencyStopAction); verr == nil {
			t.Fatalf("DecodeEmergencyStopPayload(%q) accepted a non-object body", raw)
		}
	}
}

func TestEncodeDecodeEmergencyStopPayloadRoundTrips(t *testing.T) {
	want := EmergencyStopPayload{
		Stop:          EmergencyStopLevelConfig{Actions: []string{"worklights-on"}},
		StopPowerDown: EmergencyStopLevelConfig{Actions: []string{"worklights-on", "projectors-off"}},
		HardStop:      EmergencyStopLevelConfig{Actions: []string{}},
	}
	raw, err := EncodeEmergencyStopPayload(want)
	if err != nil {
		t.Fatalf("EncodeEmergencyStopPayload returned %v", err)
	}
	got, verr := DecodeEmergencyStopPayload(raw, alwaysResolvesEmergencyStopAction)
	if verr != nil {
		t.Fatalf("DecodeEmergencyStopPayload(encoded) returned %v", verr)
	}
	if len(got.Stop.Actions) != 1 || len(got.StopPowerDown.Actions) != 2 || len(got.HardStop.Actions) != 0 {
		t.Fatalf("round trip mismatch: got %+v, want %+v", got, want)
	}
}
