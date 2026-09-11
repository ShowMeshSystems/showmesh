package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is the coordinator-side surface for a long-run drift
// recording: start/stop/get/list against h.deps.AlignmentRuns.

const maxAlignmentRunStopRequestBodyBytes = 4 * 1024

var alignmentRunStopRequestFields = map[string]bool{"reason": true}

// decodeAlignmentRunStopRequestBody decodes body into a
// [v1.AudioAlignmentRunStopRequest]. An empty body decodes to the zero
// value: reason is optional.
func decodeAlignmentRunStopRequestBody(body io.Reader) (v1.AudioAlignmentRunStopRequest, error) {
	dec := json.NewDecoder(io.LimitReader(body, maxAlignmentRunStopRequestBodyBytes+1))

	var top map[string]json.RawMessage
	if err := dec.Decode(&top); err != nil {
		if errors.Is(err, io.EOF) {
			return v1.AudioAlignmentRunStopRequest{}, nil
		}
		return v1.AudioAlignmentRunStopRequest{}, fmt.Errorf(`request body must be a JSON object matching {"reason":string?}: %w`, err)
	}
	for key := range top {
		if !alignmentRunStopRequestFields[key] {
			return v1.AudioAlignmentRunStopRequest{}, fmt.Errorf(`unknown field %q; the accepted field is "reason"`, key)
		}
	}
	var req v1.AudioAlignmentRunStopRequest
	if raw, ok := top["reason"]; ok {
		if err := json.Unmarshal(raw, &req.Reason); err != nil {
			return v1.AudioAlignmentRunStopRequest{}, fmt.Errorf(`"reason" must be a string: %w`, err)
		}
	}
	return req, nil
}

func mapAlignmentRun(rec store.AlignmentRunRecord) v1.AudioAlignmentRun {
	return v1.AudioAlignmentRun{
		ID:         rec.ID,
		NodeID:     rec.NodeID,
		StartedAt:  formatTime(rec.StartedAt),
		StoppedAt:  formatTimePtr(rec.StoppedAt),
		StartedBy:  rec.StartedBy,
		StoppedBy:  rec.StoppedBy,
		StopReason: rec.StopReason,
	}
}

// handleStartAlignmentRun serves POST /nodes/{nodeId}/audio/alignment-runs.
func (h *handlers) handleStartAlignmentRun(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	nodeID := r.PathValue("nodeId")
	if err := mqttproto.ValidateNodeID(nodeID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("nodeId is not a syntactically valid node ID: "+err.Error()))
		return
	}

	ac := authFromContext(r.Context()) // writeGuard has already required ac.ok

	rec, err := h.deps.AlignmentRuns.CreateAlignmentRun(r.Context(), store.AlignmentRunRecord{
		ID: uuid.NewString(), NodeID: nodeID, StartedBy: ac.result.Principal.Name,
	})
	var conflict *store.AlignmentRunAlreadyActiveError
	if errors.As(err, &conflict) {
		writeProblem(w, h.logger, now, alignmentRunAlreadyActiveProblem(conflict.Active.ID))
		return
	}
	if err != nil {
		h.writeInternalError(w, now, "start audio alignment run", err)
		return
	}

	jsonWrite(w, v1.AudioAlignmentRunResponse{ServerTime: formatTime(now), Run: mapAlignmentRun(rec)})
}

// handleStopAlignmentRun serves
// POST /nodes/{nodeId}/audio/alignment-runs/{runId}/stop.
func (h *handlers) handleStopAlignmentRun(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	runID := r.PathValue("runId")

	body, err := decodeAlignmentRunStopRequestBody(r.Body)
	if err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
		return
	}
	ac := authFromContext(r.Context())

	rec, err := h.deps.AlignmentRuns.StopAlignmentRun(r.Context(), runID, ac.result.Principal.Name, body.Reason)
	if errors.Is(err, store.ErrAlignmentRunNotFound) {
		writeProblem(w, h.logger, now, resourceNotFoundProblem(fmt.Sprintf("no active audio alignment run with id %q exists", runID)))
		return
	}
	if err != nil {
		h.writeInternalError(w, now, "stop audio alignment run", err)
		return
	}

	jsonWrite(w, v1.AudioAlignmentRunResponse{ServerTime: formatTime(now), Run: mapAlignmentRun(rec)})
}

// handleListAlignmentRuns serves GET /nodes/{nodeId}/audio/alignment-runs.
func (h *handlers) handleListAlignmentRuns(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	nodeID := r.PathValue("nodeId")
	if err := mqttproto.ValidateNodeID(nodeID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("nodeId is not a syntactically valid node ID: "+err.Error()))
		return
	}

	runs, err := h.deps.AlignmentRuns.ListAlignmentRuns(r.Context(), nodeID)
	if err != nil {
		h.writeInternalError(w, now, "list audio alignment runs", err)
		return
	}

	out := make([]v1.AudioAlignmentRun, 0, len(runs))
	for _, rec := range runs {
		out = append(out, mapAlignmentRun(rec))
	}
	jsonWrite(w, v1.AudioAlignmentRunListResponse{ServerTime: formatTime(now), Runs: out})
}

// handleGetAlignmentRun serves GET /nodes/{nodeId}/audio/alignment-runs/{runId}.
func (h *handlers) handleGetAlignmentRun(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	runID := r.PathValue("runId")

	rec, samples, err := h.deps.AlignmentRuns.GetAlignmentRun(r.Context(), runID)
	if errors.Is(err, store.ErrAlignmentRunNotFound) {
		writeProblem(w, h.logger, now, resourceNotFoundProblem(fmt.Sprintf("no audio alignment run with id %q exists", runID)))
		return
	}
	if err != nil {
		h.writeInternalError(w, now, "get audio alignment run", err)
		return
	}

	outSamples := make([]v1.AudioAlignmentSample, 0, len(samples))
	for _, s := range samples {
		outSamples = append(outSamples, v1.AudioAlignmentSample{
			SampledAt: formatTime(s.SampledAt), OffsetMs: s.OffsetMs, SessionID: s.SessionID,
		})
	}

	jsonWrite(w, v1.AudioAlignmentRunDetailResponse{
		ServerTime: formatTime(now), Run: mapAlignmentRun(rec), Samples: outSamples,
		Summary: summarizeAlignmentSamples(samples),
	})
}

// summarizeAlignmentSamples reduces samples (ascending sampled_at order)
// to the max excursion and drift rate. Fewer than two samples reports a
// nil rate with a reason, never zero.
func summarizeAlignmentSamples(samples []store.AlignmentSampleRecord) v1.AudioAlignmentRunSummary {
	summary := v1.AudioAlignmentRunSummary{SampleCount: len(samples)}
	if len(samples) == 0 {
		summary.DriftRateUnavailableReason = "no samples recorded"
		return summary
	}

	first := formatTime(samples[0].SampledAt)
	last := formatTime(samples[len(samples)-1].SampledAt)
	summary.FirstSampleAt = &first
	summary.LastSampleAt = &last

	maxIdx := 0
	for i, s := range samples {
		if math.Abs(s.OffsetMs) > math.Abs(samples[maxIdx].OffsetMs) {
			maxIdx = i
		}
	}
	maxOffset := samples[maxIdx].OffsetMs
	maxAt := formatTime(samples[maxIdx].SampledAt)
	summary.MaxExcursionOffsetMs = &maxOffset
	summary.MaxExcursionSampledAt = &maxAt

	if len(samples) < 2 {
		summary.DriftRateUnavailableReason = "fewer than two samples: no slope can be computed"
		return summary
	}

	rate, ok := leastSquaresSlopeMsPerHour(samples)
	if !ok {
		summary.DriftRateUnavailableReason = "every sample shares the same timestamp: no slope can be computed"
		return summary
	}
	summary.DriftRateMsPerHour = &rate
	return summary
}

// leastSquaresSlopeMsPerHour fits offset against elapsed hours since
// samples[0]. ok is false only when every sample shares one timestamp,
// which leaves the slope undefined rather than zero.
func leastSquaresSlopeMsPerHour(samples []store.AlignmentSampleRecord) (rate float64, ok bool) {
	t0 := samples[0].SampledAt
	n := float64(len(samples))

	var sumX, sumY, sumXY, sumXX float64
	for _, s := range samples {
		x := s.SampledAt.Sub(t0).Hours()
		y := s.OffsetMs
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}
	denominator := n*sumXX - sumX*sumX
	if denominator == 0 {
		return 0, false
	}
	return (n*sumXY - sumX*sumY) / denominator, true
}
