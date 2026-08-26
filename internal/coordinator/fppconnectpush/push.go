// Package fppconnectpush is ADR-039/ADR-036's "applies without a restart"
// half for the show.surface, show.active, show, and fppconnect.settings
// configuration kinds, as they bear on one node's FPP Connect advertisement
// (ADR-044 decision 5, IDENTIFIER-REGISTER.md): every node hello, and every
// write to any of those four kinds, pushes the resolved
// "fppconnect.configure" state to the affected node(s) over the existing
// cmd topic, so a node that was offline during a write converges once it
// reconnects. The agent's paired "fppconnect.configure" operation
// (internal/agent) is the only way an agent ever learns its channel
// ranges, its active show, its show name list, and its byte caps. This
// mirrors internal/coordinator/audioconfigpush's shape exactly, one
// configuration surface over, see that package's own doc comment.
package fppconnectpush

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/internal/fppconnect"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// ConfigStore is this package's read dependency, [store.Store] already
// satisfies it with no adapter, matching audioconfigpush.ConfigStore's
// identical property one file over. ListConfigObjects is the one addition
// audioconfigpush's own ConfigStore does not need: resolving one node's
// push means enumerating every "show.surface" object to find the ones
// pointing at it, and every "show" object to build the show name list;
// neither is keyed by node id the way "audio.node" is.
type ConfigStore interface {
	GetConfigObject(ctx context.Context, kind, id string) (store.ConfigObjectRecord, error)
	GetConfigRevision(ctx context.Context, kind, id string, revision int64) (store.ConfigRevisionRecord, error)
	ListConfigObjects(ctx context.Context, kind string) ([]store.ConfigObjectRecord, error)
}

// Publisher is this package's MQTT publish dependency, *broker.
// BrokerManager already satisfies it with no adapter, matching
// audioconfigpush.Publisher's identical shape.
type Publisher interface {
	Publish(ctx context.Context, topic string, qos byte, retain bool, payload []byte) error
}

// pushIssuer names this package's own commands in every audit trail and
// log line a node's CommandHandler produces for them, never a real
// operator principal, since nothing here was issued by one. Matches
// audioconfigpush.pushIssuer's identical string.
const pushIssuer = "showmesh-coordinator-config-push"

// schemaVersion is "fppconnect.configure"'s params.schema value
// (IDENTIFIER-REGISTER.md, ADR-044).
const schemaVersion = "showmesh.node.fppconnect.config/v1"

// resolvedFPPConnectState is what [resolveForNode] computes for one node:
// everything "fppconnect.configure" pushes, plus revisions, the raw
// ingredient [idempotencyKeyFor] hashes.
type resolvedFPPConnectState struct {
	ChannelRanges string
	ActiveShow    *string
	ShowNames     []string
	Settings      config.FPPConnectSettingsPayload

	// revisions is every show.surface object's own "kind/id@rev" tuple
	// (unconditionally, not filtered to surfaces currently on this node;
	// see nodeChannelRanges' own doc comment on why filtering here would
	// reintroduce the exact bug this field exists to prevent), plus show,
	// show.active, and fppconnect.settings, unsorted as gathered.
	// [idempotencyKeyFor] hashes this AND the resolved content above: a
	// write that reverts content to an earlier value (show.active set to
	// A, then B, then back to A) still produces a fresh, never-before-seen
	// key here, because ADR-009 revisions are immutable and monotonically
	// numbered, so two DIFFERENT revisions can never collide even when
	// they happen to carry identical payloads. Relying on content alone
	// would make the third (A-again) push reuse the FIRST push's key,
	// which the agent's capacity-bounded idempotency cache
	// (internal/agent/command.go) would then treat as an exact replay and
	// silently refuse to re-apply, the node would keep advertising B's
	// ranges forever, while the coordinator's push reports success
	// because IT never re-checks what the node did with a "duplicate" it
	// never actually executed a second time.
	revisions []string
}

// ToNode resolves nodeID's current channel ranges, active show, show name
// list, and fppconnect.settings, and publishes them as one
// "fppconnect.configure" command. A node with no configured show.surface
// pushes an empty channelRanges string, never an error and never "0-0"
// (RES-003 section 10.1). If ANY show.surface on this node fails to
// format (including because the combined string would exceed the ping's
// 120-byte ranges field, see pkg/multisync's own wire-layout doc
// comment), the WHOLE channelRanges string is dropped to "" and logged
// through logger (never nil-checked away: every caller of this package
// passes one, matching BestEffort's own contract) rather than failing the
// whole push, this node's active show, show names, and byte caps are
// still good pushes even when its channel ranges are not.
func ToNode(ctx context.Context, cs ConfigStore, pub Publisher, now func() time.Time, nodeID string, logger *slog.Logger) error {
	resolved, err := resolveForNode(ctx, cs, nodeID, logger)
	if err != nil {
		return fmt.Errorf("resolve fppconnect state for %s: %w", nodeID, err)
	}

	params := map[string]any{
		"schema":        schemaVersion,
		"channelRanges": resolved.ChannelRanges,
		"activeShow":    resolved.ActiveShow,
		"showNames":     resolved.ShowNames,
		"settings": map[string]any{
			"enabled":          resolved.Settings.Enabled,
			"maxFileBytes":     resolved.Settings.MaxFileBytes,
			"maxAssetDirBytes": resolved.Settings.MaxAssetDirBytes,
		},
	}
	idempotencyKey := idempotencyKeyFor(nodeID, resolved)
	return publish(ctx, pub, now, nodeID, "fppconnect.configure", idempotencyKey, params)
}

// BestEffort calls [ToNode] and logs any failure rather than propagating
// it: a push that cannot reach a node right now is not a reason to fail
// the write or the hello that triggered it. The node converges on its
// next successful push, its next hello, or the next write to any of the
// four kinds this package watches. Matches audioconfigpush.BestEffort
// exactly.
func BestEffort(ctx context.Context, cs ConfigStore, pub Publisher, now func() time.Time, nodeID string, logger *slog.Logger) {
	if err := ToNode(ctx, cs, pub, now, nodeID, logger); err != nil && logger != nil {
		logger.Warn("fppconnect config push failed", "node_id", nodeID, "error", err)
	}
}

// resolveForNode reads every input "fppconnect.configure" carries for
// nodeID, out of cs.
func resolveForNode(ctx context.Context, cs ConfigStore, nodeID string, logger *slog.Logger) (resolvedFPPConnectState, error) {
	var revisions []string

	ranges, surfaceRevisions, err := nodeChannelRanges(ctx, cs, nodeID)
	if err != nil {
		return resolvedFPPConnectState{}, fmt.Errorf("collect show.surface channel ranges: %w", err)
	}
	revisions = append(revisions, surfaceRevisions...)

	channelRanges := ""
	if len(ranges) > 0 {
		formatted, ferr := fppconnect.FormatChannelRanges(ranges)
		if ferr != nil {
			// Never a "0-0" and never a fatal push failure: this drops the
			// WHOLE channelRanges string to "", not just the offending
			// surface's own contribution, there is no way to publish a
			// partial sparse window without risking gaps a real xLights
			// render would silently skip. This node's active show, show
			// names, and byte caps are still good pushes.
			if logger != nil {
				logger.Warn("fppconnect channel range formatting failed; pushing an empty range instead", "node_id", nodeID, "error", ferr)
			}
		} else {
			channelRanges = formatted
		}
	}

	showNamesByID, showRevisions, err := allShowNames(ctx, cs)
	if err != nil {
		return resolvedFPPConnectState{}, fmt.Errorf("list show names: %w", err)
	}
	revisions = append(revisions, showRevisions...)
	showNames := make([]string, 0, len(showNamesByID))
	for _, name := range showNamesByID {
		showNames = append(showNames, name)
	}
	sort.Strings(showNames)

	activeShow, activeShowRevision, err := activeShowName(ctx, cs, showNamesByID)
	if err != nil {
		return resolvedFPPConnectState{}, fmt.Errorf("resolve active show: %w", err)
	}
	revisions = append(revisions, activeShowRevision)

	settings, settingsRevision, err := currentFPPConnectSettings(ctx, cs)
	if err != nil {
		return resolvedFPPConnectState{}, fmt.Errorf("resolve fppconnect.settings: %w", err)
	}
	revisions = append(revisions, settingsRevision)

	return resolvedFPPConnectState{
		ChannelRanges: channelRanges,
		ActiveShow:    activeShow,
		ShowNames:     showNames,
		Settings:      settings,
		revisions:     revisions,
	}, nil
}

// nodeChannelRanges collects every "show.surface" object whose "node"
// field equals nodeID, out of its currently active revision, for the
// RANGES it returns. The REVISIONS it returns are deliberately
// unfiltered, every show.surface object's own "show.surface/{id}@
// {revision}" tuple, whether or not it currently names nodeID.
//
// This asymmetry is load-bearing, not an oversight: a surface moved OFF
// nodeID changes nodeID's resolved ranges (it loses that surface's
// contribution) without changing nodeID's set of CONTRIBUTING revisions
// at all, the moved surface's new revision now names a different node,
// so a revision list filtered to "surfaces currently on nodeID" is
// EXACTLY as empty after the move as it was before the surface ever
// existed. [idempotencyKeyFor] would then mint the identical key for
// both states, and the agent's capacity-bounded idempotency cache
// (internal/agent/command.go) would treat the vacating push as a replay
// of the original hello and silently refuse to re-apply it, leaving the
// node advertising a range it no longer owns. Including every
// show.surface object's revision regardless of ownership means a move
// always changes at least one entry in the fingerprint, on both the
// gaining and the losing node's push.
//
// Decode failures propagate (a stored payload this store already
// validated at write time that fails to decode is a store-integrity
// condition, not an expected state, matching listShowSurfaceSummaries'
// identical choice one package over).
func nodeChannelRanges(ctx context.Context, cs ConfigStore, nodeID string) ([]fppconnect.ChannelRange, []string, error) {
	objs, err := cs.ListConfigObjects(ctx, config.ShowSurfaceConfigKind)
	if err != nil {
		return nil, nil, err
	}
	var out []fppconnect.ChannelRange
	var revisions []string
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := cs.GetConfigRevision(ctx, config.ShowSurfaceConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			return nil, nil, fmt.Errorf("get active show.surface config revision for %q: %w", obj.ID, err)
		}
		var payload config.ShowSurfacePayload
		if err := json.Unmarshal([]byte(rev.PayloadJSON), &payload); err != nil {
			return nil, nil, fmt.Errorf("decode show.surface config payload for %q: %w", obj.ID, err)
		}
		// Unconditional: see this function's own doc comment on why the
		// fingerprint must include every show.surface object, not only
		// the ones currently naming nodeID.
		revisions = append(revisions, fmt.Sprintf("show.surface/%s@%d", obj.ID, obj.CurrentRevision))
		if payload.Node != nodeID {
			continue
		}
		out = append(out, fppconnect.ChannelRange{
			StartChannel: payload.ChannelRange.StartChannel,
			ChannelCount: payload.ChannelRange.ChannelCount,
		})
	}
	return out, revisions, nil
}

// allShowNames reads every "show" object's current revision and returns
// its display name keyed by the show's own config object id, plus each
// contributing object's own "show/{id}@{revision}" tuple.
func allShowNames(ctx context.Context, cs ConfigStore) (map[string]string, []string, error) {
	objs, err := cs.ListConfigObjects(ctx, config.ShowConfigKind)
	if err != nil {
		return nil, nil, err
	}
	out := make(map[string]string, len(objs))
	var revisions []string
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := cs.GetConfigRevision(ctx, config.ShowConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			return nil, nil, fmt.Errorf("get active show config revision for %q: %w", obj.ID, err)
		}
		var payload config.ShowPayload
		if err := json.Unmarshal([]byte(rev.PayloadJSON), &payload); err != nil {
			return nil, nil, fmt.Errorf("decode show config payload for %q: %w", obj.ID, err)
		}
		out[obj.ID] = payload.Name
		revisions = append(revisions, fmt.Sprintf("show/%s@%d", obj.ID, obj.CurrentRevision))
	}
	return out, revisions, nil
}

// activeShowName reads "show.active" and resolves it to the active show's
// display name, out of showNamesByID ([allShowNames]'s own result, so this
// never re-reads the "show" kind a second way), plus the "show.active"
// object's own revision tuple for [idempotencyKeyFor] (a fixed sentinel
// when nothing has ever been written, since there is no revision number to
// report). Absent, null and empty stay three different things (ADR-039
// decision 5): nothing ever written, or an object with no active
// revision, is nil, never omitted from the push and never "". A
// show.active pointer that names a show id with no active "show" revision
// (a stale pointer left behind by a store inconsistency elsewhere) is ALSO
// reported as nil rather than as the raw, unresolvable id: a wrong silent
// guess (a value that appears in no /api/playlists entry an operator could
// ever select) is worse than an honest "cannot resolve" (ADR-044 decision
// 8's own standing rule, applied here to a name this function cannot
// produce rather than to an upload it cannot bind).
func activeShowName(ctx context.Context, cs ConfigStore, showNamesByID map[string]string) (*string, string, error) {
	obj, err := cs.GetConfigObject(ctx, config.ShowActiveConfigKind, config.ShowActiveObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		return nil, "show.active/none@0", nil
	case err != nil:
		return nil, "", err
	case obj.CurrentRevision == 0:
		return nil, "show.active/none@0", nil
	}

	revision := fmt.Sprintf("show.active/%s@%d", config.ShowActiveObjectID, obj.CurrentRevision)

	rev, err := cs.GetConfigRevision(ctx, config.ShowActiveConfigKind, config.ShowActiveObjectID, obj.CurrentRevision)
	if err != nil {
		return nil, "", err
	}
	var payload config.ShowActivePayload
	if err := json.Unmarshal([]byte(rev.PayloadJSON), &payload); err != nil {
		return nil, "", fmt.Errorf("decode show.active config payload: %w", err)
	}
	name, ok := showNamesByID[payload.Show]
	if !ok {
		return nil, revision, nil
	}
	return &name, revision, nil
}

// currentFPPConnectSettings mirrors audioconfigpush's own pushSettings
// resolution: [config.FPPConnectSettingsDefaultPayload] when nothing has
// ever been written, the decoded stored value otherwise. Also returns the
// object's own revision tuple for [idempotencyKeyFor] (a fixed sentinel
// when nothing has ever been written).
func currentFPPConnectSettings(ctx context.Context, cs ConfigStore) (config.FPPConnectSettingsPayload, string, error) {
	obj, err := cs.GetConfigObject(ctx, config.FPPConnectSettingsConfigKind, config.FPPConnectSettingsConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		return config.FPPConnectSettingsDefaultPayload, "fppconnect.settings/default@0", nil
	case err != nil:
		return config.FPPConnectSettingsPayload{}, "", err
	case obj.CurrentRevision == 0:
		return config.FPPConnectSettingsDefaultPayload, "fppconnect.settings/default@0", nil
	}

	rev, err := cs.GetConfigRevision(ctx, config.FPPConnectSettingsConfigKind, config.FPPConnectSettingsConfigObjectID, obj.CurrentRevision)
	if err != nil {
		return config.FPPConnectSettingsPayload{}, "", err
	}
	payload, verr := config.DecodeFPPConnectSettingsPayload(rev.PayloadJSON)
	if verr != nil {
		return config.FPPConnectSettingsPayload{}, "", fmt.Errorf("decode stored fppconnect.settings payload: %s", verr.Error())
	}
	return payload, fmt.Sprintf("fppconnect.settings/%s@%d", config.FPPConnectSettingsConfigObjectID, obj.CurrentRevision), nil
}

// idempotencyKeyFor changes whenever any contributing config object's own
// revision changes, or the resolved content differs, and stays stable
// when nothing changed: a hash of BOTH the revision fingerprint (sorted,
// for stability against ListConfigObjects' unspecified order) and the
// resolved content (channelRanges, activeShow, showNames, settings).
// Revisions are the primary signal, ADR-009 revisions are immutable and
// monotonically numbered per object, so two DIFFERENT revisions can never
// collide even when they happen to carry identical payloads (a
// show.active write of A, then B, then back to A), and
// nodeChannelRanges' own doc comment explains why the revision list must
// be unfiltered for a moved surface to register at all. Content is hashed
// too as a second, independent signal: belt-and-suspenders against any
// future path that resolves a revision fingerprint without a
// corresponding content change, which content hashing alone would still
// catch.
func idempotencyKeyFor(nodeID string, resolved resolvedFPPConnectState) string {
	sortedRevisions := make([]string, len(resolved.revisions))
	copy(sortedRevisions, resolved.revisions)
	sort.Strings(sortedRevisions)

	sortedShowNames := make([]string, len(resolved.ShowNames))
	copy(sortedShowNames, resolved.ShowNames)
	sort.Strings(sortedShowNames)

	activeShow := "\x00null"
	if resolved.ActiveShow != nil {
		activeShow = *resolved.ActiveShow
	}

	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00", nodeID)
	for _, r := range sortedRevisions {
		_, _ = fmt.Fprintf(h, "rev:%s\x00", r)
	}
	_, _ = fmt.Fprintf(h, "channelRanges:%s\x00", resolved.ChannelRanges)
	_, _ = fmt.Fprintf(h, "activeShow:%s\x00", activeShow)
	for _, name := range sortedShowNames {
		_, _ = fmt.Fprintf(h, "showName:%s\x00", name)
	}
	_, _ = fmt.Fprintf(h, "settings:%v\x00%d\x00%d", resolved.Settings.Enabled, resolved.Settings.MaxFileBytes, resolved.Settings.MaxAssetDirBytes)

	return fmt.Sprintf("fppconnect.configure/%s/%s", nodeID, hex.EncodeToString(h.Sum(nil))[:16])
}

// publish builds and sends one CmdPayload. CommandID is a fresh UUID on
// every call (topic-shape rules require it); IdempotencyKey is
// deterministic per resolved state instead, matching audioconfigpush.
// publish exactly.
func publish(ctx context.Context, pub Publisher, now func() time.Time, nodeID, action, idempotencyKey string, params map[string]any) error {
	topic, err := mqttproto.CmdTopic(nodeID)
	if err != nil {
		return err
	}
	t := now()
	cmd := mqttproto.CmdPayload{
		CommandID: uuid.NewString(), IdempotencyKey: idempotencyKey, Action: action,
		Target: mqttproto.CmdTarget{Kind: "node", ID: nodeID}, Params: params,
		Issuer:             mqttproto.CmdIssuer{PrincipalID: pushIssuer, PrincipalName: pushIssuer},
		ConfirmationMethod: "evidence",
	}
	env, err := mqttproto.NewCmdEnvelope(func() time.Time { return t }, nodeID, cmd)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return pub.Publish(ctx, topic, mqttproto.CmdDeliveryPolicy.QoS, mqttproto.CmdDeliveryPolicy.Retain, raw)
}
