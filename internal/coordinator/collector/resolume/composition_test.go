package resolume

import (
	"encoding/json"
	"testing"
)

// --- Required test 3: active_clip present/null/absent are distinguishable --

// activeClipHolder is a minimal test-only wrapper: production code used to
// decode active_clip as a field of the (now deleted) full-composition
// Layer type, but ActiveClipField's own decode behavior does not depend on
// what it is embedded in, so this is all a test of it needs.
type activeClipHolder struct {
	ActiveClip ActiveClipField `json:"active_clip"`
}

// TestActiveClipPresentNullAbsentAreDistinguishable is the direct
// reproduction of this project's already-shipped "ma": null defect
// (CLAUDE.md), in Resolume's own vocabulary: active_clip must decode to
// three DIFFERENT Presence values depending on whether the key is
// missing, present-and-null, or present-with-a-value — never collapsing
// null and absent into the same Go zero value.
//
// Before trusting this test, ActiveClipField.UnmarshalJSON was reverted
// to skip the `bytes.Equal(..., []byte("null"))` check (treating any
// present value, null included, as an attempt to json.Unmarshal into
// ActiveClip) and re-run: it failed, because json.Unmarshal(null, &c)
// itself succeeds as a no-op leaving c at its zero value, which this
// test's null case would then have misreported as PresencePresent with
// Clip.ID == 0 instead of PresenceNull with Clip == nil. Restored
// afterward.
func TestActiveClipPresentNullAbsentAreDistinguishable(t *testing.T) {
	tests := []struct {
		name         string
		json         string
		wantPresence Presence
		wantClipID   ObjectID
	}{
		{
			name:         "present",
			json:         `{"active_clip": {"id": 1765396769079}}`,
			wantPresence: PresencePresent,
			wantClipID:   1765396769079,
		},
		{
			name:         "explicit null",
			json:         `{"active_clip": null}`,
			wantPresence: PresenceNull,
		},
		{
			name:         "key absent entirely",
			json:         `{}`,
			wantPresence: PresenceAbsent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h activeClipHolder
			if err := json.Unmarshal([]byte(tt.json), &h); err != nil {
				t.Fatalf("json.Unmarshal(%s) error = %v", tt.json, err)
			}
			if h.ActiveClip.Presence != tt.wantPresence {
				t.Errorf("ActiveClip.Presence = %v, want %v", h.ActiveClip.Presence, tt.wantPresence)
			}
			switch tt.wantPresence {
			case PresencePresent:
				if h.ActiveClip.Clip == nil {
					t.Fatalf("ActiveClip.Clip = nil, want non-nil for PresencePresent")
				}
				if h.ActiveClip.Clip.ID != tt.wantClipID {
					t.Errorf("ActiveClip.Clip.ID = %v, want %v", h.ActiveClip.Clip.ID, tt.wantClipID)
				}
			default:
				if h.ActiveClip.Clip != nil {
					t.Errorf("ActiveClip.Clip = %+v, want nil for Presence=%v", h.ActiveClip.Clip, tt.wantPresence)
				}
			}
		})
	}
}

// TestActiveClipThreeOutcomesAreMutuallyDistinguishable is the same claim
// stated the other way: iterating the three Presence values pairwise,
// none of them may compare equal to another. This is the exact property
// "distinguishable" means, made explicit rather than merely implied by
// TestActiveClipPresentNullAbsentAreDistinguishable's per-case checks.
func TestActiveClipThreeOutcomesAreMutuallyDistinguishable(t *testing.T) {
	all := []Presence{PresenceAbsent, PresenceNull, PresencePresent}
	for i, a := range all {
		for j, b := range all {
			if i == j {
				continue
			}
			if a == b {
				t.Fatalf("Presence values %v and %v compare equal; they must be pairwise distinct", a, b)
			}
		}
	}
}

// --- The same tri-state rule for transport.controls (capture 11.3) ---------

// TestClipTransportControlsPresentNullAbsentAreDistinguishable decodes
// straight into [ClipTransport] — production code used to nest this
// inside the (now deleted) full-composition Clip type, but
// ClipTransportControls' own decode behavior does not depend on that
// nesting.
func TestClipTransportControlsPresentNullAbsentAreDistinguishable(t *testing.T) {
	tests := []struct {
		name         string
		json         string
		wantPresence Presence
	}{
		{
			name:         "present",
			json:         `{"controls": {"loop": true}}`,
			wantPresence: PresencePresent,
		},
		{
			name:         "explicit null under SMPTE transport",
			json:         `{"controls": null}`,
			wantPresence: PresenceNull,
		},
		{
			name:         "key absent entirely",
			json:         `{}`,
			wantPresence: PresenceAbsent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ct ClipTransport
			if err := json.Unmarshal([]byte(tt.json), &ct); err != nil {
				t.Fatalf("json.Unmarshal(%s) error = %v", tt.json, err)
			}
			if ct.Controls.Presence != tt.wantPresence {
				t.Errorf("Controls.Presence = %v, want %v", ct.Controls.Presence, tt.wantPresence)
			}
			if tt.wantPresence == PresencePresent && ct.Controls.RawJSON == nil {
				t.Errorf("Controls.RawJSON = nil, want non-nil for PresencePresent")
			}
			if tt.wantPresence != PresencePresent && ct.Controls.RawJSON != nil {
				t.Errorf("Controls.RawJSON = %s, want nil for Presence=%v", ct.Controls.RawJSON, tt.wantPresence)
			}
		})
	}
}

// --- Required test 4: connected is never reduced to a bool ------------------

// TestConnectedNeverReducesToBoolAndPreservesEveryState decodes every one
// of the five clip `connected` states the capture names, including
// "Connected & previewing" — the state a naive `== "Connected"` predicate
// misses — directly into [ParamState], and confirms Options is preserved
// verbatim rather than hard-coded, per capture section 4.3's warning that
// Options is not constant across objects of the same kind.
//
// Before trusting this test, ParamState.Value's type was changed to bool
// (mapping "Connected"/"Connected & previewing" to true and everything
// else to false, the reduction this test's name forbids) and re-run: it
// failed to compile, because the test asserts against the literal string
// values — which is itself evidence for why ParamState.Value is typed
// string, never bool: a compile-time property beats a runtime one. Also
// verified by hand that reverting to string but collapsing "Connected"
// and "Connected & previewing" into a single hard-coded two-state check
// (rather than preserving Value verbatim) makes this test's specific
// "Connected & previewing" case fail its Value assertion. Reverted
// afterward.
func TestConnectedNeverReducesToBoolAndPreservesEveryState(t *testing.T) {
	states := []string{"Empty", "Disconnected", "Previewing", "Connected", "Connected & previewing"}
	options := `["Empty","Disconnected","Previewing","Connected","Connected & previewing"]`

	for _, state := range states {
		t.Run(state, func(t *testing.T) {
			raw := `{"valuetype":"ParamState","id":8005,"value":"` + state + `","index":3,"options":` + options + `}`
			var ps ParamState
			if err := json.Unmarshal([]byte(raw), &ps); err != nil {
				t.Fatalf("json.Unmarshal error = %v", err)
			}
			if ps.Value != state {
				t.Errorf("Value = %q, want %q", ps.Value, state)
			}
			if len(ps.Options) != 5 {
				t.Errorf("len(Options) = %d, want 5 (options must be carried verbatim, never hard-coded)", len(ps.Options))
			}
		})
	}
}

// --- Required test 5: json.Marshal of a ParameterID errors ------------------

// TestParameterIDMarshalJSONErrors is the structural enforcement, tested
// directly: a bare ParameterID must refuse to marshal.
//
// Before trusting this test, ParameterID.MarshalJSON was temporarily
// changed to `return []byte(strconv.FormatInt(int64(p), 10)), nil` (the
// obvious, wrong implementation that just encodes the number) and this
// test was re-run: it failed, with json.Marshal succeeding and returning
// the raw parameter id as a JSON number. Reverted afterward.
func TestParameterIDMarshalJSONErrors(t *testing.T) {
	id := ParameterID(1786724946918)
	if _, err := json.Marshal(id); err == nil {
		t.Fatalf("json.Marshal(ParameterID) error = nil, want a non-nil error")
	}
}

// TestStructContainingParameterIDMarshalJSONErrors proves the enforcement
// survives nesting: a struct that merely CONTAINS a ParameterID field
// must also fail to marshal, since encoding/json calls the field's own
// MarshalJSON while encoding the containing struct. This is the case that
// actually matters — nobody marshals a bare ParameterID by itself; the
// realistic mistake is a struct with a ParameterID field accidentally
// reaching an API handler's json.NewEncoder.
func TestStructContainingParameterIDMarshalJSONErrors(t *testing.T) {
	type wrapper struct {
		ObjectID ObjectID    `json:"objectId"`
		ParamID  ParameterID `json:"paramId"`
	}
	w := wrapper{ObjectID: 1765396769079, ParamID: 1786724946918}
	if _, err := json.Marshal(w); err == nil {
		t.Fatalf("json.Marshal(struct containing ParameterID) error = nil, want a non-nil error")
	}
}

// TestObjectIDMarshalsNormally is the explicit non-regression check: only
// ParameterID is blocked. ObjectID — safe to hold and, per this package's
// doc comment, a later seam's decision whether to persist — must marshal
// like an ordinary integer.
func TestObjectIDMarshalsNormally(t *testing.T) {
	b, err := json.Marshal(ObjectID(1765396769079))
	if err != nil {
		t.Fatalf("json.Marshal(ObjectID) error = %v, want ObjectID to marshal normally", err)
	}
	if string(b) != "1765396769079" {
		t.Errorf("json.Marshal(ObjectID) = %s, want the bare integer", b)
	}
}

// ParameterID must still be UNMARSHALABLE — the restriction is on writing
// it, never on reading it off the wire; see this package's doc comment.
func TestParameterIDUnmarshalsNormally(t *testing.T) {
	var id ParameterID
	if err := json.Unmarshal([]byte("1786724946918"), &id); err != nil {
		t.Fatalf("json.Unmarshal(ParameterID) error = %v, want ParameterID to still be readable from JSON", err)
	}
	if id != 1786724946918 {
		t.Errorf("ParameterID = %d, want 1786724946918", id)
	}
}
