package api

import (
	"context"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/capability"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// audioEngineStateSignalID, audioEngineStateUsable, and
// audioEngineStateUnavailableMirror are this file's own copy of
// internal/coordinator/collector/nodeaudio's
// SignalEngineState/StateUsable/StateUnavailable literals. This package
// must never import internal/coordinator/collector/... at all
// (TestPackageNeverImportsACollector, resolumeinstances_test.go): api
// holds no client capable of reaching a live device or transport, and it
// must stay that way structurally, not by convention. h.deps.Audio
// (NodeAudioLister, declared in this package's own interfaces.go) is the
// observation-store-shaped dependency this package already reads
// through; its production implementation lives in the collector package
// and is wired in from outside (cmd/showmesh-coordinator), so using it
// needs no import here. Reading these three literal values needs one, so
// they are copied rather than imported, matching audionode.go's own
// audioOutputLocalCapabilityID/audioOutputLTCCapabilityID convention one
// file over.
const (
	audioEngineStateSignalID          observation.SignalID = "node.audio.engine.state"
	audioEngineStateUsable                                 = "usable"
	audioEngineStateUnavailableMirror                      = "unavailable"
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

// capabilityDetectionTimeoutMirror is this file's own copy of
// internal/agent/advertise.go's capabilityDetectionTimeout (120s as of
// this writing), for the reason string
// audioNodeEngineConfirmedUsableNow's caller states when a node's
// capability detection appears still in flight. This package cannot
// import internal/agent (it pulls in internal/agent/audio/gstengine's
// cgo dependency, which the coordinator's CGO_ENABLED=0 static build
// must never gain), so this is a literal, human-kept mirror rather than
// a shared constant, matching internal/agent/audiocapabilities.go's own
// minLTCChannels mirror of audio.MinLTCChannels one file over. Purely
// informational text in a reason string: nothing here gates behavior on
// this value, so a stale mirror only makes an operator-facing message
// slightly wrong, never a wrong health verdict.
const capabilityDetectionTimeoutMirror = "120s"

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

// transitionCapabilityID returns the capability.ID that would confirm t,
// and false for [pkgaudio.ItemTransitionSequential], which
// [pkgaudio.ValidateItemTransitionSupport] never gates on output
// confirmation at all - see [audioNodeConfirmsTransition].
func transitionCapabilityID(t pkgaudio.ItemTransition) (capability.ID, bool) {
	switch t {
	case pkgaudio.ItemTransitionGapless:
		return "audio.transition.gapless", true
	case pkgaudio.ItemTransitionCrossfade:
		return "audio.transition.crossfade", true
	default:
		return "", false
	}
}

// audioNodeConfirmsTransition reads nodeID's live capability advertisement
// and reports whether it confirms t, per [transitionCapabilityID] - the
// real answer to the outputConfirms bool
// [pkgaudio.ValidateItemTransitionSupport] takes, in place of the
// hardcoded false this coordinator passed before that capability signal
// existed. Sequential needs no confirmation and reports (true, a
// synthetic Live evidence, nil) without reading inventory at all. For
// gapless or crossfade, the returned [audioNodeCapabilityEvidence] tells
// a caller whether that answer is trustworthy as current
// (evidence.Live); [nightAdvanceBackgroundAudio] (deciding whether to
// actually dispatch a session) can ignore that and just treat confirmed
// as the answer - refusing on unconfirmable evidence is the safe default
// either way - but a readiness check needs it to report unknown rather
// than failed.
func audioNodeConfirmsTransition(ctx context.Context, nodes NodeLister, now time.Time, nodeID string, t pkgaudio.ItemTransition) (confirmed bool, evidence audioNodeCapabilityEvidence, err error) {
	id, needsConfirmation := transitionCapabilityID(t)
	if !needsConfirmation {
		return true, audioNodeCapabilityEvidence{Live: true}, nil
	}
	evidence, err = audioNodeCapabilitySet(ctx, nodes, now, nodeID)
	if err != nil {
		return false, audioNodeCapabilityEvidence{}, err
	}
	if !evidence.Live {
		return false, evidence, nil
	}
	_, confirmed = evidence.Capabilities.Lookup(id)
	return confirmed, evidence, nil
}

// nightCheckBackgroundAudioItemTransition is §13's own item-transition
// capability bullet: sequential never needs confirmation and always
// passes; gapless and crossfade pass only when the configured output
// node's live Hello capability advertisement declares the matching
// audio.transition.* ID (nightAdvanceBackgroundAudio's identical check
// at dispatch time reads the same evidence). A session configured
// against an output that has current evidence and genuinely lacks the
// ability is reported failed HERE, at readiness, rather than only
// discovered when background audio never starts; a session configured
// against an output this coordinator cannot currently confirm anything
// about (never seen, never advertised, or not currently online) is
// reported unknown, matching api/openapi.yaml's NightReadinessCheck rule
// that missing evidence is never "failed".
func (h *handlers) nightCheckBackgroundAudioItemTransition(ctx context.Context, now time.Time, ba *config.NightSessionBackgroundAudio) nightReadinessCheck {
	name := "resting:background-audio-item-transition"
	t := pkgaudio.ItemTransition(ba.ItemTransition)
	nodeID := ba.OutputNodeID()
	confirms, evidence, err := audioNodeConfirmsTransition(ctx, h.deps.Nodes, now, nodeID, t)
	if err != nil {
		return nightReadinessCheck{name: name, health: nightHealthUnknown(), reason: fmt.Sprintf(
			"could not read audio.node %q's capability advertisement: %s", nodeID, err.Error())}
	}
	if t == pkgaudio.ItemTransitionSequential {
		return nightReadinessCheck{name: name, health: nightHealthHealthy(), reason: "sequential requires no output confirmation"}
	}
	id, _ := transitionCapabilityID(t)
	if evidence.NeverPublished {
		return nightReadinessCheck{name: name, health: nightCheckStateNotVerifiable, reason: fmt.Sprintf(
			"audio.node %q has never published a capability advertisement; this coordinator has no signal to check whether it declares %q against (an agent built before this capability signal existed makes no claim either way)", nodeID, id)}
	}
	if !evidence.Live {
		return nightReadinessCheck{name: name, health: nightHealthUnknown(), reason: fmt.Sprintf(
			"cannot confirm whether output %q declares %q: %s", nodeID, id, evidence.NotLiveReason)}
	}
	if verr := pkgaudio.ValidateItemTransitionSupport(t, confirms); verr != nil {
		return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: fmt.Sprintf(
			"itemTransition %q requires output %q to declare %q, and its current capability advertisement does not; background audio will refuse to start until this is changed to \"sequential\" or the output ships that capability", ba.ItemTransition, nodeID, id)}
	}
	return nightReadinessCheck{name: name, health: nightHealthHealthy(), reason: fmt.Sprintf("output %q declares %q", nodeID, id)}
}

// audioOutputCapabilityIDsForBackgroundAudio returns the capability.IDs
// a configured resting.backgroundAudio session concretely exercises on
// its output node, independent of the separate
// item-transition ability [nightCheckBackgroundAudioItemTransition]
// already reports under its own check name: audio.playback.background
// and audio.playback.playlist always (every apply carries source role
// background and a real playlist - nightBuildBackgroundApplyBody), and
// audio.playback.gain always (nightBGStepGain runs on every session
// start/gain step, unconditionally). audio.playback.loop is required
// only when the configured repeat mode actually asks for one - "none"
// never calls Advance with RepeatItem/RepeatPlaylist semantics.
//
// audio.playback.announcement, audio.mix.duck, audio.mix.interrupt, and
// audio.playback.seek/position are deliberately NOT required here: this
// controller issues no seek for background audio, and duck/interrupt/
// announcement describe the ANNOUNCEMENT session's own requirement on
// its own output node (which may or may not be this one) - a different
// session's readiness this check does not have evidence to speak for.
func audioOutputCapabilityIDsForBackgroundAudio(ba *config.NightSessionBackgroundAudio) []capability.ID {
	ids := []capability.ID{"audio.playback.background", "audio.playback.playlist", "audio.playback.gain"}
	if ba.Repeat == string(pkgaudio.RepeatItem) || ba.Repeat == string(pkgaudio.RepeatPlaylist) {
		ids = append(ids, "audio.playback.loop")
	}
	return ids
}

// audioNodeEngineStateNow reads nodeID's most recent node.audio.engine.state
// observation and reports what it currently says: usable=true when it is
// CURRENT and reads "usable"; unavailable=true when it is CURRENT and
// reads "unavailable" (real negative evidence). Neither is true when this
// coordinator holds no reliable evidence either way right now: the
// observation is absent (no audioreport has ever landed for this node -
// runAudioReport's own discovery phase runs synchronously before its
// first tick, so a real node's first report can land 60-90s after
// connect, not on some fast independent cadence a caller could assume),
// or present but not current (aged past its own ValidFor, or of unknown
// age). Absence is NOT evidence of unavailability: evidence that cannot
// exist yet says nothing about what it would eventually say, so a caller
// must not treat "neither" the same as "unavailable".
//
// This is independent of the Hello capability advertisement's own cycle
// (up to capabilityDetectionTimeout, 120s in this build, after a binding
// change), not necessarily faster (a real node's first audioreport can
// itself land 60-90s after connect, see the "neither" case above).
// Consulted ONLY to distinguish "the engine is confirmed usable but its
// Hello capability set has not caught up to that yet" (still probing)
// from "the engine is confirmed genuinely unavailable" when a live
// node's Hello capability set declares none of what a check needs;
// never used to invent a
// capability the Hello envelope itself never declared, and never a
// timestamp-derived guess (deliberately not StartedAt-based), since an
// inferred window is wrong across a restart, wrong under clock skew, and
// wrong whenever detection outruns whatever window was picked.
//
// This reads node.audio.engine.state's two EXISTING values as a new
// corroborating consumer, rather than adding a third value to that
// signal's own vocabulary: its own doc comment
// (nodeaudio.StateUsable/StateUnavailable in
// internal/coordinator/collector/nodeaudio/signals.go) already states
// the "not known yet" case belongs to [observation.StateNotCollected] on
// Absence, not a third state string, so a "probing" member there would
// contradict that signal's own documented shape. A repository-wide
// search for every reader of this signal (this package must never
// import the collector package that produces it -
// TestPackageNeverImportsACollector, resolumeinstances_test.go) found
// none that switches on its string value at all (only its presence is
// checked in one UI test fixture), so introducing this reader risks no
// existing default-branch misread.
func audioNodeEngineStateNow(audio NodeAudioLister, nodeID string, now time.Time) (usable, unavailable bool) {
	for _, o := range audio.NodeAudioObservations(nodeID) {
		if o.Signal != audioEngineStateSignalID {
			continue
		}
		if o.StateAt(now) != observation.StateCurrent {
			return false, false
		}
		switch o.Value {
		case audioEngineStateUsable:
			return true, false
		case audioEngineStateUnavailableMirror:
			return false, true
		default:
			// An unrecognized value is exactly as reliable as no
			// evidence at all: report neither rather than guessing.
			return false, false
		}
	}
	return false, false
}

// nightCheckAudioOutputCapabilities is §13's own bullet, narrowed to what
// this configured resting.backgroundAudio session concretely needs (see
// [audioOutputCapabilityIDsForBackgroundAudio]): healthy when the output
// node's live Hello capability advertisement declares every one of them,
// failed naming whichever a node with CURRENT evidence genuinely does
// not declare, and unknown whenever that evidence cannot be trusted as
// current at all (never seen, never advertised, not presently online, or
// merely still probing per [audioNodeEngineConfirmedUsableNow]) -
// api/openapi.yaml's own NightReadinessCheck rule that missing evidence
// is never "failed". This check now has a real capability signal to
// check against; before it, this always reported not_verifiable.
func (h *handlers) nightCheckAudioOutputCapabilities(ctx context.Context, now time.Time, ba *config.NightSessionBackgroundAudio) nightReadinessCheck {
	nodeID := ba.OutputNodeID()
	name := "resting:background-audio-output-capabilities:" + nodeID
	evidence, err := audioNodeCapabilitySet(ctx, h.deps.Nodes, now, nodeID)
	if err != nil {
		return nightReadinessCheck{name: name, health: nightHealthUnknown(), reason: fmt.Sprintf(
			"could not read audio.node %q's capability advertisement: %s", nodeID, err.Error())}
	}
	// A node that has never published a Hello at all (or never appeared
	// in inventory) makes no claim about these capabilities, the same
	// way an agent built before this signal existed never will: reading
	// that as failed infers a claim it never made, the identical
	// dishonesty this check exists to remove, aimed the other way. Stays
	// not_verifiable, excluded from the aggregate outcome, exactly as
	// this check behaved before this build had any signal to check
	// against at all.
	if evidence.NeverPublished {
		return nightReadinessCheck{name: name, health: nightCheckStateNotVerifiable, reason: fmt.Sprintf(
			"audio.node %q has never published a capability advertisement; this coordinator has no signal to check against (an agent built before this capability signal existed makes no claim either way)", nodeID)}
	}
	if !evidence.Live {
		return nightReadinessCheck{name: name, health: nightHealthUnknown(), reason: fmt.Sprintf(
			"cannot confirm which capabilities audio.node %q declares: %s", nodeID, evidence.NotLiveReason)}
	}
	needed := audioOutputCapabilityIDsForBackgroundAudio(ba)
	var missing []string
	for _, id := range needed {
		if _, ok := evidence.Capabilities.Lookup(id); !ok {
			missing = append(missing, string(id))
		}
	}
	if len(missing) == len(needed) {
		usable, unavailable := audioNodeEngineStateNow(h.deps.Audio, nodeID, now)
		switch {
		case usable:
			return nightReadinessCheck{name: name, health: nightHealthUnknown(), reason: fmt.Sprintf(
				"audio.node %q's node.audio.engine.state confirms its session engine is usable right now, but its Hello capability advertisement declares none of %v yet; this node has likely not finished its post-connect capability detection, which can take up to %s",
				nodeID, needed, capabilityDetectionTimeoutMirror)}
		case !unavailable:
			// Neither usable nor unavailable: no reliable
			// node.audio.engine.state evidence exists yet either
			// (runAudioReport's own discovery phase runs before its
			// first tick, so a real node's first report can land 60-90s
			// after connect). Evidence that cannot exist yet is not
			// evidence of absence, so this reads unknown, not failed,
			// exactly like the usable case above.
			return nightReadinessCheck{name: name, health: nightHealthUnknown(), reason: fmt.Sprintf(
				"audio.node %q's Hello capability advertisement declares none of %v, and this coordinator has no current node.audio.engine.state evidence yet to confirm whether that is because the engine is genuinely unavailable or because this node has not finished reporting since it connected",
				nodeID, needed)}
		}
		// unavailable == true: real negative evidence. Falls through to
		// the ordinary missing-capabilities failure below.
	}
	if len(missing) > 0 {
		return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: fmt.Sprintf(
			"audio.node %q's current capability advertisement does not declare %v, which this configured resting.backgroundAudio session requires", nodeID, missing)}
	}
	return nightReadinessCheck{name: name, health: nightHealthHealthy(), reason: fmt.Sprintf(
		"audio.node %q declares every capability %v this configured resting.backgroundAudio session requires", nodeID, needed)}
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
