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
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppreconcile"
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
			// No prior observation for this instance: accept unconditionally,
			// and this event starts its own occurrence (schemaV18).
			rec.EntryOccurrenceSequence = rec.Sequence
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
			// schemaV18's own rule: a fresh occurrence begins whenever this
			// observation reports action "start" (FPP entering an entry,
			// whether for the first time or looping back into one it
			// already visited — EntryKey alone cannot tell those apart,
			// since a loop's second visit derives the identical key) or
			// names a different entry than the one last accepted. Anything
			// else — an ordinary "playing" tick, "stop", "query_next", or
			// "unknown" for the SAME entry — carries the prior occurrence
			// forward unchanged, so repeat ticks inside one occurrence keep
			// deriving the same [cueactivate] ActivationID and dedup to one
			// dispatch.
			if action == fppidentity.ActionStart || rec.EntryKey != existing.EntryKey {
				rec.EntryOccurrenceSequence = rec.Sequence
			} else {
				rec.EntryOccurrenceSequence = existing.EntryOccurrenceSequence
			}
		}
		return tx.PutFPPPlaylistEntryObservation(ctx, rec)
	})

	switch {
	case errors.Is(txErr, store.ErrFPPPlaylistEntryObservationStale):
		// Owner ruling 2026-09-02 (cue-deactivate-on-jump): a sequence
		// regression is contract §1.5's own named signature of a plugin/host
		// restart, a genuine reorder, or a replayed/forged observation — the
		// exact discontinuity [cueactivate.Decide] must be able to see, so
		// this refusal's evidence, unlike every other refusal audited on
		// this route, is recorded through a marker on the instance's own row
		// (schemaV29), not only in audit_log. It is written here, through
		// [identity.Service.AuditedWrite], in the SAME transaction as its
		// own audit entry, deliberately never through auditRefusal's
		// best-effort WriteAudit: that write's own error is logged and
		// swallowed, correct for a forensic record and disqualifying for a
		// control input, because on a write failure the discontinuity would
		// silently degrade back into looking like true silence, exactly
		// when the store is already unhealthy.
		//
		// A failure here is logged loudly (logError, not the ordinary
		// best-effort warn) and does NOT change the response below: contract
		// §1.7 fixes "Sequence regression | 409 | conflict" unconditionally,
		// and this refusal is real and correct regardless of whether the
		// coordinator also managed to record its own internal marker for
		// it. The underlying condition (this instance's sequence is still
		// regressed) persists until cleared, so the plugin's own next post
		// retries this identical branch and gets another chance to record
		// it — bounding a transient failure's blind spot to roughly one
		// posting interval rather than leaving it permanently unrecorded.
		reason := "sequence regression: last accepted sequence was " + formatInt64(lastAcceptedSequence)
		if markErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
			if err := tx.MarkFPPPlaylistEntryObservationEvidenceBroken(ctx, req.InstanceUUID, now); err != nil {
				return identity.AuditEntry{}, err
			}
			return identity.AuditEntry{
				Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
				Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
				Action: auditActionFPPObservePlaylistEntry, Target: req.InstanceUUID,
				Kind: identity.AuditOutcome, OutcomeReason: reason,
			}, nil
		}); markErr != nil {
			h.logError("failed to record fpp playlist entry observation evidence-broken marker", "instanceUuid", req.InstanceUUID, "error", markErr)
		}
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
	//
	// A genuinely new (non-replay) observation DOES nudge Track H seam
	// H4's own activation loop to reconcile promptly (see
	// [CueActivationNudger]'s own doc comment for why this is not "this
	// handler grants execution authority"): the loop itself still
	// independently decides and authorizes; this only asks it not to wait
	// out its own periodic tick to notice fresh evidence exists.
	if !replay {
		h.deps.CueActivationNudger.Nudge()
	}

	// Resolve the SAME reconciliation verdict GET .../reconciliation
	// computes (mapFPPPlaylistEntryReconciliation, fppreconciliation.go),
	// reusing that route's resolution rather than a second one that could
	// disagree. Read the just-written row back rather than reconciling rec
	// directly, so a replay (which does not carry EntryOccurrenceSequence
	// or EvidenceBrokenAt) reconciles from the same stored state GET does.
	var reconciliation, operatorInstruction string
	stored, err := h.deps.FPPObservations.GetFPPPlaylistEntryObservation(ctx, rec.InstanceUUID)
	if err != nil {
		h.writeInternalError(w, now, "get fpp playlist entry observation for reconciliation", err)
		return
	}
	result, err := h.deps.FPPReconciliation.ReconcileFPPPlaylistEntryObservation(ctx, stored)
	if err != nil {
		h.writeInternalError(w, now, "reconcile fpp playlist entry observation", err)
		return
	}
	reconciliation = string(result.Outcome)
	if result.Outcome.IsMismatch() {
		operatorInstruction = fppreconcile.OperatorMismatchInstruction
	}

	jsonWrite(w, v1.FPPPlaylistEntryObservationResponse{
		SchemaVersion:       req.SchemaVersion,
		InstanceUUID:        rec.InstanceUUID,
		Sequence:            rec.Sequence,
		EntryKey:            rec.EntryKey,
		Accepted:            !replay,
		Replay:              replay,
		Reconciliation:      reconciliation,
		OperatorInstruction: operatorInstruction,
		ServerTime:          formatTime(now),
	})
}

// handleListFPPPlaylistEntryObservations serves
// GET /api/v1/integrations/fpp/playlist-entry-observations, contract
// §1.1: the latest accepted observation for every known instance, ordered
// as the store returns them.
func (h *handlers) handleListFPPPlaylistEntryObservations(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	recs, err := h.deps.FPPObservations.ListFPPPlaylistEntryObservations(ctx)
	if err != nil {
		h.writeInternalError(w, now, "list fpp playlist entry observations", err)
		return
	}

	// resolve each observation's instanceUuid to a configured
	// endpoint id, one lookup for the whole list rather than one per
	// observation. Built from h.deps.FPP (the identical source GET /fpp
	// itself renders from) so this can never disagree with what that
	// endpoint reports for the same uuid.
	endpointByUUID, err := fppEndpointIDsByInstanceUUID(ctx, h.deps)
	if err != nil {
		h.writeInternalError(w, now, "resolve fpp endpoint ids for playlist entry observations", err)
		return
	}

	out := make([]v1.FPPPlaylistEntryObservation, 0, len(recs))
	for _, rec := range recs {
		out = append(out, mapFPPPlaylistEntryObservation(rec, endpointByUUID[rec.InstanceUUID]))
	}
	jsonWrite(w, v1.FPPPlaylistEntryObservationsResponse{
		Observations: out,
		ServerTime:   formatTime(now),
	})
}

// fppEndpointIDsByInstanceUUID returns, for every uuid reported by exactly
// ONE currently configured FPP endpoint, that endpoint's id. A uuid
// reported by zero or by more than one endpoint (the duplicate-uuid rule's duplicate
// finding, see GET /fpp's duplicateInstanceUuidEndpointIds) is simply
// absent from the result, so a caller correlating against it renders "no
// single endpoint owns this uuid" rather than guessing.
//
// A free function, not a *handlers method, so the stream hub (stream.go's
// [Hub], a distinct type that also holds a Dependencies but is not a
// handlers) can call the identical resolution GET /playlist-entry-observations
// uses, rather than reimplementing it — see mapFPPPlaylistEntryObservation's
// doc comment for why the two surfaces must never disagree.
func fppEndpointIDsByInstanceUUID(ctx context.Context, deps Dependencies) (map[string]string, error) {
	views, err := deps.FPP.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	byUUID := make(map[string][]string, len(views))
	for _, fv := range views {
		if fv.InstanceUUID == nil {
			continue
		}
		byUUID[fv.InstanceUUID.UUID] = append(byUUID[fv.InstanceUUID.UUID], fv.InstanceID)
	}
	out := make(map[string]string, len(byUUID))
	for uuid, ids := range byUUID {
		if len(ids) == 1 {
			out[uuid] = ids[0]
		}
	}
	return out, nil
}

// mapFPPPlaylistEntryObservation renders rec for the wire, shared by the
// GET list handler and the stream hub's fppPlaylistEntry.changed frame
// (stream.go) so both surfaces render one instance's latest observation
// identically. endpointID is the configured fpp.endpoints id that
// currently owns rec.InstanceUUID (see [fppEndpointIDsByInstanceUUID]),
// or "" when no single configured endpoint owns that uuid right now — in
// which case EndpointID is left nil rather than set to an empty string,
// matching the GET handler's pre-existing "absent means no single owner"
// contract.
func mapFPPPlaylistEntryObservation(rec store.FPPPlaylistEntryObservationRecord, endpointID string) v1.FPPPlaylistEntryObservation {
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
	if endpointID != "" {
		out.EndpointID = &endpointID
	}
	return out
}

// formatInt64 renders n for embedding in a problem's Detail prose.
func formatInt64(n int64) string {
	return strconv.FormatInt(n, 10)
}

// auditActionFPPResetPlaylistEntryObservationSequence is the recovery
// route's fixed audit action, TRACK-H-H2-SPEC.md §5.1: "It clears the
// stored observation and its sequence anchor for one instance, is
// audited."
const auditActionFPPResetPlaylistEntryObservationSequence = "fpp.reset_playlist_entry_observation_sequence"

// handleDeleteFPPPlaylistEntryObservation serves
// DELETE /api/v1/integrations/fpp/playlist-entry-observations/{instanceUuid},
// H2 spec §5.1, behind writeGuard(&scopeFPPCommand, ...) — deliberately
// fpp:command, not fpp:observe. §5.1's own reasoning: fpp:observe stays
// out of the operator bundle so an operator credential cannot forge
// plugin evidence, but clearing wedged evidence and manufacturing it are
// different powers — this route only ever removes a row, it can never
// make the store say FPP reported something it did not, so it belongs
// with the OTHER operator-held FPP authority (fpp:command) instead.
//
// This is the ONLY path that clears a stored observation and its
// sequence anchor — contract §1.5's own text: "the stored per-instance
// sequence is cleared only by an explicit, authenticated operator
// action." Idempotent: whether or not a row existed, the post-condition
// (no stored observation for this instance) is the same, so this
// succeeds either way rather than 404ing on a caller who is not sure
// whether the instance is actually wedged.
func (h *handlers) handleDeleteFPPPlaylistEntryObservation(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)

	instanceUUID := r.PathValue("instanceUuid")

	var deleted bool
	writeErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		var err error
		deleted, err = tx.DeleteFPPPlaylistEntryObservation(ctx, instanceUUID)
		if err != nil {
			return identity.AuditEntry{}, err
		}
		return identity.AuditEntry{
			Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
			Action: auditActionFPPResetPlaylistEntryObservationSequence, Target: instanceUUID,
			Params: map[string]any{"deleted": deleted},
			Kind:   identity.AuditAdmin,
		}, nil
	})
	if writeErr != nil {
		h.writeInternalError(w, now, "delete fpp playlist entry observation", writeErr)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
