package fppreconcile

import (
	"context"
	"encoding/json"
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

// TestAudioLTCSeparateLocalClockReadinessReadsTheStore proves the check
// reaches a stored object, including one whose routes differ. Such an
// object is written here directly: the write path refuses two different
// routes, so this warning covers a stored object the API would not
// currently accept.
func TestAudioLTCSeparateLocalClockReadinessReadsTheStore(t *testing.T) {
	st := openTestStore(t)
	putAudioNode(t, st, "audio-01")

	warning, err := audioLTCSeparateLocalClockReadiness(context.Background(), st)
	if err != nil {
		t.Fatalf("audioLTCSeparateLocalClockReadiness: %v", err)
	}
	if warning != "" {
		t.Fatalf("warning = %q, want none for a single-interface node", warning)
	}

	raw, err := json.Marshal(config.AudioNodePayload{
		ProgramRoute: "hw:CARD=M4,DEV=0", LTCRoute: "hw:CARD=Solo,DEV=0",
		ProgramChannels: []int{1, 2}, LTCChannel: 1,
	})
	if err != nil {
		t.Fatalf("marshal audio.node payload: %v", err)
	}
	putConfig(t, st, config.AudioNodeConfigKind, "audio-02", string(raw))

	warning, err = audioLTCSeparateLocalClockReadiness(context.Background(), st)
	if err != nil {
		t.Fatalf("audioLTCSeparateLocalClockReadiness: %v", err)
	}
	if !strings.Contains(warning, "audio-02") {
		t.Fatalf("warning = %q, want it to name audio-02", warning)
	}
}
