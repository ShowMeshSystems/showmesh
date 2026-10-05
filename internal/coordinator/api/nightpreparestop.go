package api

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// nightStopFPPAtPrepareSite stops playback on the session's FPP instances
// when the operator asked prepare-site to. It returns the operator-facing
// result and never fails prepare-site: a refused or unconfirmed stop is
// reported in the result only.
func (h *handlers) nightStopFPPAtPrepareSite(ctx context.Context, now time.Time, issuer identity.AuditEntry, idempotencyKey string, rec store.NightSessionRecord) string {
	payload, err := h.getPinnedNightSessionPayload(ctx, rec)
	if err != nil {
		h.logWarn("night prepare-site: could not read the session to find its FPP instances", "sessionId", rec.ID, "error", err)
		return "Prepare site is done, but FPP was not stopped because its instances could not be looked up. Stop FPP by hand."
	}
	var instances []string
	for _, id := range []string{payload.Resting.FPPInstanceID, payload.ShowPlaylist.FPPInstanceID} {
		if id != "" && !slices.Contains(instances, id) {
			instances = append(instances, id)
		}
	}
	if len(instances) == 0 {
		return "Prepare site is done, but this night names no FPP instance, so nothing was stopped."
	}

	attempt := idempotencyKey
	if attempt == "" {
		attempt = strconv.FormatInt(now.UnixNano(), 10)
	}
	var stopped, failed []string
	for _, id := range instances {
		outcome, problem, err := h.dispatchFPPCommand(ctx, now, FPPCommandInput{
			InstanceID:     id,
			Action:         fppActionStopPlaylist,
			IdempotencyKey: fmt.Sprintf("night-prepare-site-stop:%s:%s:%s", rec.ID, id, attempt),
			Issuer: FPPCommandIssuer{
				PrincipalID: issuer.PrincipalID, PrincipalName: issuer.PrincipalName,
				Form: issuer.Form, CredentialID: issuer.CredentialID, ClientAddr: issuer.ClientAddr,
			},
			NeverWithholdOnAuditFailure: true,
		})
		switch {
		case err != nil:
			h.logWarn("night prepare-site: FPP stop could not be dispatched", "sessionId", rec.ID, "instanceId", id, "error", err)
			failed = append(failed, fmt.Sprintf("FPP %q was not stopped because the stop could not be sent.", id))
		case problem != nil:
			failed = append(failed, fmt.Sprintf("FPP %q refused to stop.", id))
		case outcome.DispatchFailed:
			failed = append(failed, fmt.Sprintf("FPP %q could not be reached, so it was not stopped.", id))
		case outcome.Outcome != "confirmed":
			failed = append(failed, fmt.Sprintf("FPP %q was told to stop but has not reported that it is idle.", id))
		default:
			stopped = append(stopped, id)
		}
	}

	var parts []string
	if len(stopped) > 0 {
		parts = append(parts, fmt.Sprintf("Stopped what FPP was playing on %s.", strings.Join(stopped, " and ")))
	}
	if len(failed) > 0 {
		parts = append(parts, strings.Join(failed, " "), "Check FPP and stop it by hand if it is still playing.")
	}
	return strings.Join(parts, " ")
}
