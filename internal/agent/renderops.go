package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
	"github.com/showmeshsystems/showmesh/pkg/fseq"
	"github.com/showmeshsystems/showmesh/pkg/multisync"
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

// frameWriterHandle bundles a running B3 [pipeline.FrameWriter] with the
// resources its lifetime owns: the open FSEQ file (closed on
// render.surface.clear or a re-apply) and the cancel function for the
// goroutine running it.
type frameWriterHandle struct {
	fw     *pipeline.FrameWriter
	fseq   *fseq.File
	cancel context.CancelFunc
}

// renderOperations holds the four render.* allowlisted operations' shared
// dependencies: the pipeline supervisor, the on-disk assignment store
// (build contract ruling 4 — a re-read at boot resumes rendering with no
// coordinator reachable), the node's asset directory (where a
// render.surface.apply's fseqFilename resolves to), and the shared
// MultiSync timeline B3's frame writers read position from.
type renderOperations struct {
	sup      *pipeline.Supervisor
	store    *pipeline.AssignmentStore
	assetDir string
	timeline *multisync.Timeline
	logger   pipeline.Logger

	mu      sync.Mutex
	writers map[string]*frameWriterHandle

	// probeStarter is the [pipeline.ProcessStarter] probeTransport passes
	// to [pipeline.ProbeNDISend]. nil (the production default, set by
	// [newRenderOperations]) selects the real process starter; tests in
	// this package set it directly (same-package access, no exported
	// setter needed) to exercise probeTransport's wiring without shelling
	// out to a real gst-launch-1.0 — pipeline/probe_test.go already covers
	// the probe mechanism itself exhaustively against fakes and reality.
	probeStarter pipeline.ProcessStarter
}

func newRenderOperations(sup *pipeline.Supervisor, store *pipeline.AssignmentStore, assetDir string, timeline *multisync.Timeline, logger pipeline.Logger) *renderOperations {
	return &renderOperations{
		sup:      sup,
		store:    store,
		assetDir: assetDir,
		timeline: timeline,
		logger:   logger,
		writers:  make(map[string]*frameWriterHandle),
	}
}

// Shutdown stops every currently-running frame writer and closes its FSEQ
// file. Called once, at agent shutdown, before [pipeline.Supervisor.
// Shutdown] stops the pipeline processes themselves.
func (o *renderOperations) Shutdown() {
	o.mu.Lock()
	ids := make([]string, 0, len(o.writers))
	for id := range o.writers {
		ids = append(ids, id)
	}
	o.mu.Unlock()
	for _, id := range ids {
		o.stopFrameWriter(id)
	}
}

// stopFrameWriter cancels and waits for surfaceID's current frame writer
// (if any) and closes its FSEQ file, then removes it from the map. Called
// before starting a replacement (a re-apply) and on render.surface.clear.
// Not called on render.pipeline.restart: the pipeline process restarting
// does not mean the surface's assignment changed, and the frame writer
// already tolerates a mid-session process restart (it re-fetches stdin
// every tick — see [pipeline.Supervisor.Stdin]).
func (o *renderOperations) stopFrameWriter(surfaceID string) {
	o.mu.Lock()
	h, ok := o.writers[surfaceID]
	if ok {
		delete(o.writers, surfaceID)
	}
	o.mu.Unlock()
	if !ok {
		return
	}
	h.cancel()
	h.fw.Stop()
	_ = h.fseq.Close()
}

// buildFSEQAssignment parses the FSEQ-specific fields of a render.surface.
// apply params map (channelRange, geometry, frameRate, fseqFilename,
// fseqContentHash). ok is false when fseqFilename is absent — this is not
// an error: an assignment with no FSEQ information is still a valid
// request for B2a's test-pattern pipeline, matching renderApplyKnownKeys'
// existing "accepted and persisted, not yet all consumed" posture.
func buildFSEQAssignment(action string, params map[string]any) (a fseqAssignment, ok bool, err error) {
	rawFilename, has := params["fseqFilename"]
	if !has {
		return fseqAssignment{}, false, nil
	}
	filename, isStr := rawFilename.(string)
	if !isStr || filename == "" {
		return fseqAssignment{}, false, fmt.Errorf("%s: params.fseqFilename must be a non-empty string, got %T", action, rawFilename)
	}
	if filepath.Base(filename) != filename {
		return fseqAssignment{}, false, fmt.Errorf("%s: params.fseqFilename %q must be a bare filename, not a path", action, filename)
	}

	contentHash, verr := requireString(action, params, "fseqContentHash", "fseqContentHash")
	if verr != nil {
		return fseqAssignment{}, false, verr
	}

	channelRangeRaw, verr := requireObject(action, params, "channelRange", "channelRange")
	if verr != nil {
		return fseqAssignment{}, false, verr
	}
	// show.surface.channelRange.startChannel is the operator-facing
	// xLights UI number and is validated coordinator-side as >= 1: it is
	// 1-BASED. pkg/fseq is 0-based throughout, matching the file. This
	// subtraction is the ONE place that conversion happens in this
	// package — see RES-017's own rule against scattering "-1" across
	// call sites, and frame.go's FrameWriter, which only ever receives an
	// already-0-based channelStart.
	startChannel1Based, verr := requireInt(action, channelRangeRaw, "startChannel", "channelRange.startChannel")
	if verr != nil {
		return fseqAssignment{}, false, verr
	}
	if startChannel1Based < 1 {
		return fseqAssignment{}, false, fmt.Errorf("%s: params.channelRange.startChannel must be at least 1, got %d", action, startChannel1Based)
	}
	channelCount, verr := requireInt(action, channelRangeRaw, "channelCount", "channelRange.channelCount")
	if verr != nil {
		return fseqAssignment{}, false, verr
	}
	if channelCount < 1 {
		return fseqAssignment{}, false, fmt.Errorf("%s: params.channelRange.channelCount must be at least 1, got %d", action, channelCount)
	}

	geometryRaw, verr := requireObject(action, params, "geometry", "geometry")
	if verr != nil {
		return fseqAssignment{}, false, verr
	}
	width, verr := requireInt(action, geometryRaw, "width", "geometry.width")
	if verr != nil {
		return fseqAssignment{}, false, verr
	}
	height, verr := requireInt(action, geometryRaw, "height", "geometry.height")
	if verr != nil {
		return fseqAssignment{}, false, verr
	}
	pixelFormat, verr := requireString(action, geometryRaw, "pixelFormat", "geometry.pixelFormat")
	if verr != nil {
		return fseqAssignment{}, false, verr
	}

	frameRate, verr := requireInt(action, params, "frameRate", "frameRate")
	if verr != nil {
		return fseqAssignment{}, false, verr
	}

	return fseqAssignment{
		fseqFilename:    filename,
		fseqContentHash: contentHash,
		channelStart0:   startChannel1Based - 1,
		channelCount:    channelCount,
		width:           width,
		height:          height,
		pixelFormat:     pixelFormat,
		frameRate:       frameRate,
	}, true, nil
}

// fseqAssignment is buildFSEQAssignment's parsed, still-0-based-converted
// result.
type fseqAssignment struct {
	fseqFilename    string
	fseqContentHash string
	channelStart0   int
	channelCount    int
	width           int
	height          int
	pixelFormat     string
	frameRate       int
}

// requireObject, requireString, and requireInt look up key in params (the
// map's own field name, e.g. "startChannel") and report errors using label
// (the full dotted path an operator would recognize, e.g.
// "channelRange.startChannel") — the two differ for any nested field, since
// params here is already the inner object's own map, not the top-level
// params map. Passing key as its own label (the top-level-field case) is
// fine; conflating the two for a NESTED field was this function's own
// bug once (a lookup for "channelRange.startChannel" against a map whose
// only keys are "startChannel"/"channelCount" always misses) — kept as two
// parameters specifically so that mistake cannot recur silently.
func requireObject(action string, params map[string]any, key, label string) (map[string]any, error) {
	raw, ok := params[key]
	if !ok {
		return nil, fmt.Errorf("%s: params.%s is required", action, label)
	}
	v, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: params.%s must be an object, got %T", action, label, raw)
	}
	return v, nil
}

func requireString(action string, params map[string]any, key, label string) (string, error) {
	raw, ok := params[key]
	if !ok {
		return "", fmt.Errorf("%s: params.%s is required", action, label)
	}
	v, ok := raw.(string)
	if !ok || v == "" {
		return "", fmt.Errorf("%s: params.%s must be a non-empty string, got %T", action, label, raw)
	}
	return v, nil
}

// requireInt extracts an integer from a map decoded from JSON via
// encoding/json into map[string]any, where every JSON number decodes as
// float64 — never assume the value is already an int.
func requireInt(action string, params map[string]any, key, label string) (int, error) {
	raw, ok := params[key]
	if !ok {
		return 0, fmt.Errorf("%s: params.%s is required", action, label)
	}
	v, ok := raw.(float64)
	if !ok {
		return 0, fmt.Errorf("%s: params.%s must be a number, got %T", action, label, raw)
	}
	return int(v), nil
}

// buildAssignedSpec builds this surface's real (or, absent FSEQ
// information, test-pattern) pipeline spec and, for the real case, its
// [pipeline.FrameWriter] — not yet started. assetDir-relative resolution
// of fseqFilename and content-hash verification both happen here: ADR-028
// requires a node never resolve an asset by filename alone trusting the
// coordinator's say-so blindly, so a content-hash mismatch refuses the
// assignment rather than rendering unverified bytes.
func buildAssignedSpec(action, assetDir, surfaceID string, params map[string]any) (pipeline.Spec, *fseq.File, fseqAssignment, outputSinkOutcome, error) {
	a, ok, err := buildFSEQAssignment(action, params)
	if err != nil {
		return pipeline.Spec{}, nil, fseqAssignment{}, outputSinkOutcome{}, err
	}
	if !ok {
		spec, sinkOutcome, serr := applyOutputSink(pipeline.DefaultTestPatternSpec(surfaceID), surfaceID, params)
		if serr != nil {
			return pipeline.Spec{}, nil, fseqAssignment{}, outputSinkOutcome{}, fmt.Errorf("%s: %w", action, serr)
		}
		return spec, nil, fseqAssignment{}, sinkOutcome, nil
	}

	path := filepath.Join(assetDir, a.fseqFilename)
	gotHash, err := hashFile(path)
	if err != nil {
		return pipeline.Spec{}, nil, fseqAssignment{}, outputSinkOutcome{}, fmt.Errorf("%s: reading fseq asset %q: %w", action, a.fseqFilename, err)
	}
	if gotHash != a.fseqContentHash {
		return pipeline.Spec{}, nil, fseqAssignment{}, outputSinkOutcome{}, fmt.Errorf(
			"%s: fseq asset %q content hash %q does not match assignment's %q (ADR-028: identity is content, not filename)",
			action, a.fseqFilename, gotHash, a.fseqContentHash)
	}

	f, err := fseq.Open(path)
	if err != nil {
		return pipeline.Spec{}, nil, fseqAssignment{}, outputSinkOutcome{}, fmt.Errorf("%s: opening fseq asset %q: %w", action, a.fseqFilename, err)
	}

	spec, err := pipeline.FSEQSourceSpec(surfaceID, a.width, a.height, a.pixelFormat, a.frameRate)
	if err != nil {
		_ = f.Close()
		return pipeline.Spec{}, nil, fseqAssignment{}, outputSinkOutcome{}, fmt.Errorf("%s: %w", action, err)
	}
	// buildFSEQAssignment does not itself parse params.output — the sink is
	// attached here, uniformly with the test-pattern-fallback branch above,
	// so a real assignment's NDI/HDMI choice and a diagnostic assignment's
	// choice go through exactly one code path (renderspec.go's
	// applyOutputSink), never two that could disagree.
	spec, sinkOutcome, err := applyOutputSink(spec, surfaceID, params)
	if err != nil {
		_ = f.Close()
		return pipeline.Spec{}, nil, fseqAssignment{}, outputSinkOutcome{}, fmt.Errorf("%s: %w", action, err)
	}

	return spec, f, a, sinkOutcome, nil
}

// ResumeAssignment re-applies a persisted assignment at agent startup
// (build contract ruling 4: a node that restarts with no coordinator
// reachable resumes rendering from its own on-disk state). It is the same
// build-then-Apply-then-start-frame-writer sequence applySurface uses,
// minus the persistence write (already on disk) and the post-dispatch
// confirmation poll (nothing is waiting on a boot-time resume's result).
func (o *renderOperations) ResumeAssignment(surfaceID string, params map[string]any) error {
	const action = "render.surface.apply (resumed at boot)"

	spec, f, a, sinkOutcome, err := buildAssignedSpec(action, o.assetDir, surfaceID, params)
	if err != nil {
		return err
	}
	if err := o.sup.Apply(spec); err != nil {
		if f != nil {
			_ = f.Close()
		}
		return fmt.Errorf("%s: %w", action, err)
	}
	o.recordDegradedTransportEvidence(surfaceID, sinkOutcome, time.Now().UTC())
	if f != nil {
		if err := o.startFrameWriter(surfaceID, f, a); err != nil {
			_ = f.Close()
			return fmt.Errorf("%s: starting frame writer: %w", action, err)
		}
	}
	return nil
}

// recordDegradedTransportEvidence proactively records surface.transport.
// available=false with an actionable reason the moment a spec falls back
// to the diagnostic fakesink — no probe needed, because this package
// already knows with certainty (it built the spec) that a fakesink cannot
// send NDI. Distinct from render.transport.probe's own real state-
// transition evidence: this is static, build-time evidence about the
// pipeline's own construction, not a runtime probe result, but it fills
// the exact same wire fields, so a caller reading surface.transport.
// available never needs to know which of the two produced it.
//
// A no-op when sinkOutcome.Configured is false (no output was requested at
// all — nothing to report) or RealSink is true (nothing degraded).
func (o *renderOperations) recordDegradedTransportEvidence(surfaceID string, sinkOutcome outputSinkOutcome, observedAt time.Time) {
	if !sinkOutcome.Configured || sinkOutcome.RealSink {
		return
	}
	o.sup.SetTransportProbe(surfaceID, sinkOutcome.Transport, false, sinkOutcome.Reason, observedAt)
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

	spec, f, a, sinkOutcome, err := buildAssignedSpec(action, o.assetDir, surfaceID, params)
	if err != nil {
		return OperationResult{}, err
	}

	if err := o.sup.Apply(spec); err != nil {
		if f != nil {
			_ = f.Close()
		}
		return OperationResult{}, fmt.Errorf("%s: %w", action, err)
	}
	o.recordDegradedTransportEvidence(surfaceID, sinkOutcome, now())

	// A re-apply of a surface that already had a frame writer running
	// replaces it: stop the old one (and close its FSEQ file) before
	// starting the new one, so two writers never race the same pipeline's
	// stdin.
	o.stopFrameWriter(surfaceID)
	if f != nil {
		if err := o.startFrameWriter(surfaceID, f, a); err != nil {
			_ = f.Close()
			return OperationResult{}, fmt.Errorf("%s: starting frame writer: %w", action, err)
		}
	}

	return o.awaitAndReport(ctx, surfaceID, []pipeline.State{pipeline.StateRunning}, executedAt)
}

// startFrameWriter builds and launches surfaceID's [pipeline.FrameWriter]
// against the already-open fseq file f, wiring it to o.sup and o.timeline.
// The caller keeps ownership of f on error (closes it itself); on success
// the writer's own lifetime (via [renderOperations.stopFrameWriter]) owns
// closing it.
func (o *renderOperations) startFrameWriter(surfaceID string, f *fseq.File, a fseqAssignment) error {
	fw, err := pipeline.NewFrameWriter(o.sup, surfaceID, f, o.timeline, a.channelStart0, a.channelCount, o.logger)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	go fw.Run(ctx)

	o.mu.Lock()
	o.writers[surfaceID] = &frameWriterHandle{fw: fw, fseq: f, cancel: cancel}
	o.mu.Unlock()
	return nil
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

	o.stopFrameWriter(surfaceID)

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
