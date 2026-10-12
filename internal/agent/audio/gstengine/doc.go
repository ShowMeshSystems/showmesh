// Package gstengine implements the internal/agent/audio Engine interface
// against real GStreamer via go-gst (ADR-007). One Engine instance owns
// one continuously running output pipeline for the node's audio device:
// concurrent sessions are branches mixed by a single audiomixer, and the
// mixed program bus is placed onto configured channel indices of an
// interleave stage feeding one physical sink, so program and LTC can
// eventually share the single clock domain ADR-018 requires.
//
// Position is a position query on a branch's volume element, which GStreamer
// answers from upstream: the next buffer volume will receive, ahead of the mix
// by the branch queue. Nothing downstream of the mixers sees one branch alone,
// so a rendered position can only be modelled from the pipeline clock and the
// branch's pad offset (alignment.go), and that clock runs whether or not the
// sink played anything.
//
// The real implementation is built under "cgo"; a "!cgo" build of this
// package still compiles and returns an honestly unavailable Engine, so
// the coordinator's CGO_ENABLED=0 static build is unaffected by this
// package existing in the module.
package gstengine
