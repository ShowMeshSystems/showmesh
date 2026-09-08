package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is ADR-033 show mode's own loop-level regression coverage:
// mode's cue-activation pin lives on [CueActivationLoop] itself
// (resolvePin, PinStatus), so it needs a real *store.Store behind
// Dependencies.Config/AssetManifests to exercise resolveShowMode and
// assetsync.ResolveActiveShow exactly as [CueActivationLoop.Run] would:
// never a hand-built *cueactivate.ShowPin asserted against in isolation,
// which would prove nothing about when the loop itself starts, keeps, or
// drops one.

func newPinTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "db"), nil, store.WithClock(fixedClock(testNow)))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// bumpActiveShowForTest activates a NEW show.active revision naming showID.
// putActiveShowForTest can only write the object's first revision, so a
// test proving re-pin-on-generation-change needs this instead.
func bumpActiveShowForTest(t *testing.T, st *store.Store, showID string) {
	t.Helper()
	payload, err := config.EncodeShowActivePayload(config.ShowActivePayload{Show: showID})
	if err != nil {
		t.Fatalf("encode show.active payload: %v", err)
	}
	ctx := context.Background()
	obj, err := st.GetConfigObject(ctx, config.ShowActiveConfigKind, config.ShowActiveObjectID)
	nextRevision := int64(1)
	if err == nil {
		nextRevision = obj.CurrentRevision + 1
	}
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.ShowActiveConfigKind, ObjectID: config.ShowActiveObjectID, Revision: nextRevision, PayloadJSON: payload, Source: "api",
	}); err != nil {
		t.Fatalf("create config revision show.active rev %d: %v", nextRevision, err)
	}
	if _, err := st.ActivateConfigRevision(ctx, config.ShowActiveConfigKind, config.ShowActiveObjectID, nextRevision); err != nil {
		t.Fatalf("activate config revision show.active rev %d: %v", nextRevision, err)
	}
}

// TestResolvePinProgramModeNeverPins proves ADR-033 program mode's own
// "close to today's behaviour" ruling at the loop level: with no show.mode
// ever written (the built-in default, program), resolvePin must return
// nil, and PinStatus must report unpinned.
func TestResolvePinProgramModeNeverPins(t *testing.T) {
	st := newPinTestStore(t)
	putShowForTest(t, st, "show-1", "Show One")
	putActiveShowForTest(t, st, "show-1")

	loop := NewCueActivationLoop(Dependencies{Config: st, AssetManifests: st}, Options{})

	pin, err := loop.resolvePin(context.Background())
	if err != nil {
		t.Fatalf("resolvePin: %v", err)
	}
	if pin != nil {
		t.Fatalf("resolvePin = %+v, want nil in program mode (the built-in default, never written)", pin)
	}
	pinned, _, _, _ := loop.PinStatus()
	if pinned {
		t.Fatal("PinStatus reports pinned = true in program mode, want false")
	}
}

// TestResolvePinShowModeStaysPinnedAcrossTicks proves the loop's own pin
// lifetime: while show.mode stays "show" and the active Show/Generation is
// unchanged, resolvePin must return the SAME *cueactivate.ShowPin instance
// every call, never a fresh one, which is what lets a mid-show show.cue
// edit stay invisible across repeated ticks. PinStatus must report the
// pinned Show/Generation the operator would see on the show.mode panel.
func TestResolvePinShowModeStaysPinnedAcrossTicks(t *testing.T) {
	st := newPinTestStore(t)
	putShowForTest(t, st, "show-1", "Show One")
	putActiveShowForTest(t, st, "show-1")
	putShowModeForTest(t, st, config.ShowModeShow)

	loop := NewCueActivationLoop(Dependencies{Config: st, AssetManifests: st}, Options{})

	pin1, err := loop.resolvePin(context.Background())
	if err != nil {
		t.Fatalf("resolvePin (1): %v", err)
	}
	if pin1 == nil {
		t.Fatal("resolvePin (1) = nil, want a pin in show mode")
	}
	pin2, err := loop.resolvePin(context.Background())
	if err != nil {
		t.Fatalf("resolvePin (2): %v", err)
	}
	if pin1 != pin2 {
		t.Fatalf("resolvePin returned a DIFFERENT pin on the second call with the same Show/Generation: %p vs %p; a mid-show cue edit would see a fresh resolution instead of staying staged", pin1, pin2)
	}

	pinned, show, generation, pinnedAt := loop.PinStatus()
	if !pinned {
		t.Fatal("PinStatus reports pinned = false in show mode with an active show, want true")
	}
	if show != "show-1" {
		t.Fatalf("PinStatus show = %q, want show-1", show)
	}
	if generation != 1 {
		t.Fatalf("PinStatus generation = %d, want 1 (show.active's own first revision)", generation)
	}
	if pinnedAt.IsZero() {
		t.Fatal("PinStatus pinnedAt is zero, want the time the pin was minted")
	}
}

// TestResolvePinRepinsOnGenerationChange proves "the show is fully stopped
// and restarted" in this repository's own terms (ADR-027: the active show
// is configuration; Generation is show.active's own revision number): a
// NEW show.active revision, even naming the SAME show id, must start a
// FRESH pin, so a show restart picks up whatever show.cue configuration is
// current at that moment, exactly like a fresh show start would.
func TestResolvePinRepinsOnGenerationChange(t *testing.T) {
	st := newPinTestStore(t)
	putShowForTest(t, st, "show-1", "Show One")
	putActiveShowForTest(t, st, "show-1")
	putShowModeForTest(t, st, config.ShowModeShow)

	loop := NewCueActivationLoop(Dependencies{Config: st, AssetManifests: st}, Options{})

	pinBefore, err := loop.resolvePin(context.Background())
	if err != nil {
		t.Fatalf("resolvePin (before restart): %v", err)
	}

	// The show is stopped and restarted: a new show.active revision.
	bumpActiveShowForTest(t, st, "show-1")

	pinAfter, err := loop.resolvePin(context.Background())
	if err != nil {
		t.Fatalf("resolvePin (after restart): %v", err)
	}
	if pinBefore == pinAfter {
		t.Fatal("resolvePin returned the SAME pin after the active show's generation changed, want a fresh one")
	}
	_, _, generation, _ := loop.PinStatus()
	if generation != 2 {
		t.Fatalf("PinStatus generation = %d after restart, want 2 (show.active's own second revision)", generation)
	}
}

// TestResolvePinDropsOnReturnToProgramMode proves leaving show mode drops
// any held pin: re-entering show mode later must start fresh rather than
// resuming a stale pin from before program mode, matching setup mode's own
// "live" semantics for the time spent there.
func TestResolvePinDropsOnReturnToProgramMode(t *testing.T) {
	st := newPinTestStore(t)
	putShowForTest(t, st, "show-1", "Show One")
	putActiveShowForTest(t, st, "show-1")
	putShowModeForTest(t, st, config.ShowModeShow)

	loop := NewCueActivationLoop(Dependencies{Config: st, AssetManifests: st}, Options{})
	if _, err := loop.resolvePin(context.Background()); err != nil {
		t.Fatalf("resolvePin (show mode): %v", err)
	}
	if pinned, _, _, _ := loop.PinStatus(); !pinned {
		t.Fatal("PinStatus reports pinned = false right after resolvePin in show mode, want true")
	}

	bumpShowModeForTest(t, st, config.ShowModeProgram)
	pin, err := loop.resolvePin(context.Background())
	if err != nil {
		t.Fatalf("resolvePin (back to program mode): %v", err)
	}
	if pin != nil {
		t.Fatalf("resolvePin = %+v after returning to program mode, want nil", pin)
	}
	if pinned, _, _, _ := loop.PinStatus(); pinned {
		t.Fatal("PinStatus reports pinned = true after returning to program mode, want false")
	}
}

// bumpShowModeForTest activates a NEW show.mode revision.
// putShowModeForTest can only write the object's first revision.
func bumpShowModeForTest(t *testing.T, st *store.Store, mode string) {
	t.Helper()
	payload, err := config.EncodeShowModePayload(config.ShowModePayload{Mode: mode})
	if err != nil {
		t.Fatalf("encode show.mode payload: %v", err)
	}
	ctx := context.Background()
	obj, err := st.GetConfigObject(ctx, config.ShowModeConfigKind, config.ShowModeConfigObjectID)
	nextRevision := int64(1)
	if err == nil {
		nextRevision = obj.CurrentRevision + 1
	}
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.ShowModeConfigKind, ObjectID: config.ShowModeConfigObjectID, Revision: nextRevision, PayloadJSON: payload, Source: "api",
	}); err != nil {
		t.Fatalf("create config revision show.mode rev %d: %v", nextRevision, err)
	}
	if _, err := st.ActivateConfigRevision(ctx, config.ShowModeConfigKind, config.ShowModeConfigObjectID, nextRevision); err != nil {
		t.Fatalf("activate config revision show.mode rev %d: %v", nextRevision, err)
	}
}
