package agent

import (
	"context"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	agentclock "github.com/showmeshsystems/showmesh/internal/agent/clock"
	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// syncTraceSampleInterval spaces the trace's samples; FPP sends SYNC every 4 to 10 frames.
const syncTraceSampleInterval = time.Second

// syncTracer compares a MultiSync-started Cue's audio against the position
// FPP reports in each SYNC packet. It only measures; nothing here moves audio.
type syncTracer struct {
	mu     sync.Mutex
	active *syncTrace
}

type syncTrace struct {
	filename    string
	cueID       string
	startOffset time.Duration
	t0Ns        int64
	startPos    time.Duration

	samples             int
	firstAtNs, lastAtNs int64
	firstErr, lastErr   time.Duration
	minErr, maxErr      time.Duration
	lastSinkErrKnown    bool
}

// packetPosition is the sequence position pkt reports. FPP's START always
// reports zero, so a missing timeline only matters for SYNC.
func packetPosition(timeline *multisync.Timeline, pkt multisync.SyncPacket) time.Duration {
	if timeline == nil {
		if pkt.SecondsElapsed > 0 {
			return time.Duration(float64(pkt.SecondsElapsed) * float64(time.Second))
		}
		return 0
	}
	return timeline.PacketPosition(pkt)
}

// beginSyncTrace starts tracing a Cue whose audio was scheduled to present
// startPos at t0Ns on the media clock. A trace already running is summarized first.
func (c *multiSyncCueAudioTrigger) beginSyncTrace(filename, cueID string, startOffset time.Duration, t0Ns int64, startPos time.Duration) {
	c.syncTrace.mu.Lock()
	prev := c.syncTrace.active
	c.syncTrace.active = &syncTrace{filename: filename, cueID: cueID, startOffset: startOffset, t0Ns: t0Ns, startPos: startPos}
	c.syncTrace.mu.Unlock()
	c.logSyncTraceSummary(prev, "replaced")
}

// endSyncTrace closes filename's trace and logs its summary.
func (c *multiSyncCueAudioTrigger) endSyncTrace(filename, why string) {
	c.syncTrace.mu.Lock()
	prev := c.syncTrace.active
	if prev == nil || prev.filename != filename {
		c.syncTrace.mu.Unlock()
		return
	}
	c.syncTrace.active = nil
	c.syncTrace.mu.Unlock()
	c.logSyncTraceSummary(prev, why)
}

// traceSync records one SYNC packet against the running Cue's audio, at most
// once per syncTraceSampleInterval. error_ms is audio minus FPP; positive
// means the audio is ahead of the lights.
func (c *multiSyncCueAudioTrigger) traceSync(ctx context.Context, mgr *audio.Manager, filename string, arrival agentclock.MediaTime, sequenceAt time.Duration) {
	if !arrival.Valid {
		return
	}
	atNs := arrival.Time.UnixNano()

	c.syncTrace.mu.Lock()
	tr := c.syncTrace.active
	due := tr != nil && tr.filename == filename && atNs > tr.t0Ns &&
		(tr.samples == 0 || time.Duration(atNs-tr.lastAtNs) >= syncTraceSampleInterval)
	c.syncTrace.mu.Unlock()
	if !due {
		return
	}

	scheduledPos := tr.startPos + time.Duration(atNs-tr.t0Ns)
	audioPos := scheduledPos
	var sinkErr time.Duration
	sinkKnown := false
	if snap := mgr.TimelineSnapshot(ctx); snap.Scheduled && snap.SessionID == cueActivationAudioSessionID && snap.Measured {
		sinkErr = time.Duration(snap.ErrorMs) * time.Millisecond
		audioPos -= sinkErr
		sinkKnown = true
	}
	errDur := audioPos - (tr.startOffset + sequenceAt)

	c.syncTrace.mu.Lock()
	if c.syncTrace.active != tr {
		c.syncTrace.mu.Unlock()
		return
	}
	if tr.samples == 0 {
		tr.firstAtNs, tr.firstErr, tr.minErr, tr.maxErr = atNs, errDur, errDur, errDur
	}
	tr.samples++
	tr.lastAtNs, tr.lastErr, tr.lastSinkErrKnown = atNs, errDur, sinkKnown
	tr.minErr = min(tr.minErr, errDur)
	tr.maxErr = max(tr.maxErr, errDur)
	c.syncTrace.mu.Unlock()

	c.logger.Debug("multisync: cue audio against FPP",
		"cue_id", tr.cueID, "sequence_filename", filename,
		"fpp_position_ms", sequenceAt.Milliseconds(),
		"audio_position_ms", audioPos.Milliseconds(),
		"start_offset_ms", tr.startOffset.Milliseconds(),
		"sink_error_ms", sinkErr.Milliseconds(), "sink_error_known", sinkKnown,
		"error_ms", errDur.Milliseconds())
}

// logSyncTraceSummary logs one line per Cue that tells a constant offset
// (first and last error equal) from drift (drift_ms_per_min away from zero).
func (c *multiSyncCueAudioTrigger) logSyncTraceSummary(tr *syncTrace, why string) {
	if tr == nil || tr.samples == 0 {
		return
	}
	elapsed := time.Duration(tr.lastAtNs - tr.firstAtNs)
	var driftPerMin float64
	if elapsed >= 10*time.Second {
		driftPerMin = float64((tr.lastErr - tr.firstErr).Milliseconds()) / elapsed.Minutes()
	}
	c.logger.Info("multisync: cue audio against FPP over the song",
		"cue_id", tr.cueID, "sequence_filename", tr.filename, "ended_by", why,
		"samples", tr.samples, "seconds", int64(elapsed.Seconds()),
		"first_error_ms", tr.firstErr.Milliseconds(), "last_error_ms", tr.lastErr.Milliseconds(),
		"min_error_ms", tr.minErr.Milliseconds(), "max_error_ms", tr.maxErr.Milliseconds(),
		"drift_ms_per_min", driftPerMin, "sink_error_known", tr.lastSinkErrKnown)
}
