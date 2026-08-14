package resolume

import (
	"encoding/json"
	"testing"
)

// --- Required test 3: active_clip present/null/absent are distinguishable --

// TestActiveClipPresentNullAbsentAreDistinguishable is the direct
// reproduction of this project's already-shipped "ma": null defect
// (CLAUDE.md), in Resolume's own vocabulary: layers[i].active_clip must
// decode to three DIFFERENT Presence values depending on whether the key
// is missing, present-and-null, or present-with-a-value — never
// collapsing null and absent into the same Go zero value.
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
			var l Layer
			if err := json.Unmarshal([]byte(tt.json), &l); err != nil {
				t.Fatalf("json.Unmarshal(%s) error = %v", tt.json, err)
			}
			if l.ActiveClip.Presence != tt.wantPresence {
				t.Errorf("ActiveClip.Presence = %v, want %v", l.ActiveClip.Presence, tt.wantPresence)
			}
			switch tt.wantPresence {
			case PresencePresent:
				if l.ActiveClip.Clip == nil {
					t.Fatalf("ActiveClip.Clip = nil, want non-nil for PresencePresent")
				}
				if l.ActiveClip.Clip.ID != tt.wantClipID {
					t.Errorf("ActiveClip.Clip.ID = %v, want %v", l.ActiveClip.Clip.ID, tt.wantClipID)
				}
			default:
				if l.ActiveClip.Clip != nil {
					t.Errorf("ActiveClip.Clip = %+v, want nil for Presence=%v", l.ActiveClip.Clip, tt.wantPresence)
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

func TestClipTransportControlsPresentNullAbsentAreDistinguishable(t *testing.T) {
	tests := []struct {
		name         string
		json         string
		wantPresence Presence
	}{
		{
			name:         "present",
			json:         `{"transport": {"controls": {"loop": true}}}`,
			wantPresence: PresencePresent,
		},
		{
			name:         "explicit null under SMPTE transport",
			json:         `{"transport": {"controls": null}}`,
			wantPresence: PresenceNull,
		},
		{
			name:         "key absent entirely",
			json:         `{"transport": {}}`,
			wantPresence: PresenceAbsent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c Clip
			if err := json.Unmarshal([]byte(tt.json), &c); err != nil {
				t.Fatalf("json.Unmarshal(%s) error = %v", tt.json, err)
			}
			if c.Transport.Controls.Presence != tt.wantPresence {
				t.Errorf("Transport.Controls.Presence = %v, want %v", c.Transport.Controls.Presence, tt.wantPresence)
			}
			if tt.wantPresence == PresencePresent && c.Transport.Controls.RawJSON == nil {
				t.Errorf("Transport.Controls.RawJSON = nil, want non-nil for PresencePresent")
			}
			if tt.wantPresence != PresencePresent && c.Transport.Controls.RawJSON != nil {
				t.Errorf("Transport.Controls.RawJSON = %s, want nil for Presence=%v", c.Transport.Controls.RawJSON, tt.wantPresence)
			}
		})
	}
}

// --- Required test 4: connected is never reduced to a bool ------------------

// TestConnectedNeverReducesToBoolAndPreservesEveryState decodes every one
// of the five clip `connected` states the capture names, including
// "Connected & previewing" — the state a naive `== "Connected"` predicate
// misses — and confirms Options is preserved verbatim rather than
// hard-coded, per capture section 4.3's warning that Options is not
// constant across objects of the same kind.
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
			raw := `{"connected": {"valuetype":"ParamState","id":8005,"value":"` + state + `","index":3,"options":` + options + `}}`
			var c Clip
			if err := json.Unmarshal([]byte(raw), &c); err != nil {
				t.Fatalf("json.Unmarshal error = %v", err)
			}
			if c.Connected.Value != state {
				t.Errorf("Connected.Value = %q, want %q", c.Connected.Value, state)
			}
			if len(c.Connected.Options) != 5 {
				t.Errorf("len(Connected.Options) = %d, want 5 (options must be carried verbatim, never hard-coded)", len(c.Connected.Options))
			}
		})
	}
}

// TestColumnConnectedIsADifferentOptionSetThanClipConnected proves the
// capture's other named hazard: a column's `connected` is a three-state
// set (Empty|Disconnected|Connected), NOT the clip's five-state set, even
// though both decode through the identical ParamState Go type. A decoder
// that assumed one universal enum for "connected" would be wrong for one
// of the two kinds.
func TestColumnConnectedIsADifferentOptionSetThanClipConnected(t *testing.T) {
	raw := `{"id": 1765224900001, "connected": {"valuetype":"ParamState","id":9020,"value":"Connected","index":2,"options":["Empty","Disconnected","Connected"]}}`
	var col Column
	if err := json.Unmarshal([]byte(raw), &col); err != nil {
		t.Fatalf("json.Unmarshal error = %v", err)
	}
	if len(col.Connected.Options) != 3 {
		t.Errorf("Column.Connected.Options = %v, want the 3-state set, not the clip's 5-state set", col.Connected.Options)
	}
	for _, forbidden := range []string{"Previewing", "Connected & previewing"} {
		for _, opt := range col.Connected.Options {
			if opt == forbidden {
				t.Errorf("Column.Connected.Options contains %q, which is only a valid clip-connected state, never a column state", forbidden)
			}
		}
	}
}

// --- layergroups[].layers is decoded down to member ids only ---------------

// TestLayerGroupMemberDecodesOnlyID confirms the duplicate-layer discard:
// a layergroups[].layers entry carrying a full layer's worth of extra
// fields (bypassed, master, video, clips, ...) decodes to nothing but its
// object id — proving this package never models the 49%-of-payload
// duplicate the capture found there.
func TestLayerGroupMemberDecodesOnlyID(t *testing.T) {
	raw := `{
		"id": 1765224910001,
		"name": {"valuetype":"ParamString","id":9030,"value":"Group 1"},
		"bypassed": {"valuetype":"ParamBoolean","id":9031,"value":false},
		"master": {"valuetype":"ParamRange","id":9032,"value":1.0},
		"layers": [
			{
				"id": 1765224917300,
				"bypassed": {"valuetype":"ParamBoolean","id":99991,"value":false},
				"master": {"valuetype":"ParamRange","id":99992,"value":0.9},
				"video": {"opacity": {"valuetype":"ParamRange","id":99993,"value":1.0}},
				"clips": [{"id": 1}, {"id": 2}, {"id": 3}]
			}
		]
	}`
	var g LayerGroup
	if err := json.Unmarshal([]byte(raw), &g); err != nil {
		t.Fatalf("json.Unmarshal error = %v", err)
	}
	if len(g.Layers) != 1 {
		t.Fatalf("len(LayerGroup.Layers) = %d, want 1", len(g.Layers))
	}
	if g.Layers[0].ID != 1765224917300 {
		t.Errorf("LayerGroup.Layers[0].ID = %v, want 1765224917300", g.Layers[0].ID)
	}
	// layerGroupMember has no other exported field to assert against by
	// construction — this test's real assertion is that the type checks
	// above compile and decode at all despite the extra JSON fields
	// (bypassed, master, video, clips) present on the wire.
}
