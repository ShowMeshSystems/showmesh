package agent

import (
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// commandPublishTimeout bounds a single result (or agent-echo observation)
// publish attempt, matching heartbeat.go's heartbeatPublishTimeout pattern:
// a hung publish must not wedge command handling.
const commandPublishTimeout = 5 * time.Second

// confirmationMethodEvidence is the wire value for pkg/command.
// ConfirmationEvidence, the only confirmation method this codebase
// implements. This package does not import pkg/command (see
// mqttproto.CmdPayload.ConfirmationMethod's doc comment on why every
// wire-boundary package in this codebase defines its own independent,
// JSON-tagged types), so this literal must be kept in sync with
// pkg/command.ConfirmationEvidence's value by convention — the same way
// cmd/showmeshctl's own doc comments describe reconciling
// independently-chosen values without a shared import.
const confirmationMethodEvidence = "evidence"

// agentIdempotencyCacheCapacity bounds how many distinct idempotency keys
// [idempotencyCache] remembers before evicting the oldest.
//
// SHOWMESH HYPOTHESIS, NOT MEASURED: chosen only to be comfortably larger
// than the number of in-flight or recently-redelivered commands a single
// agent should ever plausibly see at once, the same class of guess
// [maxCapabilityCount] in pkg/mqttproto/payload.go already is. Widen it if
// a real deployment needs more.
const agentIdempotencyCacheCapacity = 512

// OperationResult is what an allowlisted operation reports after it has
// both applied a change AND collected fresh evidence of the result — two
// distinct steps, even when (as for this seam's one trivial operation)
// they happen in the same call, because ADR-003 requires evidence
// collected after the change, not an assumption that the write succeeded.
// A past defect in this project reported a command "confirmed" 179
// microseconds after its own dispatch by comparing against a stale
// pre-dispatch reading; OperationResult exists so that mistake cannot be
// reintroduced silently — Confirmed must be computed from a value actually
// read back after the write, not merely assumed from the write returning
// without error.
//
// A future operation whose effect is asynchronous (e.g. a GStreamer
// pipeline reaching PLAYING) must keep this same two-step shape but poll
// for its own evidence rather than reusing this synchronous one.
type OperationResult struct {
	// Confirmed reports whether the post-write read-back matched what was
	// requested. false does not mean the operation errored (that is what
	// OperationFunc's own error return is for) — it means the write was
	// attempted and the evidence collected afterward does not corroborate
	// it, which HandleMessage reports as OutcomeUnconfirmed rather than
	// OutcomeFailed.
	Confirmed bool

	// Signal names what was observed, matching
	// mqttproto.ResultEvidence.Signal.
	Signal string

	// Value is what was observed on read-back.
	Value any

	// ExecutedAt is when the operation actually applied its change (the
	// write), matching mqttproto.ResultPayload.ExecutedAt.
	//
	// ExecutedAt and ObservedAt are deliberately TWO SEPARATE fields, not
	// one value reused for both, even though for this seam's one
	// synchronous operation they are only microseconds apart: "when the
	// change was applied" and "when the evidence corroborating it was
	// collected" are different facts that only happen to coincide here.
	// This is the same shape as ADR-011's standing rule against ever
	// defaulting an observation's time to its collection time — conflating
	// the two is exactly the mistake that rule exists to prevent, one
	// layer up. A future operation whose effect is asynchronous (e.g. a
	// GStreamer pipeline reaching PLAYING, polled for after being started)
	// is precisely the case where ExecutedAt (when the pipeline was told
	// to start) and ObservedAt (when a poll finally observed PLAYING) will
	// visibly diverge, and must not be collapsed back into one field to
	// make that case easier to write.
	ExecutedAt time.Time

	// ObservedAt is when the read-back evidence was true.
	ObservedAt time.Time
}

// OperationFunc executes one allowlisted action. now is injected, matching
// this package's clock-injection convention elsewhere (see heartbeat.go),
// so tests do not depend on real time.
type OperationFunc func(ctx context.Context, params map[string]any, now func() time.Time) (OperationResult, error)

// agentEchoState is the entire state behind this seam's one allowlisted
// operation, "agent.echo": a value and when it was last applied, guarded
// by a mutex so concurrent HandleMessage calls (one per inbound MQTT
// PUBLISH — see mqtt.go's AddOnPublishReceived wiring, which handles each
// message in its own goroutine) cannot race each other.
type agentEchoState struct {
	mu        sync.Mutex
	value     string
	appliedAt time.Time
}

// current re-acquires the lock and returns whatever is actually stored,
// independent of whatever value a caller most recently wrote. It exists as
// its own method, called as a separate step AFTER [agentEchoState.apply]'s
// write, specifically so a reviewer (or a future editor) can see that the
// evidence backing OperationResult.Confirmed is a genuine read-back and
// not merely the write's own input echoed back unchanged — see
// OperationResult's doc comment for why that distinction is load-bearing
// here, not decorative.
func (s *agentEchoState) current() (value string, appliedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value, s.appliedAt
}

// apply is the OperationFunc for "agent.echo": store params["value"] (a
// string; missing or any other type is a caller error, reported by
// returning a non-nil error whose message becomes the eventual
// ResultPayload's Reason on OutcomeFailed), then read the stored value
// back via [agentEchoState.current] — a distinct, separately-coded step —
// and report Confirmed only if the read-back matches what was requested.
func (s *agentEchoState) apply(_ context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
	raw, ok := params["value"]
	if !ok {
		return OperationResult{}, fmt.Errorf("agent.echo: params.value is required")
	}
	value, ok := raw.(string)
	if !ok {
		return OperationResult{}, fmt.Errorf("agent.echo: params.value must be a string, got %T", raw)
	}

	appliedAt := now()
	s.mu.Lock()
	s.value = value
	s.appliedAt = appliedAt
	s.mu.Unlock()

	// Explicit, separately-coded read-back: this is NOT "return what was
	// just written" — current() re-acquires the lock and reads whatever is
	// actually stored, so a bug that wrote the wrong value (or a race with
	// another apply call) shows up as Confirmed: false instead of being
	// papered over by re-asserting the caller's own input.
	readBack, readBackAt := s.current()

	return OperationResult{
		Confirmed: readBack == value,
		Signal:    "node.agent.echo_value",
		Value:     readBack,
		// ExecutedAt is appliedAt (when the write happened); ObservedAt is
		// readBackAt (when the read-back evidence was collected) — two
		// separate clock reads from two separate steps, kept as two
		// separate fields per OperationResult's own doc comment, even
		// though for this synchronous operation they differ by a matter of
		// microseconds.
		ExecutedAt: appliedAt,
		ObservedAt: readBackAt,
	}, nil
}

// newOperationRegistry returns this agent's entire command allowlist:
// "agent.echo", "asset.fetch", and Track B seam B2a's three render.*
// operations. Per ARCHITECTURE section 10.4 ("agents accept only
// allowlisted operations"), this map itself IS the enforcement mechanism —
// [CommandHandler.HandleMessage] refuses any Action that is not a key here,
// never executes it, and never silently ignores it. assetDir and
// assetAPIToken configure "asset.fetch" (see assets.go); render configures
// the three render.* operations (see renderops.go). Adding a further
// allowlisted operation later means adding a further entry to this map, not
// building a second enforcement path.
func newOperationRegistry(assetDir, assetAPIToken string, render *renderOperations) map[string]OperationFunc {
	state := &agentEchoState{}
	fetch := assetFetchOperation{dir: assetDir, token: assetAPIToken}
	ops := map[string]OperationFunc{
		"agent.echo":  state.apply,
		"asset.fetch": fetch.run,
	}
	if render != nil {
		ops["render.surface.apply"] = render.applySurface
		ops["render.surface.clear"] = render.clearSurface
		ops["render.pipeline.restart"] = render.restartPipeline
	}
	return ops
}

// idempotencyCacheEntry is one bounded-cache slot: the idempotency key it
// was stored under (kept alongside the value so eviction can remove the
// matching map entry) and the exact ResultPayload a redelivery of that key
// must receive back, verbatim, without re-executing anything.
type idempotencyCacheEntry struct {
	key    string
	result mqttproto.ResultPayload
}

// inFlight tracks one idempotency key between the moment a goroutine
// claims the exclusive right to execute it and the moment that execution
// completes. Every concurrent caller that finds a key already in-flight
// blocks on done (never spins, never polls) and, once it is closed, reads
// result — safe without its own lock because complete() (below) writes
// result strictly before closing done, and Go's memory model guarantees a
// channel close happens-before the corresponding receive unblocks, which
// happens-before any read that follows it.
type inFlight struct {
	done   chan struct{}
	result mqttproto.ResultPayload
}

// idempotencyCache remembers the ResultPayload produced for each
// idempotency key this agent has already resolved, bounded to
// [agentIdempotencyCacheCapacity] completed entries by evicting the oldest
// (a plain bounded FIFO, not an access-order LRU: a key that keeps getting
// redelivered is exactly the case that must NOT be treated as "recently
// used" and kept alive indefinitely — it is a symptom of something
// upstream retrying, not a reason to grow this cache's effective
// lifetime). This is what makes ADR-008's "QoS 1 + idempotency keys so a
// redelivered command executes exactly once" true.
//
// "Executes exactly once" is a claim about CONCURRENT deliveries, not only
// sequential ones: mqtt.go spawns HandleMessage in its own goroutine per
// inbound PUBLISH, so two deliveries of the same idempotency key arriving
// close together (a genuine QoS 1 redelivery racing its original, or any
// other concurrent double-delivery) are genuinely concurrent calls into
// this cache. A plain "check the map, then execute, then store" sequence —
// two separate lock acquisitions with no claim in between — has a
// check-then-act race: both goroutines can observe a miss before either
// one has stored anything, and both execute. [idempotencyCache.
// claimOrAwait] and [idempotencyCache.complete] close that race by making
// "check, and if absent atomically claim the key as in-flight" one
// operation performed under a single mu.Lock().
type idempotencyCache struct {
	mu       sync.Mutex
	capacity int
	entries  map[string]*list.Element // completed results, FIFO-bounded
	order    *list.List
	inFlight map[string]*inFlight // keys currently being executed
}

func newIdempotencyCache(capacity int) *idempotencyCache {
	return &idempotencyCache{
		capacity: capacity,
		entries:  make(map[string]*list.Element),
		order:    list.New(),
		inFlight: make(map[string]*inFlight),
	}
}

// claimOrAwait is this cache's one entry point for the idempotency
// decision. It returns (result, true) when key was already resolved —
// either found in the completed cache immediately, or found in-flight and
// waited for — meaning the caller must NOT execute and should republish
// result verbatim. It returns (zero value, false) when THIS call is the
// one that must execute: it atomically claims key as in-flight, under the
// same lock as the "is it already known" check, so no concurrent caller
// can also receive false for the same key. A caller that receives false
// MUST call [idempotencyCache.complete] for key exactly once afterward, on
// every code path (including a refusal or a failure) — that call is what
// releases any concurrent waiters and is the only way this key's in-flight
// claim is ever cleared.
func (c *idempotencyCache) claimOrAwait(key string) (mqttproto.ResultPayload, bool) {
	c.mu.Lock()
	if el, ok := c.entries[key]; ok {
		result := el.Value.(*idempotencyCacheEntry).result
		c.mu.Unlock()
		return result, true
	}
	if inf, ok := c.inFlight[key]; ok {
		c.mu.Unlock()
		<-inf.done
		return inf.result, true
	}
	c.inFlight[key] = &inFlight{done: make(chan struct{})}
	c.mu.Unlock()
	return mqttproto.ResultPayload{}, false
}

// complete records result as key's resolved outcome (evicting the single
// oldest completed entry once the cache is over capacity — overwriting an
// existing entry in place does NOT change its eviction order; this cache
// is not an LRU, see the type's doc comment), releases key's in-flight
// claim, and closes its done channel so every concurrent caller currently
// blocked in claimOrAwait's in-flight wait unblocks with this exact
// result. Must be called exactly once per key that claimOrAwait returned
// false for.
func (c *idempotencyCache) complete(key string, result mqttproto.ResultPayload) {
	c.mu.Lock()
	inf := c.inFlight[key]
	delete(c.inFlight, key)

	if el, ok := c.entries[key]; ok {
		el.Value.(*idempotencyCacheEntry).result = result
	} else {
		el := c.order.PushBack(&idempotencyCacheEntry{key: key, result: result})
		c.entries[key] = el

		for c.order.Len() > c.capacity {
			oldest := c.order.Front()
			if oldest == nil {
				break
			}
			c.order.Remove(oldest)
			delete(c.entries, oldest.Value.(*idempotencyCacheEntry).key)
		}
	}
	c.mu.Unlock()

	if inf != nil {
		// Write result, THEN close done: this ordering is what makes an
		// unsynchronized read of inf.result safe on the receiving side of
		// claimOrAwait's <-inf.done — see [inFlight]'s doc comment.
		inf.result = result
		close(inf.done)
	}
}

// CommandHandler receives, allowlist-checks, dispatches, and reports the
// outcome of commands sent to one node's cmd topic — this seam's entire
// receive -> allowlist -> execute -> evidence -> report path. See
// [HandleMessage] for the exact decision sequence.
type CommandHandler struct {
	nodeID string
	ops    map[string]OperationFunc
	cache  *idempotencyCache
	now    func() time.Time
	logger *slog.Logger

	// assetFetchTrigger, when non-nil, is signalled (non-blockingly, like
	// mqtt.go's heartbeatConnected) after a genuinely-executed "asset.fetch"
	// completes, so assetinventory.go's publisher can republish immediately
	// instead of waiting for its next tick — the seam's own "confirmation
	// needs evidence that post-dates the action" requirement applied to a
	// sync, not just a single command.
	assetFetchTrigger chan<- struct{}

	// renderTrigger, when non-nil, is signalled after any genuinely-executed
	// "render.*" operation completes, so renderreport.go's publisher can
	// republish immediately instead of waiting for its next tick — matching
	// assetFetchTrigger's identical purpose, one seam over.
	renderTrigger chan<- struct{}
}

// newCommandHandler builds a CommandHandler for nodeID, wiring
// [newOperationRegistry]'s allowlist (configured with assetDir and
// assetAPIToken for "asset.fetch", and render for the three render.*
// operations — nil disables them, which every test in this package that
// does not exercise rendering does) and a fresh [idempotencyCache]. It
// takes no Publisher: mqtt.go constructs a fresh *mqttConn per connection
// (matching how advertise.go and heartbeat.go already do), so
// [CommandHandler.HandleMessage] takes the publisher to use as a call
// argument instead of one fixed at construction time — see that method's
// doc comment.
func newCommandHandler(nodeID, assetDir, assetAPIToken string, assetFetchTrigger chan<- struct{}, render *renderOperations, renderTrigger chan<- struct{}, now func() time.Time, logger *slog.Logger) *CommandHandler {
	return &CommandHandler{
		nodeID:            nodeID,
		ops:               newOperationRegistry(assetDir, assetAPIToken, render),
		cache:             newIdempotencyCache(agentIdempotencyCacheCapacity),
		now:               now,
		logger:            logger,
		assetFetchTrigger: assetFetchTrigger,
		renderTrigger:     renderTrigger,
	}
}

// HandleMessage processes one inbound MQTT PUBLISH received on (what
// should always be) this node's cmd topic. publisher is passed per call,
// not held on h, because mqtt.go's AddOnPublishReceived handler is
// re-registered on every reconnect and each one carries its own fresh
// *mqttConn wrapping whatever *autopaho.ConnectionManager is live at that
// moment (see that file's doc comment on why re-registering per connect is
// correct here).
//
// The decision sequence, in order — every branch from "cmd is decoded"
// onward publishes a result, even when refusing, because CommandID is
// trustworthy from that point on and a caller waiting on this command's
// result topic must never be left silent:
//
//  1. Parse topic; wrong kind or wrong node ID: log and drop, no response
//     (this should be unreachable given how the subscription is scoped,
//     but delivery is never trusted blindly).
//  2. Decode and validate the envelope: malformed means nothing (not even
//     a command ID) can be trusted; log and drop, no response.
//  3. Envelope nodeId must match topic node ID ([mqttproto.CheckNodeID]):
//     mismatch, log and drop, no response.
//  4. Decode the cmd payload: malformed, log and drop, no response.
//  5. Target must be this node: mismatch is refused.
//  6. ConfirmationMethod must be "evidence": anything else is refused.
//  7. h.cache.claimOrAwait(IdempotencyKey): if the key was already
//     resolved (found completed, or found in-flight and waited for),
//     republish that exact result verbatim, do not re-execute — this,
//     combined with claimOrAwait's single-lock check-and-claim, is what
//     makes a QoS 1 redelivery (including one arriving CONCURRENTLY with
//     its original, since HandleMessage runs in its own goroutine per
//     inbound PUBLISH — see mqtt.go) execute exactly once rather than
//     merely "usually once." Otherwise, this call now exclusively owns
//     the key and MUST call h.cache.complete on every remaining path
//     below, including a refusal or a failure.
//  8. Deadline already elapsed at receipt: refused, completed (so a
//     concurrent or later redelivery does not re-evaluate against a
//     possibly different now()).
//  9. Action not on the allowlist ([newOperationRegistry]): refused,
//     completed.
//  10. Execute. An operation error is OutcomeFailed. Otherwise OutcomeConfirmed
//     or OutcomeUnconfirmed per [OperationResult.Confirmed], with Evidence
//     populated from the OperationResult. Completed either way.
//  11. Only when the action that just executed (not refused, not a
//     replayed/awaited result) was "agent.echo": also publish the
//     retained observed/agent/echo signal — this is what makes the
//     outcome become an observation like every other signal in this
//     system, per this seam's own requirement. When it was "asset.fetch"
//     instead: signal h.assetFetchTrigger (if set) so
//     assetinventory.go's publisher republishes immediately, rather than
//     waiting for its next tick.
//
// cmd.RequestedRevision is decoded (it is part of CmdPayload) but this
// sequence never reads or enforces it — see
// [mqttproto.CmdPayload.RequestedRevision]'s doc comment for why that gap
// is deliberate for this seam's one operation and where enforcement
// belongs once a revision-sensitive operation exists.
func (h *CommandHandler) HandleMessage(ctx context.Context, publisher Publisher, topic string, payload []byte) {
	parsedTopic, err := mqttproto.ParseTopic(topic)
	if err != nil || parsedTopic.Kind != mqttproto.TopicKindCmd || parsedTopic.NodeID != h.nodeID {
		h.logger.Warn("received a message outside this agent's cmd subscription scope; dropping (should be unreachable)",
			"topic", topic, "error", err)
		return
	}

	envelope, err := mqttproto.DecodeEnvelope(payload)
	if err != nil {
		h.logger.Warn("dropping malformed command message: envelope decode failed", "topic", topic, "error", err)
		return
	}
	if err := envelope.Validate(); err != nil {
		h.logger.Warn("dropping malformed command message: envelope invalid", "topic", topic, "error", err)
		return
	}
	if err := mqttproto.CheckNodeID(envelope, h.nodeID); err != nil {
		h.logger.Warn("dropping command message: envelope nodeId does not match this agent", "topic", topic, "error", err)
		return
	}

	cmd, err := mqttproto.DecodeCmdPayload(envelope)
	if err != nil {
		h.logger.Warn("dropping command message: payload decode failed", "topic", topic, "message_id", envelope.MessageID, "error", err)
		return
	}

	// From here on, cmd is a valid CmdPayload and CommandID is
	// trustworthy: every remaining branch publishes a result.
	receivedAt := h.now()

	// Log who asked, unconditionally, before any allow/refuse decision:
	// cmd.Issuer.PrincipalID is a REQUIRED field ([mqttproto.CmdPayload.
	// Validate]) precisely so the agent's own logs can always answer "who
	// told this node to do that," regardless of the eventual outcome.
	h.logger.Info("received command", "command_id", cmd.CommandID, "action", cmd.Action,
		"issuer_principal_id", cmd.Issuer.PrincipalID, "issuer_principal_name", cmd.Issuer.PrincipalName)

	if cmd.Target.Kind != "node" || cmd.Target.ID != h.nodeID {
		result := h.refusedResult(cmd, receivedAt, fmt.Sprintf(
			"target mismatch: this agent is node %q, command targets %s %q", h.nodeID, cmd.Target.Kind, cmd.Target.ID))
		h.publishResult(ctx, publisher, result)
		return
	}

	if cmd.ConfirmationMethod != confirmationMethodEvidence {
		result := h.refusedResult(cmd, receivedAt, fmt.Sprintf(
			"confirmation method %q is not implemented; only %q is", cmd.ConfirmationMethod, confirmationMethodEvidence))
		h.publishResult(ctx, publisher, result)
		return
	}

	// Atomic check-and-claim: see claimOrAwait's doc comment for why this
	// must be one operation under one lock rather than a separate "check
	// the cache" call followed later by a separate "store the result"
	// call. resolved == true means someone else (a prior delivery, or a
	// concurrent one this call waited on) already owns this key's outcome;
	// resolved == false means THIS call now exclusively owns it and must
	// call h.cache.complete on every remaining path below.
	resolved, found := h.cache.claimOrAwait(cmd.IdempotencyKey)
	if found {
		h.logger.Info("redelivered command matched an already-resolved idempotency key; republishing that result without re-executing",
			"command_id", cmd.CommandID, "idempotency_key", cmd.IdempotencyKey, "action", cmd.Action)
		h.publishResult(ctx, publisher, resolved)
		return
	}

	if cmd.Deadline != nil && !cmd.Deadline.After(h.now()) {
		result := h.refusedResult(cmd, receivedAt, "deadline already elapsed at receipt")
		h.cache.complete(cmd.IdempotencyKey, result)
		h.publishResult(ctx, publisher, result)
		return
	}

	op, ok := h.ops[cmd.Action]
	if !ok {
		result := h.refusedResult(cmd, receivedAt, fmt.Sprintf("operation %q is not on the agent's allowlist", cmd.Action))
		h.cache.complete(cmd.IdempotencyKey, result)
		h.publishResult(ctx, publisher, result)
		return
	}

	opResult, err := op(ctx, cmd.Params, h.now)
	if err != nil {
		result := h.failedResult(cmd, receivedAt, err.Error())
		h.cache.complete(cmd.IdempotencyKey, result)
		h.publishResult(ctx, publisher, result)
		return
	}

	outcome := mqttproto.OutcomeConfirmed
	reason := ""
	if !opResult.Confirmed {
		outcome = mqttproto.OutcomeUnconfirmed
		reason = "operation applied, but the post-write read-back evidence did not match the requested value"
	}
	result := mqttproto.ResultPayload{
		CommandID:      cmd.CommandID,
		IdempotencyKey: cmd.IdempotencyKey,
		Action:         cmd.Action,
		Outcome:        outcome,
		Reason:         reason,
		Evidence: &mqttproto.ResultEvidence{
			Signal:      opResult.Signal,
			Value:       opResult.Value,
			ObservedAt:  &opResult.ObservedAt,
			CollectedAt: h.now(),
		},
		ReceivedAt: receivedAt,
		// ExecutedAt and Evidence.ObservedAt are deliberately two
		// independent fields of opResult, not the same value taken twice
		// — see OperationResult's doc comment.
		ExecutedAt:  &opResult.ExecutedAt,
		RespondedAt: h.now(),
	}
	h.cache.complete(cmd.IdempotencyKey, result)
	h.publishResult(ctx, publisher, result)

	// Only when the operation that just ran (never a refusal, never a
	// replayed/awaited idempotency result — every one of those paths above
	// already returned) was "agent.echo": publish the retained echo
	// observation too. This is deliberately a straightforward action-name
	// check rather than a generalized per-operation post-publish callback
	// registry: this seam ships exactly one allowlisted operation, and
	// generalizing ahead of a second one would be speculative.
	if cmd.Action == "agent.echo" {
		if v, ok := opResult.Value.(string); ok {
			// AppliedAt is ExecutedAt (when the write happened), not
			// ObservedAt (when the read-back evidence was collected) — see
			// OperationResult's doc comment on why the two are distinct.
			h.publishAgentEcho(ctx, publisher, v, opResult.ExecutedAt)
		}
	}
	if cmd.Action == "asset.fetch" && h.assetFetchTrigger != nil {
		select {
		case h.assetFetchTrigger <- struct{}{}:
		default:
			// A publish is already pending (or nothing is listening yet);
			// see mqtt.go's identical heartbeatConnected send for why a
			// dropped duplicate trigger here is correct, not lossy in any
			// way that matters — the inventory publisher only needs to know
			// "a fetch completed since I last checked," not how many.
		}
	}
	if isRenderAction(cmd.Action) && h.renderTrigger != nil {
		select {
		case h.renderTrigger <- struct{}{}:
		default:
			// Same non-blocking, drop-duplicate reasoning as
			// assetFetchTrigger above; the render report publisher only
			// needs to know "something changed since I last checked."
		}
	}
}

// refusedResult builds an OutcomeRefused ResultPayload for cmd.
func (h *CommandHandler) refusedResult(cmd mqttproto.CmdPayload, receivedAt time.Time, reason string) mqttproto.ResultPayload {
	return mqttproto.ResultPayload{
		CommandID:      cmd.CommandID,
		IdempotencyKey: cmd.IdempotencyKey,
		Action:         cmd.Action,
		Outcome:        mqttproto.OutcomeRefused,
		Reason:         reason,
		ReceivedAt:     receivedAt,
		RespondedAt:    h.now(),
	}
}

// failedResult builds an OutcomeFailed ResultPayload for cmd.
func (h *CommandHandler) failedResult(cmd mqttproto.CmdPayload, receivedAt time.Time, reason string) mqttproto.ResultPayload {
	return mqttproto.ResultPayload{
		CommandID:      cmd.CommandID,
		IdempotencyKey: cmd.IdempotencyKey,
		Action:         cmd.Action,
		Outcome:        mqttproto.OutcomeFailed,
		Reason:         reason,
		ReceivedAt:     receivedAt,
		RespondedAt:    h.now(),
	}
}

// publishResult marshals and publishes result to its command's result
// topic ([mqttproto.ResultTopic]), per [mqttproto.ResultDeliveryPolicy].
// Bounded by [commandPublishTimeout]; a publish failure is logged, not
// retried — there is no redelivery mechanism for a result the agent has
// already computed and cached, matching advertise.go's and heartbeat.go's
// own "best effort, log on failure" treatment of an outbound publish.
func (h *CommandHandler) publishResult(ctx context.Context, publisher Publisher, result mqttproto.ResultPayload) {
	topic, err := mqttproto.ResultTopic(h.nodeID, result.CommandID)
	if err != nil {
		// result.CommandID came from a decoded, Validate()-passed
		// CmdPayload, so this should be unreachable; fail loudly rather
		// than silently dropping a result if that invariant is ever
		// violated (matching heartbeat.go's runHeartbeat topic-build
		// guard).
		h.logger.Error("bug: could not build result topic for a decoded command", "node_id", h.nodeID, "command_id", result.CommandID, "error", err)
		return
	}

	env, err := mqttproto.NewResultEnvelope(h.now, h.nodeID, result)
	if err != nil {
		h.logger.Error("failed to build result envelope", "command_id", result.CommandID, "error", err)
		return
	}
	payload, err := json.Marshal(env)
	if err != nil {
		h.logger.Error("failed to marshal result envelope", "command_id", result.CommandID, "error", err)
		return
	}

	pubCtx, cancel := context.WithTimeout(ctx, commandPublishTimeout)
	defer cancel()
	if err := publisher.Publish(pubCtx, topic, mqttproto.ResultDeliveryPolicy.QoS, mqttproto.ResultDeliveryPolicy.Retain, payload); err != nil {
		h.logger.Error("failed to publish command result", "command_id", result.CommandID, "action", result.Action, "outcome", result.Outcome, "error", err)
		return
	}
	h.logger.Info("published command result", "command_id", result.CommandID, "action", result.Action, "outcome", result.Outcome)
}

// publishAgentEcho marshals and publishes the retained
// observed/agent/echo signal, per [mqttproto.ObservedDeliveryPolicy].
// Bounded by [commandPublishTimeout], best-effort like [publishResult].
func (h *CommandHandler) publishAgentEcho(ctx context.Context, publisher Publisher, value string, appliedAt time.Time) {
	topic, err := mqttproto.ObservedTopic(h.nodeID, "agent/echo")
	if err != nil {
		// h.nodeID is validated at config load (see heartbeat.go's
		// identical reasoning for its own health topic); should be
		// unreachable.
		h.logger.Error("bug: could not build agent echo observed topic for a validated node ID", "node_id", h.nodeID, "error", err)
		return
	}

	env, err := mqttproto.NewAgentEchoEnvelope(h.now, h.nodeID, mqttproto.AgentEchoPayload{Value: value, AppliedAt: appliedAt})
	if err != nil {
		h.logger.Error("failed to build agent echo envelope", "error", err)
		return
	}
	payload, err := json.Marshal(env)
	if err != nil {
		h.logger.Error("failed to marshal agent echo envelope", "error", err)
		return
	}

	pubCtx, cancel := context.WithTimeout(ctx, commandPublishTimeout)
	defer cancel()
	if err := publisher.Publish(pubCtx, topic, mqttproto.ObservedDeliveryPolicy.QoS, mqttproto.ObservedDeliveryPolicy.Retain, payload); err != nil {
		h.logger.Error("failed to publish agent echo observation", "error", err)
		return
	}
	h.logger.Debug("published agent echo observation", "value", value)
}
