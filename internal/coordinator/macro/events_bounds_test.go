package macro

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
)

func priorFailureEvents(t *testing.T, e *Executor) []string {
	t.Helper()
	evs, _, err := e.store.ListEvents(context.Background(), 0, 1000)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var out []string
	for _, ev := range evs {
		if ev.Category == "macro.run.prior_failure" {
			out = append(out, string(ev.Details))
		}
	}
	return out
}

// TestPriorFailureClassesAreBoundedToTheKnownSet is the eviction guard.
//
// The class is caller-supplied and it is the coalescing key, so leaving it
// unvalidated turns a write that is supposed to produce at most one event
// per class into one that produces as many events as the caller invents
// class strings. Events are under retention, so that is an unbounded write
// on a failure path, which is an eviction primitive pointed at this
// coordinator's own evidence.
func TestPriorFailureClassesAreBoundedToTheKnownSet(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	putAction(t, st, "a1", fppAction("fpp-main", "stopPlaylist", "stop", nil))
	putMacro(t, st, "m", testMacroPayload(testStep("s1", "a1")))
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, nil)

	failures := make([]api.MacroPriorFailure, 0, 40)
	for i := 0; i < 40; i++ {
		failures = append(failures, api.MacroPriorFailure{
			MacroObjectID: "m",
			Class:         fmt.Sprintf("invented-class-%d", i),
			At:            time.Now(),
		})
	}
	e.recordPriorFailures(context.Background(), api.MacroSubmitRequest{
		MacroObjectID: "m", PriorFailures: failures,
	})

	got := priorFailureEvents(t, e)
	if len(got) != 1 {
		t.Fatalf("wrote %d prior-failure events for 40 invented class strings, want 1: an unvalidated class key makes this write unbounded", len(got))
	}
	if !strings.Contains(got[0], priorFailureClassUnclassified) {
		t.Errorf("event details %q do not name the unclassified class", got[0])
	}
}

// TestPriorFailureDroppedOnlyIsStillReported closes the case the guard at
// the top of recordPriorFailures explicitly admits: a caller whose buffer
// overflowed so badly that nothing survived to send, and which reports
// only how many it discarded. Writing nothing there silently truncates a
// truncation report.
func TestPriorFailureDroppedOnlyIsStillReported(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	putAction(t, st, "a1", fppAction("fpp-main", "stopPlaylist", "stop", nil))
	putMacro(t, st, "m", testMacroPayload(testStep("s1", "a1")))
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, nil)

	e.recordPriorFailures(context.Background(), api.MacroSubmitRequest{
		MacroObjectID: "m", PriorFailuresDropped: 7,
	})

	got := priorFailureEvents(t, e)
	if len(got) != 1 {
		t.Fatalf("wrote %d events for a report of 7 dropped entries and no surviving ones, want 1", len(got))
	}
	if !strings.Contains(got[0], `"droppedByCaller":7`) {
		t.Errorf("event details %q do not carry the dropped count", got[0])
	}
}

// TestPriorFailureDroppedCountIsReportedOnce confirms the caller's buffer
// loss is not copied onto every class, which would read as three separate
// losses rather than one.
func TestPriorFailureDroppedCountIsReportedOnce(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	putAction(t, st, "a1", fppAction("fpp-main", "stopPlaylist", "stop", nil))
	putMacro(t, st, "m", testMacroPayload(testStep("s1", "a1")))
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, nil)

	now := time.Now()
	e.recordPriorFailures(context.Background(), api.MacroSubmitRequest{
		MacroObjectID: "m",
		PriorFailures: []api.MacroPriorFailure{
			{MacroObjectID: "m", Class: priorFailureClassRefused, At: now},
			{MacroObjectID: "m", Class: priorFailureClassRejected, At: now},
			{MacroObjectID: "m", Class: priorFailureClassUnreachable, At: now},
		},
		PriorFailuresDropped: 4,
	})

	got := priorFailureEvents(t, e)
	if len(got) != 3 {
		t.Fatalf("wrote %d events for three distinct classes, want 3", len(got))
	}
	carrying := 0
	for _, d := range got {
		if strings.Contains(d, `"droppedByCaller":4`) {
			carrying++
		}
	}
	if carrying != 1 {
		t.Errorf("%d of 3 events carry the dropped count, want exactly 1: repeating it reads as three separate losses", carrying)
	}
}

// TestOverlapConflictNamesRunInMachineReadableField is the guard on the
// one response whose entire purpose is to point at a run. A client that
// has to recover the id by matching a substring of this server's English
// has left the contract, and rewording the sentence would silently break
// it. LESSONS.md records that exact failure on the FPP command endpoint.
func TestOverlapConflictNamesRunInMachineReadableField(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	putAction(t, st, "a1", fppAction("fpp-main", "stopPlaylist", "stop", nil))
	putMacro(t, st, "m", testMacroPayload(testStep("s1", "a1")))

	release := make(chan struct{})
	fd := &fakeDispatcher{dispatchFn: func(ctx context.Context, in api.FPPCommandInput) (api.FPPCommandOutcome, *v1.Problem, error) {
		<-release
		now := time.Now()
		return api.FPPCommandOutcome{
			CommandID: "c1", Outcome: "confirmed", OutcomeState: "current", OutcomeReason: "ok",
			DispatchedAt: ptrTime(now), ResolvedAt: ptrTime(now),
		}, nil, nil
	}}
	e, _ := newTestExecutor(t, st, svc, fd, nil)

	first, problem, err := e.SubmitRun(context.Background(), api.MacroSubmitRequest{
		MacroObjectID: "m", IdempotencyKey: "key-1", Trigger: "api", Issuer: testIssuer(),
	})
	if err != nil || problem != nil {
		t.Fatalf("first SubmitRun = problem %v, err %v, want neither", problem, err)
	}

	_, problem, err = e.SubmitRun(context.Background(), api.MacroSubmitRequest{
		MacroObjectID: "m", IdempotencyKey: "key-2", Trigger: "api", Issuer: testIssuer(),
	})
	close(release)
	if err != nil {
		t.Fatalf("second SubmitRun error = %v, want a problem", err)
	}
	if problem == nil {
		t.Fatal("second SubmitRun returned no problem, want the overlap conflict")
	}
	if problem.ConflictingRunID != first.Run.ID {
		t.Errorf("ConflictingRunID = %q, want %q: a client must not have to parse the run id out of prose", problem.ConflictingRunID, first.Run.ID)
	}
	if err := e.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}
