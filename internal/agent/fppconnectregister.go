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
	// kind is one of the fppConnectRegistration* constants (never "";
	// fppConnectRegistrationSkipped's absence is not meaningful either,
	// since a skip is decided before any attempt is made, see
	// attemptRegister).
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

	mu       sync.Mutex
	wake     chan struct{}
	inFlight map[string]bool // dir/name of every (dir, name) a registerLoop goroutine is currently running for

	// wg counts every registerLoop goroutine this registrar has ever
	// started that has not yet returned, so agent.go's clean-shutdown path
	// can join it like every other long-lived goroutine there (review
	// round 1 finding 4): before this field existed, a registration
	// attempt in flight when the agent began shutting down was simply
	// abandoned mid-request with nothing downstream ever waiting on it.
	wg sync.WaitGroup
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
		wake:     make(chan struct{}),
		inFlight: map[string]bool{},
	}
}

// Wait blocks until every registerLoop goroutine this registrar has ever
// started has returned. Called from agent.go's clean-shutdown path,
// joined alongside every other long-lived goroutine there, after sigCtx
// has already been canceled: every registerLoop observes that via r.ctx
// (the same context) and returns promptly, either from its own ctx.Err()
// check or because the canceled context aborts its in-flight HTTP request.
func (r *fppConnectRegistrar) Wait() {
	r.wg.Wait()
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

// fppConnectRegistrationTerminal reports whether state is a state
// registerLoop never attempts again: registered (done), skipped (this
// lane never registers this mediaType), or failed (a non-retryable
// refusal that will not change on its own).
func fppConnectRegistrationTerminal(state string) bool {
	switch state {
	case fppConnectRegistrationRegistered, fppConnectRegistrationSkipped, fppConnectRegistrationFailed:
		return true
	default:
		return false
	}
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
	if fppConnectRegistrationTerminal(rec.RegistrationState) {
		// xLights fires POST /api/playlist/{name} up to twice per target
		// (RES-003 section 10.6), re-firing OnHeld for a record that is
		// already done: this is the common case that check alone catches
		// (review round 1 finding 3).
		return
	}
	r.startLoop(rec)
}

// BootWalk registers (or resumes retrying) every currently held record
// that is bound but not yet registered, once, after FC2's own startup
// sweep. A registered, skipped, or failed record is left alone; an
// unbound one stays unbound (never even considered here).
func (r *fppConnectRegistrar) BootWalk() {
	for _, rec := range r.held.Held() {
		if !rec.Bound {
			continue
		}
		if fppConnectRegistrationTerminal(rec.RegistrationState) {
			continue
		}
		r.startLoop(rec)
	}
}

// startLoop starts registerLoop for rec's (dir, name), unless a loop for
// that key is already running. This is the other half of review round 1
// finding 3: OnHeld's terminal-state check alone cannot catch xLights'
// second playlist POST arriving WHILE the first registerLoop's own first
// attempt is still in flight, since the record can still legitimately read
// "" (or "pending") the second time OnHeld fires, before that first
// attempt has had a chance to write anything back at all. Only an
// explicit "a loop for this key already owns it" guard prevents the
// resulting double dispatch (two concurrent requests, two inventory
// triggers).
func (r *fppConnectRegistrar) startLoop(rec fppConnectHeldRecord) {
	key := rec.Dir + "/" + rec.Name
	r.mu.Lock()
	if r.inFlight[key] {
		r.mu.Unlock()
		return
	}
	r.inFlight[key] = true
	r.mu.Unlock()

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer func() {
			r.mu.Lock()
			delete(r.inFlight, key)
			r.mu.Unlock()
		}()
		r.registerLoop(rec)
	}()
}

// registerLoop drives one held record from its current state to
// "registered", retrying a retryable failure with capped exponential
// backoff (woken early by Wake) for as long as r.ctx is alive, and
// returning without a further attempt once the record reaches a terminal
// state (registered, skipped, or failed; ADR-030 decision 5 applied to
// failed: a 400 or 403 will not change on their own, so this loop never
// spends another request on one). Every iteration re-reads the record's
// CURRENT state via fppConnectHeldStore.RecordFor rather than trust the
// rec argument (which can be stale the moment this goroutine actually
// starts running, and staler still after a long backoff wait, review
// round 1 finding 3): a concurrent rebind, or a fresher upload of the same
// (dir, name) replacing these bytes entirely, is picked up before the next
// attempt rather than acted on with what this loop last knew. The
// fresher-upload case is also independently guarded by
// fppConnectHeldStore.setRegistrationLocked's own content-hash check on
// every write back: even a rec this loop is mid-attempt with, if it has
// since been superseded, never overwrites the newer upload's own state.
func (r *fppConnectRegistrar) registerLoop(rec fppConnectHeldRecord) {
	backoff := fppConnectRegisterInitialBackoff
	for {
		if r.ctx.Err() != nil {
			return
		}

		current, ok := r.held.RecordFor(rec.Dir, rec.Name)
		if !ok || fppConnectRegistrationTerminal(current.RegistrationState) {
			return
		}
		rec = current

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
// file part (api/openapi.yaml's uploadAsset operation description). show
// sends rec.ShowID, not rec.Show: the API's showExists validates `show`
// against the show config OBJECT ID, never the display name FC2's own
// binding carries (review round 1 finding 1). rec.ShowID is that id,
// resolved once at bind time (fppconnectheld.go's completeLocked/BindShow).
func fppConnectRegisterFields(rec fppConnectHeldRecord, mediaType, nodeID string) [][2]string {
	return [][2]string{
		{"show", rec.ShowID},
		{"sequence", rec.LogicalSequence},
		{"mediaType", mediaType},
		{"targetKind", "node"},
		{"target", nodeID},
	}
}

// fppConnectRegisterTerminalStatuses are the response statuses that will
// never change on their own (ADR-030 decision 5's "an interrupted upload
// registers nothing" extended to a refusal this lane could spend forever
// retrying for no benefit): a malformed field (400), a credential that
// does not carry asset:write (401, 403), a method this coordinator does
// not accept on this route (405), or a file over the coordinator's own
// upload size bound (413). api/openapi.yaml's uploadAsset operation
// documents exactly these five plus 500 and 507, both retried below: a
// coordinator can recover from being out of memory or disk, and neither
// names a defect this node's own request could ever repeat forever.
var fppConnectRegisterTerminalStatuses = map[int]bool{
	http.StatusBadRequest:            true,
	http.StatusUnauthorized:          true,
	http.StatusForbidden:             true,
	http.StatusMethodNotAllowed:      true,
	http.StatusRequestEntityTooLarge: true,
}

// attemptRegister makes at most one registration attempt for rec (or none
// at all, for a coordinator base URL that is not configured, a mediaType
// this lane does not register, or a name whose slug is empty), and
// classifies the result exactly as this seam's spec requires: 200
// verified against rec's own content hash is "registered";
// fppConnectRegisterTerminalStatuses are "failed" and never retried;
// every other 4xx, 5xx, and transport error is "pending" (retry).
func (r *fppConnectRegistrar) attemptRegister(rec fppConnectHeldRecord) fppConnectRegisterOutcome {
	mediaType, registrable := fppConnectMediaTypeForDir(rec.Dir)
	if !registrable {
		return fppConnectRegisterOutcome{
			kind:   fppConnectRegistrationSkipped,
			reason: fmt.Sprintf("mediaType for directory %q is not registered by this lane", rec.Dir),
		}
	}

	// A local, always-true condition independent of the coordinator: no
	// value of coordinatorBaseUrl or any retry would ever make an empty
	// sequence slug valid, so this is checked, and failed, before ever
	// looking at the coordinator base URL or opening the file (review
	// round 1 finding 2).
	if rec.LogicalSequence == "" {
		return fppConnectRegisterOutcome{
			kind:   fppConnectRegistrationFailed,
			reason: fmt.Sprintf("file name %q has no [a-z0-9] character to derive a sequence id from", rec.Name),
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

	ctx, cancel := context.WithTimeout(r.ctx, fppConnectRegisterTimeout)
	defer cancel()

	// The request is built BEFORE the pipe writer goroutine ever starts
	// (review round 1 finding 5): http.NewRequestWithContext can fail (a
	// malformed coordinator base URL), and starting the writer first would
	// leave it blocked forever on its first pw.Write with nothing ever
	// reading pr, a goroutine leak this function's own early return could
	// not reach to unblock. defer pr.Close() is registered immediately
	// after io.Pipe(), AFTER the file's own defer above: defers run in
	// reverse order, so the pipe closes before the file on every return
	// from here on, and pr.Close() also unblocks the writer goroutine (a
	// closed pipe's Write returns io.ErrClosedPipe) on every early return
	// that follows it, not only a successful one.
	url := strings.TrimRight(baseURL, "/") + "/api/v1/assets"
	pr, pw := io.Pipe()
	defer func() { _ = pr.Close() }()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, pr)
	if err != nil {
		return fppConnectRegisterOutcome{
			kind:   fppConnectRegistrationPending,
			reason: fmt.Sprintf("building registration request: %v", err),
		}
	}

	mw := multipart.NewWriter(pw)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}

	go fppConnectWriteRegisterBody(mw, pw, f, rec, mediaType, r.nodeID)

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
	if fppConnectRegisterTerminalStatuses[resp.StatusCode] {
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
