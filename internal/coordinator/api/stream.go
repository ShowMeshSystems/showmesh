package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// hubEventsBatchLimit bounds how many events [Hub.renderNewEvents] fetches
// in a single tick. A SHOWMESH HYPOTHESIS, not a measured value: large
// enough that a normal tick interval's worth of event volume is delivered
// in one pass, small enough to bound one render call's cost. If the store
// ever produces more than this many events between two ticks, the
// remainder is picked up on the following tick — [Hub.lastEventSeq] only
// advances to the highest seq actually turned into a frame, never skips
// ahead — so no event is ever lost, only delayed.
const hubEventsBatchLimit = 500

// streamWriteTimeout bounds how long ONE write to an SSE connection
// (stream.start, one frame, one keepalive comment) may take before
// [Hub.ServeHTTP] gives up on that connection and tears it down. Reset
// immediately before every write — see resetWriteDeadline — rather than set
// once for the connection's whole lifetime: a healthy stream legitimately
// runs for hours, but any ONE write should complete almost instantly once
// the kernel accepts the bytes, so this bounds each write, not the
// connection.
//
// This closes finding 1.1: ServeHTTP used to clear the write deadline
// unconditionally right after upgrading, to defeat httpapi's own
// WriteTimeout (see TestStreamSurvivesServerWriteTimeout, which this value
// must still satisfy), and put nothing in its place. A subscriber that
// stops reading but holds its socket open eventually fills the kernel send
// buffer; with no deadline, the next w.Write blocks forever, ServeHTTP
// never returns to its select, and the stream.reset the hub already queued
// on sub.reset for exactly this situation is never read and never written
// — the silent drop ADR-020 decision 4 exists to forbid. See
// TestStreamWedgedSubscriberIsReclaimedByWriteDeadline for the regression
// guard, which uses a client that provably never reads at all.
//
// A SHOWMESH HYPOTHESIS, not a measured value: long enough that ordinary
// network jitter or a briefly-busy real client never trips it, short
// enough that a genuinely wedged connection is reclaimed within a bounded
// time an operator would notice as a stuck dashboard tile rather than a
// leak that outlives the process. A package-level var, not a const, purely
// so a test can shrink it (see stream_test.go) without inventing an env
// var for something this test-local — the same posture
// defaultStreamSubscriberBuffer already takes for a different knob.
var streamWriteTimeout = 10 * time.Second

// resetWriteDeadline gives the connection underlying w up to
// streamWriteTimeout to accept the NEXT write, refreshed immediately before
// every write [Hub.ServeHTTP] makes. A write that blocks past this deadline
// means the peer has stopped reading — the kernel send buffer is full and
// nothing is draining it — and the expired deadline turns that block into
// an error the write call returns, which every caller here already treats
// as "the connection is gone, stop" (see [writeSSE] and the keepalive
// write in [Hub.ServeHTTP]).
//
// The error return is deliberately ignored, matching this file's existing
// posture for an http.ResponseWriter that does not support deadlines at
// all (e.g. httptest.ResponseRecorder in a unit test with no real
// connection underneath): such a writer has no timeout to set in the first
// place, so failing to set one is not a fault here.
func resetWriteDeadline(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(streamWriteTimeout))
}

// Hub is the SSE change stream's server side (contract sections 6.4 and
// 6.5). It owns change detection: on a fixed tick, and whenever poked via
// [Hub.Notify], it re-renders every node and FPP instance through the same
// mapping functions the REST handlers use, diffs each against what it last
// published, and broadcasts only the resources that actually changed —
// including a change caused by nothing but time passing, such as an
// [pkg/observation.Observation] crossing from current to stale (contract
// section 6.5's reason the hub re-renders on a tick at all, not only when
// poked).
//
// One Hub serves every subscriber; each subscriber gets its own bounded
// buffer and its own per-connection seq counter (contract section 6.4:
// "seq is per-connection... so it can never become a global cursor"). A
// subscriber whose buffer overflows is sent stream.reset and disconnected
// — see [Hub.broadcast] — never blocking the producer and never growing
// without bound.
//
// [Hub.Run] must be started, in its own goroutine, exactly once, with a
// context tied to the coordinator's shutdown; [New] does not start it.
// Cancelling that context closes every open stream cleanly and Run
// returns; nothing in this type spawns a goroutine Run does not own, so a
// caller that starts Run and later cancels its context can assert the
// goroutine count returns to baseline.
type Hub struct {
	deps   Dependencies
	clock  func() time.Time
	logger *slog.Logger

	tickInterval      time.Duration
	keepaliveInterval time.Duration
	bufSize           int

	mu           sync.Mutex
	subscribers  map[uint64]*subscriber
	nextID       uint64
	lastRendered map[string][]byte
	lastEventSeq uint64

	notifyCh chan struct{}
	done     chan struct{}
}

// subscriber is one open SSE connection's mailbox. frames is the bounded
// buffer [Hub.broadcast] delivers pending changes into; reset is a
// capacity-1 channel used exactly once, to hand the overflow reason to
// [Hub.ServeHTTP]'s own goroutine when frames is full — see
// [Hub.broadcast].
type subscriber struct {
	frames chan pendingFrame
	reset  chan string
}

// pendingFrame is one change the hub has decided, at a particular render
// pass, to broadcast — captured with its resource payload and the
// serverTime of that render pass, but deliberately WITHOUT a seq: seq is
// assigned once per subscriber, in [Hub.ServeHTTP]'s own loop, at the
// moment a given subscriber actually writes the frame to its connection —
// never here, since the same logical change reaches every subscriber but
// each must see its own independent, connection-local seq sequence.
type pendingFrame struct {
	event      string
	serverTime string

	node     *v1.Node
	instance *v1.FPPInstance
	ev       *v1.Event
}

// materialize assigns seq to pf and returns the SSE event name and the
// exact struct to JSON-encode as its data.
func (pf pendingFrame) materialize(seq uint64) (event string, payload any) {
	switch pf.event {
	case "node.changed":
		return "node.changed", v1.NodeChangedEvent{Seq: seq, ServerTime: pf.serverTime, Node: *pf.node}
	case "fpp.changed":
		return "fpp.changed", v1.FPPChangedEvent{Seq: seq, ServerTime: pf.serverTime, Instance: *pf.instance}
	case "event.recorded":
		return "event.recorded", v1.EventRecordedEvent{Seq: seq, ServerTime: pf.serverTime, Event: *pf.ev}
	default:
		// Unreachable: every pendingFrame this file constructs sets event
		// to one of the three cases above. A panic here is an internal
		// invariant violation in this package, not a runtime condition a
		// caller can trigger — see mapping.go's mustObservation for the
		// same posture.
		panic("api: pendingFrame with unknown event " + pf.event)
	}
}

// newHub builds a Hub. Unexported: [New] is the only supported
// constructor, so Options' defaults are always applied first.
func newHub(deps Dependencies, opts Options, logger *slog.Logger) *Hub {
	return &Hub{
		deps:              deps,
		clock:             opts.Clock,
		logger:            logger,
		tickInterval:      opts.StreamTickInterval,
		keepaliveInterval: opts.StreamKeepaliveInterval,
		bufSize:           opts.StreamSubscriberBuffer,
		subscribers:       make(map[uint64]*subscriber),
		lastRendered:      make(map[string][]byte),
		notifyCh:          make(chan struct{}, 1),
		done:              make(chan struct{}),
	}
}

// Notify pokes the hub to render and broadcast immediately, rather than
// waiting for the next tick. Non-blocking: if a poke is already pending,
// this call coalesces into it rather than queuing a second one — the next
// render pass will see whatever is current regardless of how many times
// Notify was called since the last render.
func (h *Hub) Notify() {
	select {
	case h.notifyCh <- struct{}{}:
	default:
	}
}

// Run drives the hub's tick and poke loop until ctx is cancelled, at which
// point it closes every open stream and returns. See the [Hub] doc comment
// for the "exactly once" requirement.
func (h *Hub) Run(ctx context.Context) {
	ticker := time.NewTicker(h.tickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			close(h.done)
			return
		case <-ticker.C:
			h.render(ctx)
		case <-h.notifyCh:
			h.render(ctx)
		}
	}
}

// render re-renders every node and FPP instance and diffs each against
// what was last published (contract section 6.5), fetches any events
// recorded since the last pass, and broadcasts whatever changed. Errors
// from a dependency are logged and that resource kind is simply skipped
// for this pass — a transient store error must not crash the hub's
// goroutine or poison every subsequent tick.
func (h *Hub) render(ctx context.Context) {
	now := h.clock()
	var pending []pendingFrame

	if views, err := h.deps.Nodes.Snapshot(ctx, now); err != nil {
		h.logger.Warn("stream hub: list nodes failed", "error", err)
	} else {
		present := make(map[string]struct{}, len(views))
		for _, nv := range views {
			key := "node:" + nv.NodeID
			present[key] = struct{}{}
			node := mapNode(nv, now)
			if h.updateRendered(key, node) {
				n := node
				pending = append(pending, pendingFrame{event: "node.changed", serverTime: formatTime(now), node: &n})
			}
		}
		h.evictRendered("node:", present)
	}

	if views, err := h.deps.FPP.ListInstances(ctx); err != nil {
		h.logger.Warn("stream hub: list fpp instances failed", "error", err)
	} else {
		present := make(map[string]struct{}, len(views))
		for _, fv := range views {
			key := "fpp:" + fv.InstanceID
			present[key] = struct{}{}
			inst := mapFPPInstance(fv, now)
			if h.updateRendered(key, fppInstanceDiffProjection(inst)) {
				i := inst
				pending = append(pending, pendingFrame{event: "fpp.changed", serverTime: formatTime(now), instance: &i})
			}
		}
		h.evictRendered("fpp:", present)
	}

	pending = append(pending, h.renderNewEvents(ctx, now)...)

	for _, pf := range pending {
		h.broadcast(pf)
	}
}

// fppInstanceDiffProjection returns a copy of inst with pure
// collection-bookkeeping timestamps cleared — LastPollAt at the instance
// level and CollectedAt on every observation — used ONLY as
// [Hub.updateRendered]'s change-detection key for an FPP instance, never as
// what is actually broadcast (the caller keeps the unmodified inst, with
// its real timestamps, for that).
//
// This closes finding 1.5: the FPP collector polls on a fixed interval and
// stamps a fresh CollectedAt/LastPollAt on every attempt regardless of
// outcome, so an FPP that is — or stays — unreachable produces a
// byte-different rendering every poll even though nothing about the FPP
// itself changed, and [Hub.updateRendered]'s diff (contract section 6.5)
// would broadcast fpp.changed forever at poll cadence with nothing for a
// client to act on. Contract section 6.2 already forbids precomputed ages
// in payloads for exactly this reason (an age field would make the stream
// a firehose); collectedAt/lastPollAt are the same pathology one layer
// down, in the hub's OWN bookkeeping rather than in what it renders.
//
// Step 5 review finding 3 adds a second source of the identical pathology,
// one layer up from CollectedAt, measured against the real fleet at ~43
// KB/s per connected browser on an otherwise IDLE system (860 KB in 20s):
// with finding 2 fixed (evidenceReason no longer embeds a computed age),
// two causes remained, both entirely legitimate collector/precedence
// behavior that this projection — not the collectors, and not
// [ResolveObservations] — is the correct place to absorb:
//
//   - internal/coordinator/collector/fpp re-stamps ObservedAt on every poll
//     (every ~15s) even when the decoded value is byte-identical to the
//     last poll's.
//   - [ResolveObservations]' tier-1 "later ObservedAt wins" rule
//     legitimately flips which SOURCE wins for a signal both fpp-rest and
//     fpp-mqtt report, roughly twice per REST poll interval, whenever an
//     MQTT delivery lands one nanosecond newer — correct precedence
//     behavior (contract section 5.2), but it changes the rendered
//     `source` field with nothing an operator can act on having changed.
//
// For a resolved observation whose rendered State is "current" — a real
// value with a real known ObservedAt, current as of the render's own clock
// — ObservedAt and Source are ALSO cleared from the projection, alongside
// CollectedAt, on the reasoning contract section 5.2's own precedence rule
// already establishes: which source most recently confirmed a value, and
// exactly when, is provenance and freshness bookkeeping about how the
// value was obtained, not itself part of what the value or its state ARE.
// What remains in the projection after that — Signal, Value, Unit, State,
// Reason, Quality, ValidForSeconds — is exactly "if value, unit, reason
// and state are all unchanged AND the state is still current, nothing an
// operator can act on has changed" (Step 5 review finding 3's own words),
// so a byte-identical projection on the next render correctly produces no
// frame even though the real, broadcast inst (never mutated here) legitimately
// carries a fresher observedAt or a flipped source underneath.
//
// This masking is deliberately conditioned on State == "current", never
// unconditional, which is the ADR-011 safety property that makes it
// correct rather than the exact defect this project keeps re-catching: a
// value that STOPS being reconfirmed does not stay "current" — it ages
// into "stale" (or the source that was answering it starts reporting an
// absence), and State itself then differs in the projection, still
// producing a frame. Masking ObservedAt/Source unconditionally, across
// every state, would instead make a value that silently stopped updating
// look byte-identical to one still being actively reconfirmed, which is
// precisely the "stale reads as healthy" shape ADR-011 exists to forbid —
// see TestFPPInstanceDiffProjectionAgingToStaleStillProducesAFrame in
// stream_test.go for the regression guard.
func fppInstanceDiffProjection(inst v1.FPPInstance) v1.FPPInstance {
	proj := inst
	proj.LastPollAt = nil
	proj.Observations = make([]v1.Evidence, len(inst.Observations))
	for i, o := range inst.Observations {
		o.CollectedAt = nil
		if o.State == string(observation.StateCurrent) {
			o.ObservedAt = nil
			o.Source = ""
		}
		proj.Observations[i] = o
	}
	return proj
}

// evictRendered removes every key with the given prefix from
// h.lastRendered that is not in present. present is this render pass's
// complete, just-listed membership for one resource kind, built only
// inside [Hub.render]'s success branch for that kind: a failed
// Snapshot/ListInstances call must never reach this method at all, because
// "the store errored" is not evidence that a resource is gone — see
// render()'s own doc comment on why a dependency error simply skips that
// kind for the pass instead of treating it as an empty list.
//
// Without this, h.lastRendered (finding 1.6) keeps one full JSON rendering
// per resource forever, evicting nothing: anyone with MQTT broker publish
// rights can already forge an arbitrary node ID into the coordinator's
// SQLite inventory (this is Step 2's own threat model, not a new one), and
// every forged node's rendering would otherwise be pinned in coordinator
// memory for the process's entire lifetime even after the row is deleted.
// This is silent, not a broadcast: contract section 6.4 and ADR-020 both
// say v1 carries no deletion event, so a resource disappearing from a
// render pass produces no frame — only a forgotten diff key, so that if
// the same resource ID legitimately reappears later it is treated as new
// (correctly re-announced) rather than compared against a stale rendering
// from before it vanished.
func (h *Hub) evictRendered(prefix string, present map[string]struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for key := range h.lastRendered {
		if len(key) <= len(prefix) || key[:len(prefix)] != prefix {
			continue
		}
		if _, ok := present[key]; !ok {
			delete(h.lastRendered, key)
		}
	}
}

// updateRendered reports whether v's JSON rendering differs from what was
// last stored under key, updating the stored value when it does. This is
// the hub's entire change-detection mechanism (contract section 6.5:
// "determined by comparing the resource's rendered wire representation to
// the last one published").
func (h *Hub) updateRendered(key string, v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		// Unreachable for a v1.Node/v1.FPPInstance built by this package's
		// own mapping functions (every field is a plain JSON-marshalable
		// type); treated as "no change" rather than crashing the hub.
		h.logger.Warn("stream hub: failed to render resource for change detection", "key", key, "error", err)
		return false
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	prev, ok := h.lastRendered[key]
	if ok && bytes.Equal(prev, b) {
		return false
	}
	h.lastRendered[key] = b
	return true
}

// renderNewEvents fetches events recorded since the last render pass and
// returns one event.recorded pendingFrame per event, in ascending seq
// order. See [hubEventsBatchLimit] for why a single pass may not catch up
// entirely, and why that is safe.
//
// The store prunes history, so ListEvents can report gap: true — this
// hub's own internal cursor fell so far behind (a long tick interval, a
// very aggressive retention policy) that one or more events were pruned
// before this hub ever read them. Unlike the REST events handler, which
// surfaces gap on the wire so a caller can see the missing interval (see
// [v1.EventsResponse]), the SSE stream has no "history gap" event type to
// produce — event.recorded is a live feed, not a paged history read, and
// contract section 6.4 already gives clients a distinct, deliberate
// mechanism for "your model may be incomplete, re-synchronize": a stream
// gap or overflow, not a per-event notification about entries that no
// longer exist to describe. So this method's only obligation on a gap is
// to stop retrying the pruned interval forever: it advances the internal
// cursor to the oldest row the store still retains, sacrificing the
// pruned events (which are gone regardless of what this hub does) rather
// than calling ListEvents with the same stale since on every future tick
// and always getting zero records back for a region that will never
// un-prune. The degenerate case — retention has pruned the events table
// down to nothing at all, so there is no "oldest row" to skip to either —
// is handled the same way, one step further: see the innermost switch
// case below for why jumping straight to latest is still correct there,
// and TestRenderNewEventsAdvancesCursorWhenHistoryIsFullyPruned in
// stream_test.go for the regression guard on the infinite-retry hazard
// this closes.
func (h *Hub) renderNewEvents(ctx context.Context, now time.Time) []pendingFrame {
	h.mu.Lock()
	since := h.lastEventSeq
	h.mu.Unlock()

	latest, err := h.deps.Events.LatestEventSeq(ctx)
	if err != nil {
		h.logger.Warn("stream hub: read latest event seq failed", "error", err)
		return nil
	}
	if latest <= since {
		return nil
	}

	records, gap, err := h.deps.Events.ListEvents(ctx, since, hubEventsBatchLimit)
	if err != nil {
		h.logger.Warn("stream hub: list events since failed", "error", err)
		return nil
	}

	frames := make([]pendingFrame, 0, len(records))
	newCursor := since
	for _, rec := range records {
		ev := mapEvent(rec)
		frames = append(frames, pendingFrame{event: "event.recorded", serverTime: formatTime(now), ev: &ev})
		if rec.Seq > newCursor {
			newCursor = rec.Seq
		}
	}

	if gap {
		switch oldest, ok, oerr := h.deps.Events.OldestEventSeq(ctx); {
		case oerr != nil:
			h.logger.Warn("stream hub: read oldest event seq failed after a reported gap", "error", oerr)
		case ok && oldest > 0 && oldest-1 > newCursor:
			newCursor = oldest - 1
		case !ok && latest > newCursor:
			// The events table currently holds no rows at all — every
			// event that ever existed up to latest has been pruned — so
			// OldestEventSeq has nothing to report (ok is false) and the
			// oldest-1 skip above cannot fire. Left alone, newCursor would
			// stay at since forever: latest (read from durable
			// bookkeeping that survives the table emptying, see
			// [store.Store.LatestEventSeq]'s doc comment) would still be
			// greater than since on every future tick, so this method
			// would call ListEvents with the SAME since, get gap: true
			// and zero records back, and retry identically forever — the
			// hazard finding 1.4 named. There is nothing further
			// ListEvents could ever return for this range (the table is
			// empty), so jump straight to latest: any event appended
			// after this point gets its own fresh seq beyond latest and
			// is picked up normally on a later tick.
			newCursor = latest
		}
	}

	h.mu.Lock()
	if newCursor > h.lastEventSeq {
		h.lastEventSeq = newCursor
	}
	h.mu.Unlock()

	return frames
}

// broadcast delivers pf to every current subscriber, non-blocking. A
// subscriber whose frames buffer is full has clearly fallen behind (its
// own ServeHTTP goroutine is not draining fast enough — a slow client, a
// stalled network write); rather than block this call (which would stall
// every other subscriber's delivery, and eventually the render loop
// itself) or silently drop the frame, it is handed a stream.reset reason
// through its capacity-1 reset channel, which ServeHTTP's loop picks up on
// its next iteration and uses to close that one connection. Every other
// subscriber is unaffected.
func (h *Hub) broadcast(pf pendingFrame) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, sub := range h.subscribers {
		select {
		case sub.frames <- pf:
		default:
			select {
			case sub.reset <- "subscriber_too_slow":
				// Logged, not silent: dropping a subscriber is a real
				// operational event (a client that cannot keep up, or a
				// peer that has stopped reading its socket), and the
				// client learns of it only through the stream.reset frame
				// it is about to be sent — which it may or may not still
				// be able to read. Without this line the coordinator
				// disconnects a client and keeps no record anywhere that
				// it did.
				h.logger.Warn("stream hub: subscriber buffer overflowed; sending stream.reset and disconnecting",
					"subscriber", id, "buffer_size", h.bufSize, "reason", "subscriber_too_slow")
			default:
				// Already signaled for this subscriber; it just hasn't
				// been torn down yet.
			}
		}
	}
}

func (h *Hub) subscribe() (id uint64, sub *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id = h.nextID
	h.nextID++
	sub = &subscriber{
		frames: make(chan pendingFrame, h.bufSize),
		reset:  make(chan string, 1),
	}
	h.subscribers[id] = sub
	return id, sub
}

func (h *Hub) unsubscribe(id uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subscribers, id)
}

// ServeHTTP implements the SSE endpoint, GET /api/v1/stream. Contract
// section 6.4's mechanics, all enforced here:
//
//   - stream.start is always the first event, before this handler enters
//     its main loop, carrying snapshotRequired: true.
//   - No "id:" line is ever written — see [writeSSE] — and any
//     Last-Event-ID request header is never read, anywhere in this method,
//     which is the actual enforcement; a browser or client that sends one
//     gets no different behavior than one that does not.
//   - seq starts at 1 for the first frame after stream.start and increments
//     per connection, never shared with any other subscriber or with
//     [EventRecord.Seq]'s durable history cursor.
//   - A keepalive comment is written on a fixed interval so an idle stream
//     still traverses intermediaries and a dead peer is detectable.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// internal/coordinator/httpapi.NewServer configures a WriteTimeout on
	// the *http.Server this handler is mounted on — a reasonable default
	// for its own ordinary REST-style probes — but net/http.Server.
	// WriteTimeout bounds the ENTIRE response-writing phase of one
	// request, reset only when that connection's NEXT request's headers
	// are read, which never happens here because this response is
	// intentionally never-ending. Left in place unmodified, every SSE
	// connection would be silently killed by the coordinator's own HTTP
	// server a few seconds after connecting — discovered only by actually
	// running the real binary and watching a real stream die
	// mid-keepalive, exactly the shape of defect this task's real-process
	// harness exists to catch (see TestStreamSurvivesServerWriteTimeout).
	//
	// [resetWriteDeadline] is called before every single write below
	// instead: it defeats httpapi's blanket timeout the same way an
	// unconditional clear would, but — unlike a clear — leaves a bounded
	// deadline of this handler's OWN choosing in place for each write,
	// closing finding 1.1 (see [streamWriteTimeout]'s doc comment for the
	// failure that fix closes).

	id, sub := h.subscribe()
	defer h.unsubscribe(id)

	start := v1.StreamStart{
		StreamID:         uuid.NewString(),
		APIVersion:       1,
		ServerTime:       formatTime(h.clock()),
		SnapshotRequired: true,
	}
	resetWriteDeadline(w)
	if !writeSSE(w, "stream.start", start) {
		return
	}
	flusher.Flush()

	keepalive := time.NewTicker(h.keepaliveInterval)
	defer keepalive.Stop()

	var seq uint64
	for {
		select {
		case <-r.Context().Done():
			return
		case <-h.done:
			return
		case reason := <-sub.reset:
			seq++
			resetWriteDeadline(w)
			writeSSE(w, "stream.reset", v1.StreamReset{
				Seq: seq, ServerTime: formatTime(h.clock()), Reason: reason, SnapshotRequired: true,
			})
			flusher.Flush()
			return
		case pf := <-sub.frames:
			seq++
			event, payload := pf.materialize(seq)
			resetWriteDeadline(w)
			if !writeSSE(w, event, payload) {
				return
			}
			flusher.Flush()
		case <-keepalive.C:
			resetWriteDeadline(w)
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeSSE writes one SSE frame: "event: <event>\ndata: <json>\n\n". It
// never writes an "id:" line — see the [Hub.ServeHTTP] doc comment for why
// that omission is load-bearing, not stylistic. It reports false on any
// write or encode failure, which callers treat as "the connection is
// gone, stop".
func writeSSE(w http.ResponseWriter, event string, payload any) bool {
	data, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return false
	}
	return true
}
