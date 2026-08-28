package clock

import (
	"context"
	"testing"
	"time"
)

// fakeProvider is a scripted [Provider] for [Tracker] tests: each call to
// Poll returns the next entry in raws (repeating the last one once
// exhausted), so a test can script a sequence of raw readings and assert
// on the derived Status after each one.
type fakeProvider struct {
	kind  ProviderKind
	iface string
	raws  []RawStatus
	i     int
}

func (f *fakeProvider) Kind() ProviderKind            { return f.kind }
func (f *fakeProvider) Interface() string             { return f.iface }
func (f *fakeProvider) Now(context.Context) MediaTime { return MediaTime{} }
func (f *fakeProvider) Close() error                  { return nil }
func (f *fakeProvider) Poll(context.Context) RawStatus {
	if len(f.raws) == 0 {
		return RawStatus{}
	}
	if f.i >= len(f.raws) {
		return f.raws[len(f.raws)-1]
	}
	r := f.raws[f.i]
	f.i++
	return r
}

func lockedRaw(gm string, offset int64) RawStatus {
	return RawStatus{
		Reachable: true, Locked: true,
		Role: RoleFollower, RoleKnown: true,
		Domain: 24, DomainKnown: true,
		GrandmasterIdentity: gm, GMKnown: true,
		OffsetNs: offset, OffsetKnown: true,
		Timescale: TimescalePTP,
	}
}

func unlockedRaw(reason string) RawStatus {
	return RawStatus{Reachable: true, Locked: false, Reason: reason, Role: RoleListening, RoleKnown: true, Timescale: TimescalePTP}
}

func unreachableRaw(reason string) RawStatus {
	return RawStatus{Reachable: false, Reason: reason}
}

// clockSeq drives a *testClock through a scripted sequence of times, one
// per Poll call, so tests can control lockedSeconds/holdoverAge precisely.
type testClock struct {
	i     int
	times []time.Time
}

func (c *testClock) now() time.Time {
	if c.i >= len(c.times) {
		return c.times[len(c.times)-1]
	}
	t := c.times[c.i]
	c.i++
	return t
}

func TestTrackerStartsAcquiring(t *testing.T) {
	p := &fakeProvider{kind: ProviderExternal, iface: "eth0"}
	tr := NewTracker(p, TrackerConfig{}, func() time.Time { return time.Unix(0, 0) })
	if tr.state != StateAcquiring {
		t.Fatalf("NewTracker state = %v, want %v", tr.state, StateAcquiring)
	}
}

func TestTrackerAcquiringToLocked(t *testing.T) {
	p := &fakeProvider{kind: ProviderExternal, iface: "eth0", raws: []RawStatus{
		unlockedRaw("not yet locked"),
		lockedRaw("aaaa.bbbb.cccc", 10),
	}}
	base := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	c := &testClock{times: []time.Time{base, base.Add(time.Second)}}
	tr := NewTracker(p, TrackerConfig{}, c.now)

	s1 := tr.Poll(context.Background())
	if s1.State != StateAcquiring {
		t.Fatalf("poll 1 state = %v, want acquiring", s1.State)
	}

	s2 := tr.Poll(context.Background())
	if s2.State != StateLocked {
		t.Fatalf("poll 2 state = %v, want locked", s2.State)
	}
	if !s2.LockedSecondsKnown || s2.LockedSeconds != 0 {
		t.Fatalf("poll 2 lockedSeconds = %v/%v, want 0/known", s2.LockedSeconds, s2.LockedSecondsKnown)
	}
	if s2.GrandmasterIdentity != "aaaa.bbbb.cccc" {
		t.Fatalf("poll 2 gm = %q", s2.GrandmasterIdentity)
	}
}

func TestTrackerLockedSecondsAccumulate(t *testing.T) {
	p := &fakeProvider{kind: ProviderExternal, iface: "eth0", raws: []RawStatus{
		lockedRaw("aaaa.bbbb.cccc", 5),
		lockedRaw("aaaa.bbbb.cccc", 5),
		lockedRaw("aaaa.bbbb.cccc", 5),
	}}
	base := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	c := &testClock{times: []time.Time{base, base.Add(3 * time.Second), base.Add(10 * time.Second)}}
	tr := NewTracker(p, TrackerConfig{}, c.now)

	tr.Poll(context.Background())
	tr.Poll(context.Background())
	s3 := tr.Poll(context.Background())

	if s3.LockedSeconds != 10 {
		t.Fatalf("lockedSeconds = %d, want 10", s3.LockedSeconds)
	}
}

func TestTrackerLockLostEntersHoldoverThenUnsynchronized(t *testing.T) {
	p := &fakeProvider{kind: ProviderExternal, iface: "eth0", raws: []RawStatus{
		lockedRaw("aaaa.bbbb.cccc", 5),
		unlockedRaw("lost sync"),
		unlockedRaw("still lost"),
	}}
	base := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	c := &testClock{times: []time.Time{base, base.Add(time.Second), base.Add(2 * time.Minute)}}
	tr := NewTracker(p, TrackerConfig{HoldoverLimit: 30 * time.Second}, c.now)

	s1 := tr.Poll(context.Background())
	if s1.State != StateLocked {
		t.Fatalf("s1 state = %v, want locked", s1.State)
	}

	s2 := tr.Poll(context.Background())
	if s2.State != StateHoldover {
		t.Fatalf("s2 state = %v, want holdover", s2.State)
	}
	if !s2.HoldoverAgeKnown {
		t.Fatalf("s2 holdover age not known")
	}

	s3 := tr.Poll(context.Background())
	if s3.State != StateUnsynchronized {
		t.Fatalf("s3 state = %v, want unsynchronized (holdover limit exceeded)", s3.State)
	}
}

func TestTrackerHoldoverRecoversToLocked(t *testing.T) {
	p := &fakeProvider{kind: ProviderExternal, iface: "eth0", raws: []RawStatus{
		lockedRaw("aaaa.bbbb.cccc", 5),
		unlockedRaw("lost sync"),
		lockedRaw("aaaa.bbbb.cccc", 5),
	}}
	base := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	c := &testClock{times: []time.Time{base, base.Add(time.Second), base.Add(2 * time.Second)}}
	tr := NewTracker(p, TrackerConfig{HoldoverLimit: 30 * time.Second}, c.now)

	tr.Poll(context.Background())
	tr.Poll(context.Background())
	s3 := tr.Poll(context.Background())

	if s3.State != StateLocked {
		t.Fatalf("s3 state = %v, want locked", s3.State)
	}
}

func TestTrackerInterfaceLossIsImmediateFailed(t *testing.T) {
	p := &fakeProvider{kind: ProviderExternal, iface: "eth0", raws: []RawStatus{
		lockedRaw("aaaa.bbbb.cccc", 5),
		unreachableRaw("interface eth0 is down"),
	}}
	base := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	c := &testClock{times: []time.Time{base, base.Add(time.Second)}}
	tr := NewTracker(p, TrackerConfig{}, c.now)

	tr.Poll(context.Background())
	s2 := tr.Poll(context.Background())
	if s2.State != StateFailed {
		t.Fatalf("s2 state = %v, want failed", s2.State)
	}
	if s2.Reason == "" {
		t.Fatalf("s2 reason must be set whenever state != locked")
	}
}

func TestTrackerFailedRecoversToAcquiring(t *testing.T) {
	p := &fakeProvider{kind: ProviderExternal, iface: "eth0", raws: []RawStatus{
		unreachableRaw("ptp4l owner stopped it"),
		unlockedRaw("not yet locked"),
	}}
	base := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	c := &testClock{times: []time.Time{base, base.Add(time.Second)}}
	tr := NewTracker(p, TrackerConfig{}, c.now)

	s1 := tr.Poll(context.Background())
	if s1.State != StateFailed {
		t.Fatalf("s1 state = %v, want failed", s1.State)
	}
	s2 := tr.Poll(context.Background())
	if s2.State != StateAcquiring {
		t.Fatalf("s2 state = %v, want acquiring (failed then acquiring)", s2.State)
	}
}

func TestTrackerGrandmasterChangeIsAStep(t *testing.T) {
	p := &fakeProvider{kind: ProviderExternal, iface: "eth0", raws: []RawStatus{
		lockedRaw("aaaa.bbbb.cccc", 5),
		lockedRaw("dddd.eeee.ffff", -1200),
	}}
	base := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	c := &testClock{times: []time.Time{base, base.Add(5 * time.Second)}}
	tr := NewTracker(p, TrackerConfig{}, c.now)

	tr.Poll(context.Background())
	s2 := tr.Poll(context.Background())

	if !s2.LastStepKnown {
		t.Fatalf("s2 expected a recorded step on grandmaster change")
	}
	if s2.LastStepNs != -1200 {
		t.Fatalf("s2 LastStepNs = %d, want -1200", s2.LastStepNs)
	}
	if s2.LastStepAt != base.Add(5*time.Second) {
		t.Fatalf("s2 LastStepAt = %v, want %v", s2.LastStepAt, base.Add(5*time.Second))
	}
	// A grandmaster change starts a fresh lock episode.
	if s2.LockedSeconds != 0 {
		t.Fatalf("s2 LockedSeconds = %d, want 0 (fresh episode after step)", s2.LockedSeconds)
	}
}

func TestTrackerMismatchOnlyWhileLocked(t *testing.T) {
	p := &fakeProvider{kind: ProviderExternal, iface: "eth0", raws: []RawStatus{
		lockedRaw("aaaa.bbbb.cccc", 5),
	}}
	tr := NewTracker(p, TrackerConfig{DomainDeclared: true, Domain: 0}, func() time.Time { return time.Unix(0, 0) })
	s := tr.Poll(context.Background())
	if s.State != StateLocked {
		t.Fatalf("state = %v, want locked", s.State)
	}
	if !s.Mismatch {
		t.Fatalf("expected mismatch: locked to domain 24, declared domain 0")
	}
	if s.MismatchReason == "" {
		t.Fatalf("mismatch reason must be set")
	}
}

func TestTrackerNoMismatchWhenDomainMatches(t *testing.T) {
	p := &fakeProvider{kind: ProviderExternal, iface: "eth0", raws: []RawStatus{
		lockedRaw("aaaa.bbbb.cccc", 5),
	}}
	tr := NewTracker(p, TrackerConfig{DomainDeclared: true, Domain: 24}, func() time.Time { return time.Unix(0, 0) })
	s := tr.Poll(context.Background())
	if s.Mismatch {
		t.Fatalf("expected no mismatch: declared domain matches")
	}
}

func TestTrackerReasonRequiredWheneverNotLocked(t *testing.T) {
	cases := []struct {
		name string
		raw  RawStatus
	}{
		{"acquiring", unlockedRaw("")},
		{"failed", unreachableRaw("")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakeProvider{kind: ProviderExternal, iface: "eth0", raws: []RawStatus{tc.raw}}
			tr := NewTracker(p, TrackerConfig{}, func() time.Time { return time.Unix(0, 0) })
			s := tr.Poll(context.Background())
			if s.State == StateLocked {
				t.Fatalf("unexpected locked state for %s", tc.name)
			}
			if s.Reason == "" {
				t.Fatalf("%s: reason must be non-empty whenever state != locked", tc.name)
			}
		})
	}
}
