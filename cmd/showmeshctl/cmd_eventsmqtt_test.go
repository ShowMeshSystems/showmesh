package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func eventsMQTTServer(t *testing.T) (*httptest.Server, *[]releaseSettingsRequest) {
	return eventsMQTTServerAt(t, "0")
}

// eventsMQTTServerAt answers GET with the given stored revision.
func eventsMQTTServerAt(t *testing.T, storedRevision string) (*httptest.Server, *[]releaseSettingsRequest) {
	t.Helper()
	var seen []releaseSettingsRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/config/events.mqtt" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, releaseSettingsRequest{r.Method, r.Header.Get("If-Match"), string(body)})
		w.Header().Set("ShowMesh-API-Version", "1")
		w.Header().Set("Content-Type", "application/json")
		payload, revision := `{"enabled":false,"broker":"","topicPrefix":"showmesh"}`, storedRevision
		if r.Method == http.MethodPut {
			payload, revision = `{"enabled":true,"broker":"ha","topicPrefix":"barn"}`, "1"
		}
		_, _ = w.Write([]byte(`{"serverTime":"2026-10-12T18:05:00Z","kind":"events.mqtt","revision":` + revision + `,
			"payload":` + payload + `,"updatedAt":"2026-10-12T18:05:00Z","createdByPrincipalId":null,"createdByPrincipalName":null,"source":"api"}`))
	}))
	t.Cleanup(ts.Close)
	return ts, &seen
}

func runEventsMQTT(t *testing.T, ts *httptest.Server, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	full := append([]string{"events-mqtt"}, args...)
	full = append(full, "--server", ts.URL)
	code := run(full, &stdout, &stderr, fixedClock(mustParse(t, "2026-10-12T18:05:00Z")))
	return code, stdout.String(), stderr.String()
}

func TestCmdEventsMQTTGetPrintsTheSetting(t *testing.T) {
	ts, _ := eventsMQTTServer(t)
	code, out, errOut := runEventsMQTT(t, ts, "get")
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, errOut)
	}
	for _, want := range []string{"publishing:", "off", "broker:", "-", "topicPrefix:", "showmesh"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q is missing %q", out, want)
		}
	}
}

func TestCmdEventsMQTTSetSendsOnlyTheFlagsPassed(t *testing.T) {
	ts, seen := eventsMQTTServerAt(t, "4")
	code, out, errOut := runEventsMQTT(t, ts, "set", "--enabled", "--broker", "ha", "--topic-prefix", "barn")
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, errOut)
	}
	put := (*seen)[len(*seen)-1]
	if put.method != http.MethodPut || put.ifMatch == "" || !strings.Contains(put.body, `"broker":"ha"`) || !strings.Contains(put.body, `"topicPrefix":"barn"`) || !strings.Contains(put.body, `"enabled":true`) {
		t.Fatalf("PUT = %+v, want enabled, broker ha and topicPrefix barn with an If-Match", put)
	}
	if !strings.Contains(out, "barn") {
		t.Fatalf("output %q does not show the saved prefix", out)
	}

	*seen = nil
	if code, _, _ := runEventsMQTT(t, ts, "set", "--broker", "ha"); code != exitOK {
		t.Fatalf("exit code = %d, want exitOK", code)
	}
	if body := (*seen)[len(*seen)-1].body; strings.Contains(body, "enabled") || strings.Contains(body, "topicPrefix") {
		t.Fatalf("PUT body %q carries a flag that was not passed", body)
	}
}

func TestCmdEventsMQTTSetWithNoFlagsIsAUsageError(t *testing.T) {
	ts, _ := eventsMQTTServer(t)
	if code, _, _ := runEventsMQTT(t, ts, "set"); code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage", code)
	}
}

func TestCmdEventsMQTTFirstSaveSendsNoIfMatch(t *testing.T) {
	ts, seen := eventsMQTTServerAt(t, "0")
	if code, _, errOut := runEventsMQTT(t, ts, "set", "--broker", "ha"); code != exitOK {
		t.Fatalf("exit code = %d; stderr=%s", code, errOut)
	}
	if put := (*seen)[len(*seen)-1]; put.method != http.MethodPut || put.ifMatch != "" {
		t.Fatalf("PUT = %+v, want no If-Match while nothing is saved", put)
	}
}

func TestCmdEventsMQTTLaterSaveSendsTheReadRevision(t *testing.T) {
	ts, seen := eventsMQTTServerAt(t, "4")
	if code, _, errOut := runEventsMQTT(t, ts, "set", "--broker", "ha"); code != exitOK {
		t.Fatalf("exit code = %d; stderr=%s", code, errOut)
	}
	if put := (*seen)[len(*seen)-1]; put.ifMatch != `"4"` {
		t.Fatalf("PUT If-Match = %q, want \"4\"", put.ifMatch)
	}
}
