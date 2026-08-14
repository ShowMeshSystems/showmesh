package api

import (
	"encoding/json"
	"sort"
	"testing"
)

// TestFPPPrimitiveRegistryAdapterWireActionsMatchesRegistry pins
// FPPPrimitiveRegistryAdapter.WireActions to fppCommandWireActions's own
// output byte-for-byte, per the wave 2 shared contract section 3: "so a
// ninth primitive cannot be added to one and not the other."
func TestFPPPrimitiveRegistryAdapterWireActionsMatchesRegistry(t *testing.T) {
	adapter := FPPPrimitiveRegistryAdapter{}
	got := adapter.WireActions()
	want := fppCommandWireActions()

	if len(got) != len(want) {
		t.Fatalf("length mismatch: adapter=%v registry=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mismatch at index %d: adapter=%q registry=%q (full: adapter=%v registry=%v)", i, got[i], want[i], got, want)
		}
	}
}

// TestFPPPrimitiveRegistryAdapterDecision11ClassAgreesForAllEight asserts
// Decision11Class agrees with FPPCommandDecision11ClassForAction for every
// registered primitive, per the wave 2 shared contract section 3.
func TestFPPPrimitiveRegistryAdapterDecision11ClassAgreesForAllEight(t *testing.T) {
	adapter := FPPPrimitiveRegistryAdapter{}
	for _, action := range fppCommandWireActions() {
		t.Run(action, func(t *testing.T) {
			gotClass, gotOK := adapter.Decision11Class(action)
			wantClass, wantOK := FPPCommandDecision11ClassForAction(action)
			if gotOK != wantOK {
				t.Fatalf("ok mismatch for %q: adapter=%v registry=%v", action, gotOK, wantOK)
			}
			if gotClass != string(wantClass) {
				t.Fatalf("class mismatch for %q: adapter=%q registry=%q", action, gotClass, wantClass)
			}
		})
	}
}

func TestFPPPrimitiveRegistryAdapterDecision11ClassUnknownAction(t *testing.T) {
	adapter := FPPPrimitiveRegistryAdapter{}
	_, ok := adapter.Decision11Class("doTheHustle")
	if ok {
		t.Fatal("expected ok=false for an unregistered action")
	}
}

func TestFPPPrimitiveRegistryAdapterDecodeActionParamsUnknownAction(t *testing.T) {
	adapter := FPPPrimitiveRegistryAdapter{}
	_, err := adapter.DecodeActionParams("doTheHustle", map[string]json.RawMessage{})
	if err == nil {
		t.Fatal("expected an error for an unregistered action")
	}
}

func TestFPPPrimitiveRegistryAdapterDecodeActionParamsStopPlaylistNoParams(t *testing.T) {
	adapter := FPPPrimitiveRegistryAdapter{}
	params, err := adapter.DecodeActionParams("stopPlaylist", map[string]json.RawMessage{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(params) != 0 {
		t.Fatalf("expected an empty params map for stopPlaylist, got %+v", params)
	}
}

func TestFPPPrimitiveRegistryAdapterDecodeActionParamsStartPlaylistValid(t *testing.T) {
	adapter := FPPPrimitiveRegistryAdapter{}
	top := map[string]json.RawMessage{
		"params": json.RawMessage(`{"playlist": "Halloween Main"}`),
	}
	params, err := adapter.DecodeActionParams("startPlaylist", top)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if params["playlist"] != "Halloween Main" {
		t.Fatalf("unexpected params: %+v", params)
	}
	// ifBusy's own default, applied by decodeFPPCommandParams.
	if params["ifBusy"] != fppIfBusyRefuse {
		t.Fatalf("expected ifBusy to default to %q, got %v", fppIfBusyRefuse, params["ifBusy"])
	}
}

// TestFPPPrimitiveRegistryAdapterDecodeActionParamsRunsValidateParams
// proves the adapter runs BOTH decode stages STEP-9-SPEC.md section 5.3
// requires: generic shape decode succeeding is not enough by itself if the
// primitive's own ValidateParams (playlist name syntax here) would reject
// the value — "an action authored with a bad playlist type fails at write
// time."
func TestFPPPrimitiveRegistryAdapterDecodeActionParamsRunsValidateParams(t *testing.T) {
	adapter := FPPPrimitiveRegistryAdapter{}
	top := map[string]json.RawMessage{
		// Generic shape decode succeeds (playlist is a present, non-empty
		// string) but ValidatePlaylistName is expected to reject a name
		// this long/malformed — using an empty-after-defaulting case is
		// covered by decodeFPPCommandParams itself, so this test instead
		// forces a value ValidateParams's own logic (not the generic
		// decoder) must catch: an out-of-range volume.
		"params": json.RawMessage(`{"volume": 999}`),
	}
	_, err := adapter.DecodeActionParams("setVolume", top)
	if err == nil {
		t.Fatal("expected an error for an out-of-range volume, caught by ValidateParams")
	}
}

func TestFPPPrimitiveRegistryAdapterDecodeActionParamsAbsentNullEmptyPropagate(t *testing.T) {
	adapter := FPPPrimitiveRegistryAdapter{}

	t.Run("required-param-absent", func(t *testing.T) {
		_, err := adapter.DecodeActionParams("startPlaylist", map[string]json.RawMessage{
			"params": json.RawMessage(`{}`),
		})
		if err == nil {
			t.Fatal("expected an error for an absent required playlist param")
		}
	})
	t.Run("params-null", func(t *testing.T) {
		_, err := adapter.DecodeActionParams("startPlaylist", map[string]json.RawMessage{
			"params": json.RawMessage(`null`),
		})
		if err == nil {
			t.Fatal("expected an error for params: null")
		}
	})
}

// TestFPPCommandWireActionsSorted is a sanity check underlying the two
// pinning tests above: fppCommandWireActions is documented sorted, and this
// test would fail if that changed silently, since both pinning tests
// compare element-by-element rather than as sets.
func TestFPPCommandWireActionsSorted(t *testing.T) {
	actions := fppCommandWireActions()
	sorted := append([]string(nil), actions...)
	sort.Strings(sorted)
	for i := range actions {
		if actions[i] != sorted[i] {
			t.Fatalf("fppCommandWireActions is not sorted: %v", actions)
		}
	}
}
