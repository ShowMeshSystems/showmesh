package agent

import (
	"testing"

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
