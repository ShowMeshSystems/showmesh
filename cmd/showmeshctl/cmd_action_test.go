package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCmdActionListRendersObjects(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-14T21:00:00Z","kind":"show.action","objects":[
			{"id":"projectors-on","label":"Projectors on","show":"halloween-2026","currentRevision":1,"updatedAt":"2026-08-14T20:00:00Z"}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAction([]string{"list", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/config/show.action" {
		t.Errorf("path = %q, want /api/v1/config/show.action", gotPath)
	}
	if !strings.Contains(stdout.String(), "projectors-on") {
		t.Errorf("stdout = %q, want it to name the action id", stdout.String())
	}
}

func TestCmdActionShowRendersFPPTarget(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-14T21:00:00Z","kind":"show.action","id":"stop-main","revision":1,
			"payload":{"show":"halloween-2026","label":"Stop main show","description":"","safetyClass":"stop",
				"target":{"integration":"fpp","instanceId":"fpp-main","primitive":"stopPlaylist"}},
			"updatedAt":"2026-08-14T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAction([]string{"show", "--server", ts.URL, "stop-main"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/config/show.action/stop-main" {
		t.Errorf("path = %q, want /api/v1/config/show.action/stop-main", gotPath)
	}
	out := stdout.String()
	for _, want := range []string{"stop", "fpp-main", "stopPlaylist"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
}

func TestCmdActionShowRendersMQTTTarget(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-14T21:00:00Z","kind":"show.action","id":"projectors-on","revision":1,
			"payload":{"show":"halloween-2026","label":"Projectors on","description":"","safetyClass":"none",
				"target":{"integration":"mqtt","broker":"home-automation",
					"publish":{"topic":"home/projectors/set","payload":"ON","qos":1,"retain":false},
					"expect":{"kind":"boolean","topic":"home/projectors/state","deadlineSeconds":30}}},
			"updatedAt":"2026-08-14T20:00:00Z","createdByPrincipalId":null,"createdByPrincipalName":null,"source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAction([]string{"show", "--server", ts.URL, "projectors-on"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"home-automation", "home/projectors/set", "home/projectors/state", "boolean"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "no principal recorded") {
		t.Errorf("stdout = %q, want a null creator rendered explicitly, not blank", out)
	}
}

func TestCmdActionShowRequiresExactlyOneArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdAction([]string{"show"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage", code)
	}
}
