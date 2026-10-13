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
			Name: "audio:alignment:pi-audio-01", Label: "Audio alignment on pi-audio-01", State: "healthy", StateLabel: "Passed",
		}},
	}})
	if !strings.Contains(out.String(), "  - Audio alignment on pi-audio-01: Passed") || strings.Contains(out.String(), "audio:alignment") {
		t.Fatalf("output = %q, want the label and never the raw check name", out.String())
	}
}

func TestPrintNightSessionStateDetailPrintsEachCheckReasonOnItsOwnLine(t *testing.T) {
	var out bytes.Buffer
	printNightSessionStateDetail(&out, nightSessionStateWire{ID: "sess-1", Readiness: nightReadinessWire{
		State: "recorded",
		Checks: []nightReadinessCheckWire{{
			Label: "Announcement audio files", StateLabel: "Failed",
			Reasons: []string{"Cue welcome plays hello.wav, and audio-02 cannot get it. Upload the file.", "Cue goodbye plays bye.wav, and audio-02 cannot get it. Upload the file."},
		}},
	}})
	want := "  - Announcement audio files: Failed\n" +
		"      Cue welcome plays hello.wav, and audio-02 cannot get it. Upload the file.\n" +
		"      Cue goodbye plays bye.wav, and audio-02 cannot get it. Upload the file.\n"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("output = %q, want one line per reason", out.String())
	}
}
