package fppreconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
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
//
// The returned warning is ADR-049 decision 5's second readiness rule: a
// Cue whose audio and announcement outputs, TOGETHER, reach more than one
// node, and whose combined reach excludes the installation's program+ltc
// node, can never start aligned (decision 3's one-instant selection has no
// program+ltc reading to choose from) -- worth surfacing even though
// decision 4 still plays the audio unaligned. The two outputs are unioned
// per Cue, not judged one at a time: a Cue reaching two nodes by naming one
// each in outputs.audio and outputs.announcement is exactly as unaligned as
// one naming both in a single output. Only the FIRST such Cue is named,
// matching this function's own failure-reporting determinism; it never
// overrides an actual failure found later in the same scan.
func audioTargetReadiness(ctx context.Context, st *store.Store, logger *slog.Logger, p config.ShowPlaylistPayload) (ReadinessCondition, string, string, error) {
	declared, ltcEmitters, err := audioNodeRoles(ctx, st)
	if err != nil {
		return "", "", "", err
	}
	if len(ltcEmitters) > 1 {
		return ReadinessAudioLTCEmitterAmbiguous, fmt.Sprintf(
			"audio.node %q and %q both hold role %q; exactly one node may be the installation's LTC emitter (ADR-018's one clock domain, ADR-045)",
			ltcEmitters[0], ltcEmitters[1], config.AudioNodeRoleProgramLTC), "", nil
	}
	programLTC, defaultTarget := programLTCAndDefault(declared, ltcEmitters)

	warning := ""
	for _, entry := range p.Entries {
		payload, ok, err := decodeCueForAudioTargets(ctx, st, logger, entry.Cue)
		if err != nil {
			return "", "", "", err
		}
		if !ok || payload.Show != p.Show {
			continue
		}
		for _, out := range []struct {
			name    string
			targets []string
			set     bool
		}{
			{"outputs.audio", targetsOf(payload.Outputs.Audio), payload.Outputs.Audio != nil},
			{"outputs.ltc", targetsOfLTC(payload.Outputs.LTC), payload.Outputs.LTC != nil},
			{"outputs.announcement", targetsOfAnnouncement(payload.Outputs.Announcement), payload.Outputs.Announcement != nil},
		} {
			if !out.set {
				continue
			}
			if len(out.targets) == 0 {
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
						entry.Cue, out.name, len(declared), config.AudioNodeRoleProgramLTC), "", nil
				}
				continue
			}
			// ADR-049 widened outputs.audio/announcement to a targets
			// list; every listed node is checked, in order, so the first
			// unbound one is always the one named, matching this
			// function's own doc comment on deterministic ordering.
			for _, target := range out.targets {
				if !containsID(declared, target) {
					return ReadinessAudioTargetUnbound, fmt.Sprintf(
						"cue %q's %s targets node %q, which holds no audio.node object, so that output would reach nobody",
						entry.Cue, out.name, target), "", nil
				}
			}
		}
		// Every output above passed its own unbound-target check, so the
		// union below is safe to compute against already-valid targets.
		if warning == "" {
			union := cueAudioAnnouncementNodes(payload, defaultTarget)
			switch {
			case len(union) <= 1:
				// A Cue reaching at most one node has nothing for
				// decision 3's one-instant selection to disagree about.
			case programLTC == "":
				warning = fmt.Sprintf(
					"cue %q's audio and announcement outputs together reach %v, and no audio.node holds the installation's program+ltc role; a scheduled multi-node start has no node's clock to select an instant from (ADR-049), so this Cue can never start aligned",
					entry.Cue, union)
			case !containsID(union, programLTC):
				warning = fmt.Sprintf(
					"cue %q's audio and announcement outputs together reach %v, which exclude the installation's program+ltc node; a Cue reaching more than one node starts at one instant read from that node's clock (ADR-049), so this Cue can never start aligned",
					entry.Cue, union)
			}
		}
	}
	return "", "", warning, nil
}

// programLTCAndDefault returns the installation's program+ltc node
// (empty when none holds that role) and the node an output naming no
// target resolves to: the sole program+ltc node, or, when there is none,
// the sole audio.node of any role (ADR-045's own resolution rule, shared
// by every audio-target condition in this file, kept identical to
// assetsync's -- the two disagreeing would mean readiness passes a Show
// whose catalog resolves to nothing).
func programLTCAndDefault(declared, ltcEmitters []string) (programLTC, defaultTarget string) {
	switch {
	case len(ltcEmitters) == 1:
		return ltcEmitters[0], ltcEmitters[0]
	case len(declared) == 1:
		return "", declared[0]
	default:
		return "", ""
	}
}

// cueAudioAnnouncementNodes is the set of nodes payload's outputs.audio and
// outputs.announcement, TOGETHER, actually reach: an empty Targets list
// resolves to defaultTarget (ADR-045's own default-target rule), a
// non-empty one is taken verbatim, and the two outputs' resolved sets are
// unioned, deduplicated, and sorted for a deterministic report. outputs.ltc
// is never included: ADR-045 decision 2 keeps it single-node, so it never
// contributes to whether a Cue reaches more than one node.
func cueAudioAnnouncementNodes(payload config.ShowCuePayload, defaultTarget string) []string {
	seen := make(map[string]bool)
	var nodes []string
	add := func(targets []string) {
		for _, id := range resolvedTargets(targets, defaultTarget) {
			if id != "" && !seen[id] {
				seen[id] = true
				nodes = append(nodes, id)
			}
		}
	}
	if payload.Outputs.Audio != nil {
		add(payload.Outputs.Audio.Targets)
	}
	if payload.Outputs.Announcement != nil {
		add(payload.Outputs.Announcement.Targets)
	}
	sort.Strings(nodes)
	return nodes
}

// resolvedTargets is one output's actual reach: targets verbatim when
// non-empty, or defaultTarget alone when the output names no target and
// one exists, or no node at all when neither applies.
func resolvedTargets(targets []string, defaultTarget string) []string {
	if len(targets) > 0 {
		return targets
	}
	if defaultTarget == "" {
		return nil
	}
	return []string{defaultTarget}
}

func targetsOf(o *config.ShowCueAudioOutput) []string {
	if o == nil {
		return nil
	}
	return o.Targets
}

// targetsOfLTC wraps outputs.ltc's own singular Target (ADR-045; ADR-049
// kept it singular) in a slice so it can share this file's own uniform
// targets loop with the list-valued audio/announcement outputs.
func targetsOfLTC(o *config.ShowCueLTCOutput) []string {
	if o == nil || o.Target == "" {
		return nil
	}
	return []string{o.Target}
}

func targetsOfAnnouncement(o *config.ShowCueAnnouncementOutput) []string {
	if o == nil {
		return nil
	}
	return o.Targets
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

// clockLockedValue is the wire value [pkg/mqttproto.ClockPayload.State]
// carries when a node's clock provider is locked -- internal/agent/clock's
// own StateLocked, copied as a literal rather than imported (the
// coordinator never imports internal/agent, matching every other
// wire-vocabulary literal this package copies, e.g. audioClockAlignmentStateWithin
// in internal/coordinator/api/nightaudioreadiness.go).
const clockLockedValue = "locked"

// ClockObservationsLister is the caller-supplied source of a node's most
// recently reported PTP clock status (node.clock.ptp.* signals,
// internal/coordinator/collector/nodeclock's own vocabulary). Declared
// here, structurally identical to api.NodeClockLister, because this
// package is imported BY internal/coordinator/api and must not import it
// back; the production nodeclock.Store already satisfies this with no
// adapter (the same "the real dependency already has this method set"
// pattern that package's own wiring uses elsewhere).
type ClockObservationsLister interface {
	// NodeClockObservations returns every node.clock.ptp.* observation
	// currently held for nodeID's most recently reported clock status, or
	// nil if none has ever been received.
	NodeClockObservations(nodeID string) []observation.Observation
}

// noClockObservationsLister is [ClockObservationsLister]'s nil-safe
// default: a node that never reported a clock status looks identical to
// one this dependency was never wired for, so audioTargetClockReadiness
// treats it as "no clock evidence yet" rather than panicking.
type noClockObservationsLister struct{}

func (noClockObservationsLister) NodeClockObservations(string) []observation.Observation { return nil }

// audioTargetClockReadiness is ADR-049 decision 5's clock-alignment
// warning: every node reached by a Cue whose audio and announcement
// outputs, TOGETHER, target more than one node (see
// [cueAudioAnnouncementNodes]) is checked for the SAME clock-provider lock
// state internal/agent/audio.Manager.StartAt honors when it decides
// whether a scheduled multi-node start is actually usable
// (internal/agent/audio/timeline.go's resolveScheduleLocked, gated on
// agentclock.StateLocked) -- reported to the coordinator verbatim as
// node.clock.ptp.state (internal/agent/clockreport.go's
// clockPayloadFromStatus, internal/coordinator/collector/nodeclock's own
// SignalState). A Cue reaching exactly one node is skipped entirely:
// ADR-049 decision 3 only picks a shared start instant when a Cue reaches
// more than one node, so a single-node Cue's clock state changes nothing.
// outputs.ltc is exempt for the same reason [audioTargetReadiness] exempts
// it: ADR-045 decision 2 keeps it single-target.
//
// This never fails readiness -- decision 4 still plays the audio unaligned
// when a target's clock is not locked -- and collects EVERY offending
// node across the whole playlist rather than stopping at the first: a
// one-node Cue earlier in playlist-entry order must never suppress a
// genuinely multi-node Cue's unlocked target found later in the same
// scan. Each offending node is reported once even if more than one
// qualifying Cue names it. locked contributes no warning at all; every
// other outcome (unlocked, no evidence yet, stale evidence, node
// unavailable/offline) gets its own distinct text, per MANAGER DECISION 2,
// so an operator is never told a missing reading looks the same as a
// locked one.
func audioTargetClockReadiness(ctx context.Context, st *store.Store, logger *slog.Logger, clock ClockObservationsLister, now time.Time, p config.ShowPlaylistPayload) (string, error) {
	if clock == nil {
		clock = noClockObservationsLister{}
	}
	liveness, err := nodeLivenessLookup(ctx, st, now)
	if err != nil {
		return "", err
	}
	declared, ltcEmitters, err := audioNodeRoles(ctx, st)
	if err != nil {
		return "", err
	}
	_, defaultTarget := programLTCAndDefault(declared, ltcEmitters)

	reported := make(map[string]bool)
	var warnings []string
	for _, entry := range p.Entries {
		payload, ok, err := decodeCueForAudioTargets(ctx, st, logger, entry.Cue)
		if err != nil {
			return "", err
		}
		if !ok || payload.Show != p.Show {
			continue
		}
		if len(cueAudioAnnouncementNodes(payload, defaultTarget)) <= 1 {
			continue
		}
		for _, out := range []struct {
			name    string
			targets []string
			set     bool
		}{
			{"outputs.audio", targetsOf(payload.Outputs.Audio), payload.Outputs.Audio != nil},
			{"outputs.announcement", targetsOfAnnouncement(payload.Outputs.Announcement), payload.Outputs.Announcement != nil},
		} {
			if !out.set {
				continue
			}
			for _, target := range resolvedTargets(out.targets, defaultTarget) {
				if reported[target] {
					continue
				}
				if warning := nodeClockWarning(clock, liveness, now, target); warning != "" {
					reported[target] = true
					warnings = append(warnings, fmt.Sprintf("cue %q's %s targets node %q: %s", entry.Cue, out.name, target, warning))
				}
			}
		}
	}
	return strings.Join(warnings, "; "), nil
}

// nodeClockWarning reports nodeID's clock-lock problem, or "" when it is
// locked. Checked in this order: liveness first (a node that is not
// reporting at all cannot have its clock evidence trusted regardless of
// what it last said), then the clock evidence itself.
func nodeClockWarning(clock ClockObservationsLister, liveness map[string]nodeLivenessInfo, now time.Time, nodeID string) string {
	if info := livenessOrUnknown(liveness, nodeID); info.liveness != inventory.LivenessOnline {
		return fmt.Sprintf("its clock cannot be confirmed locked: the node itself is not currently reporting (%s)", info.reason)
	}

	var stateObs *observation.Observation
	for _, o := range clock.NodeClockObservations(nodeID) {
		if o.Signal == nodeClockStateSignal {
			o := o
			stateObs = &o
			break
		}
	}
	if stateObs == nil {
		return "no node.clock.ptp.state evidence has ever been reported for it"
	}
	switch state := stateObs.StateAt(now); state {
	case observation.StateStale:
		detail := "clock evidence"
		if stateObs.ObservedAt != nil {
			detail = fmt.Sprintf("clock evidence last observed at %s", stateObs.ObservedAt.Format(time.RFC3339))
		}
		return fmt.Sprintf("its most recent %s is stale, so its clock cannot be confirmed locked right now", detail)
	case observation.StateCurrent:
		value, _ := stateObs.Value.(string)
		if value == clockLockedValue {
			return ""
		}
		return fmt.Sprintf("its clock provider reports %q, not locked, so a scheduled multi-node start would ignore the shared instant on this node (ADR-046)", value)
	default:
		return fmt.Sprintf("its node.clock.ptp.state evidence is %s rather than current or stale, so its clock cannot be confirmed locked", state)
	}
}

// nodeClockStateSignal is internal/coordinator/collector/nodeclock.SignalState,
// copied as a literal for the same reason [clockLockedValue] is: this
// package must not import a collector (TestPackageNeverImportsACollector's
// rule, stated for the api package and followed here identically).
const nodeClockStateSignal observation.SignalID = "node.clock.ptp.state"
