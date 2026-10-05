package fallbackhold

import (
	"context"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

const testInstance = "11111111-2222-4333-8444-555555555555"

var testStart = time.Date(2026, 10, 5, 20, 0, 0, 0, time.UTC)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// newService returns a Service whose coordinator started at startedAt.
func newService(t *testing.T, startedAt time.Time) (*Service, *store.Store) {
	t.Helper()
	st := openStore(t)
	return NewService(st, startedAt, nil), st
}

func report(state string, seq int64) Report {
	r := Report{FPPInstanceUUID: testInstance, BootID: "boot-a", Sequence: seq, State: state, Since: testStart}
	if state != StateNormal {
		r.PlaylistName, r.PackageID, r.PackageRevision, r.CutoffAt = "Main Show", "pkg-1", "rev-1", "2026-10-06T08:00:00Z"
	}
	return r
}

func mustRecord(t *testing.T, svc *Service, r Report, at time.Time) RecordResult {
	t.Helper()
	res, err := svc.Record(context.Background(), r, at)
	if err != nil {
		t.Fatalf("record %s at %s: %v", r.State, at.Format(time.TimeOnly), err)
	}
	return res
}

func mustEvaluate(t *testing.T, svc *Service, at time.Time) Verdict {
	t.Helper()
	v, err := svc.Evaluate(context.Background(), testInstance, at)
	if err != nil {
		t.Fatalf("evaluate at %s: %v", at.Format(time.TimeOnly), err)
	}
	return v
}

func putObservation(t *testing.T, st *store.Store, receivedAt time.Time, sequence int64) {
	t.Helper()
	if err := st.InTx(context.Background(), func(ctx context.Context, tx *store.Tx) error {
		return tx.PutFPPPlaylistEntryObservation(ctx, store.FPPPlaylistEntryObservationRecord{
			InstanceUUID: testInstance, SchemaVersion: 1, Sequence: sequence, BodyHash: "h", ObservationJSON: "{}",
			Action: "playing", ObservedAt: receivedAt, ReceivedAt: receivedAt, EntryOccurrenceSequence: sequence,
		})
	}); err != nil {
		t.Fatalf("put observation: %v", err)
	}
}

func putProgram(t *testing.T, st *store.Store, packageID, revision string) {
	t.Helper()
	if err := st.PutFallbackProgram(context.Background(), store.FallbackProgramRecord{
		FPPInstanceUUID: testInstance, PackageID: packageID, Revision: revision, ShowID: "show", Generation: 1,
		ProgramJSON: "{}", SignatureB64: "sig", ExpiresAt: testStart.Add(24 * time.Hour), CompiledAt: testStart,
	}); err != nil {
		t.Fatalf("put program: %v", err)
	}
}

func putAck(t *testing.T, st *store.Store, packageID, revision, result string, at time.Time) {
	t.Helper()
	if err := st.PutFallbackProgramAck(context.Background(), store.FallbackProgramAckRecord{
		FPPInstanceUUID: testInstance, PackageID: packageID, Revision: revision, VerificationResult: result,
		InstalledAt: at, AcknowledgedAt: at,
	}); err != nil {
		t.Fatalf("put ack: %v", err)
	}
}

func TestAPlayerWhosePluginNeverReportedIsNeverHeld(t *testing.T) {
	svc, st := newService(t, testStart)
	putObservation(t, st, testStart.Add(-time.Hour), 1)
	for _, at := range []time.Time{testStart, testStart.Add(10 * time.Second), testStart.Add(time.Hour)} {
		if v := mustEvaluate(t, svc, at); v.Reported || v.Held || v.IgnoresObservation(testStart.Add(-time.Hour)) {
			t.Fatalf("at %s a player with no report got %+v, want never reported and never held", at.Format(time.TimeOnly), v)
		}
	}
}

// Rule (a): a report of fallback or resting holds whatever its age.
func TestAReportedFallbackOrRestingHoldsWhateverItsAge(t *testing.T) {
	for _, state := range []string{StateFallback, StateResting} {
		t.Run(state, func(t *testing.T) {
			svc, _ := newService(t, testStart.Add(-time.Hour))
			mustRecord(t, svc, report(state, 1), testStart)
			for _, age := range []time.Duration{0, 30 * time.Second, 10 * time.Minute, 20 * time.Hour} {
				v := mustEvaluate(t, svc, testStart.Add(age))
				if !v.Held || v.Reason != ReasonExecutor {
					t.Fatalf("%s old: held %v reason %q, want held as %q", age, v.Held, v.Reason, ReasonExecutor)
				}
			}
			if v := mustEvaluate(t, svc, testStart.Add(time.Hour)); v.PluginReporting {
				t.Fatal("an hour after its last report the plugin still reads as reporting")
			}
		})
	}
}

// Rule (b): a fresh normal report does not hold.
func TestAFreshNormalReportDoesNotHold(t *testing.T) {
	svc, _ := newService(t, testStart.Add(-time.Hour))
	mustRecord(t, svc, report(StateNormal, 1), testStart)
	v := mustEvaluate(t, svc, testStart.Add(SilentAfter))
	if v.Held || !v.PluginReporting || !v.Reported {
		t.Fatalf("a normal report %s old got %+v, want reported, reporting and not held", SilentAfter, v)
	}
}

// Rule (c), and the case the owner's coordinator named: a reporting plugin
// dies while FPP keeps playing and the coordinator stays up. The night
// must still advance, and the silence must be visible.
func TestAnOldNormalReportDoesNotHoldAndReadsAsNotReporting(t *testing.T) {
	svc, st := newService(t, testStart.Add(-time.Hour))
	mustRecord(t, svc, report(StateNormal, 1), testStart)
	for _, age := range []time.Duration{SilentAfter + time.Second, time.Hour, 48 * time.Hour} {
		v := mustEvaluate(t, svc, testStart.Add(age))
		if v.Held {
			t.Fatalf("%s after its last normal report the player is held (%q); a dead plugin must not stop the night", age, v.Reason)
		}
		if v.PluginReporting {
			t.Fatalf("%s after its last report the plugin still reads as reporting", age)
		}
	}

	at := testStart.Add(time.Hour)
	svc.WriteSignals(context.Background(), at)
	reporting := signal(t, st, SignalPluginReporting)
	if reporting.Value != false {
		t.Fatalf("%s = %v, want false", SignalPluginReporting, reporting.Value)
	}
	if got := signal(t, st, SignalCoordinatorHolding).Value; got != false {
		t.Fatalf("%s = %v, want false", SignalCoordinatorHolding, got)
	}
	if state := signal(t, st, SignalPlayerState).StateAt(at); state != observation.StateStale {
		t.Fatalf("an hour after the last report %s is %q, want stale", SignalPlayerState, state)
	}
	if Message(mustEvaluate(t, svc, at)) == "" {
		t.Fatal("a plugin that stopped reporting has no operator message")
	}
}

// Rule (d): for the first 45 seconds after the coordinator starts, a
// player that reported in an earlier run is held until it reports again.
func TestAfterACoordinatorStartAPlayerThatReportedBeforeIsHeldUntilItReportsOrTheGraceEnds(t *testing.T) {
	earlier := testStart.Add(-10 * time.Minute)
	svc, _ := newService(t, earlier.Add(-time.Hour))
	mustRecord(t, svc, report(StateNormal, 7), earlier)

	restarted := NewService(svc.st, testStart, nil)
	for _, since := range []time.Duration{0, time.Second, StartGrace} {
		v := mustEvaluate(t, restarted, testStart.Add(since))
		if !v.Held || v.Reason != ReasonCoordinatorStarting {
			t.Fatalf("%s after the start: held %v reason %q, want held as %q", since, v.Held, v.Reason, ReasonCoordinatorStarting)
		}
	}
	if v := mustEvaluate(t, restarted, testStart.Add(StartGrace+time.Second)); v.Held || v.PluginReporting {
		t.Fatalf("after the grace with no report: %+v, want not held and not reporting", v)
	}

	// A report in this run ends the hold at once, and nothing the player
	// did before it is replayed.
	at := testStart.Add(5 * time.Second)
	next := report(StateNormal, 1)
	next.BootID = "boot-b"
	mustRecord(t, restarted, next, at)
	v := mustEvaluate(t, restarted, at.Add(time.Second))
	if v.Held {
		t.Fatalf("after the first report of this run the player is still held as %q", v.Reason)
	}
	if !v.IgnoresObservation(earlier) || !v.IgnoresObservation(at) {
		t.Fatal("an observation received before the first report of this run would still be acted on")
	}
	if v.IgnoresObservation(at.Add(time.Millisecond)) {
		t.Fatal("an observation received after the first report of this run is ignored")
	}
}

func TestAPlayerThatNeverReportedIsNotHeldAfterACoordinatorStart(t *testing.T) {
	svc, _ := newService(t, testStart)
	if v := mustEvaluate(t, svc, testStart.Add(time.Second)); v.Held {
		t.Fatalf("a player with no report is held as %q right after a start", v.Reason)
	}
}

// D5: an observation that arrives more than 45 seconds after the latest
// report comes from a plugin that does not report, so the player is free.
func TestAnObservationLongAfterTheLatestReportEndsTheHold(t *testing.T) {
	svc, st := newService(t, testStart.Add(-time.Hour))
	mustRecord(t, svc, report(StateFallback, 1), testStart)

	putObservation(t, st, testStart.Add(SilentAfter), 1)
	if v := mustEvaluate(t, svc, testStart.Add(SilentAfter+time.Second)); !v.Held {
		t.Fatal("an observation 45 seconds after the report already ended the hold; it must be more than 45 seconds")
	}

	late := testStart.Add(SilentAfter + time.Second)
	putObservation(t, st, late, 2)
	v := mustEvaluate(t, svc, late.Add(time.Second))
	if v.Held || v.PluginReporting {
		t.Fatalf("after a late observation: %+v, want not held and not reporting", v)
	}
	if v.IgnoresObservation(late) {
		t.Fatal("the observation that ended the hold is itself ignored")
	}

	// The next report takes the player back.
	mustRecord(t, svc, report(StateFallback, 2), late.Add(10*time.Second))
	if v := mustEvaluate(t, svc, late.Add(11*time.Second)); !v.Held {
		t.Fatal("a new fallback report after the escape does not hold")
	}
}

// Q5: after a hand-back the hold lasts until this player's program
// acknowledgement is current, and the wait is visible.
func TestAfterAHandBackTheHoldWaitsForTheProgramAcknowledgement(t *testing.T) {
	svc, st := newService(t, testStart.Add(-time.Hour))
	putProgram(t, st, "pkg-2", "rev-2")
	putAck(t, st, "pkg-1", "rev-1", v1.FallbackProgramVerificationVerified, testStart.Add(-time.Minute))
	mustRecord(t, svc, report(StateFallback, 1), testStart)

	handBack := testStart.Add(10 * time.Minute)
	mustRecord(t, svc, report(StateNormal, 2), handBack)
	v := mustEvaluate(t, svc, handBack.Add(20*time.Second))
	if !v.Held || v.Reason != ReasonAwaitingAcknowledgement || !v.AckWaitSince.Equal(handBack) {
		t.Fatalf("after the hand-back with a stale acknowledgement: %+v, want held waiting since the hand-back", v)
	}
	svc.WriteSignals(context.Background(), handBack.Add(20*time.Second))
	if got := signal(t, st, SignalAcknowledgementWaitSec).Value; got != float64(20) {
		t.Fatalf("%s = %v, want 20", SignalAcknowledgementWaitSec, got)
	}
	if got := signal(t, st, SignalCoordinatorHolding).Value; got != true {
		t.Fatalf("%s = %v, want true", SignalCoordinatorHolding, got)
	}

	// Still waiting when the plugin keeps reporting normal and when it
	// goes quiet: only the acknowledgement or an operator ends this hold.
	mustRecord(t, svc, report(StateNormal, 3), handBack.Add(30*time.Second))
	if v := mustEvaluate(t, svc, handBack.Add(time.Hour)); !v.Held || v.Reason != ReasonAwaitingAcknowledgement {
		t.Fatalf("an hour into the wait: %+v, want still held waiting", v)
	}

	// A refused program is not an acknowledgement.
	putAck(t, st, "pkg-2", "rev-2", v1.FallbackProgramVerificationSignatureInvalid, handBack.Add(40*time.Second))
	if v := mustEvaluate(t, svc, handBack.Add(41*time.Second)); !v.Held {
		t.Fatal("an acknowledgement that reports a bad signature ended the hold")
	}

	putAck(t, st, "pkg-2", "rev-2", v1.FallbackProgramVerificationVerified, handBack.Add(50*time.Second))
	v = mustEvaluate(t, svc, handBack.Add(51*time.Second))
	if v.Held {
		t.Fatalf("with a current acknowledgement the player is still held as %q", v.Reason)
	}
	if v.IgnoresObservation(handBack.Add(time.Second)) {
		t.Fatal("an observation received after the hand-back report is ignored")
	}
	if !v.IgnoresObservation(handBack) {
		t.Fatal("an observation received before the hand-back report would be acted on")
	}

	// The wait is over for good: a newer program later does not hold again.
	putProgram(t, st, "pkg-3", "rev-3")
	mustRecord(t, svc, report(StateNormal, 4), handBack.Add(60*time.Second))
	if v := mustEvaluate(t, svc, handBack.Add(61*time.Second)); v.Held {
		t.Fatalf("a program published after the wait ended brought the hold back as %q", v.Reason)
	}
}

func TestAHandBackWithACurrentAcknowledgementOrNoProgramEndsTheHoldAtOnce(t *testing.T) {
	t.Run("acknowledgement already current", func(t *testing.T) {
		svc, st := newService(t, testStart.Add(-time.Hour))
		putProgram(t, st, "pkg-1", "rev-1")
		putAck(t, st, "pkg-1", "rev-1", v1.FallbackProgramVerificationVerified, testStart.Add(-time.Minute))
		mustRecord(t, svc, report(StateFallback, 1), testStart)
		mustRecord(t, svc, report(StateNormal, 2), testStart.Add(time.Minute))
		if v := mustEvaluate(t, svc, testStart.Add(time.Minute)); v.Held {
			t.Fatalf("held as %q although the acknowledgement is current", v.Reason)
		}
	})
	t.Run("nothing published", func(t *testing.T) {
		svc, _ := newService(t, testStart.Add(-time.Hour))
		mustRecord(t, svc, report(StateResting, 1), testStart)
		mustRecord(t, svc, report(StateNormal, 2), testStart.Add(time.Minute))
		if v := mustEvaluate(t, svc, testStart.Add(time.Minute)); v.Held {
			t.Fatalf("held as %q although no program is published for this player", v.Reason)
		}
	})
}

func TestAReportIsKeptOnlyWhenItIsNewerWithinOnePluginStart(t *testing.T) {
	svc, _ := newService(t, testStart.Add(-time.Hour))
	first := mustRecord(t, svc, report(StateFallback, 5), testStart)
	if !first.Recorded || !first.StateChanged || first.PreviousState != "" {
		t.Fatalf("first report = %+v, want recorded as a change from nothing", first)
	}

	// A delayed normal report from before the fallback must not end it.
	stale := mustRecord(t, svc, report(StateNormal, 4), testStart.Add(time.Second))
	if stale.Recorded || stale.Stored.State != StateFallback {
		t.Fatalf("a lower sequence = %+v, want not recorded and fallback kept", stale)
	}
	same := mustRecord(t, svc, report(StateFallback, 5), testStart.Add(2*time.Second))
	if same.Recorded {
		t.Fatal("a repeat of the stored sequence was recorded again")
	}

	repeat := mustRecord(t, svc, report(StateFallback, 6), testStart.Add(10*time.Second))
	if !repeat.Recorded || repeat.StateChanged || !repeat.Stored.StateChangedAt.Equal(testStart) {
		t.Fatalf("a repeat of the same state = %+v, want recorded, no change, change time kept", repeat)
	}

	// A new plugin start begins its own sequence.
	restarted := report(StateResting, 1)
	restarted.BootID = "boot-b"
	next := mustRecord(t, svc, restarted, testStart.Add(20*time.Second))
	if !next.Recorded || !next.StateChanged || next.PreviousState != StateFallback {
		t.Fatalf("a report from a new plugin start = %+v, want recorded as a change from fallback", next)
	}
}

func TestEveryFallbackKeepaliveMovesTheObservationFloor(t *testing.T) {
	svc, _ := newService(t, testStart.Add(-time.Hour))
	mustRecord(t, svc, report(StateFallback, 1), testStart)
	mustRecord(t, svc, report(StateFallback, 2), testStart.Add(10*time.Second))
	v := mustEvaluate(t, svc, testStart.Add(11*time.Second))
	if !v.IgnoreObservationsThrough.Equal(testStart.Add(10 * time.Second)) {
		t.Fatalf("observations are ignored through %s, want the latest fallback report's time", v.IgnoreObservationsThrough.Format(time.TimeOnly))
	}
}

func TestAnyHeldNamesAHeldPlayer(t *testing.T) {
	svc, _ := newService(t, testStart.Add(-time.Hour))
	if held, _, err := svc.AnyHeld(context.Background(), testStart); err != nil || held {
		t.Fatalf("with no reports: held %v err %v", held, err)
	}
	mustRecord(t, svc, report(StateFallback, 1), testStart)
	held, id, err := svc.AnyHeld(context.Background(), testStart.Add(time.Second))
	if err != nil || !held || id != testInstance {
		t.Fatalf("held %v id %q err %v, want the reporting player named", held, id, err)
	}
}

func TestEveryHeldVerdictAndASilentPluginHaveAnOperatorMessage(t *testing.T) {
	rec := store.FallbackPlayerStateRecord{State: StateFallback}
	cases := map[string]Verdict{
		"fallback":      {Reported: true, Held: true, Reason: ReasonExecutor, Record: rec, PluginReporting: true},
		"resting":       {Reported: true, Held: true, Reason: ReasonExecutor, Record: store.FallbackPlayerStateRecord{State: StateResting}},
		"waiting":       {Reported: true, Held: true, Reason: ReasonAwaitingAcknowledgement},
		"starting":      {Reported: true, Held: true, Reason: ReasonCoordinatorStarting},
		"not reporting": {Reported: true},
	}
	seen := map[string]string{}
	for name, v := range cases {
		msg := Message(v)
		if msg == "" {
			t.Errorf("%s has no message", name)
		}
		if other, dup := seen[msg]; dup {
			t.Errorf("%s and %s share one message", name, other)
		}
		seen[msg] = name
	}
	if msg := Message(Verdict{Reported: true, PluginReporting: true}); msg != "" {
		t.Errorf("a normal, reporting player has the message %q, want none", msg)
	}
	if msg := Message(Verdict{}); msg != "" {
		t.Errorf("a player that never reported has the message %q, want none", msg)
	}
}

func signal(t *testing.T, st *store.Store, id observation.SignalID) observation.Observation {
	t.Helper()
	all, err := st.ListObservations(context.Background(), store.ObservationFilter{ResourceKind: observation.ResourceFallbackProgram})
	if err != nil {
		t.Fatalf("list observations: %v", err)
	}
	for _, o := range all {
		if o.Signal == id && o.Resource.ID == testInstance {
			return o
		}
	}
	t.Fatalf("signal %s was not written", id)
	return observation.Observation{}
}
