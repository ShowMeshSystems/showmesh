package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/clock"
)

// This file wires "node.clock.configure" — the coordinator's ADR-039/
// ADR-036 configuration push for the node.clock kind (internal/coordinator
// side: a package mirroring internal/coordinator/audioconfigpush,
// following that exact precedent per the orchestrator's own ruling on
// this seam), the only way this agent ever learns which PTP provider to
// run. Mirrors internal/agent/audionodeops.go's audioBinding shape
// exactly, one configuration kind over.

// clockConfigSchema is this operation's payload schema string
// (docs/build/IDENTIFIER-REGISTER.md's node.clock.configure reservation:
// "showmesh.node.clock.config/v1", the ADR-044 convention), carried
// inside params rather than as the envelope's own Schema field: the
// envelope schema is [mqttproto.SchemaNodeCmdV1] for every command
// regardless of action, so a command whose PARAMS shape itself needs a
// version tag (a whole-document push, unlike audio.node.configure's flat
// scalar fields) carries one explicitly.
const clockConfigSchema = "showmesh.node.clock.config/v1"

// clockNodeConfig is "node.clock.configure"'s params shape, mirroring
// internal/coordinator/config.NodeClockPayload's JSON tags exactly —
// independently reproduced, not imported: this package has no
// coordinator dependency, matching audioNodeConfig's identical rule.
type clockNodeConfig struct {
	Schema               string `json:"schema"`
	Provider             string `json:"provider"`
	Interface            string `json:"interface"`
	Domain               int    `json:"domain"`
	ClientOnly           bool   `json:"clientOnly,omitempty"`
	HoldoverLimitSeconds int    `json:"holdoverLimitSeconds,omitempty"`
	Priority1            int    `json:"priority1,omitempty"`
	HardwareTimestamping bool   `json:"hardwareTimestamping,omitempty"`
	ExternalUDSAddress   string `json:"externalUdsAddress,omitempty"`
	FPPBaseURL           string `json:"fppBaseUrl,omitempty"`
	Revision             int64  `json:"revision"`
}

// clockBinding holds this node's most recently ACCEPTED node.clock
// configuration and rebuilds its wrapped [clock.Manager] once per
// genuinely newer revision — never for a refused or replayed one, mirroring
// [audioBinding.applyNode] exactly. Unlike audioBinding, a rebuild here can
// itself fail (the managed provider's ownership pre-check — RES-019
// section 5.3), and that failure must be reported back as the command's own
// refusal rather than silently leaving the node running its previous
// configuration: see [clockBinding.configureNode].
type clockBinding struct {
	mgr *clock.Manager

	haveConfig bool
	revision   int64
	cfg        clockNodeConfig
}

func newClockBinding(mgr *clock.Manager) *clockBinding {
	return &clockBinding{mgr: mgr}
}

// applyConfig refuses p.Revision older than the currently held one, is a
// no-op on an exact replay of the current revision, and otherwise rebuilds
// the wrapped Manager via [clock.Manager.SetConfig]. On a Manager error
// (the ownership pre-check refusing, or an unsupported provider), the
// PREVIOUSLY held revision/cfg are left in place — this node keeps
// whatever provider it already had running rather than being left with
// none — and the error is returned so the command reports Confirmed=false
// with it.
func (b *clockBinding) applyConfig(ctx context.Context, p clockNodeConfig) error {
	if b.haveConfig {
		if p.Revision < b.revision {
			return fmt.Errorf("node.clock.configure: revision %d is older than the currently held revision %d; refused", p.Revision, b.revision)
		}
		if p.Revision == b.revision {
			return nil
		}
	}

	cfg := clock.Config{
		Provider:             clock.ProviderKind(p.Provider),
		Interface:            p.Interface,
		Domain:               p.Domain,
		ClientOnly:           p.ClientOnly,
		HoldoverLimit:        time.Duration(p.HoldoverLimitSeconds) * time.Second,
		Priority1:            p.Priority1,
		HardwareTimestamping: p.HardwareTimestamping,
		ExternalUDSAddress:   p.ExternalUDSAddress,
		FPPBaseURL:           p.FPPBaseURL,
	}
	if err := b.mgr.SetConfig(ctx, cfg); err != nil {
		return fmt.Errorf("node.clock.configure: %w", err)
	}

	b.cfg = p
	b.revision = p.Revision
	b.haveConfig = true
	return nil
}

func (b *clockBinding) currentRevision() (revision int64, have bool) {
	return b.revision, b.haveConfig
}

var clockConfigureKnownKeys = map[string]bool{
	"schema": true, "provider": true, "interface": true, "domain": true,
	"clientOnly": true, "holdoverLimitSeconds": true, "priority1": true,
	"hardwareTimestamping": true, "externalUdsAddress": true, "fppBaseUrl": true,
	"revision": true,
}

// decodeClockNodeConfig validates params' shape against
// clockConfigureKnownKeys, its required fields, and this operation's own
// schema string, then decodes it via the same JSON round-trip pattern
// [decodeAudioNodeConfig] uses.
func decodeClockNodeConfig(params map[string]any) (clockNodeConfig, error) {
	const action = "node.clock.configure"
	if err := rejectUnknownKeys(action, params, clockConfigureKnownKeys); err != nil {
		return clockNodeConfig{}, err
	}
	for _, field := range []string{"schema", "provider", "interface", "domain", "revision"} {
		if _, ok := params[field]; !ok {
			return clockNodeConfig{}, fmt.Errorf("%s: params.%s is required", action, field)
		}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return clockNodeConfig{}, fmt.Errorf("%s: encoding params: %w", action, err)
	}
	var p clockNodeConfig
	if err := json.Unmarshal(raw, &p); err != nil {
		return clockNodeConfig{}, fmt.Errorf("%s: params did not decode: %w", action, err)
	}
	if p.Schema != clockConfigSchema {
		return clockNodeConfig{}, fmt.Errorf("%s: params.schema %q is not %q, the only version this agent understands", action, p.Schema, clockConfigSchema)
	}
	switch p.Provider {
	case "managed", "external", "fpp":
	default:
		return clockNodeConfig{}, fmt.Errorf("%s: params.provider %q must be \"managed\", \"external\", or \"fpp\"", action, p.Provider)
	}
	if p.Interface == "" {
		return clockNodeConfig{}, fmt.Errorf("%s: params.interface must be a non-empty string", action)
	}
	if p.Domain < 0 || p.Domain > 255 {
		return clockNodeConfig{}, fmt.Errorf("%s: params.domain must be between 0 and 255", action)
	}
	if p.Provider == "fpp" && p.FPPBaseURL == "" {
		return clockNodeConfig{}, fmt.Errorf("%s: params.fppBaseUrl is required when provider is \"fpp\"", action)
	}
	if p.Revision < 0 {
		return clockNodeConfig{}, fmt.Errorf("%s: params.revision must not be negative", action)
	}
	return p, nil
}

// configureNode is the OperationFunc for "node.clock.configure". Evidence
// is the binding's own read-back revision — a genuine re-read, matching
// audioBinding.configureNode's identical confirmation pattern. Unlike
// that operation, a rebuild here can itself fail (see
// [clockBinding.applyConfig]'s doc comment); that failure is returned as
// this OperationFunc's own error, which [CommandHandler.HandleMessage]
// reports as a refused command with the failure's own reason, never a
// silent partial success.
func (b *clockBinding) configureNode(ctx context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
	p, err := decodeClockNodeConfig(params)
	if err != nil {
		return OperationResult{}, err
	}
	executedAt := now()
	if err := b.applyConfig(ctx, p); err != nil {
		return OperationResult{}, err
	}
	observedAt := now()
	revision, _ := b.currentRevision()
	return OperationResult{
		Confirmed:  revision == p.Revision,
		Signal:     "node.clock.node_config_revision",
		Value:      revision,
		ExecutedAt: executedAt, ObservedAt: observedAt,
	}, nil
}

// clockConfigureOperations builds the one allowlist entry against b. b is
// nil-safe, matching [audioNodeConfigureOperations]'s identical rule: a
// node started with no clock manager wired never wires this either.
func clockConfigureOperations(b *clockBinding) map[string]OperationFunc {
	if b == nil {
		return nil
	}
	return map[string]OperationFunc{
		"node.clock.configure": b.configureNode,
	}
}
