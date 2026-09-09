package mqttproto

import (
	"testing"

	"github.com/showmeshsystems/showmesh/pkg/audio"
)

// TestExactIntegerParamsMatchTheAudioVocabulary pins the one param name
// this package and pkg/audio both have to spell identically. A drift
// between them is silent at compile time and shows up only as a rounded
// start instant on a real node, which is the exact defect the exact
// decode exists to prevent.
func TestExactIntegerParamsMatchTheAudioVocabulary(t *testing.T) {
	for _, name := range exactIntegerParams {
		if name == audio.ParamScheduledAtNs {
			return
		}
	}
	t.Fatalf("exactIntegerParams %v does not carry audio.ParamScheduledAtNs (%q); a start instant would decode as a rounded float64", exactIntegerParams, audio.ParamScheduledAtNs)
}

// TestExactIntegerEvidenceFieldsMatchTheAudioVocabulary is the same pin
// on the result direction: the coordinator adds a margin to this reading
// and sends it straight back as a start instant, so rounding it here
// would round the schedule.
func TestExactIntegerEvidenceFieldsMatchTheAudioVocabulary(t *testing.T) {
	for _, want := range []string{audio.ResultMediaClockNowNs, audio.ResultMediaClockErrorBoundNs} {
		found := false
		for _, name := range exactIntegerEvidenceFields {
			if name == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("exactIntegerEvidenceFields %v does not carry %q; that reading would decode as a rounded float64", exactIntegerEvidenceFields, want)
		}
	}
}
