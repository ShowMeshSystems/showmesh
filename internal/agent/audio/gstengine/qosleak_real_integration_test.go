//go:build cgo

package gstengine

import (
	"testing"

	"github.com/go-gst/go-gst/pkg/gst"
)

// TestSinkQosEventIsFreedByInterleave proves a GST_EVENT_QOS reaching
// interleave's src pad (what the output sink's qos=true posts upstream
// on every render, see buildPipeline) is actually freed rather than
// leaked. gst-plugins-good's interleave.c (GStreamer 1.26.2) refuses a
// QOS event in its src-pad event handler without ever calling
// gst_event_unref on it; without [dropLeakedQosEvents] this test fails
// because the event's reference count never drops back to 1.
func TestSinkQosEventIsFreedByInterleave(t *testing.T) {
	e := newTestEngine(t)

	interleaveElem := e.pipeline.(gst.Bin).GetByName("interleave")
	if interleaveElem == nil {
		t.Fatal("could not find the pipeline's interleave element by name")
	}
	srcPad := interleaveElem.GetStaticPad("src")
	if srcPad == nil {
		t.Fatal("interleave has no src pad")
	}

	ev := gst.NewEventQos(gst.QosTypeUnderflow, 1.0, 0, 0)
	if ev == nil {
		t.Fatal("gst.NewEventQos returned nil")
	}
	// Keep our own observer reference: SendEvent below takes ownership of
	// ev's original one and may hand it off further, so without this
	// extra ref there would be nothing left to inspect afterward.
	gst.UnsafeEventRef(ev)
	raw := gst.UnsafeEventToGlibNone(ev)

	srcPad.SendEvent(ev)

	mo := gst.UnsafeMiniObjectFromGlibBorrow(raw)
	writable := mo.IsWritable()
	// Release our observer ref regardless of outcome, so this test never
	// itself leaks the probe event it created.
	gst.UnsafeEventUnref(gst.UnsafeEventFromGlibBorrow(raw))

	if !writable {
		t.Fatal("QOS event reference count is still above 1 after reaching interleave's src pad: the event was leaked instead of freed")
	}
}
