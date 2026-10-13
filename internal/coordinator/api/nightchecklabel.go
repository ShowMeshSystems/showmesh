package api

import "strings"

// nightCheckLabels names the checks whose id carries no node, playlist or
// instance name.
var nightCheckLabels = map[string]string{
	"resting:background-audio-assets":              "Background audio files",
	"resting:background-audio-media-playlist":      "Background audio playlist",
	"resting:background-audio-nodes-defaulted":     "Background audio nodes",
	"resting:background-audio-bed-target-coverage": "Background audio files on every node",
	"resting:background-audio-bed-program-ltc":     "Background audio sync",
	"resting:asset-duration":                       "Resting sequence length",
	"announcement-assets":                          "Announcement audio files",
	"announcement-policy-enforceable":              "Announcements over background audio",
	"audio:alignment":                              "Audio alignment",
	"enterShow:first-cue-confirmable":              "First show cue",
	"enterShow:brightness-composition":             "Brightness when the show starts",
	"enterResting:brightness-composition":          "Brightness when the show ends",
	"lights:fade-out":                              "Lights fade out",
	"lights:fade-in":                               "Lights fade in",
	"lights:fade-length":                           "Lights fade length",
	"fpp-plugin-reports-refused":                   "FPP reports",
	nightCatalogCurrentCheckPrefix:                 "Cue list on nodes",
	nightParticipationCheckPrefix:                  "FPP and Resolume in this show",
}

// nightCheckLabelPrefixes names the checks whose id ends in the node,
// playlist or instance they are about. The name follows the label.
var nightCheckLabelPrefixes = []struct{ prefix, label string }{
	{"resting:background-audio-item-transition:", "Background audio track changes on "},
	{"resting:background-audio-output-capabilities:", "Background audio output on "},
	{"audio:alignment:", "Audio alignment on "},
	{nightCatalogCurrentCheckPrefix + ":", "Cue list on "},
	{nightParticipationCheckPrefix + ":fpp:", "FPP "},
	{nightParticipationCheckPrefix + ":resolume:", "Resolume "},
	{nightCheckPrefixResting + ":playlist-shape:", "Resting playlist "},
	{nightCheckPrefixEndOfNight + ":playlist-shape:", "End of night playlist "},
	{nightCheckPrefixResting + ":asset-exact-variant:", "Resting sequence file on FPP for "},
	{nightCheckPrefixEndOfNight + ":asset-exact-variant:", "End of night sequence file on FPP for "},
	{"show:playlist-present:", "Show playlist "},
}

// nightCheckLabel is the plain name an operator reads for a readiness check.
// A name it does not know still reads as words, never as the raw id.
func nightCheckLabel(name string) string {
	if label, ok := nightCheckLabels[name]; ok {
		return label
	}
	for _, p := range nightCheckLabelPrefixes {
		if rest, ok := strings.CutPrefix(name, p.prefix); ok && rest != "" {
			return p.label + rest
		}
	}
	if rest, ok := strings.CutPrefix(name, "fpp:"); ok {
		if id, ok := strings.CutSuffix(rest, ":reachable"); ok && id != "" {
			return "FPP " + id + " reachable"
		}
	}
	if rest, ok := strings.CutPrefix(name, "interlock:"); ok {
		if phase, rule, ok := strings.Cut(rest, ":"); ok && rule != "" {
			return "Safety check " + rule + " for " + inWords(phase)
		}
	}
	return inWords(name)
}

// inWords turns an id such as "start-night" into "Start night".
func inWords(id string) string {
	words := strings.Join(strings.FieldsFunc(id, func(r rune) bool { return r == ':' || r == '-' || r == '_' }), " ")
	if words == "" {
		return "Check"
	}
	return strings.ToUpper(words[:1]) + words[1:]
}

// nightCheckStateLabel is a check's state in the words an operator reads.
func nightCheckStateLabel(state nightCheckState) string {
	switch state {
	case nightHealthHealthy():
		return "Passed"
	case nightHealthDegraded():
		return "Warning"
	case nightHealthFailed():
		return "Failed"
	case nightCheckStateNotVerifiable:
		return "Cannot be checked"
	case nightCheckStateNotConfigured:
		return "Not set up"
	}
	return "Unknown"
}

var nightStateLabels = map[string]string{
	nightStateInactive:            "Not prepared",
	nightStatePreparing:           "Prepared",
	nightStatePreshow:             "Preshow",
	nightStateTransitionToShow:    "Going to show",
	nightStateLive:                "Show running",
	nightStateTransitionToResting: "Going to resting",
	nightStateRestingIntershow:    "Resting",
	nightStateEndOfNightResting:   "Resting, end of night",
	nightStateFadingOut:           "Fading out",
	nightStateStopped:             "Stopped",
}

// nightStateLabel is a session state in the words an operator reads.
func nightStateLabel(state string) string {
	if label, ok := nightStateLabels[state]; ok {
		return label
	}
	return inWords(state)
}

var nightOutcomeLabels = map[string]string{
	"ready":               "Passed",
	"ready_with_warnings": "Passed with a warning",
	"not_ready":           "Did not pass",
	"unknown":             "Could not be determined",
}
