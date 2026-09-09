package api

import (
	"context"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// Instance participation is the show's own hand-made selection of which
// FPP hosts and which Resolume instances take part, read back for the
// rendering and readiness paths. It is the instance-side counterpart of
// nodeshowparticipation.go, and follows that file's shape: resolved ONCE
// per render pass by [resolveShowInstanceParticipation], then asked per
// instance, because the active show and its selection do not vary between
// two instances of the same pass.
//
// The one state this side has that the node side does not is
// "selection_unrecorded" - see [v1.InstanceShowParticipation]'s own doc
// comment for why collapsing it into "not_participating" would make that
// value permanently unusable as evidence.

// Participation state names on the wire. participationStateParticipating,
// ...NotParticipating, ...NotConfigured and ...Unknown intentionally match
// v1.NodeShowParticipation's own strings.
const (
	participationStateParticipating    = "participating"
	participationStateNotParticipating = "not_participating"
	participationStateSelectionUnknown = "selection_unrecorded"
	participationStateNotConfigured    = "not_configured"
	participationStateUnknown          = "unknown"
)

// participationSelectionUnrecordedReason is the exact sentence an operator
// reads for a show that is active but has never had a selection recorded.
// It states the remedy AND the consequence, because the consequence (every
// configured instance still counts) is what stops an upgrade from turning
// readiness green by silence.
const participationSelectionUnrecordedReason = "the active show has no instance participation selection recorded; " +
	"every configured instance is treated as taking part until one is recorded"

// showInstanceParticipation is one render pass's resolved answer, shared
// by every instance rendered in that pass.
type showInstanceParticipation struct {
	show string

	// notConfiguredReason and unknownReason are each non-empty for at most
	// one of the two whole-pass outcomes: no show is active, or the
	// selection could not be read at all. When either is set, every
	// instance in the pass answers with it.
	notConfiguredReason string
	unknownReason       string

	fpp         map[string]bool
	fppSelected bool

	resolume         map[string]bool
	resolumeSelected bool
}

// resolveShowInstanceParticipation reads the active show's participation
// selection once. active/activeErr come from
// [resolveActiveShowForParticipation], so a caller that already renders
// node participation resolves the active show exactly once for both.
func resolveShowInstanceParticipation(ctx context.Context, cfg ConfigStore, assetManifests *store.Store, active assetsync.ActiveShow, activeErr error) showInstanceParticipation {
	// A nil store is not an inactive show. Without this the zero-valued
	// ActiveShow that [resolveActiveShowForParticipation] returns for a nil
	// store reads as "no show is currently active", which is a confident
	// false answer sending a reader to the wrong remedy.
	if assetManifests == nil {
		return showInstanceParticipation{notConfiguredReason: "no asset manifest store is configured on this coordinator"}
	}
	if activeErr != nil {
		return showInstanceParticipation{unknownReason: "could not resolve the active show: " + activeErr.Error()}
	}
	if !active.Configured {
		return showInstanceParticipation{notConfiguredReason: "no show is currently active"}
	}
	p := showInstanceParticipation{show: active.ShowID}
	payload, err := readShowPayload(ctx, cfg, active.ShowID)
	if err != nil {
		p.unknownReason = "could not read the active show's participation selection: " + err.Error()
		return p
	}
	if ids, selected := payload.FPPParticipation(); selected {
		p.fppSelected, p.fpp = true, idSet(ids)
	}
	if ids, selected := payload.ResolumeParticipation(); selected {
		p.resolumeSelected, p.resolume = true, idSet(ids)
	}
	return p
}

// readShowPayload reads and decodes one show object's current revision.
func readShowPayload(ctx context.Context, cfg ConfigStore, showID string) (config.ShowPayload, error) {
	obj, err := cfg.GetConfigObject(ctx, config.ShowConfigKind, showID)
	if err != nil {
		return config.ShowPayload{}, err
	}
	rev, err := cfg.GetConfigRevision(ctx, config.ShowConfigKind, showID, obj.CurrentRevision)
	if err != nil {
		return config.ShowPayload{}, err
	}
	var payload config.ShowPayload
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
		return config.ShowPayload{}, err
	}
	return payload, nil
}

func idSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

// forFPP answers one FPP instance's participation.
func (p showInstanceParticipation) forFPP(instanceID string) v1.InstanceShowParticipation {
	return p.answer(p.fpp, p.fppSelected, instanceID)
}

// forResolume answers one Resolume instance's participation.
func (p showInstanceParticipation) forResolume(instanceID string) v1.InstanceShowParticipation {
	return p.answer(p.resolume, p.resolumeSelected, instanceID)
}

func (p showInstanceParticipation) answer(ids map[string]bool, selected bool, instanceID string) v1.InstanceShowParticipation {
	switch {
	case p.unknownReason != "":
		return v1.InstanceShowParticipation{State: participationStateUnknown, Show: p.show, Reason: strPtr(p.unknownReason)}
	case p.notConfiguredReason != "":
		return v1.InstanceShowParticipation{State: participationStateNotConfigured, Reason: strPtr(p.notConfiguredReason)}
	case !selected:
		return v1.InstanceShowParticipation{State: participationStateSelectionUnknown, Show: p.show,
			Reason: strPtr(participationSelectionUnrecordedReason)}
	case ids[instanceID]:
		return v1.InstanceShowParticipation{State: participationStateParticipating, Show: p.show}
	default:
		return v1.InstanceShowParticipation{State: participationStateNotParticipating, Show: p.show}
	}
}

// participationCounts reports whether an instance in this state counts as
// part of tonight for a readiness or health purpose. An
// unrecorded selection counts EVERY configured instance, which is the
// loud reading: the quiet one would turn every check green the moment
// this feature shipped, on a question nobody has answered.
func participationCounts(state v1.InstanceShowParticipation) bool {
	return state.State == participationStateParticipating || state.State == participationStateSelectionUnknown
}

// resolveInstanceParticipation is the one-call form for a render pass that
// does not already need the active show for anything else (the FPP and
// Resolume list and single-instance routes). A pass that renders node
// participation too resolves the active show once and calls
// [resolveShowInstanceParticipation] directly with it.
func (h *handlers) resolveInstanceParticipation(ctx context.Context) showInstanceParticipation {
	active, activeErr := resolveActiveShowForParticipation(ctx, h.deps.AssetManifests)
	return resolveShowInstanceParticipation(ctx, h.deps.Config, h.deps.AssetManifests, active, activeErr)
}

// instanceParticipation is [handlers.resolveInstanceParticipation] for the
// stream hub, which holds the same Dependencies but is not a *handlers.
func (h *Hub) instanceParticipation(ctx context.Context) showInstanceParticipation {
	active, activeErr := resolveActiveShowForParticipation(ctx, h.deps.AssetManifests)
	return resolveShowInstanceParticipation(ctx, h.deps.Config, h.deps.AssetManifests, active, activeErr)
}

// fppSelectedIDs and resolumeSelectedIDs return the ids a selection
// actually named, and whether a selection was recorded at all. A readiness
// path needs the raw ids (an id naming no configured instance still has to
// be reported), where a rendering path only ever asks about an instance it
// already holds.
func (p showInstanceParticipation) fppSelectedIDs() (ids []string, selected bool) {
	return setIDs(p.fpp), p.fppSelected
}

func (p showInstanceParticipation) resolumeSelectedIDs() (ids []string, selected bool) {
	return setIDs(p.resolume), p.resolumeSelected
}

func setIDs(set map[string]bool) []string {
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	return ids
}
