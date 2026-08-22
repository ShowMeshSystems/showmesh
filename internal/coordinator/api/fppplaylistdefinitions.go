package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/fppidentity"
)

// This file is the FPP playlist definition publication API seam
// (FPP-PLUGIN-COORDINATOR-CONTRACTS.md §3, TRACK-H-H2-SPEC.md §3-4): the
// sibling of fppobservations.go, never an edit to it. Section 1's
// observation ingestion says WHICH entry FPP is on; this section says
// WHAT that entry (and every other entry in the same playlist) actually
// is, so an operator can bind a Cue to it before ever seeing FPP play it.

// maxFPPPlaylistDefinitionRequestBodyBytes is contract §3.2's own bound:
// "the body is bounded at 1048576 bytes ... two orders of magnitude
// above §1.2's, because unlike an observation this body does carry the
// complete definition."
const maxFPPPlaylistDefinitionRequestBodyBytes = 1048576

// auditActionFPPPublishPlaylistDefinition is this endpoint's fixed audit
// action, contract §3.4: "a store that actually inserted is audited,
// under the action fpp.publish_playlist_definition."
const auditActionFPPPublishPlaylistDefinition = "fpp.publish_playlist_definition"

// fppPlaylistDefinitionRetentionKeep is H2 spec §3's own retention bound:
// "beyond those, the newest 16 per instance are kept."
const fppPlaylistDefinitionRetentionKeep = 16

func fppPlaylistDefinitionTooLargeProblem() v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeResolumeCompositionTooLarge,
		Title:  "Payload too large",
		Status: http.StatusRequestEntityTooLarge,
		Detail: "the request body exceeds this endpoint's 1048576 byte limit",
	}
}

func fppPlaylistDefinitionUnsupportedSchemaVersionProblem(got int) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeUnsupportedDefinitionSchemaVersion,
		Title:  "Unsupported schema version",
		Status: http.StatusBadRequest,
		Detail: "this coordinator only accepts schemaVersion 1; got " + strconv.Itoa(got),
	}
}

func fppPlaylistDefinitionHashMismatchProblem(computed, declared string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeDefinitionHashMismatch,
		Title:  "Definition hash mismatch",
		Status: http.StatusBadRequest,
		Detail: "the SHA-256 of the canonicalized definition (" + computed + ") does not match the declared " +
			"playlistHash (" + declared + ")",
	}
}

// handlePostFPPPlaylistDefinition serves
// POST /api/v1/integrations/fpp/playlist-definitions, contract §3.4,
// behind writeGuard(&scopeFPPObserve, ...) — the same principal, same
// credential, same scope as playlist-entry observation ingestion (§3:
// "The same principal, the same credential, and the same fpp:observe
// scope as section 1.").
func (h *handlers) handlePostFPPPlaylistDefinition(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)

	// Step 3: bound the body before it is parsed.
	r.Body = http.MaxBytesReader(w, r.Body, maxFPPPlaylistDefinitionRequestBodyBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeProblem(w, h.logger, now, fppPlaylistDefinitionTooLargeProblem())
			return
		}
		writeProblem(w, h.logger, now, invalidParameterProblem("could not read the request body: "+err.Error()))
		return
	}

	// Step 4: decode strictly. Malformed JSON, an unknown field, trailing
	// content, or a duplicate member name anywhere in the document
	// (including inside "definition") is refused but NOT audited
	// (contract §3.4: only step 5 onward is audited), mirroring
	// fppobservations.go's identical reasoning for why a canonicalizing
	// pass — not encoding/json.Decoder alone — is what catches a
	// duplicate member name.
	var req v1.FPPPlaylistDefinitionPublishRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("malformed request body: "+err.Error()))
		return
	}
	if dec.More() {
		writeProblem(w, h.logger, now, invalidParameterProblem("malformed request body: trailing content after the JSON value"))
		return
	}
	if _, err := fppidentity.Canonicalize(raw); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body could not be canonicalized: "+err.Error()))
		return
	}

	auditRefusal := func(reason string) {
		entry := identity.AuditEntry{
			Timestamp:     now,
			PrincipalID:   ac.result.Principal.ID,
			PrincipalName: ac.result.Principal.Name,
			Form:          ac.result.Form,
			CredentialID:  ac.result.CredentialID,
			ClientAddr:    h.clientAddr(r),
			Action:        auditActionFPPPublishPlaylistDefinition,
			Target:        req.InstanceUUID,
			Kind:          identity.AuditOutcome,
			OutcomeReason: reason,
		}
		if err := h.deps.Identity.WriteAudit(ctx, entry); err != nil {
			h.logWarn("failed to audit fpp playlist definition refusal", "instanceUuid", req.InstanceUUID, "error", err)
		}
	}

	// Step 5: schemaVersion.
	if req.SchemaVersion != fppidentity.SchemaVersion {
		auditRefusal("unsupported schemaVersion")
		writeProblem(w, h.logger, now, fppPlaylistDefinitionUnsupportedSchemaVersionProblem(req.SchemaVersion))
		return
	}

	// Step 6: identity fields.
	if req.InstanceUUID == "" {
		auditRefusal("missing instanceUuid")
		writeProblem(w, h.logger, now, invalidParameterProblem("instanceUuid is required"))
		return
	}
	if req.PlaylistName == "" {
		auditRefusal("missing playlistName")
		writeProblem(w, h.logger, now, invalidParameterProblem("playlistName is required"))
		return
	}
	if !fppidentity.IsHash64(req.PlaylistHash) {
		auditRefusal("malformed playlistHash")
		writeProblem(w, h.logger, now, invalidParameterProblem("playlistHash must be 64 lowercase hex characters"))
		return
	}
	if len(req.Definition) == 0 || !json.Valid(req.Definition) {
		auditRefusal("missing definition")
		writeProblem(w, h.logger, now, invalidParameterProblem("definition is required"))
		return
	}
	{
		var probe any
		if err := json.Unmarshal(req.Definition, &probe); err != nil {
			auditRefusal("definition is not valid JSON")
			writeProblem(w, h.logger, now, invalidParameterProblem("definition is not valid JSON: "+err.Error()))
			return
		}
		if _, ok := probe.(map[string]any); !ok {
			auditRefusal("definition is not an object")
			writeProblem(w, h.logger, now, invalidParameterProblem("definition must be a JSON object"))
			return
		}
	}
	if req.CapturedAtMillis < 0 {
		auditRefusal("negative capturedAtMillis")
		writeProblem(w, h.logger, now, invalidParameterProblem("capturedAtMillis must not be negative"))
		return
	}

	// Step 7: the load-bearing check. Canonicalize the received
	// definition and refuse when its SHA-256 disagrees with the declared
	// playlistHash. A definition the coordinator's own canonicalizer
	// refuses (invalid UTF-8, excessive nesting) fails here too, same
	// refusal (contract §3.4 step 7's own text).
	canonicalDefinition, computedHash, err := fppidentity.HashCanonical(req.Definition)
	if err != nil {
		auditRefusal("definition could not be canonicalized: " + err.Error())
		writeProblem(w, h.logger, now, invalidParameterProblem("definition could not be canonicalized: "+err.Error()))
		return
	}
	if computedHash != req.PlaylistHash {
		auditRefusal("definition hash mismatch")
		writeProblem(w, h.logger, now, fppPlaylistDefinitionHashMismatchProblem(computedHash, req.PlaylistHash))
		return
	}

	// Step 8: store under the key the COORDINATOR computed, never the
	// caller's bare claim (contract §3.1) — computedHash and
	// req.PlaylistHash are equal at this point by construction, but
	// rec.PlaylistHash is built from computedHash to say so in the code,
	// not merely in prose.
	rec := store.FPPPlaylistDefinitionRecord{
		InstanceUUID:   req.InstanceUUID,
		PlaylistHash:   computedHash,
		PlaylistName:   req.PlaylistName,
		DefinitionJSON: string(canonicalDefinition),
		CapturedAt:     time.UnixMilli(req.CapturedAtMillis).UTC(),
		ReceivedAt:     now,
	}

	// errIdempotentDefinitionRepeat signals "nothing to store, nothing to
	// audit" out of the AuditedWrite closure below without treating it as
	// a real failure: InTx rolls back the (no-op) transaction either way,
	// and this coordinator never audits an idempotent repeat (contract
	// §3.4: "An idempotent repeat is not audited").
	var stored bool
	writeErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		inserted, err := tx.PutFPPPlaylistDefinition(ctx, rec)
		if err != nil {
			return identity.AuditEntry{}, err
		}
		if !inserted {
			return identity.AuditEntry{}, errIdempotentDefinitionRepeat
		}
		stored = true

		// Retention (H2 spec §3), inside the same transaction as the
		// insert it follows: never evict a hash a stored show.playlist
		// binding references; beyond those, keep only the newest 16
		// unreferenced rows for this instance.
		// tx, not h.deps.Config: this closure already runs inside
		// AuditedWrite's own transaction, and store.Store's guardNotInTx
		// refuses a Store-level call made with an in-transaction context
		// (this package's connection pool is capped at one connection,
		// see store.go's open() — a second, Store-level query would
		// block forever waiting for the connection this transaction
		// already holds). tx satisfies configReferenceReader with the
		// identical method set ConfigStore does, so the SAME helper
		// serves both callers.
		referenced, err := referencedFPPPlaylistHashesForInstance(ctx, tx, req.InstanceUUID)
		if err != nil {
			return identity.AuditEntry{}, err
		}
		if _, err := tx.PruneFPPPlaylistDefinitions(ctx, req.InstanceUUID, fppPlaylistDefinitionRetentionKeep, func(hash string) bool {
			return referenced[hash]
		}); err != nil {
			return identity.AuditEntry{}, err
		}

		return identity.AuditEntry{
			Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
			Action: auditActionFPPPublishPlaylistDefinition, Target: req.InstanceUUID,
			Params: map[string]any{"playlistHash": computedHash, "playlistName": req.PlaylistName},
			Kind:   identity.AuditAdmin,
		}, nil
	})

	switch {
	case errors.Is(writeErr, errIdempotentDefinitionRepeat):
		// Nothing stored, nothing audited, contract §3.4 step 8.
	case writeErr != nil:
		h.writeInternalError(w, now, "put fpp playlist definition", writeErr)
		return
	}

	jsonWrite(w, v1.FPPPlaylistDefinitionPublishResponse{
		SchemaVersion: req.SchemaVersion,
		InstanceUUID:  req.InstanceUUID,
		PlaylistHash:  computedHash,
		Stored:        stored,
		Idempotent:    !stored,
		ServerTime:    formatTime(now),
	})
}

// errIdempotentDefinitionRepeat is handlePostFPPPlaylistDefinition's own
// sentinel, never returned to a caller outside this file.
var errIdempotentDefinitionRepeat = errors.New("api: fpp playlist definition already held (idempotent repeat)")

// configReferenceReader is the minimum read surface
// [referencedFPPPlaylistHashesByInstance] needs, satisfied structurally
// by BOTH [ConfigStore] (h.deps.Config, for a caller outside any
// transaction — the list handler) and *store.Tx (for a caller already
// inside one — the POST handler's retention prune, which must read
// show.playlist through the SAME transaction its own insert runs in,
// never a second Store-level connection store.go's single-connection
// pool does not have to give a nested caller; see store/tx.go's
// guardNotInTx). This is the caller-supplied reference check H2 spec §3
// requires retention to run behind, rather than the store package
// importing config.
type configReferenceReader interface {
	ListConfigObjects(ctx context.Context, kind string) ([]store.ConfigObjectRecord, error)
	GetConfigRevision(ctx context.Context, kind, id string, revision int64) (store.ConfigRevisionRecord, error)
}

// referencedFPPPlaylistHashesForInstance reports, for one FPP instance,
// which playlist hashes are named by that instance's fpp binding in some
// stored show.playlist object's ACTIVE revision.
func referencedFPPPlaylistHashesForInstance(ctx context.Context, r configReferenceReader, instanceUUID string) (map[string]bool, error) {
	all, err := referencedFPPPlaylistHashesByInstance(ctx, r)
	if err != nil {
		return nil, err
	}
	return all[instanceUUID], nil
}

// referencedFPPPlaylistHashesByInstance is
// [referencedFPPPlaylistHashesForInstance]'s all-instances form, shared
// with the list handler's own "referenced" column so both read the same
// show.playlist objects once per call rather than once per row. A
// payload that fails to decode (a malformed or non-fpp-runner
// show.playlist) is treated as "names nothing", never as an error: a
// broken or unrelated show.playlist object must not make retention (or
// the list response) fail for every FPP instance.
func referencedFPPPlaylistHashesByInstance(ctx context.Context, r configReferenceReader) (map[string]map[string]bool, error) {
	objs, err := r.ListConfigObjects(ctx, config.ShowPlaylistConfigKind)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]bool{}
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := r.GetConfigRevision(ctx, config.ShowPlaylistConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			if errors.Is(err, store.ErrConfigRevisionNotFound) {
				continue
			}
			return nil, err
		}
		var payload config.ShowPlaylistPayload
		if err := json.Unmarshal([]byte(rev.PayloadJSON), &payload); err != nil {
			continue
		}
		if payload.Runner != config.ShowPlaylistRunnerFPP || payload.FPP == nil {
			continue
		}
		if payload.FPP.InstanceUUID == "" || payload.FPP.PlaylistHash == "" {
			continue
		}
		if out[payload.FPP.InstanceUUID] == nil {
			out[payload.FPP.InstanceUUID] = map[string]bool{}
		}
		out[payload.FPP.InstanceUUID][payload.FPP.PlaylistHash] = true
	}
	return out, nil
}

// handleListFPPPlaylistDefinitions serves
// GET /api/v1/integrations/fpp/playlist-definitions, contract §3.6:
// metadata for every stored definition, newest received first.
func (h *handlers) handleListFPPPlaylistDefinitions(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	recs, err := h.deps.FPPPlaylistDefinitions.ListFPPPlaylistDefinitions(ctx)
	if err != nil {
		h.writeInternalError(w, now, "list fpp playlist definitions", err)
		return
	}
	referenced, err := referencedFPPPlaylistHashesByInstance(ctx, h.deps.Config)
	if err != nil {
		h.writeInternalError(w, now, "resolve fpp playlist definition references", err)
		return
	}
	out := make([]v1.FPPPlaylistDefinitionMetadata, 0, len(recs))
	for _, rec := range recs {
		entries, err := parseFPPPlaylistDefinitionEntries(rec.DefinitionJSON)
		if err != nil {
			// A stored definition was already verified against its own
			// hash at write time (§3.4 step 7); a parse failure here
			// means an entry section is shaped in a way §4.1's parser
			// does not recognize as an array, not that the row is
			// corrupt. Report zero entries rather than fail the whole
			// list for every other instance's rows.
			entries = nil
		}
		out = append(out, v1.FPPPlaylistDefinitionMetadata{
			InstanceUUID: rec.InstanceUUID,
			PlaylistName: rec.PlaylistName,
			PlaylistHash: rec.PlaylistHash,
			CapturedAt:   formatTime(rec.CapturedAt),
			ReceivedAt:   formatTime(rec.ReceivedAt),
			EntryCount:   len(entries),
			Referenced:   referenced[rec.InstanceUUID][rec.PlaylistHash],
		})
	}
	jsonWrite(w, v1.FPPPlaylistDefinitionsListResponse{
		Definitions: out,
		ServerTime:  formatTime(now),
	})
}

// handleGetFPPPlaylistDefinition serves
// GET /api/v1/integrations/fpp/playlist-definitions/{instanceUuid}/{playlistHash},
// contract §3.6: the stored definition itself.
func (h *handlers) handleGetFPPPlaylistDefinition(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	instanceUUID := r.PathValue("instanceUuid")
	playlistHash := r.PathValue("playlistHash")

	rec, err := h.deps.FPPPlaylistDefinitions.GetFPPPlaylistDefinition(r.Context(), instanceUUID, playlistHash)
	if err != nil {
		if errors.Is(err, store.ErrFPPPlaylistDefinitionNotFound) {
			writeProblem(w, h.logger, now, resourceNotFoundProblem(
				"no stored playlist definition for instanceUuid "+instanceUUID+" and playlistHash "+playlistHash))
			return
		}
		h.writeInternalError(w, now, "get fpp playlist definition", err)
		return
	}
	jsonWrite(w, v1.FPPPlaylistDefinitionResponse{
		InstanceUUID: rec.InstanceUUID,
		PlaylistName: rec.PlaylistName,
		PlaylistHash: rec.PlaylistHash,
		Definition:   json.RawMessage(rec.DefinitionJSON),
		CapturedAt:   formatTime(rec.CapturedAt),
		ReceivedAt:   formatTime(rec.ReceivedAt),
		ServerTime:   formatTime(now),
	})
}

// handleGetFPPPlaylistDefinitionEntries serves
// GET /api/v1/integrations/fpp/playlist-definitions/{instanceUuid}/{playlistHash}/entries,
// H2 spec §4 step 2 and §4.1.
func (h *handlers) handleGetFPPPlaylistDefinitionEntries(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	instanceUUID := r.PathValue("instanceUuid")
	playlistHash := r.PathValue("playlistHash")

	rec, err := h.deps.FPPPlaylistDefinitions.GetFPPPlaylistDefinition(r.Context(), instanceUUID, playlistHash)
	if err != nil {
		if errors.Is(err, store.ErrFPPPlaylistDefinitionNotFound) {
			writeProblem(w, h.logger, now, resourceNotFoundProblem(
				"no stored playlist definition for instanceUuid "+instanceUUID+" and playlistHash "+playlistHash))
			return
		}
		h.writeInternalError(w, now, "get fpp playlist definition", err)
		return
	}
	entries, err := parseFPPPlaylistDefinitionEntries(rec.DefinitionJSON)
	if err != nil {
		h.writeInternalError(w, now, "parse fpp playlist definition entries", err)
		return
	}
	jsonWrite(w, v1.FPPPlaylistDefinitionEntriesResponse{
		InstanceUUID: rec.InstanceUUID,
		PlaylistHash: rec.PlaylistHash,
		Entries:      entries,
		ServerTime:   formatTime(now),
	})
}

// parseFPPPlaylistDefinitionEntries implements H2 spec §4.1 by delegating
// to [fppidentity.ParseDefinitionEntries], the ONE parser for a stored
// definition's entries — shared with fppreconcile's playlist readiness
// check (internal/coordinator/fppreconcile) so the two never disagree
// about what "entry 3" means for the same stored definition. This
// function's own job is only the v1 wire-shape mapping.
func parseFPPPlaylistDefinitionEntries(definitionJSON string) ([]v1.FPPPlaylistDefinitionEntry, error) {
	parsed, err := fppidentity.ParseDefinitionEntries(definitionJSON)
	if err != nil {
		return nil, err
	}
	out := make([]v1.FPPPlaylistDefinitionEntry, 0, len(parsed))
	for _, e := range parsed {
		out = append(out, v1.FPPPlaylistDefinitionEntry{
			Section:      e.Section,
			Position:     e.Position,
			Type:         e.Type,
			SequenceName: e.SequenceName,
			MediaName:    e.MediaName,
		})
	}
	return out, nil
}
