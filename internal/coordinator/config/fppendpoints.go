package config

import (
	"encoding/json"
	"fmt"
	"sort"
)

// This file is Step 7 seam A's own addition (RES-008 D1, decision "the
// first object kind is fpp.endpoints"): the config_objects.kind /
// config_revisions.payload_json shape for the FPP endpoint list, plus the
// order-insensitive equality check the env->store migration's disagreement
// rule needs. internal/coordinator/store's config.go (Step 7 seam 0) knows
// nothing about what any kind's payload_json actually contains — this file
// is that knowledge, for the one kind this seam owns.

const (
	// FPPEndpointsConfigKind is config_objects.kind and
	// config_revisions.kind for the FPP endpoint list — the wire and
	// storage identifier for the "first configuration table" RES-008 D1
	// names. Also used verbatim as the second path segment of
	// GET/PUT /api/v1/config/fpp.endpoints.
	FPPEndpointsConfigKind = "fpp.endpoints"

	// FPPEndpointsConfigObjectID is the single config_objects.id this kind
	// ever uses. There is exactly one FPP endpoints list per coordinator —
	// RES-008 section 6a's D1 describes migrating "the first configuration
	// table", singular, not a namespace of operator-named lists — so this
	// is a fixed singleton rather than something a caller chooses.
	FPPEndpointsConfigObjectID = "default"

	// FPPEndpointsSourceAPI and FPPEndpointsSourceEnvMigration are the two
	// values config_revisions.source takes for this kind: a write through
	// PUT /api/v1/config/fpp.endpoints, or the one-time startup migration
	// out of SHOWMESH_FPP_ENDPOINTS (internal/coordinator's
	// configsync.go). Exported so both writers, in two different packages,
	// use the identical literal rather than each inventing its own string.
	FPPEndpointsSourceAPI          = "api"
	FPPEndpointsSourceEnvMigration = "env_migration"
)

// FPPEndpointsPayload is config_revisions.payload_json's decoded shape for
// [FPPEndpointsConfigKind]: the same (id, url) pairs
// SHOWMESH_FPP_ENDPOINTS carries, one JSON object with a single array
// member rather than a bare array, so a later config kind's payload schema
// never has to guess whether a bare-array convention was deliberate or
// accidental — see [EncodeFPPEndpointsPayload]/[DecodeFPPEndpointsPayload].
type FPPEndpointsPayload struct {
	Endpoints []FPPEndpoint `json:"endpoints"`
}

// EncodeFPPEndpointsPayload marshals endpoints into config_revisions'
// payload_json column shape. endpoints is never nil in the encoded output
// even when the input slice is nil — an empty configured-endpoints list is
// a real, valid state (RES-008 D1 says nothing requires at least one FPP
// instance), and a JSON `null` there would be exactly the "null is not an
// absent key" defect class CLAUDE.md's Step 5 lesson already names, one
// layer up from a decoded struct field this time.
func EncodeFPPEndpointsPayload(endpoints []FPPEndpoint) (string, error) {
	if endpoints == nil {
		endpoints = []FPPEndpoint{}
	}
	b, err := json.Marshal(FPPEndpointsPayload{Endpoints: endpoints})
	if err != nil {
		return "", fmt.Errorf("config: encode fpp.endpoints payload: %w", err)
	}
	return string(b), nil
}

// DecodeFPPEndpointsPayload is [EncodeFPPEndpointsPayload]'s inverse. A
// decode failure here means a config_revisions row this package itself
// never wrote in this shape — every writer of this kind goes through
// EncodeFPPEndpointsPayload — so callers treat it as a store-integrity
// error, not a validation outcome a caller can usefully recover from.
func DecodeFPPEndpointsPayload(raw string) ([]FPPEndpoint, error) {
	var payload FPPEndpointsPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("config: decode fpp.endpoints payload: %w", err)
	}
	if payload.Endpoints == nil {
		payload.Endpoints = []FPPEndpoint{}
	}
	return payload.Endpoints, nil
}

// FPPEndpointsEqual reports whether a and b name the exact same set of
// (id, url) pairs, ORDER-INSENSITIVE — the comparison the env->store
// disagreement rule (BUILD-PLAN Step 7 seam A: "comparison is over the set
// of id and url pairs, order-insensitive") needs to decide whether a
// still-set SHOWMESH_FPP_ENDPOINTS agrees with the store's active
// revision. A caller-visible duplicate id is impossible in either input by
// the time this runs ([ValidateFPPEndpoints] already rejects it before
// either side of the comparison could exist), so this needs no dedup logic
// of its own, only a length check plus a value-independent-of-order match.
func FPPEndpointsEqual(a, b []FPPEndpoint) bool {
	if len(a) != len(b) {
		return false
	}
	sortedA := sortedFPPEndpointCopy(a)
	sortedB := sortedFPPEndpointCopy(b)
	for i := range sortedA {
		if sortedA[i] != sortedB[i] {
			return false
		}
	}
	return true
}

func sortedFPPEndpointCopy(endpoints []FPPEndpoint) []FPPEndpoint {
	out := append([]FPPEndpoint(nil), endpoints...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].URL < out[j].URL
	})
	return out
}
