package fppcommand

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type gateServer struct {
	srv      *httptest.Server
	path     string
	method   string
	received map[string]any
}

func newGateServer(t *testing.T, status int, body string) *gateServer {
	t.Helper()
	g := &gateServer{}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.path = r.URL.Path
		g.method = r.Method
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &g.received)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *gateServer) client(t *testing.T) *Client {
	t.Helper()
	c, err := New(g.srv.URL, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

const closedGateBody = `{"weatherGateClosed":true,"weatherGateRevision":3,"effectiveOutputPercent":0,"ceiling":80,"transitionGain":100}`
const openGateBody = `{"weatherGateClosed":false,"weatherGateRevision":3,"effectiveOutputPercent":60,"ceiling":80,"transitionGain":75}`

func TestSetWeatherGatePostsTheContractAddressAndBody(t *testing.T) {
	g := newGateServer(t, http.StatusOK, closedGateBody)

	out, err := g.client(t).SetWeatherGate(context.Background(), true, 3)
	if err != nil {
		t.Fatalf("SetWeatherGate: %v", err)
	}
	if g.method != http.MethodPost {
		t.Errorf("method = %q, want POST", g.method)
	}
	if g.path != WeatherGatePath {
		t.Errorf("path = %q, want %q", g.path, WeatherGatePath)
	}
	if closed, _ := g.received["closed"].(bool); !closed {
		t.Errorf("received closed = %v, want true", g.received["closed"])
	}
	if rev, _ := g.received["revision"].(float64); rev != 3 {
		t.Errorf("received revision = %v, want 3", g.received["revision"])
	}
	if !out.Closed || out.Revision != 3 || out.EffectiveOutputPercent != 0 {
		t.Errorf("out = %+v, want closed=true revision=3 effectiveOutput=0", out)
	}
}

func TestReadWeatherGateGetsTheContractAddress(t *testing.T) {
	g := newGateServer(t, http.StatusOK, openGateBody)

	out, err := g.client(t).ReadWeatherGate(context.Background())
	if err != nil {
		t.Fatalf("ReadWeatherGate: %v", err)
	}
	if g.method != http.MethodGet {
		t.Errorf("method = %q, want GET", g.method)
	}
	if g.path != WeatherGatePath {
		t.Errorf("path = %q, want %q", g.path, WeatherGatePath)
	}
	if out.Closed || out.Revision != 3 || out.EffectiveOutputPercent != 60 {
		t.Errorf("out = %+v, want closed=false revision=3 effectiveOutput=60", out)
	}
}

func TestWeatherGate404IsReportedAsUnsupportedNeverAsClosedOrOpen(t *testing.T) {
	g := newGateServer(t, http.StatusNotFound, "not found")

	_, err := g.client(t).ReadWeatherGate(context.Background())
	if !errors.Is(err, ErrWeatherGateUnsupported) {
		t.Fatalf("ReadWeatherGate error = %v, want ErrWeatherGateUnsupported", err)
	}

	_, err = g.client(t).SetWeatherGate(context.Background(), true, 1)
	if !errors.Is(err, ErrWeatherGateUnsupported) {
		t.Fatalf("SetWeatherGate error = %v, want ErrWeatherGateUnsupported", err)
	}
}

func TestWeatherGateNon2xxIsAnError(t *testing.T) {
	g := newGateServer(t, http.StatusInternalServerError, "boom")

	_, err := g.client(t).ReadWeatherGate(context.Background())
	if err == nil {
		t.Fatal("ReadWeatherGate: want an error for a 500")
	}
	if errors.Is(err, ErrWeatherGateUnsupported) {
		t.Fatalf("a 500 must not be reported as ErrWeatherGateUnsupported: %v", err)
	}
}
