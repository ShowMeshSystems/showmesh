// Package macro is Step 9's macro executor (ADR-031, STEP-9-SPEC.md section
// 6). It owns a macro run's lifecycle end to end: resolving and pinning a
// show.macro and its steps' show.action definitions at submission, creating
// the run row and its step rows, executing steps in order in the
// background, applying ADR-031 decision 2's two independent policy axes,
// and finishing the run with its two separate completed/confirmed facts
// (decision 3).
//
// This package imports internal/coordinator/api for the exported dispatch
// seam ([api.FPPCommandDispatcher]) and the wire-facing types it satisfies
// ([api.MacroRunner], declared in api/macro_seam.go). That import direction
// is forced: api must never import this package, or the two would cycle.
// See macro_seam.go's own top comment in the api package for the full
// argument.
//
// *[Executor] is this package's implementation of [api.MacroRunner]. Wiring
// one into the coordinator (constructing it with a real *api.FPPCommandDispatcher
// and a real *broker.Registry, and calling [Executor.Reconcile] before the
// server starts listening) is coordinator.go/apiwiring.go's job, not built
// here per the wave 2 shared contract.
package macro

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// fppDispatcher is this package's own, minimal view of
// [api.FPPCommandDispatcher] — declared here rather than in api (which must
// not know this package exists) so the executor can be tested against a
// fake without constructing a full *api.FPPCommandDispatcher (which itself
// needs a live [api.Dependencies]: an FPP lister, a command store, an
// observation lister, identity, and a nudger). *api.FPPCommandDispatcher
// satisfies this interface with no adapter, exactly the way *store.Store
// already satisfies [api.ConfigStore] with no adapter (interfaces.go's own
// precedent).
type fppDispatcher interface {
	Dispatch(ctx context.Context, in api.FPPCommandInput) (api.FPPCommandOutcome, *v1.Problem, error)
}

// resolumeActionDispatcher is this package's own minimal view of
// [api.ResolumeActionDispatcher] (Track D seam C): dispatch one
// already-authored Resolume action by name, through the SAME dispatch
// path the HTTP handler uses. Declared here rather than importing that
// package's own interface type directly is unnecessary — api is already
// imported for [fppDispatcher] — but the method set is narrowed to
// Dispatch alone, since Actions() (the static vocabulary listing) is a
// config-write-time concern this package never needs at run time. A real
// *api.ResolumeActionDispatcher-satisfying value (the adapter
// internal/coordinator's resolumeactionwiring.go builds) satisfies this
// with no adapter of its own, the identical "no adapter needed" property
// interfaces.go's own precedent already establishes elsewhere.
type resolumeActionDispatcher interface {
	Dispatch(ctx context.Context, action string, params map[string]any, now time.Time) (api.ResolumeActionResult, error)
}

// audioActionDispatcher is this package's own minimal view of
// [api.AudioActionDispatcher]: dispatch one audio.session.*/audio.gain.*/
// audio.output.* command through the SAME in-process dispatch/confirm/audit
// core the HTTP audio.session.* routes use — the audio integration's own
// mirror of fppDispatcher's identical role one type up.
// *api.AudioActionDispatcher satisfies this with no adapter.
type audioActionDispatcher interface {
	Dispatch(ctx context.Context, in api.AudioDispatchInput) (v1.AudioSessionCommandResult, *v1.Problem, error)
}

// mqttRegistry is this package's own minimal view of [broker.Registry]:
// resolve a broker identifier, publish through it, and run one
// publish-then-wait exchange through it. *broker.Registry satisfies this
// with no adapter. Declared here for the identical testability reason as
// fppDispatcher.
type mqttRegistry interface {
	Publish(ctx context.Context, id, topic string, qos byte, retain bool, payload []byte) error
	AwaitResponse(ctx context.Context, id string, req broker.ResponseRequest) (broker.Message, error)
}

// Dependencies is everything [NewExecutor] needs. Every field with a
// concrete production implementation names it in its own doc comment;
// every field is also satisfiable by a lightweight fake for testing this
// package in isolation from a live store, a live FPP host, or a live MQTT
// broker.
type Dependencies struct {
	// Store is the coordinator's own SQLite store: schemaV7's
	// macro_runs/macro_run_steps repository (store/macro_runs.go), the
	// generic (kind,id) config object/revision reader
	// (store/config.go — this package reads "show.macro" and
	// "show.action" objects through it, per STEP-9-SPEC.md section 3's
	// "no generic kind registry, no interface, hand-written per kind"
	// rule, mirrored here as "read the two hand-written payload shapes
	// Wave 2 Builder A's internal/coordinator/config package already
	// declares"), and the append-only event log (store/events.go), used
	// to record a buffered plugin failure report as an operator-visible
	// event (STEP-9-SPEC.md section 8.3 path 2).
	//
	// A concrete field rather than a narrow interface, matching this
	// codebase's own established precedent for anything store-touching
	// in a _test.go file (internal/coordinator/api/auth_test.go,
	// config_test.go, discovery_test.go, ... all construct a real
	// store.Open(...) rather than a fake) — see this package's own
	// report for why that precedent was followed here over interfaces.go's
	// narrower-interface convention.
	Store *store.Store

	// Identity is [identity.Service]: this package writes its own
	// submission-time and MQTT-step audit entries directly against it
	// (identity.Service.AuditedWrite for the run's own creation,
	// identity.Service.WriteAudit for an MQTT step's dispatch/outcome
	// pair, which — unlike an FPP step — has no existing dispatch seam
	// to do this on this package's behalf).
	Identity identity.Service

	// Dispatch is the in-process FPP command dispatch seam. The real
	// value is *api.FPPCommandDispatcher, built with
	// [api.NewFPPCommandDispatcher] against the SAME [api.Dependencies]
	// and [api.Options] the coordinator's HTTP surface uses, so both
	// dispatch through the identical core (that constructor's own doc
	// comment explains why this is safe to build twice rather than
	// sharing one *handlers value).
	Dispatch fppDispatcher

	// Brokers is the named-broker registry for the external MQTT macro
	// step (STEP-9-SPEC.md section 7, section 2.10). The real value is
	// *broker.Registry, populated by the coordinator's own wiring from
	// SHOWMESH_INTEGRATION_BROKERS (wave 2 shared contract section 5) —
	// never the control-plane broker, which is never registered under
	// any identifier. May be nil if the deployment declares no
	// integration brokers; every mqttRegistry call site handles that as
	// an ordinary broker.ErrUnknownBroker rather than a nil dereference
	// (see step_mqtt.go).
	Brokers mqttRegistry

	// ResolumeActions is Track D seam D-3/A's action engine, reached
	// through the SAME [api.ResolumeActionDispatcher]
	// api.Dependencies.ResolumeActions holds — coordinator.go wires this
	// field from that exact value (apiDeps.ResolumeActions), so a macro's
	// Resolume step and the HTTP endpoint dispatch through one path, never
	// two. May be nil when no live Resolume instance is configured on this
	// coordinator at all; every call site in step_resolume.go handles that
	// as an ordinary refused outcome rather than a nil dereference,
	// matching Brokers' identical nil-safe posture above.
	ResolumeActions resolumeActionDispatcher

	// AudioActions is the in-process audio dispatch seam
	// (ADR-029/STEP-9-SPEC.md section 5.3's fourth show.action integration).
	// The real value is *api.AudioActionDispatcher, built with
	// [api.NewAudioActionDispatcher] against the SAME [api.Dependencies] and
	// [api.Options] the coordinator's HTTP surface uses, mirroring Dispatch's
	// (above) identical FPP pattern: both dispatch through the identical
	// core. May be nil when no audio publisher is configured on this
	// coordinator at all; step_audio.go's own call site handles that as an
	// ordinary refused outcome rather than a nil dereference, matching
	// ResolumeActions' identical nil-safe posture above.
	AudioActions audioActionDispatcher

	// Primitives is the Step 8 FPP primitive registry, used at resolve
	// time to re-normalize a pinned action's params out of stored JSON
	// and back into the natively-typed Go values
	// [api.FPPCommandInput.Params] requires. Defaults to
	// [api.FPPPrimitiveRegistry].
	//
	// This exists because a JSON round trip is not the identity function
	// for map[string]any. The config write path normalizes params to
	// string, bool and int64, and marshalling that map and unmarshalling
	// it back yields float64 for every number. Every integer-valued
	// primitive then reads its own parameter through a
	// params["x"].(int64) assertion whose ok is deliberately discarded,
	// so the value silently becomes 0. Measured on setVolume: an action
	// authored at volume 50 dispatched volume 0, recorded desired state
	// 0, and confirmed against 0, so the run reported confirmed while the
	// show played muted. Re-decoding through the same registry the write
	// path used is what keeps one normalization rule rather than two.
	Primitives config.FPPPrimitiveRegistry

	// Notify is called after every run-level state transition (created,
	// finished) so the change stream's hub (api/stream.go, built later)
	// picks it up on its next render pass. Nil is a valid, silent no-op
	// — every call site in this package checks before calling, so a
	// caller that has not wired the hub yet (a unit test, most of them)
	// never has to supply one.
	Notify func()

	// Clock returns the current time. Defaults to time.Now. A test
	// dependency, not a production one: every acceptance-relevant
	// timestamp this package writes ultimately comes from here.
	Clock func() time.Time

	// NewID mints a new run id. Defaults to uuid.NewString. Overridable
	// so a test can assert against a known id.
	NewID func() string

	// Logger is where this package logs failures it cannot return to
	// any caller (a background run's own store write failing, a
	// best-effort audit write failing). Defaults to slog.Default().
	Logger *slog.Logger
}

func (d Dependencies) withDefaults() Dependencies {
	if d.Clock == nil {
		d.Clock = time.Now
	}
	if d.NewID == nil {
		d.NewID = uuid.NewString
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Primitives == nil {
		d.Primitives = api.FPPPrimitiveRegistry
	}
	return d
}

// Options carries this package's own tunables, kept separate from
// Dependencies for the same reason api.Options is kept separate from
// api.Dependencies: these are values, not live collaborators, and every
// one has a stated default.
type Options struct {
	// MaxSnapshotFinishedRuns bounds how many already-finished runs
	// [Executor.SnapshotRuns] returns alongside every in-flight run.
	// STEP-9-SPEC.md section 6.6 requires "a bounded window of recently
	// finished ones" and leaves the bound to this builder to choose and
	// justify: see [DefaultMaxSnapshotFinishedRuns]'s own doc comment.
	MaxSnapshotFinishedRuns int

	// MaxPriorFailuresPerSubmit bounds how many entries of one
	// [api.MacroSubmitRequest.PriorFailures] this package will read
	// before coalescing and recording them. macro_seam.go's own doc
	// comment already requires the CALLER to bound what it sends; this
	// is this package's own second, independent bound on what it will
	// act on, matching LESSONS.md's "an unbounded write on a failure
	// path evicts the evidence it exists to preserve" — a caller bug or
	// a compromised plugin sending an oversized array must not turn this
	// package's own coalescing loop into an unbounded amount of work.
	MaxPriorFailuresPerSubmit int
}

// DefaultMaxSnapshotFinishedRuns is 20: enough that an operator
// reconnecting mid-show sees the last several macro-fired transitions
// (start, intermission cues, ...) without a re-fetch, small enough that
// GET /api/v1/snapshot — already a full-state payload rendered on every
// fresh connection per ADR-020 decision 3 — does not grow with the
// installation's entire run history. This is a SHOWMESH HYPOTHESIS, sized
// the same way store/events.go's DefaultEventsPageSize is: "clearly larger
// than one operator's screen of recent activity," not derived from a
// measured payload size.
const DefaultMaxSnapshotFinishedRuns = 20

// DefaultMaxPriorFailuresPerSubmit is 50: generous headroom over what a
// bounded plugin-side buffer (STEP-9-SPEC.md section 8.3 path 2, "bounded
// in count and age") would ever legitimately accumulate between two
// successful coordinator calls, while still refusing to iterate an
// unbounded caller-supplied slice.
const DefaultMaxPriorFailuresPerSubmit = 50

func (o Options) withDefaults() Options {
	if o.MaxSnapshotFinishedRuns <= 0 {
		o.MaxSnapshotFinishedRuns = DefaultMaxSnapshotFinishedRuns
	}
	if o.MaxPriorFailuresPerSubmit <= 0 {
		o.MaxPriorFailuresPerSubmit = DefaultMaxPriorFailuresPerSubmit
	}
	return o
}

// Executor is this package's [api.MacroRunner] implementation.
//
// Lifecycle: construct with [NewExecutor], call [Executor.Reconcile] once
// at coordinator startup — before the server begins listening, per
// STEP-9-SPEC.md section 6.5 — then call [Executor.SubmitRun]/[Executor.GetRun]/
// [Executor.ListRuns]/[Executor.SnapshotRuns] for as long as the
// coordinator serves requests. Call [Executor.Stop] during shutdown; see
// that method's own doc comment for what it does and, as importantly, what
// it does not.
type Executor struct {
	store           *store.Store
	identity        identity.Service
	dispatch        fppDispatcher
	brokers         mqttRegistry
	resolumeActions resolumeActionDispatcher
	audioActions    audioActionDispatcher
	prims           config.FPPPrimitiveRegistry
	notify          func()
	clock           func() time.Time
	newID           func() string
	logger          *slog.Logger

	maxSnapshotFinishedRuns   int
	maxPriorFailuresPerSubmit int

	// wg tracks every background run goroutine started by SubmitRun (and,
	// during Reconcile, none — Reconcile finishes stranded runs inline,
	// it starts nothing new), so Stop can report how long callers waited
	// for in-flight bookkeeping to settle. See Stop's own doc comment for
	// why this is a bound on WAITING, not a cancellation mechanism.
	wg sync.WaitGroup
}

var _ api.MacroRunner = (*Executor)(nil)

// NewExecutor builds an [Executor] from deps and opts, applying both
// types' own withDefaults.
func NewExecutor(deps Dependencies, opts Options) *Executor {
	deps = deps.withDefaults()
	opts = opts.withDefaults()
	return &Executor{
		store:                     deps.Store,
		identity:                  deps.Identity,
		dispatch:                  deps.Dispatch,
		brokers:                   deps.Brokers,
		resolumeActions:           deps.ResolumeActions,
		audioActions:              deps.AudioActions,
		prims:                     deps.Primitives,
		notify:                    deps.Notify,
		clock:                     deps.Clock,
		newID:                     deps.NewID,
		logger:                    deps.Logger,
		maxSnapshotFinishedRuns:   opts.MaxSnapshotFinishedRuns,
		maxPriorFailuresPerSubmit: opts.MaxPriorFailuresPerSubmit,
	}
}

// Stop waits, up to ctx's own deadline, for every background run goroutine
// [Executor.SubmitRun] has started to finish its own bookkeeping, and
// returns ctx.Err() if that deadline elapses first.
//
// What this does NOT do, and deliberately: cancel an in-flight run. A
// run's own execution context is built with context.WithoutCancel from
// its submitting request (STEP-9-SPEC.md's "runs execute detached ...  so
// an abandoned client cannot abort an in-flight run"), so it carries no
// cancellation this method — or anything else — could deliver, by
// construction, for the identical reason [api.FPPCommandDispatcher]'s own
// bgCtx is uncancellable once a dispatch begins. A coordinator process
// shutdown ends every goroutine regardless, the instant the process exits;
// this method exists only so a caller (coordinator.go's shutdown routine)
// can choose to wait a bounded amount of time for in-flight step
// bookkeeping (a store write, an audit append) to land before that exit,
// rather than racing it unconditionally. It is not, and cannot be, a way
// to make a running macro stop.
func (e *Executor) Stop(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Executor) now() time.Time { return e.clock() }

func (e *Executor) notifyChange() {
	if e.notify != nil {
		e.notify()
	}
}

func (e *Executor) logWarn(msg string, args ...any) {
	if e.logger != nil {
		e.logger.Warn(msg, args...)
	}
}

func (e *Executor) logError(msg string, args ...any) {
	if e.logger != nil {
		e.logger.Error(msg, args...)
	}
}

// ptrTime returns a pointer to a copy of t.
func ptrTime(t time.Time) *time.Time { return &t }

// strPtr returns a pointer to a copy of s.
func strPtr(s string) *string { return &s }

// errString is a small helper so a wrapped internal error can be logged
// with its full text server-side while never being handed to an
// operator-facing string builder in this package.
//
// This claim was false when it was first written: step_fpp.go passed the
// dispatch seam's own OutcomeReason straight through to the step's
// operator-facing reason, and on a transport failure that string
// interpolates the raw Go error, so a run's reason read "dispatching to
// FPP failed: fppcommand: dispatching "Stop Now": Post "http://.../api/
// command": dial tcp: connect: connection refused". A comment stating a
// rule the code does not follow is worse than no comment, because a
// reviewer ticks it off. It is true now, and
// TestFPPTransportFailureReasonCarriesNoRawGoError is what keeps it true.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
