package nodeaudio

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// subjectCase is one signal in one condition: what the node reported, and
// the state and reason the collector must give it.
type subjectCase struct {
	name       string
	signals    []observation.SignalID
	node       func(p *mqttproto.AudioPayload)
	session    func(s *mqttproto.AudioSessionReport)
	wantState  observation.State
	wantReason string
}

func (c subjectCase) observe(t *testing.T) []observation.Observation {
	t.Helper()
	sess := mqttproto.AudioSessionReport{SessionID: "sess-1", State: "stopped", Fault: "none"}
	if c.session != nil {
		c.session(&sess)
	}
	p := samplePayloadWithSession(sess)
	if c.node != nil {
		c.node(&p)
	}
	st := NewStore()
	st.Put("audio-01", p, time.Now())
	obs, _ := New(st).Poll(context.Background())
	return obs
}

func (c subjectCase) find(t *testing.T, obs []observation.Observation, sig observation.SignalID) observation.Observation {
	t.Helper()
	if c.session != nil {
		return findSessionObs(t, obs, sig)
	}
	return findObs(t, obs, sig)
}

const (
	nothingScheduledReason = "No session on this node is playing against a scheduled start."
	noLTCHolderReason      = "No session on this node holds the LTC run."
	sessionStoppedReason   = "This session is stopped."
)

var multisyncSignals = []observation.SignalID{
	SignalSessionTriggerSequenceFilename, SignalSessionTriggerArrivalNs, SignalSessionStartLeadMs,
}

// subjectCases pins every signal ADR-056 moved, in both conditions: its
// subject is absent, and its subject exists.
var subjectCases = []subjectCase{
	{
		name: "timeline, nothing scheduled", signals: timelineSignals,
		node: func(p *mqttproto.AudioPayload) {
			p.TimelineNotApplicable, p.TimelineReason = true, nothingScheduledReason
		},
		wantState: observation.StateNotApplicable, wantReason: nothingScheduledReason,
	},
	{
		name: "timeline, agent does not say", signals: timelineSignals,
		node:      func(p *mqttproto.AudioPayload) {},
		wantState: observation.StateNotCollected, wantReason: "this node is running no session against a scheduled start instant",
	},
	{
		name:    "timeline, scheduled but clocks unread",
		signals: []observation.SignalID{SignalTimelineExpectedMs, SignalTimelineActualMs, SignalTimelineErrorMs},
		node: func(p *mqttproto.AudioPayload) {
			p.TimelineScheduled, p.TimelineReason = true, "media clock unreadable: no PHC"
		},
		wantState: observation.StateNotCollected, wantReason: "media clock unreadable: no PHC",
	},
	{
		name: "timeline, scheduled and never resynced", signals: []observation.SignalID{SignalTimelineLastResyncReason},
		node:      func(p *mqttproto.AudioPayload) { p.TimelineScheduled = true },
		wantState: observation.StateNotApplicable, wantReason: "This scheduled session has not resynced.",
	},
	{
		name: "timeline, scheduled and resynced", signals: []observation.SignalID{SignalTimelineLastResyncReason},
		node: func(p *mqttproto.AudioPayload) {
			p.TimelineScheduled, p.TimelineLastResyncReason = true, "ptp_step"
		},
		wantState: observation.StateCurrent,
	},
	{
		name: "alignment, no LTC holder", signals: []observation.SignalID{SignalClockAlignment, SignalClockAlignmentState},
		node: func(p *mqttproto.AudioPayload) {
			p.AlignmentNotApplicable, p.AlignmentReason = true, noLTCHolderReason
		},
		wantState: observation.StateNotApplicable, wantReason: noLTCHolderReason,
	},
	{
		name: "alignment, sample missed", signals: []observation.SignalID{SignalClockAlignment, SignalClockAlignmentState},
		node:      func(p *mqttproto.AudioPayload) { p.AlignmentReason = "a session's lock was busy" },
		wantState: observation.StateNotCollected, wantReason: "a session's lock was busy",
	},
	{
		name: "LTC generator reason, running", signals: []observation.SignalID{SignalLTCGeneratorReason},
		node:      func(p *mqttproto.AudioPayload) { p.LTCGeneratorState, p.LTCGeneratorReason = "running", "" },
		wantState: observation.StateNotApplicable, wantReason: "The timecode generator is running, so there is no reason to report.",
	},
	{
		name: "LTC generator reason, stopped", signals: []observation.SignalID{SignalLTCGeneratorReason},
		node:      func(p *mqttproto.AudioPayload) {},
		wantState: observation.StateCurrent,
	},
	{
		name: "LTC frame rate, generator stopped", signals: []observation.SignalID{SignalLTCFrameRate},
		node:      func(p *mqttproto.AudioPayload) {},
		wantState: observation.StateNotApplicable, wantReason: "No timecode run has set a frame rate on this node.",
	},
	{
		name: "LTC frame rate, node cannot generate", signals: []observation.SignalID{SignalLTCFrameRate},
		node:      func(p *mqttproto.AudioPayload) { p.LTCGeneratorState = "unsupported" },
		wantState: observation.StateNotApplicable, wantReason: "This node cannot generate timecode, so no frame rate is in effect.",
	},
	{
		name: "LTC frame rate, running with none reported", signals: []observation.SignalID{SignalLTCFrameRate},
		node:      func(p *mqttproto.AudioPayload) { p.LTCGeneratorState = "running" },
		wantState: observation.StateNotCollected, wantReason: "no LTC run has reported a frame rate on this node",
	},
	{
		name: "LTC timecode, generator stopped", signals: []observation.SignalID{SignalLTCTimecode},
		node:      func(p *mqttproto.AudioPayload) {},
		wantState: observation.StateNotApplicable, wantReason: "The timecode generator is not running.",
	},
	{
		name: "LTC timecode, running with none reported", signals: []observation.SignalID{SignalLTCTimecode},
		node:      func(p *mqttproto.AudioPayload) { p.LTCGeneratorState = "running" },
		wantState: observation.StateNotCollected, wantReason: "the timecode generator is not confirmed running, so no fresh timecode is available",
	},
	{
		name: "engine restore, nothing scheduled", signals: []observation.SignalID{SignalEngineRestoreNextAttemptMs},
		node:      func(p *mqttproto.AudioPayload) { p.EngineRestoreState = "idle" },
		wantState: observation.StateNotApplicable, wantReason: "No engine restore attempt is scheduled.",
	},
	{
		name: "engine restore, scheduled", signals: []observation.SignalID{SignalEngineRestoreNextAttemptMs},
		node: func(p *mqttproto.AudioPayload) {
			p.EngineRestoreState, p.EngineRestoreNextAttemptMs = "scheduled", 4000
		},
		wantState: observation.StateCurrent,
	},
	{
		name: "sink target, not PipeWire", signals: []observation.SignalID{SignalEngineSinkTarget},
		node:      func(p *mqttproto.AudioPayload) { p.EngineSinkBackend = "alsasink" },
		wantState: observation.StateNotApplicable, wantReason: "This node's audio output does not use PipeWire, so there is no PipeWire target.",
	},
	{
		name: "sink target, backend unreported", signals: []observation.SignalID{SignalEngineSinkTarget},
		node:      func(p *mqttproto.AudioPayload) {},
		wantState: observation.StateNotCollected, wantReason: "this node's report did not include an engine sink backend",
	},
	{
		name: "session source role, none set", signals: []observation.SignalID{SignalSessionSourceRole},
		session:   func(s *mqttproto.AudioSessionReport) {},
		wantState: observation.StateNotApplicable, wantReason: "No source role is set for this session.",
	},
	{
		name: "session source role, set", signals: []observation.SignalID{SignalSessionSourceRole},
		session:   func(s *mqttproto.AudioSessionReport) { s.HasSourceRole, s.SourceRole = true, "background" },
		wantState: observation.StateCurrent,
	},
	{
		name: "session playlist, none pinned", signals: []observation.SignalID{SignalSessionPlaylistRevision},
		session:   func(s *mqttproto.AudioSessionReport) {},
		wantState: observation.StateNotApplicable, wantReason: "No playlist is pinned to this session.",
	},
	{
		name: "session playlist, pinned", signals: []observation.SignalID{SignalSessionPlaylistRevision},
		session:   func(s *mqttproto.AudioSessionReport) { s.HasPlaylist, s.PlaylistRevision = true, 3 },
		wantState: observation.StateCurrent,
	},
	{
		name: "session item, none current", signals: []observation.SignalID{SignalSessionItemID, SignalSessionItemIndex},
		session:   func(s *mqttproto.AudioSessionReport) {},
		wantState: observation.StateNotApplicable, wantReason: "This session has no current item.",
	},
	{
		name: "session item, one current", signals: []observation.SignalID{SignalSessionItemID, SignalSessionItemIndex},
		session:   func(s *mqttproto.AudioSessionReport) { s.HasItem, s.ItemID = true, "item-1" },
		wantState: observation.StateCurrent,
	},
	{
		name: "session position, nothing loaded", signals: []observation.SignalID{SignalSessionPositionMs},
		session:   func(s *mqttproto.AudioSessionReport) { s.PositionNotApplicable = true },
		wantState: observation.StateNotApplicable, wantReason: "Nothing is loaded in this session.",
	},
	{
		name: "session position, loaded but unread", signals: []observation.SignalID{SignalSessionPositionMs},
		session:   func(s *mqttproto.AudioSessionReport) { s.State = "playing" },
		wantState: observation.StateNotCollected, wantReason: "no fresh position is available from the engine; it is mid-discontinuity or has nothing loaded",
	},
	{
		name: "session gain ceiling, none set", signals: []observation.SignalID{SignalSessionGainCeiling},
		session:   func(s *mqttproto.AudioSessionReport) {},
		wantState: observation.StateCurrent, wantReason: "No ceiling is set for this session. The +12 dB operator gain limit applies.",
	},
	{
		name: "session gain ceiling, set", signals: []observation.SignalID{SignalSessionGainCeiling},
		session:   func(s *mqttproto.AudioSessionReport) { s.HasCeiling, s.Ceiling = true, 0.5 },
		wantState: observation.StateCurrent,
	},
	{
		name: "session asset probe, none yet", signals: []observation.SignalID{SignalSessionAssetProbeState, SignalSessionAssetProbeReason},
		session:   func(s *mqttproto.AudioSessionReport) {},
		wantState: observation.StateNotApplicable, wantReason: "No asset has been probed for this session yet.",
	},
	{
		name: "session asset probe, probed", signals: []observation.SignalID{SignalSessionAssetProbeState, SignalSessionAssetProbeReason},
		session:   func(s *mqttproto.AudioSessionReport) { s.HasAssetProbe, s.AssetProbeState = true, "ok" },
		wantState: observation.StateCurrent,
	},
	{
		name: "session restore, none queued", signals: []observation.SignalID{SignalSessionRestoreNextAttemptMs},
		session:   func(s *mqttproto.AudioSessionReport) {},
		wantState: observation.StateNotApplicable, wantReason: "No restore is queued for this session.",
	},
	{
		name: "session restore, queued", signals: []observation.SignalID{SignalSessionRestoreNextAttemptMs},
		session:   func(s *mqttproto.AudioSessionReport) { s.RestorePending, s.RestoreNextAttemptMs = true, 2000 },
		wantState: observation.StateCurrent,
	},
	{
		name: "session item gap, session stopped", signals: []observation.SignalID{SignalSessionItemGapMs, SignalSessionItemGapReason},
		session: func(s *mqttproto.AudioSessionReport) {
			s.ItemGapNotApplicable, s.ItemGapReason = true, sessionStoppedReason
		},
		wantState: observation.StateNotApplicable, wantReason: sessionStoppedReason,
	},
	{
		name: "session item gap, measurement missed", signals: []observation.SignalID{SignalSessionItemGapMs, SignalSessionItemGapReason},
		session: func(s *mqttproto.AudioSessionReport) {
			s.ItemGapReason = "successor item did not reach a confirmed start"
		},
		wantState: observation.StateNotCollected, wantReason: "successor item did not reach a confirmed start",
	},
	{
		name: "session start, no start record", signals: startTriggerSignals,
		session:   func(s *mqttproto.AudioSessionReport) { s.StartTriggerNotApplicable = true },
		wantState: observation.StateNotApplicable, wantReason: "This node holds no start record for this session.",
	},
	{
		name: "session start, agent does not say", signals: startTriggerSignals,
		session:   func(s *mqttproto.AudioSessionReport) {},
		wantState: observation.StateNotCollected, wantReason: "this session has not started, or this node's build does not report how it started",
	},
	{
		name: "session start, started by the coordinator", signals: multisyncSignals,
		session:   func(s *mqttproto.AudioSessionReport) { s.StartTrigger = "coordinator" },
		wantState: observation.StateNotApplicable, wantReason: "This session did not start from a MultiSync START packet.",
	},
	{
		name: "session start, started by MultiSync", signals: multisyncSignals,
		session:   func(s *mqttproto.AudioSessionReport) { s.StartTrigger = "multisync" },
		wantState: observation.StateCurrent,
	},
}

func TestSubjectAbsentIsNotApplicableAndMissingReadingIsNotCollected(t *testing.T) {
	for _, c := range subjectCases {
		t.Run(c.name, func(t *testing.T) {
			obs := c.observe(t)
			for _, sig := range c.signals {
				got := c.find(t, obs, sig)
				if state := got.StateAt(sampleObservedAt); state != c.wantState {
					t.Errorf("%s state = %q, want %q", sig, state, c.wantState)
				}
				if got.Reason != c.wantReason {
					t.Errorf("%s reason = %q, want %q", sig, got.Reason, c.wantReason)
				}
			}
		})
	}
}

// TestNotApplicableGoesStaleWhenReportsStop proves ADR-056 decision 3: the
// row carries the report's own time and ages on the same rule as a value.
func TestNotApplicableGoesStaleWhenReportsStop(t *testing.T) {
	for _, c := range subjectCases {
		if c.wantState != observation.StateNotApplicable {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			obs := c.observe(t)
			for _, sig := range c.signals {
				got := c.find(t, obs, sig)
				if got.ObservedAt == nil || !got.ObservedAt.Equal(sampleObservedAt) {
					t.Errorf("%s observedAt = %v, want the report's own %s", sig, got.ObservedAt, sampleObservedAt)
				}
				quiet := sampleObservedAt.Add(DefaultValidFor + time.Second)
				if state := got.StateAt(quiet); state != observation.StateStale {
					t.Errorf("%s state after the node went quiet = %q, want %q", sig, state, observation.StateStale)
				}
			}
		})
	}
}

// TestNotApplicableNeedsAReportTime proves a report with no evidence time
// cannot establish that a subject is absent.
func TestNotApplicableNeedsAReportTime(t *testing.T) {
	p := samplePayloadWithSession(mqttproto.AudioSessionReport{SessionID: "sess-1", State: "stopped", Fault: "none"})
	p.ObservedAt = nil
	st := NewStore()
	st.Put("audio-01", p, time.Now())
	obs, _ := New(st).Poll(context.Background())

	for _, o := range obs {
		if o.Absence == observation.StateNotApplicable {
			t.Errorf("%s is not_applicable off a report with no observation time", o.Signal)
		}
	}
	if got := findSessionObs(t, obs, SignalSessionRestoreNextAttemptMs); got.Absence != observation.StateNotCollected {
		t.Errorf("restore.next_attempt_ms absence = %q, want %q", got.Absence, observation.StateNotCollected)
	}
}
