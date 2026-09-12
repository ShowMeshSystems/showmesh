// Package audioalignment appends program-to-LTC alignment samples onto an
// active audio_alignment_runs run, from the same audio reports
// nodeaudio.Store already receives.
package audioalignment

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// Store is the subset of *store.Store this package needs: finding the
// node's active run and appending a sample to it.
type Store interface {
	FindActiveAlignmentRun(ctx context.Context, nodeID string) (store.AlignmentRunRecord, error)
	AppendAlignmentSample(ctx context.Context, sample store.AlignmentSampleRecord) error
}

// Sink is the inventory.AudioSink shape, declared here rather than
// imported to avoid a package cycle; whatever Recorder wraps still
// receives every payload unchanged.
type Sink interface {
	Put(nodeID string, payload mqttproto.AudioPayload, receivedAt time.Time)
}

// alignmentStoreTimeout bounds every store call Put makes, matching
// inventory.requestTimeout: Put runs on the MQTT publish-routing worker
// inside inventory.Manager.handleAudio, and the store's single connection
// (store.go's SetMaxOpenConns(1)) means a hung call here stalls that
// worker exactly as requestTimeout exists to prevent one file over.
const alignmentStoreTimeout = 5 * time.Second

// Recorder wraps an inventory.AudioSink, also appending an alignment
// sample when the payload carries one and the node has an active run.
// It satisfies inventory.AudioSink itself.
type Recorder struct {
	next    Sink
	store   Store
	logger  *slog.Logger
	timeout time.Duration
}

// NewRecorder builds a Recorder forwarding every Put to next after
// recording. logger receives one Warn line per failed append; a failed
// append never blocks or alters the forwarded report.
func NewRecorder(next Sink, store Store, logger *slog.Logger) *Recorder {
	return &Recorder{next: next, store: store, logger: logger, timeout: alignmentStoreTimeout}
}

// Put forwards payload to the wrapped sink FIRST, so observation freshness
// never waits on this package, then appends an alignment sample if
// AlignmentMeasured is true and nodeID has an active run. A republished or
// racing tick is safe: the store dedups and no-ops itself. Both store
// calls run under one bounded timeout; on timeout, this logs and continues
// exactly like any other store error.
func (r *Recorder) Put(nodeID string, payload mqttproto.AudioPayload, receivedAt time.Time) {
	r.next.Put(nodeID, payload, receivedAt)

	if !payload.AlignmentMeasured || payload.AlignmentSampledAt == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()

	run, err := r.store.FindActiveAlignmentRun(ctx, nodeID)
	if err != nil {
		if !errors.Is(err, store.ErrAlignmentRunNotFound) {
			r.logger.Warn("audio alignment: find active run failed", "node_id", nodeID, "error", err)
		}
		return
	}
	if err := r.store.AppendAlignmentSample(ctx, store.AlignmentSampleRecord{
		RunID:     run.ID,
		SampledAt: *payload.AlignmentSampledAt,
		OffsetMs:  float64(payload.AlignmentOffsetMs),
		SessionID: payload.AlignmentSessionID,
	}); err != nil {
		r.logger.Warn("audio alignment: append sample failed", "node_id", nodeID, "run_id", run.ID, "error", err)
	}
}
