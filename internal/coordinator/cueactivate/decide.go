// Package cueactivate is Track H seam H4's coordinator-side decision core.
// [Decide] turns [fppreconcile.Result] — the trigger fppreconcile already
// resolves an accepted FPP observation into — plus the observation itself,
// into a [Decision]: a runner-neutral [cueactivation.Activation] per
// participating node when the observation resolves and every node passes
// [cueauth.Check], or H0.2's mismatch handling
// (TRACK-H-cues-and-playlists.md section H0.2) when it does not.
//
// This package has no HTTP, no MQTT, and dispatches nothing: [Decide] is
// pure of dispatch, the same "read-only resolution, never activation"
// posture fppreconcile itself holds one seam earlier. internal/coordinator/
// api's cueactivationdispatch.go and cueactivationloop.go are the only
// callers that turn a [Decision] into a wire command.
//
// [Decide] and [Authorize] are deliberately two separate calls, not one.
// TRACK-H-H3-SPEC.md section 6 requires the coordinator to refuse
// INDEPENDENTLY, "against what the coordinator resolves now" — and "now"
// is the moment right before a dispatch actually reaches the wire, which
// can be later than the moment [Decide] built the Activation (a queued
// tick, a retried publish). [Decide] pins Show/Generation/CatalogRevision
// at resolution time, exactly as TRACK-H-cues-and-playlists.md section H4
// requires ("come from assetsync.ResolveActiveShow and
// assetsync.ResolveCueCatalog"); [Authorize] re-resolves the identical
// two calls a second time, independently, and runs [cueauth.Check] against
// whatever THAT resolves — never against what [Decide] already computed.
// A caller that dispatches without calling [Authorize] first has skipped
// the one check this seam exists to add.
package cueactivate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppreconcile"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
	"github.com/showmeshsystems/showmesh/pkg/cueauth"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
)

// State is the closed vocabulary a [Decision] reaches. It is deliberately
// smaller than [fppreconcile.Outcome]: H0.2's own text collapses every
// contradicting observation (stale import, unknown entry, evidence
// mismatch, cross-show) into one operator-visible "mismatched" state that
// "carries the observed evidence" — the three policies differ only in
// their OUTPUT effect, never in what the operator is told.
type State string

const (
	// StateActivated: the observation resolved, every participating node
	// passed [cueauth.Check], and Decision.Activations carries one
	// [cueactivation.Activation] per node to dispatch cue.activate to.
	StateActivated State = "activated"

	// StateMismatched: fppreconcile located a binding (in any show) but
	// could not resolve it to an authorized activation, OR it resolved
	// but the active-show's own bound Playlist is a DIFFERENT one that
	// mismatchPolicy governs (H0.2). Decision.MismatchPolicy names which
	// of hold/blackAndSilence/safeCue applied; Decision.Activations and
	// Decision.ClearNodes carry the resulting effect, per policy.
	StateMismatched State = "mismatched"

	// StateUnbound: no fpp-runner show.playlist in the ACTIVE show names
	// this instance — there is nothing to hold and nothing to mismatch,
	// per H0.2's own closing rule. This can be reached either because
	// fppreconcile itself found no binding anywhere ([fppreconcile.
	// OutcomeUnbound]), or because it found one, but not in the active
	// show, and the active show has no fpp-runner Playlist of its own
	// bound to this instance either.
	StateUnbound State = "unbound"

	// StateIdentityUnavailable mirrors [fppreconcile.
	// OutcomeIdentityUnavailable] exactly: FPP could not establish
	// identity for this observation, so there is nothing to resolve,
	// mismatch, or hold.
	StateIdentityUnavailable State = "identity-unavailable"
)

// Decision is [Decide]'s result: the state it reached, human-readable
// evidence, and — for StateActivated, or a safeCue-effect StateMismatched
// — the per-node activations a caller must still run through [Authorize]
// before dispatching. Never both a non-empty Activations/ClearNodes AND a
// mismatched-hold state: only exactly one of "these activations, once
// authorized", "clear these nodes", or "dispatch nothing" is ever
// populated.
type Decision struct {
	State  State
	Reason string

	// MismatchPolicy is set only when State == StateMismatched: one of
	// config.ShowPlaylistMismatchPolicyHold/BlackAndSilence/SafeCue.
	MismatchPolicy string

	// Activations is nodeID -> the [cueactivation.Activation] to dispatch
	// as that node's own cue.activate command params. Populated for
	// StateActivated, and for a StateMismatched decision under the
	// safeCue policy.
	Activations map[string]cueactivation.Activation

	// ClearNodes lists the nodes a StateMismatched decision under the
	// blackAndSilence policy must be told to black/silence, using the
	// EXISTING render.surface.clear (and, where wired, audio-silence)
	// command paths rather than a cue.activate: blacking is not itself an
	// activation of any Cue.
	ClearNodes []string
}

// Decide resolves result (fppreconcile's own answer for obs) into a
// Decision, pinning Show/Generation/CatalogRevision at THIS moment for
// every Activation it builds. It never calls [cueauth.Check] itself — see
// this package's own doc comment for why that is [Authorize]'s job, run a
// second time, independently, right before a caller actually dispatches.
// runnerInstance is the FPP instance UUID [cueactivation.Activation.
// RunnerInstance] carries; it is obs.InstanceUUID for every caller today,
// threaded explicitly rather than read off obs a second time so a future
// non-FPP caller of the same decision shape is not forced through an
// FPP-shaped observation.
func Decide(ctx context.Context, st *store.Store, result fppreconcile.Result, obs store.FPPPlaylistEntryObservationRecord, runnerInstance string) (Decision, error) {
	switch result.Outcome {
	case fppreconcile.OutcomeIdentityUnavailable:
		return Decision{State: StateIdentityUnavailable, Reason: result.Reason}, nil
	case fppreconcile.OutcomeUnbound:
		return Decision{State: StateUnbound, Reason: result.Reason}, nil
	}

	// Every other outcome means fppreconcile located a binding somewhere
	// (not necessarily the active show — see OutcomeCrossShow). H0.2's own
	// policy lookup is independent of THAT binding: it reads the ACTIVE
	// show's own fpp-runner Playlist bound to this instance, which may be
	// a different show.playlist object entirely (the ordinary case an
	// operator configures mismatchPolicy for: this same physical FPP host
	// usually serves the active show, but is currently caught playing
	// something else).
	active, err := assetsync.ResolveActiveShow(ctx, st)
	if err != nil {
		return Decision{}, fmt.Errorf("cueactivate: resolve active show: %w", err)
	}
	binding, found, err := activeShowFPPBinding(ctx, st, active, obs.InstanceUUID)
	if err != nil {
		return Decision{}, fmt.Errorf("cueactivate: resolve active-show fpp binding: %w", err)
	}
	if !found {
		return Decision{
			State:  StateUnbound,
			Reason: "no fpp-runner show.playlist in the active show names this instance: there is nothing to hold",
		}, nil
	}

	if result.Outcome != fppreconcile.OutcomeResolved {
		return decideMismatch(ctx, st, active, binding, result, obs, runnerInstance)
	}

	return decideResolved(ctx, st, active, result, obs, runnerInstance)
}

// activeShowBinding is the ACTIVE show's own fpp-runner show.playlist bound
// to one FPP instance — H0.2's policy source, independent of whatever
// binding fppreconcile itself matched the observation against.
type activeShowBinding struct {
	playlistID       string
	playlistRevision int64
	mismatchPolicy   string
	safeCueRef       string
}

// activeShowFPPBinding searches active's own show.playlist objects (never
// another show's — H0.2: "the policy is resolved from the active Show's
// fpp-runner Playlist") for one whose runner is fpp and whose
// fpp.instanceUuid is instanceUUID. Independently re-implemented rather
// than reusing fppreconcile's unexported fppRunnerBindingsForInstance
// (which deliberately searches every show): this project's standing
// "each side validates independently" convention (renderSurfaceIDPattern's
// own doc comment) applies here too, and the two functions answer
// genuinely different questions.
func activeShowFPPBinding(ctx context.Context, st *store.Store, active assetsync.ActiveShow, instanceUUID string) (activeShowBinding, bool, error) {
	if !active.Configured {
		return activeShowBinding{}, false, nil
	}
	objs, err := st.ListConfigObjects(ctx, config.ShowPlaylistConfigKind)
	if err != nil {
		return activeShowBinding{}, false, fmt.Errorf("list show.playlist objects: %w", err)
	}
	type candidate struct {
		id  string
		bnd activeShowBinding
	}
	var candidates []candidate
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := st.GetConfigRevision(ctx, config.ShowPlaylistConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			if errors.Is(err, store.ErrConfigRevisionNotFound) {
				continue
			}
			return activeShowBinding{}, false, fmt.Errorf("read show.playlist %q revision %d: %w", obj.ID, obj.CurrentRevision, err)
		}
		var p struct {
			Show           string `json:"show"`
			Runner         string `json:"runner"`
			MismatchPolicy string `json:"mismatchPolicy"`
			SafeCueRef     string `json:"safeCueRef"`
			FPP            *struct {
				InstanceUUID string `json:"instanceUuid"`
			} `json:"fpp"`
		}
		if err := json.Unmarshal([]byte(rev.PayloadJSON), &p); err != nil {
			continue
		}
		if p.Show != active.ShowID || p.Runner != config.ShowPlaylistRunnerFPP || p.FPP == nil || p.FPP.InstanceUUID != instanceUUID {
			continue
		}
		policy := p.MismatchPolicy
		if policy == "" {
			policy = config.ShowPlaylistMismatchPolicyDefault
		}
		candidates = append(candidates, candidate{id: obj.ID, bnd: activeShowBinding{
			playlistID: obj.ID, playlistRevision: obj.CurrentRevision,
			mismatchPolicy: policy, safeCueRef: p.SafeCueRef,
		}})
	}
	if len(candidates) == 0 {
		return activeShowBinding{}, false, nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].id < candidates[j].id })
	return candidates[0].bnd, true, nil
}

// decideMismatch applies H0.2's policy for every non-resolved,
// active-show-bound outcome. The state is StateMismatched under every one
// of the three policies — only the effect differs.
func decideMismatch(ctx context.Context, st *store.Store, active assetsync.ActiveShow, binding activeShowBinding, result fppreconcile.Result, obs store.FPPPlaylistEntryObservationRecord, runnerInstance string) (Decision, error) {
	d := Decision{State: StateMismatched, Reason: result.Reason, MismatchPolicy: binding.mismatchPolicy}
	switch binding.mismatchPolicy {
	case config.ShowPlaylistMismatchPolicyHold:
		// No new Cue is activated; every output already authorized keeps
		// running unchanged. Nothing to dispatch.
		return d, nil

	case config.ShowPlaylistMismatchPolicyBlackAndSilence:
		nodes, err := participatingNodesForShow(ctx, st, active.ShowID)
		if err != nil {
			return Decision{}, err
		}
		d.ClearNodes = nodes
		return d, nil

	case config.ShowPlaylistMismatchPolicySafeCue:
		if binding.safeCueRef == "" {
			// Authoring already requires safeCueRef whenever the policy is
			// safeCue (config.decodeShowPlaylistMismatchPolicy); an absent
			// value here means a stored row this coordinator cannot trust
			// to activate anything from. Fail closed to hold's effect
			// (nothing dispatched) rather than guessing.
			d.Reason = result.Reason + "; mismatchPolicy is safeCue but the bound Playlist carries no safeCueRef, so nothing is activated"
			return d, nil
		}
		activations, err := resolveActivationsForCue(ctx, st, active, binding.playlistID, binding.playlistRevision, "", binding.safeCueRef, obs, runnerInstance)
		if err != nil {
			return Decision{}, err
		}
		d.Activations = activations
		return d, nil

	default:
		return Decision{}, fmt.Errorf("cueactivate: unknown mismatchPolicy %q on show.playlist %q", binding.mismatchPolicy, binding.playlistID)
	}
}

// decideResolved is [Decide]'s path for [fppreconcile.OutcomeResolved]:
// build one [cueactivation.Activation] per participating node from
// result's pinned identities. It does not itself authorize anything — see
// [Authorize].
func decideResolved(ctx context.Context, st *store.Store, active assetsync.ActiveShow, result fppreconcile.Result, obs store.FPPPlaylistEntryObservationRecord, runnerInstance string) (Decision, error) {
	activations, err := resolveActivationsForCue(ctx, st, active, result.PlaylistID, result.PlaylistRevision, result.EntryID, result.CueID, obs, runnerInstance)
	if err != nil {
		return Decision{}, err
	}
	// A resolved Cue with zero participating nodes (no surface, no
	// audio.node, anywhere in this Show) is a real but inert case, never
	// an error: Decision.Activations is simply empty and there is nothing
	// for a caller to dispatch or authorize.
	return Decision{State: StateActivated, Reason: result.Reason, Activations: activations}, nil
}

// resolveActivationsForCue builds one [cueactivation.Activation] per node
// participating in cueID — a node with at least one resolved output for
// it, per [assetsync.ResolveCueCatalog]'s own node-scoping rule. It pins
// Show/Generation/CatalogRevision from active and each node's own
// freshly-resolved catalog; it performs no authorization check of its own.
func resolveActivationsForCue(ctx context.Context, st *store.Store, active assetsync.ActiveShow, playlistID string, playlistRevision int64, entryID, cueID string, obs store.FPPPlaylistEntryObservationRecord, runnerInstance string) (map[string]cueactivation.Activation, error) {
	if !active.Configured {
		return map[string]cueactivation.Activation{}, nil
	}
	cueObj, err := st.GetConfigObject(ctx, config.ShowCueConfigKind, cueID)
	if err != nil {
		if errors.Is(err, store.ErrConfigObjectNotFound) {
			return map[string]cueactivation.Activation{}, nil
		}
		return nil, fmt.Errorf("get show.cue %q: %w", cueID, err)
	}
	cueRevision := cueObj.CurrentRevision

	nodes, err := st.ListNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	activations := make(map[string]cueactivation.Activation, len(nodes))
	for _, n := range nodes {
		catalog, err := assetsync.ResolveCueCatalog(ctx, st, active, n.NodeID)
		if err != nil {
			return nil, fmt.Errorf("resolve cue catalog for node %q: %w", n.NodeID, err)
		}
		entry, participates := catalogEntry(catalog, cueID)
		if !participates || !hasAnyOutput(entry) {
			continue
		}

		tuple := cueauth.AuthorizationTuple{
			Show: active.ShowID, Generation: active.Generation, CatalogRevision: catalog.Revision,
			CueID: cueID, CueRevision: cueRevision,
		}
		activations[n.NodeID] = cueactivation.Activation{
			Runner: "fpp", RunnerInstance: runnerInstance,
			ActivationID:     activationID(n.NodeID, tuple, playlistID, playlistRevision, entryID, obs.EntryOccurrenceSequence),
			Show:             active.ShowID,
			Generation:       active.Generation,
			CatalogRevision:  catalog.Revision,
			Playlist:         playlistID,
			PlaylistRevision: playlistRevision,
			EntryID:          entryID,
			CueID:            cueID,
			CueRevision:      cueRevision,
			PositionMS:       obs.Position,
			EvidenceAt:       obs.ObservedAt,
		}
	}
	return activations, nil
}

// Authorize is TRACK-H-H3-SPEC.md section 6's coordinator-side refusal
// check, run INDEPENDENTLY of [Decide]: it re-resolves the active show and
// nodeID's own Cue catalog FRESH, right now, and calls [cueauth.Check]
// against act.Tuple() — never against anything [Decide] computed earlier.
// A caller MUST call this once per node, immediately before dispatching
// that node's own Activation, and must not dispatch when ok is false: H3
// spec section 6's "a refusal is a state with evidence, never a silent
// no-op, and never a fallback to a different Cue, Playlist, or Show."
func Authorize(ctx context.Context, st *store.Store, now time.Time, inventoryInterval time.Duration, nodeID string, act cueactivation.Activation) (outcome cueauth.Outcome, ok bool, err error) {
	active, err := assetsync.ResolveActiveShow(ctx, st)
	if err != nil {
		return "", false, fmt.Errorf("cueactivate: authorize: resolve active show: %w", err)
	}
	if !active.Configured {
		return cueauth.OutcomeCrossShow, false, nil
	}
	catalog, err := assetsync.ResolveCueCatalog(ctx, st, active, nodeID)
	if err != nil {
		return "", false, fmt.Errorf("cueactivate: authorize: resolve cue catalog for node %q: %w", nodeID, err)
	}
	ready, err := nodeAssetsReady(ctx, st, now, inventoryInterval, nodeID)
	if err != nil {
		return "", false, fmt.Errorf("cueactivate: authorize: resolve asset readiness for node %q: %w", nodeID, err)
	}
	held := cueauth.HeldState{
		Show: active.ShowID, Generation: active.Generation, CatalogRevision: catalog.Revision,
		KnownCueRevisions: knownCueRevisions(catalog), AssetsPresent: ready,
	}
	outcome, ok = cueauth.Check(act.Tuple(), held)
	return outcome, ok, nil
}

// catalogEntry returns catalog's entry for cueID, if any.
func catalogEntry(catalog assetsync.Catalog, cueID string) (cuecatalog.Entry, bool) {
	for _, e := range catalog.Entries {
		if e.CueID == cueID {
			return e, true
		}
	}
	return cuecatalog.Entry{}, false
}

// hasAnyOutput reports whether entry declares at least one resolved output
// for the node its catalog was resolved for — a node with none of them
// (no surface, no audio.node) does not participate in this Cue at all,
// per [assetsync.ResolveCueCatalog]'s own node-scoping rule.
func hasAnyOutput(entry cuecatalog.Entry) bool {
	return entry.Outputs.Render != nil || entry.Outputs.Audio != nil || entry.Outputs.LTC != nil || entry.Outputs.Announcement != nil
}

// knownCueRevisions projects catalog's entries into the map [cueauth.
// HeldState.KnownCueRevisions] compares a tuple's CueID/CueRevision
// against.
func knownCueRevisions(catalog assetsync.Catalog) map[string]int64 {
	out := make(map[string]int64, len(catalog.Entries))
	for _, e := range catalog.Entries {
		out[e.CueID] = e.CueRevision
	}
	return out
}

// nodeAssetsReady reports whether nodeID's asset manifest is Ready, the
// [cueauth.HeldState.AssetsPresent] input — see [assetsync.
// ComputeNodeManifest]'s own three-state vocabulary; anything other than
// Ready (NotReady or Unknown) is treated as "not present", matching H3
// spec section 6's "a present file is never a reason to execute" posture
// the other direction: an UNKNOWN state must never read as present either.
func nodeAssetsReady(ctx context.Context, st *store.Store, now time.Time, inventoryInterval time.Duration, nodeID string) (bool, error) {
	m, err := assetsync.BuildNodeManifest(ctx, st, now, inventoryInterval, nodeID)
	if err != nil {
		return false, err
	}
	return m.State == assetsync.ManifestReady, nil
}

// activationID is a deterministic, stable id for one logical activation:
// identical inputs (same node, same authorization tuple, same binding, same
// entry OCCURRENCE) always produce the identical id, so a redelivered or
// repeated decision for an unchanged occurrence is recognizably the SAME
// activation rather than a new one — [cueactivation.Activation.
// ActivationID]'s own contract ("stable per activation, for idempotency").
//
// entrySequence is obs.EntryOccurrenceSequence, entry-START identity
// computed at ingestion (internal/coordinator/api/fppobservations.go,
// schemaV18's own doc comment) from the FPP-plugin-assigned action and
// entry key — not a coordinator clock, and deliberately not the raw wire
// `sequence` (that counter also advances on an ordinary MultiSync position
// tick within one ongoing entry, so hashing it directly would mint a new
// ActivationID on every tick instead of once per entry change). It is
// stable across repeat ticks inside one occurrence, so an unchanged stored
// row keeps deriving the identical ActivationID (a repeat tick must dedup),
// while a genuinely new entry-start — including a playlist looping back to
// an entry it already visited, whose entry key alone is otherwise identical
// to the first visit — always changes it, so a re-entry dispatches again.
// Without this, two occurrences of the same entry (same node/show/
// generation/catalog/playlist/entry/cue identity) hashed identically, and a
// looping playlist's second lap silently replayed the first lap's stored
// outcome forever.
func activationID(nodeID string, tuple cueauth.AuthorizationTuple, playlistID string, playlistRevision int64, entryID string, entrySequence int64) string {
	h := sha256.New()
	// hash.Hash.Write never returns an error (the interface's own
	// contract) — checked explicitly rather than ignored outright so a
	// lint pass can see that's a deliberate reading of the contract, not
	// an oversight.
	_, _ = fmt.Fprintf(h, "%s|%s|%d|%s|%s|%d|%s|%d|%s|%d",
		nodeID, tuple.Show, tuple.Generation, tuple.CatalogRevision,
		playlistID, playlistRevision, entryID, tuple.CueRevision, tuple.CueID, entrySequence)
	return "cueact-" + hex.EncodeToString(h.Sum(nil))
}

// participatingNodesForShow lists every node with at least one resolved
// output ANYWHERE in showID's Cue catalog — the blackAndSilence effect's
// target set, since a mismatched observation names no single authorized
// Cue to scope participation to.
func participatingNodesForShow(ctx context.Context, st *store.Store, showID string) ([]string, error) {
	active := assetsync.ActiveShow{Configured: true, ShowID: showID}
	// Generation is irrelevant to which nodes participate (only to
	// whether an activation is authorized), so it is left at its zero
	// value here deliberately.
	nodes, err := st.ListNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	var out []string
	for _, n := range nodes {
		catalog, err := assetsync.ResolveCueCatalog(ctx, st, active, n.NodeID)
		if err != nil {
			return nil, fmt.Errorf("resolve cue catalog for node %q: %w", n.NodeID, err)
		}
		for _, e := range catalog.Entries {
			if hasAnyOutput(e) {
				out = append(out, n.NodeID)
				break
			}
		}
	}
	sort.Strings(out)
	return out, nil
}
