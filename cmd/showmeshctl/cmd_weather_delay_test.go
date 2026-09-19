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
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-10-31T20:30:00Z","active":true,"kind":"cancelNight","startedAt":"2026-10-31T20:00:00Z","startedBy":"principal-1","startedByName":"Night Operator","revision":3,"assets":[],"powerGroups":[],"heldPlayers":[],"lastNotifyError":"webhook answered with status 500"}`)
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := cmdWeatherDelay([]string{"status", "--server", srv.URL}, &stdout, &stderr, func() time.Time { return time.Date(2026, 10, 31, 20, 30, 0, 0, time.UTC) })
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "startedBy=Night Operator") {
		t.Fatalf("stdout = %q, want startedBy=Night Operator (the name, not the raw principal id)", out)
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
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-10-31T20:30:00Z","result":{"kind":"cancelNight","idempotencyKey":"k","active":true,"startedAt":"2026-10-31T20:00:00Z","startedBy":"p1","startedByName":"Night Operator","revision":2,"targets":[]}}`)
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := cmdWeatherDelay([]string{"cancel-night", "--server", srv.URL}, &stdout, &stderr, func() time.Time { return time.Date(2026, 10, 31, 20, 30, 0, 0, time.UTC) })
	if code != exitOK {
		t.Fatalf("cancel-night exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "startedBy=Night Operator") {
		t.Fatalf("cancel-night stdout = %q, want startedBy=Night Operator", stdout.String())
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
	if !strings.Contains(out, `{"request":{"kind":"delay","issuedAt":"2026-10-31T20:30:00Z","nonce":"n1","notAfter":"2027-01-01T00:00:00Z"},"signature":"c2ln"}`) {
		t.Fatalf("stdout = %q, want the signed document to hold", out)
	}
}

func TestCmdWeatherDelayTrigger(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = decodeJSONBody(t, r, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-09-19T20:00:00Z","accepted":true,"message":"","pendingDecision":{"id":"d1","source":"nws","reason":"A tornado warning is in effect.","question":"delay","defaultAction":"delay","askedAt":"2026-09-19T20:00:00Z","deadline":"2026-09-19T20:00:30Z"}}`)
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := cmdWeatherDelay([]string{
		"trigger", "--server", srv.URL, "-source", "nws", "-kind", "warning",
		"-event-type", "Tornado Warning", "-severity", "Extreme",
	}, &stdout, &stderr, func() time.Time { return time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC) })
	if code != exitOK {
		t.Fatalf("trigger exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/weather-delay/triggers/nws" {
		t.Fatalf("path = %q, want /api/v1/weather-delay/triggers/nws", gotPath)
	}
	if gotBody["kind"] != "warning" || gotBody["eventType"] != "Tornado Warning" || gotBody["severity"] != "Extreme" {
		t.Fatalf("request body = %+v, want kind/eventType/severity set", gotBody)
	}
	if _, ok := gotBody["suggestCancel"]; ok {
		t.Fatalf("request body = %+v, want no suggestCancel key when the flag was not set", gotBody)
	}
	if !strings.Contains(stdout.String(), "A tornado warning is in effect.") {
		t.Fatalf("stdout = %q, want the pending decision's reason", stdout.String())
	}
}

func TestCmdWeatherDelayTriggerRequiresSourceAndValidKind(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdWeatherDelay([]string{"trigger", "-kind", "warning"}, &stdout, &stderr, func() time.Time { return time.Time{} })
	if code != exitUsage {
		t.Fatalf("trigger with no -source: exit code = %d, want exitUsage", code)
	}

	stderr.Reset()
	code = cmdWeatherDelay([]string{"trigger", "-source", "nws", "-kind", "hail"}, &stdout, &stderr, func() time.Time { return time.Time{} })
	if code != exitUsage {
		t.Fatalf("trigger with a bad -kind: exit code = %d, want exitUsage", code)
	}
}

func TestCmdWeatherDelayDecision(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = decodeJSONBody(t, r, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-09-19T20:00:10Z","answer":"cancelNight","result":{"kind":"cancelNight","idempotencyKey":"","active":true,"startedAt":"2026-09-19T20:00:10Z","startedBy":"admin-1","startedByName":"Admin","revision":1,"targets":[]}}`)
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := cmdWeatherDelay([]string{"decision", "--server", srv.URL, "-id", "d1", "-answer", "cancel-night"},
		&stdout, &stderr, func() time.Time { return time.Date(2026, 9, 19, 20, 0, 10, 0, time.UTC) })
	if code != exitOK {
		t.Fatalf("decision exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotPath != "/api/v1/weather-delay/decision" {
		t.Fatalf("path = %q, want /api/v1/weather-delay/decision", gotPath)
	}
	if gotBody["id"] != "d1" || gotBody["answer"] != "cancelNight" {
		t.Fatalf("request body = %+v, want id=d1 answer=cancelNight (wire spelling)", gotBody)
	}
	if !strings.Contains(stdout.String(), "startedBy=Admin") {
		t.Fatalf("stdout = %q, want startedBy=Admin", stdout.String())
	}
}

func TestCmdWeatherDelayDecisionDismiss(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-09-19T20:00:10Z","answer":"dismiss"}`)
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := cmdWeatherDelay([]string{"decision", "--server", srv.URL, "-id", "d1", "-answer", "dismiss"},
		&stdout, &stderr, func() time.Time { return time.Date(2026, 9, 19, 20, 0, 10, 0, time.UTC) })
	if code != exitOK {
		t.Fatalf("decision exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "dismissed") {
		t.Fatalf("stdout = %q, want it to report the dismiss", stdout.String())
	}
}

func TestCmdWeatherDelayDecisionRequiresIDAndValidAnswer(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdWeatherDelay([]string{"decision", "-answer", "delay"}, &stdout, &stderr, func() time.Time { return time.Time{} })
	if code != exitUsage {
		t.Fatalf("decision with no -id: exit code = %d, want exitUsage", code)
	}

	stderr.Reset()
	code = cmdWeatherDelay([]string{"decision", "-id", "d1", "-answer", "bogus"}, &stdout, &stderr, func() time.Time { return time.Time{} })
	if code != exitUsage {
		t.Fatalf("decision with a bad -answer: exit code = %d, want exitUsage", code)
	}
}
