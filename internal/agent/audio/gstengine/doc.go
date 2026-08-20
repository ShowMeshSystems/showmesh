// Package gstengine implements the internal/agent/audio Engine interface
// against real GStreamer via go-gst (ADR-007). One Engine instance owns
// one continuously running output pipeline for the node's audio device:
// concurrent sessions are branches mixed by a single audiomixer, and the
// mixed program bus is placed onto configured channel indices of an
// interleave stage feeding one physical sink, so program and LTC can
// eventually share the single clock domain ADR-018 requires.
//
// The real implementation is built under "cgo"; a "!cgo" build of this
// package still compiles and returns an honestly unavailable Engine, so
// the coordinator's CGO_ENABLED=0 static build is unaffected by this
// package existing in the module.
package gstengine
