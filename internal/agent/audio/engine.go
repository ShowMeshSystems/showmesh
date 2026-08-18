package audio

import (
	"context"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// EngineHandle identifies one engine-side playback instance, minted by the
// session layer and opaque to Engine implementations.
type EngineHandle string

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
}

// Engine drives a single media backend playing one asset at a time. It
// holds no notion of a playlist, a revision, or a session; the state
// machine in this package owns those and calls Engine only for whichever
// item is currently loaded on a given handle.
//
// This interface is deliberately the smallest set of verbs the session
// state machine needs, so either candidate pipeline backend (Go GStreamer
// bindings, or a supervised host process speaking some IPC) can implement
// it without the session layer changing shape. The pipeline backend
// itself is an open owner decision; [FakeEngine] is the only
// implementation in this repository and exists to prove the session layer
// against, never to play audio — see that type's doc comment.
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
}
