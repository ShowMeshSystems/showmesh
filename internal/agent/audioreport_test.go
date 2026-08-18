package agent

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

func decodeAudioReport(t *testing.T, payload []byte) mqttproto.AudioPayload {
	t.Helper()
	env, err := mqttproto.DecodeEnvelope(payload)
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v", err)
	}
	p, err := mqttproto.DecodeAudioPayload(env)
	if err != nil {
		t.Fatalf("DecodeAudioPayload() error = %v", err)
	}
	return p
}

// TestBuildAudioPayloadCarriesAchievedRouteEvidence proves a route's
// Channels/Rate/Format in the built payload come straight from the
// discovery evidence, unchanged, and that LTCAvailable stays false when no
// route's SEPARATE LTC-constrained probe succeeded — even though the
// unconstrained route itself achieved channels, LTCChannels being zero
// means that constrained probe never ran or never achieved it.
func TestBuildAudioPayloadCarriesAchievedRouteEvidence(t *testing.T) {
	d := audio.Discovery{
		EngineUsable: true, HardwareEnumerated: true, HasHardwareCards: true,
		Routes: []audio.RouteEvidence{
			{Device: "hw:CARD=PCH,DEV=0", ProbeResult: audio.ProbeResult{Available: true, Channels: 2, Rate: 48000, Format: "S16LE"}},
		},
	}
	observedAt := time.Unix(5000, 0).UTC()
	p := buildAudioPayload(d, observedAt)

	if !p.EngineAvailable {
		t.Error("EngineAvailable = false, want true")
	}
	if !p.ProgramAvailable || !p.DeviceAvailable {
		t.Error("DeviceAvailable/ProgramAvailable = false, want true (route achieved 2 channels)")
	}
	if p.LTCAvailable {
		t.Error("LTCAvailable = true, want false (no route's LTC-constrained probe succeeded)")
	}
	if len(p.Routes) != 1 || p.Routes[0].Channels != 2 || p.Routes[0].Rate != 48000 || p.Routes[0].Format != "S16LE" {
		t.Errorf("Routes = %+v, want the achieved evidence carried through unchanged", p.Routes)
	}
	if p.ObservedAt == nil || !p.ObservedAt.Equal(observedAt) {
		t.Errorf("ObservedAt = %v, want %v", p.ObservedAt, observedAt)
	}
	if err := p.Validate(); err != nil {
		t.Errorf("built payload fails its own Validate: %v", err)
	}
}

// TestBuildAudioPayloadLTCAvailableRequiresConstrainedProbe proves
// LTCAvailable turns true only once a route's LTCChannels field (the
// separate, explicitly-constrained probe) reports enough channels.
func TestBuildAudioPayloadLTCAvailableRequiresConstrainedProbe(t *testing.T) {
	d := audio.Discovery{
		EngineUsable: true, HardwareEnumerated: true, HasHardwareCards: true,
		Routes: []audio.RouteEvidence{
			{
				Device:      "hw:CARD=PCH,DEV=0",
				ProbeResult: audio.ProbeResult{Available: true, Channels: 2, Rate: 48000, Format: "S16LE"},
				LTCChannels: 4,
			},
		},
	}
	p := buildAudioPayload(d, time.Now())
	if !p.LTCAvailable {
		t.Error("LTCAvailable = false, want true: the route's LTC-constrained probe achieved 4 channels")
	}
}

// TestBuildAudioPayloadNoEngineIsSelfConsistent proves a no-engine
// Discovery still produces a payload that passes AudioPayload.Validate:
// every required-whenever-false reason gets filled in.
func TestBuildAudioPayloadNoEngineIsSelfConsistent(t *testing.T) {
	d := audio.Discovery{EngineUsable: false, EngineReason: "gst-launch-1.0 not found on PATH", HardwareEnumerated: true}
	p := buildAudioPayload(d, time.Now())

	if err := p.Validate(); err != nil {
		t.Fatalf("built payload fails its own Validate: %v", err)
	}
	if p.DeviceAvailable || p.ProgramAvailable || p.LTCAvailable {
		t.Errorf("payload = %+v, want engine/device/program/ltc all unavailable", p)
	}
	if len(p.Routes) != 0 {
		t.Errorf("Routes = %v, want none", p.Routes)
	}
}

// TestBuildAudioPayloadEngineOnlyDeviceUnavailableReason proves the middle
// state states a real reason distinguishing "no hardware" from "had
// hardware but nothing probed usable" — enumeration itself succeeded here
// (HardwareEnumerated: true), it just found no cards.
func TestBuildAudioPayloadEngineOnlyDeviceUnavailableReason(t *testing.T) {
	d := audio.Discovery{EngineUsable: true, HardwareEnumerated: true, HasHardwareCards: false}
	p := buildAudioPayload(d, time.Now())

	if p.DeviceAvailable {
		t.Fatal("DeviceAvailable = true, want false")
	}
	if p.DeviceReason == "" {
		t.Error("DeviceReason is empty, want a stated reason")
	}
	if err := p.Validate(); err != nil {
		t.Errorf("built payload fails its own Validate: %v", err)
	}
}

// TestBuildAudioPayloadEnumerationFailureIsUnknownNotAbsent proves finding
// 4 at the payload boundary: when HardwareEnumerated is false, Device/
// Program/LTC all report "we do not know", carrying the enumeration
// failure's own reason — never the "no hardware card found" text a clean
// empty enumeration would use, which would misrepresent an unknown as a
// confirmed absence.
func TestBuildAudioPayloadEnumerationFailureIsUnknownNotAbsent(t *testing.T) {
	d := audio.Discovery{
		EngineUsable: true, HardwareEnumerated: false,
		HardwareEnumeratedReason: "device enumeration failed: exec: \"aplay\": executable file not found in $PATH",
	}
	p := buildAudioPayload(d, time.Now())

	if p.DeviceAvailable || p.ProgramAvailable || p.LTCAvailable {
		t.Errorf("payload = %+v, want device/program/ltc all unavailable", p)
	}
	for name, got := range map[string]string{"DeviceReason": p.DeviceReason, "ProgramReason": p.ProgramReason, "LTCReason": p.LTCReason} {
		if got != d.HardwareEnumeratedReason {
			t.Errorf("%s = %q, want the enumeration failure's own reason %q, not a confirmed-absence claim", name, got, d.HardwareEnumeratedReason)
		}
	}
	if err := p.Validate(); err != nil {
		t.Errorf("built payload fails its own Validate: %v", err)
	}
}

// TestRunAudioReportPublishesOnEachTick proves the report loop actually
// publishes to the audio observed topic on the injected discoverer's
// evidence.
func TestRunAudioReportPublishesOnEachTick(t *testing.T) {
	orig := audioDiscoverer
	audioDiscoverer = func(ctx context.Context, enum audio.Enumerator) audio.Discovery {
		return audio.Discovery{EngineUsable: true, HardwareEnumerated: true, HasHardwareCards: true, Routes: []audio.RouteEvidence{
			{Device: "hw:CARD=X,DEV=0", ProbeResult: audio.ProbeResult{Available: true, Channels: 2, Rate: 44100, Format: "S16LE"}},
		}}
	}
	t.Cleanup(func() { audioDiscoverer = orig })

	pub := newFakePublisher()
	ticks := make(chan time.Time, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAudioReport(ctx, pub, "audio-01", time.Now, ticks, discardLogger())
	}()

	ticks <- time.Now()
	<-pub.notify

	cancel()
	<-done

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("publish calls = %d, want 1", len(calls))
	}
	wantTopic, err := mqttproto.ObservedTopic("audio-01", "audio")
	if err != nil {
		t.Fatalf("ObservedTopic: %v", err)
	}
	if calls[0].topic != wantTopic {
		t.Errorf("topic = %q, want %q", calls[0].topic, wantTopic)
	}
	if !calls[0].retain {
		t.Error("retain = false, want true (ObservedDeliveryPolicy)")
	}
	got := decodeAudioReport(t, calls[0].payload)
	if !got.DeviceAvailable || got.OutputsCount != 1 {
		t.Errorf("decoded payload = %+v, want DeviceAvailable=true OutputsCount=1", got)
	}
}

// TestRunAudioReportProbesOnceAcrossMultipleTicks proves finding 1's core
// rule: audioDiscoverer runs exactly once for the life of the loop, no
// matter how many ticks arrive — a periodic re-probe would re-run this
// package's throwaway audiotestsrc pipelines against real outputs on a
// fixed cadence for as long as the agent runs.
func TestRunAudioReportProbesOnceAcrossMultipleTicks(t *testing.T) {
	orig := audioDiscoverer
	var calls int
	audioDiscoverer = func(ctx context.Context, enum audio.Enumerator) audio.Discovery {
		calls++
		return audio.Discovery{EngineUsable: true, HardwareEnumerated: true, HasHardwareCards: true}
	}
	t.Cleanup(func() { audioDiscoverer = orig })

	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAudioReport(ctx, pub, "audio-01", time.Now, ticks, discardLogger())
	}()

	for i := 0; i < 3; i++ {
		ticks <- time.Now()
		<-pub.notify
	}

	cancel()
	<-done

	if calls != 1 {
		t.Errorf("audioDiscoverer called %d times across 3 ticks, want exactly 1", calls)
	}
	publishes := pub.snapshot()
	if len(publishes) != 3 {
		t.Fatalf("publish calls = %d, want 3 (republishing the same cached evidence each tick)", len(publishes))
	}
	first := decodeAudioReport(t, publishes[0].payload)
	last := decodeAudioReport(t, publishes[2].payload)
	if first.ObservedAt == nil || last.ObservedAt == nil || !first.ObservedAt.Equal(*last.ObservedAt) {
		t.Errorf("ObservedAt changed across republishes (%v -> %v), want the SAME original probe time on every tick", first.ObservedAt, last.ObservedAt)
	}
}
