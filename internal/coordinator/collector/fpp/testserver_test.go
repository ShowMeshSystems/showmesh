package fpp

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// testLogger discards output: these tests assert behavior, not log
// content.
func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// loadTestdata reads a file from testdata/, failing the test immediately if
// it is missing — a missing fixture should never be silently treated as an
// empty response.
func loadTestdata(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("loadTestdata(%q): %v", name, err)
	}
	return body
}

// fppServer is a minimal httptest-backed stand-in for a real fppd's HTTP
// API. It records every request it receives (path and method), which is
// what TestMultiSyncEnabledNeverReadsTheSettingsEndpoint uses to prove the
// collector never requests /api/settings/MultiSyncEnabled at all — a
// stronger assertion than checking the decoded value, since it holds
// regardless of what that endpoint happens to return.
type fppServer struct {
	mu    sync.Mutex
	hits  map[string][]string // path -> methods observed, in order
	route map[string]http.HandlerFunc

	// trapPrefixes are path prefixes that panic the server the instant any
	// request path starts with one — see trapPrefix's doc comment.
	trapPrefixes []string

	// trapFired and trapDetail record a trapped request independently of
	// the panic below. Step 5 review finding 7: the panic happens inside
	// an http.Handler goroutine, and net/http's own per-connection
	// recovery catches it and merely logs to stderr — it never reaches the
	// test goroutine's stack, so go test reports the test as passing even
	// though the forbidden path was hit. trapFired is what start's
	// t.Cleanup actually asserts on; the panic is kept only as a loud,
	// immediate signal for a human running the suite with -v.
	trapFired  atomic.Bool
	trapDetail atomic.Value // string, set at most once, first trap wins
}

func newFPPServer() *fppServer {
	return &fppServer{
		hits:  make(map[string][]string),
		route: make(map[string]http.HandlerFunc),
	}
}

// trapPrefix registers prefix such that ANY request whose path starts with
// it panics immediately, regardless of the exact remaining path.
//
// Step 3 review finding 4.3: the exact-path trap on
// "/api/settings/MultiSyncEnabled" alone is real but narrow — confirmed by
// mutation, re-adding a call to that exact path fails the test correctly,
// but a plausible refactor that instead reads "/api/settings" (a "fetch all
// settings at once" endpoint a future collector version might use to answer
// the same question) is a different path and sailed straight through the
// exact-path trap without ever tripping it. A whole-family prefix trap
// closes that: the collector must never talk to anything under
// "/api/settings" at all, not merely never that one exact leaf.
func (s *fppServer) trapPrefix(prefix string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trapPrefixes = append(s.trapPrefixes, prefix)
}

// serveBody registers path to respond 200 with body and the given content
// type.
func (s *fppServer) serveBody(path string, body []byte) {
	s.route[path] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
}

// serveStatus registers path to respond with a fixed HTTP status and no
// meaningful body, simulating an FPP-side error.
func (s *fppServer) serveStatus(path string, status int) {
	s.route[path] = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}
}

// serveRedirect registers path to respond 302 Found with a Location header
// pointing at location — simulating an FPP whose response is (accidentally
// misconfigured, or maliciously) a redirect to an entirely different host
// and path. Step 5 review finding 1's exact reproduction: a real FPP
// answering /api/fppd/status with a 302 to another host's
// /api/command/Start%20Playlist/... .
func (s *fppServer) serveRedirect(path, location string) {
	s.route[path] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", location)
		w.WriteHeader(http.StatusFound)
	}
}

func (s *fppServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits[r.URL.Path] = append(s.hits[r.URL.Path], r.Method)
		var trapped string
		for _, prefix := range s.trapPrefixes {
			if strings.HasPrefix(r.URL.Path, prefix) {
				trapped = prefix
				break
			}
		}
		h, ok := s.route[r.URL.Path]
		s.mu.Unlock()

		if trapped != "" {
			detail := "request to " + r.URL.Path + " trapped under forbidden prefix " + trapped
			s.trapDetail.CompareAndSwap(nil, detail)
			s.trapFired.Store(true)
			panic("fppServer: " + detail + " — see doc.go for why")
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.Close()
		if s.trapFired.Load() {
			detail, _ := s.trapDetail.Load().(string)
			t.Errorf("fppServer: %s. net/http recovers a handler panic per connection, so it never reaches this test's own goroutine — this assertion, not the panic, is what actually fails the test. See trapPrefix's doc comment.", detail)
		}
	})
	return srv
}

// hitCount reports how many requests path received.
func (s *fppServer) hitCount(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.hits[path])
}

// pathSet returns the sorted, deduplicated set of every distinct path this
// server has ever received a request for.
func (s *fppServer) pathSet() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	paths := make([]string, 0, len(s.hits))
	for p := range s.hits {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// assertExactPathSet fails t unless the set of distinct paths this server
// has received requests for is exactly want (order-independent, but no
// more and no fewer).
//
// Step 3 review finding 4.3: "nothing pins the *set* of endpoints polled,
// so an extra request added anywhere in Poll is invisible." hitCount alone
// only ever proves a named path was or was not hit; it says nothing about
// whether some OTHER, unnamed path was hit instead or in addition. This is
// the assertion that closes that: pin the whole set, not one member of it.
func (s *fppServer) assertExactPathSet(t *testing.T, want ...string) {
	t.Helper()
	sort.Strings(want)
	got := s.pathSet()
	if len(got) != len(want) {
		t.Fatalf("fppServer: paths requested = %v, want exactly %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("fppServer: paths requested = %v, want exactly %v", got, want)
		}
	}
}

// assertOnlyGET fails the test if any recorded request used a method other
// than GET — the mechanical check behind the Step 3 contract's "the
// collector issues GET requests and nothing else."
func (s *fppServer) assertOnlyGET(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for path, methods := range s.hits {
		for _, m := range methods {
			if m != http.MethodGet {
				t.Errorf("fppServer: %s %s: collector must issue GET only", m, path)
			}
		}
	}
}
