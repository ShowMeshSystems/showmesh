package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppcommand"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/command"
)

// This file is Step 8's own deliverable: docs/bench/fpp-command-vocabulary.md
// section 4's table of eight primitives, turned into a registry that
// replaces Step 7's single hardcoded "stopPlaylist" action. Every primitive
// here is bound by that capture (sections 2-5 are load-bearing) and by the
// same ADR-001/ADR-003 rules fppcommand_handler.go's own doc comment
// already states: FPP is dispatched, never scheduled, and a 200 from FPP
// is never treated as this endpoint's own success.
//
// fppcommand_handler.go owns everything generic (decode, authorize,
// atomic write, dispatch, confirm, audit); this file owns what varies
// PER primitive — its wire shape, its FPP call, its desired state, and
// (the part capture section 4 spends the most words on) its own
// confirmation predicate, because "one predicate, one deadline" was never
// true past Stop Playlist. fppcommand_evidence.go holds the shared
// evidence-resolution plumbing every predicate here is built from.

// --- Signal name constants. Inlined string literals, not imports of
// internal/coordinator/collector/fpp's own SignalXxx constants — the
// identical choice fppStatusSignal already documents in
// fppcommand_handler.go, for the identical reason: this package confirms
// against whichever collector source produced the evidence, and importing
// one concrete collector package to borrow a string constant would tie a
// write endpoint to one specific collector implementation. ---

const (
	fppPlaylistNameSignal  = "fpp.playlist.name"
	fppPlaylistIndexSignal = "fpp.playlist.index"
	fppVolumeSignal        = "fpp.volume"
)

// FPP's own status_name vocabulary this file's predicates compare against
// — capture section 3.1's complete list, the members this step's
// predicates actually use. fppStatusValueIdle lives in
// fppcommand_handler.go (Step 7's own constant, reused here unchanged).
// fppStatusValueUnknown is Finding 5's own addition (Step 8 review):
// primitiveStartPlaylist.PreDispatchCheck must refuse rather than guess
// when FPP itself cannot say what is running, and "unknown" is capture
// section 3.1's own named member for exactly that case — distinct from
// "no CURRENT observation exists at all", which the evidence-currency
// check already handles.
const (
	fppStatusValuePlaying                     = "playing"
	fppStatusValuePaused                      = "paused"
	fppStatusValueStoppingGracefully          = "stopping gracefully"
	fppStatusValueStoppingGracefullyAfterLoop = "stopping gracefully after loop"
	fppStatusValueUnknown                     = "unknown"
)

// fppIfBusyRefuse and fppIfBusyReplace are v1.FPPCommandRequest's
// startPlaylist-only "ifBusy" param values — capture section 5's design
// decision, ShowMesh's own answer to what FPP itself leaves silent
// (Start Playlist always replaces whatever is running). Refuse is the
// default: an absent ifBusy decodes to this value, never to "replace".
const (
	fppIfBusyRefuse  = "refuse"
	fppIfBusyReplace = "replace"
)

// --- Parameter vocabulary: the machinery decodeFPPCommandParams (in
// fppcommand_handler.go) uses to enforce section 2's absent/null/empty
// rule identically for every primitive, rather than each primitive
// re-implementing its own ad hoc decode. ---

// fppParamKind is the wire JSON type one parameter's value must decode as.
type fppParamKind int

const (
	fppParamString fppParamKind = iota
	fppParamBool
	fppParamInt
)

// fppParamDef describes one named parameter a primitive's "params" object
// may carry. Default is meaningful only when Required is false: an
// OPTIONAL param that is ABSENT takes this value; an optional param
// present as an explicit JSON null is still a 400 (CLAUDE.md's own
// standing rule, restated for this endpoint: "explicit null is not the
// same as omitted"), never silently treated as "use the default".
type fppParamDef struct {
	Name     string
	Kind     fppParamKind
	Required bool
	Default  any
}

// fppBaseline is nextPlaylistItem/prevPlaylistItem's own pre-dispatch
// evidence snapshot — see [fppCaptureIndexBaseline]. Every other
// primitive's Confirm function ignores this parameter entirely (Go's
// zero value, never referenced).
//
// IndexKnown is false when no CURRENT fpp.playlist.index observation
// existed at the moment this handler captured the baseline (never
// observed yet, or stale/unknown-age at that instant) — capture per
// spec: "if no current baseline exists at dispatch time, say so in the
// outcome reason and report unconfirmed rather than inventing one."
// [fppBaseline]{} (the Go zero value) is exactly this state, which is
// also what [ReconcileStrandedFPPCommands] passes for every primitive:
// a stranded command's true pre-dispatch baseline was never captured by
// the crashed process, and reconciliation must not invent one after the
// fact.
//
// IndexSource records which collector source (fpp-rest, fpp-mqtt, ...)
// answered the baseline read — Finding 9 (Step 8 review): both sources
// emit fpp.playlist.index, and [ResolveObservations] can pick a
// DIFFERENT winning source on the confirming read than it picked here,
// which is a source flip, not FPP's own counter moving. A movement
// comparison is only valid when the confirming read's source matches
// this one; see [evaluateNextItemEvidence]/[evaluatePrevItemEvidence].
//
// StatusKnown/StatusValue is nextPlaylistItem's own pre-dispatch
// fpp.status snapshot — Finding 1 (Step 8 review): "Next Playlist Item"
// while already idle is FPP's own documented no-op
// (docs/bench/fpp-command-vocabulary.md section 2), so the idle branch
// of [evaluateNextItemEvidence] may confirm only when this baseline
// shows the host was CURRENT and NOT idle before dispatch.
// [evaluatePrevItemEvidence] ignores these two fields entirely — see
// its own doc comment for why Prev carries no idle fallback at all.
type fppBaseline struct {
	IndexKnown  bool
	IndexValue  any
	IndexSource string

	StatusKnown bool
	StatusValue string
}

// fppSafetyClass records, for one primitive, whether it is a member of
// ADR-024 decision 11's named safety class — "blackout, stop, and
// power-off" — which proceeds on an audit-write failure with degraded
// attribution rather than being refused. The zero value
// (fppSafetyClassUndeclared) is deliberately not a valid registry entry: a
// bare bool defaults to false with no way to tell "explicitly decided
// not exempt" from "nobody ever decided", which is exactly how Step 8
// inherited Step 7's one-primitive exemption onto all eight primitives
// unreviewed. TestEveryFPPCommandPrimitiveDeclaresSafetyClass
// (fppcommand_primitives_test.go) walks [fppCommandPrimitives] and fails
// the moment one carries fppSafetyClassUndeclared, so a primitive added to
// the registry without setting this field explicitly fails the build
// rather than silently inheriting an exemption (or a refusal) nobody
// decided for it.
//
// Membership (decided against decision 11's own named list — "blackout,
// stop, power-off" — narrowed to what this registry's eight primitives
// actually are): [primitiveStopPlaylist] and
// [primitiveStopPlaylistGracefully] are fppSafetyClassExempt, and nothing
// else is. In particular:
//
//   - primitivePausePlaylist is fppSafetyClassNotExempt, and this is the
//     line most likely to be re-argued later, so the reasoning is spelled
//     out here rather than left to be rediscovered: pause preserves
//     playback state and the playlist stays active — the show is not
//     stopped, only held — so pause is not decision 11's "stop" path.
//     stopPlaylist remains available and exempt regardless of whether a
//     pause request is refused for want of an audit write, so refusing
//     pause never costs the operator their actual stop control.
//   - primitiveStartPlaylist, primitiveResumePlaylist,
//     primitiveNextPlaylistItem, primitivePrevPlaylistItem, and
//     primitiveSetVolume are all fppSafetyClassNotExempt: none of them is
//     "stop", and every one of them makes the show DO something. Proceeding
//     on an unattributable START (or resume, or item change, or volume
//     change) costs an unaccountable actor making the display act; refusing
//     it costs only that the display does not act, which is CLAUDE.md's own
//     recorded shape for this mistake — "an argument that degradation is
//     safe, made against the wrong failure direction" — corrected in the
//     safe direction rather than repeated.
type fppSafetyClass int

const (
	// fppSafetyClassUndeclared is the zero value and is never a valid
	// registry entry — see this type's own doc comment.
	fppSafetyClassUndeclared fppSafetyClass = iota

	// fppSafetyClassExempt means this primitive is a member of ADR-024
	// decision 11's blackout/stop/power-off safety class: an audit-write
	// failure on the pre-dispatch write degrades attribution (stderr plus
	// a wire flag) but never refuses the command.
	fppSafetyClassExempt

	// fppSafetyClassNotExempt means this primitive fails closed on a
	// pre-dispatch audit-write failure: the command is refused, and
	// (because the whole transaction already rolled back — see
	// [identity.ErrAuditWrite]'s own doc comment) nothing is re-inserted
	// and nothing is dispatched to FPP.
	fppSafetyClassNotExempt
)

// fppPrimitive is one row of docs/bench/fpp-command-vocabulary.md section
// 4's table, plus everything internal/coordinator/api needs to serve it:
// its wire and audit action names, its parameter vocabulary, how it is
// validated beyond generic JSON-shape checking, an optional pre-dispatch
// guard (startPlaylist's ifBusy check is the only user today), how it is
// actually dispatched to FPP, what desired state (if any) it asks for,
// how its own effect is confirmed, and its own confirmation deadline
// function.
type fppPrimitive struct {
	// WireAction is v1.FPPCommandRequest.Action's value, e.g.
	// "startPlaylist". Checked against the fixed vocabulary this
	// registry defines; an unrecognized value is a 400 naming every
	// supported action (fppCommandWireActions).
	WireAction string

	// SafetyClass declares, for THIS primitive, whether an audit-write
	// failure on the pre-dispatch write is ADR-024 decision 11's exemption
	// (proceed, degraded) or the default fail-closed rule (refuse, dispatch
	// nothing) — see [fppSafetyClass]'s own doc comment for the exact
	// membership decision and its reasoning. Never left at its zero value
	// in a registry entry; see [fppSafetyClassUndeclared].
	SafetyClass fppSafetyClass

	// AuditAction is this primitive's own internal action identifier —
	// commands.action and every audit_log entry's action column,
	// namespaced and dotted like this codebase's other admin action
	// names, matching Step 7's own "fpp.stop_playlist" convention
	// exactly (that primitive's own WireAction/AuditAction pair is
	// UNCHANGED by this registry — see primitiveStopPlaylist).
	AuditAction string

	// Params is this primitive's parameter vocabulary, in the canonical
	// order [canonicalParamsJSON] and every doc comment in this file
	// lists it — empty for a zero-argument primitive (stopPlaylist,
	// pausePlaylist, resumePlaylist, nextPlaylistItem,
	// prevPlaylistItem).
	Params []fppParamDef

	// ValidateParams runs AFTER generic JSON-shape decode succeeds
	// (every param present, of the right JSON type, no unknown key) and
	// BEFORE anything is stored or dispatched. It exists so a
	// primitive's OWN value rules — [fppcommand.ValidatePlaylistName],
	// [fppcommand.ValidateVolume], ifBusy's closed two-value set — run
	// through internal/coordinator/fppcommand's exported validators
	// (never reimplemented here), keeping ONE rule rather than two that
	// can silently drift apart. nil for a primitive with no parameter
	// beyond generic shape checking.
	ValidateParams func(params map[string]any) error

	// PreDispatchCheck is startPlaylist's own ifBusy guard (capture
	// section 5) and nil for every other primitive. It runs AFTER
	// ValidateParams and BEFORE anything is stored (a refusal here
	// produces no commands row at all, matching every other
	// pre-dispatch validation failure in this handler) — see
	// primitiveStartPlaylist's own doc comment for the full reasoning.
	// The returned bool is FPP's own "ifNotRunning" argument
	// (Dispatch's own fourth parameter for the one primitive that uses
	// it), computed once here rather than re-derived at dispatch time,
	// since it is this same evidence check that decides it.
	PreDispatchCheck func(ctx context.Context, lister ObservationLister, instanceID string, params map[string]any, now time.Time) (ifNotRunning bool, refusal *v1.Problem)

	// CaptureBaseline is nextPlaylistItem/prevPlaylistItem's own
	// pre-dispatch evidence read (capture section 4's navigation
	// primitives) and nil for every other primitive. Called
	// immediately before dispatch, on the SAME clock instant recorded
	// as this command's dispatchAttemptedAt.
	CaptureBaseline func(ctx context.Context, lister ObservationLister, instanceID string, now time.Time) fppBaseline

	// Dispatch issues the actual FPP command via
	// internal/coordinator/fppcommand's typed client method for this
	// primitive. ifNotRunning is PreDispatchCheck's own computed value
	// (false, and ignored, for every primitive except startPlaylist).
	Dispatch func(ctx context.Context, client *fppcommand.Client, params map[string]any, ifNotRunning bool) (fppcommand.Outcome, error)

	// DesiredState builds zero or more desired_state rows this command
	// asks for (ADR-003). nil for nextPlaylistItem/prevPlaylistItem —
	// see those primitives' own doc comments for why there is no
	// absolute desired state to name for either, rather than an empty
	// slice standing in for "nothing was decided here".
	DesiredState func(env command.Envelope, now time.Time, params map[string]any) []store.DesiredStateRecord

	// Confirm is this primitive's own confirmation predicate — capture
	// section 4's central claim, that these are not one rule. Every
	// implementation in this file is built on
	// [resolveConfirmationEvidence], which carries forward Step 7 seam
	// C review defects 2 and 3 (the notBefore fence, and
	// [ResolveObservations] precedence) unconditionally: no predicate
	// in this file may bypass either.
	Confirm func(ctx context.Context, lister ObservationLister, instanceID string, params map[string]any, baseline fppBaseline, notBefore, now time.Time) (confirmed bool, outcomeState, outcomeReason string)

	// ConfirmDeadline computes this primitive's own confirmation
	// deadline from the coordinator's configured base deadline
	// (h.fppCommandConfirmDeadline, Options.FPPCommandConfirmDeadline)
	// — never a hardcoded literal. Every primitive in this registry is
	// [fppConfirmDeadlineUnchanged] today: SHOWMESH HYPOTHESIS, NOT
	// MEASURED, RES-009 — nothing measured justifies differentiating
	// any primitive's deadline from any other's yet, and inventing
	// distinct numbers now would be fabricated precision (BUILD-PLAN's
	// own instruction for this step). The mechanism for a future,
	// evidence-backed difference is this function pointer itself: a
	// later primitive with real bench data changes ONE primitive's own
	// ConfirmDeadline, not a new mechanism — and
	// [command.MaxFPPCommandConfirmDeadline]'s own enforcement test
	// (TestNoFPPCommandPrimitiveDeadlineExceedsMaxConfirmDeadline) is
	// what stops that change from silently invalidating every client's
	// request budget.
	ConfirmDeadline func(base time.Duration) time.Duration
}

// fppConfirmDeadlineUnchanged is every primitive's ConfirmDeadline today
// — see [fppPrimitive.ConfirmDeadline]'s own doc comment.
func fppConfirmDeadlineUnchanged(base time.Duration) time.Duration { return base }

// fppSingleSignalDesiredState builds one desired_state row for (signal,
// value) against env's own target — the generalized form of Step 7's
// fppStopPlaylistDesiredState, reused by every primitive whose desired
// state is a single (signal, value) pair.
func fppSingleSignalDesiredState(env command.Envelope, now time.Time, signal string, value any) store.DesiredStateRecord {
	return store.DesiredStateRecord{
		ResourceKind:           env.Target.Kind,
		ResourceID:             env.Target.ID,
		Signal:                 signal,
		Value:                  value,
		RequestedAt:            now,
		RequestedByPrincipalID: env.Issuer.PrincipalID,
		CommandID:              env.ID,
		DeadlineAt:             env.Deadline,
	}
}

// fppCaptureIndexBaseline is [fppPrimitive.CaptureBaseline] for
// nextPlaylistItem and prevPlaylistItem: a pre-dispatch read of BOTH
// fpp.playlist.index and fpp.status, each resolved through the same
// [resolveConfirmationEvidence] precedence/currency machinery every
// confirmation predicate uses, with notBefore fixed at the zero
// time.Time — deliberately never triggering the post-dispatch fence,
// since this read happens BEFORE dispatch and there is no dispatch
// instant yet to fence against. Only StateAt(now) == StateCurrent makes
// either half a known baseline; a stale, absent, or unknown-age reading
// leaves the corresponding [fppBaseline] field false/zero rather than
// inventing a value — and a present-but-nil observation Value (Finding
// 13, Step 8 review: an absent reading is not a reading of zero, "",
// or nil) is treated identically to "not known", never compared as if
// it were real.
//
// fpp.status is captured here (not only by the primitives that already
// had their own reason to read it) because [evaluateNextItemEvidence]'s
// idle branch needs it: Finding 1 (Step 8 review) is that "Next
// Playlist Item" issued against an ALREADY idle host is FPP's own
// documented no-op (docs/bench/fpp-command-vocabulary.md section 2),
// and confirming on fpp.status == idle post-dispatch without first
// knowing it was NOT idle pre-dispatch cannot tell that shape apart
// from "Next ended the show" (section 3.5) — which is exactly the
// defect proved live against the bench: idle before, idle after,
// outcome=confirmed. prevPlaylistItem's own evaluator
// ([evaluatePrevItemEvidence]) ignores the status half of this
// baseline entirely; capturing it unconditionally here (rather than
// only for Next) keeps this one function the single source of a
// pre-dispatch baseline for both navigation primitives, matching how
// [fppPrimitive.CaptureBaseline] is wired for each.
func fppCaptureIndexBaseline(ctx context.Context, lister ObservationLister, instanceID string, now time.Time) fppBaseline {
	var baseline fppBaseline
	if val, source, current, _, _ := resolveConfirmationEvidence(ctx, lister, instanceID, fppPlaylistIndexSignal, time.Time{}, now); current && val != nil {
		baseline.IndexKnown = true
		baseline.IndexValue = val
		baseline.IndexSource = source
	}
	if val, _, current, _, _ := resolveConfirmationEvidence(ctx, lister, instanceID, fppStatusSignal, time.Time{}, now); current {
		if s, ok := val.(string); ok {
			baseline.StatusKnown = true
			baseline.StatusValue = s
		}
	}
	return baseline
}

// --- The registry itself. ---

// primitiveStopPlaylist is UNCHANGED from Step 7: same wire action
// ("stopPlaylist"), same internal audit action ("fpp.stop_playlist"),
// same FPP command ("Stop Now"), same desired state, same confirmation
// predicate. Only its packaging (a registry entry instead of inlined
// logic) is new.
var primitiveStopPlaylist = fppPrimitive{
	WireAction:  fppActionStopPlaylist,
	AuditAction: auditActionFPPStopPlaylist,
	// ADR-024 decision 11's own named "stop" — see [fppSafetyClass]'s doc
	// comment.
	SafetyClass: fppSafetyClassExempt,
	Dispatch: func(ctx context.Context, client *fppcommand.Client, _ map[string]any, _ bool) (fppcommand.Outcome, error) {
		return client.StopPlaylist(ctx)
	},
	DesiredState: func(env command.Envelope, now time.Time, _ map[string]any) []store.DesiredStateRecord {
		return []store.DesiredStateRecord{fppSingleSignalDesiredState(env, now, fppStatusSignal, fppStatusValueIdle)}
	},
	Confirm: func(ctx context.Context, lister ObservationLister, instanceID string, _ map[string]any, _ fppBaseline, notBefore, now time.Time) (bool, string, string) {
		return evaluateFPPStatusEvidence(ctx, lister, instanceID, fppStatusValueIdle, notBefore, now)
	},
	ConfirmDeadline: fppConfirmDeadlineUnchanged,
}

// primitiveStartPlaylist is capture section 4/5's most involved
// primitive: three FPP arguments (name, repeat, ifNotRunning — the
// fourth, scheduleProtected, is deliberately never sent; see
// [fppcommand.Client.StartPlaylist]'s own doc comment for ADR-001's
// reasoning), a ShowMesh-only "ifBusy" guard with no FPP equivalent
// (capture section 5), and a TWO-signal confirmation predicate.
//
// PreDispatchCheck implements ifBusy. "refuse" (the default) reads
// CURRENT fpp.status/fpp.playlist.name evidence (no notBefore fence: this
// is a pre-dispatch guard, not a confirmation) and refuses BEFORE
// anything is stored or sent to FPP when a DIFFERENT playlist is
// currently playing. Requesting the playlist that is ALREADY playing is
// never "busy" — it is the same playlist — and this case sends FPP's own
// ifNotRunning=true, which capture section 3.2 measured preserves the
// running item rather than restarting it. When the evidence needed to
// decide is not CURRENT, this refuses and says so: "could not tell" must
// never be read as "not busy" (CLAUDE.md: absence of evidence is not
// evidence of absence, restated a fifth time for this endpoint). This is
// a GUARD, not a lock — it is evaluated against evidence that can be
// stale, and it cannot prevent a race against FPP's own scheduler
// (capture section 5's own closing paragraph).
//
// What counts as "busy" (Finding 5, Step 8 review — the original rule was
// wrong, proved live against the bench): "busy" means the instance is NOT
// idle, not "the instance is not exactly playing". The original
// PreDispatchCheck treated ANY status other than "playing" as never
// busy, which let a default (ifBusy=refuse) startPlaylist DISPATCH over a
// host reading "paused" with a DIFFERENT playlist loaded, and over a host
// "stopping gracefully" — capture section 3.3 measured that state holding
// the CURRENT item still playing, indefinitely, against a 120-second
// item. Capture section 5 is unambiguous that startPlaylist must never
// silently replace a running show, and a paused show is one the operator
// deliberately put there. Using capture section 3.1's complete
// status_name vocabulary (idle, playing, paused, stopping gracefully,
// stopping gracefully after loop, stopping now, testing, unknown):
//
//   - idle is the ONLY status this predicate treats as "not busy" —
//     nothing is running, so there is nothing to protect and
//     fpp.playlist.name is irrelevant (requiring it to be current here
//     would refuse a perfectly idle host merely because the collector has
//     no playlist name to report while idle).
//   - Every OTHER known status is busy: fpp.playlist.name decides SAME
//     vs. DIFFERENT exactly as the "playing" branch already did, and that
//     comparison is unchanged by this fix.
//   - "unknown", or a status that is not CURRENT, means this coordinator
//     cannot tell what is running, and it REFUSES rather than guess —
//     the identical "could not tell must never be read as not busy" rule
//     already applied to a stale/absent reading, extended to FPP's own
//     "unknown" value.
var primitiveStartPlaylist = fppPrimitive{
	WireAction:  "startPlaylist",
	AuditAction: "fpp.start_playlist",
	// NOT exempt: starting a playlist makes the show DO something, and it
	// is not decision 11's "stop" — see [fppSafetyClass]'s doc comment.
	SafetyClass: fppSafetyClassNotExempt,
	Params: []fppParamDef{
		{Name: "playlist", Kind: fppParamString, Required: true},
		{Name: "repeat", Kind: fppParamBool, Required: false, Default: false},
		{Name: "ifBusy", Kind: fppParamString, Required: false, Default: fppIfBusyRefuse},
	},
	ValidateParams: func(params map[string]any) error {
		// Returned bare, not wrapped with a "playlist: " prefix: every
		// [fppcommand.ValidationError] this package's sentinel errors
		// produce already names "playlist name" in plain English (see
		// validation.go), so a field-name prefix here would only repeat
		// it back redundantly on the wire ("playlist: playlist name is
		// required").
		name, _ := params["playlist"].(string)
		if err := fppcommand.ValidatePlaylistName(name); err != nil {
			return err
		}
		ifBusy, _ := params["ifBusy"].(string)
		if ifBusy != fppIfBusyRefuse && ifBusy != fppIfBusyReplace {
			return fmt.Errorf("ifBusy must be %q or %q, not %q", fppIfBusyRefuse, fppIfBusyReplace, ifBusy)
		}
		return nil
	},
	PreDispatchCheck: func(ctx context.Context, lister ObservationLister, instanceID string, params map[string]any, now time.Time) (bool, *v1.Problem) {
		requested, _ := params["playlist"].(string)
		ifBusy, _ := params["ifBusy"].(string)

		// fpp.status must be CURRENT, and not FPP's own "unknown" value, to
		// decide anything at all — except under ifBusy=replace, which
		// dispatches unconditionally and so never needs to know what is
		// currently running.
		statusVal, _, statusCurrent, _, statusReason := resolveConfirmationEvidence(ctx, lister, instanceID, fppStatusSignal, time.Time{}, now)
		statusStr, _ := statusVal.(string)
		if !statusCurrent || statusStr == fppStatusValueUnknown {
			if ifBusy == fppIfBusyReplace {
				return false, nil
			}
			reason := statusReason
			if statusCurrent {
				// FPP's own "unknown" status_name (capture section 3.1) means
				// this coordinator cannot tell what is running — treated the
				// same as a stale/absent reading, not as "not busy". See this
				// primitive's own doc comment (Finding 5).
				reason = fmt.Sprintf("fpp.status reads %q, which can't be used to tell whether the host is busy.", fppStatusValueUnknown)
			}
			p := fppStartPlaylistEvidenceNotCurrentProblem(instanceID, "fpp.status", reason)
			return false, &p
		}

		if statusStr == fppStatusValueIdle {
			// idle is the ONLY status this predicate treats as "not busy"
			// — see this primitive's own doc comment (Finding 5) for why
			// every OTHER known status, not just "playing", is busy.
			// fpp.playlist.name is irrelevant here: requiring it to be
			// current would refuse a perfectly idle host merely because
			// the collector has no playlist name to report while idle.
			return false, nil
		}

		// Anything other than idle is busy: playing, paused, any stopping
		// variant, testing, or a future status this coordinator has no
		// named constant for. Only fpp.playlist.name can say WHICH
		// playlist — needed to tell "same" from "different" regardless of
		// ifBusy, since sending ifNotRunning=true for the SAME playlist
		// (capture section 3.2: preserves the running item) is worth doing
		// even when the operator said "replace".
		nameVal, _, nameCurrent, _, nameReason := resolveConfirmationEvidence(ctx, lister, instanceID, fppPlaylistNameSignal, time.Time{}, now)
		nameStr, _ := nameVal.(string)
		if nameCurrent && nameStr == requested {
			return true, nil
		}

		if ifBusy == fppIfBusyReplace {
			// The operator said, in THIS request, that interrupting
			// whatever is running is intended — dispatch; FPP does what
			// capture section 3.2 describes (Start Playlist always
			// replaces the running show regardless of ifNotRunning).
			return false, nil
		}

		// ifBusy == "refuse" (the default), the instance is not idle, and
		// it is not confirmed to be the SAME playlist.
		if !nameCurrent {
			p := fppStartPlaylistEvidenceNotCurrentProblem(instanceID, "fpp.playlist.name", nameReason)
			return false, &p
		}
		p := fppStartPlaylistBusyProblem(instanceID, nameStr)
		return false, &p
	},
	Dispatch: func(ctx context.Context, client *fppcommand.Client, params map[string]any, ifNotRunning bool) (fppcommand.Outcome, error) {
		name, _ := params["playlist"].(string)
		repeat, _ := params["repeat"].(bool)
		return client.StartPlaylist(ctx, name, repeat, ifNotRunning)
	},
	DesiredState: func(env command.Envelope, now time.Time, params map[string]any) []store.DesiredStateRecord {
		name, _ := params["playlist"].(string)
		return []store.DesiredStateRecord{
			fppSingleSignalDesiredState(env, now, fppStatusSignal, fppStatusValuePlaying),
			fppSingleSignalDesiredState(env, now, fppPlaylistNameSignal, name),
		}
	},
	Confirm: func(ctx context.Context, lister ObservationLister, instanceID string, params map[string]any, _ fppBaseline, notBefore, now time.Time) (bool, string, string) {
		name, _ := params["playlist"].(string)
		return evaluateStartPlaylistEvidence(ctx, lister, instanceID, name, notBefore, now)
	},
	ConfirmDeadline: fppConfirmDeadlineUnchanged,
}

// primitiveStopPlaylistGracefully is capture section 3.3/4's own
// illustration of ADR-003's desired/observed split: DesiredState still
// names fpp.status=idle (that IS what a graceful stop is ultimately for),
// while Confirm accepts entering a STOPPING state as this command's own
// success — a graceful stop's terminal state is bounded by the currently
// playing item's own remaining runtime, which is show content, not a
// number ShowMesh can choose (capture section 3.3 measured a 120-second
// item holding "stopping gracefully" indefinitely). See
// [evaluateFPPStopGracefullyEvidence]'s own doc comment for why the
// outcome reason states plainly, even on a CONFIRMED result, that the
// show has not stopped when it has only started winding down — an
// operator reading "confirmed" must not be able to conclude playback has
// ended.
var primitiveStopPlaylistGracefully = fppPrimitive{
	WireAction:  "stopPlaylistGracefully",
	AuditAction: "fpp.stop_playlist_gracefully",
	// ADR-024 decision 11's own named "stop" — see [fppSafetyClass]'s doc
	// comment.
	SafetyClass: fppSafetyClassExempt,
	Params: []fppParamDef{
		{Name: "afterLoop", Kind: fppParamBool, Required: false, Default: false},
	},
	Dispatch: func(ctx context.Context, client *fppcommand.Client, params map[string]any, _ bool) (fppcommand.Outcome, error) {
		afterLoop, _ := params["afterLoop"].(bool)
		return client.StopPlaylistGracefully(ctx, afterLoop)
	},
	DesiredState: func(env command.Envelope, now time.Time, _ map[string]any) []store.DesiredStateRecord {
		// Deliberately fpp.status=idle — the DESIRED end state, distinct
		// from Confirm's own accepted evidence. See this primitive's own
		// doc comment.
		return []store.DesiredStateRecord{fppSingleSignalDesiredState(env, now, fppStatusSignal, fppStatusValueIdle)}
	},
	Confirm: func(ctx context.Context, lister ObservationLister, instanceID string, _ map[string]any, _ fppBaseline, notBefore, now time.Time) (bool, string, string) {
		return evaluateFPPStopGracefullyEvidence(ctx, lister, instanceID, notBefore, now)
	},
	ConfirmDeadline: fppConfirmDeadlineUnchanged,
}

var primitivePausePlaylist = fppPrimitive{
	WireAction:  "pausePlaylist",
	AuditAction: "fpp.pause_playlist",
	// NOT exempt, and deliberately so: pause preserves playback state and
	// the playlist stays active, so it is not decision 11's "stop" path —
	// see [fppSafetyClass]'s doc comment for the full reasoning on this
	// boundary specifically. stopPlaylist remains available and exempt
	// regardless of whether this refuses.
	SafetyClass: fppSafetyClassNotExempt,
	Dispatch: func(ctx context.Context, client *fppcommand.Client, _ map[string]any, _ bool) (fppcommand.Outcome, error) {
		return client.PausePlaylist(ctx)
	},
	DesiredState: func(env command.Envelope, now time.Time, _ map[string]any) []store.DesiredStateRecord {
		return []store.DesiredStateRecord{fppSingleSignalDesiredState(env, now, fppStatusSignal, fppStatusValuePaused)}
	},
	Confirm: func(ctx context.Context, lister ObservationLister, instanceID string, _ map[string]any, _ fppBaseline, notBefore, now time.Time) (bool, string, string) {
		return evaluateFPPStatusEvidence(ctx, lister, instanceID, fppStatusValuePaused, notBefore, now)
	},
	ConfirmDeadline: fppConfirmDeadlineUnchanged,
}

var primitiveResumePlaylist = fppPrimitive{
	WireAction:  "resumePlaylist",
	AuditAction: "fpp.resume_playlist",
	// NOT exempt: resuming makes the show DO something, and it is not
	// decision 11's "stop" — see [fppSafetyClass]'s doc comment.
	SafetyClass: fppSafetyClassNotExempt,
	Dispatch: func(ctx context.Context, client *fppcommand.Client, _ map[string]any, _ bool) (fppcommand.Outcome, error) {
		return client.ResumePlaylist(ctx)
	},
	DesiredState: func(env command.Envelope, now time.Time, _ map[string]any) []store.DesiredStateRecord {
		return []store.DesiredStateRecord{fppSingleSignalDesiredState(env, now, fppStatusSignal, fppStatusValuePlaying)}
	},
	Confirm: func(ctx context.Context, lister ObservationLister, instanceID string, _ map[string]any, _ fppBaseline, notBefore, now time.Time) (bool, string, string) {
		return evaluateFPPStatusEvidence(ctx, lister, instanceID, fppStatusValuePlaying, notBefore, now)
	},
	ConfirmDeadline: fppConfirmDeadlineUnchanged,
}

// primitiveNextPlaylistItem carries no DesiredState: there is no single
// absolute value to name as "desired" — the command's own effect is
// either "the index moved by one" (from whatever it was) or "the
// playlist ended" (capture section 3.5: Next past the last item ends the
// playlist), and ADR-003's desired_state table has no way to express
// "one of these, depending on where it started" as a single row. Naming
// EITHER value alone would be false in the other case, so this primitive
// states neither, matching this file's own instruction: "say so in the
// comment rather than inventing one."
//
// See [evaluateNextItemEvidence] for the confirmation predicate itself,
// and CaptureBaseline/[fppCaptureIndexBaseline] for why a pre-dispatch
// snapshot must exist before dispatch, not be reconstructed after.
var primitiveNextPlaylistItem = fppPrimitive{
	WireAction:  "nextPlaylistItem",
	AuditAction: "fpp.next_playlist_item",
	// NOT exempt: advancing the playlist makes the show DO something, and
	// it is not decision 11's "stop" — see [fppSafetyClass]'s doc comment.
	SafetyClass:     fppSafetyClassNotExempt,
	CaptureBaseline: fppCaptureIndexBaseline,
	Dispatch: func(ctx context.Context, client *fppcommand.Client, _ map[string]any, _ bool) (fppcommand.Outcome, error) {
		return client.NextPlaylistItem(ctx)
	},
	Confirm: func(ctx context.Context, lister ObservationLister, instanceID string, _ map[string]any, baseline fppBaseline, notBefore, now time.Time) (bool, string, string) {
		return evaluateNextItemEvidence(ctx, lister, instanceID, baseline, notBefore, now)
	},
	ConfirmDeadline: fppConfirmDeadlineUnchanged,
}

// primitivePrevPlaylistItem is [primitiveNextPlaylistItem]'s sibling,
// minus the idle-fallback branch: capture section 3.5 did not measure
// "Prev Playlist Item" ending a playlist the way Next does at the last
// item, so this predicate names only index movement — see
// [evaluatePrevItemEvidence].
var primitivePrevPlaylistItem = fppPrimitive{
	WireAction:  "prevPlaylistItem",
	AuditAction: "fpp.prev_playlist_item",
	// NOT exempt: moving the playlist makes the show DO something, and it
	// is not decision 11's "stop" — see [fppSafetyClass]'s doc comment.
	SafetyClass:     fppSafetyClassNotExempt,
	CaptureBaseline: fppCaptureIndexBaseline,
	Dispatch: func(ctx context.Context, client *fppcommand.Client, _ map[string]any, _ bool) (fppcommand.Outcome, error) {
		return client.PrevPlaylistItem(ctx)
	},
	Confirm: func(ctx context.Context, lister ObservationLister, instanceID string, _ map[string]any, baseline fppBaseline, notBefore, now time.Time) (bool, string, string) {
		return evaluatePrevItemEvidence(ctx, lister, instanceID, baseline, notBefore, now)
	},
	ConfirmDeadline: fppConfirmDeadlineUnchanged,
}

var primitiveSetVolume = fppPrimitive{
	WireAction:  "setVolume",
	AuditAction: "fpp.set_volume",
	// NOT exempt: changing volume makes the show DO something (an audible
	// change), and it is not decision 11's "stop" — see [fppSafetyClass]'s
	// doc comment.
	SafetyClass: fppSafetyClassNotExempt,
	Params: []fppParamDef{
		{Name: "volume", Kind: fppParamInt, Required: true},
	},
	ValidateParams: func(params map[string]any) error {
		// Returned bare — see primitiveStartPlaylist's identical note:
		// ErrVolumeOutOfRange already names "volume" itself.
		v, _ := params["volume"].(int64)
		if err := fppcommand.ValidateVolume(int(v)); err != nil {
			return err
		}
		return nil
	},
	Dispatch: func(ctx context.Context, client *fppcommand.Client, params map[string]any, _ bool) (fppcommand.Outcome, error) {
		v, _ := params["volume"].(int64)
		return client.SetVolume(ctx, int(v))
	},
	DesiredState: func(env command.Envelope, now time.Time, params map[string]any) []store.DesiredStateRecord {
		v, _ := params["volume"].(int64)
		return []store.DesiredStateRecord{fppSingleSignalDesiredState(env, now, fppVolumeSignal, v)}
	},
	Confirm: func(ctx context.Context, lister ObservationLister, instanceID string, params map[string]any, _ fppBaseline, notBefore, now time.Time) (bool, string, string) {
		v, _ := params["volume"].(int64)
		return evaluateSetVolumeEvidence(ctx, lister, instanceID, v, notBefore, now)
	},
	ConfirmDeadline: fppConfirmDeadlineUnchanged,
}

// fppCommandPrimitives is docs/bench/fpp-command-vocabulary.md section 4's
// table, in full — the ONLY eight actions this endpoint accepts.
// stopPlaylist is listed first and is byte-for-byte Step 7's own
// behavior; the other seven are Step 8's own addition. Do not rename, do
// not add, do not drop a member without updating that capture — this
// registry exists specifically so the wire vocabulary is never
// discovered by reading this file in isolation from the bench evidence it
// was captured from.
var fppCommandPrimitives = []fppPrimitive{
	primitiveStopPlaylist,
	primitiveStartPlaylist,
	primitiveStopPlaylistGracefully,
	primitivePausePlaylist,
	primitiveResumePlaylist,
	primitiveNextPlaylistItem,
	primitivePrevPlaylistItem,
	primitiveSetVolume,
}

var fppPrimitivesByWireAction = indexFPPPrimitivesByWireAction(fppCommandPrimitives)
var fppPrimitivesByAuditAction = indexFPPPrimitivesByAuditAction(fppCommandPrimitives)

func indexFPPPrimitivesByWireAction(ps []fppPrimitive) map[string]fppPrimitive {
	m := make(map[string]fppPrimitive, len(ps))
	for _, p := range ps {
		m[p.WireAction] = p
	}
	return m
}

func indexFPPPrimitivesByAuditAction(ps []fppPrimitive) map[string]fppPrimitive {
	m := make(map[string]fppPrimitive, len(ps))
	for _, p := range ps {
		m[p.AuditAction] = p
	}
	return m
}

// fppMaxConfirmDeadline returns the largest confirmation deadline any
// registered primitive produces when evaluated against base — used by
// [handlers.handleFPPCommand] to size its own HTTP write deadline before
// the request body (and so the specific primitive) has even been read.
// Deliberately evaluated against base (h.fppCommandConfirmDeadline, the
// coordinator's own CONFIGURED value) rather than
// [command.DefaultFPPCommandConfirmDeadline]/
// [command.MaxFPPCommandConfirmDeadline]'s fixed constants: those are
// sized for the DEFAULT and are what a CLIENT budgets against (a client
// cannot know a deployment's runtime override), but this function answers
// a different question — what THIS server, running with THIS configured
// base, could actually wait — and must track an operator-configured
// override, never silently keep assuming the default.
func fppMaxConfirmDeadline(base time.Duration) time.Duration {
	largest := base
	for _, p := range fppCommandPrimitives {
		if d := p.ConfirmDeadline(base); d > largest {
			largest = d
		}
	}
	return largest
}

// fppCommandWireActions returns every supported wire action, sorted, for
// an "unsupported action" problem detail that names the full vocabulary
// rather than just the one member ("stopPlaylist") Step 7 hardcoded.
func fppCommandWireActions() []string {
	out := make([]string, 0, len(fppCommandPrimitives))
	for _, p := range fppCommandPrimitives {
		out = append(out, p.WireAction)
	}
	sort.Strings(out)
	return out
}

// --- Parameter decoding: section 2's absent/null/empty rule, enforced
// once here rather than reimplemented per primitive. ---

// isJSONNull reports whether raw is the literal JSON null token
// (ignoring surrounding whitespace, which encoding/json's own tokenizer
// already strips before RawMessage ever sees it, but checked defensively
// since RawMessage carries the bytes verbatim).
func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

// decodeFPPCommandParams implements this step's own absent/null/empty
// rule (spec section 2) for primitive's own parameter vocabulary, given
// top — the WHOLE request body decoded into map[string]json.RawMessage so
// key PRESENCE and an explicit JSON null are distinguishable (verified:
// a *json.RawMessage STRUCT FIELD does not make this distinction — Go's
// encoding/json sets a pointer field to nil for a JSON null before ever
// consulting the pointee's own UnmarshalJSON, so "params" absent and
// "params": null both decode to a nil *json.RawMessage identically; only
// decoding into a map and checking for the key's own presence separates
// them).
//
// Every branch below is a DIFFERENT wire-visible 400, per this step's own
// mandate that absent, null, and empty are three different things:
//
//   - "params" absent, or {} → every optional param takes its Default;
//     a primitive with no Params at all succeeds with an empty map.
//   - "params": null → always 400, for every action, naming that null is
//     not the same as omitted.
//   - a required param absent → 400 naming the param.
//   - a required param present as null → 400, a DIFFERENT message than
//     absent (the distinction is the point).
//   - a required STRING param present as "" → 400, a third message.
//   - an optional param absent → its Default.
//   - an optional param present as null → 400 (explicit null is never
//     "use the default").
//   - a key params does not define → 400 naming the unknown key,
//     DELIBERATELY stricter than ADR-020's "clients ignore unknown
//     fields", which governs a client reading RESPONSES, not a server
//     accepting a WRITE — a typo'd key here must never silently fall
//     back to a default the caller never asked for.
//   - a non-empty params object for a zero-parameter action → 400.
func decodeFPPCommandParams(primitive fppPrimitive, top map[string]json.RawMessage) (map[string]any, *v1.Problem) {
	rawParams, hasParams := top["params"]
	if hasParams && isJSONNull(rawParams) {
		p := invalidParameterProblem(fmt.Sprintf(
			"params must not be null for action %q; omit the field entirely (or send {}) to use every parameter's own "+
				"default — an explicit null is not the same as an omitted field", primitive.WireAction))
		return nil, &p
	}

	var fields map[string]json.RawMessage
	if hasParams {
		if err := json.Unmarshal(rawParams, &fields); err != nil {
			p := invalidParameterProblem("params must be a JSON object: " + err.Error())
			return nil, &p
		}
	}

	if len(primitive.Params) == 0 {
		if len(fields) > 0 {
			keys := make([]string, 0, len(fields))
			for k := range fields {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			p := invalidParameterProblem(fmt.Sprintf(
				"action %q takes no parameters, but params named: %s", primitive.WireAction, strings.Join(keys, ", ")))
			return nil, &p
		}
		return map[string]any{}, nil
	}

	normalized := make(map[string]any, len(primitive.Params))
	known := make(map[string]bool, len(primitive.Params))
	for _, def := range primitive.Params {
		known[def.Name] = true
	}

	// The unknown-key sweep runs BEFORE the per-parameter loop, and the
	// ordering is the whole point (Step 8 review finding 14). When it ran
	// after, a misspelled REQUIRED parameter was reported as the correctly
	// spelled one being absent: `{"playlistt":"x"}` answered "params.playlist
	// is required and was not provided", because the loop reached `playlist`,
	// found it missing, and returned before anything ever looked at
	// `playlistt`. The refusal was right and the explanation pointed at the
	// wrong key, which is the worst combination for an operator staring at a
	// request they can see contains a playlist name. Report what is actually
	// wrong first; absence is only interesting once nothing unrecognized is
	// present to explain it.
	var unknown []string
	for k := range fields {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		p := invalidParameterProblem(fmt.Sprintf(
			"params contains unrecognized key(s) for action %q: %s (a typo'd parameter name is refused rather than "+
				"silently applying that parameter's own default)", primitive.WireAction, strings.Join(unknown, ", ")))
		return nil, &p
	}

	for _, def := range primitive.Params {
		raw, present := fields[def.Name]
		switch {
		case !present:
			if def.Required {
				p := invalidParameterProblem(fmt.Sprintf("params.%s is required and was not provided", def.Name))
				return nil, &p
			}
			normalized[def.Name] = def.Default
		case isJSONNull(raw):
			if def.Required {
				p := invalidParameterProblem(fmt.Sprintf(
					"params.%s is required and must not be null (an explicit null is not the same as an omitted field)", def.Name))
				return nil, &p
			}
			p := invalidParameterProblem(fmt.Sprintf(
				"params.%s must not be null; omit it entirely to use its default (%v) — an explicit null is not the same as omitted", def.Name, def.Default))
			return nil, &p
		default:
			val, err := decodeFPPParamValue(def, raw)
			if err != nil {
				p := invalidParameterProblem(fmt.Sprintf("params.%s: %v", def.Name, err))
				return nil, &p
			}
			normalized[def.Name] = val
		}
	}

	return normalized, nil
}

// decodeFPPParamValue decodes one present, non-null raw JSON value
// against def's own kind, returning a *ValidationError-free, natively
// typed value (string, bool, or int64) or an error describing exactly
// what is wrong — never a silent coercion, matching capture section 1.5's
// own lesson applied one layer up from FPP itself: this coordinator does
// not repeat FPP's "accept anything, coerce silently" behavior for its
// OWN wire parameters either.
func decodeFPPParamValue(def fppParamDef, raw json.RawMessage) (any, error) {
	switch def.Kind {
	case fppParamString:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("must be a string")
		}
		if def.Required && s == "" {
			return nil, fmt.Errorf("must not be an empty string (an explicit empty string is not the same as omitted or null)")
		}
		return s, nil
	case fppParamBool:
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, fmt.Errorf("must be a boolean")
		}
		return b, nil
	case fppParamInt:
		var f float64
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("must be a JSON number")
		}
		if f != math.Trunc(f) {
			// Rejected outright rather than truncated: capture section 1.5
			// measured FPP itself silently coercing a bad value, and this
			// coordinator does not repeat that for its own parameters —
			// see this function's own doc comment.
			return nil, fmt.Errorf("must be a whole number; %v has a fractional part", f)
		}
		return int64(f), nil
	default:
		return nil, fmt.Errorf("unsupported parameter kind")
	}
}

// canonicalParamsJSON serializes normalized (already defaulted by
// [decodeFPPCommandParams]) to a deterministic JSON string: Go's
// encoding/json sorts map[string]any keys alphabetically when marshaling,
// which is exactly the "canonical key order" section 5's idempotency rule
// needs — a client that omits a defaulted field and one that sends the
// default explicitly produce IDENTICAL normalized maps, and therefore
// byte-identical JSON here, which is what makes a byte-equality
// comparison of two commands' stored params a correct test for "the same
// normalized params" rather than merely "the same bytes the client
// happened to send".
func canonicalParamsJSON(normalized map[string]any) (string, error) {
	if normalized == nil {
		normalized = map[string]any{}
	}
	b, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
