package v1

// This file is Track B seam B2b-front's wire contract: the three
// render.surface.apply/render.surface.clear/render.pipeline.restart
// dispatch endpoints. Mirrors FPPCommandResult's shape (evidence-based
// outcome, never a bare 200-means-success), narrowed to what this seam
// actually reports.

// RenderApplyRequest is the body of
// POST /nodes/{nodeId}/render/surfaces/{surfaceId}/apply. sequenceId names
// which of the show's sequences to resolve the surface's current FSEQ
// asset from (identity is show+sequence+target+contentHash, never a
// filename — ADR-028).
type RenderApplyRequest struct {
	SequenceID     string `json:"sequenceId"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
}

// RenderSurfaceRequest is the body of the clear and restart endpoints,
// which take no parameters of their own beyond the path's surfaceId.
type RenderSurfaceRequest struct {
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
}

// RenderCommandResult is the outcome of dispatching one render.* operation
// to a node — Confirmed is only ever true when evidence dated at or after
// DispatchedAt corroborates it (ADR-003); a bare successful dispatch alone
// is never reported as success.
type RenderCommandResult struct {
	CommandID      string `json:"commandId"`
	IdempotencyKey string `json:"idempotencyKey"`
	Action         string `json:"action"`
	NodeID         string `json:"nodeId"`
	SurfaceID      string `json:"surfaceId"`
	Replay         bool   `json:"replay"`

	Outcome       string `json:"outcome"` // "confirmed" | "unconfirmed"
	OutcomeState  string `json:"outcomeState"`
	OutcomeReason string `json:"outcomeReason"`

	// PipelineFailed is Finding 15: true only when this command's outcome
	// evidence is itself the pipeline's own reported "failed" state
	// (mqttproto.RenderPipelineStateFailed), distinct from absent, stale,
	// or a merely-not-yet-reached state. OutcomeState above only ever
	// carries pkg/observation's six-value State vocabulary (current,
	// stale, unknown_age, not_collected, collection_failed, unsupported)
	// — it can never equal "failed" — so a caller that wants to react to
	// the pipeline specifically being down must use this field, never
	// OutcomeState or a parse of OutcomeReason's free text. Always false
	// for a confirmed outcome and for render.transport.probe, which has
	// no pipeline state to fail.
	PipelineFailed bool `json:"pipelineFailed"`

	DispatchedAt string  `json:"dispatchedAt"`
	ResolvedAt   *string `json:"resolvedAt"`

	// IdleOutput is the render.settings.idleOutput value RESOLVED and sent
	// to the node as part of THIS render.surface.apply assignment — empty
	// for render.surface.clear/render.pipeline.restart/render.transport.probe,
	// which carry no idleOutput. Surfaced so a caller can see, without a
	// second request, exactly what this surface was told to draw while
	// idle; it reflects the value resolved at THIS dispatch, not whatever
	// render.settings holds now if it has since changed.
	IdleOutput string `json:"idleOutput"`
}

// RenderCommandResponse wraps RenderCommandResult with the standard
// serverTime envelope (contract section 6.2).
type RenderCommandResponse struct {
	ServerTime string              `json:"serverTime"`
	Command    RenderCommandResult `json:"command"`
}
