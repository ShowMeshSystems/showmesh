package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	agentclock "github.com/showmeshsystems/showmesh/internal/agent/clock"
	"github.com/showmeshsystems/showmesh/internal/agent/heldcatalog"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// This file is ADR-051's node-side start path: a MultiSync sequence
// OPEN/START/STOP packet, matched against this node's own held Cue
// catalog, drives the SAME [audio.Manager] calls
// internal/agent/cueactivationaudio.go's coordinator-driven activateAudio
// makes, with no coordinator round trip in the path (decision 1). See
// docs/decisions/ADR-051-cue-audio-starts-on-the-multisync-start-packet.md
// and docs/research/RES-002-fpp-multisync-compatibility.md's "Semantics a
// listener must implement" before changing anything here.

// cueAudioTriggerEntry is one FPP sequence filename's resolved target: the
// Cue that filename starts and that Cue's audio output, projected from the
// node's held Cue catalog (ADR-051 decision 2).
type cueAudioTriggerEntry struct {
	CueID string
	Audio cuecatalog.AudioOutput
}

// cueAudioTriggerTable maps an FPP sequence filename to the Cue audio it
// starts.
type cueAudioTriggerTable map[string]cueAudioTriggerEntry

// buildCueAudioTriggerTable projects entries into a
// [cueAudioTriggerTable]: every entry with an audio output and at least
// one trigger filename contributes one table row per filename (ADR-051
// decision 2). An entry with no audio output, or no triggers at all
// (older coordinator, or a Cue nothing yet binds), contributes nothing,
// which is exactly how this node keeps working with a catalog that has
// never carried triggers: an empty table answers every lookup with "not
// found," the coordinator-only start path this node already ran before
// ADR-051 existed.
func buildCueAudioTriggerTable(entries []cuecatalog.Entry) cueAudioTriggerTable {
	table := make(cueAudioTriggerTable)
	for _, e := range entries {
		if e.Outputs.Audio == nil || len(e.Triggers) == 0 {
			continue
		}
		for _, filename := range e.Triggers {
			if filename == "" {
				continue
			}
			table[filename] = cueAudioTriggerEntry{CueID: e.CueID, Audio: *e.Outputs.Audio}
		}
	}
	return table
}

// multiSyncCueAudioTrigger is the node-side consumer of every MultiSync
// sequence packet: it holds this node's own cache of the current cue
// audio trigger table, and drives audio start/stop against [audio.
// Manager] on OPEN/START/STOP.
//
// Constructed once in agent.go, before the MultiSync listener goroutine
// starts (runMultiSyncListener needs a value to call into immediately).
// Its real sources, the held-catalog store, the audio Manager, the
// shared start-trigger evidence registry, and the render Timeline (for
// its own step time, STOP's blanking grace), are wired in once via
// SetSources, after agent.go has constructed them, matching this
// codebase's established "holder built early, filled in once its
// dependencies exist" pattern (see agent.go's own ShowModeHolder).
// HandleSequencePacket is a silent no-op before that first SetSources
// call, the same tolerance rangesFunc(fppConnect) already extends
// multisync.go's discover-ping path for a dependency that is not wired
// yet.
type multiSyncCueAudioTrigger struct {
	logger *slog.Logger
	now    func() time.Time

	// audioReportTrigger, when non-nil, is signalled after a MultiSync
	// start so the audio report loop (audioreport.go) can publish the
	// node's own startTrigger evidence immediately instead of waiting for
	// its resting tick, matching command.go's identical use of the same
	// channel after a dispatched command.
	audioReportTrigger chan<- struct{}

	mu           sync.Mutex
	catalogStore *heldcatalog.FileStore
	mgr          *audio.Manager
	assetDir     string
	timeline     *multisync.Timeline
	weatherDelay *WeatherDelayHolder

	tableMu       sync.Mutex
	tableRevision string
	table         cueAudioTriggerTable

	// stateMu guards generation and preparedLate: this hook's own
	// bookkeeping for the ONE show audio session a node ever runs
	// (cueActivationAudioSessionID), ADR-026 N=1 applied to this session
	// the way it already applies to a render node's own surfaces.
	// generation increments on every OPEN, START, or STOP this hook
	// handles (for a recognized filename); a STOP's own deferred grace
	// timer compares its captured generation against the current one
	// before actually stopping, so a START (of this cue's own restart, or
	// any other cue, since there is only ever one show audio session)
	// that lands before the grace elapses cancels the stop rather than
	// killing whatever is playing by the time the timer fires.
	stateMu      sync.Mutex
	generation   uint64
	preparedLate map[string]bool

	// openMu guards openWaiters: filename -> a channel closed once that
	// filename's own OPEN-triggered cold prepare has finished, so START
	// can wait for it without blocking the packet read loop.
	openMu      sync.Mutex
	openWaiters map[string]chan struct{}
}

// newMultiSyncCueAudioTrigger constructs a trigger consumer with no
// sources wired yet. now is injected for tests; production passes
// time.Now, matching every other invocation/revision-minting caller in
// this package (cueactivationaudio.go's activationRevision).
func newMultiSyncCueAudioTrigger(logger *slog.Logger, now func() time.Time, audioReportTrigger chan<- struct{}) *multiSyncCueAudioTrigger {
	return &multiSyncCueAudioTrigger{
		logger:             logger,
		now:                now,
		audioReportTrigger: audioReportTrigger,
		preparedLate:       make(map[string]bool),
		openWaiters:        make(map[string]chan struct{}),
	}
}

// signalAudioReport requests an immediate audio report the same
// non-blocking way command.go's audioReportTrigger send does: a pending
// signal already covers "something changed since the last report," so a
// dropped duplicate here is correct, not lossy.
func (c *multiSyncCueAudioTrigger) signalAudioReport() {
	if c.audioReportTrigger == nil {
		return
	}
	select {
	case c.audioReportTrigger <- struct{}{}:
	default:
	}
}

// SetSources wires this hook's real dependencies. Called exactly once in
// production, from agent.go, once catalogStore and mgr exist; safe to
// call again (a later call simply replaces the sources), which tests use
// freely. The shared start-trigger evidence registry is not one of these:
// it is cueActivationTriggerRegistry, a package-level var this hook reads
// directly, matching activateAudio's own identical convention, see that
// var's own doc comment (audiostarttrigger.go).
func (c *multiSyncCueAudioTrigger) SetSources(catalogStore *heldcatalog.FileStore, mgr *audio.Manager, assetDir string, timeline *multisync.Timeline, weatherDelay *WeatherDelayHolder) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.catalogStore = catalogStore
	c.mgr = mgr
	c.assetDir = assetDir
	c.timeline = timeline
	c.weatherDelay = weatherDelay
}

func (c *multiSyncCueAudioTrigger) sources() (*heldcatalog.FileStore, *audio.Manager, string, *multisync.Timeline) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.catalogStore, c.mgr, c.assetDir, c.timeline
}

// weatherDelayActive reports whether this node currently holds an active
// weather delay, read fresh at the point of decision — see
// [WeatherDelayHolder.Current]'s own doc comment.
func (c *multiSyncCueAudioTrigger) weatherDelayActive() bool {
	c.mu.Lock()
	holder := c.weatherDelay
	c.mu.Unlock()
	return holder != nil && holder.Current().Active
}

// lookup resolves filename against this node's current held catalog,
// rebuilding the cached trigger table only when the held catalog's own
// revision has changed since the last lookup, cheap enough to check on
// every OPEN/START/STOP (a handful of packets per sequence, never on the
// much more frequent SYNC, which this hook never even sees, see
// HandleSequencePacket).
func (c *multiSyncCueAudioTrigger) lookup(filename string, catalogStore *heldcatalog.FileStore) (cueAudioTriggerEntry, bool) {
	rec, ok, err := catalogStore.Load()
	if err != nil || !ok {
		return cueAudioTriggerEntry{}, false
	}
	c.tableMu.Lock()
	if c.tableRevision != rec.Revision {
		c.table = buildCueAudioTriggerTable(rec.Entries)
		c.tableRevision = rec.Revision
	}
	table := c.table
	c.tableMu.Unlock()
	entry, found := table[filename]
	return entry, found
}

// targetIdentity is the identity entry's audio output would carry once
// Applied to a session directly, see [audio.TargetMediaIdentity].
func (c *multiSyncCueAudioTrigger) targetIdentity(entry cueAudioTriggerEntry) string {
	return audio.TargetMediaIdentity(pkgaudio.MediaRef{
		AssetID:     entry.Audio.Asset,
		ContentHash: firstAssetHash(entry.Audio.AssetHashes),
	})
}

// invocation mints a stable-enough [pkgaudio.InvocationID] for one named
// step of a MultiSync-triggered action against entry's Cue: unique per
// cue/step/wall-clock-nanosecond, which is all [pkgaudio.RevisionState]
// needs an invocation id for (redelivery detection uses the id together
// with the revision, and no two packets processed on this hook's single
// goroutine share a nanosecond AND a step).
func (c *multiSyncCueAudioTrigger) invocation(cueID, step string, now time.Time) pkgaudio.InvocationID {
	return pkgaudio.InvocationID(fmt.Sprintf("multisync:%s:%s:%d", cueID, step, now.UnixNano()))
}

// revision derives a session revision through the SAME shared rule
// activateAudio's own activationRevision uses
// ([cueactivation.AudioSessionRevision]): a real wall-clock reading plus a
// small additive step, so a MultiSync-triggered call and a
// coordinator-dispatched one against the same session never
// independently invent two incompatible revision spaces.
func (c *multiSyncCueAudioTrigger) revision(now time.Time, step int) pkgaudio.Revision {
	return pkgaudio.Revision(cueactivation.AudioSessionRevision(now, step))
}

func (c *multiSyncCueAudioTrigger) markPreparedLate(filename string) {
	c.stateMu.Lock()
	c.preparedLate[filename] = true
	c.stateMu.Unlock()
}

func (c *multiSyncCueAudioTrigger) consumePreparedLate(filename string) bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	late := c.preparedLate[filename]
	delete(c.preparedLate, filename)
	return late
}

func (c *multiSyncCueAudioTrigger) bumpGeneration() uint64 {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.generation++
	return c.generation
}

func (c *multiSyncCueAudioTrigger) currentGeneration() uint64 {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.generation
}

// HandleSequencePacket is runMultiSyncListener's own audio-trigger hook:
// multisync.go's handle closure calls this once per received Sync packet
// whose FileType is [multisync.SyncFileTypeSequence]; a Media packet is
// never passed here at all, so ADR-051 decision 1's "Media START packets
// are ignored" is true by construction rather than a runtime check.
//
// ctx bounds only the audio.Manager calls this makes; the caller already
// ties it to agent shutdown.
func (c *multiSyncCueAudioTrigger) HandleSequencePacket(ctx context.Context, pkt multisync.SyncPacket) {
	if pkt.FileType != multisync.SyncFileTypeSequence {
		// Belt and suspenders: the caller (multisync.go) already filters
		// to sequence packets before calling here, but this method's own
		// contract, "Media START packets are ignored" (ADR-051 decision
		// 1; RES-002's own "media START hardcodes secondsElapsed = 0,"
		// which makes a media packet's own position field unusable for
		// this purpose regardless), must hold even if a future caller
		// forgets to filter.
		return
	}
	catalogStore, mgr, assetDir, timeline := c.sources()
	registry := cueActivationTriggerRegistry
	if catalogStore == nil || mgr == nil || registry == nil {
		return
	}

	// Recorded at receive time, before any other work, on the SAME clock
	// a scheduled start's own T0 is read on (ADR-051 decision 1).
	arrival := mgr.MediaNow(ctx)

	entry, ok := c.lookup(pkt.Filename, catalogStore)
	if !ok {
		return
	}

	// ADR-053 decision 3: while a weather delay is active, no MultiSync
	// packet may start show output. OPEN and START are both refused;
	// STOP is left alone, since stopping never starts anything.
	if (pkt.Action == multisync.SyncActionOpen || pkt.Action == multisync.SyncActionStart) && c.weatherDelayActive() {
		c.logger.Info("multisync: ignoring a sequence OPEN/START packet because a weather delay is active",
			"cue_id", entry.CueID, "sequence_filename", pkt.Filename, "action", pkt.Action)
		return
	}

	switch pkt.Action {
	case multisync.SyncActionOpen:
		c.bumpGeneration()
		c.handleOpenAsync(ctx, mgr, assetDir, entry, pkt.Filename)
	case multisync.SyncActionStart:
		c.bumpGeneration()
		c.waitOpen(pkt.Filename)
		c.handleStart(ctx, mgr, assetDir, registry, entry, pkt.Filename, arrival)
	case multisync.SyncActionStop:
		gen := c.bumpGeneration()
		c.handleStop(ctx, mgr, timeline, entry, pkt.Filename, gen)
	}
}

// handleOpenAsync runs handleOpen's own cold prepare on its own goroutine,
// so it never delays this hook's caller (the listener's single read loop)
// from promptly reading and timestamping this sequence's own START.
func (c *multiSyncCueAudioTrigger) handleOpenAsync(ctx context.Context, mgr *audio.Manager, assetDir string, entry cueAudioTriggerEntry, filename string) {
	done := c.beginOpen(filename)
	go func() {
		defer c.endOpen(filename, done)
		c.handleOpen(ctx, mgr, assetDir, entry, filename)
	}()
}

// beginOpen registers filename as having an OPEN prepare in flight and
// returns the channel waitOpen blocks on, called synchronously so a later
// START for the same filename always finds it already registered.
func (c *multiSyncCueAudioTrigger) beginOpen(filename string) chan struct{} {
	c.openMu.Lock()
	defer c.openMu.Unlock()
	done := make(chan struct{})
	c.openWaiters[filename] = done
	return done
}

// endOpen marks filename's OPEN prepare finished, waking any waitOpen call
// blocked on it.
func (c *multiSyncCueAudioTrigger) endOpen(filename string, done chan struct{}) {
	close(done)
	c.openMu.Lock()
	if c.openWaiters[filename] == done {
		delete(c.openWaiters, filename)
	}
	c.openMu.Unlock()
}

// waitOpen blocks until filename's own in-flight OPEN prepare, if any, has
// finished. A no-op when OPEN was never seen for filename (RES-002's own
// "a robust listener must also accept START without a preceding OPEN").
func (c *multiSyncCueAudioTrigger) waitOpen(filename string) {
	c.openMu.Lock()
	done, ok := c.openWaiters[filename]
	c.openMu.Unlock()
	if ok {
		<-done
	}
}

// handleOpen is ADR-051 decision 3's cold-start prepare: if this Cue's
// audio is not already prepared, either directly on the cue session or
// held by the prepare-ahead staging session under this exact media
// (checked by the same identity [Manager.Promote] uses), apply and
// prepare it now, and remember that this filename's eventual START ran
// late (this node was not armed ahead of time).
func (c *multiSyncCueAudioTrigger) handleOpen(ctx context.Context, mgr *audio.Manager, assetDir string, entry cueAudioTriggerEntry, filename string) {
	target := c.targetIdentity(entry)

	if identity, ok := mgr.LoadedMediaIdentity(cueActivationAudioSessionID); ok && identity == target {
		return
	}
	if identity, ok := mgr.LoadedMediaIdentity(pkgaudio.SessionID(cueactivation.PrepareStagingSessionID)); ok && identity == target {
		return
	}
	if err := c.applyAndPrepare(ctx, mgr, assetDir, entry); err != nil {
		c.logger.Warn("multisync: could not prepare a cue's audio on its sequence's OPEN packet",
			"cue_id", entry.CueID, "sequence_filename", filename, "error", err)
		return
	}
	c.markPreparedLate(filename)
}

// applyMedia applies entry's audio output directly onto the cue session,
// without preparing it: Apply never touches the engine, so this is the
// cheap half of "arm the already-staged handle": it gives the cue
// session a desired identity to compare a staged handle against, without
// paying for a second decode of media the staging session already
// prepared.
func (c *multiSyncCueAudioTrigger) applyMedia(ctx context.Context, mgr *audio.Manager, assetDir string, entry cueAudioTriggerEntry, now time.Time) (pkgaudio.OutcomeResult, error) {
	if entry.Audio.Filename == "" {
		return pkgaudio.OutcomeResult{}, fmt.Errorf("cue %q's audio asset %q has not been uploaded to this node", entry.CueID, entry.Audio.Asset)
	}
	contentHash := firstAssetHash(entry.Audio.AssetHashes)
	var sizeBytes int64
	if info, err := os.Stat(filepath.Join(assetDir, entry.Audio.Filename)); err == nil {
		sizeBytes = info.Size()
	}
	req := pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleShow),
		Media: pkgaudio.SetField(pkgaudio.MediaRef{
			AssetID:         entry.Audio.Asset,
			ContentHash:     contentHash,
			SizeBytes:       sizeBytes,
			RuntimeFilename: entry.Audio.Filename,
		}),
	}
	outcome := mgr.Apply(ctx, cueActivationAudioSessionID, c.invocation(entry.CueID, "apply", now), c.revision(now, cueactivation.AudioSessionStepApply), req)
	return outcome, nil
}

// applyAndPrepare is a cold prepare: apply entry's audio output onto the
// cue session and prepare it there, paying the full decode cost: the
// path a Cue that was never armed ahead of time takes, on OPEN or (if
// OPEN was never observed, RES-002's own "a robust listener must also
// accept START without a preceding OPEN") on START itself.
func (c *multiSyncCueAudioTrigger) applyAndPrepare(ctx context.Context, mgr *audio.Manager, assetDir string, entry cueAudioTriggerEntry) error {
	now := c.now()
	applyOutcome, err := c.applyMedia(ctx, mgr, assetDir, entry, now)
	if err != nil {
		return err
	}
	if audioOutcomeFailed(applyOutcome) {
		return fmt.Errorf("apply (%s): %s", applyOutcome.Outcome, applyOutcome.Reason)
	}
	prepOutcome := mgr.Prepare(ctx, cueActivationAudioSessionID, c.invocation(entry.CueID, "prepare", now), c.revision(now, cueactivation.AudioSessionStepPrepare))
	if audioOutcomeFailed(prepOutcome) {
		return fmt.Errorf("prepare (%s): %s", prepOutcome.Outcome, prepOutcome.Reason)
	}
	return nil
}

// handleStart is ADR-051 decision 1: T0 is arrival plus the configured
// lead, position is the Cue's own startOffsetMillis (FPP sends START at
// frame 0, and SYNC position is never chased, ADR-017), and the actual
// engine call is [Manager.StartAtPosition] (or, when the prepare-ahead
// staging session holds this exact media, [Manager.PromoteAtPosition],
// which reuses its already-loaded handle instead of paying for a second
// decode) so the first sample is presented at T0 on the SAME engine call
// as position, never a Start-then-Seek pair.
func (c *multiSyncCueAudioTrigger) handleStart(ctx context.Context, mgr *audio.Manager, assetDir string, registry *audioStartTriggerRegistry, entry cueAudioTriggerEntry, filename string, arrival agentclock.MediaTime) {
	// A resting or preshow bed must never keep playing once any show
	// sequence starts: cut it immediately, node-local, rather than wait
	// for the coordinator's own later pause command to arrive.
	mgr.CutBackgroundBed(ctx)

	target := c.targetIdentity(entry)

	cueIdentity, cueReady := mgr.LoadedMediaIdentity(cueActivationAudioSessionID)
	if cueReady && cueIdentity == target && c.sessionAlreadyPlaying(ctx, mgr) {
		// Already playing this exact media, from either trigger source: a
		// coordinator activation that got here first is left alone
		// (ADR-051 decision 6 / item 7), and a MultiSync START arriving
		// second must be left alone the same way: restarting would be an
		// audible glitch for no benefit.
		return
	}

	preparedLate := c.consumePreparedLate(filename)
	stagingIdentity, stagingReady := mgr.LoadedMediaIdentity(pkgaudio.SessionID(cueactivation.PrepareStagingSessionID))
	usePromote := (!cueReady || cueIdentity != target) && stagingReady && stagingIdentity == target

	switch {
	case cueReady && cueIdentity == target:
		// Already prepared directly on the cue session.
	case usePromote:
		if applyOutcome, err := c.applyMedia(ctx, mgr, assetDir, entry, c.now()); err != nil || audioOutcomeFailed(applyOutcome) {
			reason := errOrOutcome(err, applyOutcome)
			c.logger.Warn("multisync: could not apply a cue's staged audio for its sequence's START packet",
				"cue_id", entry.CueID, "sequence_filename", filename, "reason", reason)
			return
		}
	default:
		if err := c.applyAndPrepare(ctx, mgr, assetDir, entry); err != nil {
			c.logger.Warn("multisync: could not prepare a cue's audio for its sequence's START packet",
				"cue_id", entry.CueID, "sequence_filename", filename, "error", err)
			return
		}
		preparedLate = true
	}

	leadMs := mgr.SettingsSnapshot().MultisyncStartLeadMs
	var arrivalNs int64
	if arrival.Valid {
		arrivalNs = arrival.Time.UnixNano()
	}
	t0 := arrivalNs + int64(leadMs)*int64(time.Millisecond)

	// resolveScheduleLocked subtracts the engine's own calibrated output
	// latency from t0, so a lead smaller than that latency always refuses
	// as in-past. Bump t0 (and the lead actually applied) past it first.
	if outputLatencyNs := mgr.OutputLatencyUs() * int64(time.Microsecond); outputLatencyNs > 0 {
		if mediaNow := mgr.MediaNow(ctx); mediaNow.Valid {
			minHonorable := mediaNow.Time.UnixNano() + outputLatencyNs + int64(multisyncScheduleMargin)
			if minHonorable > t0 {
				t0 = minHonorable
				leadMs = int((t0 - arrivalNs) / int64(time.Millisecond))
			}
		}
	}
	position := time.Duration(entry.Audio.StartOffsetMillis) * time.Millisecond

	now := c.now()
	invocation := c.invocation(entry.CueID, "start", now)
	revision := c.revision(now, cueactivation.AudioSessionStepStart)

	var startOutcome pkgaudio.OutcomeResult
	if usePromote {
		startOutcome = mgr.PromoteAtPosition(ctx, pkgaudio.SessionID(cueactivation.PrepareStagingSessionID), cueActivationAudioSessionID, invocation, revision, t0, position)
	} else {
		startOutcome = mgr.StartAtPosition(ctx, cueActivationAudioSessionID, invocation, revision, t0, position)
	}
	if audioOutcomeFailed(startOutcome) {
		if startOutcome.Outcome == pkgaudio.OutcomeRefused && strings.HasPrefix(startOutcome.Reason, pkgaudio.ReasonScheduledStartInPast) {
			c.startLateOnArrival(ctx, mgr, registry, entry, filename, position, t0, arrivalNs, int64(leadMs), target, startOutcome.Reason)
			return
		}
		c.logger.Warn("multisync: cue audio did not start on its sequence's START packet",
			"cue_id", entry.CueID, "sequence_filename", filename, "outcome", startOutcome.Outcome, "reason", startOutcome.Reason)
		return
	}
	// A confirmed outcome still carries [pkgaudio.ReasonScheduledStartIgnored]
	// in its own Reason when this node's clock provider was not locked
	// (resolveScheduleLocked's own note): the node started on arrival
	// instead of at T0, exactly as [audio.Manager.StartAtPosition]'s doc
	// comment says a node with no usable clock provider does, "and says
	// so" (ADR-051 decision 1) here rather than in any report field
	// this ADR adds, since it is not this Cue's own evidence but this
	// node's clock evidence, already reported on node.audio.timeline.*.
	if strings.HasPrefix(startOutcome.Reason, pkgaudio.ReasonScheduledStartIgnored) {
		c.logger.Info("multisync: cue audio started on arrival instead of at the computed lead instant",
			"cue_id", entry.CueID, "sequence_filename", filename, "reason", startOutcome.Reason)
	}

	registry.set(cueActivationAudioSessionID, audioStartTriggerRecord{
		Trigger:          pkgaudio.StartTriggerMultiSync,
		CueID:            entry.CueID,
		MediaIdentity:    target,
		SequenceFilename: filename,
		ArrivalNs:        arrivalNs,
		LeadMs:           int64(leadMs),
		PreparedLate:     preparedLate,
	})
	c.signalAudioReport()
}

// startLateOnArrival mirrors cueactivationaudio.go's startUnalignedOnArrival:
// a T0 already past starts immediately at the Cue's own position, never a
// refusal. The cue session already carries this media, so Start reuses it.
func (c *multiSyncCueAudioTrigger) startLateOnArrival(ctx context.Context, mgr *audio.Manager, registry *audioStartTriggerRegistry, entry cueAudioTriggerEntry, filename string, position time.Duration, t0, arrivalNs, leadMs int64, target, missedReason string) {
	now := c.now()
	startOutcome := mgr.Start(ctx, cueActivationAudioSessionID, c.invocation(entry.CueID, "start-late", now), c.revision(now, unalignedFallbackStepStart))
	if audioOutcomeFailed(startOutcome) {
		c.logger.Warn("multisync: cue audio did not start on its sequence's START packet after a missed scheduled instant",
			"cue_id", entry.CueID, "sequence_filename", filename, "outcome", startOutcome.Outcome, "reason", startOutcome.Reason)
		return
	}
	seekOutcome := mgr.Seek(ctx, cueActivationAudioSessionID, c.invocation(entry.CueID, "seek-late", now), c.revision(now, unalignedFallbackStepSeek), position)
	if audioOutcomeFailed(seekOutcome) {
		c.logger.Warn("multisync: cue audio did not move to its cue position after a late start",
			"cue_id", entry.CueID, "sequence_filename", filename, "outcome", seekOutcome.Outcome, "reason", seekOutcome.Reason)
		return
	}

	var latenessMs int64
	if mediaNow := mgr.MediaNow(ctx); mediaNow.Valid {
		latenessMs = mediaNow.Time.Sub(time.Unix(0, t0)).Milliseconds()
	}
	c.logger.Info("multisync: cue audio started late instead of being refused",
		"cue_id", entry.CueID, "sequence_filename", filename, "lateness_ms", latenessMs, "reason", missedReason)

	registry.set(cueActivationAudioSessionID, audioStartTriggerRecord{
		Trigger: pkgaudio.StartTriggerMultiSync, CueID: entry.CueID, MediaIdentity: target,
		SequenceFilename: filename, ArrivalNs: arrivalNs, LeadMs: leadMs,
		PreparedLate: true, LatenessMs: latenessMs,
	})
	c.signalAudioReport()
}

// sessionAlreadyPlaying reports whether the cue session's own snapshot
// currently reads Playing.
func (c *multiSyncCueAudioTrigger) sessionAlreadyPlaying(ctx context.Context, mgr *audio.Manager) bool {
	for _, snap := range mgr.Snapshot(ctx) {
		if snap.ID == cueActivationAudioSessionID {
			return snap.State == pkgaudio.StatePlaying
		}
	}
	return false
}

// errOrOutcome renders whichever of err or outcome actually carries the
// failure, for a log line that has only one of the two shapes to report.
func errOrOutcome(err error, outcome pkgaudio.OutcomeResult) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("%s: %s", outcome.Outcome, outcome.Reason)
}

// stopBlankingGrace is the fallback grace [handleStop] waits before
// stopping a cue's audio when the shared render Timeline has never
// learned a real step time from an actual FSEQ file, RES-020 section
// 3.1's "one sequence frame, 50ms at 20fps" times the five-frame grace
// RES-002 documents FPP's own remotes waiting before blanking
// (Sequence.cpp's own ~5 frame delay), used here as a flat fallback
// because an audio-only node commonly has no FSEQ of its own to learn a
// real step time from at all.
const stopBlankingGrace = 250 * time.Millisecond

// stopBlankingGraceFrames is RES-002's own "~5 frames before blanking,"
// applied to audio the same way FPP's own remotes apply it to lighting so
// a back-to-back stop/start does not audibly blink.
const stopBlankingGraceFrames = 5

// multisyncScheduleMargin is added past the engine's own calibrated
// output-latency adjustment when bumping a start's own T0, so processing
// time between that computation and the engine's own check does not
// itself turn the bumped instant into a fresh in-past refusal.
//
// SHOWMESH HYPOTHESIS, NOT MEASURED.
const multisyncScheduleMargin = 20 * time.Millisecond

// handleStop is ADR-051 decision 1's own stop path: wait a blanking
// grace, then stop the cue's audio session, unless a later OPEN, START,
// or STOP this hook processed in the meantime has already superseded it
// (gen, this STOP's own generation stamp, no longer matches the current
// one). There is only ever one show audio session on this node, so any
// later event necessarily means stopping now would silence whatever is
// playing by the time the timer fires, not the sequence this STOP
// actually belongs to.
func (c *multiSyncCueAudioTrigger) handleStop(ctx context.Context, mgr *audio.Manager, timeline *multisync.Timeline, entry cueAudioTriggerEntry, filename string, gen uint64) {
	grace := stopBlankingGrace
	if timeline != nil {
		if stepTime, known := timeline.StepTime(); known {
			grace = stopBlankingGraceFrames * stepTime
		}
	}

	time.AfterFunc(grace, func() {
		if c.currentGeneration() != gen {
			return
		}
		now := c.now()
		outcome := mgr.Stop(ctx, cueActivationAudioSessionID, c.invocation(entry.CueID, "stop", now), c.revision(now, cueactivation.AudioSessionStepStop))
		if audioOutcomeFailed(outcome) && outcome.Outcome != pkgaudio.OutcomeRefused {
			c.logger.Warn("multisync: cue audio did not stop after its sequence's STOP packet",
				"cue_id", entry.CueID, "sequence_filename", filename, "outcome", outcome.Outcome, "reason", outcome.Reason)
		}
	})
}
