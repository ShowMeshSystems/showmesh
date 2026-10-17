package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const brightnessDocBody = `{"serverTime":"2026-10-17T20:00:00Z",
	"ceiling":{"targetPercent":60,"fadeEndsAt":"2026-10-17T20:00:30Z","changedAt":"2026-10-17T20:00:00Z","changedBy":{"kind":"operator","id":"p1","name":"operator-1"}},
	"players":[
		{"instanceId":"bench-fpp","state":"following","ceiling":60,"ceilingTarget":60,"reportedAt":"2026-10-17T20:00:00Z"},
		{"instanceId":"remote-01","state":"catchingUp","reason":"The player remote-01 is at 100% and is being set to 60%.","ceiling":100,"ceilingTarget":100},
		{"instanceId":"old-fpp","state":"pluginTooOld","reason":"The player old-fpp follows the display brightness, but a change made on it reaches no other player. Update its ShowMesh plugin.","ceiling":60}
	]}`

func brightnessServer(t *testing.T, body string) (*httptest.Server, *string, *[]byte) {
	t.Helper()
	var path string
	var raw []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		raw, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts, &path, &raw
}

func TestCmdFPPBrightnessPrintsTheCeilingAndEachFPP(t *testing.T) {
	ts, path, _ := brightnessServer(t, brightnessDocBody)
	var stdout, stderr bytes.Buffer
	if code := cmdFPP([]string{"brightness", "--server", ts.URL}, &stdout, &stderr, time.Now); code != exitOK {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr.String())
	}
	if *path != "GET /api/v1/fpp/brightness" {
		t.Errorf("request = %q", *path)
	}
	out := stdout.String()
	for _, want := range []string{
		"display ceiling: 60%, fading until 2026-10-17T20:00:30Z (set 2026-10-17T20:00:00Z by operator operator-1)",
		"bench-fpp", "following", "remote-01", "catchingUp", "is being set to 60%", "pluginTooOld", "Update its ShowMesh plugin.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to contain %q", out, want)
		}
	}
}

func TestCmdFPPBrightnessBeforeAnyCeiling(t *testing.T) {
	ts, _, _ := brightnessServer(t, `{"serverTime":"2026-10-17T20:00:00Z","ceiling":null,"players":[]}`)
	var stdout, stderr bytes.Buffer
	if code := cmdFPP([]string{"brightness", "--server", ts.URL}, &stdout, &stderr, time.Now); code != exitOK {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "display ceiling: not set") || !strings.Contains(stdout.String(), "no FPP players are configured") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestCmdFPPSetBrightnessCeilingPutsTheDisplayCeiling(t *testing.T) {
	ts, path, raw := brightnessServer(t, brightnessDocBody)
	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"set-brightness-ceiling", "--server", ts.URL, "--fade-seconds", "30", "60"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr.String())
	}
	if *path != "PUT /api/v1/fpp/brightness/ceiling" {
		t.Errorf("request = %q, want PUT /api/v1/fpp/brightness/ceiling", *path)
	}
	if got := string(*raw); !strings.Contains(got, `"targetPercent":60`) || !strings.Contains(got, `"fadeSeconds":30`) {
		t.Errorf("body = %s", got)
	}
	if !strings.Contains(stdout.String(), "display ceiling: 60%") {
		t.Errorf("stdout = %q, want the resulting document", stdout.String())
	}
}

func TestCmdFPPSetBrightnessCeilingRefusesBadArgumentsLocally(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
	}{
		{"above 100", []string{"101"}},
		{"below 0", []string{"-1"}},
		{"not a number", []string{"bright"}},
		{"fade above the bound", []string{"--fade-seconds", "86401", "50"}},
		{"an instance argument", []string{"bench-fpp", "50"}},
		{"nothing", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			defer ts.Close()
			var stdout, stderr bytes.Buffer
			code := cmdFPP(append([]string{"set-brightness-ceiling", "--server", ts.URL}, tt.args...), &stdout, &stderr, time.Now)
			if code != exitUsage || called {
				t.Fatalf("exit code = %d, called = %v; want a local exitUsage refusal", code, called)
			}
		})
	}
}
