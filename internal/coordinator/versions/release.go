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
	fetch    Fetcher
	interval time.Duration
	now      func() time.Time
	logger   *slog.Logger
	publish  func(Status)

	mu     sync.Mutex
	status Status
}

// NewChecker builds a Checker. A nil fetch or enabled false turns the check off.
// publish, when set, receives the status after every check.
func NewChecker(fetch Fetcher, enabled bool, interval time.Duration, now func() time.Time, logger *slog.Logger, publish func(Status)) *Checker {
	if interval <= 0 {
		interval = DefaultCheckInterval
	}
	return &Checker{
		fetch: fetch, interval: interval, now: now, logger: logger, publish: publish,
		status: Status{Enabled: enabled && fetch != nil, Interval: interval},
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

// Run checks once at start and then every interval until ctx ends.
func (c *Checker) Run(ctx context.Context) {
	if !c.Status().Enabled {
		if c.publish != nil {
			c.publish(c.Status())
		}
		return
	}
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		c.CheckOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
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
	if err != nil {
		c.status.Problem = problemText(err)
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

func problemText(err error) string {
	if errors.Is(err, ErrNoRelease) {
		return "GitHub lists no published ShowMesh release yet."
	}
	return "GitHub could not be reached. The coordinator will try again."
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

// NewGitHubFetcher builds the fetcher the coordinator runs with.
func NewGitHubFetcher(userAgent string) *GitHubFetcher {
	return &GitHubFetcher{
		Client:    &http.Client{Timeout: 15 * time.Second},
		URL:       "https://api.github.com/repos/" + Repository + "/releases/latest",
		UserAgent: userAgent,
	}
}

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
	if resp.StatusCode == http.StatusNotFound {
		return Release{}, ErrNoRelease
	}
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("versions: release host answered HTTP %d", resp.StatusCode)
	}
	var body struct {
		TagName     string    `json:"tag_name"`
		PublishedAt time.Time `json:"published_at"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("versions: decode release: %w", err)
	}
	if body.TagName == "" {
		return Release{}, ErrNoRelease
	}
	return Release{Tag: body.TagName, PublishedAt: body.PublishedAt}, nil
}
