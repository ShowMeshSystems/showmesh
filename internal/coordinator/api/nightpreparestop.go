package api

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

const nightPrepareSiteAlreadyPreparedReason = "The night is already prepared. FPP was not stopped."

// nightStopFPPAtPrepareSite stops playback on the session's FPP instances,
// all at once so the wait is one confirmation deadline. It returns the
// operator-facing result and never fails prepare-site: a refused or
// unconfirmed stop is reported in the result only.
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
	results := make([]string, len(instances))
	var wg sync.WaitGroup
	for i, id := range instances {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = h.nightStopOneFPP(ctx, now, issuer, rec, id, attempt)
		}()
	}
	wg.Wait()

	var stopped, failed []string
	for i, r := range results {
		if r == "" {
			stopped = append(stopped, instances[i])
		} else {
			failed = append(failed, r)
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

// nightStopOneFPP returns "" when the instance was confirmed idle, and
// otherwise the sentence naming why it was not.
func (h *handlers) nightStopOneFPP(ctx context.Context, now time.Time, issuer identity.AuditEntry, rec store.NightSessionRecord, id, attempt string) string {
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
		return fmt.Sprintf("FPP %q was not asked to stop because the coordinator hit an internal error.", id)
	case problem != nil:
		return fmt.Sprintf("FPP %q was not asked to stop: %s", id, strings.TrimSuffix(strings.TrimSpace(problem.Detail), ".")+".")
	case outcome.DispatchFailed && outcome.FPPStatusCode != 0:
		return fmt.Sprintf("FPP %q refused the stop.", id)
	case outcome.DispatchFailed:
		return fmt.Sprintf("FPP %q could not be reached, so it was not stopped.", id)
	case outcome.Outcome != "confirmed":
		return fmt.Sprintf("FPP %q was told to stop but has not reported that it is idle.", id)
	}
	return ""
}
