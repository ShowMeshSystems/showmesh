package config

import "testing"

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
	p, verr := DecodeShowActionPayload(validAudioActionJSON(), testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Target.Integration != ShowActionIntegrationAudio {
		t.Fatalf("integration = %q, want %q", p.Target.Integration, ShowActionIntegrationAudio)
	}
	if p.Target.AudioNodeID != "node-a" || p.Target.AudioSessionID != "resting-bg" || p.Target.AudioAction != "audio.session.stop" {
		t.Fatalf("target = %+v", p.Target)
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
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
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
	_, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
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
	p, verr := DecodeShowActionPayload(raw, testEndpoints(), testBrokers(), newFakeFPPPrimitiveRegistry(), newFakeResolumeReferenceResolver(), alwaysTrueShowExists)
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
