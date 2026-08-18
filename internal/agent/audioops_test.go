package agent

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
)

func withAudioProbeOutput(t *testing.T, fn func(ctx context.Context, device string, channels, rate int) audio.ProbeResult) {
	t.Helper()
	orig := audioProbeOutput
	audioProbeOutput = fn
	t.Cleanup(func() { audioProbeOutput = orig })
}

// TestProbeAudioDeviceReportsAchievedNeverRequested proves the command
// boundary: a caller requesting 6 channels/96000 Hz gets back exactly
// what the probe achieved, never the request echoed.
func TestProbeAudioDeviceReportsAchievedNeverRequested(t *testing.T) {
	var gotDevice string
	var gotChannels, gotRate int
	withAudioProbeOutput(t, func(ctx context.Context, device string, channels, rate int) audio.ProbeResult {
		gotDevice, gotChannels, gotRate = device, channels, rate
		return audio.ProbeResult{Available: true, Channels: 2, Rate: 44100, Format: "F64LE"}
	})

	result, err := probeAudioDevice(context.Background(), map[string]any{
		"device": "hw:CARD=PCH,DEV=0", "channels": float64(6), "rate": float64(96000),
	}, time.Now)
	if err != nil {
		t.Fatalf("probeAudioDevice: %v", err)
	}

	if gotDevice != "hw:CARD=PCH,DEV=0" || gotChannels != 6 || gotRate != 96000 {
		t.Fatalf("audioProbeOutput called with (%q, %d, %d), want the parsed request forwarded unchanged", gotDevice, gotChannels, gotRate)
	}
	if !result.Confirmed {
		t.Fatal("Confirmed = false, want true (probe succeeded)")
	}
	val, ok := result.Value.(map[string]any)
	if !ok {
		t.Fatalf("Value = %v (%T), want map[string]any", result.Value, result.Value)
	}
	if val["channels"] != 2 || val["rate"] != 44100 {
		t.Errorf("reported Value = %+v, want the ACHIEVED 2/44100, not the requested 6/96000", val)
	}
}

func TestProbeAudioDeviceRequiresDevice(t *testing.T) {
	_, err := probeAudioDevice(context.Background(), map[string]any{}, time.Now)
	if err == nil {
		t.Fatal("probeAudioDevice with no device param: got nil error, want one")
	}
}

func TestProbeAudioDeviceRejectsUnknownKeys(t *testing.T) {
	_, err := probeAudioDevice(context.Background(), map[string]any{"device": "hw:CARD=X,DEV=0", "bogus": "x"}, time.Now)
	if err == nil {
		t.Fatal("probeAudioDevice with an unrecognized key: got nil error, want one")
	}
}

func TestProbeAudioDeviceRejectsNonNumericChannels(t *testing.T) {
	_, err := probeAudioDevice(context.Background(), map[string]any{"device": "hw:CARD=X,DEV=0", "channels": "two"}, time.Now)
	if err == nil {
		t.Fatal("probeAudioDevice with a non-numeric channels: got nil error, want one")
	}
}

// TestProbeAudioDeviceUnavailableIsNotConfirmedButNotAnError proves an
// honest probe failure reports Confirmed=false with evidence, never an
// OperationFunc error (which would be a transport/refusal signal, not a
// probe outcome) — matching probeTransport's identical contract one
// package over.
func TestProbeAudioDeviceUnavailableIsNotConfirmedButNotAnError(t *testing.T) {
	withAudioProbeOutput(t, func(ctx context.Context, device string, channels, rate int) audio.ProbeResult {
		return audio.ProbeResult{Available: false, Reason: "could not open audio device"}
	})

	result, err := probeAudioDevice(context.Background(), map[string]any{"device": "hw:CARD=GHOST,DEV=0"}, time.Now)
	if err != nil {
		t.Fatalf("probeAudioDevice: unexpected error %v", err)
	}
	if result.Confirmed {
		t.Error("Confirmed = true, want false")
	}
	val := result.Value.(map[string]any)
	if val["reason"] != "could not open audio device" {
		t.Errorf("reason = %v, want the probe's stated reason", val["reason"])
	}
}

// TestProbeAudioDeviceIsAllowlisted proves "audio.device.probe" is a real
// key in the agent's command allowlist, not merely a function that exists
// — Step 6's own lesson (a capability with no caller is not a capability).
func TestProbeAudioDeviceIsAllowlisted(t *testing.T) {
	ops := newOperationRegistry(t.TempDir(), "", nil)
	if _, ok := ops["audio.device.probe"]; !ok {
		t.Fatal(`newOperationRegistry() does not contain "audio.device.probe"`)
	}
}
