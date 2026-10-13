package api

import (
	"strings"
	"testing"
)

// TestNightCheckLabelNamesEveryCheckInPlainWords pins one label per check
// family and holds every label, known or not, to words an operator can read.
func TestNightCheckLabelNamesEveryCheckInPlainWords(t *testing.T) {
	for name, want := range map[string]string{
		"resting:background-audio-assets":                          "Background audio files",
		"resting:background-audio-item-transition:pi-audio-01":     "Background audio track changes on pi-audio-01",
		"resting:background-audio-output-capabilities:pi-audio-01": "Background audio output on pi-audio-01",
		"resting:background-audio-media-playlist":                  "Background audio playlist",
		"resting:background-audio-nodes-defaulted":                 "Background audio nodes",
		"resting:background-audio-bed-target-coverage":             "Background audio files on every node",
		"resting:background-audio-bed-program-ltc":                 "Background audio sync",
		"announcement-assets":                                      "Announcement audio files",
		"announcement-policy-enforceable":                          "Announcements over background audio",
		"audio:alignment":                                          "Audio alignment",
		"audio:alignment:pi-audio-01":                              "Audio alignment on pi-audio-01",
		"resting:playlist-shape:Resting":                           "Resting playlist Resting",
		"resting-end-of-night:playlist-shape:Goodnight":            "End of night playlist Goodnight",
		"resting:asset-exact-variant:Resting":                      "Resting sequence file on FPP for Resting",
		"resting-end-of-night:asset-exact-variant:Goodnight":       "End of night sequence file on FPP for Goodnight",
		"show:playlist-present:Main Show":                          "Show playlist Main Show",
		"resting:asset-duration":                                   "Resting sequence length",
		"enterShow:first-cue-confirmable":                          "First show cue",
		"enterShow:brightness-composition":                         "Brightness when the show starts",
		"enterResting:brightness-composition":                      "Brightness when the show ends",
		nightCatalogCurrentCheckPrefix:                             "Cue list on nodes",
		nightCatalogCurrentCheckPrefix + ":pi-audio-01":            "Cue list on pi-audio-01",
		nightParticipationCheckPrefix:                              "FPP and Resolume in this show",
		nightParticipationCheckPrefix + ":fpp:main-fpp":            "FPP main-fpp",
		nightParticipationCheckPrefix + ":resolume:arena":          "Resolume arena",
		"lights:fade-out":                                          "Lights fade out",
		"lights:fade-in":                                           "Lights fade in",
		"lights:fade-length":                                       "Lights fade length",
		"interlock:start-night:garage-door":                        "Safety check garage-door for Start night",
		"fpp:main-fpp:reachable":                                   "FPP main-fpp reachable",
		"fpp-plugin-reports-refused":                               "FPP reports",
		"something:added-later":                                    "Something added later",
	} {
		got := nightCheckLabel(name)
		if got != want {
			t.Errorf("nightCheckLabel(%q) = %q, want %q", name, got, want)
		}
		if strings.ContainsAny(got, ":_") || strings.Contains(got, "interlock") || strings.Contains(got, "resting:") {
			t.Errorf("nightCheckLabel(%q) = %q, which still reads as an id", name, got)
		}
	}
}

func TestNightEncodeChecksCarriesTheLabel(t *testing.T) {
	out := nightEncodeChecks([]nightReadinessCheck{{name: "audio:alignment:pi-audio-01", health: nightHealthHealthy()}})
	if len(out) != 1 || out[0].Name != "audio:alignment:pi-audio-01" || out[0].Label != "Audio alignment on pi-audio-01" {
		t.Fatalf("encoded = %+v, want the name kept and the label added", out)
	}
}
