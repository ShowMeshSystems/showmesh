package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fppConnectRegisterRequest is one recorded POST /api/v1/assets request:
// every field part, in the order it arrived, plus the file part's name and
// bytes and the Authorization header.
type fppConnectRegisterRequest struct {
	fieldOrder []string
	fields     map[string]string
	fileName   string
	fileBytes  []byte
	auth       string
}

// newFPPConnectRegisterFake builds an httptest.Server standing in for the
// coordinator's POST /api/v1/assets, recording every request it receives
// and answering however respond decides. The file part is read directly
// from the multipart stream (never buffered by net/http's own form-parsing,
// which would defeat the point of asserting streaming behavior).
func newFPPConnectRegisterFake(t *testing.T, respond func(req fppConnectRegisterRequest) (status int, body string)) (*httptest.Server, func() []fppConnectRegisterRequest) {
	t.Helper()
	var mu sync.Mutex
	var received []fppConnectRegisterRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/assets" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		mr, err := r.MultipartReader()
		if err != nil {
			t.Errorf("fake assets API: request is not multipart: %v", err)
			http.Error(w, "not multipart", http.StatusBadRequest)
			return
		}
		rec := fppConnectRegisterRequest{fields: map[string]string{}, auth: r.Header.Get("Authorization")}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("fake assets API: reading a part: %v", err)
				http.Error(w, "bad multipart body", http.StatusBadRequest)
				return
			}
			name := part.FormName()
			data, err := io.ReadAll(part)
			if err != nil {
				t.Errorf("fake assets API: reading part %q: %v", name, err)
				http.Error(w, "bad multipart part", http.StatusBadRequest)
				return
			}
			if name == "file" {
				rec.fileName = part.FileName()
				rec.fileBytes = data
				continue
			}
			rec.fieldOrder = append(rec.fieldOrder, name)
			rec.fields[name] = string(data)
		}

		mu.Lock()
		received = append(received, rec)
		mu.Unlock()

		status, body := respond(rec)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))

	return srv, func() []fppConnectRegisterRequest {
		mu.Lock()
		defer mu.Unlock()
		out := make([]fppConnectRegisterRequest, len(received))
		copy(out, received)
		return out
	}
}

// assetResponseBody builds a minimal POST /api/v1/assets 200 body
// (api/openapi.yaml's AssetResponse/Asset schemas), narrowed to the fields
// this seam reads.
func assetResponseBody(t *testing.T, id, contentHash string, rolledBack bool) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"serverTime": time.Now().UTC().Format(time.RFC3339),
		"asset": map[string]any{
			"id":          id,
			"contentHash": contentHash,
		},
		"rolledBack": rolledBack,
	})
	if err != nil {
		t.Fatalf("encoding a fake AssetResponse: %v", err)
	}
	return string(body)
}

// problemBody builds a minimal RFC 9457 application/problem+json body
// (api/openapi.yaml's Problem schema), narrowed to the fields this seam
// records verbatim.
func problemBody(t *testing.T, problemType, detail string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"type":       problemType,
		"detail":     detail,
		"serverTime": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("encoding a fake Problem: %v", err)
	}
	return string(body)
}

// fppConnectTestBackoff and fppConnectTestMaxBackoff are a millisecond-scale
// stand-in for the real 10s initial / 5m max backoff (review round 5
// finding 5): registerLoop's backoff is a field on the registrar
// specifically so a test can substitute this schedule on the one registrar
// that needs it (via reg.initialBackoff/reg.maxBackoff, after construction)
// rather than every registrar newTestFPPConnectRegistrar builds, which
// would otherwise make every OTHER test's own background retry loop far
// more active than its own timing assumptions expect. fppConnectWaitPastBackoff
// sleeps comfortably past several of these fast intervals (10+20+40+80ms,
// capped at fppConnectTestMaxBackoff, well past "at least two").
const (
	fppConnectTestBackoff    = 10 * time.Millisecond
	fppConnectTestMaxBackoff = 100 * time.Millisecond
)

func fppConnectWaitPastBackoff() { time.Sleep(250 * time.Millisecond) }

// newTestFPPConnectRegistrar wires a registrar over held whose retry loops
// share ctx (canceled automatically at test cleanup) and whose inventory
// trigger signals the returned channel, matching agent.go's own wiring of
// assetFetchTrigger. The push callback mirrors agent.go's own composition
// exactly (RebindPendingShowIDs before Wake), so a test simulating a push
// via state.Apply/state.notifyPush exercises the identical rebind path a
// real "fppconnect.configure" push does (review round 3 finding 2). The
// returned registrar keeps the real production backoff schedule; a test
// that needs the fast one sets reg.initialBackoff/reg.maxBackoff itself
// (fppConnectTestBackoff/fppConnectTestMaxBackoff) right after this call.
func newTestFPPConnectRegistrar(t *testing.T, held *fppConnectHeldStore, coordinatorBaseURL, token string) (*fppConnectRegistrar, *fppConnectState, chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	state := newFPPConnectState()
	state.SetCoordinatorBaseURL(coordinatorBaseURL)

	trigger := make(chan struct{}, 1)
	reg := newFPPConnectRegistrar(ctx, held, state, "node-1", token, func() {
		select {
		case trigger <- struct{}{}:
		default:
		}
	}, time.Now, discardLogger())
	held.SetOnHeld(reg.OnHeld)
	state.SetOnPush(func() {
		held.RebindPendingShowIDs(state.ShowID)
		reg.Wake()
	})
	return reg, state, trigger
}

// uploadAndBind uploads data as a single chunk to dir/name against a fresh
// xLights-facing test listener over held, with an already-known active
// show so the file binds on completion (FC2's own established pattern,
// see TestFPPConnectUploadActiveShowFallback).
func uploadAndBind(t *testing.T, held *fppConnectHeldStore, dir, name string, data []byte) fppConnectHeldRecord {
	t.Helper()
	view := fakeFPPConnectView{
		enabled: true, activeShowName: "Halloween", activeShowKnown: true, activeShowEver: true,
		shows: []fppConnectShowIDName{{ID: "halloween-2026", Name: "Halloween"}},
	}
	srv := startFPPConnectTestServer(t, view, "node-1", held)
	defer srv.Close()

	if resp, body := patchChunk(t, srv, dir, name, 0, int64(len(data)), data); resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: status = %d, body=%s", resp.StatusCode, body)
	}
	rec, ok := findHeldRecord(t, held, dir, name)
	if !ok {
		t.Fatalf("no held record for %s/%s after upload", dir, name)
	}
	if !rec.Bound {
		t.Fatalf("held record for %s/%s is not bound: %+v", dir, name, rec)
	}
	return rec
}

// waitForRegistrationState polls held for dir/name's record until its
// RegistrationState equals want or a short deadline passes.
func waitForRegistrationState(t *testing.T, held *fppConnectHeldStore, dir, name, want string) fppConnectHeldRecord {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last fppConnectHeldRecord
	var lastOK bool
	for time.Now().Before(deadline) {
		rec, ok := findHeldRecord(t, held, dir, name)
		last, lastOK = rec, ok
		if ok && rec.RegistrationState == want {
			return rec
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s/%s RegistrationState = %q; last observed = %+v (found=%v)", dir, name, want, last, lastOK)
	return fppConnectHeldRecord{}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestFPPConnectRegisterCompletedBoundFileRegisters is this seam's headline
// test: a completed, bound upload produces exactly one POST
// /api/v1/assets with the required field order, the bearer token, and a
// file part matching the held bytes; on 200 the record carries the asset
// id and the inventory trigger fires exactly once.
func TestFPPConnectRegisterCompletedBoundFileRegisters(t *testing.T) {
	held, _ := newTestHeldStore(t)

	data := bytes.Repeat([]byte("F"), 40)
	wantHash := sha256Hex(data)

	fakeSrv, requests := newFPPConnectRegisterFake(t, func(fppConnectRegisterRequest) (int, string) {
		return http.StatusOK, assetResponseBody(t, "asset-1", wantHash, false)
	})
	defer fakeSrv.Close()

	_, _, trigger := newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "test-token")

	uploadAndBind(t, held, "sequences", "Test.fseq", data)

	rec := waitForRegistrationState(t, held, "sequences", "Test.fseq", fppConnectRegistrationRegistered)
	if rec.RegistrationAssetID != "asset-1" {
		t.Fatalf("RegistrationAssetID = %q, want asset-1", rec.RegistrationAssetID)
	}

	reqs := requests()
	if len(reqs) != 1 {
		t.Fatalf("registration requests = %d, want exactly 1: %+v", len(reqs), reqs)
	}
	req := reqs[0]
	wantOrder := []string{"show", "sequence", "mediaType", "targetKind", "target"}
	if !equalStringSlices(req.fieldOrder, wantOrder) {
		t.Fatalf("field order = %v, want %v", req.fieldOrder, wantOrder)
	}
	if req.fields["show"] != "halloween-2026" || req.fields["sequence"] != "test" ||
		req.fields["mediaType"] != "fseq" || req.fields["targetKind"] != "node" || req.fields["target"] != "node-1" {
		t.Fatalf("fields = %+v, want show=halloween-2026 sequence=test mediaType=fseq targetKind=node target=node-1", req.fields)
	}
	if req.auth != "Bearer test-token" {
		t.Fatalf("Authorization = %q, want %q", req.auth, "Bearer test-token")
	}
	if !bytes.Equal(req.fileBytes, data) {
		t.Fatal("file part bytes do not match the held file")
	}

	select {
	case <-trigger:
	default:
		t.Fatal("inventory trigger did not fire after a successful registration")
	}
	select {
	case <-trigger:
		t.Fatal("inventory trigger fired more than once")
	default:
	}
}

// TestFPPConnectRegisterUnboundFileProducesNoRequest proves an unbound held
// file (no active show ever pushed) never reaches the registration
// endpoint at all, and its RegistrationState stays empty rather than
// "pending" (ADR-030 decision 5 extended: an unresolved binding registers
// nothing and must never read as awaiting registration).
func TestFPPConnectRegisterUnboundFileProducesNoRequest(t *testing.T) {
	held, _ := newTestHeldStore(t)

	var requestCount int32
	fakeSrv, _ := newFPPConnectRegisterFake(t, func(fppConnectRegisterRequest) (int, string) {
		atomic.AddInt32(&requestCount, 1)
		return http.StatusOK, "{}"
	})
	defer fakeSrv.Close()

	newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "")

	view := fakeFPPConnectView{enabled: true} // never pushed an active show
	srv := startFPPConnectTestServer(t, view, "node-1", held)
	defer srv.Close()
	if resp, body := patchChunk(t, srv, "sequences", "Orphan.fseq", 0, 3, []byte("abc")); resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: status = %d, body=%s", resp.StatusCode, body)
	}
	rec, ok := findHeldRecord(t, held, "sequences", "Orphan.fseq")
	if !ok || rec.Bound {
		t.Fatalf("expected an unbound held record, got %+v (found=%v)", rec, ok)
	}

	time.Sleep(100 * time.Millisecond)
	if rec, ok = findHeldRecord(t, held, "sequences", "Orphan.fseq"); !ok || rec.RegistrationState != "" {
		t.Fatalf("RegistrationState = %q, want empty for an unbound file", rec.RegistrationState)
	}
	if got := atomic.LoadInt32(&requestCount); got != 0 {
		t.Fatalf("registration requests = %d, want 0 for an unbound file", got)
	}
}

// TestFPPConnectRegisterPartialUploadProducesNoRequest proves an
// interrupted upload (FC2's own case, ADR-030 decision 5) produces no held
// record at all, so nothing ever reaches the registration endpoint.
func TestFPPConnectRegisterPartialUploadProducesNoRequest(t *testing.T) {
	held, _ := newTestHeldStore(t)

	var requestCount int32
	fakeSrv, _ := newFPPConnectRegisterFake(t, func(fppConnectRegisterRequest) (int, string) {
		atomic.AddInt32(&requestCount, 1)
		return http.StatusOK, "{}"
	})
	defer fakeSrv.Close()

	newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "")

	view := fakeFPPConnectView{enabled: true, activeShowName: "Halloween", activeShowKnown: true, activeShowEver: true}
	srv := startFPPConnectTestServer(t, view, "node-1", held)
	defer srv.Close()

	data := bytes.Repeat([]byte("X"), 30)
	if resp, body := patchChunk(t, srv, "sequences", "Partial.fseq", 0, 30, data[0:10]); resp.StatusCode != http.StatusOK {
		t.Fatalf("chunk 1: status = %d, body=%s", resp.StatusCode, body)
	}
	// Chunk 2 and 3 are never sent: the upload stops here.

	if _, ok := findHeldRecord(t, held, "sequences", "Partial.fseq"); ok {
		t.Fatal("a held record exists for an interrupted upload, want none")
	}
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&requestCount); got != 0 {
		t.Fatalf("registration requests = %d, want 0 for a partial upload", got)
	}
}

// TestFPPConnectRegisterRetriesAfterCoordinatorComesUp proves a coordinator
// that is unreachable at upload time leaves the record pending (never
// deleting the held file), and that the retry loop, woken explicitly
// rather than waiting out its backoff, registers successfully once the
// coordinator comes up on the same address.
func TestFPPConnectRegisterRetriesAfterCoordinatorComesUp(t *testing.T) {
	held, _ := newTestHeldStore(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving an address: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("releasing the reserved address: %v", err)
	}
	baseURL := "http://" + addr

	reg, _, _ := newTestFPPConnectRegistrar(t, held, baseURL, "")

	data := bytes.Repeat([]byte("D"), 20)
	uploadAndBind(t, held, "sequences", "Down.fseq", data)

	pending := waitForRegistrationState(t, held, "sequences", "Down.fseq", fppConnectRegistrationPending)
	if pending.RegistrationReason == "" {
		t.Fatal("RegistrationReason is empty while pending, want the transport error")
	}
	if _, err := os.Stat(held.HeldFilePath("sequences", "Down.fseq")); err != nil {
		t.Fatalf("held file missing while pending: %v", err)
	}

	wantHash := pending.ContentHash
	ln2, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("re-binding the reserved address: %v", err)
	}
	fakeSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(assetResponseBody(t, "asset-down", wantHash, false)))
	})}
	go func() { _ = fakeSrv.Serve(ln2) }()
	t.Cleanup(func() { _ = fakeSrv.Close() })

	reg.Wake()
	waitForRegistrationState(t, held, "sequences", "Down.fseq", fppConnectRegistrationRegistered)
}

// TestFPPConnectRegisterEmptyBaseURLPendingUntilConfigured proves an empty
// coordinatorBaseUrl (never pushed, or pushed empty) leaves the record
// pending with a "not configured" reason and sends no request at all, and
// that a fresh "fppconnect.configure" push carrying a real URL (simulated
// here exactly as fppconnectops.go's configure operation does it:
// SetCoordinatorBaseURL then notifyPush) wakes the retry loop immediately.
func TestFPPConnectRegisterEmptyBaseURLPendingUntilConfigured(t *testing.T) {
	held, _ := newTestHeldStore(t)

	data := bytes.Repeat([]byte("E"), 12)
	wantHash := sha256Hex(data)

	fakeSrv, requests := newFPPConnectRegisterFake(t, func(fppConnectRegisterRequest) (int, string) {
		return http.StatusOK, assetResponseBody(t, "asset-2", wantHash, false)
	})
	defer fakeSrv.Close()

	_, state, _ := newTestFPPConnectRegistrar(t, held, "", "") // no coordinatorBaseUrl yet

	uploadAndBind(t, held, "sequences", "NoURL.fseq", data)

	pending := waitForRegistrationState(t, held, "sequences", "NoURL.fseq", fppConnectRegistrationPending)
	if !strings.Contains(pending.RegistrationReason, "not configured") {
		t.Fatalf("RegistrationReason = %q, want it to say the coordinator base URL is not configured", pending.RegistrationReason)
	}
	if got := len(requests()); got != 0 {
		t.Fatalf("registration requests = %d, want 0 while no coordinator base URL is configured", got)
	}

	state.SetCoordinatorBaseURL(fakeSrv.URL)
	state.notifyPush()

	waitForRegistrationState(t, held, "sequences", "NoURL.fseq", fppConnectRegistrationRegistered)
}

// TestFPPConnectRegister400And403AreTerminal proves a 400 or 403 is
// recorded as a failure, verbatim, and never retried. Uses
// fppConnectTestBackoff/fppConnectTestMaxBackoff on this test's own
// registrar (review round 5 finding 5): the real 10s initial backoff made
// the "never retried" assertion below pass regardless of whether the code
// under test actually classified the status as terminal, since no retry
// could fire within any sleep this test could afford either way.
func TestFPPConnectRegister400And403AreTerminal(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		problemType string
		detail      string
	}{
		{"400", http.StatusBadRequest, "invalid-parameter", "sequence is required"},
		{"401", http.StatusUnauthorized, "unauthorized", "no valid credential"},
		{"403", http.StatusForbidden, "forbidden", "missing asset:write"},
		{"405", http.StatusMethodNotAllowed, "method-not-allowed", "POST not accepted here"},
		{"413", http.StatusRequestEntityTooLarge, "payload-too-large", "exceeds the coordinator's upload size bound"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			held, _ := newTestHeldStore(t)

			var requestCount int32
			fakeSrv, _ := newFPPConnectRegisterFake(t, func(fppConnectRegisterRequest) (int, string) {
				atomic.AddInt32(&requestCount, 1)
				return tc.status, problemBody(t, tc.problemType, tc.detail)
			})
			defer fakeSrv.Close()

			reg, _, _ := newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "")
			reg.initialBackoff = fppConnectTestBackoff
			reg.maxBackoff = fppConnectTestMaxBackoff

			uploadAndBind(t, held, "sequences", "Bad.fseq", []byte("payload"))

			failed := waitForRegistrationState(t, held, "sequences", "Bad.fseq", fppConnectRegistrationFailed)
			if failed.RegistrationProblemType != tc.problemType || failed.RegistrationReason != tc.detail {
				t.Fatalf("failure = type=%q reason=%q, want type=%q reason=%q",
					failed.RegistrationProblemType, failed.RegistrationReason, tc.problemType, tc.detail)
			}

			fppConnectWaitPastBackoff()
			if got := atomic.LoadInt32(&requestCount); got != 1 {
				t.Fatalf("registration requests = %d, want exactly 1 (a %d must never be retried)", got, tc.status)
			}
		})
	}
}

// TestFPPConnectRegister5xxAndStorageFullAreRetried proves a 5xx and a 507
// are both retryable: the first attempt fails, the record goes pending,
// and a woken retry succeeds.
func TestFPPConnectRegister5xxAndStorageFullAreRetried(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusInternalServerError, http.StatusInsufficientStorage} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			held, _ := newTestHeldStore(t)

			data := []byte("retry-me")
			wantHash := sha256Hex(data)

			var attempt int32
			fakeSrv, _ := newFPPConnectRegisterFake(t, func(fppConnectRegisterRequest) (int, string) {
				if atomic.AddInt32(&attempt, 1) == 1 {
					return status, ""
				}
				return http.StatusOK, assetResponseBody(t, "asset-3", wantHash, false)
			})
			defer fakeSrv.Close()

			reg, _, _ := newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "")

			uploadAndBind(t, held, "sequences", "Flaky.fseq", data)

			waitForRegistrationState(t, held, "sequences", "Flaky.fseq", fppConnectRegistrationPending)
			reg.Wake()
			waitForRegistrationState(t, held, "sequences", "Flaky.fseq", fppConnectRegistrationRegistered)

			if got := atomic.LoadInt32(&attempt); got != 2 {
				t.Fatalf("attempts = %d, want 2 (one %d, one success after Wake)", got, status)
			}
		})
	}
}

// TestFPPConnectRegisterSequenceFieldIsSlugified proves the sent
// "sequence" field is the slugified stem (review round 1 finding 2), not
// the raw file name, for a name containing spaces, underscores, and
// uppercase, and that the same value is visible on the held record.
func TestFPPConnectRegisterSequenceFieldIsSlugified(t *testing.T) {
	held, _ := newTestHeldStore(t)
	data := []byte("slug-me")
	wantHash := sha256Hex(data)

	fakeSrv, requests := newFPPConnectRegisterFake(t, func(fppConnectRegisterRequest) (int, string) {
		return http.StatusOK, assetResponseBody(t, "asset-slug", wantHash, false)
	})
	defer fakeSrv.Close()

	newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "")

	uploadAndBind(t, held, "sequences", "Halloween Spooky_Scene.fseq", data)

	rec := waitForRegistrationState(t, held, "sequences", "Halloween Spooky_Scene.fseq", fppConnectRegistrationRegistered)
	if rec.LogicalSequence != "halloween-spooky-scene" {
		t.Fatalf("held record LogicalSequence = %q, want halloween-spooky-scene", rec.LogicalSequence)
	}

	reqs := requests()
	if len(reqs) != 1 {
		t.Fatalf("registration requests = %d, want exactly 1", len(reqs))
	}
	if got := reqs[0].fields["sequence"]; got != "halloween-spooky-scene" {
		t.Fatalf("sequence field = %q, want halloween-spooky-scene", got)
	}
}

// TestFPPConnectRegisterEmptySlugFailsWithoutRequest proves a name whose
// stem slugifies to "" (review round 1 finding 2) is recorded as a failure
// with no registration request ever sent: no coordinatorBaseUrl or retry
// would ever make an empty sequence valid.
func TestFPPConnectRegisterEmptySlugFailsWithoutRequest(t *testing.T) {
	held, _ := newTestHeldStore(t)

	var requestCount int32
	fakeSrv, _ := newFPPConnectRegisterFake(t, func(fppConnectRegisterRequest) (int, string) {
		atomic.AddInt32(&requestCount, 1)
		return http.StatusOK, "{}"
	})
	defer fakeSrv.Close()

	newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "")

	uploadAndBind(t, held, "sequences", "???.fseq", []byte("data"))

	failed := waitForRegistrationState(t, held, "sequences", "???.fseq", fppConnectRegistrationFailed)
	if !strings.Contains(failed.RegistrationReason, "sequence id") {
		t.Fatalf("RegistrationReason = %q, want it to mention the missing sequence id", failed.RegistrationReason)
	}
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&requestCount); got != 0 {
		t.Fatalf("registration requests = %d, want 0 for a name with no valid sequence characters", got)
	}
}

// TestFPPConnectRegisterDoublePlaylistPostRegistersOnce is review round 1
// finding 3's own regression test: xLights fires POST
// /api/playlist/{name} up to twice per target, re-firing FC2's OnHeld for
// an already-registered (or already in-flight) record. Binding the same
// file twice must produce exactly one registration request and exactly
// one inventory trigger, never two concurrent goroutines racing the same
// key.
func TestFPPConnectRegisterDoublePlaylistPostRegistersOnce(t *testing.T) {
	held, _ := newTestHeldStore(t)
	data := []byte("bind-twice")
	wantHash := sha256Hex(data)

	fakeSrv, requests := newFPPConnectRegisterFake(t, func(fppConnectRegisterRequest) (int, string) {
		return http.StatusOK, assetResponseBody(t, "asset-twice", wantHash, false)
	})
	defer fakeSrv.Close()

	_, _, trigger := newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "")

	view := fakeFPPConnectView{enabled: true, showNames: []string{"Halloween"}, shows: []fppConnectShowIDName{{ID: "halloween-2026", Name: "Halloween"}}}
	srv := startFPPConnectTestServer(t, view, "node-1", held)
	defer srv.Close()

	if resp, body := patchChunk(t, srv, "sequences", "Twice.fseq", 0, int64(len(data)), data); resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: status = %d, body=%s", resp.StatusCode, body)
	}

	postBody := []byte(`{"mainPlaylist":[{"sequenceName":"Twice.fseq"}]}`)
	// xLights' own "up to twice per target" playlist POST, fired back to
	// back with no delay: the second call re-fires OnHeld for a record
	// the first call's own registerLoop may still be mid-attempt on.
	for i := 0; i < 2; i++ {
		if resp, body := postPlaylist(t, srv, "Halloween", postBody); resp.StatusCode != http.StatusOK {
			t.Fatalf("POST %d: status = %d, body=%s", i, resp.StatusCode, body)
		}
	}

	waitForRegistrationState(t, held, "sequences", "Twice.fseq", fppConnectRegistrationRegistered)
	// Give any wrongly-started second goroutine a chance to also fire its
	// own request before asserting the count.
	time.Sleep(100 * time.Millisecond)

	reqs := requests()
	if len(reqs) != 1 {
		t.Fatalf("registration requests = %d, want exactly 1", len(reqs))
	}

	select {
	case <-trigger:
	default:
		t.Fatal("inventory trigger did not fire")
	}
	select {
	case <-trigger:
		t.Fatal("inventory trigger fired more than once")
	default:
	}
}

// TestFPPConnectRegisterBootWalkRetriesUnregistered proves the startup
// walk resumes a bound-but-unregistered record left over from a previous
// run (the coordinator was unreachable before this node last stopped),
// sends exactly one registration request, and the coordinator's own
// idempotent 200 leaves exactly one held record, now registered. A
// further boot walk against an already-registered record sends no further
// request: this seam's own boot rule leaves a registered record alone.
func TestFPPConnectRegisterBootWalkRetriesUnregistered(t *testing.T) {
	held, dir := newTestHeldStore(t)

	data := bytes.Repeat([]byte("R"), 16)
	wantHash := sha256Hex(data)

	var requestCount int32
	fakeSrv, _ := newFPPConnectRegisterFake(t, func(fppConnectRegisterRequest) (int, string) {
		atomic.AddInt32(&requestCount, 1)
		return http.StatusOK, assetResponseBody(t, "asset-4", wantHash, false)
	})
	defer fakeSrv.Close()

	// First "run": the coordinator base URL is not yet configured, so the
	// upload completes bound but stays unregistered.
	newTestFPPConnectRegistrar(t, held, "", "")
	uploadAndBind(t, held, "sequences", "Restarted.fseq", data)
	waitForRegistrationState(t, held, "sequences", "Restarted.fseq", fppConnectRegistrationPending)

	// "Restart": a fresh held store rooted at the same directory (loading
	// the persisted, still-unregistered record from disk), with the
	// coordinator now reachable.
	held2 := newFPPConnectHeldStore(dir, discardLogger())
	reg2, _, _ := newTestFPPConnectRegistrar(t, held2, fakeSrv.URL, "")
	reg2.BootWalk()

	waitForRegistrationState(t, held2, "sequences", "Restarted.fseq", fppConnectRegistrationRegistered)
	if got := atomic.LoadInt32(&requestCount); got != 1 {
		t.Fatalf("registration requests = %d, want exactly 1", got)
	}
	matches := 0
	for _, r := range held2.Held() {
		if r.Dir == "sequences" && r.Name == "Restarted.fseq" {
			matches++
		}
	}
	if matches != 1 {
		t.Fatalf("held records for Restarted.fseq = %d, want exactly 1", matches)
	}

	// A second restart's boot walk leaves an already-registered record
	// alone: no further request.
	held3 := newFPPConnectHeldStore(dir, discardLogger())
	reg3, _, _ := newTestFPPConnectRegistrar(t, held3, fakeSrv.URL, "")
	reg3.BootWalk()

	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&requestCount); got != 1 {
		t.Fatalf("registration requests after a second boot walk over an already-registered record = %d, want still 1", got)
	}
}

// TestFPPConnectRegisterContentHashMismatchIsFailure proves a coordinator
// response whose content hash does not match FC2's own is recorded as a
// failure, never trusted as a registration.
func TestFPPConnectRegisterContentHashMismatchIsFailure(t *testing.T) {
	held, _ := newTestHeldStore(t)

	fakeSrv, _ := newFPPConnectRegisterFake(t, func(fppConnectRegisterRequest) (int, string) {
		return http.StatusOK, assetResponseBody(t, "asset-5", "sha256:0000000000000000000000000000000000000000000000000000000000000000", false)
	})
	defer fakeSrv.Close()

	newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "")

	uploadAndBind(t, held, "sequences", "Mismatch.fseq", []byte("original-bytes"))

	failed := waitForRegistrationState(t, held, "sequences", "Mismatch.fseq", fppConnectRegistrationFailed)
	if !strings.Contains(failed.RegistrationReason, "content hash") {
		t.Fatalf("RegistrationReason = %q, want it to mention the content hash mismatch", failed.RegistrationReason)
	}
	if failed.RegistrationProblemType != "" {
		t.Fatalf("RegistrationProblemType = %q, want empty for a locally-detected mismatch", failed.RegistrationProblemType)
	}
}

// TestFPPConnectRegisterMusicFileIsSkipped proves a bound music/videos
// upload never reaches the registration endpoint: FC3 registers FSEQ
// content only, and the record says so.
func TestFPPConnectRegisterMusicFileIsSkipped(t *testing.T) {
	held, _ := newTestHeldStore(t)

	var requestCount int32
	fakeSrv, _ := newFPPConnectRegisterFake(t, func(fppConnectRegisterRequest) (int, string) {
		atomic.AddInt32(&requestCount, 1)
		return http.StatusOK, "{}"
	})
	defer fakeSrv.Close()

	newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "")

	uploadAndBind(t, held, "music", "Song.mp3", []byte("audio-bytes"))

	skipped := waitForRegistrationState(t, held, "music", "Song.mp3", fppConnectRegistrationSkipped)
	if !strings.Contains(skipped.RegistrationReason, "music") {
		t.Fatalf("RegistrationReason = %q, want it to name the music directory", skipped.RegistrationReason)
	}
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&requestCount); got != 0 {
		t.Fatalf("registration requests = %d, want 0 for a music upload", got)
	}
}

// TestFPPConnectRegistrarWaitJoinsRegisterLoops is review round 1 finding
// 4's own regression test, driving the real shutdown ordering review
// round 2 finding A fixed: agent.go used to spawn a goroutine that called
// fppConnectRegistrar.Wait right after BootWalk and joined that goroutine
// through its own done channel; since BootWalk found nothing to register
// on a fresh store, the WaitGroup's counter was already zero, so that
// early Wait returned immediately and its done channel closed during
// startup, before any upload ever completed, meaning shutdown never
// actually joined a single real registration. This uses two separate
// registrars, one per half of that sequence (review round 5 finding 3
// changed Wait into a one-shot operation: it permanently marks its own
// registrar closed to every future startLoop call, so the ORIGINAL
// single-registrar "Wait early, then bind, then Wait again" sequence this
// test used to drive is no longer a scenario any real registrar's own
// caller could hit twice; agent.go's own contract already said as much,
// "must be called directly in the shutdown sequence," and now the
// registrar enforces it). The first registrar proves a Wait call with
// nothing ever registered returns immediately, the harmless case that hid
// the bug; the second proves agent.go's own shutdown-path call, made
// after a real registerLoop against an unreachable coordinator has
// already started, blocks on that loop and unblocks only once its
// context is canceled.
func TestFPPConnectRegistrarWaitJoinsRegisterLoops(t *testing.T) {
	emptyHeld, _ := newTestHeldStore(t)
	earlyCtx, earlyCancel := context.WithCancel(context.Background())
	t.Cleanup(earlyCancel)
	earlyReg := newFPPConnectRegistrar(earlyCtx, emptyHeld, newFPPConnectState(), "node-1", "", func() {}, time.Now, discardLogger())

	earlyDone := make(chan struct{})
	go func() {
		earlyReg.Wait()
		close(earlyDone)
	}()
	select {
	case <-earlyDone:
	case <-time.After(2 * time.Second):
		t.Fatal("a Wait with nothing registered did not return promptly")
	}

	held, _ := newTestHeldStore(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving an address: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("releasing the reserved address: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	state := newFPPConnectState()
	state.SetCoordinatorBaseURL("http://" + addr)
	reg := newFPPConnectRegistrar(ctx, held, state, "node-1", "", func() {}, time.Now, discardLogger())
	held.SetOnHeld(reg.OnHeld)

	uploadAndBind(t, held, "sequences", "NeverUp.fseq", []byte("data"))
	waitForRegistrationState(t, held, "sequences", "NeverUp.fseq", fppConnectRegistrationPending)

	// This is agent.go's own shutdown-path call, made after the loop
	// above has already started: it must block on that loop, not return
	// immediately the way the buggy early goroutine's call did.
	waitDone := make(chan struct{})
	go func() {
		reg.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
		t.Fatal("Wait returned before the context was canceled, want it still blocked on the running retry loop")
	case <-time.After(50 * time.Millisecond):
	}

	cancel()

	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return within 2s of the context being canceled")
	}
}

// TestFPPConnectRegistrarStartLoopRefusesAfterWaitStarted is review round 5
// finding 3's own regression test: startLoop's r.ctx.Err() check and its
// wg.Add(1) used to be two separate, unguarded steps, so a late OnHeld (the
// "fppconnect.configure" push path, never gated by the HTTP listener's own
// shutdown, see startLoop's own doc comment) could pass the ctx check and
// then call wg.Add after Wait had already observed the WaitGroup's counter
// reach zero and returned, a documented sync.WaitGroup misuse. Wait's own
// doc comment states its closed flag is set, under the same lock startLoop
// now shares, before Wait ever calls wg.Wait: this test drives that exact
// state directly (there is nothing in flight yet for a real Wait call to
// usefully block on) and proves a late OnHeld arriving after it neither
// starts a registerLoop nor calls wg.Add at all.
func TestFPPConnectRegistrarStartLoopRefusesAfterWaitStarted(t *testing.T) {
	held, _ := newTestHeldStore(t)

	var requestCount int32
	fakeSrv, _ := newFPPConnectRegisterFake(t, func(fppConnectRegisterRequest) (int, string) {
		atomic.AddInt32(&requestCount, 1)
		return http.StatusOK, "{}"
	})
	defer fakeSrv.Close()

	reg, _, _ := newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "")

	reg.mu.Lock()
	reg.closed = true
	reg.mu.Unlock()

	// held.SetOnHeld(reg.OnHeld) is already wired by newTestFPPConnectRegistrar:
	// this upload's own completeLocked call fires OnHeld the same way a
	// late "fppconnect.configure" push's own call path would, after Wait
	// has already started.
	uploadAndBind(t, held, "sequences", "TooLate.fseq", []byte("late"))

	fppConnectWaitPastBackoff()
	if got := atomic.LoadInt32(&requestCount); got != 0 {
		t.Fatalf("registration requests = %d, want 0: a late OnHeld after Wait started must never register anything", got)
	}
	reg.mu.Lock()
	inFlight := len(reg.inFlight)
	reg.mu.Unlock()
	if inFlight != 0 {
		t.Fatalf("inFlight entries = %d after a late OnHeld post-Wait, want 0", inFlight)
	}

	// wg.Wait must return immediately: nothing was ever Add'ed for the
	// late record, so there is nothing left to join.
	done := make(chan struct{})
	go func() { reg.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("wg.Wait() did not return promptly; a late OnHeld must never call wg.Add")
	}
}

// TestFPPConnectRegistrarWaitBeforeRetryPausesOnSupersession is review
// round 6 finding 4's own regression test: every "superseded" continue
// path in registerLoop used to reset backoff and loop straight back to
// the top with no wait at all, so a record superseded in a tight loop
// (offset-0 re-uploads of a file this lane never registers, which
// supersede and skip without ever doing any I/O) would spin the retry
// goroutine hot, pegging a CPU core for as long as the superseding kept
// happening. waitBeforeRetry now pauses first: this proves a real pause
// elapses before it returns true, and that Wake() lets a caller skip the
// rest of that pause early, exactly like the normal backoff wait already
// does.
func TestFPPConnectRegistrarWaitBeforeRetryPausesOnSupersession(t *testing.T) {
	held, _ := newTestHeldStore(t)
	reg, _, _ := newTestFPPConnectRegistrar(t, held, "", "")

	start := time.Now()
	if !reg.waitBeforeRetry(reg.wakeChan()) {
		t.Fatal("waitBeforeRetry returned false with ctx still alive")
	}
	if elapsed := time.Since(start); elapsed < fppConnectSupersededPause {
		t.Fatalf("waitBeforeRetry returned after %v, want at least %v (no hot spin on the superseded continue path)", elapsed, fppConnectSupersededPause)
	}

	wakeStart := time.Now()
	done := make(chan bool, 1)
	go func() { done <- reg.waitBeforeRetry(reg.wakeChan()) }()
	time.Sleep(10 * time.Millisecond)
	reg.Wake()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("waitBeforeRetry returned false after a Wake(), want true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitBeforeRetry did not return after Wake()")
	}
	if woke := time.Since(wakeStart); woke >= fppConnectSupersededPause {
		t.Fatalf("waitBeforeRetry took %v after Wake(), want well under fppConnectSupersededPause (%v)", woke, fppConnectSupersededPause)
	}
}

// TestFPPConnectRegisterMalformedBaseURLDoesNotLeakTheWriterGoroutine is
// review round 1 finding 5's own regression test: when
// http.NewRequestWithContext fails (here, a coordinatorBaseUrl containing
// a raw control character), the multipart writer goroutine must never be
// started at all, rather than left blocked forever on a pipe nothing
// reads. Uses the identical runtime.NumGoroutine() baseline pattern
// internal/coordinator/collector's own leak regression test uses.
func TestFPPConnectRegisterMalformedBaseURLDoesNotLeakTheWriterGoroutine(t *testing.T) {
	held, _ := newTestHeldStore(t)
	newTestFPPConnectRegistrar(t, held, "http://exa\nmple.com", "")

	baseline := runtime.NumGoroutine()

	uploadAndBind(t, held, "sequences", "Malformed.fseq", []byte("data"))

	pending := waitForRegistrationState(t, held, "sequences", "Malformed.fseq", fppConnectRegistrationPending)
	if !strings.Contains(pending.RegistrationReason, "building registration request") {
		t.Fatalf("RegistrationReason = %q, want it to name the request-build failure", pending.RegistrationReason)
	}

	// baseline was captured before OnHeld started this record's own
	// long-lived retry loop goroutine (which stays alive, backed off,
	// until this test's registrar ctx is canceled at cleanup): +1 accounts
	// for exactly that one expected goroutine. A leaked writer goroutine
	// would show up as a second, unaccounted-for one.
	deadline := time.After(2 * time.Second)
	for {
		if runtime.NumGoroutine() <= baseline+1 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("goroutine count = %d after a malformed base URL, want close to baseline+1 %d (writer goroutine leak)", runtime.NumGoroutine(), baseline+1)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// TestFPPConnectRegisterSupersedingUploadDuringInFlightAttemptStillRegisters
// is review round 2 finding B's own regression test: a second upload of
// the same (dir, name) that completes WHILE the first upload's own
// registration attempt is already in flight must still end up registered.
// startLoop's in-flight guard refuses to start a second registerLoop for
// the same key while the first one owns it, so the ONLY way the second
// upload's bytes ever get registered is the first loop's own terminal
// branch treating its setter's false return (a content-hash mismatch
// against the now-superseding record) as "pick up the new record and
// keep going" rather than "give up."
func TestFPPConnectRegisterSupersedingUploadDuringInFlightAttemptStillRegisters(t *testing.T) {
	held, _ := newTestHeldStore(t)

	v1Data := []byte("version-one")
	v2Data := []byte("version-two-is-longer")
	v1Hash := sha256Hex(v1Data)
	v2Hash := sha256Hex(v2Data)

	v1RequestReceived := make(chan struct{})
	v2Bound := make(chan struct{})

	fakeSrv, requests := newFPPConnectRegisterFake(t, func(req fppConnectRegisterRequest) (int, string) {
		gotHash := sha256Hex(req.fileBytes)
		switch gotHash {
		case v1Hash:
			close(v1RequestReceived)
			<-v2Bound // hold this response until v2 has already superseded the record
			return http.StatusOK, assetResponseBody(t, "asset-v1", v1Hash, false)
		case v2Hash:
			return http.StatusOK, assetResponseBody(t, "asset-v2", v2Hash, false)
		default:
			t.Errorf("unexpected file bytes hash %q in a registration request", gotHash)
			return http.StatusInternalServerError, ""
		}
	})
	defer fakeSrv.Close()

	newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "")

	uploadAndBind(t, held, "sequences", "Race.fseq", v1Data)
	select {
	case <-v1RequestReceived:
	case <-time.After(2 * time.Second):
		t.Fatal("v1's registration request never reached the fake coordinator")
	}

	// v2 completes (and binds) while v1's own request is still blocked
	// server-side. startLoop's in-flight guard means this does NOT start
	// a second registerLoop for "sequences/Race.fseq": v1's own loop is
	// the only thing that can ever register these bytes now.
	uploadAndBind(t, held, "sequences", "Race.fseq", v2Data)
	close(v2Bound)

	rec := waitForRegistrationState(t, held, "sequences", "Race.fseq", fppConnectRegistrationRegistered)
	if rec.ContentHash != v2Hash || rec.RegistrationAssetID != "asset-v2" {
		t.Fatalf("record = %+v, want it registered as v2 (hash %q, asset asset-v2)", rec, v2Hash)
	}

	reqs := requests()
	if len(reqs) != 2 {
		t.Fatalf("registration requests = %d, want exactly 2 (v1's superseded attempt, then v2's)", len(reqs))
	}
}

// TestFPPConnectRegisterRebindDuringInFlightAttemptRegistersUnderNewIdentity
// is review round 6 finding 1's own regression test: a rebind (BindShow)
// that lands while a registration attempt is already in flight used to be
// lost. setRegistrationLocked guarded only on ContentHash, which a rebind
// never changes, so the in-flight attempt's own stale write would stamp
// "registered" on a record that had already moved to a different show,
// and no loop was left running to register it for real under the new
// identity. The fix carries the attempt's own (ShowID, LogicalSequence)
// into the setters, so that stale write is refused exactly like a
// content-hash mismatch already is, and registerLoop's own continue path
// re-attempts fresh under the new identity.
func TestFPPConnectRegisterRebindDuringInFlightAttemptRegistersUnderNewIdentity(t *testing.T) {
	held, _ := newTestHeldStore(t)

	aRequestReceived := make(chan struct{})
	rebindDone := make(chan struct{})

	fakeSrv, requests := newFPPConnectRegisterFake(t, func(req fppConnectRegisterRequest) (int, string) {
		hash := sha256Hex(req.fileBytes)
		if req.fields["show"] == "halloween-2026" {
			close(aRequestReceived)
			<-rebindDone // hold this response until the rebind has landed
		}
		return http.StatusOK, assetResponseBody(t, "asset-"+req.fields["show"], hash, false)
	})
	defer fakeSrv.Close()

	newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "")

	uploadAndBind(t, held, "sequences", "Rebind.fseq", []byte("rebind-me")) // binds to Halloween/halloween-2026

	select {
	case <-aRequestReceived:
	case <-time.After(2 * time.Second):
		t.Fatal("the in-flight request for show A never reached the fake coordinator")
	}

	// A rebind to a different show lands while the attempt above is still
	// in flight, waiting on the fake coordinator's response.
	held.BindShow("Christmas", "christmas-2026", []string{"Rebind.fseq"}, time.Now())
	close(rebindDone)

	registered := waitForRegistrationState(t, held, "sequences", "Rebind.fseq", fppConnectRegistrationRegistered)
	if registered.ShowID != "christmas-2026" || registered.RegistrationAssetID != "asset-christmas-2026" {
		t.Fatalf("record = %+v, want registered under christmas-2026", registered)
	}

	reqs := requests()
	if len(reqs) != 2 {
		t.Fatalf("registration requests = %d, want exactly 2 (the in-flight attempt for A, then B's)", len(reqs))
	}
	if reqs[0].fields["show"] != "halloween-2026" || reqs[1].fields["show"] != "christmas-2026" {
		t.Fatalf("request shows = %q, %q, want halloween-2026 then christmas-2026", reqs[0].fields["show"], reqs[1].fields["show"])
	}
}

// TestFPPConnectRegisterCollidingSlugsFailTheLaterOne is review round 2
// finding C's own regression test: two distinct file name stems that
// slugify to the identical sequence id under the same show ("My Show.fseq"
// and "my_show.fseq" both slug to "my-show") must never both register:
// the earlier-received one registers normally, and the later one fails
// with a reason naming the earlier file, without ever sending a request.
func TestFPPConnectRegisterCollidingSlugsFailTheLaterOne(t *testing.T) {
	held, _ := newTestHeldStore(t)

	fakeSrv, requests := newFPPConnectRegisterFake(t, func(req fppConnectRegisterRequest) (int, string) {
		hash := sha256Hex(req.fileBytes)
		return http.StatusOK, assetResponseBody(t, "asset-"+req.fields["sequence"], hash, false)
	})
	defer fakeSrv.Close()

	newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "")

	firstData := []byte("first-file")
	uploadAndBind(t, held, "sequences", "My Show.fseq", firstData)

	// A later, distinct ReceivedAt for the second (colliding) file.
	time.Sleep(5 * time.Millisecond)
	secondData := []byte("second-file-is-longer")
	uploadAndBind(t, held, "sequences", "my_show.fseq", secondData)

	registered := waitForRegistrationState(t, held, "sequences", "My Show.fseq", fppConnectRegistrationRegistered)
	failed := waitForRegistrationState(t, held, "sequences", "my_show.fseq", fppConnectRegistrationFailed)

	if registered.LogicalSequence != "my-show" || failed.LogicalSequence != "my-show" {
		t.Fatalf("LogicalSequence = %q / %q, want both my-show", registered.LogicalSequence, failed.LogicalSequence)
	}
	if !strings.Contains(failed.RegistrationReason, "My Show.fseq") {
		t.Fatalf("RegistrationReason = %q, want it to name the colliding file My Show.fseq", failed.RegistrationReason)
	}
	if failed.RegistrationProblemType != "" {
		t.Fatalf("RegistrationProblemType = %q, want empty for a locally-detected collision", failed.RegistrationProblemType)
	}

	reqs := requests()
	if len(reqs) != 1 {
		t.Fatalf("registration requests = %d, want exactly 1 (the colliding file must never even attempt)", len(reqs))
	}
}

// TestFPPConnectRegisterLateBindingNeverSupersedesAlreadyRegistered is
// review round 3 finding 1's own regression test: an unbound file (A) that
// completed uploading FIRST must not be able to supersede a colliding file
// (B) that bound and registered LATER but before A was ever bound. Binding
// order, not upload order, decides when a registration attempt starts, so
// comparing ReceivedAt alone let A's later binding win the collision check
// against an already-registered B.
func TestFPPConnectRegisterLateBindingNeverSupersedesAlreadyRegistered(t *testing.T) {
	held, _ := newTestHeldStore(t)

	fakeSrv, requests := newFPPConnectRegisterFake(t, func(req fppConnectRegisterRequest) (int, string) {
		hash := sha256Hex(req.fileBytes)
		return http.StatusOK, assetResponseBody(t, "asset-"+req.fields["sequence"], hash, false)
	})
	defer fakeSrv.Close()

	newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "")

	view := fakeFPPConnectView{
		enabled:   true,
		showNames: []string{"Halloween"},
		shows:     []fppConnectShowIDName{{ID: "halloween-2026", Name: "Halloween"}},
	}
	srv := startFPPConnectTestServer(t, view, "node-1", held)
	defer srv.Close()

	// A uploads FIRST (the earlier ReceivedAt) with no active show known
	// yet, so it completes unbound and never attempts registration.
	aData := []byte("a-file")
	if resp, body := patchChunk(t, srv, "sequences", "Same_Show.fseq", 0, int64(len(aData)), aData); resp.StatusCode != http.StatusOK {
		t.Fatalf("upload A: status = %d, body=%s", resp.StatusCode, body)
	}
	recA, ok := findHeldRecord(t, held, "sequences", "Same_Show.fseq")
	if !ok || recA.Bound {
		t.Fatalf("A = %+v (ok=%v), want held and unbound", recA, ok)
	}

	// B uploads and binds SECOND (a strictly later ReceivedAt), to the
	// same show, with a name that slugifies to the identical sequence id,
	// and registers successfully before A is ever bound.
	time.Sleep(5 * time.Millisecond)
	bData := []byte("b-file-is-longer-than-a")
	registeredB := uploadAndBind(t, held, "sequences", "Same-Show.fseq", bData)
	if registeredB.LogicalSequence != "same-show" {
		t.Fatalf("B LogicalSequence = %q, want same-show", registeredB.LogicalSequence)
	}
	registeredB = waitForRegistrationState(t, held, "sequences", "Same-Show.fseq", fppConnectRegistrationRegistered)

	// Only now does a playlist POST bind A, to the identical show: this is
	// A's own FIRST registration attempt, even though A's upload finished
	// before B's.
	postBody := []byte(`{"mainPlaylist":[{"sequenceName":"Same_Show.fseq"}]}`)
	if resp, body := postPlaylist(t, srv, "Halloween", postBody); resp.StatusCode != http.StatusOK {
		t.Fatalf("POST playlist: status = %d, body=%s", resp.StatusCode, body)
	}

	failedA := waitForRegistrationState(t, held, "sequences", "Same_Show.fseq", fppConnectRegistrationFailed)
	if !strings.Contains(failedA.RegistrationReason, "Same-Show.fseq") {
		t.Fatalf("A's RegistrationReason = %q, want it to name the already-registered file Same-Show.fseq", failedA.RegistrationReason)
	}

	// B must remain registered, unchanged: A's later attempt must never
	// have superseded it.
	finalB, ok := findHeldRecord(t, held, "sequences", "Same-Show.fseq")
	if !ok || finalB.RegistrationState != fppConnectRegistrationRegistered || finalB.RegistrationAssetID != registeredB.RegistrationAssetID {
		t.Fatalf("B = %+v (ok=%v), want unchanged, still registered as %q", finalB, ok, registeredB.RegistrationAssetID)
	}

	reqs := requests()
	if len(reqs) != 1 {
		t.Fatalf("registration requests = %d, want exactly 1 (A must never attempt once B already owns the identity)", len(reqs))
	}
}

// TestFPPConnectRegisterLoserRetriesAfterWinnerFailsTerminally is review
// round 5 finding 2's own regression test: claimIdentity's identityOwner
// cache used to be permanent for the registrar's whole life, so once A won
// a slug collision against B, A's own key stayed the recorded owner even
// after A itself later failed terminally for a reason that has nothing to
// do with the collision (its own non-retryable coordinator refusal) and so
// will never attempt registration again. B, the loser, stayed "failed"
// forever: even a fresh re-upload giving B a real second chance still lost
// to a dead owner that could never actually win the identity for real. The
// fix clears a cached owner the moment it no longer matches what the held
// store currently reports for that identity, so B's fresh attempt here
// correctly wins once A is confirmed terminally failed.
func TestFPPConnectRegisterLoserRetriesAfterWinnerFailsTerminally(t *testing.T) {
	held, _ := newTestHeldStore(t)

	var attempt int32
	fakeSrv, requests := newFPPConnectRegisterFake(t, func(req fppConnectRegisterRequest) (int, string) {
		if atomic.AddInt32(&attempt, 1) == 1 {
			// A's own first (and only) attempt: a non-retryable refusal
			// unrelated to the collision.
			return http.StatusBadRequest, problemBody(t, "invalid-parameter", "synthetic failure")
		}
		hash := sha256Hex(req.fileBytes)
		return http.StatusOK, assetResponseBody(t, "asset-b", hash, false)
	})
	defer fakeSrv.Close()

	newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "")

	uploadAndBind(t, held, "sequences", "My Show.fseq", []byte("first-file"))
	failedA := waitForRegistrationState(t, held, "sequences", "My Show.fseq", fppConnectRegistrationFailed)
	if failedA.RegistrationProblemType != "invalid-parameter" {
		t.Fatalf("A's failure = %+v, want the synthetic 400 refusal, not a collision failure", failedA)
	}

	// Only now, with A confirmed terminally failed, does B (the colliding
	// slug) upload and bind.
	time.Sleep(5 * time.Millisecond)
	uploadAndBind(t, held, "sequences", "my_show.fseq", []byte("second-file-is-longer"))

	registeredB := waitForRegistrationState(t, held, "sequences", "my_show.fseq", fppConnectRegistrationRegistered)
	if registeredB.RegistrationAssetID != "asset-b" {
		t.Fatalf("B's record = %+v, want it to register once A's terminal failure released the identity", registeredB)
	}

	reqs := requests()
	if len(reqs) != 2 {
		t.Fatalf("registration requests = %d, want exactly 2 (A's own failed attempt, then B's)", len(reqs))
	}
}

// TestFPPConnectRegisterReadyFileStaysPendingBehindAwaitingCompetitor is
// review round 6 finding 3's own regression test: a ready, bound file that
// loses a slug collision to a competitor that is only awaiting its own
// show id used to be marked terminally failed; if that competitor never
// actually bound, nobody would ever register the sequence at all. It must
// instead stay pending, naming the competitor, and be retried on the
// normal wake/backoff cycle, winning once the competitor's show id is
// confirmed resolvable but it still has not bound.
func TestFPPConnectRegisterReadyFileStaysPendingBehindAwaitingCompetitor(t *testing.T) {
	held, _ := newTestHeldStore(t)

	fakeSrv, requests := newFPPConnectRegisterFake(t, func(req fppConnectRegisterRequest) (int, string) {
		hash := sha256Hex(req.fileBytes)
		return http.StatusOK, assetResponseBody(t, "asset-ready", hash, false)
	})
	defer fakeSrv.Close()

	reg, state, _ := newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "")
	reg.initialBackoff = fppConnectTestBackoff
	reg.maxBackoff = fppConnectTestMaxBackoff

	// The competitor uploads (an EARLIER ReceivedAt than the ready file
	// below) and lands held, then is put directly into the awaiting-id
	// state BindPendingShowID itself produces, with the show's id not yet
	// resolvable through state at all.
	competitorSrv := startFPPConnectTestServer(t, fakeFPPConnectView{enabled: true}, "node-1", held)
	if resp, body := patchChunk(t, competitorSrv, "sequences", "My Show.fseq", 0, 3, []byte("abc")); resp.StatusCode != http.StatusOK {
		competitorSrv.Close()
		t.Fatalf("upload competitor: status = %d, body=%s", resp.StatusCode, body)
	}
	competitorSrv.Close()
	held.BindPendingShowID("Halloween", []string{"My Show.fseq"}, time.Now())
	competitor, ok := findHeldRecord(t, held, "sequences", "My Show.fseq")
	if !ok || competitor.Bound || competitor.UnboundReason != fppConnectUnboundReasonShowIDNotPushed {
		t.Fatalf("competitor = %+v (ok=%v), want held, unbound, awaiting the show id", competitor, ok)
	}

	// The ready file is bound directly, to the identical show (a manually
	// supplied ShowID, decoupled from state, which has not resolved
	// "Halloween" at all yet) and a colliding slug, uploaded after the
	// competitor so it would lose any ReceivedAt tie-break.
	time.Sleep(5 * time.Millisecond)
	readySrv := startFPPConnectTestServer(t, fakeFPPConnectView{enabled: true}, "node-1", held)
	if resp, body := patchChunk(t, readySrv, "sequences", "my_show.fseq", 0, 4, []byte("late")); resp.StatusCode != http.StatusOK {
		readySrv.Close()
		t.Fatalf("upload ready file: status = %d, body=%s", resp.StatusCode, body)
	}
	readySrv.Close()
	held.BindShow("Halloween", "halloween-2026", []string{"my_show.fseq"}, time.Now())

	pending := waitForRegistrationState(t, held, "sequences", "my_show.fseq", fppConnectRegistrationPending)
	if !strings.Contains(pending.RegistrationReason, "My Show.fseq") {
		t.Fatalf("RegistrationReason = %q, want it to name the still-awaiting competitor My Show.fseq", pending.RegistrationReason)
	}
	if got := len(requests()); got != 0 {
		t.Fatalf("registration requests = %d, want 0 while genuinely blocked on an unresolvable competitor", got)
	}

	// The show's id becomes resolvable, but nothing walks the
	// competitor's own record to actually bind it (notifyPush, the only
	// thing that would trigger RebindPendingShowIDs, is never called):
	// the ready file must win outright rather than wait on it forever.
	state.SetShows([]fppConnectShowIDName{{ID: "halloween-2026", Name: "Halloween"}})
	reg.Wake()

	registered := waitForRegistrationState(t, held, "sequences", "my_show.fseq", fppConnectRegistrationRegistered)
	if registered.RegistrationAssetID != "asset-ready" {
		t.Fatalf("record = %+v, want it registered once the competitor's id was confirmed resolvable but still unbound", registered)
	}

	if got := len(requests()); got != 1 {
		t.Fatalf("registration requests = %d, want exactly 1", got)
	}
}

// TestFPPConnectRegisterActiveShowKnownButIDNotPushedRebindsOnLaterPush is
// review round 3 finding 2's own regression test: an active show whose
// display name is already known (ShowNames) but whose config object id
// has not been pushed yet must be held unbound with
// fppConnectUnboundReasonShowIDNotPushed and Show set to the show's name,
// not the old generic "does not currently resolve" reason with Show left
// empty, which RebindPendingShowIDs could never match. A later push that
// carries the show's id must then rebind the record automatically, and
// registration must follow with no operator action required.
func TestFPPConnectRegisterActiveShowKnownButIDNotPushedRebindsOnLaterPush(t *testing.T) {
	held, _ := newTestHeldStore(t)

	fakeSrv, requests := newFPPConnectRegisterFake(t, func(req fppConnectRegisterRequest) (int, string) {
		hash := sha256Hex(req.fileBytes)
		return http.StatusOK, assetResponseBody(t, "asset-late-show-id", hash, false)
	})
	defer fakeSrv.Close()

	_, state, _ := newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "")

	view := fakeFPPConnectView{
		enabled: true, activeShowName: "Halloween", activeShowKnown: true, activeShowEver: true,
		showNames: []string{"Halloween"},
	}
	srv := startFPPConnectTestServer(t, view, "node-1", held)
	defer srv.Close()

	data := []byte("late-show-id")
	if resp, body := patchChunk(t, srv, "sequences", "Late.fseq", 0, int64(len(data)), data); resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: status = %d, body=%s", resp.StatusCode, body)
	}

	rec, ok := findHeldRecord(t, held, "sequences", "Late.fseq")
	if !ok {
		t.Fatal("no held record")
	}
	if rec.Bound {
		t.Fatalf("record = %+v, want held unbound: the show's id has not been pushed yet", rec)
	}
	if rec.Show != "Halloween" {
		t.Fatalf("Show = %q, want Halloween, so a later push can rebind it", rec.Show)
	}
	if rec.UnboundReason != fppConnectUnboundReasonShowIDNotPushed {
		t.Fatalf("UnboundReason = %q, want %q", rec.UnboundReason, fppConnectUnboundReasonShowIDNotPushed)
	}

	// A later push carries the show's id, exactly as fppconnectops.go's
	// configure operation applies one: Apply then notifyPush.
	snap := state.Snapshot()
	snap.ShowNames = []string{"Halloween"}
	snap.Shows = []fppConnectShowIDName{{ID: "halloween-2026", Name: "Halloween"}}
	state.Apply(snap)
	state.notifyPush()

	registered := waitForRegistrationState(t, held, "sequences", "Late.fseq", fppConnectRegistrationRegistered)
	if registered.ShowID != "halloween-2026" || registered.LogicalSequence != "late" {
		t.Fatalf("registered record = %+v, want ShowID halloween-2026 and LogicalSequence late", registered)
	}

	reqs := requests()
	if len(reqs) != 1 {
		t.Fatalf("registration requests = %d, want exactly 1", len(reqs))
	}
	if reqs[0].fields["show"] != "halloween-2026" {
		t.Fatalf("request show field = %q, want halloween-2026", reqs[0].fields["show"])
	}
}

// TestFPPConnectRegisterCollidingAwaitingShowIDCompetesFairly is review
// round 5 finding 4's own regression test: BindPendingShowID temporarily
// unbinds a record awaiting its show's config object id
// (fppConnectUnboundReasonShowIDNotPushed), and CollidingRecord used to
// skip every unbound record outright, making that record invisible to a
// colliding file's own collision check for as long as the wait lasted.
// The fix keeps LogicalSequence on a record awaiting its show id and has
// CollidingRecord match it by intended show name, so a competitor
// attempting while the other waits still finds it. Review round 6 finding
// 3 refined what happens next: a competitor merely awaiting its own show
// id must never permanently block a ready file, since if that competitor
// never actually binds, nobody would ever register the sequence at all.
// Here the show's id is always independently confirmed resolvable
// (state.SetShows, below) before the attempting file's own bind can ever
// succeed at all, since both files share the identical show name; the
// awaiting file's own record is deliberately never rebound (notifyPush,
// the only thing that would trigger RebindPendingShowIDs, is never
// called), so the attempting file always finds a competitor whose id is
// confirmed resolvable but who still has not bound, and must win outright
// rather than wait on it forever. This drives the interleaving both ways:
// the awaiting file can have EITHER the earlier or the later ReceivedAt,
// and either way the attempting file ends up registered.
func TestFPPConnectRegisterCollidingAwaitingShowIDCompetesFairly(t *testing.T) {
	// run uploads earlierFile then laterFile (in that order, so
	// earlierFile always has the earlier ReceivedAt), binds awaitingFile's
	// playlist POST first, while the show's id has not been pushed yet
	// (BindPendingShowID leaves it held, unbound, awaiting that id), THEN
	// makes the id resolvable and binds the OTHER file's playlist,
	// triggering ITS registration attempt against a competitor that is
	// still sitting in the awaiting-id state.
	run := func(t *testing.T, earlierFile, laterFile, awaitingFile string) {
		t.Helper()
		held, _ := newTestHeldStore(t)

		fakeSrv, requests := newFPPConnectRegisterFake(t, func(req fppConnectRegisterRequest) (int, string) {
			hash := sha256Hex(req.fileBytes)
			return http.StatusOK, assetResponseBody(t, "asset-"+req.fields["sequence"], hash, false)
		})
		defer fakeSrv.Close()

		_, state, _ := newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "")
		srv := startFPPConnectTestServer(t, newFPPConnectStateView(state), "node-1", held)
		defer srv.Close()

		// The show's name is known, but not yet its config object id: a
		// playlist POST naming either file resolves to "known, id not
		// pushed" (fppConnectUnboundReasonShowIDNotPushed), never
		// "unknown" or "ambiguous".
		state.SetShowNames([]string{"Halloween"})

		if resp, body := patchChunk(t, srv, "sequences", earlierFile, 0, 5, []byte("early")); resp.StatusCode != http.StatusOK {
			t.Fatalf("upload %s: status = %d, body=%s", earlierFile, resp.StatusCode, body)
		}
		time.Sleep(5 * time.Millisecond)
		if resp, body := patchChunk(t, srv, "sequences", laterFile, 0, 4, []byte("late")); resp.StatusCode != http.StatusOK {
			t.Fatalf("upload %s: status = %d, body=%s", laterFile, resp.StatusCode, body)
		}

		attemptingFile := laterFile
		if awaitingFile == laterFile {
			attemptingFile = earlierFile
		}

		postAwaiting := []byte(`{"mainPlaylist":[{"sequenceName":"` + awaitingFile + `"}]}`)
		if resp, body := postPlaylist(t, srv, "Halloween", postAwaiting); resp.StatusCode != http.StatusOK {
			t.Fatalf("POST playlist (awaiting): status = %d, body=%s", resp.StatusCode, body)
		}
		awaiting, ok := findHeldRecord(t, held, "sequences", awaitingFile)
		if !ok || awaiting.Bound || awaiting.UnboundReason != fppConnectUnboundReasonShowIDNotPushed {
			t.Fatalf("%s = %+v (ok=%v), want held, unbound, awaiting the show id", awaitingFile, awaiting, ok)
		}
		if awaiting.Show != "Halloween" || awaiting.LogicalSequence != "my-show" {
			t.Fatalf("%s = %+v, want Show=Halloween and LogicalSequence=my-show preserved while awaiting the show id", awaitingFile, awaiting)
		}

		// The show's id becomes resolvable for a FRESH lookup (the
		// coordinator has pushed it via SetShows, properly synchronized
		// through state's own mutex), but nothing has walked awaitingFile's
		// own already-awaiting record to rebind it yet: notifyPush, the
		// only thing that would trigger RebindPendingShowIDs, is
		// deliberately never called in this test. This is the exact
		// interleaving window review round 5 finding 4 covers: a real
		// push's Apply and its RebindPendingShowIDs sweep are two
		// separate steps too, so a brand new file's own bind can
		// legitimately observe the updated id before the sweep ever
		// reaches an existing awaiting record.
		state.SetShows([]fppConnectShowIDName{{ID: "halloween-2026", Name: "Halloween"}})

		postAttempting := []byte(`{"mainPlaylist":[{"sequenceName":"` + attemptingFile + `"}]}`)
		if resp, body := postPlaylist(t, srv, "Halloween", postAttempting); resp.StatusCode != http.StatusOK {
			t.Fatalf("POST playlist (attempting): status = %d, body=%s", resp.StatusCode, body)
		}

		// Review round 6 finding 3: the awaiting competitor's own show id
		// is already confirmed resolvable (proven above, and required for
		// attemptingFile's own bind to have succeeded at all, since both
		// share the identical show name), yet it still has not bound.
		// attemptingFile must not wait on it forever, regardless of
		// ReceivedAt: it wins outright.
		registered := waitForRegistrationState(t, held, "sequences", attemptingFile, fppConnectRegistrationRegistered)
		if registered.LogicalSequence != "my-show" {
			t.Fatalf("%s registered = %+v, want LogicalSequence my-show", attemptingFile, registered)
		}

		if got := len(requests()); got != 1 {
			t.Fatalf("registration requests = %d, want exactly 1", got)
		}
	}

	t.Run("awaiting file received earlier does not block the attempting file forever", func(t *testing.T) {
		run(t, "My Show.fseq", "my_show.fseq", "My Show.fseq")
	})
	t.Run("awaiting file received later loses the identity outright", func(t *testing.T) {
		run(t, "My Show.fseq", "my_show.fseq", "my_show.fseq")
	})
}
