package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/audiosched"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is the aligned multi-node start: prepare every target, take
// the media-clock reading the prepare results carry, pick ONE instant
// (internal/coordinator/audiosched), and start every target at that same
// instant.
//
// The instant is chosen here and not per node on purpose. RES-019
// section 6's whole point is one instant on one shared clock: a
// coordinator that computed a value per node would produce a set of
// different times by construction, which is the defect this endpoint
// exists to prevent rather than a detail of it.
//
// The readings ride the PREPARE RESULTS dispatched by this very request,
// never a retained observation: a retained media-clock instant would be
// served from whenever that node last published while looking exactly
// like a current one.

// maxAlignedStartTargets bounds one request's fan-out. Each target costs
// a prepare and a start, each of which waits on its own result, so an
// unbounded list would let one request hold a handler for an unbounded
// time. Sized well past any plausible audio-node count rather than tuned.
const maxAlignedStartTargets = 32

func (h *handlers) handleAlignedAudioStart(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	_ = http.NewResponseController(w).SetWriteDeadline(now.Add(alignedStartWriteDeadline()))

	sessionID := r.PathValue("sessionId")
	if !audioSessionIDPattern.MatchString(sessionID) {
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf("sessionId %q is not a safe identifier (must match %s)", sessionID, audioSessionIDPattern.String())))
		return
	}

	body, err := decodeAlignedAudioStartRequestBody(r.Body)
	if err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
		return
	}
	if len(body.NodeIDs) == 0 {
		writeProblem(w, h.logger, now, invalidParameterProblem(`"nodeIds" must name at least one target node`))
		return
	}
	if len(body.NodeIDs) > maxAlignedStartTargets {
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf(
			`"nodeIds" names %d nodes; at most %d may be started together`, len(body.NodeIDs), maxAlignedStartTargets)))
		return
	}
	seen := map[string]bool{}
	for _, nodeID := range body.NodeIDs {
		if err := mqttproto.ValidateNodeID(nodeID); err != nil {
			writeProblem(w, h.logger, now, invalidParameterProblem("nodeIds contains a node ID that is not syntactically valid: "+err.Error()))
			return
		}
		if seen[nodeID] {
			// Starting one node twice in the same aligned group would
			// dispatch two starts at one instant and report two results
			// for one node, so it is refused rather than deduplicated
			// silently.
			writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf("nodeIds names %q more than once", nodeID)))
			return
		}
		seen[nodeID] = true
	}

	settings, err := h.alignedStartSettings(ctx)
	if err != nil {
		h.writeInternalError(w, now, "read audio.settings for the scheduled-start terms", err)
		return
	}

	ac := authFromContext(ctx)
	issuerID := ac.result.Principal.ID
	if issuerID == "" {
		issuerID = "unknown"
	}
	key := body.IdempotencyKey
	if key == "" {
		key = uuid.NewString()
	}

	resp := v1.AlignedAudioStartResponse{SessionID: sessionID}
	readiness := make([]audiosched.Readiness, 0, len(body.NodeIDs))

	for _, nodeID := range body.NodeIDs {
		var evidence map[string]any
		in := h.alignedStartDispatchInput(ac, "audio.session.prepare", nodeID, sessionID, body.Revision, key+":prepare:"+nodeID, r)
		in.OnEvidence = func(v map[string]any) { evidence = v }

		result, problem, err := h.executeAudioSessionDispatch(ctx, h.now(), in)
		if err != nil {
			h.writeInternalError(w, now, "dispatch aligned prepare", err)
			return
		}
		if problem != nil {
			writeProblem(w, h.logger, now, *problem)
			return
		}
		resp.Prepares = append(resp.Prepares, result)

		holdsClock, err := h.nodeHoldsMediaClock(ctx, nodeID)
		if err != nil {
			h.writeInternalError(w, now, "read audio.node config", err)
			return
		}
		readiness = append(readiness, readinessFromEvidence(nodeID, holdsClock, evidence))
	}

	sel, selErr := audiosched.Select(readiness, settings.ScheduledStartDeliveryBoundMs, settings.ScheduledStartMarginMs)
	var startParams map[string]any
	if selErr != nil {
		var noClock *audiosched.ErrNoUsableClock
		if !errors.As(selErr, &noClock) {
			h.writeInternalError(w, now, "select an aligned start instant", selErr)
			return
		}
		// Every target starts on arrival, exactly as it did before this
		// endpoint existed: RES-019 and the shipped node behaviour both
		// say a node without a usable clock keeps start-on-arrival, and
		// the show continuing matters more than the alignment. It is
		// reported as unaligned rather than as success, because an
		// operator who asked for an aligned start and silently got an
		// unaligned one has been told something false.
		resp.Aligned = false
		resp.UnalignedReason = audiosched.DescribeUnscheduled(selErr)
	} else {
		resp.Aligned = true
		resp.Selection = &v1.AlignedAudioStartSelection{
			ScheduledAtNs: sel.ScheduledAtNs, ClockNodeID: sel.ClockNodeID, LeadNs: sel.LeadNs,
			PrerollNs: sel.PrerollNs, PrerollReportedBy: sel.PrerollReportedBy,
			DeliveryBoundNs: sel.DeliveryBoundNs, MarginNs: sel.MarginNs,
			ClockErrorBoundKnown: sel.ClockErrorBoundKnown, ClockErrorBoundNs: sel.ClockErrorBoundNs,
		}
		// json.Number, never an int64 or a float64: this value is around
		// 1.79e18 and reaches the node through a JSON encode, where a
		// float64 would round it and an int64 would be correct only until
		// something in the path widened it. The digits are carried
		// verbatim.
		startParams = map[string]any{
			pkgaudio.ParamScheduledAtNs: json.Number(fmt.Sprintf("%d", sel.ScheduledAtNs)),
		}
	}

	for _, nodeID := range body.NodeIDs {
		in := h.alignedStartDispatchInput(ac, "audio.session.start", nodeID, sessionID, body.Revision, key+":start:"+nodeID, r)
		for k, v := range startParams {
			in.Params[k] = v
		}
		result, problem, err := h.executeAudioSessionDispatch(ctx, h.now(), in)
		if err != nil {
			h.writeInternalError(w, now, "dispatch aligned start", err)
			return
		}
		if problem != nil {
			writeProblem(w, h.logger, now, *problem)
			return
		}
		resp.Starts = append(resp.Starts, result)
	}

	resp.ServerTime = formatTime(h.now())
	jsonWrite(w, resp)
}

// alignedStartWriteDeadline covers a prepare and a start for every
// target, each of which waits on its own result deadline.
func alignedStartWriteDeadline() time.Duration {
	return 2*maxAlignedStartTargets*audioCommandConfirmDeadline + audioHandlerWriteDeadlineMargin
}

func (h *handlers) alignedStartDispatchInput(ac authContext, action, nodeID, sessionID string, revision uint64, key string, r *http.Request) AudioDispatchInput {
	return AudioDispatchInput{
		Action: action, NodeID: nodeID, SessionID: sessionID,
		Params: map[string]any{
			"sessionId":    sessionID,
			"invocationId": key,
			"revision":     revision,
		},
		Revision: revision, IdempotencyKey: key,
		IssuerID: alignedStartIssuerID(ac), IssuerName: ac.result.Principal.Name,
		IssuerForm: ac.result.Form, IssuerCredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
	}
}

func alignedStartIssuerID(ac authContext) string {
	if ac.result.Principal.ID == "" {
		return "unknown"
	}
	return ac.result.Principal.ID
}

// nodeHoldsMediaClock reports whether nodeID carries the program plus LTC
// role: its audio.node configuration declares an LTC route, which is what
// makes it the node RES-019 section 6 takes the shared clock from. A node
// with no audio.node configuration at all holds no clock and is not an
// error: it is simply not the clock holder.
func (h *handlers) nodeHoldsMediaClock(ctx context.Context, nodeID string) (bool, error) {
	rev, _, problem, err := h.getActiveShowConfigRevision(ctx, config.AudioNodeConfigKind, nodeID)
	if err != nil {
		return false, err
	}
	if problem != nil {
		return false, nil
	}
	var payload config.AudioNodePayload
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
		return false, err
	}
	return payload.LTCRoute != "", nil
}

// readinessFromEvidence decodes one prepare result's media-clock fields
// (pkg/audio's Result* constants) into the selection's input.
//
// Everything is optional and everything absent means absent. A node that
// reported no validity flag is NOT valid: treating a missing flag as true
// would invent a clock, which is the one failure the flag exists to make
// impossible.
func readinessFromEvidence(nodeID string, holdsClock bool, evidence map[string]any) audiosched.Readiness {
	out := audiosched.Readiness{NodeID: nodeID, HoldsMediaClock: holdsClock}
	if evidence == nil {
		out.MediaClockReason = "the node's prepare reported no evidence at all"
		return out
	}
	valid, _ := evidence[pkgaudio.ResultMediaClockValid].(bool)
	out.MediaClockValid = valid
	out.MediaClockReason, _ = evidence[pkgaudio.ResultMediaClockReason].(string)
	if out.MediaClockValid {
		if ns, ok := evidenceInt64(evidence[pkgaudio.ResultMediaClockNowNs]); ok {
			out.MediaClockNowNs = ns
		} else {
			out.MediaClockValid = false
			out.MediaClockReason = "the node reported a valid media clock but no readable reading"
		}
	}
	if known, _ := evidence[pkgaudio.ResultMediaClockErrorBoundKnown].(bool); known {
		if ns, ok := evidenceInt64(evidence[pkgaudio.ResultMediaClockErrorBoundNs]); ok {
			out.ErrorBoundKnown, out.ErrorBoundNs = true, ns
		}
	}
	if ms, ok := evidenceInt64(evidence[pkgaudio.ResultPrerollMs]); ok {
		out.PrerollKnown, out.PrerollMs = true, ms
	}
	return out
}

// evidenceInt64 reads an evidence number that may have decoded either as
// a json.Number (mqttproto preserves the nanosecond-scale fields exactly)
// or as a float64 (every other number). A float64 is accepted only when
// it is integral and inside the exactly-representable range, because
// outside it the value has already been rounded and is not the number the
// node sent.
func evidenceInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case json.Number:
		parsed, err := n.Int64()
		return parsed, err == nil
	case float64:
		const maxExact = float64(1 << 53)
		if n != float64(int64(n)) || n > maxExact || n < -maxExact {
			return 0, false
		}
		return int64(n), true
	case int64:
		return n, true
	default:
		return 0, false
	}
}

// alignedStartSettings reads the two scheduled-start terms from
// audio.settings, falling back to the shipped default payload when
// nothing has ever been written (that object never 404s).
func (h *handlers) alignedStartSettings(ctx context.Context) (config.AudioSettingsPayload, error) {
	rev, _, problem, err := h.getActiveShowConfigRevision(ctx, config.AudioSettingsConfigKind, config.AudioSettingsConfigObjectID)
	if err != nil {
		return config.AudioSettingsPayload{}, err
	}
	if problem != nil {
		return config.AudioSettingsDefaultPayload, nil
	}
	payload, verr := config.DecodeAudioSettingsPayload(rev.PayloadJSON)
	if verr != nil {
		return config.AudioSettingsPayload{}, fmt.Errorf("stored audio.settings does not decode: %s", verr.Detail)
	}
	return payload, nil
}

var alignedStartRequestFields = map[string]bool{"revision": true, "idempotencyKey": true, "nodeIds": true}

func decodeAlignedAudioStartRequestBody(body io.Reader) (v1.AlignedAudioStartRequest, error) {
	dec := json.NewDecoder(io.LimitReader(body, maxAudioCommandRequestBodyBytes+1))
	var top map[string]json.RawMessage
	if err := dec.Decode(&top); err != nil {
		return v1.AlignedAudioStartRequest{}, fmt.Errorf(
			`request body must be a JSON object matching {"revision":number,"idempotencyKey":string?,"nodeIds":[string]}: %w`, err)
	}
	for key := range top {
		if !alignedStartRequestFields[key] {
			return v1.AlignedAudioStartRequest{}, fmt.Errorf(
				`unknown field %q; the accepted fields are "revision","idempotencyKey","nodeIds"`, key)
		}
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return v1.AlignedAudioStartRequest{}, errors.New("request body must contain exactly one JSON object; unexpected data after it")
		}
		return v1.AlignedAudioStartRequest{}, fmt.Errorf("reading request body: %w", err)
	}

	var req v1.AlignedAudioStartRequest
	if raw, ok := top["revision"]; ok {
		if err := json.Unmarshal(raw, &req.Revision); err != nil {
			return v1.AlignedAudioStartRequest{}, fmt.Errorf(`"revision" must be a non-negative integer: %w`, err)
		}
	}
	if raw, ok := top["idempotencyKey"]; ok {
		if err := json.Unmarshal(raw, &req.IdempotencyKey); err != nil {
			return v1.AlignedAudioStartRequest{}, fmt.Errorf(`"idempotencyKey" must be a string: %w`, err)
		}
	}
	if raw, ok := top["nodeIds"]; ok {
		if err := json.Unmarshal(raw, &req.NodeIDs); err != nil {
			return v1.AlignedAudioStartRequest{}, fmt.Errorf(`"nodeIds" must be an array of strings: %w`, err)
		}
	}
	return req, nil
}
