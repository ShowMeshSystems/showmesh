package config

import (
	"encoding/json"
	"fmt"
	"strings"
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

// fakeResolumeReferenceResolver is a minimal stand-in for a real
// [ResolumeReferenceResolver] (implemented in package internal/coordinator
// over internal/coordinator/collector/resolume — see that interface's own
// doc comment for why this package cannot import the real thing). Each
// resolve method is keyed by (kind, label): "known" resolves; "ambiguous"
// refuses naming candidates; anything else refuses as not found — matching
// the three outcomes ADR-037 decision 5 requires
// (resolved/not-found/ambiguous). uploaded defaults true; withNotUploaded
// flips it so every method returns [ErrResolumeCompositionNotUploaded]
// instead.
type fakeResolumeReferenceResolver struct {
	uploaded  bool
	known     map[string]bool
	ambiguous map[string]bool
}

func newFakeResolumeReferenceResolver() *fakeResolumeReferenceResolver {
	return &fakeResolumeReferenceResolver{uploaded: true, known: map[string]bool{}, ambiguous: map[string]bool{}}
}

func (f *fakeResolumeReferenceResolver) withKnown(kind, label string) *fakeResolumeReferenceResolver {
	f.known[kind+"|"+label] = true
	return f
}

func (f *fakeResolumeReferenceResolver) withAmbiguous(kind, label string) *fakeResolumeReferenceResolver {
	f.ambiguous[kind+"|"+label] = true
	return f
}

func (f *fakeResolumeReferenceResolver) withNotUploaded() *fakeResolumeReferenceResolver {
	f.uploaded = false
	return f
}

func (f *fakeResolumeReferenceResolver) resolve(kind, label string) error {
	if !f.uploaded {
		return ErrResolumeCompositionNotUploaded
	}
	key := kind + "|" + label
	if f.ambiguous[key] {
		return fmt.Errorf("more than one %s named %q was found (candidate A, candidate B); rename one of them in Resolume to disambiguate", kind, label)
	}
	if f.known[key] {
		return nil
	}
	return fmt.Errorf("no %s in the current composition is named %q", kind, label)
}

func (f *fakeResolumeReferenceResolver) ResolveClip(ref ResolumeClipReference) error {
	return f.resolve("clip", ref.Clip)
}

func (f *fakeResolumeReferenceResolver) ResolveLayer(name string) error {
	return f.resolve("layer", name)
}

func (f *fakeResolumeReferenceResolver) ResolveColumn(deck, column string) error {
	return f.resolve("column", column)
}

func (f *fakeResolumeReferenceResolver) ResolveDeck(name string) error {
	return f.resolve("deck", name)
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
	p, verr := DecodeShowActionPayload(validFPPActionJSON(), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
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
	p, verr := DecodeShowActionPayload(validFPPActionJSON(), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
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
		{"number-with-value", `{"kind": "number", "topic": "home/projectors/state", "value": "1", "deadlineSeconds": 30}`},
		{"text", `{"kind": "text", "topic": "home/projectors/state", "deadlineSeconds": 30}`},
		{"match", `{"kind": "match", "topic": "home/projectors/state", "value": "on", "deadlineSeconds": 30}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := validMQTTActionJSON(tc.expect)
			_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
			if verr != nil {
				t.Fatalf("unexpected error: %+v", verr)
			}
		})
	}
}

func TestDecodeShowActionPayloadShowRequired(t *testing.T) {
	raw := `{"label": "x", "safetyClass": "none", "target": {"integration": "fpp", "instanceId": "fpp-main", "primitive": "startPlaylist", "params": {"playlist": "x"}}}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "show" {
		t.Fatalf("expected show-required error, got %+v", verr)
	}
}

func TestDecodeShowActionPayloadShowNull(t *testing.T) {
	raw := `{"show": null, "label": "x", "safetyClass": "none", "target": {"integration": "fpp", "instanceId": "fpp-main", "primitive": "startPlaylist", "params": {"playlist": "x"}}}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != "show" {
		t.Fatalf("expected show-null error, got %+v", verr)
	}
}

func TestDecodeShowActionPayloadShowInvalidFormat(t *testing.T) {
	raw := `{"show": "Not A Valid Show!", "label": "x", "safetyClass": "none", "target": {"integration": "fpp", "instanceId": "fpp-main", "primitive": "startPlaylist", "params": {"playlist": "x"}}}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "show" {
		t.Fatalf("expected show format error, got %+v", verr)
	}
}

func TestDecodeShowActionPayloadLabelRequired(t *testing.T) {
	raw := `{"show": "halloween-2026", "safetyClass": "none", "target": {"integration": "fpp", "instanceId": "fpp-main", "primitive": "startPlaylist", "params": {"playlist": "x"}}}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "label" {
		t.Fatalf("expected label-required error, got %+v", verr)
	}
}

func TestDecodeShowActionPayloadDescriptionAbsentNullEmpty(t *testing.T) {
	base := func(desc string) string {
		return `{"show": "halloween-2026", "label": "x", ` + desc + `"safetyClass": "stop", "target": {"integration": "fpp", "instanceId": "fpp-main", "primitive": "stopPlaylist"}}`
	}
	t.Run("absent", func(t *testing.T) {
		p, verr := DecodeShowActionPayload(base(""), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr != nil {
			t.Fatalf("unexpected error: %+v", verr)
		}
		if p.Description != "" {
			t.Fatalf("expected empty description, got %q", p.Description)
		}
	})
	t.Run("null", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(base(`"description": null, `), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != "description" {
			t.Fatalf("expected description-null error, got %+v", verr)
		}
	})
	t.Run("explicit-empty", func(t *testing.T) {
		p, verr := DecodeShowActionPayload(base(`"description": "", `), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr != nil {
			t.Fatalf("unexpected error: %+v", verr)
		}
		if p.Description != "" {
			t.Fatalf("expected empty description, got %q", p.Description)
		}
	})
	t.Run("explicit-value", func(t *testing.T) {
		p, verr := DecodeShowActionPayload(base(`"description": "a real description", `), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
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
		_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "safetyClass" {
			t.Fatalf("expected safetyClass-required error, got %+v", verr)
		}
	})
	t.Run("not-a-member", func(t *testing.T) {
		raw := `{"show": "halloween-2026", "label": "x", "safetyClass": "critical", "target": {"integration": "fpp", "instanceId": "fpp-main", "primitive": "stopPlaylist"}}`
		_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "safetyClass" {
			t.Fatalf("expected safetyClass-invalid error, got %+v", verr)
		}
	})
}

func TestDecodeShowActionPayloadTargetRequired(t *testing.T) {
	raw := `{"show": "halloween-2026", "label": "x", "safetyClass": "none"}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "target" {
		t.Fatalf("expected target-required error, got %+v", verr)
	}
}

func TestDecodeShowActionPayloadIntegrationInvalid(t *testing.T) {
	raw := `{"show": "halloween-2026", "label": "x", "safetyClass": "none", "target": {"integration": "dmx"}}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "target.integration" {
		t.Fatalf("expected target.integration-invalid error, got %+v", verr)
	}
}

func TestDecodeShowActionPayloadFPPInstanceIDUnconfigured(t *testing.T) {
	raw := `{"show": "halloween-2026", "label": "x", "safetyClass": "stop", "target": {"integration": "fpp", "instanceId": "not-configured", "primitive": "stopPlaylist"}}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference || verr.Field != "target.instanceId" {
		t.Fatalf("expected instanceId-unknown-reference error, got %+v", verr)
	}
}

func TestDecodeShowActionPayloadFPPPrimitiveUnknown(t *testing.T) {
	raw := `{"show": "halloween-2026", "label": "x", "safetyClass": "none", "target": {"integration": "fpp", "instanceId": "fpp-main", "primitive": "doTheHustle"}}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
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
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
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
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), reg, newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
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
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
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
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
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
		_, verr := DecodeShowActionPayload(mk(`{"payload": "ON", "qos": 1}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr == nil || verr.Field != "target.publish.topic" {
			t.Fatalf("expected topic-required error, got %+v", verr)
		}
	})
	t.Run("payload-required-absent", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk(`{"topic": "t", "qos": 1}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr == nil || verr.Field != "target.publish.payload" || verr.Code != ValidationCodeFieldRequired {
			t.Fatalf("expected payload-required error, got %+v", verr)
		}
	})
	t.Run("payload-empty-string-allowed", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk(`{"topic": "t", "payload": "", "qos": 1}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr != nil {
			t.Fatalf("expected empty payload to be accepted, got %+v", verr)
		}
	})
	t.Run("payload-null-rejected", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk(`{"topic": "t", "payload": null, "qos": 1}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != "target.publish.payload" {
			t.Fatalf("expected payload-null error, got %+v", verr)
		}
	})
	t.Run("qos-required", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk(`{"topic": "t", "payload": "ON"}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr == nil || verr.Field != "target.publish.qos" {
			t.Fatalf("expected qos-required error, got %+v", verr)
		}
	})
	t.Run("qos-out-of-range", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk(`{"topic": "t", "payload": "ON", "qos": 3}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
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
		p, verr := DecodeShowActionPayload(mk(""), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr != nil {
			t.Fatalf("unexpected error: %+v", verr)
		}
		if p.Target.Publish.Retain != false {
			t.Fatalf("expected retain to default false, got %v", p.Target.Publish.Retain)
		}
	})
	t.Run("null-is-error", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk(`, "retain": null`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != "target.publish.retain" {
			t.Fatalf("expected retain-null error, got %+v", verr)
		}
	})
	t.Run("explicit-true", func(t *testing.T) {
		p, verr := DecodeShowActionPayload(mk(`, "retain": true`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
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
		_, verr := DecodeShowActionPayload(mk(`{"kind": "none", "topic": "t"}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr == nil {
			t.Fatal("expected error for topic supplied under kind none")
		}
	})
	t.Run("value-forbidden", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk(`{"kind": "none", "value": "x"}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr == nil {
			t.Fatal("expected error for value supplied under kind none")
		}
	})
	t.Run("deadline-forbidden", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk(`{"kind": "none", "deadlineSeconds": 10}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
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
		_, verr := DecodeShowActionPayload(mk("0"), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr == nil {
			t.Fatal("expected error for a zero deadline")
		}
	})
	t.Run("negative-rejected", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk("-5"), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr == nil {
			t.Fatal("expected error for a negative deadline")
		}
	})
	t.Run("over-cap-rejected", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk("121"), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr == nil {
			t.Fatal("expected error for a deadline over 120")
		}
	})
	t.Run("at-cap-accepted", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk("120"), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
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
		_, verr := DecodeShowActionPayload(mk(`{"kind": "match", "topic": "t2", "deadlineSeconds": 10}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr == nil || verr.Field != "target.expect.value" {
			t.Fatalf("expected value-required error for match, got %+v", verr)
		}
	})
	t.Run("boolean-forbids-value", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk(`{"kind": "boolean", "topic": "t2", "value": "x", "deadlineSeconds": 10}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr == nil || verr.Field != "target.expect.value" {
			t.Fatalf("expected value-forbidden error for boolean, got %+v", verr)
		}
	})
	t.Run("text-forbids-value", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk(`{"kind": "text", "topic": "t2", "value": "x", "deadlineSeconds": 10}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr == nil || verr.Field != "target.expect.value" {
			t.Fatalf("expected value-forbidden error for text, got %+v", verr)
		}
	})
	t.Run("number-value-must-be-numeric", func(t *testing.T) {
		_, verr := DecodeShowActionPayload(mk(`{"kind": "number", "topic": "t2", "value": "not-a-number", "deadlineSeconds": 10}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr == nil || verr.Field != "target.expect.value" {
			t.Fatalf("expected value-invalid error for a non-numeric number value, got %+v", verr)
		}
	})
	// number-value-accepts-numeric-string and
	// number-value-rejects-bare-json-number-literal are the round-trip
	// defect this pair exists to lock in (found by review after this file
	// first shipped): EncodeShowActionPayload always emits value as a
	// quoted JSON string (ShowActionMQTTExpect.Value is a Go string), so a
	// decoder that instead required a bare JSON number for kind "number"
	// rejected its own read output on re-save. value now has exactly one
	// wire representation for kind "number" — a JSON string that parses
	// as a number — matching kind "match" and matching what a GET
	// returns, in both directions.
	t.Run("number-value-accepts-numeric-string", func(t *testing.T) {
		p, verr := DecodeShowActionPayload(mk(`{"kind": "number", "topic": "t2", "value": "42", "deadlineSeconds": 10}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr != nil {
			t.Fatalf("expected a numeric JSON string to be accepted for kind \"number\", got %+v", verr)
		}
		if p.Target.Expect == nil || p.Target.Expect.Value == nil || *p.Target.Expect.Value != "42" {
			t.Fatalf("expected value %q to be stored verbatim, got %+v", "42", p.Target.Expect)
		}
	})
	t.Run("number-value-rejects-bare-json-number-literal", func(t *testing.T) {
		// Broken and confirmed to fail: before this fix, this exact body
		// (an unquoted JSON number for kind "number") was the ONLY shape
		// decodeMQTTExpect accepted, which is what made the read shape
		// (always a quoted string) unusable as a write body. Restored
		// afterward — see this builder's report.
		_, verr := DecodeShowActionPayload(mk(`{"kind": "number", "topic": "t2", "value": 42, "deadlineSeconds": 10}`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr == nil || verr.Field != "target.expect.value" {
			t.Fatalf("expected a bare JSON number literal to be rejected for kind \"number\" (value has exactly one wire representation), got %+v", verr)
		}
	})
}

// TestDecodeShowActionPayloadMQTTExpectValueRoundTrips is the property the
// reviewer named directly: a GET followed by an unchanged PUT must
// succeed. For every expect.kind that can carry a value ("number" and
// "match"), decode a request, encode the result exactly as a stored
// revision would, and decode that encoded JSON again unchanged — the
// second decode must succeed and must produce the identical Value. This
// is a plain Go-level version of the same property
// TestPutShowActionGetThenPutRoundTrips (internal/coordinator/api) proves
// against the real HTTP handler.
func TestDecodeShowActionPayloadMQTTExpectValueRoundTrips(t *testing.T) {
	mk := func(expect string) string {
		return `{"show": "halloween-2026", "label": "x", "safetyClass": "none", "target": {
			"integration": "mqtt", "broker": "home-automation",
			"publish": {"topic": "t", "payload": "ON", "qos": 1},
			"expect": ` + expect + `
		}}`
	}
	cases := []struct {
		name   string
		expect string
	}{
		{"number-with-integer-text", `{"kind": "number", "topic": "t2", "value": "42", "deadlineSeconds": 10}`},
		{"number-with-decimal-text", `{"kind": "number", "topic": "t2", "value": "4.2e1", "deadlineSeconds": 10}`},
		{"number-without-value", `{"kind": "number", "topic": "t2", "deadlineSeconds": 10}`},
		{"match-with-value", `{"kind": "match", "topic": "t2", "value": "on", "deadlineSeconds": 10}`},
		{"match-with-empty-value", `{"kind": "match", "topic": "t2", "value": "", "deadlineSeconds": 10}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first, verr := DecodeShowActionPayload(mk(tc.expect), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
			if verr != nil {
				t.Fatalf("first decode: unexpected error: %+v", verr)
			}
			raw, err := EncodeShowActionPayload(first)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			second, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
			if verr != nil {
				t.Fatalf("re-decoding the encoded (read) shape unchanged: unexpected error: %+v\nencoded: %s", verr, raw)
			}
			firstV, secondV := first.Target.Expect.Value, second.Target.Expect.Value
			switch {
			case firstV == nil && secondV == nil:
				// both absent — fine (kind "number" with no value).
			case firstV == nil || secondV == nil:
				t.Fatalf("value presence did not round-trip: first=%v second=%v", firstV, secondV)
			case *firstV != *secondV:
				t.Fatalf("value did not round-trip byte-identical: first=%q second=%q", *firstV, *secondV)
			}
		})
	}
}

func TestDecodeShowActionPayloadBodyInvalid(t *testing.T) {
	_, verr := DecodeShowActionPayload("not json", testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeBodyInvalid {
		t.Fatalf("expected body-invalid error, got %+v", verr)
	}
}

// TestDecodeShowActionPayloadUnknownTopLevelKeyRejected is the defect a
// second review found, proved concretely rather than abstractly: before
// rejectUnknownTopLevelKeys existed, this exact body — "descriptio"
// (missing the trailing "n") instead of "description" — decoded
// successfully with description silently left at its default (empty), so
// the operator's typed description was silently discarded with no error
// at all. Silent defaulting on a typo is worse than outright rejection or
// outright ignoring, because the operator has no signal anything is
// wrong. Now rejected instead.
func TestDecodeShowActionPayloadUnknownTopLevelKeyRejected(t *testing.T) {
	raw := `{"show": "halloween-2026", "label": "x", "descriptio": "oops", "safetyClass": "none", "target": {
		"integration": "fpp", "instanceId": "fpp-main", "primitive": "stopPlaylist"
	}}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownKey {
		t.Fatalf("expected field-unknown-key error for typo'd \"descriptio\", got %+v", verr)
	}
}

// --- Track D seam C: the "resolume" integration ---

func validResolumeLaunchClipJSON() string {
	return `{
		"show": "halloween-2026",
		"label": "Launch main clip",
		"safetyClass": "none",
		"target": {
			"integration": "resolume",
			"action": "launchClip",
			"ref": {"clip": "Whole House 1", "deck": "Main"}
		}
	}`
}

// TestDecodeShowActionPayloadResolumeValidRoundTripsForEveryAction is
// acceptance criterion 1, as a table over all seven actions rather than
// launchClip alone: each is authored, encoded, and re-decoded from the
// coordinator's own encoded output (the "action show --output json" into
// a file, edited, "action put --file" back round trip cmd_action.go's own
// doc comment promises), with names intact and no object id anywhere.
//
// This table is what caught a real bug: with only launchClip covered,
// blackout's own round trip (an empty ref, dropped on encode by
// omitempty) was never exercised, and a stored blackout action could not
// be re-PUT.
func TestDecodeShowActionPayloadResolumeValidRoundTripsForEveryAction(t *testing.T) {
	cases := []struct {
		action, safetyClass, refJSON string
		resolver                     *fakeResolumeReferenceResolver
	}{
		{
			action: ShowActionResolumeLaunchClip, safetyClass: ShowSafetyClassNone,
			refJSON:  `{"clip": "Whole House 1", "deck": "Main"}`,
			resolver: newFakeResolumeReferenceResolver().withKnown("clip", "Whole House 1"),
		},
		{
			action: ShowActionResolumeClearLayer, safetyClass: ShowSafetyClassBlackout,
			refJSON:  `{"layer": "Whole House 1"}`,
			resolver: newFakeResolumeReferenceResolver().withKnown("layer", "Whole House 1"),
		},
		{
			action: ShowActionResolumeBlackout, safetyClass: ShowSafetyClassBlackout,
			refJSON:  `{}`,
			resolver: newFakeResolumeReferenceResolver(),
		},
		{
			action: ShowActionResolumeLaunchColumn, safetyClass: ShowSafetyClassNone,
			refJSON:  `{"column": "Column 3", "deck": "Main"}`,
			resolver: newFakeResolumeReferenceResolver().withKnown("column", "Column 3"),
		},
		{
			action: ShowActionResolumeSelectDeck, safetyClass: ShowSafetyClassNone,
			refJSON:  `{"deck": "Main"}`,
			resolver: newFakeResolumeReferenceResolver().withKnown("deck", "Main"),
		},
		{
			action: ShowActionResolumeSetLayerBypass, safetyClass: ShowSafetyClassNone,
			refJSON:  `{"layer": "Whole House 1", "bypassed": true}`,
			resolver: newFakeResolumeReferenceResolver().withKnown("layer", "Whole House 1"),
		},
		{
			action: ShowActionResolumeSetLayerMaster, safetyClass: ShowSafetyClassNone,
			refJSON:  `{"layer": "Whole House 1", "master": 0.5}`,
			resolver: newFakeResolumeReferenceResolver().withKnown("layer", "Whole House 1"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			raw := fmt.Sprintf(`{"show": "halloween-2026", "label": "x", "safetyClass": %q,
				"target": {"integration": "resolume", "action": %q, "ref": %s}}`,
				tc.safetyClass, tc.action, tc.refJSON)

			p, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), tc.resolver, alwaysTrueShowExists)
			if verr != nil {
				t.Fatalf("decode: %+v", verr)
			}
			if p.Target.Action != tc.action {
				t.Fatalf("action = %q, want %q", p.Target.Action, tc.action)
			}

			encoded, err := EncodeShowActionPayload(p)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if strings.Contains(encoded, `"ref":{"id"`) {
				t.Fatalf("an object id leaked into ref: %s", encoded)
			}

			p2, verr := DecodeShowActionPayload(encoded, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), tc.resolver, alwaysTrueShowExists)
			if verr != nil {
				t.Fatalf("re-decode the coordinator's own encoded output: %+v", verr)
			}
			if p2.Target.Action != tc.action {
				t.Fatalf("re-decoded action = %q, want %q", p2.Target.Action, tc.action)
			}
			for k, v := range p.Target.Ref {
				if p2.Target.Ref[k] != v {
					t.Fatalf("re-decoded ref[%q] = %v, want %v", k, p2.Target.Ref[k], v)
				}
			}
		})
	}
}

// TestDecodeShowActionPayloadResolumeClipNotFoundRefused is acceptance
// criterion 2: a clip name that does not resolve is refused at write time,
// naming the clip.
//
// Broken and confirmed to fail: made resolveResolumeRef always return nil
// (skipping resolution entirely) — this test's verr == nil check failed to
// catch it, i.e. the write was wrongly accepted. Restored afterward.
func TestDecodeShowActionPayloadResolumeClipNotFoundRefused(t *testing.T) {
	resolver := newFakeResolumeReferenceResolver() // nothing known
	_, verr := DecodeShowActionPayload(validResolumeLaunchClipJSON(), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), resolver, alwaysTrueShowExists)
	if verr == nil {
		t.Fatal("expected an error for a clip the resolver does not know")
	}
	if verr.Code != ValidationCodeFieldUnknownReference {
		t.Fatalf("expected field-unknown-reference error, got %+v", verr)
	}
	if !strings.Contains(verr.Detail, "Whole House 1") {
		t.Fatalf("expected the refusal to name the clip \"Whole House 1\", got %q", verr.Detail)
	}
}

// TestDecodeShowActionPayloadResolumeClipAmbiguousNamesCandidates is
// acceptance criterion 3: an ambiguous clip name is refused at write time
// naming every candidate.
func TestDecodeShowActionPayloadResolumeClipAmbiguousNamesCandidates(t *testing.T) {
	resolver := newFakeResolumeReferenceResolver().withAmbiguous("clip", "Whole House 1")
	_, verr := DecodeShowActionPayload(validResolumeLaunchClipJSON(), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), resolver, alwaysTrueShowExists)
	if verr == nil {
		t.Fatal("expected an error for an ambiguous clip")
	}
	if verr.Code != ValidationCodeFieldUnknownReference {
		t.Fatalf("expected field-unknown-reference error, got %+v", verr)
	}
	if !strings.Contains(verr.Detail, "candidate A") || !strings.Contains(verr.Detail, "candidate B") {
		t.Fatalf("expected the refusal to name every candidate, got %q", verr.Detail)
	}
}

// TestDecodeShowActionPayloadResolumeCompositionNotUploadedRefused proves
// section 2.1's own extra rule: a write attempted when no composition has
// ever been uploaded is refused, not accepted optimistically and not
// resolved lazily.
func TestDecodeShowActionPayloadResolumeCompositionNotUploadedRefused(t *testing.T) {
	resolver := newFakeResolumeReferenceResolver().withNotUploaded()
	_, verr := DecodeShowActionPayload(validResolumeLaunchClipJSON(), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), resolver, alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference {
		t.Fatalf("expected field-unknown-reference error, got %+v", verr)
	}
	if !strings.Contains(verr.Detail, "no composition has been uploaded") {
		t.Fatalf("expected ErrResolumeCompositionNotUploaded's own sentence, got %q", verr.Detail)
	}
}

// TestDecodeShowActionPayloadResolumeBlackoutRequiresBlackoutSafetyClass and
// TestDecodeShowActionPayloadResolumeClearLayerRequiresBlackoutSafetyClass
// are acceptance criterion 4.
func TestDecodeShowActionPayloadResolumeBlackoutRequiresBlackoutSafetyClass(t *testing.T) {
	raw := `{"show": "halloween-2026", "label": "Blackout", "safetyClass": "none",
		"target": {"integration": "resolume", "action": "blackout", "ref": {}}}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeSafetyClassMismatch {
		t.Fatalf("expected safety-class-mismatch error, got %+v", verr)
	}
}

func TestDecodeShowActionPayloadResolumeClearLayerRequiresBlackoutSafetyClass(t *testing.T) {
	raw := `{"show": "halloween-2026", "label": "Clear the main layer", "safetyClass": "none",
		"target": {"integration": "resolume", "action": "clearLayer", "ref": {"layer": "Whole House 1"}}}`
	resolver := newFakeResolumeReferenceResolver().withKnown("layer", "Whole House 1")
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), resolver, alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeSafetyClassMismatch {
		t.Fatalf("expected safety-class-mismatch error, got %+v", verr)
	}
}

// TestDecodeShowActionPayloadResolumeBlackoutAcceptsBlackoutSafetyClass is
// the accepted-path sibling of the two tests above: blackout with the
// CORRECT declared class is accepted, and needs no ref at all.
func TestDecodeShowActionPayloadResolumeBlackoutAcceptsBlackoutSafetyClass(t *testing.T) {
	raw := `{"show": "halloween-2026", "label": "Blackout", "safetyClass": "blackout",
		"target": {"integration": "resolume", "action": "blackout", "ref": {}}}`
	p, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Target.Action != ShowActionResolumeBlackout || len(p.Target.Ref) != 0 {
		t.Fatalf("unexpected target: %+v", p.Target)
	}
}

func TestDecodeShowActionPayloadResolumeUnrecognizedActionRejected(t *testing.T) {
	raw := `{"show": "halloween-2026", "label": "x", "safetyClass": "none",
		"target": {"integration": "resolume", "action": "teleportClip", "ref": {}}}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "target.action" {
		t.Fatalf("expected target.action-invalid error, got %+v", verr)
	}
}

func TestDecodeShowActionPayloadResolumeUnknownRefKeyRejected(t *testing.T) {
	raw := `{"show": "halloween-2026", "label": "x", "safetyClass": "none",
		"target": {"integration": "resolume", "action": "clearLayer", "ref": {"layer": "Whole House 1", "colum": "3"}}}`
	resolver := newFakeResolumeReferenceResolver().withKnown("layer", "Whole House 1")
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), resolver, alwaysTrueShowExists)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownKey {
		t.Fatalf("expected field-unknown-key error for typo'd \"colum\", got %+v", verr)
	}
}

// TestDecodeShowActionPayloadResolumeRefRequiredExceptForBlackout is the
// review fix for a real bug: ref was unconditionally required, blackout's
// own vocabulary is empty, and an empty ref map encodes to no "ref" key at
// all (json:"ref,omitempty"), so a stored blackout action could never be
// re-decoded from its own encoded output. ref must be required for every
// action with a non-empty vocabulary, and for blackout it must be absent
// or an empty object — never required to be a key that can only ever hold
// nothing.
func TestDecodeShowActionPayloadResolumeRefRequiredExceptForBlackout(t *testing.T) {
	t.Run("ref absent on a non-empty-vocabulary action is required", func(t *testing.T) {
		raw := `{"show": "halloween-2026", "label": "x", "safetyClass": "none",
			"target": {"integration": "resolume", "action": "selectDeck"}}`
		_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "target.ref" {
			t.Fatalf("expected target.ref-required error, got %+v", verr)
		}
	})
	t.Run("ref absent on blackout is accepted", func(t *testing.T) {
		raw := `{"show": "halloween-2026", "label": "x", "safetyClass": "blackout",
			"target": {"integration": "resolume", "action": "blackout"}}`
		p, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr != nil {
			t.Fatalf("unexpected error: %+v", verr)
		}
		if len(p.Target.Ref) != 0 {
			t.Fatalf("unexpected ref: %+v", p.Target.Ref)
		}
	})
	t.Run("ref null on blackout is rejected", func(t *testing.T) {
		raw := `{"show": "halloween-2026", "label": "x", "safetyClass": "blackout",
			"target": {"integration": "resolume", "action": "blackout", "ref": null}}`
		_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != "target.ref" {
			t.Fatalf("expected target.ref-null error, got %+v", verr)
		}
	})
	t.Run("ref non-empty on blackout is rejected naming the keys", func(t *testing.T) {
		raw := `{"show": "halloween-2026", "label": "x", "safetyClass": "blackout",
			"target": {"integration": "resolume", "action": "blackout", "ref": {"layer": "Whole House 1"}}}`
		_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr == nil || verr.Code != ValidationCodeFieldUnknownKey {
			t.Fatalf("expected field-unknown-key error, got %+v", verr)
		}
		if !strings.Contains(verr.Detail, "layer") {
			t.Fatalf("expected the refusal to name the key, got %q", verr.Detail)
		}
	})

	// The bug this test exists to catch: what the coordinator itself
	// encodes for blackout must decode again without error.
	t.Run("blackout's own encoded output re-decodes", func(t *testing.T) {
		raw := `{"show": "halloween-2026", "label": "x", "safetyClass": "blackout",
			"target": {"integration": "resolume", "action": "blackout", "ref": {}}}`
		p, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
		if verr != nil {
			t.Fatalf("decode: %+v", verr)
		}
		encoded, err := EncodeShowActionPayload(p)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if _, verr := DecodeShowActionPayload(encoded, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists); verr != nil {
			t.Fatalf("re-decoding the coordinator's own encoded output was refused: %+v (encoded: %s)", verr, encoded)
		}
	})
}

// TestDecodeShowActionPayloadResolumeLaunchClipDeckConditional proves
// section 2.1's own rule 3: "deck" is required unless "persistent", and
// forbidden when "persistent" is true — explicit null is always an error
// regardless.
func TestDecodeShowActionPayloadResolumeLaunchClipDeckConditional(t *testing.T) {
	mk := func(refExtra string) string {
		return `{"show": "halloween-2026", "label": "x", "safetyClass": "none",
			"target": {"integration": "resolume", "action": "launchClip", "ref": {"clip": "Whole House 1"` + refExtra + `}}}`
	}
	t.Run("neither-deck-nor-persistent", func(t *testing.T) {
		resolver := newFakeResolumeReferenceResolver().withKnown("clip", "Whole House 1")
		_, verr := DecodeShowActionPayload(mk(""), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), resolver, alwaysTrueShowExists)
		if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "target.ref.deck" {
			t.Fatalf("expected target.ref.deck-required error, got %+v", verr)
		}
	})
	t.Run("both-deck-and-persistent", func(t *testing.T) {
		resolver := newFakeResolumeReferenceResolver().withKnown("clip", "Whole House 1")
		_, verr := DecodeShowActionPayload(mk(`, "deck": "Main", "persistent": true`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), resolver, alwaysTrueShowExists)
		if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "target.ref.deck" {
			t.Fatalf("expected target.ref.deck-invalid error, got %+v", verr)
		}
	})
	t.Run("persistent-only-accepted", func(t *testing.T) {
		resolver := newFakeResolumeReferenceResolver().withKnown("clip", "Whole House 1")
		p, verr := DecodeShowActionPayload(mk(`, "persistent": true`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), resolver, alwaysTrueShowExists)
		if verr != nil {
			t.Fatalf("unexpected error: %+v", verr)
		}
		if p.Target.Ref["persistent"] != true {
			t.Fatalf("unexpected ref: %+v", p.Target.Ref)
		}
	})
	t.Run("deck-null-rejected", func(t *testing.T) {
		resolver := newFakeResolumeReferenceResolver().withKnown("clip", "Whole House 1")
		_, verr := DecodeShowActionPayload(mk(`, "deck": null`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), resolver, alwaysTrueShowExists)
		if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != "target.ref.deck" {
			t.Fatalf("expected target.ref.deck-null error, got %+v", verr)
		}
	})
}

// TestDecodeShowActionPayloadShowMustExist is E7-3's own write-time
// existence gate: an action naming a "show" that does not resolve is
// refused naming the missing show, not accepted on shape alone. This is
// the "zero shows defined" transition — creating the first action before
// any show exists must fail this way, not with a bare 400.
func TestDecodeShowActionPayloadShowMustExist(t *testing.T) {
	_, verr := DecodeShowActionPayload(validFPPActionJSON(), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysFalse)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference || verr.Field != "show" {
		t.Fatalf("expected show-unknown-reference error, got %+v", verr)
	}
}
