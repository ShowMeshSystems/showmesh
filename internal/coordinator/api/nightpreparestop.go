package api

import (
	"context"
	"fmt"
	"slices"
	"strconv"
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
		return "Prepare site is done, but ShowMesh could not tell which FPP to stop. Stop FPP by hand."
	}
	var instances []string
	for _, id := range []string{payload.Resting.FPPInstanceID, payload.ShowPlaylist.FPPInstanceID} {
		if id != "" && !slices.Contains(instances, id) {
			instances = append(instances, id)
		}
	}
	if len(instances) == 0 {
		return "Prepare site is done. This night names no FPP, so nothing was stopped."
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

	var failed, facts []string
	for i, fact := range results {
		if fact != "" {
			failed = append(failed, instances[i])
			facts = append(facts, fact)
		}
	}
	switch len(failed) {
	case 0:
		return "Prepare site is done, and FPP was stopped."
	case 1:
		return facts[0] + " Stop it by hand if it is still playing."
	}
	return fmt.Sprintf("FPP %s were not confirmed stopped. Stop them by hand if they are still playing.", nightNames(failed))
}

// nightStopOneFPP returns "" when the instance was confirmed idle, and
// otherwise the one fact naming why it was not.
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
		return fmt.Sprintf("FPP %s was not asked to stop because of a coordinator error.", id)
	case problem != nil:
		h.logWarn("night prepare-site: the FPP stop was not sent", "sessionId", rec.ID, "instanceId", id, "reason", problem.Detail)
		return fmt.Sprintf("ShowMesh did not ask FPP %s to stop.", id)
	case outcome.DispatchFailed && outcome.FPPStatusCode != 0:
		return fmt.Sprintf("FPP %s refused the stop.", id)
	case outcome.DispatchFailed:
		return fmt.Sprintf("FPP %s could not be reached, so it was not stopped.", id)
	case outcome.Outcome != "confirmed":
		return fmt.Sprintf("FPP %s was told to stop but has not reported that it is idle.", id)
	}
	return ""
}
