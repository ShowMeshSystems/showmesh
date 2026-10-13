package api

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is run-readiness' own instance-participation check, added to
// nightComputeReadinessChecks (nightsessioncontrol.go) beside the existing
// FPP, asset, catalog and interlock checks. It is the readiness half of
// instanceshowparticipation.go: the show says which FPP hosts and which
// Resolume instances take part, and this is where that selection acquires
// a consequence.
//
// The asymmetry is the point, and it runs both ways:
//
//   - An instance the active show does NOT select produces no check at
//     all. A connected, unhealthy FPP host no active show uses is not
//     tonight's problem and must not redden tonight's readiness.
//   - An instance the active show DOES select produces a check that fails
//     when the instance is missing from this coordinator's configuration
//     entirely, and carries its real health otherwise.
//
// A show whose selection has never been recorded counts EVERY configured
// instance, so shipping participation cannot turn a readiness result green
// on a question nobody has answered - see
// [v1.InstanceShowParticipation]'s doc comment.
//
// The existing fpp:<id>:reachable checks (nightCheckFPPReachable) are NOT
// participation-filtered and are deliberately left alone: their two
// instance ids come from the night session's own showPlaylist/resting
// configuration, which is already an explicit operator selection of the
// hosts this night dispatches to, not "whatever happens to be connected".

// nightParticipationCheckPrefix names every check this file produces.
const nightParticipationCheckPrefix = "participation"

// nightCheckShowInstanceParticipation is this file's entry point, called
// from nightComputeReadinessChecks with the night session's own show.
func (h *handlers) nightCheckShowInstanceParticipation(ctx context.Context, now time.Time, show string) []nightReadinessCheck {
	one := func(state nightCheckState, reason string) []nightReadinessCheck {
		return []nightReadinessCheck{{name: nightParticipationCheckPrefix, health: state, reason: reason}}
	}
	unreadable := func(reason string, err error) []nightReadinessCheck {
		return []nightReadinessCheck{{name: nightParticipationCheckPrefix, health: nightHealthUnknown(), reason: reason, cause: err}}
	}

	active, activeErr := resolveActiveShowForParticipation(ctx, h.deps.AssetManifests)
	if activeErr != nil {
		return unreadable(nightActiveShowUnreadReason, activeErr)
	}
	if !active.Configured || active.ShowID != show {
		// Identical to nightCheckCatalogCurrent's own guard, for the
		// identical reason: a selection recorded on a show that is not
		// active authorizes nothing tonight, and evaluating it would
		// report a finding about a show this coordinator is not running.
		return one(nightCheckStateNotConfigured, fmt.Sprintf(
			"Show %s is not the active show, so its FPP and Resolume were not checked. Activate the show.", show))
	}
	selection := resolveShowInstanceParticipation(ctx, h.deps.Config, h.deps.AssetManifests, active, activeErr)
	if selection.unknownReason != "" {
		return one(nightHealthUnknown(), "ShowMesh could not read which FPP and Resolume the active show uses. Run readiness again.")
	}

	fppViews, err := h.deps.FPP.ListInstances(ctx)
	if err != nil {
		return unreadable("ShowMesh could not read its FPP list. Run readiness again.", err)
	}
	resolumeViews, err := h.deps.Resolume.ListInstances(ctx)
	if err != nil {
		return unreadable("ShowMesh could not read its Resolume list. Run readiness again.", err)
	}

	var checks []nightReadinessCheck
	configuredFPP := make(map[string]bool, len(fppViews))
	for _, fv := range fppViews {
		configuredFPP[fv.InstanceID] = true
		if !participationCounts(selection.forFPP(fv.InstanceID)) {
			continue
		}
		health := deriveInstanceHealth(ResolveObservations(fv.Observations), now)
		checks = append(checks, nightParticipationHealthCheck("fpp", "FPP", fv.InstanceID, health))
	}
	configuredResolume := make(map[string]bool, len(resolumeViews))
	for _, rv := range resolumeViews {
		configuredResolume[rv.InstanceID] = true
		if !participationCounts(selection.forResolume(rv.InstanceID)) {
			continue
		}
		health := deriveResolumeHealth(ResolveObservations(rv.Observations), now)
		checks = append(checks, nightParticipationHealthCheck("resolume", "Resolume", rv.InstanceID, health))
	}

	// A selected id with no configured instance behind it is the loudest
	// case this file has: the operator said this host takes part in
	// tonight's show and it is not even configured, so there is no health
	// to report and nothing will ever poll it.
	if fppIDs, selected := selection.fppSelectedIDs(); selected {
		checks = append(checks, nightParticipationAbsentChecks("fpp", "FPP", fppIDs, configuredFPP)...)
	}
	if resolumeIDs, selected := selection.resolumeSelectedIDs(); selected {
		checks = append(checks, nightParticipationAbsentChecks("resolume", "Resolume", resolumeIDs, configuredResolume)...)
	}
	return checks
}

// nightParticipationHealthCheck states one participating instance's health
// as a readiness check, reusing the SAME derived health GET /fpp and GET
// /resolume/instances report rather than re-deriving it a second way.
func nightParticipationHealthCheck(kind, label, instanceID string, health observation.Health) nightReadinessCheck {
	name := nightParticipationCheckPrefix + ":" + kind + ":" + instanceID
	problem := "has not reported how it is doing. Check that it is on."
	switch health {
	case observation.HealthHealthy:
		return nightReadinessCheck{name: name, health: nightHealthHealthy()}
	case observation.HealthDegraded:
		problem = "reports a problem. Check it before the show."
	case observation.HealthFailed:
		problem = "is not working. Check it before the show."
	}
	return nightReadinessCheck{name: name, health: nightCheckState(health),
		reason: fmt.Sprintf("%s %s is in the active show and %s", label, instanceID, problem)}
}

// nightParticipationAbsentChecks produces one failing check per selected
// id that names no configured instance, in a stable order.
func nightParticipationAbsentChecks(kind, label string, ids []string, configured map[string]bool) []nightReadinessCheck {
	var missing []string
	for _, id := range ids {
		if !configured[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	checks := make([]nightReadinessCheck, 0, len(missing))
	for _, id := range missing {
		checks = append(checks, nightReadinessCheck{
			name:   nightParticipationCheckPrefix + ":" + kind + ":" + id,
			health: nightHealthFailed(),
			reason: fmt.Sprintf("The active show uses %s %s, which is not set up here. Add it, or remove it from the show.", label, id),
		})
	}
	return checks
}
