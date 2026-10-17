package api

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/nodeaudio"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

func engineSignal(nodeID string, signal observation.SignalID, value string) observation.Observation {
	at := testNow
	return observation.Observation{
		Resource: observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID},
		Signal:   signal, Value: value, ObservedAt: &at, ValidFor: 45 * time.Second,
	}
}

func TestNightCheckAudioClock(t *testing.T) {
	cases := []struct {
		name    string
		backend string
		source  string
		want    nightCheckState
	}{
		{"pipewire on the default clock plays nothing", "pipewiresink", "default", nightHealthFailed()},
		{"pipewire on the PTP clock", "pipewiresink", "phc", nightHealthHealthy()},
		{"pipewire on the realtime clock", "pipewiresink", "realtime", nightHealthHealthy()},
		{"alsa keeps the card's own clock", "alsasink", "default", nightHealthHealthy()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, st, _, _ := nightBackgroundAudioTestHandlers(t)
			putAudioNodeForTest(t, st, "node-a")
			h.deps.Audio.(*fakeNodeAudioLister).setObservations("node-a", []observation.Observation{
				engineSignal("node-a", nodeaudio.SignalEngineSinkBackend, tc.backend),
				engineSignal("node-a", nodeaudio.SignalEngineClockSource, tc.source),
			})

			checks := h.nightCheckAudioClock(context.Background(), testNow)

			if len(checks) != 1 || checks[0].name != nightAudioClockCheckPrefix+"node-a" || checks[0].health != tc.want {
				t.Fatalf("checks = %+v, want one %s check", checks, tc.want)
			}
			if tc.want == nightHealthFailed() && !containsAllSubstrings(checks[0].reason, "node-a", "plays nothing", "PTP clock") {
				t.Fatalf("reason = %q, want the node, the effect, and the fix", checks[0].reason)
			}
		})
	}
}

func TestNightCheckAudioClock_NoReportIsNotVerifiable(t *testing.T) {
	h, st, _, _ := nightBackgroundAudioTestHandlers(t)
	putAudioNodeForTest(t, st, "node-a")

	checks := h.nightCheckAudioClock(context.Background(), testNow)

	if len(checks) != 1 || checks[0].health != nightCheckStateNotVerifiable {
		t.Fatalf("checks = %+v, want one not_verifiable check", checks)
	}
}
