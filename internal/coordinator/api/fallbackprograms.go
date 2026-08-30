package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/fallbackprogram"
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
		ackStatus, ackPackage, ackAt, ackErr := h.resolveFallbackProgramAckFields(ctx, instanceUUID, "")
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

	var signed fallbackprogram.SignedProgram
	if err := json.Unmarshal([]byte(rec.ProgramJSON), &signed); err != nil {
		h.writeInternalError(w, now, "decode stored fallback program", err)
		return
	}

	ackStatus, ackPackage, ackAt, err := h.resolveFallbackProgramAckFields(ctx, instanceUUID, rec.PackageID)
	if err != nil {
		h.writeInternalError(w, now, "get fallback program acknowledgement", err)
		return
	}

	jsonWrite(w, v1.FallbackProgramResponse{
		ServerTime: formatTime(now), FPPInstanceUUID: instanceUUID, Published: true,
		Program:            mapFallbackProgramBody(signed),
		AcknowledgedStatus: ackStatus, AcknowledgedPackage: ackPackage, AcknowledgedAt: ackAt,
	})
}

// resolveFallbackProgramAckFields turns the stored acknowledgement (if
// any) into FallbackProgramResponse's three-way verdict, on
// [(*handlers).resolveCueCatalogAcknowledgedFields]'s identical shape
// next door: AcknowledgedPackage/AcknowledgedAt are both nil exactly when
// the status is [v1.FallbackProgramStatusNeverAcknowledged].
// currentPackageID empty (no program published, or the caller has not
// resolved one yet) means there is no "current" for any acknowledgement
// to match, so the status is never Current in that case.
func (h *handlers) resolveFallbackProgramAckFields(ctx context.Context, instanceUUID, currentPackageID string) (status string, pkg, ackAt *string, err error) {
	ack, err := h.deps.FallbackPrograms.GetFallbackProgramAck(ctx, instanceUUID)
	if errors.Is(err, store.ErrFallbackProgramAckNotFound) {
		return v1.FallbackProgramStatusNeverAcknowledged, nil, nil, nil
	}
	if err != nil {
		return "", nil, nil, err
	}
	status = v1.FallbackProgramStatusStale
	if currentPackageID != "" && ack.PackageID == currentPackageID {
		status = v1.FallbackProgramStatusCurrent
	}
	p := ack.PackageID
	at := formatTime(ack.AcknowledgedAt)
	return status, &p, &at, nil
}

func mapFallbackProgramBody(signed fallbackprogram.SignedProgram) *v1.FallbackProgramBody {
	p := signed.Program
	entries := make([]v1.FallbackProgramEntry, 0, len(p.Entries))
	for _, e := range p.Entries {
		targets := make([]v1.FallbackProgramTarget, 0, len(e.Targets))
		for _, t := range e.Targets {
			target := v1.FallbackProgramTarget{NodeID: t.NodeID}
			if t.Render != nil {
				target.Render = &v1.FallbackProgramRenderActivation{
					Sequence: t.Render.Sequence, Filename: t.Render.Filename, AssetHashes: emptyIfNil(t.Render.AssetHashes),
				}
			}
			if t.Audio != nil {
				target.Audio = &v1.FallbackProgramAudioActivation{
					Asset: t.Audio.Asset, Filename: t.Audio.Filename, StartOffsetMillis: t.Audio.StartOffsetMillis,
					AssetHashes: emptyIfNil(t.Audio.AssetHashes), LTCStartOffsetMillis: t.Audio.LTCStartOffsetMillis,
				}
			}
			targets = append(targets, target)
		}
		entries = append(entries, v1.FallbackProgramEntry{
			EntryKey: e.EntryKey, CueID: e.CueID, CueRevision: e.CueRevision, Targets: targets,
		})
	}
	return &v1.FallbackProgramBody{
		SchemaVersion: p.SchemaVersion, PackageID: p.PackageID, Revision: p.Revision,
		ExpiresAt: formatTime(p.ExpiresAt), CompiledAt: formatTime(p.CompiledAt),
		FPPInstanceUUID: p.FPPInstanceUUID, Show: p.Show, Generation: p.Generation,
		PlaylistRevisions: p.PlaylistRevisions, CatalogRevisions: p.CatalogRevisions,
		Entries: entries,
		Rules: v1.FallbackProgramRules{
			FallbackBoundary: p.Rules.FallbackBoundary, RestHold: p.Rules.RestHold,
			LocalShutdown: p.Rules.LocalShutdown, RecoveryBoundary: p.Rules.RecoveryBoundary,
		},
		SignatureBase64: base64.StdEncoding.EncodeToString(signed.Signature),
	}
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
