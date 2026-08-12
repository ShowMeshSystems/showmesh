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
func (h *handlers) handleAudit(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	since, limit, problem := parseAuditQuery(r.URL.Query())
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}

	entries, err := h.deps.Identity.ListAudit(r.Context(), since, limit)
	if err != nil {
		h.writeInternalError(w, now, "list audit entries", err)
		return
	}

	out := make([]v1.AuditEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, mapAuditEntry(e))
	}
	jsonWrite(w, v1.AuditResponse{ServerTime: formatTime(now), Entries: out})
}

// parseAuditQuery reads since (default 0, an opaque cursor per
// internal/coordinator/identity.Service.ListAudit's own doc comment) and
// limit (default [defaultAuditLimit], capped at [maxAuditLimit]),
// mirroring parseEventsQuery's exact validation shape in handlers.go.
func parseAuditQuery(query url.Values) (since int64, limit int, problem *v1.Problem) {
	limit = defaultAuditLimit

	if raw := query.Get("since"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 0 {
			p := invalidParameterProblem("since must be a non-negative integer, got " + strconv.Quote(raw))
			return 0, 0, &p
		}
		since = v
	}

	if raw := query.Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			p := invalidParameterProblem("limit must be a positive integer, got " + strconv.Quote(raw))
			return 0, 0, &p
		}
		if v > maxAuditLimit {
			v = maxAuditLimit
		}
		limit = v
	}

	return since, limit, nil
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
