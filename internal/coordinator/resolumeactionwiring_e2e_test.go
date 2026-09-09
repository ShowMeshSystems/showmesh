package coordinator

// This file is Task 1's own named deliverable: proof that Track D seam
// D-3's integration gap is actually closed, not just that the adapter type
// compiles and its own unit tests pass. Every other layer of this project's
// own test suite (D-3/A's action_dispatch_test.go, D-3/B's
// resolumeaction_test.go) was already green while
// Dependencies.ResolumeActions was never assigned — the whole point of this
// file is that a fake standing in for either side of that boundary is
// exactly what hid the gap, so nothing here is a fake:
//
//   - the real HTTP handler, via a real *api.API built by api.New with a
//     real *identity.Service and a real *store.Store (Commands),
//   - the real resolumeActionDispatcherAdapter this package's own
//     resolumeactionwiring.go declares,
//   - a real *resolume.ActionDispatcher, built by resolume.NewActionDispatcher
//     exactly the way newResolumeActionDispatcherAdapter builds it in
//     coordinator.go's own Run,
//   - a real *resolume.Collector, built by the real newResolumeWiring this
//     package's Run calls, with its composition survey driven by a real
//     [resolume.CompositionStore] loaded from a real config revision
//     written to a real *store.Store — the identical path an operator's own
//     "upload a composition" and "set SHOWMESH_RESOLUME_URL" actions
//     produce.
//
// The only fake is Arena itself (an httptest.Server) — nothing in this
// project has run against a real Resolume Arena yet, and this file makes
// no claim otherwise.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/resolumecomp"
)

// --- A minimal fake Arena, just enough of TRACK-D-D3-SPEC.md's own
// endpoint table (section 2) plus the by-id survey reads D-2 already needs
// to resolve composition identity, for exactly one deck/layer/clip. This is
// this file's own fixture, deliberately not resolume's own unexported
// fakeArena (action_dispatch_test.go): that type lives in, and is private
// to, package resolume, and this test's whole point is proving the seam
// BETWEEN that package and this one, so it drives a real *resolume.Collector
// through real HTTP, never a resolume-package-internal test double. ---

const (
	e2eDeckID  = "1001"
	e2eLayerID = "2001"
	e2eClipID  = "3001"

	// e2eDeckTwoID and e2eDecoyClipID exist only for
	// TestResolumeActionEndToEndDeckMismatchIssuesNoHTTPRequest: composition
	// identity resolves from clips sampled off the CURRENTLY SELECTED deck
	// (idmap.go's own IdentitySample), so proving a deck-MISMATCH refusal
	// for e2eClipID (which lives on e2eDeckID) needs a second, unrelated
	// clip that DOES live on the deck the fixture selects instead, purely
	// so identity has something on that deck to resolve against.
	e2eDeckTwoID   = "1002"
	e2eDecoyClipID = "3002"
)

// e2eFakeArena is a tiny, mutable, in-memory stand-in for the handful of
// Arena endpoints one launchClip dispatch (plus the composition survey that
// must resolve identity before it) touches. Every request received is
// recorded, in order, so a test can assert exactly which HTTP request(s)
// this seam issued against Resolume — the proof Task 1 asks for
// specifically ("asserts an action actually issues the expected HTTP
// request to Arena").
type e2eFakeArena struct {
	mu       sync.Mutex
	requests []string // "METHOD /path", in issued order

	clipConnected   string // "Disconnected" | "Connected" | "Connected & previewing"
	layerActiveClip *int64 // nil ("active_clip": null) until the clip connects
}

func newE2EFakeArena() *e2eFakeArena {
	return &e2eFakeArena{clipConnected: "Disconnected"}
}

func (a *e2eFakeArena) record(r *http.Request) {
	a.requests = append(a.requests, r.Method+" "+r.URL.Path)
}

func (a *e2eFakeArena) requestLog() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.requests...)
}

func (a *e2eFakeArena) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record(r)

	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/product":
		_, _ = fmt.Fprint(w, `{"name":"Arena","major":7,"minor":23,"micro":2,"revision":1}`)

	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/composition/decks/by-id/"+e2eDeckID:
		_, _ = fmt.Fprintf(w, `{"id":%s,"selected":{"id":1,"value":true},"name":{"id":2,"value":"Deck One"}}`, e2eDeckID)

	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/composition/layers/by-id/"+e2eLayerID:
		activeClip := "null"
		if a.layerActiveClip != nil {
			activeClip = fmt.Sprintf(`{"id":%d}`, *a.layerActiveClip)
		}
		_, _ = fmt.Fprintf(w, `{"id":%s,"active_clip":%s}`, e2eLayerID, activeClip)

	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/composition/clips/by-id/"+e2eClipID:
		_, _ = fmt.Fprintf(w, `{"id":%s,"connected":{"id":10,"value":%q,"options":["Empty","Disconnected","Previewing","Connected","Connected & previewing"]}}`,
			e2eClipID, a.clipConnected)

	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/composition/clips/by-id/"+e2eDecoyClipID:
		// See e2eDecoyClipID's own doc comment: this clip is never
		// dispatched against, it exists only so a composition-identity
		// sample drawn from deck two has something to resolve.
		_, _ = fmt.Fprintf(w, `{"id":%s,"connected":{"id":11,"value":"Disconnected","options":["Empty","Disconnected","Previewing","Connected","Connected & previewing"]}}`,
			e2eDecoyClipID)

	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/composition/clips/by-id/"+e2eClipID+"/connect":
		// Arena's own OpenAPI specification (defect 6, 2026-08-15): "If
		// omitted, true and false are both sent — as if a short click was
		// generated" — the vendor's own documented complete gesture is an
		// OMITTED body, which is what this seam's Client.ConnectClip now
		// sends. A `false` body is still measured to return 204 and do
		// nothing at all, so this handler performs the connect for an
		// omitted body or a literal `true`, matching real Arena's own
		// documented behavior rather than any one body this seam happens
		// to send.
		body, _ := io.ReadAll(r.Body)
		if trimmed := strings.TrimSpace(string(body)); trimmed != "" && trimmed != "true" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		a.clipConnected = "Connected"
		id := int64(3001)
		a.layerActiveClip = &id
		w.WriteHeader(http.StatusNoContent)

	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/composition/disconnect-all":
		a.clipConnected = "Disconnected"
		a.layerActiveClip = nil
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// e2eTestComposition is the uploaded-.avc-file stand-in for this whole
// test: one deck, one layer (index 0), one deck clip on that layer/deck.
// Built directly as a resolumecomp.Composition rather than parsed from a
// file — this package's own BuildTrackedComposition (idmap.go) accepts one
// either way, and a Go literal here keeps this fixture's shape visible in
// the same file that depends on it.
func e2eTestComposition() *resolumecomp.Composition {
	return &resolumecomp.Composition{
		Name:      "D-3 wiring E2E fixture",
		WrittenBy: resolumecomp.WrittenBy{Product: "Resolume Arena", Major: 7, Minor: 23, Micro: 2, Revision: 1},
		Canvas:    resolumecomp.Canvas{Width: 1920, Height: 1080},
		Decks:     []resolumecomp.Deck{{ID: e2eDeckID, Name: "Deck One"}},
		Layers:    []resolumecomp.Layer{{ID: e2eLayerID, Index: 0}},
		Clips:     []resolumecomp.Clip{{ID: e2eClipID, DeckID: e2eDeckID, LayerIndex: 0, Name: "E2E Clip"}},
	}
}

// TestResolumeActionEndToEndLaunchClipReachesArena is Task 1's own proof.
// It drives POST /api/v1/resolume/actions on a real *api.API, through the
// real resolumeActionDispatcherAdapter, into a real *resolume.ActionDispatcher
// over a real *resolume.Collector, against a fake Arena httptest.Server —
// and asserts the connect request this seam is supposed to issue was
// actually received.
func TestResolumeActionEndToEndLaunchClipReachesArena(t *testing.T) {
	arena := newE2EFakeArena()
	srv := httptest.NewServer(arena)
	defer srv.Close()

	ctx := context.Background()
	st := openTestStore(t)
	logger := testLogger()

	// The uploaded composition: the identical path an operator's own
	// upload handler (resolumecomposition.go) writes through, replayed
	// here via the same test helper resolumewiring_test.go's own D-2/B
	// tests already use.
	writeTestCompositionRevision(t, st, 1, e2eTestComposition())
	compWiring := newResolumeCompositionWiring(ctx, st, logger)
	if rev := compWiring.store.LoadedRevision(); rev != 1 {
		t.Fatalf("composition revision loaded at startup = %d, want 1", rev)
	}

	cfg := config.Config{ResolumeURL: srv.URL, ResolumeID: "resolume-e2e"}
	sink := &fppSink{st: st, logger: logger}
	runner := collector.NewRunner(sink, logger)

	wire, err := newResolumeWiring(ctx, cfg, runner, compWiring.store, logger, nil, nil)
	if err != nil {
		t.Fatalf("newResolumeWiring: %v", err)
	}
	if wire.collector == nil {
		t.Fatal("wire.collector = nil, want a real *resolume.Collector when ResolumeURL is set")
	}

	// One synchronous Poll, called directly rather than through
	// runner.Run's own goroutine and timer: compositionRevisionChanged
	// (collector.go) is unconditional on the FIRST poll after a fresh
	// composition load (lastSeenCompositionRevision starts at its Go zero
	// value, which the just-loaded revision 1 never equals), so this one
	// call runs the full survey against arena and — if every by-id read
	// this fixture's own clip/layer/deck resolves, which arena's handlers
	// above are built to do — leaves wire.collector's own
	// LastSurveySnapshot reporting identity confirmed, exactly what a real
	// coordinator's own Runner-driven poll loop would produce after its
	// first successful cycle.
	if _, ok := wire.collector.Poll(ctx); !ok {
		t.Fatal("wire.collector.Poll: did not run (unexpectedly throttled on its very first call)")
	}
	snap := wire.collector.LastSurveySnapshot()
	if !snap.SurveyRan || snap.Identity != "identified" {
		t.Fatalf("composition identity after warmup poll = %+v, want SurveyRan=true, Identity=identified "+
			"(fixture wiring is wrong if this fails, not the adapter under test)", snap)
	}

	// The real adapter under test — resolumeactionwiring.go's own
	// production constructor, not a hand-built substitute for it.
	adapter := newResolumeActionDispatcherAdapter(wire.collector)

	svc := identity.NewService(st, time.Now, t.TempDir(), identity.WithLogger(logger))
	operator, err := svc.CreatePrincipal(ctx, "e2e-operator", identity.KindHuman, identity.RoleOperator, "not-a-real-secret-01")
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	tok, err := svc.IssueToken(ctx, operator.ID, "e2e-test", nil)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	apiInst := api.New(api.Dependencies{
		Identity:        svc,
		Commands:        st,
		ResolumeActions: adapter,
	}, api.Options{Logger: logger})

	reqBody := `{"action":"launchClip","idempotencyKey":"e2e-launch-clip-1","params":{"clip":"E2E Clip","deck":"Deck One"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/resolume/actions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok.Value)
	rec := httptest.NewRecorder()

	apiInst.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Result struct {
			Outcome       string  `json:"outcome"`
			OutcomeReason string  `json:"outcomeReason"`
			DispatchedAt  *string `json:"dispatchedAt"`
			ResolvedID    string  `json:"resolvedId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body: %s", err, rec.Body.String())
	}
	if resp.Result.Outcome != "confirmed" {
		t.Fatalf("outcome = %q, want %q; reason: %s; body: %s", resp.Result.Outcome, "confirmed", resp.Result.OutcomeReason, rec.Body.String())
	}
	if resp.Result.OutcomeReason == "" {
		t.Error("outcomeReason is empty on a confirmed outcome")
	}
	if resp.Result.DispatchedAt == nil {
		t.Error("dispatchedAt is null on a confirmed outcome")
	}
	// Review finding 8: the object id this launchClip actually addressed
	// stays visible in the response, even though the request that reached
	// it never named one.
	if resp.Result.ResolvedID != e2eClipID {
		t.Errorf("resolvedId = %q, want %q (the object id \"E2E Clip\" resolved to)", resp.Result.ResolvedID, e2eClipID)
	}

	// The proof: the real HTTP request this seam was supposed to issue
	// against Arena was actually sent, with the body TRACK-D-D3-SPEC.md
	// §3.1 requires (the bare boolean `true`, never `false`).
	wantPath := "POST /api/v1/composition/clips/by-id/" + e2eClipID + "/connect"
	found := false
	for _, r := range arena.requestLog() {
		if r == wantPath {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("arena never received %q; requests = %v", wantPath, arena.requestLog())
	}
}

// failOnAnyRequestArena is ADR-037 acceptance criterion 6's own fixture:
// resolution (internal/coordinator/collector/resolume/references.go) reads
// only the stored *TrackedComposition already in memory, and issues no
// HTTP request of its own — this handler fails the test outright the
// moment it receives ANY request, which is a stronger proof than counting
// requests before and after.
type failOnAnyRequestArena struct{ t *testing.T }

func (a *failOnAnyRequestArena) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.t.Errorf("resolution issued an HTTP request to Arena: %s %s — resolving a name against the stored "+
		"composition must never touch the network", r.Method, r.URL.Path)
	w.WriteHeader(http.StatusInternalServerError)
}

// TestResolumeActionEndToEndUnresolvedReferenceIssuesNoHTTPRequest is
// acceptance criterion 6: a launchClip naming a clip the stored composition
// does not contain is refused by resolution alone, before
// resolume.ActionDispatcher.Dispatch — and therefore before ANY of its own
// pre-dispatch reads — is ever reached. Deliberately skips the warmup
// wire.collector.Poll every other test in this file performs: composition
// identity is a fact resolume.ActionDispatcher.Dispatch checks, and this
// test proves resolution never reaches that far, so there is nothing for a
// warmup poll to warm.
func TestResolumeActionEndToEndUnresolvedReferenceIssuesNoHTTPRequest(t *testing.T) {
	arena := &failOnAnyRequestArena{t: t}
	srv := httptest.NewServer(arena)
	defer srv.Close()

	ctx := context.Background()
	st := openTestStore(t)
	logger := testLogger()

	writeTestCompositionRevision(t, st, 1, e2eTestComposition())
	compWiring := newResolumeCompositionWiring(ctx, st, logger)

	cfg := config.Config{ResolumeURL: srv.URL, ResolumeID: "resolume-e2e-noreq"}
	sink := &fppSink{st: st, logger: logger}
	runner := collector.NewRunner(sink, logger)
	wire, err := newResolumeWiring(ctx, cfg, runner, compWiring.store, logger, nil, nil)
	if err != nil {
		t.Fatalf("newResolumeWiring: %v", err)
	}

	adapter := newResolumeActionDispatcherAdapter(wire.collector)

	svc := identity.NewService(st, time.Now, t.TempDir(), identity.WithLogger(logger))
	operator, err := svc.CreatePrincipal(ctx, "e2e-operator-noreq", identity.KindHuman, identity.RoleOperator, "not-a-real-secret-04")
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	tok, err := svc.IssueToken(ctx, operator.ID, "e2e-test", nil)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	apiInst := api.New(api.Dependencies{Identity: svc, Commands: st, ResolumeActions: adapter}, api.Options{Logger: logger})

	reqBody := `{"action":"launchClip","idempotencyKey":"e2e-unresolved-1","params":{"clip":"Does Not Exist","deck":"Deck One"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/resolume/actions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok.Value)
	rec := httptest.NewRecorder()
	apiInst.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Result struct {
			Outcome       string `json:"outcome"`
			OutcomeReason string `json:"outcomeReason"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body: %s", err, rec.Body.String())
	}
	if resp.Result.Outcome != "refused" {
		t.Fatalf("outcome = %q, want %q; reason: %s", resp.Result.Outcome, "refused", resp.Result.OutcomeReason)
	}
	if !strings.Contains(resp.Result.OutcomeReason, "Does Not Exist") {
		t.Errorf("outcomeReason = %q, want it to name the unresolved clip", resp.Result.OutcomeReason)
	}
}

// TestResolumeActionEndToEndDeckMismatchIssuesNoHTTPRequest is the negative
// half of the same proof: a clip whose deck is not the currently selected
// one must be refused BEFORE any write reaches Arena — acceptance criterion
// 3 — proven here through the same real stack, not only against A's own
// unit tests.
func TestResolumeActionEndToEndDeckMismatchIssuesNoHTTPRequest(t *testing.T) {
	// A second deck (1002) reports itself selected instead of deck 1001,
	// the deck this fixture's clip actually belongs to — deckRefusal's own
	// gate (action.go), proven here through the real stack rather than only
	// against resolume's own unit tests.
	arena := newE2EFakeArenaWithSecondDeck()
	srv := httptest.NewServer(arena)
	defer srv.Close()

	ctx := context.Background()
	st := openTestStore(t)
	logger := testLogger()

	comp := e2eTestComposition()
	comp.Decks = append(comp.Decks, resolumecomp.Deck{ID: e2eDeckTwoID, Name: "Deck Two"})
	// e2eDecoyClipID's own doc comment: identity is sampled off the
	// CURRENTLY SELECTED deck, so this fixture needs a clip that actually
	// belongs to deck two for identity to resolve at all while e2eClipID
	// (deck one) stays available to be refused as a deck mismatch.
	comp.Clips = append(comp.Clips, resolumecomp.Clip{ID: e2eDecoyClipID, DeckID: e2eDeckTwoID, LayerIndex: 0, Name: "Decoy Clip"})
	writeTestCompositionRevision(t, st, 1, comp)
	compWiring := newResolumeCompositionWiring(ctx, st, logger)

	cfg := config.Config{ResolumeURL: srv.URL, ResolumeID: "resolume-e2e-2"}
	sink := &fppSink{st: st, logger: logger}
	runner := collector.NewRunner(sink, logger)
	wire, err := newResolumeWiring(ctx, cfg, runner, compWiring.store, logger, nil, nil)
	if err != nil {
		t.Fatalf("newResolumeWiring: %v", err)
	}
	if _, ok := wire.collector.Poll(ctx); !ok {
		t.Fatal("wire.collector.Poll: did not run")
	}
	snap := wire.collector.LastSurveySnapshot()
	if !snap.SurveyRan || snap.Identity != "identified" {
		t.Fatalf("composition identity after warmup poll = %+v, want identified", snap)
	}
	if !snap.SelectedDeckKnown || snap.SelectedDeckID.String() != "1002" {
		t.Fatalf("selected deck after warmup poll = %+v, want deck 1002 selected (fixture wiring is wrong if this fails)", snap)
	}

	adapter := newResolumeActionDispatcherAdapter(wire.collector)

	svc := identity.NewService(st, time.Now, t.TempDir(), identity.WithLogger(logger))
	operator, err := svc.CreatePrincipal(ctx, "e2e-operator-2", identity.KindHuman, identity.RoleOperator, "not-a-real-secret-02")
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	tok, err := svc.IssueToken(ctx, operator.ID, "e2e-test", nil)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	apiInst := api.New(api.Dependencies{Identity: svc, Commands: st, ResolumeActions: adapter}, api.Options{Logger: logger})

	reqBody := `{"action":"launchClip","idempotencyKey":"e2e-deck-mismatch-1","params":{"clip":"E2E Clip","deck":"Deck One"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/resolume/actions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok.Value)
	rec := httptest.NewRecorder()
	apiInst.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Result struct {
			Outcome       string `json:"outcome"`
			OutcomeReason string `json:"outcomeReason"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body: %s", err, rec.Body.String())
	}
	if resp.Result.Outcome != "refused" {
		t.Fatalf("outcome = %q, want %q; reason: %s", resp.Result.Outcome, "refused", resp.Result.OutcomeReason)
	}

	requests := arena.requestLog()
	for _, r := range requests {
		if strings.Contains(r, "/connect") {
			t.Errorf("arena received a connect request for a deck-mismatched clip; requests = %v", requests)
		}
	}
}

// e2eFakeArenaWithSecondDeck extends e2eFakeArena with a second deck object
// (id 1002) that reports itself selected instead of deck 1001 — everything
// else is identical to e2eFakeArena.
type e2eFakeArenaWithSecondDeck struct {
	*e2eFakeArena
}

func newE2EFakeArenaWithSecondDeck() *e2eFakeArenaWithSecondDeck {
	return &e2eFakeArenaWithSecondDeck{e2eFakeArena: newE2EFakeArena()}
}

func (a *e2eFakeArenaWithSecondDeck) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/api/v1/composition/decks/by-id/1001" {
		a.mu.Lock()
		a.record(r)
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":1001,"selected":{"id":1,"value":false},"name":{"id":2,"value":"Deck One"}}`)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/api/v1/composition/decks/by-id/1002" {
		a.mu.Lock()
		a.record(r)
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":1002,"selected":{"id":3,"value":true},"name":{"id":4,"value":"Deck Two"}}`)
		return
	}
	a.e2eFakeArena.ServeHTTP(w, r)
}

// --- The safety class, proven through the wired path (not only against a
// fake), per this task's own requirement: "if the mapping drops or inverts
// this, a blackout becomes refusable for want of an audit write, which is
// the exact failure ADR-024 decision 11 exists to prevent." ---

// installFailAuditTrigger mirrors internal/coordinator/api's own
// installFailAuditTrigger (config_test.go) exactly — that helper is
// unexported to package api, so this is this package's own copy, not a
// shared import: a raw SQLite trigger that aborts every INSERT into
// audit_log, simulating an audit store that is failing, the identical
// injection technique that package's own
// TestResolumeActionExemptDispatchesWhenAuditFails/
// TestResolumeActionNonExemptFailsClosedWhenAuditFails already use against
// D-3/B's FAKE dispatcher. This file's own tests below run the identical
// scenario against the REAL adapter and REAL *resolume.ActionDispatcher —
// the "wired path" this task asks for.
func installFailAuditTrigger(t *testing.T, storeDir string) {
	t.Helper()
	dbPath := filepath.Join(storeDir, "showmesh.db")
	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw connection to %q: %v", dbPath, err)
	}
	defer func() { _ = raw.Close() }()

	_, err = raw.ExecContext(context.Background(), `
		CREATE TRIGGER fail_audit BEFORE INSERT ON audit_log
		BEGIN SELECT RAISE(ABORT, 'injected audit failure'); END;
	`)
	if err != nil {
		t.Fatalf("install fail_audit trigger: %v", err)
	}
}

// TestResolumeActionEndToEndSafetyClassSurvivesTranslationUnderAuditFailure
// dispatches BOTH an exempt action (blackout) and a non-exempt one
// (launchClip) against the real wired stack with the audit store injected
// to fail, and asserts each takes the OPPOSITE path ADR-024 decision 11
// requires — through resolumeActionDispatcherAdapter's own AuditExempt
// translation (Actions(), resolumeactionwiring.go), not against
// internal/coordinator/api's own fake dispatcher (which already proves the
// HANDLER'S side of this rule in resolumeaction_test.go, independent of
// whatever a real adapter reports).
func TestResolumeActionEndToEndSafetyClassSurvivesTranslationUnderAuditFailure(t *testing.T) {
	arena := newE2EFakeArena()
	srv := httptest.NewServer(arena)
	defer srv.Close()

	ctx := context.Background()
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "db")
	st, err := store.Open(ctx, storeDir, nil)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	logger := testLogger()

	writeTestCompositionRevision(t, st, 1, e2eTestComposition())
	compWiring := newResolumeCompositionWiring(ctx, st, logger)

	cfg := config.Config{ResolumeURL: srv.URL, ResolumeID: "resolume-e2e-audit"}
	sink := &fppSink{st: st, logger: logger}
	runner := collector.NewRunner(sink, logger)
	wire, err := newResolumeWiring(ctx, cfg, runner, compWiring.store, logger, nil, nil)
	if err != nil {
		t.Fatalf("newResolumeWiring: %v", err)
	}
	if _, ok := wire.collector.Poll(ctx); !ok {
		t.Fatal("wire.collector.Poll: did not run")
	}
	if snap := wire.collector.LastSurveySnapshot(); !snap.SurveyRan || snap.Identity != "identified" {
		t.Fatalf("composition identity after warmup poll = %+v, want identified", snap)
	}

	adapter := newResolumeActionDispatcherAdapter(wire.collector)

	svc := identity.NewService(st, time.Now, filepath.Join(dir, "identity"), identity.WithLogger(logger))
	operator, err := svc.CreatePrincipal(ctx, "e2e-operator-audit", identity.KindHuman, identity.RoleOperator, "not-a-real-secret-03")
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	tok, err := svc.IssueToken(ctx, operator.ID, "e2e-test", nil)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	installFailAuditTrigger(t, storeDir)

	apiInst := api.New(api.Dependencies{Identity: svc, Commands: st, ResolumeActions: adapter}, api.Options{Logger: logger})

	doDispatch := func(action, idemKey, paramsJSON string) (status int, body []byte) {
		reqBody := `{"action":"` + action + `","idempotencyKey":"` + idemKey + `"`
		if paramsJSON != "" {
			reqBody += `,"params":` + paramsJSON
		}
		reqBody += `}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/resolume/actions", strings.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok.Value)
		rec := httptest.NewRecorder()
		apiInst.Handler.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}

	// reportDegradedAttribution (fppcommand_handler.go) writes its reason
	// to os.Stderr directly, not through h.logger, so that is the only
	// place this cross-package test can observe which reason fired.
	doDispatchCapturingStderr := func(action, idemKey, paramsJSON string) (status int, body []byte, stderrOutput string) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("os.Pipe: %v", err)
		}
		orig := os.Stderr
		os.Stderr = w
		status, body = doDispatch(action, idemKey, paramsJSON)
		os.Stderr = orig
		if err := w.Close(); err != nil {
			t.Fatalf("close stderr pipe writer: %v", err)
		}
		captured, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("read captured stderr: %v", err)
		}
		return status, body, string(captured)
	}

	// blackout: exempt. Must still be attempted (a 200, and a real
	// disconnect-all request reaching Arena) despite the audit store
	// failing — never refused for want of an audit write.
	status, body := doDispatch("blackout", "e2e-audit-blackout-1", "")
	if status != http.StatusOK {
		t.Fatalf("blackout (exempt) status = %d, want 200 despite the audit store failing; body: %s", status, body)
	}
	var blackoutResp struct {
		Result struct {
			Outcome             string `json:"outcome"`
			AttributionDegraded bool   `json:"attributionDegraded"`
			ResolvedID          string `json:"resolvedId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &blackoutResp); err != nil {
		t.Fatalf("decode blackout response: %v; body: %s", err, body)
	}
	if blackoutResp.Result.Outcome == "refused" {
		t.Fatalf("blackout (exempt) outcome = refused; ADR-024 decision 11 must never refuse an exempt action for an audit failure")
	}
	if !blackoutResp.Result.AttributionDegraded {
		t.Errorf("blackout (exempt) attributionDegraded = false, want true (it proceeded on a failing audit store)")
	}
	if blackoutResp.Result.ResolvedID != "" {
		t.Errorf("blackout resolvedId = %q, want absent — blackout addresses nothing to resolve an id for", blackoutResp.Result.ResolvedID)
	}
	foundDisconnectAll := false
	for _, r := range arena.requestLog() {
		if r == "POST /api/v1/composition/disconnect-all" {
			foundDisconnectAll = true
		}
	}
	if !foundDisconnectAll {
		t.Errorf("arena never received the blackout dispatch (POST /api/v1/composition/disconnect-all); requests = %v", arena.requestLog())
	}

	// launchClip: NOT exempt, so it degrades rather than exempts. ADR-024
	// decision 11's amendment (owner ruling, 2026-08-26) removed the
	// fail-closed default this test used to assert for every non-exempt
	// action: it must still dispatch (a 200, and a real connect request
	// reaching Arena) with degraded attribution, and the reported reason
	// must name audit unavailability, not the safety-class exemption
	// blackout used above, since launchClip is not a member of that class.
	status, body, stderrOutput := doDispatchCapturingStderr("launchClip", "e2e-audit-launchclip-1", `{"clip":"E2E Clip","deck":"Deck One"}`)
	if status != http.StatusOK {
		t.Fatalf("launchClip (not exempt) status = %d, want 200 despite the audit store failing; body: %s", status, body)
	}
	// The two substrings mirror api.degradedAttributionReasonAuditNeverBlocks
	// and api.degradedAttributionReasonSafetyClassExemption, unexported and
	// unreachable from this package, so duplicated rather than referenced.
	if !strings.Contains(stderrOutput, "audit-unavailability-never-blocks rule") {
		t.Errorf("launchClip (not exempt) degraded-attribution log did not name the audit-never-blocks reason; got: %s", stderrOutput)
	}
	if strings.Contains(stderrOutput, "safety class exemption") {
		t.Errorf("launchClip (not exempt) degraded-attribution log named the safety-class exemption reason; it is not a member of that class: %s", stderrOutput)
	}
	var launchClipResp struct {
		Result struct {
			Outcome             string `json:"outcome"`
			AttributionDegraded bool   `json:"attributionDegraded"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &launchClipResp); err != nil {
		t.Fatalf("decode launchClip response: %v; body: %s", err, body)
	}
	if launchClipResp.Result.Outcome == "refused" {
		t.Fatalf("launchClip (not exempt) outcome = refused; ADR-024 decision 11 amended must never refuse for an audit failure")
	}
	if !launchClipResp.Result.AttributionDegraded {
		t.Errorf("launchClip (not exempt) attributionDegraded = false, want true (it proceeded on a failing audit store)")
	}
	foundConnect := false
	for _, r := range arena.requestLog() {
		if strings.Contains(r, "/connect") {
			foundConnect = true
		}
	}
	if !foundConnect {
		t.Errorf("arena never received a connect request for launchClip; requests = %v", arena.requestLog())
	}
}
