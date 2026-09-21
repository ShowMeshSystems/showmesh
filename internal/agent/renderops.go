package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
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
// ignored key reads as an applied one. surfaceId, channelRange, geometry,
// and frameRate are consumed by buildFSEQAssignment on every apply,
// content or not (mirroring internal/coordinator/config.ShowSurfacePayload);
// fseqFilename and fseqContentHash (build contract ruling 4) are consumed
// only when both are present; show/name/node are persisted verbatim for a
// caller that wants them back, never validated here.
var renderApplyKnownKeys = map[string]bool{
	"surfaceId": true, "show": true, "name": true, "node": true,
	"channelRange": true, "geometry": true, "frameRate": true, "output": true,
	"fseqFilename": true, "fseqContentHash": true, "idleOutput": true,
	"generation": true, "catalogRevision": true,
}

// idleOutputKnown is this package's own copy of render.settings.idleOutput's
// three permitted values (internal/coordinator/config's renderIdleOutputs) —
// independently reproduced, not imported, per this codebase's standing
// each-side-of-a-wire-boundary-decodes-independently convention
// (surfaceIDPattern's own doc comment already applies this once).
var idleOutputKnown = map[string]bool{
	pipeline.IdleOutputBlack:      true,
	pipeline.IdleOutputHold:       true,
	pipeline.IdleOutputDiagnostic: true,
}

// parseIdleOutput extracts params.idleOutput. Absent means an assignment
// persisted before this field existed — an older render.surface.apply body
// resumed at boot (build contract ruling 4) — and defaults to black, the
// same "default when nothing was ever written" posture ADR-039 gives the
// coordinator's own render.settings store, reapplied here for a value that
// predates the field entirely. A JSON null or any other present-but-invalid
// value (wrong type, empty, unrecognized) is refused rather than silently
// defaulted: the coordinator always resolves and sends a concrete value
// going forward, so a malformed one here means something upstream is wrong,
// not that nothing was configured.
func parseIdleOutput(action, surfaceID string, params map[string]any, logger pipeline.Logger) (string, error) {
	raw, ok := params["idleOutput"]
	if !ok {
		// Logged, not silent: black is a real choice about what this
		// surface shows an operator for the whole life of the assignment,
		// and one nobody made here.
		logger.Warn("render assignment carries no idle output; this surface will draw black whenever it is idle",
			"action", action, "surface_id", surfaceID, "using", pipeline.IdleOutputBlack)
		return pipeline.IdleOutputBlack, nil
	}
	v, isStr := raw.(string)
	if !isStr || v == "" {
		return "", fmt.Errorf("%s: params.idleOutput must be a non-empty string, got %T", action, raw)
	}
	if !idleOutputKnown[v] {
		return "", fmt.Errorf("%s: params.idleOutput %q must be one of black, hold, diagnostic", action, v)
	}
	return v, nil
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

// parseAssignmentAuth extracts params' optional TRACK-H-H3-SPEC.md section 7
// authorization tuple ("generation", "catalogRevision", plus "show") for
// persisting on the resulting [pipeline.Assignment]. Absent means an
// assignment applied before H3's boot-clearing rule existed, or one applied
// by a coordinator that has not yet started sending H4's full activation
// envelope — both are the same "no tuple" case downstream (build item 4:
// treated as unauthorized at boot, never grandfathered), so this function
// returns (nil, nil) rather than a zero-value tuple, and the caller
// distinguishes the two via the pointer.
//
// "show" is NOT part of this presence test: it was already a
// renderApplyKnownKeys member and a required top-level render.surface.apply
// field before this seam existed (internal/coordinator/api/renderdispatch.go's
// renderApplyParamsPayload.Show has no omitempty and is always populated by
// the real coordinator), so every real apply already carries it regardless
// of whether H3's tuple is present. Only "generation" and "catalogRevision"
// are new with this seam, so only they gate whether the tuple is present at
// all; a caller sending exactly one of those two (and not the other) is a
// bug upstream, not a case to silently paper over with a mixed real/zero
// tuple that could pass a later boot comparison by accident. "show" is read
// for the tuple once the other two are present.
func parseAssignmentAuth(action string, params map[string]any) (*pipeline.AssignmentAuth, error) {
	_, hasGeneration := params["generation"]
	_, hasCatalogRevision := params["catalogRevision"]
	if !hasGeneration && !hasCatalogRevision {
		return nil, nil
	}
	if !hasGeneration || !hasCatalogRevision {
		return nil, fmt.Errorf("%s: params.generation and params.catalogRevision must be supplied together or not at all", action)
	}

	show, ok := params["show"].(string)
	if !ok || show == "" {
		return nil, fmt.Errorf("%s: params.show must be a non-empty string, got %T", action, params["show"])
	}
	catalogRevision, ok := params["catalogRevision"].(string)
	if !ok || catalogRevision == "" {
		return nil, fmt.Errorf("%s: params.catalogRevision must be a non-empty string, got %T", action, params["catalogRevision"])
	}

	var generation int64
	switch v := params["generation"].(type) {
	case float64:
		generation = int64(v)
	case int64:
		generation = v
	case int:
		generation = int64(v)
	default:
		return nil, fmt.Errorf("%s: params.generation must be a number, got %T", action, params["generation"])
	}

	return &pipeline.AssignmentAuth{Show: show, Generation: generation, CatalogRevision: catalogRevision}, nil
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

// isRenderAction reports whether action is one of this seam's five
// allowlisted render.* operations — used by command.go's HandleMessage to
// decide whether to signal renderTrigger after a genuinely-executed
// command.
func isRenderAction(action string) bool {
	switch action {
	case "render.surface.apply", "render.surface.clear", "render.surface.blackout", "render.pipeline.restart", "render.transport.probe":
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

	// showMode is handed to every frame writer this type starts, which
	// reads it fresh at the point of decision (ADR-036 decision 1). It is
	// deliberately NOT resolved here into a value baked into an
	// assignment: a mode flip must change what a live surface draws, not
	// tear down and rebuild its frame writer. May be nil, which behaves as
	// Show Mode.
	showMode pipeline.ShowModeSource

	// diagnosticSurfaceID is the surface this node was locally configured
	// to draw the diagnostic idle pattern on, or "" for none. Fixed at
	// construction and read without a lock: it is start-time node
	// configuration, never something a command changes. See idleOutputFor.
	diagnosticSurfaceID string

	// holdBlackStore persists which surfaces currently have an active
	// render.surface.blackout (build item 3), separately from
	// AssignmentStore since a surface may be held black with no assignment
	// at all — see [pipeline.HoldBlackStore]'s own doc comment.
	holdBlackStore *pipeline.HoldBlackStore

	mu      sync.Mutex
	writers map[string]*frameWriterHandle

	// holdBlack is the in-memory mirror of holdBlackStore's own persisted
	// set, loaded once at construction (before any frame writer starts, so
	// boot resume comes up black) and read fresh, at the point of decision,
	// by every frame writer's own [surfaceHoldBlackSource] on every tick.
	// Guarded by mu, alongside writers/timelineStepOwner. An entry present
	// here always wins over holdBlackDefaultAll, explicit true or explicit
	// false alike.
	holdBlack map[string]bool

	// holdBlackDefaultAll is true only when holdBlackStore.Load failed at
	// construction (an unreadable or corrupt file): every surface with no
	// explicit entry in holdBlack is then held black, because black is the
	// safe direction for a stop and this node cannot honestly say which
	// surfaces the lost file held black. Cleared per surface the moment
	// that surface gets an explicit entry (blackoutSurface or
	// clearHeldBlack); never reset as a whole.
	holdBlackDefaultAll bool

	// holdBlackDefaultMaterialized is true once
	// materializeHeldBlackDefaultOnFirstWrite has already run, so a later
	// write does not repeat persisting every currently assigned surface's
	// default truth. Meaningless (and left false) when holdBlackDefaultAll
	// is false. Deliberately separate from holdBlackDefaultAll itself:
	// materializing is a one-time disk backfill, never a reason to lift
	// the in-memory default for a surface this node still has no explicit
	// entry for — see holdBlackDefaultAll's own "never reset as a whole".
	holdBlackDefaultMaterialized bool

	// timelineStepOwner and timelineStepMS record which surface last set
	// o.timeline's shared step time, and to what value — see
	// applyTimelineStepTime's own doc comment for why a shared Timeline
	// needs this at all. "" means no surface has ever set one.
	timelineStepOwner string
	timelineStepMS    byte

	// probeStarter is the [pipeline.ProcessStarter] probeTransport passes
	// to [pipeline.ProbeNDISend]. nil (the production default, set by
	// [newRenderOperations]) selects the real process starter; tests in
	// this package set it directly (same-package access, no exported
	// setter needed) to exercise probeTransport's wiring without shelling
	// out to a real gst-launch-1.0 — pipeline/probe_test.go already covers
	// the probe mechanism itself exhaustively against fakes and reality.
	probeStarter pipeline.ProcessStarter

	// weatherDelay, when set, holds every frame writer's timeline at
	// stopped during a delay, so MultiSync cannot drive content.
	weatherDelay *WeatherDelayHolder
}

func newRenderOperations(sup *pipeline.Supervisor, store *pipeline.AssignmentStore, holdBlackStore *pipeline.HoldBlackStore, assetDir string, timeline *multisync.Timeline, showMode pipeline.ShowModeSource, diagnosticSurfaceID string, logger pipeline.Logger) *renderOperations {
	holdBlack, err := holdBlackStore.Load()
	holdBlackDefaultAll := false
	if err != nil {
		// Black is the safe direction for a stop, and this node cannot
		// honestly say which surfaces the lost file held black — so every
		// surface with no explicit entry comes up held black, not lit,
		// until an authorized cue.activate or render.surface.apply clears
		// it per surface (see holdBlackDefaultAll's own doc comment).
		logger.Warn("failed to load persisted held-black state at startup; holding every surface black until it is explicitly cleared, because black is the safe direction for a stop", "error", err)
		holdBlack = map[string]bool{}
		holdBlackDefaultAll = true
	}
	return &renderOperations{
		sup:                 sup,
		store:               store,
		holdBlackStore:      holdBlackStore,
		assetDir:            assetDir,
		timeline:            timeline,
		showMode:            showMode,
		diagnosticSurfaceID: diagnosticSurfaceID,
		logger:              logger,
		writers:             make(map[string]*frameWriterHandle),
		holdBlack:           holdBlack,
		holdBlackDefaultAll: holdBlackDefaultAll,
	}
}

// isHeldBlack reports whether surfaceID currently has an active
// render.surface.blackout in effect: an explicit entry always wins, and a
// surface never explicitly touched falls back to holdBlackDefaultAll (see
// its own doc comment).
func (o *renderOperations) isHeldBlack(surfaceID string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if v, ok := o.holdBlack[surfaceID]; ok {
		return v
	}
	return o.holdBlackDefaultAll
}

// knownHeldBlackSurfaceIDs returns every surface id this node currently
// holds an explicit held-black entry for (blackoutSurface has run against
// it), sorted. It does not include a surface only defaulted black by
// holdBlackDefaultAll with no explicit entry yet, or already lit
// (explicit false): a surface with an explicit true entry is exactly the
// set publishOneRenderReport (renderreport.go) needs to synthesize a
// "blacked out" report row for when that surface has no FrameWriter to
// report it through — render.surface.blackout on a surface with no
// current assignment (build item 1) never starts one.
func (o *renderOperations) knownHeldBlackSurfaceIDs() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]string, 0, len(o.holdBlack))
	for id, held := range o.holdBlack {
		if held {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// setHeldBlack updates surfaceID's in-memory held-black flag. Callers
// persist through holdBlackStore themselves first (blackoutSurface,
// clearHeldBlack) so disk and memory never disagree about which surface
// this call is even for.
func (o *renderOperations) setHeldBlack(surfaceID string, held bool) {
	o.mu.Lock()
	o.holdBlack[surfaceID] = held
	o.mu.Unlock()
}

// materializeHeldBlackDefaultOnFirstWrite persists this node's actual
// held-black truth for every surface it knows about, the first time
// anything writes to holdBlackStore after a corrupt or unreadable file
// forced holdBlackDefaultAll at construction: every surface named in the
// assignment store is written held black, except one that already has an
// explicit in-memory entry (set true by an earlier blackoutSurface this
// session, or cleared false by an earlier clearHeldBlack). Without this,
// the FIRST write after a corrupt file (a single-surface Set call) is the
// only chance to record every OTHER surface's default-black truth:
// holdBlackStore.Set's own recovery starts from an EMPTY set on a load
// failure, so that first write would otherwise persist a file naming only
// the one surface just touched, silently losing every other surface's
// black default the moment the corrupt file is overwritten. A no-op once
// holdBlackDefaultAll is false (the file loaded cleanly at construction)
// or holdBlackDefaultMaterialized is already true (this already ran once
// this process's lifetime) — it never clears holdBlackDefaultAll itself,
// only backfills the disk record while that default stays in force for
// any surface this node still has no explicit entry for.
func (o *renderOperations) materializeHeldBlackDefaultOnFirstWrite() error {
	o.mu.Lock()
	if !o.holdBlackDefaultAll || o.holdBlackDefaultMaterialized {
		o.mu.Unlock()
		return nil
	}
	alreadyExplicit := make(map[string]bool, len(o.holdBlack))
	for id := range o.holdBlack {
		alreadyExplicit[id] = true
	}
	o.mu.Unlock()

	assignments, err := o.store.Load()
	if err != nil {
		return fmt.Errorf("materializing held-black default: loading assignments: %w", err)
	}
	for _, a := range assignments {
		if alreadyExplicit[a.SurfaceID] {
			continue
		}
		o.setHeldBlack(a.SurfaceID, true)
		if err := o.holdBlackStore.Set(a.SurfaceID, true); err != nil {
			return fmt.Errorf("materializing held-black default for surface %q: %w", a.SurfaceID, err)
		}
	}

	o.mu.Lock()
	o.holdBlackDefaultMaterialized = true
	o.mu.Unlock()
	return nil
}

// clearHeldBlack persists and records surfaceID as no longer held black —
// build item 2's two clearing paths (an authorized cue.activate swap or
// confirmation, and render.surface.apply) both call this.
func (o *renderOperations) clearHeldBlack(surfaceID string) error {
	if err := o.materializeHeldBlackDefaultOnFirstWrite(); err != nil {
		return fmt.Errorf("clearing held-black flag for surface %q: %w", surfaceID, err)
	}
	if err := o.holdBlackStore.Set(surfaceID, false); err != nil {
		return err
	}
	o.setHeldBlack(surfaceID, false)
	return nil
}

// surfaceHoldBlackSource implements [pipeline.HoldBlackSource] for one
// surface: held black either by an explicit render.surface.blackout, or
// because a weather delay is currently active (build item 4 — the same
// forced-black path, never refused for a weather delay and never routed
// around it).
type surfaceHoldBlackSource struct {
	o         *renderOperations
	surfaceID string
}

func (s surfaceHoldBlackSource) HoldBlack() bool {
	if s.o.isHeldBlack(s.surfaceID) {
		return true
	}
	return s.o.weatherDelay != nil && s.o.weatherDelay.Current().Active
}

// frameTimeline is the timeline a frame writer reads.
func (o *renderOperations) frameTimeline() pipeline.TimelineSource {
	if o.weatherDelay == nil {
		return o.timeline
	}
	return weatherDelayGatedTimeline{timeline: o.timeline, holder: o.weatherDelay}
}

// weatherDelayGatedTimeline reports the timeline as stopped while a weather
// delay is active, whatever MultiSync says.
type weatherDelayGatedTimeline struct {
	timeline pipeline.TimelineSource
	holder   *WeatherDelayHolder
}

func (g weatherDelayGatedTimeline) Snapshot() multisync.Snapshot {
	snap := g.timeline.Snapshot()
	if g.holder.Current().Active {
		snap.State = multisync.StateStopped
		snap.PositionMS = 0
	}
	return snap
}

// idleOutputFor resolves which idle output a surface's writer is built
// with: its assignment's own value, unless this node was locally configured
// to draw the diagnostic pattern on that exact surface, which wins.
//
// The node-local setting takes precedence over the assigned one on purpose
// (TRACK-B-BUILD-CONTRACT.md ruling 3's node-local amendment). Refusing
// instead would defeat the whole point on the nodes this was built for: a
// node holding a persisted assignment resumes it at boot with no
// coordinator, the dead FPP master leaves the timeline unknown, unknown is
// an idle state, and the assigned idle output defaults to black. The
// operator would be standing in front of a black wall with the diagnostic
// surface configured and refused. Content still draws whenever the timeline
// plays, so this changes a running show not at all.
func (o *renderOperations) idleOutputFor(surfaceID, assigned string) string {
	if o.diagnosticSurfaceID != "" && surfaceID == o.diagnosticSurfaceID {
		return pipeline.IdleOutputDiagnostic
	}
	return assigned
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
	// nil for the node-local diagnostic surface, which owns no FSEQ file
	// (see renderdiagnostic.go).
	if h.fseq != nil {
		_ = h.fseq.Close()
	}
}

// hasRunningFrameWriter reports whether surfaceID currently has a live
// entry in o.writers — the actual running state a swap either left in
// place or, on a startFrameWriter failure, never installed. This is the
// ground truth cueactivationrender.go's surfaceAlreadyActivated must
// consult rather than only the persisted assignment: store.Upsert runs
// BEFORE the old writer stops and the new one starts (activateSurfaceRender
// finding 15's own ordering), so a startFrameWriter failure leaves a dark
// surface whose persisted assignment already names the new file — a store-
// only check would then read that surface as "already activated" and skip
// repairing it forever.
func (o *renderOperations) hasRunningFrameWriter(surfaceID string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.writers[surfaceID]
	return ok
}

// applyTimelineStepTime is finding 6's fix: [multisync.Timeline.
// SetStepTime] was never called in production, so the timeline ran
// permanently at [multisync.DefaultStepTime] regardless of what any real
// FSEQ actually specified — wrong for the first second of every show (see
// pkg/multisync/timeline.go's positionFromPacketLocked, which falls back to
// FrameNumber*stepTime whenever SecondsElapsed is unusable) and for any FPP
// 8.x master.
//
// SHARED-TIMELINE DECISION (build contract seam B3, "decide what happens
// when two surfaces have different step times... do not silently let the
// last apply win"): this node's Timeline is one shared instance across
// every surface (ADR-026 N=1 is a renderer scope limit, not a reason to
// build N timelines), but step time is read from each surface's own FSEQ,
// so two surfaces with differently-authored FSEQs could disagree. The rule
// here is FIRST SURFACE WINS: whichever surface's apply set the step time
// first keeps it for as long as it remains applied (see
// releaseTimelineStepTimeIfOwner); a later surface reporting a DIFFERENT
// step time never silently overrides it — it is logged loudly instead, so
// the conflict is visible rather than swallowed as "whoever applied most
// recently." A same-surface re-apply with a changed FSEQ still updates its
// own value. This is deliberately not "last apply wins": that would make
// the position every OTHER surface renders from silently drift depending on
// dispatch order, which is worse than picking one surface and saying so.
// Today's coordinator-side validation only ever assigns one surface per
// node (N=1), so this conflict path is not currently reachable in
// production; it exists so a future N>1 node degrades loudly instead of
// silently the day that changes.
func (o *renderOperations) applyTimelineStepTime(surfaceID string, stepTimeMS byte) {
	o.mu.Lock()
	owner := o.timelineStepOwner
	current := o.timelineStepMS
	sameOwnerOrUnowned := owner == "" || owner == surfaceID
	conflict := !sameOwnerOrUnowned && current != stepTimeMS
	if sameOwnerOrUnowned {
		o.timelineStepOwner = surfaceID
		o.timelineStepMS = stepTimeMS
	}
	o.mu.Unlock()

	if conflict {
		o.logger.Warn("render: surface's FSEQ step time conflicts with the shared timeline's current owner; keeping the owner's step time rather than silently overriding it",
			"surface_id", surfaceID, "requested_step_ms", stepTimeMS, "owner_surface_id", owner, "owner_step_ms", current)
		return
	}
	o.timeline.SetStepTime(time.Duration(stepTimeMS) * time.Millisecond)
}

// releaseTimelineStepTimeIfOwner clears the shared timeline's step-time
// ownership when the OWNING surface is the one being cleared, so a
// subsequently-applied surface (with a different FSEQ) can claim it rather
// than being permanently blocked by a surface that no longer exists. A
// no-op for any other surface: clearing a non-owning surface must never
// affect the timeline another surface is actively depending on.
func (o *renderOperations) releaseTimelineStepTimeIfOwner(surfaceID string) {
	o.mu.Lock()
	if o.timelineStepOwner == surfaceID {
		o.timelineStepOwner = ""
	}
	o.mu.Unlock()
}

// buildFSEQAssignment parses a render.surface.apply params map. channelRange,
// geometry, frameRate, and idleOutput are REQUIRED regardless of whether
// this assignment carries real FSEQ content: every real caller (the
// coordinator's establish-on-deploy path and a resolved render.surface.apply
// dispatch alike) always sends a surface's complete geometry, and a
// bare or partial-geometry request is refused outright here rather than
// silently accepted: there is no longer a "not yet consumed" partial
// acceptance for these fields (see buildAssignedSpec's own doc comment for
// why: this seam now builds a real, idle-output-drawing pipeline for the
// no-content case, which needs real geometry to size itself).
//
// ok is false when fseqFilename is absent; this is not an error: an
// established assignment with no FSEQ content resolved yet is a valid
// request, matching renderApplyKnownKeys' "accepted, validated where
// practical" posture for the FSEQ-specific pair alone.
func buildFSEQAssignment(action, surfaceID string, params map[string]any, logger pipeline.Logger) (a fseqAssignment, ok bool, err error) {
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

	idleOutput, verr := parseIdleOutput(action, surfaceID, params, logger)
	if verr != nil {
		return fseqAssignment{}, false, verr
	}

	base := fseqAssignment{
		channelStart0: startChannel1Based - 1,
		channelCount:  channelCount,
		width:         width,
		height:        height,
		pixelFormat:   pixelFormat,
		frameRate:     frameRate,
		idleOutput:    idleOutput,
	}

	rawFilename, has := params["fseqFilename"]
	if !has {
		return base, false, nil
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

	base.fseqFilename = filename
	base.fseqContentHash = contentHash
	return base, true, nil
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
	idleOutput      string
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
// information, idle) pipeline spec and, for the real case, its
// [pipeline.FrameWriter] source, not yet started. assetDir-relative
// resolution of fseqFilename and content-hash verification both happen
// here: ADR-028 requires a node never resolve an asset by filename alone
// trusting the coordinator's say-so blindly, so a content-hash mismatch
// refuses the assignment rather than rendering unverified bytes.
//
// The no-FSEQ-content branch returns a *fseq.File of nil and a populated
// fseqAssignment (geometry, pixel format, frame rate, idle output): the
// caller (applySurface/ResumeAssignment's startFrameWriter call) reads a
// nil file as "start this surface's [pipeline.NewIdleFrameWriter] instead
// of a real one" and therefore must always call startFrameWriter now,
// content or not: this must draw and report its own configured idle
// output honestly, never a content-free pipeline with no frame writer and
// no output.mode/output.failure evidence at all.
func buildAssignedSpec(action, assetDir, surfaceID string, params map[string]any, logger pipeline.Logger) (pipeline.Spec, *fseq.File, fseqAssignment, outputSinkOutcome, error) {
	a, ok, err := buildFSEQAssignment(action, surfaceID, params, logger)
	if err != nil {
		return pipeline.Spec{}, nil, fseqAssignment{}, outputSinkOutcome{}, err
	}
	if !ok {
		idleSpec, serr := pipeline.FSEQSourceSpec(surfaceID, a.width, a.height, a.pixelFormat, a.frameRate, pipeline.FdsrcSupportsIsLive(nil))
		if serr != nil {
			return pipeline.Spec{}, nil, fseqAssignment{}, outputSinkOutcome{}, fmt.Errorf("%s: %w", action, serr)
		}
		spec, sinkOutcome, serr := applyOutputSink(idleSpec, surfaceID, params)
		if serr != nil {
			return pipeline.Spec{}, nil, fseqAssignment{}, outputSinkOutcome{}, fmt.Errorf("%s: %w", action, serr)
		}
		return spec, nil, a, sinkOutcome, nil
	}

	path := filepath.Join(assetDir, a.fseqFilename)
	gotHash, err := hashFile(path)
	if err != nil {
		return pipeline.Spec{}, nil, fseqAssignment{}, outputSinkOutcome{}, fmt.Errorf("%s: reading fseq asset %q: %w", action, a.fseqFilename, err)
	}
	if gotHash != a.fseqContentHash {
		return pipeline.Spec{}, nil, fseqAssignment{}, outputSinkOutcome{}, fmt.Errorf(
			"%s: fseq asset %q content hash %q does not match assignment's %q",
			action, a.fseqFilename, gotHash, a.fseqContentHash)
	}

	f, err := fseq.Open(path)
	if err != nil {
		return pipeline.Spec{}, nil, fseqAssignment{}, outputSinkOutcome{}, fmt.Errorf("%s: opening fseq asset %q: %w", action, a.fseqFilename, err)
	}

	spec, err := pipeline.FSEQSourceSpec(surfaceID, a.width, a.height, a.pixelFormat, a.frameRate, pipeline.FdsrcSupportsIsLive(nil))
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
	const action = "render.surface.apply"

	spec, f, a, sinkOutcome, err := buildAssignedSpec(action, o.assetDir, surfaceID, params, o.logger)
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
		o.applyTimelineStepTime(surfaceID, f.StepTimeMS())
	}
	if err := o.startFrameWriter(surfaceID, f, a); err != nil {
		if f != nil {
			_ = f.Close()
		}
		return fmt.Errorf("%s: starting frame writer: %w", action, err)
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
// params AND the FSEQ assignment they describe, persist only once that
// validation has actually passed, start the surface's pipeline, and poll
// for post-dispatch evidence that it reached running before reporting
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
	auth, err := parseAssignmentAuth(action, params)
	if err != nil {
		return OperationResult{}, err
	}

	rawParams, err := json.Marshal(params)
	if err != nil {
		return OperationResult{}, fmt.Errorf("%s: encoding params for persistence: %w", action, err)
	}

	executedAt := now()

	// buildAssignedSpec BEFORE persisting (finding 10): it is the step that
	// actually validates this assignment (content-hash check, FSEQ open,
	// FSEQSourceSpec's geometry/pixel-format checks). Persisting first meant
	// a REJECTED apply still overwrote the surface's last known-good
	// assignment on disk — the running pipeline stayed up and the operator
	// was told the apply failed, so nothing looked wrong, but the next boot
	// would resume the broken one (or fail per finding 9) with the good one
	// permanently gone. Validate, then persist only what actually passed.
	spec, f, a, sinkOutcome, err := buildAssignedSpec(action, o.assetDir, surfaceID, params, o.logger)
	if err != nil {
		return OperationResult{}, err
	}

	if err := o.store.Upsert(pipeline.Assignment{SurfaceID: surfaceID, RawParams: rawParams, AppliedAt: executedAt, Auth: auth}); err != nil {
		if f != nil {
			_ = f.Close()
		}
		return OperationResult{}, fmt.Errorf("%s: persisting assignment: %w", action, err)
	}

	// Read the surface's current process-attempt generation BEFORE
	// dispatching Apply, so awaitAndReport can require the confirmed
	// StateRunning to describe a strictly later attempt than whatever was
	// running (or not) before this call — see [pipeline.Supervisor.
	// AwaitState]'s doc comment on the race this closes (finding 2).
	baseline := o.sup.Generation(surfaceID)

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
		// Finding 6: the shared Timeline's step time comes from whichever
		// surface owns it — see applyTimelineStepTime's own doc comment for
		// the shared-timeline decision. An idle writer (f == nil) owns no
		// content and therefore no step time to contribute.
		o.applyTimelineStepTime(surfaceID, f.StepTimeMS())
	}
	if err := o.startFrameWriter(surfaceID, f, a); err != nil {
		if f != nil {
			_ = f.Close()
		}
		return OperationResult{}, fmt.Errorf("%s: starting frame writer: %w", action, err)
	}

	// Build item 2: render.surface.apply clears a held-black flag, alongside
	// an authorized cue.activate swap or confirmation (cueactivationrender.go).
	// No restart follows this: the frame writer just started above already
	// draws whatever this apply assigned.
	if err := o.clearHeldBlack(surfaceID); err != nil {
		return OperationResult{}, fmt.Errorf("%s: clearing held-black flag: %w", action, err)
	}

	return o.awaitAndReport(ctx, surfaceID, []pipeline.State{pipeline.StateRunning}, executedAt, baseline)
}

// startFrameWriter builds and launches surfaceID's [pipeline.FrameWriter],
// wiring it to o.sup and o.timeline. f is the already-open fseq file for a
// real assignment, or nil for one with no FSEQ content resolved yet
// (buildAssignedSpec's own doc comment): this function ALWAYS starts a
// writer either way, real or idle, so a surface with nothing to draw still
// reports its own configured idle output honestly rather than going dark
// with no draw-state evidence at all. The caller keeps ownership of a
// non-nil f on error (closes it itself); on success the writer's own
// lifetime (via [renderOperations.stopFrameWriter]) owns closing it.
func (o *renderOperations) startFrameWriter(surfaceID string, f *fseq.File, a fseqAssignment) error {
	idleOutput := o.idleOutputFor(surfaceID, a.idleOutput)
	if idleOutput != a.idleOutput {
		o.logger.Info("this node's locally configured diagnostic idle output is overriding the surface's assigned one",
			"surface_id", surfaceID, "assigned_idle_output", a.idleOutput, "using", idleOutput)
	}
	holdBlack := surfaceHoldBlackSource{o: o, surfaceID: surfaceID}
	var fw *pipeline.FrameWriter
	var err error
	if f != nil {
		fw, err = pipeline.NewFrameWriter(o.sup, surfaceID, f, o.frameTimeline(), a.fseqFilename, a.channelStart0, a.channelCount, a.width, a.height, idleOutput, o.showMode, holdBlack, o.logger)
	} else {
		fw, err = pipeline.NewIdleFrameWriter(o.sup, surfaceID, a.width, a.height, a.pixelFormat, a.frameRate, idleOutput, holdBlack, o.logger)
	}
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
	o.releaseTimelineStepTimeIfOwner(surfaceID)

	if err := o.sup.Clear(surfaceID); err != nil {
		return OperationResult{}, fmt.Errorf("%s: %w", action, err)
	}
	if err := o.store.Remove(surfaceID); err != nil {
		return OperationResult{}, fmt.Errorf("%s: removing persisted assignment: %w", action, err)
	}

	// A cleared surface has no assignment left to resume rendering onto,
	// so a held-black flag from before this clear must not survive it and
	// come back on a later, unrelated assignment. Logged, never returned:
	// the clear itself (stop the pipeline, remove the assignment) already
	// succeeded by this point, matching cue.activate (render)'s identical
	// reasoning for its own held-black bookkeeping write.
	if err := o.clearHeldBlack(surfaceID); err != nil {
		o.logger.Warn("render.surface.clear: failed to clear held-black flag", "surface_id", surfaceID, "error", err)
	}

	// No generation floor here (-1): cmdClear always synchronously calls
	// setState(Stopped, ...) as part of processing this exact command, so
	// ObservedAt freshness alone already proves the evidence postdates it —
	// see [pipeline.Supervisor.AwaitState]'s doc comment.
	return o.awaitAndReport(ctx, surfaceID, []pipeline.State{pipeline.StateStopped}, executedAt, -1)
}

// blackoutSurface is the OperationFunc for "render.surface.blackout": set a
// hold-black flag for surfaceID so its frame writer, if one is running,
// draws forced black on every tick from here on (build item 1) — the
// assignment, the pipeline process, and the NDI source are all left
// untouched, and no restart happens. Idempotent, and succeeds identically
// for a surface with no current assignment at all: there is nothing to draw
// yet, but the flag is still recorded, so a later apply or cue activation
// comes up black until something clears it (build items 2 and 3). Never
// refused for a weather delay: this operation touches nothing decision 3 or
// 4 of ADR-053 already gates.
//
// Synchronous, like probeTransport: setting the flag has no asynchronous
// pipeline state to await, so Confirmed reports true as soon as it is
// durably recorded.
func (o *renderOperations) blackoutSurface(ctx context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
	const action = "render.surface.blackout"

	surfaceID, err := parseSurfaceID(action, params)
	if err != nil {
		return OperationResult{}, err
	}
	if err := rejectUnknownKeys(action, params, renderSurfaceKnownKeys); err != nil {
		return OperationResult{}, err
	}

	executedAt := now()

	// The in-memory flag is set FIRST, always, before persistence is even
	// attempted: a running frame writer reads it fresh on its very next
	// tick (surfaceHoldBlackSource), so the surface is already drawing
	// black by the time this function does anything else. Persisting it
	// can still fail, but that must never leave the flag unset — a stop
	// that fails lit because its own bookkeeping write failed is the exact
	// hazard this ordering closes.
	o.setHeldBlack(surfaceID, true)
	// materializeHeldBlackDefaultOnFirstWrite runs AFTER the target
	// surface's own in-memory flag is already set (never fails lit), but
	// BEFORE persisting it: it is the one chance to persist every OTHER
	// surface's corrupt-file default truth before that default is lost —
	// see its own doc comment. This surface is already in o.holdBlack by
	// now, so it skips re-persisting it here; the call below still does.
	if err := o.materializeHeldBlackDefaultOnFirstWrite(); err != nil {
		return OperationResult{}, fmt.Errorf("%s: surface is drawing black, but the held-black flag could not be fully persisted: %w", action, err)
	}
	if err := o.holdBlackStore.Set(surfaceID, true); err != nil {
		return OperationResult{}, fmt.Errorf("%s: surface is drawing black, but the held-black flag could not be persisted: %w", action, err)
	}

	// running names whether a frame writer is currently on this surface:
	// build item 4 (coordinator side, renderdispatch.go) reads this off the
	// command's own direct result to confirm promptly when it is false —
	// nothing is drawing, so the surface is already dark, and no emergency
	// stop should burn a full confirmation deadline waiting for a periodic
	// report that a silent surface will never produce.
	running := o.hasRunningFrameWriter(surfaceID)

	observedAt := now()
	return OperationResult{
		Confirmed: true,
		Signal:    "node.render.surface_state",
		Value: map[string]any{
			"surfaceId": surfaceID,
			"heldBlack": true,
			"running":   running,
		},
		ExecutedAt: executedAt,
		ObservedAt: observedAt,
	}, nil
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

	// Same race as applySurface (finding 2): Restart also kills the current
	// process and starts a new one, so the confirmed StateRunning must
	// describe the new attempt, not the one just killed.
	baseline := o.sup.Generation(surfaceID)

	if err := o.sup.Restart(surfaceID); err != nil {
		return OperationResult{}, fmt.Errorf("%s: %w", action, err)
	}

	return o.awaitAndReport(ctx, surfaceID, []pipeline.State{pipeline.StateRunning}, executedAt, baseline)
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

	// Finding 18: refuse cleanly BEFORE running the probe process at all
	// when this node has never applied surfaceID — otherwise the probe
	// still ran, and [pipeline.Supervisor.SetTransportProbe] would have
	// silently discarded its own result (it no longer creates a runner on
	// demand), reporting Confirmed off evidence nothing kept. A typo'd or
	// stale surface id gets an honest, named refusal instead of a phantom
	// `surface` resource that persists on the dashboard forever with no
	// discoverable removal path.
	if _, ok := o.sup.Snapshot(surfaceID); !ok {
		return OperationResult{}, fmt.Errorf("%s: surface %q has never been applied on this node; nothing to probe", action, surfaceID)
	}

	executedAt := now()
	result := pipeline.ProbeNDISend(ctx, o.probeStarter)
	observedAt := now()

	if !o.sup.SetTransportProbe(surfaceID, transport, result.Available, result.Reason, observedAt) {
		// The surface existed moments ago (checked above) but is gone now
		// — vanishingly unlikely (nothing in this package deletes a
		// runner), but never silently drop a completed probe's result.
		return OperationResult{}, fmt.Errorf("%s: surface %q no longer has a runner to record this probe against", action, surfaceID)
	}

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
// executedAt AND from a process-attempt generation strictly after
// afterGeneration (finding 2 — see [pipeline.Supervisor.AwaitState]'s doc
// comment; pass -1 to disable the generation check for a caller with no
// analogous risk), that surfaceID reached one of want, bounded by
// renderConfirmDeadline (and ctx). It never returns a non-nil error for
// "did not confirm in time" — that is Confirmed: false, per
// [OperationResult]'s own Confirmed doc comment: the operation was
// dispatched successfully; whether the read-back evidence corroborates it
// is a separate question.
func (o *renderOperations) awaitAndReport(ctx context.Context, surfaceID string, want []pipeline.State, executedAt time.Time, afterGeneration int64) (OperationResult, error) {
	awaitCtx, cancel := context.WithTimeout(ctx, renderConfirmDeadline)
	defer cancel()

	snap, found := o.sup.AwaitState(awaitCtx, surfaceID, want, executedAt, afterGeneration, renderConfirmPollInterval)

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
