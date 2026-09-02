package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
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
// showFilter, when non-empty, narrows the result to objects whose "show"
// field equals it. Not existence-checked: an unmatched value returns an
// empty list, never a refusal.
func listConfigObjectSummaries(ctx context.Context, cfg ConfigStore, kind, showFilter string) ([]v1.ConfigObjectSummary, error) {
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
		if showFilter != "" && head.Show != showFilter {
			continue
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
	objs, err := listConfigObjectSummaries(r.Context(), h.deps.Config, config.ShowActionConfigKind, r.URL.Query().Get("show"))
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
	objs, err := listConfigObjectSummaries(r.Context(), h.deps.Config, config.ShowMacroConfigKind, r.URL.Query().Get("show"))
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

// refuseShowChange refuses a PUT to kind/id that would change its stored
// "show" (TRACK-H-H1-SPEC.md section 2: "a PUT whose show differs from
// the stored revision's is refused, naming both"). id having no active
// revision yet has no stored show to compare against, so a first-time PUT
// is never refused here: (nil, nil) means "nothing stored, proceed", a
// non-nil *v1.Problem means "refuse this write", and a non-nil error means
// the store lookup itself failed. Shared by handlePutShowCue and
// handlePutShowPlaylist rather than duplicated: both kinds carry "show" at
// the same JSON path and refuse the same way.
func (h *handlers) refuseShowChange(ctx context.Context, kind, id, incomingShow string) (*v1.Problem, error) {
	obj, err := h.deps.Config.GetConfigObject(ctx, kind, id)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if obj.CurrentRevision == 0 {
		return nil, nil
	}
	rev, err := h.deps.Config.GetConfigRevision(ctx, kind, id, obj.CurrentRevision)
	if err != nil {
		return nil, err
	}
	var head struct {
		Show string `json:"show"`
	}
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &head); err != nil {
		return nil, err
	}
	if head.Show == incomingShow {
		return nil, nil
	}
	p := mapValidationError(&config.ValidationError{
		Code: config.ValidationCodeCrossShowReference, Field: "show",
		Detail: fmt.Sprintf(
			"show is immutable: %s %q belongs to show %q and cannot be moved to show %q by a PUT; author a new %s in show %q instead",
			kind, id, head.Show, incomingShow, kind, incomingShow),
	})
	return &p, nil
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
// and "show" — [config.DecodeShowMacroPayload]'s resolveAction callback.
// The integration is what lets that decoder enforce the Resolume
// localFallback rule at write time rather than fetching it a second way;
// the show is what lets it refuse a macro step naming an action outside
// the macro's own show namespace (ADR-027). A store error, or a payload
// this function cannot decode enough to read target.integration/show
// from, is treated as "does not resolve": a transient failure surfaces as
// a validation refusal on this ONE step rather than crashing or silently
// accepting an unverifiable reference.
func (h *handlers) showActionLookup(ctx context.Context) func(actionID string) (string, string, bool) {
	return func(actionID string) (string, string, bool) {
		obj, err := h.deps.Config.GetConfigObject(ctx, config.ShowActionConfigKind, actionID)
		if err != nil || obj.CurrentRevision == 0 {
			return "", "", false
		}
		rev, err := h.deps.Config.GetConfigRevision(ctx, config.ShowActionConfigKind, actionID, obj.CurrentRevision)
		if err != nil {
			return "", "", false
		}
		var head struct {
			Show   string `json:"show"`
			Target struct {
				Integration string `json:"integration"`
			} `json:"target"`
		}
		if err := jsonUnmarshalStrict(rev.PayloadJSON, &head); err != nil {
			return "", "", false
		}
		return head.Target.Integration, head.Show, true
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

	precondition, problem := parseRevisionPrecondition(r)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}

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

	payload, verr := config.DecodeShowActionPayload(string(raw), endpoints, h.deps.IntegrationBrokers, FPPPrimitiveRegistry, h.deps.ResolumeReferences, h.showExists(r.Context()))
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

	activated, nextRevisionNo, writeErr := h.writeShowConfigRevision(r, now, ac, config.ShowActionConfigKind, id, payloadJSON, precondition,
		map[string]any{"show": payload.Show, "safetyClass": payload.SafetyClass, "integration": payload.Target.Integration})
	if writeErr != nil {
		var conflict *errConfigRevisionPreconditionFailed
		if errors.As(writeErr, &conflict) {
			writeProblem(w, h.logger, now, configRevisionConflictProblem(conflict))
			return
		}
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

	precondition, problem := parseRevisionPrecondition(r)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxShowConfigRequestBodyBytes+1))
	if err != nil {
		h.writeInternalError(w, now, "read show.macro request body", err)
		return
	}
	if len(raw) > maxShowConfigRequestBodyBytes {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body exceeds the maximum accepted size"))
		return
	}

	payload, verr := config.DecodeShowMacroPayload(string(raw), h.showActionLookup(r.Context()), h.showExists(r.Context()))
	if verr != nil {
		writeProblem(w, h.logger, now, mapValidationError(verr))
		return
	}

	payloadJSON, err := config.EncodeShowMacroPayload(payload)
	if err != nil {
		h.writeInternalError(w, now, "encode show.macro config payload", err)
		return
	}

	activated, nextRevisionNo, writeErr := h.writeShowConfigRevision(r, now, ac, config.ShowMacroConfigKind, id, payloadJSON, precondition,
		map[string]any{"show": payload.Show, "stepCount": len(payload.Steps)})
	if writeErr != nil {
		var conflict *errConfigRevisionPreconditionFailed
		if errors.As(writeErr, &conflict) {
			writeProblem(w, h.logger, now, configRevisionConflictProblem(conflict))
			return
		}
		h.writeInternalError(w, now, "write show.macro config revision", writeErr)
		return
	}

	jsonWrite(w, mapShowMacroConfigResponse(now, activated, store.ConfigObjectRecord{
		Kind: config.ShowMacroConfigKind, ID: id, CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, payload))
}

// revisionPreconditionMode is [revisionPrecondition]'s own discriminant.
type revisionPreconditionMode int

const (
	// revisionPreconditionNone is the ruled default (D-014, Manager-D's
	// build authorization): a PUT that sends neither If-Match nor
	// If-None-Match is accepted unconditionally, exactly as before this
	// change. The guarantee below is opt-in, not mandatory - a caller
	// that never sends either header gets none of it, silently, which is
	// the accepted, narrower-than-total fix, not an oversight.
	revisionPreconditionNone revisionPreconditionMode = iota
	// revisionPreconditionIfMatch is an update guard: the request named
	// the revision it expects to still be current.
	revisionPreconditionIfMatch
	// revisionPreconditionIfNoneMatchCreate is a create guard
	// (If-None-Match: *): the request expects no revision has ever been
	// activated for this id yet.
	revisionPreconditionIfNoneMatchCreate
)

// revisionPrecondition is what a config PUT asked writeShowConfigRevision
// to enforce, parsed once per request by [parseRevisionPrecondition] and
// threaded through to the single check inside writeShowConfigRevision's
// AuditedWrite closure. Revision is meaningful only when Mode is
// revisionPreconditionIfMatch.
type revisionPrecondition struct {
	Mode     revisionPreconditionMode
	Revision int64
}

// parseRevisionPrecondition reads the optional If-Match/If-None-Match
// request headers and returns what writeShowConfigRevision should
// enforce. Absence of both is revisionPreconditionNone - see that
// constant's own doc comment for why that is the ruled default rather
// than a gap this function papers over. A malformed value, or both
// headers present on the same request, is reported as a non-nil
// *v1.Problem (400) the caller writes and returns immediately, never
// silently downgraded to "no precondition": a client that took the
// trouble to send a header gets told when this coordinator could not
// honor it, rather than getting an unprotected write it never asked for.
func parseRevisionPrecondition(r *http.Request) (revisionPrecondition, *v1.Problem) {
	ifMatch := strings.TrimSpace(r.Header.Get("If-Match"))
	ifNoneMatch := strings.TrimSpace(r.Header.Get("If-None-Match"))

	if ifMatch != "" && ifNoneMatch != "" {
		p := invalidParameterProblem("If-Match and If-None-Match cannot both be sent on the same request")
		return revisionPrecondition{}, &p
	}

	if ifMatch != "" {
		rev, ok := parseQuotedRevisionETag(ifMatch)
		if !ok {
			p := invalidParameterProblem(fmt.Sprintf(
				`If-Match %q is not a quoted revision integer of 1 or greater, e.g. "7" (the value of "revision" from a prior GET or PUT response, quoted; revisions start at 1, so "0" is refused as malformed rather than accepted as a second spelling of If-None-Match: "*")`, ifMatch))
			return revisionPrecondition{}, &p
		}
		return revisionPrecondition{Mode: revisionPreconditionIfMatch, Revision: rev}, nil
	}

	if ifNoneMatch != "" {
		if ifNoneMatch != "*" {
			p := invalidParameterProblem(`If-None-Match only supports the literal value "*", asserting that no revision has been activated for this id yet`)
			return revisionPrecondition{}, &p
		}
		return revisionPrecondition{Mode: revisionPreconditionIfNoneMatchCreate}, nil
	}

	return revisionPrecondition{}, nil
}

// parseQuotedRevisionETag parses v as an RFC 7232 strong entity tag
// wrapping a revision integer of 1 or greater, e.g. `"7"`. Every other
// shape (unquoted, a weak validator's leading `W/`, zero, negative,
// non-numeric) is rejected: this coordinator never emits a revision ETag
// of 0 or less (revisions start at 1 - store/config.go's own "current
// revision 0" means "nothing activated yet", never a real revision
// number), so a value outside 1..N is either a hand-typed guess or an
// undocumented second spelling of the create guard If-None-Match: "*"
// already exists for. One documented spelling per behaviour, enforced
// here rather than silently accepted as a second one.
func parseQuotedRevisionETag(v string) (int64, bool) {
	if len(v) < 2 || v[0] != '"' || v[len(v)-1] != '"' {
		return 0, false
	}
	rev, err := strconv.ParseInt(v[1:len(v)-1], 10, 64)
	if err != nil || rev < 1 {
		return 0, false
	}
	return rev, true
}

// errConfigRevisionPreconditionFailed is writeShowConfigRevision's own
// sentinel for a failed If-Match/If-None-Match precondition, returned
// from inside the AuditedWrite closure BEFORE tx.CreateConfigRevision
// ever runs (a refused precondition creates nothing, not even a
// discarded revision row) and returned UNWRAPPED by AuditedWrite itself
// (identity.Service.AuditedWrite's own doc comment), so every call site
// tells it apart from a generic store failure with errors.As rather than
// string-matching an error message.
type errConfigRevisionPreconditionFailed struct {
	kind, id       string
	precondition   revisionPrecondition
	actualRevision int64
}

func (e *errConfigRevisionPreconditionFailed) Error() string {
	return fmt.Sprintf("config revision precondition failed for %s/%s: current revision is %d", e.kind, e.id, e.actualRevision)
}

// ProblemTypeConfigRevisionPreconditionFailed is
// [configRevisionConflictProblem]'s own type URI - ONE type shared by
// every config kind this change touches (no per-kind variation, matching
// the scope this task was given), never a distinct URI per kind.
const ProblemTypeConfigRevisionPreconditionFailed = ProblemBaseURI + "config-revision-precondition-failed"

// configRevisionConflictProblem renders e as the 409 [v1.Problem] every
// PUT handler in this package writes when its own call to
// writeShowConfigRevision reports errConfigRevisionPreconditionFailed.
func configRevisionConflictProblem(e *errConfigRevisionPreconditionFailed) v1.Problem {
	var detail string
	if e.precondition.Mode == revisionPreconditionIfNoneMatchCreate {
		detail = fmt.Sprintf(
			`If-None-Match: "*" requires %s %q to have no active revision yet, but it is already at revision %d`,
			e.kind, e.id, e.actualRevision)
	} else {
		detail = fmt.Sprintf(
			`If-Match %q is no longer current for %s %q; its current revision is %d`,
			fmt.Sprintf("%d", e.precondition.Revision), e.kind, e.id, e.actualRevision)
	}
	return v1.Problem{
		Type:   ProblemTypeConfigRevisionPreconditionFailed,
		Title:  "Config write refused: the revision precondition is no longer current",
		Status: http.StatusConflict,
		Detail: detail,
	}
}

// writeShowConfigRevision is handlePutShowAction/handlePutShowMacro's
// shared write core, mirroring handlePutFPPEndpointsConfig's own
// AuditedWrite closure (config.go) exactly: the next revision number is
// computed INSIDE the transaction (race-free against a second concurrent
// PUT of the same object, per that handler's own doc comment), and the
// revision write, its activation, and its audit entry land in one
// transaction or none of them do (ADR-024 decision 11).
//
// precondition is checked against that SAME read, before nextRevisionNo
// is used for anything: this is deliberately not a second read. The
// store's own single-connection, BEGIN IMMEDIATE design (store.go's DSN
// comment) means this closure already runs with the database's one write
// lock held for its whole duration, so the read this precondition check
// reuses and the write it guards can never have a second writer's commit
// land in between them - the check does not need a lock or a retry of
// its own, only to run inside this existing closure rather than before
// it.
func (h *handlers) writeShowConfigRevision(r *http.Request, now time.Time, ac authContext, kind, id, payloadJSON string, precondition revisionPrecondition, auditParams map[string]any) (store.ConfigRevisionRecord, int64, error) {
	var (
		activated      store.ConfigRevisionRecord
		nextRevisionNo int64
	)
	writeErr := h.deps.Identity.AuditedWrite(r.Context(), func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		currentRevision := int64(0)
		nextRevisionNo = 1
		if obj, gerr := tx.GetConfigObject(ctx, kind, id); gerr == nil {
			currentRevision = obj.CurrentRevision
			nextRevisionNo = obj.CurrentRevision + 1
		} else if !errors.Is(gerr, store.ErrConfigObjectNotFound) {
			return identity.AuditEntry{}, gerr
		}

		switch precondition.Mode {
		case revisionPreconditionIfMatch:
			if currentRevision != precondition.Revision {
				return identity.AuditEntry{}, &errConfigRevisionPreconditionFailed{kind: kind, id: id, precondition: precondition, actualRevision: currentRevision}
			}
		case revisionPreconditionIfNoneMatchCreate:
			if currentRevision != 0 {
				return identity.AuditEntry{}, &errConfigRevisionPreconditionFailed{kind: kind, id: id, precondition: precondition, actualRevision: currentRevision}
			}
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
		Target: mapConfigShowActionTarget(p.Target), Idempotent: p.Idempotent,
	}
}

func mapConfigShowActionTarget(t config.ShowActionTarget) v1.ConfigShowActionTarget {
	out := v1.ConfigShowActionTarget{
		Integration: t.Integration,
		InstanceID:  t.InstanceID, Primitive: t.Primitive, Params: t.Params,
		Broker: t.Broker,
		Action: t.Action, Ref: t.Ref,
		AudioNodeIDs: v1.AudioNodeIDList(t.AudioNodeIDs), AudioSessionID: t.AudioSessionID, AudioAction: t.AudioAction,
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
	config.ValidationCodeAudioNodeChannelDuplicate: ProblemBaseURI + "audio-node-channel-duplicate",
	config.ValidationCodeAudioNodeChannelOverlap:   ProblemBaseURI + "audio-node-channel-overlap",
	config.ValidationCodeAudioNodeRouteMismatch:    ProblemBaseURI + "audio-node-route-mismatch",

	// Track F seam F6's own additions (nightsitecontrol.go).
	config.ValidationCodeInterlockNameDuplicate:                 ProblemBaseURI + "show-config-interlock-name-duplicate",
	config.ValidationCodeInterlockSignalNotConfirmable:          ProblemBaseURI + "show-config-interlock-signal-not-confirmable",
	config.ValidationCodePowerDomainRefused:                     ProblemBaseURI + "show-config-power-domain-refused",
	config.ValidationCodeDomainProvenanceRefused:                ProblemBaseURI + "show-config-domain-provenance-refused",
	config.ValidationCodePrerequisitesEmpty:                     ProblemBaseURI + "show-config-prerequisites-empty",
	config.ValidationCodePowerOffPrerequisiteCycle:              ProblemBaseURI + "show-config-power-off-prerequisite-cycle",
	config.ValidationCodeInterlockShutdownPhaseRequiresOverride: ProblemBaseURI + "interlock-shutdown-phase-requires-override",
	config.ValidationCodeInterlockSignalNoFalseAnswer:           ProblemBaseURI + "interlock-signal-no-false-answer",

	// Track H seam H1's own additions (showplaylist.go).
	config.ValidationCodeEntriesEmpty:           ProblemBaseURI + "show-config-entries-empty",
	config.ValidationCodeEntryPositionDuplicate: ProblemBaseURI + "show-config-entry-position-duplicate",

	// config.ValidationCodeCrossShowReference predates Track H (showmacro.go
	// and others already produce it) but had no problem type of its own
	// until H1's review found it: an unmapped code falls back to the
	// generic invalid-parameter type, making H1's headline refusal
	// indistinguishable from any other bad field.
	config.ValidationCodeCrossShowReference: ProblemBaseURI + "show-config-cross-show-reference",
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
