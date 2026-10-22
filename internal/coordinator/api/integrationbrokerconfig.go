package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// IntegrationBrokerSource lists the brokers an mqtt show.action may name:
// the built-in broker when this coordinator manages it, then the stored ones.
type IntegrationBrokerSource interface {
	IntegrationBrokers(ctx context.Context) ([]config.IntegrationBroker, error)
	BuiltIn() (config.IntegrationBroker, bool)
	// Notify asks the connections to follow a broker change now.
	Notify()
}

// StaticIntegrationBrokers is a fixed [IntegrationBrokerSource] with no built-in broker.
type StaticIntegrationBrokers []config.IntegrationBroker

func (s StaticIntegrationBrokers) IntegrationBrokers(context.Context) ([]config.IntegrationBroker, error) {
	return s, nil
}
func (StaticIntegrationBrokers) BuiltIn() (config.IntegrationBroker, bool) {
	return config.IntegrationBroker{}, false
}
func (StaticIntegrationBrokers) Notify() {}

const (
	maxIntegrationBrokerRequestBodyBytes = 16 * 1024
	integrationBrokerEditsBlocked        = "Brokers are set by SHOWMESH_INTEGRATION_BROKERS on the coordinator. Remove it and restart the coordinator to edit them here."
)

func integrationBrokerEditsBlockedProblem() v1.Problem {
	return v1.Problem{
		Type: ProblemTypeConflict, Title: "Broker change refused: SHOWMESH_INTEGRATION_BROKERS is still set",
		Status: http.StatusConflict, Detail: integrationBrokerEditsBlocked,
	}
}

func builtInIntegrationBrokerProblem() v1.Problem {
	return v1.Problem{
		Type: ProblemTypeConflict, Title: "Broker change refused: the built-in broker cannot be changed",
		Status: http.StatusConflict, Detail: "The built-in broker is always listed and cannot be changed or removed.",
	}
}

func mapBuiltInIntegrationBroker(b config.IntegrationBroker) v1.IntegrationBroker {
	return v1.IntegrationBroker{
		ID: b.ID, URL: b.URL, Username: b.Username, PasswordSet: b.Password != "",
		BuiltIn: true, TopicPrefix: b.TopicPrefix, Source: "built-in",
	}
}

func mapStoredIntegrationBroker(rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, p config.IntegrationBrokerPayload) v1.IntegrationBroker {
	updated := formatTime(obj.UpdatedAt)
	return v1.IntegrationBroker{
		ID: obj.ID, URL: p.URL, Username: p.Username, PasswordSet: p.PasswordSet,
		Revision: rev.Revision, UpdatedAt: &updated,
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName), Source: rev.Source,
	}
}

func (h *handlers) handleListIntegrationBrokers(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	out := []v1.IntegrationBroker{}
	if b, ok := h.deps.IntegrationBrokers.BuiltIn(); ok {
		out = append(out, mapBuiltInIntegrationBroker(b))
	}
	objs, err := h.deps.Config.ListConfigObjects(r.Context(), config.IntegrationBrokerConfigKind)
	if err != nil {
		h.writeInternalError(w, now, "list integration.broker config objects", err)
		return
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].ID < objs[j].ID })
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := h.deps.Config.GetConfigRevision(r.Context(), config.IntegrationBrokerConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			h.writeInternalError(w, now, "get active integration.broker config revision", err)
			return
		}
		p, err := config.DecodeIntegrationBrokerPayload(rev.PayloadJSON)
		if err != nil {
			h.writeInternalError(w, now, "decode integration.broker config payload", err)
			return
		}
		out = append(out, mapStoredIntegrationBroker(rev, obj, p))
	}
	resp := v1.IntegrationBrokersListResponse{ServerTime: formatTime(now), Brokers: out}
	if h.deps.IntegrationBrokersEnvVarSet {
		resp.EditsBlocked = integrationBrokerEditsBlocked
	}
	jsonWrite(w, resp)
}

func (h *handlers) handleGetIntegrationBroker(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")
	if b, ok := h.deps.IntegrationBrokers.BuiltIn(); ok && id == b.ID {
		jsonWrite(w, v1.IntegrationBrokerResponse{ServerTime: formatTime(now), Broker: mapBuiltInIntegrationBroker(b)})
		return
	}
	cur, err := h.readActivePreviewConfig(r.Context(), config.IntegrationBrokerConfigKind, id)
	if err != nil {
		h.writeInternalError(w, now, "get active integration.broker config revision", err)
		return
	}
	if !cur.ok {
		writeProblem(w, h.logger, now, resourceNotFoundProblem("No broker has this id. Add it first or check the id."))
		return
	}
	p, err := config.DecodeIntegrationBrokerPayload(cur.rev.PayloadJSON)
	if err != nil {
		h.writeInternalError(w, now, "decode integration.broker config payload", err)
		return
	}
	jsonWrite(w, v1.IntegrationBrokerResponse{ServerTime: formatTime(now), Broker: mapStoredIntegrationBroker(cur.rev, cur.obj, p)})
}

func (h *handlers) handleGetIntegrationBrokerRevisions(w http.ResponseWriter, r *http.Request) {
	h.handlePreviewConfigRevisions(w, r, config.IntegrationBrokerConfigKind, r.PathValue("id"))
}

// handlePutIntegrationBroker creates or partly updates a broker. An absent
// field keeps its stored value; a null or empty password clears it.
func (h *handlers) handlePutIntegrationBroker(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")
	bad := func(msg string) { writeProblem(w, h.logger, now, invalidParameterProblem(msg)) }
	if id == config.BuiltInIntegrationBrokerID {
		writeProblem(w, h.logger, now, builtInIntegrationBrokerProblem())
		return
	}
	if msg := config.IntegrationBrokerIDProblem(id); msg != "" {
		bad(msg)
		return
	}
	if h.deps.IntegrationBrokersEnvVarSet {
		writeProblem(w, h.logger, now, integrationBrokerEditsBlockedProblem())
		return
	}
	precondition, problem := parseRevisionPrecondition(r)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	top, err := decodePreviewPutBody(io.LimitReader(r.Body, maxIntegrationBrokerRequestBodyBytes+1), "url", "username", "password")
	if err != nil {
		bad(err.Error())
		return
	}
	cur, err := h.readActivePreviewConfig(r.Context(), config.IntegrationBrokerConfigKind, id)
	if err != nil {
		h.writeInternalError(w, now, "read integration.broker config", err)
		return
	}
	var next config.IntegrationBrokerPayload
	if cur.ok {
		if next, err = config.DecodeIntegrationBrokerPayload(cur.rev.PayloadJSON); err != nil {
			h.writeInternalError(w, now, "decode integration.broker config payload", err)
			return
		}
	}
	if raw, ok := top["url"]; ok {
		s, err := decodePreviewString(raw, "address")
		if err != nil {
			bad(err.Error())
			return
		}
		next.URL = strings.TrimSpace(s)
	}
	if raw, ok := top["username"]; ok {
		s, err := decodePreviewString(raw, "username")
		if err != nil {
			bad(err.Error())
			return
		}
		next.Username = strings.TrimSpace(s)
	}
	cred := credentialChange{kind: config.IntegrationBrokerConfigKind, id: id, field: config.IntegrationBrokerPasswordCredentialField}
	if raw, ok := top["password"]; ok {
		password := ""
		if !isJSONNull(raw) {
			if password, err = decodePreviewString(raw, "password"); err != nil {
				bad(err.Error())
				return
			}
		}
		cred.apply, cred.set, cred.value = true, password != "", password
		next.PasswordSet = password != ""
	}
	if msg := config.IntegrationBrokerURLProblem(next.URL); msg != "" {
		bad(msg)
		return
	}
	payloadJSON, err := config.EncodeIntegrationBrokerPayload(next)
	if err != nil {
		h.writeInternalError(w, now, "encode integration.broker config payload", err)
		return
	}
	ac := authFromContext(r.Context())
	rec, revNo, err := h.writeShowConfigRevisionChecked(r, now, ac, config.IntegrationBrokerConfigKind, id, payloadJSON, precondition, map[string]any{},
		func(ctx context.Context, tx *store.Tx) error { return cred.run(ctx, tx) })
	if err != nil {
		var conflict *errConfigRevisionPreconditionFailed
		if errors.As(err, &conflict) {
			writeProblem(w, h.logger, now, previewConflictProblem(conflict))
			return
		}
		h.writeInternalError(w, now, "write integration.broker config revision", err)
		return
	}
	h.deps.IntegrationBrokers.Notify()
	obj := previewObjectRecord(config.IntegrationBrokerConfigKind, id, revNo, now)
	jsonWrite(w, v1.IntegrationBrokerResponse{ServerTime: formatTime(now), Broker: mapStoredIntegrationBroker(rec, obj, next)})
}

// errIntegrationBrokerInUse refuses a delete while a stored action still names the broker.
type errIntegrationBrokerInUse struct{ actions []string }

func (e *errIntegrationBrokerInUse) Error() string {
	return fmt.Sprintf("integration broker is used by actions %s", strings.Join(e.actions, ", "))
}

// handleDeleteIntegrationBroker removes a broker and its password, and refuses
// while any stored action still names it.
func (h *handlers) handleDeleteIntegrationBroker(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")
	if id == config.BuiltInIntegrationBrokerID {
		writeProblem(w, h.logger, now, builtInIntegrationBrokerProblem())
		return
	}
	if h.deps.IntegrationBrokersEnvVarSet {
		writeProblem(w, h.logger, now, integrationBrokerEditsBlockedProblem())
		return
	}
	precondition, problem := parseDeleteRevisionPrecondition(r)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	if problem := decodeConfigDeleteConfirmBody(r); problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	refuseIfUsed := func(ctx context.Context, tx *store.Tx) error {
		users, err := actionsNamingBroker(ctx, tx, id)
		if err != nil {
			return err
		}
		if len(users) > 0 {
			return &errIntegrationBrokerInUse{actions: users}
		}
		return tx.ClearCredential(ctx, config.IntegrationBrokerConfigKind, id, config.IntegrationBrokerPasswordCredentialField)
	}
	_, err := h.deleteConfigObjectRevision(r, now, authFromContext(r.Context()), config.IntegrationBrokerConfigKind, id, precondition, refuseIfUsed)
	var inUse *errIntegrationBrokerInUse
	if errors.As(err, &inUse) {
		writeProblem(w, h.logger, now, v1.Problem{
			Type: ProblemTypeConflict, Title: "Broker delete refused: actions still use this broker", Status: http.StatusConflict,
			Detail: fmt.Sprintf("Not deleted: actions %s still use this broker. Move them to another broker first.", strings.Join(inUse.actions, ", ")),
		})
		return
	}
	if err == nil {
		h.deps.IntegrationBrokers.Notify()
	}
	if h.writeConfigDeleteResponse(w, now, config.IntegrationBrokerConfigKind, id, err) {
		return
	}
	h.writeInternalError(w, now, "delete integration.broker config object", err)
}

// actionsNamingBroker returns the ids of stored mqtt actions whose target names broker.
func actionsNamingBroker(ctx context.Context, tx *store.Tx, brokerID string) ([]string, error) {
	objs, err := tx.ListConfigObjects(ctx, config.ShowActionConfigKind)
	if err != nil {
		return nil, err
	}
	var users []string
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := tx.GetConfigRevision(ctx, config.ShowActionConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			return nil, err
		}
		payload, err := decodeShowActionPayloadForRead(rev.PayloadJSON)
		if err != nil {
			continue
		}
		if payload.Target.Integration == config.ShowActionIntegrationMQTT && payload.Target.Broker == brokerID {
			users = append(users, obj.ID)
		}
	}
	return users, nil
}
