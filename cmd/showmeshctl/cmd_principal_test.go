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

// principalTestServer serves every principal/token endpoint these tests
// drive, recording each request's method and path.
func principalTestServer(t *testing.T, requests *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*requests = append(*requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		switch {
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/tokens") && r.Method == http.MethodPost:
			_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-17T00:00:00Z",
				"token":{"id":"t1","principalId":"p1","hint":"smt_ab...cd","label":"","createdAt":"2026-08-17T00:00:00Z","expiresAt":null,"lastUsedAt":null},
				"value":"smt_secret"}`)
		case strings.HasSuffix(r.URL.Path, "/tokens"):
			_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-17T00:00:00Z","tokens":[]}`)
		default:
			_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-17T00:00:00Z",
				"principal":{"id":"p1","name":"n","kind":"human","role":"admin","disabled":false,"hasPassword":true,"reserved":false,"createdAt":"2026-08-17T00:00:00Z"}}`)
		}
	}))
}

// TestPrincipalDocumentedInvocationsParse drives every documented
// flags-first invocation form through the real arg parser and out to a
// stub coordinator: the usage strings promise "[flags] <positionals>",
// and the standard flag package stops parsing at the first non-flag
// argument, so an invocation documented in the other order would exit
// usage here instead of reaching the server.
func TestPrincipalDocumentedInvocationsParse(t *testing.T) {
	cases := []struct {
		name     string
		args     func(server string) []string
		wantHit  string
		wantExit int
	}{
		{
			name:     "disable [flags] <id>",
			args:     func(s string) []string { return []string{"disable", "--server", s, "p1"} },
			wantHit:  "POST /api/v1/principals/p1/disable",
			wantExit: exitOK,
		},
		{
			name:     "enable [flags] <id>",
			args:     func(s string) []string { return []string{"enable", "--server", s, "--output", "json", "p1"} },
			wantHit:  "POST /api/v1/principals/p1/enable",
			wantExit: exitOK,
		},
		{
			name:     "reset-password [flags] <id>",
			args:     func(s string) []string { return []string{"reset-password", "--server", s, "--password", "pw", "p1"} },
			wantHit:  "POST /api/v1/principals/p1/password",
			wantExit: exitOK,
		},
		{
			name:     "set-role [flags] <id> <role>",
			args:     func(s string) []string { return []string{"set-role", "--server", s, "p1", "operator"} },
			wantHit:  "PUT /api/v1/principals/p1/role",
			wantExit: exitOK,
		},
		{
			name: "create -name -kind -role [--password]",
			args: func(s string) []string {
				return []string{"create", "--server", s, "-name", "n", "-kind", "machine", "-role", "viewer"}
			},
			wantHit:  "POST /api/v1/principals",
			wantExit: exitOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var requests []string
			ts := principalTestServer(t, &requests)
			defer ts.Close()

			var stdout, stderr bytes.Buffer
			code := cmdPrincipal(tc.args(ts.URL), &stdout, &stderr, time.Now)
			if code != tc.wantExit {
				t.Fatalf("exit code = %d, want %d; stderr=%s", code, tc.wantExit, stderr.String())
			}
			if len(requests) != 1 || requests[0] != tc.wantHit {
				t.Errorf("requests = %v, want exactly [%s]", requests, tc.wantHit)
			}
		})
	}
}

// TestResolvePasswordFrom covers resolvePassword's decision logic with
// the terminal injected, including the interactive create path (required
// false, TTY): the prompt must show and an empty answer must mean "no
// password", never a silent skip of the prompt.
func TestResolvePasswordFrom(t *testing.T) {
	secretNotCalled := func() (string, error) {
		t.Error("readSecret called when a flag already supplied the password")
		return "", nil
	}

	t.Run("--password wins, no prompt", func(t *testing.T) {
		var stderr bytes.Buffer
		got, err := resolvePasswordFrom(strings.NewReader(""), &stderr, "flagpw", false, "Password: ", false, true, secretNotCalled)
		if err != nil || got != "flagpw" {
			t.Fatalf("got %q, %v; want \"flagpw\", nil", got, err)
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr = %q, want no prompt", stderr.String())
		}
	})

	t.Run("--password-stdin reads one line", func(t *testing.T) {
		var stderr bytes.Buffer
		got, err := resolvePasswordFrom(strings.NewReader("stdinpw\r\n"), &stderr, "", true, "Password: ", true, false, secretNotCalled)
		if err != nil || got != "stdinpw" {
			t.Fatalf("got %q, %v; want \"stdinpw\", nil", got, err)
		}
	})

	t.Run("TTY not required prompts", func(t *testing.T) {
		var stderr bytes.Buffer
		got, err := resolvePasswordFrom(strings.NewReader(""), &stderr, "", false, "Password: ", false, true,
			func() (string, error) { return "typed", nil })
		if err != nil || got != "typed" {
			t.Fatalf("got %q, %v; want \"typed\", nil", got, err)
		}
		if !strings.Contains(stderr.String(), "Password: ") {
			t.Errorf("stderr = %q, want the prompt label", stderr.String())
		}
	})

	t.Run("TTY not required empty answer means no password", func(t *testing.T) {
		var stderr bytes.Buffer
		got, err := resolvePasswordFrom(strings.NewReader(""), &stderr, "", false, "Password: ", false, true,
			func() (string, error) { return "", nil })
		if err != nil || got != "" {
			t.Fatalf("got %q, %v; want \"\", nil", got, err)
		}
		if !strings.Contains(stderr.String(), "Password: ") {
			t.Errorf("stderr = %q, want the prompt to have shown", stderr.String())
		}
	})

	t.Run("TTY required prompts", func(t *testing.T) {
		var stderr bytes.Buffer
		got, err := resolvePasswordFrom(strings.NewReader(""), &stderr, "", false, "New password: ", true, true,
			func() (string, error) { return "typed", nil })
		if err != nil || got != "typed" {
			t.Fatalf("got %q, %v; want \"typed\", nil", got, err)
		}
	})

	t.Run("non-TTY not required means no password", func(t *testing.T) {
		var stderr bytes.Buffer
		got, err := resolvePasswordFrom(strings.NewReader(""), &stderr, "", false, "Password: ", false, false, secretNotCalled)
		if err != nil || got != "" {
			t.Fatalf("got %q, %v; want \"\", nil", got, err)
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr = %q, want no prompt on a non-TTY", stderr.String())
		}
	})

	t.Run("non-TTY required is a usage error", func(t *testing.T) {
		var stderr bytes.Buffer
		_, err := resolvePasswordFrom(strings.NewReader(""), &stderr, "", false, "Password: ", true, false, secretNotCalled)
		if err == nil || !strings.Contains(err.Error(), "a password is required") {
			t.Fatalf("err = %v, want the usage error, never a hang", err)
		}
	})
}
