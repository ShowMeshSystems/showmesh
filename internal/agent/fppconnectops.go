package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// This file wires "fppconnect.configure" (Track E phase 2 seam FC1a,
// ADR-044 decision 5) — the coordinator's fppconnectpush push
// (internal/coordinator/fppconnectpush is the coordinator half), the only
// way this agent ever learns its channel ranges, its active show, its
// show name list, and the two ingestion byte caps. Mirrors
// audionodeops.go's "audio.node.configure"/"audio.settings.configure"
// wiring one configuration surface over: decode, validate, apply to the
// holder (fppconnectstate.go), persist, and acknowledge from a genuine
// read-back.

// fppConnectConfigureSchema is the only params.schema value this
// operation accepts (IDENTIFIER-REGISTER.md, ADR-044) — independently
// reproduced from internal/coordinator/fppconnectpush's identical
// constant, matching this codebase's established wire-boundary
// convention of no shared import across the coordinator/agent boundary.
const fppConnectConfigureSchema = "showmesh.node.fppconnect.config/v1"

// fppConnectConfigureParams is "fppconnect.configure"'s params shape.
// ActiveShow is a pointer because null and a name are distinct wire
// values (ADR-039 decision 5): nil is "no active show", a non-nil string
// (including "") is a named active show.
type fppConnectConfigureParams struct {
	Schema        string             `json:"schema"`
	ChannelRanges string             `json:"channelRanges"`
	ActiveShow    *string            `json:"activeShow"`
	ShowNames     []string           `json:"showNames"`
	Settings      fppConnectSettings `json:"settings"`
}

var fppConnectConfigureKnownKeys = map[string]bool{
	"schema": true, "channelRanges": true, "activeShow": true,
	"showNames": true, "settings": true,
}

// decodeFPPConnectConfigureParams validates params' shape against
// fppConnectConfigureKnownKeys and every field's presence (activeShow's
// presence, not its value — see decodeAudioNodeConfig's identical
// "params arrives as map[string]any off the wire" pattern one file
// over), then decodes it via a JSON round trip.
func decodeFPPConnectConfigureParams(params map[string]any) (fppConnectConfigureParams, error) {
	const action = "fppconnect.configure"
	if err := rejectUnknownKeys(action, params, fppConnectConfigureKnownKeys); err != nil {
		return fppConnectConfigureParams{}, err
	}
	for _, field := range []string{"schema", "channelRanges", "activeShow", "showNames", "settings"} {
		if _, ok := params[field]; !ok {
			return fppConnectConfigureParams{}, fmt.Errorf("%s: params.%s is required", action, field)
		}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return fppConnectConfigureParams{}, fmt.Errorf("%s: encoding params: %w", action, err)
	}
	var p fppConnectConfigureParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return fppConnectConfigureParams{}, fmt.Errorf("%s: params did not decode: %w", action, err)
	}
	if p.Schema != fppConnectConfigureSchema {
		return fppConnectConfigureParams{}, fmt.Errorf("%s: unknown params.schema %q, want %q", action, p.Schema, fppConnectConfigureSchema)
	}
	if p.ShowNames == nil {
		p.ShowNames = []string{}
	}

	// The coordinator's own internal/fppconnect.FormatChannelRanges
	// already caps this at the same limit before ever pushing it, but
	// this agent is the actual trust boundary for an MQTT command
	// payload, and o.state.Apply (below) bypasses [fppConnectState.
	// SetChannelRanges]'s own identical check by design (Apply replaces
	// the whole snapshot atomically, including a value already validated
	// once by whichever path produced it) — so this is the one point on
	// the "fppconnect.configure" path that must check it.
	if len(p.ChannelRanges) > multisync.MaxPingRangesLength {
		return fppConnectConfigureParams{}, fmt.Errorf("%s: params.channelRanges is %d bytes, exceeds the ping Ranges field capacity of %d", action, len(p.ChannelRanges), multisync.MaxPingRangesLength)
	}

	// The coordinator's own internal/coordinator/config.
	// DecodeFPPConnectSettingsPayload already enforces these three rules
	// at write time, but this agent is the actual trust boundary for an
	// MQTT command payload — matching decodeAudioSettingsConfig's
	// identical re-validation of coordinator-pushed configuration one
	// file over, rather than assuming a value that reached this node over
	// the wire is automatically well-formed.
	if p.Settings.MaxFileBytes < 1 {
		return fppConnectConfigureParams{}, fmt.Errorf("%s: params.settings.maxFileBytes must be at least 1, got %d", action, p.Settings.MaxFileBytes)
	}
	if p.Settings.MaxAssetDirBytes < 1 {
		return fppConnectConfigureParams{}, fmt.Errorf("%s: params.settings.maxAssetDirBytes must be at least 1, got %d", action, p.Settings.MaxAssetDirBytes)
	}
	if p.Settings.MaxAssetDirBytes < p.Settings.MaxFileBytes {
		return fppConnectConfigureParams{}, fmt.Errorf("%s: params.settings.maxAssetDirBytes (%d) must be at least params.settings.maxFileBytes (%d)", action, p.Settings.MaxAssetDirBytes, p.Settings.MaxFileBytes)
	}

	return p, nil
}

// fppConnectConfigureOperation is the OperationFunc receiver for
// "fppconnect.configure": state is this node's held configuration
// (fppconnectstate.go) and assetDir is where it persists.
type fppConnectConfigureOperation struct {
	state    *fppConnectState
	assetDir string
}

// configure applies params to o.state, persists the result under
// o.assetDir, and acknowledges from a genuine post-write read-back of
// ChannelRanges — matching audioBinding.configureSettings's identical
// two-step (apply, then read back) shape one configuration surface over.
// A persist failure is returned as an error (never silently swallowed):
// an agent that accepted a push but could not save it would answer the
// coordinator's confirmation honestly while quietly risking a restart
// that forgets it.
func (o *fppConnectConfigureOperation) configure(_ context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
	p, err := decodeFPPConnectConfigureParams(params)
	if err != nil {
		return OperationResult{}, err
	}

	executedAt := now()
	o.state.Apply(fppConnectSnapshot{
		ChannelRanges:     p.ChannelRanges,
		ActiveShowEverSet: true,
		ActiveShowKnown:   p.ActiveShow != nil,
		ActiveShowName:    derefOrEmpty(p.ActiveShow),
		ShowNames:         p.ShowNames,
		SettingsEverSet:   true,
		Settings:          p.Settings,
	})
	if err := o.state.Save(o.assetDir); err != nil {
		return OperationResult{}, fmt.Errorf("fppconnect.configure: persist state: %w", err)
	}
	observedAt := now()

	gotRanges := o.state.ChannelRanges()
	return OperationResult{
		Confirmed:  gotRanges == p.ChannelRanges,
		Signal:     "node.fppconnect.channel_ranges",
		Value:      gotRanges,
		ExecutedAt: executedAt, ObservedAt: observedAt,
	}, nil
}

func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// fppConnectOperations builds the one allowlist entry against
// state and assetDir. state is nil-safe: newOperationRegistry always
// constructs a holder (agent.go), so this only ever returns nil in a test
// that passes nil directly, matching audioNodeConfigureOperations'
// identical nil-safety one file over.
func fppConnectOperations(state *fppConnectState, assetDir string) map[string]OperationFunc {
	if state == nil {
		return nil
	}
	op := &fppConnectConfigureOperation{state: state, assetDir: assetDir}
	return map[string]OperationFunc{
		"fppconnect.configure": op.configure,
	}
}
