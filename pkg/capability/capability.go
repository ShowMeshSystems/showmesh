package capability

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// Capability is one namespaced, versioned capability a node advertises, per
// ADR-002 and the YAML example in ARCHITECTURE section 6. Attributes is
// free-form per ADR-002 (max_width, tested_fps, channel counts, device
// names, and so on); this package deliberately does not invent a typed
// attribute schema, since that is not decided yet (see CLAUDE.md's capacity
// note for pkg/capability).
type Capability struct {
	ID         ID             `json:"id"`
	Version    int            `json:"version"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// ErrInvalidVersion is wrapped by [Capability.Validate] when Version is not
// a positive integer, per the ARCHITECTURE section 6 YAML example (every
// example capability carries version: 1; nothing describes a zero or
// negative version).
var ErrInvalidVersion = errors.New("capability: version must be a positive integer")

// Validate reports whether c is well-formed: c.ID passes [ID.Validate] and
// c.Version is a positive integer. It does not check c.Attributes, which is
// intentionally free-form and untyped.
func (c Capability) Validate() error {
	if err := c.ID.Validate(); err != nil {
		return err
	}
	if c.Version < 1 {
		return fmt.Errorf("%w: capability %q has version %d", ErrInvalidVersion, c.ID, c.Version)
	}
	return nil
}

// Set is an unordered collection of capabilities, such as the full set one
// node advertises in its hello payload.
type Set []Capability

// Lookup returns the capability with the given ID and true, or the zero
// Capability and false if no member of s has that ID. If s contains a
// duplicate ID (which [Set.Validate] rejects but Lookup does not itself
// enforce), Lookup returns the first match.
func (s Set) Lookup(id ID) (Capability, bool) {
	for _, c := range s {
		if c.ID == id {
			return c, true
		}
	}
	return Capability{}, false
}

// ErrDuplicateCapabilityID is wrapped by [Set.Validate] when two members of
// the set share the same ID.
var ErrDuplicateCapabilityID = errors.New("capability: duplicate capability ID in set")

// Validate reports whether every member of s is individually valid (see
// [Capability.Validate]) and no two members share an ID. An unknown or
// withdrawn ID does not fail validation; see the package doc comment.
func (s Set) Validate() error {
	seen := make(map[ID]struct{}, len(s))
	for _, c := range s {
		if err := c.Validate(); err != nil {
			return err
		}
		if _, dup := seen[c.ID]; dup {
			return fmt.Errorf("%w: %q", ErrDuplicateCapabilityID, c.ID)
		}
		seen[c.ID] = struct{}{}
	}
	return nil
}

// CanonicalJSON encodes s as JSON in a form that is stable regardless of
// the order capabilities were appended to s or the order any Attributes map
// was built in, so two logically identical sets compare and store
// identically (byte for byte).
//
// This relies on two things: encoding/json's documented behavior of
// sorting map keys when marshaling any map value (which already makes each
// Capability's Attributes deterministic on its own, with no extra work
// needed here), and a total order imposed on the capabilities themselves
// before marshaling, since map key sorting alone says nothing about slice
// element order.
//
// The sort key is (ID, Version, marshaled Attributes bytes), in that order,
// specifically so that two entries sharing an ID (which [Set.Validate]
// rejects but CanonicalJSON does not itself enforce; see below) still sort
// into one deterministic order regardless of their position in s, rather
// than merely preserving whatever order they arrived in. Sorting by ID
// alone is not a total order over a set that may contain a duplicate ID:
// Set{{a.b,v1},{a.b,v2}} and Set{{a.b,v2},{a.b,v1}} are different inputs
// with the same multiset of IDs, and a comparator that never returns "less"
// for equal IDs, combined with a stable sort, preserves each input's
// original order — producing two different byte strings for what a caller
// relying on this method's promise would reasonably treat as "the same
// set, reordered". The extra tie-break keys close that gap.
//
// This is NOT a general JSON canonicalization (RFC 8785): it does not
// normalize number formatting, Unicode escaping, or the like. It is
// sufficient for this package's purpose, comparing and storing a
// capability.Set deterministically, because it only needs to be stable
// across repeated calls within this codebase, not interoperable with an
// external canonicalizer.
//
// CanonicalJSON does not call [Set.Validate] first, so a duplicate ID is
// not rejected here — only sorted into the deterministic order described
// above. A caller that needs to reject duplicates must call
// [Set.Validate] itself; ADR-003 makes an unvalidated set's checksum
// consequential (spurious "capabilities changed" transitions), which is
// exactly why this method's ordering must not depend on input order even
// in the duplicate case.
func (s Set) CanonicalJSON() ([]byte, error) {
	type keyed struct {
		cap     Capability
		attrKey []byte
	}

	entries := make([]keyed, len(s))
	for i, c := range s {
		attrKey, err := json.Marshal(c.Attributes)
		if err != nil {
			return nil, fmt.Errorf("capability: encode canonical JSON: %w", err)
		}
		entries[i] = keyed{cap: c, attrKey: attrKey}
	}

	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.cap.ID != b.cap.ID {
			return a.cap.ID < b.cap.ID
		}
		if a.cap.Version != b.cap.Version {
			return a.cap.Version < b.cap.Version
		}
		return string(a.attrKey) < string(b.attrKey)
	})

	sorted := make(Set, len(entries))
	for i, e := range entries {
		sorted[i] = e.cap
	}

	b, err := json.Marshal(sorted)
	if err != nil {
		return nil, fmt.Errorf("capability: encode canonical JSON: %w", err)
	}
	return b, nil
}
