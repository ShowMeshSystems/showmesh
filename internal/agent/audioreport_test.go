package agent

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
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
		runAudioReport(ctx, pub, "audio-01", nil, nil, time.Now, ticks, discardLogger())
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

// stubLTCObserver is a scriptable [ltcObserver]: each call returns the
// next entry in results, matching stubSnapshotter's own shape.
type stubLTCObserver struct {
	results []audio.LTCObservation
	calls   int
}

func (s *stubLTCObserver) ObserveLTC(context.Context) audio.LTCObservation {
	i := s.calls
	if i >= len(s.results) {
		i = len(s.results) - 1
	}
	s.calls++
	return s.results[i]
}

// TestRunAudioReportNilLTCObserverReportsUnsupportedWithReason proves the
// degrade-never-omit rule at the report boundary: a node with no wired
// LTC source still publishes a self-consistent, Validate-passing payload
// naming node.audio.ltc.generator.state, never an absent or zero-value
// field.
func TestRunAudioReportNilLTCObserverReportsUnsupportedWithReason(t *testing.T) {
	orig := audioDiscoverer
	audioDiscoverer = func(ctx context.Context, enum audio.Enumerator) audio.Discovery {
		return audio.Discovery{EngineUsable: true, HardwareEnumerated: true, HasHardwareCards: true}
	}
	t.Cleanup(func() { audioDiscoverer = orig })

	pub := newFakePublisher()
	ticks := make(chan time.Time, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAudioReport(ctx, pub, "audio-01", nil, nil, time.Now, ticks, discardLogger())
	}()

	ticks <- time.Now()
	<-pub.notify
	cancel()
	<-done

	got := decodeAudioReport(t, pub.snapshot()[0].payload)
	if got.LTCGeneratorState != string(audio.LTCUnsupported) {
		t.Errorf("LTCGeneratorState = %q, want %q", got.LTCGeneratorState, audio.LTCUnsupported)
	}
	if got.LTCGeneratorReason == "" {
		t.Error("LTCGeneratorReason is empty, want a stated reason")
	}
	if got.LTCFrameRateKnown || got.LTCTimecodeKnown {
		t.Errorf("payload = %+v, want no frame rate or timecode evidence", got)
	}
}

// TestRunAudioReportRebuildsLTCStateEveryTick proves the LTC half of the
// report is live, matching TestRunAudioReportRebuildsSessionsEveryTick's
// identical proof for sessions: LTC that stops between two ticks must
// produce two different published states.
func TestRunAudioReportRebuildsLTCStateEveryTick(t *testing.T) {
	orig := audioDiscoverer
	audioDiscoverer = func(ctx context.Context, enum audio.Enumerator) audio.Discovery {
		return audio.Discovery{EngineUsable: true, HardwareEnumerated: true, HasHardwareCards: true}
	}
	t.Cleanup(func() { audioDiscoverer = orig })

	gen := &stubLTCObserver{results: []audio.LTCObservation{
		{State: audio.LTCRunning, FrameRateKnown: true, FrameRate: pkgaudio.LTCFrameRate30, TimecodeKnown: true, Timecode: "00:00:03:00"},
		{State: audio.LTCStopped, Reason: "the show session that drove LTC stopped", FrameRateKnown: true, FrameRate: pkgaudio.LTCFrameRate30},
	}}

	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAudioReport(ctx, pub, "audio-01", nil, gen, time.Now, ticks, discardLogger())
	}()

	for i := 0; i < 2; i++ {
		ticks <- time.Now()
		<-pub.notify
	}
	cancel()
	<-done

	publishes := pub.snapshot()
	if len(publishes) != 2 {
		t.Fatalf("publish calls = %d, want 2", len(publishes))
	}
	first := decodeAudioReport(t, publishes[0].payload)
	second := decodeAudioReport(t, publishes[1].payload)

	if first.LTCGeneratorState != string(audio.LTCRunning) {
		t.Errorf("first tick LTCGeneratorState = %q, want running", first.LTCGeneratorState)
	}
	if !first.LTCTimecodeKnown || first.LTCTimecode != "00:00:03:00" {
		t.Errorf("first tick = %+v, want TimecodeKnown 00:00:03:00", first)
	}
	if first.LTCGeneratorReason != "" {
		t.Errorf("first tick (running) LTCGeneratorReason = %q, want empty", first.LTCGeneratorReason)
	}
	if second.LTCGeneratorState != string(audio.LTCStopped) {
		t.Errorf("second tick LTCGeneratorState = %q, want stopped", second.LTCGeneratorState)
	}
	if second.LTCTimecodeKnown {
		t.Error("second tick reports TimecodeKnown, want false (no longer emitting)")
	}
	if second.LTCGeneratorReason == "" {
		t.Error("second tick (stopped) LTCGeneratorReason is empty, want a stated reason")
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
		runAudioReport(ctx, pub, "audio-01", nil, nil, time.Now, ticks, discardLogger())
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
	if first.DiscoveredAt == nil || last.DiscoveredAt == nil || !first.DiscoveredAt.Equal(*last.DiscoveredAt) {
		t.Errorf("DiscoveredAt changed across republishes (%v -> %v), want the SAME original probe time on every tick", first.DiscoveredAt, last.DiscoveredAt)
	}
}

// TestRunAudioReportObservedAtAdvancesWhileDiscoveredAtStaysPinned proves
// finding 4 (the bug this file's ADR-011 lesson names): DiscoveredAt is the
// one-shot startup probe time and must stay pinned across every tick, but
// ObservedAt is per-tick live evidence and must actually advance — never
// share DiscoveredAt's single startup reading, which is what previously
// left every session and LTC generator signal marked stale after 45
// seconds regardless of how fresh the underlying data was.
func TestRunAudioReportObservedAtAdvancesWhileDiscoveredAtStaysPinned(t *testing.T) {
	orig := audioDiscoverer
	audioDiscoverer = func(ctx context.Context, enum audio.Enumerator) audio.Discovery {
		return audio.Discovery{EngineUsable: true, HardwareEnumerated: true, HasHardwareCards: true}
	}
	t.Cleanup(func() { audioDiscoverer = orig })

	base := time.Unix(10_000, 0).UTC()
	var calls int
	now := func() time.Time {
		got := base.Add(time.Duration(calls) * time.Minute)
		calls++
		return got
	}

	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAudioReport(ctx, pub, "audio-01", nil, nil, now, ticks, discardLogger())
	}()

	const numTicks = 3
	for i := 0; i < numTicks; i++ {
		ticks <- time.Now()
		<-pub.notify
	}
	cancel()
	<-done

	publishes := pub.snapshot()
	if len(publishes) != numTicks {
		t.Fatalf("publish calls = %d, want %d", len(publishes), numTicks)
	}

	probedAt := decodeAudioReport(t, publishes[0].payload).DiscoveredAt
	if probedAt == nil {
		t.Fatal("DiscoveredAt is nil, want the startup probe time")
	}

	var prevObservedAt *time.Time
	for i, call := range publishes {
		got := decodeAudioReport(t, call.payload)
		if got.DiscoveredAt == nil || !got.DiscoveredAt.Equal(*probedAt) {
			t.Errorf("tick %d DiscoveredAt = %v, want the pinned startup probe time %v", i, got.DiscoveredAt, probedAt)
		}
		if got.ObservedAt == nil {
			t.Fatalf("tick %d ObservedAt is nil", i)
		}
		if got.ObservedAt.Equal(*probedAt) {
			t.Errorf("tick %d ObservedAt = %v, want a fresh tick time distinct from the startup probe time %v", i, got.ObservedAt, probedAt)
		}
		if prevObservedAt != nil && !got.ObservedAt.After(*prevObservedAt) {
			t.Errorf("tick %d ObservedAt = %v, want it to advance past the previous tick's %v", i, got.ObservedAt, prevObservedAt)
		}
		prevObservedAt = got.ObservedAt
	}
}

// stubSnapshotter is a scriptable [audioSessionSnapshotter]: each call to
// Snapshot returns the next entry in results (repeating the last one once
// exhausted), so a test can prove session evidence is rebuilt per tick
// rather than cached like the hardware discovery half of the report.
type stubSnapshotter struct {
	results [][]audio.SessionSnapshot
	calls   int
}

func (s *stubSnapshotter) Snapshot(context.Context) []audio.SessionSnapshot {
	i := s.calls
	if i >= len(s.results) {
		i = len(s.results) - 1
	}
	s.calls++
	return s.results[i]
}

// TestRunAudioReportRebuildsSessionsEveryTick proves the report's session half of
// the report is NOT subject to finding 1's discovery cache: two ticks
// against a snapshotter returning different session state must produce
// two different published Sessions payloads, unlike the hardware
// discovery evidence, which TestRunAudioReportProbesOnceAcrossMultipleTicks
// proves stays identical across ticks.
func TestRunAudioReportRebuildsSessionsEveryTick(t *testing.T) {
	orig := audioDiscoverer
	audioDiscoverer = func(ctx context.Context, enum audio.Enumerator) audio.Discovery {
		return audio.Discovery{EngineUsable: true, HardwareEnumerated: true, HasHardwareCards: true}
	}
	t.Cleanup(func() { audioDiscoverer = orig })

	mgr := &stubSnapshotter{results: [][]audio.SessionSnapshot{
		{{ID: "s1", State: pkgaudio.StatePreparing, Fault: pkgaudio.FaultNone}},
		{{
			ID: "s1", State: pkgaudio.StatePlaying, Fault: pkgaudio.FaultPipelineCrash,
			FaultReason:   "engine: audio: pipeline crashed",
			PositionKnown: true, Position: 4200 * time.Millisecond, ObservedAt: time.Unix(9000, 0).UTC(),
		}},
	}}

	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAudioReport(ctx, pub, "audio-01", mgr, nil, time.Now, ticks, discardLogger())
	}()

	for i := 0; i < 2; i++ {
		ticks <- time.Now()
		<-pub.notify
	}
	cancel()
	<-done

	calls := pub.snapshot()
	if len(calls) != 2 {
		t.Fatalf("publish calls = %d, want 2", len(calls))
	}
	firstReport := decodeAudioReport(t, calls[0].payload)
	secondReport := decodeAudioReport(t, calls[1].payload)

	if len(firstReport.Sessions) != 1 || firstReport.Sessions[0].State != string(pkgaudio.StatePreparing) {
		t.Fatalf("first tick sessions = %+v, want one session in state %q", firstReport.Sessions, pkgaudio.StatePreparing)
	}
	if len(secondReport.Sessions) != 1 {
		t.Fatalf("second tick sessions = %+v, want 1", secondReport.Sessions)
	}
	got := secondReport.Sessions[0]
	if got.State != string(pkgaudio.StatePlaying) {
		t.Errorf("second tick state = %q, want %q (must not be the first tick's cached value)", got.State, pkgaudio.StatePlaying)
	}
	if got.Fault != string(pkgaudio.FaultPipelineCrash) || got.FaultReason == "" {
		t.Errorf("second tick fault = %q/%q, want %q with a non-empty reason", got.Fault, got.FaultReason, pkgaudio.FaultPipelineCrash)
	}
	if !got.PositionKnown || got.PositionMs != 4200 {
		t.Errorf("second tick position = known=%v ms=%d, want known=true ms=4200", got.PositionKnown, got.PositionMs)
	}
	if got.ObservedAt == nil || !got.ObservedAt.Equal(time.Unix(9000, 0).UTC()) {
		t.Errorf("second tick ObservedAt = %v, want the snapshot's own engine evidence time", got.ObservedAt)
	}

	firstSession := firstReport.Sessions[0]
	if firstSession.Fault != "none" {
		t.Errorf("first tick fault = %q, want %q (FaultNone renders as the literal string \"none\")", firstSession.Fault, "none")
	}
	if firstSession.PositionKnown {
		t.Error("first tick PositionKnown = true, want false (no engine evidence was ever supplied)")
	}
}
