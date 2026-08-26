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

// newTestFPPConnectRegistrar wires a registrar over held whose retry loops
// share ctx (canceled automatically at test cleanup) and whose inventory
// trigger signals the returned channel, matching agent.go's own wiring of
// assetFetchTrigger.
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
	state.SetOnPush(reg.Wake)
	return reg, state, trigger
}

// uploadAndBind uploads data as a single chunk to dir/name against a fresh
// xLights-facing test listener over held, with an already-known active
// show so the file binds on completion (FC2's own established pattern,
// see TestFPPConnectUploadActiveShowFallback).
func uploadAndBind(t *testing.T, held *fppConnectHeldStore, dir, name string, data []byte) fppConnectHeldRecord {
	t.Helper()
	view := fakeFPPConnectView{enabled: true, activeShowName: "Halloween", activeShowKnown: true, activeShowEver: true}
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
	if req.fields["show"] != "Halloween" || req.fields["sequence"] != "Test" ||
		req.fields["mediaType"] != "fseq" || req.fields["targetKind"] != "node" || req.fields["target"] != "node-1" {
		t.Fatalf("fields = %+v, want show=Halloween sequence=Test mediaType=fseq targetKind=node target=node-1", req.fields)
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
// recorded as a failure, verbatim, and never retried.
func TestFPPConnectRegister400And403AreTerminal(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		problemType string
		detail      string
	}{
		{"400", http.StatusBadRequest, "invalid-parameter", "sequence is required"},
		{"403", http.StatusForbidden, "forbidden", "missing asset:write"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			held, _ := newTestHeldStore(t)

			var requestCount int32
			fakeSrv, _ := newFPPConnectRegisterFake(t, func(fppConnectRegisterRequest) (int, string) {
				atomic.AddInt32(&requestCount, 1)
				return tc.status, problemBody(t, tc.problemType, tc.detail)
			})
			defer fakeSrv.Close()

			newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "")

			uploadAndBind(t, held, "sequences", "Bad.fseq", []byte("payload"))

			failed := waitForRegistrationState(t, held, "sequences", "Bad.fseq", fppConnectRegistrationFailed)
			if failed.RegistrationProblemType != tc.problemType || failed.RegistrationReason != tc.detail {
				t.Fatalf("failure = type=%q reason=%q, want type=%q reason=%q",
					failed.RegistrationProblemType, failed.RegistrationReason, tc.problemType, tc.detail)
			}

			time.Sleep(150 * time.Millisecond)
			if got := atomic.LoadInt32(&requestCount); got != 1 {
				t.Fatalf("registration requests = %d, want exactly 1 (a %d must never be retried)", got, tc.status)
			}
		})
	}
}

// TestFPPConnectRegister503IsRetried proves a 5xx is retryable: the first
// attempt fails, the record goes pending, and a woken retry succeeds.
func TestFPPConnectRegister503IsRetried(t *testing.T) {
	held, _ := newTestHeldStore(t)

	data := []byte("retry-me")
	wantHash := sha256Hex(data)

	var attempt int32
	fakeSrv, _ := newFPPConnectRegisterFake(t, func(fppConnectRegisterRequest) (int, string) {
		if atomic.AddInt32(&attempt, 1) == 1 {
			return http.StatusServiceUnavailable, ""
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
		t.Fatalf("attempts = %d, want 2 (one 503, one success after Wake)", got)
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
