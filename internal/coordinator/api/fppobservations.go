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
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/fppidentity"
)

// This file is the FPP playlist-entry observation API seam: ingestion and read-back of FPP
// playlist-entry observations, FPP-PLUGIN-COORDINATOR-CONTRACTS.md §1.
// It never dispatches anything and never activates a cue, see
// [handlers.handlePostFPPPlaylistEntryObservation]'s own doc comment for
// where "ingestion grants no execution authority" (§1.6's closing rule)
// is made concrete.

// scopeFPPObserve exists only so api.go's route registration can take its
// address, mirroring [scopeFPPCommand]'s identical reason: [handlers.
// writeGuard] takes *identity.Scope, and a Go constant's address cannot be
// taken directly.
var scopeFPPObserve = identity.ScopeFPPObserve

// maxFPPObservationRequestBodyBytes is contract §1.2's own bound: "the
// body is bounded at 16384 bytes; a larger body is refused with 413
// before it is parsed." The complete playlist definition never travels in
// this body, only its hash, so this is generous for every legitimate
// observation.
const maxFPPObservationRequestBodyBytes = 16384

// auditActionFPPObservePlaylistEntry is this endpoint's fixed audit
// action, contract §1.6: "every refusal from step 5 onward IS audited,
// under the action fpp.observe_playlist_entry."
const auditActionFPPObservePlaylistEntry = "fpp.observe_playlist_entry"

// fppObservationTooLargeProblem reuses [ProblemTypeResolumeCompositionTooLarge]'s
// wire value ("payload-too-large"), matching
// [resolumeActionRequestBodyTooLargeProblem]'s identical reasoning: every
// "too large" refusal in this API has the same remedy (shrink the
// request), so one generic type serves every producer of it rather than
// minting a second 413 class.
func fppObservationTooLargeProblem() v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeResolumeCompositionTooLarge,
		Title:  "Payload too large",
		Status: http.StatusRequestEntityTooLarge,
		Detail: "the request body exceeds this endpoint's 16384 byte limit; the complete playlist definition never " +
			"travels in this body, only its hash, so a legitimate observation never needs more",
	}
}

func fppObservationUnsupportedSchemaVersionProblem(got int) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeUnsupportedObservationSchemaVersion,
		Title:  "Unsupported schema version",
		Status: http.StatusBadRequest,
		Detail: "this coordinator only accepts schemaVersion 1; got " + strconv.Itoa(got),
	}
}

func fppObservationEntryKeyMismatchProblem(derived, submitted string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeObservationEntryKeyMismatch,
		Title:  "Entry key mismatch",
		Status: http.StatusBadRequest,
		Detail: "the entry key re-derived from instanceUuid, playlistName, playlistHash, section, and position " +
			"(" + derived + ") does not match the submitted entryKey (" + submitted + ")",
	}
}

func fppObservationConflictProblem(detail string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "Observation sequence conflict",
		Status: http.StatusConflict,
		Detail: detail,
	}
}

// handlePostFPPPlaylistEntryObservation serves
// POST /api/v1/integrations/fpp/playlist-entry-observations, contract
// §1.6, behind writeGuard(&scopeFPPObserve, ...). Ingestion grants no
// execution authority: this method calls nothing in this package's
// macro, command, or cue-dispatch paths, and the sequence read that
// decides replay-vs-conflict shares one transaction with the write it
// gates (see [FPPObservationStore]'s own doc comment).
func (h *handlers) handlePostFPPPlaylistEntryObservation(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)

	// Step 3: bound the body before it is parsed.
	r.Body = http.MaxBytesReader(w, r.Body, maxFPPObservationRequestBodyBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeProblem(w, h.logger, now, fppObservationTooLargeProblem())
			return
		}
		writeProblem(w, h.logger, now, invalidParameterProblem("could not read the request body: "+err.Error()))
		return
	}

	// Step 4: decode and canonicalize. Malformed JSON, an unknown field,
	// trailing content, or a duplicate member name is refused but NOT
	// audited (contract §1.6: only step 5 onward is audited). Canonicalizing
	// here, not later, is what puts a duplicate member name at this
	// unaudited step: encoding/json.Decoder accepts one silently (last
	// value wins) and does not check dec.More(), so without this the
	// coordinator's own parser would be the first thing to notice, after
	// schema and identity validation had already run and been audited.
	var req v1.FPPPlaylistEntryObservationRequest
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
	// bodyHash is the canonical form's hash, reused below both for the
	// stored record and for step 9's replay comparison; contract §1.3:
	// replay detection is over the canonical form of what was sent, not
	// over byte-identical text this handler happens to have received twice.
	_, bodyHash, err := fppidentity.HashCanonical(raw)
	if err != nil {
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
			Action:        auditActionFPPObservePlaylistEntry,
			Target:        req.InstanceUUID,
			Kind:          identity.AuditOutcome,
			OutcomeReason: reason,
		}
		if err := h.deps.Identity.WriteAudit(ctx, entry); err != nil {
			h.logWarn("failed to audit fpp playlist entry observation refusal", "instanceUuid", req.InstanceUUID, "error", err)
		}
	}

	// Step 5: schemaVersion.
	if req.SchemaVersion != fppidentity.SchemaVersion {
		auditRefusal("unsupported schemaVersion")
		writeProblem(w, h.logger, now, fppObservationUnsupportedSchemaVersionProblem(req.SchemaVersion))
		return
	}

	// Step 6: instanceUuid. An unavailable observation whose instanceUuid
	// is itself missing is refused HERE, unconditionally, contract §1.4:
	// "missing_instance_uuid is reportable only when some other identity
	// input failed, never as the reason a body arrived with no instance
	// at all."
	if req.InstanceUUID == "" {
		auditRefusal("missing instanceUuid")
		writeProblem(w, h.logger, now, invalidParameterProblem("instanceUuid is required"))
		return
	}

	// Step 7: enum, hash, and position validation.
	action, err := fppidentity.ParseAction(req.Action)
	if err != nil {
		auditRefusal("invalid action")
		writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
		return
	}
	unavailable := fppidentity.UnavailableNone
	if req.Unavailable != "" {
		unavailable, err = fppidentity.ParseUnavailable(req.Unavailable)
		if err != nil {
			auditRefusal("invalid unavailable reason")
			writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
			return
		}
	}
	if req.Position != nil && *req.Position < 0 {
		auditRefusal("negative position")
		writeProblem(w, h.logger, now, invalidParameterProblem("position must not be negative"))
		return
	}
	if req.PlaylistHash != "" && !fppidentity.IsHash64(req.PlaylistHash) {
		auditRefusal("malformed playlistHash")
		writeProblem(w, h.logger, now, invalidParameterProblem("playlistHash must be 64 lowercase hex characters"))
		return
	}
	if req.EntryKey != "" && !fppidentity.IsHash64(req.EntryKey) {
		auditRefusal("malformed entryKey")
		writeProblem(w, h.logger, now, invalidParameterProblem("entryKey must be 64 lowercase hex characters"))
		return
	}
	if req.CoalescedSincePreviousAcknowledged < 0 {
		auditRefusal("negative coalescedSincePreviousAcknowledged")
		writeProblem(w, h.logger, now, invalidParameterProblem("coalescedSincePreviousAcknowledged must not be negative"))
		return
	}
	if req.Sequence < 0 {
		auditRefusal("negative sequence")
		writeProblem(w, h.logger, now, invalidParameterProblem("sequence must not be negative"))
		return
	}
	// playlistHash and entryKey are derived identity: nothing computed
	// them for an unavailable observation, so a value in either field is
	// a claim nothing verified, and storing it would hand Track H an
	// unverified key in the Cue binding (contract §1.2, §1.4).
	if unavailable != fppidentity.UnavailableNone && (req.PlaylistHash != "" || req.EntryKey != "") {
		auditRefusal("derived identity present with unavailable set")
		writeProblem(w, h.logger, now,
			invalidParameterProblem("playlistHash and entryKey must be absent when unavailable is set"))
		return
	}
	// playlistName, playlistHash, position, and entryKey are all required
	// when unavailable is absent (contract §1.6 step 7); section may
	// legitimately be empty and is not checked here.
	if unavailable == fppidentity.UnavailableNone &&
		(req.PlaylistName == "" || req.PlaylistHash == "" || req.Position == nil || req.EntryKey == "") {
		auditRefusal("missing identity field with unavailable absent")
		writeProblem(w, h.logger, now,
			invalidParameterProblem("playlistName, playlistHash, position, and entryKey are required when unavailable is absent"))
		return
	}

	// Step 8: re-derive the entry key when identity is available.
	if unavailable == fppidentity.UnavailableNone {
		position := 0
		if req.Position != nil {
			position = *req.Position
		}
		derived, err := fppidentity.DeriveEntryKey(fppidentity.EntryIdentity{
			InstanceUUID: req.InstanceUUID,
			PlaylistName: req.PlaylistName,
			PlaylistHash: req.PlaylistHash,
			Section:      req.Section,
			Position:     position,
		})
		if err != nil {
			auditRefusal("could not derive entry key: " + err.Error())
			h.writeInternalError(w, now, "derive fpp playlist entry key", err)
			return
		}
		if derived != req.EntryKey {
			auditRefusal("entry key mismatch")
			writeProblem(w, h.logger, now, fppObservationEntryKeyMismatchProblem(derived, req.EntryKey))
			return
		}
	}

	rec := store.FPPPlaylistEntryObservationRecord{
		InstanceUUID:                       req.InstanceUUID,
		SchemaVersion:                      int64(req.SchemaVersion),
		Sequence:                           req.Sequence,
		BodyHash:                           bodyHash,
		ObservationJSON:                    string(raw),
		PlaylistName:                       req.PlaylistName,
		PlaylistHash:                       req.PlaylistHash,
		Section:                            req.Section,
		EntryKey:                           req.EntryKey,
		SequenceFilename:                   req.SequenceFilename,
		MediaFilename:                      req.MediaFilename,
		Action:                             string(action),
		Unavailable:                        string(unavailable),
		ObservedAt:                         time.UnixMilli(req.ObservedAtMillis).UTC(),
		CoalescedSincePreviousAcknowledged: req.CoalescedSincePreviousAcknowledged,
		ReceivedAt:                         now,
	}
	if req.Position != nil {
		rec.Position = int64(*req.Position)
	}

	// Step 9-10: sequence comparison and store, inside one transaction
	// see [FPPObservationStore]'s own doc comment for why the read that
	// decides "replay vs. conflict" must share the transaction with the
	// write it gates.
	var (
		replay               bool
		lastAcceptedSequence int64
	)
	txErr := h.deps.FPPObservations.InTx(ctx, func(ctx context.Context, tx *store.Tx) error {
		existing, err := tx.GetFPPPlaylistEntryObservation(ctx, rec.InstanceUUID)
		switch {
		case errors.Is(err, store.ErrFPPPlaylistEntryObservationNotFound):
			// No prior observation for this instance: accept unconditionally.
		case err != nil:
			return err
		default:
			lastAcceptedSequence = existing.Sequence
			switch {
			case rec.Sequence < existing.Sequence:
				return store.ErrFPPPlaylistEntryObservationStale
			case rec.Sequence == existing.Sequence:
				if rec.BodyHash == existing.BodyHash {
					replay = true
					return nil
				}
				return store.ErrFPPPlaylistEntryObservationSequenceConflict
			}
		}
		return tx.PutFPPPlaylistEntryObservation(ctx, rec)
	})

	switch {
	case errors.Is(txErr, store.ErrFPPPlaylistEntryObservationStale):
		auditRefusal("sequence regression: last accepted sequence was " + formatInt64(lastAcceptedSequence))
		writeProblem(w, h.logger, now, fppObservationConflictProblem(
			"sequence "+formatInt64(rec.Sequence)+" is lower than the last accepted sequence "+formatInt64(lastAcceptedSequence)+
				" for this instance; this refusal leaves the stored observation untouched"))
		return
	case errors.Is(txErr, store.ErrFPPPlaylistEntryObservationSequenceConflict):
		auditRefusal("sequence " + formatInt64(rec.Sequence) + " reused with a different body")
		writeProblem(w, h.logger, now, fppObservationConflictProblem(
			"sequence "+formatInt64(rec.Sequence)+" was already used for this instance with a different observation body"))
		return
	case txErr != nil:
		h.writeInternalError(w, now, "put fpp playlist entry observation", txErr)
		return
	}

	// Accepted observations are not audited (contract §1.6: "a per-entry
	// audit entry would flood it during an ordinary show"), and neither is
	// an idempotent replay, it stored nothing and changed nothing. The
	// stream hub's own next render pass picks up a real change from the
	// store; nothing here publishes synchronously.
	jsonWrite(w, v1.FPPPlaylistEntryObservationResponse{
		SchemaVersion: req.SchemaVersion,
		InstanceUUID:  rec.InstanceUUID,
		Sequence:      rec.Sequence,
		EntryKey:      rec.EntryKey,
		Accepted:      !replay,
		Replay:        replay,
		ServerTime:    formatTime(now),
	})
}

// handleListFPPPlaylistEntryObservations serves
// GET /api/v1/integrations/fpp/playlist-entry-observations, contract
// §1.1: the latest accepted observation for every known instance, ordered
// as the store returns them.
func (h *handlers) handleListFPPPlaylistEntryObservations(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	recs, err := h.deps.FPPObservations.ListFPPPlaylistEntryObservations(r.Context())
	if err != nil {
		h.writeInternalError(w, now, "list fpp playlist entry observations", err)
		return
	}
	out := make([]v1.FPPPlaylistEntryObservation, 0, len(recs))
	for _, rec := range recs {
		out = append(out, mapFPPPlaylistEntryObservation(rec))
	}
	jsonWrite(w, v1.FPPPlaylistEntryObservationsResponse{
		Observations: out,
		ServerTime:   formatTime(now),
	})
}

// mapFPPPlaylistEntryObservation renders rec for the wire, shared by the
// GET list handler and the stream hub's fppPlaylistEntry.changed frame
// (stream.go) so both surfaces render one instance's latest observation
// identically.
func mapFPPPlaylistEntryObservation(rec store.FPPPlaylistEntryObservationRecord) v1.FPPPlaylistEntryObservation {
	out := v1.FPPPlaylistEntryObservation{
		InstanceUUID:                       rec.InstanceUUID,
		SchemaVersion:                      int(rec.SchemaVersion),
		Sequence:                           rec.Sequence,
		PlaylistName:                       rec.PlaylistName,
		PlaylistHash:                       rec.PlaylistHash,
		Section:                            rec.Section,
		EntryKey:                           rec.EntryKey,
		SequenceFilename:                   rec.SequenceFilename,
		MediaFilename:                      rec.MediaFilename,
		Action:                             rec.Action,
		Unavailable:                        rec.Unavailable,
		ObservedAt:                         formatTime(rec.ObservedAt),
		CoalescedSincePreviousAcknowledged: rec.CoalescedSincePreviousAcknowledged,
		ReceivedAt:                         formatTime(rec.ReceivedAt),
	}
	if rec.Unavailable == "" {
		pos := int(rec.Position)
		out.Position = &pos
	}
	return out
}

// formatInt64 renders n for embedding in a problem's Detail prose.
func formatInt64(n int64) string {
	return strconv.FormatInt(n, 10)
}
