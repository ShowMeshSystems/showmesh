package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/assetstore"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is Track E seam E3/E4's own surface: upload, listing, and
// content retrieval for the asset store (ADR-028). It follows
// resolumecomposition.go's precedent for the multipart mechanics
// (http.MaxBytesReader, one file part named "file", a second refuses, a
// fileless "file" part refuses) and showconfig.go's precedent for the
// identity.Service.AuditedWrite one-transaction write. Bytes never live in
// SQLite (ADR-028 decision 4): assetstore.Backend addresses those, this
// file addresses metadata only.
//
// The manifest ("what should a node hold") is a different seam and is not
// built here — see TRACK-E-SESSION-SPEC.md section 4. This file answers
// three narrower questions only: register an asset, list/get its metadata,
// and serve its bytes.

// scopeAssetWrite exists only so api.go's route registration can take its
// address, mirroring scopeConfigWrite's identical reason (config.go):
// [handlers.writeGuard] takes *identity.Scope, and identity.ScopeAssetWrite
// is a typed string CONSTANT, whose address Go does not allow taking
// directly.
var scopeAssetWrite = identity.ScopeAssetWrite

// assetBackendVolume is every asset row's stored "backend" value today —
// see schemaV8's own comment on the column. ADR-028 decision 4 ships only
// the volume directory backend (assetstore.VolumeBackend); a future
// pluggable backend gets its own value when it exists, not before.
const assetBackendVolume = "volume"

// assetUploadFieldOverheadBytes bounds how much of POST /assets' body may
// be form fields rather than the file part, on top of
// [Dependencies.AssetMaxUploadBytes]. Generous headroom for five short
// string fields and multipart boundary framing — not a real per-field
// limit, only a sanity bound so a caller cannot inflate the whole request
// past its own file-size bound by sending an unbounded field value.
const assetUploadFieldOverheadBytes = 64 * 1024

// assetUploadFieldValueLimit bounds a single form field's own value —
// every field this endpoint reads is a short id or enum value, never a
// multi-kilobyte string.
const assetUploadFieldValueLimit = 4096

// assetUploadTooLargeNoun is this route's own noun argument to
// [payloadTooLargeProblem] (resolumecomposition.go): naming what was
// uploaded, never the fixed Resolume composition bound that constructor
// used to report unconditionally regardless of which route called it.
const assetUploadTooLargeNoun = "an uploaded asset"

// --- POST /assets ---

// assetUploadFields is what readAssetUploadFilePart collects from the
// multipart parts that arrive before the "file" part.
type assetUploadFields struct {
	show       string
	sequence   string
	mediaType  string
	targetKind string
	target     string
}

// assetTargetRequiredProblem is TRACK-E-SESSION-SPEC.md section 3.3's own
// problem type (minted by the orchestrator, section 0): targetKind="node"
// with no target. ADR-030: target selection is mandatory because the
// target is part of the asset's identity, and a defaulted target produces
// a confidently mislabelled artifact.
func assetTargetRequiredProblem() v1.Problem {
	return v1.Problem{
		Type:   problemBaseURI + "asset-target-required",
		Title:  "Asset target required",
		Status: http.StatusBadRequest,
		Detail: `targetKind is "node" but target is empty; a node-targeted asset must name the declared node it belongs to`,
	}
}

// assetStorageFullProblem is TRACK-E-SESSION-SPEC.md section 3.3's other
// minted problem type: assetstore.ErrNoSpace. Nothing was registered —
// this is reported before identity.Service.AuditedWrite is ever called.
func assetStorageFullProblem() v1.Problem {
	return v1.Problem{
		Type:   problemBaseURI + "storage-full",
		Title:  "Storage full",
		Status: http.StatusInsufficientStorage,
		Detail: "this coordinator's asset storage is full; the upload was discarded and nothing was registered",
	}
}

// readAssetUploadFilePart reads r's multipart parts up to and including
// the "file" part, collecting every field part that arrived before it into
// fields. It does NOT read the file part's own body — the caller streams
// that directly into assetstore.Backend.Put, never buffering it here (an
// FSEQ is not a 407 KB XML file).
//
// Mirrors readResolumeCompositionFilePart's rules (resolumecomposition.go)
// exactly for the file part itself: a second "file" part refuses, and a
// "file" part with no filename (a plain form field, not an uploaded file)
// refuses. It ADDS one rule that file has no analogue for: the file part
// must not be the very first part in the body — form fields must precede
// it, so every field is already known before this function ever streams a
// byte to the backend.
//
// It also returns the live *multipart.Reader it read
// from, positioned immediately after the file part's own boundary — the
// caller uses it, after streaming the file part's body, to check for a
// stray SECOND "file" part. http.Request.MultipartReader() may only be
// called ONCE per request ("http: MultipartReader called twice" is a
// hard error), so this reader — not a fresh call to r.MultipartReader() —
// is the only way to keep reading this body's remaining parts.
func readAssetUploadFilePart(r *http.Request) (fields assetUploadFields, filePart *multipart.Part, mr *multipart.Reader, err error) {
	mr, err = r.MultipartReader()
	if err != nil {
		return assetUploadFields{}, nil, nil, fmt.Errorf(`request must be multipart/form-data with one file part named "file": %w`, err)
	}

	partIndex := 0
	for {
		part, perr := mr.NextPart()
		if errors.Is(perr, io.EOF) {
			break
		}
		if perr != nil {
			return assetUploadFields{}, nil, nil, perr
		}

		if part.FormName() == "file" {
			if part.FileName() == "" {
				_ = part.Close()
				return assetUploadFields{}, nil, nil, errors.New(`the "file" part must be an uploaded file with a filename, not a plain form field`)
			}
			if partIndex == 0 {
				_ = part.Close()
				return assetUploadFields{}, nil, nil, errors.New(
					`the "file" part arrived first; send the show, sequence, mediaType, targetKind, and target ` +
						`form fields before the file part`)
			}
			return fields, part, mr, nil
		}

		val, rerr := io.ReadAll(io.LimitReader(part, assetUploadFieldValueLimit+1))
		_ = part.Close()
		if rerr != nil {
			return assetUploadFields{}, nil, nil, rerr
		}
		if len(val) > assetUploadFieldValueLimit {
			return assetUploadFields{}, nil, nil, fmt.Errorf("form field %q exceeds %d bytes", part.FormName(), assetUploadFieldValueLimit)
		}
		switch part.FormName() {
		case "show":
			fields.show = string(val)
		case "sequence":
			fields.sequence = string(val)
		case "mediaType":
			fields.mediaType = string(val)
		case "targetKind":
			fields.targetKind = string(val)
		case "target":
			fields.target = string(val)
		}
		partIndex++
	}

	return assetUploadFields{}, nil, nil, errors.New(`request must include one file part named "file"`)
}

// validateAssetUploadFields is POST /assets' own field validation, run
// BEFORE any byte of the file part is streamed to the backend. show must
// name an existing "show" config object; sequence uses
// config.ValidateShowObjectID's slug rule (TRACK-E-SESSION-SPEC.md section
// 3.3); mediaType is one of fseq/audio/media; targetKind is required with
// NO default, and targetKind="node" requires a non-empty target naming a
// declared node (ADR-030: a defaulted target produces a confidently
// mislabelled artifact).
func (h *handlers) validateAssetUploadFields(ctx context.Context, f assetUploadFields) *v1.Problem {
	if f.show == "" {
		p := invalidParameterProblem(`"show" is required`)
		return &p
	}
	if !h.showExists(ctx)(f.show) {
		p := invalidParameterProblem(fmt.Sprintf("show %q does not name an existing show", f.show))
		return &p
	}

	if f.sequence == "" {
		p := invalidParameterProblem(`"sequence" is required`)
		return &p
	}
	if verr := config.ValidateShowObjectID("sequence", f.sequence); verr != nil {
		p := mapValidationError(verr)
		return &p
	}

	switch f.mediaType {
	case "fseq", "audio", "media":
	case "":
		p := invalidParameterProblem(`"mediaType" is required and must be one of "fseq", "audio", "media"`)
		return &p
	default:
		p := invalidParameterProblem(fmt.Sprintf(`"mediaType" %q is not one of "fseq", "audio", "media"`, f.mediaType))
		return &p
	}

	switch f.targetKind {
	case store.AssetTargetKindNode:
		if f.target == "" {
			p := assetTargetRequiredProblem()
			return &p
		}
		if !h.nodeDeclared(ctx)(f.target) {
			p := invalidParameterProblem(fmt.Sprintf("target %q does not name a declared node", f.target))
			return &p
		}
	case store.AssetTargetKindShow:
		if f.target != "" {
			p := invalidParameterProblem(`"target" must be empty when targetKind is "show"`)
			return &p
		}
	case "":
		p := invalidParameterProblem(`"targetKind" is required and must be "node" or "show"`)
		return &p
	default:
		p := invalidParameterProblem(fmt.Sprintf(`"targetKind" %q is not "node" or "show"`, f.targetKind))
		return &p
	}

	return nil
}

// handlePostAssetUpload serves POST /api/v1/assets: multipart/form-data
// upload, streamed directly into [Dependencies.AssetBackend] (never
// buffered whole), with its metadata row and audit entry written in one
// transaction ONLY after the bytes are whole and hashed (ADR-030: an
// interrupted upload registers nothing).
func (h *handlers) handlePostAssetUpload(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ac := authFromContext(r.Context())

	maxUpload := h.deps.AssetSettings.MaxUploadBytes()

	// httpapi.NewServer sets ReadTimeout AND WriteTimeout to 10s, and both
	// bound this handler. Both are extended here, sized from the same
	// budget showmeshctl derives its own client timeout from.
	//
	// The write half is the non-obvious one and was a shipped defect. Go
	// arms the write deadline inside conn.readRequest, at roughly the
	// moment the request HEADERS were read, so it expires while a large
	// body is still arriving. Extending only the read deadline gives an
	// upload that is staged, hashed, registered and audited, and then
	// fails on the response flush: the operator is told a transport error
	// for a request that fully succeeded, and the retry reproduces it.
	//
	// The errors are ignored, matching stream.go's resetWriteDeadline: a
	// ResponseWriter with no deadline support has none to extend.
	uploadDeadline := time.Now().Add(assetstore.UploadBudget(maxUpload))
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(uploadDeadline)
	_ = rc.SetWriteDeadline(uploadDeadline)

	// Bounds the ENTIRE request body — form fields plus the file part —
	// before any multipart parsing begins, matching
	// maxResolumeCompositionUploadBytes' identical reasoning one file
	// over: a hostile or accidental body is never buffered past this
	// bound, let alone whole.
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload+assetUploadFieldOverheadBytes)

	fields, filePart, mr, err := readAssetUploadFilePart(r)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeProblem(w, h.logger, now, payloadTooLargeProblem(maxUpload, assetUploadTooLargeNoun, ""))
			return
		}
		writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
		return
	}

	if p := h.validateAssetUploadFields(r.Context(), fields); p != nil {
		_ = filePart.Close()
		writeProblem(w, h.logger, now, *p)
		return
	}

	runtimeFilename := filePart.FileName()
	blob, err := h.deps.AssetBackend.Put(r.Context(), filePart, maxUpload)
	_ = filePart.Close()
	switch {
	case errors.Is(err, assetstore.ErrNoSpace):
		writeProblem(w, h.logger, now, assetStorageFullProblem())
		return
	case errors.Is(err, assetstore.ErrTooLarge):
		writeProblem(w, h.logger, now, payloadTooLargeProblem(maxUpload, assetUploadTooLargeNoun, ""))
		return
	case err != nil:
		h.writeInternalError(w, now, "stage asset upload", err)
		return
	}

	// A second "file" part, if one follows, is refused — mirroring
	// readResolumeCompositionFilePart's identical rule. The first part's
	// bytes are already staged as a content-addressed blob at this point;
	// an unregistered, orphaned blob is acceptable (ADR-030: retention's
	// problem, explicitly out of scope), an orphaned ASSET ROW is not, and
	// this refusal runs before any row is ever created. Continuing to read
	// mr (rather than calling r.MultipartReader() again, which errors —
	// see that function's own doc comment) is what lets this see any part
	// still left in the body.
	if p, perr := mr.NextPart(); perr == nil {
		_ = p.Close()
		writeProblem(w, h.logger, now, invalidParameterProblem(`request must include exactly one file part named "file"; a second part was found`))
		return
	}

	var created store.AssetRecord
	var rolledBack bool
	var writeNow time.Time
	writeErr := h.deps.Identity.AuditedWrite(r.Context(), func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		// Timestamped here, not at request start (:246's now): that read
		// precedes the whole upload stream, so an audit entry stamped from
		// it can predate the write it records by the upload's entire
		// duration and misorder concurrent transitions.
		writeNow = h.now()

		// Read before CreateAsset runs: a rollback supersedes whatever this
		// returns, and that row is unrecoverable from the tuple alone once
		// superseded.
		prevCurrent, prevErr := tx.GetCurrentAssetForTuple(ctx, fields.show, fields.sequence, fields.targetKind, fields.target, fields.mediaType)
		if prevErr != nil && !errors.Is(prevErr, store.ErrAssetNotFound) {
			return identity.AuditEntry{}, prevErr
		}

		rec, rb, cerr := tx.CreateAsset(ctx, store.AssetRecord{
			ID:                     uuid.NewString(),
			ShowID:                 fields.show,
			SequenceID:             fields.sequence,
			TargetKind:             fields.targetKind,
			TargetID:               fields.target,
			MediaType:              fields.mediaType,
			ContentHash:            blob.ContentHash,
			RuntimeFilename:        runtimeFilename,
			SizeBytes:              blob.SizeBytes,
			Backend:                assetBackendVolume,
			StorageKey:             blob.ContentHash,
			CreatedByPrincipalID:   ac.result.Principal.ID,
			CreatedByPrincipalName: ac.result.Principal.Name,
		})
		if cerr != nil {
			return identity.AuditEntry{}, cerr
		}
		created = rec
		rolledBack = rb

		// A rollback (ADR-028 decision 10) gets its own audit Action.
		action := "asset.upload"
		params := map[string]any{
			"show": fields.show, "sequence": fields.sequence,
			"targetKind": fields.targetKind, "target": fields.target,
			"mediaType": fields.mediaType, "contentHash": blob.ContentHash,
			"sizeBytes": blob.SizeBytes, "runtimeFilename": runtimeFilename,
			"rolledBack": rb,
		}
		if rb {
			action = "asset.rollback"
			// The transition this entry records: what stopped being
			// current and what replaced it (review blocker 1).
			params["fromAssetId"] = prevCurrent.ID
			params["toAssetId"] = rec.ID
		}
		return identity.AuditEntry{
			Timestamp: writeNow, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
			Action: action, Target: rec.ID,
			Params: params,
			Kind:   identity.AuditAdmin,
		}, nil
	})

	var existsErr *store.AssetIdentityExistsError
	switch {
	case errors.As(writeErr, &existsErr):
		// Still-current identity match: idempotent no-op, no audit entry.
		jsonWrite(w, mapAssetResponse(writeNow, existsErr.Existing, false))
		return
	case writeErr != nil:
		h.writeInternalError(w, now, "write asset", writeErr)
		return
	}

	// ADR-028 decision 7: sync runs on upload and on a timer, never at
	// showtime. A rollback changes what the manifest expects exactly like a
	// fresh upload does, so it nudges the same way.
	h.deps.AssetSyncNudger.Nudge()

	jsonWrite(w, mapAssetResponse(writeNow, created, rolledBack))
}

// --- GET /assets, GET /assets/{id} ---

func (h *handlers) handleListAssets(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	q := r.URL.Query()
	// NodeID filters to node-TARGETED assets only, never show-wide ones —
	// see [store.AssetFilter.NodeID]'s own doc comment. The manifest (a
	// different seam) is the surface that answers "what should this node
	// hold" by combining both; this is one question, not the other.
	filter := store.AssetFilter{ShowID: q.Get("show"), SequenceID: q.Get("sequence"), NodeID: q.Get("node")}

	recs, err := h.deps.Assets.ListAssets(r.Context(), filter)
	if err != nil {
		h.writeInternalError(w, now, "list assets", err)
		return
	}
	out := make([]v1.Asset, 0, len(recs))
	for _, rec := range recs {
		out = append(out, mapAsset(rec))
	}
	jsonWrite(w, v1.AssetsListResponse{ServerTime: formatTime(now), Assets: out})
}

func assetNotFoundProblem(id string) v1.Problem {
	return resourceNotFoundProblem(fmt.Sprintf("no asset with id %q exists", id))
}

func (h *handlers) handleGetAsset(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")
	rec, err := h.deps.Assets.GetAsset(r.Context(), id)
	if errors.Is(err, store.ErrAssetNotFound) {
		writeProblem(w, h.logger, now, assetNotFoundProblem(id))
		return
	}
	if err != nil {
		h.writeInternalError(w, now, "get asset", err)
		return
	}
	jsonWrite(w, mapAssetResponse(now, rec, false))
}

// --- GET /assets/{id}/content ---

// handleGetAssetContent serves GET /api/v1/assets/{id}/content's bytes via
// http.ServeContent (Range support, so an interrupted agent transfer can
// resume). Before serving, the on-disk size assetstore.Backend.Open
// reports is compared against the row's own SizeBytes: a mismatch fails
// loudly with a 500 naming the asset rather than serving a truncated body
// (acceptance criterion 4 — a corrupted or truncated asset is reported,
// never served). Backend.Open deliberately takes no expected-size
// parameter (see that method's own doc comment), so this comparison is
// this handler's job.
func (h *handlers) handleGetAssetContent(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")

	rec, err := h.deps.Assets.GetAsset(r.Context(), id)
	if errors.Is(err, store.ErrAssetNotFound) {
		writeProblem(w, h.logger, now, assetNotFoundProblem(id))
		return
	}
	if err != nil {
		h.writeInternalError(w, now, "get asset for content", err)
		return
	}

	// httpapi.NewServer's WriteTimeout (10s) is armed when the request
	// headers are read and bounds the whole response, not just its first
	// byte — see handlePostAssetUpload's identical extension for the
	// upload side of this same contract. Without this, any transfer past
	// this project's own 1 MiB/s floor (assetstore.MinTransferBytesPerSecond)
	// fails mid-body with a dropped connection after the asset was found
	// and opened successfully.
	writeDeadline := time.Now().Add(assetstore.UploadBudget(rec.SizeBytes))
	_ = http.NewResponseController(w).SetWriteDeadline(writeDeadline)

	rc, size, err := h.deps.AssetBackend.Open(r.Context(), rec.StorageKey)
	if err != nil {
		h.writeInternalError(w, now, fmt.Sprintf("open stored asset %q", id), err)
		return
	}
	defer func() { _ = rc.Close() }()

	if size != rec.SizeBytes {
		h.writeInternalError(w, now, fmt.Sprintf("serve asset %q", id),
			fmt.Errorf("stored blob is %d bytes but the recorded size is %d bytes: refusing to serve a truncated or corrupted asset", size, rec.SizeBytes))
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("ETag", strconv.Quote(rec.ContentHash))
	http.ServeContent(w, r, rec.RuntimeFilename, rec.CreatedAt, rc)
}

// --- mapping: store.AssetRecord -> v1 wire types ---

func mapAsset(rec store.AssetRecord) v1.Asset {
	return v1.Asset{
		ID: rec.ID, Show: rec.ShowID, Sequence: rec.SequenceID,
		TargetKind: rec.TargetKind, Target: rec.TargetID,
		MediaType: rec.MediaType, ContentHash: rec.ContentHash,
		RuntimeFilename: rec.RuntimeFilename, SizeBytes: rec.SizeBytes,
		CreatedAt:              formatTime(rec.CreatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rec.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rec.CreatedByPrincipalName),
		SupersededAt:           formatTimePtr(rec.SupersededAt),
		Current:                rec.SupersededAt == nil,
	}
}

func mapAssetResponse(now time.Time, rec store.AssetRecord, rolledBack bool) v1.AssetResponse {
	return v1.AssetResponse{ServerTime: formatTime(now), Asset: mapAsset(rec), RolledBack: rolledBack}
}
