package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
)

// coordinatorRenderApplyParamsPayload mirrors
// internal/coordinator/api/renderdispatch.go's renderApplyParamsPayload
// field-for-field and tag-for-tag, matching this codebase's standing
// each-side-of-a-wire-boundary-decodes/encodes-independently convention
// (surfaceIDPattern's own doc comment applies it once already, and
// renderApplyKnownKeys' comment names this exact payload). It exists so a
// test in THIS package can build the params object the way the real
// coordinator's resolveRenderApplyParams actually does — "show" populated,
// "generation"/"catalogRevision" absent, because that function does not
// send H3's tuple yet — without importing internal/coordinator/api (which
// would not even work: renderApplyParamsPayload is unexported there). If
// that payload's shape changes, this struct silently drifts out of sync and
// TestApplySurfaceAcceptsRealCoordinatorRenderApplyParams below should be
// revisited.
type coordinatorRenderApplyParamsPayload struct {
	SurfaceID       string                        `json:"surfaceId"`
	Show            string                        `json:"show"`
	Name            string                        `json:"name"`
	Node            string                        `json:"node"`
	ChannelRange    coordinatorShowSurfaceChannel `json:"channelRange"`
	Geometry        coordinatorShowSurfaceGeom    `json:"geometry"`
	FrameRate       int                           `json:"frameRate"`
	Output          coordinatorShowSurfaceOutput  `json:"output"`
	FSEQFilename    string                        `json:"fseqFilename"`
	FSEQContentHash string                        `json:"fseqContentHash"`
	IdleOutput      string                        `json:"idleOutput"`
}

type coordinatorShowSurfaceChannel struct {
	StartChannel int `json:"startChannel"`
	ChannelCount int `json:"channelCount"`
}

type coordinatorShowSurfaceGeom struct {
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	PixelFormat string `json:"pixelFormat"`
}

type coordinatorShowSurfaceOutput struct {
	Transport string                     `json:"transport"`
	NDI       *coordinatorShowSurfaceNDI `json:"ndi,omitempty"`
}

type coordinatorShowSurfaceNDI struct {
	SourceName string `json:"sourceName"`
}

func TestParseAssignmentAuthAbsentIsNilNotZero(t *testing.T) {
	auth, err := parseAssignmentAuth("render.surface.apply", map[string]any{"surfaceId": "s1"})
	if err != nil {
		t.Fatalf("parseAssignmentAuth with no tuple keys: %v", err)
	}
	if auth != nil {
		t.Fatalf("parseAssignmentAuth returned %+v for params with no tuple keys, want nil", auth)
	}
}

func TestParseAssignmentAuthComplete(t *testing.T) {
	params := map[string]any{
		"surfaceId": "s1", "show": "halloween-2026", "generation": float64(3), "catalogRevision": "rev-a",
	}
	auth, err := parseAssignmentAuth("render.surface.apply", params)
	if err != nil {
		t.Fatalf("parseAssignmentAuth: %v", err)
	}
	if auth == nil {
		t.Fatalf("parseAssignmentAuth returned nil for a complete tuple")
	}
	want := pipeline.AssignmentAuth{Show: "halloween-2026", Generation: 3, CatalogRevision: "rev-a"}
	if *auth != want {
		t.Fatalf("parseAssignmentAuth = %+v, want %+v", *auth, want)
	}
}

func TestParseAssignmentAuthPartialIsRefused(t *testing.T) {
	cases := []map[string]any{
		{"surfaceId": "s1", "generation": float64(3), "catalogRevision": "rev-a"},
		{"surfaceId": "s1", "show": "halloween-2026", "catalogRevision": "rev-a"},
		{"surfaceId": "s1", "show": "halloween-2026", "generation": float64(3)},
	}
	for _, params := range cases {
		if _, err := parseAssignmentAuth("render.surface.apply", params); err == nil {
			t.Fatalf("parseAssignmentAuth accepted a partial tuple: %v", params)
		}
	}
}

// TestApplySurfacePersistsAuthorizationTuple proves render.surface.apply
// carries an optional authorization tuple (per TRACK-H-H3-SPEC.md section
// 6's ruling that cuecatalog.deploy's params carry it "exactly as
// render.surface.apply carries its parameters") through to the persisted
// [pipeline.Assignment], for the boot-clearing check to read back later.
func TestApplySurfacePersistsAuthorizationTuple(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)

	params := minimalRenderApplyParams("surface-1")
	params["show"] = "halloween-2026"
	params["generation"] = float64(3)
	params["catalogRevision"] = "rev-a"
	result, err := renderOps.applySurface(context.Background(), params, clock.now)
	if err != nil {
		t.Fatalf("applySurface: %v", err)
	}
	if !result.Confirmed {
		t.Fatalf("applySurface did not confirm")
	}

	reloaded, err := pipeline.NewAssignmentStore(dir).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(reloaded) != 1 {
		t.Fatalf("persisted assignments = %+v, want one entry", reloaded)
	}
	if reloaded[0].Auth == nil {
		t.Fatalf("persisted assignment has no authorization tuple")
	}
	want := pipeline.AssignmentAuth{Show: "halloween-2026", Generation: 3, CatalogRevision: "rev-a"}
	if *reloaded[0].Auth != want {
		t.Fatalf("persisted authorization tuple = %+v, want %+v", *reloaded[0].Auth, want)
	}
}

// TestApplySurfaceWithNoAuthorizationTupleIsAcceptedForLegacyCallers
// proves an apply carrying none of the three tuple keys is still accepted
// (a coordinator not yet sending H4's authorization tuple), and persists
// with Auth == nil rather than being refused outright.
func TestApplySurfaceWithNoAuthorizationTupleIsAcceptedForLegacyCallers(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)

	if _, err := renderOps.applySurface(context.Background(), minimalRenderApplyParams("surface-1"), clock.now); err != nil {
		t.Fatalf("applySurface: %v", err)
	}

	reloaded, err := pipeline.NewAssignmentStore(dir).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(reloaded) != 1 || reloaded[0].Auth != nil {
		t.Fatalf("persisted assignments = %+v, want one entry with Auth == nil", reloaded)
	}
}

// TestApplySurfaceAcceptsRealCoordinatorRenderApplyParams is the regression
// test for the defect a fresh reviewer found: parseAssignmentAuth used to
// treat "show" as a member of the H3 authorization tuple's presence test,
// even though "show" was already a required, always-populated
// render.surface.apply field before H3 existed (renderApplyParamsPayload's
// Show field has no `omitempty` and internal/coordinator/api/renderdispatch.
// go's resolveRenderApplyParams always sets it, but never sets
// "generation"/"catalogRevision" — H4, not H3, is what will start sending
// those). Every prior test in this file synthesizes params missing "show"
// entirely, which is why a green `go test ./...` never caught it: a REAL
// apply from the REAL coordinator always carries "show" with no
// "generation" or "catalogRevision", which used to hit "must be supplied
// together or not at all" and refuse every render assignment on the fleet.
func TestApplySurfaceAcceptsRealCoordinatorRenderApplyParams(t *testing.T) {
	dir := t.TempDir()
	const channelCount = 12 // 2x2 rgb: width*height*3 = 12
	path := writeSynthFSEQ(t, dir, "surface-1.fseq", channelCount, 10, 5)
	hash, err := hashFile(path)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}

	clock := &fakeClock{t: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)

	payload := coordinatorRenderApplyParamsPayload{
		SurfaceID: "surface-1",
		Show:      "halloween-2026",
		Name:      "wall-1",
		Node:      testNodeID,
		ChannelRange: coordinatorShowSurfaceChannel{
			StartChannel: 1, ChannelCount: channelCount,
		},
		Geometry:  coordinatorShowSurfaceGeom{Width: 2, Height: 2, PixelFormat: "rgb"},
		FrameRate: 40,
		Output: coordinatorShowSurfaceOutput{
			Transport: "ndi",
			NDI:       &coordinatorShowSurfaceNDI{SourceName: "wall-1"},
		},
		FSEQFilename:    "surface-1.fseq",
		FSEQContentHash: hash,
		IdleOutput:      pipeline.IdleOutputBlack,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal coordinator payload: %v", err)
	}
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("unmarshal coordinator payload back into params: %v", err)
	}
	if _, hasGeneration := params["generation"]; hasGeneration {
		t.Fatalf("test payload unexpectedly carries generation; the real coordinator does not send it yet")
	}
	if _, hasCatalogRevision := params["catalogRevision"]; hasCatalogRevision {
		t.Fatalf("test payload unexpectedly carries catalogRevision; the real coordinator does not send it yet")
	}
	if show, _ := params["show"].(string); show == "" {
		t.Fatalf("test payload does not carry show; the real coordinator always sends it")
	}

	result, err := renderOps.applySurface(context.Background(), params, clock.now)
	if err != nil {
		t.Fatalf("applySurface refused a real coordinator render.surface.apply payload: %v", err)
	}
	if !result.Confirmed {
		t.Fatalf("applySurface did not confirm: %+v", result)
	}
}
