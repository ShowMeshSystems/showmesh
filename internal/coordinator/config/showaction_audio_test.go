package config

import (
	"strings"
	"testing"
)

// Track F seam F5: show.action.target.integration == "audio" — ADR-029's
// fourth integration binding, reaching pkg/audio's session command
// surface through an ordinary logical action, never a night-session-
// private adapter.

func validAudioActionJSON() string {
	return `{
		"show": "halloween-2026",
		"label": "Hush resting background audio",
		"safetyClass": "stop",
		"target": {
			"integration": "audio",
			"audioNodeId": "node-a",
			"audioSessionId": "resting-bg",
			"audioAction": "audio.session.stop"
		}
	}`
}

func TestDecodeShowActionPayloadAudioValid(t *testing.T) {
	p, verr := DecodeShowActionPayload(validAudioActionJSON(), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists, nil)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Target.Integration != ShowActionIntegrationAudio {
		t.Fatalf("integration = %q, want %q", p.Target.Integration, ShowActionIntegrationAudio)
	}
	if len(p.Target.AudioNodeIDs) != 1 || p.Target.AudioNodeIDs[0] != "node-a" || p.Target.AudioSessionID != "resting-bg" || p.Target.AudioAction != "audio.session.stop" {
		t.Fatalf("target = %+v", p.Target)
	}
}

// TestDecodeShowActionPayloadAudioAcceptsNodeIDArray proves the
// widening: target.audioNodeId as a JSON array of strings decodes to the
// same AudioNodeIDs list a caller (the night controller) fans out to.
func TestDecodeShowActionPayloadAudioAcceptsNodeIDArray(t *testing.T) {
	raw := `{
		"show": "halloween-2026",
		"label": "Announce on two nodes",
		"safetyClass": "none",
		"target": {
			"integration": "audio",
			"audioNodeId": ["node-a", "node-b"],
			"audioSessionId": "announce-1",
			"audioAction": "audio.session.apply"
		}
	}`
	p, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists, nil)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if len(p.Target.AudioNodeIDs) != 2 || p.Target.AudioNodeIDs[0] != "node-a" || p.Target.AudioNodeIDs[1] != "node-b" {
		t.Fatalf("AudioNodeIDs = %+v", p.Target.AudioNodeIDs)
	}
}

// TestDecodeShowActionPayloadAudioEmptyNodeIDArrayMeansUnset is ADR-049
// decision 10's own widening of the pre-existing "at least one node" rule:
// an explicitly empty audioNodeId array is no longer refused; it means the
// same as an absent one, resolving against the show's own audioNodes list
// instead ([ResolveAudioNodes]). No previously stored payload could carry
// an empty array here, since it was refused outright before this decision.
func TestDecodeShowActionPayloadAudioEmptyNodeIDArrayMeansUnset(t *testing.T) {
	raw := `{
		"show": "halloween-2026",
		"label": "Empty node list",
		"safetyClass": "none",
		"target": {
			"integration": "audio",
			"audioNodeId": [],
			"audioSessionId": "announce-1",
			"audioAction": "audio.session.apply"
		}
	}`
	p, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists, nil)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if len(p.Target.AudioNodeIDs) != 0 {
		t.Fatalf("AudioNodeIDs = %+v, want empty", p.Target.AudioNodeIDs)
	}
}

// TestDecodeShowActionPayloadAudioAbsentNodeIDMeansUnset is the absent-key
// sibling of the empty-array case above.
func TestDecodeShowActionPayloadAudioAbsentNodeIDMeansUnset(t *testing.T) {
	raw := `{
		"show": "halloween-2026",
		"label": "No node list",
		"safetyClass": "none",
		"target": {
			"integration": "audio",
			"audioSessionId": "announce-1",
			"audioAction": "audio.session.apply"
		}
	}`
	p, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists, nil)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if len(p.Target.AudioNodeIDs) != 0 {
		t.Fatalf("AudioNodeIDs = %+v, want empty", p.Target.AudioNodeIDs)
	}
}

// TestDecodeShowActionPayloadAudioRejectsUnsupportedAction defends the
// closed showActionAudioActions vocabulary — an audio show.action can
// never name an operation this coordinator's agent-facing dispatch does
// not already ship. Mutation-checked: removing the membership check
// (accepting any string) makes this pass when it should fail.
func TestDecodeShowActionPayloadAudioRejectsUnsupportedAction(t *testing.T) {
	raw := `{
		"show": "halloween-2026",
		"label": "Bogus audio op",
		"safetyClass": "none",
		"target": {
			"integration": "audio",
			"audioNodeId": "node-a",
			"audioSessionId": "resting-bg",
			"audioAction": "audio.session.teleport"
		}
	}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists, nil)
	if verr == nil {
		t.Fatal("expected a validation error for an unsupported audioAction, got none")
	}
	if verr.Field != "target.audioAction" {
		t.Fatalf("verr.Field = %q, want target.audioAction", verr.Field)
	}
}

// TestDecodeShowActionPayloadAudioSafetyClassMismatch defends the
// stop/clear/mute safety-class rule (audioActionDeclaredSafetyClass):
// "audio.session.stop" is declared "stop", so a payload declaring
// safetyClass "none" for it is rejected rather than silently accepted.
func TestDecodeShowActionPayloadAudioSafetyClassMismatch(t *testing.T) {
	raw := `{
		"show": "halloween-2026",
		"label": "Hush resting background audio",
		"safetyClass": "none",
		"target": {
			"integration": "audio",
			"audioNodeId": "node-a",
			"audioSessionId": "resting-bg",
			"audioAction": "audio.session.stop"
		}
	}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists, nil)
	if verr == nil || verr.Code != ValidationCodeSafetyClassMismatch {
		t.Fatalf("verr = %+v, want ValidationCodeSafetyClassMismatch", verr)
	}
}

// TestDecodeShowActionPayloadAudioParamsRoundTrip proves target.params
// survives decode and re-encode unchanged, exactly as an fpp primitive's
// own params do.
func TestDecodeShowActionPayloadAudioParamsRoundTrip(t *testing.T) {
	raw := `{
		"show": "halloween-2026",
		"label": "Apply resting background playlist",
		"safetyClass": "none",
		"target": {
			"integration": "audio",
			"audioNodeId": "node-a",
			"audioSessionId": "resting-bg",
			"audioAction": "audio.session.apply",
			"params": {"sourceRole": "background"}
		}
	}`
	p, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists, nil)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Target.Params["sourceRole"] != "background" {
		t.Fatalf("params = %+v, want sourceRole=background", p.Target.Params)
	}
	encoded, err := EncodeShowActionPayload(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(encoded) == 0 {
		t.Fatal("encode produced empty output")
	}
}

// An authored audio.gain.* target is the one audio show.action whose
// params are not fully opaque: the gain moved from a linear amplitude
// multiplier to decibels, and the two units share a number range, so a
// target still naming the old parameter must be refused where it is
// authored rather than dispatched as a wrong level mid-show.

func audioGainActionJSON(action, params string) string {
	return `{
		"show": "halloween-2026",
		"label": "Set the bed level",
		"safetyClass": "none",
		"target": {
			"integration": "audio",
			"audioNodeId": "node-a",
			"audioSessionId": "resting-bg",
			"audioAction": "` + action + `",
			"params": ` + params + `
		}
	}`
}

func decodeAudioGainAction(t *testing.T, action, params string) (ShowActionPayload, *ValidationError) {
	t.Helper()
	return DecodeShowActionPayload(audioGainActionJSON(action, params), testEndpoints(), testBrokers(),
		newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists, nil)
}

func TestDecodeShowActionPayloadAudioGainAcceptsDecibels(t *testing.T) {
	p, verr := decodeAudioGainAction(t, "audio.gain.set", `{"gainDb": -6.02}`)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Target.Params["gainDb"] != -6.02 {
		t.Fatalf("params = %+v, want gainDb preserved as authored", p.Target.Params)
	}

	p, verr = decodeAudioGainAction(t, "audio.gain.fade", `{"targetGainDb": -60, "durationMs": 500}`)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Target.Params["targetGainDb"] != float64(-60) {
		t.Fatalf("params = %+v, want targetGainDb preserved as authored", p.Target.Params)
	}
}

func TestDecodeShowActionPayloadAudioGainRefusesPreDecibelParamNames(t *testing.T) {
	for _, tc := range []struct{ action, params, field, names string }{
		{"audio.gain.set", `{"gain": 0.5}`, "target.params.gain", "gainDb"},
		{"audio.gain.fade", `{"targetGain": 0.5, "durationMs": 500}`, "target.params.targetGain", "targetGainDb"},
	} {
		_, verr := decodeAudioGainAction(t, tc.action, tc.params)
		if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != tc.field {
			t.Fatalf("%s: verr = %+v, want field-invalid on %s", tc.action, verr, tc.field)
		}
		if !strings.Contains(verr.Detail, tc.names) {
			t.Errorf("%s: refusal must name %s, got %q", tc.action, tc.names, verr.Detail)
		}
	}
}

func TestDecodeShowActionPayloadAudioGainRefusesMissingAndOutOfRangeDecibels(t *testing.T) {
	_, verr := decodeAudioGainAction(t, "audio.gain.set", `{}`)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "target.params.gainDb" {
		t.Fatalf("verr = %+v, want field-required on target.params.gainDb", verr)
	}

	_, verr = decodeAudioGainAction(t, "audio.gain.set", `{"gainDb": 40}`)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "target.params.gainDb" {
		t.Fatalf("verr = %+v, want field-invalid on target.params.gainDb for a value past the typo guard", verr)
	}

	_, verr = decodeAudioGainAction(t, "audio.gain.set", `{"gainDb": "loud"}`)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "target.params.gainDb" {
		t.Fatalf("verr = %+v, want field-invalid on a non-numeric target.params.gainDb", verr)
	}
}

// Every other audio action's params stay opaque: only the two gain
// operations carry a unit this file has any business checking.
func TestDecodeShowActionPayloadAudioNonGainParamsStayOpaque(t *testing.T) {
	raw := `{
		"show": "halloween-2026",
		"label": "Seek the bed",
		"safetyClass": "none",
		"target": {
			"integration": "audio",
			"audioNodeId": "node-a",
			"audioSessionId": "resting-bg",
			"audioAction": "audio.session.seek",
			"params": {"positionMs": 1500, "gain": 0.5}
		}
	}`
	p, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(),
		newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists, nil)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Target.Params["positionMs"] != float64(1500) || p.Target.Params["gain"] != 0.5 {
		t.Fatalf("params = %+v, want them carried through untouched", p.Target.Params)
	}
}

// --- ADR-049 decision 10: target.excludeNodes ---

func audioTargetJSONWithExcludeNodes(excludeNodes string) string {
	return `{
		"show": "halloween-2026",
		"label": "Hush resting background audio",
		"safetyClass": "stop",
		"target": {
			"integration": "audio",
			"excludeNodes": ` + excludeNodes + `,
			"audioSessionId": "resting-bg",
			"audioAction": "audio.session.stop"
		}
	}`
}

func TestDecodeShowActionPayloadAudioExcludeNodesValid(t *testing.T) {
	p, verr := DecodeShowActionPayload(audioTargetJSONWithExcludeNodes(`["node-b"]`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists,
		showAudioNodesFixture("node-a", "node-b"))
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if !strEqual(p.Target.ExcludeNodes, []string{"node-b"}) {
		t.Fatalf("unexpected excludeNodes: %v", p.Target.ExcludeNodes)
	}
}

func TestDecodeShowActionPayloadAudioExcludeNodesWithNodeIDRejected(t *testing.T) {
	raw := `{
		"show": "halloween-2026",
		"label": "Hush resting background audio",
		"safetyClass": "stop",
		"target": {
			"integration": "audio",
			"audioNodeId": ["node-a"],
			"excludeNodes": ["node-b"],
			"audioSessionId": "resting-bg",
			"audioAction": "audio.session.stop"
		}
	}`
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists,
		showAudioNodesFixture("node-a", "node-b"))
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "target.excludeNodes" {
		t.Fatalf("expected field-invalid on target.excludeNodes, got %+v", verr)
	}
}

func TestDecodeShowActionPayloadAudioExcludeNodesNotInShowListRejected(t *testing.T) {
	_, verr := DecodeShowActionPayload(audioTargetJSONWithExcludeNodes(`["ghost"]`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists,
		showAudioNodesFixture("node-a", "node-b"))
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference || verr.Field != "target.excludeNodes[0]" {
		t.Fatalf("expected field-unknown-reference on target.excludeNodes[0], got %+v", verr)
	}
}

func TestDecodeShowActionPayloadAudioExcludeNodesAllRejected(t *testing.T) {
	_, verr := DecodeShowActionPayload(audioTargetJSONWithExcludeNodes(`["node-a", "node-b"]`), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists,
		showAudioNodesFixture("node-a", "node-b"))
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "target.excludeNodes" {
		t.Fatalf("expected field-invalid on target.excludeNodes for excluding every node, got %+v", verr)
	}
}

func strEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
