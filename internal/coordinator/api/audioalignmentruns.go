package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is the coordinator-side surface for a long-run drift
// recording: start/stop/get/list against h.deps.AlignmentRuns, except
// start and stop, which are coordinator-local state changes composed with
// their ADR-024 decision 11 audit entry via h.deps.Identity.AuditedWrite
// and the concrete [store.Tx] it hands the caller, see
// handleStartAlignmentRun/handleStopAlignmentRun.

const maxAlignmentRunStopRequestBodyBytes = 4 * 1024

var alignmentRunStopRequestFields = map[string]bool{"reason": true}

// defaultAlignmentSampleLimit/maxAlignmentSampleLimit bound the "limit"
// query parameter on GET .../alignment-runs/{runId}: nothing auto-stops a
// run, so a forgotten one can accumulate hundreds of thousands of samples,
// and this response must never grow unbounded just because the run did.
const (
	defaultAlignmentSampleLimit = 5000
	maxAlignmentSampleLimit     = 50000
)

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

// parseAlignmentSampleLimit reads the optional "limit" query parameter:
// absent defaults to defaultAlignmentSampleLimit; present must be a
// positive integer no greater than maxAlignmentSampleLimit.
func parseAlignmentSampleLimit(r *http.Request) (int, *v1.Problem) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return defaultAlignmentSampleLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		p := invalidParameterProblem(fmt.Sprintf("limit must be a positive integer, got %q", raw))
		return 0, &p
	}
	if n > maxAlignmentSampleLimit {
		p := invalidParameterProblem(fmt.Sprintf("limit must be at most %d, got %d", maxAlignmentSampleLimit, n))
		return 0, &p
	}
	return n, nil
}

func mapAlignmentRun(rec store.AlignmentRunRecord) v1.AudioAlignmentRun {
	return v1.AudioAlignmentRun{
		ID:                   rec.ID,
		NodeID:               rec.NodeID,
		StartedAt:            formatTime(rec.StartedAt),
		StoppedAt:            formatTimePtr(rec.StoppedAt),
		StartedBy:            rec.StartedBy,
		StartedByPrincipalID: rec.StartedByPrincipalID,
		StoppedBy:            rec.StoppedBy,
		StoppedByPrincipalID: rec.StoppedByPrincipalID,
		StopReason:           rec.StopReason,
	}
}

func mapAlignmentRunSummary(s store.AlignmentRunSummary) v1.AudioAlignmentRunSummary {
	return v1.AudioAlignmentRunSummary{
		SampleCount:                s.SampleCount,
		FirstSampleAt:              formatTimePtr(s.FirstSampleAt),
		LastSampleAt:               formatTimePtr(s.LastSampleAt),
		MaxExcursionOffsetMs:       s.MaxExcursionOffsetMs,
		MaxExcursionSampledAt:      formatTimePtr(s.MaxExcursionSampledAt),
		DriftRateMsPerHour:         s.DriftRateMsPerHour,
		DriftRateUnavailableReason: s.DriftRateUnavailableReason,
	}
}

// alignmentRunNotFoundForNode is the response an unknown run and a run
// that belongs to a different node render identically as, on both get and
// stop: a caller must never learn a run id exists under some OTHER node's
// path than the one requested.
func alignmentRunNotFoundForNode(runID string) v1.Problem {
	return resourceNotFoundProblem(fmt.Sprintf("no audio alignment run with id %q exists", runID))
}

// handleStartAlignmentRun serves POST /nodes/{nodeId}/audio/alignment-runs,
// composing the run's creation with its ADR-024 decision 11 audit entry in
// one transaction (identity.Service.AuditedWrite), matching
// discovery.go's handlePromoteNode.
func (h *handlers) handleStartAlignmentRun(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	nodeID := r.PathValue("nodeId")
	if err := mqttproto.ValidateNodeID(nodeID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("nodeId is not a syntactically valid node ID: "+err.Error()))
		return
	}

	ac := authFromContext(ctx) // writeGuard has already required ac.ok

	runID := uuid.NewString()
	var rec store.AlignmentRunRecord
	err := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		out, err := tx.CreateAlignmentRun(ctx, store.AlignmentRunRecord{
			ID: runID, NodeID: nodeID,
			StartedBy: ac.result.Principal.Name, StartedByPrincipalID: ac.result.Principal.ID,
		})
		if err != nil {
			return identity.AuditEntry{}, err
		}
		rec = out
		return identity.AuditEntry{
			Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
			Action: "audio.alignment_run.start", Target: rec.ID,
			Params: map[string]any{"nodeId": nodeID},
			Kind:   identity.AuditAdmin,
		}, nil
	})
	var conflict *store.AlignmentRunAlreadyActiveError
	if errors.As(err, &conflict) {
		writeProblem(w, h.logger, now, alignmentRunAlreadyActiveProblem(conflict.Active.ID))
		return
	}
	if err != nil {
		// Whether this is identity.ErrAuditWrite (the audit append itself
		// failed) or fn's own error, ADR-024 decision 11's same-transaction
		// rule has already rolled the whole write back, matching
		// handlePromoteNode's identical posture (discovery.go), so both
		// cases are reported identically, and no run row exists afterward.
		h.writeInternalError(w, now, "start audio alignment run", err)
		return
	}

	jsonWrite(w, v1.AudioAlignmentRunResponse{ServerTime: formatTime(now), Run: mapAlignmentRun(rec)})
}

// handleStopAlignmentRun serves
// POST /nodes/{nodeId}/audio/alignment-runs/{runId}/stop, composing the
// stop with its ADR-024 decision 11 audit entry in one transaction. The
// store scopes the stop to nodeID (store.Tx.StopAlignmentRun), so a run
// cannot be stopped through any node's path but its own: a mismatch reads
// identically to an unknown run.
func (h *handlers) handleStopAlignmentRun(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	nodeID := r.PathValue("nodeId")
	if err := mqttproto.ValidateNodeID(nodeID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("nodeId is not a syntactically valid node ID: "+err.Error()))
		return
	}
	runID := r.PathValue("runId")

	body, err := decodeAlignmentRunStopRequestBody(r.Body)
	if err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
		return
	}
	ac := authFromContext(ctx)

	var rec store.AlignmentRunRecord
	writeErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		out, err := tx.StopAlignmentRun(ctx, runID, nodeID, ac.result.Principal.Name, ac.result.Principal.ID, body.Reason)
		if err != nil {
			return identity.AuditEntry{}, err
		}
		rec = out
		return identity.AuditEntry{
			Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
			Action: "audio.alignment_run.stop", Target: runID,
			Params: map[string]any{"nodeId": nodeID},
			Kind:   identity.AuditAdmin,
		}, nil
	})
	if errors.Is(writeErr, store.ErrAlignmentRunNotFound) {
		writeProblem(w, h.logger, now, resourceNotFoundProblem(
			"no active audio alignment run with that id exists for this node; it is unknown or already stopped"))
		return
	}
	if writeErr != nil {
		h.writeInternalError(w, now, "stop audio alignment run", writeErr)
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
// nodeId in the path is validated against the run's own recorded NodeID:
// reading a real run id under the wrong node's path answers exactly as an
// unknown run would, never leaking that the id exists elsewhere.
func (h *handlers) handleGetAlignmentRun(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	nodeID := r.PathValue("nodeId")
	if err := mqttproto.ValidateNodeID(nodeID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("nodeId is not a syntactically valid node ID: "+err.Error()))
		return
	}
	runID := r.PathValue("runId")

	limit, problem := parseAlignmentSampleLimit(r)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}

	rec, samples, truncated, summary, err := h.deps.AlignmentRuns.GetAlignmentRun(r.Context(), runID, limit)
	if errors.Is(err, store.ErrAlignmentRunNotFound) {
		writeProblem(w, h.logger, now, alignmentRunNotFoundForNode(runID))
		return
	}
	if err != nil {
		h.writeInternalError(w, now, "get audio alignment run", err)
		return
	}
	if rec.NodeID != nodeID {
		writeProblem(w, h.logger, now, alignmentRunNotFoundForNode(runID))
		return
	}

	outSamples := make([]v1.AudioAlignmentSample, 0, len(samples))
	for _, s := range samples {
		outSamples = append(outSamples, v1.AudioAlignmentSample{
			SampledAt: formatTime(s.SampledAt), OffsetMs: s.OffsetMs, SessionID: s.SessionID,
		})
	}

	jsonWrite(w, v1.AudioAlignmentRunDetailResponse{
		ServerTime: formatTime(now), Run: mapAlignmentRun(rec), Samples: outSamples, Truncated: truncated,
		Summary: mapAlignmentRunSummary(summary),
	})
}
