package versions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// DefaultCheckInterval is how often the coordinator asks GitHub for the newest release.
const DefaultCheckInterval = 6 * time.Hour

// SourceID is the collector source id on every release check observation.
const SourceID = "coordinator-release"

const (
	SignalNewestTag observation.SignalID = "coordinator.release.newest_tag"
	SignalCheckedAt observation.SignalID = "coordinator.release.checked_at"
	SignalAvailable observation.SignalID = "coordinator.release.available"
)

// Release is one published release.
type Release struct {
	Tag         string
	PublishedAt time.Time
}

// Fetcher asks the release host for the newest published release.
type Fetcher interface {
	LatestRelease(ctx context.Context) (Release, error)
}

// ErrNoRelease means the repository answered and has no published release.
var ErrNoRelease = errors.New("versions: no published release")

// ErrRefused means the release host answered and refused the request, as GitHub does past its rate limit.
var ErrRefused = errors.New("versions: release host refused the request")

// Settings is whether and how often the check runs.
type Settings struct {
	Enabled  bool
	Interval time.Duration
}

// Status is the cached release check answer a request reads.
type Status struct {
	Enabled  bool
	Interval time.Duration
	// Release and CheckedAt are the last answer and when it was fetched; nil and zero before the first one.
	Release   *Release
	CheckedAt time.Time
	// Problem is why the latest check produced no answer, empty when it did.
	Problem string
}

// Checker runs the release check on a timer and keeps the last answer.
type Checker struct {
	fetch   Fetcher
	now     func() time.Time
	logger  *slog.Logger
	publish func(Status)
	changed chan struct{}

	mu      sync.Mutex
	status  Status
	lastErr error
}

// NewChecker builds a Checker. A nil fetch turns the check off whatever the settings say.
// publish, when set, receives the status after every check.
func NewChecker(fetch Fetcher, settings Settings, now func() time.Time, logger *slog.Logger, publish func(Status)) *Checker {
	c := &Checker{fetch: fetch, now: now, logger: logger, publish: publish, changed: make(chan struct{}, 1)}
	c.status.Enabled, c.status.Interval = c.effective(settings)
	return c
}

func (c *Checker) effective(s Settings) (bool, time.Duration) {
	if s.Interval <= 0 {
		s.Interval = DefaultCheckInterval
	}
	return s.Enabled && c.fetch != nil, s.Interval
}

// SetSettings applies new settings to the running checker: the timer restarts
// on the new interval, and a check that was off runs as soon as it is turned on.
func (c *Checker) SetSettings(s Settings) {
	c.mu.Lock()
	c.status.Enabled, c.status.Interval = c.effective(s)
	if c.lastErr != nil {
		c.status.Problem = problemText(c.lastErr, c.status.Interval)
	}
	c.mu.Unlock()
	select {
	case c.changed <- struct{}{}:
	default:
	}
}

// Status returns the cached answer. It never waits for a check in progress.
func (c *Checker) Status() Status {
	if c == nil {
		return Status{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Run checks when the check is on, then every interval, until ctx ends.
// It follows SetSettings without a restart.
func (c *Checker) Run(ctx context.Context) {
	wasEnabled := false
	for {
		s := c.Status()
		switch {
		case s.Enabled && !wasEnabled:
			c.CheckOnce(ctx)
		case !s.Enabled && c.publish != nil:
			c.publish(s)
		}
		wasEnabled = s.Enabled

		var tick <-chan time.Time
		var timer *time.Timer
		if s.Enabled {
			timer = time.NewTimer(s.Interval)
			tick = timer.C
		}
		select {
		case <-ctx.Done():
		case <-tick:
			c.CheckOnce(ctx)
		case <-c.changed:
		}
		if timer != nil {
			timer.Stop()
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// CheckOnce performs one check and stores its result. The fetch runs outside the lock.
func (c *Checker) CheckOnce(ctx context.Context) {
	rel, err := c.fetch.LatestRelease(ctx)
	if ctx.Err() != nil {
		return
	}
	c.mu.Lock()
	c.lastErr = err
	if err != nil {
		c.status.Problem = problemText(err, c.status.Interval)
	} else {
		c.status.Release, c.status.CheckedAt, c.status.Problem = &rel, c.now(), ""
	}
	status := c.status
	c.mu.Unlock()
	if err != nil && c.logger != nil {
		c.logger.Warn("coordinator: release check failed", "error", err)
	}
	if c.publish != nil {
		c.publish(status)
	}
}

func problemText(err error, interval time.Duration) string {
	switch {
	case errors.Is(err, ErrNoRelease):
		return "GitHub lists no published ShowMesh release yet."
	case errors.Is(err, ErrRefused):
		return "GitHub refused the request. The coordinator tries again in " + intervalText(interval) + "."
	}
	return "GitHub could not be reached. The coordinator will try again."
}

// intervalText words an interval as whole hours when it is one, and minutes otherwise.
func intervalText(d time.Duration) string {
	unit, n := "minute", int(d.Round(time.Minute)/time.Minute)
	if d >= time.Hour && d%time.Hour == 0 {
		unit, n = "hour", int(d/time.Hour)
	}
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// Observations renders a status as the three coordinator.release.* signals.
func Observations(s Status, current string, now time.Time) []observation.Observation {
	res := observation.ResourceRef{Kind: observation.ResourceCoordinator, ID: "release"}
	opts := []observation.Option{observation.WithSource(SourceID), observation.WithCollectedAt(now)}
	signals := []observation.SignalID{SignalNewestTag, SignalCheckedAt, SignalAvailable}
	out := make([]observation.Observation, 0, len(signals))
	if s.Release == nil {
		for _, sig := range signals {
			var o observation.Observation
			var err error
			switch {
			case !s.Enabled:
				o, err = observation.NotCollected(res, sig, "the release check is turned off", opts...)
			case s.Problem != "":
				o, err = observation.CollectionFailed(res, sig, s.Problem, opts...)
			default:
				o, err = observation.NotCollected(res, sig, "the first release check has not finished", opts...)
			}
			if err == nil {
				out = append(out, o)
			}
		}
		return out
	}
	opts = append(opts, observation.WithQuality(observation.QualityDirect))
	values := []any{s.Release.Tag, s.CheckedAt.UTC().Format(time.RFC3339), Newer(s.Release.Tag, current)}
	for i, sig := range signals {
		if o, err := observation.Measured(res, sig, values[i], s.CheckedAt, opts...); err == nil {
			out = append(out, o)
		}
	}
	return out
}

// GitHubFetcher reads the newest release from the GitHub REST API.
type GitHubFetcher struct {
	Client    *http.Client
	URL       string
	UserAgent string
}

// NewGitHubFetcher builds the fetcher the coordinator runs with. It lists
// releases because GitHub's "latest" route leaves out every prerelease.
func NewGitHubFetcher(userAgent string) *GitHubFetcher {
	return &GitHubFetcher{
		Client:    &http.Client{Timeout: 15 * time.Second},
		URL:       "https://api.github.com/repos/" + Repository + "/releases?per_page=10",
		UserAgent: userAgent,
	}
}

// LatestRelease returns the listed release with the highest version, prereleases included and drafts left out.
func (f *GitHubFetcher) LatestRelease(ctx context.Context) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", f.UserAgent)
	resp, err := f.Client.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return Release{}, ErrNoRelease
	case http.StatusForbidden, http.StatusTooManyRequests:
		return Release{}, fmt.Errorf("%w: HTTP %d", ErrRefused, resp.StatusCode)
	default:
		return Release{}, fmt.Errorf("versions: release host answered HTTP %d", resp.StatusCode)
	}
	var body []struct {
		TagName     string    `json:"tag_name"`
		PublishedAt time.Time `json:"published_at"`
		Draft       bool      `json:"draft"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("versions: decode releases: %w", err)
	}
	var newest Release
	var newestVersion semver
	for _, r := range body {
		v, ok := parse(r.TagName)
		if r.Draft || !ok {
			continue
		}
		if newest.Tag == "" || compare(v, newestVersion) > 0 {
			newest, newestVersion = Release{Tag: r.TagName, PublishedAt: r.PublishedAt}, v
		}
	}
	if newest.Tag == "" {
		return Release{}, ErrNoRelease
	}
	return newest, nil
}
