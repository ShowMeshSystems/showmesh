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

func TestCmdWeatherDelayStatusPrintsHeldPlayers(t *testing.T) {
	const msg = "Player player-01 is being held dark and no weather delay is active. Press Resume to release it."
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"serverTime":"2026-10-31T20:30:00Z","active":false,"revision":8,"assets":[],"powerGroups":[],"heldPlayers":[{"instanceId":"player-01","message":%q}]}`, msg)
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := cmdWeatherDelay([]string{"status", "--server", srv.URL}, &stdout, &stderr, func() time.Time { return time.Date(2026, 10, 31, 20, 30, 0, 0, time.UTC) })
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "weather delay: not active") || !strings.Contains(stdout.String(), msg) {
		t.Fatalf("stdout = %q, want the not-active line and the held player sentence", stdout.String())
	}
}

func TestCmdWeatherDelayStatusPrintsStartedByNameAndNotifyError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-10-31T20:30:00Z","active":true,"kind":"cancelNight","startedAt":"2026-10-31T20:00:00Z","startedBy":"principal-1","startedByName":"Eric","revision":3,"assets":[],"powerGroups":[],"heldPlayers":[],"lastNotifyError":"webhook answered with status 500"}`)
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := cmdWeatherDelay([]string{"status", "--server", srv.URL}, &stdout, &stderr, func() time.Time { return time.Date(2026, 10, 31, 20, 30, 0, 0, time.UTC) })
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "startedBy=Eric") {
		t.Fatalf("stdout = %q, want startedBy=Eric (the name, not the raw principal id)", out)
	}
	if !strings.Contains(out, "webhook answered with status 500") {
		t.Fatalf("stdout = %q, want the last webhook delivery failure", out)
	}
}

func TestCmdWeatherDelayCancelNightAndClear(t *testing.T) {
	var gotPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-10-31T20:30:00Z","result":{"kind":"cancelNight","idempotencyKey":"k","active":true,"startedAt":"2026-10-31T20:00:00Z","startedBy":"p1","startedByName":"Eric","revision":2,"targets":[]}}`)
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := cmdWeatherDelay([]string{"cancel-night", "--server", srv.URL}, &stdout, &stderr, func() time.Time { return time.Date(2026, 10, 31, 20, 30, 0, 0, time.UTC) })
	if code != exitOK {
		t.Fatalf("cancel-night exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "startedBy=Eric") {
		t.Fatalf("cancel-night stdout = %q, want startedBy=Eric", stdout.String())
	}

	stdout.Reset()
	code = cmdWeatherDelay([]string{"clear", "--server", srv.URL}, &stdout, &stderr, func() time.Time { return time.Date(2026, 10, 31, 20, 30, 0, 0, time.UTC) })
	if code != exitOK {
		t.Fatalf("clear exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}

	if len(gotPaths) != 2 || gotPaths[0] != "/api/v1/weather-delay/cancel-night" || gotPaths[1] != "/api/v1/weather-delay/resume" {
		t.Fatalf("requested paths = %v, want cancel-night then resume (clear is resume at parity)", gotPaths)
	}
}

func TestCmdWeatherDelayPresign(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/weather-delay/presigned-start" {
			t.Fatalf("path = %q, want /api/v1/weather-delay/presigned-start", r.URL.Path)
		}
		_ = decodeJSONBody(t, r, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-10-31T20:30:00Z","request":{"request":{"kind":"delay","issuedAt":"2026-10-31T20:30:00Z","nonce":"n1","notAfter":"2027-01-01T00:00:00Z"},"signature":"c2ln"},"nodeUrls":["http://10.0.0.5:9090/showmesh/v1/weather-delay/start"]}`)
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := cmdWeatherDelay([]string{"presign", "--kind", "delay", "--valid-days", "60", "--server", srv.URL}, &stdout, &stderr, func() time.Time { return time.Date(2026, 10, 31, 20, 30, 0, 0, time.UTC) })
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotBody["kind"] != "delay" || gotBody["validDays"] != float64(60) {
		t.Fatalf("request body = %+v, want kind=delay validDays=60", gotBody)
	}
	out := stdout.String()
	if !strings.Contains(out, "notAfter=2027-01-01T00:00:00Z") {
		t.Fatalf("stdout = %q, want the notAfter", out)
	}
	if !strings.Contains(out, "http://10.0.0.5:9090/showmesh/v1/weather-delay/start") {
		t.Fatalf("stdout = %q, want the node url", out)
	}
}
