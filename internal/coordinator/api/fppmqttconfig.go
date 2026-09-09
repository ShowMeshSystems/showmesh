package api

// This file is Track G seam G-3 (ADR-039): the fpp.mqtt configuration
// write surface, mirroring config.go's fpp.endpoints shape (singleton
// object id, still-set-env-var 409, fail-closed-on-audit write). It
// differs from fpp.endpoints and resolumeinstancesconfig.go in one
// structural way: PUT is a PARTIAL UPDATE over five independent fields
// rather than one required array. That is required for the credential
// rule (ADR-039 decision 7): since GET never returns the password, a PUT
// that required every field present would make a naive GET-then-PUT round
// trip erase a credential the operator never saw. Generalizing "absent
// means keep the stored value" to every field, not only password, keeps
// the contract consistent rather than giving password special-cased
// forgiveness other fields lack.
//
// Reads here are gated identically to fpp.endpoints: config:write, never
// opened by [Options.CloseReads].

import (
	"bytes"
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

const maxFPPMQTTConfigRequestBodyBytes = 16 * 1024

// currentFPPMQTTConfig reads the active fpp.mqtt payload, or the zero
// value if nothing has ever been configured — the base a partial PUT
// applies its changes on top of (ADR-039 decision 5: an absent key means
// leave the stored value alone).
func (h *handlers) currentFPPMQTTConfig(ctx context.Context) (config.FPPMQTTConfig, bool, error) {
	obj, err := h.deps.Config.GetConfigObject(ctx, config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		return config.FPPMQTTConfig{}, false, nil
	}
	if err != nil {
		return config.FPPMQTTConfig{}, false, err
	}
	if obj.CurrentRevision == 0 {
		return config.FPPMQTTConfig{}, false, nil
	}
	rev, err := h.deps.Config.GetConfigRevision(ctx, config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID, obj.CurrentRevision)
	if err != nil {
		return config.FPPMQTTConfig{}, false, err
	}
	cfg, _, err := config.DecodeFPPMQTTPayload(rev.PayloadJSON)
	if err != nil {
		return config.FPPMQTTConfig{}, false, err
	}
	return cfg, true, nil
}

// handleGetFPPMQTTConfig serves GET /api/v1/config/fpp.mqtt: the active
// revision, its decoded non-secret payload, and the password's LIVE
// presence (read through [Dependencies.FPPMQTTSecret], never from the
// revision's own stored marker, so this always answers the current state
// of the secret file even in the rare window where the two could
// disagree — see handlePutFPPMQTTConfig's own note on write ordering).
// 404 when no revision has ever been activated, mirroring
// handleGetFPPEndpointsConfig's "not configured yet" vs. "migration
// deferred" distinction.
func (h *handlers) handleGetFPPMQTTConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	obj, err := h.deps.Config.GetConfigObject(ctx, config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		if h.deps.FPPMQTTMigrationDeferred {
			writeProblem(w, h.logger, now, resourceNotFoundProblem(
				"no fpp.mqtt configuration is stored, but this coordinator IS collecting from the broker named by "+
					"SHOWMESH_FPP_MQTT_BROKER_URL: the startup migration of those variables into this store could not be "+
					"persisted on this boot, and was deferred rather than refusing to start. Nothing was written, so "+
					"nothing here is stale or half-applied. Check this coordinator's startup log for the failure, fix "+
					"the data volume (usually full, read-only, or a damaged database), and restart: the migration is "+
					"retried on every start. Do NOT remove SHOWMESH_FPP_MQTT_* until it has succeeded — while the "+
					"migration is deferred those variables are the only copy of this configuration."))
			return
		}
		writeProblem(w, h.logger, now, resourceNotFoundProblem(
			"no fpp.mqtt configuration has been created yet; PUT one to create it"))
		return
	}
	if err != nil {
		h.writeInternalError(w, now, "get fpp.mqtt config object", err)
		return
	}
	if obj.CurrentRevision == 0 {
		writeProblem(w, h.logger, now, resourceNotFoundProblem(
			"no fpp.mqtt configuration has been created yet; PUT one to create it"))
		return
	}

	rev, err := h.deps.Config.GetConfigRevision(ctx, config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID, obj.CurrentRevision)
	if err != nil {
		h.writeInternalError(w, now, "get active fpp.mqtt config revision", err)
		return
	}

	cfg, _, err := config.DecodeFPPMQTTPayload(rev.PayloadJSON)
	if err != nil {
		h.writeInternalError(w, now, "decode fpp.mqtt config payload", err)
		return
	}

	passwordSet, err := h.deps.FPPMQTTSecret.HasFPPMQTTPassword(ctx)
	if err != nil {
		h.writeInternalError(w, now, "get fpp.mqtt password presence", err)
		return
	}

	jsonWrite(w, mapFPPMQTTConfigResponse(now, rev, obj, cfg, passwordSet))
}

// handleGetFPPMQTTConfigRevisions serves
// GET /api/v1/config/fpp.mqtt/revisions: every revision's metadata, newest
// first — 200 with an empty list when nothing has ever been created,
// mirroring handleGetFPPEndpointsConfigRevisions.
func (h *handlers) handleGetFPPMQTTConfigRevisions(w http.ResponseWriter, r *http.Request) {
	now := h.now()

	activeRevision := int64(0)
	obj, err := h.deps.Config.GetConfigObject(r.Context(), config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
	case err != nil:
		h.writeInternalError(w, now, "get fpp.mqtt config object", err)
		return
	default:
		activeRevision = obj.CurrentRevision
	}

	revs, err := h.deps.Config.ListConfigRevisions(r.Context(), config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID)
	if err != nil {
		h.writeInternalError(w, now, "list fpp.mqtt config revisions", err)
		return
	}

	out := make([]v1.ConfigRevisionMeta, 0, len(revs))
	for i := len(revs) - 1; i >= 0; i-- {
		out = append(out, mapConfigRevisionMeta(revs[i], activeRevision))
	}

	jsonWrite(w, v1.ConfigRevisionsResponse{
		ServerTime: formatTime(now),
		Kind:       config.FPPMQTTConfigKind,
		Revisions:  out,
	})
}

// fppMQTTPutFields is decodeFPPMQTTConfigPutBody's result: one *string (or
// map) per top-level field, nil meaning the key was absent from the
// request body — "leave the stored value alone" (ADR-039 decision 5). A
// non-nil Password of "" means an explicit clear (the wire accepts either
// `null` or `""` for that — see this function's own doc comment).
type fppMQTTPutFields struct {
	BrokerURL   *string
	Username    *string
	TopicPrefix *string
	Hosts       map[string]string
	HostsSet    bool
	Password    *string
}

var fppMQTTPutFieldNames = map[string]bool{
	"brokerURL": true, "username": true, "topicPrefix": true, "hosts": true, "password": true,
}

// decodeFPPMQTTConfigPutBody implements PUT /api/v1/config/fpp.mqtt's
// request-body contract: every top-level field is independently optional,
// and ABSENT/NULL/PRESENT are told apart per field exactly as
// decodeFPPEndpointsConfigPutBody already does for its one array field —
// see that function's own doc comment for the general "a bare struct
// field cannot tell these apart" reasoning, which applies per-field here.
//
//   - brokerURL/username/topicPrefix: absent keeps the stored value; null
//     is rejected (there is no stored-value-shaped "null" for a string);
//     an explicit "" is accepted and means "clear this field" (topicPrefix
//     "" resolves to the collector's own default, matching the env var's
//     existing behavior).
//   - hosts: absent keeps the stored value; null is rejected — mirroring
//     ConfigFPPEndpointsPayload's identical rule — and an explicit {} is
//     the only way to deliberately configure zero hosts.
//   - password: absent keeps the stored value AND its presence — this is
//     the rule ADR-039 decision 7 exists for for. Both null and "" are
//     accepted as an explicit clear, since a JSON client has no third way
//     to say "set it to nothing" for a scalar string. Any other value
//     sets a new password.
func decodeFPPMQTTConfigPutBody(body io.Reader) (fppMQTTPutFields, error) {
	var top map[string]json.RawMessage
	if err := json.NewDecoder(body).Decode(&top); err != nil {
		return fppMQTTPutFields{}, fmt.Errorf(`request body must be a JSON object matching `+
			`{"brokerURL":string,"username":string,"topicPrefix":string,"hosts":{"id":"HostName",...},"password":string}, `+
			`every field optional: %w`, err)
	}
	// A literal null decodes into a nil map with no error, which would
	// read as "every field absent" and mint a no-op revision.
	if top == nil {
		return fppMQTTPutFields{}, errors.New(`request body must be a JSON object, not null; send {} to change nothing`)
	}

	for key := range top {
		if !fppMQTTPutFieldNames[key] {
			return fppMQTTPutFields{}, fmt.Errorf(`unknown field %q; accepted top-level fields are `+
				`"brokerURL", "username", "topicPrefix", "hosts", "password"`, key)
		}
	}

	var out fppMQTTPutFields

	if raw, present := top["brokerURL"]; present {
		s, err := decodeFPPMQTTStringField(raw, "brokerURL", `pass "" to deliberately clear it`)
		if err != nil {
			return fppMQTTPutFields{}, err
		}
		out.BrokerURL = &s
	}
	if raw, present := top["username"]; present {
		s, err := decodeFPPMQTTStringField(raw, "username", `pass "" to deliberately clear it`)
		if err != nil {
			return fppMQTTPutFields{}, err
		}
		out.Username = &s
	}
	if raw, present := top["topicPrefix"]; present {
		s, err := decodeFPPMQTTStringField(raw, "topicPrefix", `pass "" to reset it to the default`)
		if err != nil {
			return fppMQTTPutFields{}, err
		}
		out.TopicPrefix = &s
	}
	if raw, present := top["hosts"]; present {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fppMQTTPutFields{}, errors.New(`"hosts" must not be null; pass {} to deliberately configure zero ` +
				`hosts, or omit the key to leave it unchanged`)
		}
		var m map[string]string
		if err := json.Unmarshal(raw, &m); err != nil {
			return fppMQTTPutFields{}, fmt.Errorf(`"hosts" must be an object of {"instanceId":"HostName",...}: %w`, err)
		}
		if m == nil {
			m = map[string]string{}
		}
		out.Hosts = m
		out.HostsSet = true
	}
	if raw, present := top["password"]; present {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			empty := ""
			out.Password = &empty
		} else {
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return fppMQTTPutFields{}, fmt.Errorf(`"password" must be a string, or null to clear it: %w`, err)
			}
			out.Password = &s
		}
	}

	return out, nil
}

func decodeFPPMQTTStringField(raw json.RawMessage, field, clearHint string) (string, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", fmt.Errorf(`%q must not be null; %s, or omit the key to leave it unchanged`, field, clearHint)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf(`%q must be a string: %w`, field, err)
	}
	return s, nil
}

// handlePutFPPMQTTConfig serves PUT /api/v1/config/fpp.mqtt: applies a
// partial update over the current stored configuration, validates,
// appends an immutable revision, activates it, and returns the new active
// revision — mirroring handlePutFPPEndpointsConfig's still-set-env 409
// and fail-closed-on-audit posture.
//
// The broker password is written to [Dependencies.FPPMQTTSecret] BEFORE
// the config_revisions write, not inside the same SQL transaction (the
// secret lives in a different storage system entirely — see
// internal/coordinator/config/fppmqttsecret.go). If the secret write
// fails, nothing below it runs and no revision is created, so a refused
// write never leaves the store and the secret file disagreeing about
// whether it happened. The narrower remaining risk — the secret write
// succeeds and the SQL transaction fails afterward for an unrelated
// reason — is the same class of cross-system risk this codebase already
// accepts wherever a write spans two storage systems (see ADR-024
// decision 11's own scope: "a coordinator-local state change").
func (h *handlers) handlePutFPPMQTTConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)

	if h.deps.FPPMQTTEnvVarSet {
		if h.deps.FPPMQTTMigrationDeferred {
			writeProblem(w, h.logger, now, fppMQTTMigrationDeferredProblem())
			return
		}
		writeProblem(w, h.logger, now, fppMQTTEnvVarSetProblem())
		return
	}

	precondition, precondProblem := parseRevisionPrecondition(r)
	if precondProblem != nil {
		writeProblem(w, h.logger, now, *precondProblem)
		return
	}

	fields, err := decodeFPPMQTTConfigPutBody(io.LimitReader(r.Body, maxFPPMQTTConfigRequestBodyBytes+1))
	if err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
		return
	}

	current, _, err := h.currentFPPMQTTConfig(ctx)
	if err != nil {
		h.writeInternalError(w, now, "get current fpp.mqtt config for partial update", err)
		return
	}

	next := current
	if fields.BrokerURL != nil {
		next.BrokerURL = *fields.BrokerURL
	}
	if fields.Username != nil {
		next.Username = *fields.Username
	}
	if fields.TopicPrefix != nil {
		next.TopicPrefix = *fields.TopicPrefix
	}
	if fields.HostsSet {
		next.Hosts = fields.Hosts
	}

	fppEndpoints, err := currentFPPEndpoints(ctx, h.deps.FPP)
	if err != nil {
		h.writeInternalError(w, now, "get current fpp.endpoints for fpp.mqtt collision check", err)
		return
	}
	if err := config.ValidateFPPMQTTConfigKind(next, fppEndpoints); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
		return
	}

	if fields.Password != nil {
		if *fields.Password == "" {
			if err := h.deps.FPPMQTTSecret.ClearFPPMQTTPassword(ctx); err != nil {
				h.writeInternalError(w, now, "clear fpp.mqtt password", err)
				return
			}
		} else if err := h.deps.FPPMQTTSecret.SetFPPMQTTPassword(ctx, *fields.Password); err != nil {
			h.writeInternalError(w, now, "set fpp.mqtt password", err)
			return
		}
	}

	passwordSet, err := h.deps.FPPMQTTSecret.HasFPPMQTTPassword(ctx)
	if err != nil {
		h.writeInternalError(w, now, "get fpp.mqtt password presence", err)
		return
	}

	payloadJSON, err := config.EncodeFPPMQTTPayload(next, passwordSet)
	if err != nil {
		h.writeInternalError(w, now, "encode fpp.mqtt config payload", err)
		return
	}

	var (
		activated      store.ConfigRevisionRecord
		nextRevisionNo int64
	)
	writeErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		currentRevision := int64(0)
		nextRevisionNo = 1
		if obj, gerr := tx.GetConfigObject(ctx, config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID); gerr == nil {
			currentRevision = obj.CurrentRevision
			nextRevisionNo = obj.CurrentRevision + 1
		} else if !errors.Is(gerr, store.ErrConfigObjectNotFound) {
			return identity.AuditEntry{}, gerr
		}
		if err := checkRevisionPrecondition(config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID, precondition, currentRevision); err != nil {
			return identity.AuditEntry{}, err
		}

		rec, cerr := tx.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
			Kind:                   config.FPPMQTTConfigKind,
			ObjectID:               config.FPPMQTTConfigObjectID,
			Revision:               nextRevisionNo,
			PayloadJSON:            payloadJSON,
			CreatedByPrincipalID:   ac.result.Principal.ID,
			CreatedByPrincipalName: ac.result.Principal.Name,
			Source:                 config.FPPMQTTSourceAPI,
		})
		if cerr != nil {
			return identity.AuditEntry{}, cerr
		}

		if _, aerr := tx.ActivateConfigRevision(ctx, config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID, nextRevisionNo); aerr != nil {
			return identity.AuditEntry{}, aerr
		}

		activated = rec

		return identity.AuditEntry{
			Timestamp:     now,
			PrincipalID:   ac.result.Principal.ID,
			PrincipalName: ac.result.Principal.Name,
			Form:          ac.result.Form,
			CredentialID:  ac.result.CredentialID,
			ClientAddr:    h.clientAddr(r),
			Action:        "config.write",
			Target:        config.FPPMQTTConfigKind,
			Params: map[string]any{
				"revision":  nextRevisionNo,
				"hostCount": len(next.Hosts),
			},
			Kind: identity.AuditAdmin,
		}, nil
	})
	if writeErr != nil {
		var conflict *errConfigRevisionPreconditionFailed
		if errors.As(writeErr, &conflict) {
			writeProblem(w, h.logger, now, configRevisionConflictProblem(conflict))
			return
		}
		h.writeInternalError(w, now, "write fpp.mqtt config revision", writeErr)
		return
	}

	jsonWrite(w, mapFPPMQTTConfigResponse(now, activated, store.ConfigObjectRecord{
		Kind: config.FPPMQTTConfigKind, ID: config.FPPMQTTConfigObjectID,
		CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, next, passwordSet))
}

// fppMQTTRestartRequiredReason mirrors resolumeInstancesRestartRequiredReason:
// applies without a restart from the start (ADR-036 via ADR-039 decision 6).
const fppMQTTRestartRequiredReason = "this change is already in effect: the FPP MQTT collector follows this " +
	"configuration within about ten seconds. No restart is needed."

func mapFPPMQTTConfigResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, cfg config.FPPMQTTConfig, passwordSet bool) v1.FPPMQTTConfigResponse {
	hosts := cfg.Hosts
	if hosts == nil {
		hosts = map[string]string{}
	}
	return v1.FPPMQTTConfigResponse{
		ServerTime: formatTime(now),
		Kind:       config.FPPMQTTConfigKind,
		Revision:   rev.Revision,
		Payload: v1.ConfigFPPMQTTPayload{
			BrokerURL:   cfg.BrokerURL,
			Username:    cfg.Username,
			TopicPrefix: cfg.TopicPrefix,
			Hosts:       hosts,
			PasswordSet: passwordSet,
		},
		UpdatedAt:              formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
		RestartRequired:        false,
		RestartRequiredReason:  fppMQTTRestartRequiredReason,
	}
}
