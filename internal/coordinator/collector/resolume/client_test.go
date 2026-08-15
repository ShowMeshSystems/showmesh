package resolume

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewClientRejectsBaseURLWithPath(t *testing.T) {
	if _, err := NewClient("http://127.0.0.1:9080/composition", ClientOptions{}); err == nil {
		t.Fatalf("NewClient() error = nil, want an error for a base URL carrying a path")
	}
}

func TestNewClientRejectsBaseURLWithQuery(t *testing.T) {
	if _, err := NewClient("http://127.0.0.1:9080?a=1", ClientOptions{}); err == nil {
		t.Fatalf("NewClient() error = nil, want an error for a base URL carrying a query")
	}
}

func TestNewClientRejectsBaseURLWithFragment(t *testing.T) {
	if _, err := NewClient("http://127.0.0.1:9080#frag", ClientOptions{}); err == nil {
		t.Fatalf("NewClient() error = nil, want an error for a base URL carrying a fragment")
	}
}

func TestNewClientRejectsBaseURLWithUserinfo(t *testing.T) {
	if _, err := NewClient("http://user:pass@127.0.0.1:9080", ClientOptions{}); err == nil {
		t.Fatalf("NewClient() error = nil, want an error for a base URL carrying userinfo")
	}
}

func TestNewClientRejectsNonHTTPScheme(t *testing.T) {
	if _, err := NewClient("ws://127.0.0.1:9080", ClientOptions{}); err == nil {
		t.Fatalf("NewClient() error = nil, want an error for a non-http(s) scheme")
	}
}

func TestNewClientRejectsMissingHost(t *testing.T) {
	if _, err := NewClient("http:///composition", ClientOptions{}); err == nil {
		t.Fatalf("NewClient() error = nil, want an error for a URL with no host")
	}
}

func TestNewClientAcceptsBareTrailingSlash(t *testing.T) {
	if _, err := NewClient("http://127.0.0.1:9080/", ClientOptions{}); err != nil {
		t.Fatalf("NewClient() error = %v, want a bare trailing slash to be accepted", err)
	}
}

// --- Product ------------------------------------------------------------

func TestClientProductDecodesCapturedShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/product" {
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %q, want GET", r.Method)
		}
		w.Write(loadTestdata(t, "product.json"))
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	p, err := c.Product(context.Background())
	if err != nil {
		t.Fatalf("Product() error = %v", err)
	}
	if p.Name != "Arena" || p.Major != 7 || p.Minor != 23 || p.Micro != 2 || p.Revision != 51094 {
		t.Fatalf("Product() = %+v, want the captured shape", p)
	}
	want := "Arena 7.23.2 (r51094)"
	if got := p.String(); got != want {
		t.Errorf("Product.String() = %q, want %q", got, want)
	}
}

func TestClientProductNon2xxIsStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("The requested resource '/api/v1/product' was not found on this server."))
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = c.Product(context.Background())
	if err == nil {
		t.Fatalf("Product() error = nil, want a StatusError for a 404 response")
	}
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("Product() error = %v (%T), want a *StatusError", err, err)
	}
	if se.StatusCode != http.StatusNotFound {
		t.Errorf("StatusError.StatusCode = %d, want 404", se.StatusCode)
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound(err) = false, want true for a 404 StatusError")
	}
	if !strings.Contains(se.Body, "not found") {
		t.Errorf("StatusError.Body = %q, want it to carry Resolume's plain-text error body", se.Body)
	}
}

func TestClientProductDecodeErrorIsDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = c.Product(context.Background())
	if err == nil {
		t.Fatalf("Product() error = nil, want a decode error for a non-JSON body")
	}
	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("Product() error = %v (%T), want a *DecodeError", err, err)
	}
	if de.Path != "/product" {
		t.Errorf("DecodeError.Path = %q, want /product", de.Path)
	}
	if de.Unwrap() == nil {
		t.Errorf("DecodeError.Unwrap() = nil, want the underlying json error")
	}
}

// --- ClassifyError ----------------------------------------------------------

func TestClassifyErrorNames(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"status error", &StatusError{StatusCode: 404, Path: "/product", Body: "not found"}, "http status 404"},
		{"decode error", &DecodeError{Path: "/product", Err: errors.New("unexpected EOF")}, "decode error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyError(tt.err); got != tt.want {
				t.Errorf("ClassifyError(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

// TestClassifyErrorFallthroughIsBounded is the direct reproduction of
// review finding G (2026-08-14): ClassifyError's doc comment promises
// "never a full error dump," but the fallthrough for an unclassified
// error shape returned err.Error() whole, unbounded. A verbose wrapped
// error (this test builds one comfortably longer than
// maxClassifiedErrorLen) must come back truncated.
//
// Before trusting this test, the truncation ("if len(s) >
// maxClassifiedErrorLen { s = s[:maxClassifiedErrorLen] + ... }") was
// temporarily removed, returning err.Error() whole again, and this test
// was re-run: it failed, with ClassifyError's output exceeding the bound.
// Restored afterward.
func TestClassifyErrorFallthroughIsBounded(t *testing.T) {
	long := strings.Repeat("x", maxClassifiedErrorLen*3)
	err := fmt.Errorf("resolume: some unclassified wrapped failure: %s", long)

	got := ClassifyError(err)
	if len(got) > maxClassifiedErrorLen+len("…(truncated)") {
		t.Fatalf("ClassifyError(long error) returned %d bytes, want at most %d (maxClassifiedErrorLen plus the truncation marker)",
			len(got), maxClassifiedErrorLen+len("…(truncated)"))
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("ClassifyError(long error) = %q, want it to signal truncation", got)
	}
}

// TestClassifyErrorFallthroughStillClassifiesShortUnknownErrors is the
// paired non-regression check: an ordinary short, unclassified error must
// still come back unchanged (not padded, not marked "truncated") — the
// bound in TestClassifyErrorFallthroughIsBounded must fire only when it is
// actually needed.
func TestClassifyErrorFallthroughStillClassifiesShortUnknownErrors(t *testing.T) {
	err := errors.New("some short unclassified failure")
	if got := ClassifyError(err); got != err.Error() {
		t.Errorf("ClassifyError(short error) = %q, want %q unchanged", got, err.Error())
	}
}

// --- Track D seam D-2: by-id reads ---------------------------------------

// TestClientLayerDecodesPresentFields exercises every one of [Layer]'s
// leaves against layer_present.json, where every optional leaf carries a
// real value.
func TestClientLayerDecodesPresentFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/composition/layers/by-id/1765224917300" {
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
		w.Write(loadTestdata(t, "layer_present.json"))
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	l, err := c.Layer(context.Background(), ObjectID(1765224917300))
	if err != nil {
		t.Fatalf("Layer() error = %v", err)
	}
	if l.ID != 1765224917300 {
		t.Errorf("Layer.ID = %v, want 1765224917300", l.ID)
	}
	if l.Bypassed.Presence != PresencePresent || l.Bypassed.Param.Value != false {
		t.Errorf("Layer.Bypassed = %+v, want present/false", l.Bypassed)
	}
	if l.Master.Presence != PresencePresent || l.Master.Param.Value != 0.917 {
		t.Errorf("Layer.Master = %+v, want present/0.917", l.Master)
	}
	if op := l.VideoOpacity(); op.Presence != PresencePresent || op.Param.Value != 1.0 {
		t.Errorf("Layer.VideoOpacity() = %+v, want present/1.0", op)
	}
	if l.ActiveClip.Presence != PresencePresent || l.ActiveClip.Clip.ID != 1765396769079 {
		t.Errorf("Layer.ActiveClip = %+v, want present with clip id 1765396769079", l.ActiveClip)
	}
	if l.Name.Presence != PresencePresent || l.Name.Param.Value != "Layer 1" {
		t.Errorf("Layer.Name = %+v, want present/\"Layer 1\"", l.Name)
	}
}

// TestClientLayerNullFieldsAreNeverReadAsZeroValues is the direct
// reproduction, at the by-id-decode level, of TRACK-D-D2-SPEC.md §2's
// named worst case: "bypassed": null must decode to PresenceNull, never
// to Go's zero value false, which would read as "not bypassed" and make a
// dark layer report ready.
func TestClientLayerNullFieldsAreNeverReadAsZeroValues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(loadTestdata(t, "layer_null_terms.json"))
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	l, err := c.Layer(context.Background(), ObjectID(1765224917301))
	if err != nil {
		t.Fatalf("Layer() error = %v", err)
	}
	if l.Bypassed.Presence != PresenceNull {
		t.Fatalf("Layer.Bypassed.Presence = %v, want PresenceNull (a null bypassed must NEVER read as false)", l.Bypassed.Presence)
	}
	if l.Bypassed.Param != nil {
		t.Errorf("Layer.Bypassed.Param = %+v, want nil for PresenceNull", l.Bypassed.Param)
	}
	if op := l.VideoOpacity(); op.Presence != PresenceNull {
		t.Errorf("Layer.VideoOpacity().Presence = %v, want PresenceNull", op.Presence)
	}
	if l.ActiveClip.Presence != PresenceNull {
		t.Errorf("Layer.ActiveClip.Presence = %v, want PresenceNull", l.ActiveClip.Presence)
	}
	if l.Name.Presence != PresenceNull {
		t.Errorf("Layer.Name.Presence = %v, want PresenceNull", l.Name.Presence)
	}
	// Master was NOT null in this fixture (0.0, a real value) — the
	// non-regression half: a genuinely present zero value must still read
	// as PresencePresent, not be conflated with null or absent.
	if l.Master.Presence != PresencePresent || l.Master.Param.Value != 0.0 {
		t.Errorf("Layer.Master = %+v, want present/0.0 (a real zero value, distinct from null)", l.Master)
	}
}

// TestClientLayerAbsentFieldsArePresenceAbsent proves PresenceAbsent and
// PresenceNull are different outcomes, not merely that both differ from
// present: layer_absent_terms.json carries only "id".
func TestClientLayerAbsentFieldsArePresenceAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(loadTestdata(t, "layer_absent_terms.json"))
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	l, err := c.Layer(context.Background(), ObjectID(1765224917302))
	if err != nil {
		t.Fatalf("Layer() error = %v", err)
	}
	if l.Bypassed.Presence != PresenceAbsent {
		t.Errorf("Layer.Bypassed.Presence = %v, want PresenceAbsent", l.Bypassed.Presence)
	}
	if l.Master.Presence != PresenceAbsent {
		t.Errorf("Layer.Master.Presence = %v, want PresenceAbsent", l.Master.Presence)
	}
	if op := l.VideoOpacity(); op.Presence != PresenceAbsent {
		t.Errorf("Layer.VideoOpacity().Presence = %v, want PresenceAbsent (whole 'video' object absent)", op.Presence)
	}
	if l.ActiveClip.Presence != PresenceAbsent {
		t.Errorf("Layer.ActiveClip.Presence = %v, want PresenceAbsent", l.ActiveClip.Presence)
	}
	if l.Name.Presence != PresenceAbsent {
		t.Errorf("Layer.Name.Presence = %v, want PresenceAbsent", l.Name.Presence)
	}
}

func TestClientLayerNotFoundIsDistinguishableFromTransportFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("The requested resource '/api/v1/composition/layers/by-id/999' was not found on this server."))
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = c.Layer(context.Background(), ObjectID(999))
	if err == nil {
		t.Fatalf("Layer() error = nil, want a StatusError for a 404")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound(err) = false, want true for a 404 on a by-id layer read")
	}

	// The other side of "distinguishable": a connection failure against a
	// closed listener must NOT also report IsNotFound true.
	closedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := closedSrv.URL
	closedSrv.Close()
	c2, err := NewClient(closedURL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = c2.Layer(context.Background(), ObjectID(999))
	if err == nil {
		t.Fatalf("Layer() error = nil, want a connection failure against a closed server")
	}
	if IsNotFound(err) {
		t.Errorf("IsNotFound(err) = true for a connection-refused failure, want false — a transport failure must never be mistaken for a 404")
	}
}

func TestClientLayerGroupDecodesPresentAndNullFields(t *testing.T) {
	tests := []struct {
		name         string
		fixture      string
		wantPresence Presence
	}{
		{"present", "layergroup_present.json", PresencePresent},
		{"null", "layergroup_null_terms.json", PresenceNull},
		{"absent", "layergroup_absent_terms.json", PresenceAbsent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write(loadTestdata(t, tt.fixture))
			}))
			defer srv.Close()

			c, err := NewClient(srv.URL, ClientOptions{})
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			lg, err := c.LayerGroup(context.Background(), ObjectID(1))
			if err != nil {
				t.Fatalf("LayerGroup() error = %v", err)
			}
			if lg.Bypassed.Presence != tt.wantPresence {
				t.Errorf("LayerGroup.Bypassed.Presence = %v, want %v", lg.Bypassed.Presence, tt.wantPresence)
			}
			if lg.Master.Presence != tt.wantPresence {
				t.Errorf("LayerGroup.Master.Presence = %v, want %v", lg.Master.Presence, tt.wantPresence)
			}
		})
	}
}

func TestClientLayerGroupPathAndNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/composition/layergroups/by-id/1733100600800" {
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("not found"))
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = c.LayerGroup(context.Background(), ObjectID(1733100600800))
	if !IsNotFound(err) {
		t.Errorf("IsNotFound(err) = false, want true")
	}
}

func TestClientDeckDecodesPresentAndNullFields(t *testing.T) {
	tests := []struct {
		name         string
		fixture      string
		wantPresence Presence
	}{
		{"present", "deck_present.json", PresencePresent},
		{"null", "deck_null_terms.json", PresenceNull},
		{"absent", "deck_absent_terms.json", PresenceAbsent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write(loadTestdata(t, tt.fixture))
			}))
			defer srv.Close()

			c, err := NewClient(srv.URL, ClientOptions{})
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			d, err := c.Deck(context.Background(), ObjectID(1))
			if err != nil {
				t.Fatalf("Deck() error = %v", err)
			}
			if d.Selected.Presence != tt.wantPresence {
				t.Errorf("Deck.Selected.Presence = %v, want %v", d.Selected.Presence, tt.wantPresence)
			}
			if d.Name.Presence != tt.wantPresence {
				t.Errorf("Deck.Name.Presence = %v, want %v", d.Name.Presence, tt.wantPresence)
			}
		})
	}
}

func TestClientDeckPathAndNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/composition/decks/by-id/1733100600915" {
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("not found"))
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = c.Deck(context.Background(), ObjectID(1733100600915))
	if !IsNotFound(err) {
		t.Errorf("IsNotFound(err) = false, want true")
	}
}

// TestClientClipDecodesPresentFields exercises [Clip]'s leaves, including
// TestConnectedNeverReducesToBoolAndPreservesEveryState's exact concern
// wired through the real by-id path: "Connected & previewing" must survive
// decode unchanged.
func TestClientClipDecodesPresentFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/composition/clips/by-id/1765396769100" {
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
		w.Write(loadTestdata(t, "clip_present.json"))
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	clip, err := c.Clip(context.Background(), ObjectID(1765396769100))
	if err != nil {
		t.Fatalf("Clip() error = %v", err)
	}
	if clip.Connected.Presence != PresencePresent || clip.Connected.Param.Value != "Connected & previewing" {
		t.Errorf("Clip.Connected = %+v, want present/\"Connected & previewing\"", clip.Connected)
	}
	if clip.TransportType.Presence != PresencePresent || clip.TransportType.Param.Value != "Timeline" {
		t.Errorf("Clip.TransportType = %+v, want present/\"Timeline\"", clip.TransportType)
	}
	if clip.Name.Presence != PresencePresent || clip.Name.Param.Value != "Test Clip" {
		t.Errorf("Clip.Name = %+v, want present/\"Test Clip\"", clip.Name)
	}
	if clip.Transport.Position.Value != 0.42 {
		t.Errorf("Clip.Transport.Position.Value = %v, want 0.42", clip.Transport.Position.Value)
	}
	if clip.Transport.Controls.Presence != PresenceNull {
		t.Errorf("Clip.Transport.Controls.Presence = %v, want PresenceNull (SMPTE-style null, capture 11.3)", clip.Transport.Controls.Presence)
	}
}

func TestClientClipNullAndAbsentFields(t *testing.T) {
	tests := []struct {
		name         string
		fixture      string
		wantPresence Presence
	}{
		{"null", "clip_null_terms.json", PresenceNull},
		{"absent", "clip_absent_terms.json", PresenceAbsent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write(loadTestdata(t, tt.fixture))
			}))
			defer srv.Close()

			c, err := NewClient(srv.URL, ClientOptions{})
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			clip, err := c.Clip(context.Background(), ObjectID(1))
			if err != nil {
				t.Fatalf("Clip() error = %v", err)
			}
			if clip.Connected.Presence != tt.wantPresence {
				t.Errorf("Clip.Connected.Presence = %v, want %v (never reduced to a bool, and never confused with a real Empty/Disconnected value)", clip.Connected.Presence, tt.wantPresence)
			}
			if clip.TransportType.Presence != tt.wantPresence {
				t.Errorf("Clip.TransportType.Presence = %v, want %v", clip.TransportType.Presence, tt.wantPresence)
			}
			if clip.Name.Presence != tt.wantPresence {
				t.Errorf("Clip.Name.Presence = %v, want %v", clip.Name.Presence, tt.wantPresence)
			}
		})
	}
}

func TestClientClipNotFoundIsNotByItselfAStaleReference(t *testing.T) {
	// This test's name states TRACK-D-D2-SPEC.md §6's exact caution: a
	// 404 on a stored clip id, unlike a layer/layergroup/deck 404, is not
	// by itself evidence the clip is gone (capture section 16.1) — it can
	// equally mean the clip's own deck is not selected. This package's job
	// is only to make the 404 distinguishable at all (which IsNotFound
	// does identically for every by-id type); interpreting it with a deck
	// term is a later seam's job (ADR-032 decision 6), not tested here.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/composition/clips/by-id/42" {
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("The requested resource '/api/v1/composition/clips/by-id/42' was not found on this server."))
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = c.Clip(context.Background(), ObjectID(42))
	if !IsNotFound(err) {
		t.Errorf("IsNotFound(err) = false, want true — the 404 itself must still be classifiable, whatever a later seam decides it means")
	}
}

// The composition-level parameter ladder was deleted (defect 2, 2026-08-15):
// see client.go's own doc comment for why there is no GET /composition/{parameter}
// path to test against.

func TestClassifyErrorConnectionRefused(t *testing.T) {
	// A closed listener on loopback reliably produces ECONNREFUSED rather
	// than a timeout, which is what this test needs to name specifically.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // closed immediately: nothing is listening on this port now

	c, cErr := NewClient(url, ClientOptions{})
	if cErr != nil {
		t.Fatalf("NewClient() error = %v", cErr)
	}
	_, pErr := c.Product(context.Background())
	if pErr == nil {
		t.Fatalf("Product() error = nil, want a connection-refused error against a closed server")
	}
	if got := ClassifyError(pErr); got != "connection refused" {
		t.Errorf("ClassifyError(%v) = %q, want %q", pErr, got, "connection refused")
	}
}
