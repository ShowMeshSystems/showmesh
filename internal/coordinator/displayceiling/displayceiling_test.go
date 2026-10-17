package displayceiling

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

type fakeStore struct {
	mu  sync.Mutex
	rec *store.DisplayCeilingRecord
}

func (s *fakeStore) GetDisplayCeiling(context.Context) (store.DisplayCeilingRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rec == nil {
		return store.DisplayCeilingRecord{}, false, nil
	}
	return *s.rec, true, nil
}

func (s *fakeStore) SetDisplayCeiling(_ context.Context, rec store.DisplayCeilingRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rec = &rec
	return nil
}

type write struct {
	url          string
	target, fade int
}

type rig struct {
	t      *testing.T
	now    time.Time
	store  *fakeStore
	eps    []Endpoint
	mu     sync.Mutex
	writes []write
	fail   map[string]error
	r      *Reconciler
}

var t0 = time.Date(2026, 10, 17, 20, 0, 0, 0, time.UTC)

func newRig(t *testing.T, ids ...string) *rig {
	g := &rig{t: t, now: t0, store: &fakeStore{}, fail: map[string]error{}}
	for _, id := range ids {
		g.eps = append(g.eps, Endpoint{ID: id, URL: "http://" + id})
	}
	g.r = New(Options{
		Store:     g.store,
		Endpoints: func(context.Context) ([]Endpoint, error) { return g.eps, nil },
		Write: func(_ context.Context, url string, target, fade int) error {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.writes = append(g.writes, write{url, target, fade})
			return g.fail[url]
		},
		Now:          func() time.Time { return g.now },
		PollInterval: 5 * time.Second,
	})
	return g
}

func (g *rig) step() []write {
	g.mu.Lock()
	before := len(g.writes)
	g.mu.Unlock()
	g.r.Step(context.Background())
	g.r.wg.Wait()
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]write(nil), g.writes[before:]...)
}

func (g *rig) setDesired(target int, at time.Time) {
	g.store.rec = &store.DisplayCeilingRecord{TargetPercent: target, ChangedAt: at, ChangedByKind: ChangedByOperator, ChangedBy: "p1"}
}

func i64(v int64) *int64 { return &v }
func boolp(v bool) *bool { return &v }

// newPlugin is a reading from a plugin with section 2.8's fields, whose clock
// runs skew behind the coordinator's. changedAt is on the coordinator's clock.
func (g *rig) newPlugin(id string, target int, changedAt time.Time, skew time.Duration) Reading {
	plugin := func(t time.Time) int64 { return t.Add(-skew).UnixMilli() }
	return Reading{
		InstanceID: id, ReceivedAt: g.now,
		Ceiling: i64(int64(target)), FadeActive: boolp(false), UpdatedAtMillis: i64(plugin(g.now)),
		CeilingTarget: i64(int64(target)), CeilingFadeEndMillis: i64(0), CeilingChangedAtMillis: i64(plugin(changedAt)),
	}
}

func oldPlugin(id string, ceiling int, at time.Time) Reading {
	return Reading{
		InstanceID: id, ReceivedAt: at,
		Ceiling: i64(int64(ceiling)), FadeActive: boolp(false), UpdatedAtMillis: i64(at.UnixMilli()),
	}
}

func TestNoDesiredCeilingWritesNothing(t *testing.T) {
	g := newRig(t, "a")
	g.r.Observe(oldPlugin("a", 40, g.now))
	if w := g.step(); len(w) != 0 {
		t.Fatalf("writes = %+v, want none before the first ceiling", w)
	}
}

func TestSkewCorrectionPutsTheChangeOnTheCoordinatorClock(t *testing.T) {
	g := newRig(t, "a", "b")
	g.setDesired(50, t0.Add(-time.Minute))
	// The plugin's clock is ten minutes behind; its change was 30 s ago on the coordinator's clock.
	g.r.Observe(g.newPlugin("a", 20, t0.Add(-30*time.Second), 10*time.Minute))
	g.r.Observe(g.newPlugin("b", 50, t0.Add(-time.Hour), 0))
	g.step()
	got := g.store.rec
	if got.TargetPercent != 20 || got.ChangedBy != "a" || got.ChangedByKind != ChangedByFPP {
		t.Fatalf("desired = %+v, want 20 adopted from a", got)
	}
	if !got.ChangedAt.Equal(t0.Add(-30 * time.Second)) {
		t.Fatalf("changedAt = %v, want %v on the coordinator's clock", got.ChangedAt, t0.Add(-30*time.Second))
	}

	// Skewed the other way, the same raw plugin time reads as older than the desired ceiling.
	h := newRig(t, "a")
	h.setDesired(50, t0.Add(-time.Minute))
	h.r.Observe(h.newPlugin("a", 20, t0.Add(-2*time.Minute), -10*time.Minute))
	if w := h.step(); len(w) != 1 || w[0].target != 50 || h.store.rec.TargetPercent != 50 {
		t.Fatalf("writes = %+v desired = %+v, want a written back to 50", w, h.store.rec)
	}
}

func TestANewerChangeOnAnFPPIsAdoptedAndSpread(t *testing.T) {
	g := newRig(t, "a", "b")
	g.setDesired(50, t0.Add(-time.Minute))
	g.r.Observe(g.newPlugin("a", 20, t0.Add(-10*time.Second), 0))
	g.r.Observe(g.newPlugin("b", 50, t0.Add(-time.Hour), 0))
	w := g.step()
	if len(w) != 1 || w[0] != (write{"http://b", 20, 0}) {
		t.Fatalf("writes = %+v, want only b set to 20", w)
	}
}

func TestAnOlderChangeOnAnFPPIsOverwritten(t *testing.T) {
	g := newRig(t, "a")
	g.setDesired(50, t0.Add(-time.Minute))
	g.r.Observe(g.newPlugin("a", 20, t0.Add(-2*time.Minute), 0))
	w := g.step()
	if g.store.rec.TargetPercent != 50 || len(w) != 1 || w[0] != (write{"http://a", 50, 0}) {
		t.Fatalf("desired = %+v writes = %+v, want a set back to 50", g.store.rec, w)
	}
}

func TestLatestChangeWinsWithinOneRound(t *testing.T) {
	g := newRig(t, "a", "b", "c")
	g.setDesired(50, t0.Add(-time.Minute))
	g.r.Observe(g.newPlugin("a", 20, t0.Add(-20*time.Second), 0))
	g.r.Observe(g.newPlugin("b", 30, t0.Add(-10*time.Second), 3*time.Second))
	g.r.Observe(g.newPlugin("c", 50, t0.Add(-time.Hour), 0))
	w := g.step()
	if g.store.rec.TargetPercent != 30 || g.store.rec.ChangedBy != "b" {
		t.Fatalf("desired = %+v, want b's 30", g.store.rec)
	}
	if len(w) != 2 {
		t.Fatalf("writes = %+v, want a and c set to 30", w)
	}
	for _, x := range w {
		if x.target != 30 || x.url == "http://b" {
			t.Fatalf("write %+v, want only a and c set to 30", x)
		}
	}
}

func TestAnOldPluginIsWrittenButNeverAdoptedFrom(t *testing.T) {
	g := newRig(t, "old")
	g.setDesired(50, t0.Add(-time.Minute))
	g.r.Observe(oldPlugin("old", 20, g.now))
	w := g.step()
	if g.store.rec.TargetPercent != 50 || len(w) != 1 || w[0].target != 50 {
		t.Fatalf("desired = %+v writes = %+v, want the old plugin set to 50", g.store.rec, w)
	}
	g.r.Observe(oldPlugin("old", 50, g.now.Add(time.Second)))
	g.now = g.now.Add(time.Second)
	snap, _ := g.r.Snapshot(context.Background())
	if snap.FPPs[0].State != StatePluginTooOld || snap.FPPs[0].Reason == "" {
		t.Fatalf("status = %+v, want pluginTooOld with a reason", snap.FPPs[0])
	}

	// An old plugin mid-fade has no knowable target, so it is left alone.
	g.r.Observe(Reading{InstanceID: "old", ReceivedAt: g.now, Ceiling: i64(30), FadeActive: boolp(true), UpdatedAtMillis: i64(g.now.UnixMilli())})
	g.now = g.now.Add(10 * time.Second)
	if w := g.step(); len(w) != 0 {
		t.Fatalf("writes = %+v, want none while an old plugin fades", w)
	}
}

func TestAPlayerWithoutTheRouteIsNotFollowingAndNotWritten(t *testing.T) {
	g := newRig(t, "bare")
	g.setDesired(50, t0.Add(-time.Minute))
	g.r.Observe(Reading{InstanceID: "bare", ReceivedAt: g.now, NotFound: true})
	if w := g.step(); len(w) != 0 {
		t.Fatalf("writes = %+v, want none to a player without the route", w)
	}
	snap, _ := g.r.Snapshot(context.Background())
	if snap.FPPs[0].State != StateNotReporting || snap.FPPs[0].Reason == "" {
		t.Fatalf("status = %+v, want notReporting with a reason", snap.FPPs[0])
	}
}

func TestAWriteIsNotRepeatedWithinOnePollInterval(t *testing.T) {
	g := newRig(t, "a")
	g.setDesired(50, t0.Add(-time.Minute))
	g.r.Observe(oldPlugin("a", 20, g.now))
	if w := g.step(); len(w) != 1 {
		t.Fatalf("first pass writes = %+v, want 1", w)
	}
	g.now = g.now.Add(2 * time.Second)
	g.r.Observe(oldPlugin("a", 20, g.now))
	if w := g.step(); len(w) != 0 {
		t.Fatalf("writes 2 s later = %+v, want none", w)
	}
	g.now = g.now.Add(4 * time.Second)
	if w := g.step(); len(w) != 1 {
		t.Fatalf("writes a poll later after a reading still at 20 = %+v, want 1", w)
	}
	g.now = g.now.Add(6 * time.Second)
	if w := g.step(); len(w) != 0 {
		t.Fatalf("writes with no reading since the last write = %+v, want none", w)
	}

	// A new desired ceiling is written at once, whatever the last attempt was.
	if _, err := g.r.Set(context.Background(), 70, 0, ChangedByOperator, "p1", ""); err != nil {
		t.Fatal(err)
	}
	if w := g.step(); len(w) != 1 || w[0].target != 70 {
		t.Fatalf("writes after a new ceiling = %+v, want 70 at once", w)
	}
}

func TestALateJoinerGetsTheRemainingFadeRoundedUp(t *testing.T) {
	g := newRig(t, "a")
	if _, err := g.r.Set(context.Background(), 10, 30, ChangedByOperator, "p1", ""); err != nil {
		t.Fatal(err)
	}
	g.now = g.now.Add(19*time.Second + 700*time.Millisecond)
	g.r.Observe(oldPlugin("a", 100, g.now))
	if w := g.step(); len(w) != 1 || w[0].fade != 11 {
		t.Fatalf("writes = %+v, want the remaining 10.3 s sent as 11", w)
	}
	g.now = g.now.Add(time.Minute)
	g.r.Observe(oldPlugin("a", 100, g.now))
	if w := g.step(); len(w) != 1 || w[0].fade != 0 {
		t.Fatalf("writes after the fade ended = %+v, want fade 0", w)
	}
}

func TestOurOwnLateWriteIsNotAdopted(t *testing.T) {
	g := newRig(t, "a")
	g.setDesired(50, t0.Add(-time.Minute))
	g.r.Observe(g.newPlugin("a", 20, t0.Add(-time.Hour), 0))
	g.step() // writes 50
	// The operator sets 70 before the FPP's poll shows the write of 50 landing.
	g.now = g.now.Add(500 * time.Millisecond)
	if _, err := g.r.Set(context.Background(), 70, 0, ChangedByOperator, "p1", ""); err != nil {
		t.Fatal(err)
	}
	g.now = g.now.Add(500 * time.Millisecond)
	g.r.Observe(g.newPlugin("a", 50, g.now.Add(-200*time.Millisecond), 0))
	w := g.step()
	if g.store.rec.TargetPercent != 70 || len(w) != 1 || w[0].target != 70 {
		t.Fatalf("desired = %+v writes = %+v, want 70 kept and written", g.store.rec, w)
	}
}

func TestAnFPPAddedLaterIsBroughtIn(t *testing.T) {
	g := newRig(t, "a")
	g.setDesired(50, t0.Add(-time.Minute))
	g.r.Observe(g.newPlugin("a", 50, t0.Add(-time.Hour), 0))
	g.step()
	g.eps = append(g.eps, Endpoint{ID: "late", URL: "http://late"})
	g.r.Observe(g.newPlugin("late", 100, t0.Add(-2*time.Hour), 0))
	if w := g.step(); len(w) != 1 || w[0] != (write{"http://late", 50, 0}) {
		t.Fatalf("writes = %+v, want the new FPP set to 50", w)
	}
}

func TestOneFailingFPPDoesNotStopTheOthers(t *testing.T) {
	g := newRig(t, "a", "b")
	g.fail["http://a"] = errors.New("connection refused")
	g.setDesired(50, t0.Add(-time.Minute))
	g.r.Observe(oldPlugin("a", 20, g.now))
	g.r.Observe(g.newPlugin("b", 20, t0.Add(-time.Hour), 0))
	if w := g.step(); len(w) != 2 {
		t.Fatalf("writes = %+v, want both attempted", w)
	}
	g.now = g.now.Add(time.Second)
	snap, _ := g.r.Snapshot(context.Background())
	for _, s := range snap.FPPs {
		if s.State != StateCatchingUp {
			t.Fatalf("status = %+v, want catchingUp", s)
		}
	}
	if snap.FPPs[0].Reason == snap.FPPs[1].Reason {
		t.Fatalf("a failed write and a pending write read the same: %q", snap.FPPs[0].Reason)
	}
}

func TestSnapshotStates(t *testing.T) {
	g := newRig(t, "following", "silent", "stale")
	g.setDesired(50, t0.Add(-time.Minute))
	g.r.Observe(g.newPlugin("following", 50, t0.Add(-time.Hour), 0))
	g.r.Observe(oldPlugin("stale", 50, t0.Add(-time.Minute)))
	snap, err := g.r.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Desired == nil || snap.Desired.TargetPercent != 50 {
		t.Fatalf("desired = %+v", snap.Desired)
	}
	want := map[string]State{"following": StateFollowing, "silent": StateNotReporting, "stale": StateNotReporting}
	for _, s := range snap.FPPs {
		if s.State != want[s.InstanceID] {
			t.Errorf("%s state = %s, want %s", s.InstanceID, s.State, want[s.InstanceID])
		}
		if (s.State == StateFollowing) != (s.Reason == "") {
			t.Errorf("%s reason = %q; only following carries no reason", s.InstanceID, s.Reason)
		}
	}
}

func TestSetRefusesOutOfRange(t *testing.T) {
	g := newRig(t)
	for _, c := range [][2]int{{-1, 0}, {101, 0}, {50, -1}, {50, MaxFadeSeconds + 1}} {
		if _, err := g.r.Set(context.Background(), c[0], c[1], ChangedByOperator, "p1", ""); err == nil {
			t.Errorf("Set(%d, %d) succeeded, want refused", c[0], c[1])
		}
	}
	if g.store.rec != nil {
		t.Fatalf("stored %+v after refusals", g.store.rec)
	}
}

func TestAdoptionCarriesTheFadeEnd(t *testing.T) {
	g := newRig(t, "a")
	g.setDesired(50, t0.Add(-time.Minute))
	rd := g.newPlugin("a", 20, t0.Add(-time.Second), time.Hour)
	rd.CeilingFadeEndMillis = i64(t0.Add(-time.Hour).Add(25 * time.Second).UnixMilli())
	g.r.Observe(rd)
	g.step()
	if want := t0.Add(25 * time.Second); !g.store.rec.FadeEndsAt.Equal(want) {
		t.Fatalf("fade end = %v, want %v", g.store.rec.FadeEndsAt, want)
	}
}

// A write of the old ceiling that lands after the operator's newer change,
// seen by a poll after the newer write has been launched, is still our own.
func TestAnEarlierOwnWriteLandingLateIsNotAdopted(t *testing.T) {
	g := newRig(t, "a")
	g.setDesired(50, t0.Add(-time.Minute))
	g.r.Observe(g.newPlugin("a", 20, t0.Add(-time.Hour), 0))
	if w := g.step(); len(w) != 1 || w[0].target != 50 { // W50
		t.Fatalf("writes = %+v, want W50", w)
	}
	g.now = g.now.Add(200 * time.Millisecond)
	if _, err := g.r.Set(context.Background(), 60, 0, ChangedByOperator, "p1", ""); err != nil {
		t.Fatal(err)
	}
	// W50 lands on the FPP after the 60 was set; a poll before W60 lands still reads 20.
	g.now = g.now.Add(200 * time.Millisecond)
	g.r.Observe(g.newPlugin("a", 20, t0.Add(-time.Hour), 0))
	if w := g.step(); len(w) != 1 || w[0].target != 60 { // W60 replaces the latest write
		t.Fatalf("writes = %+v, want W60", w)
	}
	g.now = g.now.Add(300 * time.Millisecond)
	g.r.Observe(g.newPlugin("a", 50, t0.Add(300*time.Millisecond), 0))
	g.step()
	if g.store.rec.TargetPercent != 60 {
		t.Fatalf("desired = %+v, want the operator's 60 kept", g.store.rec)
	}
}
