package config

import (
	"strings"
	"testing"
)

func TestLoadConfigDiagnosticSurfaceDefaultsToDisabled(t *testing.T) {
	cfg, err := LoadConfigFrom(lookupFrom(map[string]string{"SHOWMESH_NODE_ID": "node-1"}), unreachableHostname(t))
	if err != nil {
		t.Fatalf("LoadConfigFrom: %v", err)
	}
	if cfg.DiagnosticSurface.Enabled() {
		t.Fatalf("diagnostic surface is enabled with no configuration: %+v", cfg.DiagnosticSurface)
	}
}

func TestLoadConfigDiagnosticSurfaceFromEnv(t *testing.T) {
	cfg, err := LoadConfigFrom(lookupFrom(map[string]string{
		"SHOWMESH_NODE_ID":                           "node-1",
		"SHOWMESH_RENDER_DIAGNOSTIC_SURFACE":         "front-wall",
		"SHOWMESH_RENDER_DIAGNOSTIC_NDI_SOURCE_NAME": "ShowMesh Diagnostic",
	}), unreachableHostname(t))
	if err != nil {
		t.Fatalf("LoadConfigFrom: %v", err)
	}
	if !cfg.DiagnosticSurface.Enabled() {
		t.Fatal("diagnostic surface is disabled after being named")
	}
	want := DiagnosticSurface{
		SurfaceID:     "front-wall",
		Width:         defaultDiagnosticWidth,
		Height:        defaultDiagnosticHeight,
		FrameRate:     defaultDiagnosticFrameRate,
		NDISourceName: "ShowMesh Diagnostic",
	}
	if cfg.DiagnosticSurface != want {
		t.Fatalf("diagnostic surface = %+v, want %+v", cfg.DiagnosticSurface, want)
	}
	if !strings.Contains(redactedConfig(cfg), "front-wall") {
		t.Fatalf("the logged config does not name the diagnostic surface: %s", redactedConfig(cfg))
	}
}

// TestConfigValidateRejectsInvalidDiagnosticSurface covers Validate
// directly, for a Config assembled in code rather than from the
// environment: LoadConfigFrom's own parsing refuses these first, so without
// this test Validate's diagnostic block would never actually be exercised.
func TestConfigValidateRejectsInvalidDiagnosticSurface(t *testing.T) {
	base, err := LoadConfigFrom(lookupFrom(map[string]string{"SHOWMESH_NODE_ID": "node-1"}), unreachableHostname(t))
	if err != nil {
		t.Fatalf("LoadConfigFrom: %v", err)
	}

	cases := map[string]DiagnosticSurface{
		"zero width":      {SurfaceID: "front-wall", Width: 0, Height: 1080, FrameRate: 40},
		"zero height":     {SurfaceID: "front-wall", Width: 1920, Height: 0, FrameRate: 40},
		"zero frame rate": {SurfaceID: "front-wall", Width: 1920, Height: 1080, FrameRate: 0},
	}
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := base
			cfg.DiagnosticSurface = d
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate accepted an invalid diagnostic surface")
			}
		})
	}

	cfg := base
	cfg.DiagnosticSurface = DiagnosticSurface{Width: 0, Height: 0, FrameRate: 0}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate rejected a config with no diagnostic surface at all: %v", err)
	}
}

func TestLoadConfigDiagnosticSurfaceValidationFailures(t *testing.T) {
	cases := map[string]map[string]string{
		"geometry without a surface id": {"SHOWMESH_RENDER_DIAGNOSTIC_WIDTH": "1280"},
		"ndi name without a surface id": {"SHOWMESH_RENDER_DIAGNOSTIC_NDI_SOURCE_NAME": "wall"},
		"non-numeric width": {
			"SHOWMESH_RENDER_DIAGNOSTIC_SURFACE": "front-wall",
			"SHOWMESH_RENDER_DIAGNOSTIC_WIDTH":   "wide",
		},
		"zero height": {
			"SHOWMESH_RENDER_DIAGNOSTIC_SURFACE": "front-wall",
			"SHOWMESH_RENDER_DIAGNOSTIC_HEIGHT":  "0",
		},
		"negative frame rate": {
			"SHOWMESH_RENDER_DIAGNOSTIC_SURFACE":    "front-wall",
			"SHOWMESH_RENDER_DIAGNOSTIC_FRAME_RATE": "-1",
		},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			env["SHOWMESH_NODE_ID"] = "node-1"
			if _, err := LoadConfigFrom(lookupFrom(env), unreachableHostname(t)); err == nil {
				t.Fatal("LoadConfigFrom accepted an invalid diagnostic surface configuration")
			}
		})
	}
}
