package api

import (
	"net/http"
	"net/url"
	"strconv"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// defaultAuditLimit and maxAuditLimit mirror
// defaultEventsLimit/maxEventsLimit's exact reasoning in handlers.go: one
// constant pair — store's — is the only place either bound is chosen, so
// this package cannot drift from what internal/coordinator/identity's
// underlying store.Store.ListAuditEntries actually enforces.
const (
	defaultAuditLimit = store.DefaultAuditPageSize
	maxAuditLimit     = store.MaxAuditPageSize
)

// handleAudit serves GET /api/v1/audit (ADR-024 decision 11), behind
// [handlers.requireScope](identity.ScopeAuditRead, ...) regardless of
// [Options.CloseReads] — see that guard's doc comment in auth.go. Not on
// the change stream: decision 11 states this explicitly ("the audit log
// ... is not carried on the change stream").
//
// Pages in either direction: order=asc walks forward from since (the
// default), order=desc walks backward from before, which is how a client
// opens on the most recent activity in one request instead of walking
// every retained entry. Every response echoes the ordering it used and
// reports the oldest retained id, so neither is inferred.
func (h *handlers) handleAudit(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	q, problem := parseAuditQuery(r.URL.Query())
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}

	var (
		entries []identity.AuditEntry
		err     error
	)
	if q.order == auditOrderDesc {
		entries, err = h.deps.Identity.ListAuditNewestFirst(r.Context(), q.before, q.limit)
	} else {
		entries, err = h.deps.Identity.ListAudit(r.Context(), q.since, q.limit)
	}
	if err != nil {
		h.writeInternalError(w, now, "list audit entries", err)
		return
	}

	oldest, retained, err := h.deps.Identity.OldestAuditID(r.Context())
	if err != nil {
		h.writeInternalError(w, now, "read oldest audit id", err)
		return
	}
	var oldestRetained *int64
	if retained {
		oldestRetained = &oldest
	}

	out := make([]v1.AuditEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, mapAuditEntry(e))
	}
	jsonWrite(w, v1.AuditResponse{
		ServerTime:       formatTime(now),
		Order:            q.order,
		OldestRetainedID: oldestRetained,
		Entries:          out,
	})
}

// auditOrderAsc pages forward from `since`, oldest first (the default,
// unchanged from before `order` existed). auditOrderDesc pages backward
// from `before`, newest first.
const (
	auditOrderAsc  = "asc"
	auditOrderDesc = "desc"
)

// auditQuery is one parsed GET /api/v1/audit query. Exactly one of since
// and before is meaningful, decided by order; parseAuditQuery refuses the
// combination that would leave which one applies to guesswork.
type auditQuery struct {
	order  string
	since  int64
	before int64
	limit  int
}

// parseAuditQuery reads order (default "asc"), the cursor that order
// selects (since for "asc", before for "desc"; both default 0, meaning
// the far end of retained history in that direction), and limit (default
// [defaultAuditLimit], capped at [maxAuditLimit]), mirroring
// parseEventsQuery's exact validation shape in handlers.go.
func parseAuditQuery(query url.Values) (auditQuery, *v1.Problem) {
	out := auditQuery{order: auditOrderAsc, limit: defaultAuditLimit}

	if raw := query.Get("order"); raw != "" {
		if raw != auditOrderAsc && raw != auditOrderDesc {
			p := invalidParameterProblem("order must be \"asc\" or \"desc\", got " + strconv.Quote(raw))
			return auditQuery{}, &p
		}
		out.order = raw
	}

	sinceRaw, beforeRaw := query.Get("since"), query.Get("before")
	if sinceRaw != "" && beforeRaw != "" {
		p := invalidParameterProblem("since and before are the two directions of one cursor and cannot be combined; use since with order=asc or before with order=desc")
		return auditQuery{}, &p
	}
	if sinceRaw != "" && out.order == auditOrderDesc {
		p := invalidParameterProblem("since pages forward and is not valid with order=desc; use before instead")
		return auditQuery{}, &p
	}
	if beforeRaw != "" && out.order == auditOrderAsc {
		p := invalidParameterProblem("before pages backward and requires order=desc")
		return auditQuery{}, &p
	}

	if sinceRaw != "" {
		v, err := strconv.ParseInt(sinceRaw, 10, 64)
		if err != nil || v < 0 {
			p := invalidParameterProblem("since must be a non-negative integer, got " + strconv.Quote(sinceRaw))
			return auditQuery{}, &p
		}
		out.since = v
	}

	if beforeRaw != "" {
		v, err := strconv.ParseInt(beforeRaw, 10, 64)
		if err != nil || v < 0 {
			p := invalidParameterProblem("before must be a non-negative integer, got " + strconv.Quote(beforeRaw))
			return auditQuery{}, &p
		}
		out.before = v
	}

	if raw := query.Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			p := invalidParameterProblem("limit must be a positive integer, got " + strconv.Quote(raw))
			return auditQuery{}, &p
		}
		if v > maxAuditLimit {
			v = maxAuditLimit
		}
		out.limit = v
	}

	return out, nil
}

// mapAuditEntry renders one identity.AuditEntry onto the wire. Params is
// never null on the wire (matching Event.Details' identical convention in
// mapping.go).
func mapAuditEntry(e identity.AuditEntry) v1.AuditEntry {
	params := e.Params
	if params == nil {
		params = map[string]any{}
	}
	return v1.AuditEntry{
		ID:             e.ID,
		Timestamp:      formatTime(e.Timestamp),
		PrincipalID:    e.PrincipalID,
		PrincipalName:  e.PrincipalName,
		Form:           string(e.Form),
		CredentialID:   e.CredentialID,
		ClientAddr:     e.ClientAddr,
		Action:         e.Action,
		Target:         e.Target,
		Params:         params,
		IdempotencyKey: e.IdempotencyKey,
		Kind:           string(e.Kind),
		CommandID:      e.CommandID,
		Outcome:        e.Outcome,
		OutcomeState:   e.OutcomeState,
		OutcomeReason:  e.OutcomeReason,
	}
}
