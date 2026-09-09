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
