package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

// This file tests Track F seam F1's "night" subcommand. Each drives a
// real httptest.Server, matching cmd_show_test.go's own established
// pattern one seam over: the request this program actually issues
// (method, path, body) is what is under test, not a mock of the client.

const nightSessionSampleJSON = `{"serverTime":"2026-08-16T21:00:00Z","kind":"night.session","id":"halloween-main","revision":1,
	"payload":{
		"show":"halloween-2026","label":"Halloween main loop",
		"showPlaylist":{"fppInstanceId":"player-01","playlist":"halloween-show"},
		"resting":{
			"fppInstanceId":"player-01","playlist":"halloween-resting","endOfNightPlaylist":"halloween-resting",
			"timelineAsset":{"show":"halloween-2026","sequence":"resting-loop","target":"player-01"},
			"endOfNightRepeat":true
		},
		"enterShow":{"cues":[{"name":"lighting-fade","role":"lighting","action":"lighting-fade-out","offsetMs":-20000,"barrier":true,"onFailure":"continue"}],"blackoutHoldMs":6000},
		"enterResting":{"cues":[],"blackoutAfterShowMs":6000}
	},
	"updatedAt":"2026-08-16T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`

func TestCmdNightListRendersObjects(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"night.session","objects":[
			{"id":"halloween-main","label":"Halloween main loop","show":"halloween-2026","currentRevision":1,"updatedAt":"2026-08-16T20:00:00Z"}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"list", "--server", ts.URL}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/config/night.session" {
		t.Errorf("path = %q, want /api/v1/config/night.session", gotPath)
	}
	if !strings.Contains(stdout.String(), "halloween-main") {
		t.Errorf("stdout = %q, want it to name the session id", stdout.String())
	}
}

func TestCmdNightGetRendersDetail(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, nightSessionSampleJSON)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"get", "--server", ts.URL, "halloween-main"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "Halloween main loop") || !strings.Contains(out, "lighting-fade") {
		t.Errorf("stdout missing expected detail; got: %s", out)
	}
}

// nightSessionSampleJSONWithSiteControlAndInterlocks is
// nightSessionSampleJSON plus an authored siteControl block and
// interlocks list, proving showmeshctl's existing client types
// (types_night.go) and print path (night_print.go) round-trip a real
// server response that carries them: this command issues no request of
// its own for these fields (they arrive however the coordinator sends
// them), so this is coverage for the decode/print side of the same
// response-mapping gap the coordinator API fix (mapConfigNightSession)
// closes.
const nightSessionSampleJSONWithSiteControlAndInterlocks = `{"serverTime":"2026-08-16T21:00:00Z","kind":"night.session","id":"halloween-main","revision":1,
	"payload":{
		"show":"halloween-2026","label":"Halloween main loop",
		"showPlaylist":{"fppInstanceId":"player-01","playlist":"halloween-show"},
		"resting":{
			"fppInstanceId":"player-01","playlist":"halloween-resting","endOfNightPlaylist":"halloween-resting",
			"timelineAsset":{"show":"halloween-2026","sequence":"resting-loop","target":"player-01"},
			"endOfNightRepeat":true
		},
		"enterShow":{"cues":[{"name":"lighting-fade","role":"lighting","action":"lighting-fade-out","offsetMs":-20000,"barrier":true,"onFailure":"continue"}],"blackoutHoldMs":6000},
		"enterResting":{"cues":[],"blackoutAfterShowMs":6000},
		"announcementDefaultPolicy":"duck",
		"siteControl":{
			"requestThermalProfile":"thermal-profile",
			"presentationPowerOn":{"action":"power-on","powerDomain":"presentation","domainProvenance":"operator-declared"},
			"presentationPowerOff":{"action":"power-off","powerDomain":"presentation","domainProvenance":"operator-declared","removalPolicy":"after-actions","prerequisites":[{"kind":"action","action":"power-off-prep"}]}
		},
		"interlocks":[
			{"name":"cooldown","phase":"prepare-site","posture":"block","signal":"cooldown-check","failureText":"not cool","onUnavailable":"block","overridePolicy":"authorized-operator"}
		]
	},
	"updatedAt":"2026-08-16T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`

// TestCmdNightGetRendersSiteControlAndInterlocks proves showmeshctl parity
// for the two Track F seam F6 blocks: "night get" decodes and prints an
// authored siteControl binding and interlock rule from a real server
// response, not just "not configured".
func TestCmdNightGetRendersSiteControlAndInterlocks(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, nightSessionSampleJSONWithSiteControlAndInterlocks)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"get", "--server", ts.URL, "halloween-main"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"thermal-profile", "power-on", "power-off", "after-actions", "power-off-prep", "cooldown", "prepare-site", "authorized-operator"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got: %s", want, out)
		}
	}
}

// nightSessionSampleJSONWithInlineBackgroundAudioTargets is
// nightSessionSampleJSON plus an inline resting.backgroundAudio carrying
// ADR-049 decision 7's own targets list, for both the text-output and
// round-trip coverage below.
const nightSessionSampleJSONWithInlineBackgroundAudioTargets = `{"serverTime":"2026-08-16T21:00:00Z","kind":"night.session","id":"halloween-main","revision":1,
	"payload":{
		"show":"halloween-2026","label":"Halloween main loop",
		"showPlaylist":{"fppInstanceId":"player-01","playlist":"halloween-show"},
		"resting":{
			"fppInstanceId":"player-01","playlist":"halloween-resting","endOfNightPlaylist":"halloween-resting",
			"timelineAsset":{"show":"halloween-2026","sequence":"resting-loop","target":"player-01"},
			"endOfNightRepeat":true,
			"backgroundAudio":{
				"items":[{"itemId":"track-1","show":"halloween-2026","sequence":"bg-track-1","target":"player-01"}],
				"repeat":"none","resume":"resume","itemTransition":"sequential","maxGainDb":-10,
				"targets":["player-01","player-02"]
			}
		},
		"enterShow":{"cues":[],"blackoutHoldMs":6000},
		"enterResting":{"cues":[],"blackoutAfterShowMs":6000}
	},
	"updatedAt":"2026-08-16T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`

// nightSessionSampleJSONWithReferenceBackgroundAudioTargets is the same
// session, but with the REFERENCE (media.playlist) form of the bed, plus
// the same targets list - ADR-049 decision 7 applies identically to both
// forms, so this file exercises both instead of only the inline one.
const nightSessionSampleJSONWithReferenceBackgroundAudioTargets = `{"serverTime":"2026-08-16T21:00:00Z","kind":"night.session","id":"halloween-main","revision":1,
	"payload":{
		"show":"halloween-2026","label":"Halloween main loop",
		"showPlaylist":{"fppInstanceId":"player-01","playlist":"halloween-show"},
		"resting":{
			"fppInstanceId":"player-01","playlist":"halloween-resting","endOfNightPlaylist":"halloween-resting",
			"timelineAsset":{"show":"halloween-2026","sequence":"resting-loop","target":"player-01"},
			"endOfNightRepeat":true,
			"backgroundAudio":{"mediaPlaylist":"holiday-bed","targets":["player-01","player-02"]}
		},
		"enterShow":{"cues":[],"blackoutHoldMs":6000},
		"enterResting":{"cues":[],"blackoutAfterShowMs":6000}
	},
	"updatedAt":"2026-08-16T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`

// TestCmdNightGetRendersBackgroundAudioTargets proves "night get"'s text
// output states the bed's own targets for both forms, and the documented
// fallback sentence when a bed is configured but declares none (ADR-049
// decision 7: absent/empty is today's per-node behavior, not "no bed").
func TestCmdNightGetRendersBackgroundAudioTargets(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want []string
	}{
		{"inline form", nightSessionSampleJSONWithInlineBackgroundAudioTargets, []string{"Targets: player-01, player-02"}},
		{"reference form", nightSessionSampleJSONWithReferenceBackgroundAudioTargets, []string{"mediaPlaylist=holiday-bed", "Targets: player-01, player-02"}},
		{"no targets declared", nightSessionSampleJSON, []string{"Background audio:    (not configured)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("ShowMesh-API-Version", "1")
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer ts.Close()

			var stdout, stderr bytes.Buffer
			code := cmdNight([]string{"get", "--server", ts.URL, "halloween-main"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
			if code != exitOK {
				t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
			}
			out := stdout.String()
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("stdout missing %q; got: %s", want, out)
				}
			}
		})
	}
}

// TestCmdNightGetRendersBackgroundAudioWithNoTargetsDeclared covers the
// documented fallback sentence for a bed that IS configured but declares
// no targets, distinct from the "(not configured)" case above.
func TestCmdNightGetRendersBackgroundAudioWithNoTargetsDeclared(t *testing.T) {
	body := strings.Replace(nightSessionSampleJSONWithInlineBackgroundAudioTargets, `,
				"targets":["player-01","player-02"]`, "", 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, body)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"get", "--server", ts.URL, "halloween-main"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Targets: none: each node plays its registered items") {
		t.Errorf("stdout missing the no-targets fallback sentence; got: %s", stdout.String())
	}
}

// TestCmdNightGetSetRoundTripsBackgroundAudioTargets proves "night get
// --output json | night set" carries resting.backgroundAudio.targets
// through unchanged for both bed forms - the exact round trip
// types_night.go's own doc comment promises, and the reason this test
// drives the real "get" then "set" commands rather than hand-building a
// draft.
func TestCmdNightGetSetRoundTripsBackgroundAudioTargets(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"inline form", nightSessionSampleJSONWithInlineBackgroundAudioTargets},
		{"reference form", nightSessionSampleJSONWithReferenceBackgroundAudioTargets},
	} {
		t.Run(tc.name, func(t *testing.T) {
			getServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("ShowMesh-API-Version", "1")
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer getServer.Close()

			var getOut, getErr bytes.Buffer
			code := cmdNight([]string{"get", "--server", getServer.URL, "--output", "json", "halloween-main"}, &getOut, &getErr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
			if code != exitOK {
				t.Fatalf("night get exit code = %d, want exitOK; stderr=%s", code, getErr.String())
			}

			var gotPutBody []byte
			putServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPutBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("ShowMesh-API-Version", "1")
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer putServer.Close()

			oldStdin := os.Stdin
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatalf("os.Pipe: %v", err)
			}
			os.Stdin = r
			defer func() { os.Stdin = oldStdin }()
			piped := getOut.Bytes()
			go func() {
				_, _ = w.Write(piped)
				_ = w.Close()
			}()

			var setOut, setErr bytes.Buffer
			code = cmdNight([]string{"set", "--server", putServer.URL, "halloween-main"}, &setOut, &setErr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
			if code != exitOK {
				t.Fatalf("night set exit code = %d, want exitOK; stderr=%s", code, setErr.String())
			}

			var sent struct {
				Resting struct {
					BackgroundAudio map[string]json.RawMessage `json:"backgroundAudio"`
				} `json:"resting"`
			}
			if err := json.Unmarshal(gotPutBody, &sent); err != nil {
				t.Fatalf("PUT body was not valid JSON: %v; body=%s", err, gotPutBody)
			}
			ba := sent.Resting.BackgroundAudio
			if ba == nil {
				t.Fatalf("PUT body has no resting.backgroundAudio at all: %s", gotPutBody)
			}
			var targets []string
			if err := json.Unmarshal(ba["targets"], &targets); err != nil {
				t.Fatalf("resting.backgroundAudio.targets did not decode: %v; body=%s", err, gotPutBody)
			}
			if want := []string{"player-01", "player-02"}; !reflect.DeepEqual(targets, want) {
				t.Fatalf("PUT body targets = %v, want %v - the round trip must survive", targets, want)
			}
			if tc.name == "reference form" {
				if _, ok := ba["items"]; ok {
					t.Fatalf("reference-form PUT body carries an \"items\" key, which the reference write shape forbids: %s", gotPutBody)
				}
				var mediaPlaylist string
				_ = json.Unmarshal(ba["mediaPlaylist"], &mediaPlaylist)
				if mediaPlaylist != "holiday-bed" {
					t.Fatalf("PUT body mediaPlaylist = %q, want %q", mediaPlaylist, "holiday-bed")
				}
			} else {
				var items []map[string]json.RawMessage
				if err := json.Unmarshal(ba["items"], &items); err != nil || len(items) != 1 {
					t.Fatalf("inline-form PUT body lost its own item: %v; body=%s", err, gotPutBody)
				}
			}
		})
	}
}

// TestCmdNightGetSetPreservesAbsentBackgroundAudioTargets proves a bed
// with no targets declared still round-trips unchanged: absent stays
// absent, never turned into an empty array on the wire.
func TestCmdNightGetSetPreservesAbsentBackgroundAudioTargets(t *testing.T) {
	getServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, nightSessionSampleJSON)
	}))
	defer getServer.Close()

	var getOut, getErr bytes.Buffer
	code := cmdNight([]string{"get", "--server", getServer.URL, "--output", "json", "halloween-main"}, &getOut, &getErr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("night get exit code = %d, want exitOK; stderr=%s", code, getErr.String())
	}

	var gotPutBody []byte
	putServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPutBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, nightSessionSampleJSON)
	}))
	defer putServer.Close()

	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()
	piped := getOut.Bytes()
	go func() {
		_, _ = w.Write(piped)
		_ = w.Close()
	}()

	var setOut, setErr bytes.Buffer
	code = cmdNight([]string{"set", "--server", putServer.URL, "halloween-main"}, &setOut, &setErr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("night set exit code = %d, want exitOK; stderr=%s", code, setErr.String())
	}
	var sent map[string]json.RawMessage
	if err := json.Unmarshal(gotPutBody, &sent); err != nil {
		t.Fatalf("PUT body was not valid JSON: %v; body=%s", err, gotPutBody)
	}
	if _, hasResting := sent["resting"]; !hasResting {
		t.Fatalf("PUT body lost \"resting\" entirely: %s", gotPutBody)
	}
	var resting map[string]json.RawMessage
	_ = json.Unmarshal(sent["resting"], &resting)
	if _, hasBackgroundAudio := resting["backgroundAudio"]; hasBackgroundAudio {
		t.Fatalf("PUT body added a backgroundAudio the original session never had: %s", gotPutBody)
	}
}

func TestCmdNightSetSendsFullReplacementBody(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, nightSessionSampleJSON)
	}))
	defer ts.Close()

	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()
	payload := `{"show":"halloween-2026","label":"x"}`
	go func() {
		_, _ = w.Write([]byte(payload))
		_ = w.Close()
	}()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"set", "--server", ts.URL, "halloween-main"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/api/v1/config/night.session/halloween-main" {
		t.Errorf("path = %q, want /api/v1/config/night.session/halloween-main", gotPath)
	}
	var sent map[string]any
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("request body was not valid JSON: %v; body=%s", err, gotBody)
	}
	if sent["label"] != "x" {
		t.Errorf("request body did not carry the stdin payload verbatim: %s", gotBody)
	}
}

// TestCmdNightSetAcceptsAFullNightGetResponse is the night.session half of
// generalizing parseConfigSetPayload's fpp.endpoints-only round-trip fix
// (cmd_config.go, unwrapConfigGetResponse) to every config kind: feed
// "night set" the EXACT bytes "night get --output json" prints
// (nightSessionSampleJSON), and prove the PUT body carries the bare
// payload object directly at the top level, not still wrapped under
// "payload".
func TestCmdNightSetAcceptsAFullNightGetResponse(t *testing.T) {
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, nightSessionSampleJSON)
	}))
	defer ts.Close()

	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()
	go func() {
		// The EXACT shape `night get --output json` emits.
		_, _ = w.Write([]byte(nightSessionSampleJSON))
		_ = w.Close()
	}()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"set", "--server", ts.URL, "halloween-main"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}

	var sentTop map[string]json.RawMessage
	if err := json.Unmarshal(gotBody, &sentTop); err != nil {
		t.Fatalf("PUT body was not a JSON object: %v; body=%s", err, gotBody)
	}
	if _, stillWrapped := sentTop["payload"]; stillWrapped {
		t.Fatalf("PUT body = %s, still has a top-level \"payload\" key — the wrapper was sent unmodified, not unwrapped", gotBody)
	}
	var label string
	if err := json.Unmarshal(sentTop["label"], &label); err != nil {
		t.Fatalf("PUT body's \"label\" did not decode: %v; body=%s", err, gotBody)
	}
	if label != "Halloween main loop" {
		t.Fatalf("PUT body label = %q, want %q from the \"night get\" response — the round trip must survive", label, "Halloween main loop")
	}
}

func TestCmdNightActiveDeactivateSendsEmptySession(t *testing.T) {
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"night.session.active","id":"default","revision":2,
			"payload":{"session":""},"updatedAt":"2026-08-16T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"deactivate", "--server", ts.URL}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	var sent map[string]any
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("request body was not valid JSON: %v; body=%s", err, gotBody)
	}
	if sent["session"] != "" {
		t.Errorf("expected an explicit empty session on the wire, got %v (body: %s)", sent["session"], gotBody)
	}
	if !strings.Contains(stdout.String(), "none") {
		t.Errorf("expected the detail view to render the cleared pointer; got: %s", stdout.String())
	}
}

func TestCmdNightActivateSendsSessionID(t *testing.T) {
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"night.session.active","id":"default","revision":1,
			"payload":{"session":"halloween-main"},"updatedAt":"2026-08-16T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"activate", "--server", ts.URL, "halloween-main"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	var sent map[string]any
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("request body was not valid JSON: %v; body=%s", err, gotBody)
	}
	if sent["session"] != "halloween-main" {
		t.Errorf("expected session halloween-main on the wire, got %v (body: %s)", sent["session"], gotBody)
	}
}

func TestCmdNightRevisionFetchesSpecificRevision(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, nightSessionSampleJSON)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"revision", "--server", ts.URL, "halloween-main", "1"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/config/night.session/halloween-main/revisions/1" {
		t.Errorf("path = %q, want .../revisions/1", gotPath)
	}
}

func TestCmdNightRevisionRejectsNonPositiveArgument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"revision", "--server", "http://example.invalid", "halloween-main", "0"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
}
