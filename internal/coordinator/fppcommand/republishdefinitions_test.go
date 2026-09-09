package fppcommand

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// republishServer answers one republish POST with body, recording what it
// received so a test can assert the wire shape rather than trusting the
// client's own view of what it sent.
type republishServer struct {
	srv      *httptest.Server
	path     string
	method   string
	received map[string]any
}

func newRepublishServer(t *testing.T, status int, body string) *republishServer {
	t.Helper()
	s := &republishServer{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.path = r.URL.Path
		s.method = r.Method
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		_ = json.Unmarshal(raw, &s.received)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *republishServer) client(t *testing.T) *Client {
	t.Helper()
	c, err := New(s.srv.URL, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

const appliedRepublishBody = `{"schemaVersion":1,"applied":true,"definitionsCleared":6,` +
	`"definitionsHeld":0,"definitionsRefusedTerminally":1,"sweepPending":true}`

func TestRepublishDefinitionsPostsTheContractAddressAndBody(t *testing.T) {
	s := newRepublishServer(t, http.StatusOK, appliedRepublishBody)

	out, err := s.client(t).RepublishDefinitions(context.Background(), "req-1")
	if err != nil {
		t.Fatalf("RepublishDefinitions: %v", err)
	}

	// The address, not the registered path. The plugin registers
	// /showmesh/playlists/republish; only this spelling is proxied to it,
	// and posting the registered path reaches FPP's own PHP API instead.
	if s.path != "/api/plugin-apis/showmesh/playlists/republish" {
		t.Errorf("posted to %q, want the plugin-apis address", s.path)
	}
	if s.method != http.MethodPost {
		t.Errorf("method = %q, want POST", s.method)
	}
	if got, _ := s.received["schemaVersion"].(float64); got != 1 {
		t.Errorf("body schemaVersion = %v, want 1", s.received["schemaVersion"])
	}
	if got, _ := s.received["requestId"].(string); got != "req-1" {
		t.Errorf("body requestId = %v, want req-1", s.received["requestId"])
	}
	// Section 3.9 fixes exactly two fields. A third would be an unknown
	// field to the plugin's strict decode.
	if len(s.received) != 2 {
		t.Errorf("body carried %d fields (%v), want exactly schemaVersion and requestId", len(s.received), s.received)
	}

	if !out.Applied {
		t.Error("Applied = false, want true")
	}
	if out.DefinitionsCleared != 6 || out.DefinitionsHeld != 0 || out.DefinitionsRefusedTerminally != 1 {
		t.Errorf("counts = cleared %d held %d refused %d, want 6/0/1",
			out.DefinitionsCleared, out.DefinitionsHeld, out.DefinitionsRefusedTerminally)
	}
	// The strongest honest claim at response time: the sweep is owed, not
	// done.
	if !out.SweepPending {
		t.Error("SweepPending = false, want true on an applied answer")
	}
}

// TestRepublishDefinitionsTreatsAnIdempotentRepeatAsSuccess: a repeat of
// an applied requestId clears nothing and reports the state as it stands,
// which is the idempotency key working. It is also how a caller polls for
// the sweep finishing, so returning an error here would remove the only
// way to learn that.
func TestRepublishDefinitionsTreatsAnIdempotentRepeatAsSuccess(t *testing.T) {
	s := newRepublishServer(t, http.StatusOK,
		`{"schemaVersion":1,"applied":false,"definitionsCleared":0,`+
			`"definitionsHeld":4,"definitionsRefusedTerminally":1,"sweepPending":false}`)

	out, err := s.client(t).RepublishDefinitions(context.Background(), "req-1")
	if err != nil {
		t.Fatalf("an idempotent repeat returned an error: %v", err)
	}
	if out.Applied {
		t.Error("Applied = true, want false for a repeat")
	}
	if out.DefinitionsCleared != 0 {
		t.Errorf("DefinitionsCleared = %d, want 0: a repeat clears nothing", out.DefinitionsCleared)
	}
	// A repeat reports progress rather than an echo: four definitions have
	// been re-sent and accepted since, and the sweep has finished.
	if out.DefinitionsHeld != 4 || out.SweepPending {
		t.Errorf("repeat reported held %d sweepPending %t, want 4 and false", out.DefinitionsHeld, out.SweepPending)
	}
}

// TestRepublishDefinitionsRefusesAnUndecodable200 is this route's version
// of the transition gain's identical hazard. A zero-valued outcome here
// reads as "cleared nothing, holds nothing, refused nothing, no sweep
// owed", which is indistinguishable from a plugin that did nothing and is
// the most reassuring answer the shape can produce.
func TestRepublishDefinitionsRefusesAnUndecodable200(t *testing.T) {
	for _, body := range []string{"", "not json", "[1,2]"} {
		s := newRepublishServer(t, http.StatusOK, body)
		out, err := s.client(t).RepublishDefinitions(context.Background(), "req-1")
		if err == nil {
			t.Fatalf("body %q returned no error; outcome %+v", body, out)
		}
		if out.SweepPending {
			t.Errorf("body %q yielded SweepPending true from a body that carried nothing", body)
		}
	}
}

func TestRepublishDefinitionsRefusesAWrongSchemaVersion(t *testing.T) {
	s := newRepublishServer(t, http.StatusOK, `{"schemaVersion":2,"applied":true,"sweepPending":true}`)
	if _, err := s.client(t).RepublishDefinitions(context.Background(), "req-1"); err == nil {
		t.Fatal("a response declaring schemaVersion 2 was accepted")
	}
}

// TestRepublishDefinitionsRefusesAppliedWithNoSweepOwed: section 3.9 fixes
// sweepPending as always true on an applied answer, because the sweep runs
// on the worker thread and cannot have completed while the handler that
// made it due is still writing its response. Relaying the pair would tell
// an operator the resend is finished at the one moment it provably has not
// started.
func TestRepublishDefinitionsRefusesAppliedWithNoSweepOwed(t *testing.T) {
	s := newRepublishServer(t, http.StatusOK,
		`{"schemaVersion":1,"applied":true,"definitionsCleared":6,`+
			`"definitionsHeld":0,"definitionsRefusedTerminally":0,"sweepPending":false}`)

	out, err := s.client(t).RepublishDefinitions(context.Background(), "req-1")
	if err == nil {
		t.Fatalf("an applied answer with sweepPending false was accepted; outcome %+v", out)
	}
	if !strings.Contains(err.Error(), "sweepPending") {
		t.Errorf("error = %q, want it to name sweepPending", err)
	}
}

// TestRepublishDefinitionsRefusesAnEmptyRequestIDBeforeSpendingARequest:
// the key is what makes a retry safe and what makes polling possible, so
// an empty one is refused before it reaches the show LAN.
func TestRepublishDefinitionsRefusesAnEmptyRequestIDBeforeSpendingARequest(t *testing.T) {
	s := newRepublishServer(t, http.StatusOK, appliedRepublishBody)
	if _, err := s.client(t).RepublishDefinitions(context.Background(), ""); err == nil {
		t.Fatal("an empty requestId was accepted")
	}
	if s.method != "" {
		t.Fatalf("a refused request still reached the host as %s %s", s.method, s.path)
	}
}

func TestRepublishDefinitionsReportsAnHTTPRefusal(t *testing.T) {
	s := newRepublishServer(t, http.StatusBadRequest,
		`{"schemaVersion":1,"applied":false,"error":"definition publication is not configured"}`)

	out, err := s.client(t).RepublishDefinitions(context.Background(), "req-1")
	if err == nil {
		t.Fatal("a 400 returned no error")
	}
	if out.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want 400", out.StatusCode)
	}
	// The plugin's own refusal text is carried verbatim so an operator
	// surface can report it rather than paraphrasing.
	if !strings.Contains(out.Body, "definition publication is not configured") {
		t.Errorf("Body = %q, want the host's own refusal text", out.Body)
	}
}
