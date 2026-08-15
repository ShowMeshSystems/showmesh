//go:build integration

package integration

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
)

// requireLiveFPPForWrites is what every WRITE-dispatching test in this
// package calls instead of requireLiveFPP directly: the plain guard only
// checks that SOMETHING answers /api/fppd/status, so pointed at a deployed
// host the command suites would dispatch Start Playlist, Stop Now, volume
// changes, and playlist navigation at a live show display. CLAUDE.md's
// standing rule: never a write, a command, a restart, or a settings change
// against docs/reference-installation.md's fleet.
//
// The refusal is decided BEFORE any probe: FPP invokes commands over GET
// (the Step 5 lesson), so even the reachability probe is not a request this
// guard may send to a host it is about to refuse. READ-only tests
// (fpp_e2e_test.go's TestFPPSuccessPathThroughRealCoordinator) keep calling
// requireLiveFPP: reads against a real host are explicitly permitted by the
// standing rule. The collector package's own integration_test.go has a
// separate default-URL-only gate for its container stop/start test.

// envAllowNonlocalFPPWrites is the escape hatch for the legitimate case of
// someone deliberately pointing SHOWMESH_TEST_FPP_URL at a second bench
// machine on a non-loopback address. Its value must be that machine's
// hostname exactly as it appears in the URL, never a blanket "yes": a
// stale export in a shell profile then re-authorizes one named machine,
// not whatever the URL points at years later.
const envAllowNonlocalFPPWrites = "SHOWMESH_TEST_FPP_ALLOW_NONLOCAL_WRITES"

// isLoopbackFPPHost reports whether host (a URL hostname: an IP literal,
// "localhost", or an arbitrary DNS name) names a loopback address. DNS
// names other than "localhost" are deliberately never treated as loopback:
// resolving them would add a network round trip to a safety check and
// would still be wrong the instant the name's A record pointed elsewhere.
func isLoopbackFPPHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// evaluateFPPWriteGuard is requireLiveFPPForWrites's pure decision: given
// the URL a write suite is about to dispatch against and the override
// env's value, return nil to proceed or an error naming why not. Kept
// separate from requireLiveFPPForWrites so it is testable without a
// network probe or a *testing.T.
func evaluateFPPWriteGuard(rawURL, allowNonlocalHost string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parsing FPP write target %q: %w", rawURL, err)
	}
	host := parsed.Hostname()
	if isLoopbackFPPHost(host) {
		return nil
	}
	// The override authorizes exactly one named host, compared to the
	// URL's own hostname; an empty value authorizes nothing.
	if allow := strings.TrimSpace(allowNonlocalHost); allow != "" && strings.EqualFold(allow, host) {
		return nil
	}
	return fmt.Errorf(
		"refusing to dispatch a write against non-loopback host %q (target %s): "+
			"CLAUDE.md's standing rule is never a write, a command, a restart, or a "+
			"settings change against the deployed FPP fleet, and only the bench "+
			"container is a permitted default write target. If this is deliberately "+
			"a second bench machine, set %s=%s to authorize that one host",
		host, rawURL, envAllowNonlocalFPPWrites, host)
}

// requireLiveFPPForWrites evaluates the non-loopback refusal against the
// configured URL BEFORE probing anything, then defers to requireLiveFPP
// for the reachability probe. It calls t.Fatalf, never t.Skipf, when the
// target is refused: LESSONS.md records twice that a skip is silent and a
// test can report success while never having run at all, and a write suite
// silently skipping past a deployed host would be exactly that failure
// mode aimed at a live show.
func requireLiveFPPForWrites(t *testing.T) string {
	t.Helper()
	if err := evaluateFPPWriteGuard(testFPPURL(), os.Getenv(envAllowNonlocalFPPWrites)); err != nil {
		t.Fatalf("%v", err)
	}
	return requireLiveFPP(t)
}

func TestEvaluateFPPWriteGuard(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		allow   string
		wantErr bool
	}{
		{name: "loopback IPv4 default", url: "http://localhost:8090", wantErr: false},
		{name: "loopback IPv4 literal", url: "http://127.0.0.1:8090", wantErr: false},
		{name: "loopback IPv4 literal high in the 127/8 block", url: "http://127.44.9.2:8090", wantErr: false},
		{name: "loopback IPv6 literal", url: "http://[::1]:8090", wantErr: false},
		{name: "LAN IP without override", url: "http://172.20.10.3:8090", wantErr: true},
		{name: "LAN IP with matching override", url: "http://172.20.10.3:8090", allow: "172.20.10.3", wantErr: false},
		{name: "LAN IP with a DIFFERENT host's override", url: "http://172.20.10.3:8090", allow: "192.168.1.50", wantErr: true},
		{name: "hostname without override", url: "http://fpp-bench-2.local:80", wantErr: true},
		{name: "hostname with matching override, case-insensitive", url: "http://fpp-bench-2.local:80", allow: "FPP-Bench-2.local", wantErr: false},
		{name: "legacy blanket yes authorizes nothing", url: "http://172.20.10.3:8090", allow: "yes", wantErr: true},
		{name: "whitespace-only override authorizes nothing", url: "http://172.20.10.3:8090", allow: "   ", wantErr: true},
		{name: "deployed-looking hostname without override", url: "http://fpp-main.showmesh.internal", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := evaluateFPPWriteGuard(tc.url, tc.allow)
			if tc.wantErr && err == nil {
				t.Fatalf("evaluateFPPWriteGuard(%q, %q) = nil, want an error", tc.url, tc.allow)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("evaluateFPPWriteGuard(%q, %q) = %v, want nil", tc.url, tc.allow, err)
			}
		})
	}
}
