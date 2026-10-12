package nodeaudio

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

func TestSignalSessionUnreferencedReportsTheWireFlag(t *testing.T) {
	for _, unreferenced := range []bool{false, true} {
		st := NewStore()
		st.Put("audio-01", samplePayloadWithSession(mqttproto.AudioSessionReport{
			SessionID: "sess-1", State: "stopped", Fault: "none", Unreferenced: unreferenced,
		}), time.Now())
		obs, _ := New(st).Poll(context.Background())

		got := findSessionObs(t, obs, SignalSessionUnreferenced)
		if got.Value != unreferenced {
			t.Errorf("Unreferenced=%v: signal value = %v", unreferenced, got.Value)
		}
		if got.Absence != "" {
			t.Errorf("Unreferenced=%v: signal reports absence %q, want always collected", unreferenced, got.Absence)
		}
	}
}
