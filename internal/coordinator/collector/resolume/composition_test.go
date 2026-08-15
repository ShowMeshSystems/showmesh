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

// --- Track D seam D-2: the five *Field wrapper types, present/null/absent --
//
// D-1 only ever proved the null-vs-absent pattern for two hand-written
// types (ActiveClipField, ClipTransportControls), tested above. D-2 reuses
// the identical pattern — via the shared presenceFieldValue helper — for
// five more leaf kinds. Testing only the two D-1 already covered would be
// exactly the kind of test that would have passed while a null-in-a-new-
// leaf bug shipped, so each of the five gets its own present/null/absent
// table here, independent of any HTTP wiring.
//
// Before trusting these five tests: ParamBooleanField.UnmarshalJSON's own
// null check (the bytes.Equal(...,"null") branch inside
// presenceFieldValue) was temporarily removed, routing every input
// straight into json.Unmarshal, and TestParamBooleanFieldPresenceNullAbsent
// was re-run. It failed: json.Unmarshal(null, &ParamBoolean{}) succeeds as
// a silent no-op, so the null case reported PresencePresent with a
// non-nil Param whose Value was false — the exact "bypassed": null-reads-
// as-not-bypassed defect TRACK-D-D2-SPEC.md §2 names as the one that
// matters most. Restored afterward.

type boolLeafHolder struct {
	Bypassed ParamBooleanField `json:"bypassed"`
}

func TestParamBooleanFieldPresenceNullAbsent(t *testing.T) {
	tests := []struct {
		name         string
		json         string
		wantPresence Presence
		wantValue    bool
	}{
		{"present true", `{"bypassed":{"valuetype":"ParamBoolean","id":1,"value":true}}`, PresencePresent, true},
		{"present false", `{"bypassed":{"valuetype":"ParamBoolean","id":1,"value":false}}`, PresencePresent, false},
		{"explicit null", `{"bypassed":null}`, PresenceNull, false},
		{"key absent entirely", `{}`, PresenceAbsent, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h boolLeafHolder
			if err := json.Unmarshal([]byte(tt.json), &h); err != nil {
				t.Fatalf("json.Unmarshal(%s) error = %v", tt.json, err)
			}
			if h.Bypassed.Presence != tt.wantPresence {
				t.Fatalf("Bypassed.Presence = %v, want %v", h.Bypassed.Presence, tt.wantPresence)
			}
			switch tt.wantPresence {
			case PresencePresent:
				if h.Bypassed.Param == nil {
					t.Fatalf("Bypassed.Param = nil, want non-nil for PresencePresent")
				}
				if h.Bypassed.Param.Value != tt.wantValue {
					t.Errorf("Bypassed.Param.Value = %v, want %v", h.Bypassed.Param.Value, tt.wantValue)
				}
			default:
				if h.Bypassed.Param != nil {
					t.Errorf("Bypassed.Param = %+v, want nil for Presence=%v", h.Bypassed.Param, tt.wantPresence)
				}
			}
		})
	}
}

type rangeLeafHolder struct {
	Master ParamRangeField `json:"master"`
}

func TestParamRangeFieldPresenceNullAbsent(t *testing.T) {
	tests := []struct {
		name         string
		json         string
		wantPresence Presence
		wantValue    float64
	}{
		{"present", `{"master":{"valuetype":"ParamRange","id":1,"min":0.0,"max":1.0,"value":0.5}}`, PresencePresent, 0.5},
		{"explicit null", `{"master":null}`, PresenceNull, 0},
		{"key absent entirely", `{}`, PresenceAbsent, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h rangeLeafHolder
			if err := json.Unmarshal([]byte(tt.json), &h); err != nil {
				t.Fatalf("json.Unmarshal(%s) error = %v", tt.json, err)
			}
			if h.Master.Presence != tt.wantPresence {
				t.Fatalf("Master.Presence = %v, want %v", h.Master.Presence, tt.wantPresence)
			}
			switch tt.wantPresence {
			case PresencePresent:
				if h.Master.Param == nil {
					t.Fatalf("Master.Param = nil, want non-nil for PresencePresent")
				}
				if h.Master.Param.Value != tt.wantValue {
					t.Errorf("Master.Param.Value = %v, want %v", h.Master.Param.Value, tt.wantValue)
				}
			default:
				if h.Master.Param != nil {
					t.Errorf("Master.Param = %+v, want nil for Presence=%v", h.Master.Param, tt.wantPresence)
				}
			}
		})
	}
}

type stringLeafHolder struct {
	Name ParamStringField `json:"name"`
}

func TestParamStringFieldPresenceNullAbsent(t *testing.T) {
	tests := []struct {
		name         string
		json         string
		wantPresence Presence
		wantValue    string
	}{
		{"present", `{"name":{"valuetype":"ParamString","id":1,"value":"Layer 1"}}`, PresencePresent, "Layer 1"},
		{"explicit null", `{"name":null}`, PresenceNull, ""},
		{"key absent entirely", `{}`, PresenceAbsent, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h stringLeafHolder
			if err := json.Unmarshal([]byte(tt.json), &h); err != nil {
				t.Fatalf("json.Unmarshal(%s) error = %v", tt.json, err)
			}
			if h.Name.Presence != tt.wantPresence {
				t.Fatalf("Name.Presence = %v, want %v", h.Name.Presence, tt.wantPresence)
			}
			switch tt.wantPresence {
			case PresencePresent:
				if h.Name.Param == nil {
					t.Fatalf("Name.Param = nil, want non-nil for PresencePresent")
				}
				if h.Name.Param.Value != tt.wantValue {
					t.Errorf("Name.Param.Value = %q, want %q", h.Name.Param.Value, tt.wantValue)
				}
			default:
				if h.Name.Param != nil {
					t.Errorf("Name.Param = %+v, want nil for Presence=%v", h.Name.Param, tt.wantPresence)
				}
			}
		})
	}
}

type choiceLeafHolder struct {
	TransportType ParamChoiceField `json:"transporttype"`
}

func TestParamChoiceFieldPresenceNullAbsent(t *testing.T) {
	tests := []struct {
		name         string
		json         string
		wantPresence Presence
		wantValue    string
	}{
		{"present", `{"transporttype":{"valuetype":"ParamChoice","id":1,"value":"Timeline","options":["Timeline","SMPTE 1"]}}`, PresencePresent, "Timeline"},
		{"explicit null", `{"transporttype":null}`, PresenceNull, ""},
		{"key absent entirely", `{}`, PresenceAbsent, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h choiceLeafHolder
			if err := json.Unmarshal([]byte(tt.json), &h); err != nil {
				t.Fatalf("json.Unmarshal(%s) error = %v", tt.json, err)
			}
			if h.TransportType.Presence != tt.wantPresence {
				t.Fatalf("TransportType.Presence = %v, want %v", h.TransportType.Presence, tt.wantPresence)
			}
			switch tt.wantPresence {
			case PresencePresent:
				if h.TransportType.Param == nil {
					t.Fatalf("TransportType.Param = nil, want non-nil for PresencePresent")
				}
				if h.TransportType.Param.Value != tt.wantValue {
					t.Errorf("TransportType.Param.Value = %q, want %q", h.TransportType.Param.Value, tt.wantValue)
				}
				if len(h.TransportType.Param.Options) != 2 {
					t.Errorf("TransportType.Param.Options = %v, want the 2 options carried verbatim", h.TransportType.Param.Options)
				}
			default:
				if h.TransportType.Param != nil {
					t.Errorf("TransportType.Param = %+v, want nil for Presence=%v", h.TransportType.Param, tt.wantPresence)
				}
			}
		})
	}
}

type stateLeafHolder struct {
	Connected ParamStateField `json:"connected"`
}

// TestParamStateFieldPresenceNullAbsent is the tri-state test for
// `connected`, deliberately distinct from
// TestConnectedNeverReducesToBoolAndPreservesEveryState above: that test
// proves the five VALUE states are preserved; this one proves
// present/null/absent are three distinguishable OUTCOMES for the same
// field, the property none of the five-state cases by themselves say
// anything about. "Connected & previewing" is included as the present
// case specifically because it is the state a naive `== "Connected"`
// predicate misses (capture section 4.3/8.1).
func TestParamStateFieldPresenceNullAbsent(t *testing.T) {
	tests := []struct {
		name         string
		json         string
		wantPresence Presence
		wantValue    string
	}{
		{
			"present, Connected & previewing",
			`{"connected":{"valuetype":"ParamState","id":1,"value":"Connected & previewing","options":["Empty","Disconnected","Previewing","Connected","Connected & previewing"]}}`,
			PresencePresent, "Connected & previewing",
		},
		{"explicit null", `{"connected":null}`, PresenceNull, ""},
		{"key absent entirely", `{}`, PresenceAbsent, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h stateLeafHolder
			if err := json.Unmarshal([]byte(tt.json), &h); err != nil {
				t.Fatalf("json.Unmarshal(%s) error = %v", tt.json, err)
			}
			if h.Connected.Presence != tt.wantPresence {
				t.Fatalf("Connected.Presence = %v, want %v", h.Connected.Presence, tt.wantPresence)
			}
			switch tt.wantPresence {
			case PresencePresent:
				if h.Connected.Param == nil {
					t.Fatalf("Connected.Param = nil, want non-nil for PresencePresent")
				}
				if h.Connected.Param.Value != tt.wantValue {
					t.Errorf("Connected.Param.Value = %q, want %q", h.Connected.Param.Value, tt.wantValue)
				}
			default:
				if h.Connected.Param != nil {
					t.Errorf("Connected.Param = %+v, want nil for Presence=%v", h.Connected.Param, tt.wantPresence)
				}
			}
		})
	}
}

// TestFiveFieldTypesRemainDistinctGoTypes is a compile-time-adjacent check
// on this section's own doc comment claim: sharing presenceFieldValue must
// not have merged the five field types into one interchangeable shape. If
// this package ever collapsed them (e.g. via a single generic
// ParamField[T] alias used directly as every field's type), the five
// distinct struct literals below would still compile — this test cannot
// catch that mechanically — but it does pin the five type names as
// existing and independently constructible, which a merge that renamed or
// removed any of them would break at compile time.
func TestFiveFieldTypesRemainDistinctGoTypes(t *testing.T) {
	_ = ParamBooleanField{Presence: PresencePresent, Param: &ParamBoolean{Value: true}}
	_ = ParamRangeField{Presence: PresencePresent, Param: &ParamRange{Value: 1}}
	_ = ParamStringField{Presence: PresencePresent, Param: &ParamString{Value: "x"}}
	_ = ParamChoiceField{Presence: PresencePresent, Param: &ParamChoice{Value: "x"}}
	_ = ParamStateField{Presence: PresencePresent, Param: &ParamState{Value: "x"}}
}
