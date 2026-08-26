package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/fppconnect"
	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// fakeFPPConnectView is the test fake fppConnectView's own doc comment
// promises: every handler test in this file is built against this
// interface, never against fppConnectState, so these tests do not depend on
// FC1a's eventual extension of that holder.
type fakeFPPConnectView struct {
	channelRanges string
	enabled       bool
	activeShow    string
	hasActiveShow bool
	showNames     []string
}

func (f fakeFPPConnectView) ChannelRanges() string      { return f.channelRanges }
func (f fakeFPPConnectView) Enabled() bool              { return f.enabled }
func (f fakeFPPConnectView) ActiveShow() (string, bool) { return f.activeShow, f.hasActiveShow }
func (f fakeFPPConnectView) ShowNames() []string        { return f.showNames }

// startFPPConnectTestServer serves the real handler newFPPConnectHandler
// builds over a real loopback listener (httptest.Server, an OS-assigned
// port, never real port 80), with the same ConnContext hook and
// DisableGeneralOptionsHandler setting runFPPConnectHTTPListener installs
// in production: the former so a self-entry's address field reflects the
// real connection it arrived on, the latter so a test here actually
// exercises route()'s own OPTIONS handling rather than net/http's built-in
// one, which httptest.Server would otherwise leave enabled by default.
func startFPPConnectTestServer(t *testing.T, view fppConnectView, nodeID string) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(newFPPConnectHandler(view, nodeID))
	srv.Config.ConnContext = fppConnectConnContext
	srv.Config.DisableGeneralOptionsHandler = true
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func getBody(t *testing.T, url string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec // url is always this test's own loopback httptest server
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body of %s: %v", url, err)
	}
	return resp, body
}

func TestFPPConnectSystemInfoRoute(t *testing.T) {
	view := fakeFPPConnectView{enabled: true, channelRanges: "0-9"}
	srv := startFPPConnectTestServer(t, view, "node-1")

	resp, body := getBody(t, srv.URL+"/api/system/info")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}

	var got fppConnectSystemInfoResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, body)
	}
	if got.UUID == "" {
		t.Fatal("uuid is empty, want the node's derived UUIDv5")
	}
	if got.HostName != "node-1" {
		t.Fatalf("HostName = %q, want %q", got.HostName, "node-1")
	}
	if got.Version != fppconnect.AdvertisedVersion {
		t.Fatalf("Version = %q, want %q", got.Version, fppconnect.AdvertisedVersion)
	}
	if got.MajorVersion != fppconnect.AdvertisedVersionMajor || got.MinorVersion != fppconnect.AdvertisedVersionMinor {
		t.Fatalf("majorVersion/minorVersion = %d/%d, want %d/%d",
			got.MajorVersion, got.MinorVersion, fppconnect.AdvertisedVersionMajor, fppconnect.AdvertisedVersionMinor)
	}
	if got.Mode != fppconnect.AdvertisedMode {
		t.Fatalf("Mode = %q, want %q", got.Mode, fppconnect.AdvertisedMode)
	}
	if got.TypeID != int(multisync.SystemTypeShowMesh) {
		t.Fatalf("typeId = %d, want %d", got.TypeID, int(multisync.SystemTypeShowMesh))
	}
	if got.ChannelRanges != "0-9" {
		t.Fatalf("channelRanges = %q, want %q", got.ChannelRanges, "0-9")
	}
	if got.Platform != fppConnectPlatform || got.Variant != fppConnectPlatform {
		t.Fatalf("Platform/Variant = %q/%q, want %q", got.Platform, got.Variant, fppConnectPlatform)
	}
}

func TestFPPConnectSystemInfoOmitsEmptyChannelRanges(t *testing.T) {
	view := fakeFPPConnectView{enabled: true}
	srv := startFPPConnectTestServer(t, view, "node-1")

	_, body := getBody(t, srv.URL+"/api/system/info")
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode: %v; body=%s", err, body)
	}
	if _, ok := raw["channelRanges"]; ok {
		t.Fatalf("channelRanges key present with no configured surface, want it omitted entirely: %s", body)
	}
}

func TestFPPConnectMultiSyncSystemsRoute(t *testing.T) {
	view := fakeFPPConnectView{enabled: true, channelRanges: "10-19"}
	srv := startFPPConnectTestServer(t, view, "node-1")

	resp, body := getBody(t, srv.URL+"/api/fppd/multiSyncSystems")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}

	var got fppConnectMultiSyncSystemsResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, body)
	}
	if len(got.Systems) != 1 {
		t.Fatalf("systems has %d entries, want 1", len(got.Systems))
	}
	entry := got.Systems[0]

	wantHost, _, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("splitting test server URL %q: %v", srv.URL, err)
	}
	if entry.Address != wantHost {
		t.Fatalf("address = %q, want %q (the local address this request actually arrived on)", entry.Address, wantHost)
	}
	if !entry.Local {
		t.Fatal("local = false, want true")
	}
	if entry.HostName != "node-1" {
		t.Fatalf("hostname = %q, want %q", entry.HostName, "node-1")
	}
	if entry.FPPModeString != fppconnect.AdvertisedMode {
		t.Fatalf("fppModeString = %q, want %q", entry.FPPModeString, fppconnect.AdvertisedMode)
	}
	if entry.FPPMode != int(multisync.PingModePlayer) {
		t.Fatalf("fppMode = %d, want %d", entry.FPPMode, int(multisync.PingModePlayer))
	}
	if entry.Type != fppConnectPlatform {
		t.Fatalf("type = %q, want %q", entry.Type, fppConnectPlatform)
	}
	if entry.TypeID != int(multisync.SystemTypeShowMesh) {
		t.Fatalf("typeId = %d, want %d", entry.TypeID, int(multisync.SystemTypeShowMesh))
	}
	if entry.ChannelRanges != "10-19" {
		t.Fatalf("channelRanges = %q, want %q", entry.ChannelRanges, "10-19")
	}
	if entry.UUID == "" {
		t.Fatal("uuid is empty")
	}
}

func TestFPPConnectMultiSyncSystemsOmitsEmptyChannelRanges(t *testing.T) {
	view := fakeFPPConnectView{enabled: true}
	srv := startFPPConnectTestServer(t, view, "node-1")

	_, body := getBody(t, srv.URL+"/api/fppd/multiSyncSystems")
	var raw struct {
		Systems []map[string]json.RawMessage `json:"systems"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode: %v; body=%s", err, body)
	}
	if len(raw.Systems) != 1 {
		t.Fatalf("systems has %d entries, want 1", len(raw.Systems))
	}
	if _, ok := raw.Systems[0]["channelRanges"]; ok {
		t.Fatalf("channelRanges key present with no configured surface, want it omitted entirely: %s", body)
	}
}

func TestFPPConnectPlaylistsRoute(t *testing.T) {
	t.Run("with shows returns a bare array", func(t *testing.T) {
		view := fakeFPPConnectView{enabled: true, showNames: []string{"Halloween", "Christmas"}}
		srv := startFPPConnectTestServer(t, view, "node-1")

		resp, body := getBody(t, srv.URL+"/api/playlists")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
		}
		if !strings.HasPrefix(strings.TrimSpace(string(body)), "[") {
			t.Fatalf("body is not a bare JSON array: %s", body)
		}
		var got []string
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode: %v; body=%s", err, body)
		}
		if len(got) != 2 || got[0] != "Halloween" || got[1] != "Christmas" {
			t.Fatalf("got %v, want [Halloween Christmas]", got)
		}
	})

	t.Run("with no shows returns an empty array, not null", func(t *testing.T) {
		view := fakeFPPConnectView{enabled: true}
		srv := startFPPConnectTestServer(t, view, "node-1")

		resp, body := getBody(t, srv.URL+"/api/playlists")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
		}
		if strings.TrimSpace(string(body)) != "[]" {
			t.Fatalf("body = %s, want []", body)
		}
	})
}

func TestFPPConnectPlaylistRoute(t *testing.T) {
	t.Run("known name", func(t *testing.T) {
		view := fakeFPPConnectView{enabled: true, showNames: []string{"Halloween"}}
		srv := startFPPConnectTestServer(t, view, "node-1")

		resp, body := getBody(t, srv.URL+"/api/playlist/Halloween")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
		}
		var got fppConnectPlaylistResponse
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode: %v; body=%s", err, body)
		}
		if got.Name != "Halloween" {
			t.Fatalf("name = %q, want %q", got.Name, "Halloween")
		}
		if len(got.MainPlaylist) != 0 || len(got.LeadIn) != 0 || len(got.LeadOut) != 0 {
			t.Fatalf("expected every list empty, got %+v", got)
		}
		if got.PlaylistInfo.TotalDuration != 0 || got.PlaylistInfo.TotalItems != 0 {
			t.Fatalf("playlistInfo = %+v, want zeroes", got.PlaylistInfo)
		}
		if !strings.Contains(string(body), `"total_duration"`) || !strings.Contains(string(body), `"total_items"`) {
			t.Fatalf("expected snake_case playlistInfo keys, got %s", body)
		}
	})

	t.Run("unknown name is 404", func(t *testing.T) {
		view := fakeFPPConnectView{enabled: true, showNames: []string{"Halloween"}}
		srv := startFPPConnectTestServer(t, view, "node-1")

		resp, _ := getBody(t, srv.URL+"/api/playlist/DoesNotExist")
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", resp.StatusCode)
		}
	})

	t.Run("percent-encoded name with a space decodes", func(t *testing.T) {
		view := fakeFPPConnectView{enabled: true, showNames: []string{"My Show"}}
		srv := startFPPConnectTestServer(t, view, "node-1")

		resp, body := getBody(t, srv.URL+"/api/playlist/My%20Show")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
		}
		var got fppConnectPlaylistResponse
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode: %v; body=%s", err, body)
		}
		if got.Name != "My Show" {
			t.Fatalf("name = %q, want %q", got.Name, "My Show")
		}
	})

	t.Run("a literal plus is not decoded as a space", func(t *testing.T) {
		view := fakeFPPConnectView{enabled: true, showNames: []string{"My+Show"}}
		srv := startFPPConnectTestServer(t, view, "node-1")

		resp, body := getBody(t, srv.URL+"/api/playlist/My+Show")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
		}
		var got fppConnectPlaylistResponse
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode: %v; body=%s", err, body)
		}
		if got.Name != "My+Show" {
			t.Fatalf("name = %q, want %q (a literal '+' must not become a space)", got.Name, "My+Show")
		}
	})
}

// TestFPPConnectRejectsOversizedOrChunkedBody is the regression test for
// review round 2 finding C: none of this listener's routes read the
// request body, so http.MaxBytesReader alone never rejects anything, since
// nothing ever calls Read. A declared Content-Length over the cap, and a
// chunked request (Content-Length unknown, reported as -1), must both be
// refused outright rather than left to sit until ReadTimeout.
func TestFPPConnectRejectsOversizedOrChunkedBody(t *testing.T) {
	view := fakeFPPConnectView{enabled: true}
	srv := startFPPConnectTestServer(t, view, "node-1")

	t.Run("declared Content-Length over the cap", func(t *testing.T) {
		body := strings.NewReader(strings.Repeat("a", fppConnectMaxBodyBytes+1))
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/system/info", body)
		if err != nil {
			t.Fatalf("building request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 for a body over the %d byte cap", resp.StatusCode, fppConnectMaxBodyBytes)
		}
	})

	t.Run("chunked body with no declared length", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/system/info", io.NopCloser(strings.NewReader("x")))
		if err != nil {
			t.Fatalf("building request: %v", err)
		}
		req.ContentLength = -1
		req.TransferEncoding = []string{"chunked"}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 for a chunked (unbounded) body", resp.StatusCode)
		}
	})

	t.Run("no body at all still serves normally", func(t *testing.T) {
		resp, _ := getBody(t, srv.URL+"/api/system/info")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200 for an ordinary bodyless GET", resp.StatusCode)
		}
	})
}

func TestFPPConnectUnmappedPathIs404(t *testing.T) {
	view := fakeFPPConnectView{enabled: true}
	srv := startFPPConnectTestServer(t, view, "node-1")

	resp, body := getBody(t, srv.URL+"/api/does-not-exist")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if len(body) == 0 {
		t.Fatal("expected a non-empty plain-text 404 body")
	}
	if strings.Contains(string(body), "<") {
		t.Fatalf("expected a plain-text body, not HTML: %s", body)
	}
}

func TestFPPConnectDisabledServesEvery404(t *testing.T) {
	view := fakeFPPConnectView{enabled: false, showNames: []string{"Halloween"}}
	srv := startFPPConnectTestServer(t, view, "node-1")

	for _, path := range []string{
		"/api/system/info",
		"/api/fppd/multiSyncSystems",
		"/api/playlists",
		"/api/playlist/Halloween",
	} {
		resp, body := getBody(t, srv.URL+path)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404 while disabled; body=%s", path, resp.StatusCode, body)
		}
	}
}

// TestFPPConnectUUIDStableAndDistinct proves the derived uuid is stable
// across separately constructed handlers for the same node id, and
// distinct for a different node id, per ADR-044's requirement that the
// advertised uuid be stable across restarts and identical on every node
// sharing a node id.
func TestFPPConnectUUIDStableAndDistinct(t *testing.T) {
	view := fakeFPPConnectView{enabled: true}

	srvA := startFPPConnectTestServer(t, view, "node-alpha")
	srvB := startFPPConnectTestServer(t, view, "node-alpha")
	srvC := startFPPConnectTestServer(t, view, "node-beta")

	fetchUUID := func(url string) string {
		_, body := getBody(t, url+"/api/system/info")
		var got fppConnectSystemInfoResponse
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode: %v; body=%s", err, body)
		}
		return got.UUID
	}

	uuidA := fetchUUID(srvA.URL)
	uuidB := fetchUUID(srvB.URL)
	uuidC := fetchUUID(srvC.URL)

	if uuidA != uuidB {
		t.Fatalf("uuid differs across two handlers for the same node id: %q vs %q", uuidA, uuidB)
	}
	if uuidA == uuidC {
		t.Fatalf("uuid identical for two different node ids: %q", uuidA)
	}
}

// TestFPPConnectNoProductIdentityLeak walks every route this listener
// serves, including a 404, and asserts no response body or header names a
// Falcon Player identity string. Mode is served lowercase "player", a
// protocol value FPP itself defines, not the product name "Player"; this
// check is case-sensitive on purpose so that value never trips it.
func TestFPPConnectNoProductIdentityLeak(t *testing.T) {
	view := fakeFPPConnectView{enabled: true, channelRanges: "0-9", showNames: []string{"Halloween"}}
	srv := startFPPConnectTestServer(t, view, "node-1")

	forbidden := []string{"Falcon", "Player", "FPP"}
	routes := []string{
		"/api/system/info",
		"/api/fppd/multiSyncSystems",
		"/api/playlists",
		"/api/playlist/Halloween",
		"/api/playlist/DoesNotExist", // this route's own 404
		"/api/does-not-exist",        // an unmapped path's 404
	}

	for _, path := range routes {
		resp, body := getBody(t, srv.URL+path)
		for _, bad := range forbidden {
			if strings.Contains(string(body), bad) {
				t.Errorf("%s: body contains forbidden identity string %q: %s", path, bad, body)
			}
		}
		for name, values := range resp.Header {
			for _, bad := range forbidden {
				if strings.Contains(name, bad) {
					t.Errorf("%s: header name %q contains forbidden identity string %q", path, name, bad)
				}
				for _, v := range values {
					if strings.Contains(v, bad) {
						t.Errorf("%s: header %q value %q contains forbidden identity string %q", path, name, v, bad)
					}
				}
			}
		}
	}
}

// TestRunFPPConnectHTTPListenerBindFailure proves a bind failure is
// recorded on status with a reason naming the address, and that the
// function returns without panicking or blocking, per ADR-044's "a bind
// failure never stops the agent" rule, exercised the same way
// multisyncstatus_test.go exercises runMultiSyncListener's identical
// contract.
func TestRunFPPConnectHTTPListenerBindFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("setup: reserving a port: %v", err)
	}
	defer func() { _ = ln.Close() }()
	addr := ln.Addr().String()

	status := newFPPConnectHTTPStatus()
	view := fakeFPPConnectView{enabled: true}

	runFPPConnectHTTPListener(context.Background(), addr, view, "node-1", status, discardLogger())

	listening, reason, observedAt := status.get()
	if listening {
		t.Fatal("listening = true, want false after a bind failure")
	}
	if reason == "" {
		t.Fatal("reason is empty, want the bind error")
	}
	if !strings.Contains(reason, addr) {
		t.Fatalf("reason = %q, want it to name the address %q", reason, addr)
	}
	if observedAt.IsZero() {
		t.Fatal("observedAt is zero, want it stamped at the failed bind attempt")
	}
}

// TestFPPConnectDirtyPathsNeverRedirect is the regression test for review
// finding 1: http.ServeMux 301-redirects, with a text/html body, any
// request whose path needs cleaning, regardless of which patterns are
// registered. route() must never hand such a request to anything that
// would do that: every case here must come back as this listener's own
// plain-text 404, never a redirect and never HTML.
func TestFPPConnectDirtyPathsNeverRedirect(t *testing.T) {
	view := fakeFPPConnectView{enabled: true, showNames: []string{"Halloween"}}
	srv := startFPPConnectTestServer(t, view, "node-1")
	srv.Client().CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	for _, path := range []string{
		"//api/system/info",
		"/api/playlist/../system/info",
		"/api/playlist/My%2FShow", // legitimate percent-encoding; still an invalid name (contains "/" once decoded, finding 7), so still 404, but must not be a redirect either
	} {
		resp, body := getBody(t, srv.URL+path)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", path, resp.StatusCode)
		}
		if loc := resp.Header.Get("Location"); loc != "" {
			t.Errorf("%s: Location header = %q, want none", path, loc)
		}
		if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "html") {
			t.Errorf("%s: Content-Type = %q, want no HTML", path, ct)
		}
		if strings.Contains(string(body), "<") {
			t.Errorf("%s: body looks like HTML: %s", path, body)
		}
	}
}

// TestFPPConnectNonGETIs404NotAllowed is the regression test for review
// finding 2: ADR-044 decision 1 says everything outside the four routes is
// 404, not http.ServeMux's own 405-plus-Allow-header answer for a wrong
// method on a registered path.
func TestFPPConnectNonGETIs404NotAllowed(t *testing.T) {
	view := fakeFPPConnectView{enabled: true}
	srv := startFPPConnectTestServer(t, view, "node-1")

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req, err := http.NewRequest(method, srv.URL+"/api/system/info", nil)
		if err != nil {
			t.Fatalf("building %s request: %v", method, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", method, resp.StatusCode)
		}
		if allow := resp.Header.Get("Allow"); allow != "" {
			t.Errorf("%s: Allow header = %q, want none", method, allow)
		}
	}

	// "OPTIONS * HTTP/1.1" is the one request net/http answers itself,
	// with a 200 and no body, before the registered Handler ever runs,
	// unless DisableGeneralOptionsHandler is set (review round 2 finding
	// A). "*" is not a URL http.NewRequest can build, so this probes the
	// raw request line over a socket instead.
	t.Run("OPTIONS * is 404, not net/http's built-in 200", func(t *testing.T) {
		host := strings.TrimPrefix(srv.URL, "http://")
		conn, err := net.Dial("tcp", host)
		if err != nil {
			t.Fatalf("dial %s: %v", host, err)
		}
		defer func() { _ = conn.Close() }()

		if _, err := fmt.Fprintf(conn, "OPTIONS * HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", host); err != nil {
			t.Fatalf("write request line: %v", err)
		}
		statusLine, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			t.Fatalf("read status line: %v", err)
		}
		if !strings.Contains(statusLine, " 404 ") {
			t.Fatalf("status line = %q, want it to contain 404", statusLine)
		}
	})
}

// TestFPPConnectHeadRequestHasNoBody proves HEAD is served (allowed
// alongside GET) with the same headers a GET would carry and an empty
// body, relying on net/http's own HEAD handling rather than a bespoke
// per-handler case.
func TestFPPConnectHeadRequestHasNoBody(t *testing.T) {
	view := fakeFPPConnectView{enabled: true}
	srv := startFPPConnectTestServer(t, view, "node-1")

	req, err := http.NewRequest(http.MethodHead, srv.URL+"/api/system/info", nil)
	if err != nil {
		t.Fatalf("building HEAD request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading HEAD body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(body) != 0 {
		t.Fatalf("HEAD body = %q, want empty", body)
	}
}

// TestFPPConnectPlaylistRejectsTraversalName is the regression test for
// review finding 7: a percent-encoded name that decodes to a path
// containing "/" must never reach the show-name membership check.
func TestFPPConnectPlaylistRejectsTraversalName(t *testing.T) {
	view := fakeFPPConnectView{enabled: true, showNames: []string{"Halloween"}}
	srv := startFPPConnectTestServer(t, view, "node-1")

	resp, _ := getBody(t, srv.URL+"/api/playlist/..%2f..%2fetc")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a traversal-shaped decoded name", resp.StatusCode)
	}
}

// TestFPPConnectValidPlaylistName unit-tests the validation review finding
// 7 requires directly, independent of routing.
func TestFPPConnectValidPlaylistName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"ordinary name", "Halloween", true},
		{"forward slash", "a/b", false},
		{"backslash", `a\b`, false},
		{"embedded NUL", "a\x00b", false},
		{"exactly dot-dot", "..", false},
		{"starts with dot-dot but is not one", "..foo", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fppConnectValidPlaylistName(tt.in); got != tt.want {
				t.Errorf("fppConnectValidPlaylistName(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestFPPConnectPollEnabledTransitionsBothWays is the regression test for
// review finding 3: the status must reflect view.Enabled() going false
// (listening stays true; reason becomes fppConnectDisabledReason) and going
// back true (reason clears), independent of any real ticker tick.
func TestFPPConnectPollEnabledTransitionsBothWays(t *testing.T) {
	status := newFPPConnectHTTPStatus()

	fppConnectPollEnabled(fakeFPPConnectView{enabled: true}, status)
	if listening, reason, _ := status.get(); !listening || reason != "" {
		t.Fatalf("enabled: listening=%v reason=%q, want true/empty", listening, reason)
	}

	fppConnectPollEnabled(fakeFPPConnectView{enabled: false}, status)
	if listening, reason, _ := status.get(); !listening || reason != fppConnectDisabledReason {
		t.Fatalf("disabled: listening=%v reason=%q, want true/%q", listening, reason, fppConnectDisabledReason)
	}

	fppConnectPollEnabled(fakeFPPConnectView{enabled: true}, status)
	if listening, reason, _ := status.get(); !listening || reason != "" {
		t.Fatalf("re-enabled: listening=%v reason=%q, want true/empty", listening, reason)
	}
}
