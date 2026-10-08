package audio

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// preparedStartLead is audio.settings' default lead for a start whose nodes
// all loaded the session first: 250 ms delivery bound plus 500 ms margin.
const preparedStartLead = 750 * time.Millisecond

const startTimingLine = "audio session start timing"

// timedNode is one node's scheduled fixture with its log captured.
type timedNode struct {
	*scheduledFixture
	log *bytes.Buffer
}

func newTimedNode(t *testing.T) timedNode {
	t.Helper()
	f := newScheduledFixture(t, 20)
	var buf bytes.Buffer
	f.m.logger = slog.New(slog.NewTextHandler(&buf, nil))
	return timedNode{scheduledFixture: f, log: &buf}
}

func (n timedNode) timingLines() []string {
	var out []string
	for _, line := range strings.Split(n.log.String(), "\n") {
		if strings.Contains(line, startTimingLine) {
			out = append(out, line)
		}
	}
	return out
}

func (n timedNode) prepare(t *testing.T) {
	t.Helper()
	if out := n.m.Prepare(context.Background(), n.id, "inv-prepare", 2); out.Outcome != pkgaudio.OutcomePosition {
		t.Fatalf("Prepare = %q (%s), want position", out.Outcome, out.Reason)
	}
}

// startAt starts the node at t0 and moves its media clock to t0 once the
// start has read it, so the wait for the instant takes no real time.
func (n timedNode) startAt(t *testing.T, t0 time.Time) pkgaudio.OutcomeResult {
	t.Helper()
	lead := t0.Sub(n.media.Now(context.Background()).Time)
	before := n.media.reads()
	done := make(chan pkgaudio.OutcomeResult, 1)
	go func() {
		done <- n.m.StartAt(context.Background(), n.id, "inv-start", 3, t0.UnixNano())
	}()
	n.media.waitForReads(t, before+1)
	if lead > 0 {
		n.media.advance(lead)
	}
	select {
	case out := <-done:
		return out
	case <-time.After(10 * time.Second):
		t.Fatal("StartAt never returned")
		return pkgaudio.OutcomeResult{}
	}
}

// TestPreparedNodesStartTogetherAtTheDefaultPreparedLead holds the
// synchronized start at the shortened lead: two nodes that loaded the bed
// first and are given one instant 750 ms ahead both start at that instant,
// and neither loads anything inside the start.
func TestPreparedNodesStartTogetherAtTheDefaultPreparedLead(t *testing.T) {
	a, b := newTimedNode(t), newTimedNode(t)
	a.prepare(t)
	b.prepare(t)
	t0 := a.media.Now(context.Background()).Time.Add(preparedStartLead)

	for name, n := range map[string]timedNode{"node-a": a, "node-b": b} {
		if out := n.startAt(t, t0); out.Outcome != pkgaudio.OutcomeStarted {
			t.Fatalf("%s: StartAt = %q (%s), want started", name, out.Outcome, out.Reason)
		}
		snap := n.timeline(t)
		if !snap.Scheduled || snap.ScheduledAtNs != t0.UnixNano() {
			t.Fatalf("%s: timeline scheduled=%v at %d, want scheduled at the shared instant %d", name, snap.Scheduled, snap.ScheduledAtNs, t0.UnixNano())
		}
		lines := n.timingLines()
		if len(lines) != 1 {
			t.Fatalf("%s: %d timing lines, want exactly 1 per start:\n%s", name, len(lines), n.log.String())
		}
		for _, want := range []string{"outcome=started", "scheduled=true", "loadedInStart=false", "loadMs=0", "waitForInstantMs=", "engineStartMs=", "resolveScheduleMs="} {
			if !strings.Contains(lines[0], want) {
				t.Errorf("%s: timing line %q lacks %q", name, lines[0], want)
			}
		}
	}
}

// TestNodeThatMissesTheSharedInstantIsRefusedAsBefore proves the shortened
// lead changed nothing for a node the start reaches too late: it refuses,
// does not play, and the node on time is unaffected.
func TestNodeThatMissesTheSharedInstantIsRefusedAsBefore(t *testing.T) {
	onTime, late := newTimedNode(t), newTimedNode(t)
	onTime.prepare(t)
	late.prepare(t)
	t0 := onTime.media.Now(context.Background()).Time.Add(preparedStartLead)

	if out := onTime.startAt(t, t0); out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("on-time node: StartAt = %q (%s), want started", out.Outcome, out.Reason)
	}

	late.media.advance(preparedStartLead + 10*time.Millisecond)
	out := late.startAt(t, t0)
	if out.Outcome != pkgaudio.OutcomeRefused || !strings.Contains(out.Reason, pkgaudio.ReasonScheduledStartInPast) {
		t.Fatalf("late node: StartAt = %q (%s), want refused with %q", out.Outcome, out.Reason, pkgaudio.ReasonScheduledStartInPast)
	}
	if snap := late.timeline(t); snap.Scheduled {
		t.Fatal("late node reports a scheduled timeline; a refused start must not play")
	}
	lines := late.timingLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "outcome=refused") {
		t.Fatalf("late node timing lines = %q, want exactly one naming the refusal", lines)
	}
}

// TestStartThatLoadsItsMediaSaysSoInTheTimingLine proves the line tells a
// cold start, which pays the load inside the start, from a prepared one.
func TestStartThatLoadsItsMediaSaysSoInTheTimingLine(t *testing.T) {
	n := newTimedNode(t)
	if out := n.m.Start(context.Background(), n.id, "inv-start", 2); out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("Start = %q (%s), want started", out.Outcome, out.Reason)
	}
	lines := n.timingLines()
	if len(lines) != 1 {
		t.Fatalf("%d timing lines, want exactly 1:\n%s", len(lines), n.log.String())
	}
	for _, want := range []string{"scheduled=false", "loadedInStart=true"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("timing line %q lacks %q", lines[0], want)
		}
	}
}
