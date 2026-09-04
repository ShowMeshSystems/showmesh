package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testFPPMQTTConfigResponse = `{
	"serverTime":"2026-08-17T00:00:00Z","kind":"fpp.mqtt","revision":1,
	"payload":{"brokerURL":"tcp://10.0.1.5:1883","username":"showmesh","topicPrefix":"falcon/player","hosts":{"player-01":"FPP-Player"},"passwordSet":true},
	"updatedAt":"2026-08-17T00:00:00Z","createdByPrincipalId":"p-1","createdByPrincipalName":"admin-1",
	"source":"api","restartRequired":false,
	"restartRequiredReason":"this change is already in effect: the FPP MQTT collector follows this configuration within about ten seconds. No restart is needed."
}`

func TestCmdFPPMQTTGetRendersActiveConfiguration(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/config/fpp.mqtt" {
			t.Errorf("request = %s %s, want GET /api/v1/config/fpp.mqtt", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, testFPPMQTTConfigResponse)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPPMQTT([]string{"get", "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "tcp://10.0.1.5:1883") {
		t.Errorf("stdout = %q, want the broker URL rendered", out)
	}
	if !strings.Contains(out, "player-01") || !strings.Contains(out, "FPP-Player") {
		t.Errorf("stdout = %q, want the host map rendered", out)
	}
	if !strings.Contains(out, "password:    set") {
		t.Errorf("stdout = %q, want \"password:    set\"", out)
	}
	if strings.Contains(out, "s3cret") {
		t.Errorf("stdout must never contain a raw password")
	}
}

func TestCmdFPPMQTTGetNotConfigured(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := `{"serverTime":"2026-08-17T00:00:00Z","kind":"fpp.mqtt","revision":1,` +
			`"payload":{"brokerURL":"","username":"","topicPrefix":"","hosts":{},"passwordSet":false},` +
			`"updatedAt":"2026-08-17T00:00:00Z","createdByPrincipalId":null,"createdByPrincipalName":null,` +
			`"source":"api","restartRequired":false,"restartRequiredReason":"no restart needed"}`
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, body)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPPMQTT([]string{"get", "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no FPP MQTT broker configured") {
		t.Errorf("stdout = %q, want the unconfigured message", stdout.String())
	}
}

// TestCmdFPPMQTTSetOnlySendsVisitedFields is ADR-039 decision 5's own
// client-side proof: a flag never passed on the command line must not
// appear in the request body at all — an absent key, not a zero value.
func TestCmdFPPMQTTSetOnlySendsVisitedFields(t *testing.T) {
	var gotBody map[string]json.RawMessage
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("ShowMesh-API-Version", "1")
			_, _ = fmt.Fprint(w, testFPPMQTTConfigResponse)
			return
		}
		if r.Method != http.MethodPut || r.URL.Path != "/api/v1/config/fpp.mqtt" {
			t.Errorf("request = %s %s, want PUT /api/v1/config/fpp.mqtt", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("If-Match"); got != `"1"` {
			t.Errorf("If-Match = %q, want %q (the revision the fresh read supplied)", got, `"1"`)
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, testFPPMQTTConfigResponse)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPPMQTT([]string{"set", "--topic-prefix", "custom/prefix", "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if len(gotBody) != 1 {
		t.Fatalf("request body keys = %v, want exactly {topicPrefix}", gotBody)
	}
	if _, ok := gotBody["topicPrefix"]; !ok {
		t.Errorf("request body = %v, want a topicPrefix key", gotBody)
	}
	if _, ok := gotBody["password"]; ok {
		t.Errorf("request body has a password key when --password was never passed")
	}
	if _, ok := gotBody["brokerURL"]; ok {
		t.Errorf("request body has a brokerURL key when --broker-url was never passed")
	}
}

// TestCmdFPPMQTTSetPasswordSendsExplicitKey proves --password puts the
// value on the wire, and --clear-password sends an explicit null —
// distinct request shapes for "leave alone" vs. "clear".
func TestCmdFPPMQTTSetPasswordSendsExplicitKey(t *testing.T) {
	var gotBody map[string]json.RawMessage
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("ShowMesh-API-Version", "1")
			_, _ = fmt.Fprint(w, testFPPMQTTConfigResponse)
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, testFPPMQTTConfigResponse)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPPMQTT([]string{"set", "--password", "new-secret", "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if string(gotBody["password"]) != `"new-secret"` {
		t.Errorf("password field = %s, want the quoted new value", gotBody["password"])
	}

	stdout.Reset()
	stderr.Reset()
	code = cmdFPPMQTT([]string{"set", "--clear-password", "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if string(gotBody["password"]) != "null" {
		t.Errorf("password field after --clear-password = %s, want null", gotBody["password"])
	}
}

func TestCmdFPPMQTTSetHostFlagRepeatable(t *testing.T) {
	var gotBody map[string]json.RawMessage
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("ShowMesh-API-Version", "1")
			_, _ = fmt.Fprint(w, testFPPMQTTConfigResponse)
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, testFPPMQTTConfigResponse)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPPMQTT([]string{
		"set", "--host", "player-01=FPP-Player", "--host", "shed=FPP-Shed",
		"--server", ts.URL, "--token", "t",
	}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	var hosts map[string]string
	if err := json.Unmarshal(gotBody["hosts"], &hosts); err != nil {
		t.Fatalf("unmarshal hosts: %v", err)
	}
	if hosts["player-01"] != "FPP-Player" || hosts["shed"] != "FPP-Shed" {
		t.Errorf("hosts = %v, want both repeated --host entries", hosts)
	}
}

func TestCmdFPPMQTTSetNoFlagsIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdFPPMQTT([]string{"set", "--server", "http://unused", "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage when no fields are named", code)
	}
}

func TestCmdFPPMQTTUnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdFPPMQTT([]string{"bogus"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage", code)
	}
}

func TestCmdFPPMQTTHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdFPPMQTT([]string{"help"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK", code)
	}
	if !strings.Contains(stdout.String(), "fpp-mqtt") {
		t.Errorf("help output = %q, want it to name fpp-mqtt", stdout.String())
	}
}

func TestCmdFPPMQTTStillSetEnvVarRefusedWith409(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/conflict","title":"Configuration write refused","status":409,"detail":"SHOWMESH_FPP_MQTT_BROKER_URL is still set","serverTime":"2026-08-17T00:00:00Z"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdFPPMQTT([]string{"set", "--topic-prefix", "x", "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitConflict {
		t.Fatalf("exit code = %d, want exitConflict", code)
	}
	if !strings.Contains(stderr.String(), "SHOWMESH_FPP_MQTT_BROKER_URL") {
		t.Errorf("stderr = %q, want it to name the still-set variable", stderr.String())
	}
}
