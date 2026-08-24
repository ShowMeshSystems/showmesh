package audio

import (
	"context"
	"fmt"
	"sync"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// FakeEngineUnavailableReason is [FakeEngine.Available]'s constant reason.
// It names the open owner decision this engine stands in for, so anything
// that surfaces the reason to an operator points at the actual blocker
// rather than an unexplained "false".
const FakeEngineUnavailableReason = "no pipeline backend is implemented; nothing plays audio"

// FakeEngine is a deterministic, in-memory [Engine] with no real playback:
// position is computed from an injected clock, and Available always
// reports false. It exists to prove the session state machine's behavior
// against a substitutable Engine — never to be reported, logged, or
// tested as a working audio engine. Nothing built against FakeEngine may
// claim otherwise.
type FakeEngine struct {
	now func() time.Time

	mu       sync.Mutex
	handles  map[EngineHandle]*fakeHandle
	failNext map[EngineHandle]error

	// ltc is FakeEngine's single, handle-less LTC run, matching
	// [LTCGenerator]'s own shape: this node has one LTC output, not one
	// per session. Zero value reports [LTCStopped] with
	// fakeLTCNeverStartedReason, never an empty state.
	ltc         LTCObservation
	ltcFailNext error

	// ltcRequest is the most recent StartLTC spec, held so a test can
	// assert what a lifecycle transition asked for separately from
	// whether any frame was then emitted. Cleared by StopLTC.
	ltcRequest   LTCSpec
	ltcRequested bool
}

// fakeLTCNeverStartedReason is FakeEngine's LTC state before StartLTC is
// ever called, or after StopLTC last ran — never mistakable for a real
// backend's own wording, per this type's doc comment.
const fakeLTCNeverStartedReason = "fake engine: no LTC run has been started"

// fakeLTCRequestedReason is the state between a StartLTC request and the
// first emitted frame.
const fakeLTCRequestedReason = "fake engine: LTC run requested; no frame emitted yet"

type fakeHandle struct {
	media    pkgaudio.MediaRef
	duration time.Duration
	state    pkgaudio.State
	// position is the playback position as of the last state change;
	// while playing, the true current position is position plus elapsed
	// time since playStartedAt, computed lazily on read.
	position      time.Duration
	playStartedAt time.Time // zero when not currently playing

	// gain is the handle's output gain as of the last resolved change;
	// while fade is non-nil, the true current gain is interpolated
	// lazily on read exactly as position is derived from playStartedAt.
	gain pkgaudio.Gain
	fade *fakeFade
}

// fakeFade is one in-progress [Engine.Fade] ramp: linear interpolation
// between the gain the fade started at and its target, timed against the
// engine's own injected clock. elapsed accumulates only the wall time
// this ramp has actually spent running; runningSince is when it last
// started counting toward elapsed, and is zero while Pause or Stop holds
// it, matching the real engine, where a genuinely blocked branch cannot
// advance its own ramp either (see gstengine's blockFlow).
type fakeFade struct {
	startGain    pkgaudio.Gain
	targetGain   pkgaudio.Gain
	duration     time.Duration
	elapsed      time.Duration
	runningSince time.Time
}

// NewFakeEngine returns a FakeEngine using now for its internal clock.
func NewFakeEngine(now func() time.Time) *FakeEngine {
	return &FakeEngine{now: now, handles: make(map[EngineHandle]*fakeHandle), failNext: make(map[EngineHandle]error)}
}

// InjectFailure arms handle so its next call to Load, Start, Pause,
// Resume, Seek, Stop, Release, or Observe returns err instead of running
// normally, then
// disarms itself — a one-shot fault, not a standing one, so a test
// controls exactly which call fails. There is no real backend to produce
// a pipeline crash, a freeze, a route change, or timing-authority loss
// (AUDIO-ENGINE section 11.4); this is how a test exercises the session
// layer's classification of those four against something that can never
// happen on its own here. err should wrap one of this package's
// ErrEngine* sentinels ([pkgaudio.ClassifyFault]) for a test to assert a
// specific fault class, or an unwrapped error to exercise [pkgaudio.
// FaultOther].
func (e *FakeEngine) InjectFailure(handle EngineHandle, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.failNext[handle] = err
}

// takeFailure returns and clears handle's armed failure, if any. Callers
// hold e.mu.
func (e *FakeEngine) takeFailure(handle EngineHandle) error {
	err, ok := e.failNext[handle]
	if !ok {
		return nil
	}
	delete(e.failNext, handle)
	return err
}

// Available always reports false with [FakeEngineUnavailableReason].
func (e *FakeEngine) Available() (bool, string) {
	return false, FakeEngineUnavailableReason
}

func (e *FakeEngine) obs(h *fakeHandle) EngineObservation {
	position := e.currentPosition(h)
	gain := e.currentGain(h)
	return EngineObservation{
		State: h.state, Position: position, ObservedAt: e.now(),
		Gain: gain, FadeActive: h.fade != nil,
	}
}

// currentGain resolves h.fade to h.gain and clears it, exactly once, the
// instant the ramp's elapsed wall time reaches its duration — never
// before, and never inferred by the caller from duration alone. Callers
// hold e.mu.
func (e *FakeEngine) currentGain(h *fakeHandle) pkgaudio.Gain {
	if h.fade == nil {
		return h.gain
	}
	elapsed := h.fade.elapsed
	if !h.fade.runningSince.IsZero() {
		elapsed += e.now().Sub(h.fade.runningSince)
	}
	if elapsed >= h.fade.duration {
		h.gain = h.fade.targetGain
		h.fade = nil
		return h.gain
	}
	frac := float64(elapsed) / float64(h.fade.duration)
	span := float64(h.fade.targetGain) - float64(h.fade.startGain)
	return pkgaudio.Gain(float64(h.fade.startGain) + span*frac)
}

// freezeFade holds h's in-progress fade exactly where currentGain
// reports it right now: no further wall-clock time counts toward the
// ramp until resumeFade runs. A no-op when there is no fade, or it is
// already frozen. Callers hold e.mu.
func (e *FakeEngine) freezeFade(h *fakeHandle) {
	if h.fade == nil || h.fade.runningSince.IsZero() {
		return
	}
	h.fade.elapsed += e.now().Sub(h.fade.runningSince)
	h.fade.runningSince = time.Time{}
}

// resumeFade lets a fade freezeFade held start advancing again from
// where it was left. A no-op when there is no fade. Callers hold e.mu.
func (e *FakeEngine) resumeFade(h *fakeHandle) {
	if h.fade == nil {
		return
	}
	h.fade.runningSince = e.now()
}

// currentPosition advances h.state to Completed, and clamps position to
// h.duration, when a Playing handle's elapsed wall time has reached its
// known duration. Callers holding e.mu.
func (e *FakeEngine) currentPosition(h *fakeHandle) time.Duration {
	if h.state != pkgaudio.StatePlaying || h.playStartedAt.IsZero() {
		return h.position
	}
	elapsed := h.position + e.now().Sub(h.playStartedAt)
	if h.duration > 0 && elapsed >= h.duration {
		h.position = h.duration
		h.state = pkgaudio.StateCompleted
		h.playStartedAt = time.Time{}
		return h.duration
	}
	return elapsed
}

func (e *FakeEngine) Load(_ context.Context, handle EngineHandle, media pkgaudio.MediaRef, duration time.Duration) (EngineObservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.takeFailure(handle); err != nil {
		return EngineObservation{}, err
	}
	h := &fakeHandle{media: media, duration: duration, state: pkgaudio.StateReady, gain: 1}
	e.handles[handle] = h
	return e.obs(h), nil
}

func (e *FakeEngine) get(handle EngineHandle) (*fakeHandle, error) {
	h, ok := e.handles[handle]
	if !ok {
		return nil, fmt.Errorf("audio: fake engine has no loaded handle %q", handle)
	}
	return h, nil
}

func (e *FakeEngine) Start(_ context.Context, handle EngineHandle, position time.Duration) (EngineObservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h, err := e.get(handle)
	if err != nil {
		return EngineObservation{}, err
	}
	if err := e.takeFailure(handle); err != nil {
		return EngineObservation{}, err
	}
	h.position = position
	h.state = pkgaudio.StatePlaying
	h.playStartedAt = e.now()
	return e.obs(h), nil
}

func (e *FakeEngine) Pause(_ context.Context, handle EngineHandle) (EngineObservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h, err := e.get(handle)
	if err != nil {
		return EngineObservation{}, err
	}
	if err := e.takeFailure(handle); err != nil {
		return EngineObservation{}, err
	}
	h.position = e.currentPosition(h)
	e.freezeFade(h)
	if h.state == pkgaudio.StatePlaying {
		h.state = pkgaudio.StatePaused
	}
	h.playStartedAt = time.Time{}
	return e.obs(h), nil
}

func (e *FakeEngine) Resume(_ context.Context, handle EngineHandle) (EngineObservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h, err := e.get(handle)
	if err != nil {
		return EngineObservation{}, err
	}
	if h.state != pkgaudio.StatePaused {
		return EngineObservation{}, fmt.Errorf("audio: fake engine cannot resume handle %q from state %q", handle, h.state)
	}
	if err := e.takeFailure(handle); err != nil {
		return EngineObservation{}, err
	}
	h.state = pkgaudio.StatePlaying
	h.playStartedAt = e.now()
	e.resumeFade(h)
	return e.obs(h), nil
}

func (e *FakeEngine) Seek(_ context.Context, handle EngineHandle, position time.Duration) (EngineObservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h, err := e.get(handle)
	if err != nil {
		return EngineObservation{}, err
	}
	if err := e.takeFailure(handle); err != nil {
		return EngineObservation{}, err
	}
	h.position = position
	if h.state == pkgaudio.StatePlaying {
		h.playStartedAt = e.now()
	}
	return e.obs(h), nil
}

func (e *FakeEngine) Stop(_ context.Context, handle EngineHandle) (EngineObservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h, err := e.get(handle)
	if err != nil {
		return EngineObservation{}, err
	}
	if err := e.takeFailure(handle); err != nil {
		return EngineObservation{}, err
	}
	e.freezeFade(h)
	h.state = pkgaudio.StateStopped
	h.playStartedAt = time.Time{}
	return e.obs(h), nil
}

func (e *FakeEngine) SetGain(_ context.Context, handle EngineHandle, gain pkgaudio.Gain) (EngineObservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h, err := e.get(handle)
	if err != nil {
		return EngineObservation{}, err
	}
	h.gain = gain
	h.fade = nil
	return e.obs(h), nil
}

func (e *FakeEngine) Fade(_ context.Context, handle EngineHandle, fade pkgaudio.Fade) (EngineObservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h, err := e.get(handle)
	if err != nil {
		return EngineObservation{}, err
	}
	start := e.currentGain(h)
	f := &fakeFade{startGain: start, targetGain: fade.TargetGain, duration: fade.Duration}
	// A fade dispatched while paused or stopped starts already frozen:
	// the real engine's Fade can be issued in either state too (Fade
	// only refuses a branch that has never been Start'd), and its ramp
	// cannot advance without flow any more than one already in progress
	// can.
	if h.state == pkgaudio.StatePlaying {
		f.runningSince = e.now()
	}
	h.fade = f
	return e.obs(h), nil
}

func (e *FakeEngine) Release(_ context.Context, handle EngineHandle) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.takeFailure(handle); err != nil {
		return err
	}
	delete(e.handles, handle)
	return nil
}

func (e *FakeEngine) Observe(_ context.Context, handle EngineHandle) (EngineObservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h, err := e.get(handle)
	if err != nil {
		return EngineObservation{}, err
	}
	if err := e.takeFailure(handle); err != nil {
		return EngineObservation{}, err
	}
	return e.obs(h), nil
}

// InjectLTCFailure arms a one-shot failure for the next call to StartLTC
// or StopLTC, then disarms itself — the same one-shot shape as
// [FakeEngine.InjectFailure], for a test to prove that an LTC failure
// never fails the session operation it accompanied.
func (e *FakeEngine) InjectLTCFailure(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ltcFailNext = err
}

func (e *FakeEngine) takeLTCFailure() error {
	err := e.ltcFailNext
	e.ltcFailNext = nil
	return err
}

// StartLTC records a request to begin, or realign, FakeEngine's one LTC
// run at spec's timecode. It deliberately does NOT report [LTCRunning]:
// a dispatch is not evidence that a sample was emitted, and a fake that
// confirmed its own request would make every test pass against a backend
// that emits nothing. [FakeEngine.EmitLTCFrame] is how a test says the
// backend actually produced output.
func (e *FakeEngine) StartLTC(_ context.Context, spec LTCSpec) (LTCObservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.takeLTCFailure(); err != nil {
		e.ltc = LTCObservation{State: LTCFailed, Reason: err.Error(), ObservedAt: e.now()}
		return e.ltc, err
	}
	e.ltcRequest, e.ltcRequested = spec, true
	e.ltc = LTCObservation{State: LTCStopped, Reason: fakeLTCRequestedReason, ObservedAt: e.now()}
	return e.ltc, nil
}

// EmitLTCFrame reports that the requested run produced a frame, which is
// the only thing that ever makes this fake report [LTCRunning].
func (e *FakeEngine) EmitLTCFrame() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.ltcRequested {
		return
	}
	e.ltc = LTCObservation{
		State:          LTCRunning,
		FrameRateKnown: true, FrameRate: e.ltcRequest.FrameRate,
		TimecodeKnown: true, Timecode: e.ltcRequest.StartTimecode,
		ObservedAt: e.now(),
	}
}

// LastLTCRequest returns the most recent StartLTC spec and whether a run
// is currently requested.
func (e *FakeEngine) LastLTCRequest() (LTCSpec, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ltcRequest, e.ltcRequested
}

// StopLTC ends FakeEngine's LTC run.
func (e *FakeEngine) StopLTC(_ context.Context) (LTCObservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.takeLTCFailure(); err != nil {
		return e.ltc, err
	}
	e.ltcRequest, e.ltcRequested = LTCSpec{}, false
	e.ltc = LTCObservation{State: LTCStopped, Reason: "fake engine: LTC run stopped", ObservedAt: e.now()}
	return e.ltc, nil
}

// ObserveLTC returns FakeEngine's current LTC evidence, freshly stamped
// on every call.
func (e *FakeEngine) ObserveLTC(_ context.Context) LTCObservation {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ltc.State == "" {
		return LTCObservation{State: LTCStopped, Reason: fakeLTCNeverStartedReason, ObservedAt: e.now()}
	}
	obs := e.ltc
	obs.ObservedAt = e.now()
	return obs
}
