package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
)

// audioDeviceProbeKnownKeys is the key allowlist for "audio.device.probe".
var audioDeviceProbeKnownKeys = map[string]bool{"device": true, "channels": true, "rate": true}

// audioProbeOutput wraps [audio.ProbeOutput], a package-level var (matching
// audioDiscoverer's own injection convention in audiocapabilities.go) so
// command_test.go can prove this operation's wiring without shelling out
// to a real gst-launch-1.0.
var audioProbeOutput = audio.ProbeOutput

// probeAudioDevice is the OperationFunc for "audio.device.probe": run a
// real state-transition probe ([audio.ProbeOutput]) against params.device
// and report the achieved evidence — never the requested channels/rate
// echoed back (ruling 1). This operation's own effect is synchronous and
// complete by the time it returns, matching renderops.go's probeTransport
// shape one package over.
func probeAudioDevice(ctx context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
	const action = "audio.device.probe"

	device, err := parseAudioDevice(action, params)
	if err != nil {
		return OperationResult{}, err
	}
	if err := rejectUnknownKeys(action, params, audioDeviceProbeKnownKeys); err != nil {
		return OperationResult{}, err
	}
	channels, err := parseOptionalAudioInt(action, params, "channels")
	if err != nil {
		return OperationResult{}, err
	}
	rate, err := parseOptionalAudioInt(action, params, "rate")
	if err != nil {
		return OperationResult{}, err
	}

	executedAt := now()
	result := audioProbeOutput(ctx, device, channels, rate)
	observedAt := now()

	return OperationResult{
		Confirmed: result.Available,
		Signal:    "node.audio.device_probe",
		Value: map[string]any{
			"device":    device,
			"available": result.Available,
			"reason":    result.Reason,
			"channels":  result.Channels,
			"rate":      result.Rate,
			"format":    result.Format,
		},
		ExecutedAt: executedAt,
		ObservedAt: observedAt,
	}, nil
}

// parseAudioDevice extracts and validates params.device, the one required
// parameter for "audio.device.probe".
func parseAudioDevice(action string, params map[string]any) (string, error) {
	raw, ok := params["device"]
	if !ok {
		return "", fmt.Errorf("%s: params.device is required", action)
	}
	v, ok := raw.(string)
	if !ok || v == "" {
		return "", fmt.Errorf("%s: params.device must be a non-empty string, got %T", action, raw)
	}
	return v, nil
}

// parseOptionalAudioInt reads an optional numeric parameter, defaulting to
// 0 (audio.ProbeOutput's own "let GStreamer negotiate its own default"
// value) when absent. JSON numbers decode into map[string]any as float64.
func parseOptionalAudioInt(action string, params map[string]any, key string) (int, error) {
	raw, ok := params[key]
	if !ok {
		return 0, nil
	}
	f, ok := raw.(float64)
	if !ok {
		return 0, fmt.Errorf("%s: params.%s must be a number, got %T", action, key, raw)
	}
	return int(f), nil
}
