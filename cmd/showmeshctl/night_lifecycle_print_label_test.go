package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintNightSessionStateDetailShowsTheCheckLabelNotItsName(t *testing.T) {
	var out bytes.Buffer
	printNightSessionStateDetail(&out, nightSessionStateWire{ID: "sess-1", Readiness: nightReadinessWire{
		State: "recorded",
		Checks: []nightReadinessCheckWire{{
			Name: "audio:alignment:pi-audio-01", Label: "Audio alignment on pi-audio-01", State: "healthy",
		}},
	}})
	if !strings.Contains(out.String(), "  - Audio alignment on pi-audio-01: healthy") || strings.Contains(out.String(), "audio:alignment") {
		t.Fatalf("output = %q, want the label and never the raw check name", out.String())
	}
}
