package api

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppreconcile"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
)

// Night readiness checks that every audio output reaches a node set up to
// play it. Each one fails where the show would otherwise run silent while
// every node reports success.

const (
	nightShowCueAudioTargetsPrefix      = "show:cue-audio-targets:"
	nightShowCueStartTriggersPrefix     = "show:cue-start-triggers:"
	nightBackgroundAudioNodeSetupPrefix = "resting:background-audio-node-setup:"
	nightBackgroundAudioItemTargetsName = "resting:background-audio-item-targets"
)

// nightCheckShowCueAudioTargets runs the playlist readiness audio-target
// rule for each of show's playlists: a cue whose audio resolves to a node
// with no audio settings is never compiled into any node's cue list.
func (h *handlers) nightCheckShowCueAudioTargets(ctx context.Context, show string) []nightReadinessCheck {
	if h.deps.AssetManifests == nil {
		return nil
	}
	playlists, err := readCurrentPlaylists(ctx, h.deps.Config)
	if err != nil {
		return []nightReadinessCheck{{name: nightShowCueAudioTargetsPrefix + show, health: nightHealthUnknown(), cause: err,
			reason: "The coordinator could not read the show playlists. Run readiness again."}}
	}
	var checks []nightReadinessCheck
	for _, p := range playlists {
		if p.Payload.Show != show {
			continue
		}
		name := nightShowCueAudioTargetsPrefix + p.ID
		cond, reason, warnings, err := fppreconcile.AudioTargetReadiness(ctx, h.deps.AssetManifests, h.logger, p.Payload)
		switch {
		case err != nil:
			checks = append(checks, nightReadinessCheck{name: name, health: nightHealthUnknown(), cause: err, reason: fmt.Sprintf(
				"The coordinator could not check where the cues in playlist %s send audio. Run readiness again.", p.ID)})
		case cond != "":
			checks = append(checks, nightReadinessCheck{name: name, health: nightHealthFailed(), reason: reason})
		case len(warnings) > 0:
			checks = append(checks, nightReadinessCheck{name: name, health: nightHealthDegraded(), reasons: warnings})
		default:
			checks = append(checks, nightReadinessCheck{name: name, health: nightHealthHealthy()})
		}
	}
	return checks
}

// nightCheckShowCueStartTriggers warns for each of show's playlists when a
// cue's audio has no FPP sequence to start on, so it starts late from the
// coordinator instead of with the lights.
func (h *handlers) nightCheckShowCueStartTriggers(ctx context.Context, show string) []nightReadinessCheck {
	if h.deps.AssetManifests == nil {
		return nil
	}
	playlists, err := readCurrentPlaylists(ctx, h.deps.Config)
	if err != nil {
		return []nightReadinessCheck{{name: nightShowCueStartTriggersPrefix + show, health: nightHealthUnknown(), cause: err,
			reason: "The coordinator could not read the show playlists. Run readiness again."}}
	}
	var checks []nightReadinessCheck
	for _, p := range playlists {
		if p.Payload.Show != show {
			continue
		}
		name := nightShowCueStartTriggersPrefix + p.ID
		warning, err := fppreconcile.CueMultisyncTriggerReadiness(ctx, h.deps.AssetManifests, p.Payload)
		switch {
		case err != nil:
			checks = append(checks, nightReadinessCheck{name: name, health: nightHealthUnknown(), cause: err, reason: fmt.Sprintf(
				"The coordinator could not check how the cues in playlist %s start their audio. Run readiness again.", p.ID)})
		case warning != "":
			checks = append(checks, nightReadinessCheck{name: name, health: nightHealthDegraded(), reasons: []string{warning}})
		default:
			checks = append(checks, nightReadinessCheck{name: name, health: nightHealthHealthy()})
		}
	}
	return checks
}

// nightCatalogLTCWithoutAudio names the cues in entries that carry timecode
// but no audio. A node refuses every such activation, because timecode runs
// only inside the cue's audio playback.
func nightCatalogLTCWithoutAudio(entries []cuecatalog.Entry) []string {
	var cues []string
	for _, e := range entries {
		if e.Outputs.LTC != nil && e.Outputs.Audio == nil {
			cues = append(cues, e.CueID)
		}
	}
	return cues
}

// nightCheckBackgroundAudioNodeSetup fails when nodeID, a node the bed will
// be sent to, has no audio settings or has lost its program interface. Its
// agent would confirm every command and play to no connected output.
func (h *handlers) nightCheckBackgroundAudioNodeSetup(ctx context.Context, now time.Time, nodeID string) nightReadinessCheck {
	name := nightBackgroundAudioNodeSetupPrefix + nodeID
	unreadable := func(err error) nightReadinessCheck {
		return nightReadinessCheck{name: name, health: nightHealthUnknown(), cause: err, reason: fmt.Sprintf(
			"The coordinator could not read the audio settings for node %s. Run readiness again.", nodeID)}
	}
	rev, _, problem, err := h.getActiveShowConfigRevision(ctx, config.AudioNodeConfigKind, nodeID)
	if err != nil {
		return unreadable(err)
	}
	if problem != nil {
		return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: fmt.Sprintf(
			"Node %s has no audio settings, so background audio sent to it plays nowhere. Set up the node's audio or change the show's audio nodes.", nodeID)}
	}
	payload, verr := config.DecodeAudioNodePayload(rev.PayloadJSON)
	if verr != nil {
		return unreadable(fmt.Errorf("decode audio.node %q: %s", nodeID, verr.Detail))
	}
	evidence, err := audioNodeCapabilitySet(ctx, h.deps.Nodes, now, nodeID)
	if err != nil {
		return unreadable(err)
	}
	if evidence.Live && fppreconcile.GoneAudioRoutes(evidence.Capabilities)[payload.ProgramRoute] {
		return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: fmt.Sprintf(
			"Node %s no longer finds its program interface %s, so background audio sent to it plays nowhere. Reconnect it or pick another route in Node routing.",
			nodeID, config.AudioNodeRouteInterface(payload.ProgramRoute))}
	}
	return nightReadinessCheck{name: name, health: nightHealthHealthy()}
}

// nightCheckBackgroundAudioItemTargets warns when the show's audio nodes
// send the bed somewhere other than the node an item names. ok is false
// when the show's audio nodes did not decide where the bed plays.
func nightCheckBackgroundAudioItemTargets(ba *config.NightSessionBackgroundAudio, resolved []string, resolvedFrom config.AudioNodeResolution) (check nightReadinessCheck, ok bool) {
	if resolvedFrom != config.AudioNodeResolutionShow {
		return nightReadinessCheck{}, false
	}
	var reasons []string
	for _, item := range ba.Items {
		if slices.Contains(resolved, item.Asset.Target) {
			continue
		}
		reasons = append(reasons, fmt.Sprintf(
			"Background audio item %s names node %s but plays on %s, because the show's audio nodes decide. Change the show's audio nodes or the item.",
			item.ItemID, item.Asset.Target, nightNames(resolved)))
	}
	if len(reasons) > 0 {
		return nightReadinessCheck{name: nightBackgroundAudioItemTargetsName, health: nightHealthDegraded(), reasons: reasons}, true
	}
	return nightReadinessCheck{name: nightBackgroundAudioItemTargetsName, health: nightHealthHealthy()}, true
}
