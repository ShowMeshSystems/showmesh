package api

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// These tests drive the real dispatch-and-persist path with a clock that
// advances while each command is in flight, and with node clocks an hour
// ahead of this coordinator's.

type advancingClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *advancingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *advancingClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

const (
	bedFenceAnswerWait = 4 * time.Second
	bedFenceNodeSkew   = time.Hour
)

type bedFenceRig struct {
	h     *handlers
	st    *store.Store
	pub   *fakeAudioPublisher
	audio *fakeNodeAudioLister
	clk   *advancingClock
	rec   store.NightSessionRecord
	sid   string
}

func newBedFenceRig(t *testing.T) *bedFenceRig {
	t.Helper()
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	clk := &advancingClock{t: testNow}
	h.clock = clk.Now
	pub.onAwaitResponse = func() { clk.advance(bedFenceAnswerWait) }
	pub.respondedAt = func() time.Time { return clk.Now().Add(bedFenceNodeSkew) }
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	putAudioNodeNoLTCForTest(t, st, "node-a")
	putAudioNodeNoLTCForTest(t, st, "node-b")
	for _, nodeID := range []string{"node-a", "node-b"} {
		putNodeInventoryForTest(t, st, nodeID, testNow, bedItemInventoryForTest(nodeID, "asset-1", "asset-2")...)
	}
	ba := multiNodeBedConfig("node-a", "node-a", "node-b")
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	pub.result = confirmedResultForAction("x", nightBackgroundAudioSessionID(rec), "started")
	return &bedFenceRig{h: h, st: st, pub: pub, audio: h.deps.Audio.(*fakeNodeAudioLister), clk: clk, rec: rec, sid: nightBackgroundAudioSessionID(rec)}
}

// report builds a node's audio report built at builtAt and received at
// receivedAt on this coordinator's clock; the node stamps its own clock.
func (r *bedFenceRig) report(nodeID string, builtAt, receivedAt time.Time, extra ...observation.Observation) []observation.Observation {
	return append([]observation.Observation{bedNodeAudioReport(nodeID, builtAt.Add(bedFenceNodeSkew), receivedAt)}, extra...)
}

func (r *bedFenceRig) applies() int {
	return countDispatchedActionForSession(r.pub, "audio.session.apply", r.sid)
}

func (r *bedFenceRig) tick() {
	r.h.nightAdvanceBackgroundAudio(context.Background(), r.clk.Now(), r.rec)
}

func (r *bedFenceRig) settle(t *testing.T) {
	t.Helper()
	for i := 0; i < 12; i++ {
		before := len(r.pub.dispatchedSnapshot())
		r.tick()
		if len(r.pub.dispatchedSnapshot()) == before {
			return
		}
	}
	t.Fatalf("bed did not settle")
}

func (r *bedFenceRig) latestStep(t *testing.T, nodeID, kind string) store.NightCueOutboxRecord {
	t.Helper()
	history, err := r.h.nightBackgroundAudioHistory(context.Background(), r.rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	var out store.NightCueOutboxRecord
	for _, row := range nightBackgroundAudioStepsForNode(history, nodeID) {
		if row.Step.Kind == kind {
			out = row.Row
		}
	}
	return out
}

func TestNightBedFence_RowRecordsWhenTheAnswerArrivedNotTheTick(t *testing.T) {
	r := newBedFenceRig(t)
	r.settle(t)
	start := r.latestStep(t, "node-b", nightBGStepStart)
	if start.DispatchedAt == nil || start.ResolvedAt == nil {
		t.Fatalf("start row = %+v, want dispatched and resolved", start)
	}
	if got := start.ResolvedAt.Sub(*start.DispatchedAt); got < bedFenceAnswerWait {
		t.Fatalf("start resolved %v after dispatch, want at least the %v the node took to answer", got, bedFenceAnswerWait)
	}
}

func TestNightBedFence_ReportReceivedDuringTheStartWaitIsNotALoss(t *testing.T) {
	r := newBedFenceRig(t)
	r.settle(t)
	start := r.latestStep(t, "node-b", nightBGStepStart)
	duringWait := start.DispatchedAt.Add(time.Second)
	r.audio.setObservations("node-a", r.report("node-a", duringWait, duringWait,
		bedSessionPlayingReport(r.sid, "track-1", 0, 1_000, duringWait)...))
	r.audio.setObservations("node-b", r.report("node-b", duringWait, duringWait))

	before := r.applies()
	r.tick()
	if got := r.applies(); got != before {
		t.Fatalf("bed applies = %d, want %d: a report received before the start was answered says nothing about its result", got, before)
	}
}

func TestNightBedFence_ReportBuiltBeforeAnApplyButReceivedAfterItsAnswerIsNotALoss(t *testing.T) {
	r := newBedFenceRig(t)
	r.tick()
	apply := r.latestStep(t, "node-b", nightBGStepApply)
	if apply.ResolvedAt == nil {
		t.Fatalf("setup: node-b apply row = %+v, want resolved", apply)
	}
	builtBeforeApply := testNow
	receivedAfterAnswer := apply.ResolvedAt.Add(time.Second)
	r.audio.setObservations("node-b", r.report("node-b", builtBeforeApply, receivedAfterAnswer))

	before := r.applies()
	r.clk.advance(2 * time.Second)
	r.tick()
	if got := r.applies(); got != before {
		t.Fatalf("bed applies = %d, want %d: a report built before the apply cannot show the session the apply created", got, before)
	}
}

func TestNightBedFence_LossAfterPlaybackBeganIsStillRecovered(t *testing.T) {
	r := newBedFenceRig(t)
	r.settle(t)
	builtAfterAnswer := r.clk.Now().Add(time.Second)
	r.audio.setObservations("node-a", r.report("node-a", builtAfterAnswer, builtAfterAnswer,
		bedSessionPlayingReport(r.sid, "track-1", 0, 5_000, builtAfterAnswer)...))
	r.audio.setObservations("node-b", r.report("node-b", builtAfterAnswer, builtAfterAnswer))
	r.clk.advance(2 * time.Second)

	before := r.applies()
	r.tick()
	if got := r.applies(); got != before+1 {
		t.Fatalf("bed applies = %d, want %d: a report built after the answer that lacks the session is a real loss", got, before+1)
	}
}
