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
