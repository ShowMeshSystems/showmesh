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
)

// Tests for "showmeshctl show participation". Each drives a real
// httptest.Server, so what is under test is the request this program
// actually issues, not a mock of the client.

func showParticipationServer(t *testing.T, payload string, gotBody *[]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && gotBody != nil {
			*gotBody, _ = io.ReadAll(r.Body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprintf(w, `{"serverTime":"2026-09-08T21:00:00Z","kind":"show","id":"quiet-night","revision":3,
			"payload":%s,
			"updatedAt":"2026-09-08T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`, payload)
	}))
}

// TestCmdShowParticipationGetNamesAllThreeStates is the CLI half of the
// distinction the whole feature rests on: a show nobody has configured
// must SAY so, and must not read the same as a show whose operator chose
// that nothing takes part.
func TestCmdShowParticipationGetNamesAllThreeStates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		want    string
		notWant string
	}{
		{
			name:    "absent",
			payload: `{"name":"Quiet Night","notes":""}`,
			want:    "no selection recorded",
			notWant: "none take part",
		},
		{
			name:    "explicitly empty",
			payload: `{"name":"Quiet Night","notes":"","fppInstances":["fpp-a"],"resolumeInstances":[]}`,
			want:    "none take part",
			notWant: "no selection recorded",
		},
		{
			name:    "populated",
			payload: `{"name":"Quiet Night","notes":"","fppInstances":["fpp-a"],"resolumeInstances":["arena-01"]}`,
			want:    "arena-01",
			notWant: "no selection recorded",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := showParticipationServer(t, tc.payload, nil)
			defer ts.Close()

			var stdout, stderr bytes.Buffer
			code := cmdShow([]string{"participation", "get", "--server", ts.URL, "quiet-night"},
				&stdout, &stderr, fixedClock(mustParse(t, "2026-09-08T21:00:00Z")))
			if code != exitOK {
				t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
			}
			out := stdout.String()
			if !strings.Contains(out, tc.want) {
				t.Errorf("stdout missing %q:\n%s", tc.want, out)
			}
			if strings.Contains(out, tc.notWant) {
				t.Errorf("stdout must not say %q for a %s selection:\n%s", tc.notWant, tc.name, out)
			}
		})
	}
}

// TestCmdShowParticipationSetSendsExplicitEmptyArray proves --resolume-none
// reaches the coordinator as [] and not as an omitted key: omitting it
// would record "never configured", the opposite of what the operator
// asked for.
func TestCmdShowParticipationSetSendsExplicitEmptyArray(t *testing.T) {
	var gotBody []byte
	ts := showParticipationServer(t, `{"name":"Quiet Night","notes":"barn only","fppInstances":["fpp-a"]}`, &gotBody)
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"participation", "set", "--server", ts.URL, "--resolume-none", "quiet-night"},
		&stdout, &stderr, fixedClock(mustParse(t, "2026-09-08T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("decoding request body: %v; body: %s", err, gotBody)
	}
	raw, ok := decoded["resolumeInstances"]
	if !ok {
		t.Fatalf("--resolume-none must send an explicit empty array, not omit the key: %s", gotBody)
	}
	if string(raw) != `[]` {
		t.Errorf("resolumeInstances = %s, want []", raw)
	}
	// The unnamed integration and the rest of the show are carried
	// forward: this verb is not a full replacement.
	if string(decoded["fppInstances"]) != `["fpp-a"]` {
		t.Errorf("fppInstances = %s, want the existing selection carried forward", decoded["fppInstances"])
	}
	if string(decoded["notes"]) != `"barn only"` {
		t.Errorf("notes = %s, want the existing notes carried forward", decoded["notes"])
	}
}

// TestCmdShowParticipationSetUnsetOmitsTheKey is the third state's own
// write path: --fpp-unset returns the show to "never configured", which on
// the wire is an absent key, never [].
func TestCmdShowParticipationSetUnsetOmitsTheKey(t *testing.T) {
	var gotBody []byte
	ts := showParticipationServer(t, `{"name":"Quiet Night","notes":"","fppInstances":["fpp-a"],"resolumeInstances":[]}`, &gotBody)
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"participation", "set", "--server", ts.URL, "--fpp-unset", "quiet-night"},
		&stdout, &stderr, fixedClock(mustParse(t, "2026-09-08T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("decoding request body: %v; body: %s", err, gotBody)
	}
	if _, present := decoded["fppInstances"]; present {
		t.Fatalf("--fpp-unset must omit the key entirely, got %s", gotBody)
	}
	if string(decoded["resolumeInstances"]) != `[]` {
		t.Errorf("resolumeInstances = %s, want the explicitly empty selection carried forward", decoded["resolumeInstances"])
	}
}

func TestCmdShowParticipationSetContradictoryFlagsRefused(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"participation", "set", "--fpp", "fpp-a", "--fpp-none", "quiet-night"},
		&stdout, &stderr, fixedClock(mustParse(t, "2026-09-08T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
}

func TestCmdShowParticipationSetRequiresAtLeastOneFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"participation", "set", "quiet-night"},
		&stdout, &stderr, fixedClock(mustParse(t, "2026-09-08T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
}

// TestCmdShowSetOmitsParticipationWhenUnnamed keeps "show set" honest: it
// is a full replacement, so a write that names no integration records the
// show as having no selection at all rather than quietly preserving one.
func TestCmdShowSetOmitsParticipationWhenUnnamed(t *testing.T) {
	var gotBody []byte
	ts := showParticipationServer(t, `{"name":"Quiet Night","notes":""}`, &gotBody)
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"set", "--server", ts.URL, "--name", "Quiet Night", "quiet-night"},
		&stdout, &stderr, fixedClock(mustParse(t, "2026-09-08T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("decoding request body: %v; body: %s", err, gotBody)
	}
	if _, present := decoded["fppInstances"]; present {
		t.Fatalf("show set with no participation flag must omit the key: %s", gotBody)
	}
}

// TestCmdShowSetSendsNamedParticipation proves the full-replacement verb
// can still state a selection, so an operator is not forced through the
// participation verb to create a configured show in one call.
func TestCmdShowSetSendsNamedParticipation(t *testing.T) {
	var gotBody []byte
	ts := showParticipationServer(t, `{"name":"Quiet Night","notes":"","fppInstances":["fpp-a","fpp-b"],"resolumeInstances":[]}`, &gotBody)
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"set", "--server", ts.URL, "--name", "Quiet Night",
		"--fpp", "fpp-a, fpp-b", "--resolume-none", "quiet-night"},
		&stdout, &stderr, fixedClock(mustParse(t, "2026-09-08T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("decoding request body: %v; body: %s", err, gotBody)
	}
	if string(decoded["fppInstances"]) != `["fpp-a","fpp-b"]` {
		t.Errorf("fppInstances = %s, want [\"fpp-a\",\"fpp-b\"]", decoded["fppInstances"])
	}
	if string(decoded["resolumeInstances"]) != `[]` {
		t.Errorf("resolumeInstances = %s, want []", decoded["resolumeInstances"])
	}
}
