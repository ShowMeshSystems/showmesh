package agent

import (
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// This file covers parseApplyRequest's ltcStartOffset handling (a
// session's per-session LTC start-offset override) — the wire-decode
// half of pkg/audio/ltc_test.go's SessionDesiredState/ApplyRequest
// coverage.

func TestParseApplyRequestAcceptsLTCStartOffset(t *testing.T) {
	req, err := parseApplyRequest("audio.session.apply", map[string]any{
		"ltcStartOffset": "01:00:00:00",
	})
	if err != nil {
		t.Fatalf("parseApplyRequest: %v", err)
	}
	v, ok := req.LTCStartOffset.Value()
	if !ok || v != pkgaudio.LTCTimecode("01:00:00:00") {
		t.Errorf("LTCStartOffset = %v (ok=%v), want 01:00:00:00", v, ok)
	}
}

func TestParseApplyRequestOmittedLTCStartOffsetStaysUnset(t *testing.T) {
	req, err := parseApplyRequest("audio.session.apply", map[string]any{})
	if err != nil {
		t.Fatalf("parseApplyRequest: %v", err)
	}
	if !req.LTCStartOffset.IsUnset() {
		t.Errorf("LTCStartOffset state = %v, want unset", req.LTCStartOffset.State())
	}
}

func TestParseApplyRequestRejectsMalformedLTCStartOffset(t *testing.T) {
	_, err := parseApplyRequest("audio.session.apply", map[string]any{
		"ltcStartOffset": "not-a-timecode",
	})
	if err == nil {
		t.Error("parseApplyRequest(malformed ltcStartOffset) = nil error, want one")
	}
}

func TestParseApplyRequestRejectsNonStringLTCStartOffset(t *testing.T) {
	_, err := parseApplyRequest("audio.session.apply", map[string]any{
		"ltcStartOffset": 123,
	})
	if err == nil {
		t.Error("parseApplyRequest(numeric ltcStartOffset) = nil error, want one")
	}
}

func TestParseApplyRequestRejectsEmptyLTCStartOffset(t *testing.T) {
	_, err := parseApplyRequest("audio.session.apply", map[string]any{
		"ltcStartOffset": "",
	})
	if err == nil {
		t.Error("parseApplyRequest(empty ltcStartOffset) = nil error, want one")
	}
}

// TestParseApplyRequestAcceptsExpiresInMs is the load-bearing case for
// the night controller's periodic re-affirm: a refresh carrying only
// expiresInMs must be accepted, not refused as an unknown key, or the
// expiry mechanism goes stale silently (see nightbackgroundaudio.go's
// own re-affirm doc comment).
func TestParseApplyRequestAcceptsExpiresInMs(t *testing.T) {
	before := time.Now()
	req, err := parseApplyRequest("audio.session.apply", map[string]any{
		"expiresInMs": float64(60_000),
	})
	if err != nil {
		t.Fatalf("parseApplyRequest: %v", err)
	}
	after := time.Now()
	v, ok := req.Expiry.Value()
	if !ok {
		t.Fatal("Expiry state = unset, want set")
	}
	if v.Before(before.Add(60*time.Second)) || v.After(after.Add(60*time.Second)) {
		t.Errorf("Expiry = %v, want between %v and %v", v, before.Add(60*time.Second), after.Add(60*time.Second))
	}
}

func TestParseApplyRequestOmittedExpiresInMsStaysUnset(t *testing.T) {
	req, err := parseApplyRequest("audio.session.apply", map[string]any{})
	if err != nil {
		t.Fatalf("parseApplyRequest: %v", err)
	}
	if !req.Expiry.IsUnset() {
		t.Errorf("Expiry state = %v, want unset", req.Expiry.State())
	}
}

func TestParseApplyRequestRejectsNonPositiveExpiresInMs(t *testing.T) {
	for _, ms := range []float64{0, -1} {
		if _, err := parseApplyRequest("audio.session.apply", map[string]any{"expiresInMs": ms}); err == nil {
			t.Errorf("parseApplyRequest(expiresInMs=%v) = nil error, want one", ms)
		}
	}
}

func TestParseApplyRequestRejectsNonNumericExpiresInMs(t *testing.T) {
	if _, err := parseApplyRequest("audio.session.apply", map[string]any{"expiresInMs": "60000"}); err == nil {
		t.Error("parseApplyRequest(string expiresInMs) = nil error, want one")
	}
}

// TestParseApplyRequestAcceptsCeiling is the load-bearing case for the
// gain ceiling traveling on the wire at all: a linear params.ceiling
// must land in ApplyRequest.Ceiling for Manager.Apply to merge onto the
// session's desired state.
func TestParseApplyRequestAcceptsCeiling(t *testing.T) {
	req, err := parseApplyRequest("audio.session.apply", map[string]any{
		"ceiling": 0.5,
	})
	if err != nil {
		t.Fatalf("parseApplyRequest: %v", err)
	}
	v, ok := req.Ceiling.Value()
	if !ok || v != pkgaudio.Ceiling(0.5) {
		t.Errorf("Ceiling = %v (ok=%v), want 0.5", v, ok)
	}
}

// TestParseApplyRequestOmittedCeilingStaysUnset is the optionality
// requirement itself: an apply that never mentions ceiling must leave a
// session's already-applied ceiling exactly as it was, matching every
// deployed caller (older agent or coordinator) that has never heard of
// this field.
func TestParseApplyRequestOmittedCeilingStaysUnset(t *testing.T) {
	req, err := parseApplyRequest("audio.session.apply", map[string]any{})
	if err != nil {
		t.Fatalf("parseApplyRequest: %v", err)
	}
	if !req.Ceiling.IsUnset() {
		t.Errorf("Ceiling state = %v, want unset", req.Ceiling.State())
	}
}

// TestParseApplyRequestRejectsNonPositiveCeiling reuses
// pkgaudio.Ceiling.Validate rather than a second bound: zero and
// negative are both refused, because a ceiling that clamps everything to
// silence is indistinguishable from a deliberate mute.
func TestParseApplyRequestRejectsNonPositiveCeiling(t *testing.T) {
	for _, c := range []float64{0, -1} {
		if _, err := parseApplyRequest("audio.session.apply", map[string]any{"ceiling": c}); err == nil {
			t.Errorf("parseApplyRequest(ceiling=%v) = nil error, want one", c)
		}
	}
}

func TestParseApplyRequestRejectsNonNumericCeiling(t *testing.T) {
	if _, err := parseApplyRequest("audio.session.apply", map[string]any{"ceiling": "loud"}); err == nil {
		t.Error("parseApplyRequest(string ceiling) = nil error, want one")
	}
}

// TestApplyAcceptsEveryMixPolicyOverTheWire guards reachability rather
// than behaviour. Announcement policy is a resolved decision and the
// session layer implements all three, but none of it is worth anything
// if an operator cannot set the field: an unknown key is rejected, so a
// policy absent from the parser is a policy nothing can ever select.
func TestApplyAcceptsEveryMixPolicyOverTheWire(t *testing.T) {
	for _, policy := range []string{"mix", "duck", "interrupt"} {
		req, err := parseApplyRequest("audio.session.apply", map[string]any{
			"sessionId":    "s1",
			"invocationId": "inv-1",
			"revision":     float64(1),
			"sourceRole":   "announcement",
			"mixPolicy":    policy,
		})
		if err != nil {
			t.Fatalf("mixPolicy %q was refused over the wire: %v", policy, err)
		}
		got, ok := req.MixPolicy.Value()
		if !ok {
			t.Fatalf("mixPolicy %q parsed but did not reach the request", policy)
		}
		if string(got) != policy {
			t.Fatalf("mixPolicy parsed as %q, want %q", got, policy)
		}
	}
}

func TestApplyRefusesAnUnknownMixPolicy(t *testing.T) {
	_, err := parseApplyRequest("audio.session.apply", map[string]any{
		"sessionId":    "s1",
		"invocationId": "inv-1",
		"revision":     float64(1),
		"mixPolicy":    "quieten",
	})
	if err == nil {
		t.Fatal("an unknown mix policy was accepted; a closed vocabulary must refuse a member it does not know")
	}
}
