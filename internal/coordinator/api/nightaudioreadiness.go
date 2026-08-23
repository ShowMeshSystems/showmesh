package api

import (
	"context"
	"fmt"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// Track F seam F5's own readiness checks (RESTING-MODE.md §13), added to
// nightComputeReadinessChecks (nightsessioncontrol.go) alongside seam F3/
// F4's own. Only checked when resting.backgroundAudio is configured at
// all - omitting it is valid and disables no part of the rest/show loop
// (RESTING-MODE.md §2).
//
// §13 also names announcement asset freshness and a synchronized third-
// party output's own provisioning evidence. Both are reported
// not_verifiable here rather than invented: this seam's own night.session
// config carries no structured asset reference for an announcement (its
// content lives inside the bound show.action's own opaque target.params,
// which this package does not interpret), and night.session carries no
// configuration surface for a synchronized third-party output at all
// (RES-016 remains open research, not a shipped config kind) - there is
// nothing here to check either claim against.

// nightCheckBackgroundAudioAssets is §13's "required audio ... assets
// local and hash-current" bullet, narrowed to what this coordinator's own
// asset store answers: every configured item resolves to a CURRENT (non-
// superseded) asset of MediaType "audio" for its own (show, sequence,
// target) - the identical check nightBuildBackgroundPlaylistItems already
// performs before a dispatch, run here so a missing or wrong-typed asset
// is visible before start-night rather than only at the first dispatch
// failure.
func (h *handlers) nightCheckBackgroundAudioAssets(ctx context.Context, show string, ba *config.NightSessionBackgroundAudio) nightReadinessCheck {
	name := "resting:background-audio-assets"
	if _, err := h.nightBuildBackgroundPlaylistItems(ctx, show, ba.Items); err != nil {
		return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: err.Error()}
	}
	return nightReadinessCheck{name: name, health: nightHealthHealthy(), reason: fmt.Sprintf(
		"%d item(s) resolve to a current, non-superseded coordinator asset record of media type \"audio\". NOT checked: whether the stored bytes still hash-match on disk (no re-hash-on-read exists in this build), and whether audio.media.probe has confirmed a usable duration for any item (this readiness pass never dispatches a node probe)",
		len(ba.Items))}
}

// nightCheckBackgroundAudioItemTransition is §13's own item-transition
// capability bullet, for the ONE case this coordinator can actually
// answer without a real capability signal: sequential never needs
// confirmation and always passes; gapless and crossfade require an
// output confirmation this codebase has no signal for (this file's own
// top doc comment, and nightAdvanceBackgroundAudio's identical check at
// dispatch time), so configuring either is reported failed HERE, at
// readiness, rather than only discovered when background audio never
// starts.
func nightCheckBackgroundAudioItemTransition(ba *config.NightSessionBackgroundAudio) nightReadinessCheck {
	name := "resting:background-audio-item-transition"
	if err := pkgaudio.ValidateItemTransitionSupport(pkgaudio.ItemTransition(ba.ItemTransition), false); err != nil {
		return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: fmt.Sprintf(
			"itemTransition %q requires the selected output to confirm support, and no audio.node capability signal for that exists in this build; background audio will refuse to start until this is changed to \"sequential\" or that capability signal ships", ba.ItemTransition)}
	}
	return nightReadinessCheck{name: name, health: nightHealthHealthy(), reason: "sequential requires no output confirmation"}
}

// nightCheckAudioOutputCapabilities is §13's own bullet: "every configured
// audio output declares the background, announcement, playlist, mix,
// duck, interrupt, loop, gain, fade, seek, position, and requested
// sequential/gapless/crossfade item-transition capabilities this session
// requires." pkg/capability's own registered vocabulary
// (pkg/capability/id.go) has no member for any of these - only
// audio.engine/audio.output.local/audio.output.fm/audio.output.ltc/
// audio.output.dante/audio.playback/audio.multichannel/audio.dante exist,
// none of them this granular - so this coordinator holds no evidence to
// check against and reports that honestly rather than inventing a passing
// check.
func nightCheckAudioOutputCapabilities(nodeID string) nightReadinessCheck {
	return nightReadinessCheck{
		name: "resting:background-audio-output-capabilities:" + nodeID, health: nightCheckStateNotVerifiable,
		reason: "no audio.node capability signal for background/announcement/playlist/mix/duck/interrupt/loop/gain/fade/seek/position exists in this build; this coordinator cannot confirm the output declares them",
	}
}

// nightCheckAnnouncementAssets is §13's own announcement-asset bullet -
// see this file's own top doc comment for why it is always not_verifiable
// here.
func nightCheckAnnouncementAssets(cues []config.NightSessionCue) nightReadinessCheck {
	name := "announcement-assets"
	for _, cue := range cues {
		if cue.Role == config.NightSessionCueRoleAnnouncement {
			return nightReadinessCheck{
				name: name, health: nightCheckStateNotVerifiable,
				reason: "an announcement cue's audio content lives inside its bound show.action's own opaque target.params; this coordinator has no structured asset reference for it to check against the asset store",
			}
		}
	}
	return nightReadinessCheck{name: name, health: nightCheckStateNotConfigured, reason: "no announcement-role cue is configured"}
}

// nightCheckAnnouncementPolicyEnforceable reports whether every
// configured announcement cue can actually play and actually make the
// room its configured policy asks for. The node resolves duck and
// interrupt from the source role and mix policy declared on the
// announcement's own playback session, at the moment that session
// STARTS, so three separate things have to hold and each is checked
// here rather than discovered on the night.
//
// The bound action must be audio.session.apply: that is the dispatch the
// declaration rides on, and it is the one this controller pairs with an
// audio.session.start of its own (nightannouncement.go). An action bound
// straight to audio.session.start has nothing to start, since nothing
// applied the session first.
//
// The operator's own action params must not contradict the cue's
// configured policy. They win at dispatch, by design, so a show.action
// declaring mixPolicy "mix" under a cue configured to duck silently
// discards the cue's policy. Reported here rather than resolved in
// either direction: neither value is safe to overrule on the operator's
// behalf.
//
// A declared source role must be able to outrank a background bed.
// pkgaudio.OutranksForMixing is the exact comparison the node makes, so
// a role of "background" ducks strictly-lower-than-background, meaning
// nothing at all.
func (h *handlers) nightCheckAnnouncementPolicyEnforceable(ctx context.Context, cues []config.NightSessionCue, payload config.NightSessionPayload) nightReadinessCheck {
	backgroundAudioConfigured := payload.Resting.BackgroundAudio != nil
	name := "announcement-policy-enforceable"
	var announcements int
	var notApply, contradicted, cannotOutrank []string
	for _, cue := range cues {
		if cue.Role != config.NightSessionCueRoleAnnouncement {
			continue
		}
		announcements++
		action, _, err := nightResolveShowAction(ctx, h.deps.Config, cue.Action)
		if err != nil {
			return nightReadinessCheck{name: name, health: nightHealthUnknown(), reason: fmt.Sprintf("could not read announcement cue %q's bound show.action %q: %s", cue.Name, cue.Action, err.Error())}
		}
		if !nightAnnouncementTargetDeclarable(action.Target) {
			notApply = append(notApply, cue.Name)
			continue
		}
		if declared, ok := action.Target.Params["mixPolicy"].(string); ok && declared != "" && declared != nightAnnouncementPolicy(cue, payload) {
			contradicted = append(contradicted, fmt.Sprintf("%s (cue policy %q, action params %q)", cue.Name, nightAnnouncementPolicy(cue, payload), declared))
		}
		if declared, ok := action.Target.Params["sourceRole"].(string); ok && declared != "" {
			if !pkgaudio.OutranksForMixing(pkgaudio.SourceRole(declared), pkgaudio.SourceRoleBackground) {
				cannotOutrank = append(cannotOutrank, fmt.Sprintf("%s (source role %q)", cue.Name, declared))
			}
		}
	}
	switch {
	case announcements == 0:
		return nightReadinessCheck{name: name, health: nightCheckStateNotConfigured, reason: "no announcement-role cue is configured"}
	case len(notApply) > 0:
		return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: fmt.Sprintf("announcement cue(s) %v are not bound to an audio.session.apply action; an announcement is an apply carrying source role \"announcement\" and a declared mix policy, followed by the audio.session.start this controller issues itself, so these cues will not play at all", notApply)}
	case len(contradicted) > 0:
		return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: fmt.Sprintf("announcement cue(s) %v bind a show.action whose own params declare a mix policy different from the cue's configured announcementPolicy; operator-authored params win at dispatch, so the configured policy would be silently discarded, and neither value is safe to overrule on the operator's behalf", contradicted)}
	case len(cannotOutrank) > 0 && backgroundAudioConfigured:
		return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: fmt.Sprintf("announcement cue(s) %v bind a show.action declaring a source role that cannot outrank a background bed, so the node would duck nothing and resting background audio would play straight through the announcement", cannotOutrank)}
	case len(cannotOutrank) > 0:
		return nightReadinessCheck{name: name, health: nightHealthHealthy(), reason: fmt.Sprintf("announcement cue(s) %v declare a source role that could not outrank a background bed, but no resting background audio is configured for them to make room in", cannotOutrank)}
	default:
		return nightReadinessCheck{name: name, health: nightHealthHealthy(), reason: fmt.Sprintf("%d announcement cue(s) bind audio.session.apply, declare no params contradicting their configured policy, and declare no source role that could not outrank a background bed", announcements)}
	}
}
