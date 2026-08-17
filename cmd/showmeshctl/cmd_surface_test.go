package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// This file tests Track E seam E1/E2's "surface" subcommands. Each drives
// a real httptest.Server, exactly like this package's other cmd_*_test.go
// files.

func TestCmdSurfaceListPassesShowFilter(t *testing.T) {
	var gotPath, gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"show.surface","objects":[
			{"id":"garage","label":"Garage Door","show":"halloween-2026","currentRevision":1,"updatedAt":"2026-08-16T20:00:00Z"}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdSurface([]string{"list", "--server", ts.URL, "--show", "halloween-2026"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/config/show.surface" {
		t.Errorf("path = %q, want /api/v1/config/show.surface", gotPath)
	}
	if gotQuery != "show=halloween-2026" {
		t.Errorf("query = %q, want show=halloween-2026", gotQuery)
	}
	if !strings.Contains(stdout.String(), "garage") {
		t.Errorf("stdout = %q, want it to name the surface id", stdout.String())
	}
}

// TestCmdSurfaceListPassesNodeFilter proves CLI parity (CLAUDE.md's "every
// API capability gets CLI coverage in the step that adds it") for the
// server-side ?node= filter added for the PR #14 review finding.
//
// Broken and confirmed to fail: reverted --node to a parsed-but-unused
// flag — gotQuery came back empty instead of "node=render-01" and the
// test failed on that assertion. Restored afterward.
func TestCmdSurfaceListPassesNodeFilter(t *testing.T) {
	var gotPath, gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"show.surface","objects":[
			{"id":"garage","label":"Garage Door","show":"halloween-2026","currentRevision":1,"updatedAt":"2026-08-16T20:00:00Z"}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdSurface([]string{"list", "--server", ts.URL, "--node", "render-01"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/config/show.surface" {
		t.Errorf("path = %q, want /api/v1/config/show.surface", gotPath)
	}
	if gotQuery != "node=render-01" {
		t.Errorf("query = %q, want node=render-01", gotQuery)
	}
}

// TestCmdSurfaceListPassesShowAndNodeFilterTogether proves both flags
// reach the request at once, matching the API's AND semantics.
func TestCmdSurfaceListPassesShowAndNodeFilterTogether(t *testing.T) {
	var gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"show.surface","objects":[]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdSurface(
		[]string{"list", "--server", ts.URL, "--show", "halloween-2026", "--node", "render-01"},
		&stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")),
	)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	q, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatalf("parsing query %q: %v", gotQuery, err)
	}
	if q.Get("show") != "halloween-2026" || q.Get("node") != "render-01" {
		t.Errorf("query = %q, want both show=halloween-2026 and node=render-01", gotQuery)
	}
}

func TestCmdSurfaceListWithoutShowSendsNoQuery(t *testing.T) {
	var gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"show.surface","objects":[]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdSurface([]string{"list", "--server", ts.URL}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotQuery != "" {
		t.Errorf("query = %q, want empty when --show is not given", gotQuery)
	}
}

func TestCmdSurfaceGetRendersDetail(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"show.surface","id":"garage","revision":1,
			"payload":{"show":"halloween-2026","name":"Garage Door","node":"render-01",
				"channelRange":{"startChannel":1,"channelCount":3600},
				"geometry":{"width":40,"height":30,"pixelFormat":"rgb"},
				"frameRate":40,
				"output":{"transport":"ndi","ndi":{"sourceName":"ShowMesh Garage"}}},
			"updatedAt":"2026-08-16T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdSurface([]string{"get", "--server", ts.URL, "garage"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"Garage Door", "render-01", "ndi", "ShowMesh Garage", "1-3600"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
}

// TestCmdSurfaceSetSendsFullPayloadNDI proves "surface set" builds and
// sends the complete ConfigShowSurface shape, with output.ndi populated
// and output.hdmi absent for --transport=ndi.
func TestCmdSurfaceSetSendsFullPayloadNDI(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"show.surface","id":"garage","revision":1,
			"payload":{"show":"halloween-2026","name":"Garage Door","node":"render-01",
				"channelRange":{"startChannel":1,"channelCount":3600},
				"geometry":{"width":40,"height":30,"pixelFormat":"rgb"},
				"frameRate":40,
				"output":{"transport":"ndi","ndi":{"sourceName":"ShowMesh Garage"}}},
			"updatedAt":"2026-08-16T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdSurface([]string{
		"set", "--server", ts.URL,
		"--show", "halloween-2026", "--name", "Garage Door", "--node", "render-01",
		"--start-channel", "1", "--channel-count", "3600",
		"--width", "40", "--height", "30", "--pixel-format", "rgb",
		"--frame-rate", "40",
		"--transport", "ndi", "--ndi-source-name", "ShowMesh Garage",
		"garage",
	}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/api/v1/config/show.surface/garage" {
		t.Errorf("path = %q, want /api/v1/config/show.surface/garage", gotPath)
	}

	var decoded configShowSurface
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("decoding request body: %v; body: %s", err, gotBody)
	}
	if decoded.Show != "halloween-2026" || decoded.Node != "render-01" {
		t.Errorf("decoded = %+v, want show=halloween-2026 node=render-01", decoded)
	}
	if decoded.ChannelRange.StartChannel != 1 || decoded.ChannelRange.ChannelCount != 3600 {
		t.Errorf("channelRange = %+v, want 1/3600", decoded.ChannelRange)
	}
	if decoded.Output.Transport != "ndi" || decoded.Output.NDI == nil || decoded.Output.NDI.SourceName != "ShowMesh Garage" {
		t.Errorf("output = %+v, want ndi with sourceName ShowMesh Garage", decoded.Output)
	}
	if decoded.Output.HDMI != nil {
		t.Errorf("output.hdmi = %+v, want absent for an ndi transport", decoded.Output.HDMI)
	}
	// The raw JSON must not carry an "hdmi" key at all — omitempty must
	// actually omit it, not merely decode back to nil.
	if strings.Contains(string(gotBody), `"hdmi"`) {
		t.Errorf("request body carries an \"hdmi\" key for an ndi-transport surface: %s", gotBody)
	}
}

// TestCmdSurfaceSetSendsFullPayloadHDMI is the --transport=hdmi mirror of
// the NDI case above.
func TestCmdSurfaceSetSendsFullPayloadHDMI(t *testing.T) {
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"show.surface","id":"porch","revision":1,
			"payload":{"show":"halloween-2026","name":"Porch","node":"render-01",
				"channelRange":{"startChannel":1,"channelCount":8},
				"geometry":{"width":2,"height":1,"pixelFormat":"rgbw"},
				"frameRate":30,
				"output":{"transport":"hdmi","hdmi":{"display":"HDMI-1"}}},
			"updatedAt":"2026-08-16T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdSurface([]string{
		"set", "--server", ts.URL,
		"--show", "halloween-2026", "--name", "Porch", "--node", "render-01",
		"--start-channel", "1", "--channel-count", "8",
		"--width", "2", "--height", "1", "--pixel-format", "rgbw",
		"--frame-rate", "30",
		"--transport", "hdmi", "--hdmi-display", "HDMI-1",
		"porch",
	}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}

	var decoded configShowSurface
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("decoding request body: %v; body: %s", err, gotBody)
	}
	if decoded.Output.Transport != "hdmi" || decoded.Output.HDMI == nil || decoded.Output.HDMI.Display != "HDMI-1" {
		t.Errorf("output = %+v, want hdmi with display HDMI-1", decoded.Output)
	}
	if decoded.Output.NDI != nil {
		t.Errorf("output.ndi = %+v, want absent for an hdmi transport", decoded.Output.NDI)
	}
	if strings.Contains(string(gotBody), `"ndi"`) {
		t.Errorf("request body carries an \"ndi\" key for an hdmi-transport surface: %s", gotBody)
	}
}

// TestCmdSurfaceSetRequiresAllFields proves this command refuses, before
// any request is sent, when a required flag is missing — never silently
// defaulting startChannel/channelCount/width/height/frameRate to 0, which
// the coordinator would refuse anyway but this program should catch first.
func TestCmdSurfaceSetRequiresAllFields(t *testing.T) {
	requested := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdSurface([]string{
		"set", "--server", ts.URL,
		"--show", "halloween-2026", "--name", "Garage Door",
		// --node, --start-channel, --channel-count, --width, --height,
		// --frame-rate, --transport all omitted.
		"garage",
	}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
	if requested {
		t.Errorf("a request was sent despite missing required flags")
	}
}

// TestCmdSurfaceSetRequiresTransportSpecificFlag proves --transport=ndi
// without --ndi-source-name (and the hdmi mirror) is refused client-side.
func TestCmdSurfaceSetRequiresTransportSpecificFlag(t *testing.T) {
	requested := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
	}))
	defer ts.Close()

	baseArgs := []string{
		"set", "--server", ts.URL,
		"--show", "halloween-2026", "--name", "Garage Door", "--node", "render-01",
		"--start-channel", "1", "--channel-count", "3600",
		"--width", "40", "--height", "30",
		"--frame-rate", "40",
	}

	t.Run("ndi without source name", func(t *testing.T) {
		requested = false
		var stdout, stderr bytes.Buffer
		args := append(append([]string{}, baseArgs...), "--transport", "ndi", "garage")
		code := cmdSurface(args, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
		if code != exitUsage {
			t.Errorf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
		}
		if requested {
			t.Errorf("a request was sent despite a missing --ndi-source-name")
		}
	})

	t.Run("hdmi without display", func(t *testing.T) {
		requested = false
		var stdout, stderr bytes.Buffer
		args := append(append([]string{}, baseArgs...), "--transport", "hdmi", "garage")
		code := cmdSurface(args, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
		if code != exitUsage {
			t.Errorf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
		}
		if requested {
			t.Errorf("a request was sent despite a missing --hdmi-display")
		}
	})
}

func TestCmdSurfaceUnknownSubcommandIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdSurface([]string{"bogus"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage", code)
	}
}
