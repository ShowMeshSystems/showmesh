package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	"github.com/showmeshsystems/showmesh/internal/agent/heldcatalog"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
	"github.com/showmeshsystems/showmesh/pkg/cueauth"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
)

// verifiedHashCache caches hashFile's own answer per path, keyed also by
// size and modification time: a hit only when both are unchanged since the
// cached hash was computed, shared by assetPresent below and by
// assetFetchOperation's own post-write verification (assets.go), so a
// large asset already verified once is never re-hashed whole on every
// cue.activate.
type verifiedHashCache struct {
	mu      sync.Mutex
	entries map[string]hashCacheEntry
}

func newVerifiedHashCache() *verifiedHashCache {
	return &verifiedHashCache{entries: make(map[string]hashCacheEntry)}
}

func (c *verifiedHashCache) get(path string, size int64, modTime time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[path]
	if !ok || e.size != size || !e.modTime.Equal(modTime) {
		return "", false
	}
	return e.hash, true
}

func (c *verifiedHashCache) set(path string, size int64, modTime time.Time, hash string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[path] = hashCacheEntry{size: size, modTime: modTime, hash: hash}
}

// This file is TRACK-H-cues-and-playlists.md section H4's node-agent
// entry point: the "cue.activate" OperationFunc. See this seam's
// H4-BRIEF.md for the shared [cueactivation.Activation] envelope and its
// four closed rulings; cueactivationrender.go and cueactivationaudio.go
// carry the render and audio (plus, transitively, LTC) sides of actually
// applying an authorized activation.

// cueActivationOperation is the OperationFunc receiver for "cue.activate":
// decode the envelope, load this node's held Cue catalog
// (internal/agent/heldcatalog) and build a [cueauth.HeldState] from it,
// authorize with [cueauth.CheckLazy] (H3 spec section 6's seven refusal
// reasons — disk is consulted only once the other six checks have already
// passed), and, only on authorization, apply the Cue's resolved outputs.
// render and audioMgr are nil-safe (a node with no render surfaces, or no
// audio engine, configured never wires their respective outputs — matching
// newOperationRegistry's identical nil-disables convention elsewhere).
type cueActivationOperation struct {
	assetDir     string
	catalogStore *heldcatalog.FileStore
	render       *renderOperations
	audioMgr     *audio.Manager
	nodeID       string
	hashCache    *verifiedHashCache

	// announcementMu guards announcementActivationID/announcementCueID:
	// TRACK-H-cues-and-playlists.md section H5 build item 3's own state,
	// tracking the identity of whichever announcement Cue this node most
	// recently accepted into [cueactivation.AnnouncementSessionID] — see
	// concurrentAnnouncementRefusalReason's own doc comment for why this is
	// needed at all (pkg/audio's own session state carries no Cue
	// identity to read back).
	announcementMu           sync.Mutex
	announcementActivationID string
	announcementCueID        string
}

// heldStateAndEntry loads this node's held catalog (if any) and projects
// it into the [cueauth.HeldState] shape [cueauth.CheckLazy] checks
// against, plus the specific [cuecatalog.Entry] for cueID, if the held
// catalog carries one.
//
// A node holding no catalog at all is represented as the ZERO VALUE
// HeldState, not a special case: [cueauth.Check]'s own doc comment states
// that a tuple differing from the zero value in Show (every real
// activation does — Show is required by [cueactivation.Activation.
// Validate]) is refused by the Show check before anything about the
// (absent) catalog is even consulted. That is exactly the refusal a node
// that has never been deployed a catalog must produce.
func (o *cueActivationOperation) heldStateAndEntry(cueID string) (held cueauth.HeldState, entry cuecatalog.Entry, entryFound bool, err error) {
	rec, ok, loadErr := o.catalogStore.Load()
	if loadErr != nil {
		return cueauth.HeldState{}, cuecatalog.Entry{}, false, fmt.Errorf("could not load the held cue catalog: %w", loadErr)
	}
	if !ok {
		return cueauth.HeldState{}, cuecatalog.Entry{}, false, nil
	}
	held = cueauth.HeldState{
		Show:              rec.Show,
		Generation:        rec.Generation,
		CatalogRevision:   rec.Revision,
		KnownCueRevisions: rec.KnownCueRevisions(),
	}
	for _, e := range rec.Entries {
		if e.CueID == cueID {
			return held, e, true, nil
		}
	}
	return held, cuecatalog.Entry{}, false, nil
}

// assetPresent reports whether the file named filename is present under
// this node's asset directory and, when hashes is non-empty, that its
// content hash matches [firstAssetHash](hashes) — the SAME single hash
// cueactivationrender.go's activateSurfaceRender pins into
// params["fseqContentHash"] and buildAssignedSpec then checks exactly
// against at apply time (renderops.go). This authorization check and that
// apply-time check must be one rule, not two independently written ones:
// this function used to accept a match against ANY of hashes, so with two
// CURRENT asset hashes for one sequence (a legitimate mid-supersession
// state) and the on-disk file carrying the second hash, authorization
// passed here and the apply then failed buildAssignedSpec's exact
// first-hash comparison — a confusing two-stage refusal (authorized, then
// a hash-mismatch apply failure) instead of a clean, single asset-missing
// refusal at authorization time, where a caller actually expects to see
// it (TRACK-H-H3-SPEC.md section 6: "a present file is never a reason to
// execute" applies here to "a present-but-WRONG-hash file" too — the node
// must refuse it up front, not discover it mid-swap).
//
// An empty filename is never "present": an output the resolved catalog
// declares with no asset name at all is a coordinator-side bug, not a
// node-side "nothing to check."
func (o *cueActivationOperation) assetPresent(filename string, hashes []string) bool {
	if filename == "" {
		return false
	}
	path := filepath.Join(o.assetDir, filename)
	got, err := o.hashFileCached(path)
	if err != nil {
		return false
	}
	expected := firstAssetHash(hashes)
	if expected == "" {
		// No declared hash at all: presence alone is enough (matches the
		// apply-time side's own "no recorded hash is a coordinator-side
		// bug, not a node-side trust-it" posture — firstAssetHash.go's own
		// doc comment).
		return true
	}
	return got == expected
}

// hashFileCached answers path's content hash from o.hashCache when its
// size and modification time still match a prior verification, hashing
// (and caching the result) only on a miss so a missing or altered file
// still hashes, and still fails, exactly as before this cache existed.
func (o *cueActivationOperation) hashFileCached(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if o.hashCache != nil {
		if hash, ok := o.hashCache.get(path, info.Size(), info.ModTime()); ok {
			return hash, nil
		}
	}
	hash, err := hashFile(path)
	if err != nil {
		return "", err
	}
	if o.hashCache != nil {
		o.hashCache.set(path, info.Size(), info.ModTime(), hash)
	}
	return hash, nil
}

// assetsPresent is [cueauth.CheckLazy]'s assetsPresent callback for entry:
// every asset entry's declared outputs (render, audio) need must be
// present and hash-verified locally. LTC and Announcement carry no asset
// of their own (see [cuecatalog.LTCOutput] and [cuecatalog.
// AnnouncementOutput]'s own doc comments), so neither is checked here.
func (o *cueActivationOperation) assetsPresent(entry cuecatalog.Entry) bool {
	if r := entry.Outputs.Render; r != nil {
		// Filename, never Sequence: Sequence is a logical identity, not a
		// name this node's asset directory is ever keyed by (ADR-043
		// decision 6) — see [cuecatalog.RenderOutput]'s own doc comment.
		if !o.assetPresent(r.Filename, r.AssetHashes) {
			return false
		}
	}
	if a := entry.Outputs.Audio; a != nil {
		if !o.assetPresent(a.Filename, a.AssetHashes) {
			return false
		}
	}
	return true
}

// refusalOutcomeValue is the "stated result carrying the outcome and the
// evidence, never a silent no-op, never a fallback to another Cue" TRACK-
// H-H3-SPEC.md section 6 requires: Confirmed:false (matching
// audiosessionops.go's sessionOp's identical "Refused/Failed/
// Unconfirmable all report Confirmed:false" convention) with the refusal
// outcome and reason in Value, never a bare Go error. An error return here
// would report OutcomeFailed, which this codebase reserves for an
// unexpected/structural failure — not an ordinary authorization refusal
// evaluated exactly as [cueauth.Check] is specified to.
func refusalOutcomeValue(act cueactivation.Activation, outcome cueauth.Outcome) map[string]any {
	return map[string]any{
		"activationId": act.ActivationID,
		"runner":       act.Runner,
		"cueId":        act.CueID,
		"cueRevision":  act.CueRevision,
		"outcome":      string(outcome),
	}
}

// activate is the OperationFunc for "cue.activate":
//
//  1. Decode the envelope ([cueactivation.DecodeParams]) and validate it.
//  2. Load this node's held catalog and build a [cueauth.HeldState].
//  3. Authorize with [cueauth.CheckLazy].
//  4. Only on authorization, apply the Cue's resolved outputs from the
//     held catalog entry (render, audio, and — transitively, through the
//     audio session's own Start/Seek path — LTC).
func (o *cueActivationOperation) activate(ctx context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
	act, err := cueactivation.DecodeParams(params)
	if err != nil {
		return OperationResult{}, fmt.Errorf("this activation could not be read: %w", err)
	}
	if err := act.Validate(); err != nil {
		return OperationResult{}, fmt.Errorf("this activation is invalid: %w", err)
	}

	executedAt := now()

	held, entry, entryFound, err := o.heldStateAndEntry(act.CueID)
	if err != nil {
		return OperationResult{}, err
	}

	assetsPresent := func() bool {
		if !entryFound {
			return false
		}
		return o.assetsPresent(entry)
	}
	outcome, ok := cueauth.CheckLazy(act.Tuple(), held, assetsPresent)
	if !ok {
		return OperationResult{
			Confirmed:  false,
			Signal:     "node.cue_activation.outcome",
			Value:      refusalOutcomeValue(act, outcome),
			ExecutedAt: executedAt,
			ObservedAt: now(),
		}, nil
	}

	var applyErrs []string
	var unalignedReason string
	var startTrigger *audioStartTriggerRecord
	if entry.Outputs.Render != nil {
		if o.render == nil {
			applyErrs = append(applyErrs, "This Cue needs a render surface, but none are configured on this node.")
		} else if err := o.render.activateRender(act, *entry.Outputs.Render, now); err != nil {
			applyErrs = append(applyErrs, err.Error())
		}
	}
	if entry.Outputs.Audio != nil {
		concurrentAnnouncementReason := ""
		if entry.Outputs.Announcement != nil {
			concurrentAnnouncementReason = o.concurrentAnnouncementRefusalReason(ctx, act)
		}
		if o.audioMgr == nil {
			applyErrs = append(applyErrs, "This Cue needs an audio engine, but none is configured on this node.")
		} else if concurrentAnnouncementReason != "" {
			// TRACK-H-cues-and-playlists.md section H5 build item 3's own
			// ruling: a second announcement arriving while one is Playing
			// is REFUSED, never superseded — activateAudio is not even
			// called, so its own Apply/Prepare never tears down the
			// in-flight announcement mid-sentence.
			applyErrs = append(applyErrs, concurrentAnnouncementReason)
		} else if reason, trigger, err := activateAudio(ctx, o.audioMgr, o.assetDir, act, *entry.Outputs.Audio, entry.Outputs.LTC, entry.Outputs.Announcement, now); err != nil {
			applyErrs = append(applyErrs, err.Error())
		} else {
			unalignedReason = reason
			startTrigger = trigger
			if entry.Outputs.Announcement != nil {
				o.recordAnnouncementActivation(act)
			}
		}
	} else if entry.Outputs.LTC != nil {
		// H0.3/H4: LTC is emitted from the program-audio clock domain via
		// the show audio session's own Start/Seek path (activateAudio) —
		// there is no session to attach an LTC offset to without an audio
		// output declared on the same Cue. Stated, never silently skipped.
		applyErrs = append(applyErrs, "This Cue has a timecode output but no audio output. Timecode needs an audio output on the same Cue.")
	}

	observedAt := now()
	if len(applyErrs) > 0 {
		return OperationResult{
			Confirmed: false,
			Signal:    "node.cue_activation.outcome",
			Value: map[string]any{
				"activationId": act.ActivationID, "cueId": act.CueID, "cueRevision": act.CueRevision,
				"outcome": "apply-failed", "reasons": applyErrs,
			},
			ExecutedAt: executedAt, ObservedAt: observedAt,
		}, nil
	}

	value := map[string]any{
		"activationId": act.ActivationID, "runner": act.Runner, "cueId": act.CueID,
		"cueRevision": act.CueRevision, "outcome": "authorized",
	}
	if unalignedReason != "" {
		value["unalignedReason"] = unalignedReason
	}
	// ADR-051 decision 6: written only when this activation found its
	// Cue's audio session already started by MultiSync (activateAudio's
	// own item 7 restart guard), so the coordinator that dispatched this
	// activation knows it did not restart or reseek anything, and why.
	if startTrigger != nil {
		value[pkgaudio.ResultStartTrigger] = startTrigger.Trigger
		value[pkgaudio.ResultTriggerSequenceFilename] = startTrigger.SequenceFilename
		value[pkgaudio.ResultTriggerArrivalNs] = startTrigger.ArrivalNs
		value[pkgaudio.ResultStartLeadMs] = startTrigger.LeadMs
		value[pkgaudio.ResultPreparedLate] = startTrigger.PreparedLate
	}
	return OperationResult{
		Confirmed:  true,
		Signal:     "node.cue_activation.outcome",
		Value:      value,
		ExecutedAt: executedAt, ObservedAt: observedAt,
	}, nil
}

// concurrentAnnouncementRefusalReason is TRACK-H-cues-and-playlists.md
// section H5 build item 3's own fix: internal/agent/cueactivationaudio.go's
// activateAudio routes every announcement Cue to the SAME well-known
// session ([cueactivation.AnnouncementSessionID]), so a second
// announcement's own Apply used to call the engine's releaseEngineLocked
// and tear down whatever was already playing there mid-sentence — reported
// Confirmed, never refused. H0.5 says a second announcement is refused,
// never silently preempted, and the refusal names both Cues and the exact
// claim string.
//
// Returns "" when there is nothing to refuse: no audio engine, the
// announcement session is not currently Playing, or the arriving act is a
// REDELIVERY of the SAME activation already accepted (act.ActivationID
// matches the one recorded — pkg/audio's own RevisionState already
// resolves that as an idempotent replay one layer down, at
// activateAudio's own Apply call, so it must not be refused here first).
// pkg/audio's own session state carries no Cue identity to read back
// (SessionSnapshot has none), which is why this node-agent-level state is
// needed at all, rather than deriving the answer purely from
// [audio.Manager.Snapshot].
func (o *cueActivationOperation) concurrentAnnouncementRefusalReason(ctx context.Context, act cueactivation.Activation) string {
	if o.audioMgr == nil {
		return ""
	}
	id := pkgaudio.SessionID(cueactivation.AnnouncementSessionID)
	var playing bool
	for _, snap := range o.audioMgr.Snapshot(ctx) {
		if snap.ID == id && snap.State == pkgaudio.StatePlaying {
			playing = true
			break
		}
	}
	if !playing {
		return ""
	}

	o.announcementMu.Lock()
	priorActivationID, priorCueID := o.announcementActivationID, o.announcementCueID
	o.announcementMu.Unlock()

	if priorActivationID == act.ActivationID {
		// A redelivery of the SAME activation this node already accepted
		// — not a second announcement.
		return ""
	}
	if priorCueID == "" {
		// The announcement session is Playing but this node never
		// recorded which Cue put it there (e.g. a restart lost the
		// in-memory record). Still refuse — H0.5's exclusivity does not
		// depend on this node being able to name the prior Cue — but say
		// so honestly rather than fabricating an id.
		priorCueID = "unknown (this node did not record it)"
	}
	return fmt.Sprintf(
		"Cue %q was refused because Cue %q is already playing as the announcement. Wait for it to finish.",
		act.CueID, priorCueID)
}

// recordAnnouncementActivation records act as the last accepted
// announcement activation, so a later concurrent arrival can name it in a
// refusal and a redelivery of act itself is not refused as "concurrent
// with itself".
func (o *cueActivationOperation) recordAnnouncementActivation(act cueactivation.Activation) {
	o.announcementMu.Lock()
	o.announcementActivationID, o.announcementCueID = act.ActivationID, act.CueID
	o.announcementMu.Unlock()
}
