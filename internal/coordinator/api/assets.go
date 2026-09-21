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
	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/audiorendition"
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

	// assetBlobMu (handlers.go) is held read-locked from here through this
	// request's own metadata transaction below, so a concurrent
	// handleDeleteAsset (which excludes every upload with its own write
	// lock) can never count this blob as unreferenced and remove it while
	// this upload's own row is still landing. Two uploads still run
	// concurrently with each other; only a delete excludes them.
	h.assetBlobMu.RLock()
	defer h.assetBlobMu.RUnlock()

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

	// An audio upload is checked against its own actual content, never its
	// filename, before anything is registered (owner ruling 2026-09-18,
	// PCM show audio): the full transcode into a 48kHz/16-bit/stereo WAV
	// rendition runs off this request path (see [Dependencies.
	// AudioRenditionNudger]), but a file this coordinator cannot decode at
	// all is refused synchronously, exactly like every other upload
	// validation in this handler.
	if fields.mediaType == "audio" {
		if perr := h.probeAudioUpload(r.Context(), blob.ContentHash); perr != nil {
			writeProblem(w, h.logger, now, invalidParameterProblem(perr.Error()))
			return
		}
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
		if fields.mediaType == "audio" {
			h.deps.AudioRenditionNudger.Nudge()
		}
		a, aerr := h.mapAssetWithRendition(r.Context(), existsErr.Existing)
		if aerr != nil {
			h.writeInternalError(w, now, "read asset rendition", aerr)
			return
		}
		jsonWrite(w, assetResponse(writeNow, a, false))
		return
	case writeErr != nil:
		h.writeInternalError(w, now, "write asset", writeErr)
		return
	}

	// ADR-028 decision 7: sync runs on upload and on a timer, never at
	// showtime. A rollback changes what the manifest expects exactly like a
	// fresh upload does, so it nudges the same way.
	h.deps.AssetSyncNudger.Nudge()
	if fields.mediaType == "audio" {
		// The transcode itself runs off this request path (owner ruling
		// 2026-09-18): only a wake-up is sent here, never a wait for it.
		h.deps.AudioRenditionNudger.Nudge()
	}

	a, aerr := h.mapAssetWithRendition(r.Context(), created)
	if aerr != nil {
		h.writeInternalError(w, now, "read asset rendition", aerr)
		return
	}
	jsonWrite(w, assetResponse(writeNow, a, rolledBack))
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
		a, aerr := h.mapAssetWithRendition(r.Context(), rec)
		if aerr != nil {
			h.writeInternalError(w, now, "read asset rendition", aerr)
			return
		}
		out = append(out, a)
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
	a, aerr := h.mapAssetWithRendition(r.Context(), rec)
	if aerr != nil {
		h.writeInternalError(w, now, "read asset rendition", aerr)
		return
	}
	jsonWrite(w, assetResponse(now, a, false))
}

// --- DELETE /assets/{id} ---

// errAssetPinned is handleDeleteAsset's own refusal (refuseIfAssetPinned):
// id is named directly by the current show.weatherdelay alert or by a
// current show.action's announcement media, so deleting it (which also
// removes the bytes) would leave that reference playing nothing.
type errAssetPinned struct {
	assetID string
	detail  string
}

func (e *errAssetPinned) Error() string {
	return fmt.Sprintf("asset %q is pinned: %s", e.assetID, e.detail)
}

// ProblemTypeAssetPinned is [assetPinnedProblem]'s own type URI. A
// separate minted type from showconfig.go's config-object-currently-
// active (same 409, confirm-body, "name what references it" shape): an
// asset row is not a configuration object, and conflating the two type
// URIs would let a client's Type-keyed handling silently mix them up.
const ProblemTypeAssetPinned = problemBaseURI + "asset-pinned"

func assetPinnedProblem(e *errAssetPinned) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeAssetPinned,
		Title:  "Asset delete refused: this asset is pinned",
		Status: http.StatusConflict,
		Detail: e.detail,
	}
}

// refuseIfAssetPinned checks whether id is named directly, by id, from
// outside its own identity tuple: the current show.weatherdelay alert's
// delayAssetId/cancelNightAssetId, or any current show.action's
// announcement media (assetsync/announcementfallback.go's own field,
// carried at target.params.media.assetId). Both keep playing that exact
// asset's bytes by id, so deleting it out from under either reference
// would play nothing with no operator-visible reason why.
func refuseIfAssetPinned(ctx context.Context, tx *store.Tx, id string) error {
	if detail, err := assetPinnedByWeatherDelay(ctx, tx, id); err != nil {
		return err
	} else if detail != "" {
		return &errAssetPinned{assetID: id, detail: detail}
	}
	if detail, err := assetPinnedByShowAction(ctx, tx, id); err != nil {
		return err
	} else if detail != "" {
		return &errAssetPinned{assetID: id, detail: detail}
	}
	return nil
}

func assetPinnedByWeatherDelay(ctx context.Context, tx *store.Tx, id string) (string, error) {
	obj, err := tx.GetConfigObject(ctx, config.ShowWeatherDelayConfigKind, config.ShowWeatherDelayConfigObjectID)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if obj.CurrentRevision == 0 {
		return "", nil
	}
	rev, err := tx.GetConfigRevision(ctx, config.ShowWeatherDelayConfigKind, config.ShowWeatherDelayConfigObjectID, obj.CurrentRevision)
	if err != nil {
		return "", err
	}
	var payload config.WeatherDelayPayload
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
		return "", err
	}
	switch id {
	case payload.Alert.DelayAssetID:
		return "This asset is the weather delay alert. Choose a different alert in the weather delay settings, then delete it.", nil
	case payload.Alert.CancelNightAssetID:
		return "This asset is the weather delay cancel alert. Choose a different alert in the weather delay settings, then delete it.", nil
	}
	return "", nil
}

func assetPinnedByShowAction(ctx context.Context, tx *store.Tx, id string) (string, error) {
	objs, err := tx.ListConfigObjects(ctx, config.ShowActionConfigKind)
	if err != nil {
		return "", err
	}
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := tx.GetConfigRevision(ctx, config.ShowActionConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			return "", err
		}
		var payload config.ShowActionPayload
		if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
			return "", err
		}
		media, _ := payload.Target.Params["media"].(map[string]any)
		assetID, _ := media["assetId"].(string)
		if assetID != id {
			continue
		}
		label := payload.Label
		if label == "" {
			label = obj.ID
		}
		return fmt.Sprintf("This asset is the announcement media for %q. Choose different media for that action, then delete it.", label), nil
	}
	return "", nil
}

// handleDeleteAsset serves DELETE /api/v1/assets/{id}: a hard delete of
// one asset row, guarded by asset:write and requiring the same explicit
// {"confirm":true} body every other delete in this package requires
// (decodeConfigDeleteConfirmBody, showconfig.go), so a mis-issued call
// cannot quietly remove a row. Unlike a config object's DELETE, this is
// not a tombstone: assets carry no revision history to preserve, and
// deleting the current row for an identity leaves that identity with no
// current asset (never promoting a superseded row back; that is
// rollback's job, ADR-028 decision 10).
//
// No refuseIfActive-style check applies for an asset row being the live
// "what is running now" selector or a running night session's own pinned
// object, matching upload's own unconditional posture there. Two OTHER
// things do pin an asset by id directly, and refuseIfAssetPinned refuses
// those: the current show.weatherdelay alert, and a current show.action's
// announcement media.
//
// assetBlobMu (handlers.go) is held write-locked for the whole call, from
// before the metadata transaction through the blob and rendition removal
// below: a concurrent upload holds it read-locked from before it stages
// its own blob until its transaction commits, so this delete can never
// count a just-staged blob as unreferenced and remove it while that
// upload's own row is still landing.
func (h *handlers) handleDeleteAsset(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ac := authFromContext(r.Context())
	id := r.PathValue("id")

	if problem := decodeConfigDeleteConfirmBody(r); problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}

	h.assetBlobMu.Lock()
	defer h.assetBlobMu.Unlock()

	var deleted store.AssetRecord
	var blobOrphaned bool
	var rendition store.AudioRenditionRecord
	var renditionFound bool
	var renditionBlobOrphaned bool
	writeErr := h.deps.Identity.AuditedWrite(r.Context(), func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		writeNow := h.now()

		if perr := refuseIfAssetPinned(ctx, tx, id); perr != nil {
			return identity.AuditEntry{}, perr
		}

		rec, derr := tx.DeleteAsset(ctx, id)
		if derr != nil {
			return identity.AuditEntry{}, derr
		}
		deleted = rec

		remaining, cerr := tx.CountAssetsByContentHash(ctx, rec.ContentHash)
		if cerr != nil {
			return identity.AuditEntry{}, cerr
		}
		blobOrphaned = remaining == 0

		// A rendition row is keyed by its ORIGINAL asset's own content
		// hash, one row per hash (schemaV38's primary key), regardless of
		// that asset's own mediaType: whenever the last row for a hash is
		// gone, that hash's own rendition row (if any) is orphaned too,
		// never gated on the deleted row's mediaType. Removed in this same
		// transaction as the asset row, row first: a failure between this
		// and the blob removal below leaves only a harmless orphan blob,
		// never a ready row naming bytes that are already gone.
		if blobOrphaned {
			rend, rerr := tx.GetAudioRendition(ctx, rec.ContentHash)
			switch {
			case errors.Is(rerr, store.ErrAudioRenditionNotFound):
			case rerr != nil:
				return identity.AuditEntry{}, rerr
			default:
				if derr := tx.DeleteAudioRendition(ctx, rec.ContentHash); derr != nil {
					return identity.AuditEntry{}, derr
				}
				rendition = rend
				renditionFound = true
				if rend.ContentHash != "" {
					otherRenditions, cerr := tx.CountAudioRenditionsByContentHash(ctx, rend.ContentHash)
					if cerr != nil {
						return identity.AuditEntry{}, cerr
					}
					otherAssets, cerr := tx.CountAssetsByContentHash(ctx, rend.ContentHash)
					if cerr != nil {
						return identity.AuditEntry{}, cerr
					}
					renditionBlobOrphaned = otherRenditions == 0 && otherAssets == 0
				}
			}
		}

		return identity.AuditEntry{
			Timestamp: writeNow, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
			Action: "asset.delete", Target: rec.ID,
			Params: map[string]any{
				"assetId": rec.ID, "show": rec.ShowID, "sequence": rec.SequenceID,
				"targetKind": rec.TargetKind, "target": rec.TargetID,
				"mediaType": rec.MediaType, "contentHash": rec.ContentHash,
			},
			Kind: identity.AuditAdmin,
		}, nil
	})
	var pinned *errAssetPinned
	switch {
	case errors.As(writeErr, &pinned):
		writeProblem(w, h.logger, now, assetPinnedProblem(pinned))
		return
	case errors.Is(writeErr, store.ErrAssetNotFound):
		writeProblem(w, h.logger, now, assetNotFoundProblem(id))
		return
	case writeErr != nil:
		h.writeInternalError(w, now, "delete asset", writeErr)
		return
	}

	// The manifest and cue readiness read the assets table live; nudging
	// asset sync is what tells a node to stop holding what it fetched for
	// the row that is now gone, exactly mirroring handlePostAssetUpload's
	// own nudge after a write that changes what is current.
	h.deps.AssetSyncNudger.Nudge()

	// Bytes are removed only after their owning row is gone (row, then
	// bytes, for the original asset and for its rendition alike) and only
	// when nothing else references them (ADR-028 decision 4: bytes are
	// metadata-driven, never owned by one row). This runs after commit and
	// is best-effort: a failure here leaves an orphaned blob, the same
	// accepted outcome handlePostAssetUpload's own doc comment already
	// allows for a staged blob whose row was never registered.
	if blobOrphaned {
		if derr := h.deps.AssetBackend.Delete(r.Context(), deleted.ContentHash); derr != nil {
			h.logWarn("failed to remove orphaned asset blob", "assetId", deleted.ID, "contentHash", deleted.ContentHash, "error", derr)
		}
	}
	if renditionFound && renditionBlobOrphaned {
		if derr := h.deps.AssetBackend.Delete(r.Context(), rendition.ContentHash); derr != nil {
			h.logWarn("failed to remove orphaned audio rendition blob", "assetId", deleted.ID, "contentHash", rendition.ContentHash, "error", derr)
		}
	}

	w.WriteHeader(http.StatusNoContent)
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

// --- GET /assets/{id}/rendition/content ---

func assetRenditionNotFoundProblem(id string) v1.Problem {
	return resourceNotFoundProblem(fmt.Sprintf("no ready rendition exists for asset %q", id))
}

// handleGetAssetRenditionContent serves GET /api/v1/assets/{id}/rendition/
// content: the SAME id as GET /api/v1/assets/{id}/content, but the audio
// asset's own separately content-addressed rendition blob, never the
// original upload. A node is only ever dispatched this route once
// [assetsync]'s expected-set computation has substituted a ready
// rendition for this asset, so a 404 here (no rendition row, or one not
// yet ready) means that node's own manifest is stale relative to what the
// coordinator now expects.
func (h *handlers) handleGetAssetRenditionContent(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")

	rec, err := h.deps.Assets.GetAsset(r.Context(), id)
	if errors.Is(err, store.ErrAssetNotFound) {
		writeProblem(w, h.logger, now, assetNotFoundProblem(id))
		return
	}
	if err != nil {
		h.writeInternalError(w, now, "get asset for rendition content", err)
		return
	}

	rend, err := h.deps.Assets.GetAudioRendition(r.Context(), rec.ContentHash)
	switch {
	case errors.Is(err, store.ErrAudioRenditionNotFound):
		writeProblem(w, h.logger, now, assetRenditionNotFoundProblem(id))
		return
	case err != nil:
		h.writeInternalError(w, now, "get asset rendition for content", err)
		return
	case rend.Status != store.AudioRenditionStatusReady:
		writeProblem(w, h.logger, now, assetRenditionNotFoundProblem(id))
		return
	}

	// Same write-deadline extension as handleGetAssetContent, sized from
	// the rendition's own recorded size rather than the original's.
	writeDeadline := time.Now().Add(assetstore.UploadBudget(rend.SizeBytes))
	_ = http.NewResponseController(w).SetWriteDeadline(writeDeadline)

	rc, size, err := h.deps.AssetBackend.Open(r.Context(), rend.ContentHash)
	if err != nil {
		h.writeInternalError(w, now, fmt.Sprintf("open stored rendition for asset %q", id), err)
		return
	}
	defer func() { _ = rc.Close() }()

	if size != rend.SizeBytes {
		h.writeInternalError(w, now, fmt.Sprintf("serve rendition for asset %q", id),
			fmt.Errorf("stored rendition blob is %d bytes but the recorded size is %d bytes: refusing to serve a truncated or corrupted rendition", size, rend.SizeBytes))
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("ETag", strconv.Quote(rend.ContentHash))
	http.ServeContent(w, r, assetsync.RenditionFilename(rec.RuntimeFilename), rec.CreatedAt, rc)
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

// mapAssetWithRendition maps rec and, for a "audio" MediaType, attaches its
// rendition state (owner ruling 2026-09-18): nil when no rendition has
// ever been queued, matching [substituteAudioRenditions]'s identical
// "never built yet" reading in the sync package.
func (h *handlers) mapAssetWithRendition(ctx context.Context, rec store.AssetRecord) (v1.Asset, error) {
	out := mapAsset(rec)
	if rec.MediaType != "audio" {
		return out, nil
	}

	rend, err := h.deps.Assets.GetAudioRendition(ctx, rec.ContentHash)
	if errors.Is(err, store.ErrAudioRenditionNotFound) {
		return out, nil
	}
	if err != nil {
		return v1.Asset{}, err
	}

	r := &v1.AssetRendition{Status: rend.Status}
	switch rend.Status {
	case store.AudioRenditionStatusReady:
		r.Format = rend.Format
		r.DurationMillis = rend.DurationMillis
	case store.AudioRenditionStatusFailed:
		r.FailureReason = rend.FailureReason
	}
	out.Rendition = r
	return out, nil
}

func assetResponse(now time.Time, a v1.Asset, rolledBack bool) v1.AssetResponse {
	return v1.AssetResponse{ServerTime: formatTime(now), Asset: a, RolledBack: rolledBack}
}

// probeAudioUpload runs [audiorendition.DetectFormat] against the just-
// staged blob's own bytes, reading only what identifying its format needs
// (never the whole file, and never a full decode): the fast, synchronous
// half of PCM show audio's two-part check. The full transcode is
// [Dependencies.AudioRenditionNudger]'s job, off this request path.
func (h *handlers) probeAudioUpload(ctx context.Context, contentHash string) error {
	rc, _, err := h.deps.AssetBackend.Open(ctx, contentHash)
	if err != nil {
		return fmt.Errorf("open uploaded audio to detect its format: %w", err)
	}
	defer func() { _ = rc.Close() }()

	if _, err := audiorendition.DetectFormat(rc); err != nil {
		return err
	}
	return nil
}
