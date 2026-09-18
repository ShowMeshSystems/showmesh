package api

import (
	"context"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is ADR-051 decision 4's own fallback timer: after this tick
// has decided to dispatch an activation with an audio output, the
// coordinator waits a bounded window for the target node's own evidence
// that a MultiSync START packet already started that Cue's audio, before
// falling back to dispatching cue.activate with no scheduled instant
// (start on arrival), exactly as it always has for a Cue with no trigger
// at all. Decision 1 puts the real start decision on the node itself, on
// its own media clock, with no coordinator round trip in that path; this
// wait only decides how the coordinator's OWN evidence-gathering and
// bookkeeping dispatch (cue.activate) is scheduled, never the audience-
// visible start.

// audioSessionStartTriggerSignal, audioSessionTriggerSequenceFilenameSignal,
// audioSessionTriggerArrivalNsSignal, audioSessionStartLeadMsSignal, and
// audioSessionPreparedLateSignal are internal/coordinator/collector/
// nodeaudio's own identically-named Signal* constants, copied as literals
// for the same reason nightbackgroundaudio.go's audioSessionFadeStateSignalID
// and currentruns.go's bare "audio_session.*" string arguments already are:
// this package must not import a collector (TestPackageNeverImportsACollector's
// rule).
const (
	audioSessionStartTriggerSignal            observation.SignalID = "audio_session.start.trigger"
	audioSessionTriggerSequenceFilenameSignal observation.SignalID = "audio_session.start.trigger_sequence_filename"
	audioSessionTriggerArrivalNsSignal        observation.SignalID = "audio_session.start.trigger_arrival_ns"
	audioSessionStartLeadMsSignal             observation.SignalID = "audio_session.start.lead_ms"
	audioSessionPreparedLateSignal            observation.SignalID = "audio_session.start.prepared_late"
)

// multiSyncStartTriggerValue is the exact "multisync" string
// internal/coordinator/collector/nodeaudio's own sess.StartTrigger passes
// straight through from [mqttproto.AudioSessionReport.StartTrigger] — a
// wire vocabulary literal both sides of that boundary reproduce
// independently, matching cueActivationNodeOutcomeAuthorized's identical
// convention one file over (cueactivationdispatch.go).
const multiSyncStartTriggerValue = "multisync"

// multiSyncFallbackPollInterval is how often waitForMultiSyncStart re-reads
// nodeID's own push-cache evidence while its window is still open. Short
// enough that a real START packet's arrival is caught promptly relative to
// the whole window (RES-020's own 100ms lead would otherwise be swallowed
// by the poll granularity), long enough not to spin.
var multiSyncFallbackPollInterval = 50 * time.Millisecond

// multiSyncStartEvidence is what waitForMultiSyncStart found (or did not)
// for one node's own audio session by the time its window closed.
type multiSyncStartEvidence struct {
	// Triggered is true only when the node's own evidence showed
	// StartTrigger == "multisync", observed no earlier than the baseline
	// this activation's own tick began at — never a stale reading left
	// over from a PRECEDING Cue's own MultiSync start.
	Triggered bool

	TriggerSequenceFilename string
	TriggerArrivalNs        int64
	StartLeadMs             int
	PreparedLate            bool
}

// waitForMultiSyncStart bounded-polls h.deps.Audio's own push-cache
// evidence for nodeID's [cueactivation.AudioSessionID] session, up to
// window, for a MultiSync start reported no earlier than baseline. It
// returns as soon as matching evidence appears, never holding the full
// window once found. window <= 0 returns immediately with no evidence:
// the fallback fires at once, exactly as if no MultiSync trigger could
// ever exist for this Cue.
func (h *handlers) waitForMultiSyncStart(ctx context.Context, nodeID string, baseline time.Time, window time.Duration) multiSyncStartEvidence {
	if window <= 0 {
		return multiSyncStartEvidence{}
	}
	deadline := time.Now().Add(window)
	for {
		if ev, ok := h.readMultiSyncStartEvidence(nodeID, baseline); ok {
			return ev
		}
		if time.Now().After(deadline) {
			return multiSyncStartEvidence{}
		}
		select {
		case <-ctx.Done():
			return multiSyncStartEvidence{}
		case <-time.After(multiSyncFallbackPollInterval):
		}
	}
}

// readMultiSyncStartEvidence reads nodeID's own current audio session
// observations once, answering whether they already show a MultiSync
// start for THIS activation: ok is true only when StartTrigger is
// "multisync" and the observation's own ObservedAt is not before baseline
// — a report the node published before this activation's own tick began
// is evidence about whatever Cue preceded it, never this one.
func (h *handlers) readMultiSyncStartEvidence(nodeID string, baseline time.Time) (multiSyncStartEvidence, bool) {
	obs := h.deps.Audio.NodeAudioObservations(nodeID)
	trigger := latestAudioObservation(obs, audioSessionStartTriggerSignal)
	if trigger == nil || trigger.Absence != "" {
		return multiSyncStartEvidence{}, false
	}
	if triggerVal, _ := trigger.Value.(string); triggerVal != multiSyncStartTriggerValue {
		return multiSyncStartEvidence{}, false
	}
	if trigger.ObservedAt == nil || trigger.ObservedAt.Before(baseline) {
		return multiSyncStartEvidence{}, false
	}

	ev := multiSyncStartEvidence{Triggered: true}
	if o := latestAudioObservation(obs, audioSessionTriggerSequenceFilenameSignal); o != nil && o.Absence == "" {
		ev.TriggerSequenceFilename, _ = o.Value.(string)
	}
	if o := latestAudioObservation(obs, audioSessionTriggerArrivalNsSignal); o != nil && o.Absence == "" {
		if v, ok := o.Value.(int64); ok {
			ev.TriggerArrivalNs = v
		}
	}
	if o := latestAudioObservation(obs, audioSessionStartLeadMsSignal); o != nil && o.Absence == "" {
		if v, ok := o.Value.(int64); ok {
			ev.StartLeadMs = int(v)
		}
	}
	if o := latestAudioObservation(obs, audioSessionPreparedLateSignal); o != nil && o.Absence == "" {
		ev.PreparedLate, _ = o.Value.(bool)
	}
	return ev, true
}

// multiSyncFallbackWindow reads audio.settings' own MultisyncFallbackWindowMs
// live, defaulting to config.AudioSettingsDefaultPayload's shipped value
// when nothing has ever been configured or the read itself fails — a
// misread here must never block or lengthen an activation dispatch past
// the shipped default's own bound.
func (h *handlers) multiSyncFallbackWindow(ctx context.Context) time.Duration {
	settings, err := h.alignedStartSettings(ctx)
	if err != nil {
		h.logWarn("cue activation dispatch: read audio.settings for the multisync fallback window failed; using the shipped default", "error", err)
		return time.Duration(config.AudioSettingsDefaultPayload.MultisyncFallbackWindowMs) * time.Millisecond
	}
	return time.Duration(settings.MultisyncFallbackWindowMs) * time.Millisecond
}
