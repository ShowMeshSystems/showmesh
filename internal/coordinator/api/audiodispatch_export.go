package api

import (
	"context"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
)

// AudioActionDispatcher is the in-process, exported entry point onto
// [handlers.executeAudioSessionDispatch] — the SAME dispatch/confirm/audit
// core every audio.session.*/audio.gain.*/audio.output.* HTTP route uses.
// Mirrors [FPPCommandDispatcher]'s identical role and doc comment: a caller
// in another package, in the same coordinator process (Step 9's macro
// executor, internal/coordinator/macro), uses this instead of issuing
// itself an HTTP request against its own coordinator.
type AudioActionDispatcher struct {
	h *handlers
}

// NewAudioActionDispatcher builds an [AudioActionDispatcher] from deps and
// opts, applying the identical defaulting [New] itself applies
// ([Dependencies.withDefaults]/[Options.withDefaults]) before building its
// own *handlers — the same "supported pattern for a coordinator wiring both
// this API's HTTP surface and Step 9's macro executor" [NewFPPCommandDispatcher]
// documents, applied to the audio seam: neither
// [handlers.executeAudioSessionDispatch] nor anything it calls reads or
// writes any *handlers field this constructor does not set, so two
// independently-constructed *handlers values sharing one Dependencies
// behave identically to one shared value for every purpose this type
// exists for.
func NewAudioActionDispatcher(deps Dependencies, opts Options) *AudioActionDispatcher {
	deps = deps.withDefaults()
	opts = opts.withDefaults()
	return &AudioActionDispatcher{h: &handlers{
		deps:   deps,
		clock:  opts.Clock,
		logger: opts.Logger,
	}}
}

// Dispatch runs one audio.session.*/audio.gain.*/audio.output.* command
// in-process, through the exact same dispatch/confirm/audit core the HTTP
// handler uses. The returned *v1.Problem is a caller-facing refusal; err is
// this coordinator's own dependency failing. Exactly one of (a non-empty
// AudioSessionCommandResult.CommandID, problem, err) is meaningful on
// return — mirroring [FPPCommandDispatcher.Dispatch]'s identical contract.
func (d *AudioActionDispatcher) Dispatch(ctx context.Context, in AudioDispatchInput) (v1.AudioSessionCommandResult, *v1.Problem, error) {
	return d.h.executeAudioSessionDispatch(ctx, d.h.now(), in)
}
