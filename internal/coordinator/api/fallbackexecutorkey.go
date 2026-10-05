package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/fallbackprogram"
)

// A paired FPP plugin registers the public key it signs fallback
// activations with. The key is public, so nothing here is a secret; the
// coordinator carries it in that FPP player's signed fallback program.

const auditActionFallbackExecutorKeyRegister = "fallback.executor_key.register"

const maxFallbackExecutorKeyRequestBodyBytes = 4 << 10 // 4 KiB

// FallbackProgramNudger asks the fallback-program reconciler for an
// immediate pass. fallbackreconcile.Service satisfies it.
type FallbackProgramNudger interface {
	Nudge()
}

type noFallbackProgramNudger struct{}

func (noFallbackProgramNudger) Nudge() {}

// fallbackExecutorKeyNotPairedProblem refuses a caller that is not the
// plugin paired as this FPP player.
func fallbackExecutorKeyNotPairedProblem() v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeForbidden,
		Title:  "Forbidden",
		Status: http.StatusForbidden,
		Detail: "Only the plugin paired with this FPP player can register its key. Pair the plugin with this player and try again.",
	}
}

// callerIsPairedPluginFor reports whether p is the pairing principal of
// the FPP player whose recorded instance UUID is instanceUUID. A player
// the coordinator has not yet read a UUID from matches nothing.
func (h *handlers) callerIsPairedPluginFor(ctx context.Context, p identity.Principal, instanceUUID string) (bool, error) {
	endpointID, isPlugin := strings.CutPrefix(p.Name, fppPairingPrincipalPrefix)
	if p.Kind != identity.KindMachine || !isPlugin || endpointID == "" {
		return false, nil
	}
	rec, err := h.deps.FallbackPrograms.GetFPPInstanceUUID(ctx, endpointID)
	if errors.Is(err, store.ErrFPPInstanceUUIDNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return rec.UUID == instanceUUID, nil
}

// handlePutFallbackExecutorKey serves
// PUT /api/v1/fallback-programs/{fppInstanceId}/executor-key.
func (h *handlers) handlePutFallbackExecutorKey(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)
	instanceUUID := r.PathValue("fppInstanceId")
	if instanceUUID == "" {
		writeProblem(w, h.logger, now, invalidParameterProblem("fppInstanceId is required"))
		return
	}

	var req v1.FallbackExecutorKeyRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxFallbackExecutorKeyRequestBodyBytes+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("malformed request body: "+err.Error()))
		return
	}
	if _, err := fallbackprogram.ParseExecutorPublicKey(req.PublicKey); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("publicKey must be a 32 byte Ed25519 public key in standard base64"))
		return
	}
	if h.deps.FallbackPrograms == nil {
		h.writeInternalError(w, now, "register fallback executor key", errFallbackProgramStoreNotWired)
		return
	}
	paired, err := h.callerIsPairedPluginFor(ctx, ac.result.Principal, instanceUUID)
	if err != nil {
		h.writeInternalError(w, now, "resolve the caller's FPP player", err)
		return
	}
	if !paired {
		writeProblem(w, h.logger, now, fallbackExecutorKeyNotPairedProblem())
		return
	}

	existing, err := h.deps.FallbackPrograms.GetFallbackExecutorKey(ctx, instanceUUID)
	if err == nil && existing.PublicKeyB64 == req.PublicKey {
		jsonWrite(w, fallbackExecutorKeyResponse(now, existing, false))
		return
	}
	if err != nil && !errors.Is(err, store.ErrFallbackExecutorKeyNotFound) {
		h.writeInternalError(w, now, "get fallback executor key", err)
		return
	}

	audit := identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: auditActionFallbackExecutorKeyRegister, Target: instanceUUID,
		Kind: identity.AuditDispatch, CommandID: uuid.NewString(),
		Params: map[string]any{"publicKey": req.PublicKey, "replaced": err == nil},
	}
	if !h.writeAuditOrFail(ctx, w, now, audit) {
		return
	}

	stored, changed, err := h.deps.FallbackPrograms.PutFallbackExecutorKey(ctx, store.FallbackExecutorKeyRecord{
		FPPInstanceUUID: instanceUUID, PublicKeyB64: req.PublicKey, RegisteredAt: now,
	})
	if err != nil {
		h.writeInternalError(w, now, "store fallback executor key", err)
		return
	}
	if changed {
		h.deps.FallbackProgramNudger.Nudge()
	}

	outcome := audit
	outcome.Timestamp, outcome.Kind, outcome.OutcomeReason = h.now(), identity.AuditOutcome, "registered"
	if h.deps.Identity != nil {
		if err := h.deps.Identity.WriteAudit(ctx, outcome); err != nil {
			h.logWarn("fallback executor key outcome audit write failed", "fppInstanceUuid", instanceUUID, "error", err)
		}
	}
	jsonWrite(w, fallbackExecutorKeyResponse(now, stored, changed))
}

func fallbackExecutorKeyResponse(now time.Time, rec store.FallbackExecutorKeyRecord, changed bool) v1.FallbackExecutorKeyResponse {
	return v1.FallbackExecutorKeyResponse{
		ServerTime: formatTime(now), FPPInstanceUUID: rec.FPPInstanceUUID,
		PublicKey: rec.PublicKeyB64, RegisteredAt: formatTime(rec.RegisteredAt), Changed: changed,
	}
}
