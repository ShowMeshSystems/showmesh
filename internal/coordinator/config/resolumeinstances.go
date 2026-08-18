package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is Track G seam G-2's config kind (ADR-039): the
// config_objects.kind / config_revisions.payload_json shape for
// SHOWMESH_RESOLUME_URL/SHOWMESH_RESOLUME_ID promoted to store-backed
// configuration. Mirrors fppendpoints.go's shape closely — same singleton
// object id, same "api"/"env_migration" source constants, same
// absent/null/empty payload discipline at the API layer (resolumeinstances.go
// in package api) — narrowed to at most one element: the schema stays a
// list (api/interfaces.go's ResolumeLister doc comment already commits to
// that), but the limit lives in validation, not in the schema, matching
// ADR-026's "N surfaces implemented at N=1" shape.

const (
	// ResolumeInstancesConfigKind is config_objects.kind and
	// config_revisions.kind for the Resolume instance list — the wire and
	// storage identifier for GET/PUT /api/v1/config/resolume.instances.
	ResolumeInstancesConfigKind = "resolume.instances"

	// ResolumeInstancesConfigObjectID is the single config_objects.id this
	// kind ever uses — one Resolume instance list per coordinator.
	ResolumeInstancesConfigObjectID = "default"

	// ResolumeInstancesSourceAPI and ResolumeInstancesSourceEnvMigration are
	// the two values config_revisions.source takes for this kind: a write
	// through PUT /api/v1/config/resolume.instances, or the one-time startup
	// migration out of SHOWMESH_RESOLUME_URL/SHOWMESH_RESOLUME_ID
	// (internal/coordinator's resolumeinstancessync.go).
	ResolumeInstancesSourceAPI          = "api"
	ResolumeInstancesSourceEnvMigration = "env_migration"
)

// ResolumeInstance is one entry of [ResolumeInstancesPayload.Instances]: an
// id (the same node-id syntax an FPP endpoint id uses) and a REST base URL.
type ResolumeInstance struct {
	ID  string
	URL string
}

// ResolumeInstancesPayload is config_revisions.payload_json's decoded shape
// for [ResolumeInstancesConfigKind]: one JSON object with a single array
// member, mirroring [FPPEndpointsPayload]'s identical shape for the
// identical reason — a bare-array convention would leave a later config
// kind's payload schema guessing whether that shape was deliberate.
type ResolumeInstancesPayload struct {
	Instances []ResolumeInstance `json:"instances"`
}

// EncodeResolumeInstancesPayload marshals instances into config_revisions'
// payload_json column shape. instances is never nil in the encoded output
// even when the input slice is nil — zero configured Resolume instances is
// a real, valid state (an operator who has not yet connected Arena, or who
// has decommissioned it), and a JSON `null` there would be the "null is not
// an absent key" defect class this project has already shipped twice.
func EncodeResolumeInstancesPayload(instances []ResolumeInstance) (string, error) {
	if instances == nil {
		instances = []ResolumeInstance{}
	}
	b, err := json.Marshal(ResolumeInstancesPayload{Instances: instances})
	if err != nil {
		return "", fmt.Errorf("config: encode resolume.instances payload: %w", err)
	}
	return string(b), nil
}

// DecodeResolumeInstancesPayload is [EncodeResolumeInstancesPayload]'s
// inverse. A decode failure means a config_revisions row this package never
// wrote in this shape — every writer of this kind goes through
// EncodeResolumeInstancesPayload — so callers treat it as a store-integrity
// error, not a validation outcome to recover from.
func DecodeResolumeInstancesPayload(raw string) ([]ResolumeInstance, error) {
	var payload ResolumeInstancesPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("config: decode resolume.instances payload: %w", err)
	}
	if payload.Instances == nil {
		payload.Instances = []ResolumeInstance{}
	}
	return payload.Instances, nil
}

// ResolumeInstancesEqual reports whether a and b name the exact same set of
// (id, url) pairs, ORDER-INSENSITIVE — mirroring [FPPEndpointsEqual], for
// the identical env->store migration disagreement check
// (internal/coordinator's resolumeinstancessync.go).
func ResolumeInstancesEqual(a, b []ResolumeInstance) bool {
	if len(a) != len(b) {
		return false
	}
	sortedA := sortedResolumeInstanceCopy(a)
	sortedB := sortedResolumeInstanceCopy(b)
	for i := range sortedA {
		if sortedA[i] != sortedB[i] {
			return false
		}
	}
	return true
}

func sortedResolumeInstanceCopy(instances []ResolumeInstance) []ResolumeInstance {
	out := append([]ResolumeInstance(nil), instances...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].URL < out[j].URL
	})
	return out
}

// maxResolumeInstances is the scope limit Track G seam G-2's own spec
// states explicitly: "Validation rejects more than one instance with a
// reason naming the limit ... the scope limit lives in validation, never in
// the schema" — mirroring ADR-026's "N surfaces implemented at N=1". The
// wire payload stays a list (api/interfaces.go's ResolumeLister doc
// comment already commits to that for GET), so a later relaxation of this
// constant is additive, not a schema change.
const maxResolumeInstances = 1

// ValidateResolumeInstances validates one resolume.instances config
// payload: at most [maxResolumeInstances] entries, each entry's id and URL
// checked against the same shape [validateResolumeConfig] already applies
// to SHOWMESH_RESOLUME_URL/SHOWMESH_RESOLUME_ID (http/https scheme,
// non-empty host, no userinfo, [mqttproto.ValidateNodeID] on the id), and
// each id cross-checked against fppEndpoints via
// [ValidateResolumeIDAgainstFPPEndpoints] — the SAME exported rule
// validateResolumeConfig and handlePutFPPEndpointsConfig (package api)
// both already call, reused rather than re-implemented here so all three
// call sites can never silently disagree about what a collision is.
//
// The per-field checks are duplicated, not shared, from
// validateResolumeConfig's own three URL/userinfo checks — mirroring that
// function's own doc comment ("duplicated here rather than shared because
// the two validate unrelated fields of unrelated structs"): this validates
// a []ResolumeInstance payload, not a Config.
func ValidateResolumeInstances(instances []ResolumeInstance, fppEndpoints []FPPEndpoint) error {
	if len(instances) > maxResolumeInstances {
		return fmt.Errorf("resolume.instances: at most %d instance is supported today, got %d", maxResolumeInstances, len(instances))
	}

	seen := make(map[string]bool, len(instances))
	for _, inst := range instances {
		if err := mqttproto.ValidateNodeID(inst.ID); err != nil {
			return fmt.Errorf("resolume.instances: instance id %q: %w", inst.ID, err)
		}
		if seen[inst.ID] {
			return fmt.Errorf("resolume.instances: duplicate instance id %q", inst.ID)
		}
		seen[inst.ID] = true

		u, err := url.Parse(inst.URL)
		if err != nil {
			return fmt.Errorf("resolume.instances: instance %q: url %q is not valid: %w", inst.ID, inst.URL, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("resolume.instances: instance %q: url %q must use http or https", inst.ID, inst.URL)
		}
		if u.Host == "" {
			return fmt.Errorf("resolume.instances: instance %q: url %q must include a host", inst.ID, inst.URL)
		}
		if u.User != nil {
			return fmt.Errorf("resolume.instances: instance %q: url must not include userinfo/credentials", inst.ID)
		}

		if err := ValidateResolumeIDAgainstFPPEndpoints(inst.ID, fppEndpoints); err != nil {
			return err
		}
	}
	return nil
}
