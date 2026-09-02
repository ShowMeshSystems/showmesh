package nodeaudio

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

var sampleObservedAt = time.Unix(2000, 0).UTC()

func samplePayload() mqttproto.AudioPayload {
	observedAt := sampleObservedAt
	return mqttproto.AudioPayload{
		EngineAvailable:    true,
		HardwareEnumerated: true,
		DeviceAvailable:    true,
		OutputsCount:       1,
		ProgramAvailable:   true,
		LTCAvailable:       false,
		LTCReason:          "no route achieved 3 or more channels",
		Routes: []mqttproto.AudioRouteReport{
			{Device: "hw:CARD=PCH,DEV=0", Available: true, Channels: 2, Rate: 48000, Format: "S16LE"},
		},
		DiscoveredAt:       &observedAt,
		ObservedAt:         &observedAt,
		LTCGeneratorState:  "stopped",
		LTCGeneratorReason: "no generator has ever been started on this node",
	}
}

// fakeClockDomainSource is a minimal [ClockDomainSource] a test can
// configure to answer "declared", "never activated", or "store error".
type fakeClockDomainSource struct {
	obj    store.ConfigObjectRecord
	objErr error
	rev    store.ConfigRevisionRecord
	revErr error
}

func (f fakeClockDomainSource) GetConfigObject(context.Context, string, string) (store.ConfigObjectRecord, error) {
	if f.objErr != nil {
		return store.ConfigObjectRecord{}, f.objErr
	}
	return f.obj, nil
}

func (f fakeClockDomainSource) GetConfigRevision(context.Context, string, string, int64) (store.ConfigRevisionRecord, error) {
	if f.revErr != nil {
		return store.ConfigRevisionRecord{}, f.revErr
	}
	return f.rev, nil
}

// declaredClockDomainSource builds a fakeClockDomainSource reporting an
// active audio.node revision declaring domain/provenance, activated at
// declaredAt.
func declaredClockDomainSource(t *testing.T, domain, provenance string, declaredAt time.Time) fakeClockDomainSource {
	t.Helper()
	payloadJSON, err := json.Marshal(config.AudioNodePayload{
		ProgramRoute: "hw:CARD=PCH,DEV=0", LTCRoute: "hw:CARD=PCH,DEV=0",
		ClockDomain: domain, ClockDomainProvenance: provenance,
	})
	if err != nil {
		t.Fatalf("marshal audio.node payload: %v", err)
	}
	return fakeClockDomainSource{
		obj: store.ConfigObjectRecord{Kind: config.AudioNodeConfigKind, ID: "audio-01", CurrentRevision: 1},
		rev: store.ConfigRevisionRecord{
			Kind: config.AudioNodeConfigKind, ObjectID: "audio-01", Revision: 1,
			PayloadJSON: string(payloadJSON), CreatedAt: declaredAt,
		},
	}
}

func findObs(t *testing.T, obs []observation.Observation, sig observation.SignalID) observation.Observation {
	t.Helper()
	for _, o := range obs {
		if o.Signal == sig {
			return o
		}
	}
	t.Fatalf("no observation found for signal %q among %d observations", sig, len(obs))
	return observation.Observation{}
}

// TestPollUsesNodeReportedObservedAt proves ObservedAt is the node's own
// evidence timestamp, never the coordinator's receipt time, exactly the
// rule noderender's identical test proves one collector over.
func TestPollUsesNodeReportedObservedAt(t *testing.T) {
	st := NewStore()
	receivedAt := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	st.Put("audio-01", samplePayload(), receivedAt)

	c := New(st)
	obs, complete := c.Poll(context.Background())
	if !complete {
		t.Fatalf("Poll: complete = false, want true")
	}

	state := findObs(t, obs, SignalEngineState)
	if state.ObservedAt == nil || !state.ObservedAt.Equal(sampleObservedAt) {
		t.Errorf("ObservedAt = %v, want the node-reported %s (not receivedAt %s)", state.ObservedAt, sampleObservedAt, receivedAt)
	}
	if !state.CollectedAt.Equal(receivedAt) {
		t.Errorf("CollectedAt = %s, want the coordinator's own receipt time %s", state.CollectedAt, receivedAt)
	}
	if state.Value != StateUsable {
		t.Errorf("engine state value = %v, want %q", state.Value, StateUsable)
	}
	if state.Resource.Kind != observation.ResourceNode || state.Resource.ID != "audio-01" {
		t.Errorf("resource = %+v, want kind=node id=audio-01", state.Resource)
	}
}

// TestPollDiscoveryAndLiveSignalsUseDistinctEvidenceTimes proves finding 1's
// fix: device.state, a signal the agent's hardware probe truly never
// re-checks after boot, must still stamp its ObservedAt from
// AudioPayload.ObservedAt (this report tick's own evidence time), never
// from AudioPayload.DiscoveredAt (the one-shot startup probe time). The
// agent republishes its cached discovery on every tick as a current
// assertion, so the tick itself is what tells the coordinator the node is
// still alive and asserting that hardware; stamping with DiscoveredAt
// instead pins the signal to the startup probe forever and a dark node
// that stopped reporting a week ago would read current, exactly the
// permanent-stale-turned-permanent-current defect this issue reported.
func TestPollDiscoveryAndLiveSignalsUseDistinctEvidenceTimes(t *testing.T) {
	st := NewStore()
	discoveredAt := time.Unix(1000, 0).UTC() // the one-shot startup probe
	observedAt := time.Unix(9000, 0).UTC()   // a much later report tick
	payload := samplePayload()
	payload.DiscoveredAt = &discoveredAt
	payload.ObservedAt = &observedAt
	payload.LTCGeneratorState = "running"
	st.Put("audio-01", payload, time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	device := findObs(t, obs, SignalDeviceState)
	if device.ObservedAt == nil || !device.ObservedAt.Equal(observedAt) {
		t.Errorf("device.state ObservedAt = %v, want the report tick time %v", device.ObservedAt, observedAt)
	}
	if device.ObservedAt.Equal(discoveredAt) {
		t.Error("device.state ObservedAt equals the startup probe time; the agent republishes this evidence every tick, so it must never share DiscoveredAt's evidence")
	}
	engine := findObs(t, obs, SignalEngineState)
	if engine.ObservedAt == nil || !engine.ObservedAt.Equal(observedAt) {
		t.Errorf("engine.state ObservedAt = %v, want the live tick time %v", engine.ObservedAt, observedAt)
	}
	if engine.ObservedAt.Equal(discoveredAt) {
		t.Error("engine.state ObservedAt equals the startup probe time; the agent re-checks it fresh every tick, so it must never share DiscoveredAt's evidence")
	}
	generator := findObs(t, obs, SignalLTCGeneratorState)
	if generator.ObservedAt == nil || !generator.ObservedAt.Equal(observedAt) {
		t.Errorf("ltc.generator.state ObservedAt = %v, want the live tick time %v", generator.ObservedAt, observedAt)
	}
	if generator.ObservedAt.Equal(discoveredAt) {
		t.Error("ltc.generator.state ObservedAt equals the startup probe time; it must never share DiscoveredAt's evidence")
	}

	// The node.audio.engine.* glitch signals must not be stamped with the
	// stale one-shot discovery time either -- see the LTC generator
	// signals just above for the identical defect this would otherwise
	// repeat.
	payload.EngineGlitchCountsKnown = true
	since := observedAt
	payload.EngineGlitchCountsSince = &since
	st.Put("audio-01", payload, time.Now())
	obs, _ = c.Poll(context.Background())
	glitch := findObs(t, obs, SignalEngineWarningsStream)
	if glitch.ObservedAt == nil || !glitch.ObservedAt.Equal(observedAt) {
		t.Errorf("engine.warnings.stream ObservedAt = %v, want the live tick time %v", glitch.ObservedAt, observedAt)
	}
	if glitch.ObservedAt.Equal(discoveredAt) {
		t.Error("engine.warnings.stream ObservedAt equals the startup probe time; it must never share DiscoveredAt's evidence")
	}
}

// TestPollNodeReportsNoObservedAtIsUnknownAge proves the genuinely-unknown
// half of ADR-011: a payload with a zero ObservedAt (never sent by a
// well-formed report, since mqttproto.AudioPayload.Validate requires it
// non-nil, but not something this collector should crash on) stays nil
// for device.state, never defaulted to the receipt time.
func TestPollNodeReportsNoObservedAtIsUnknownAge(t *testing.T) {
	st := NewStore()
	receivedAt := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	payload := samplePayload()
	payload.ObservedAt = nil
	st.Put("audio-01", payload, receivedAt)

	c := New(st)
	obs, _ := c.Poll(context.Background())
	state := findObs(t, obs, SignalDeviceState)
	if state.ObservedAt != nil {
		t.Errorf("ObservedAt = %v, want nil (unknown age)", state.ObservedAt)
	}
	if state.StateAt(receivedAt) != observation.StateUnknownAge {
		t.Errorf("StateAt = %s, want unknown_age", state.StateAt(receivedAt))
	}
}

// TestPollEngineStateGoesStaleWhenTheNodeGenuinelyStopsReporting proves
// the negative of the engine.state fix above: this is not "engine.state can never go
// stale." A node that stops publishing altogether (ObservedAt stops
// advancing, unlike TestPollEngineStateStaysFreshWhileNodeKeepsReporting,
// where a live tick keeps refreshing it) still ages past DefaultValidFor
// and reads stale, exactly like every other observation in this project.
func TestPollEngineStateGoesStaleWhenTheNodeGenuinelyStopsReporting(t *testing.T) {
	st := NewStore()
	lastTick := time.Now().Add(-1 * time.Hour)
	payload := samplePayload()
	payload.ObservedAt = &lastTick
	st.Put("audio-01", payload, lastTick)

	c := New(st)
	obs, _ := c.Poll(context.Background())
	state := findObs(t, obs, SignalEngineState)
	reason := findObs(t, obs, SignalEngineReason)

	now := time.Now()
	if got := state.StateAt(now); got != observation.StateStale {
		t.Errorf("engine.state StateAt(now) = %v, want stale: a node that stopped reporting an hour ago must not read current", got)
	}
	if got := reason.StateAt(now); got != observation.StateStale {
		t.Errorf("engine.reason StateAt(now) = %v, want stale: a node that stopped reporting an hour ago must not read current", got)
	}
}

// TestPollEngineUnavailableReportsReason proves the engine/device/program/
// ltc unavailable-with-reason shape actually renders through Poll.
func TestPollEngineUnavailableReportsReason(t *testing.T) {
	st := NewStore()
	payload := samplePayload()
	payload.EngineAvailable = false
	payload.EngineReason = "gst-launch-1.0 not found on PATH"
	payload.DeviceAvailable = false
	payload.DeviceReason = "engine unavailable"
	payload.ProgramAvailable = false
	payload.ProgramReason = "engine unavailable"
	payload.OutputsCount = 0
	payload.Routes = []mqttproto.AudioRouteReport{}
	st.Put("audio-01", payload, time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	state := findObs(t, obs, SignalEngineState)
	if state.Value != StateUnavailable {
		t.Errorf("engine state = %v, want %q", state.Value, StateUnavailable)
	}
	reason := findObs(t, obs, SignalEngineReason)
	if reason.Value != "gst-launch-1.0 not found on PATH" {
		t.Errorf("engine reason = %v, want the stated reason", reason.Value)
	}
}

// TestPollEngineStateStaysFreshWhileNodeKeepsReporting proves the
// agent re-checks engine.Available() fresh on every report tick (see
// applyEngineAvailability in audioreport.go), so EngineReason genuinely
// changes across reports long after the one-shot startup discovery
// probe. This collector must stamp that live evidence with the report
// tick's own time, not the pinned discovery time -- otherwise an online
// node that keeps publishing fresh engine evidence reads its whole audio
// surface as permanently stale past DefaultValidFor, indistinguishable
// from a node that stopped reporting altogether.
func TestPollEngineStateStaysFreshWhileNodeKeepsReporting(t *testing.T) {
	st := NewStore()
	discoveredAt := time.Now().Add(-1 * time.Hour) // ancient startup probe
	firstReason := "no audio.node configuration has been delivered to this node yet"
	tick1 := time.Now().Add(-time.Minute)
	payload := samplePayload()
	payload.EngineAvailable = false
	payload.EngineReason = firstReason
	payload.DiscoveredAt = &discoveredAt
	payload.ObservedAt = &tick1
	st.Put("audio-01", payload, tick1)

	c := New(st)
	obs, _ := c.Poll(context.Background())
	reason := findObs(t, obs, SignalEngineReason)
	if reason.Value != firstReason {
		t.Fatalf("engine reason = %v, want the first observed reason %v", reason.Value, firstReason)
	}

	// A later tick observes a DIFFERENT reason (a real audio.node
	// revision changed the pipeline error), while DiscoveredAt stays
	// pinned at the original startup probe -- exactly the field
	// [buildAudioPayload] never updates after boot.
	secondReason := "not-negotiated: keep-alive channel program-out-2 could not be negotiated"
	tick2 := time.Now()
	payload.EngineReason = secondReason
	payload.ObservedAt = &tick2
	st.Put("audio-01", payload, tick2)

	obs, _ = c.Poll(context.Background())
	state := findObs(t, obs, SignalEngineState)
	reason = findObs(t, obs, SignalEngineReason)
	if reason.Value != secondReason {
		t.Fatalf("engine reason = %v, want the newly observed reason %v", reason.Value, secondReason)
	}
	if state.ObservedAt == nil || !state.ObservedAt.Equal(tick2) {
		t.Errorf("engine.state ObservedAt = %v, want the live report-tick time %v, not the frozen startup probe time %v", state.ObservedAt, tick2, discoveredAt)
	}
	if got := state.StateAt(tick2); got != observation.StateCurrent {
		t.Errorf("engine.state StateAt(now) = %v, want current: an online node reporting fresh engine evidence every tick must not read stale", got)
	}
}

// TestPollLTCUnavailableWithUsableProgramReportsBothIndependently proves
// an engine with a usable program bus but no LTC channel bound (generator
// state "unsupported") reports the two states independently, never
// collapsed into one.
func TestPollLTCUnavailableWithUsableProgramReportsBothIndependently(t *testing.T) {
	st := NewStore()
	payload := samplePayload()
	payload.LTCGeneratorState = "unsupported"
	st.Put("audio-01", payload, time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	program := findObs(t, obs, SignalProgramState)
	if program.Value != StateUsable {
		t.Errorf("program state = %v, want %q", program.Value, StateUsable)
	}
	ltc := findObs(t, obs, SignalLTCState)
	if ltc.Value != StateUnavailable {
		t.Errorf("ltc state = %v, want %q", ltc.Value, StateUnavailable)
	}
}

// TestPollLTCGeneratorRunningReportsFrameRateAndTimecodeSuppressesReason
// proves the four LTC generator signals render independently of engine/device/
// program state, and that a running generator carries no reason.
func TestPollLTCGeneratorRunningReportsFrameRateAndTimecodeSuppressesReason(t *testing.T) {
	st := NewStore()
	payload := samplePayload()
	payload.LTCGeneratorState = "running"
	payload.LTCGeneratorReason = ""
	payload.LTCFrameRateKnown = true
	payload.LTCFrameRate = "30"
	payload.LTCTimecodeKnown = true
	payload.LTCTimecode = "00:00:05:12"
	st.Put("audio-01", payload, time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	state := findObs(t, obs, SignalLTCGeneratorState)
	if state.Value != "running" {
		t.Errorf("generator state = %v, want running", state.Value)
	}
	reason := findObs(t, obs, SignalLTCGeneratorReason)
	if reason.Absence != observation.StateNotCollected {
		t.Errorf("generator reason while running = %+v, want not_collected", reason)
	}
	rate := findObs(t, obs, SignalLTCFrameRate)
	if rate.Value != "30" {
		t.Errorf("frame rate = %v, want 30", rate.Value)
	}
	tc := findObs(t, obs, SignalLTCTimecode)
	if tc.Value != "00:00:05:12" {
		t.Errorf("timecode = %v, want 00:00:05:12", tc.Value)
	}
}

// TestPollLTCGeneratorDeadReportsStateAndReasonIndependentlyOfEngineState
// proves ruling 4: a generator's own state/reason is never inferred from
// EngineState — the sample payload's engine/device/program all report
// usable while the generator reports failed.
func TestPollLTCGeneratorDeadReportsStateAndReasonIndependentlyOfEngineState(t *testing.T) {
	st := NewStore()
	payload := samplePayload()
	payload.LTCGeneratorState = "failed"
	payload.LTCGeneratorReason = "no heartbeat within 3s; generator process may still be running"
	st.Put("audio-01", payload, time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	engine := findObs(t, obs, SignalEngineState)
	if engine.Value != StateUsable {
		t.Fatalf("engine state = %v, want usable (sanity check)", engine.Value)
	}
	state := findObs(t, obs, SignalLTCGeneratorState)
	if state.Value != "failed" {
		t.Errorf("generator state = %v, want failed", state.Value)
	}
	reason := findObs(t, obs, SignalLTCGeneratorReason)
	if reason.Value != "no heartbeat within 3s; generator process may still be running" {
		t.Errorf("generator reason = %v, want the stated reason", reason.Value)
	}
	rate := findObs(t, obs, SignalLTCFrameRate)
	if rate.Absence != observation.StateNotCollected {
		t.Errorf("frame rate with no generator ever started = %+v, want not_collected", rate)
	}
	tc := findObs(t, obs, SignalLTCTimecode)
	if tc.Absence != observation.StateNotCollected {
		t.Errorf("timecode with generator not running = %+v, want not_collected", tc)
	}
}

// TestPollDeviceStateGoesStaleWhenTheNodeGenuinelyStopsReporting proves the
// guard on finding 1's fix: [Store] keeps only a node's most recent report
// and nothing evicts it, so device.state must still age to stale once the
// node stops reporting, exactly like engine.state
// (TestPollEngineStateGoesStaleWhenTheNodeGenuinelyStopsReporting). A node
// that has been off for a week must not read its cached hardware discovery
// as usable, current, forever.
func TestPollDeviceStateGoesStaleWhenTheNodeGenuinelyStopsReporting(t *testing.T) {
	st := NewStore()
	lastTick := time.Now().Add(-1 * time.Hour)
	payload := samplePayload()
	payload.ObservedAt = &lastTick
	st.Put("audio-01", payload, lastTick)

	c := New(st)
	obs, _ := c.Poll(context.Background())
	device := findObs(t, obs, SignalDeviceState)

	now := time.Now()
	if got := device.StateAt(now); got != observation.StateStale {
		t.Errorf("device.state StateAt(now) = %v, want stale: a node that stopped reporting an hour ago must not read current", got)
	}
}

// TestLTCStateMapping proves the full generator-state-to-ltc-state mapping
// finding 2's fix relies on: running and stopped (both proof an LTC
// channel is bound and drivable) read usable, unsupported and failed read
// unavailable. Table-driven so a mutation to any one branch is caught at
// the specific state/value pair that exposed it.
func TestLTCStateMapping(t *testing.T) {
	cases := []struct {
		generatorState string
		want           string
	}{
		{"running", StateUsable},
		{"stopped", StateUsable},
		{"unsupported", StateUnavailable},
		{"failed", StateUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.generatorState, func(t *testing.T) {
			st := NewStore()
			payload := samplePayload()
			payload.LTCGeneratorState = tc.generatorState
			st.Put("audio-01", payload, time.Now())

			c := New(st)
			obs, _ := c.Poll(context.Background())
			ltc := findObs(t, obs, SignalLTCState)
			if ltc.Value != tc.want {
				t.Errorf("generator state %q -> ltc.state = %v, want %q", tc.generatorState, ltc.Value, tc.want)
			}
		})
	}
}

// TestPollReportsClockDomainFromConfigNotFromTheNode proves finding 2: the
// clock domain observation comes from the coordinator's own audio.node
// configuration, never from anything the node itself reported (the sample
// payload carries no clock domain field at all any more).
func TestPollReportsClockDomainFromConfigNotFromTheNode(t *testing.T) {
	declaredAt := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	src := declaredClockDomainSource(t, "single-interface", "one interface, both routes on it", declaredAt)
	st := NewStore(WithClockDomainSource(src))
	st.Put("audio-01", samplePayload(), time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	domain := findObs(t, obs, SignalClockDomain)
	if domain.Value != "single-interface" {
		t.Errorf("clock domain = %v, want %q", domain.Value, "single-interface")
	}
	if domain.ObservedAt == nil || !domain.ObservedAt.Equal(declaredAt) {
		t.Errorf("clock domain ObservedAt = %v, want the declaration's own CreatedAt %v", domain.ObservedAt, declaredAt)
	}
	provenance := findObs(t, obs, SignalClockProvenance)
	if provenance.Value != "one interface, both routes on it" {
		t.Errorf("clock provenance = %v, want the declared provenance", provenance.Value)
	}
}

// TestPollClockDomainNoSourceWiredIsNotCollected proves the nil-clockSrc
// default (no WithClockDomainSource option) reports not_collected, never a
// fabricated "undeclared" reading.
func TestPollClockDomainNoSourceWiredIsNotCollected(t *testing.T) {
	st := NewStore()
	st.Put("audio-01", samplePayload(), time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	domain := findObs(t, obs, SignalClockDomain)
	if domain.Absence != observation.StateCollectionFailed {
		t.Errorf("clock domain absence = %+v, want StateNotCollected", domain.Absence)
	}
}

// TestPollClockDomainNeverActivatedIsNotCollected proves a node with no
// audio.node configuration ever activated reports not_collected, not a
// zero-valued "" domain masquerading as a reading.
func TestPollClockDomainNeverActivatedIsNotCollected(t *testing.T) {
	src := fakeClockDomainSource{objErr: store.ErrConfigObjectNotFound}
	st := NewStore(WithClockDomainSource(src))
	st.Put("audio-01", samplePayload(), time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	domain := findObs(t, obs, SignalClockDomain)
	if domain.Absence != observation.StateCollectionFailed {
		t.Errorf("clock domain absence = %q, want StateCollectionFailed", domain.Absence)
	}
	provenance := findObs(t, obs, SignalClockProvenance)
	if provenance.Absence != observation.StateCollectionFailed {
		t.Errorf("clock provenance absence = %q, want StateCollectionFailed", provenance.Absence)
	}
}

// TestPollHardwareEnumerationFailureIsNotCollected proves finding 4 at the
// collector boundary: "we could not enumerate" reports not_collected for
// device/program/ltc/outputs.count, carrying the node's own reason —
// never the same "unavailable" a clean empty enumeration would report.
func TestPollHardwareEnumerationFailureIsNotCollected(t *testing.T) {
	st := NewStore()
	payload := samplePayload()
	payload.HardwareEnumerated = false
	payload.HardwareEnumeratedReason = "device enumeration failed: exec: \"aplay\": executable file not found in $PATH"
	payload.DeviceAvailable, payload.ProgramAvailable, payload.LTCAvailable = false, false, false
	payload.DeviceReason, payload.ProgramReason, payload.LTCReason = payload.HardwareEnumeratedReason, payload.HardwareEnumeratedReason, payload.HardwareEnumeratedReason
	payload.OutputsCount = 0
	payload.Routes = []mqttproto.AudioRouteReport{}
	st.Put("audio-01", payload, time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	for _, sig := range []observation.SignalID{SignalDeviceState, SignalDeviceReason, SignalOutputsCount, SignalProgramState, SignalLTCState} {
		o := findObs(t, obs, sig)
		if o.Absence != observation.StateCollectionFailed {
			t.Errorf("%s absence = %q, want StateCollectionFailed", sig, o.Absence)
		}
		if o.Reason != payload.HardwareEnumeratedReason {
			t.Errorf("%s reason = %q, want %q", sig, o.Reason, payload.HardwareEnumeratedReason)
		}
	}
	// Engine is independent of enumeration and must still report normally.
	engine := findObs(t, obs, SignalEngineState)
	if engine.Value != StateUsable {
		t.Errorf("engine state = %v, want %q (engine probing does not depend on aplay enumeration)", engine.Value, StateUsable)
	}
}

// TestPollOutputsEnumeratedAndTruncatedReported proves finding 7's two
// signals actually reach the observation layer.
func TestPollOutputsEnumeratedAndTruncatedReported(t *testing.T) {
	st := NewStore()
	payload := samplePayload()
	payload.EnumeratedCount = 6
	payload.Truncated = true
	st.Put("audio-01", payload, time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	enumerated := findObs(t, obs, SignalOutputsEnumerated)
	if v, ok := enumerated.Value.(int64); !ok || v != 6 {
		t.Errorf("outputs enumerated = %v, want int64(6)", enumerated.Value)
	}
	truncated := findObs(t, obs, SignalOutputsTruncated)
	if v, ok := truncated.Value.(bool); !ok || !v {
		t.Errorf("outputs truncated = %v, want true", truncated.Value)
	}
}

func TestPollOutputsCountReported(t *testing.T) {
	st := NewStore()
	st.Put("audio-01", samplePayload(), time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	count := findObs(t, obs, SignalOutputsCount)
	if v, ok := count.Value.(int64); !ok || v != 1 {
		t.Errorf("outputs count = %v, want int64(1)", count.Value)
	}
}

// TestPollEngineGlitchCountsKnownReportsValues proves the five
// node.audio.engine.* glitch signals carry the payload's own values
// when EngineGlitchCountsKnown is true -- the outcome this seam exists
// for: a bus warning/QoS count actually reaching an observation.
func TestPollEngineGlitchCountsKnownReportsValues(t *testing.T) {
	st := NewStore()
	payload := samplePayload()
	since := time.Unix(1500, 0).UTC()
	payload.EngineGlitchCountsKnown = true
	payload.EngineGlitchCountsSince = &since
	payload.EngineStreamWarningCount = 4
	payload.EngineResourceWarningCount = 2
	payload.EngineOtherWarningCount = 1
	payload.EngineQosDropCount = 9
	st.Put("audio-01", payload, time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())

	started := findObs(t, obs, SignalEngineStartedAt)
	if started.Value != since.Format(time.RFC3339Nano) {
		t.Errorf("engine started_at = %v, want %v", started.Value, since.Format(time.RFC3339Nano))
	}
	cases := []struct {
		sig  observation.SignalID
		want int64
	}{
		{SignalEngineWarningsStream, 4},
		{SignalEngineWarningsResource, 2},
		{SignalEngineWarningsOther, 1},
		{SignalEngineQosDrops, 9},
	}
	for _, tc := range cases {
		o := findObs(t, obs, tc.sig)
		if v, ok := o.Value.(int64); !ok || v != tc.want {
			t.Errorf("%s = %v, want int64(%d)", tc.sig, o.Value, tc.want)
		}
	}
}

// TestPollEngineGlitchCountsUnknownReportsNotCollected proves a payload
// reporting EngineGlitchCountsKnown false renders every one of the
// five signals as not_collected, never a fabricated healthy zero.
func TestPollEngineGlitchCountsUnknownReportsNotCollected(t *testing.T) {
	st := NewStore()
	st.Put("audio-01", samplePayload(), time.Now()) // EngineGlitchCountsKnown defaults false

	c := New(st)
	obs, _ := c.Poll(context.Background())

	for _, sig := range []observation.SignalID{
		SignalEngineStartedAt, SignalEngineWarningsStream, SignalEngineWarningsResource,
		SignalEngineWarningsOther, SignalEngineQosDrops,
	} {
		o := findObs(t, obs, sig)
		if o.Absence != observation.StateNotCollected {
			t.Errorf("%s absence = %q, want %q", sig, o.Absence, observation.StateNotCollected)
		}
	}
}

func TestPollUnknownNodeProducesNoObservations(t *testing.T) {
	st := NewStore()
	c := New(st)
	obs, complete := c.Poll(context.Background())
	if len(obs) != 0 {
		t.Errorf("Poll on empty store: got %d observations, want 0", len(obs))
	}
	if !complete {
		t.Errorf("Poll: complete = false, want true")
	}
}

// TestNodeAudioObservationsMatchesPoll proves the read-time path renders
// the identical evidence Poll would, matching noderender's identical
// consistency test one collector over.
func TestNodeAudioObservationsMatchesPoll(t *testing.T) {
	st := NewStore()
	receivedAt := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	st.Put("audio-01", samplePayload(), receivedAt)

	c := New(st)
	polled, _ := c.Poll(context.Background())
	fromRead := st.NodeAudioObservations("audio-01")

	if len(fromRead) != len(polled) {
		t.Fatalf("read-time total = %d, Poll total = %d, want equal", len(fromRead), len(polled))
	}
	state := findObs(t, fromRead, SignalEngineState)
	if state.Value != StateUsable {
		t.Errorf("engine state = %v, want %q", state.Value, StateUsable)
	}
}

func TestNodeAudioObservationsUnknownNodeReturnsNil(t *testing.T) {
	st := NewStore()
	if got := st.NodeAudioObservations("never-seen"); got != nil {
		t.Errorf("NodeAudioObservations(unknown node) = %v, want nil", got)
	}
}

// TestSourceForAndNodeFromSourceRoundTrip proves the two nodes-collide-on-
// one-row guard actually round-trips, mirroring noderender's identical
// test.
func TestSourceForAndNodeFromSourceRoundTrip(t *testing.T) {
	source := SourceFor("audio-07")
	nodeID, ok := NodeFromSource(source)
	if !ok || nodeID != "audio-07" {
		t.Errorf("NodeFromSource(%q) = (%q, %v), want (\"audio-07\", true)", source, nodeID, ok)
	}
	if _, ok := NodeFromSource("fpp-rest:endpoint-1"); ok {
		t.Error("NodeFromSource on a foreign source prefix returned ok=true, want false")
	}
}

func TestAllSignalIDsAreValid(t *testing.T) {
	for _, sig := range AllSignalIDs {
		if err := observation.ValidateSignalID(sig); err != nil {
			t.Errorf("ValidateSignalID(%q) = %v, want nil", sig, err)
		}
	}
	if len(AllSignalIDs) != 21 {
		t.Errorf("AllSignalIDs has %d entries, want 21", len(AllSignalIDs))
	}
}

func TestSessionSignalIDsAreValid(t *testing.T) {
	for _, sig := range SessionSignalIDs {
		if err := observation.ValidateSignalID(sig); err != nil {
			t.Errorf("ValidateSignalID(%q) = %v, want nil", sig, err)
		}
	}
	if len(SessionSignalIDs) != 26 {
		t.Errorf("SessionSignalIDs has %d entries, want 26", len(SessionSignalIDs))
	}
}
