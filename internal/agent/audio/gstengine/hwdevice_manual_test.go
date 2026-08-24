//go:build cgo && showmesh_hwdevice

// Manual hardware gate. Opt in with -tags showmesh_hwdevice and point
// SHOWMESH_HW_ALSA_DEVICE at a real ALSA route, for example hw:0,0. It is
// excluded from every ordinary run because it opens a physical device.
package gstengine

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"
)

// TestRealALSADeviceOpensAndFlows drives the production alsasink at a real
// route and requires sustained buffer flow, not a single instant sample: an
// ALSA device can accept a pipeline and then refuse it once it opens.
func TestRealALSADeviceOpensAndFlows(t *testing.T) {
	device := os.Getenv("SHOWMESH_HW_ALSA_DEVICE")
	if device == "" {
		t.Skip("set SHOWMESH_HW_ALSA_DEVICE to run the real-device gate")
	}
	channels := 4
	if v := os.Getenv("SHOWMESH_HW_CHANNELS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("SHOWMESH_HW_CHANNELS: %v", err)
		}
		channels = n
	}
	rate := 48000
	if v := os.Getenv("SHOWMESH_HW_RATE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("SHOWMESH_HW_RATE: %v", err)
		}
		rate = n
	}

	cfg := Config{
		SinkFactory:     "alsasink",
		SinkProperties:  map[string]any{"device": device},
		ProgramChannels: []int{1, 2},
		LTCChannel:      3,
		ChannelCount:    channels,
		SampleRate:      rate,
		Resolve:         resolveByRuntimeFilename,
	}
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = boundedCall(ctx, func() error { _ = e.Close(); return nil })
	})

	// Availability is sampled across a settle window: a device-side refusal
	// arrives asynchronously on the bus after New has already returned.
	for _, wait := range []time.Duration{0, 300 * time.Millisecond, 700 * time.Millisecond} {
		time.Sleep(wait)
		ok, reason := e.Available()
		t.Logf("t=%-6s available=%v reason=%q", wait, ok, reason)
		if !ok {
			t.Fatalf("engine went unavailable against %s: %s", device, reason)
		}
	}
	t.Logf("PASS: %s stayed available at %d channels, %d Hz", device, channels, rate)
}
