package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is Track J's J1 own HTTP surface (ADR-048,
// IDENTIFIER-REGISTER.md's reservation of every path under
// /api/v1/fallback-programs for this build item): a listing, a
// per-FPP-host current-program read, and an acknowledgement write.
// Publication itself (compiling, signing, and storing a new program) is
// internal/coordinator/fallbackreconcile's own background loop, never
// this file: these handlers only ever read what that loop already wrote,
// and write the acknowledgement a host reports about itself, matching
// cuecatalog.go's identical "this file adds no second resolution rule"
// posture next door.

// auditActionFallbackProgramAcknowledge is IDENTIFIER-REGISTER.md's own
// reservation for this route's audit action string.
const auditActionFallbackProgramAcknowledge = "fallback.program.acknowledge"

// scopeFPPFallback exists only so api.go's route registration can take
// its address, mirroring [scopeNodeObserve]'s identical reason
// (cuecatalog.go): [handlers.writeGuard] takes *identity.Scope, and
// identity.ScopeFPPFallback is a typed string CONSTANT, whose address Go
// does not allow taking directly.
var scopeFPPFallback = identity.ScopeFPPFallback

// maxFallbackProgramAcknowledgeRequestBodyBytes bounds the acknowledgement
// request body: it carries only a package id, a revision, a
// verification result, and a timestamp, so this is generous headroom,
// matching maxCueCatalogAcknowledgeRequestBodyBytes's identical sizing
// next door.
const maxFallbackProgramAcknowledgeRequestBodyBytes = 16 * 1024

// errFallbackProgramStoreNotWired is [handlers.writeInternalError]'s
// cause for a request received while [Dependencies.FallbackPrograms] is
// nil, matching [errCueCatalogStoreNotWired]'s identical posture.
var errFallbackProgramStoreNotWired = errors.New("no fallback program data source (FallbackPrograms) is wired into this API's Dependencies")

// --- GET /fallback-programs ---

func (h *handlers) handleListFallbackPrograms(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	if h.deps.FallbackPrograms == nil {
		jsonWrite(w, v1.FallbackProgramListResponse{ServerTime: formatTime(now), Programs: []v1.FallbackProgramListEntry{}})
		return
	}

	recs, err := h.deps.FallbackPrograms.ListFallbackPrograms(ctx)
	if err != nil {
		h.writeInternalError(w, now, "list fallback programs", err)
		return
	}
	entries := make([]v1.FallbackProgramListEntry, 0, len(recs))
	for _, rec := range recs {
		entries = append(entries, v1.FallbackProgramListEntry{
			FPPInstanceUUID: rec.FPPInstanceUUID, PackageID: rec.PackageID, Revision: rec.Revision,
			Show: rec.ShowID, Generation: rec.Generation,
			ExpiresAt: formatTime(rec.ExpiresAt), CompiledAt: formatTime(rec.CompiledAt),
		})
	}
	jsonWrite(w, v1.FallbackProgramListResponse{ServerTime: formatTime(now), Programs: entries})
}

// --- GET /fallback-programs/{fppInstanceId} ---

func (h *handlers) handleGetFallbackProgram(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	instanceUUID := r.PathValue("fppInstanceId")
	if instanceUUID == "" {
		writeProblem(w, h.logger, now, invalidParameterProblem("fppInstanceId is required"))
		return
	}

	if h.deps.FallbackPrograms == nil {
		jsonWrite(w, v1.FallbackProgramResponse{
			ServerTime: formatTime(now), FPPInstanceUUID: instanceUUID, Published: false,
			AcknowledgedStatus: v1.FallbackProgramStatusNeverAcknowledged,
		})
		return
	}

	rec, err := h.deps.FallbackPrograms.GetFallbackProgram(ctx, instanceUUID)
	if errors.Is(err, store.ErrFallbackProgramNotFound) {
		ackStatus, ackPackage, ackAt, ackErr := h.resolveFallbackProgramAckFields(ctx, instanceUUID, "", "")
		if ackErr != nil {
			h.writeInternalError(w, now, "get fallback program acknowledgement", ackErr)
			return
		}
		jsonWrite(w, v1.FallbackProgramResponse{
			ServerTime: formatTime(now), FPPInstanceUUID: instanceUUID, Published: false,
			AcknowledgedStatus: ackStatus, AcknowledgedPackage: ackPackage, AcknowledgedAt: ackAt,
		})
		return
	}
	if err != nil {
		h.writeInternalError(w, now, "get fallback program", err)
		return
	}

	programBytes, err := extractStoredProgramBytes(rec.ProgramJSON)
	if err != nil {
		h.writeInternalError(w, now, "extract stored fallback program bytes", err)
		return
	}

	ackStatus, ackPackage, ackAt, err := h.resolveFallbackProgramAckFields(ctx, instanceUUID, rec.PackageID, rec.Revision)
	if err != nil {
		h.writeInternalError(w, now, "get fallback program acknowledgement", err)
		return
	}

	jsonWrite(w, v1.FallbackProgramResponse{
		ServerTime: formatTime(now), FPPInstanceUUID: instanceUUID, Published: true,
		Program: programBytes, SignatureBase64: rec.SignatureB64,
		AcknowledgedStatus: ackStatus, AcknowledgedPackage: ackPackage, AcknowledgedAt: ackAt,
	})
}

// extractStoredProgramBytes returns the exact "program" sub-object bytes
// out of programJSON (the stored [fallbackprogram.SignedProgram],
// marshaled whole by internal/coordinator/fallbackreconcile) via
// json.RawMessage, which slices the original bytes out verbatim rather
// than decoding into a Go type and re-marshaling one. See
// v1.FallbackProgramBody's own doc comment for why any re-derivation,
// however faithful it looks, risks producing a byte sequence that no
// longer canonicalizes identically to what was actually signed.
func extractStoredProgramBytes(programJSON string) (json.RawMessage, error) {
	var envelope struct {
		Program json.RawMessage `json:"program"`
	}
	if err := json.Unmarshal([]byte(programJSON), &envelope); err != nil {
		return nil, fmt.Errorf("decode stored signed program envelope: %w", err)
	}
	if len(envelope.Program) == 0 {
		return nil, fmt.Errorf("stored signed program has no program field")
	}
	return envelope.Program, nil
}

// resolveFallbackProgramAckFields turns the stored acknowledgement (if
// any) into FallbackProgramResponse's four-way verdict, on
// [(*handlers).resolveCueCatalogAcknowledgedFields]'s identical shape
// next door: AcknowledgedPackage/AcknowledgedAt are both nil exactly when
// the status is [v1.FallbackProgramStatusNeverAcknowledged].
// currentPackageID/currentRevision both empty (no program published, or
// the caller has not resolved one yet) means there is no "current" for
// any acknowledgement to match, so the status is never Current in that
// case. A host reporting anything other than
// [v1.FallbackProgramVerificationVerified] is [v1.FallbackProgramStatusRejected]
// unconditionally, checked BEFORE the package/revision comparison: a
// host that explicitly said it did not trust what it received must never
// read back as current merely because the packageId or revision happens
// to match.
func (h *handlers) resolveFallbackProgramAckFields(ctx context.Context, instanceUUID, currentPackageID, currentRevision string) (status string, pkg, ackAt *string, err error) {
	ack, err := h.deps.FallbackPrograms.GetFallbackProgramAck(ctx, instanceUUID)
	if errors.Is(err, store.ErrFallbackProgramAckNotFound) {
		return v1.FallbackProgramStatusNeverAcknowledged, nil, nil, nil
	}
	if err != nil {
		return "", nil, nil, err
	}
	p := ack.PackageID
	at := formatTime(ack.AcknowledgedAt)
	if ack.VerificationResult != v1.FallbackProgramVerificationVerified {
		return v1.FallbackProgramStatusRejected, &p, &at, nil
	}
	status = v1.FallbackProgramStatusStale
	if currentPackageID != "" && ack.PackageID == currentPackageID && ack.Revision == currentRevision {
		status = v1.FallbackProgramStatusCurrent
	}
	return status, &p, &at, nil
}

// --- POST /fallback-programs/{fppInstanceId}/acknowledge ---

func decodeFallbackProgramAcknowledgeBody(r *http.Request) (v1.FallbackProgramAcknowledgeRequest, *v1.Problem) {
	var req v1.FallbackProgramAcknowledgeRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxFallbackProgramAcknowledgeRequestBodyBytes+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		p := invalidParameterProblem("malformed request body: " + err.Error())
		return v1.FallbackProgramAcknowledgeRequest{}, &p
	}
	if req.PackageID == "" {
		p := invalidParameterProblem("packageId is required")
		return v1.FallbackProgramAcknowledgeRequest{}, &p
	}
	if req.Revision == "" {
		p := invalidParameterProblem("revision is required")
		return v1.FallbackProgramAcknowledgeRequest{}, &p
	}
	switch req.VerificationResult {
	case v1.FallbackProgramVerificationVerified, v1.FallbackProgramVerificationSignatureInvalid, v1.FallbackProgramVerificationMismatchedProgram:
	default:
		p := invalidParameterProblem("verificationResult must be one of \"verified\", \"signature-invalid\", \"mismatched-program\"")
		return v1.FallbackProgramAcknowledgeRequest{}, &p
	}
	if req.InstalledAt == "" {
		p := invalidParameterProblem("installedAt is required")
		return v1.FallbackProgramAcknowledgeRequest{}, &p
	}
	return req, nil
}

func (h *handlers) handlePostFallbackProgramAcknowledge(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)
	instanceUUID := r.PathValue("fppInstanceId")
	if instanceUUID == "" {
		writeProblem(w, h.logger, now, invalidParameterProblem("fppInstanceId is required"))
		return
	}

	req, problem := decodeFallbackProgramAcknowledgeBody(r)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}

	installedAt, perr := parseTime(req.InstalledAt)
	if perr != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("installedAt is not a valid timestamp: "+perr.Error()))
		return
	}

	if h.deps.FallbackPrograms == nil {
		h.writeInternalError(w, now, "acknowledge fallback program", errFallbackProgramStoreNotWired)
		return
	}

	commandID := uuid.NewString()
	if !h.writeAuditOrFail(ctx, w, now, identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: auditActionFallbackProgramAcknowledge, Target: instanceUUID,
		Kind: identity.AuditDispatch, CommandID: commandID,
		Params: map[string]any{"packageId": req.PackageID, "revision": req.Revision, "verificationResult": req.VerificationResult},
	}) {
		return
	}

	if err := h.deps.FallbackPrograms.PutFallbackProgramAck(ctx, store.FallbackProgramAckRecord{
		FPPInstanceUUID: instanceUUID, PackageID: req.PackageID, Revision: req.Revision,
		VerificationResult: req.VerificationResult, InstalledAt: installedAt, AcknowledgedAt: now,
	}); err != nil {
		h.writeInternalError(w, now, "store fallback program acknowledgement", err)
		return
	}

	outcomeNow := h.now()
	outcome := identity.AuditEntry{
		Timestamp: outcomeNow, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: auditActionFallbackProgramAcknowledge, Target: instanceUUID,
		Kind: identity.AuditOutcome, CommandID: commandID,
		Params:        map[string]any{"packageId": req.PackageID, "revision": req.Revision, "verificationResult": req.VerificationResult},
		OutcomeReason: "acknowledged",
	}
	if h.deps.Identity != nil {
		if err := h.deps.Identity.WriteAudit(ctx, outcome); err != nil {
			h.logWarn("fallback program acknowledge outcome audit write failed", "fppInstanceUuid", instanceUUID, "error", err)
		}
	}

	jsonWrite(w, v1.FallbackProgramAcknowledgeResponse{
		ServerTime: formatTime(now), FPPInstanceUUID: instanceUUID, AcknowledgedAt: formatTime(now),
	})
}
