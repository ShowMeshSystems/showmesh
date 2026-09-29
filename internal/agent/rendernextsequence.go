package agent

import (
	"path/filepath"

	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
	"github.com/showmeshsystems/showmesh/pkg/fseq"
)

// queuedRender is the next Cue's sequence, verified and opened while the
// current Cue plays, under the authorization of the activation that named
// it as next.
type queuedRender struct {
	seq             *pipeline.QueuedSequence
	file            *fseq.File
	cueID           string
	filename        string
	contentHash     string
	show            string
	generation      int64
	catalogRevision string
}

// matches reports whether q is exactly what act activates with out. Any
// difference means the queue is stale and must never be drawn as act.
func (q *queuedRender) matches(act cueactivation.Activation, out cuecatalog.RenderOutput) bool {
	return q.cueID == act.CueID &&
		q.show == act.Show &&
		q.generation == act.Generation &&
		q.catalogRevision == act.CatalogRevision &&
		q.filename == out.Filename &&
		q.contentHash != "" &&
		q.contentHash == firstAssetHash(out.AssetHashes)
}

// queueNextRender replaces every running surface's queued sequence with
// next, the render output of the Cue act names as coming next, or clears
// it when next is nil. Verifying and opening the file runs in the
// background so the activation that carried the hint never waits on it.
func (o *renderOperations) queueNextRender(act cueactivation.Activation, nextCueID string, next *cuecatalog.RenderOutput) {
	o.mu.Lock()
	surfaces := make(map[string]uint64, len(o.writers))
	for surfaceID := range o.writers {
		surfaces[surfaceID] = o.bumpQueueGenLocked(surfaceID)
	}
	o.mu.Unlock()

	for surfaceID, gen := range surfaces {
		o.discardQueued(surfaceID, gen)
		if next == nil || next.Filename == "" {
			continue
		}
		o.queueing.Add(1)
		go func(surfaceID string, gen uint64) {
			defer o.queueing.Done()
			o.prepareQueuedRender(surfaceID, gen, surfaces, act, nextCueID, *next)
		}(surfaceID, gen)
	}
}

// heldCatalogChanged records the catalog this node now holds and drops
// every queued sequence, so nothing verified under an older catalog draws.
func (o *renderOperations) heldCatalogChanged(show string, generation int64, revision string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.heldCatalog = &catalogIdentity{show: show, generation: generation, revision: revision}
	o.mu.Unlock()
	o.queueNextRender(cueactivation.Activation{}, "", nil)
}

// catalogIdentity names one held catalog.
type catalogIdentity struct {
	show       string
	generation int64
	revision   string
}

func (c *catalogIdentity) authorizes(act cueactivation.Activation) bool {
	return c == nil || (c.show == act.Show && c.generation == act.Generation && c.revision == act.CatalogRevision)
}

func (o *renderOperations) bumpQueueGenLocked(surfaceID string) uint64 {
	if o.queueGen == nil {
		o.queueGen = make(map[string]uint64)
	}
	o.queueGen[surfaceID]++
	return o.queueGen[surfaceID]
}

// prepareQueuedRender verifies next against its catalog hash, opens it,
// and hands it to surfaceID's writer, unless a later activation has
// already settled or replaced this surface's queue. Surfaces in receiving
// get the same sequence, so only surfaces outside it are compared for frame
// timing.
func (o *renderOperations) prepareQueuedRender(surfaceID string, gen uint64, receiving map[string]uint64, act cueactivation.Activation, nextCueID string, next cuecatalog.RenderOutput) {
	wantHash := firstAssetHash(next.AssetHashes)
	if wantHash == "" {
		return
	}
	path := filepath.Join(o.assetDir, next.Filename)
	gotHash, err := cachedHashFile(o.hashCache, path)
	if err != nil || gotHash != wantHash {
		o.logger.Warn("render: the next sequence could not be verified, so it will open when its Cue starts",
			"surface_id", surfaceID, "sequence", next.Filename, "error", err)
		return
	}
	f, err := fseq.Open(path)
	if err != nil {
		o.logger.Warn("render: the next sequence could not be opened, so it will open when its Cue starts",
			"surface_id", surfaceID, "sequence", next.Filename, "error", err)
		return
	}

	o.mu.Lock()
	h, ok := o.writers[surfaceID]
	if !ok || h.fseq == nil || o.queueGen[surfaceID] != gen || h.filename == next.Filename || !o.heldCatalog.authorizes(act) {
		o.mu.Unlock()
		_ = f.Close()
		return
	}
	skip := make(map[string]bool, len(receiving))
	for id := range receiving {
		skip[id] = true
	}
	if err := o.stepTimeConflictLocked(surfaceID, f.StepTimeMS(), skip, true); err != nil {
		o.mu.Unlock()
		_ = f.Close()
		o.logger.Warn("render: the next sequence runs at a different frame timing than another surface, so it will be refused when its Cue starts",
			"surface_id", surfaceID, "sequence", next.Filename, "error", err)
		return
	}
	q, displaced, err := h.fw.Queue(next.Filename, f)
	if err != nil {
		o.mu.Unlock()
		_ = f.Close()
		o.logger.Warn("render: the next sequence does not fit this surface, so it will open when its Cue starts",
			"surface_id", surfaceID, "sequence", next.Filename, "error", err)
		return
	}
	stale := o.releaseQueueLocked(h, displaced)
	h.queued = &queuedRender{
		seq: q, file: f, cueID: nextCueID, filename: next.Filename, contentHash: wantHash,
		show: act.Show, generation: act.Generation, catalogRevision: act.CatalogRevision,
	}
	o.mu.Unlock()
	closeFSEQ(stale)
	o.logger.Info("render: the next sequence is open and queued", "surface_id", surfaceID, "sequence", next.Filename, "cue_id", nextCueID)
}

// settleQueuedRender runs at the start of every render activation on
// surfaceID: it reports whether the writer already draws act's sequence,
// switched to ahead of time from a queue matching act exactly, and
// discards whatever else is queued. stale is true when the writer switched
// ahead to something act does not match, so the caller must swap.
func (o *renderOperations) settleQueuedRender(surfaceID string, act cueactivation.Activation, out cuecatalog.RenderOutput) (ahead, stale bool) {
	o.mu.Lock()
	gen := o.bumpQueueGenLocked(surfaceID)
	h, ok := o.writers[surfaceID]
	var took *queuedRender
	if ok {
		took, h.ahead = h.ahead, nil
	}
	o.mu.Unlock()
	o.discardQueued(surfaceID, gen)
	if took == nil {
		return false, false
	}
	if took.matches(act, out) {
		return true, false
	}
	return false, true
}

// discardQueued drops surfaceID's queued sequence unless a newer queue
// generation has taken over since gen.
func (o *renderOperations) discardQueued(surfaceID string, gen uint64) {
	o.mu.Lock()
	h, ok := o.writers[surfaceID]
	if !ok || o.queueGen[surfaceID] != gen {
		o.mu.Unlock()
		return
	}
	stale := o.releaseQueueLocked(h, h.fw.TakeQueued())
	o.mu.Unlock()
	closeFSEQ(stale)
}

// releaseQueueLocked forgets h's queued sequence when taken is it, and
// returns the file the caller must now close. Caller holds o.mu.
func (o *renderOperations) releaseQueueLocked(h *frameWriterHandle, taken *pipeline.QueuedSequence) *fseq.File {
	if taken == nil || h.queued == nil || h.queued.seq != taken {
		return nil
	}
	f := h.queued.file
	h.queued = nil
	return f
}

// closeTakenQueue closes the file behind a queued sequence taken from a
// writer that has already stopped.
func (o *renderOperations) closeTakenQueue(h *frameWriterHandle, taken *pipeline.QueuedSequence) {
	o.mu.Lock()
	stale := o.releaseQueueLocked(h, taken)
	o.mu.Unlock()
	closeFSEQ(stale)
}

// queuedSequenceStarted runs on the writer's goroutine once it has switched
// to q: the queued file becomes the one the surface draws, and the one it
// replaced is closed.
func (o *renderOperations) queuedSequenceStarted(surfaceID string, h *frameWriterHandle, q *pipeline.QueuedSequence) {
	o.mu.Lock()
	if h.queued == nil || h.queued.seq != q {
		o.mu.Unlock()
		return
	}
	previous := h.fseq
	h.fseq = h.queued.file
	h.filename = h.queued.filename
	h.ahead = h.queued
	h.queued = nil
	stepTimeMS := h.fseq.StepTimeMS()
	o.mu.Unlock()
	closeFSEQ(previous)
	o.applyTimelineStepTime(stepTimeMS)
}

// allowQueuedSwitch runs on the writer's goroutine just before it switches
// to q. It refuses the switch when the queued sequence's step time differs
// from another surface's, counting a peer's own queued sequence as the one it
// is about to play, so the shared timeline never moves under another surface.
func (o *renderOperations) allowQueuedSwitch(surfaceID string, h *frameWriterHandle, q *pipeline.QueuedSequence) bool {
	o.mu.Lock()
	if h.queued == nil || h.queued.seq != q {
		o.mu.Unlock()
		return true
	}
	err := o.stepTimeConflictLocked(surfaceID, h.queued.file.StepTimeMS(), nil, true)
	o.mu.Unlock()
	if err != nil {
		o.logger.Warn("render: the next sequence was not started because it runs at a different frame timing than another surface",
			"surface_id", surfaceID, "sequence", q.Filename, "error", err)
		return false
	}
	return true
}

// queuedSequenceDropped runs on the writer's goroutine when a held-black
// tick drops q.
func (o *renderOperations) queuedSequenceDropped(h *frameWriterHandle, q *pipeline.QueuedSequence) {
	o.closeTakenQueue(h, q)
}

// drawnAhead reports the sequence surfaceID's writer switched to ahead of
// its activation, if any.
func (o *renderOperations) drawnAhead(surfaceID string) (queuedRender, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	h, ok := o.writers[surfaceID]
	if !ok || h.ahead == nil {
		return queuedRender{}, false
	}
	return *h.ahead, true
}

func closeFSEQ(f *fseq.File) {
	if f != nil {
		_ = f.Close()
	}
}
