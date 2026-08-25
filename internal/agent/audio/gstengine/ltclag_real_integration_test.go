//go:build cgo

package gstengine

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
	"github.com/showmeshsystems/showmesh/internal/agent/audio/ltcgen/ltcdecodetest"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// This suite proves the LTC queue-lag fix directly against real decoded
// audio rather than fakesink or ShowMesh's own reported evidence: an
// appsink captures the actual interleaved output samples, channel 1
// (program) is scanned
// for the real onset of an audible tone, and channel 2 (LTC) is decoded
// by libltc through ltcdecodetest -- the same decoder used to prove
// ltcgen's own round trip. Comparing the two locates the lag entirely in
// the sample domain, so it holds regardless of how fast or slow this
// process actually runs the pipeline.

const ltcLagSampleRate = 44100

// ltcLagCapture pulls interleaved S16LE samples off a two-channel appsink
// and deinterleaves them into per-channel buffers on the fly, so a test
// can read a consistent, growing timeline while the pipeline is still
// running.
type ltcLagCapture struct {
	sink gstapp.AppSink

	mu    sync.Mutex
	ch1   []int16
	ch2   []int16
	total int

	stop chan struct{}
	done chan struct{}
}

func newLTCLagCapture(sink gstapp.AppSink) *ltcLagCapture {
	return &ltcLagCapture{sink: sink, stop: make(chan struct{}), done: make(chan struct{})}
}

func (c *ltcLagCapture) run() {
	defer close(c.done)
	for {
		select {
		case <-c.stop:
			return
		default:
		}
		sample := c.sink.TryPullSample(gst.ClockTime(200 * time.Millisecond))
		if sample == nil {
			continue
		}
		buf := sample.GetBuffer()
		if buf == nil {
			continue
		}
		info, ok := buf.Map(gst.MapRead)
		if !ok {
			continue
		}
		data := info.Int16Data(binary.LittleEndian)
		info.Unmap()

		c.mu.Lock()
		for i := 0; i+1 < len(data); i += 2 {
			c.ch1 = append(c.ch1, data[i])
			c.ch2 = append(c.ch2, data[i+1])
		}
		c.total = len(c.ch1)
		c.mu.Unlock()
	}
}

// framesCaptured is the sample-domain "now": how many interleaved frames
// have been pulled off the appsink so far. Because the appsink is
// sync=true, this tracks real time closely enough to correlate a
// wall-clock event (an ObserveLTC call) against a position in the
// captured timeline.
func (c *ltcLagCapture) framesCaptured() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}

func (c *ltcLagCapture) snapshot() (ch1, ch2 []int16) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch1 = make([]int16, len(c.ch1))
	copy(ch1, c.ch1)
	ch2 = make([]int16, len(c.ch2))
	copy(ch2, c.ch2)
	return ch1, ch2
}

func (c *ltcLagCapture) Stop() {
	close(c.stop)
	<-c.done
}

// newLTCLagSink builds a real, unpositioned appsink fixed to interleaved
// S16LE at ltcLagSampleRate/2 channels, meant for [useSinkElement].
// sync=true paces delivery to the pipeline clock, same as every other
// real-integration sink in this package (fakesink with sync=true) --
// this is a real, data-consuming sink, not a test double for GStreamer.
func newLTCLagSink(t *testing.T) gstapp.AppSink {
	t.Helper()
	gst.Init() // ElementFactoryMake below needs GStreamer initialized before New would otherwise do it
	el := gst.ElementFactoryMake("appsink", "ltclag-capture")
	if el == nil {
		t.Skip("skipping: could not construct appsink")
	}
	sink, ok := el.(gstapp.AppSink)
	if !ok {
		t.Fatalf("appsink element does not implement gstapp.AppSink")
	}
	sink.SetObjectProperty("caps", gst.CapsFromString(
		fmt.Sprintf("audio/x-raw,format=S16LE,rate=%d,channels=2,layout=interleaved", ltcLagSampleRate)))
	sink.SetObjectProperty("sync", true)
	sink.SetObjectProperty("emit-signals", false)
	sink.SetObjectProperty("max-buffers", uint32(0))
	sink.SetObjectProperty("drop", false)
	return sink
}

// newLTCLagEngine builds a 2-channel engine (1: program, 2: LTC) whose
// sink is a real appsink substituted via [useSinkElement], and starts
// capturing its output immediately. The caller must call cap.Stop()
// before the engine itself tears down.
func newLTCLagEngine(t *testing.T) (*Engine, *ltcLagCapture) {
	t.Helper()
	sink := newLTCLagSink(t)
	useSinkElement(t, sink)

	cfg := Config{
		SinkFactory:     "fakesink", // overridden by useSinkElement; must still name a real factory
		ProgramChannels: []int{1},
		LTCChannel:      2,
		ChannelCount:    2,
		SampleRate:      ltcLagSampleRate,
		Resolve:         resolveByRuntimeFilename,
	}
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: unexpected structural config error: %v", err)
	}
	if ok, reason := e.Available(); !ok {
		t.Skipf("skipping: gstengine unavailable in this environment: %s", reason)
	}

	cap := newLTCLagCapture(sink)
	go cap.run()
	t.Cleanup(func() {
		cap.Stop()
		_ = e.Close()
	})
	return e, cap
}

// toneOnsetFrame scans ch1 for the first sample whose magnitude clears
// silenceThreshold. Program channel content is otherwise exact digital
// silence up to that point (a keepalive audiotestsrc, see
// addMixerKeepAlive, feeding into the mixer with nothing else attached),
// so the very first sample above threshold is a clean edge -- a 440Hz
// sine's own half-cycle (~50 samples at 44.1kHz) is too short for a
// sustained-run requirement to be reliable here.
const silenceThreshold = int16(2000)

func toneOnsetFrame(t *testing.T, ch1 []int16) int {
	t.Helper()
	for i, v := range ch1 {
		if v > silenceThreshold || v < -silenceThreshold {
			return i
		}
	}
	t.Fatalf("no audible tone onset found in %d captured program-channel samples", len(ch1))
	return -1
}

// ltcValueAtOffset reports the timecode frame count that libltc's decode
// of f implies is playing at sampleOffset, extrapolating from f's own
// decoded value and start position at ltcLagSampleRate. Used to compare
// what LTC's own audio actually carries at a given point in the captured
// timeline against a reference frame count from elsewhere (a program
// onset, or a wall-clock ObserveLTC call).
func ltcValueAtOffset(f ltcdecodetest.Frame, rate pkgaudio.LTCFrameRate, sampleOffset int64) (int64, error) {
	tc := pkgaudio.LTCTimecode(fmt.Sprintf("%02d:%02d:%02d:%02d", f.Hours, f.Mins, f.Secs, f.Frame))
	base, err := tc.FrameCount(rate)
	if err != nil {
		return 0, err
	}
	deltaSamples := sampleOffset - f.OffStart
	deltaFrames := int64(math.Round(float64(deltaSamples) / float64(ltcLagSampleRate) * rate.Rate()))
	return base + deltaFrames, nil
}

// decodeLTCChannel decodes every LTC frame libltc recovers from ch2 at
// rate, skipping the (silent, pre-run) lead where nothing decodes.
func decodeLTCChannel(t *testing.T, ch2 []int16, rate pkgaudio.LTCFrameRate) []ltcdecodetest.Frame {
	t.Helper()
	apv := int(float64(ltcLagSampleRate) / rate.Rate())
	frames, err := ltcdecodetest.Decode(ch2, apv)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(frames) == 0 {
		t.Fatalf("libltc recovered no frames from %d captured LTC-channel samples", len(ch2))
	}
	return frames
}

// nearestFrame returns whichever of frames has an OffStart closest to
// sampleOffset, minimizing how far [ltcValueAtOffset] has to extrapolate.
func nearestFrame(frames []ltcdecodetest.Frame, sampleOffset int64) ltcdecodetest.Frame {
	best := frames[0]
	bestDist := best.OffStart - sampleOffset
	if bestDist < 0 {
		bestDist = -bestDist
	}
	for _, f := range frames[1:] {
		d := f.OffStart - sampleOffset
		if d < 0 {
			d = -d
		}
		if d < bestDist {
			best, bestDist = f, d
		}
	}
	return best
}

// writeMonoWAV writes samples as a mono 16-bit PCM WAV file at sampleRate.
// A hand-built fixture, not a GStreamer-rendered one: this suite needs an
// exact, known silence/tone boundary to measure against, not merely "a
// tone somewhere in a file".
func writeMonoWAV(t *testing.T, path string, samples []int16, sampleRate int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %q: %v", path, err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Fatalf("close %q: %v", path, err)
		}
	}()

	dataSize := len(samples) * 2
	write := func(v any) {
		if err := binary.Write(f, binary.LittleEndian, v); err != nil {
			t.Fatalf("write WAV: %v", err)
		}
	}
	writeStr := func(s string) {
		if _, err := f.WriteString(s); err != nil {
			t.Fatalf("write WAV: %v", err)
		}
	}
	writeStr("RIFF")
	write(uint32(36 + dataSize))
	writeStr("WAVE")
	writeStr("fmt ")
	write(uint32(16))             // fmt chunk size
	write(uint16(1))              // PCM
	write(uint16(1))              // mono
	write(uint32(sampleRate))     // sample rate
	write(uint32(sampleRate * 2)) // byte rate
	write(uint16(2))              // block align
	write(uint16(16))             // bits per sample
	writeStr("data")
	write(uint32(dataSize))
	write(samples)
}

// generateSilenceThenToneWAV writes a mono fixture that is exact digital
// silence for silence, followed by a 440Hz sine for tone. The boundary
// between them is a real, unambiguous edge a test can locate in captured
// output and treat as ground truth for "this much program time has
// actually elapsed" -- unlike a tone with no silent lead-in, which gives
// no edge to measure once a branch has already been decoding since Load
// (see this suite's own doc comment on why Start's own seek, not the
// fixture's start, is what actually anchors position 0).
func generateSilenceThenToneWAV(t *testing.T, path string, silence, tone time.Duration) {
	t.Helper()
	const sampleRate = ltcLagSampleRate
	const freq = 440.0
	const amplitude = 0.8 * 32767.0

	silenceSamples := int(silence.Seconds() * sampleRate)
	toneSamples := int(tone.Seconds() * sampleRate)
	samples := make([]int16, silenceSamples+toneSamples)
	for i := 0; i < toneSamples; i++ {
		tSec := float64(i) / sampleRate
		samples[silenceSamples+i] = int16(amplitude * math.Sin(2*math.Pi*freq*tSec))
	}
	writeMonoWAV(t, path, samples, sampleRate)
}

// TestLTCAudioAlignedToProgramOnset is the LTC queue-lag fix's core
// reproduction and regression guard: it measures, from real decoded
// audio, how far LTC's own on-wire value is from what it should read at
// the sample where the program's audible content actually reaches a
// known position.
//
// The fixture is silence followed by a tone rather than a bare tone: a
// branch loaded well ahead of Start keeps decoding into the shared,
// always-PLAYING mixer the whole time (Start's own doc comment: "a
// branch loaded ahead of Start may have kept decoding while frozen"), so
// a bare tone gives no usable edge -- it is already audible before Start
// is ever called. Start's own seek is what actually re-anchors the
// branch to position 0, so the fixture's silence prefix is what turns
// "silenceDuration after this test's Start call" into a real, physically
// located edge in the captured stream: the sample where that edge
// appears is, by construction, exactly silenceDuration of true program
// time after the position StartLTC's spec.StartTimecode was asked to
// describe. Before the fix this measured lag is approximately
// ltcAppSrcLeadSeconds (the LTC feeder's queue depth, ~5-6 frames at
// typical rates); after it, close to zero.
func TestLTCAudioAlignedToProgramOnset(t *testing.T) {
	e, cap := newLTCLagEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), ltcOpTimeout)
	defer cancel()

	const silenceDuration = 3 * time.Second
	const toneDuration = 2 * time.Second
	dir := t.TempDir()
	wav := filepath.Join(dir, "silence-then-tone.wav")
	generateSilenceThenToneWAV(t, wav, silenceDuration, toneDuration)
	if _, err := e.Load(ctx, "prog", mediaRef(wav), silenceDuration+toneDuration); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Let the LTC feeder run silence for a moment so its appsrc queue
	// reaches the steady state block=true holds it at (see
	// ltcAppSrcLeadDuration) -- the state every real start, resume, or
	// seek actually swaps encoders against. This is well inside the
	// fixture's own silence prefix, so it does not race the edge below.
	time.Sleep(500 * time.Millisecond)

	const rate = pkgaudio.LTCFrameRate25
	const startTC = pkgaudio.LTCTimecode("01:00:00:00")
	if _, err := e.StartLTC(ctx, agentaudio.LTCSpec{FrameRate: rate, StartTimecode: startTC}); err != nil {
		t.Fatalf("StartLTC: %v", err)
	}
	// Start's own seek re-anchors the branch to position 0 regardless of
	// how far it drifted while merely loaded -- see the doc comment above.
	if _, err := e.Start(ctx, "prog", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForLTCState(t, e, agentaudio.LTCRunning, ltcOpTimeout)

	time.Sleep(silenceDuration + 1*time.Second) // past the fixture's own silence-to-tone edge, with margin

	ch1, ch2 := cap.snapshot()
	onset := toneOnsetFrame(t, ch1)
	frames := decodeLTCChannel(t, ch2, rate)
	nearest := nearestFrame(frames, int64(onset))

	startFrames, err := startTC.FrameCount(rate)
	if err != nil {
		t.Fatalf("FrameCount(%q): %v", startTC, err)
	}
	// audibleFrames is what LTC's own decoded audio claims is playing at
	// the exact sample the program tone actually starts.
	audibleFrames, err := ltcValueAtOffset(nearest, rate, int64(onset))
	if err != nil {
		t.Fatalf("ltcValueAtOffset: %v", err)
	}
	// expectedFrames is what it should claim: exactly silenceDuration of
	// program time past spec.StartTimecode, by construction of the fixture
	// and Start's own seek to position 0.
	expectedFrames := startFrames + int64(math.Round(silenceDuration.Seconds()*rate.Rate()))

	lagFrames := expectedFrames - audibleFrames
	lagSeconds := float64(lagFrames) / rate.Rate()
	t.Logf("program tone onset at sample %d; nearest decoded LTC frame %02d:%02d:%02d:%02d at sample %d; audible value implies frame %d, want %d; lag = %d frames (%.4fs)",
		onset, nearest.Hours, nearest.Mins, nearest.Secs, nearest.Frame, nearest.OffStart, audibleFrames, expectedFrames, lagFrames, lagSeconds)

	const toleranceFrames = 2
	if lagFrames > toleranceFrames || lagFrames < -toleranceFrames {
		t.Fatalf("LTC on the wire is %d frames (%.4fs) behind the program's real position, want within %d frames", lagFrames, lagSeconds, toleranceFrames)
	}
}

// TestLTCObservedTimecodeMatchesAudibleFrame is the LTC queue-lag fix's
// reporting-side reproduction and regression guard: it compares
// ObserveLTC's reported Timecode, at the instant it is called, against
// what libltc actually decodes as audible at that same sample-domain
// position. Before the
// fix, the reported value is the frame just pushed -- ahead of the
// audible frame by the queue depth; after it, the two agree.
func TestLTCObservedTimecodeMatchesAudibleFrame(t *testing.T) {
	e, cap := newLTCLagEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), ltcOpTimeout)
	defer cancel()

	const rate = pkgaudio.LTCFrameRate30
	if _, err := e.StartLTC(ctx, agentaudio.LTCSpec{FrameRate: rate, StartTimecode: "02:00:00:00"}); err != nil {
		t.Fatalf("StartLTC: %v", err)
	}
	waitForLTCState(t, e, agentaudio.LTCRunning, ltcOpTimeout)
	// Let a real, decodable stretch of LTC accumulate before sampling.
	time.Sleep(2 * time.Second)

	obs := e.ObserveLTC(ctx)
	sampleAtObserve := cap.framesCaptured()
	if obs.State != agentaudio.LTCRunning || !obs.TimecodeKnown {
		t.Fatalf("ObserveLTC at sampling instant = %+v, want running with a known timecode", obs)
	}
	reportedFrames, err := obs.Timecode.FrameCount(rate)
	if err != nil {
		t.Fatalf("FrameCount(%q): %v", obs.Timecode, err)
	}

	// A little more audio after the observation gives the decoder a
	// frame whose start is at or after sampleAtObserve to extrapolate
	// back from, without needing the observation instant itself to fall
	// exactly on a frame boundary.
	time.Sleep(300 * time.Millisecond)
	_, ch2 := cap.snapshot()
	frames := decodeLTCChannel(t, ch2, rate)

	var anchor *ltcdecodetest.Frame
	for i := range frames {
		if int(frames[i].OffStart) >= sampleAtObserve {
			anchor = &frames[i]
			break
		}
	}
	if anchor == nil {
		anchor = &frames[len(frames)-1]
	}
	audibleFrames, err := ltcValueAtOffset(*anchor, rate, int64(sampleAtObserve))
	if err != nil {
		t.Fatalf("ltcValueAtOffset: %v", err)
	}

	lagFrames := reportedFrames - audibleFrames
	lagSeconds := float64(lagFrames) / rate.Rate()
	t.Logf("ObserveLTC reported %q (frame %d); audible frame %d at the same sample position; lead = %d frames (%.4fs)",
		obs.Timecode, reportedFrames, audibleFrames, lagFrames, lagSeconds)

	const toleranceFrames = 2
	if lagFrames > toleranceFrames || lagFrames < -toleranceFrames {
		t.Fatalf("ObserveLTC's reported timecode leads the audible frame by %d frames (%.4fs), want within %d frames", lagFrames, lagSeconds, toleranceFrames)
	}
}
