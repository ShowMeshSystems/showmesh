// Package fallbackcompile builds ADR-048's coordinator-signed fallback
// program for one FPP host, from the active show's already-resolved
// state: [assetsync.ResolveActiveShow], [assetsync.ResolveCueCatalog],
// the stored show.playlist/show.cue configuration, the stored FPP
// playlist definitions (FPP-PLUGIN-COORDINATOR-CONTRACTS.md section 3),
// and each participating node's cue-catalog acknowledgement
// (TRACK-H-H3-SPEC.md section 4). It reads every one of those through
// its existing exported functions only. This package derives no Cue
// authorization, entry identity, or asset expectation a second way.
//
// Compile either produces a signed [fallbackprogram.SignedProgram] or
// refuses with one of [Outcome]'s six named reasons
// (TRACK-J-fpp-fallback.md J1: "The compiler must refuse an ambiguous
// entry key, cross-show reference, missing node catalog acknowledgement,
// unresolvable target, unsupported output, or unsigned result."). A
// refusal is always [Result.Outcome] and [Result.Reason], a visible,
// reported condition. This package never returns a program that is
// silently smaller than what the inputs actually authorize.
package fallbackcompile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/coordsig"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
	"github.com/showmeshsystems/showmesh/pkg/fallbackprogram"
	"github.com/showmeshsystems/showmesh/pkg/fppidentity"
)

// ProgramTTL bounds how long a compiled program is valid before it must
// be reconciled again, TRACK-J-fpp-fallback.md J1's own "reconciles the
// program periodically" requirement needs an expiry to reconcile
// against. This is a ShowMesh hypothesis (CONTRIBUTING.md's evidence
// ladder), not a measured value: nothing about a real FPP outage has
// been observed yet, and RES-009 is where that measurement belongs.
// Exported so a reconciler can size its own retry cadence relative to it.
const ProgramTTL = 15 * time.Minute

// Outcome is the closed vocabulary TRACK-J-fpp-fallback.md J1 fixes for
// [Compile]'s result, on [pkg/cueauth.Outcome] and
// [internal/coordinator/fppreconcile.Outcome]'s identical "state with
// evidence, never a silent no-op" precedent.
type Outcome string

const (
	// OutcomePublished: compilation succeeded and the result is signed.
	OutcomePublished Outcome = "published"

	// OutcomeAmbiguousEntryKey: two different authored show.playlist
	// entries (in this host's participating playlists) derive the
	// identical deterministic entry key while naming different Cues ,
	// either because two entries claim the same (section, position), or
	// because two playlists on this host independently derive a
	// colliding key. The program cannot state one answer for a key that
	// authoring disagrees about.
	OutcomeAmbiguousEntryKey Outcome = "ambiguous-entry-key"

	// OutcomeCrossShowReference: an entry's Cue does not belong to the
	// active show. Authoring-time validation already refuses this
	// (config.DecodeShowPlaylistPayload's cross-show check), so this is
	// a defensive, second check against whatever is actually persisted,
	// never trusted to be unreachable.
	OutcomeCrossShowReference Outcome = "cross-show-reference"

	// OutcomeMissingCatalogAcknowledgement: a node this program would
	// name as a target has not acknowledged the exact Cue-catalog
	// revision the coordinator resolves for it right now
	// (TRACK-H-H3-SPEC.md section 4). Publishing a fallback activation a
	// node has not proven it can authorize offline would grant fallback
	// authority the node's own normal-path authorization never granted.
	OutcomeMissingCatalogAcknowledgement Outcome = "missing-node-catalog-acknowledgement"

	// OutcomeUnresolvableTarget: a Cue's resolved output for a node names
	// an asset with no resolvable runtime filename (nothing uploaded for
	// that logical sequence yet). There is nothing for the fallback
	// activation to point at.
	OutcomeUnresolvableTarget Outcome = "unresolvable-target"

	// OutcomeUnsupportedOutput: a Cue reachable from this host's
	// playlists declares an announcement output for a node target. An
	// announcement's duck/mix/interrupt arbitration against whatever else
	// is playing is not something an offline, pre-authorized activation
	// can decide, so this program never includes one.
	OutcomeUnsupportedOutput Outcome = "unsupported-output"

	// OutcomeUnsigned: every other check passed, but the coordinator's
	// own signing step failed (a corrupt or unreadable signing key). The
	// compiler never publishes an unsigned result.
	OutcomeUnsigned Outcome = "unsigned-result"
)

// Signer is the coordinator signing authority [Compile] uses to sign a
// compiled program, [internal/coordinator/signingkey.Manager]'s own
// Sign method satisfies it. Compile takes this as an interface, not a
// concrete *signingkey.Manager, so this package (and its tests) never
// import internal/coordinator/signingkey's key-generation and on-disk
// persistence machinery merely to sign a payload.
type Signer interface {
	Sign(payload []byte) (coordsig.Signature, error)
}

// Result is [Compile]'s return value: the outcome it reached, the FPP
// host it compiled for, a human-readable reason, and, only when Outcome
// is [OutcomePublished], the signed program itself.
type Result struct {
	FPPInstanceUUID string
	Outcome         Outcome
	Reason          string
	Program         *fallbackprogram.SignedProgram
}

func refuse(instanceUUID string, outcome Outcome, format string, args ...any) Result {
	return Result{FPPInstanceUUID: instanceUUID, Outcome: outcome, Reason: fmt.Sprintf(format, args...)}
}

// ParticipatingFPPHosts returns the distinct FPP instance UUIDs that the
// active show's fpp-runner show.playlist objects name, one fallback
// program is owed to each (ADR-048 decision 1: "For every active
// FPP-backed show, the coordinator builds a fallback program for each
// participating FPP host."). It is empty, with no error, when no show is
// active or the active show has no fpp-runner playlist.
func ParticipatingFPPHosts(ctx context.Context, st *store.Store) ([]string, error) {
	active, err := assetsync.ResolveActiveShow(ctx, st)
	if err != nil {
		return nil, fmt.Errorf("fallbackcompile: resolve active show: %w", err)
	}
	if !active.Configured {
		return nil, nil
	}
	playlists, err := fppRunnerPlaylistsForShow(ctx, st, active.ShowID)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var out []string
	for _, p := range playlists {
		id := p.payload.FPP.InstanceUUID
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Compile builds and signs the fallback program for fppInstanceUUID, or
// refuses per [Outcome]'s vocabulary. now is the compile time; it is
// injected rather than read from the clock so a caller (and every test)
// controls ExpiresAt/CompiledAt deterministically.
func Compile(ctx context.Context, st *store.Store, signer Signer, fppInstanceUUID string, now time.Time) (Result, error) {
	active, err := assetsync.ResolveActiveShow(ctx, st)
	if err != nil {
		return Result{}, fmt.Errorf("fallbackcompile: resolve active show: %w", err)
	}
	if !active.Configured {
		return Result{}, fmt.Errorf("fallbackcompile: no active show is configured; compile must not be called for an unconfigured active show")
	}

	allPlaylists, err := fppRunnerPlaylistsForShow(ctx, st, active.ShowID)
	if err != nil {
		return Result{}, err
	}
	var playlists []fppRunnerPlaylist
	for _, p := range allPlaylists {
		if p.payload.FPP.InstanceUUID == fppInstanceUUID {
			playlists = append(playlists, p)
		}
	}
	if len(playlists) == 0 {
		return Result{}, fmt.Errorf("fallbackcompile: no fpp-runner show.playlist in the active show names FPP instance %q", fppInstanceUUID)
	}
	sort.Slice(playlists, func(i, j int) bool { return playlists[i].objectID < playlists[j].objectID })

	nodes, err := st.ListNodeDeclarations(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("fallbackcompile: list node declarations: %w", err)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].NodeID < nodes[j].NodeID })

	nodeCatalogs := make(map[string]assetsync.Catalog, len(nodes))
	for _, n := range nodes {
		catalog, err := assetsync.ResolveCueCatalog(ctx, st, active, n.NodeID)
		if err != nil {
			return Result{}, fmt.Errorf("fallbackcompile: resolve cue catalog for node %q: %w", n.NodeID, err)
		}
		nodeCatalogs[n.NodeID] = catalog
	}

	playlistRevisions := make(map[string]int64, len(playlists))
	entriesByKey := make(map[string]string) // entryKey -> cueID, for cross-playlist ambiguity detection
	catalogRevisions := make(map[string]string)
	var entries []fallbackprogram.EntryMapping

	for _, p := range playlists {
		playlistRevisions[p.objectID] = p.revision

		authoredByPos, refusal := authoredEntriesByPosition(fppInstanceUUID, p)
		if refusal != nil {
			return *refusal, nil
		}
		if len(authoredByPos) == 0 {
			continue
		}

		def, err := st.GetFPPPlaylistDefinition(ctx, fppInstanceUUID, p.payload.FPP.PlaylistHash)
		if errors.Is(err, store.ErrFPPPlaylistDefinitionNotFound) {
			// No stored definition for this playlist revision yet: this
			// playlist contributes no entries, exactly as
			// FPP-PLUGIN-COORDINATOR-CONTRACTS.md section 3's own framing
			// ("not fatal to matching ... fatal to readiness") treats it
			// elsewhere in this codebase. It is not one of this
			// compiler's six named refusals.
			continue
		}
		if err != nil {
			return Result{}, fmt.Errorf("fallbackcompile: get fpp playlist definition for %q/%q: %w", fppInstanceUUID, p.payload.FPP.PlaylistHash, err)
		}

		defEntries, err := fppidentity.ParseDefinitionEntries(def.DefinitionJSON)
		if err != nil {
			return Result{}, fmt.Errorf("fallbackcompile: parse stored playlist definition for %q/%q: %w", fppInstanceUUID, p.payload.FPP.PlaylistHash, err)
		}

		for _, de := range defEntries {
			authored, ok := authoredByPos[sectionPosition{de.Section, de.Position}]
			if !ok {
				continue // this definition entry is not bound to any Cue
			}

			entryKey, err := fppidentity.DeriveEntryKey(fppidentity.EntryIdentity{
				InstanceUUID: fppInstanceUUID,
				PlaylistName: p.payload.FPP.PlaylistName,
				PlaylistHash: p.payload.FPP.PlaylistHash,
				Section:      de.Section,
				Position:     de.Position,
			})
			if err != nil {
				return Result{}, fmt.Errorf("fallbackcompile: derive entry key: %w", err)
			}
			if existingCue, seen := entriesByKey[entryKey]; seen && existingCue != authored.Cue {
				return refuse(fppInstanceUUID, OutcomeAmbiguousEntryKey,
					"entry key %q maps to both cue %q and cue %q across this host's playlists", entryKey, existingCue, authored.Cue), nil
			}
			entriesByKey[entryKey] = authored.Cue

			cuePayload, cueRevision, err := loadShowCuePayload(ctx, st, authored.Cue)
			if err != nil {
				return Result{}, fmt.Errorf("fallbackcompile: load cue %q: %w", authored.Cue, err)
			}
			if cuePayload.Show != active.ShowID {
				return refuse(fppInstanceUUID, OutcomeCrossShowReference,
					"entry key %q resolves to cue %q, which belongs to show %q, not the active show %q",
					entryKey, authored.Cue, cuePayload.Show, active.ShowID), nil
			}

			var targets []fallbackprogram.NodeTarget
			for _, n := range nodes {
				catalog := nodeCatalogs[n.NodeID]
				outputs, found := cueOutputsForNode(catalog, authored.Cue)
				if !found {
					continue
				}
				if outputs.Announcement != nil {
					return refuse(fppInstanceUUID, OutcomeUnsupportedOutput,
						"cue %q declares an announcement output for node %q, which a fallback program cannot pre-authorize", authored.Cue, n.NodeID), nil
				}
				if outputs.Render == nil && outputs.Audio == nil {
					continue
				}

				target := fallbackprogram.NodeTarget{NodeID: n.NodeID}
				if outputs.Render != nil {
					if outputs.Render.Filename == "" {
						return refuse(fppInstanceUUID, OutcomeUnresolvableTarget,
							"cue %q's render output for node %q has no resolvable asset (sequence %q)", authored.Cue, n.NodeID, outputs.Render.Sequence), nil
					}
					target.Render = &fallbackprogram.RenderActivation{
						Sequence: outputs.Render.Sequence, Filename: outputs.Render.Filename,
						AssetHashes: append([]string(nil), outputs.Render.AssetHashes...),
					}
				}
				if outputs.Audio != nil {
					if outputs.Audio.Filename == "" {
						return refuse(fppInstanceUUID, OutcomeUnresolvableTarget,
							"cue %q's audio output for node %q has no resolvable asset (asset %q)", authored.Cue, n.NodeID, outputs.Audio.Asset), nil
					}
					audio := &fallbackprogram.AudioActivation{
						Asset: outputs.Audio.Asset, Filename: outputs.Audio.Filename,
						StartOffsetMillis: outputs.Audio.StartOffsetMillis,
						AssetHashes:       append([]string(nil), outputs.Audio.AssetHashes...),
					}
					if outputs.LTC != nil {
						offset := outputs.LTC.StartOffsetMillis
						audio.LTCStartOffsetMillis = &offset
					}
					target.Audio = audio
				}

				ack, err := st.GetNodeCueCatalogAck(ctx, n.NodeID)
				if errors.Is(err, store.ErrNodeCueCatalogAckNotFound) {
					return refuse(fppInstanceUUID, OutcomeMissingCatalogAcknowledgement,
						"node %q has never acknowledged a cue catalog", n.NodeID), nil
				}
				if err != nil {
					return Result{}, fmt.Errorf("fallbackcompile: get node cue-catalog ack for %q: %w", n.NodeID, err)
				}
				if ack.Revision != catalog.Revision {
					return refuse(fppInstanceUUID, OutcomeMissingCatalogAcknowledgement,
						"node %q acknowledged catalog revision %q, but the coordinator currently resolves %q", n.NodeID, ack.Revision, catalog.Revision), nil
				}

				catalogRevisions[n.NodeID] = catalog.Revision
				targets = append(targets, target)
			}

			if len(targets) == 0 {
				continue // this Cue resolves to nothing on any declared node
			}
			sort.Slice(targets, func(i, j int) bool { return targets[i].NodeID < targets[j].NodeID })

			entries = append(entries, fallbackprogram.EntryMapping{
				EntryKey: entryKey, CueID: authored.Cue, CueRevision: cueRevision, Targets: targets,
			})
		}
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].EntryKey < entries[j].EntryKey })

	revision, err := fallbackprogram.ComputeRevision(fallbackprogram.RevisionInput{
		FPPInstanceUUID: fppInstanceUUID, Show: active.ShowID, Generation: active.Generation,
		PlaylistRevisions: playlistRevisions, CatalogRevisions: catalogRevisions, Entries: entries,
	})
	if err != nil {
		return Result{}, fmt.Errorf("fallbackcompile: compute revision: %w", err)
	}

	compiledAt := now
	program := fallbackprogram.Program{
		SchemaVersion: fallbackprogram.SchemaVersion, PackageID: uuid.NewString(), Revision: revision,
		ExpiresAt: compiledAt.Add(ProgramTTL), CompiledAt: compiledAt,
		FPPInstanceUUID: fppInstanceUUID, Show: active.ShowID, Generation: active.Generation,
		PlaylistRevisions: playlistRevisions, CatalogRevisions: catalogRevisions,
		Entries: entries, Rules: fallbackprogram.FixedRules,
	}

	payload, err := program.CanonicalBytes()
	if err != nil {
		return Result{}, fmt.Errorf("fallbackcompile: build canonical bytes: %w", err)
	}
	sig, signErr := signer.Sign(payload)
	if signErr != nil {
		return refuse(fppInstanceUUID, OutcomeUnsigned, "signing the compiled program failed: %s", signErr.Error()), nil
	}

	return Result{
		FPPInstanceUUID: fppInstanceUUID, Outcome: OutcomePublished,
		Program: &fallbackprogram.SignedProgram{Program: program, Signature: sig},
	}, nil
}

// --- internal helpers ---

// sectionPosition is one (section, position) slot, the identity an
// authored show.playlist entry's fpp binding and a parsed definition
// entry are matched by.
type sectionPosition struct {
	Section  string
	Position int
}

// fppRunnerPlaylist is one decoded, active-show, fpp-runner show.playlist
// object, with its own object id and current revision, the same shape
// internal/coordinator/fppreconcile's candidateBinding carries, kept
// local to this package for the identical reason that one is:
// fppreconcile must not be imported merely to reuse a struct shape.
type fppRunnerPlaylist struct {
	objectID string
	revision int64
	payload  config.ShowPlaylistPayload
}

// fppRunnerPlaylistsForShow lists every fpp-runner show.playlist object
// in showID, decoded directly from its persisted, already-validated
// revision, [internal/coordinator/assetsync]'s own
// decodeStoredShowPlaylistPayload precedent, repeated here rather than
// imported because that function is unexported.
func fppRunnerPlaylistsForShow(ctx context.Context, st *store.Store, showID string) ([]fppRunnerPlaylist, error) {
	objs, err := st.ListConfigObjects(ctx, config.ShowPlaylistConfigKind)
	if err != nil {
		return nil, fmt.Errorf("fallbackcompile: list show.playlist objects: %w", err)
	}
	var out []fppRunnerPlaylist
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := st.GetConfigRevision(ctx, config.ShowPlaylistConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			if errors.Is(err, store.ErrConfigRevisionNotFound) {
				continue
			}
			return nil, fmt.Errorf("fallbackcompile: read show.playlist %q revision %d: %w", obj.ID, obj.CurrentRevision, err)
		}
		var payload config.ShowPlaylistPayload
		if err := json.Unmarshal([]byte(rev.PayloadJSON), &payload); err != nil {
			continue // a broken payload on an unrelated object must not fail every other playlist's compile
		}
		if payload.Show != showID {
			continue
		}
		if payload.Runner != config.ShowPlaylistRunnerFPP || payload.FPP == nil {
			continue
		}
		out = append(out, fppRunnerPlaylist{objectID: obj.ID, revision: obj.CurrentRevision, payload: payload})
	}
	return out, nil
}

// authoredEntriesByPosition indexes p's own FPP-bound entries by
// (section, position), refusing with [OutcomeAmbiguousEntryKey] when two
// entries of the SAME playlist claim the same slot for different Cues ,
// they would derive the identical entry key by construction (the key is
// a pure function of instance, playlist identity, section, and
// position), so the program could not state one answer for it.
func authoredEntriesByPosition(fppInstanceUUID string, p fppRunnerPlaylist) (map[sectionPosition]config.ShowPlaylistEntry, *Result) {
	out := make(map[sectionPosition]config.ShowPlaylistEntry, len(p.payload.Entries))
	for _, e := range p.payload.Entries {
		if e.FPP == nil {
			continue
		}
		pos := sectionPosition{e.FPP.Section, e.FPP.Position}
		if existing, ok := out[pos]; ok && existing.Cue != e.Cue {
			r := refuse(fppInstanceUUID, OutcomeAmbiguousEntryKey,
				"playlist %q entries %q and %q both claim section %q position %d, naming different cues %q and %q",
				p.objectID, existing.ID, e.ID, pos.Section, pos.Position, existing.Cue, e.Cue)
			return nil, &r
		}
		out[pos] = e
	}
	return out, nil
}

// loadShowCuePayload reads cueID's current show.cue revision directly,
// the identical "already-persisted, already-validated" trust
// [internal/coordinator/assetsync.ResolveCueCatalog] extends its own
// show.cue reads, never re-run through write-time cross-reference
// validation, which would reject a payload that was valid when written.
func loadShowCuePayload(ctx context.Context, st *store.Store, cueID string) (config.ShowCuePayload, int64, error) {
	obj, err := st.GetConfigObject(ctx, config.ShowCueConfigKind, cueID)
	if err != nil {
		return config.ShowCuePayload{}, 0, fmt.Errorf("get show.cue %q: %w", cueID, err)
	}
	rev, err := st.GetConfigRevision(ctx, config.ShowCueConfigKind, cueID, obj.CurrentRevision)
	if err != nil {
		return config.ShowCuePayload{}, 0, fmt.Errorf("read show.cue %q revision %d: %w", cueID, obj.CurrentRevision, err)
	}
	var payload config.ShowCuePayload
	if err := json.Unmarshal([]byte(rev.PayloadJSON), &payload); err != nil {
		return config.ShowCuePayload{}, 0, fmt.Errorf("decode show.cue %q: %w", cueID, err)
	}
	return payload, obj.CurrentRevision, nil
}

// cueOutputsForNode returns cueID's resolved [cuecatalog.Outputs] out of
// catalog, if catalog carries an entry for it, mirroring
// internal/coordinator/assetsync/cuecatalog_test.go's own
// cueOutputsByID helper, reimplemented here because that one is
// unexported test-only code in another package.
func cueOutputsForNode(catalog assetsync.Catalog, cueID string) (cuecatalog.Outputs, bool) {
	for _, e := range catalog.Entries {
		if e.CueID == cueID {
			return e.Outputs, true
		}
	}
	return cuecatalog.Outputs{}, false
}
