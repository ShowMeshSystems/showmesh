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

// gainServer answers one transition-gain POST with body, recording what
// it received so a test can assert the wire shape rather than trusting
// the client's own view of what it sent.
type gainServer struct {
	srv      *httptest.Server
	path     string
	method   string
	received map[string]any
}

func newGainServer(t *testing.T, status int, body string) *gainServer {
	t.Helper()
	g := &gainServer{}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.path = r.URL.Path
		g.method = r.Method
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		_ = json.Unmarshal(raw, &g.received)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *gainServer) client(t *testing.T) *Client {
	t.Helper()
	c, err := New(g.srv.URL, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

const appliedBody = `{"schemaVersion":1,"applied":true,"gainStart":100,"gainTarget":75,` +
	`"fadeSeconds":30,"ceiling":60,"effectiveOutput":45}`

func TestSetTransitionGainPostsTheContractAddressAndBody(t *testing.T) {
	g := newGainServer(t, http.StatusOK, appliedBody)

	out, err := g.client(t).SetTransitionGain(context.Background(), 75, 30, "req-1")
	if err != nil {
		t.Fatalf("SetTransitionGain: %v", err)
	}

	// The address, not the registered path. The plugin registers
	// /showmesh/brightness/transition-gain; only this spelling is proxied
	// to it, and posting the registered path returns 404 from FPP's PHP
	// API rather than reaching the plugin at all.
	if g.path != "/api/plugin-apis/showmesh/brightness/transition-gain" {
		t.Errorf("posted to %q, want the plugin-apis address", g.path)
	}
	if g.method != http.MethodPost {
		t.Errorf("method = %q, want POST", g.method)
	}
	for field, want := range map[string]float64{
		"schemaVersion": 1, "targetPercent": 75, "fadeSeconds": 30,
	} {
		if got, _ := g.received[field].(float64); got != want {
			t.Errorf("body %s = %v, want %v", field, g.received[field], want)
		}
	}
	if got, _ := g.received["requestId"].(string); got != "req-1" {
		t.Errorf("body requestId = %v, want req-1", g.received["requestId"])
	}

	// The applied state, not a bare 200. Section 2.2 requires the
	// response to carry evidence, and discarding it would leave a caller
	// with nothing better than "the request did not error".
	if !out.Applied {
		t.Error("Applied = false, want true")
	}
	if out.GainStart != 100 || out.GainTarget != 75 || out.FadeSeconds != 30 {
		t.Errorf("fade = start %d target %d over %ds, want 100/75/30", out.GainStart, out.GainTarget, out.FadeSeconds)
	}
	if out.Ceiling != 60 || out.EffectiveOutput != 45 {
		t.Errorf("composition = ceiling %d output %d, want 60 and 45", out.Ceiling, out.EffectiveOutput)
	}
}

// TestSetTransitionGainTreatsAnIdempotentRepeatAsSuccess is the case a
// caller is most likely to get wrong. A repeated requestId returns 200
// with applied:false and the gain unchanged, which is the idempotency key
// working as designed, observed on a real FPP host. A client that
// returned an error here would make a retry of an unseen response look
// like a broken fade and invite a second write that restarts it.
func TestSetTransitionGainTreatsAnIdempotentRepeatAsSuccess(t *testing.T) {
	g := newGainServer(t, http.StatusOK,
		`{"schemaVersion":1,"applied":false,"gainStart":75,"gainTarget":75,`+
			`"fadeSeconds":30,"ceiling":60,"effectiveOutput":45}`)

	out, err := g.client(t).SetTransitionGain(context.Background(), 75, 30, "req-1")
	if err != nil {
		t.Fatalf("an idempotent repeat returned an error: %v", err)
	}
	if out.Applied {
		t.Error("Applied = true, want false for a repeat")
	}
	// The state still comes back, so a caller learns where the fade got
	// to rather than nothing.
	if out.GainStart != 75 || out.EffectiveOutput != 45 {
		t.Errorf("a repeat reported gainStart %d output %d, want the state as it stands", out.GainStart, out.EffectiveOutput)
	}
}

// TestSetTransitionGainRefusesAnUndecodable200 is the other case worth
// naming. A 200 whose body does not decode must not yield a zero-valued
// outcome, because zero reads as "gain 0, effective output 0", which is a
// dark display reported as a successful write.
func TestSetTransitionGainRefusesAnUndecodable200(t *testing.T) {
	for _, body := range []string{"", "not json", "[1,2]"} {
		g := newGainServer(t, http.StatusOK, body)
		out, err := g.client(t).SetTransitionGain(context.Background(), 75, 30, "req-1")
		if err == nil {
			t.Fatalf("body %q returned no error; outcome %+v", body, out)
		}
	}
}

func TestSetTransitionGainRefusesAWrongSchemaVersion(t *testing.T) {
	g := newGainServer(t, http.StatusOK, `{"schemaVersion":2,"applied":true}`)
	if _, err := g.client(t).SetTransitionGain(context.Background(), 75, 30, "req-1"); err == nil {
		t.Fatal("a response declaring schemaVersion 2 was accepted")
	}
}

// TestSetTransitionGainRefusesOutOfRangeBeforeSpendingARequest: section
// 2.2 rejects rather than clamps so a mistyped value stays visible, and
// refusing locally means a wrong value never reaches the show LAN.
func TestSetTransitionGainRefusesOutOfRangeBeforeSpendingARequest(t *testing.T) {
	g := newGainServer(t, http.StatusOK, appliedBody)
	c := g.client(t)

	for _, tc := range []struct {
		name         string
		target, fade int
		requestID    string
	}{
		{"target above 100", 101, 0, "r"},
		{"target below 0", -1, 0, "r"},
		{"fade negative", 50, -1, "r"},
		{"fade above 86400", 50, 86401, "r"},
		{"empty requestId defeats idempotency", 50, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.SetTransitionGain(context.Background(), tc.target, tc.fade, tc.requestID); err == nil {
				t.Fatal("accepted a value the contract refuses")
			}
			// Nothing was sent: the server never saw a body.
			if g.method != "" {
				t.Fatalf("a refused value still reached the host as %s %s", g.method, g.path)
			}
		})
	}
}

func TestSetTransitionGainReportsAnHTTPRefusal(t *testing.T) {
	g := newGainServer(t, http.StatusBadRequest,
		`{"schemaVersion":1,"applied":false,"error":"target percent must be between 0 and 100"}`)

	out, err := g.client(t).SetTransitionGain(context.Background(), 75, 30, "req-1")
	if err == nil {
		t.Fatal("a 400 returned no error")
	}
	if out.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want 400", out.StatusCode)
	}
	// The plugin's own refusal text is carried verbatim so an operator
	// surface can report it rather than paraphrasing.
	if !strings.Contains(out.Body, "must be between 0 and 100") {
		t.Errorf("Body = %q, want the host's own refusal text", out.Body)
	}
}
