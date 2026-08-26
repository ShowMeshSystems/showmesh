package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// This file is FC3: it turns a held, bound file (FC2's fppconnectheld.go)
// into a dispatchable asset by registering it through the coordinator's
// existing POST /api/v1/assets, with targetKind=node and this node as the
// target (ADR-028 decision 8, docs/build/TRACK-E-FPP-CONNECT.md's FC3
// paragraph). Supersession, rollback, hashing and audit all come from that
// existing path; this seam contributes nothing to that model, only an
// ingestion route into it.

// fppConnectRegistrationSkipped, fppConnectRegistrationPending,
// fppConnectRegistrationRegistered and fppConnectRegistrationFailed are
// [fppConnectHeldRecord.RegistrationState]'s values; see that field's own
// doc comment.
const (
	fppConnectRegistrationSkipped    = "skipped"
	fppConnectRegistrationPending    = "pending"
	fppConnectRegistrationRegistered = "registered"
	fppConnectRegistrationFailed     = "failed"
)

// fppConnectRegisterInitialBackoff and fppConnectRegisterMaxBackoff are the
// registration retry loop's capped exponential backoff: 10s, doubling, up
// to 5 minutes, for as long as the agent runs.
const (
	fppConnectRegisterInitialBackoff = 10 * time.Second
	fppConnectRegisterMaxBackoff     = 5 * time.Minute
)

// fppConnectRegisterTimeout bounds one registration HTTP attempt.
// Generous: a held FSEQ can run to hundreds of megabytes, and this is a
// compatibility shim for one node talking to its own coordinator on a show
// network, not a public endpoint that needs a tight bound.
const fppConnectRegisterTimeout = 30 * time.Minute

// fppConnectRegisterHTTPClient is the HTTP client registration attempts
// use. A package-level var, matching assets.go's assetHTTPClient, so a
// test can point it at an httptest.Server without touching
// http.DefaultClient.
var fppConnectRegisterHTTPClient = &http.Client{}

// fppConnectMediaTypeForDir maps FC2's upload directory to the assets
// API's mediaType. Only "sequences" registers in this lane: FC3 registers
// FSEQ content only, per docs/build/TRACK-E-FPP-CONNECT.md's FC3
// paragraph; "music" and "videos" are held (an operator can still see and
// manually promote them) but never reach POST /api/v1/assets from here.
func fppConnectMediaTypeForDir(dir string) (mediaType string, registrable bool) {
	if dir == "sequences" {
		return "fseq", true
	}
	return "", false
}

// fppConnectRegisterOutcome is one registration attempt's result.
type fppConnectRegisterOutcome struct {
	// kind is one of the fppConnectRegistration* constants (never "" and
	// never fppConnectRegistrationSkipped's absence — a skip is decided
	// before any attempt is made, see attemptRegister).
	kind string

	assetID     string
	rolledBack  bool
	reason      string
	problemType string
}

// fppConnectRegistrar is FC3's whole state: it watches
// fppConnectHeldStore.SetOnHeld for newly bound files, walks the held
// backlog at boot, and drives one retry loop goroutine per file currently
// awaiting registration.
type fppConnectRegistrar struct {
	ctx    context.Context
	held   *fppConnectHeldStore
	state  *fppConnectState
	nodeID string
	token  string
	now    func() time.Time
	logger *slog.Logger

	// triggerInventory signals the asset inventory to republish out of
	// cadence after a successful registration, so the coordinator's own
	// evidence of this node's held bytes post-dates the registration that
	// made them dispatchable. Matches command.go's assetFetchTrigger
	// non-blocking-send discipline exactly (this IS that same channel).
	triggerInventory func()

	mu   sync.Mutex
	wake chan struct{}
}

// newFPPConnectRegistrar constructs a registrar. ctx bounds every retry
// loop this registrar ever starts (matching every other long-lived
// goroutine in agent.go, all keyed off sigCtx): a registration attempt in
// flight when ctx ends is abandoned, never left to complete after the rest
// of the agent has begun shutting down.
func newFPPConnectRegistrar(ctx context.Context, held *fppConnectHeldStore, state *fppConnectState, nodeID, token string, triggerInventory func(), now func() time.Time, logger *slog.Logger) *fppConnectRegistrar {
	return &fppConnectRegistrar{
		ctx: ctx, held: held, state: state, nodeID: nodeID, token: token,
		triggerInventory: triggerInventory, now: now, logger: logger,
		wake: make(chan struct{}),
	}
}

// wakeChan returns the channel a waiting retry loop should select on
// alongside its backoff timer.
func (r *fppConnectRegistrar) wakeChan() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.wake
}

// Wake broadcasts to every retry loop currently waiting out a backoff:
// try again now. Called after every applied "fppconnect.configure" push
// (fppConnectState.notifyPush, wired in agent.go), since the operator may
// have just fixed the coordinator base URL or coordinator reachability.
func (r *fppConnectRegistrar) Wake() {
	r.mu.Lock()
	close(r.wake)
	r.wake = make(chan struct{})
	r.mu.Unlock()
}

// OnHeld is FC2's SetOnHeld callback (fppconnectheld.go): invoked
// synchronously under the held store's own lock whenever a record is
// created or its binding changes. It must return quickly and must never
// call back into the held store, so it does no I/O itself: it only decides
// whether rec is a registration candidate and, if so, starts a retry loop
// goroutine for it.
func (r *fppConnectRegistrar) OnHeld(rec fppConnectHeldRecord) {
	if !rec.Bound {
		// ADR-030 decision 5 extended: an unresolved binding registers
		// nothing, and must never be reported as pending registration.
		return
	}
	go r.registerLoop(rec)
}

// BootWalk registers (or resumes retrying) every currently held record
// that is bound but not yet registered, once, after FC2's own startup
// sweep. A registered record is left alone; an unbound one stays unbound
// (never even considered here).
func (r *fppConnectRegistrar) BootWalk() {
	for _, rec := range r.held.Held() {
		if !rec.Bound {
			continue
		}
		if rec.RegistrationState == fppConnectRegistrationRegistered {
			continue
		}
		go r.registerLoop(rec)
	}
}

// registerLoop drives one held record from its current state to
// "registered", retrying a retryable failure with capped exponential
// backoff (woken early by Wake) for as long as r.ctx is alive, and
// returning without a further attempt on "skipped", "registered" or
// "failed" (ADR-030 decision 5 applied here: a 400 or 403 will not change
// on their own, so this loop never spends another request on one). Every
// state it writes back is guarded against a fresher upload of the same
// (dir, name) having replaced rec's own bytes in the meantime
// (fppConnectHeldStore.setRegistrationLocked's content-hash check): when
// that has happened, this loop simply stops, since the newer upload's own
// registerLoop call owns the record now.
func (r *fppConnectRegistrar) registerLoop(rec fppConnectHeldRecord) {
	backoff := fppConnectRegisterInitialBackoff
	for {
		if r.ctx.Err() != nil {
			return
		}

		// Captured BEFORE the attempt runs, not after: a Wake() call that
		// lands anytime from here through the end of this iteration's
		// backoff wait still reaches this iteration, because closing a
		// channel is a permanent, order-independent signal to a select
		// already holding a reference to it. Capturing it after a failed
		// attempt instead would leave a real window where a Wake() landing
		// between "the attempt just failed" and "the wait started" closes
		// a channel this iteration never subscribed to, silently swallowed
		// until the next Wake() or the full backoff elapses.
		wake := r.wakeChan()

		outcome := r.attemptRegister(rec)

		switch outcome.kind {
		case fppConnectRegistrationSkipped:
			r.held.SetRegistrationSkipped(rec.Dir, rec.Name, rec.ContentHash, outcome.reason)
			return
		case fppConnectRegistrationRegistered:
			if r.held.SetRegistrationRegistered(rec.Dir, rec.Name, rec.ContentHash, outcome.assetID, outcome.rolledBack) {
				r.triggerInventory()
			}
			return
		case fppConnectRegistrationFailed:
			r.held.SetRegistrationFailed(rec.Dir, rec.Name, rec.ContentHash, outcome.problemType, outcome.reason)
			return
		default: // fppConnectRegistrationPending: retryable
			nextRetryAt := r.now().Add(backoff)
			if !r.held.SetRegistrationPending(rec.Dir, rec.Name, rec.ContentHash, outcome.reason, nextRetryAt) {
				return
			}
			select {
			case <-r.ctx.Done():
				return
			case <-time.After(backoff):
			case <-wake:
			}
			backoff *= 2
			if backoff > fppConnectRegisterMaxBackoff {
				backoff = fppConnectRegisterMaxBackoff
			}
		}
	}
}

// fppConnectRegisterFields is the exact, required order the assets API's
// POST /assets multipart body must arrive in: every field part before the
// file part (api/openapi.yaml's uploadAsset operation description).
func fppConnectRegisterFields(rec fppConnectHeldRecord, mediaType, nodeID string) [][2]string {
	return [][2]string{
		{"show", rec.Show},
		{"sequence", rec.LogicalSequence},
		{"mediaType", mediaType},
		{"targetKind", "node"},
		{"target", nodeID},
	}
}

// attemptRegister makes at most one registration attempt for rec (or none
// at all, for a coordinator base URL that is not configured, or a
// mediaType this lane does not register), and classifies the result
// exactly as this seam's spec requires: 200 verified against rec's own
// content hash is "registered"; 400 and 403 are "failed" and never
// retried; every other 4xx, 5xx, and transport error is "pending" (retry).
func (r *fppConnectRegistrar) attemptRegister(rec fppConnectHeldRecord) fppConnectRegisterOutcome {
	mediaType, registrable := fppConnectMediaTypeForDir(rec.Dir)
	if !registrable {
		return fppConnectRegisterOutcome{
			kind:   fppConnectRegistrationSkipped,
			reason: fmt.Sprintf("mediaType for directory %q is not registered by this lane", rec.Dir),
		}
	}

	baseURL := r.state.CoordinatorBaseURL()
	if baseURL == "" {
		return fppConnectRegisterOutcome{
			kind:   fppConnectRegistrationPending,
			reason: "coordinator base URL not configured",
		}
	}

	path := r.held.HeldFilePath(rec.Dir, rec.Name)
	f, err := os.Open(path)
	if err != nil {
		return fppConnectRegisterOutcome{
			kind:   fppConnectRegistrationPending,
			reason: fmt.Sprintf("opening held file %q: %v", path, err),
		}
	}
	defer func() { _ = f.Close() }()

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go fppConnectWriteRegisterBody(mw, pw, f, rec, mediaType, r.nodeID)

	ctx, cancel := context.WithTimeout(r.ctx, fppConnectRegisterTimeout)
	defer cancel()

	url := strings.TrimRight(baseURL, "/") + "/api/v1/assets"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, pr)
	if err != nil {
		return fppConnectRegisterOutcome{
			kind:   fppConnectRegistrationPending,
			reason: fmt.Sprintf("building registration request: %v", err),
		}
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}

	resp, err := fppConnectRegisterHTTPClient.Do(req)
	if err != nil {
		return fppConnectRegisterOutcome{
			kind:   fppConnectRegistrationPending,
			reason: fmt.Sprintf("registration request failed: %v", err),
		}
	}
	defer func() { _ = resp.Body.Close() }()

	// A problem+json body, and the 200 response body, are both small JSON
	// documents (never the uploaded bytes): capped generously, not
	// unbounded, against a misbehaving coordinator.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode == http.StatusOK {
		return fppConnectClassifyRegisterSuccess(body, rec.ContentHash)
	}

	problemType, detail := fppConnectDecodeProblem(body)
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusForbidden {
		// ADR-030 decision 5's "an interrupted upload registers nothing"
		// extends here: a 400 or 403 names a condition that will not
		// change on its own (a malformed field, a missing scope), so
		// spending another request on it is never correct.
		if detail == "" {
			detail = fmt.Sprintf("registration refused with status %d", resp.StatusCode)
		}
		return fppConnectRegisterOutcome{kind: fppConnectRegistrationFailed, problemType: problemType, reason: detail}
	}

	reason := detail
	if reason == "" {
		reason = fmt.Sprintf("unexpected registration status %d", resp.StatusCode)
	}
	return fppConnectRegisterOutcome{kind: fppConnectRegistrationPending, reason: reason}
}

// fppConnectWriteRegisterBody streams rec's multipart registration body
// into pw: every field part in fppConnectRegisterFields' required order,
// then the file part last, copied directly from f without ever buffering
// the whole file in memory. Always closes pw (with the first error
// encountered, if any) so the reading side of the pipe observes either a
// clean EOF or that error, never a hang.
func fppConnectWriteRegisterBody(mw *multipart.Writer, pw *io.PipeWriter, f *os.File, rec fppConnectHeldRecord, mediaType, nodeID string) {
	err := func() error {
		for _, kv := range fppConnectRegisterFields(rec, mediaType, nodeID) {
			if err := mw.WriteField(kv[0], kv[1]); err != nil {
				return err
			}
		}
		part, err := mw.CreateFormFile("file", rec.Name)
		if err != nil {
			return err
		}
		if _, err := io.Copy(part, f); err != nil {
			return err
		}
		return mw.Close()
	}()
	if err != nil {
		_ = pw.CloseWithError(err)
		return
	}
	_ = pw.Close()
}

// fppConnectAssetResponse is POST /api/v1/assets' 200 body shape
// (api/openapi.yaml's AssetResponse/Asset schemas), narrowed to the three
// fields this seam reads.
type fppConnectAssetResponse struct {
	Asset struct {
		ID          string `json:"id"`
		ContentHash string `json:"contentHash"`
	} `json:"asset"`
	RolledBack bool `json:"rolledBack"`
}

// fppConnectClassifyRegisterSuccess decodes a 200 registration response
// and verifies its content hash against wantHash (this node's own
// FC2-computed hash): a mismatch is a failure this seam records, never a
// registration it trusts. See this seam's spec: "Verify the returned
// content hash equals the hash FC2 computed; a mismatch is recorded as a
// failure and the record stays unregistered."
func fppConnectClassifyRegisterSuccess(body []byte, wantHash string) fppConnectRegisterOutcome {
	var resp fppConnectAssetResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return fppConnectRegisterOutcome{
			kind:   fppConnectRegistrationPending,
			reason: fmt.Sprintf("decoding registration response: %v", err),
		}
	}
	if resp.Asset.ContentHash != wantHash {
		return fppConnectRegisterOutcome{
			kind:   fppConnectRegistrationFailed,
			reason: fmt.Sprintf("coordinator returned content hash %q, want %q", resp.Asset.ContentHash, wantHash),
		}
	}
	return fppConnectRegisterOutcome{
		kind:       fppConnectRegistrationRegistered,
		assetID:    resp.Asset.ID,
		rolledBack: resp.RolledBack,
	}
}

// fppConnectProblem is RFC 9457 application/problem+json's shape
// (api/openapi.yaml's Problem schema), narrowed to the two fields this
// seam records verbatim as failure evidence.
type fppConnectProblem struct {
	Type   string `json:"type"`
	Detail string `json:"detail"`
}

// fppConnectDecodeProblem best-effort decodes body as a Problem, returning
// ("", "") for a body that is not one (never an error: an unexpected
// non-JSON body from a broken coordinator is still classified by status
// code alone, one level up).
func fppConnectDecodeProblem(body []byte) (problemType, detail string) {
	var p fppConnectProblem
	if err := json.Unmarshal(body, &p); err != nil {
		return "", ""
	}
	return p.Type, p.Detail
}
