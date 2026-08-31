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
	"sort"
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

	// initialBackoff and maxBackoff are registerLoop's capped exponential
	// backoff bounds, set to fppConnectRegisterInitialBackoff/
	// fppConnectRegisterMaxBackoff by newFPPConnectRegistrar and otherwise
	// fixed for the registrar's whole life (review round 5 finding 5):
	// fields rather than the package constants directly so a test can
	// substitute a millisecond-scale schedule and actually observe (or
	// prove the absence of) a retry within a short deadline, something
	// impossible against the real 10s initial backoff without either an
	// explicit Wake() or a multi-second sleep.
	initialBackoff time.Duration
	maxBackoff     time.Duration

	// triggerInventory signals the asset inventory to republish out of
	// cadence after a successful registration, so the coordinator's own
	// evidence of this node's held bytes post-dates the registration that
	// made them dispatchable. Matches command.go's assetFetchTrigger
	// non-blocking-send discipline exactly (this IS that same channel).
	triggerInventory func()

	mu       sync.Mutex
	wake     chan struct{}
	inFlight map[string]bool // dir/name of every (dir, name) a registerLoop goroutine is currently running for

	// identityOwner records which (dir, name) key currently owns each
	// assets-API identity (dir + showID + logicalSequence), once decided
	// (review round 3 finding 1): two distinct file names that slugify to
	// the same sequence id under the same show must never both register,
	// and which one wins must not depend on upload arrival order alone,
	// since binding (the event that actually starts a registration
	// attempt) can happen long after upload completion. Populated and
	// read only through claimIdentity, one locked decision at a time. A
	// cached entry is not permanent (review round 5 finding 2): claimIdentity
	// clears it, and decides fresh, the moment it no longer matches either
	// side of the collision the held store currently reports, so a dead
	// owner (superseded, rebound elsewhere, or terminally failed) can
	// never lock an identity a fresh upload of the losing file has every
	// right to win.
	identityOwner map[string]string

	// wg counts every registerLoop goroutine this registrar has ever
	// started that has not yet returned, so agent.go's clean-shutdown path
	// can join it like every other long-lived goroutine there (review
	// round 1 finding 4): before this field existed, a registration
	// attempt in flight when the agent began shutting down was simply
	// abandoned mid-request with nothing downstream ever waiting on it.
	wg sync.WaitGroup

	// closed is set true, under mu, by Wait, before Wait ever calls
	// wg.Wait (review round 5 finding 3): startLoop checks closed in the
	// SAME locked critical section as its in-flight claim and its own
	// wg.Add call, so the two orderings are mutually exclusive rather
	// than racing. Either startLoop's whole critical section (claim,
	// Add) completes before Wait's does (in which case Wait's own
	// wg.Wait, called only after that critical section releases mu, is
	// guaranteed to see the Add), or Wait's critical section (setting
	// closed) completes first, in which case startLoop observes closed
	// and never calls wg.Add at all. Before this field existed, r.ctx.Err()
	// and wg.Add were two separate, unguarded steps: a late OnHeld (the
	// "fppconnect.configure" push path, not gated by HTTP listener
	// shutdown, see startLoop's own doc comment) could pass the ctx
	// check and then call wg.Add after Wait had already observed the
	// counter reach zero and returned, a documented sync.WaitGroup
	// misuse.
	closed bool
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
		initialBackoff: fppConnectRegisterInitialBackoff,
		maxBackoff:     fppConnectRegisterMaxBackoff,
		wake:           make(chan struct{}),
		inFlight:       map[string]bool{},
		identityOwner:  map[string]string{},
	}
}

// Wait blocks until every registerLoop goroutine this registrar has ever
// started, at any point in this process's life, has returned. Called
// inline from agent.go's clean-shutdown path, after the FPP Connect HTTP
// listener has already stopped (so no further call to OnHeld can start a
// new one) and after sigCtx has already been canceled: every registerLoop
// observes that via r.ctx (the same context) and returns promptly, either
// from its own ctx.Err() check or because the canceled context aborts its
// in-flight HTTP request. Must be called directly in the shutdown
// sequence, never from a goroutine started earlier in the process's life:
// a goroutine that calls Wait before every registration has even started
// can observe the WaitGroup's counter at zero and return immediately,
// joining nothing (review round 2 finding A).
//
// closed is set true, under r.mu, BEFORE wg.Wait is ever called (review
// round 5 finding 3): see closed's own doc comment on the struct for why
// that ordering, shared with startLoop's identical lock, is what makes a
// late startLoop call and this call mutually exclusive instead of racing.
func (r *fppConnectRegistrar) Wait() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
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
//
// r.ctx.Err() is checked before ever taking r.mu (review round 3 finding
// 4): a late caller (see agent.go's own doc comment on the shutdown
// sequence, in particular the "fppconnect.configure" push path, which is
// not gated by the HTTP listener's own shutdown) can still reach
// OnHeld/startLoop after Wait has already returned or is currently
// blocked returning. This alone is only a fast path, not the actual
// guarantee: ctx being canceled and Wait having started are two different
// events (agent.go cancels sigCtx, then separately calls Wait), so a
// caller could still observe r.ctx.Err() == nil right up to the moment it
// takes r.mu. r.closed, checked in the SAME critical section as the
// in-flight claim and wg.Add below, is what actually closes the race
// (review round 5 finding 3): see closed's own doc comment on the struct.
func (r *fppConnectRegistrar) startLoop(rec fppConnectHeldRecord) {
	if r.ctx.Err() != nil {
		return
	}

	key := rec.Dir + "/" + rec.Name
	r.mu.Lock()
	if r.closed || r.inFlight[key] {
		r.mu.Unlock()
		return
	}
	r.inFlight[key] = true
	r.wg.Add(1)
	r.mu.Unlock()

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
// failed: a 400, 403, 405, or 413 will not change on their own, so this
// loop never spends another request on one; 401 is deliberately excluded
// from that set, see fppConnectRegisterTerminalStatuses). Every iteration
// re-reads the record's CURRENT state via fppConnectHeldStore.RecordFor
// rather than trust the rec argument (which can be stale the moment this
// goroutine actually starts running, and staler still after a long
// backoff wait, review round 1 finding 3): a concurrent rebind, or a
// fresher upload of the same (dir, name) replacing these bytes entirely,
// is picked up before the next
// attempt rather than acted on with what this loop last knew. The
// fresher-upload case is also independently guarded by
// fppConnectHeldStore.setRegistrationLocked's own content-hash and
// identity check on every write back: even a rec this loop is mid-attempt
// with, if it has since been superseded with different bytes (review
// round 2 finding B) or rebound to a different show or sequence (review
// round 6 finding 1), never overwrites the newer state. When that guard
// refuses a write, this loop continues rather than returns: this record's
// own key is the only registerLoop startLoop's in-flight guard will ever
// allow to run, so if this instance exits without picking up the
// superseding record, nothing ever registers it. Continuing lets the next
// iteration's RecordFor read the new record and register it instead,
// under whichever identity it now carries.
func (r *fppConnectRegistrar) registerLoop(rec fppConnectHeldRecord) {
	backoff := r.initialBackoff
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

		// Every branch below treats a false return from its setter as
		// "superseded": a fresher upload of this same (dir, name)
		// completed, or a rebind changed its identity, while this attempt
		// was in flight, and setRegistrationLocked's own content-hash and
		// identity guard refused to let this stale outcome overwrite it
		// (review round 2 finding B, review round 6 finding 1). Before
		// this fix every branch returned unconditionally on a false
		// setter result, which meant the record this loop was started for
		// is current from the newer upload's or rebind's own write, but
		// NO registerLoop is left running for it: startLoop's in-flight
		// guard already refused a second goroutine for this key while
		// this one owned it, so once this one exits without registering
		// anything, the newer state is never acted on at all. continue
		// instead: the loop's own top-of-iteration RecordFor picks up the
		// new record fresh, and backoff resets since this is a new
		// attempt cycle against new content or a new identity, not a
		// retry of the same failure. waitBeforeRetry pauses first (review
		// round 6 finding 4): a tight loop of supersessions with no I/O
		// between them (e.g. offset-0 re-uploads of a file this lane
		// never registers, which supersede and skip without ever doing
		// any I/O) would otherwise spin this goroutine hot, with no wait
		// at all between iterations.
		switch outcome.kind {
		case fppConnectRegistrationSkipped:
			if !r.held.SetRegistrationSkipped(rec.Dir, rec.Name, rec.ContentHash, rec.ShowID, rec.LogicalSequence, outcome.reason) {
				if !r.waitBeforeRetry(wake) {
					return
				}
				backoff = r.initialBackoff
				continue
			}
			return
		case fppConnectRegistrationRegistered:
			if !r.held.SetRegistrationRegistered(rec.Dir, rec.Name, rec.ContentHash, rec.ShowID, rec.LogicalSequence, outcome.assetID, outcome.rolledBack) {
				if !r.waitBeforeRetry(wake) {
					return
				}
				backoff = r.initialBackoff
				continue
			}
			r.triggerInventory()
			return
		case fppConnectRegistrationFailed:
			if !r.held.SetRegistrationFailed(rec.Dir, rec.Name, rec.ContentHash, rec.ShowID, rec.LogicalSequence, outcome.problemType, outcome.reason) {
				if !r.waitBeforeRetry(wake) {
					return
				}
				backoff = r.initialBackoff
				continue
			}
			return
		default: // fppConnectRegistrationPending: retryable
			nextRetryAt := r.now().Add(backoff)
			if !r.held.SetRegistrationPending(rec.Dir, rec.Name, rec.ContentHash, rec.ShowID, rec.LogicalSequence, outcome.reason, nextRetryAt) {
				if !r.waitBeforeRetry(wake) {
					return
				}
				backoff = r.initialBackoff
				continue
			}
			select {
			case <-r.ctx.Done():
				return
			case <-time.After(backoff):
			case <-wake:
			}
			backoff *= 2
			if backoff > r.maxBackoff {
				backoff = r.maxBackoff
			}
		}
	}
}

// fppConnectSupersededPause is how long waitBeforeRetry pauses on the
// "superseded" continue path (review round 6 finding 4): a short, fixed
// pause independent of the registrar's own backoff schedule, since that
// schedule can be a millisecond-scale test override, and this pause exists
// only to stop a tight supersession loop from spinning hot, not to back
// off a real coordinator failure.
const fppConnectSupersededPause = 250 * time.Millisecond

// waitBeforeRetry pauses for fppConnectSupersededPause, or until r.ctx is
// done or a Wake() call lands, whichever comes first. Returns false when
// r.ctx ended first, so the caller can return immediately rather than loop
// back into a canceled context.
func (r *fppConnectRegistrar) waitBeforeRetry(wake <-chan struct{}) bool {
	select {
	case <-r.ctx.Done():
		return false
	case <-time.After(fppConnectSupersededPause):
		return true
	case <-wake:
		return true
	}
}

// fppConnectIdentityKey is the assets API's own identity, as far as this
// registrar's collision detection is concerned: the directory (which
// pins mediaType), the show, and the sequence id. Two records sharing all
// three would register as the identical (show, sequence, targetKind,
// target) asset.
func fppConnectIdentityKey(dir, showID, logicalSequence string) string {
	return dir + "\x00" + showID + "\x00" + logicalSequence
}

// claimIdentity decides, in one locked step, whether key (dir/name) may
// proceed to attempt registering identityKey (review round 3 finding 1):
// deciding via ReceivedAt and separately staking the claim let a colliding
// record with an EARLIER ReceivedAt win the check even after the OTHER
// record had already registered under that identity, since binding (the
// event that starts a registration attempt) can happen long after upload
// completion and has no relation to arrival order. A collision whose
// other side is already registered, or whose registerLoop currently owns
// this registrar's in-flight slot, wins outright regardless of
// ReceivedAt: the identity is already spoken for by a completed or
// currently-running attempt. Only when NEITHER side has ever claimed this
// identity does the earlier ReceivedAt decide, and winning stakes the
// claim for key in this same locked step, so a concurrent competitor's
// own call for the identical identityKey sees it already taken rather
// than reaching its own, possibly different, conclusion moments apart.
//
// A cached owner is trusted only while it still matches key or otherKey
// (review round 5 finding 2): otherKnown/otherKey/otherRegistered are
// derived fresh from the held store on every call (attemptRegister's own
// CollidingRecord lookup, immediately before this call), so a cached
// owner that matches neither is proof its own record has since moved on
// from this identity, whether by being superseded with different bytes,
// rebound to a different show or sequence (BindShow), or (via
// attemptRegister's own terminal-other check just above this call)
// failed non-retryably and so will never attempt again. Before this fix
// identityOwner was permanent for the life of the process: a dead owner
// locked its identity forever, and the losing file it beat could never
// win it even after a fresh upload gave it a real second chance. A stale
// entry is cleared and the identity is decided fresh, exactly as if
// nothing had ever claimed it.
func (r *fppConnectRegistrar) claimIdentity(key, identityKey string, receivedAt time.Time, otherKnown bool, otherKey string, otherReceivedAt time.Time, otherRegistered bool) (won bool, ownerKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if owner, ok := r.identityOwner[identityKey]; ok {
		switch {
		case owner == key:
			return true, key
		case otherKnown && owner == otherKey:
			return false, owner
		default:
			delete(r.identityOwner, identityKey)
		}
	}
	if !otherKnown {
		r.identityOwner[identityKey] = key
		return true, key
	}
	if otherRegistered || r.inFlight[otherKey] {
		r.identityOwner[identityKey] = otherKey
		return false, otherKey
	}
	if receivedAt.Before(otherReceivedAt) {
		r.identityOwner[identityKey] = key
		return true, key
	}
	r.identityOwner[identityKey] = otherKey
	return false, otherKey
}

// isInFlight reports whether a registerLoop goroutine currently owns
// key's (dir/name) in-flight slot.
func (r *fppConnectRegistrar) isInFlight(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inFlight[key]
}

// fppConnectCompetitorTier ranks rec for strongestCompetitor's own sort: 2
// for a registered record (never displaced by anything), 1 for one
// currently in flight, 0 for anything else. Higher wins.
func (r *fppConnectRegistrar) fppConnectCompetitorTier(rec fppConnectHeldRecord) int {
	switch {
	case rec.RegistrationState == fppConnectRegistrationRegistered:
		return 2
	case r.isInFlight(rec.Dir + "/" + rec.Name):
		return 1
	default:
		return 0
	}
}

// strongestCompetitor reduces colliding (every record CollidingRecords
// found for one identity) to the single record claimIdentity should treat
// as "the" competitor (review round 7 finding 1): with three or more
// files sharing one identity, deciding against whichever record a single
// prior CollidingRecord happened to return first risked missing the
// identity's actual registered or in-flight owner entirely, letting
// claimIdentity's own stale-owner check wrongly discard a still-valid
// cached owner and hand a later file a claim it should never have won.
// Every candidate that failed non-retryably (review round 5 finding 2) or
// that is only awaiting a show id already independently confirmed
// resolvable (review round 6 finding 3) is dropped first, exactly as
// attemptRegister already did for a single candidate. What remains is
// sorted by (tier, ReceivedAt, Name), strongest first, so the choice is
// fully deterministic even when two candidates tie on both tier and
// ReceivedAt (review round 8 finding 3: two records BindShow's own single
// call bound with the identical `at` timestamp could otherwise be picked
// either way depending on Go's own randomized map iteration order,
// nothing here or in CollidingRecords ever sorted first). known is false
// only when colliding has no surviving candidate at all; awaitingShowID
// is decided from the chosen strongest candidate alone, after sorting,
// never from whichever candidate the sort happened to visit first.
func (r *fppConnectRegistrar) strongestCompetitor(colliding []fppConnectHeldRecord) (strongest fppConnectHeldRecord, known, awaitingShowID bool) {
	var candidates []fppConnectHeldRecord
	for _, cand := range colliding {
		if cand.RegistrationState == fppConnectRegistrationFailed {
			// A competitor that failed non-retryably will never attempt
			// again (ADR-030 decision 5): it no longer contests this
			// identity, the same way a competitor CollidingRecords never
			// returned in the first place would not.
			continue
		}
		if !cand.Bound {
			if _, ok := r.state.ShowID(cand.Show); ok {
				// Its own show id is already confirmed resolvable but it
				// still has not bound: whatever is stopping
				// RebindPendingShowIDs from converging it is not this
				// attempt's problem to wait on.
				continue
			}
		}
		candidates = append(candidates, cand)
	}
	if len(candidates) == 0 {
		return fppConnectHeldRecord{}, false, false
	}

	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if ta, tb := r.fppConnectCompetitorTier(a), r.fppConnectCompetitorTier(b); ta != tb {
			return ta > tb
		}
		if !a.ReceivedAt.Equal(b.ReceivedAt) {
			return a.ReceivedAt.Before(b.ReceivedAt)
		}
		return a.Name < b.Name
	})

	strongest = candidates[0]
	return strongest, true, !strongest.Bound
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
// authenticates but does not carry asset:write (403), a method this
// coordinator does not accept on this route (405), or a file over the
// coordinator's own upload size bound (413). None of those changes without
// a human editing the request itself (a different file, a different show)
// or the principal's own role — nothing this same node will ever do on a
// future attempt against the identical held bytes.
//
// 401 is deliberately NOT in this map. A shipped node install had no way
// to provision SHOWMESH_AGENT_API_TOKEN, so every upload such a node ever
// received drew a 401 that this map used to classify as permanent, and
// fixing the credential afterwards could never recover it — only a fresh
// re-upload could. Unlike 403, a 401 means "no valid credential was
// presented at all," which an operator fixes from OUTSIDE this request
// (minting a token, editing agent.env, restarting the agent) with no need
// to touch the held file or re-upload it. Classifying it "pending" instead
// lets exactly that recovery happen: the record keeps its retry schedule
// (see registerLoop), and the very next attempt succeeds once the
// corrected token is in place — see fppConnectRegistrationTerminal and
// registerLoop's own retry loop, which re-reads r.token fresh on every
// attempt.
//
// api/openapi.yaml's uploadAsset operation documents 400/401/403/405/413
// plus 500 and 507; 401, and both 500 and 507, are retried below: a
// coordinator can recover from being out of memory or disk, or a node can
// have its credential corrected, and none of those name a defect this
// node's own request could ever repeat forever.
var fppConnectRegisterTerminalStatuses = map[int]bool{
	http.StatusBadRequest:            true,
	http.StatusForbidden:             true,
	http.StatusMethodNotAllowed:      true,
	http.StatusRequestEntityTooLarge: true,
}

// attemptRegister makes at most one registration attempt for rec (or none
// at all, for a coordinator base URL that is not configured, a mediaType
// this lane does not register, a name whose slug is empty, a bound record
// with no resolved show id (review round 6 finding 2), or a collision
// against a competitor still awaiting its own show id (review round 6
// finding 3)), and classifies the result exactly as this seam's spec
// requires: 200 verified against rec's own content hash is "registered";
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

	// A bound record with no resolved show id is not this seam's normal
	// shape (every live binding path resolves ShowID and LogicalSequence
	// together), but load()'s own record repair is not the only way one
	// could reach here (review round 6 finding 2): unlike an empty
	// LogicalSequence, an empty ShowID is not permanent, so this is
	// "pending," retryable, never "failed." Sending show="" would only
	// ever earn a terminal refusal from the coordinator with nothing left
	// to retry it, since RebindPendingShowIDs never walks an already-bound
	// record.
	if rec.ShowID == "" {
		return fppConnectRegisterOutcome{
			kind:   fppConnectRegistrationPending,
			reason: fmt.Sprintf("bound record for %q has no resolved show id", rec.Name),
		}
	}

	// Another local condition independent of the coordinator (review
	// round 2 finding C, refined by review round 3 finding 1): two
	// distinct file names whose stems slugify to the same sequence id
	// under the same show ("My Show.fseq" and "my_show.fseq" both slug to
	// "my-show") would register as the SAME asset identity (show,
	// sequence, targetKind, target) and silently supersede each other,
	// with both held records claiming "registered." claimIdentity decides
	// who wins, in one locked step: a colliding record that is already
	// registered, or currently mid-attempt, wins regardless of
	// ReceivedAt (binding, not upload time, is what starts a registration
	// attempt, and the two are not the same event); only a genuine
	// simultaneous first attempt by both falls back to ReceivedAt.
	// strongestCompetitor reduces every OTHER record CollidingRecords
	// returns to the single one claimIdentity needs to see (review round 7
	// finding 1): with three or more colliding files, deciding against
	// whichever one happens to come back is not enough, since that one
	// might be neither the registered owner nor in flight, even though a
	// real registered owner exists among the others.
	identityKey := fppConnectIdentityKey(rec.Dir, rec.ShowID, rec.LogicalSequence)
	myKey := rec.Dir + "/" + rec.Name
	colliding := r.held.CollidingRecords(rec.Dir, rec.Name, rec.ShowID, rec.Show, rec.LogicalSequence)
	other, otherKnown, otherAwaitingShowID := r.strongestCompetitor(colliding)
	otherKey, otherReceivedAt, otherRegistered := "", time.Time{}, false
	if otherKnown {
		otherKey = other.Dir + "/" + other.Name
		otherReceivedAt = other.ReceivedAt
		otherRegistered = other.RegistrationState == fppConnectRegistrationRegistered
	}
	if won, ownerKey := r.claimIdentity(myKey, identityKey, rec.ReceivedAt, otherKnown, otherKey, otherReceivedAt, otherRegistered); !won {
		ownerName := ownerKey
		if otherKnown && otherKey == ownerKey {
			// other.Name is copied straight from the Upload-Name header,
			// bounded only by fppConnectMaxHeaderBytes (16 KiB, review
			// round 6 finding 5): bounded here, before it is ever written
			// into RegistrationReason, not only where that record is
			// later read for the wire (renderreport.go's
			// toRenderFPPConnectHeldFile), since RegistrationReason is
			// also persisted to disk by setRegistrationLocked and must
			// never carry an unbounded competitor name into that file
			// either.
			ownerName = fppConnectBoundEventString(other.Name)
		}
		if otherAwaitingShowID {
			return fppConnectRegisterOutcome{
				kind: fppConnectRegistrationPending,
				reason: fmt.Sprintf(
					"sequence id %q under show %q may collide with held file %q, which is still awaiting its own show id; will re-check",
					rec.LogicalSequence, rec.ShowID, ownerName),
			}
		}
		// Pending, not failed (review round 7 finding 3): a collision loss
		// is never permanent on its own the way an empty slug or a
		// coordinator 4xx is. If ownerName is ever discarded, rebound to a
		// different identity, or itself fails, this file's next retry
		// (wake or backoff) re-evaluates the collision fresh and can win
		// it; marking this terminal left the only path back a fresh
		// re-upload, since nothing else ever starts a new attempt for an
		// already-terminal record.
		return fppConnectRegisterOutcome{
			kind: fppConnectRegistrationPending,
			reason: fmt.Sprintf(
				"sequence id %q under show %q collides with already-held file %q, which slugifies to the same id; will re-check if %q is ever discarded, rebound, or fails; rename one of the two files to resolve this permanently",
				rec.LogicalSequence, rec.ShowID, ownerName, ownerName),
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
