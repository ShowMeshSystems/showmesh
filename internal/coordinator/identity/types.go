package identity

import (
	"errors"
	"fmt"
	"time"
)

// Kind distinguishes a human principal (a person authenticating from a
// browser or pasting a token) from a machine principal (FPP's own plugin,
// showmeshctl run unattended, a future scheduler integration). ADR-024
// decision 1: kind does NOT restrict which credential form a principal may
// hold, and role is independent of kind — a human may mint an API token,
// a machine principal may hold admin. Kind exists for display and for
// audit readability ("who/what did this"), never as an input to the
// authorization check itself.
type Kind string

const (
	KindHuman   Kind = "human"
	KindMachine Kind = "machine"
)

// Role is a named bundle of [Scope]s. See [Role.Scopes] for the exact
// membership ADR-024 decision 4 fixes.
type Role string

const (
	RoleViewer    Role = "viewer"
	RoleOperator  Role = "operator"
	RoleAdmin     Role = "admin"
	RoleScheduler Role = "scheduler"

	// RoleRecovery is Track D seam D-3a's own role, minted for the
	// built-in automatic-recovery principal (build contract §1.2): exactly
	// one scope wide (ScopeResolumeAction), because no existing bundle is
	// that narrow in the right place — scheduler is show:macro:run,
	// operator is far wider.
	RoleRecovery Role = "recovery"
)

// Scope is one `<resource>:<action>` authorization unit (ADR-024 decision
// 4). The coordinator's API boundary checks scope membership; nothing
// checks a [Role] directly once a principal has been resolved to its
// effective scope set.
type Scope string

// Read scopes: one per resource the v1 read API actually serves.
const (
	ScopeNodeRead        Scope = "node:read"
	ScopeFPPRead         Scope = "fpp:read"
	ScopeObservationRead Scope = "observation:read"
	ScopeEventRead       Scope = "event:read"
)

// Write scopes. Step 6 adds no endpoint that consumes ScopeShowMacroRun,
// ScopeDevicePower, or ScopeFPPCommand — they exist so the vocabulary is
// fixed by the record that decided it (ADR-024) rather than invented by
// whichever write endpoint ships first.
const (
	ScopeShowMacroRun   Scope = "show:macro:run"
	ScopeDevicePower    Scope = "device:power"
	ScopeFPPCommand     Scope = "fpp:command"
	ScopeConfigWrite    Scope = "config:write"
	ScopePrincipalWrite Scope = "principal:write"
	ScopeAuditRead      Scope = "audit:read"

	// ScopePrincipalRead is Track G seam G-5's own read counterpart to
	// [ScopePrincipalWrite]: listing principals and tokens is exactly as
	// sensitive as GET /api/v1/audit (identity, not telemetry), so it gets
	// its own scope rather than reusing one of the four ADR-024 decision 4
	// read scopes the way config.go's own doc comment explains
	// config:write does for fpp.endpoints. Admin-only, alongside every
	// other identity-administration scope.
	ScopePrincipalRead Scope = "principal:read"

	// ScopeResolumeAction is Track D seam D-3's own action-dispatch scope
	// (TRACK-D-D3-SPEC.md section 5.1): every one of the seven Resolume
	// actions (launchClip, clearLayer, blackout, launchColumn, selectDeck,
	// setLayerBypass, setLayerMaster) requires it. Reads stay open by
	// default (ADR-024) — this scope exists only because dispatching one of
	// these actions changes what the wall shows, the identical reasoning
	// [ScopeFPPCommand] already carries for FPP's own lifecycle commands.
	ScopeResolumeAction Scope = "resolume:action"

	// ScopeAssetWrite is Track E seam E3/E4's own write scope (ADR-028):
	// uploading an asset. Admin-only, like config:write and
	// principal:write — asset upload is configuration in the same sense
	// fpp.endpoints and show.surface are, not an operator-role action.
	ScopeAssetWrite Scope = "asset:write"

	// ScopeRenderCommand is Track B seam B2b-front's own dispatch scope:
	// render.surface.apply, render.surface.clear, and
	// render.pipeline.restart all require it. Reads stay open by default
	// (ADR-024) — this scope exists only because dispatching one of these
	// operations changes what a surface renders, the identical reasoning
	// [ScopeFPPCommand] and [ScopeResolumeAction] already carry for their
	// own vendors.
	ScopeRenderCommand Scope = "render:command"

	// ScopeAudioCommand guards every audio.session.* and audio.gain/
	// output.* dispatch: apply, prepare, start, pause, resume, seek,
	// advance, stop, clear, gain set/fade, and output mute/unmute. Reads
	// stay open by default (ADR-024) — this scope exists only because
	// dispatching one of these operations changes what a session plays,
	// the identical reasoning [ScopeFPPCommand], [ScopeResolumeAction],
	// and [ScopeRenderCommand] already carry for their own domains.
	ScopeAudioCommand Scope = "audio:command"

	// ScopeNightCommand is Track F seam F2's own dispatch scope
	// (RESTING-MODE.md, ADR-038): every one of the seven lifecycle
	// commands (prepare-site, run-readiness, start-preshow, start-night,
	// request-final-show, fade-out-night, power-down-presentation)
	// requires it. Reads stay open by default (ADR-024) — GET
	// /api/v1/night/session needs no scope, the identical reasoning
	// [ScopeFPPCommand], [ScopeResolumeAction], and [ScopeRenderCommand]
	// already carry: a credential problem must never cost the operator
	// sight of the lifecycle state.
	ScopeNightCommand Scope = "night:command"

	// ScopeShowActionInvoke gates POST /api/v1/actions/{id}/invocations:
	// invoke one stored show.action by id, outside of a macro run. Reads
	// stay open by default (ADR-024) — this scope exists only because
	// invoking an action changes what the show does, the identical
	// reasoning [ScopeFPPCommand], [ScopeResolumeAction], and
	// [ScopeRenderCommand] already carry for their own dispatch surfaces.
	// It is the ONLY scope check on that route: the per-integration
	// dispatch underneath (dispatchFPPCommand, ResolumeActions.Dispatch)
	// authorizes nothing of its own for an in-process caller — a
	// principal holding only this scope can dispatch fpp and resolume
	// actions alike, matching how show:macro:run already authorizes a
	// macro's own steps across every integration with one scope.
	ScopeShowActionInvoke Scope = "show:action:invoke"
)

// readScopes is every scope [RoleViewer] holds, and the read-scope subset
// every other role includes too (ADR-024 decision 4: "the read scopes
// plus...").
var readScopes = []Scope{ScopeNodeRead, ScopeFPPRead, ScopeObservationRead, ScopeEventRead}

// operatorActionScopes is what [RoleOperator] adds on top of readScopes:
// "the show, device, and FPP action scopes" — extended by Track D seam D-3
// to include [ScopeResolumeAction], the identical class of action scope for
// a second vendor.
var operatorActionScopes = []Scope{ScopeShowMacroRun, ScopeDevicePower, ScopeFPPCommand, ScopeResolumeAction, ScopeRenderCommand, ScopeShowActionInvoke, ScopeAudioCommand, ScopeNightCommand}

// adminOnlyScopes is what [RoleAdmin] adds on top of everything
// [RoleOperator] holds: "everything, including principal:write and
// audit:read". config:write is also admin-only — ADR-024 decision 11
// names it, alongside principal:write, as the pair that stays fail-closed
// even under the audit-write-failure exemption, and neither appears in
// operatorActionScopes above.
var adminOnlyScopes = []Scope{ScopeConfigWrite, ScopePrincipalWrite, ScopeAuditRead, ScopeAssetWrite, ScopePrincipalRead}

// Scopes returns role's fixed scope bundle, per the table in ADR-024
// decision 4. The returned slice is a fresh copy on every call, so a
// caller mutating it can never corrupt another caller's view.
func (r Role) Scopes() []Scope {
	switch r {
	case RoleViewer:
		return append([]Scope(nil), readScopes...)
	case RoleOperator:
		out := append([]Scope(nil), readScopes...)
		return append(out, operatorActionScopes...)
	case RoleAdmin:
		out := append([]Scope(nil), readScopes...)
		out = append(out, operatorActionScopes...)
		return append(out, adminOnlyScopes...)
	case RoleScheduler:
		// ADR-038 decision 1: FPP invokes the seven night-session
		// lifecycle commands, and scheduler is the machine credential
		// that exists for exactly that.
		return []Scope{ScopeShowMacroRun, ScopeNightCommand}
	case RoleRecovery:
		return []Scope{ScopeResolumeAction}
	default:
		return nil
	}
}

// Has reports whether role's scope bundle includes s.
func (r Role) Has(s Scope) bool {
	for _, have := range r.Scopes() {
		if have == s {
			return true
		}
	}
	return false
}

// ErrUnknownRole is returned by [ParseRole] for any string that is not
// exactly one of the four ADR-024 decision 4 role names.
var ErrUnknownRole = errors.New("identity: unknown role")

// ParseRole validates s against the fixed ADR-024 role vocabulary. Unlike
// [observation.SignalID] (which accepts any syntactically valid
// dot-separated identifier because its vocabulary is open-ended), Role's
// vocabulary is exactly the four rows of ADR-024 decision 4's table —
// there is no extension point, and a caller (the API layer decoding a
// principal-creation request body, or a future config file) must reject
// anything else rather than silently accept and store an unrecognized
// role string a scope check would then treat as "no scopes at all".
func ParseRole(s string) (Role, error) {
	switch Role(s) {
	case RoleViewer, RoleOperator, RoleAdmin, RoleScheduler, RoleRecovery:
		return Role(s), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownRole, s)
	}
}

// Principal is a coordinator-local identity: a human or a machine, with a
// role, that every authorization decision and every audit entry is
// written against (ADR-024 decision 1) — never against a credential form.
type Principal struct {
	ID         string
	Name       string
	Kind       Kind
	Role       Role
	CreatedAt  time.Time
	Disabled   bool
	Generation uint64 // bumped by password change, revoke-all, or restore

	// HasPassword reports whether this principal has a password hash set —
	// never the hash itself, which this type deliberately never carries.
	// Track G seam G-5's lockout guard (api/principals.go) uses this,
	// alongside an active-token count, to decide whether a principal has
	// ANY remaining way to authenticate before refusing a write that would
	// remove the coordinator's last reachable administrator.
	HasPassword bool

	// Reserved is true only for [ReservedResolumeRecoveryPrincipalID] —
	// build contract §1.2: "visible wherever principals are listed". Never
	// stored; derived from ID at read time.
	Reserved bool
}

// CredentialForm records how a request authenticated: with a bearer token
// or with a session cookie. It is what [Authenticated.Form] carries into
// an audit entry, and — critically — it is what the CSRF bearer exemption
// (ADR-024 decision 6) keys on. It must NEVER be inferred from header
// presence (e.g. "an Authorization header was present, so treat this as
// FormToken"): decision 6 spells out exactly why that is exploitable
// (URL userinfo makes a browser attach Authorization to a top-level
// navigation, alongside a SameSite=Lax cookie), and CredentialForm exists
// so the API layer's CSRF check can ask "what actually authenticated
// this request" instead of "what header showed up".
type CredentialForm string

const (
	FormSession CredentialForm = "session"
	FormToken   CredentialForm = "token"

	// FormPassword marks an audit entry written by a request whose entire
	// job is to CREATE a session or claim bootstrap from a name/password
	// pair — POST /api/v1/session and POST /api/v1/bootstrap — rather than
	// one that already presented a pre-existing FormSession/FormToken
	// credential. Moved here from internal/coordinator/api's own
	// package-local const (review finding: this package owns
	// CredentialForm's vocabulary, and a caller growing it outside this
	// package is exactly the drift the CSRF deny-list fix in auth.go's
	// writeGuard depends on this type staying closed against — see that
	// function's doc comment).
	FormPassword CredentialForm = "password"

	// FormCLI marks an audit entry written by one of
	// cmd/showmesh-coordinator's host-level subcommands (bootstrap,
	// create-admin, reset-password, issue-token, revoke-token,
	// invalidate-all-sessions, ...) rather than by an HTTP request this
	// package's own Service methods authenticated. Moved here for the
	// identical reason FormPassword was: a second package inventing its
	// own CredentialForm value is this type's vocabulary growing outside
	// the package that owns it.
	FormCLI CredentialForm = "cli"
)

// Authenticated is what a successful [Service.AuthenticateToken] or
// [Service.AuthenticateSession] call resolves a request to.
type Authenticated struct {
	Principal Principal
	Form      CredentialForm

	// CredentialID is the session's or token's own non-secret row
	// identifier, for audit attribution (ARCHITECTURE §8.1's "a command
	// carries an issuer", made concrete here as "and the specific
	// credential that authenticated it"). NEVER the secret itself — see
	// this package's doc comment for why that rule is what settles the
	// [Session.ID] ambiguity in the seam contract's type comment.
	CredentialID string
}

// Session is one browser session: a device-scoped, individually
// revocable credential that slides on any cookie-bearing request
// (ADR-024 decision 5).
//
// ID is the session's non-secret row identifier — safe to return from
// [Service.ListSessions], safe to display in a "your devices" UI, safe to
// pass to [Service.RevokeSession] — and is NOT the cookie's actual bearer
// value. See this package's doc comment for why that departs from the
// seam contract's literal type comment, and [Service.CreateSession] for
// where the real cookie value is returned instead.
type Session struct {
	ID          string
	PrincipalID string
	DeviceLabel string
	Generation  uint64
	CreatedAt   time.Time
	LastUsedAt  time.Time
}

// Sentinel errors. ErrInvalidCredential is deliberately reused across
// every "this credential does not authenticate" case a caller must not be
// able to distinguish (unknown principal name vs. wrong password;
// unknown, revoked, or expired token; unknown, revoked, generation-stale,
// or idle-expired session) — see each Service method's doc comment for
// exactly which cases collapse into it. ErrDisabled is deliberately
// DISTINCT from it for the cases documented on [Service.AuthenticatePassword]
// and [Service.AuthenticateSession]: an operator whose account still
// exists and whose credential is otherwise structurally valid deserves
// "your account was disabled", not a generic invalid-credential message
// indistinguishable from a typo.
var (
	ErrInvalidCredential = errors.New("identity: invalid credential")
	ErrDisabled          = errors.New("identity: principal is disabled")
	ErrBootstrapClaimed  = errors.New("identity: bootstrap code already used")

	// ErrBootstrapExpired and ErrBootstrapNotAvailable extend the seam
	// contract's sentinel set: ADR-024 decision 9 requires the code to
	// "carry an expiry" and describes a state where no principal exists
	// yet but the bootstrap file/row is momentarily absent (e.g. between
	// process start and the first EnsureBootstrap-triggered generation).
	// Both collapse to generic messaging at the API layer exactly like
	// ErrInvalidCredential does elsewhere, but are distinct sentinels here
	// so a caller CAN tell them apart if it needs to (e.g. for a more
	// specific log line on the host that already has file access, where
	// there is no oracle concern because reading the log already requires
	// the access a written code would too).
	ErrBootstrapExpired      = errors.New("identity: bootstrap code has expired")
	ErrBootstrapNotAvailable = errors.New("identity: no bootstrap code is currently available")

	// ErrAuditWrite is [Service.AuditedWrite]'s distinguishable failure
	// mode (Step 7 seam 0, ADR-024 decision 11's same-transaction rule):
	// wraps the underlying store error when fn's own state change
	// succeeded but appending its audit entry failed, so the whole
	// transaction rolled back. A caller can errors.Is against this to tell
	// "the write itself failed" (fn returned its own error, returned
	// unwrapped by AuditedWrite) apart from "the attribution failed" —
	// seams A and B need that distinction for decision 11's fail-closed
	// rule on config:write/principal:write, and seam C needs it for the
	// blackout/stop/power-off safety-class exemption (decision 11: those
	// three proceed with a degraded, stderr-only attribution record rather
	// than being refused for want of an audit write — a distinction only
	// possible to act on if the caller can tell this failure mode apart
	// from every other one).
	ErrAuditWrite = errors.New("identity: state change succeeded but its audit entry could not be written; the transaction was rolled back")
)

// ReservedResolumeRecoveryPrincipalID is Track D seam D-3a's built-in
// automatic-recovery principal id and name (build contract §1.2) — the
// same fixed string for both, so "which principal" and "what is it called"
// are never two facts that can drift apart. It cannot be deleted,
// disabled, demoted, renamed, or re-credentialed through any path in this
// package (build contract §1.2's enumerated survey): a deletable recovery
// principal is a silent disarm, and the toggle is the one off switch.
const ReservedResolumeRecoveryPrincipalID = "system-resolume-recovery"

// ErrReservedPrincipal is returned by every mutation this package refuses
// against [ReservedResolumeRecoveryPrincipalID] (or a caller attempting to
// CREATE a principal under that name). The message names the toggle as the
// way to turn recovery off, because a refusal that just says "reserved"
// sends the operator hunting.
var ErrReservedPrincipal = errors.New("identity: this is the built-in Resolume recovery principal and cannot be deleted, disabled, renamed, re-roled, or re-credentialed; use the resolume.recovery configuration toggle to turn automatic recovery off instead")

// AuditKind distinguishes the append-only records ADR-024 decision 11
// requires.
type AuditKind string

const (
	AuditDispatch AuditKind = "dispatch"
	AuditOutcome  AuditKind = "outcome"
	AuditReplay   AuditKind = "replay" // idempotent replay that dispatched nothing
	AuditAuthFail AuditKind = "auth_failure"
	AuditAdmin    AuditKind = "admin" // principal/token/session mutation
)

// AuditEntry is one append-only audit_log row, expressed as the domain
// type the API layer reads (see [Service.ListAudit]) rather than
// store.AuditRecord's DB-shaped fields. Every field's meaning is exactly
// as ADR-024 decision 11 describes; see that decision for the full
// reasoning behind the dispatch/outcome/replay split and the blackout
// exemption, which lives in how the API layer calls [Service.WriteAudit]
// under decision 11's failure rule, not in this type.
type AuditEntry struct {
	Timestamp      time.Time
	PrincipalID    string
	PrincipalName  string
	Form           CredentialForm
	CredentialID   string
	ClientAddr     string // empty unless a trusted proxy is configured
	Action         string
	Target         string
	Params         map[string]any // secrets already redacted by the caller
	IdempotencyKey string

	Kind      AuditKind
	CommandID string // correlates dispatch with outcome

	// Outcome, OutcomeState, and OutcomeReason are populated only on an
	// AuditOutcome entry. OutcomeState uses ADR-020's evidence-state
	// vocabulary (observation.State) for the case an outcome never
	// arrives — this package does not import pkg/observation to enforce
	// that as a type, the same way store.EventRecord's Category/Severity
	// deliberately stay untyped strings (see events.go's doc comment):
	// fixing the vocabulary here would let this package quietly become
	// the place it is decided, instead of the API layer that actually
	// renders it.
	Outcome       string
	OutcomeState  string
	OutcomeReason string
}
