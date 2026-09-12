package agent

import (
	"context"
	"strings"
	"sync"
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

// TestBuildAudioPayloadPipeWireReadFailureIsStatedNotSilent proves
// acceptance 5: a sinkBackend pipewiresink node whose PipeWire graph
// could not be read (pw-dump ran but returned garbage) states that in
// DeviceReason/ProgramReason rather than reading identically to a node
// that simply has no usable hardware at all.
func TestBuildAudioPayloadPipeWireReadFailureIsStatedNotSilent(t *testing.T) {
	d := audio.Discovery{
		EngineUsable: true, HardwareEnumerated: true, HasHardwareCards: false,
		PipeWireEnumeratedReason: "PipeWire graph enumeration failed: decoding pw-dump JSON: unexpected end of JSON input",
	}
	p := buildAudioPayload(d, time.Now())

	if p.DeviceAvailable {
		t.Fatal("DeviceAvailable = true, want false")
	}
	if !strings.Contains(p.DeviceReason, "PipeWire graph could not be read") {
		t.Errorf("DeviceReason = %q, want it to say the PipeWire graph could not be read", p.DeviceReason)
	}
	if !strings.Contains(p.DeviceReason, "unexpected end of JSON input") {
		t.Errorf("DeviceReason = %q, want the underlying read failure text carried through", p.DeviceReason)
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
		runAudioReport(ctx, pub, "audio-01", nil, nil, nil, time.Now, ticks, nil, discardLogger())
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
		runAudioReport(ctx, pub, "audio-01", nil, nil, nil, time.Now, ticks, nil, discardLogger())
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
		runAudioReport(ctx, pub, "audio-01", nil, gen, nil, time.Now, ticks, nil, discardLogger())
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
		runAudioReport(ctx, pub, "audio-01", nil, nil, nil, time.Now, ticks, nil, discardLogger())
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
		runAudioReport(ctx, pub, "audio-01", nil, nil, nil, now, ticks, nil, discardLogger())
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

	// nodeRestoreState and its companions script
	// NodeRestoreRetryStatus's return; left zero, it reports
	// EngineRestoreIdle/all-zero, matching audio.Manager's own
	// zero-value convention.
	nodeRestoreState         audio.EngineRestoreState
	nodeRestoreAttempts      int
	nodeRestoreNextAttemptAt time.Time
	nodeRestoreLastReason    string

	// timeline scripts TimelineSnapshot's return; left zero, it reports
	// a node running nothing scheduled, which is what every test here
	// that does not care about a schedule should see.
	timeline audio.TimelineSnapshot

	// settingsState and its companions script SettingsSubstitution's
	// return; left zero, it reports SettingsAccepted with no fields,
	// matching audio.Manager's own zero-value convention.
	settingsState  audio.SettingsState
	settingsFields []string
	settingsReason string

	// alignment scripts AlignmentSnapshot's return; left zero, it reports
	// not measured with a stub reason.
	alignment audio.AlignmentSnapshot
}

func (s *stubSnapshotter) TimelineSnapshot(context.Context) audio.TimelineSnapshot {
	if !s.timeline.Scheduled && s.timeline.Reason == "" {
		return audio.TimelineSnapshot{Reason: "stub: nothing scheduled"}
	}
	return s.timeline
}

func (s *stubSnapshotter) Snapshot(context.Context) []audio.SessionSnapshot {
	i := s.calls
	if i >= len(s.results) {
		i = len(s.results) - 1
	}
	s.calls++
	return s.results[i]
}

// NodeRestoreRetryStatus reports EngineRestoreIdle/all-zero unless a test
// overrides nodeRestoreState, matching a stubSnapshotter's default
// zero-value convention for everything it does not script.
func (s *stubSnapshotter) NodeRestoreRetryStatus(now time.Time) (audio.EngineRestoreState, int, time.Duration, string) {
	if s.nodeRestoreState == "" {
		return audio.EngineRestoreIdle, 0, 0, ""
	}
	next := time.Duration(0)
	if !s.nodeRestoreNextAttemptAt.IsZero() {
		if d := s.nodeRestoreNextAttemptAt.Sub(now); d > 0 {
			next = d
		}
	}
	return s.nodeRestoreState, s.nodeRestoreAttempts, next, s.nodeRestoreLastReason
}

// SettingsSubstitution reports SettingsAccepted/no fields unless a test
// overrides settingsState, matching a stubSnapshotter's default
// zero-value convention for everything it does not script.
func (s *stubSnapshotter) SettingsSubstitution() (audio.SettingsState, []string, string) {
	if s.settingsState == "" {
		return audio.SettingsAccepted, nil, ""
	}
	return s.settingsState, s.settingsFields, s.settingsReason
}

// AlignmentSnapshot reports s.alignment unless a test overrides it,
// matching a stubSnapshotter's default zero-value convention for
// everything it does not script.
func (s *stubSnapshotter) AlignmentSnapshot(context.Context) audio.AlignmentSnapshot {
	if !s.alignment.Measured && s.alignment.Reason == "" {
		return audio.AlignmentSnapshot{Reason: "stub: no alignment sample"}
	}
	return s.alignment
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
			LTCClaimState: audio.LTCClaimRefused, LTCClaimReason: "this node's one LTC run is held by session s0",
		}},
	}}

	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAudioReport(ctx, pub, "audio-01", mgr, nil, nil, time.Now, ticks, nil, discardLogger())
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
	if got.LTCClaimState != string(audio.LTCClaimRefused) || got.LTCClaimReason == "" {
		t.Errorf("second tick ltc claim = %q/%q, want %q with a non-empty reason", got.LTCClaimState, got.LTCClaimReason, audio.LTCClaimRefused)
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
	if firstSession.LTCClaimState != "none" {
		t.Errorf("first tick ltc claim state = %q, want %q (the zero value renders as the literal string \"none\")", firstSession.LTCClaimState, "none")
	}
	if firstSession.PositionKnown {
		t.Error("first tick PositionKnown = true, want false (no engine evidence was ever supplied)")
	}
}

// TestRunAudioReportPublishesFreshLTCFromRealManager proves the wiring
// agent.go actually performs — passing a real [*audio.Manager] as the
// report loop's ltcObserver, not a stub — carries fresh engine evidence
// through [audio.Manager.ObserveLTC] on every tick. The fake engine's LTC
// state is driven directly, standing in for what the session lifecycle
// would otherwise trigger; that lifecycle itself is exercised in
// internal/agent/audio's own tests.
func TestRunAudioReportPublishesFreshLTCFromRealManager(t *testing.T) {
	orig := audioDiscoverer
	audioDiscoverer = func(ctx context.Context, enum audio.Enumerator) audio.Discovery {
		return audio.Discovery{EngineUsable: true, HardwareEnumerated: true, HasHardwareCards: true}
	}
	t.Cleanup(func() { audioDiscoverer = orig })

	dir := t.TempDir()
	engine := audio.NewFakeEngine(time.Now)
	mgr := audio.NewManager(engine, audio.NewFileSessionStore(dir), dir, audio.RealDecoder{}, time.Now, nil)

	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAudioReport(ctx, pub, "audio-01", mgr, mgr, nil, time.Now, ticks, nil, discardLogger())
	}()

	if _, err := engine.StartLTC(ctx, audio.LTCSpec{FrameRate: pkgaudio.LTCFrameRate30, StartTimecode: "01:00:00:00"}); err != nil {
		t.Fatalf("StartLTC: %v", err)
	}
	engine.EmitLTCFrame()
	ticks <- time.Now()
	<-pub.notify

	if _, err := engine.StopLTC(ctx); err != nil {
		t.Fatalf("StopLTC: %v", err)
	}
	ticks <- time.Now()
	<-pub.notify

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
	if !first.LTCTimecodeKnown || first.LTCTimecode != "01:00:00:00" {
		t.Errorf("first tick = %+v, want TimecodeKnown 01:00:00:00", first)
	}
	if second.LTCGeneratorState != string(audio.LTCStopped) {
		t.Errorf("second tick LTCGeneratorState = %q, want stopped", second.LTCGeneratorState)
	}
	if second.LTCTimecodeKnown {
		t.Error("second tick reports TimecodeKnown, want false: the real Manager's engine evidence must be re-read, not cached from the first tick")
	}
}

// stubEngineAvailability is a scriptable [engineAvailability]: each call
// returns the next entry in results, mirroring stubLTCObserver's own
// shape, so a test can simulate the live engine going from available to
// broken between ticks.
type stubEngineAvailability struct {
	results []struct {
		ok     bool
		reason string
	}
	calls int
}

func (s *stubEngineAvailability) Available() (bool, string) {
	i := s.calls
	if i >= len(s.results) {
		i = len(s.results) - 1
	}
	s.calls++
	r := s.results[i]
	return r.ok, r.reason
}

// TestRunAudioReportEngineAvailableReflectsLiveEngineNotStartupDiscovery
// proves the published report's EngineAvailable must track the live
// playback engine on every tick, not the one-time
// startup discovery cache that seeds buildAudioPayload. Discovery here
// claims the engine is usable (as it would be at boot, before the
// engine ever broke); the live engine then reports itself broken, and
// the published report must say so rather than repeating the stale
// startup verdict.
func TestRunAudioReportEngineAvailableReflectsLiveEngineNotStartupDiscovery(t *testing.T) {
	orig := audioDiscoverer
	audioDiscoverer = func(ctx context.Context, enum audio.Enumerator) audio.Discovery {
		return audio.Discovery{EngineUsable: true, HardwareEnumerated: true, HasHardwareCards: true, Routes: []audio.RouteEvidence{
			{Device: "hw:CARD=X,DEV=0", ProbeResult: audio.ProbeResult{Available: true, Channels: 2, Rate: 44100, Format: "S16LE"}},
		}}
	}
	t.Cleanup(func() { audioDiscoverer = orig })

	engine := &stubEngineAvailability{results: []struct {
		ok     bool
		reason string
	}{{ok: false, reason: "output pipeline reported a fatal sink error"}}}

	pub := newFakePublisher()
	ticks := make(chan time.Time, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAudioReport(ctx, pub, "audio-01", nil, nil, engine, time.Now, ticks, nil, discardLogger())
	}()

	ticks <- time.Now()
	<-pub.notify

	cancel()
	<-done

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("publish calls = %d, want 1", len(calls))
	}
	got := decodeAudioReport(t, calls[0].payload)
	if got.EngineAvailable {
		t.Error("EngineAvailable = true, want false: the live engine reports broken, and the report must not repeat the startup discovery cache's healthy verdict")
	}
	if got.EngineReason != "output pipeline reported a fatal sink error" {
		t.Errorf("EngineReason = %q, want the live engine's own reason", got.EngineReason)
	}
}

// stubEngineGlitchCounts is a [stubEngineAvailability] that also
// implements the [engineGlitchCounts] optional interface with a fixed,
// distinguishable count.
type stubEngineGlitchCounts struct {
	stubEngineAvailability
	counts audio.GlitchCounts
	known  bool
}

func (s *stubEngineGlitchCounts) GlitchCounts() (audio.GlitchCounts, bool) {
	return s.counts, s.known
}

// TestRunAudioReportPublishesGlitchCountsWhenEngineCollectsThem proves an
// engine that implements [engineGlitchCounts] has its counts land on the
// published report: the operator-visible evidence a bus warning/QoS/xrun
// message was previously discarded into.
func TestRunAudioReportPublishesGlitchCountsWhenEngineCollectsThem(t *testing.T) {
	orig := audioDiscoverer
	audioDiscoverer = func(ctx context.Context, enum audio.Enumerator) audio.Discovery {
		return audio.Discovery{EngineUsable: true, HardwareEnumerated: true, HasHardwareCards: true, Routes: []audio.RouteEvidence{
			{Device: "hw:CARD=X,DEV=0", ProbeResult: audio.ProbeResult{Available: true, Channels: 2, Rate: 44100, Format: "S16LE"}},
		}}
	}
	t.Cleanup(func() { audioDiscoverer = orig })

	engine := &stubEngineGlitchCounts{
		stubEngineAvailability: stubEngineAvailability{results: []struct {
			ok     bool
			reason string
		}{{ok: true, reason: ""}}},
		counts: audio.GlitchCounts{Since: time.Unix(500, 0), StreamWarnings: 4, ResourceWarnings: 2, OtherWarnings: 1, QosEvents: 9},
		known:  true,
	}

	pub := newFakePublisher()
	ticks := make(chan time.Time, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAudioReport(ctx, pub, "audio-01", nil, nil, engine, time.Now, ticks, nil, discardLogger())
	}()

	ticks <- time.Now()
	<-pub.notify
	cancel()
	<-done

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("publish calls = %d, want 1", len(calls))
	}
	got := decodeAudioReport(t, calls[0].payload)
	if !got.EngineGlitchCountsKnown {
		t.Fatal("EngineGlitchCountsKnown = false, want true: the wired engine implements engineGlitchCounts and reported known=true")
	}
	if got.EngineStreamWarningCount != 4 || got.EngineResourceWarningCount != 2 || got.EngineOtherWarningCount != 1 || got.EngineQosDropCount != 9 {
		t.Errorf("stream/resource/other/qos counts = %d/%d/%d/%d, want 4/2/1/9",
			got.EngineStreamWarningCount, got.EngineResourceWarningCount, got.EngineOtherWarningCount, got.EngineQosDropCount)
	}
	if got.EngineGlitchCountsSince == nil || !got.EngineGlitchCountsSince.Equal(time.Unix(500, 0)) {
		t.Errorf("EngineGlitchCountsSince = %v, want %v", got.EngineGlitchCountsSince, time.Unix(500, 0))
	}
}

// TestRunAudioReportLeavesGlitchCountsUnknownWhenEngineCannotCollectThem
// proves an engine that only implements [engineAvailability] (not
// [engineGlitchCounts]) -- e.g. gstengine's non-cgo stub, or any bound
// engine that never observed a bus -- leaves EngineGlitchCountsKnown
// false and both counts at zero, never a fabricated healthy zero: the
// exact distinction between "counted, and zero" and "not collected".
func TestRunAudioReportLeavesGlitchCountsUnknownWhenEngineCannotCollectThem(t *testing.T) {
	orig := audioDiscoverer
	audioDiscoverer = func(ctx context.Context, enum audio.Enumerator) audio.Discovery {
		return audio.Discovery{EngineUsable: true, HardwareEnumerated: true, HasHardwareCards: true, Routes: []audio.RouteEvidence{
			{Device: "hw:CARD=X,DEV=0", ProbeResult: audio.ProbeResult{Available: true, Channels: 2, Rate: 44100, Format: "S16LE"}},
		}}
	}
	t.Cleanup(func() { audioDiscoverer = orig })

	engine := &stubEngineAvailability{results: []struct {
		ok     bool
		reason string
	}{{ok: true, reason: ""}}}

	pub := newFakePublisher()
	ticks := make(chan time.Time, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAudioReport(ctx, pub, "audio-01", nil, nil, engine, time.Now, ticks, nil, discardLogger())
	}()

	ticks <- time.Now()
	<-pub.notify
	cancel()
	<-done

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("publish calls = %d, want 1", len(calls))
	}
	got := decodeAudioReport(t, calls[0].payload)
	if got.EngineGlitchCountsKnown {
		t.Fatal("EngineGlitchCountsKnown = true for an engine that does not implement engineGlitchCounts, want false")
	}
	if got.EngineStreamWarningCount != 0 || got.EngineResourceWarningCount != 0 || got.EngineOtherWarningCount != 0 || got.EngineQosDropCount != 0 {
		t.Errorf("stream/resource/other/qos counts = %d/%d/%d/%d, want 0/0/0/0 when not collected",
			got.EngineStreamWarningCount, got.EngineResourceWarningCount, got.EngineOtherWarningCount, got.EngineQosDropCount)
	}
	if got.EngineGlitchCountsSince != nil {
		t.Errorf("EngineGlitchCountsSince = %v, want nil when not collected", got.EngineGlitchCountsSince)
	}
}

// TestRunAudioReportPublishesNodeLevelRestoreStatus proves
// applyEngineRestoreStatus carries mgr's node-level automatic
// restore-retry status onto the published report's four
// EngineRestore* fields, live from [audioSessionSnapshotter.
// NodeRestoreRetryStatus] on every tick -- the wire evidence this whole
// issue exists to add, independent of Sessions (empty here) or of
// anything RestorePending-gated one resource kind down.
func TestRunAudioReportPublishesNodeLevelRestoreStatus(t *testing.T) {
	orig := audioDiscoverer
	audioDiscoverer = func(ctx context.Context, enum audio.Enumerator) audio.Discovery {
		return audio.Discovery{EngineUsable: true, HardwareEnumerated: true, HasHardwareCards: true}
	}
	t.Cleanup(func() { audioDiscoverer = orig })

	mgr := &stubSnapshotter{
		results:                  [][]audio.SessionSnapshot{{}},
		nodeRestoreState:         audio.EngineRestoreScheduled,
		nodeRestoreAttempts:      3,
		nodeRestoreNextAttemptAt: time.Unix(1_700_000_040, 0),
		nodeRestoreLastReason:    "engine build refused: no advertised probe evidence for hw:1,0",
	}

	pub := newFakePublisher()
	ticks := make(chan time.Time, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAudioReport(ctx, pub, "audio-01", mgr, nil, nil, func() time.Time { return time.Unix(1_700_000_000, 0) }, ticks, nil, discardLogger())
	}()

	ticks <- time.Unix(1_700_000_000, 0)
	<-pub.notify
	cancel()
	<-done

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("publish calls = %d, want 1", len(calls))
	}
	got := decodeAudioReport(t, calls[0].payload)
	if got.EngineRestoreState != "scheduled" {
		t.Errorf("EngineRestoreState = %q, want %q", got.EngineRestoreState, "scheduled")
	}
	if got.EngineRestoreAttempts != 3 {
		t.Errorf("EngineRestoreAttempts = %d, want 3", got.EngineRestoreAttempts)
	}
	if got.EngineRestoreNextAttemptMs != 40_000 {
		t.Errorf("EngineRestoreNextAttemptMs = %d, want 40000", got.EngineRestoreNextAttemptMs)
	}
	if got.EngineRestoreLastReason != "engine build refused: no advertised probe evidence for hw:1,0" {
		t.Errorf("EngineRestoreLastReason = %q, want the scripted reason preserved", got.EngineRestoreLastReason)
	}
}

// TestRunAudioReportNilSnapshotterReportsIdleRestoreStatus proves a node
// with no asset directory configured (mgr nil, matching this loop's
// other nil-safe optional sources) still publishes a valid report: every
// EngineRestore* field at its zero value, which reads as
// [audio.EngineRestoreIdle] on the wire -- never omitted, never a
// fabricated "exhausted".
func TestRunAudioReportNilSnapshotterReportsIdleRestoreStatus(t *testing.T) {
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
		runAudioReport(ctx, pub, "audio-01", nil, nil, nil, time.Now, ticks, nil, discardLogger())
	}()

	ticks <- time.Now()
	<-pub.notify
	cancel()
	<-done

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("publish calls = %d, want 1", len(calls))
	}
	got := decodeAudioReport(t, calls[0].payload)
	if got.EngineRestoreState != "" {
		t.Errorf("EngineRestoreState with a nil snapshotter = %q, want \"\" (reads as idle on the wire)", got.EngineRestoreState)
	}
	if got.EngineRestoreAttempts != 0 || got.EngineRestoreNextAttemptMs != 0 || got.EngineRestoreLastReason != "" {
		t.Errorf("EngineRestore attempts/next/reason with a nil snapshotter = %d/%d/%q, want 0/0/\"\"",
			got.EngineRestoreAttempts, got.EngineRestoreNextAttemptMs, got.EngineRestoreLastReason)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("Validate() with all engineRestore* fields omitted = %v, want nil", err)
	}
}

// TestRunAudioReportPublishesSettingsSubstitutionStatus proves
// applySettingsStatus carries mgr's live settings-substitution status onto
// the published report's three Settings* fields, naming the substituted
// field and stating the reason -- the wire evidence this whole issue
// exists to add.
func TestRunAudioReportPublishesSettingsSubstitutionStatus(t *testing.T) {
	orig := audioDiscoverer
	audioDiscoverer = func(ctx context.Context, enum audio.Enumerator) audio.Discovery {
		return audio.Discovery{EngineUsable: true, HardwareEnumerated: true, HasHardwareCards: true}
	}
	t.Cleanup(func() { audioDiscoverer = orig })

	mgr := &stubSnapshotter{
		results:        [][]audio.SessionSnapshot{{}},
		settingsState:  audio.SettingsSubstituted,
		settingsFields: []string{"DefaultFadeDurationMs"},
		settingsReason: "DefaultFadeDurationMs 0 is not positive",
	}

	pub := newFakePublisher()
	ticks := make(chan time.Time, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAudioReport(ctx, pub, "audio-01", mgr, nil, nil, time.Now, ticks, nil, discardLogger())
	}()

	ticks <- time.Now()
	<-pub.notify
	cancel()
	<-done

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("publish calls = %d, want 1", len(calls))
	}
	got := decodeAudioReport(t, calls[0].payload)
	if got.SettingsState != "substituted" {
		t.Errorf("SettingsState = %q, want %q", got.SettingsState, "substituted")
	}
	if len(got.SettingsSubstitutedFields) != 1 || got.SettingsSubstitutedFields[0] != "DefaultFadeDurationMs" {
		t.Errorf("SettingsSubstitutedFields = %v, want exactly [DefaultFadeDurationMs]", got.SettingsSubstitutedFields)
	}
	if got.SettingsReason != "DefaultFadeDurationMs 0 is not positive" {
		t.Errorf("SettingsReason = %q, want the scripted reason preserved", got.SettingsReason)
	}
}

// TestRunAudioReportNilSnapshotterReportsAcceptedSettingsStatus proves a
// node with no asset directory configured (mgr nil, matching this loop's
// other nil-safe optional sources) still publishes a valid report: every
// Settings* field at its zero value, which reads as [audio.
// SettingsAccepted] on the wire -- never omitted, never a fabricated
// "substituted".
func TestRunAudioReportNilSnapshotterReportsAcceptedSettingsStatus(t *testing.T) {
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
		runAudioReport(ctx, pub, "audio-01", nil, nil, nil, time.Now, ticks, nil, discardLogger())
	}()

	ticks <- time.Now()
	<-pub.notify
	cancel()
	<-done

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("publish calls = %d, want 1", len(calls))
	}
	got := decodeAudioReport(t, calls[0].payload)
	if got.SettingsState != "" {
		t.Errorf("SettingsState with a nil snapshotter = %q, want \"\" (reads as accepted on the wire)", got.SettingsState)
	}
	if len(got.SettingsSubstitutedFields) != 0 || got.SettingsReason != "" {
		t.Errorf("SettingsSubstitutedFields/SettingsReason with a nil snapshotter = %v/%q, want none/\"\"",
			got.SettingsSubstitutedFields, got.SettingsReason)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("Validate() with all settings* fields omitted = %v, want nil", err)
	}
}

// hangingEngineAvailability is an [engineAvailability] whose Available()
// blocks until release is closed, simulating a future backend whose
// Available() call is not the fast, lock-only read the real
// [gstengine.Engine.Available] and [audio.SwitchableEngine.Available]
// are today (see those types' own doc comments: SwitchableEngine never
// holds its mutex across a delegated call, and gstengine.Engine.Available
// reads only availOK/brokenReason, never the pipeline itself, so neither
// can be held hostage by a wedged Start/Load). This type exists only to
// prove what runAudioReport does IF that invariant were ever broken:
// applyEngineAvailability calling Available() synchronously in the tick
// loop must freeze the WHOLE tick, never publish a fresh-looking
// timestamp for a value it could not actually re-derive.
type hangingEngineAvailability struct {
	mu      sync.Mutex
	hang    bool
	release chan struct{}
}

func newHangingEngineAvailability() *hangingEngineAvailability {
	return &hangingEngineAvailability{release: make(chan struct{})}
}

func (h *hangingEngineAvailability) armHang() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hang = true
}

func (h *hangingEngineAvailability) releaseHang() {
	h.mu.Lock()
	defer h.mu.Unlock()
	select {
	case <-h.release:
	default:
		close(h.release)
	}
}

func (h *hangingEngineAvailability) Available() (bool, string) {
	h.mu.Lock()
	hang := h.hang
	release := h.release
	h.mu.Unlock()
	if hang {
		<-release
	}
	return true, ""
}

// TestRunAudioReportWedgedEngineAvailableFreezesTheTickInsteadOfPublishingFalseFreshness
// is a wedged-engine interaction test: stamping engine.state/engine.reason
// with the report tick's own live time (this fix) must never let a
// genuinely wedged engine check publish a fresh-looking timestamp for
// evidence it did not actually re-derive. applyEngineAvailability calls
// Available() synchronously inside runAudioReport's tick loop, so a
// wedged Available() call blocks the ENTIRE tick, including the publish
// that would otherwise carry a new ObservedAt: no report reaches the
// coordinator for that tick at all. The coordinator then ages the last
// genuinely published report past DefaultValidFor and reports it stale
// (see nodeaudio.TestPollEngineStateGoesStaleWhenTheNodeGenuinelyStopsReporting),
// exactly the PR #83 last-known-evidence-plus-stale behavior, never a
// fabricated current reading.
func TestRunAudioReportWedgedEngineAvailableFreezesTheTickInsteadOfPublishingFalseFreshness(t *testing.T) {
	orig := audioDiscoverer
	audioDiscoverer = func(ctx context.Context, enum audio.Enumerator) audio.Discovery {
		return audio.Discovery{EngineUsable: true, HardwareEnumerated: true, HasHardwareCards: true}
	}
	t.Cleanup(func() { audioDiscoverer = orig })

	engine := newHangingEngineAvailability()
	t.Cleanup(engine.releaseHang)

	pub := newFakePublisher()
	ticks := make(chan time.Time, 2)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAudioReport(ctx, pub, "audio-01", nil, nil, engine, time.Now, ticks, nil, discardLogger())
	}()

	// First tick: engine responsive, publishes normally.
	ticks <- time.Now()
	<-pub.notify
	if got := len(pub.snapshot()); got != 1 {
		t.Fatalf("publish calls after tick 1 = %d, want 1", got)
	}

	// Second tick: Available() wedges. The tick must not complete, so no
	// second publish -- and in particular no publish carrying a fresh
	// ObservedAt for evidence Available() never actually returned.
	engine.armHang()
	ticks <- time.Now()

	select {
	case <-pub.notify:
		t.Fatal("a wedged engine.Available() call still let the tick publish; it must freeze the whole tick instead of reporting false freshness")
	case <-time.After(200 * time.Millisecond):
		// Expected: the tick is genuinely stuck, exactly like a wedged
		// GStreamer call must be.
	}
	if got := len(pub.snapshot()); got != 1 {
		t.Fatalf("publish calls while wedged = %d, want still 1 (no new publish)", got)
	}

	// Release the wedge: the stuck tick completes and the loop can shut
	// down cleanly, proving this is a genuine freeze, not a deadlock this
	// test would otherwise hang forever on.
	engine.releaseHang()
	<-pub.notify
	cancel()
	<-done

	if got := len(pub.snapshot()); got != 2 {
		t.Fatalf("publish calls after releasing the wedge = %d, want 2", got)
	}
}

func TestDecideAudioReportIntervalStaysNormalWhenNoFadeIsActive(t *testing.T) {
	var state audioReportCadenceState
	now := time.Unix(1_700_000_000, 0)
	got := decideAudioReportInterval(&state, false, now, 5*time.Second, 500*time.Millisecond, 10*time.Second)
	if got != 5*time.Second {
		t.Fatalf("interval with no fade active = %v, want the normal 5s interval", got)
	}
}

func TestDecideAudioReportIntervalElevatesTheTickAFadeFirstReportsActive(t *testing.T) {
	var state audioReportCadenceState
	now := time.Unix(1_700_000_000, 0)
	got := decideAudioReportInterval(&state, true, now, 5*time.Second, 500*time.Millisecond, 10*time.Second)
	if got != 500*time.Millisecond {
		t.Fatalf("interval on the tick a fade first reports active = %v, want the elevated 500ms interval", got)
	}
}

func TestDecideAudioReportIntervalStaysElevatedWhileWithinTheCap(t *testing.T) {
	var state audioReportCadenceState
	start := time.Unix(1_700_000_000, 0)
	decideAudioReportInterval(&state, true, start, 5*time.Second, 500*time.Millisecond, 10*time.Second)
	got := decideAudioReportInterval(&state, true, start.Add(9*time.Second), 5*time.Second, 500*time.Millisecond, 10*time.Second)
	if got != 500*time.Millisecond {
		t.Fatalf("interval 9s into a still-active fade (10s cap) = %v, want still elevated", got)
	}
}

// mutation target: the cap comparison in decideAudioReportInterval. This
// is the case that matters most in this issue's lane: another builder is
// separately investigating a fade whose own FadeState can be left stuck
// in_progress by a stop that interrupts it, never reporting completion.
// If this node's report cadence keyed only on "a fade is active" with no
// independent bound, a single such stuck session would report fast for
// the rest of the process's life. This proves the cap alone ends the
// elevation once it elapses, with no dependence on FadeState ever
// clearing on its own.
func TestDecideAudioReportIntervalCapEndsElevationWhenFadeStateNeverReportsComplete(t *testing.T) {
	var state audioReportCadenceState
	start := time.Unix(1_700_000_000, 0)
	decideAudioReportInterval(&state, true, start, 5*time.Second, 500*time.Millisecond, 10*time.Second)
	got := decideAudioReportInterval(&state, true, start.Add(10*time.Second), 5*time.Second, 500*time.Millisecond, 10*time.Second)
	if got != 5*time.Second {
		t.Fatalf("interval once the elevation cap elapses with fadeActive still true = %v, want back to the normal 5s interval", got)
	}
}

// mutation target: a capped elevation re-arming itself on the very next
// tick merely because the same stuck fade is still reported active,
// which would make the node alternate between elevated and normal
// forever instead of settling at normal -- the flapping the cap alone,
// without this latch, would produce.
func TestDecideAudioReportIntervalDoesNotReElevateOnTheTickAfterCappingWhileStillStuck(t *testing.T) {
	var state audioReportCadenceState
	start := time.Unix(1_700_000_000, 0)
	decideAudioReportInterval(&state, true, start, 5*time.Second, 500*time.Millisecond, 10*time.Second)
	decideAudioReportInterval(&state, true, start.Add(10*time.Second), 5*time.Second, 500*time.Millisecond, 10*time.Second)
	got := decideAudioReportInterval(&state, true, start.Add(10500*time.Millisecond), 5*time.Second, 500*time.Millisecond, 10*time.Second)
	if got != 5*time.Second {
		t.Fatalf("interval one tick after capping, fade still reported active = %v, want normal (no re-elevation until fadeActive genuinely reports false)", got)
	}
}

func TestDecideAudioReportIntervalReArmsAfterCapOnceFadeStateGenuinelyClears(t *testing.T) {
	var state audioReportCadenceState
	start := time.Unix(1_700_000_000, 0)
	decideAudioReportInterval(&state, true, start, 5*time.Second, 500*time.Millisecond, 10*time.Second)
	decideAudioReportInterval(&state, true, start.Add(10*time.Second), 5*time.Second, 500*time.Millisecond, 10*time.Second)
	decideAudioReportInterval(&state, false, start.Add(15*time.Second), 5*time.Second, 500*time.Millisecond, 10*time.Second)
	got := decideAudioReportInterval(&state, true, start.Add(20*time.Second), 5*time.Second, 500*time.Millisecond, 10*time.Second)
	if got != 500*time.Millisecond {
		t.Fatalf("interval on a genuinely new fade after a prior one capped and then cleared = %v, want elevated again", got)
	}
}

func TestAnyFadeInProgressTrueOnlyWhenASessionReportsFadeInProgress(t *testing.T) {
	cases := []struct {
		name  string
		snaps []audio.SessionSnapshot
		want  bool
	}{
		{"no sessions", nil, false},
		{"one session none", []audio.SessionSnapshot{{FadeState: audio.FadeStateNone}}, false},
		{"one session complete", []audio.SessionSnapshot{{FadeState: audio.FadeStateComplete}}, false},
		{"one session in progress", []audio.SessionSnapshot{{FadeState: audio.FadeStateInProgress}}, true},
		{"second of two in progress", []audio.SessionSnapshot{{FadeState: audio.FadeStateNone}, {FadeState: audio.FadeStateInProgress}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := anyFadeInProgress(tc.snaps); got != tc.want {
				t.Fatalf("anyFadeInProgress(...) = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRunAudioReportTickerElevatesCadenceWhileAFadeIsActive(t *testing.T) {
	mgr := &stubSnapshotter{results: [][]audio.SessionSnapshot{
		{{FadeState: audio.FadeStateInProgress}},
	}}
	out := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAudioReportTicker(ctx, mgr, 400*time.Millisecond, 40*time.Millisecond, time.Second, time.Now, out)
	}()

	start := time.Now()
	<-out
	firstGap := time.Since(start)
	if firstGap < 300*time.Millisecond {
		t.Fatalf("first tick arrived after %v, want close to the normal 400ms interval (must not already be elevated)", firstGap)
	}

	afterFirst := time.Now()
	<-out
	secondGap := time.Since(afterFirst)
	if secondGap > 250*time.Millisecond {
		t.Fatalf("second tick arrived after %v once a fade was reported active, want close to the elevated 40ms interval", secondGap)
	}

	cancel()
	<-done
}

// Constraint: a node that cannot actually speed up (here, an
// elevatedInterval no shorter than normalInterval) must keep reporting
// at its normal cadence rather than failing -- never handed to
// time.Ticker.Reset, which this would not even make invalid, just
// pointless or, for a shorter normalInterval than elevatedInterval,
// backwards.
func TestRunAudioReportTickerStaysNormalWhenElevatedIntervalCannotSpeedAnythingUp(t *testing.T) {
	mgr := &stubSnapshotter{results: [][]audio.SessionSnapshot{
		{{FadeState: audio.FadeStateInProgress}},
	}}
	out := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAudioReportTicker(ctx, mgr, 120*time.Millisecond, 120*time.Millisecond, time.Second, time.Now, out)
	}()

	start := time.Now()
	<-out
	<-out
	if d := time.Since(start); d < 200*time.Millisecond {
		t.Fatalf("two ticks arrived within %v, want each spaced close to the normal 120ms interval (elevation must not apply)", d)
	}

	cancel()
	<-done
}

// A nil snapshotter (no asset directory configured on this node, the
// same nil convention every other live-evidence source in this file
// already honors) must not panic this loop; it simply has nothing to
// elevate for.
func TestRunAudioReportTickerToleratesNilSnapshotter(t *testing.T) {
	out := make(chan time.Time, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAudioReportTicker(ctx, nil, 50*time.Millisecond, 10*time.Millisecond, time.Second, time.Now, out)
	}()
	<-out
	cancel()
	<-done
}

// TestRunAudioReportPublishesOnTriggerOutOfCadence proves a signal on
// triggered produces an immediate publish without waiting for ticks --
// the audio-report counterpart to runRenderReport's identical trigger
// behaviour, and the mechanism command.go's audio.gain.fade dispatch
// relies on.
func TestRunAudioReportPublishesOnTriggerOutOfCadence(t *testing.T) {
	orig := audioDiscoverer
	audioDiscoverer = func(ctx context.Context, enum audio.Enumerator) audio.Discovery {
		return audio.Discovery{EngineUsable: true, HardwareEnumerated: true, HasHardwareCards: true}
	}
	t.Cleanup(func() { audioDiscoverer = orig })

	pub := newFakePublisher()
	ticks := make(chan time.Time)
	triggered := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAudioReport(ctx, pub, "audio-01", nil, nil, nil, time.Now, ticks, triggered, discardLogger())
	}()

	select {
	case triggered <- struct{}{}:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out sending trigger")
	}
	select {
	case <-pub.notify:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for publish")
	}

	cancel()
	<-done

	if len(pub.snapshot()) != 1 {
		t.Fatalf("publish calls = %d, want 1 (the trigger alone, no tick was ever sent)", len(pub.snapshot()))
	}
}

// TestHandleMessageAudioReportTriggerSignalsOnlyForGainFade proves
// command.go's own gate: an unrelated allowlisted action (agent.echo)
// must not fire audioReportTrigger, and a genuinely-dispatched
// audio.gain.fade must -- the wiring that makes a fade's very first
// report immediate rather than waiting for whichever resting tick
// happens to land inside a two-second fade.
func TestHandleMessageAudioReportTriggerSignalsOnlyForGainFade(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Now()}
	mgr, _ := newTestAudioManager(t, dir, clock)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeAudioClaimTestAsset(t, dir, "a.wav", "asset-1", []byte("pretend this is wav audio"))
	if r := mgr.Apply(ctx, id, "apply-1", 1, pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleBackground),
		Media:      pkgaudio.SetField(ref),
	}); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply refused: %+v", r)
	}
	if r := mgr.Start(ctx, id, "start-1", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start refused: %+v", r)
	}

	trigger := make(chan struct{}, 1)
	h := newCommandHandler(testNodeID, dir, "", nil, nil, nil, mgr, trigger, nil, nil, nil, nil, clock.now, discardLogger())
	pub := newFakePublisher()

	echoCmd := baseEchoCmd("cmd-1", "idem-1")
	topic, payload := buildCmdMessage(t, clock, echoCmd)
	h.HandleMessage(context.Background(), pub, topic, payload)

	select {
	case <-trigger:
		t.Fatal("audioReportTrigger fired for a non-fade action")
	default:
	}

	fadeCmd := mqttproto.CmdPayload{
		CommandID:      "cmd-2",
		IdempotencyKey: "idem-2",
		Action:         string(pkgaudio.OperationGainFade),
		Target:         mqttproto.CmdTarget{Kind: "node", ID: testNodeID},
		Params: map[string]any{
			"sessionId": string(id), "invocationId": "inv-fade", "revision": 3,
			"targetGain": 0.4, "durationMs": 2000,
		},
		Issuer:             mqttproto.CmdIssuer{PrincipalID: "principal-1"},
		ConfirmationMethod: confirmationMethodEvidence,
	}
	topic2, payload2 := buildCmdMessage(t, clock, fadeCmd)
	beforeFade := len(pub.snapshot())
	h.HandleMessage(context.Background(), pub, topic2, payload2)

	calls := pub.snapshot()
	if len(calls) != beforeFade+1 {
		t.Fatalf("publish calls after the fade dispatch = %d, want %d (exactly one more, the fade's own result)", len(calls), beforeFade+1)
	}
	result := decodeResultFromCall(t, calls[beforeFade])
	if result.Outcome == mqttproto.OutcomeRefused {
		t.Fatalf("audio.gain.fade refused: %+v", result)
	}

	select {
	case <-trigger:
	default:
		t.Fatal("audioReportTrigger did not fire for a dispatched audio.gain.fade")
	}
}

// TestRunAudioReportReportsIntermediateGainAcrossADispatchedFade is this
// change's acceptance test: dispatching a fade produces a report without
// waiting for the resting tick (the trigger fires immediately on
// dispatch, before any tick is ever sent), and the gain values a two-
// second fade reports across that report plus two later ticks are
// intermediate, never a single step straight to the target.
//
// This drives a real [*audio.Manager] against [audio.FakeEngine], whose
// Observe ramps gain by linear interpolation over elapsed clock time --
// a deterministic stand-in proven against mix_test.go's own fade
// coverage, not a real playback engine. It demonstrates that THIS
// report path surfaces an in-flight ramp already computed elsewhere; it
// does not, and cannot, demonstrate a real engine's own fade completing
// early or drifting from the requested curve.
func TestRunAudioReportReportsIntermediateGainAcrossADispatchedFade(t *testing.T) {
	orig := audioDiscoverer
	audioDiscoverer = func(ctx context.Context, enum audio.Enumerator) audio.Discovery {
		return audio.Discovery{EngineUsable: true, HardwareEnumerated: true, HasHardwareCards: true}
	}
	t.Cleanup(func() { audioDiscoverer = orig })

	dir := t.TempDir()
	clock := &fakeClock{t: time.Now()}
	mgr, _ := newTestAudioManager(t, dir, clock)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeAudioClaimTestAsset(t, dir, "a.wav", "asset-1", []byte("pretend this is wav audio"))
	if r := mgr.Apply(ctx, id, "apply-1", 1, pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleBackground),
		Media:      pkgaudio.SetField(ref),
	}); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply refused: %+v", r)
	}
	if r := mgr.Start(ctx, id, "start-1", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start refused: %+v", r)
	}

	pub := newFakePublisher()
	ticks := make(chan time.Time)
	triggered := make(chan struct{})
	ctx2, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAudioReport(ctx2, pub, testNodeID, mgr, nil, nil, clock.now, ticks, triggered, discardLogger())
	}()

	const target = pkgaudio.Gain(0.4)
	if r := mgr.GainFade(ctx, id, "inv-fade", 3, pkgaudio.FadeCurveLinear, 2*time.Second, target); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("gain fade refused: %+v", r)
	}

	// Simulate command.go's own non-blocking send immediately after
	// dispatch -- see HandleMessage's audioReportTrigger case.
	select {
	case triggered <- struct{}{}:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out sending trigger")
	}
	<-pub.notify

	clock.advance(time.Second)
	ticks <- clock.now()
	<-pub.notify

	clock.advance(1100 * time.Millisecond)
	ticks <- clock.now()
	<-pub.notify

	cancel()
	<-done

	calls := pub.snapshot()
	if len(calls) != 3 {
		t.Fatalf("publish calls = %d, want 3 (one from the trigger, two from ticks)", len(calls))
	}

	session := func(i int) mqttproto.AudioSessionReport {
		r := decodeAudioReport(t, calls[i].payload)
		if len(r.Sessions) != 1 {
			t.Fatalf("report %d sessions = %+v, want exactly 1", i, r.Sessions)
		}
		return r.Sessions[0]
	}

	immediately := session(0)
	if immediately.FadeState != string(audio.FadeStateInProgress) {
		t.Fatalf("report from the trigger alone: FadeState = %q, want %q -- the fade must be visible on the very first out-of-cadence report",
			immediately.FadeState, audio.FadeStateInProgress)
	}
	if immediately.Gain == float64(target) {
		t.Fatalf("report from the trigger alone: Gain already = %v, the dispatched target -- the engine has not ramped anywhere yet", immediately.Gain)
	}

	halfway := session(1)
	if halfway.Gain == float64(target) || halfway.Gain == 1 {
		t.Fatalf("halfway through the fade: Gain = %v, want a value strictly between the starting gain (1) and the target (%v)", halfway.Gain, target)
	}

	after := session(2)
	if after.Gain != float64(target) {
		t.Fatalf("once the 2s fade's duration has elapsed: Gain = %v, want the dispatched target %v", after.Gain, target)
	}
}

// stubEngineBackendInfo is a [stubEngineAvailability] that also
// implements the [engineBackendInfo] optional interface, matching
// stubEngineGlitchCounts's identical shape.
type stubEngineBackendInfo struct {
	stubEngineAvailability
	sinkBackend string
	sinkTarget  string
	clockSource string
	clockReason string
}

func (s *stubEngineBackendInfo) SinkBackend() string { return s.sinkBackend }
func (s *stubEngineBackendInfo) SinkTarget() string  { return s.sinkTarget }
func (s *stubEngineBackendInfo) ClockSource() (string, string) {
	return s.clockSource, s.clockReason
}

// TestApplyEngineBackendInfoNilEngineLeavesFieldsBlank proves a nil
// engine (no asset directory configured on this node) never fabricates a
// backend or clock source.
func TestApplyEngineBackendInfoNilEngineLeavesFieldsBlank(t *testing.T) {
	payload := mqttproto.AudioPayload{EngineSinkBackend: "stale", EngineSinkTarget: "stale", EngineClockSource: "stale", EngineClockReason: "stale"}
	applyEngineBackendInfo(&payload, nil)
	if payload.EngineSinkBackend != "" || payload.EngineSinkTarget != "" || payload.EngineClockSource != "" || payload.EngineClockReason != "" {
		t.Fatalf("applyEngineBackendInfo with a nil engine left stale values: %+v", payload)
	}
}

// TestApplyEngineBackendInfoEngineWithoutTheOptionalInterfaceLeavesFieldsBlank
// proves an engine that does not implement [engineBackendInfo] (a test
// double, or an older build) reports blank rather than a fabricated
// value -- matching applyEngineGlitchCounts's identical rule for engines
// that do not implement its own optional interface.
func TestApplyEngineBackendInfoEngineWithoutTheOptionalInterfaceLeavesFieldsBlank(t *testing.T) {
	engine := &stubEngineAvailability{results: []struct {
		ok     bool
		reason string
	}{{ok: true}}}
	payload := mqttproto.AudioPayload{EngineSinkBackend: "stale"}
	applyEngineBackendInfo(&payload, engine)
	if payload.EngineSinkBackend != "" {
		t.Fatalf("EngineSinkBackend = %q, want \"\" for an engine with no SinkBackend method", payload.EngineSinkBackend)
	}
}

// TestApplyEngineBackendInfoReportsWhatTheEngineBuiltWith proves the
// live values reach the payload unchanged, fresh on every call -- the
// same "live, never cached" rule every other applyEngine* function in
// this file follows.
func TestApplyEngineBackendInfoReportsWhatTheEngineBuiltWith(t *testing.T) {
	engine := &stubEngineBackendInfo{sinkBackend: "pipewiresink", sinkTarget: "showmesh-pw-target", clockSource: "phc"}
	var payload mqttproto.AudioPayload
	applyEngineBackendInfo(&payload, engine)
	if payload.EngineSinkBackend != "pipewiresink" {
		t.Errorf("EngineSinkBackend = %q, want %q", payload.EngineSinkBackend, "pipewiresink")
	}
	if payload.EngineSinkTarget != "showmesh-pw-target" {
		t.Errorf("EngineSinkTarget = %q, want %q", payload.EngineSinkTarget, "showmesh-pw-target")
	}
	if payload.EngineClockSource != "phc" {
		t.Errorf("EngineClockSource = %q, want %q", payload.EngineClockSource, "phc")
	}
	if payload.EngineClockReason != "" {
		t.Errorf("EngineClockReason = %q, want \"\"", payload.EngineClockReason)
	}
}

// TestApplyEngineBackendInfoReportsAFallbackClockReason proves a "default"
// clock source's reason is carried through unchanged.
func TestApplyEngineBackendInfoReportsAFallbackClockReason(t *testing.T) {
	engine := &stubEngineBackendInfo{sinkBackend: "alsasink", clockSource: "default", clockReason: "interface eth0 has no associated PHC"}
	var payload mqttproto.AudioPayload
	applyEngineBackendInfo(&payload, engine)
	if payload.EngineClockSource != "default" || payload.EngineClockReason != "interface eth0 has no associated PHC" {
		t.Errorf("EngineClockSource/EngineClockReason = %q/%q, want %q/%q",
			payload.EngineClockSource, payload.EngineClockReason, "default", "interface eth0 has no associated PHC")
	}
}

// TestApplyAlignmentWritesTheFields proves applyAlignment carries a
// measured snapshot's fields onto the payload's Alignment* fields
// exactly, including a non-nil AlignmentSampledAt.
func TestApplyAlignmentWritesTheFields(t *testing.T) {
	sampledAt := time.Unix(1000, 0).UTC()
	mgr := &stubSnapshotter{alignment: audio.AlignmentSnapshot{
		Measured: true, OffsetMs: -1234, SampledAt: sampledAt, SessionID: "show",
	}}

	var payload mqttproto.AudioPayload
	applyAlignment(context.Background(), &payload, mgr)

	if !payload.AlignmentMeasured {
		t.Fatal("AlignmentMeasured = false, want true")
	}
	if payload.AlignmentOffsetMs != -1234 {
		t.Errorf("AlignmentOffsetMs = %d, want -1234", payload.AlignmentOffsetMs)
	}
	if payload.AlignmentSampledAt == nil || !payload.AlignmentSampledAt.Equal(sampledAt) {
		t.Errorf("AlignmentSampledAt = %v, want %v", payload.AlignmentSampledAt, sampledAt)
	}
	if payload.AlignmentSessionID != "show" {
		t.Errorf("AlignmentSessionID = %q, want %q", payload.AlignmentSessionID, "show")
	}
}

// TestApplyAlignmentNotMeasuredCarriesReasonNoNumbers proves the
// not-measured half writes the session id and reason but leaves every
// numeric field zero and AlignmentSampledAt nil.
func TestApplyAlignmentNotMeasuredCarriesReasonNoNumbers(t *testing.T) {
	mgr := &stubSnapshotter{alignment: audio.AlignmentSnapshot{
		SessionID: "show", Reason: "underrun suspected",
	}}

	var payload mqttproto.AudioPayload
	applyAlignment(context.Background(), &payload, mgr)

	if payload.AlignmentMeasured {
		t.Fatal("AlignmentMeasured = true, want false")
	}
	if payload.AlignmentOffsetMs != 0 {
		t.Errorf("AlignmentOffsetMs = %d, want 0", payload.AlignmentOffsetMs)
	}
	if payload.AlignmentSampledAt != nil {
		t.Errorf("AlignmentSampledAt = %v, want nil", payload.AlignmentSampledAt)
	}
	if payload.AlignmentReason != "underrun suspected" {
		t.Errorf("AlignmentReason = %q, want %q", payload.AlignmentReason, "underrun suspected")
	}
}

// TestApplyAlignmentNilManagerLeavesFieldsZero proves a nil mgr (no asset
// directory configured on this node) leaves every Alignment* field zero,
// matching applyTimeline's identical nil-safe rule.
func TestApplyAlignmentNilManagerLeavesFieldsZero(t *testing.T) {
	var payload mqttproto.AudioPayload
	applyAlignment(context.Background(), &payload, nil)

	if payload.AlignmentMeasured || payload.AlignmentOffsetMs != 0 || payload.AlignmentSampledAt != nil ||
		payload.AlignmentSessionID != "" || payload.AlignmentReason != "" {
		t.Errorf("payload = %+v, want every Alignment* field zero for a nil manager", payload)
	}
}
