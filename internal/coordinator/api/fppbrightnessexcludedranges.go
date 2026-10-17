package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppcommand"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// The operator write of the channel ranges one FPP host's brightness
// ceiling leaves alone: PUT /api/v1/fpp/{instanceId}/brightness/excluded-ranges.
// It replaces the plugin's whole list and never touches the ceiling or
// the transition gain.

// scopeFPPBrightnessExcludedRanges is fpp:command, like the ceiling write.
var scopeFPPBrightnessExcludedRanges = identity.ScopeFPPCommand

// auditActionFPPSetBrightnessExcludedRanges is this route's audit action.
const auditActionFPPSetBrightnessExcludedRanges = "fpp.set_brightness_excluded_ranges"

// fppBrightnessExcludedRangesAction is the wire action name this route's
// result carries. Like the ceiling's, it is not an FPP native command.
const fppBrightnessExcludedRangesAction = "setBrightnessExcludedRanges"

// maxFPPBrightnessExcludedRangesRequestBodyBytes bounds this route's body.
const maxFPPBrightnessExcludedRangesRequestBodyBytes = 64 << 10 // 64 KiB

// fppMaxChannelNumber is the highest channel FPP addresses, 8 x 1024 x 1024.
const fppMaxChannelNumber = 8388608

// fppBrightnessExcludedRangesWriter performs one write against baseURL and
// reports the list the plugin read back, nil when it did not match in
// time. Only a test ever substitutes it.
type fppBrightnessExcludedRangesWriter func(ctx context.Context, baseURL string, ranges []v1.FPPChannelRange) (fppcommand.ExcludedRangesOutcome, *[]v1.FPPChannelRange, error)

func writeFPPBrightnessExcludedRanges(ctx context.Context, baseURL string, ranges []v1.FPPChannelRange) (fppcommand.ExcludedRangesOutcome, *[]v1.FPPChannelRange, error) {
	client, err := fppcommand.New(baseURL, fppcommand.Options{})
	if err != nil {
		return fppcommand.ExcludedRangesOutcome{}, nil, fmt.Errorf("api: building the fpp client for the excluded ranges write: %w", err)
	}
	wire := make([]fppcommand.ChannelRange, 0, len(ranges))
	for _, r := range ranges {
		wire = append(wire, fppcommand.ChannelRange{StartChannel: r.StartChannel, ChannelCount: r.ChannelCount})
	}
	outcome, err := client.SetBrightnessExcludedRanges(ctx, wire)
	if err != nil {
		return outcome, nil, err
	}
	return outcome, readBackFPPBrightnessExcludedRanges(ctx, baseURL, ranges), nil
}

// readBackFPPBrightnessExcludedRanges polls the plugin's brightness route
// until it reports the same channels as want or the ceiling write's
// budget runs out. Nil means the new list was not seen.
func readBackFPPBrightnessExcludedRanges(ctx context.Context, baseURL string, want []v1.FPPChannelRange) *[]v1.FPPChannelRange {
	deadline := time.Now().Add(fppBrightnessReadBackBudget)
	readCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	client := &http.Client{Timeout: fppBrightnessReadBackBudget}
	for {
		if got, ok := fetchFPPBrightnessExcludedRanges(readCtx, client, baseURL); ok && sameChannels(got, want) {
			return &got
		}
		if !time.Now().Add(250 * time.Millisecond).Before(deadline) {
			return nil
		}
		select {
		case <-readCtx.Done():
			return nil
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// fetchFPPBrightnessExcludedRanges reads the plugin's current list. Any
// failure, and a plugin too old to report the list, is ok false.
func fetchFPPBrightnessExcludedRanges(ctx context.Context, client *http.Client, baseURL string) ([]v1.FPPChannelRange, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(baseURL, "/")+fppPluginBrightnessPath, nil)
	if err != nil {
		return nil, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}
	var body struct {
		ExcludedRanges *[]v1.FPPChannelRange `json:"excludedRanges"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&body); err != nil || body.ExcludedRanges == nil {
		return nil, false
	}
	return append([]v1.FPPChannelRange{}, *body.ExcludedRanges...), true
}

// sameChannels reports whether a and b cover exactly the same channels,
// so a plugin that reorders or joins touching ranges still confirms.
func sameChannels(a, b []v1.FPPChannelRange) bool {
	x, y := mergedChannelSpans(a), mergedChannelSpans(b)
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// mergedChannelSpans returns ranges as sorted [first, last] spans with
// touching and overlapping ones joined.
func mergedChannelSpans(ranges []v1.FPPChannelRange) [][2]int {
	spans := make([][2]int, 0, len(ranges))
	for _, r := range ranges {
		spans = append(spans, [2]int{r.StartChannel, r.StartChannel + r.ChannelCount - 1})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	merged := spans[:0]
	for _, s := range spans {
		if n := len(merged); n > 0 && s[0] <= merged[n-1][1]+1 {
			if s[1] > merged[n-1][1] {
				merged[n-1][1] = s[1]
			}
			continue
		}
		merged = append(merged, s)
	}
	return merged
}

// formatFPPChannelRanges renders ranges as "start-count, start-count",
// the text form the CLI and the UI take.
func formatFPPChannelRanges(ranges []v1.FPPChannelRange) string {
	if len(ranges) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(ranges))
	for _, r := range ranges {
		parts = append(parts, fmt.Sprintf("%d-%d", r.StartChannel, r.ChannelCount))
	}
	return strings.Join(parts, ", ")
}

// validateFPPChannelRanges returns the one-line refusal for the first
// range that cannot be used, or "" when every range can.
func validateFPPChannelRanges(ranges []v1.FPPChannelRange) string {
	for _, r := range ranges {
		name := fmt.Sprintf("%d-%d", r.StartChannel, r.ChannelCount)
		switch {
		case r.StartChannel < 1:
			return fmt.Sprintf("Range %s starts below channel 1. Enter start-count, like 1-512.", name)
		case r.ChannelCount < 1:
			return fmt.Sprintf("Range %s has no channels. Enter start-count, like 1-512.", name)
		case r.StartChannel > fppMaxChannelNumber || r.ChannelCount > fppMaxChannelNumber-r.StartChannel+1:
			return fmt.Sprintf("Range %s ends past channel %d. Shorten it or remove it.", name, fppMaxChannelNumber)
		}
	}
	sorted := append([]v1.FPPChannelRange{}, ranges...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].StartChannel < sorted[j].StartChannel })
	for i := 1; i < len(sorted); i++ {
		prev, cur := sorted[i-1], sorted[i]
		if cur.StartChannel <= prev.StartChannel+prev.ChannelCount-1 {
			return fmt.Sprintf("Range %d-%d overlaps %d-%d. Use separate ranges, like 1-512, 1025-512.",
				cur.StartChannel, cur.ChannelCount, prev.StartChannel, prev.ChannelCount)
		}
	}
	return ""
}

// handleFPPBrightnessExcludedRanges serves PUT
// /api/v1/fpp/{instanceId}/brightness/excluded-ranges.
func (h *handlers) handleFPPBrightnessExcludedRanges(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	instanceID := r.PathValue("instanceId")
	if err := mqttproto.ValidateNodeID(instanceID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("instanceId is not a syntactically valid instance ID: "+err.Error()))
		return
	}

	req, problem := decodeFPPBrightnessExcludedRangesBody(r)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	if refusal := validateFPPChannelRanges(req.Ranges); refusal != "" {
		writeProblem(w, h.logger, now, invalidParameterProblem(refusal))
		return
	}

	endpoints, err := currentFPPEndpoints(ctx, h.deps.FPP)
	if err != nil {
		h.writeInternalError(w, now, "list fpp endpoints for an excluded ranges write", err)
		return
	}
	baseURL := ""
	for _, ep := range endpoints {
		if ep.ID == instanceID {
			baseURL = ep.URL
			break
		}
	}
	if baseURL == "" {
		writeProblem(w, h.logger, now, resourceNotFoundProblem(
			fmt.Sprintf("no configured FPP endpoint has instance id %q", instanceID)))
		return
	}

	requestID := req.RequestID
	if requestID == "" {
		requestID = uuid.NewString()
	}

	ac := authFromContext(ctx)
	params := map[string]any{"ranges": req.Ranges}
	commandID := uuid.NewString()
	if !h.writeAuditOrFail(ctx, w, now, identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: auditActionFPPSetBrightnessExcludedRanges, Target: instanceID,
		Kind: identity.AuditDispatch, CommandID: commandID, IdempotencyKey: requestID,
		Params: params,
	}) {
		return
	}

	write := h.fppExcludedRangesWriter
	if write == nil {
		write = writeFPPBrightnessExcludedRanges
	}
	dispatchedAt := h.now()
	written, readBack, writeErr := write(ctx, baseURL, req.Ranges)
	resolvedAt := h.now()

	outcome, outcomeState, outcomeReason := fppBrightnessExcludedRangesOutcome(req.Ranges, written, readBack, writeErr)
	h.auditFPPBrightnessExcludedRangesOutcome(r, ac, commandID, instanceID, params, outcome, outcomeState, outcomeReason)
	if writeErr != nil {
		writeProblem(w, h.logger, h.now(), fppBrightnessExcludedRangesWriteFailedProblem(outcomeReason))
		return
	}

	dispatchedStamp, resolvedStamp := formatTime(dispatchedAt), formatTime(resolvedAt)
	jsonWrite(w, v1.FPPBrightnessExcludedRangesResponse{
		ServerTime: formatTime(h.now()),
		Command: v1.FPPCommandResult{
			ID: commandID, IdempotencyKey: requestID,
			Action: fppBrightnessExcludedRangesAction, InstanceID: instanceID,
			Params: params, Replay: false,
			Outcome: outcome, OutcomeState: outcomeState, OutcomeReason: outcomeReason,
			DispatchedAt: &dispatchedStamp, ResolvedAt: &resolvedStamp,
		},
		ExcludedRanges: readBack,
	})
}

// fppBrightnessExcludedRangesOutcome decides what this write may claim.
// Only the plugin reporting the same channels back confirms anything.
func fppBrightnessExcludedRangesOutcome(want []v1.FPPChannelRange, written fppcommand.ExcludedRangesOutcome, readBack *[]v1.FPPChannelRange, writeErr error) (outcome, outcomeState, outcomeReason string) {
	switch {
	case writeErr != nil && written.StatusCode == http.StatusNotFound:
		return outcomeWordFailed, string(observation.StateUnsupported),
			"The ShowMesh plugin on this FPP cannot exclude channels. Update the plugin."
	case writeErr != nil && written.Refusal != "":
		return outcomeWordFailed, string(observation.StateCollectionFailed),
			"FPP refused the excluded channels: " + strings.TrimRight(written.Refusal, ".") + "."
	case writeErr != nil && written.StatusCode != 0:
		return outcomeWordFailed, string(observation.StateCollectionFailed),
			"FPP saved nothing. Check the ShowMesh plugin on FPP."
	case writeErr != nil:
		return outcomeWordFailed, string(observation.StateCollectionFailed),
			"FPP could not be reached, so nothing was saved. Check that FPP is on and try again."
	case readBack != nil:
		return outcomeWordConfirmed, string(observation.StateCurrent),
			"FPP reports excluded channels: " + formatFPPChannelRanges(*readBack) + "."
	default:
		return outcomeWordUnconfirmed, string(observation.StateNotCollected),
			fmt.Sprintf("FPP took %s and has not reported it back yet. Check again shortly.", formatFPPChannelRanges(want))
	}
}

// auditFPPBrightnessExcludedRangesOutcome records what the write did,
// best effort: the write has already been sent by the time this runs.
func (h *handlers) auditFPPBrightnessExcludedRangesOutcome(r *http.Request, ac authContext, commandID, instanceID string, params map[string]any, outcome, outcomeState, outcomeReason string) {
	entry := identity.AuditEntry{
		Timestamp: h.now(), PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: auditActionFPPSetBrightnessExcludedRanges, Target: instanceID,
		Kind: identity.AuditOutcome, CommandID: commandID,
		Params: params, Outcome: outcome, OutcomeState: outcomeState, OutcomeReason: outcomeReason,
	}
	if err := h.deps.Identity.WriteAudit(r.Context(), entry); err != nil {
		h.logWarn("failed to audit an excluded ranges outcome", "instanceId", instanceID, "commandId", commandID, "error", err)
	}
}

// excludedRangesBodyForm is the body shape every decode refusal shows.
const excludedRangesBodyForm = `Send {"ranges":[{"startChannel":1,"channelCount":512}]}.`

// decodeFPPBrightnessExcludedRangesBody decodes this route's body under
// the ceiling route's rule: an absent field, a null and a value are three
// different things, and an unrecognized key is a 400 naming it.
func decodeFPPBrightnessExcludedRangesBody(r *http.Request) (v1.FPPBrightnessExcludedRangesRequest, *v1.Problem) {
	refuse := func(fact string) (v1.FPPBrightnessExcludedRangesRequest, *v1.Problem) {
		p := invalidParameterProblem(fact + " " + excludedRangesBodyForm)
		return v1.FPPBrightnessExcludedRangesRequest{}, &p
	}

	var top map[string]json.RawMessage
	dec := json.NewDecoder(io.LimitReader(r.Body, maxFPPBrightnessExcludedRangesRequestBodyBytes+1))
	if err := dec.Decode(&top); err != nil {
		return refuse("The request body is not a JSON object.")
	}
	var unknown []string
	for key := range top {
		if key != "ranges" && key != "requestId" {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return refuse(fmt.Sprintf("The field %s is not recognized.", strings.Join(unknown, ", ")))
	}

	raw, ok := top["ranges"]
	if !ok || string(raw) == "null" {
		return refuse("The ranges list is missing.")
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return refuse("The ranges value is not a list of ranges.")
	}
	out := v1.FPPBrightnessExcludedRangesRequest{Ranges: make([]v1.FPPChannelRange, 0, len(items))}
	for i, item := range items {
		var start, count *int
		if len(item) != 2 || json.Unmarshal(item["startChannel"], &start) != nil || json.Unmarshal(item["channelCount"], &count) != nil || start == nil || count == nil {
			return refuse(fmt.Sprintf("Range %d needs a whole-number startChannel and channelCount only.", i+1))
		}
		out.Ranges = append(out.Ranges, v1.FPPChannelRange{StartChannel: *start, ChannelCount: *count})
	}

	if raw, ok := top["requestId"]; ok {
		if err := json.Unmarshal(raw, &out.RequestID); err != nil || out.RequestID == "" {
			return refuse("The requestId is not a non-empty string. Leave it out to have one made.")
		}
	}
	return out, nil
}
