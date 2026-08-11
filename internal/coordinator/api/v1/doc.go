// Package v1 holds the ShowMesh control API's version 1 wire types: the
// exact JSON shapes documented in api/openapi.yaml and pinned in the Step 3
// shared design contract section 6.10.
//
// # Why this is a separate package from the domain model
//
// [github.com/showmeshsystems/showmesh/pkg/observation.Observation],
// [github.com/showmeshsystems/showmesh/internal/coordinator/inventory.NodeView],
// and the coordinator's store types already exist and already have their
// own reasons to change: a collector adds a field it needs internally, a
// store migration reshapes a record, [pkg/observation] adds a validation
// rule. None of those reasons has anything to do with the public contract
// this package renders, and ADR-014 requires that contract to be
// independently stable — usable by a CLI, an automation system, or a
// future alternate coordinator with no UI involved, versioned in the path,
// additive-only within v1.
//
// If a domain struct carried `json:"..."` tags directly, every one of
// those internal reasons to change would also be a reason the wire format
// could change, silently, with no version bump and no reviewer forced to
// look at api/openapi.yaml. A routine refactor of the store's node record —
// renaming a field, restructuring an embedded struct — would then quietly
// retype a contract the path claims is versioned. That is exactly the
// failure ADR-015 calls out for the UI side ("hand-maintaining a second
// copy of the state model will drift, and the drift will present as UI
// bugs that look like backend bugs"); the fix on the API side is the
// mirror image, not "generate the wire type from the domain type" but "the
// wire type is its own type, and something maps into it explicitly".
//
// So this package is deliberately dumb: JSON structs, their doc comments,
// and nothing else. No I/O, no SQL, no MQTT, no business logic, and no
// import of pkg/observation's or the store's *behavior* — only the mapping
// functions in the parent internal/coordinator/api package are allowed to
// know how a domain value becomes one of these. The duplication this
// creates (a field that exists, in slightly different shape, in both a
// domain struct and a struct here) looks like waste to anyone who has not
// been burned by the alternative. It is the cost of the version in the
// path meaning something.
//
// # Conventions every type here follows
//
// JSON field names are lowerCamelCase throughout. Timestamps are RFC 3339
// strings with an explicit offset (never a bare "Z"-less local time, and
// never omitted). Every observation-bearing field uses the [Evidence]
// envelope from contract section 6.3: a value is never rendered without
// its state, and a state that is not "current" always carries a non-nil
// reason. A field that has no current value is never omitted from a
// response — the field is present with state and reason set and value
// null — because omission is how a client comes to believe something is
// fine when nobody actually checked.
//
// v1 is additive-only: a field may be added to any struct here, but never
// removed, renamed, or retyped. A breaking change is a v2 package, not an
// edit to this one.
package v1
