package config

import (
	"encoding/json"
	"testing"
)

// fakeFPPPrimitiveRegistry is a minimal stand-in for
// internal/coordinator/api's FPPPrimitiveRegistryAdapter, built here rather
// than imported: internal/coordinator/config must not import
// internal/coordinator/api (see showaction.go's own top doc comment and
// TestPackageNeverImportsAPI in importgraph_test.go), so this package's
// own tests need a fake that behaves like the real Step 8 registry for the
// two primitives these tests exercise (stopPlaylist, exempt/"stop"; and
// startPlaylist, not-exempt/"none").
type fakeFPPPrimitiveRegistry struct {
	classes map[string]string
	decode  func(wireAction string, raw map[string]json.RawMessage) (map[string]any, error)
}

func newFakeFPPPrimitiveRegistry() fakeFPPPrimitiveRegistry {
	return fakeFPPPrimitiveRegistry{
		classes: map[string]string{
			"stopPlaylist":  ShowSafetyClassStop,
			"startPlaylist": ShowSafetyClassNone,
		},
		decode: func(wireAction string, raw map[string]json.RawMessage) (map[string]any, error) {
			return map[string]any{}, nil
		},
	}
}

func (f fakeFPPPrimitiveRegistry) DecodeActionParams(wireAction string, raw map[string]json.RawMessage) (map[string]any, error) {
	return f.decode(wireAction, raw)
}

func (f fakeFPPPrimitiveRegistry) Decision11Class(wireAction string) (string, bool) {
	c, ok := f.classes[wireAction]
	return c, ok
}

func (f fakeFPPPrimitiveRegistry) WireActions() []string {
	return []string{"startPlaylist", "stopPlaylist"}
}

func testEndpoints() []FPPEndpoint {
	return []FPPEndpoint{{ID: "fpp-main", URL: "http://10.0.1.20"}}
}

func testBrokers() []IntegrationBroker {
	return []IntegrationBroker{{ID: "home-automation", URL: "tcp://10.0.0.5:1883"}}
}

func validFPPActionJSON() string {
	return `{
		"show": "halloween-2026",
		"label": "Stop the show",
		"safetyClass": "stop",
		"target": {
			"integration": "fpp",
			"instanceId": "fpp-main",
			"primitive": "stopPlaylist"
		}
	}`
}

func validMQTTActionJSON(extraExpect string) string {
	return `{
		"show": "halloween-2026",
		"label": "Projectors on",
		"safetyClass": "none",
		"target": {
			"integration": "mqtt",
			"broker": "home-automation",
			"publish": {"topic": "home/projectors/set", "payload": "ON", "qos": 1, "retain": false},
			"expect": ` + extraExpect + `
		}
	}`
}

func TestDecodeShowActionPayloadFPPValid(t *testing.T) {
	p, verr := DecodeShowActionPayload(validFPPActionJSON(), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Show != "halloween-2026" || p.Label != "Stop the show" || p.SafetyClass != ShowSafetyClassStop {
		t.Fatalf("unexpected payload: %+v", p)
	}
	if p.Target.Integration != ShowActionIntegrationFPP || p.Target.InstanceID != "fpp-main" || p.Target.Primitive != "stopPlaylist" {
		t.Fatalf("unexpected target: %+v", p.Target)
	}
}

func TestEncodeShowActionPayloadRoundTrips(t *testing.T) {
	p, verr := DecodeShowActionPayload(validFPPActionJSON(), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	raw, err := EncodeShowActionPayload(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if back["show"] != "halloween-2026" {
		t.Fatalf("show did not round trip: %v", back["show"])
	}
}

func TestDecodeShowActionPayloadMQTTValidKinds(t *testing.T) {
	cases := []struct {
		name   string
		expect string
	}{
		{"none", `{"kind": "none"}`},
		{"boolean", `{"kind": "boolean", "topic": "home/projectors/state", "deadlineSeconds": 30}`},
		{"number", `{"kind": "number", "topic": "home/projectors/state", "deadlineSeconds": 30}`},
		{"number-with-value", `{"kind": "number", "topic": "home/projectors/state", "value": 1, "deadlineSeconds": 30}`},
		{"text", `{"kind": "text", "topic": "home/projectors/state", "deadlineSeconds": 30}`},
		{"match", `{"kind": "match", "topic": "home/projectors/state", "value": "on", "deadlineSeconds": 30}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := validMQTTActionJSON(tc.expect)
			_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
			if verr != nil {
				t.Fatalf("unexpected error: %+v", verr)
			}
		})
	}
}

func TestDecodeShowActionPayloadShowRequired(t *testing.T) {
	raw := `{"label": "x", "safetyClass": "none", "target": {"integration": "fpp", "instanceId": "fpp-main", "primitive": "startPlaylist", "params": {"playlist": "x"}}}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "show" {
		t.Fatalf("expected show-required error, got %+v", verr)
	}
}

func TestDecodeShowActionPayloadShowNull(t *testing.T) {
	raw := `{"show": null, "label": "x", "safetyClass": "none", "target": {"integration": "fpp", "instanceId": "fpp-main", "primitive": "startPlaylist", "params": {"playlist": "x"}}}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
	if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != "show" {
		t.Fatalf("expected show-null error, got %+v", verr)
	}
}

func TestDecodeShowActionPayloadShowInvalidFormat(t *testing.T) {
	raw := `{"show": "Not A Valid Show!", "label": "x", "safetyClass": "none", "target": {"integration": "fpp", "instanceId": "fpp-main", "primitive": "startPlaylist", "params": {"playlist": "x"}}}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "show" {
		t.Fatalf("expected show format error, got %+v", verr)
	}
}

func TestDecodeShowActionPayloadLabelRequired(t *testing.T) {
	raw := `{"show": "halloween-2026", "safetyClass": "none", "target": {"integration": "fpp", "instanceId": "fpp-main", "primitive": "startPlaylist", "params": {"playlist": "x"}}}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "label" {
		t.Fatalf("expected label-required error, got %+v", verr)
	}
}

func TestDecodeShowActionPayloadDescriptionAbsentNullEmpty(t *testing.T) {
	base := func(desc string) string {
		return `{"show": "halloween-2026", "label": "x", ` + desc + `"safetyClass": "stop", "target": {"integration": "fpp", "instanceId": "fpp-main", "primitive": "stopPlaylist"}}`
	}
	t.Run("absent", func(t *testing.T) {
		p, verr := DecodeShowActionPayload(base(""), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr != nil {
			t.Fatalf("unexpected error: %+v", verr)
		}
		if p.Description != "" {
			t.Fatalf("expected empty description, got %q", p.Description)
		}
	})
	t.Run("null", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(base(`"description": null, `), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != "description" {
			t.Fatalf("expected description-null error, got %+v", verr)
		}
	})
	t.Run("explicit-empty", func(t *testing.T) {
		p, verr := DecodeShowActionPayload(base(`"description": "", `), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr != nil {
			t.Fatalf("unexpected error: %+v", verr)
		}
		if p.Description != "" {
			t.Fatalf("expected empty description, got %q", p.Description)
		}
	})
	t.Run("explicit-value", func(t *testing.T) {
		p, verr := DecodeShowActionPayload(base(`"description": "a real description", `), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr != nil {
			t.Fatalf("unexpected error: %+v", verr)
		}
		if p.Description != "a real description" {
			t.Fatalf("unexpected description: %q", p.Description)
		}
	})
}

func TestDecodeShowActionPayloadSafetyClassRequiredAndClosed(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		raw := `{"show": "halloween-2026", "label": "x", "target": {"integration": "fpp", "instanceId": "fpp-main", "primitive": "stopPlaylist"}}`
		_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "safetyClass" {
			t.Fatalf("expected safetyClass-required error, got %+v", verr)
		}
	})
	t.Run("not-a-member", func(t *testing.T) {
		raw := `{"show": "halloween-2026", "label": "x", "safetyClass": "critical", "target": {"integration": "fpp", "instanceId": "fpp-main", "primitive": "stopPlaylist"}}`
		_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "safetyClass" {
			t.Fatalf("expected safetyClass-invalid error, got %+v", verr)
		}
	})
}

func TestDecodeShowActionPayloadTargetRequired(t *testing.T) {
	raw := `{"show": "halloween-2026", "label": "x", "safetyClass": "none"}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "target" {
		t.Fatalf("expected target-required error, got %+v", verr)
	}
}

func TestDecodeShowActionPayloadIntegrationInvalid(t *testing.T) {
	raw := `{"show": "halloween-2026", "label": "x", "safetyClass": "none", "target": {"integration": "dmx"}}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "target.integration" {
		t.Fatalf("expected target.integration-invalid error, got %+v", verr)
	}
}

func TestDecodeShowActionPayloadFPPInstanceIDUnconfigured(t *testing.T) {
	raw := `{"show": "halloween-2026", "label": "x", "safetyClass": "stop", "target": {"integration": "fpp", "instanceId": "not-configured", "primitive": "stopPlaylist"}}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference || verr.Field != "target.instanceId" {
		t.Fatalf("expected instanceId-unknown-reference error, got %+v", verr)
	}
}

func TestDecodeShowActionPayloadFPPPrimitiveUnknown(t *testing.T) {
	raw := `{"show": "halloween-2026", "label": "x", "safetyClass": "none", "target": {"integration": "fpp", "instanceId": "fpp-main", "primitive": "doTheHustle"}}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference || verr.Field != "target.primitive" {
		t.Fatalf("expected primitive-unknown-reference error, got %+v", verr)
	}
}

// TestDecodeShowActionPayloadFPPSafetyClassMustAgreeWithRegistry is one of
// the wave2-builder-a.md brief's six required break-and-confirm tests: the
// declared safetyClass must agree with the primitive's own registered
// class (STEP-9-SPEC.md section 5.3), on its own distinct Code.
func TestDecodeShowActionPayloadFPPSafetyClassMustAgreeWithRegistry(t *testing.T) {
	raw := `{"show": "halloween-2026", "label": "x", "safetyClass": "blackout", "target": {"integration": "fpp", "instanceId": "fpp-main", "primitive": "stopPlaylist"}}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
	if verr == nil {
		t.Fatal("expected an error for safetyClass disagreeing with the registry, got none")
	}
	if verr.Code != ValidationCodeSafetyClassMismatch {
		t.Fatalf("expected code %q, got %q (%+v)", ValidationCodeSafetyClassMismatch, verr.Code, verr)
	}
}

func TestDecodeShowActionPayloadFPPParamsInvalidPropagates(t *testing.T) {
	reg := newFakeFPPPrimitiveRegistry()
	reg.decode = func(wireAction string, raw map[string]json.RawMessage) (map[string]any, error) {
		return nil, errParamsBoom
	}
	raw := `{"show": "halloween-2026", "label": "x", "safetyClass": "stop", "target": {"integration": "fpp", "instanceId": "fpp-main", "primitive": "stopPlaylist"}}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), reg)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "target.params" {
		t.Fatalf("expected target.params field-invalid error, got %+v", verr)
	}
}

var errParamsBoom = &ValidationError{Code: ValidationCodeFieldInvalid, Field: "target.params", Detail: "boom"}

// TestDecodeShowActionPayloadMQTTBrokerRequired is one of the required
// break-and-confirm tests: broker has no default.
func TestDecodeShowActionPayloadMQTTBrokerRequired(t *testing.T) {
	raw := `{"show": "halloween-2026", "label": "x", "safetyClass": "none", "target": {
		"integration": "mqtt",
		"publish": {"topic": "home/projectors/set", "payload": "ON", "qos": 1},
		"expect": {"kind": "none"}
	}}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
	if verr == nil {
		t.Fatal("expected an error for an absent broker, got none")
	}
	if verr.Code != ValidationCodeFieldRequired || verr.Field != "target.broker" {
		t.Fatalf("expected field-required on target.broker, got %+v", verr)
	}
}

func TestDecodeShowActionPayloadMQTTBrokerUndeclared(t *testing.T) {
	raw := `{"show": "halloween-2026", "label": "x", "safetyClass": "none", "target": {
		"integration": "mqtt",
		"broker": "not-declared",
		"publish": {"topic": "home/projectors/set", "payload": "ON", "qos": 1},
		"expect": {"kind": "none"}
	}}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference || verr.Field != "target.broker" {
		t.Fatalf("expected target.broker unknown-reference error, got %+v", verr)
	}
}

func TestDecodeShowActionPayloadMQTTPublishRequiredFields(t *testing.T) {
	mk := func(publish string) string {
		return `{"show": "halloween-2026", "label": "x", "safetyClass": "none", "target": {
			"integration": "mqtt", "broker": "home-automation",
			"publish": ` + publish + `,
			"expect": {"kind": "none"}
		}}`
	}
	t.Run("topic-required", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk(`{"payload": "ON", "qos": 1}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr == nil || verr.Field != "target.publish.topic" {
			t.Fatalf("expected topic-required error, got %+v", verr)
		}
	})
	t.Run("payload-required-absent", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk(`{"topic": "t", "qos": 1}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr == nil || verr.Field != "target.publish.payload" || verr.Code != ValidationCodeFieldRequired {
			t.Fatalf("expected payload-required error, got %+v", verr)
		}
	})
	t.Run("payload-empty-string-allowed", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk(`{"topic": "t", "payload": "", "qos": 1}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr != nil {
			t.Fatalf("expected empty payload to be accepted, got %+v", verr)
		}
	})
	t.Run("payload-null-rejected", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk(`{"topic": "t", "payload": null, "qos": 1}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != "target.publish.payload" {
			t.Fatalf("expected payload-null error, got %+v", verr)
		}
	})
	t.Run("qos-required", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk(`{"topic": "t", "payload": "ON"}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr == nil || verr.Field != "target.publish.qos" {
			t.Fatalf("expected qos-required error, got %+v", verr)
		}
	})
	t.Run("qos-out-of-range", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk(`{"topic": "t", "payload": "ON", "qos": 3}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr == nil || verr.Field != "target.publish.qos" || verr.Code != ValidationCodeFieldInvalid {
			t.Fatalf("expected qos out-of-range error, got %+v", verr)
		}
	})
}

func TestDecodeShowActionPayloadMQTTRetainAbsentNullExplicit(t *testing.T) {
	mk := func(retain string) string {
		return `{"show": "halloween-2026", "label": "x", "safetyClass": "none", "target": {
			"integration": "mqtt", "broker": "home-automation",
			"publish": {"topic": "t", "payload": "ON", "qos": 1` + retain + `},
			"expect": {"kind": "none"}
		}}`
	}
	t.Run("absent-defaults-false", func(t *testing.T) {
		p, verr := DecodeShowActionPayload(mk(""), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr != nil {
			t.Fatalf("unexpected error: %+v", verr)
		}
		if p.Target.Publish.Retain != false {
			t.Fatalf("expected retain to default false, got %v", p.Target.Publish.Retain)
		}
	})
	t.Run("null-is-error", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk(`, "retain": null`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != "target.publish.retain" {
			t.Fatalf("expected retain-null error, got %+v", verr)
		}
	})
	t.Run("explicit-true", func(t *testing.T) {
		p, verr := DecodeShowActionPayload(mk(`, "retain": true`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr != nil {
			t.Fatalf("unexpected error: %+v", verr)
		}
		if p.Target.Publish.Retain != true {
			t.Fatalf("expected retain true, got %v", p.Target.Publish.Retain)
		}
	})
}

func TestDecodeShowActionPayloadMQTTExpectNoneForbidsFields(t *testing.T) {
	mk := func(expect string) string {
		return `{"show": "halloween-2026", "label": "x", "safetyClass": "none", "target": {
			"integration": "mqtt", "broker": "home-automation",
			"publish": {"topic": "t", "payload": "ON", "qos": 1},
			"expect": ` + expect + `
		}}`
	}
	t.Run("topic-forbidden", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk(`{"kind": "none", "topic": "t"}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr == nil {
			t.Fatal("expected error for topic supplied under kind none")
		}
	})
	t.Run("value-forbidden", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk(`{"kind": "none", "value": "x"}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr == nil {
			t.Fatal("expected error for value supplied under kind none")
		}
	})
	t.Run("deadline-forbidden", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk(`{"kind": "none", "deadlineSeconds": 10}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr == nil {
			t.Fatal("expected error for deadlineSeconds supplied under kind none")
		}
	})
}

func TestDecodeShowActionPayloadMQTTExpectDeadlineBounds(t *testing.T) {
	mk := func(deadline string) string {
		return `{"show": "halloween-2026", "label": "x", "safetyClass": "none", "target": {
			"integration": "mqtt", "broker": "home-automation",
			"publish": {"topic": "t", "payload": "ON", "qos": 1},
			"expect": {"kind": "boolean", "topic": "t2", "deadlineSeconds": ` + deadline + `}
		}}`
	}
	t.Run("zero-rejected", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk("0"), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr == nil {
			t.Fatal("expected error for a zero deadline")
		}
	})
	t.Run("negative-rejected", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk("-5"), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr == nil {
			t.Fatal("expected error for a negative deadline")
		}
	})
	t.Run("over-cap-rejected", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk("121"), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr == nil {
			t.Fatal("expected error for a deadline over 120")
		}
	})
	t.Run("at-cap-accepted", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk("120"), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr != nil {
			t.Fatalf("expected 120 to be accepted, got %+v", verr)
		}
	})
}

func TestDecodeShowActionPayloadMQTTExpectValueRulesPerKind(t *testing.T) {
	mk := func(expect string) string {
		return `{"show": "halloween-2026", "label": "x", "safetyClass": "none", "target": {
			"integration": "mqtt", "broker": "home-automation",
			"publish": {"topic": "t", "payload": "ON", "qos": 1},
			"expect": ` + expect + `
		}}`
	}
	t.Run("match-requires-value", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk(`{"kind": "match", "topic": "t2", "deadlineSeconds": 10}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr == nil || verr.Field != "target.expect.value" {
			t.Fatalf("expected value-required error for match, got %+v", verr)
		}
	})
	t.Run("boolean-forbids-value", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk(`{"kind": "boolean", "topic": "t2", "value": "x", "deadlineSeconds": 10}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr == nil || verr.Field != "target.expect.value" {
			t.Fatalf("expected value-forbidden error for boolean, got %+v", verr)
		}
	})
	t.Run("text-forbids-value", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk(`{"kind": "text", "topic": "t2", "value": "x", "deadlineSeconds": 10}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr == nil || verr.Field != "target.expect.value" {
			t.Fatalf("expected value-forbidden error for text, got %+v", verr)
		}
	})
	t.Run("number-value-must-be-numeric", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk(`{"kind": "number", "topic": "t2", "value": "not-a-number", "deadlineSeconds": 10}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
		if verr == nil || verr.Field != "target.expect.value" {
			t.Fatalf("expected value-invalid error for a non-numeric number value, got %+v", verr)
		}
	})
}

func TestDecodeShowActionPayloadBodyInvalid(t *testing.T) {
	_, verr := DecodeShowActionPayload("not json", testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry())
	if verr == nil || verr.Code != ValidationCodeBodyInvalid {
		t.Fatalf("expected body-invalid error, got %+v", verr)
	}
}
