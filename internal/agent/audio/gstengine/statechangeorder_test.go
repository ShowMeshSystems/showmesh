//go:build cgo

package gstengine

import (
	"testing"

	"github.com/go-gst/go-gst/pkg/gst"
)

// TestBranchComesUpDownstreamFirstAndGoesDownSourceFirst proves the
// branch's source is the last element brought up, so its src pad is
// activated in pull mode by a decodebin that is already there rather
// than starting a push-mode streaming task that races decodebin's own
// switch to pull. A source-first upward transition loses that race
// intermittently and fails the loading branch at Load with
// "Internal data stream error".
func TestBranchComesUpDownstreamFirstAndGoesDownSourceFirst(t *testing.T) {
	gst.Init()

	els := make([]gst.Element, 0, 3)
	for _, factory := range []string{"filesrc", "decodebin", "audioconvert"} {
		el := gst.ElementFactoryMake(factory, factory)
		if el == nil {
			t.Skipf("skipping: %q not in this environment's GStreamer registry", factory)
		}
		els = append(els, el)
	}

	names := func(ordered []gst.Element) []string {
		out := make([]string, len(ordered))
		for i, el := range ordered {
			out[i] = el.GetName()
		}
		return out
	}

	for _, tc := range []struct {
		state gst.State
		want  []string
	}{
		{gst.StatePaused, []string{"audioconvert", "decodebin", "filesrc"}},
		{gst.StatePlaying, []string{"audioconvert", "decodebin", "filesrc"}},
		{gst.StateNull, []string{"filesrc", "decodebin", "audioconvert"}},
	} {
		got := names(stateChangeOrder(els, tc.state))
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Fatalf("state %v: order = %v, want %v", tc.state, got, tc.want)
			}
		}
	}
}
