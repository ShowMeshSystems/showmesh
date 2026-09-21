package fppreconcile

import (
	"context"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

// TestAudioNodeLocalClockWarning covers ADR-052 decision 4's whole
// decision table for one node: the warning fires only when LTC leaves
// through a second interface with nothing saying the two share a clock.
func TestAudioNodeLocalClockWarning(t *testing.T) {
	cases := []struct {
		name    string
		payload config.AudioNodePayload
		want    bool
	}{
		{
			name: "LTC on a second device of the same card",
			payload: config.AudioNodePayload{
				ProgramRoute: "hw:CARD=M4,DEV=0", LTCRoute: "hw:CARD=M4,DEV=1",
				ProgramChannels: []int{1, 2}, LTCChannel: 1,
			},
			want: false,
		},
		{
			name: "LTC on a second interface",
			payload: config.AudioNodePayload{
				ProgramRoute: "hw:CARD=M4,DEV=0", LTCRoute: "hw:CARD=Solo,DEV=0",
				ProgramChannels: []int{1, 2}, LTCChannel: 1,
			},
			want: true,
		},
		{
			name: "program only",
			payload: config.AudioNodePayload{
				ProgramRoute: "hw:CARD=M4,DEV=0", ProgramChannels: []int{1, 2},
			},
			want: false,
		},
		{
			name: "one interface carries both",
			payload: config.AudioNodePayload{
				ProgramRoute: "hw:CARD=M4,DEV=0", LTCRoute: "hw:CARD=M4,DEV=0",
				ProgramChannels: []int{1, 2}, LTCChannel: 3,
			},
			want: false,
		},
		{
			name: "second interface with an override",
			payload: config.AudioNodePayload{
				ProgramRoute: "hw:CARD=M4,DEV=0", LTCRoute: "hw:CARD=Solo,DEV=0",
				ProgramChannels: []int{1, 2}, LTCChannel: 1,
				LocalClockOverride: "house word clock",
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			warning := audioNodeLocalClockWarning("audio-01", tc.payload)
			if (warning != "") != tc.want {
				t.Fatalf("audioNodeLocalClockWarning = %q, want warning: %v", warning, tc.want)
			}
			if tc.want && !strings.Contains(warning, "audio-01") {
				t.Errorf("warning does not name the node: %q", warning)
			}
		})
	}
}

// TestPlaylistReadinessAudioLTCSeparateLocalClockSilentOnOneInterface
// proves the condition runs in the whole readiness path and says nothing
// for the ordinary node, one interface carrying both.
//
// The warning's own firing case cannot be reached from here yet:
// DecodeAudioNodePayload still refuses an LTC route that differs from the
// program route, and assetsync's cue-catalog resolution decodes every
// declared node's object, so a stored object of that shape fails this
// whole report before condition 16 is reached. The decision itself is
// covered by TestAudioNodeLocalClockWarning above, so the warning works
// the day that refusal is lifted.
func TestPlaylistReadinessAudioLTCSeparateLocalClockSilentOnOneInterface(t *testing.T) {
	st := openTestStore(t)
	p := multisyncReadyPlaylistFixture(t, st)

	report, err := PlaylistReadiness(context.Background(), st, nil, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if !report.Ready || strings.Contains(report.Warning, "drift against the music") {
		t.Fatalf("Ready = %v, Warning = %q, want ready with no drift warning for a node whose LTC and program share one route", report.Ready, report.Warning)
	}
}
