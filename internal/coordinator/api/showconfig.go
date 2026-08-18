package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is Step 9 wave 2's own seam: the four routes per kind
// (STEP-9-SPEC.md section 5.5) for the two new configuration kinds
// show.action and show.macro. It follows config.go's existing
// fpp.endpoints pattern deliberately (STEP-9-SPEC.md section 3: "there is
// no generic kind registry... a second kind is a hand-written parallel
// implementation... this step does not build one either") rather than
// inventing an abstraction two more kinds still does not justify — a
// FOURTH consumer, if one ever arrives, gets to design the registry with
// three real examples to generalize from instead of guessing from one.
//
// Reads on both kinds require show:macro:run OR config:write
// ([showConfigReadScopes], auth.go) — the corrected posture STEP-9-SPEC.md
// section 5.5 spells out precisely because copying fpp.endpoints' own
// config:write-only posture would break the operator role's macro list.
// Writes require config:write only, exactly like fpp.endpoints, and are
// audited in the same transaction as the revision write.
//
// Every PUT handler stores config.Encode{ShowAction,ShowMacro}Payload's
// output — the VALIDATED, NORMALIZED Go struct DecodeShowActionPayload/
// DecodeShowMacroPayload already produced — never the raw request body.
// This is deliberate, not incidental: those two Decode functions resolve
// show.macro's onFailure/onUnconfirmed to their defaults (STEP-9-SPEC.md
// section 2.2), and a revision written from raw bytes would persist an
// authored step that named neither key with BOTH keys still blank,
// silently pushing the "what does an absent key mean" question from write
// time (where this project has a rule for it) to whatever reads the
// stored JSON back (which would then have to know the same rule a second
// time) — see TestPutShowMacroPersistsResolvedPolicyDefaults, this file's
// own regression test for wave 2A/2B's flagged gap 1.

// maxShowConfigRequestBodyBytes bounds every PUT in this file, mirroring
// [maxConfigRequestBodyBytes]'s reasoning one file over: generous for a
// realistic show.action or show.macro payload (STEP-9-SPEC.md section 5.4
// caps steps at 32), small enough that a malicious or misbehaving caller
// cannot make a handler buffer an unbounded body before validation ever
// runs.
const maxShowConfigRequestBodyBytes = 256 * 1024

// --- list: GET /config/{kind} ---

// listConfigObjectSummaries reads every config_objects row of kind and,
// for each, its currently active revision's Show/Label — the two fields
// [store.ConfigObjectRecord] itself does not carry (they live inside the
// payload). Both show.action and show.macro's stored JSON begins with the
// identical {"show":...,"label":...} shape (config.ShowActionPayload and
// config.ShowMacroPayload agree on this, deliberately — see showaction.go
// and showmacro.go in internal/coordinator/config), so one small anonymous
// struct decodes either kind's payload for exactly the two fields a list
// entry needs, without this package importing either kind's full payload
// type for a list route that renders neither Target nor Steps
// (STEP-9-SPEC.md section 5.5: "Not the full payloads.").
//
// An object whose CurrentRevision is 0 ("declared, nothing active yet" —
// store/config.go's own documented state; this step's PUT handlers never
// leave an object in that state, but a caller of CreateConfigObject
// elsewhere in the future might) is skipped rather than rendered with a
// blank label and show: it has no active payload to read either field
// from, and a caller enumerating "what macros exist" should see only
// what it could actually run.
func listConfigObjectSummaries(ctx context.Context, cfg ConfigStore, kind string) ([]v1.ConfigObjectSummary, error) {
	objs, err := cfg.ListConfigObjects(ctx, kind)
	if err != nil {
		return nil, fmt.Errorf("list %s config objects: %w", kind, err)
	}

	out := make([]v1.ConfigObjectSummary, 0, len(objs))
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := cfg.GetConfigRevision(ctx, kind, obj.ID, obj.CurrentRevision)
		if err != nil {
			// store/config.go's own F10 caveat (ActivateConfigRevision's doc
			// comment): current_revision CAN name a row this store does not
			// hold, if some other caller ever violated "activate only what
			// you just created". Surfaced as an internal error rather than
			// silently skipped: unlike CurrentRevision == 0 above (a normal,
			// expected state), this is a store-integrity condition a caller
			// of this function needs to know happened, not one this
			// function can quietly paper over by omitting the object.
			return nil, fmt.Errorf("get active %s config revision for %q: %w", kind, obj.ID, err)
		}
		var head struct {
			Show  string `json:"show"`
			Label string `json:"label"`
		}
		if err := jsonUnmarshalStrict(rev.PayloadJSON, &head); err != nil {
			return nil, fmt.Errorf("decode %s config payload head for %q: %w", kind, obj.ID, err)
		}
		out = append(out, v1.ConfigObjectSummary{
			ID: obj.ID, Label: head.Label, Show: head.Show,
			CurrentRevision: obj.CurrentRevision, UpdatedAt: formatTime(obj.UpdatedAt),
		})
	}
	return out, nil
}

// unsupportedNodeFilterProblem rejects `?node=` on a list route whose kind
// has no "node" field to filter on (show.action, show.macro). show.surface
// is the only kind a node filter is meaningful for (payload.node —
// showsurface.go); silently ignoring the parameter here would let a
// caller believe the response was narrowed when it was not, which is
// exactly the "quietly returning everything" failure GET
// /config/show.surface?node= exists to avoid.
func unsupportedNodeFilterProblem(kind string) v1.Problem {
	return invalidParameterProblem(fmt.Sprintf(`"node" is not a supported filter for kind %q; only show.surface objects carry a node`, kind))
}

func (h *handlers) handleListShowActions(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	if r.URL.Query().Has("node") {
		writeProblem(w, h.logger, now, unsupportedNodeFilterProblem(config.ShowActionConfigKind))
		return
	}
	objs, err := listConfigObjectSummaries(r.Context(), h.deps.Config, config.ShowActionConfigKind)
	if err != nil {
		h.writeInternalError(w, now, "list show.action config objects", err)
		return
	}
	jsonWrite(w, v1.ConfigObjectsListResponse{ServerTime: formatTime(now), Kind: config.ShowActionConfigKind, Objects: objs})
}

func (h *handlers) handleListShowMacros(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	if r.URL.Query().Has("node") {
		writeProblem(w, h.logger, now, unsupportedNodeFilterProblem(config.ShowMacroConfigKind))
		return
	}
	objs, err := listConfigObjectSummaries(r.Context(), h.deps.Config, config.ShowMacroConfigKind)
	if err != nil {
		h.writeInternalError(w, now, "list show.macro config objects", err)
		return
	}
	jsonWrite(w, v1.ConfigObjectsListResponse{ServerTime: formatTime(now), Kind: config.ShowMacroConfigKind, Objects: objs})
}

// --- get one: GET /config/{kind}/{id} ---

// showConfigObjectNotFoundProblem mirrors handleGetFPPEndpointsConfig's own
// 404 (config.go): "not configured yet" and "configured but this store
// holds no active revision" both need a client-visible answer, and unlike
// fpp.endpoints (a singleton, so the object either exists or the whole
// kind is unconfigured), the interesting fact here is which OBJECT ID was
// asked for.
func showConfigObjectNotFoundProblem(kind, id string) v1.Problem {
	return resourceNotFoundProblem(fmt.Sprintf("no %s object with id %q has an active revision; PUT one to create it", kind, id))
}

func (h *handlers) getActiveShowConfigRevision(ctx context.Context, kind, id string) (store.ConfigRevisionRecord, store.ConfigObjectRecord, *v1.Problem, error) {
	obj, err := h.deps.Config.GetConfigObject(ctx, kind, id)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		p := showConfigObjectNotFoundProblem(kind, id)
		return store.ConfigRevisionRecord{}, store.ConfigObjectRecord{}, &p, nil
	}
	if err != nil {
		return store.ConfigRevisionRecord{}, store.ConfigObjectRecord{}, nil, err
	}
	if obj.CurrentRevision == 0 {
		p := showConfigObjectNotFoundProblem(kind, id)
		return store.ConfigRevisionRecord{}, store.ConfigObjectRecord{}, &p, nil
	}
	rev, err := h.deps.Config.GetConfigRevision(ctx, kind, id, obj.CurrentRevision)
	if err != nil {
		return store.ConfigRevisionRecord{}, store.ConfigObjectRecord{}, nil, err
	}
	return rev, obj, nil, nil
}

func (h *handlers) handleGetShowAction(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")
	rev, obj, problem, err := h.getActiveShowConfigRevision(r.Context(), config.ShowActionConfigKind, id)
	if err != nil {
		h.writeInternalError(w, now, "get active show.action config revision", err)
		return
	}
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	payload, err := decodeShowActionPayloadForRead(rev.PayloadJSON)
	if err != nil {
		h.writeInternalError(w, now, "decode show.action config payload", err)
		return
	}
	jsonWrite(w, mapShowActionConfigResponse(now, rev, obj, payload))
}

func (h *handlers) handleGetShowMacro(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")
	rev, obj, problem, err := h.getActiveShowConfigRevision(r.Context(), config.ShowMacroConfigKind, id)
	if err != nil {
		h.writeInternalError(w, now, "get active show.macro config revision", err)
		return
	}
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	var payload config.ShowMacroPayload
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
		h.writeInternalError(w, now, "decode show.macro config payload", err)
		return
	}
	jsonWrite(w, mapShowMacroConfigResponse(now, rev, obj, payload))
}

// decodeShowActionPayloadForRead decodes an ALREADY-VALIDATED, stored
// show.action payload back into its Go shape for rendering. This is a
// plain json.Unmarshal, never a re-run of DecodeShowActionPayload (which
// needs live endpoints/brokers/registry state that is irrelevant to
// reading back a value this store already accepted) — mirrors
// handleGetFPPEndpointsConfig's identical use of
// config.DecodeFPPEndpointsPayload for a stored (not incoming) payload one
// file over. Named distinctly from config.DecodeShowActionPayload (the
// write-time validator) specifically so nobody mistakes the two for the
// same function.
func decodeShowActionPayloadForRead(payloadJSON string) (config.ShowActionPayload, error) {
	var payload config.ShowActionPayload
	if err := jsonUnmarshalStrict(payloadJSON, &payload); err != nil {
		return config.ShowActionPayload{}, err
	}
	return payload, nil
}

// --- revisions: GET /config/{kind}/{id}/revisions ---

// handleGetShowConfigRevisions is shared by both kinds: [v1.ConfigRevisionMeta]/
// [v1.ConfigRevisionsResponse] (types.go) are already kind-agnostic — a
// plain Kind string field, not an fpp.endpoints-specific shape — so
// config.go's existing mapConfigRevisionMeta is reused verbatim rather than
// duplicated a third time.
func (h *handlers) handleGetShowConfigRevisions(w http.ResponseWriter, r *http.Request, kind string) {
	now := h.now()
	id := r.PathValue("id")

	activeRevision := int64(0)
	obj, err := h.deps.Config.GetConfigObject(r.Context(), kind, id)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		// No object at all yet: activeRevision stays 0, matching
		// handleGetFPPEndpointsConfigRevisions' identical reasoning.
	case err != nil:
		h.writeInternalError(w, now, "get "+kind+" config object", err)
		return
	default:
		activeRevision = obj.CurrentRevision
	}

	revs, err := h.deps.Config.ListConfigRevisions(r.Context(), kind, id)
	if err != nil {
		h.writeInternalError(w, now, "list "+kind+" config revisions", err)
		return
	}

	out := make([]v1.ConfigRevisionMeta, 0, len(revs))
	for i := len(revs) - 1; i >= 0; i-- {
		out = append(out, mapConfigRevisionMeta(revs[i], activeRevision))
	}
	jsonWrite(w, v1.ConfigRevisionsResponse{ServerTime: formatTime(now), Kind: kind, Revisions: out})
}

func (h *handlers) handleGetShowActionRevisions(w http.ResponseWriter, r *http.Request) {
	h.handleGetShowConfigRevisions(w, r, config.ShowActionConfigKind)
}

func (h *handlers) handleGetShowMacroRevisions(w http.ResponseWriter, r *http.Request) {
	h.handleGetShowConfigRevisions(w, r, config.ShowMacroConfigKind)
}

// --- write: PUT /config/{kind}/{id} ---

// currentFPPEndpoints derives config.FPPEndpoint from
// [Dependencies.FPP].ListInstances rather than re-reading the fpp.endpoints
// store object directly: FPPLister is already this package's single
// authoritative view of "what FPP instances does this coordinator actually
// have configured" (fppInstanceLister, wired from the store-authoritative,
// post-migration list — internal/coordinator/apiwiring.go), so reusing it
// here means show.action's instanceId validation can never disagree with
// what GET /fpp itself reports, and this file never needs to duplicate
// handleGetFPPEndpointsConfig's own migration-deferred/env-var-set
// bookkeeping to answer a much narrower question ("does this id exist").
func currentFPPEndpoints(ctx context.Context, fpp FPPLister) ([]config.FPPEndpoint, error) {
	views, err := fpp.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]config.FPPEndpoint, 0, len(views))
	for _, v := range views {
		out = append(out, config.FPPEndpoint{ID: v.InstanceID, URL: v.Endpoint})
	}
	return out, nil
}

// showActionLookup reports whether id names a show.action object with an
// active revision and, when it does, that action's own target.integration
// — [config.DecodeShowMacroPayload]'s resolveAction callback. The
// integration is what lets that decoder enforce the Resolume localFallback
// rule at write time rather than fetching it a second way. A store error,
// or a payload this function cannot decode enough to read
// target.integration from, is treated as "does not resolve": a transient
// failure surfaces as a validation refusal on this ONE step rather than
// crashing or silently accepting an unverifiable reference.
func (h *handlers) showActionLookup(ctx context.Context) func(actionID string) (string, bool) {
	return func(actionID string) (string, bool) {
		obj, err := h.deps.Config.GetConfigObject(ctx, config.ShowActionConfigKind, actionID)
		if err != nil || obj.CurrentRevision == 0 {
			return "", false
		}
		rev, err := h.deps.Config.GetConfigRevision(ctx, config.ShowActionConfigKind, actionID, obj.CurrentRevision)
		if err != nil {
			return "", false
		}
		var head struct {
			Target struct {
				Integration string `json:"integration"`
			} `json:"target"`
		}
		if err := jsonUnmarshalStrict(rev.PayloadJSON, &head); err != nil {
			return "", false
		}
		return head.Target.Integration, true
	}
}

// ProblemTypeShowActionResolumeIntegrationBlockedByMacroFallback is
// [showActionResolumeIntegrationBlockedProblem]'s own type URI.
const ProblemTypeShowActionResolumeIntegrationBlockedByMacroFallback = ProblemBaseURI + "show-action-resolume-blocked-by-macro-fallback"

// showActionResolumeIntegrationBlockedProblem refuses a show.action write
// that would make it a Resolume action while a stored macro step still
// references it with a localFallback.class other than
// "coordinator-required" — symmetric with the write-time guard on the
// macro side (config.DecodeShowMacroPayload). Revisions are immutable, so
// the fix is refusing this write, never rewriting the macro.
func showActionResolumeIntegrationBlockedProblem(actionID, macroID, stepID string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeShowActionResolumeIntegrationBlockedByMacroFallback,
		Title:  "Action refused: a stored macro step declares the wrong local fallback for a Resolume action",
		Status: http.StatusConflict,
		Detail: fmt.Sprintf(
			"action %q cannot become a resolume action: show.macro %q step %q references it with a localFallback.class "+
				"other than \"coordinator-required\"; fix that step first",
			actionID, macroID, stepID),
	}
}

// findMacroStepBlockingResolumeIntegration scans every stored show.macro
// for a step that names actionID with a localFallback.class other than
// "coordinator-required" — the write-ordering hole
// config.DecodeShowMacroPayload's own guard cannot close on its own: that
// guard only runs when the MACRO is written, so an action authored as
// "fpp", referenced by a macro with a non-coordinator-required fallback,
// and then rewritten to "resolume" bypasses it entirely.
func (h *handlers) findMacroStepBlockingResolumeIntegration(ctx context.Context, actionID string) (macroID, stepID string, blocked bool, err error) {
	objs, err := h.deps.Config.ListConfigObjects(ctx, config.ShowMacroConfigKind)
	if err != nil {
		return "", "", false, err
	}
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := h.deps.Config.GetConfigRevision(ctx, config.ShowMacroConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			return "", "", false, err
		}
		var payload config.ShowMacroPayload
		if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
			return "", "", false, err
		}
		for _, step := range payload.Steps {
			if step.Action == actionID && step.LocalFallback.Class != config.ShowMacroLocalFallbackCoordinatorRequired {
				return obj.ID, step.ID, true, nil
			}
		}
	}
	return "", "", false, nil
}

func (h *handlers) handlePutShowAction(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ac := authFromContext(r.Context())
	id := r.PathValue("id")

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxShowConfigRequestBodyBytes+1))
	if err != nil {
		h.writeInternalError(w, now, "read show.action request body", err)
		return
	}
	if len(raw) > maxShowConfigRequestBodyBytes {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body exceeds the maximum accepted size"))
		return
	}

	endpoints, err := currentFPPEndpoints(r.Context(), h.deps.FPP)
	if err != nil {
		h.writeInternalError(w, now, "list fpp instances for show.action validation", err)
		return
	}

	payload, verr := config.DecodeShowActionPayload(string(raw), endpoints, h.deps.IntegrationBrokers, FPPPrimitiveRegistry, h.deps.ResolumeReferences)
	if verr != nil {
		writeProblem(w, h.logger, now, mapValidationError(verr))
		return
	}

	if payload.Target.Integration == config.ShowActionIntegrationResolume {
		macroID, stepID, blocked, err := h.findMacroStepBlockingResolumeIntegration(r.Context(), id)
		if err != nil {
			h.writeInternalError(w, now, "check stored macros for a blocking local fallback", err)
			return
		}
		if blocked {
			writeProblem(w, h.logger, now, showActionResolumeIntegrationBlockedProblem(id, macroID, stepID))
			return
		}
	}

	payloadJSON, err := config.EncodeShowActionPayload(payload)
	if err != nil {
		h.writeInternalError(w, now, "encode show.action config payload", err)
		return
	}

	activated, nextRevisionNo, writeErr := h.writeShowConfigRevision(r, now, ac, config.ShowActionConfigKind, id, payloadJSON,
		map[string]any{"show": payload.Show, "safetyClass": payload.SafetyClass, "integration": payload.Target.Integration})
	if writeErr != nil {
		h.writeInternalError(w, now, "write show.action config revision", writeErr)
		return
	}

	jsonWrite(w, mapShowActionConfigResponse(now, activated, store.ConfigObjectRecord{
		Kind: config.ShowActionConfigKind, ID: id, CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, payload))
}

func (h *handlers) handlePutShowMacro(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ac := authFromContext(r.Context())
	id := r.PathValue("id")

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxShowConfigRequestBodyBytes+1))
	if err != nil {
		h.writeInternalError(w, now, "read show.macro request body", err)
		return
	}
	if len(raw) > maxShowConfigRequestBodyBytes {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body exceeds the maximum accepted size"))
		return
	}

	payload, verr := config.DecodeShowMacroPayload(string(raw), h.showActionLookup(r.Context()))
	if verr != nil {
		writeProblem(w, h.logger, now, mapValidationError(verr))
		return
	}

	payloadJSON, err := config.EncodeShowMacroPayload(payload)
	if err != nil {
		h.writeInternalError(w, now, "encode show.macro config payload", err)
		return
	}

	activated, nextRevisionNo, writeErr := h.writeShowConfigRevision(r, now, ac, config.ShowMacroConfigKind, id, payloadJSON,
		map[string]any{"show": payload.Show, "stepCount": len(payload.Steps)})
	if writeErr != nil {
		h.writeInternalError(w, now, "write show.macro config revision", writeErr)
		return
	}

	jsonWrite(w, mapShowMacroConfigResponse(now, activated, store.ConfigObjectRecord{
		Kind: config.ShowMacroConfigKind, ID: id, CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, payload))
}

// writeShowConfigRevision is handlePutShowAction/handlePutShowMacro's
// shared write core, mirroring handlePutFPPEndpointsConfig's own
// AuditedWrite closure (config.go) exactly: the next revision number is
// computed INSIDE the transaction (race-free against a second concurrent
// PUT of the same object, per that handler's own doc comment), and the
// revision write, its activation, and its audit entry land in one
// transaction or none of them do (ADR-024 decision 11).
func (h *handlers) writeShowConfigRevision(r *http.Request, now time.Time, ac authContext, kind, id, payloadJSON string, auditParams map[string]any) (store.ConfigRevisionRecord, int64, error) {
	var (
		activated      store.ConfigRevisionRecord
		nextRevisionNo int64
	)
	writeErr := h.deps.Identity.AuditedWrite(r.Context(), func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		nextRevisionNo = 1
		if obj, gerr := tx.GetConfigObject(ctx, kind, id); gerr == nil {
			nextRevisionNo = obj.CurrentRevision + 1
		} else if !errors.Is(gerr, store.ErrConfigObjectNotFound) {
			return identity.AuditEntry{}, gerr
		}

		rec, cerr := tx.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
			Kind: kind, ObjectID: id, Revision: nextRevisionNo, PayloadJSON: payloadJSON,
			CreatedByPrincipalID: ac.result.Principal.ID, CreatedByPrincipalName: ac.result.Principal.Name,
			Source: "api",
		})
		if cerr != nil {
			return identity.AuditEntry{}, cerr
		}
		if _, aerr := tx.ActivateConfigRevision(ctx, kind, id, nextRevisionNo); aerr != nil {
			return identity.AuditEntry{}, aerr
		}
		activated = rec

		params := map[string]any{"revision": nextRevisionNo}
		for k, v := range auditParams {
			params[k] = v
		}
		return identity.AuditEntry{
			Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
			Action: "config.write", Target: kind + "/" + id, Params: params, Kind: identity.AuditAdmin,
		}, nil
	})
	if writeErr != nil {
		return store.ConfigRevisionRecord{}, 0, writeErr
	}
	return activated, nextRevisionNo, nil
}

// --- mapping: config.ShowAction/ShowMacroPayload -> v1 wire types ---

func mapShowActionConfigResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, p config.ShowActionPayload) v1.ShowActionConfigResponse {
	return v1.ShowActionConfigResponse{
		ServerTime: formatTime(now), Kind: config.ShowActionConfigKind, ID: obj.ID, Revision: rev.Revision,
		Payload:                mapConfigShowAction(p),
		UpdatedAt:              formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
	}
}

func mapConfigShowAction(p config.ShowActionPayload) v1.ConfigShowAction {
	return v1.ConfigShowAction{
		Show: p.Show, Label: p.Label, Description: p.Description, SafetyClass: p.SafetyClass,
		Target: mapConfigShowActionTarget(p.Target),
	}
}

func mapConfigShowActionTarget(t config.ShowActionTarget) v1.ConfigShowActionTarget {
	out := v1.ConfigShowActionTarget{
		Integration: t.Integration,
		InstanceID:  t.InstanceID, Primitive: t.Primitive, Params: t.Params,
		Broker: t.Broker,
		Action: t.Action, Ref: t.Ref,
	}
	if t.Publish != nil {
		out.Publish = &v1.ConfigShowActionMQTTPublish{
			Topic: t.Publish.Topic, Payload: t.Publish.Payload, QoS: t.Publish.QoS, Retain: t.Publish.Retain,
		}
	}
	if t.Expect != nil {
		out.Expect = &v1.ConfigShowActionMQTTExpect{
			Kind: t.Expect.Kind, Topic: t.Expect.Topic, Value: t.Expect.Value, DeadlineSeconds: t.Expect.DeadlineSeconds,
		}
	}
	return out
}

func mapShowMacroConfigResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, p config.ShowMacroPayload) v1.ShowMacroConfigResponse {
	return v1.ShowMacroConfigResponse{
		ServerTime: formatTime(now), Kind: config.ShowMacroConfigKind, ID: obj.ID, Revision: rev.Revision,
		Payload:                mapConfigShowMacro(p),
		UpdatedAt:              formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
	}
}

func mapConfigShowMacro(p config.ShowMacroPayload) v1.ConfigShowMacro {
	steps := make([]v1.ConfigShowMacroStep, 0, len(p.Steps))
	for _, s := range p.Steps {
		steps = append(steps, v1.ConfigShowMacroStep{
			ID: s.ID, Action: s.Action, OnFailure: s.OnFailure, OnUnconfirmed: s.OnUnconfirmed,
			LocalFallback: v1.ConfigShowMacroLocalFallback{Class: s.LocalFallback.Class, Reason: s.LocalFallback.Reason},
		})
	}
	return v1.ConfigShowMacro{Show: p.Show, Label: p.Label, Description: p.Description, Steps: steps}
}

// --- config.ValidationError -> v1.Problem, mechanically, per code ---

// showConfigValidationProblemTypes maps every [config.ValidationError.Code]
// this step's config package produces onto its own, distinct problem type
// URI (wave 2 shared contract section 4: "a client that must tell two
// refusals apart may never branch on prose... One Code, one type URI").
// TestShowConfigValidationCodesAllMapToDistinctProblemTypes pins this
// list against config's own exported Code constants, so a code added to
// config without a matching entry here is caught by a test rather than
// silently falling through to a generic 400 with the wrong Type.
var showConfigValidationProblemTypes = map[string]string{
	config.ValidationCodeBodyInvalid:           ProblemBaseURI + "show-config-body-invalid",
	config.ValidationCodeFieldRequired:         ProblemBaseURI + "show-config-field-required",
	config.ValidationCodeFieldNull:             ProblemBaseURI + "show-config-field-null",
	config.ValidationCodeFieldEmpty:            ProblemBaseURI + "show-config-field-empty",
	config.ValidationCodeFieldInvalid:          ProblemBaseURI + "show-config-field-invalid",
	config.ValidationCodeFieldUnknownReference: ProblemBaseURI + "show-config-field-unknown-reference",
	config.ValidationCodeSafetyClassMismatch:   ProblemBaseURI + "show-config-safety-class-mismatch",
	config.ValidationCodeLocalFallbackReduced:  ProblemBaseURI + "show-config-local-fallback-reduced",
	config.ValidationCodeStepsEmpty:            ProblemBaseURI + "show-config-steps-empty",
	config.ValidationCodeStepsTooMany:          ProblemBaseURI + "show-config-steps-too-many",
	config.ValidationCodeStepIDDuplicate:       ProblemBaseURI + "show-config-step-id-duplicate",
	config.ValidationCodeFieldUnknownKey:       ProblemBaseURI + "show-config-field-unknown-key",

	// Track F seam F1's own additions (nightsession.go/showaction.go).
	config.ValidationCodeCalendarFieldRejected:     ProblemBaseURI + "show-config-calendar-field-rejected",
	config.ValidationCodeDuplicateRestDuration:     ProblemBaseURI + "show-config-duplicate-rest-duration",
	config.ValidationCodeNotImplemented:            ProblemBaseURI + "show-config-not-implemented",
	config.ValidationCodeBackgroundAudioItemsEmpty: ProblemBaseURI + "show-config-background-audio-items-empty",
	config.ValidationCodeItemIDDuplicate:           ProblemBaseURI + "show-config-item-id-duplicate",
	config.ValidationCodeCueNameDuplicate:          ProblemBaseURI + "show-config-cue-name-duplicate",
}

// mapValidationError renders verr as a v1.Problem whose Type names verr's
// own Code, mechanically — see showConfigValidationProblemTypes' own doc
// comment. An unrecognized Code (config added one and this map was not
// updated — TestShowConfigValidationCodesAllMapToDistinctProblemTypes
// exists to catch that before it ships) falls back to the generic
// invalid-parameter type rather than panicking; Field is always carried in
// Detail, since Field itself is not a wire member of v1.Problem.
func mapValidationError(verr *config.ValidationError) v1.Problem {
	problemType, ok := showConfigValidationProblemTypes[verr.Code]
	if !ok {
		problemType = ProblemTypeInvalidParameter
	}
	detail := verr.Detail
	if verr.Field != "" {
		detail = fmt.Sprintf("%s: %s", verr.Field, verr.Detail)
	}
	return v1.Problem{
		Type:   problemType,
		Title:  "Invalid show configuration",
		Status: http.StatusBadRequest,
		Detail: detail,
	}
}

// jsonUnmarshalStrict decodes raw (a stored config_revisions.payload_json
// value) into v. A plain json.Unmarshal wrapper, named distinctly from an
// ordinary call so every read-back site in this file is visibly the same
// operation.
func jsonUnmarshalStrict(raw string, v any) error {
	return json.Unmarshal([]byte(raw), v)
}
