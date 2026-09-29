package pipeline

import (
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// QueuedSequence is the sequence a surface is told comes next. Its writer
// switches to it only when the timeline reports exactly Filename while
// content is being drawn, so a planned sequence change needs no black gap
// and a sequence nobody queued still draws black.
type QueuedSequence struct {
	Filename string
	source   FrameSource
	stepTime time.Duration
}

// Queue validates source against this writer's channel range, the same
// check NewFrameWriter makes, and makes it the writer's next sequence.
// displaced is a previously queued sequence the writer never switched to;
// the caller now owns it. Safe to call while the writer runs.
func (fw *FrameWriter) Queue(filename string, source FrameSource) (queued, displaced *QueuedSequence, err error) {
	if filename == "" {
		return nil, nil, fmt.Errorf("pipeline: surface %q: a queued sequence needs a filename", fw.surfaceID)
	}
	if source.FrameCount() <= 0 {
		return nil, nil, fmt.Errorf("pipeline: surface %q: the queued sequence %q has no frames", fw.surfaceID, filename)
	}
	probe := make([]byte, fw.channelCount)
	if err := source.ChannelRange(0, fw.channelStart, fw.channelCount, probe); err != nil {
		return nil, nil, err
	}
	stepTime := time.Duration(source.StepTimeMS()) * time.Millisecond
	if stepTime <= 0 {
		stepTime = multisync.DefaultStepTime
	}
	queued = &QueuedSequence{Filename: filename, source: source, stepTime: stepTime}
	return queued, fw.queued.Swap(queued), nil
}

// TakeQueued removes the queued sequence, if the writer has not switched to
// it yet, and hands its ownership to the caller.
func (fw *FrameWriter) TakeQueued() *QueuedSequence {
	return fw.queued.Swap(nil)
}

// SetQueueHandlers registers what runs on the writer's own goroutine when
// it switches to a queued sequence, and when it drops one because the
// surface is held black. Call before Run.
func (fw *FrameWriter) SetQueueHandlers(onSwitch, onDrop func(*QueuedSequence)) {
	fw.onSwitch = onSwitch
	fw.onDrop = onDrop
}

// holdsSequence reports whether filename is a sequence this writer may draw,
// switching to the queued sequence first when filename names exactly it.
// An empty filename is no evidence either way and never blanks a surface.
func (fw *FrameWriter) holdsSequence(filename string) bool {
	if filename == "" || filename == fw.sequenceFilename {
		return true
	}
	q := fw.queued.Load()
	if q == nil || q.Filename != filename || !fw.queued.CompareAndSwap(q, nil) {
		return false
	}
	fw.source = q.source
	fw.sequenceFilename = q.Filename
	fw.stepTime = q.stepTime
	fw.loggedStale = false
	fw.logger.Info("frame writer: switched to the queued next sequence as the timeline started it",
		"surface_id", fw.surfaceID, "sequence", q.Filename)
	if fw.onSwitch != nil {
		fw.onSwitch(q)
	}
	return true
}

// dropQueued discards the queued sequence on a held-black tick: a stop or a
// weather delay ends the plan it belonged to, so only a later activation
// can queue a sequence again.
func (fw *FrameWriter) dropQueued() {
	q := fw.queued.Swap(nil)
	if q != nil && fw.onDrop != nil {
		fw.onDrop(q)
	}
}
