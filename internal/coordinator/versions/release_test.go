package versions

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

type fakeFetcher struct {
	release Release
	err     error
	started chan struct{}
	unblock chan struct{}
	calls   atomic.Int32
}

func (f *fakeFetcher) LatestRelease(ctx context.Context) (Release, error) {
	f.calls.Add(1)
	if f.started != nil {
		close(f.started)
		select {
		case <-f.unblock:
		case <-ctx.Done():
			return Release{}, ctx.Err()
		}
	}
	return f.release, f.err
}

var checkTime = time.Date(2026, 10, 12, 18, 0, 0, 0, time.UTC)

func fixedNow() time.Time { return checkTime }

func TestCheckerCachesTheAnswerAndWhenItWasFetched(t *testing.T) {
	rel := Release{Tag: "v0.3.0", PublishedAt: checkTime.Add(-48 * time.Hour)}
	var published []Status
	c := NewChecker(&fakeFetcher{release: rel}, Settings{Enabled: true, Interval: time.Hour}, fixedNow, nil, func(s Status) { published = append(published, s) })

	if got := c.Status(); got.Release != nil || !got.CheckedAt.IsZero() || !got.Enabled {
		t.Fatalf("before the first check: %+v", got)
	}
	c.CheckOnce(context.Background())
	got := c.Status()
	if got.Release == nil || *got.Release != rel || !got.CheckedAt.Equal(checkTime) || got.Problem != "" {
		t.Fatalf("after the check: %+v", got)
	}
	if len(published) != 1 || published[0].Release == nil {
		t.Fatalf("published = %+v, want the one answer", published)
	}
}

func TestCheckerKeepsTheLastAnswerWhenALaterCheckFails(t *testing.T) {
	f := &fakeFetcher{release: Release{Tag: "v0.3.0"}}
	c := NewChecker(f, Settings{Enabled: true, Interval: time.Hour}, fixedNow, nil, nil)
	c.CheckOnce(context.Background())
	f.err = errors.New("dial tcp: no route to host")
	c.CheckOnce(context.Background())

	got := c.Status()
	if got.Release == nil || got.Release.Tag != "v0.3.0" {
		t.Fatalf("the last answer was dropped: %+v", got)
	}
	if got.Problem != "GitHub could not be reached. The coordinator will try again." {
		t.Fatalf("Problem = %q", got.Problem)
	}
}

func TestCheckerReportsARepositoryWithNoRelease(t *testing.T) {
	c := NewChecker(&fakeFetcher{err: ErrNoRelease}, Settings{Enabled: true, Interval: time.Hour}, fixedNow, nil, nil)
	c.CheckOnce(context.Background())
	if got := c.Status().Problem; got != "GitHub lists no published ShowMesh release yet." {
		t.Fatalf("Problem = %q", got)
	}
}

func TestStatusNeverWaitsForACheckInProgress(t *testing.T) {
	f := &fakeFetcher{release: Release{Tag: "v0.3.0"}, started: make(chan struct{}), unblock: make(chan struct{})}
	c := NewChecker(f, Settings{Enabled: true, Interval: time.Hour}, fixedNow, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	<-f.started

	answered := make(chan Status, 1)
	go func() { answered <- c.Status() }()
	select {
	case got := <-answered:
		if got.Release != nil {
			t.Fatalf("Status returned an answer the blocked check never produced: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Status waited for the check in progress")
	}
	cancel()
	<-done
}

func TestDisabledCheckerNeverFetches(t *testing.T) {
	f := &fakeFetcher{}
	var published []Status
	c := NewChecker(f, Settings{Enabled: false, Interval: time.Hour}, fixedNow, nil, func(s Status) { published = append(published, s) })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	if f.calls.Load() != 0 {
		t.Fatal("a disabled checker fetched")
	}
	if len(published) != 1 || published[0].Enabled {
		t.Fatalf("published = %+v, want one disabled status", published)
	}
	var nilChecker *Checker
	if nilChecker.Status().Enabled {
		t.Fatal("a nil checker reads as enabled")
	}
}

func TestNewCheckerDefaultsTheInterval(t *testing.T) {
	if got := NewChecker(&fakeFetcher{}, Settings{Enabled: true, Interval: 0}, fixedNow, nil, nil).Status().Interval; got != DefaultCheckInterval {
		t.Fatalf("Interval = %v, want %v", got, DefaultCheckInterval)
	}
}

func TestObservationsCarryTheThreeSignals(t *testing.T) {
	s := Status{Enabled: true, Release: &Release{Tag: "v0.3.0"}, CheckedAt: checkTime}
	obs := Observations(s, "0.2.0", checkTime.Add(time.Minute))
	want := map[observation.SignalID]any{
		SignalNewestTag: "v0.3.0",
		SignalCheckedAt: "2026-10-12T18:00:00Z",
		SignalAvailable: true,
	}
	if len(obs) != len(want) {
		t.Fatalf("got %d observations, want %d", len(obs), len(want))
	}
	for _, o := range obs {
		if o.Value != want[o.Signal] || o.Source != SourceID || o.Resource.Kind != observation.ResourceCoordinator {
			t.Errorf("observation %+v, want value %v from %s", o, want[o.Signal], SourceID)
		}
	}
}

func TestObservationsSayWhyThereIsNoAnswer(t *testing.T) {
	for _, tc := range []struct {
		status Status
		state  observation.State
	}{
		{Status{Enabled: false}, observation.StateNotCollected},
		{Status{Enabled: true}, observation.StateNotCollected},
		{Status{Enabled: true, Problem: "GitHub could not be reached. The coordinator will try again."}, observation.StateCollectionFailed},
	} {
		obs := Observations(tc.status, "0.2.0", checkTime)
		if len(obs) != 3 {
			t.Fatalf("%+v: got %d observations, want 3", tc.status, len(obs))
		}
		for _, o := range obs {
			if o.Absence != tc.state || o.Reason == "" {
				t.Errorf("%+v: observation %+v, want absence %s with a reason", tc.status, o, tc.state)
			}
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSetSettingsTurnsTheRunningCheckOnAndOffWithoutARestart(t *testing.T) {
	f := &fakeFetcher{release: Release{Tag: "v0.3.0"}}
	c := NewChecker(f, Settings{Enabled: false, Interval: time.Hour}, fixedNow, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	c.SetSettings(Settings{Enabled: true, Interval: time.Hour})
	waitFor(t, "the check to run once turned on", func() bool { return c.Status().Release != nil })
	if got := c.Status(); !got.Enabled || got.Interval != time.Hour {
		t.Fatalf("status = %+v, want on and hourly", got)
	}

	c.SetSettings(Settings{Enabled: false, Interval: time.Hour})
	if c.Status().Enabled {
		t.Fatal("the status still reads on after it was turned off")
	}
}

func TestSetSettingsRestartsTheTimerOnTheNewInterval(t *testing.T) {
	f := &fakeFetcher{release: Release{Tag: "v0.3.0"}}
	c := NewChecker(f, Settings{Enabled: true, Interval: time.Hour}, fixedNow, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	waitFor(t, "the first check", func() bool { return f.calls.Load() == 1 })

	c.SetSettings(Settings{Enabled: true, Interval: 20 * time.Millisecond})
	waitFor(t, "a check on the new interval", func() bool { return f.calls.Load() >= 3 })
}

func TestARefusedCheckSaysWhenTheNextTryIs(t *testing.T) {
	c := NewChecker(&fakeFetcher{err: ErrRefused}, Settings{Enabled: true, Interval: 6 * time.Hour}, fixedNow, nil, nil)
	c.CheckOnce(context.Background())
	if got := c.Status().Problem; got != "GitHub refused the request. The coordinator tries again in 6 hours." {
		t.Fatalf("Problem = %q", got)
	}
	c.SetSettings(Settings{Enabled: true, Interval: 5 * time.Minute})
	if got := c.Status().Problem; got != "GitHub refused the request. The coordinator tries again in 5 minutes." {
		t.Fatalf("Problem after a new interval = %q", got)
	}
}

func TestGitHubFetcherReadsTheNewestRelease(t *testing.T) {
	var agent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agent = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`[
			{"tag_name":"v0.2.0","published_at":"2026-10-01T12:00:00Z","prerelease":true},
			{"tag_name":"v0.4.0","published_at":"2026-10-11T12:00:00Z","draft":true},
			{"tag_name":"v0.3.0","published_at":"2026-10-10T12:00:00Z","prerelease":true,"body":"notes"},
			{"tag_name":"nightly","published_at":"2026-10-12T12:00:00Z"},
			{"tag_name":"v0.1.0","published_at":"2026-09-01T12:00:00Z","prerelease":true}
		]`))
	}))
	defer srv.Close()
	f := &GitHubFetcher{Client: srv.Client(), URL: srv.URL, UserAgent: "showmesh-coordinator/0.2.0"}

	rel, err := f.LatestRelease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel.Tag != "v0.3.0" || !rel.PublishedAt.Equal(time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("release = %+v, want the prerelease v0.3.0", rel)
	}
	if agent != "showmesh-coordinator/0.2.0" {
		t.Fatalf("User-Agent = %q", agent)
	}
}

func TestNewGitHubFetcherListsReleasesSoPrereleasesAreSeen(t *testing.T) {
	want := "https://api.github.com/repos/ShowMeshSystems/showmesh/releases?per_page=10"
	if got := NewGitHubFetcher("ua").URL; got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
}

func TestGitHubFetcherTellsNoReleaseFromAFailure(t *testing.T) {
	status, body := http.StatusNotFound, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	f := &GitHubFetcher{Client: srv.Client(), URL: srv.URL}

	if _, err := f.LatestRelease(context.Background()); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("404: err = %v, want ErrNoRelease", err)
	}
	status, body = http.StatusOK, `[{"tag_name":"v0.4.0","draft":true}]`
	if _, err := f.LatestRelease(context.Background()); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("only a draft: err = %v, want ErrNoRelease", err)
	}
	for _, refused := range []int{http.StatusForbidden, http.StatusTooManyRequests} {
		status, body = refused, ""
		if _, err := f.LatestRelease(context.Background()); !errors.Is(err, ErrRefused) {
			t.Fatalf("%d: err = %v, want ErrRefused", refused, err)
		}
	}
	status = http.StatusBadGateway
	if _, err := f.LatestRelease(context.Background()); err == nil || errors.Is(err, ErrNoRelease) || errors.Is(err, ErrRefused) {
		t.Fatalf("502: err = %v, want a plain failure", err)
	}
}
