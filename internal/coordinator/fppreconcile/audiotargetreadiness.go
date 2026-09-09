package fppreconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// audioTargetReadiness evaluates ADR-045's two authoring-time audio rules
// against the state the store is actually in, which authoring alone cannot
// guarantee: an audio.node deleted after a Cue named it, or a second
// program+ltc node declared while the first was absent, both leave a Show
// that authored cleanly and now routes nowhere.
//
// It reports the FIRST failure in a deterministic order (nodes sorted, then
// playlist entries in their own order, then audio/ltc/announcement), so the
// same broken installation always names the same cause rather than
// whichever object the store happened to return first.
//
// An undecodable stored Cue is skipped rather than failed: [cueReady] has
// already run for every entry by the time this condition is evaluated, so a
// Cue that cannot be decoded here has already been reported as
// cue-not-ready and reporting it a second time under an audio condition
// would name the wrong cause.
func audioTargetReadiness(ctx context.Context, st *store.Store, logger *slog.Logger, p config.ShowPlaylistPayload) (ReadinessCondition, string, error) {
	declared, ltcEmitters, err := audioNodeRoles(ctx, st)
	if err != nil {
		return "", "", err
	}
	if len(ltcEmitters) > 1 {
		return ReadinessAudioLTCEmitterAmbiguous, fmt.Sprintf(
			"audio.node %q and %q both hold role %q; exactly one node may be the installation's LTC emitter (ADR-018's one clock domain, ADR-045)",
			ltcEmitters[0], ltcEmitters[1], config.AudioNodeRoleProgramLTC), nil
	}

	// The node an output naming no target resolves to: the sole
	// program+ltc node, or, when there is none, the sole audio.node of any
	// role. Kept identical to assetsync's own resolution rule; the two
	// disagreeing would mean readiness passes a Show whose catalog resolves
	// to nothing, which is the exact failure this condition exists to stop.
	defaultTarget := ""
	switch {
	case len(ltcEmitters) == 1:
		defaultTarget = ltcEmitters[0]
	case len(declared) == 1:
		defaultTarget = declared[0]
	}

	for _, entry := range p.Entries {
		payload, ok, err := decodeCueForAudioTargets(ctx, st, logger, entry.Cue)
		if err != nil {
			return "", "", err
		}
		if !ok || payload.Show != p.Show {
			continue
		}
		for _, out := range []struct {
			name   string
			target string
			set    bool
		}{
			{"outputs.audio", targetOf(payload.Outputs.Audio), payload.Outputs.Audio != nil},
			{"outputs.ltc", targetOfLTC(payload.Outputs.LTC), payload.Outputs.LTC != nil},
			{"outputs.announcement", targetOfAnnouncement(payload.Outputs.Announcement), payload.Outputs.Announcement != nil},
		} {
			if !out.set {
				continue
			}
			if out.target == "" {
				// An installation with NO audio.node at all is left
				// exactly as it was: a Show declaring audio outputs on a
				// fleet with no audio node has always been reported ready,
				// and turning that into a failure is a different decision
				// than ADR-045's routing rules. This condition fires only
				// where routing is genuinely ambiguous: several audio
				// nodes, none of them the LTC emitter.
				if len(declared) > 1 && defaultTarget == "" {
					return ReadinessAudioTargetUnresolved, fmt.Sprintf(
						"cue %q's %s names no target and this installation has no node for it to resolve to: %d audio.node objects exist and none holds role %q",
						entry.Cue, out.name, len(declared), config.AudioNodeRoleProgramLTC), nil
				}
				continue
			}
			if !containsID(declared, out.target) {
				return ReadinessAudioTargetUnbound, fmt.Sprintf(
					"cue %q's %s targets node %q, which holds no audio.node object, so that output would reach nobody",
					entry.Cue, out.name, out.target), nil
			}
		}
	}
	return "", "", nil
}

func targetOf(o *config.ShowCueAudioOutput) string {
	if o == nil {
		return ""
	}
	return o.Target
}

func targetOfLTC(o *config.ShowCueLTCOutput) string {
	if o == nil {
		return ""
	}
	return o.Target
}

func targetOfAnnouncement(o *config.ShowCueAnnouncementOutput) string {
	if o == nil {
		return ""
	}
	return o.Target
}

func containsID(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// audioNodeRoles returns every declared audio.node object id and the subset
// of them holding role program+ltc, both sorted, so a reported pair is a
// stable fact about the installation rather than store iteration order.
func audioNodeRoles(ctx context.Context, st *store.Store) (declared, ltcEmitters []string, err error) {
	objs, err := st.ListConfigObjects(ctx, config.AudioNodeConfigKind)
	if err != nil {
		return nil, nil, fmt.Errorf("fppreconcile: list audio.node objects: %w", err)
	}
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := st.GetConfigRevision(ctx, config.AudioNodeConfigKind, obj.ID, obj.CurrentRevision)
		if errors.Is(err, store.ErrConfigRevisionNotFound) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("fppreconcile: read audio.node %q revision %d: %w", obj.ID, obj.CurrentRevision, err)
		}
		payload, verr := config.DecodeAudioNodePayload(rev.PayloadJSON)
		if verr != nil {
			return nil, nil, fmt.Errorf("fppreconcile: decode audio.node %q: %s", obj.ID, verr.Detail)
		}
		declared = append(declared, obj.ID)
		if payload.Role == config.AudioNodeRoleProgramLTC {
			ltcEmitters = append(ltcEmitters, obj.ID)
		}
	}
	sort.Strings(declared)
	sort.Strings(ltcEmitters)
	return declared, ltcEmitters, nil
}

// decodeCueForAudioTargets reads one Cue's current revision. ok is false
// for a Cue that does not exist, has never been activated, or cannot be
// decoded; every one of those has already been reported by [cueReady].
func decodeCueForAudioTargets(ctx context.Context, st *store.Store, logger *slog.Logger, cueID string) (config.ShowCuePayload, bool, error) {
	obj, err := st.GetConfigObject(ctx, config.ShowCueConfigKind, cueID)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		return config.ShowCuePayload{}, false, nil
	}
	if err != nil {
		return config.ShowCuePayload{}, false, fmt.Errorf("fppreconcile: get cue %q: %w", cueID, err)
	}
	if obj.CurrentRevision == 0 {
		return config.ShowCuePayload{}, false, nil
	}
	rev, err := st.GetConfigRevision(ctx, config.ShowCueConfigKind, cueID, obj.CurrentRevision)
	if errors.Is(err, store.ErrConfigRevisionNotFound) {
		return config.ShowCuePayload{}, false, nil
	}
	if err != nil {
		return config.ShowCuePayload{}, false, fmt.Errorf("fppreconcile: get cue %q revision %d: %w", cueID, obj.CurrentRevision, err)
	}
	var payload config.ShowCuePayload
	if err := json.Unmarshal([]byte(rev.PayloadJSON), &payload); err != nil {
		if logger != nil {
			logger.Warn("fppreconcile: stored cue revision could not be decoded; audio target readiness skipped it", "cueId", cueID, "error", err)
		}
		return config.ShowCuePayload{}, false, nil
	}
	return payload, true, nil
}
