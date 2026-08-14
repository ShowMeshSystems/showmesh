package resolume

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Watcher holds one WebSocket connection to Resolume Arena open, reconnecting
// with backoff on any loss, and turns everything it reads into exactly three
// signals delivered through the callbacks on WatcherOptions: a connection
// came up, a connection went down, or something arrived that means the
// composition may have changed.
//
// Per the adapter specification §3.4, the WebSocket is a wake-up channel and
// never an authority: Watcher holds no observation, no parameter id, and no
// fact about the composition. It produces zero [pkg/observation.Observation]
// values and imports nothing from that package. Every OnChange callback means
// "go read the REST API again"; the read, not this component, is what
// produces evidence.
//
// Watcher never writes to Resolume. It has no subscribe, set, post, or
// remove path, deliberately: subscribing would key on a parameter id, and
// per §3.6 a parameter id is minted per session and must never be held
// across a reconnect. The absence of a write path is the enforcement
// mechanism, not a runtime check.
type Watcher struct {
	opts WatcherOptions

	mu    sync.Mutex
	stats WatcherStats

	// dialFailing and lastDialFailureLogAt are logging-only bookkeeping
	// for noteDialFailure's rate limit (review finding D, 2026-08-14) —
	// deliberately NOT part of WatcherStats, which stays diagnostics-only
	// per its own doc comment and is read via Stats(), not via logging
	// internals. Guarded by the same mu as stats.
	dialFailing          bool
	lastDialFailureLogAt time.Time
}

// WatcherOptions configures a [Watcher]. Zero-value backoff/timeout/limit
// fields take the documented defaults in [NewWatcher].
type WatcherOptions struct {
	// URL is the WebSocket endpoint, e.g. "ws://127.0.0.1:9080/api/v1".
	URL string

	// OnConnect fires after the connection is established, before Watcher
	// reads its first message. Its contract is "re-resolve everything from
	// a fresh REST read now": parameter ids are minted at composition load
	// and die on every restart with nothing announcing it (bench capture
	// §3.2), so every reconnect — including the first connect — invalidates
	// every parameter id this watcher's caller may have resolved.
	//
	// Called synchronously from Watcher's own connection-handling loop.
	// Arena pushes its entire composition unsolicited on connect, ~2.27 MB
	// per the capture's §5.1 — firing OnConnect before Watcher starts
	// reading means a caller's REST re-resolve is already in flight rather
	// than waiting behind that push draining. A caller that wants OnConnect
	// itself to not block Watcher's read loop should return quickly (e.g.
	// by starting its own goroutine); Watcher does not do that for it.
	OnConnect func(ctx context.Context)

	// OnDisconnect fires when the connection is lost, for any reason,
	// including a clean shutdown via ctx cancellation. Its contract is
	// "drop everything you resolved" — every held parameter id is now
	// invalid (§3.2), and the next OnConnect is the only signal that
	// resolution is safe to redo.
	OnDisconnect func(ctx context.Context)

	// OnChange fires when a message arrives that means the composition may
	// have changed. It carries NO payload, deliberately: per §3.4 no
	// observed value is ever taken from a WebSocket message, so there is
	// nothing to hand the caller except "go read again."
	OnChange func(ctx context.Context)

	// Logger receives connection lifecycle and diagnostic events. Defaults
	// to slog.Default() if nil.
	Logger *slog.Logger

	// Now returns the current time, for ConnectedSince/LastErrorAt and for
	// tests. Defaults to time.Now if nil.
	Now func() time.Time

	// MinBackoff is the initial (and floor) wait between a lost connection
	// and the next reconnect attempt. Defaults to 500ms if zero or
	// negative.
	MinBackoff time.Duration

	// MaxBackoff caps the reconnect wait after repeated exponential
	// doubling. Defaults to 30s if zero or negative.
	MaxBackoff time.Duration

	// HandshakeTimeout bounds the WebSocket upgrade handshake. Defaults to
	// 10s if zero or negative.
	HandshakeTimeout time.Duration

	// ReadLimitBytes caps the size of one WebSocket message. Exceeding it
	// aborts the connection (counted as an error, triggering reconnect),
	// never treated as an observation. Defaults to 64 MiB if zero or
	// negative — see defaultReadLimitBytes for why that number.
	ReadLimitBytes int64
}

// WatcherStats is diagnostics for a log line and a future dashboard. It is
// NOT evidence and must never be turned into an observation or any other
// value this system treats as a fact about the show: it describes the
// health of this watcher's own TCP/WebSocket connection to Resolume, which
// per the adapter specification §3.4 is a wake-up channel, never an
// authority on composition state. Whether the composition itself is in any
// particular state is answered only by a REST read triggered from OnChange,
// never by anything counted here.
type WatcherStats struct {
	// Connected is whether the WebSocket is currently up. Even true tells
	// you nothing about the composition — see the type doc comment.
	Connected bool

	// ConnectedSince is when the current connection (if Connected) was
	// established. Zero if never connected.
	ConnectedSince time.Time

	// Connects and Disconnects count lifecycle transitions since the
	// Watcher was created. Connects increments only on a SUCCESSFUL dial
	// (an established connection). Disconnects increments every time an
	// established connection ENDS — whether from a real error or a clean
	// ctx-cancelled shutdown — so the two counters describe connection
	// churn symmetrically. A dial that never succeeded is counted in
	// DialFailures instead, never here: a connection that was never
	// established cannot "disconnect" (review finding D, 2026-08-14 —
	// before this fix, a dial failure incorrectly incremented Disconnects,
	// and a clean ctx-cancelled teardown of an established connection
	// incorrectly did NOT, so this counter was wrong in both directions).
	Connects    int64
	Disconnects int64

	// DialFailures counts a dial attempt that failed to establish a
	// connection at all — distinct from Disconnects, which requires a
	// connection to have existed first. See Connects/Disconnects' own doc
	// comment for why the two must not be conflated.
	DialFailures int64

	// FullPushes counts untyped messages classified as the full
	// composition push (§5.1). TypedMessages counts EVERY message
	// carrying a top-level "type" field, regardless of which type — this
	// includes both known change-worthy types and unrecognized ones.
	// UnrecognizedTypedMessages is the subset of TypedMessages whose
	// "type" value is neither a known change-worthy type
	// (knownChangeMessageTypes) nor one of the enumerated exclusions
	// (excludedMessageTypes) — see runConnection's own comment for why an
	// unrecognized type still wakes a caller rather than being silently
	// dropped. AmbiguousMessages counts messages the discriminator could
	// not resolve within its bounded prefix — see classifyAndDrain.
	FullPushes                int64
	TypedMessages             int64
	UnrecognizedTypedMessages int64
	AmbiguousMessages         int64

	// LastError is the most recent connection-level error (dial failure,
	// read failure, read-limit exceeded), or "" if none yet. It is never
	// set for an ordinary shutdown via ctx cancellation. LastErrorAt is its
	// timestamp.
	LastError   string
	LastErrorAt time.Time
}

// statsLogAttrs renders a WatcherStats snapshot as slog key/value pairs.
// Review finding D (2026-08-14): WatcherStats was read by no production
// code at all — grep for ".Stats()" outside this package's own tests found
// only an unrelated probe binary — so a dead wake-up channel could sit
// beside a healthy-looking resolume.reachable observation with no log
// line, no observation, and no reader for the only counter that knew. This
// function is what makes every counter this type documents actually
// surface: it is appended to the connect and disconnect log lines below,
// which is this file's own chosen answer to "make WatcherStats actually
// read." In particular, the AmbiguousMessages field's own doc comment
// claims that counting it separately from the others "is what makes a
// wrong bound findable in the field" — that claim is only true if
// something prints it, which this function is what does.
func statsLogAttrs(s WatcherStats) []any {
	return []any{
		"connects", s.Connects,
		"disconnects", s.Disconnects,
		"dial_failures", s.DialFailures,
		"full_pushes", s.FullPushes,
		"typed_messages", s.TypedMessages,
		"unrecognized_typed_messages", s.UnrecognizedTypedMessages,
		"ambiguous_messages", s.AmbiguousMessages,
	}
}

const (
	defaultMinBackoff       = 500 * time.Millisecond
	defaultMaxBackoff       = 30 * time.Second
	defaultHandshakeTimeout = 10 * time.Second

	// defaultReadLimitBytes bounds one WebSocket message at 64 MiB. The
	// full composition push measured in the bench capture
	// (docs/bench/resolume-control-surface.md §4.1, 2026-08-14, arm64
	// laptop, loopback, 252-slot composition) was 2.27 MB. 64 MiB carries
	// roughly 28x headroom over that one measured composition — a margin,
	// not a promise about every composition's size. Exceeding it aborts
	// the connection as a counted error and triggers a reconnect; it is
	// never treated as an observation.
	defaultReadLimitBytes = 64 << 20
)

// knownChangeMessageTypes are the WebSocket message "type" values (bench
// capture §5.2) Watcher has POSITIVELY identified as meaning the
// composition may have changed. parameter_update, parameter_set and
// parameter_get can all carry a change to composition-affecting state (a
// clip's connected state, a layer's master, etc.) so each wakes a
// re-read.
//
// This list no longer decides whether an unrecognized type wakes a
// caller — see excludedMessageTypes and runConnection's own comment for
// why review finding E (2026-08-14) inverted that decision. It exists
// only so WatcherStats.UnrecognizedTypedMessages can tell "a type this
// package has positively identified" from "a type it has never seen",
// which is a diagnostics distinction, not a wake/no-wake one.
var knownChangeMessageTypes = map[string]bool{
	"parameter_update": true,
	"parameter_set":    true,
	"parameter_get":    true,
}

// excludedMessageTypes are the ONLY WebSocket "type" values (bench capture
// §5.1-§5.2) Watcher treats as NOT meaning "the composition may have
// changed": sources_update and effects_update describe the sources/effects
// browser panel, thumbnail_update describes a clip's preview image, and
// parameter_subscribed only acknowledges a subscribe request Watcher never
// sends — none of the four describes composition structure or a
// parameter's value.
//
// Review finding E (2026-08-14): this used to be phrased as an ALLOW list
// (changeMessageTypes) rather than this DENY list, which meant a future
// Arena release renaming parameter_update, or adding an entirely new
// structural-change type, would silently stop waking anything — the
// opposite of the ambiguous-message branch two cases below, which already
// fires OnChange specifically because "a missed wake-up is a silent stale
// reading, which is the exact shape ADR-020 refuses to trust in
// ShowMesh's own change stream." An unrecognized type now gets the
// identical treatment: it wakes, and is counted separately
// (UnrecognizedTypedMessages) so the boundary is still visible and
// adjustable, per this map's own predecessor's reasoning, without ever
// being able to go silently stale. Keep this list to exactly the four
// types the capture enumerated as SAFE to ignore; everything not on it
// wakes.
var excludedMessageTypes = map[string]bool{
	"sources_update":       true,
	"effects_update":       true,
	"thumbnail_update":     true,
	"parameter_subscribed": true,
}

// NewWatcher validates opts and applies documented defaults to any
// zero-value field. It does not connect; call Run to do that.
func NewWatcher(opts WatcherOptions) (*Watcher, error) {
	if opts.URL == "" {
		return nil, errors.New("resolume: WatcherOptions.URL is required")
	}
	if opts.MinBackoff <= 0 {
		opts.MinBackoff = defaultMinBackoff
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = defaultMaxBackoff
	}
	if opts.MinBackoff > opts.MaxBackoff {
		return nil, fmt.Errorf("resolume: MinBackoff (%s) exceeds MaxBackoff (%s)", opts.MinBackoff, opts.MaxBackoff)
	}
	if opts.HandshakeTimeout <= 0 {
		opts.HandshakeTimeout = defaultHandshakeTimeout
	}
	if opts.ReadLimitBytes <= 0 {
		opts.ReadLimitBytes = defaultReadLimitBytes
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Watcher{opts: opts}, nil
}

// Run connects, reads, and reconnects with capped exponential backoff plus
// jitter until ctx is done. It returns only after the connection is closed
// and no goroutine of its own is left running.
//
// No heartbeat, keepalive, or read deadline is implemented: the bench
// capture (§5.1, §5.5) found no server-initiated heartbeat and an abrupt
// loss indistinguishable in-band from a quiet connection (close code 1006,
// no close frame). Resolume can legitimately be silent for hours; Run never
// treats silence itself as evidence of anything, in either direction. Only
// an actual read/dial error (including a closed TCP connection) triggers a
// reconnect.
func (w *Watcher) Run(ctx context.Context) {
	backoff := w.opts.MinBackoff
	for ctx.Err() == nil {
		conn, err := w.dial(ctx)
		if err != nil {
			if ctx.Err() == nil {
				w.noteDialFailure(err)
			}
			if !w.wait(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff, w.opts.MaxBackoff)
			continue
		}

		backoff = w.opts.MinBackoff
		w.runConnection(ctx, conn)

		if ctx.Err() != nil {
			return
		}
		if !w.wait(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff, w.opts.MaxBackoff)
	}
}

// Stats returns a snapshot of the watcher's connection diagnostics. See the
// WatcherStats doc comment: this is not evidence about Resolume's state.
func (w *Watcher) Stats() WatcherStats {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stats
}

func (w *Watcher) dial(ctx context.Context) (*websocket.Conn, error) {
	dialer := websocket.Dialer{
		HandshakeTimeout: w.opts.HandshakeTimeout,
	}
	conn, resp, err := dialer.DialContext(ctx, w.opts.URL, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(w.opts.ReadLimitBytes)
	return conn, nil
}

// runConnection owns one live connection end to end: it fires OnConnect,
// reads and classifies messages until the connection fails or ctx is done,
// then fires OnDisconnect. It spawns exactly one internal goroutine (to
// force-close the connection promptly on ctx cancellation, since a blocking
// WebSocket read does not otherwise observe ctx) and joins it before
// returning, so Run — which calls this synchronously — leaves no goroutine
// of Watcher's own running once it returns.
func (w *Watcher) runConnection(ctx context.Context, conn *websocket.Conn) {
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	closed := make(chan struct{})
	var closer sync.WaitGroup
	closer.Add(1)
	go func() {
		defer closer.Done()
		select {
		case <-connCtx.Done():
			_ = conn.Close()
		case <-closed:
		}
	}()
	defer func() {
		close(closed)
		closer.Wait()
	}()

	w.markConnected()
	w.opts.Logger.Info("resolume watcher connected", statsLogAttrs(w.Stats())...)
	if w.opts.OnConnect != nil {
		w.opts.OnConnect(ctx)
	}

	var lastErr error
	for {
		_, r, err := conn.NextReader()
		if err != nil {
			lastErr = err
			break
		}

		result, err := classifyAndDrain(r, discriminatorPrefixBytes)
		if err != nil {
			lastErr = err
			break
		}

		switch result.kind {
		case kindFullPush:
			w.incFullPush()
			w.fireOnChange(ctx)
		case kindTyped:
			w.incTyped()
			switch {
			case excludedMessageTypes[result.typeValue]:
				// One of the four enumerated-safe-to-ignore types
				// (excludedMessageTypes' own doc comment) — deliberately
				// no wake.
			case knownChangeMessageTypes[result.typeValue]:
				w.fireOnChange(ctx)
			default:
				// Review finding E (2026-08-14): an UNRECOGNIZED type —
				// neither positively known as change-worthy nor on the
				// enumerated exclusion list — wakes, on the identical
				// reasoning the ambiguous branch below already applies: a
				// missed wake-up is a silent stale reading, and a future
				// Arena release renaming or adding a type must not go
				// silently unnoticed. Counted separately
				// (UnrecognizedTypedMessages) so the boundary stays
				// visible instead of hiding inside TypedMessages.
				w.incUnrecognizedTyped()
				w.fireOnChange(ctx)
			}
		default:
			// Ambiguous: the prefix ended without resolving to either a
			// typed message or a full push. Fire OnChange anyway. A
			// wake-up costs one cheap REST read; a missed wake-up is a
			// silent stale reading, which is the exact shape ADR-020
			// refuses to trust in ShowMesh's own change stream. Counting
			// it separately (AmbiguousMessages) rather than folding it
			// into TypedMessages or FullPushes is what makes a wrong
			// discriminatorPrefixBytes bound findable in the field instead
			// of silently costing extra REST reads forever.
			w.incAmbiguous()
			w.fireOnChange(ctx)
		}
	}

	// Review finding D (2026-08-14): Disconnects increments here
	// UNCONDITIONALLY — whether this connection ended in a real error or a
	// clean ctx-cancelled shutdown — because a connection was established
	// (runConnection is only ever called with one) and now is not; see
	// WatcherStats.Connects/Disconnects' own doc comment for why that must
	// not be conflated with a dial that never succeeded (DialFailures,
	// counted in Run's own dial branch, never here). LastError/LastErrorAt
	// stay error-only, unchanged from before this fix.
	if lastErr != nil && ctx.Err() == nil {
		w.recordError(lastErr)
		w.incDisconnects()
		w.opts.Logger.Warn("resolume watcher disconnected", append([]any{"error", lastErr}, statsLogAttrs(w.Stats())...)...)
	} else {
		w.incDisconnects()
		w.opts.Logger.Info("resolume watcher disconnected", statsLogAttrs(w.Stats())...)
	}
	w.markDisconnected()
	if w.opts.OnDisconnect != nil {
		w.opts.OnDisconnect(ctx)
	}
}

func (w *Watcher) wait(ctx context.Context, base time.Duration) bool {
	// Full jitter in [base/2, base): avoids every disconnected watcher in
	// a fleet retrying in lockstep, without ever waiting less than half
	// the nominal backoff.
	half := base / 2
	d := half
	if half > 0 {
		d += rand.N(half)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func nextBackoff(cur, max time.Duration) time.Duration {
	next := cur * 2
	if next <= 0 || next > max {
		next = max
	}
	return next
}

func (w *Watcher) markConnected() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stats.Connected = true
	w.stats.ConnectedSince = w.opts.Now()
	w.stats.Connects++
	// A successful connect ends whatever dial-failure streak was in
	// progress, so the next failure (if any) is once again treated as the
	// FIRST failure of a new streak — see noteDialFailure's own doc
	// comment for what that controls.
	w.dialFailing = false
}

func (w *Watcher) markDisconnected() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stats.Connected = false
}

// recordError records the most recent connection-level error for
// diagnostics (LastError/LastErrorAt). It does NOT touch any lifecycle
// counter — Connects/Disconnects/DialFailures are each owned by their own
// dedicated method (incDisconnects, noteDialFailure) precisely so this
// function cannot accidentally conflate "an error happened" with "a
// connection ended" or "a dial failed", which is what review finding D
// found wrong about this method's previous, combined form.
func (w *Watcher) recordError(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stats.LastError = err.Error()
	w.stats.LastErrorAt = w.opts.Now()
}

// noteDialFailure records a failed dial attempt (no connection was ever
// established — see WatcherStats.DialFailures' own doc comment for why
// this must not increment Disconnects) and logs it at WARN, rate-limited
// per review finding D (2026-08-14): "log a dial failure... first failure
// after a connected period at WARN... then rate-limited so a long outage
// does not become a log storm — one line per backoff cap is enough."
//
// A WARN is emitted when this is the FIRST dial failure since the last
// successful connect (markConnected resets the streak), or when at least
// opts.MaxBackoff has elapsed since the last logged dial failure —
// approximating "one line per backoff cap" without coupling this method
// to Run's own live backoff value, which would need a lock ordering this
// method has no reason to take on.
func (w *Watcher) noteDialFailure(err error) {
	w.mu.Lock()
	w.stats.LastError = err.Error()
	w.stats.LastErrorAt = w.opts.Now()
	w.stats.DialFailures++

	now := w.opts.Now()
	shouldLog := false
	if !w.dialFailing {
		w.dialFailing = true
		shouldLog = true
	} else if now.Sub(w.lastDialFailureLogAt) >= w.opts.MaxBackoff {
		shouldLog = true
	}
	if shouldLog {
		w.lastDialFailureLogAt = now
	}
	stats := w.stats
	w.mu.Unlock()

	if shouldLog {
		w.opts.Logger.Warn("resolume watcher dial failed", append([]any{"error", err}, statsLogAttrs(stats)...)...)
	}
}

func (w *Watcher) incFullPush() {
	w.mu.Lock()
	w.stats.FullPushes++
	w.mu.Unlock()
}

func (w *Watcher) incTyped() {
	w.mu.Lock()
	w.stats.TypedMessages++
	w.mu.Unlock()
}

func (w *Watcher) incUnrecognizedTyped() {
	w.mu.Lock()
	w.stats.UnrecognizedTypedMessages++
	w.mu.Unlock()
}

func (w *Watcher) incAmbiguous() {
	w.mu.Lock()
	w.stats.AmbiguousMessages++
	w.mu.Unlock()
}

func (w *Watcher) incDisconnects() {
	w.mu.Lock()
	w.stats.Disconnects++
	w.mu.Unlock()
}

func (w *Watcher) fireOnChange(ctx context.Context) {
	if w.opts.OnChange != nil {
		w.opts.OnChange(ctx)
	}
}

// --- discrimination -------------------------------------------------------

type messageKind int

const (
	kindAmbiguous messageKind = iota
	kindTyped
	kindFullPush
)

type classification struct {
	kind      messageKind
	typeValue string
}

// discriminatorPrefixBytes bounds how much of one WebSocket message
// classifyAndDrain will parse looking for a depth-1 "type" key (typed
// message) or a depth-1 "layers"/"columns" key with no "type" (full
// composition push). Every typed-message sample in the bench capture
// (docs/bench/resolume-control-surface.md §5.1–§5.2) carries "type" as
// effectively the first key, well under 200 bytes in; every full-push
// sample carries "layers"/"columns" at or near the start of the same
// top-level object. 8 KiB is generous headroom over that — about 35x the
// smallest observed typed message (parameter_update, 233 bytes per §5.3) —
// while staying under 0.4% of the measured 2.27 MB full push it exists to
// avoid decoding.
const discriminatorPrefixBytes = 8192

// classifyAndDrain answers "does this WebSocket message mean the
// composition may have changed" without ever decoding the message into a
// Go value and without ever buffering more than discriminatorPrefixBytes of
// it in memory at once.
//
// It scans only the depth-1 keys of the top-level JSON object, which the
// bench capture's §5.1 establishes is always what a Resolume WebSocket
// message is: a JSON object, either carrying "type" (a typed message,
// per §5.2) or carrying "layers"/"columns" with no "type" (the untyped full
// composition push). Scanning is done with encoding/json's token reader
// wrapped around an io.LimitReader, so the JSON decoder itself is
// structurally incapable of pulling more than discriminatorPrefixBytes out
// of r — this is not a runtime check, it is what io.LimitReader guarantees.
//
// Once classification is resolved (or the prefix is exhausted without
// resolving it — see kindAmbiguous), whatever remains of the message is
// drained with io.Copy(io.Discard, r), which reads and discards in bounded
// chunks rather than ever holding the tail of a 2.27 MB push in a []byte.
//
// The returned error is non-nil only for a genuine failure to read from r
// (e.g. the underlying connection broke mid-message); a message whose shape
// the scanner cannot resolve within the prefix is not an error, it is
// classification{kind: kindAmbiguous}.
func classifyAndDrain(r io.Reader, prefixBytes int64) (classification, error) {
	dec := json.NewDecoder(io.LimitReader(r, prefixBytes))

	kind := kindAmbiguous
	typeValue := ""
	lastKey := ""
	depth := 0
	awaitingKey := false

scan:
	for {
		tok, err := dec.Token()
		if err != nil {
			// Prefix exhausted, or the object closed within the prefix
			// without resolving either shape: either way, not a read
			// failure on r — just an unresolved classification. See the
			// doc comment: only the final drain's error is reported to
			// the caller as a real error.
			break scan
		}

		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{', '[':
				depth++
				if depth == 1 {
					awaitingKey = true
				}
			case '}', ']':
				depth--
				switch depth {
				case 1:
					awaitingKey = true
				case 0:
					break scan // top-level object closed; nothing left to learn
				}
			}
		default:
			if depth == 1 {
				if awaitingKey {
					if key, ok := v.(string); ok {
						lastKey = key
						switch key {
						case "type":
							kind = kindTyped
						case "layers", "columns":
							if kind != kindTyped {
								kind = kindFullPush
							}
							break scan // full push: recognised, never unmarshalled
						}
					}
					awaitingKey = false
				} else {
					if lastKey == "type" {
						if s, ok := v.(string); ok {
							typeValue = s
						}
						break scan // typed message: type value captured
					}
					awaitingKey = true
				}
			}
		}
	}

	// Drain the remainder of the message regardless of how the scan ended.
	// For a full push this is ~2.27 MB read in Discard's fixed-size chunks,
	// never held as one value. For a typed message it is whatever the
	// message carries past its "type" field, which this component never
	// looks at either.
	if _, err := io.Copy(io.Discard, r); err != nil {
		return classification{}, err
	}

	switch kind {
	case kindTyped:
		return classification{kind: kindTyped, typeValue: typeValue}, nil
	case kindFullPush:
		return classification{kind: kindFullPush}, nil
	default:
		return classification{kind: kindAmbiguous}, nil
	}
}
