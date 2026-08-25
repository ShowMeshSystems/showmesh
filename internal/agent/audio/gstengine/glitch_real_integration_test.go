//go:build cgo

package gstengine

import (
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
)

// This suite drives the same real GStreamer pipeline and bus as
// engine_real_integration_test.go (fakesink, no hardware). It proves the
// bus watcher's handling of WARNING and QOS messages -- previously
// silently discarded -- by posting genuine,
// correctly-formed GstMessage values onto the pipeline's real bus
// (Element.MessageFull for the warning, matching gst_element_message_full,
// the same call every real element's GST_ELEMENT_WARNING macro makes
// internally; gst.NewMessageQos for the QOS message, a real public
// constructor), which watchBus then picks up through its own real
// TimedPop loop exactly as it would a message an element posted itself.
// This does not touch audio hardware and provokes no genuine ALSA
// condition; it exercises the bus-message handling watchBus itself owns.

// TestWarningAndQosMessagesAreCountedNotIgnored proves a WARNING or QOS
// bus message -- how a real alsasink/audiobasesink reports xruns,
// underruns, and clock-drift sample drop/insert -- is counted, and that
// counting it never marks the engine broken. Before this fix, watchBus's
// switch matched only gst.MessageError; a warning or QOS message reached
// the same TimedPop loop and was silently dropped by falling through the
// switch with no case matched, leaving no counter, no log line, and no
// change to Available() -- audible glitching with a clean record.
func TestWarningAndQosMessagesAreCountedNotIgnored(t *testing.T) {
	e := newTestEngine(t)

	before, known := e.GlitchCounts()
	if !known {
		t.Fatalf("GlitchCounts: known = false for a real gstengine.Engine, want true (a real engine always counts, even at zero)")
	}

	bus := e.pipeline.GetBus()
	src := e.pipeline.(gst.Object)
	pipelineElement := e.pipeline.(gst.Element)

	// A real element reporting an ALSA xrun/underrun or a clock-drift
	// sample drop/insert does exactly this: GST_ELEMENT_WARNING expands
	// to gst_element_message_full, which MessageFull wraps directly.
	pipelineElement.MessageFull(gst.MessageWarning, gst.StreamErrorQuark(), int32(gst.StreamErrorTooLazy),
		"injected by TestWarningAndQosMessagesAreCountedNotIgnored", "test debug detail", "glitch_real_integration_test.go", "test", 0)

	qos := gst.NewMessageQos(src, true, 0, 0, 0, 0)
	if !bus.Post(qos) {
		t.Fatalf("bus.Post(qos message) returned false")
	}

	deadline := time.Now().Add(2 * time.Second)
	var after agentaudio.GlitchCounts
	for {
		var ok bool
		after, ok = e.GlitchCounts()
		if !ok {
			t.Fatalf("GlitchCounts: known became false after startup")
		}
		if after.Warnings > before.Warnings && after.QosEvents > before.QosEvents {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("GlitchCounts did not increment after posting one warning and one QOS bus message within %s: before=%+v after=%+v",
				2*time.Second, before, after)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A degraded-but-alive condition (a dropped buffer, a clock warning)
	// is not a pipeline failure: Available() must keep reporting true.
	if ok, reason := e.Available(); !ok {
		t.Fatalf("Available() = false (%s) after a warning/QOS message; a degraded-but-alive condition must never be reported as broken", reason)
	}
}
