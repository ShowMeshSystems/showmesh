// Command spike is throwaway exploratory code, not part of ShowMesh. It
// exercises go-gst against a running audiomixer pipeline to establish, by
// observation, the five behaviors Track C phase 1's engine design depends
// on: dynamic branch add/remove, per-branch pause via pad blocking probes,
// GstController-driven gain ramps read back accurately, distinguishable
// per-branch EOS, and interleave onto chosen channel indices.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstcontroller"
)

func must(name string, ok bool) {
	if !ok {
		panic(fmt.Sprintf("%s failed", name))
	}
}

func mustElem(factory, name string) gst.Element {
	e := gst.ElementFactoryMake(factory, name)
	if e == nil {
		panic(fmt.Sprintf("could not create element %s (%s)", name, factory))
	}
	return e
}

func waitState(pl gst.Pipeline, want gst.State, timeout time.Duration) {
	ret, _, _ := pl.GetState(gst.ClockTime(timeout.Nanoseconds()))
	fmt.Println("waitState ret:", ret)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: spike <a|b|c|d|e>")
		os.Exit(2)
	}
	gst.Init()
	switch os.Args[1] {
	case "a":
		spikeA_DynamicAddRemove()
	case "b":
		spikeB_PauseOneBranch()
	case "c":
		spikeC_GstControllerFade()
	case "d":
		spikeD_PerBranchEOS()
	case "e":
		spikeE_InterleaveChannelIndices()
	case "f":
		spikeF_DecodebinPadAdded()
	case "g":
		spikeG_UndecodableFileError()
	default:
		fmt.Println("unknown spike", os.Args[1])
		os.Exit(2)
	}
}

// spikeA_DynamicAddRemove: audiomixer running with one steady branch, then
// a second branch is added mid-run and later removed, while the pipeline
// keeps producing output the whole time (verified via a fakesink handoff
// probe counting buffers before/during/after).
func spikeA_DynamicAddRemove() {
	pl, err := gst.ParseLaunch(
		"audiomixer name=mix ! audioconvert ! fakesink name=out signal-handoffs=true sync=false " +
			"audiotestsrc name=src1 is-live=true wave=sine freq=220 ! audioconvert ! audioresample ! mix.",
	)
	must("parselaunch", err == nil)
	pipeline := pl.(gst.Pipeline)

	out := pipeline.GetByName("out")
	var count int
	out.GetStaticPad("sink").AddProbe(gst.PadProbeTypeBuffer, func(self gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		count++
		return gst.PadProbeOK
	})

	pipeline.SetState(gst.StatePlaying)
	waitState(pipeline, gst.StatePlaying, 5*time.Second)
	time.Sleep(500 * time.Millisecond)
	fmt.Println("buffers before second branch:", count)

	mix := pipeline.GetByName("mix")

	src2 := mustElem("audiotestsrc", "src2")
	src2.SetObjectProperty("is-live", true)
	src2.SetObjectProperty("wave", int32(0)) // sine
	src2.SetObjectProperty("freq", 440.0)
	conv2 := mustElem("audioconvert", "conv2")
	res2 := mustElem("audioresample", "res2")

	pipeline.Add(src2)
	pipeline.Add(conv2)
	pipeline.Add(res2)
	must("link src2->conv2", src2.Link(conv2))
	must("link conv2->res2", conv2.Link(res2))

	sinkPad := mix.RequestPadSimple("sink_%u")
	must("got request pad", sinkPad != nil)
	must("link res2->mixer pad", res2.GetStaticPad("src").Link(sinkPad) == gst.PadLinkOK)

	src2.SyncStateWithParent()
	conv2.SyncStateWithParent()
	res2.SyncStateWithParent()

	countAtAdd := count
	time.Sleep(500 * time.Millisecond)
	fmt.Println("buffers after add, before remove:", count, "delta since add:", count-countAtAdd)

	// Block the second branch's src pad before unlinking/removing, the
	// documented safe teardown, then remove from the pipeline.
	blocked := make(chan struct{})
	srcPad := res2.GetStaticPad("src")
	srcPad.AddProbe(gst.PadProbeTypeIdle | gst.PadProbeTypeBlock, func(self gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		close(blocked)
		return gst.PadProbeRemove
	})
	select {
	case <-blocked:
	case <-time.After(3 * time.Second):
		fmt.Println("FAIL: pad never idled/blocked")
		os.Exit(1)
	}

	src2.SetState(gst.StateNull)
	conv2.SetState(gst.StateNull)
	res2.SetState(gst.StateNull)
	srcPad.Unlink(sinkPad)
	mix.ReleaseRequestPad(sinkPad)
	pipeline.Remove(src2)
	pipeline.Remove(conv2)
	pipeline.Remove(res2)

	countAtRemove := count
	time.Sleep(500 * time.Millisecond)
	fmt.Println("buffers after remove:", count, "delta since remove:", count-countAtRemove)

	pipeline.SetState(gst.StateNull)
	if count > countAtRemove && countAtRemove > countAtAdd && countAtAdd > 0 {
		fmt.Println("RESULT a: PASS - mix kept producing buffers before, during, and after dynamic add/remove")
	} else {
		fmt.Println("RESULT a: FAIL - buffer counts did not monotonically advance through all three phases")
		os.Exit(1)
	}
}

// spikeB_PauseOneBranch: two branches feed one audiomixer with
// ignore-inactive-pads=true. One branch is "paused" by blocking its src pad
// with a probe (data flow stops, position freezes) while the other branch's
// handoff count keeps advancing, proving the mix as a whole keeps running.
func spikeB_PauseOneBranch() {
	pl, err := gst.ParseLaunch(
		"audiomixer name=mix ignore-inactive-pads=true ! audioconvert ! fakesink name=out signal-handoffs=true sync=false " +
			"audiotestsrc name=src1 is-live=true wave=sine freq=220 ! audioconvert ! audioresample ! queue ! mix.sink_0 " +
			"audiotestsrc name=src2 is-live=true wave=sine freq=440 ! audioconvert ! audioresample ! identity name=idbranch2 ! queue ! mix.sink_1",
	)
	must("parselaunch", err == nil)
	pipeline := pl.(gst.Pipeline)
	out := pipeline.GetByName("out")
	var totalCount int
	out.GetStaticPad("sink").AddProbe(gst.PadProbeTypeBuffer, func(self gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		totalCount++
		return gst.PadProbeOK
	})

	id2 := pipeline.GetByName("idbranch2")
	var branch2Count int64
	srcPad2 := id2.GetStaticPad("src")
	srcPad2.AddProbe(gst.PadProbeTypeBuffer, func(self gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		branch2Count++
		return gst.PadProbeOK
	})

	pipeline.SetState(gst.StatePlaying)
	waitState(pipeline, gst.StatePlaying, 5*time.Second)
	time.Sleep(500 * time.Millisecond)

	countBefore := branch2Count
	totalBefore := totalCount
	fmt.Println("branch2 buffers before pause:", countBefore, "total mix buffers:", totalBefore)

	// Pause branch2 by blocking its output pad. This is the mechanism
	// under test: does the rest of the mix keep going?
	blockID := make(chan gst.PadProbeReturn, 1)
	srcPad2.AddProbe(gst.PadProbeTypeBlockDownstream, func(self gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		select {
		case blockID <- gst.PadProbeOK:
		default:
		}
		return gst.PadProbeOK
	})
	time.Sleep(50 * time.Millisecond) // let the block probe actually engage

	countAtPause := branch2Count
	time.Sleep(700 * time.Millisecond)
	countAfterPauseWait := branch2Count
	totalAfterPauseWait := totalCount

	fmt.Println("branch2 buffers frozen at:", countAtPause, "still at:", countAfterPauseWait)
	fmt.Println("total mix buffers grew during branch2 pause:", totalAfterPauseWait-totalBefore)

	pipeline.SetState(gst.StateNull)

	if countAfterPauseWait == countAtPause && totalAfterPauseWait > totalBefore {
		fmt.Println("RESULT b: PASS - blocking one branch's src pad freezes that branch's dataflow while the mix as a whole keeps producing output")
	} else {
		fmt.Println("RESULT b: FAIL - branch2 delta", countAfterPauseWait-countAtPause, "mix delta", totalAfterPauseWait-totalBefore)
		os.Exit(1)
	}
}

// spikeC_GstControllerFade: a GstController InterpolationControlSource
// bound to a per-branch volume element via NewDirectControlBindingAbsolute,
// ramping from 1.0 to 0.3 over 500ms, with the property read back at start,
// mid-ramp, and after completion.
func spikeC_GstControllerFade() {
	pl, err := gst.ParseLaunch(
		"audiotestsrc is-live=true wave=sine ! audioconvert ! volume name=vol volume=1.0 ! fakesink sync=false",
	)
	must("parselaunch", err == nil)
	pipeline := pl.(gst.Pipeline)
	vol := pipeline.GetByName("vol")

	cs := gstcontroller.NewInterpolationControlSource()
	tvcs, ok := cs.(gstcontroller.TimedValueControlSource)
	must("control source is TimedValueControlSource", ok)

	// InterpolationMode is a GObject property named "mode" on
	// GstInterpolationControlSource in the C API.
	csObj, ok := cs.(gst.Object)
	must("control source is gst.Object", ok)
	csObj.SetObjectProperty("mode", gstcontroller.InterpolationModeLinear)

	must("set control point at t=0 value=1.0", tvcs.Set(0, 1.0))
	must("set control point at t=500ms value=0.3", tvcs.Set(gst.ClockTime(500*time.Millisecond), 0.3))

	volObj, ok := vol.(gst.Object)
	must("vol is gst.Object", ok)
	binding := gstcontroller.NewDirectControlBindingAbsolute(volObj, "volume", cs)
	must("AddControlBinding", volObj.AddControlBinding(binding))

	pipeline.SetState(gst.StatePlaying)
	waitState(pipeline, gst.StatePlaying, 5*time.Second)

	readGain := func(label string) float64 {
		volObj2, ok := vol.(gst.Object)
		if !ok {
			panic("vol is not gst.Object")
		}
		f := volObj2.ObjectProperty("volume").(float64)
		fmt.Printf("%s volume=%v\n", label, f)
		return f
	}

	g0 := readGain("t=0ms")
	time.Sleep(250 * time.Millisecond)
	gMid := readGain("t=250ms")
	time.Sleep(500 * time.Millisecond)
	gEnd := readGain("t=750ms (post-ramp)")

	pipeline.SetState(gst.StateNull)

	pass := g0 > 0.9 && gMid < g0 && gMid > gEnd && gEnd > 0.25 && gEnd < 0.35
	if pass {
		fmt.Println("RESULT c: PASS - GstController ramp read back monotonically decreasing, landed at target gain, no 10x scale defect observed")
	} else {
		fmt.Println("RESULT c: FAIL - g0", g0, "gMid", gMid, "gEnd", gEnd)
		os.Exit(1)
	}
}

// spikeD_PerBranchEOS: two branches into one audiomixer via a
// concat-terminated short source so one branch reaches EOS naturally while
// the other keeps playing. Bus messages are inspected to see whether a
// per-element EOS can be distinguished from the pipeline-wide EOS, and
// whether the mixer keeps running with one branch gone.
func spikeD_PerBranchEOS() {
	pl, err := gst.ParseLaunch(
		"audiomixer name=mix ignore-inactive-pads=true ! audioconvert ! fakesink name=out signal-handoffs=true sync=false " +
			"audiotestsrc name=src1 is-live=true wave=sine freq=220 num-buffers=20 ! audioconvert ! audioresample ! queue ! mix.sink_0 " +
			"audiotestsrc name=src2 is-live=true wave=sine freq=440 ! audioconvert ! audioresample ! queue ! mix.sink_1",
	)
	must("parselaunch", err == nil)
	pipeline := pl.(gst.Pipeline)
	out := pipeline.GetByName("out")
	var totalCount int
	out.GetStaticPad("sink").AddProbe(gst.PadProbeTypeBuffer, func(self gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		totalCount++
		return gst.PadProbeOK
	})

	sink0Pad := pipeline.GetByName("mix").GetStaticPad("sink_0")
	eosSeenOnBranch := make(chan struct{}, 1)
	sink0Pad.AddProbe(gst.PadProbeTypeEventDownstream, func(self gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		ev := info.GetEvent()
		if ev != nil && ev.GetType() == gst.EventEOS {
			select {
			case eosSeenOnBranch <- struct{}{}:
			default:
			}
		}
		return gst.PadProbeOK
	})

	pipeline.SetState(gst.StatePlaying)
	waitState(pipeline, gst.StatePlaying, 5*time.Second)

	branchEOS := false
	pipelineEOS := false
	timeout := time.After(4 * time.Second)
	bus := pipeline.GetBus()
loop:
	for {
		select {
		case <-eosSeenOnBranch:
			branchEOS = true
			fmt.Println("branch-level EOS event observed on mix.sink_0 at", time.Now().Format(time.RFC3339Nano))
		case <-timeout:
			break loop
		default:
			msg := bus.TimedPop(gst.ClockTime(100 * time.Millisecond))
			if msg == nil {
				continue
			}
			if msg.Type() == gst.MessageEOS {
				pipelineEOS = true
				fmt.Println("pipeline-wide EOS message observed at", time.Now().Format(time.RFC3339Nano))
				break loop
			}
		}
	}

	totalAtBranchEOS := totalCount
	time.Sleep(400 * time.Millisecond)
	totalAfter := totalCount
	fmt.Println("branchEOS:", branchEOS, "pipelineEOS:", pipelineEOS, "mix buffers grew after branch EOS:", totalAfter-totalAtBranchEOS)

	pipeline.SetState(gst.StateNull)

	if branchEOS && !pipelineEOS && totalAfter > totalAtBranchEOS {
		fmt.Println("RESULT d: PASS - one branch's EOS is observable per-pad, distinct from any pipeline-wide EOS, and the mix keeps running on the surviving branch")
	} else {
		fmt.Println("RESULT d: FAIL or inconclusive - branchEOS", branchEOS, "pipelineEOS", pipelineEOS)
		os.Exit(1)
	}
}

// spikeE_InterleaveChannelIndices: interleave assembles a mono program
// stream and a mono LTC-placeholder stream onto specific 1-based channel
// indices of a 4-channel output, verified via a capsfilter asserting the
// resulting channel-mask/positions and a successful negotiate+preroll+EOS.
func spikeE_InterleaveChannelIndices() {
	// interleave takes N mono sink pads named sink_%u in the order
	// requested; channel-positions/channel-mask on the source caps says
	// where each ends up. We request 4 channels total, placing program on
	// channel 1 (index 0) and a placeholder "LTC" tone on channel 3 (index
	// 2), leaving channels 2 and 4 silent.
	pl, err := gst.ParseLaunch(
		"interleave name=il channel-positions-from-input=false ! " +
			"audio/x-raw,channels=4,channel-mask=(bitmask)0x0000000000000000 ! " +
			"fakesink name=out signal-handoffs=true sync=false " +
			"audiotestsrc is-live=true wave=sine freq=220 num-buffers=30 ! audioconvert ! audio/x-raw,channels=1 ! il.sink_0 " +
			"audiotestsrc is-live=true wave=silence num-buffers=30 ! audioconvert ! audio/x-raw,channels=1 ! il.sink_1 " +
			"audiotestsrc is-live=true wave=sine freq=2000 num-buffers=30 ! audioconvert ! audio/x-raw,channels=1 ! il.sink_2 " +
			"audiotestsrc is-live=true wave=silence num-buffers=30 ! audioconvert ! audio/x-raw,channels=1 ! il.sink_3",
	)
	must("parselaunch", err == nil)
	pipeline := pl.(gst.Pipeline)
	out := pipeline.GetByName("out")
	var count int
	var lastCaps string
	sinkPad := out.GetStaticPad("sink")
	sinkPad.AddProbe(gst.PadProbeTypeBuffer, func(self gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		count++
		if lastCaps == "" {
			c := sinkPad.GetCurrentCaps()
			if c != nil {
				lastCaps = c.String()
			}
		}
		return gst.PadProbeOK
	})

	pipeline.SetState(gst.StatePlaying)
	waitState(pipeline, gst.StatePlaying, 5*time.Second)

	bus := pipeline.GetBus()
	reachedEOS := false
	for msg := range bus.Messages(context.Background()) {
		if msg.Type() == gst.MessageEOS {
			reachedEOS = true
			break
		}
		if msg.Type() == gst.MessageError {
			_, gerr := msg.ParseError()
			fmt.Println("pipeline error:", gerr)
			os.Exit(1)
		}
	}

	pipeline.SetState(gst.StateNull)
	fmt.Println("buffers seen at 4-channel sink:", count)
	fmt.Println("negotiated caps at sink:", lastCaps)
	fmt.Println("reached EOS:", reachedEOS)

	if reachedEOS && count > 0 && lastCaps != "" {
		fmt.Println("RESULT e: PASS - interleave negotiated a 4-channel output from 4 mono branches and ran to completion; see caps above for actual channel assembly")
	} else {
		fmt.Println("RESULT e: FAIL")
		os.Exit(1)
	}
}

// spikeF_DecodebinPadAdded proves the pad-added closure signature for
// filesrc ! decodebin branches feeding a running audiomixer, which is the
// real engine's per-handle branch shape (spike a used a synthetic source,
// not a decoded file).
func spikeF_DecodebinPadAdded() {
	pl, err := gst.ParseLaunch(
		"audiomixer name=mix ignore-inactive-pads=true ! audioconvert ! fakesink name=out sync=false",
	)
	must("parselaunch", err == nil)
	pipeline := pl.(gst.Pipeline)
	mix := pipeline.GetByName("mix")

	src := mustElem("filesrc", "src")
	src.SetObjectProperty("location", "/tmp/spike_fixture.wav")
	dec := mustElem("decodebin", "dec")
	conv := mustElem("audioconvert", "conv")
	res := mustElem("audioresample", "res")
	q := mustElem("queue", "q")

	pipeline.Add(src)
	pipeline.Add(dec)
	pipeline.Add(conv)
	pipeline.Add(res)
	pipeline.Add(q)
	must("link src->dec", src.Link(dec))
	must("link conv->res", conv.Link(res))
	must("link res->q", res.Link(q))

	sinkPad := mix.RequestPadSimple("sink_%u")
	must("link q->mixer", q.GetStaticPad("src").Link(sinkPad) == gst.PadLinkOK)

	linked := make(chan struct{})
	dec.Connect("pad-added", func(self gst.Element, pad gst.Pad) {
		sinkP := conv.GetStaticPad("sink")
		if sinkP.IsLinked() {
			return
		}
		ret := pad.Link(sinkP)
		fmt.Println("pad-added fired, link result:", ret)
		close(linked)
	})

	out := pipeline.GetByName("out")
	var count int
	out.GetStaticPad("sink").AddProbe(gst.PadProbeTypeBuffer, func(self gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		count++
		return gst.PadProbeOK
	})

	pipeline.SetState(gst.StatePlaying)
	waitState(pipeline, gst.StatePlaying, 5*time.Second)

	select {
	case <-linked:
	case <-time.After(3 * time.Second):
		fmt.Println("RESULT f: FAIL - pad-added never fired")
		os.Exit(1)
	}

	time.Sleep(200 * time.Millisecond)
	pos1, ok1 := dec.QueryPosition(gst.FormatTime)
	time.Sleep(300 * time.Millisecond)
	pos2, ok2 := dec.QueryPosition(gst.FormatTime)
	fmt.Println("branch position query 1:", pos1, ok1, "query 2:", pos2, ok2, "delta ns:", pos2-pos1)

	pipeline.SetState(gst.StateNull)
	fmt.Println("buffers reached sink:", count)
	if count > 0 {
		fmt.Println("RESULT f: PASS - decodebin pad-added(element, pad) closure links a file-decode branch into a running mixer")
	} else {
		fmt.Println("RESULT f: FAIL - no buffers reached the sink")
		os.Exit(1)
	}
}

func spikeG_UndecodableFileError() {
	pl, err := gst.ParseLaunch(
		"filesrc name=fsrc location=/tmp/garbage.bin ! decodebin name=dec ! fakesink",
	)
	must("parselaunch", err == nil)
	pipeline := pl.(gst.Pipeline)
	dec := pipeline.GetByName("dec")
	fmt.Println("decodebin element name:", dec.GetName())

	pipeline.SetState(gst.StatePaused)
	bus := pipeline.GetBus()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		msg := bus.TimedPop(gst.ClockTime(200 * time.Millisecond))
		if msg == nil {
			continue
		}
		fmt.Println("msg type:", msg.Type())
		if msg.Type() == gst.MessageError {
			text, gerr := msg.ParseError()
			src := msg.Source()
			var chain []string
			for o := src; o != nil; o = o.GetParent() {
				chain = append(chain, o.GetName())
			}
			fmt.Println("error text:", text, "gerr:", gerr, "src chain:", chain)
			pipeline.SetState(gst.StateNull)
			return
		}
	}
	fmt.Println("no error within timeout")
	pipeline.SetState(gst.StateNull)
}
