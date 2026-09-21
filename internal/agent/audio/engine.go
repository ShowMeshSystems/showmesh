package audio

import (
	"context"
	"errors"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// EngineHandle identifies one engine-side playback instance, minted by the
// session layer and opaque to Engine implementations.
type EngineHandle string

// observeTimeout bounds every supervision-driven [Engine.Observe] call:
// watchTick and checkFadeCompletionLocked poll every
// session's handle in one serial loop, and an unbounded call against one
// hung handle would stall supervision of every other session behind it.
// SHOWMESH HYPOTHESIS, NOT MEASURED: no bench data exists for the right
// bound against a real backend; 5 seconds is chosen to be well above a
// healthy poll's cost while still bounding a genuinely stuck call to a
// single missed tick rather than an indefinite stall.
var observeTimeout = 5 * time.Second // var, not const: shrunk by tests exercising the bound itself

// boundedObserveContext derives a child of ctx bounded by [observeTimeout],
// for a supervision-driven Observe call. The returned cancel must be
// called once the call returns, same as any context.WithTimeout use.
func boundedObserveContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, observeTimeout)
}

// engineCallTimeout bounds one state-changing Engine call issued from a
// path with no deadline of its own (RunWatcher's root context, agent.go).
// SHOWMESH HYPOTHESIS, NOT MEASURED: chosen above observeTimeout since
// these calls do real engine work Observe does not.
var engineCallTimeout = 10 * time.Second // var, not const: shrunk by tests exercising the bound itself

// boundedEngineCallContext derives a child of ctx bounded by
// [engineCallTimeout]. Call it fresh before each engine call, matching
// [boundedObserveContext]'s own per-call convention.
func boundedEngineCallContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, engineCallTimeout)
}

// EngineObservation is what an Engine reports about one handle, collected
// at ObservedAt. Every state-changing Engine method returns one collected
// strictly after the change took effect — never the request echoed back —
// so a caller can tell a genuine confirmation from an assumption that a
// dispatched change succeeded.
type EngineObservation struct {
	State      pkgaudio.State
	Position   time.Duration
	ObservedAt time.Time
	Reason     string

	// Gain is the handle's effective output gain as of ObservedAt.
	Gain pkgaudio.Gain

	// FadeActive reports whether a gain fade dispatched via [Engine.Fade]
	// is still in progress. A caller detects fade completion by observing
	// this transition true-to-false with Gain equal to the fade's target —
	// never by assuming a fade finished because its requested duration has
	// elapsed.
	FadeActive bool
}

// Engine drives a single media backend playing one asset at a time. It
// holds no notion of a playlist, a revision, or a session; the state
// machine in this package owns those and calls Engine only for whichever
// item is currently loaded on a given handle.
//
// This interface is deliberately the smallest set of verbs the session
// state machine needs, so any pipeline backend can implement it without
// the session layer changing shape. [internal/agent/audio/gstengine] is
// the one production implementation (ADR-007); [FakeEngine] implements
// the same interface only to prove the session layer against, never to
// play audio, see that type's doc comment.
//
// Every method here must give up when ctx's deadline expires, not merely
// accept ctx as a parameter: callers bound these calls (see
// [boundedEngineCallContext]/[boundedObserveContext]) precisely so one
// wedged call cannot hold a session's mutex forever, and that guarantee
// holds only as long as every implementation actually honours the
// deadline it is given.
type Engine interface {
	// Load prepares handle to play media, without starting it, so
	// readiness gating (a missing, changed, or undecodable asset) happens
	// before any Start is attempted. duration is the media's known
	// runtime, resolved by the caller (e.g. from a media probe) — Load
	// does not probe the asset itself.
	Load(ctx context.Context, handle EngineHandle, media pkgaudio.MediaRef, duration time.Duration) (EngineObservation, error)

	// Start begins playback of a loaded handle from position.
	Start(ctx context.Context, handle EngineHandle, position time.Duration) (EngineObservation, error)

	// Pause suspends playback, preserving position.
	Pause(ctx context.Context, handle EngineHandle) (EngineObservation, error)

	// Resume continues playback from the position Pause left it at.
	Resume(ctx context.Context, handle EngineHandle) (EngineObservation, error)

	// Seek is a discontinuity: the caller must treat the returned
	// observation's position as freshly re-anchored, never as a
	// continuation of pre-seek timing — never extrapolated and reported
	// as synchronized.
	Seek(ctx context.Context, handle EngineHandle, position time.Duration) (EngineObservation, error)

	// Stop ends playback. Distinct from natural completion — see
	// [EngineObservation]'s State, which reports Stopped or Completed as
	// two permanently different values — a session that stops on its own
	// must never be reported as if it had been commanded to stop.
	Stop(ctx context.Context, handle EngineHandle) (EngineObservation, error)

	// SetGain changes handle's output gain immediately, cancelling any
	// fade in progress on it. The caller is responsible for ceiling
	// enforcement before calling this — Engine trusts the gain it is
	// given and does not itself hold a ceiling.
	SetGain(ctx context.Context, handle EngineHandle, gain pkgaudio.Gain) (EngineObservation, error)

	// Fade begins ramping handle's output gain toward fade.TargetGain over
	// fade.Duration along fade.Curve, replacing any fade already in
	// progress. It returns immediately with the fade just started, not its
	// eventual result — completion is read back later via [Engine.Observe]
	// or a subsequent Fade/SetGain call, per [EngineObservation.FadeActive]
	// and [EngineObservation.Gain], never inferred from fade.Duration
	// alone.
	Fade(ctx context.Context, handle EngineHandle, fade pkgaudio.Fade) (EngineObservation, error)

	// Release discards handle and any engine-side resources it holds.
	// Idempotent: releasing an already-released or never-loaded handle is
	// not an error.
	Release(ctx context.Context, handle EngineHandle) error

	// Observe returns handle's current state with no side effects other
	// than a state transition an already-elapsed play run has genuinely
	// reached (e.g. natural completion) — the read-back evidence a
	// confirmation compares against, collected fresh on every call, never
	// cached across calls.
	Observe(ctx context.Context, handle EngineHandle) (EngineObservation, error)

	// Available reports whether this engine can actually play anything,
	// with a reason when it cannot. A caller that only ever sees
	// Available() == false must refuse every transition with that
	// reason, never silently no-op it, and must never advertise a
	// playback capability while it is false.
	Available() (ok bool, reason string)

	// LiveHandles reports every handle this Engine currently holds live,
	// regardless of whether any session still references it — see
	// [Manager]'s own watcher, which sweeps whatever this reports orphaned.
	LiveHandles(ctx context.Context) ([]EngineHandle, error)

	// ReleaseAll tears down every live branch this Engine holds except the
	// handles named in except, and reports how many it released. It exists
	// for [Manager.SilenceAll]/[Manager.SilenceAllExcept]'s own final sweep:
	// that call must reach a branch even when the session layer's own
	// per-session accounting missed it, so an emergency stop never leaves
	// audio playing because one session's own bookkeeping lost track of it.
	ReleaseAll(ctx context.Context, except ...EngineHandle) (released int, err error)
}

// ErrHandleNotLoaded is the error class an [Engine] wraps its own "no such
// handle" error with, so a caller can tell "this session's handle is
// genuinely gone" apart from every other engine failure via errors.Is,
// without matching on backend-specific error text. See
// [Manager.stopExecLocked]'s own use: a commanded stop against a handle
// the engine no longer holds resolves the session exactly as a
// successful stop, rather than sticking it in StateStopping forever.
var ErrHandleNotLoaded = errors.New("audio: engine holds no such handle")
