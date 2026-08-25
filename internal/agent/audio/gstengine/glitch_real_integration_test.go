//go:build cgo

package gstengine

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
)

// This suite proves watchBus's WARNING/QOS handling against a real bus, by posting real GstMessage values onto it (not a hardware condition; no ALSA underrun is provoked or verified here).

// TestWarningAndQosMessagesAreCountedNotIgnored proves a stream-domain
// WARNING and a QOS message are each counted in their own bucket and
// never mark the engine broken.
func TestWarningAndQosMessagesAreCountedNotIgnored(t *testing.T) {
	e := newTestEngine(t)

	before, known := e.GlitchCounts()
	if !known {
		t.Fatalf("GlitchCounts: known = false for a real gstengine.Engine, want true (a real engine always counts, even at zero)")
	}

	bus := e.pipeline.GetBus()
	src := e.pipeline.(gst.Object)
	pipelineElement := e.pipeline.(gst.Element)

	// GST_ELEMENT_WARNING expands to gst_element_message_full, which
	// MessageFull wraps directly -- a real, correctly-formed warning.
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
		if after.StreamWarnings > before.StreamWarnings && after.QosEvents > before.QosEvents {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("GlitchCounts did not increment after posting one stream warning and one QOS bus message within %s: before=%+v after=%+v",
				2*time.Second, before, after)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if after.ResourceWarnings != before.ResourceWarnings || after.OtherWarnings != before.OtherWarnings {
		t.Errorf("a stream-domain warning also moved resource/other buckets: before=%+v after=%+v", before, after)
	}

	// A degraded-but-alive condition must never be reported as broken.
	if ok, reason := e.Available(); !ok {
		t.Fatalf("Available() = false (%s) after a warning/QOS message; want still available", reason)
	}
}

// TestResourceDomainWarningCountsSeparatelyFromStream proves domain
// classification actually discriminates, not just counts everything into
// one bucket.
func TestResourceDomainWarningCountsSeparatelyFromStream(t *testing.T) {
	e := newTestEngine(t)

	before, known := e.GlitchCounts()
	if !known {
		t.Fatalf("GlitchCounts: known = false, want true")
	}

	pipelineElement := e.pipeline.(gst.Element)
	pipelineElement.MessageFull(gst.MessageWarning, gst.ResourceErrorQuark(), int32(gst.ResourceErrorWrite),
		"injected by TestResourceDomainWarningCountsSeparatelyFromStream", "test debug detail", "glitch_real_integration_test.go", "test", 0)

	deadline := time.Now().Add(2 * time.Second)
	var after agentaudio.GlitchCounts
	for {
		var ok bool
		after, ok = e.GlitchCounts()
		if !ok {
			t.Fatalf("GlitchCounts: known became false after startup")
		}
		if after.ResourceWarnings > before.ResourceWarnings {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ResourceWarnings did not increment after posting one resource-domain warning within %s: before=%+v after=%+v", 2*time.Second, before, after)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if after.StreamWarnings != before.StreamWarnings {
		t.Errorf("a resource-domain warning also moved the stream bucket: before=%+v after=%+v", before, after)
	}
}

// TestSinkHasQosEnabled proves buildPipeline turns qos on for the output
// sink, without which GstBaseSink never posts a QOS bus message for a
// dropped/skipped buffer and QosEvents would read zero forever.
func TestSinkHasQosEnabled(t *testing.T) {
	e := newTestEngine(t)
	sink := e.pipeline.(gst.Bin).GetByName("sink")
	if sink == nil {
		t.Fatal("could not find the pipeline's sink element by name")
	}
	got := sink.ObjectProperty("qos")
	on, ok := got.(bool)
	if !ok || !on {
		t.Fatalf("sink qos property = %#v, want true", got)
	}
}

// syncBuffer is a mutex-guarded bytes.Buffer, safe for the watchBus
// goroutine to write into while the test goroutine reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestWarningLogIsRateLimitedButCountingIsNot proves a burst of
// warnings is fully counted while the log line is rate-limited -- the
// count is the durable evidence, the log is a bounded convenience.
func TestWarningLogIsRateLimitedButCountingIsNot(t *testing.T) {
	e := newTestEngine(t)

	var buf syncBuffer
	origLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(origLogger) })

	before, _ := e.GlitchCounts()
	pipelineElement := e.pipeline.(gst.Element)
	const n = 5
	for i := 0; i < n; i++ {
		pipelineElement.MessageFull(gst.MessageWarning, gst.StreamErrorQuark(), int32(gst.StreamErrorTooLazy),
			fmt.Sprintf("rapid warning %d", i), "test debug detail", "glitch_real_integration_test.go", "test", 0)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		after, ok := e.GlitchCounts()
		if !ok {
			t.Fatalf("GlitchCounts: known became false after startup")
		}
		if after.StreamWarnings-before.StreamWarnings >= n {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("StreamWarnings only reached %d of %d rapid warnings within %s", after.StreamWarnings-before.StreamWarnings, n, 2*time.Second)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The count above already proves every message was counted. Give
	// any in-flight log write time to land before reading the buffer.
	time.Sleep(300 * time.Millisecond)

	lines := strings.Count(buf.String(), "gstengine: output pipeline warning")
	if lines >= n {
		t.Errorf("logged %d lines for %d warnings posted well within warningLogInterval, want fewer (rate limiting not working)", lines, n)
	}
}

// TestGlitchCountsSinceIsPopulatedAndStableAcrossReads proves Since is
// set (a real engine's counting epoch, not the zero value) and stays
// fixed across repeated reads of the same engine instance, so a
// consumer can use it to detect a rebind.
func TestGlitchCountsSinceIsPopulatedAndStableAcrossReads(t *testing.T) {
	e := newTestEngine(t)

	first, ok := e.GlitchCounts()
	if !ok {
		t.Fatalf("GlitchCounts: known = false, want true")
	}
	if first.Since.IsZero() {
		t.Fatal("GlitchCounts().Since is the zero value, want the engine's start time")
	}

	second, ok := e.GlitchCounts()
	if !ok {
		t.Fatalf("GlitchCounts: known = false on second read, want true")
	}
	if !second.Since.Equal(first.Since) {
		t.Errorf("Since changed across reads of the same engine: %v -> %v", first.Since, second.Since)
	}
}
