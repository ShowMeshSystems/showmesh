package macro

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
)

// TestResolveActionRestoresIntegerParamTypes is the regression test for a
// defect that reported success while doing the wrong thing, which is the
// hardest class this project has to catch.
//
// The config write path normalizes an FPP action's params to string, bool
// and int64. Storing that map marshals it to JSON; resolving a pinned
// revision unmarshals it back into map[string]any, which yields float64
// for every number. Every integer-valued primitive reads its parameter
// through a params["x"].(int64) assertion whose ok is deliberately
// discarded, because at the command endpoint the value cannot be anything
// else. So volume 50 became volume 0, and because the desired state and
// the confirmation predicate both read the same zero, the step confirmed.
//
// This asserts the type, not just the value, because the value alone would
// pass under float64(50) too and prove nothing.
func TestResolveActionRestoresIntegerParamTypes(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	putAction(t, st, "house-volume", fppAction("fpp-main", "setVolume", "none", map[string]any{"volume": int64(50)}))

	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, nil)
	got, err := e.resolveAction(context.Background(), "house-volume")
	if err != nil {
		t.Fatalf("resolveAction() error = %v, want nil", err)
	}

	raw, ok := got.Payload.Target.Params["volume"]
	if !ok {
		t.Fatal(`resolved params has no "volume" key`)
	}
	v, ok := raw.(int64)
	if !ok {
		t.Fatalf("resolved volume is %T(%v), want int64: every FPP primitive reads it through an int64 assertion whose ok is discarded, so any other type silently becomes 0", raw, raw)
	}
	if v != 50 {
		t.Errorf("resolved volume = %d, want 50", v)
	}
}

// TestResolveActionPreservesStringAndBoolParams confirms the renormalization
// did not disturb the param kinds that already survived the round trip.
func TestResolveActionPreservesStringAndBoolParams(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	putAction(t, st, "start-main", fppAction("fpp-main", "startPlaylist", "none", map[string]any{
		"playlist": "Halloween Main",
		"ifBusy":   "refuse",
	}))

	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, nil)
	got, err := e.resolveAction(context.Background(), "start-main")
	if err != nil {
		t.Fatalf("resolveAction() error = %v, want nil", err)
	}
	if p, ok := got.Payload.Target.Params["playlist"].(string); !ok || p != "Halloween Main" {
		t.Errorf(`resolved playlist = %#v, want the string "Halloween Main"`, got.Payload.Target.Params["playlist"])
	}
	if b, ok := got.Payload.Target.Params["ifBusy"].(string); !ok || b != "refuse" {
		t.Errorf(`resolved ifBusy = %#v, want the string "refuse"`, got.Payload.Target.Params["ifBusy"])
	}
}

// TestDispatchedFPPParamsCarryIntegerTypes closes the loop the unit above
// leaves open: it is the params actually handed to the dispatch seam that
// matter, not the ones resolveAction returns, and only an end-to-end run
// proves nothing in between re-widens them.
func TestDispatchedFPPParamsCarryIntegerTypes(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	putAction(t, st, "house-volume", fppAction("fpp-main", "setVolume", "none", map[string]any{"volume": int64(50)}))
	putMacro(t, st, "set-volume", testMacroPayload(testStep("vol", "house-volume")))

	fd := &fakeDispatcher{}
	e, _ := newTestExecutor(t, st, svc, fd, nil)

	res, problem, err := e.SubmitRun(context.Background(), api.MacroSubmitRequest{
		MacroObjectID: "set-volume", IdempotencyKey: "k1", Trigger: "api", Issuer: testIssuer(),
	})
	if err != nil || problem != nil {
		t.Fatalf("SubmitRun() = problem %v, err %v, want neither", problem, err)
	}
	if err := e.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	_ = res

	in := fd.lastInput()
	raw, ok := in.Params["volume"]
	if !ok {
		t.Fatal(`the dispatched command carried no "volume" param`)
	}
	v, ok := raw.(int64)
	if !ok {
		t.Fatalf("dispatched volume is %T(%v), want int64: the primitive's own assertion discards ok, so this dispatches 0 and then confirms against 0", raw, raw)
	}
	if v != 50 {
		t.Errorf("dispatched volume = %d, want 50", v)
	}
}
