package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
)

// renderConfirmDeadline bounds how long render.surface.apply,
// render.surface.clear, and render.pipeline.restart poll for their own
// post-dispatch evidence before reporting Confirmed: false. Starting (or
// stopping) a pipeline is asynchronous — see [OperationResult]'s doc
// comment, which names this exact case in advance — so this seam's three
// operations must never report success off the dispatch call returning
// without error; they poll [pipeline.Supervisor.AwaitState] for real
// evidence instead.
//
// SHOWMESH HYPOTHESIS, NOT MEASURED: no bench data exists for real
// gst-launch-1.0 startup time on the reference hardware class. 5 seconds is
// a conservative guess comfortably longer than this seam's own trivial
// test-pattern pipeline needs.
const renderConfirmDeadline = 5 * time.Second

// renderConfirmPollInterval is how often AwaitState re-checks the
// supervisor's snapshot while waiting for renderConfirmDeadline to elapse.
const renderConfirmPollInterval = 25 * time.Millisecond

// surfaceIDPattern bounds params.surfaceId the same way
// mqttproto.nodeIDPattern bounds a node ID: this value is joined directly
// into a filesystem path by pipeline.AssignmentStore, so its character
// class must exclude path separators and ".." from the start, not merely
// have them sanitized afterward. Slightly more permissive than a node ID
// (allows uppercase, since show.surface object ids are caller-chosen and
// this project does not otherwise constrain their case).
var surfaceIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

// renderApplyKnownKeys is the complete set of top-level keys
// render.surface.apply recognizes, matching this project's
// reject-unknown-keys convention (internal/coordinator/config/showsurface.go):
// an unrecognized key is refused rather than silently ignored, because an
// ignored key reads as an applied one. Only surfaceId is consumed by this
// seam's fixed test-pattern pipeline; the rest (mirroring
// internal/coordinator/config.ShowSurfacePayload plus the two FSEQ fields
// build contract ruling 4 names) are accepted, validated where practical,
// and persisted verbatim for B3/B4 to read once they exist.
var renderApplyKnownKeys = map[string]bool{
	"surfaceId": true, "show": true, "name": true, "node": true,
	"channelRange": true, "geometry": true, "frameRate": true, "output": true,
	"fseqFilename": true, "fseqContentHash": true,
}

// renderSurfaceKnownKeys is the key allowlist for render.surface.clear and
// render.pipeline.restart, which take only a surface identifier.
var renderSurfaceKnownKeys = map[string]bool{"surfaceId": true}

// renderTransportProbeKnownKeys is the key allowlist for
// render.transport.probe, which (like clear/restart) takes only a surface
// identifier: the probe result is recorded against that surface's snapshot
// (see [pipeline.Supervisor.SetTransportProbe]) so it flows through the
// existing render report rather than needing its own wire shape.
var renderTransportProbeKnownKeys = map[string]bool{"surfaceId": true}

// parseSurfaceID extracts and validates params.surfaceId, common to all
// three render.* operations.
func parseSurfaceID(action string, params map[string]any) (string, error) {
	raw, ok := params["surfaceId"]
	if !ok {
		return "", fmt.Errorf("%s: params.surfaceId is required", action)
	}
	v, ok := raw.(string)
	if !ok || v == "" {
		return "", fmt.Errorf("%s: params.surfaceId must be a non-empty string, got %T", action, raw)
	}
	if !surfaceIDPattern.MatchString(v) {
		return "", fmt.Errorf("%s: params.surfaceId %q is not a safe identifier (must match %s)", action, v, surfaceIDPattern.String())
	}
	return v, nil
}

// rejectUnknownKeys reports an error naming the first unrecognized key in
// params, matching internal/coordinator/config's own "a typo'd key is
// refused, not ignored" rule applied here at the agent's command boundary.
func rejectUnknownKeys(action string, params map[string]any, known map[string]bool) error {
	for k := range params {
		if !known[k] {
			return fmt.Errorf("%s: params has an unrecognized key %q", action, k)
		}
	}
	return nil
}

// isRenderAction reports whether action is one of this seam's four
// allowlisted render.* operations — used by command.go's HandleMessage to
// decide whether to signal renderTrigger after a genuinely-executed
// command.
func isRenderAction(action string) bool {
	switch action {
	case "render.surface.apply", "render.surface.clear", "render.pipeline.restart", "render.transport.probe":
		return true
	default:
		return false
	}
}

// renderOperations holds the four render.* allowlisted operations' shared
// dependencies: the pipeline supervisor and the on-disk assignment store
// (build contract ruling 4 — a re-read at boot resumes rendering with no
// coordinator reachable).
type renderOperations struct {
	sup   *pipeline.Supervisor
	store *pipeline.AssignmentStore

	// probeStarter is the [pipeline.ProcessStarter] probeTransport passes
	// to [pipeline.ProbeNDISend]. nil (the production default, set by
	// [newRenderOperations]) selects the real process starter; tests in
	// this package set it directly (same-package access, no exported
	// setter needed) to exercise probeTransport's wiring without shelling
	// out to a real gst-launch-1.0 — pipeline/probe_test.go already covers
	// the probe mechanism itself exhaustively against fakes and reality.
	probeStarter pipeline.ProcessStarter
}

func newRenderOperations(sup *pipeline.Supervisor, store *pipeline.AssignmentStore) *renderOperations {
	return &renderOperations{sup: sup, store: store}
}

// applySurface is the OperationFunc for "render.surface.apply": validate
// params, persist the assignment (so a later boot with no coordinator
// reachable can resume it), start the surface's pipeline, and poll for
// post-dispatch evidence that it reached running before reporting
// Confirmed.
func (o *renderOperations) applySurface(ctx context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
	const action = "render.surface.apply"

	surfaceID, err := parseSurfaceID(action, params)
	if err != nil {
		return OperationResult{}, err
	}
	if err := rejectUnknownKeys(action, params, renderApplyKnownKeys); err != nil {
		return OperationResult{}, err
	}

	rawParams, err := json.Marshal(params)
	if err != nil {
		return OperationResult{}, fmt.Errorf("%s: encoding params for persistence: %w", action, err)
	}

	executedAt := now()

	// Persist BEFORE starting the pipeline: a crash between these two steps
	// leaves a durable assignment a later boot can still apply, which is
	// the failure direction this project always prefers (resume rendering,
	// not lose the assignment).
	if err := o.store.Upsert(pipeline.Assignment{SurfaceID: surfaceID, RawParams: rawParams, AppliedAt: executedAt}); err != nil {
		return OperationResult{}, fmt.Errorf("%s: persisting assignment: %w", action, err)
	}

	spec, err := buildSurfaceSpec(surfaceID, params)
	if err != nil {
		return OperationResult{}, err
	}

	if err := o.sup.Apply(spec); err != nil {
		return OperationResult{}, fmt.Errorf("%s: %w", action, err)
	}

	return o.awaitAndReport(ctx, surfaceID, []pipeline.State{pipeline.StateRunning}, executedAt)
}

// clearSurface is the OperationFunc for "render.surface.clear": stop the
// surface's pipeline and remove its persisted assignment so a later boot
// does not resume it.
func (o *renderOperations) clearSurface(ctx context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
	const action = "render.surface.clear"

	surfaceID, err := parseSurfaceID(action, params)
	if err != nil {
		return OperationResult{}, err
	}
	if err := rejectUnknownKeys(action, params, renderSurfaceKnownKeys); err != nil {
		return OperationResult{}, err
	}

	executedAt := now()

	if err := o.sup.Clear(surfaceID); err != nil {
		return OperationResult{}, fmt.Errorf("%s: %w", action, err)
	}
	if err := o.store.Remove(surfaceID); err != nil {
		return OperationResult{}, fmt.Errorf("%s: removing persisted assignment: %w", action, err)
	}

	return o.awaitAndReport(ctx, surfaceID, []pipeline.State{pipeline.StateStopped}, executedAt)
}

// restartPipeline is the OperationFunc for "render.pipeline.restart":
// clear the fast-failure lockout (if any) and restart the surface's
// pipeline from its currently-applied spec.
func (o *renderOperations) restartPipeline(ctx context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
	const action = "render.pipeline.restart"

	surfaceID, err := parseSurfaceID(action, params)
	if err != nil {
		return OperationResult{}, err
	}
	if err := rejectUnknownKeys(action, params, renderSurfaceKnownKeys); err != nil {
		return OperationResult{}, err
	}

	executedAt := now()

	if err := o.sup.Restart(surfaceID); err != nil {
		return OperationResult{}, fmt.Errorf("%s: %w", action, err)
	}

	return o.awaitAndReport(ctx, surfaceID, []pipeline.State{pipeline.StateRunning}, executedAt)
}

// probeTransport is the OperationFunc for "render.transport.probe": run a
// real NDI state-transition probe ([pipeline.ProbeNDISend]) and record the
// outcome on surfaceID's snapshot so it reaches the next render report
// (command.go's renderTrigger fires after this, same as the other three
// render.* operations). Unlike applySurface/clearSurface/restartPipeline,
// this operation's own effect (the probe) is synchronous and complete by
// the time it returns — there is no separate asynchronous state to poll
// for, so Confirmed reports the probe's own result (available or not),
// not a read-back of something else.
func (o *renderOperations) probeTransport(ctx context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
	const action = "render.transport.probe"
	const transport = "ndi" // the only transport this build has a real probe for; see ProbeNDISend.

	surfaceID, err := parseSurfaceID(action, params)
	if err != nil {
		return OperationResult{}, err
	}
	if err := rejectUnknownKeys(action, params, renderTransportProbeKnownKeys); err != nil {
		return OperationResult{}, err
	}

	executedAt := now()
	result := pipeline.ProbeNDISend(ctx, o.probeStarter)
	observedAt := now()

	o.sup.SetTransportProbe(surfaceID, transport, result.Available, result.Reason, observedAt)

	return OperationResult{
		Confirmed: result.Available,
		Signal:    "node.render.transport_probe",
		Value: map[string]any{
			"surfaceId": surfaceID,
			"transport": transport,
			"available": result.Available,
			"reason":    result.Reason,
		},
		ExecutedAt: executedAt,
		ObservedAt: observedAt,
	}, nil
}

// awaitAndReport polls the supervisor for evidence, dated at or after
// executedAt, that surfaceID reached one of want, bounded by
// renderConfirmDeadline (and ctx). It never returns a non-nil error for
// "did not confirm in time" — that is Confirmed: false, per
// [OperationResult]'s own Confirmed doc comment: the operation was
// dispatched successfully; whether the read-back evidence corroborates it
// is a separate question.
func (o *renderOperations) awaitAndReport(ctx context.Context, surfaceID string, want []pipeline.State, executedAt time.Time) (OperationResult, error) {
	awaitCtx, cancel := context.WithTimeout(ctx, renderConfirmDeadline)
	defer cancel()

	snap, found := o.sup.AwaitState(awaitCtx, surfaceID, want, executedAt, renderConfirmPollInterval)

	observedAt := snap.ObservedAt
	if observedAt.IsZero() {
		// No snapshot evidence exists at all yet (e.g. AwaitState's very
		// first poll raced the runner's construction); ObservedAt must
		// still be evidence-shaped, so fall back to "now," not the
		// zero-time default ADR-011 forbids treating as meaningful.
		observedAt = time.Now().UTC()
	}

	return OperationResult{
		Confirmed: found,
		Signal:    "node.render.surface_state",
		Value: map[string]any{
			"surfaceId": surfaceID,
			"state":     string(snap.State),
			"reason":    snap.Reason,
		},
		ExecutedAt: executedAt,
		ObservedAt: observedAt,
	}, nil
}
