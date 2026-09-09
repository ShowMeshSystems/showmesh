package identity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// SessionMaxIdle is ADR-024 decision 5's fixed sliding-idle window: "a
// session expires only after 90 consecutive days without use." Unlike
// retention.go's DefaultMaxEventAge and this package's
// DefaultBootstrapCodeTTL, this is NOT a SHOWMESH HYPOTHESIS — it is a
// number the accepted decision states explicitly, so it is a constant
// here rather than a configurable [Option]: making it tunable would let a
// future change quietly narrow the cold-phone guarantee decision 5 spends
// several paragraphs justifying, with no ADR recording that a durable
// constraint moved.
const SessionMaxIdle = 90 * 24 * time.Hour

// Service is ADR-024's authentication, session, bootstrap, and audit
// surface. The coordinator's API layer (internal/coordinator/api) is the
// only intended caller; see this package's doc comment for the layering
// rule (identity imports store, api imports identity, neither store nor
// identity may import api).
//
// Every method's sentinel-error contract:
//   - [ErrInvalidCredential]: the presented credential does not
//     authenticate this principal, for any reason a caller must not be
//     able to distinguish from any other (unknown name, wrong password;
//     unknown/revoked/expired token; unknown/revoked/generation-stale/
//     idle-expired session).
//   - [ErrDisabled]: the credential is otherwise structurally valid, but
//     the principal it names has been administratively disabled.
//   - [ErrBootstrapClaimed] / [ErrBootstrapExpired] /
//     [ErrBootstrapNotAvailable]: [ClaimBootstrap]-specific, see that
//     method.
//
// # The first six methods, and one deliberate signature deviation
//
// AuthenticatePassword, AuthenticateToken, AuthenticateSession,
// RevokeSession, ListSessions, HasAnyPrincipal, and ClaimBootstrap match
// the Step 6 seam contract's Service interface exactly. CreateSession
// does not: the contract shows `CreateSession(ctx, principalID,
// deviceLabel string, now time.Time) (Session, error)`. This
// implementation returns `(Session, string, error)` — the extra string is
// the one-time plaintext session secret the caller must set as the
// HttpOnly cookie's value. See this package's doc comment for why: the
// contract's own [Authenticated.CredentialID] doc comment says a
// session's audit-attribution identifier is "never the secret itself",
// which is only possible to honor if [Session.ID] is NOT the cookie
// value, which in turn means CreateSession's single Session return value
// cannot ALSO carry the raw secret the API layer needs to actually set
// the cookie. Splitting it out is the fix; leaving it in Session.ID is
// the alternative this implementation rejected as a live-credential
// disclosure risk (see [Service.ListSessions]).
//
// # Methods beyond the seam contract
//
// The seam contract's Service interface snippet is, by its own framing,
// what the identity package's authentication path needs — it does not
// enumerate how a principal, token, or audit entry ever comes to exist in
// the first place, even though the Step 6 deliverables list ("SQLite-
// backed principals with argon2id passwords... API tokens... one-time
// display... Audit... append-only") require that capability to exist
// somewhere. CreatePrincipal, SetPassword, SetDisabled,
// RevokeAllSessions, ListPrincipals, GetPrincipal, IssueToken,
// RevokeToken, ListTokens, WriteAudit, and ListAudit are this
// implementation's answer: real methods, backed by real schemaV5
// repository methods and covered by this package's tests, reachable by
// Go code today and by an HTTP endpoint once a later step wires one —
// consistent with BUILD-PLAN's Step 6 framing that this step "builds the
// mechanism that permits [a write endpoint], not adds one of its own."
type Service interface {
	AuthenticatePassword(ctx context.Context, name, password string) (Principal, error)
	AuthenticateToken(ctx context.Context, token string) (Authenticated, error)
	AuthenticateSession(ctx context.Context, sessionSecret string, now time.Time) (Authenticated, error)

	// RevalidateSession and RevalidateToken perform the identical checks
	// AuthenticateSession/AuthenticateToken do (digest lookup, disabled,
	// generation, and — for a session — the sliding-idle-window
	// comparison against now), but never write a "this credential was
	// just used" touch (store.Store.TouchSession/TouchToken). They exist
	// for exactly one caller: [Hub.revalidateSubscribers]
	// (internal/coordinator/api/stream.go), which re-presents an open SSE
	// connection's original credential on a fixed tick purely to confirm
	// it STILL authenticates (ADR-024 decision 5: "the coordinator
	// therefore revalidates the credential of an open stream
	// periodically"). That periodic re-check is not itself a "use" of the
	// credential by any operator action — a browser tab left open in the
	// background is not an operator doing anything — so touching
	// LastUsedAt on every tick would make decision 5's 90-day idle window
	// unenforceable for exactly the case it exists to catch (a forgotten,
	// abandoned tab), and would cost one UPDATE per tick per open
	// connection for no attribution benefit. AuthenticateSession/
	// AuthenticateToken remain the only two methods that slide anything —
	// every OTHER caller (an ordinary HTTP request) is a genuine use and
	// must keep going through them.
	RevalidateSession(ctx context.Context, sessionSecret string, now time.Time) (Authenticated, error)
	RevalidateToken(ctx context.Context, token string) (Authenticated, error)

	// CreateSession's second return value is the raw session secret to
	// set as the cookie — see this type's doc comment for why that is
	// not Session.ID. principalName and clientAddr (Step 7 seam 0) are
	// what CreateSession now needs to write its own "session.create"
	// audit entry atomically with the session row itself (ADR-024
	// decision 11's same-transaction rule; see [Service.AuditedWrite]) —
	// the caller already has both in hand (principalName from whatever
	// authenticated the request; clientAddr from [handlers.clientAddr]'s
	// equivalent upstream) and passing them in avoids a second store round
	// trip inside the transaction just to look the name back up.
	CreateSession(ctx context.Context, principalID, principalName, deviceLabel, clientAddr string, now time.Time) (Session, string, error)
	RevokeSession(ctx context.Context, sessionID string) error
	ListSessions(ctx context.Context, principalID string) ([]Session, error)

	// HasAnyPrincipal reports first-run state (ADR-024 decision 9) as a
	// PURE query: it never generates or rotates a bootstrap code as a
	// side effect. It used to (see [EnsureBootstrap]'s doc comment for
	// why that was a defect, not a convenience): every unauthenticated
	// caller of GET /api/v1/session reached this method on every poll,
	// which meant an expired bootstrap code was silently reissued on the
	// very next anonymous request — decision 9's expiry bounding nothing
	// in practice, "a window that stays open with rotating contents"
	// rather than the bounded, host-triggered window the decision
	// describes.
	HasAnyPrincipal(ctx context.Context) (bool, error)

	// EnsureBootstrap guarantees a valid, unclaimed, unexpired bootstrap
	// code and file exist WHEN no principal currently exists, generating
	// a fresh pair only when the stored one is missing, claimed, or
	// expired — the code-generation half [HasAnyPrincipal] used to fold
	// into itself. This is deliberately NOT called from any request
	// handler: its only callers are the coordinator's own startup path
	// and its periodic unclaimed-bootstrap warning loop
	// (internal/coordinator/coordinator.go's watchUnclaimedBootstrap),
	// neither of which is reachable or acceleratable by an unauthenticated
	// network caller. Safe to call repeatedly and cheaply idempotent in
	// the common case (an existing valid row short-circuits after one
	// SELECT, with no file write).
	EnsureBootstrap(ctx context.Context) error

	// ClaimBootstrap's deviceLabel, clientAddr, and form parameters (Step 7
	// seam 0) are what let this method write its own "bootstrap.claim"
	// audit entry atomically with the principal creation and the bootstrap
	// row's claim (ADR-024 decision 11's same-transaction rule; see
	// [Service.AuditedWrite]) — closing the live defect ADR-024 names by
	// name: "an audit failure on a bootstrap claim leaves the first
	// administrator existing with no record of its creation."
	//
	// form exists because ClaimBootstrap has two genuinely different
	// callers with two genuinely different credentials, and decision 11
	// requires the entry to record which one was used: the network path
	// (POST /api/v1/bootstrap) verifies a password over HTTP and passes
	// [FormPassword]; the host-shell path (cmd/showmesh-coordinator's
	// `bootstrap` subcommand) verifies filesystem access to the data
	// volume — ADR-024 decision 9's stronger authority — and passes
	// [FormCLI]. A review finding caught an earlier version of this method
	// hardcoding FormPassword regardless of caller, which made a host-shell
	// claim and a network claim byte-identical in the audit log. deviceLabel
	// is threaded the same way [Service.CreateSession] already threads its
	// own deviceLabel: the caller has it in hand (the API request body's
	// deviceLabel field, or the CLI's own -device-label flag), and losing
	// it off this entry was the other half of that same review finding.
	ClaimBootstrap(ctx context.Context, code, name, password, deviceLabel, clientAddr string, form CredentialForm, now time.Time) (Principal, error)

	// AuditedWrite runs fn and appends the [AuditEntry] fn returns, both
	// inside ONE transaction: either the state change and its audit record
	// both land, or neither does (ADR-024 decision 11's same-transaction
	// rule for a coordinator-local write). fn returns the entry rather than
	// receiving one so it can name a target that does not exist until the
	// write has happened — a new principal id, a new config revision
	// number — which also makes it impossible to write the audit record
	// without doing the work it describes.
	//
	// An audit-append failure is returned wrapped in [ErrAuditWrite], so a
	// caller can distinguish "the write failed" (fn's own error, returned
	// UNWRAPPED — errors.Is against whatever sentinel fn itself returned
	// still works) from "the attribution failed" (wrapped in
	// ErrAuditWrite). [WriteAudit] stays: it remains correct for a command
	// dispatched outward (decision 11's write-before-dispatch rule, where
	// the command has not happened yet and there is nothing to compose a
	// transaction around) and for authentication-failure records, which
	// name no state change at all.
	AuditedWrite(ctx context.Context, fn func(ctx context.Context, tx *store.Tx) (AuditEntry, error)) error

	// --- extensions beyond the seam contract; see type doc comment ---

	CreatePrincipal(ctx context.Context, name string, kind Kind, role Role, password string) (Principal, error)
	SetPassword(ctx context.Context, principalID, password string) (Principal, error)
	SetDisabled(ctx context.Context, principalID string, disabled bool) (Principal, error)

	// SetRole changes principalID's role and bumps its generation counter
	// — ADR-024 decision 12: "a role change... increments the generation
	// counter in decision 5, which closes open streams and forces a
	// re-fetch, so the stale window is bounded rather than indefinite."
	// This is the one write this package's Service interface was missing
	// entirely relative to [Role]'s own fixed vocabulary: without it,
	// nothing could change a principal's role at all, let alone do so in
	// a way decision 12's guarantee has anything to hang on. See
	// [store.Store.SetPrincipalRole], which this delegates to.
	SetRole(ctx context.Context, principalID string, role Role) (Principal, error)

	RevokeAllSessions(ctx context.Context, principalID string) error

	// InvalidateAllSessions bumps EVERY principal's generation counter in
	// one call — ADR-024 decision 5's third named trigger alongside a
	// password change and an administrative revoke-all: "a database
	// restore increments it". A restore rolls the whole database back to
	// an earlier point, including every principal's own generation
	// counter, so nothing left behind BY the restore can distinguish a
	// session that was legitimately revoked after the backup point from
	// one the restore just resurrected — the restore procedure itself
	// must call this, once, before the restored data is trusted. Exposed
	// only via the `invalidate-all-sessions` coordinator subcommand
	// (ADR-024 decision 9's host-level posture: filesystem access to the
	// data volume, never the network), never over the API.
	InvalidateAllSessions(ctx context.Context) error

	ListPrincipals(ctx context.Context) ([]Principal, error)
	GetPrincipal(ctx context.Context, principalID string) (Principal, error)

	// EnsureReservedRecoveryPrincipal idempotently creates Track D seam
	// D-3a's built-in automatic-recovery principal
	// ([ReservedResolumeRecoveryPrincipalID]) if it does not already
	// exist. Called once at coordinator startup.
	EnsureReservedRecoveryPrincipal(ctx context.Context) (Principal, error)

	IssueToken(ctx context.Context, principalID, label string, expiresAt *time.Time) (Token, error)
	RevokeToken(ctx context.Context, tokenID string) error
	ListTokens(ctx context.Context, principalID string) ([]TokenInfo, error)

	WriteAudit(ctx context.Context, entry AuditEntry) error

	// ListAudit pages FORWARD from since, oldest first.
	// ListAuditNewestFirst pages BACKWARD from before, newest first, and
	// is how an operator surface opens on recent activity without walking
	// retained history. OldestAuditID is what keeps the backward walk
	// honest at its far end.
	ListAudit(ctx context.Context, since int64, limit int) ([]AuditEntry, error)
	ListAuditNewestFirst(ctx context.Context, before int64, limit int) ([]AuditEntry, error)
	OldestAuditID(ctx context.Context) (int64, bool, error)

	// AuditWriteStatus reports coordinator.audit.store.state and
	// coordinator.audit.store.reason (docs/build/IDENTIFIER-REGISTER.md):
	// whether this coordinator can currently write to its audit store.
	// Computed FRESH on every call via [store.Store.ProbeAuditWrite] (a
	// real INSERT into audit_log, always rolled back), matching
	// [v1.AudioConfigPushStatus]'s own "computed fresh on every snapshot
	// request" precedent one layer up: a caller polling this on some
	// fixed interval (a dashboard left open overnight, say) gets a live
	// answer every time, never a value that goes stale between real
	// command traffic. This answers a different question than
	// [store.Store.Readiness]'s plain connection ping: a disk that is
	// full or a table that has hit a constraint can leave the connection
	// itself perfectly reachable while every append fails, which is
	// ADR-024 decision 11's own named trigger for the audit-unavailable
	// condition this reports. The same probe result also updates this
	// Service's own internal latch (the one [AuditedWrite]/[WriteAudit]'s
	// real append attempts already maintain), so a caller checking
	// immediately after a real dispatch sees a consistent answer either
	// way. state is "usable" or "unusable"; reason is empty exactly when
	// state is "usable", mirroring [v1.AudioConfigPushStatus]'s identical
	// convention.
	AuditWriteStatus(ctx context.Context) (state, reason string)
}

// TokenInfo is an API token's non-secret metadata — what [Service.ListTokens]
// returns. Never carries the token's digest, let alone its raw value; see
// [Service.ListTokens]'s doc comment.
type TokenInfo struct {
	ID          string
	PrincipalID string
	Hint        string
	Label       string
	CreatedAt   time.Time
	ExpiresAt   *time.Time
	LastUsedAt  *time.Time
}

// svc is [Service]'s concrete implementation over *store.Store, unexported
// because [NewService] returns the Service interface — matching how a
// small, consumer-facing interface is normally constructed in Go, and
// matching the seam contract's own framing ("the Service interface and a
// concrete implementation over the store").
type svc struct {
	st  *store.Store
	now func() time.Time

	// dataDir is where bootstrap.go's writeBootstrapFile/deleteBootstrapFile
	// read and write [BootstrapFileName]. Distinct from store's own data
	// directory parameter only in that this package receives it directly
	// rather than asking store for it — store has no method to report its
	// own dataDir back out, and inventing one only for this would leak an
	// implementation detail across a package boundary that does not
	// otherwise need it.
	dataDir string

	bootstrapTTL time.Duration

	logger *slog.Logger

	// auditWriteMu guards auditWriteState/auditWriteReason: see
	// [Service.AuditWriteStatus]'s own doc comment.
	auditWriteMu     sync.Mutex
	auditWriteState  string
	auditWriteReason string
}

// Option configures [NewService]. Matches store.Option's functional-option
// shape for the same reason store's own doc comment gives: a future
// option never requires every existing NewService call site to change.
type Option func(*svc)

// WithBootstrapCodeTTL overrides [DefaultBootstrapCodeTTL]. A non-positive
// d is ignored (the default is kept) — an expiry of zero or less would
// make every generated bootstrap code unclaimable the instant it is
// written, which is never a coherent choice, unlike
// [store.WithMaxEventAge]'s "disable the bound entirely" case for event
// retention.
func WithBootstrapCodeTTL(d time.Duration) Option {
	return func(s *svc) {
		if d > 0 {
			s.bootstrapTTL = d
		}
	}
}

// WithLogger overrides the [slog.Logger] [NewService] uses for the
// warnings decision 9 requires be loud (a bootstrap provisioning failure,
// a failed post-claim file deletion) — see [svc.HasAnyPrincipal] and
// [svc.ClaimBootstrap]. Defaults to slog.Default() when unset, matching
// store.Open's identical default.
func WithLogger(logger *slog.Logger) Option {
	return func(s *svc) {
		if logger != nil {
			s.logger = logger
		}
	}
}

// NewService constructs a [Service] over st. now is the injected clock
// seam (CLAUDE.md: "do not call time.Now inside logic under test") used
// for every internal time comparison this package makes that the Service
// interface's own method signatures do not already supply explicitly
// (token/session expiry, bootstrap expiry) — nil defaults to time.Now,
// matching store.open's identical convention. dataDir is where the
// bootstrap file lives; see [svc.dataDir]'s doc comment.
//
// A caller that wants st's own bookkeeping timestamps (principals.created_at
// and friends) to advance on the SAME clock as this service's own
// decisions — the property every test in this package's test file relies
// on — must open st with the matching [store.WithClock](now) option. This
// package cannot enforce that from here (st is already open by the time
// NewService receives it); it is a wiring responsibility documented here
// because getting it wrong produces no compile error, only two clocks
// that quietly drift apart in a deterministic test and never in
// production, where both callers pass real time.Now anyway.
func NewService(st *store.Store, now func() time.Time, dataDir string, opts ...Option) Service {
	if now == nil {
		now = time.Now
	}
	s := &svc{
		st:           st,
		now:          now,
		dataDir:      dataDir,
		bootstrapTTL: DefaultBootstrapCodeTTL,
		logger:       slog.Default(),
		// auditWriteState/auditWriteReason start at their zero values and
		// are never read raw: [Service.AuditWriteStatus] always probes
		// and overwrites them before returning, and nothing else reads
		// them, so there is no externally observable "before the first
		// call" state to initialize here.
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// dummyPasswordHash is computed once, lazily, at the argon2id cost
// [HashPassword] always uses, and reused for every [svc.AuthenticatePassword]
// call against an unknown principal name — see that method's doc comment
// for why: the seam contract requires "a dummy verify on unknown name so
// timing does not distinguish [it from a known name with a wrong
// password]", and computing a *fresh* hash-then-verify per unknown-name
// attempt would itself be an unbounded-cost surface (this predates
// decision 8's concurrency limiter, which bounds ATTEMPTS, not a
// per-attempt hash-then-verify-then-throwaway pair only the unknown-name
// path would otherwise pay). sync.OnceValue runs the argon2id work
// exactly once, on first use, in whichever goroutine reaches it first;
// every other concurrent caller blocks briefly rather than duplicating
// the work.
var dummyPasswordHash = sync.OnceValue(func() string {
	h, err := HashPassword("identity-dummy-verify-password-never-authenticates-anything")
	if err != nil {
		// HashPassword only fails if crypto/rand itself fails, which is a
		// process-fatal condition for every other credential operation
		// this package performs too; panicking here surfaces that
		// immediately instead of silently degrading every unknown-name
		// login attempt's timing profile.
		panic(fmt.Sprintf("identity: precompute dummy password hash: %v", err))
	}
	return h
})

func principalFromRecord(rec store.PrincipalRecord) Principal {
	return Principal{
		ID:          rec.ID,
		Name:        rec.Name,
		Kind:        Kind(rec.Kind),
		Role:        Role(rec.Role),
		CreatedAt:   rec.CreatedAt,
		Disabled:    rec.Disabled,
		Generation:  rec.Generation,
		HasPassword: rec.PasswordHash != "",
		Reserved:    rec.ID == ReservedResolumeRecoveryPrincipalID,
	}
}

func sessionFromRecord(rec store.SessionRecord) Session {
	return Session{
		ID:          rec.ID,
		PrincipalID: rec.PrincipalID,
		DeviceLabel: rec.DeviceLabel,
		Generation:  rec.Generation,
		CreatedAt:   rec.CreatedAt,
		LastUsedAt:  rec.LastUsedAt,
	}
}

func tokenInfoFromRecord(rec store.TokenRecord) TokenInfo {
	return TokenInfo{
		ID:          rec.ID,
		PrincipalID: rec.PrincipalID,
		Hint:        rec.Hint,
		Label:       rec.Label,
		CreatedAt:   rec.CreatedAt,
		ExpiresAt:   rec.ExpiresAt,
		LastUsedAt:  rec.LastUsedAt,
	}
}

// --- authentication ---

// AuthenticatePassword is the only password verification path (ADR-024
// decision 8 requires the concurrency limit and per-source delay around
// login cost; per the seam contract, that bound belongs to the CALLER,
// not here — this method does exactly the argon2id verify and nothing
// that throttles it).
//
// Returns [ErrInvalidCredential] for both an unknown name and a wrong
// password for a known name, with a dummy verify on the unknown-name path
// so the two are not distinguishable by timing — see [dummyPasswordHash].
// Returns [ErrDisabled], distinctly, ONLY once the password has already
// been verified correct: checking Disabled before password correctness
// would let a caller who does not know the password learn whether the
// account is disabled, which decision 8's threat model (anyone on the
// VLAN, no credential) makes a real, not hypothetical, information leak.
func (s *svc) AuthenticatePassword(ctx context.Context, name, password string) (Principal, error) {
	rec, err := s.st.GetPrincipalByName(ctx, name)
	if errors.Is(err, store.ErrPrincipalNotFound) {
		_, _ = VerifyPassword(dummyPasswordHash(), password)
		return Principal{}, ErrInvalidCredential
	}
	if err != nil {
		return Principal{}, fmt.Errorf("identity: authenticate password: %w", err)
	}

	ok, verr := VerifyPassword(rec.PasswordHash, password)
	if verr != nil {
		// A malformed stored hash is a data problem, not a guessed
		// password, but the caller-visible outcome is identical: this
		// credential does not authenticate. See ErrMalformedPasswordHash's
		// doc comment.
		return Principal{}, ErrInvalidCredential
	}
	if !ok {
		return Principal{}, ErrInvalidCredential
	}
	if rec.Disabled {
		return Principal{}, ErrDisabled
	}
	return principalFromRecord(rec), nil
}

// AuthenticateToken looks up token by its SHA-256 digest. Returns
// [ErrInvalidCredential] for an unknown, revoked, expired, generation-stale,
// OR disabled case alike — the seam contract states this explicitly for
// AuthenticateToken, unlike AuthenticateSession/AuthenticatePassword, which
// distinguish [ErrDisabled]: a bearer token is presented non-interactively
// (a script, a plugin), where a differentiated "your account is disabled"
// message has no operator on the other end to read it, so collapsing the
// case costs nothing and keeps the token-probing surface uniform. Touches
// [store.Store.TouchToken] on success; see [Service.RevalidateToken] for
// the identical check with no touch.
func (s *svc) AuthenticateToken(ctx context.Context, token string) (Authenticated, error) {
	return s.checkToken(ctx, token, true)
}

// RevalidateToken implements [Service.RevalidateToken] — see that method's
// doc comment on the interface for why a caller would ever want the checks
// without the touch.
func (s *svc) RevalidateToken(ctx context.Context, token string) (Authenticated, error) {
	return s.checkToken(ctx, token, false)
}

// checkToken is AuthenticateToken/RevalidateToken's shared implementation,
// duplicated nowhere else — see [svc.checkSession]'s doc comment for why
// that matters, applied here identically. touch controls only the final
// [store.Store.TouchToken] write; every verification step above it runs
// unconditionally either way.
func (s *svc) checkToken(ctx context.Context, token string, touch bool) (Authenticated, error) {
	digest := HashToken(token)
	rec, err := s.st.GetTokenByDigest(ctx, digest)
	if errors.Is(err, store.ErrTokenNotFound) {
		return Authenticated{}, ErrInvalidCredential
	}
	if err != nil {
		return Authenticated{}, fmt.Errorf("identity: authenticate token: %w", err)
	}
	if !tokensEqualConstantTime(rec.Digest, digest) {
		return Authenticated{}, ErrInvalidCredential
	}

	now := s.now()
	if rec.ExpiresAt != nil && !now.Before(*rec.ExpiresAt) {
		return Authenticated{}, ErrInvalidCredential
	}

	principal, err := s.st.GetPrincipal(ctx, rec.PrincipalID)
	if errors.Is(err, store.ErrPrincipalNotFound) {
		return Authenticated{}, ErrInvalidCredential
	}
	if err != nil {
		return Authenticated{}, fmt.Errorf("identity: authenticate token: load principal: %w", err)
	}
	if principal.Disabled {
		return Authenticated{}, ErrInvalidCredential
	}
	// ADR-024 decision 12's stale-scope bound, extended to tokens: a
	// SetRole/SetDisabled(true)/RevokeAllSessions/InvalidateAllSessions
	// bump on the owning principal must invalidate a token exactly the
	// way it already invalidates a session (checkSession's identical
	// comparison below) — see migrations.go's schemaV5 doc comment on
	// principal_tokens.generation for the review finding this closes: an
	// AuthenticateToken with no generation check meant a token-backed SSE
	// connection never observed a role change or revocation at all.
	if rec.Generation < principal.Generation {
		return Authenticated{}, ErrInvalidCredential
	}

	if touch {
		if err := s.st.TouchToken(ctx, rec.ID, now); err != nil {
			return Authenticated{}, fmt.Errorf("identity: authenticate token: %w", err)
		}
	}
	return Authenticated{Principal: principalFromRecord(principal), Form: FormToken, CredentialID: rec.ID}, nil
}

// AuthenticateSession validates sessionSecret against the stored digest,
// checks the owning principal's disabled flag and current generation
// (ADR-024 decision 5: "reject a session whose generation is below the
// principal's current generation"), applies the 90-day sliding-idle rule
// ([SessionMaxIdle]) against now, and — only once every check has passed
// — slides LastUsedAt to now. now is caller-supplied (not s.now()) so a
// single incoming request's multiple checks (and the eventual
// [store.Store.TouchSession] write) all agree on exactly one instant,
// matching the seam contract's explicit now parameter.
//
// Sliding happens on ANY cookie-bearing request including a read
// (decision 5), so the API layer is expected to call this on every
// request that carries the session cookie, not only ones that reach an
// authenticated endpoint. See [Service.RevalidateSession] for the
// identical check with no touch.
func (s *svc) AuthenticateSession(ctx context.Context, sessionSecret string, now time.Time) (Authenticated, error) {
	return s.checkSession(ctx, sessionSecret, now, true)
}

// RevalidateSession implements [Service.RevalidateSession] — see that
// method's doc comment on the interface for why a caller would ever want
// the checks without the touch.
func (s *svc) RevalidateSession(ctx context.Context, sessionSecret string, now time.Time) (Authenticated, error) {
	return s.checkSession(ctx, sessionSecret, now, false)
}

// checkSession is AuthenticateSession/RevalidateSession's shared
// implementation. This package's own CLAUDE.md lesson (Step 5: "duplication
// found the bug in the code that replaced it") is the reason this is one
// function with a touch flag rather than two independent copies of the
// same six checks: two implementations that both claim to enforce
// decision 5's rules are exactly the shape that silently stops agreeing
// the moment one of them changes. touch controls only the final
// [store.Store.TouchSession] write; every verification step above it runs
// unconditionally either way.
func (s *svc) checkSession(ctx context.Context, sessionSecret string, now time.Time, touch bool) (Authenticated, error) {
	digest := hashSessionSecret(sessionSecret)
	rec, err := s.st.GetSessionByDigest(ctx, digest)
	if errors.Is(err, store.ErrSessionNotFound) {
		return Authenticated{}, ErrInvalidCredential
	}
	if err != nil {
		return Authenticated{}, fmt.Errorf("identity: authenticate session: %w", err)
	}

	principal, err := s.st.GetPrincipal(ctx, rec.PrincipalID)
	if errors.Is(err, store.ErrPrincipalNotFound) {
		return Authenticated{}, ErrInvalidCredential
	}
	if err != nil {
		return Authenticated{}, fmt.Errorf("identity: authenticate session: load principal: %w", err)
	}
	if principal.Disabled {
		return Authenticated{}, ErrDisabled
	}
	if rec.Generation < principal.Generation {
		return Authenticated{}, ErrInvalidCredential
	}
	if now.Sub(rec.LastUsedAt) > SessionMaxIdle {
		return Authenticated{}, ErrInvalidCredential
	}

	if touch {
		if err := s.st.TouchSession(ctx, rec.ID, now); err != nil {
			return Authenticated{}, fmt.Errorf("identity: authenticate session: %w", err)
		}
	}
	return Authenticated{Principal: principalFromRecord(principal), Form: FormSession, CredentialID: rec.ID}, nil
}

// --- sessions ---

// CreateSession mints a new session for principalID, and — Step 7 seam
// 0 — writes its own "session.create" [AuditAdmin] entry in the SAME
// transaction as the session row via [Service.AuditedWrite] (ADR-024
// decision 11's same-transaction rule). Before this, the caller (the API
// layer) sequenced CreateSession and a separate WriteAudit call, and
// session.go documented the resulting gap: "the session row itself now
// exists, orphaned and unreferenced by anything the caller received" if
// the audit write failed. That gap no longer exists — an audit-append
// failure here rolls the session insert back too, and CreateSession
// returns an error wrapping [ErrAuditWrite] rather than a secret for a
// session nothing will ever be able to present, since the row never
// commits.
//
// See [Service]'s doc comment for why the raw secret is this method's
// second return value rather than [Session.ID].
func (s *svc) CreateSession(ctx context.Context, principalID, principalName, deviceLabel, clientAddr string, now time.Time) (Session, string, error) {
	secret, err := generateSessionSecret()
	if err != nil {
		return Session{}, "", err
	}
	digest := hashSessionSecret(secret)
	id := uuid.NewString()

	var created store.SessionRecord
	err = s.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (AuditEntry, error) {
		rec, err := tx.CreateSession(ctx, store.SessionRecord{
			ID:          id,
			PrincipalID: principalID,
			Digest:      digest,
			DeviceLabel: deviceLabel,
		}, now)
		if err != nil {
			return AuditEntry{}, err
		}
		created = rec
		return AuditEntry{
			Timestamp: now, PrincipalID: principalID, PrincipalName: principalName,
			Form: FormPassword, ClientAddr: clientAddr,
			Action: "session.create", Target: rec.ID,
			Params: map[string]any{"deviceLabel": deviceLabel},
			Kind:   AuditAdmin,
		}, nil
	})
	if err != nil {
		return Session{}, "", fmt.Errorf("identity: create session: %w", err)
	}
	return sessionFromRecord(created), secret, nil
}

func (s *svc) RevokeSession(ctx context.Context, sessionID string) error {
	if err := s.st.RevokeSession(ctx, sessionID); err != nil {
		return fmt.Errorf("identity: revoke session: %w", err)
	}
	return nil
}

func (s *svc) ListSessions(ctx context.Context, principalID string) ([]Session, error) {
	recs, err := s.st.ListSessions(ctx, principalID)
	if err != nil {
		return nil, fmt.Errorf("identity: list sessions: %w", err)
	}
	out := make([]Session, len(recs))
	for i, rec := range recs {
		out[i] = sessionFromRecord(rec)
	}
	return out, nil
}

func (s *svc) RevokeAllSessions(ctx context.Context, principalID string) error {
	if _, err := s.st.BumpPrincipalGeneration(ctx, principalID); err != nil {
		return fmt.Errorf("identity: revoke all sessions: %w", err)
	}
	return nil
}

// InvalidateAllSessions implements [Service.InvalidateAllSessions] — see
// that method's doc comment on the interface for the database-restore
// scenario it exists to close. Best-effort per principal is deliberately
// NOT this method's shape: it stops and returns the first error
// encountered, because a caller running this once, host-side, immediately
// after restoring a backup (and before trusting it with any traffic) needs
// to know definitively whether every principal was actually invalidated,
// not "most of them, probably".
func (s *svc) InvalidateAllSessions(ctx context.Context) error {
	principals, err := s.st.ListPrincipals(ctx)
	if err != nil {
		return fmt.Errorf("identity: invalidate all sessions: list principals: %w", err)
	}
	for _, p := range principals {
		if _, err := s.st.BumpPrincipalGeneration(ctx, p.ID); err != nil {
			return fmt.Errorf("identity: invalidate all sessions: bump generation for %q: %w", p.ID, err)
		}
	}
	return nil
}

// --- bootstrap ---

// HasAnyPrincipal implements [Service.HasAnyPrincipal]: a pure query, no
// side effect. See [svc.EnsureBootstrap] for the code-generation half this
// method used to fold into itself, and that interface method's doc comment
// for why splitting them apart is the fix rather than a refactor of
// convenience.
func (s *svc) HasAnyPrincipal(ctx context.Context) (bool, error) {
	has, err := s.st.HasAnyPrincipal(ctx)
	if err != nil {
		return false, fmt.Errorf("identity: has any principal: %w", err)
	}
	return has, nil
}

// EnsureBootstrap implements [Service.EnsureBootstrap]. It is a no-op the
// instant a principal already exists — the common case for the rest of
// this coordinator's lifetime — and otherwise delegates to
// [svc.ensureBootstrap], the actual generate-if-needed logic this method
// used to run as GET /api/v1/session's own side effect before that was
// found to make the bootstrap code's expiry meaningless (see this method's
// interface doc comment).
func (s *svc) EnsureBootstrap(ctx context.Context) error {
	has, err := s.st.HasAnyPrincipal(ctx)
	if err != nil {
		return fmt.Errorf("identity: ensure bootstrap: %w", err)
	}
	if has {
		return nil
	}
	return s.ensureBootstrap(ctx)
}

// ensureBootstrap guarantees a valid, unclaimed, unexpired bootstrap code
// and file exist, generating a fresh pair only when the stored one is
// missing, claimed, or expired. It is safe to call repeatedly and cheaply
// idempotent in the common case (an existing valid row short-circuits
// after one SELECT, with no file write) — [svc.EnsureBootstrap] calls it
// on every invocation while no principal exists, which its own callers
// (coordinator startup and its periodic unclaimed-bootstrap warning loop —
// see that interface method's doc comment) do on a fixed, internal timer,
// never per unauthenticated request.
//
// Deliberately does NOT verify the file on disk still exists when the DB
// row looks valid: doing so would need a stat on every call, and the only
// case that distinguishes ("DB says valid, file is gone") cannot be
// repaired anyway, because the raw code is never stored anywhere but the
// file — see [BootstrapFileName]'s doc comment. An operator who deletes
// the file before claiming it waits out [DefaultBootstrapCodeTTL] (or its
// [WithBootstrapCodeTTL] override) for a fresh one, which is judged an
// acceptable, rare inconvenience rather than a per-call stat cost paid by
// every ordinary poll.
func (s *svc) ensureBootstrap(ctx context.Context) error {
	now := s.now()

	existing, err := s.st.GetBootstrap(ctx)
	switch {
	case errors.Is(err, store.ErrBootstrapNotFound):
		// Fall through to generate.
	case err != nil:
		return fmt.Errorf("identity: ensure bootstrap: %w", err)
	default:
		if existing.ClaimedAt == nil && now.Before(existing.ExpiresAt) {
			return nil
		}
	}

	code, err := generateBootstrapCode()
	if err != nil {
		return err
	}
	if err := writeBootstrapFile(s.dataDir, code); err != nil {
		return err
	}
	if _, err := s.st.PutBootstrap(ctx, store.BootstrapRecord{
		CodeDigest: hashBootstrapCode(code),
		ExpiresAt:  now.Add(s.bootstrapTTL),
	}); err != nil {
		return fmt.Errorf("identity: ensure bootstrap: %w", err)
	}
	return nil
}

// ClaimBootstrap validates code against the stored bootstrap digest and
// expiry, and — only if valid — creates the first administrator principal
// (always [KindHuman]/[RoleAdmin]: bootstrap exists specifically to
// create "the first admin", per ADR-024 decision 9's own phrasing) with
// name and password, atomically with invalidating the code AND with its
// own "bootstrap.claim" audit entry (Step 7 seam 0, via
// [Service.AuditedWrite] over [store.Tx.ClaimBootstrapAndCreatePrincipal])
// — closing the live defect ADR-024 names by name: "an audit failure on a
// bootstrap claim leaves the first administrator existing with no record
// of its creation." Only once that transaction has committed does this
// method delete the bootstrap file: a filesystem side effect, deliberately
// OUTSIDE the transaction, because a database rollback cannot undo a file
// deletion, so doing it first (or inside the transaction, which cannot
// roll it back either) would risk deleting the operator's only copy of
// the code out from under a claim that then failed for an unrelated
// reason (e.g. the audit write).
//
// Returns [ErrBootstrapNotAvailable] if no bootstrap code has ever been
// generated (should not happen once [HasAnyPrincipal] has been called at
// least once with zero principals, but a caller invoking ClaimBootstrap
// without ever having called HasAnyPrincipal is a real, if unusual,
// calling pattern this method must not panic or misbehave under),
// [ErrBootstrapClaimed] if the stored code has already been used (by this
// call or a concurrent one — see [store.ErrBootstrapClaimedRace]),
// [ErrBootstrapExpired] if now is past the stored expiry, and
// [ErrInvalidCredential] if code itself does not match (compared in
// constant time, matching [Service.AuthenticateToken]'s pattern, even
// though a bootstrap code's single-use nature already limits a timing
// side channel's usefulness far more than a reusable token's would be).
// An audit-append failure inside the transaction is reported wrapped in
// [ErrAuditWrite]; per the same-transaction rule, that also means no
// principal was created and the bootstrap code was NOT consumed — the
// operator may simply try again.
func (s *svc) ClaimBootstrap(ctx context.Context, code, name, password, deviceLabel, clientAddr string, form CredentialForm, now time.Time) (Principal, error) {
	if name == ReservedResolumeRecoveryPrincipalID {
		return Principal{}, ErrReservedPrincipal
	}
	rec, err := s.st.GetBootstrap(ctx)
	if errors.Is(err, store.ErrBootstrapNotFound) {
		return Principal{}, ErrBootstrapNotAvailable
	}
	if err != nil {
		return Principal{}, fmt.Errorf("identity: claim bootstrap: %w", err)
	}
	if rec.ClaimedAt != nil {
		return Principal{}, ErrBootstrapClaimed
	}
	if !now.Before(rec.ExpiresAt) {
		return Principal{}, ErrBootstrapExpired
	}
	if !tokensEqualConstantTime(hashBootstrapCode(code), rec.CodeDigest) {
		return Principal{}, ErrInvalidCredential
	}

	hash, err := HashPassword(password)
	if err != nil {
		return Principal{}, fmt.Errorf("identity: claim bootstrap: %w", err)
	}

	id := uuid.NewString()
	var created store.PrincipalRecord
	err = s.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (AuditEntry, error) {
		rec, err := tx.ClaimBootstrapAndCreatePrincipal(ctx, store.PrincipalRecord{
			ID:           id,
			Name:         name,
			Kind:         string(KindHuman),
			Role:         string(RoleAdmin),
			PasswordHash: hash,
		})
		if err != nil {
			return AuditEntry{}, err
		}
		created = rec
		return AuditEntry{
			Timestamp: now, PrincipalID: rec.ID, PrincipalName: rec.Name,
			Form: form, ClientAddr: clientAddr,
			Action: "bootstrap.claim", Target: rec.ID,
			Params: map[string]any{"deviceLabel": deviceLabel},
			Kind:   AuditAdmin,
		}, nil
	})
	if errors.Is(err, store.ErrBootstrapClaimedRace) {
		return Principal{}, ErrBootstrapClaimed
	}
	if err != nil {
		return Principal{}, fmt.Errorf("identity: claim bootstrap: %w", err)
	}

	if err := deleteBootstrapFile(s.dataDir); err != nil {
		// The principal now exists and the DB row is claimed regardless —
		// a leftover file on disk is stale-but-harmless (it cannot be
		// claimed again; ClaimBootstrapAndCreatePrincipal already made
		// that impossible), not a reason to fail an otherwise-successful
		// admin creation. Still logged loudly per decision 9's posture on
		// bootstrap-adjacent failures.
		s.logger.Warn("identity: failed to delete the bootstrap file after a successful claim",
			"error", err)
	}

	return principalFromRecord(created), nil
}

// --- principal and token administration (extensions; see Service doc comment) ---

func (s *svc) CreatePrincipal(ctx context.Context, name string, kind Kind, role Role, password string) (Principal, error) {
	if name == ReservedResolumeRecoveryPrincipalID {
		return Principal{}, ErrReservedPrincipal
	}
	var hash string
	if password != "" {
		h, err := HashPassword(password)
		if err != nil {
			return Principal{}, fmt.Errorf("identity: create principal: %w", err)
		}
		hash = h
	}
	rec, err := s.st.CreatePrincipal(ctx, store.PrincipalRecord{
		ID:           uuid.NewString(),
		Name:         name,
		Kind:         string(kind),
		Role:         string(role),
		PasswordHash: hash,
	})
	if err != nil {
		return Principal{}, fmt.Errorf("identity: create principal: %w", err)
	}
	return principalFromRecord(rec), nil
}

func (s *svc) SetPassword(ctx context.Context, principalID, password string) (Principal, error) {
	if principalID == ReservedResolumeRecoveryPrincipalID {
		return Principal{}, ErrReservedPrincipal
	}
	hash, err := HashPassword(password)
	if err != nil {
		return Principal{}, fmt.Errorf("identity: set password: %w", err)
	}
	if _, err := s.st.SetPrincipalPasswordHash(ctx, principalID, hash); err != nil {
		return Principal{}, fmt.Errorf("identity: set password: %w", err)
	}
	rec, err := s.st.GetPrincipal(ctx, principalID)
	if err != nil {
		return Principal{}, fmt.Errorf("identity: set password: %w", err)
	}
	return principalFromRecord(rec), nil
}

func (s *svc) SetDisabled(ctx context.Context, principalID string, disabled bool) (Principal, error) {
	if principalID == ReservedResolumeRecoveryPrincipalID {
		return Principal{}, ErrReservedPrincipal
	}
	if _, err := s.st.SetPrincipalDisabled(ctx, principalID, disabled); err != nil {
		return Principal{}, fmt.Errorf("identity: set disabled: %w", err)
	}
	rec, err := s.st.GetPrincipal(ctx, principalID)
	if err != nil {
		return Principal{}, fmt.Errorf("identity: set disabled: %w", err)
	}
	return principalFromRecord(rec), nil
}

// SetRole implements [Service.SetRole]. role is trusted to already be a
// valid [Role] — this method does not re-validate it against
// [ParseRole]; every caller (the API layer decoding a request body, or a
// coordinator subcommand) is expected to have already rejected an
// unrecognized role string before it reaches here, matching
// [svc.CreatePrincipal]'s identical trust of its own role parameter.
func (s *svc) SetRole(ctx context.Context, principalID string, role Role) (Principal, error) {
	if principalID == ReservedResolumeRecoveryPrincipalID {
		return Principal{}, ErrReservedPrincipal
	}
	if _, err := s.st.SetPrincipalRole(ctx, principalID, string(role)); err != nil {
		return Principal{}, fmt.Errorf("identity: set role: %w", err)
	}
	rec, err := s.st.GetPrincipal(ctx, principalID)
	if err != nil {
		return Principal{}, fmt.Errorf("identity: set role: %w", err)
	}
	return principalFromRecord(rec), nil
}

func (s *svc) ListPrincipals(ctx context.Context) ([]Principal, error) {
	recs, err := s.st.ListPrincipals(ctx)
	if err != nil {
		return nil, fmt.Errorf("identity: list principals: %w", err)
	}
	out := make([]Principal, len(recs))
	for i, rec := range recs {
		out[i] = principalFromRecord(rec)
	}
	return out, nil
}

// EnsureReservedRecoveryPrincipal idempotently creates
// [ReservedResolumeRecoveryPrincipalID] with [RoleRecovery] and no
// credential of any form (build contract §1.2: "not something anything
// logs in as; it is the attribution the automatic restore writes into the
// audit trail"). Called at coordinator startup, in the same place schema
// migrations run — see [store.Store.EnsureReservedPrincipal], the one
// store path permitted to create it.
func (s *svc) EnsureReservedRecoveryPrincipal(ctx context.Context) (Principal, error) {
	rec, _, err := s.st.EnsureReservedPrincipal(ctx, store.PrincipalRecord{
		ID:   ReservedResolumeRecoveryPrincipalID,
		Name: ReservedResolumeRecoveryPrincipalID,
		Kind: string(KindMachine),
		Role: string(RoleRecovery),
	})
	if err != nil {
		return Principal{}, fmt.Errorf("identity: ensure reserved recovery principal: %w", err)
	}
	return principalFromRecord(rec), nil
}

func (s *svc) GetPrincipal(ctx context.Context, principalID string) (Principal, error) {
	rec, err := s.st.GetPrincipal(ctx, principalID)
	if err != nil {
		return Principal{}, fmt.Errorf("identity: get principal: %w", err)
	}
	return principalFromRecord(rec), nil
}

// IssueToken mints a new API token for principalID. The returned [Token]'s
// Value is the only time the raw token string is ever available — only
// [Token.Digest] and [Token.Hint] are persisted (store.Store.CreateToken).
// Refused for [ReservedResolumeRecoveryPrincipalID]: that principal holds
// no credential of any form, and a minted token would contradict that.
func (s *svc) IssueToken(ctx context.Context, principalID, label string, expiresAt *time.Time) (Token, error) {
	if principalID == ReservedResolumeRecoveryPrincipalID {
		return Token{}, ErrReservedPrincipal
	}
	tok, err := GenerateToken()
	if err != nil {
		return Token{}, err
	}
	id := uuid.NewString()
	if _, err := s.st.CreateToken(ctx, store.TokenRecord{
		ID:          id,
		PrincipalID: principalID,
		Digest:      tok.Digest,
		Hint:        tok.Hint,
		Label:       label,
		ExpiresAt:   expiresAt,
	}); err != nil {
		return Token{}, fmt.Errorf("identity: issue token: %w", err)
	}
	tok.ID = id
	return tok, nil
}

func (s *svc) RevokeToken(ctx context.Context, tokenID string) error {
	if err := s.st.RevokeToken(ctx, tokenID); err != nil {
		return fmt.Errorf("identity: revoke token: %w", err)
	}
	return nil
}

// ListTokens returns [TokenInfo] — non-secret metadata only. Never
// returns a digest or a raw value; see [TokenInfo]'s doc comment.
func (s *svc) ListTokens(ctx context.Context, principalID string) ([]TokenInfo, error) {
	recs, err := s.st.ListTokens(ctx, principalID)
	if err != nil {
		return nil, fmt.Errorf("identity: list tokens: %w", err)
	}
	out := make([]TokenInfo, len(recs))
	for i, rec := range recs {
		out[i] = tokenInfoFromRecord(rec)
	}
	return out, nil
}
