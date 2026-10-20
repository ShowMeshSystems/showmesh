package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testResolumeInstancesOSCResponse = `{"serverTime":"2026-10-20T06:00:00Z","kind":"resolume.instances","revision":2,
"payload":{"instances":[{"id":"arena-1","url":"http://10.0.1.30:8080","oscPort":7001,"control":"osc"}]},
"updatedAt":"2026-10-20T06:00:00Z","createdByPrincipalId":null,"createdByPrincipalName":null,"source":"api",
"restartRequired":false,"restartRequiredReason":"This change is already in effect. No restart is needed."}`

func TestCmdResolumeInstanceSetSendsControlAndOSCPort(t *testing.T) {
	var putBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		if r.Method == http.MethodPut {
			b, _ := io.ReadAll(r.Body)
			putBody = string(b)
		}
		_, _ = fmt.Fprint(w, testResolumeInstancesOSCResponse)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolumeInstance([]string{"set", "--id", "arena-1", "--url", "http://10.0.1.30:8080", "--osc-port", "7001", "--control", "osc",
		"--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(putBody, `"oscPort":7001`) || !strings.Contains(putBody, `"control":"osc"`) {
		t.Errorf("PUT body = %s, want oscPort 7001 and control osc", putBody)
	}
	for _, want := range []string{"CONTROL", "7001", "osc"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestCmdResolumeInstanceSetOmitsControlWhenNotGiven(t *testing.T) {
	var putBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		if r.Method == http.MethodPut {
			b, _ := io.ReadAll(r.Body)
			putBody = string(b)
		}
		_, _ = fmt.Fprint(w, testResolumeInstancesOSCResponse)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolumeInstance([]string{"set", "--id", "arena-1", "--url", "http://10.0.1.30:8080", "--server", ts.URL, "--token", "t"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if strings.Contains(putBody, "control") || strings.Contains(putBody, "oscPort") {
		t.Errorf("PUT body = %s, want no control or oscPort when neither flag is given", putBody)
	}
}
