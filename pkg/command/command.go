package command

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Target names what a command acts on: a resource kind ("fpp", "node",
// ...) and an identifier within it, the same (kind, id) pair
// pkg/observation.ResourceRef uses for what was OBSERVED, so a command's
// target and an observation's subject are directly comparable. This
// package does not import pkg/observation to enforce that as a shared
// type — see [Envelope]'s doc comment for why this package stays
// deliberately thin.
type Target struct {
	Kind string
	ID   string
}

// Issuer records who asked, per ARCHITECTURE section 8.1's "issuer"
// field. A command is always attributed to a principal, never to a bare
// credential — matching ADR-024 decision 1's rule that authorization and
// audit are both expressed against a principal.
type Issuer struct {
	PrincipalID   string
	PrincipalName string
}

// ConfirmationMethod names how a dispatched command's effect is
// confirmed, per ARCHITECTURE section 8.1. [ConfirmationEvidence] is the
// only value any command in this codebase uses today — a command whose
// effect nothing observes would ship the dispatch half of ADR-003 and
// call it done, which BUILD-PLAN Step 7's own primitive-command choice
// was made specifically to avoid. More values (e.g. a command with no
// observable effect, whose confirmation is deliberately absent) are added
// here when a real command needs one, not speculatively ahead of one.
type ConfirmationMethod string

// ConfirmationEvidence means the issuer confirms the command's effect by
// checking observed state against what was requested, per ADR-003 — the
// only confirmation method this codebase implements.
const ConfirmationEvidence ConfirmationMethod = "evidence"

// Envelope is ARCHITECTURE section 8.1's command envelope: "every command
// carries an identifier, target, parameters, idempotency key, deadline,
// issuer, requested revision, confirmation method, and result."
//
// This type carries eight of those nine fields. The ninth, "result," is
// deliberately absent: unlike every other field, a result does not exist
// at the moment an Envelope is constructed — it exists only once dispatch
// and confirmation have both happened, which is after the command this
// Envelope describes has already been recorded. Modeling it here as a
// field that starts nil and is mutated later would blur the one property
// this type exists to keep clear: an Envelope is what was ASKED for,
// fixed at construction. What actually happened is
// internal/coordinator/store.CommandRecord's job (its
// State/OutcomeState/OutcomeReason/ResultJSON columns), because that is
// where a resolved command's outcome durably lives, correlated by command
// ID rather than carried on a value that is discarded once the request
// that built it returns.
type Envelope struct {
	// ID is this command's own identifier — ARCHITECTURE section 8.1's
	// "identifier." Distinct from IdempotencyKey: ID names one row once
	// it has been recorded; IdempotencyKey is what a replay of the SAME
	// logical request is detected by. A caller mints ID itself (this
	// package does not, unlike [NewIdempotencyKey] — an issuer needs
	// exactly one idempotency key per invocation but may reasonably want
	// its own convention for command IDs, e.g. reusing a value its
	// storage layer already generates).
	ID string

	// IdempotencyKey is required on every command — see this package's
	// doc comment. Validate with [ValidateIdempotencyKey] before using a
	// caller-supplied value; mint a fresh one with [NewIdempotencyKey]
	// when the issuer is generating it itself rather than accepting one
	// from a further-upstream caller.
	IdempotencyKey string

	// Action identifies what this command does, e.g. "fpp.stop_playlist".
	// This package defines no action vocabulary of its own — see this
	// package's doc comment: "no FPP knowledge" — an action string is
	// coined by whichever package owns the command it names.
	Action string

	Target Target
	Params map[string]any
	Issuer Issuer

	// RequestedRevision is ARCHITECTURE section 8.1's "requested
	// revision": the configuration revision this command was issued
	// against, when the command is revision-sensitive. Empty for a
	// command with no revision to be sensitive to, e.g. a lifecycle
	// primitive like Stop Playlist that touches no configuration object.
	RequestedRevision string

	ConfirmationMethod ConfirmationMethod

	// Deadline is the absolute time by which confirmation must succeed or
	// the command is reported unconfirmed. Nil means no deadline was set
	// — never a zero time standing in for "none," matching every other
	// nullable evidence timestamp this codebase uses.
	Deadline *time.Time
}

// ErrEmptyIdempotencyKey is returned by [ValidateIdempotencyKey] for an
// empty key. ARCHITECTURE section 8.1 requires an idempotency key on
// every command, not on some of them, so an empty one is always a caller
// error, never a value this package tolerates as "not provided."
var ErrEmptyIdempotencyKey = errors.New("command: idempotency key is empty")

// ErrIdempotencyKeyTooLong is returned by [ValidateIdempotencyKey] for a
// key longer than [MaxIdempotencyKeyLength].
var ErrIdempotencyKeyTooLong = errors.New("command: idempotency key exceeds the maximum length")

// MaxIdempotencyKeyLength bounds an idempotency key accepted from a
// caller (an HTTP request body, in practice). SHOWMESH HYPOTHESIS, NOT
// MEASURED: chosen only to be comfortably larger than any value this
// codebase's own issuers ever mint (a UUID string, 36 bytes) while still
// bounding what an API request body may make the coordinator store in its
// commands.idempotency_key column, which has no length constraint of its
// own (schemaV6). RES-013 owns real storage-sizing evidence; nothing here
// claims to be it.
const MaxIdempotencyKeyLength = 200

// ValidateIdempotencyKey rejects an empty key or one exceeding
// [MaxIdempotencyKeyLength]. It does not otherwise constrain the key's
// character set: an idempotency key is compared for exact equality
// (schemaV6's UNIQUE constraint on commands.idempotency_key), never
// parsed or interpreted, so nothing else about its shape matters.
func ValidateIdempotencyKey(key string) error {
	if key == "" {
		return ErrEmptyIdempotencyKey
	}
	if len(key) > MaxIdempotencyKeyLength {
		return fmt.Errorf("%w: got %d bytes, want at most %d", ErrIdempotencyKeyTooLong, len(key), MaxIdempotencyKeyLength)
	}
	return nil
}

// DefaultFPPCommandConfirmDeadline is the coordinator's own default
// confirmation deadline for a command using [ConfirmationEvidence]
// (internal/coordinator/api's defaultFPPCommandConfirmDeadline is defined
// AS this constant, not as an independently chosen literal that happens to
// agree with it — see that constant's doc comment). Exported here, in this
// shared package, so the server side of every confirmation-bearing command
// has exactly one place this number is chosen.
//
// Step 7 seam C review, defect 1: a client dispatching a command with
// [ConfirmationEvidence] and abandoning the request before the server's
// own deadline elapses can never observe "unconfirmed" — only "the
// connection timed out," which reports a transport failure for what was a
// successful conversation with a healthy coordinator (CLAUDE.md's
// recurring architectural error, restated for a client timeout rather than
// a fallback trigger). See [MinClientTimeoutForConfirmation].
//
// cmd/showmeshctl deliberately does NOT import this package (see that
// program's importgraph_test.go: "this CLI mints its own idempotency key
// independently... for the identical reason it decodes every wire type
// independently rather than importing pkg/observation for one"), so its
// own minimum request timeout for "fpp stop-playlist" is a second,
// independently chosen literal that must be reconciled against this value
// by a test that runs the real coordinator and the real CLI together
// (test/integration), not by a shared import. The Operator UI, being
// TypeScript, cannot import this constant either; its own FPP-command
// request timeout is reconciled the same documented, non-imported way.
const DefaultFPPCommandConfirmDeadline = 20 * time.Second

// ClientTimeoutMargin is added on top of [DefaultFPPCommandConfirmDeadline]
// to get a client-side request budget: even a command that resolves
// EXACTLY at the server's deadline still has to round-trip the response
// (and its own JSON body) back to the client, so a client timeout equal to
// the server deadline is already too tight. SHOWMESH HYPOTHESIS, NOT
// MEASURED — chosen only to be comfortably larger than one HTTP round trip
// and JSON encode/decode on a LAN, the same class of hypothesis
// internal/coordinator/fppcommand.DefaultTimeout already is.
const ClientTimeoutMargin = 15 * time.Second

// MinClientTimeoutForConfirmation returns the minimum request budget a
// client dispatching a command with [ConfirmationEvidence] must allow,
// given the server's own confirmation deadline. A client budget below this
// value can NEVER observe the confirmed/unconfirmed outcome the server
// eventually reaches — it always aborts first.
func MinClientTimeoutForConfirmation(serverConfirmDeadline time.Duration) time.Duration {
	return serverConfirmDeadline + ClientTimeoutMargin
}

// MaxFPPCommandConfirmDeadline is the maximum confirmation deadline ANY
// primitive FPP command this coordinator ships (internal/coordinator/api's
// fppCommandPrimitives registry) may use with [ConfirmationEvidence]. A
// client dispatching any of those primitives — not only Stop Playlist —
// must derive its own request budget from THIS value via
// [MinClientTimeoutForConfirmation], never from
// [DefaultFPPCommandConfirmDeadline] alone: a future primitive whose own
// deadline exceeds the default would otherwise silently understate every
// existing client's budget, the same class of defect Step 7 seam C review
// defect 1 already shipped once for a single primitive.
//
// Today this equals DefaultFPPCommandConfirmDeadline, because Step 8's own
// registry gives every primitive the identical ConfirmDeadline function
// (return the shared base unchanged) — SHOWMESH HYPOTHESIS, NOT MEASURED;
// RES-009 owns real latency evidence, and nothing measured justifies
// differentiating one primitive's deadline from another's yet, so
// inventing distinct numbers now would be fabricated precision. This is
// enforced, not merely documented: internal/coordinator/api's
// TestNoFPPCommandPrimitiveDeadlineExceedsMaxConfirmDeadline computes
// every registered primitive's own deadline against the real
// [DefaultFPPCommandConfirmDeadline] base and fails the build the day one
// exceeds this constant without this constant being raised to match — the
// two are independent numbers for the identical reason
// [DefaultFPPCommandConfirmDeadline]'s own doc comment gives for
// cmd/showmeshctl's literal: a unit test comparing two hand-copied
// literals cannot catch them silently disagreeing, only a test that reads
// both real sources of truth can.
const MaxFPPCommandConfirmDeadline = DefaultFPPCommandConfirmDeadline

// NewIdempotencyKey mints a fresh idempotency key: a random UUID (RFC
// 4122 version 4, via github.com/google/uuid — already a dependency of
// this module, used identically for principal, session, and command IDs
// elsewhere). Called once per invocation by an issuer minting its own key
// rather than accepting one from a further-upstream caller — see this
// package's doc comment for why that is every issuer this codebase has
// today (RES-015 section 7.3: FPP supplies nothing to derive one from).
func NewIdempotencyKey() string {
	return uuid.NewString()
}
