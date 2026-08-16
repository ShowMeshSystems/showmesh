package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/resolume"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/resolumecomp"
)

// This file is Track D seam D-2a: the Resolume composition upload API and
// its storage (ADR-032). It stores pkg/resolumecomp's parsed output as a
// configuration object, using the SAME config_objects/config_revisions
// mechanism and the SAME [identity.Service.AuditedWrite] one-transaction
// pattern config.go's fpp.endpoints handlers already established — this
// file adds a new configuration KIND, not a new storage mechanism, exactly
// as ADR-032 decision 1 asks ("stored as a configuration object with the
// existing revision and audit semantics").
//
// Nothing in this file ever calls Resolume's own REST API. ADR-032
// decision 2 is unconditional: "No runtime path may call GET
// /composition. Not on connect, not on a timer, not on a change signal,
// not to verify anything." An uploaded file, parsed once at upload time,
// is the only way a composition id map enters this coordinator (decision
// 4) — this file has no HTTP client field anywhere in it, which is the
// simplest possible proof that rule holds.

// resolumeCompositionConfigKind is this configuration kind's name in
// config_objects/config_revisions, kept local to this file (an api
// package constant, not internal/coordinator/config's — this seam does
// not own that package and the generic config_objects/config_revisions
// storage mechanism it built in Step 7 needs no per-kind code there;
// see store/config.go's own doc comment: "it only ever treats
// payload_json as an opaque string").
const resolumeCompositionConfigKind = "resolume.composition"

// resolumeCompositionObjectIDConst is the config_objects id this
// coordinator ALWAYS stores its composition under — a fixed constant, never
// derived from any configuration value. Review finding F (Track D seam
// D-2a): an earlier version of this file plumbed cfg.ResolumeID (the
// SHOWMESH_RESOLUME_ID env var — the live Resolume collector's own
// registration key and observation-resource id) through as this object's
// id, on the theory that it already defaults to "resolume" so it would
// "never be empty in practice". That reasoning missed the actual hazard:
// SHOWMESH_RESOLUME_ID is an operator-settable identifier for a DIFFERENT
// subsystem (the live REST/WebSocket collector), and renaming it — for a
// reason that has nothing to do with the stored composition, e.g.
// disambiguating a second Resolume instance — would silently orphan every
// revision stored under the old id: GET /config/resolume/composition would
// report "nothing uploaded yet" while the revision rows sit intact in the
// store. That is the same "manufacturing absence from a rename" defect this
// project has now caught four times in different disguises (see CLAUDE.md).
//
// Composition upload is a pure file-parsing feature that never talks to
// Arena at all (ADR-032 decision 2) and has no relationship to which — or
// whether any — live Resolume instance is configured, so it must not share
// an identifier with a feature that does. Using a fixed constant instead of
// any configuration value makes that impossible to get wrong by renaming
// something unrelated.
const resolumeCompositionObjectIDConst = "resolume"

// resolumeCompositionSourceAPI mirrors config.FPPEndpointsSourceAPI's
// "api" value for this kind's config_revisions.source column. Every
// revision this endpoint creates has a real uploading principal — unlike
// fpp.endpoints' "env_migration" source, there is no startup migration
// path for a composition file (ADR-032 decision 1: upload is the only
// ingestion path), so this kind never needs a second source value.
const resolumeCompositionSourceAPI = "api"

// maxResolumeCompositionUploadBytes bounds the ENTIRE request body of
// POST /config/resolume/composition, enforced via [http.MaxBytesReader]
// before any multipart parsing begins — so a hostile or accidental
// multi-gigabyte body is never buffered past this bound, let alone
// whole.
//
// 16 MiB: real composition files measured during ADR-032's bench capture
// ranged up to 2.6 MB, so this is roughly 6x headroom above the largest
// file actually seen, generous for a format this package's own docs
// already call undocumented and liable to grow. It is deliberately well
// under [resolumecomp.DefaultMaxBytes] (64 MiB): a request that clears
// THIS bound can never trip the parser's own limit, so
// [resolumecomp.ErrTooLarge] is dead code in practice and this file's own
// 413 is the only "too large" outcome an uploader can ever actually see —
// one bound, one message, rather than two limits that could disagree.
const maxResolumeCompositionUploadBytes = 16 * 1024 * 1024

// ProblemTypeResolumeCompositionTooLarge is this endpoint's own 413 class
// (Track D seam D-2a). Defined here rather than in problem.go: this seam
// owns resolumecomposition.go and its own minimal edits to api.go,
// v1/types.go, and interfaces.go, not problem.go, and a Go package's
// constants and functions may live in any file in the package — nothing
// requires every Problem type live in one file, and adding this here
// keeps this seam's diff to files it was asked to touch.
const ProblemTypeResolumeCompositionTooLarge = problemBaseURI + "payload-too-large"

// resolumeCompositionTooLargeProblem is [ProblemTypeResolumeCompositionTooLarge]'s
// constructor, matching this package's standing per-class-constructor
// convention (problem.go's resourceNotFoundProblem, invalidParameterProblem,
// and friends).
func resolumeCompositionTooLargeProblem() v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeResolumeCompositionTooLarge,
		Title:  "Payload too large",
		Status: http.StatusRequestEntityTooLarge,
		Detail: fmt.Sprintf(
			"the uploaded file exceeds this coordinator's %d byte upload limit for a Resolume composition file; "+
				"nothing was stored. Real composition files are typically well under 3 MB — check that the correct "+
				"file was selected.",
			maxResolumeCompositionUploadBytes,
		),
	}
}

// resolumeCompositionStoredPayload is this kind's config_revisions
// payload_json shape: [resolumecomp.Composition]'s own parsed model, plus
// the three facts about the UPLOAD ITSELF (as opposed to its parsed
// content) the task spec requires be stored alongside it — the source
// filename as uploaded, a content hash, and the byte size. Keeping these
// three next to the parsed model (rather than, say, deriving SizeBytes
// from re-marshaling Composition later) is what lets GET report exactly
// what was uploaded even if a future version of this package's mapping
// code changes how Composition itself renders.
type resolumeCompositionStoredPayload struct {
	SourceFilename string                    `json:"sourceFilename"`
	ContentHash    string                    `json:"contentHash"`
	SizeBytes      int64                     `json:"sizeBytes"`
	Composition    *resolumecomp.Composition `json:"composition"`
}

// encodeResolumeCompositionPayload marshals p for config_revisions'
// payload_json column. Mirrors config.EncodeFPPEndpointsPayload's shape
// (a plain json.Marshal wrapped with a package-prefixed error), kept local
// to this file for the identical "this seam does not own the config
// package" reason [resolumeCompositionConfigKind] documents.
func encodeResolumeCompositionPayload(p resolumeCompositionStoredPayload) (string, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("api: encode resolume.composition payload: %w", err)
	}
	return string(raw), nil
}

// decodeResolumeCompositionPayload is [encodeResolumeCompositionPayload]'s
// inverse. An error here means this store's own payload_json for this
// kind is not what this file itself wrote — a store-integrity condition,
// not a client input to validate, matching
// config.DecodeFPPEndpointsPayload's identical framing.
func decodeResolumeCompositionPayload(raw string) (resolumeCompositionStoredPayload, error) {
	var p resolumeCompositionStoredPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return resolumeCompositionStoredPayload{}, fmt.Errorf("api: decode resolume.composition payload: %w", err)
	}
	return p, nil
}

// resolumeCompositionObjectID returns the config_objects id this
// coordinator stores its composition under: always
// [resolumeCompositionObjectIDConst] — see that constant's own doc comment
// (review finding F) for why this is a fixed value rather than anything
// plumbed through [Dependencies], and why it is deliberately not the same
// value [handlePutFPPEndpointsConfig] cross-checks
// [Dependencies.ResolumeID] against. This helper exists only to name the
// property at each call site rather than repeating the constant.
func (h *handlers) resolumeCompositionObjectID() string {
	return resolumeCompositionObjectIDConst
}

// handleGetResolumeComposition serves GET /config/resolume/composition:
// the active revision's stored summary plus its full id map.
//
// 404 resourceNotFoundProblem when no revision has ever been activated —
// this is a DELIBERATE match of [handleGetFPPEndpointsConfig]'s own
// "nothing configured yet" answer (config.go), including its status code:
// the two configuration surfaces "agree" on what an empty store looks
// like, so an operator (or a client) does not have to learn two different
// shapes of "nothing here yet". This route's GATING matches
// handleGetFPPEndpointsConfig too — see this route's registration comment
// in api.go — so, unlike an earlier version of this handler, there is no
// divergence left to call out here: both the empty-store answer and the
// auth posture are now the same deliberate choice.
func (h *handlers) handleGetResolumeComposition(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	objectID := h.resolumeCompositionObjectID()

	obj, err := h.deps.Config.GetConfigObject(r.Context(), resolumeCompositionConfigKind, objectID)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		writeProblem(w, h.logger, now, resourceNotFoundProblem(
			"no Resolume composition has been uploaded yet; upload a composition file to create one"))
		return
	}
	if err != nil {
		h.writeInternalError(w, now, "get resolume.composition config object", err)
		return
	}
	if obj.CurrentRevision == 0 {
		// Mirrors handleGetFPPEndpointsConfig's identical guard: a config
		// object can exist "declared, nothing active yet" (store/config.go's
		// CreateConfigObject doc comment) even though this file's own POST
		// handler never leaves one in that state — this branch exists so a
		// future caller creating this kind's object some other way cannot
		// make this endpoint panic or fabricate a revision that does not
		// exist.
		writeProblem(w, h.logger, now, resourceNotFoundProblem(
			"no Resolume composition has been uploaded yet; upload a composition file to create one"))
		return
	}

	rev, err := h.deps.Config.GetConfigRevision(r.Context(), resolumeCompositionConfigKind, objectID, obj.CurrentRevision)
	if err != nil {
		// store/config.go's ActivateConfigRevision doc comment (F10): the
		// active pointer CAN name a revision this store does not hold if a
		// caller elsewhere ever violated "activate only what you just
		// created". Surfaced as an internal error, not a 404: the config
		// object demonstrably exists, so "not found" would misreport what is
		// actually a store-integrity condition — matching
		// handleGetFPPEndpointsConfig's identical reasoning.
		h.writeInternalError(w, now, "get active resolume.composition config revision", err)
		return
	}

	payload, err := decodeResolumeCompositionPayload(rev.PayloadJSON)
	if err != nil {
		h.writeInternalError(w, now, "decode resolume.composition config payload", err)
		return
	}

	jsonWrite(w, mapResolumeCompositionResponse(now, obj, rev, payload))
}

// handlePostResolumeCompositionUpload serves
// POST /config/resolume/composition: parses and validates an uploaded
// composition file completely, then — only if that succeeds — appends a
// new immutable revision and activates it in the same transaction as its
// audit entry (ADR-024 decision 11), exactly matching
// [handlePutFPPEndpointsConfig]'s own transaction pattern.
//
// Registered behind [handlers.writeGuard](&scopeConfigWrite, ...), so by
// the time this runs the request already carries an authenticated
// principal holding config:write and has passed decision 6's CSRF check.
//
// ADR-032 decision 7: "a rejected file changes nothing." Every failure
// path in this function returns before [identity.Service.AuditedWrite] is
// ever called, so a malformed or oversized upload never creates a
// config_revisions row, never touches config_objects, and never writes an
// audit entry — there is no partial state for any of these refusals to
// leave behind.
func (h *handlers) handlePostResolumeCompositionUpload(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ac := authFromContext(r.Context())
	objectID := h.resolumeCompositionObjectID()

	// http.MaxBytesReader wraps r.Body itself (not merely a reader this
	// function reads from), so EVERY subsequent read anywhere in this
	// request's lifecycle — this function's own multipart parsing below —
	// is bounded by it; there is no path that can buffer more than
	// maxResolumeCompositionUploadBytes before erroring out.
	r.Body = http.MaxBytesReader(w, r.Body, maxResolumeCompositionUploadBytes)

	fileBytes, filename, err := readResolumeCompositionFilePart(r)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeProblem(w, h.logger, now, resolumeCompositionTooLargeProblem())
			return
		}
		writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
		return
	}

	comp, err := resolumecomp.ParseWithLimit(bytes.NewReader(fileBytes), maxResolumeCompositionUploadBytes)
	if err != nil {
		if errors.Is(err, resolumecomp.ErrTooLarge) {
			// Dead in practice (see maxResolumeCompositionUploadBytes' own
			// doc comment on why this file's request-body bound already
			// makes the parser's identical limit unreachable), kept only so
			// this function never has an untested branch that silently
			// falls through to the generic "could not be parsed" message
			// below for what is really the same size refusal readResolumeCompositionFilePart
			// already handles.
			writeProblem(w, h.logger, now, resolumeCompositionTooLargeProblem())
			return
		}
		// ADR-032 decision 7: the parse result is reported in terms an
		// operator can act on, never a bare decode failure.
		//
		// Fix (owner found by loading the real UI, Track D seam D-2a): the
		// previous version of this branch put err.Error() directly on the
		// wire. err is one of pkg/resolumecomp's own sentinel errors, and
		// that package's own doc comment on them is explicit that their text
		// is "for logs, not for branching on" — it is Go package-qualified
		// ("resolumecomp: root element is not <Composition>: found
		// <NotAComposition>") and was never written for an operator to read.
		//
		// This handler is the boundary ADR-030 puts between an internal
		// failure and what the operator sees, so the translation belongs
		// HERE, not in pkg/resolumecomp (whose error text also serves a
		// developer reading that package's own tests and godoc — leaving it
		// alone costs nothing there) and not in the UI (which renders
		// Detail verbatim and must hold no parsing or validation logic of
		// its own — this task's own rule). showmeshctl decodes this same
		// Detail rather than calling the parser itself, so it sees the same
		// clean sentence, not a second, different one.
		//
		// resolumeCompositionParseFailureReason classifies err against
		// pkg/resolumecomp's five documented failure sentinels with
		// errors.Is — never by matching err's own text — and returns a
		// fixed, operator-vocabulary clause for each: what was wrong with
		// THE FILE, never a Go type name or a wrapped error chain.
		writeProblem(w, h.logger, now, invalidParameterProblem(
			fmt.Sprintf("This does not look like a Resolume composition file: %s.",
				resolumeCompositionParseFailureReason(err))))
		return
	}

	sum := sha256.Sum256(fileBytes)
	payload := resolumeCompositionStoredPayload{
		SourceFilename: filename,
		ContentHash:    "sha256:" + hex.EncodeToString(sum[:]),
		SizeBytes:      int64(len(fileBytes)),
		Composition:    comp,
	}
	payloadJSON, err := encodeResolumeCompositionPayload(payload)
	if err != nil {
		h.writeInternalError(w, now, "encode resolume.composition config payload", err)
		return
	}

	var (
		activatedRev store.ConfigRevisionRecord
		activatedObj store.ConfigObjectRecord
	)
	writeErr := h.deps.Identity.AuditedWrite(r.Context(), func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		// Computed INSIDE the transaction, exactly matching
		// handlePutFPPEndpointsConfig's own reasoning: the single-connection
		// pool serializes every concurrent InTx call, so reading the current
		// revision pointer here is what makes "read current revision, then
		// create the next one" race-free against a second concurrent upload.
		nextRevisionNo := int64(1)
		if obj, gerr := tx.GetConfigObject(ctx, resolumeCompositionConfigKind, objectID); gerr == nil {
			nextRevisionNo = obj.CurrentRevision + 1
		} else if !errors.Is(gerr, store.ErrConfigObjectNotFound) {
			return identity.AuditEntry{}, gerr
		}

		rec, cerr := tx.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
			Kind:                   resolumeCompositionConfigKind,
			ObjectID:               objectID,
			Revision:               nextRevisionNo,
			PayloadJSON:            payloadJSON,
			CreatedByPrincipalID:   ac.result.Principal.ID,
			CreatedByPrincipalName: ac.result.Principal.Name,
			Source:                 resolumeCompositionSourceAPI,
		})
		if cerr != nil {
			return identity.AuditEntry{}, cerr
		}

		obj, aerr := tx.ActivateConfigRevision(ctx, resolumeCompositionConfigKind, objectID, nextRevisionNo)
		if aerr != nil {
			return identity.AuditEntry{}, aerr
		}

		activatedRev = rec
		activatedObj = obj

		return identity.AuditEntry{
			Timestamp:     now,
			PrincipalID:   ac.result.Principal.ID,
			PrincipalName: ac.result.Principal.Name,
			Form:          ac.result.Form,
			CredentialID:  ac.result.CredentialID,
			ClientAddr:    h.clientAddr(r),
			Action:        "config.write",
			Target:        resolumeCompositionConfigKind,
			Params: map[string]any{
				"revision":            nextRevisionNo,
				"sourceFilename":      payload.SourceFilename,
				"contentHash":         payload.ContentHash,
				"sizeBytes":           payload.SizeBytes,
				"clipCount":           len(comp.Clips),
				"persistentClipCount": len(comp.PersistentClips),
			},
			Kind: identity.AuditAdmin,
		}, nil
	})
	if writeErr != nil {
		// ADR-024 decision 11's fail-closed rule for config:write, identical
		// to handlePutFPPEndpointsConfig's own: store.Store.InTx has already
		// rolled back the WHOLE transaction on any failure inside the
		// closure — no config_revisions row and no config_objects activation
		// survive either way, whether writeErr wraps identity.ErrAuditWrite
		// or is a plain store error.
		h.writeInternalError(w, now, "write resolume.composition config revision", writeErr)
		return
	}

	jsonWrite(w, mapResolumeCompositionUploadResponse(now, activatedRev, activatedObj, payload))
}

// resolumeCompositionParseFailureReason renders err — one of
// pkg/resolumecomp's five documented parse-failure sentinels
// ([resolumecomp.ErrNotXML], [resolumecomp.ErrWrongRoot],
// [resolumecomp.ErrMissingCompositionInfo], [resolumecomp.ErrMalformedIndex],
// [resolumecomp.ErrMissingDeckID]) — as a single clause naming what was
// wrong with THE FILE, for [handlePostResolumeCompositionUpload]'s own
// Detail sentence ("This does not look like a Resolume composition file:
// <clause>."). See that call site's own comment for why this translation
// happens here rather than in pkg/resolumecomp or the UI.
//
// Classification is by [errors.Is] against the sentinel, never by
// inspecting err's own formatted text — pkg/resolumecomp's own doc comment
// on these sentinels says that text "is for logs, not for branching on",
// and this function does not branch on it either; every returned clause is
// a fixed string, never err.Error() or any substring of it, so nothing
// this package did not itself write in this file can ever reach the wire
// through this path. [resolumecomp.ErrTooLarge] is not one of the cases
// here: [handlePostResolumeCompositionUpload] checks it first, before this
// function is ever called, and returns [resolumeCompositionTooLargeProblem]
// instead.
//
// The default case covers any error [resolumecomp.ParseWithLimit] could
// return that is not one of its five documented sentinels — unreachable
// with that package's current implementation (every one of its returned
// errors wraps one of the five, per its own doc comment on each), kept so
// this function can never itself construct a Detail from an unclassified
// error's own text if that ever stops being true.
func resolumeCompositionParseFailureReason(err error) string {
	switch {
	case errors.Is(err, resolumecomp.ErrNotXML):
		return "it is not a valid XML file"
	case errors.Is(err, resolumecomp.ErrWrongRoot):
		return "its root element is not <Composition>"
	case errors.Is(err, resolumecomp.ErrMissingCompositionInfo):
		return "it has no composition information (name, canvas size) to read"
	case errors.Is(err, resolumecomp.ErrMalformedIndex):
		return "a clip or layer in it has a missing or invalid position"
	case errors.Is(err, resolumecomp.ErrMissingDeckID):
		return "a deck in it has no unique ID"
	default:
		return "its contents could not be recognized"
	}
}

// readResolumeCompositionFilePart reads the single "file" part of r's
// multipart/form-data body into memory and returns its bytes and the
// filename the client sent. r.Body must already be wrapped by
// [http.MaxBytesReader] before this is called — this function relies on
// that wrapping (rather than imposing a second, independent bound of its
// own) to keep exactly one size limit in this file, matching
// [maxResolumeCompositionUploadBytes]'s own doc comment on why two
// disagreeing limits would be worse than one.
//
// Uses [http.Request.MultipartReader] (streaming one part at a time)
// rather than [http.Request.ParseMultipartForm] (which spills large parts
// to a temp file on disk) deliberately: a composition file is at most
// [maxResolumeCompositionUploadBytes], already small enough to hold in
// memory outright — matching showmeshctl's own
// buildCompositionMultipartBody, which buffers the whole request body
// client-side for the identical reason — and this avoids ParseMultipartForm's
// temp-file lifecycle (creation, and the caller then being responsible for
// removing it) for no benefit at this size.
//
// Review finding H: this contract (and api/openapi.yaml's own description)
// says "exactly one file part named file". Two things this function used to
// accept silently now refuse instead, both with a message naming what was
// wrong rather than a bare parse error: a SECOND part also named "file"
// (the earlier version returned on the first match and simply never looked
// at the rest of the body, discarding the second with no warning), and a
// part named "file" that is a plain form FIELD rather than an uploaded file
// — [multipart.Part.FileName] is empty for a field, and storing that empty
// string as the composition's sourceFilename produced a stored record the
// UI would render as an unlabeled size ("(4.6 KiB)") with nothing before
// it.
func readResolumeCompositionFilePart(r *http.Request) (fileBytes []byte, filename string, err error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, "", fmt.Errorf(`request must be multipart/form-data with one file part named "file": %w`, err)
	}

	found := false
	for {
		part, perr := mr.NextPart()
		if errors.Is(perr, io.EOF) {
			break
		}
		if perr != nil {
			return nil, "", perr
		}
		if part.FormName() != "file" {
			_ = part.Close()
			continue
		}
		if found {
			_ = part.Close()
			return nil, "", errors.New(`request must include exactly one file part named "file"; a second one was found`)
		}
		name := part.FileName()
		if name == "" {
			_ = part.Close()
			return nil, "", errors.New(`the "file" part must be an uploaded file with a filename, not a plain form field`)
		}
		buf, rerr := io.ReadAll(part)
		_ = part.Close()
		if rerr != nil {
			return nil, "", rerr
		}
		fileBytes, filename = buf, name
		found = true
	}

	if !found {
		return nil, "", errors.New(`request must include one file part named "file"`)
	}
	return fileBytes, filename, nil
}

// resolumeInstanceComposition reads the stored resolume.composition config
// revision and renders it as a [v1.ResolumeInstanceComposition] (Track D
// seam E) — the reduced summary a Resolume instance's own payload needs,
// not the full id map GET /config/resolume/composition renders. nil, nil is
// the correctly-distinguished "nothing uploaded yet" case (no config object
// for this kind, or one declared but never activated), mirroring
// [handlers.handleGetResolumeComposition]'s identical two guards — this is
// the ONE place both that handler and every Resolume instance renderer
// (mapping.go's mapResolumeInstance, called from resolumeinstances.go's
// handlers, handleSnapshot, and Hub.render) compute this fact, so they
// cannot answer it inconsistently.
func resolumeInstanceComposition(ctx context.Context, cfg ConfigStore) (*v1.ResolumeInstanceComposition, error) {
	obj, err := cfg.GetConfigObject(ctx, resolumeCompositionConfigKind, resolumeCompositionObjectIDConst)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get resolume.composition config object: %w", err)
	}
	if obj.CurrentRevision == 0 {
		return nil, nil
	}

	rev, err := cfg.GetConfigRevision(ctx, resolumeCompositionConfigKind, resolumeCompositionObjectIDConst, obj.CurrentRevision)
	if err != nil {
		return nil, fmt.Errorf("get resolume.composition config revision %d: %w", obj.CurrentRevision, err)
	}
	payload, err := decodeResolumeCompositionPayload(rev.PayloadJSON)
	if err != nil {
		return nil, fmt.Errorf("decode resolume.composition config payload: %w", err)
	}

	return &v1.ResolumeInstanceComposition{
		Name: payload.Composition.Name,
	}, nil
}

// resolumeCompositionDegradeOnError reads the stored Resolume composition
// via [resolumeInstanceComposition], logging and returning nil on a
// config-store error rather than propagating it (owner review finding 3,
// 2026-08-16). Composition is stored configuration; a caller here still
// has real ListInstances evidence to render (reachability, health), and a
// storage hiccup reading composition must not cost the operator that view
// too — CLAUDE.md constraint 23 and ADR-024 decision 7 scope a failure to
// "you cannot act", never "you cannot see".
func resolumeCompositionDegradeOnError(ctx context.Context, cfg ConfigStore, logger *slog.Logger, action string) *v1.ResolumeInstanceComposition {
	composition, err := resolumeInstanceComposition(ctx, cfg)
	if err != nil {
		logger.Warn("resolume composition read failed; rendering composition: null", "action", action, "error", err)
		return nil
	}
	return composition
}

// mapResolumeCompositionSummary renders payload's own metadata plus its
// parsed [resolumecomp.Composition] into the wire summary shared by
// POST's own response and the "composition" member of GET's.
func mapResolumeCompositionSummary(payload resolumeCompositionStoredPayload) v1.ResolumeCompositionSummary {
	c := payload.Composition

	clipCountByDeck := make(map[string]int, len(c.Decks))
	for _, clip := range c.Clips {
		clipCountByDeck[clip.DeckID]++
	}
	decks := make([]v1.ResolumeCompositionDeckSummary, 0, len(c.Decks))
	for i, d := range c.Decks {
		// position is 1-based (ADR-037 decision 4, resolume.DeckLabel), the
		// deck's own order in this response's own list — never recomputed
		// from anything but that position.
		name, generated := resolume.DeckLabel(i+1, d.Name)
		decks = append(decks, v1.ResolumeCompositionDeckSummary{
			ID:            d.ID,
			Name:          name,
			NameGenerated: generated,
			Closed:        d.Closed,
			ClipCount:     clipCountByDeck[d.ID],
		})
	}

	return v1.ResolumeCompositionSummary{
		Name:           c.Name,
		SourceFilename: payload.SourceFilename,
		ContentHash:    payload.ContentHash,
		SizeBytes:      payload.SizeBytes,
		WrittenBy: v1.ResolumeCompositionWrittenBy{
			Product:  c.WrittenBy.Product,
			Major:    c.WrittenBy.Major,
			Minor:    c.WrittenBy.Minor,
			Micro:    c.WrittenBy.Micro,
			Revision: c.WrittenBy.Revision,
		},
		Canvas:              v1.ResolumeCompositionCanvas{Width: c.Canvas.Width, Height: c.Canvas.Height},
		Decks:               decks,
		LayerCount:          len(c.Layers),
		LayerGroupCount:     len(c.LayerGroups),
		ColumnCount:         len(c.Columns),
		ClipCount:           len(c.Clips),
		PersistentClipCount: len(c.PersistentClips),
	}
}

// mapResolumeCompositionUploadResponse renders a freshly activated
// revision as POST /config/resolume/composition's success body.
// ActivatedAt is the config_objects row's own UpdatedAt — the moment
// [store.Tx.ActivateConfigRevision] pointed current_revision at this
// revision, inside the same transaction that created it — not the
// revision's own CreatedAt, mirroring GET's identical choice (see
// mapResolumeCompositionResponse) so both routes report "activated" from
// the same source field.
func mapResolumeCompositionUploadResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, payload resolumeCompositionStoredPayload) v1.ResolumeCompositionUploadResponse {
	return v1.ResolumeCompositionUploadResponse{
		ServerTime:  formatTime(now),
		Revision:    rev.Revision,
		ActivatedAt: formatTime(obj.UpdatedAt),
		Composition: mapResolumeCompositionSummary(payload),
	}
}

// mapResolumeCompositionResponse renders the active revision plus its
// full id map as GET /config/resolume/composition's body.
func mapResolumeCompositionResponse(now time.Time, obj store.ConfigObjectRecord, rev store.ConfigRevisionRecord, payload resolumeCompositionStoredPayload) v1.ResolumeCompositionResponse {
	c := payload.Composition
	summary := mapResolumeCompositionSummary(payload)

	layerGroups := make([]v1.ResolumeCompositionLayerGroup, 0, len(c.LayerGroups))
	for _, lg := range c.LayerGroups {
		layerGroups = append(layerGroups, v1.ResolumeCompositionLayerGroup{ID: lg.ID, Index: lg.Index})
	}

	layers := make([]v1.ResolumeCompositionLayer, 0, len(c.Layers))
	for _, l := range c.Layers {
		name, generated := resolume.LayerLabel(l.Index, l.Name)
		layers = append(layers, v1.ResolumeCompositionLayer{
			ID:              l.ID,
			Index:           l.Index,
			LayerGroupIndex: l.LayerGroupIndex,
			Name:            name,
			NameGenerated:   generated,
		})
	}

	columns := make([]v1.ResolumeCompositionColumn, 0, len(c.Columns))
	for _, col := range c.Columns {
		// Columns never carry an authored name at all (resolumecomp.Column
		// has no Name field), so this is always generated — see
		// [resolume.ColumnLabel]'s own doc comment.
		columns = append(columns, v1.ResolumeCompositionColumn{
			ID: col.ID, DeckID: col.DeckID, Index: col.Index,
			Name: resolume.ColumnLabel(col.Index), NameGenerated: true,
		})
	}

	ambiguous := resolumeClipAmbiguity(c)

	clips := make([]v1.ResolumeCompositionClip, 0, len(c.Clips))
	for _, clip := range c.Clips {
		name, generated := resolume.ClipLabel(clip.LayerIndex, clip.ColumnIndex, clip.Name)
		clips = append(clips, mapResolumeCompositionClip(clip, name, generated, ambiguous[clip.ID]))
	}

	persistentClips := make([]v1.ResolumeCompositionClip, 0, len(c.PersistentClips))
	for i, clip := range c.PersistentClips {
		// position is 1-based (resolume.PersistentClipLabel), this clip's
		// own order in this response's own persistentClips list.
		name, generated := resolume.PersistentClipLabel(i+1, clip.Name)
		persistentClips = append(persistentClips, mapResolumeCompositionClip(clip, name, generated, ambiguous[clip.ID]))
	}

	return v1.ResolumeCompositionResponse{
		ServerTime:      formatTime(now),
		Revision:        rev.Revision,
		ActivatedAt:     formatTime(obj.UpdatedAt),
		Composition:     summary,
		Decks:           summary.Decks,
		LayerGroups:     layerGroups,
		Layers:          layers,
		Columns:         columns,
		Clips:           clips,
		PersistentClips: persistentClips,
	}
}

// mapResolumeCompositionClip renders one [resolumecomp.Clip] onto the
// wire. clip.DeckID passes straight through to the JSON-tagged
// "deckId,omitempty" field: empty for a persistent clip (never sent at
// all, per ADR-032 decision 6 — see [v1.ResolumeCompositionClip]'s own
// doc comment), non-empty for a deck clip. name/generated are ADR-037
// decision 4's already-computed label (resolume.ClipLabel or
// resolume.PersistentClipLabel, chosen by the caller per clip kind — a
// deck clip's generated form needs its layer/column index, a persistent
// clip's needs its list position, and neither is available from clip
// alone), never recomputed here a second way.
func mapResolumeCompositionClip(clip resolumecomp.Clip, name string, generated, ambiguous bool) v1.ResolumeCompositionClip {
	return v1.ResolumeCompositionClip{
		ID:                 clip.ID,
		DeckID:             clip.DeckID,
		LayerIndex:         clip.LayerIndex,
		ColumnIndex:        clip.ColumnIndex,
		Name:               name,
		NameGenerated:      generated,
		Ambiguous:          ambiguous,
		TransportTypeIndex: clip.TransportTypeIndex,
		SourcePath:         clip.SourcePath,
		Width:              clip.Width,
		Height:             clip.Height,
	}
}

// resolumeClipAmbiguity computes, keyed by each clip's own id, the
// (deck-or-persistent, layer, label) triple two clips must not share (see
// [resolume.AmbiguousClipIDs]) — over BOTH c.Clips and c.PersistentClips
// together: deck.ID is always non-empty for a deck clip and always "" for
// a persistent one, so the two collections never collide in the key space.
func resolumeClipAmbiguity(c *resolumecomp.Composition) map[string]bool {
	// A clip whose layerIndex does not resolve to any tracked layer gets a
	// key unique to it — two such clips are never thereby known to share a
	// layer, mirroring resolveDeckClip's own identical rule in the resolume
	// package.
	unknownLayerSeq := 0
	layerKey := func(layerIndex int) string {
		if label, known := resolume.LayerLabelByIndex(c.Layers, layerIndex); known {
			return label
		}
		unknownLayerSeq++
		return fmt.Sprintf("\x00unknown-%d", unknownLayerSeq)
	}

	entries := make(map[string]resolume.ClipTripleKey, len(c.Clips)+len(c.PersistentClips))
	for _, clip := range c.Clips {
		label, _ := resolume.ClipLabel(clip.LayerIndex, clip.ColumnIndex, clip.Name)
		entries[clip.ID] = resolume.ClipTripleKey{Deck: clip.DeckID, Layer: layerKey(clip.LayerIndex), Label: label}
	}
	for i, clip := range c.PersistentClips {
		label, _ := resolume.PersistentClipLabel(i+1, clip.Name)
		entries[clip.ID] = resolume.ClipTripleKey{Layer: layerKey(clip.LayerIndex), Label: label}
	}
	return resolume.AmbiguousClipIDs(entries)
}
