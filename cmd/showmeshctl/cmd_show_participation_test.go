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

// TestCmdShowSetPreservesAnUnnamedSelection is the ruled behaviour: a
// rename must not cost a show its instance selection. "show set" replaces
// name and notes outright but reads the show and carries an integration
// nobody named forward, because that selection decides what gets checked
// on a show night and is not the same class of field as a free-text note.
func TestCmdShowSetPreservesAnUnnamedSelection(t *testing.T) {
	var gotBody []byte
	ts := showParticipationServer(t,
		`{"name":"Quiet Night","notes":"barn only","fppInstances":["fpp-a","fpp-b"],"resolumeInstances":[]}`, &gotBody)
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"set", "--server", ts.URL, "--name", "Quiet Night (renamed)", "quiet-night"},
		&stdout, &stderr, fixedClock(mustParse(t, "2026-09-08T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("decoding request body: %v; body: %s", err, gotBody)
	}
	if string(decoded["fppInstances"]) != `["fpp-a","fpp-b"]` {
		t.Errorf("renaming a show must not clear its FPP selection; sent %s", gotBody)
	}
	// The explicitly empty selection is carried forward as [], not lost:
	// "the operator chose no Resolume" must survive a rename too.
	if string(decoded["resolumeInstances"]) != `[]` {
		t.Errorf("an explicitly empty selection must survive a rename as [], sent %s", gotBody)
	}
	// Notes keep the old rule: unnamed means empty, never carried forward.
	if string(decoded["notes"]) != `""` {
		t.Errorf("notes = %s, want an explicit empty string; only participation is carried forward", decoded["notes"])
	}
	if string(decoded["name"]) != `"Quiet Night (renamed)"` {
		t.Errorf("name = %s, want the new name", decoded["name"])
	}
}

// TestCmdShowSetNoneIsNotMistakenForLeaveAlone is the other half of the
// same rule, and the one carrying forward could quietly break: --fpp-none
// is an instruction to record an EMPTY selection, and must not be treated
// as the silence that carries the old one forward.
func TestCmdShowSetNoneIsNotMistakenForLeaveAlone(t *testing.T) {
	var gotBody []byte
	ts := showParticipationServer(t,
		`{"name":"Quiet Night","notes":"","fppInstances":["fpp-a","fpp-b"]}`, &gotBody)
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"set", "--server", ts.URL, "--name", "Quiet Night", "--fpp-none", "quiet-night"},
		&stdout, &stderr, fixedClock(mustParse(t, "2026-09-08T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("decoding request body: %v; body: %s", err, gotBody)
	}
	if string(decoded["fppInstances"]) != `[]` {
		t.Errorf("--fpp-none must record an empty selection, not carry the old one forward; sent %s", gotBody)
	}
}

// TestCmdShowSetUnsetClearsTheSelection: with silence now meaning "leave
// it alone", removing a selection through this verb needs a flag that
// says so, and it must reach the wire as an omitted key rather than [].
func TestCmdShowSetUnsetClearsTheSelection(t *testing.T) {
	var gotBody []byte
	ts := showParticipationServer(t,
		`{"name":"Quiet Night","notes":"","fppInstances":["fpp-a"],"resolumeInstances":["arena-01"]}`, &gotBody)
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"set", "--server", ts.URL, "--name", "Quiet Night", "--fpp-unset", "quiet-night"},
		&stdout, &stderr, fixedClock(mustParse(t, "2026-09-08T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("decoding request body: %v; body: %s", err, gotBody)
	}
	if _, present := decoded["fppInstances"]; present {
		t.Fatalf("--fpp-unset must omit the key entirely, sent %s", gotBody)
	}
	if string(decoded["resolumeInstances"]) != `["arena-01"]` {
		t.Errorf("the untouched integration must be carried forward; sent %s", gotBody)
	}
}

// TestCmdShowSetOnAShowWithNoSelectionStillSendsNothing: absent carried
// forward is still absent. Creating or editing a show nobody has
// configured must not invent an empty selection for it.
func TestCmdShowSetOnAShowWithNoSelectionStillSendsNothing(t *testing.T) {
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
		t.Fatalf("a show with no selection must stay unconfigured, sent %s", gotBody)
	}
	if _, present := decoded["resolumeInstances"]; present {
		t.Fatalf("a show with no selection must stay unconfigured, sent %s", gotBody)
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

// TestCmdShowSetCreatesAShowThatDoesNotExistYet: now that this verb always
// reads before writing, a 404 on that read is the first creation of the
// show, not a failure. The write must still go out, with no participation
// carried forward and no If-Match precondition to fail against an object
// that does not exist.
func TestCmdShowSetCreatesAShowThatDoesNotExistYet(t *testing.T) {
	var gotBody []byte
	var putSeen bool
	var ifMatch string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ShowMesh-API-Version", "1")
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/not-found","title":"Not Found","status":404}`)
			return
		}
		putSeen = true
		ifMatch = r.Header.Get("If-Match")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-09-08T21:00:00Z","kind":"show","id":"brand-new","revision":1,
			"payload":{"name":"Brand New","notes":""},
			"updatedAt":"2026-09-08T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdShow([]string{"set", "--server", ts.URL, "--name", "Brand New", "brand-new"},
		&stdout, &stderr, fixedClock(mustParse(t, "2026-09-08T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !putSeen {
		t.Fatal("a 404 on the pre-read must not stop the write; no PUT was issued")
	}
	if ifMatch != "" {
		t.Errorf("If-Match = %q, want none for a show that does not exist yet", ifMatch)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("decoding request body: %v; body: %s", err, gotBody)
	}
	if _, present := decoded["fppInstances"]; present {
		t.Fatalf("a new show must not be given a selection nobody chose, sent %s", gotBody)
	}
}
