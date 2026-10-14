package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

var scopeCompanionReport = identity.ScopeCompanionReport

const (
	auditActionCompanionReportUpload = "companion.report.upload"

	// maxCompanionReportContentBytes bounds one report's content. A macOS
	// crash report is tens to hundreds of kilobytes.
	maxCompanionReportContentBytes = 4 << 20
	// maxCompanionReportRequestBodyBytes leaves room for JSON escaping.
	maxCompanionReportRequestBodyBytes = 6 << 20

	defaultCompanionReportListLimit = 50
	maxCompanionReportListLimit     = 500

	companionReportKindCrash = "crash_report"
)

var (
	companionIDPattern     = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	companionSourcePattern = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
	sha256HexPattern       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func companionReportTooLargeProblem() v1.Problem {
	return payloadTooLargeProblem(maxCompanionReportContentBytes, "a companion report", "")
}

func decodeCompanionReportUpload(r *http.Request) (v1.CompanionReportUploadRequest, time.Time, *v1.Problem) {
	fail := func(p v1.Problem) (v1.CompanionReportUploadRequest, time.Time, *v1.Problem) {
		return v1.CompanionReportUploadRequest{}, time.Time{}, &p
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxCompanionReportRequestBodyBytes+1))
	if err != nil {
		return fail(invalidParameterProblem("The request body could not be read. Send the report again."))
	}
	if len(body) > maxCompanionReportRequestBodyBytes {
		return fail(companionReportTooLargeProblem())
	}
	var req v1.CompanionReportUploadRequest
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return fail(invalidParameterProblem("The report could not be read as JSON. Check the companion version and send it again."))
	}
	switch {
	case !companionIDPattern.MatchString(req.CompanionID):
		return fail(invalidParameterProblem("The companion name must be 1 to 64 letters, digits, dots, dashes or underscores. Rename the companion and send again."))
	case req.Kind != companionReportKindCrash:
		return fail(invalidParameterProblem("This kind of report is not accepted. Only crash reports can be sent."))
	case !companionSourcePattern.MatchString(req.Source):
		return fail(invalidParameterProblem("The report source must be 1 to 64 lowercase letters, digits or dashes."))
	case req.FileName == "" || len(req.FileName) > 255 || strings.ContainsAny(req.FileName, `/\`):
		return fail(invalidParameterProblem("The file name must be 1 to 255 characters with no folder in it."))
	case !sha256HexPattern.MatchString(req.ContentSHA256):
		return fail(invalidParameterProblem("The content hash must be 64 lowercase hex characters."))
	}
	capturedAt, err := time.Parse(time.RFC3339Nano, req.CapturedAt)
	if err != nil {
		return fail(invalidParameterProblem("The capture time is not a valid timestamp."))
	}
	if len(req.Content) > maxCompanionReportContentBytes {
		return fail(companionReportTooLargeProblem())
	}
	sum := sha256.Sum256([]byte(req.Content))
	if hex.EncodeToString(sum[:]) != req.ContentSHA256 {
		return fail(invalidParameterProblem("The content does not match contentSha256. Read the file again and resend it."))
	}
	return req, capturedAt.UTC(), nil
}

func companionReportSummary(rec store.CompanionReportRecord) v1.CompanionReportSummary {
	return v1.CompanionReportSummary{
		ID: rec.ID, CompanionID: rec.CompanionID, Kind: rec.Kind, Source: rec.Source, FileName: rec.FileName,
		CapturedAt: formatTime(rec.CapturedAt), ReceivedAt: formatTime(rec.ReceivedAt),
		SizeBytes: rec.SizeBytes, ContentSHA256: rec.ContentSHA256,
	}
}

var errCompanionReportsNotWired = errors.New("companion report storage is not wired")

// --- POST /companion-reports ---

func (h *handlers) handlePostCompanionReport(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)

	req, capturedAt, problem := decodeCompanionReportUpload(r)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	if h.deps.CompanionReports == nil {
		h.writeInternalError(w, now, "store companion report", errCompanionReportsNotWired)
		return
	}

	commandID := uuid.NewString()
	params := map[string]any{"companionId": req.CompanionID, "kind": req.Kind, "source": req.Source,
		"fileName": req.FileName, "contentSha256": req.ContentSHA256, "sizeBytes": len(req.Content)}
	audit := func(kind identity.AuditKind, at time.Time, reason string) identity.AuditEntry {
		return identity.AuditEntry{
			Timestamp: at, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
			Action: auditActionCompanionReportUpload, Target: req.CompanionID,
			Kind: kind, CommandID: commandID, Params: params, OutcomeReason: reason,
		}
	}
	if !h.writeAuditOrFail(ctx, w, now, audit(identity.AuditDispatch, now, "")) {
		return
	}

	stored, created, err := h.deps.CompanionReports.PutCompanionReport(ctx, store.CompanionReportRecord{
		ID: uuid.NewString(), CompanionID: req.CompanionID, Kind: req.Kind, Source: req.Source,
		FileName: req.FileName, CapturedAt: capturedAt, ReceivedAt: now,
		ContentSHA256: req.ContentSHA256, SizeBytes: int64(len(req.Content)), Content: req.Content,
		PrincipalID: ac.result.Principal.ID,
	})
	if err != nil {
		h.writeInternalError(w, now, "store companion report", err)
		return
	}

	reason := "stored"
	status := http.StatusCreated
	if !created {
		reason = "already stored"
		status = http.StatusOK
	}
	if h.deps.Identity != nil {
		if err := h.deps.Identity.WriteAudit(ctx, audit(identity.AuditOutcome, h.now(), reason)); err != nil {
			h.logWarn("companion report outcome audit write failed", "companion", req.CompanionID, "error", err)
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	jsonEncode(w, v1.CompanionReportUploadResponse{
		ServerTime: formatTime(now), Created: created, Report: companionReportSummary(stored),
	})
}

// --- GET /companion-reports ---

func (h *handlers) handleListCompanionReports(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	query := r.URL.Query()
	companionID := query.Get("companionId")
	if companionID != "" && !companionIDPattern.MatchString(companionID) {
		writeProblem(w, h.logger, now, invalidParameterProblem("The companion name must be 1 to 64 letters, digits, dots, dashes or underscores. Rename the companion and send again."))
		return
	}
	limit := defaultCompanionReportListLimit
	if raw := query.Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 || v > maxCompanionReportListLimit {
			writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf("The limit must be a whole number from 1 to %d.", maxCompanionReportListLimit)))
			return
		}
		limit = v
	}
	resp := v1.CompanionReportListResponse{ServerTime: formatTime(now), Reports: []v1.CompanionReportSummary{}}
	if h.deps.CompanionReports == nil {
		jsonWrite(w, resp)
		return
	}
	recs, err := h.deps.CompanionReports.ListCompanionReports(r.Context(), companionID, limit)
	if err != nil {
		h.writeInternalError(w, now, "list companion reports", err)
		return
	}
	for _, rec := range recs {
		resp.Reports = append(resp.Reports, companionReportSummary(rec))
	}
	jsonWrite(w, resp)
}

// --- GET /companion-reports/{reportId} ---

func (h *handlers) handleGetCompanionReport(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("reportId")
	if h.deps.CompanionReports == nil {
		writeProblem(w, h.logger, now, resourceNotFoundProblem("That companion report was not found. Older reports are removed as new ones arrive."))
		return
	}
	rec, err := h.deps.CompanionReports.GetCompanionReport(r.Context(), id)
	if errors.Is(err, store.ErrCompanionReportNotFound) {
		writeProblem(w, h.logger, now, resourceNotFoundProblem("That companion report was not found. Older reports are removed as new ones arrive."))
		return
	}
	if err != nil {
		h.writeInternalError(w, now, "get companion report", err)
		return
	}
	jsonWrite(w, v1.CompanionReportResponse{ServerTime: formatTime(now), Report: companionReportSummary(rec), Content: rec.Content})
}
