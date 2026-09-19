package api

import (
	"sync"
	"time"
)

// WeatherDelayGateCache is the shared, in-memory record of what
// [WeatherDelayEnforcer] most recently observed for each FPP instance's
// weather gate and each configured power group's own dark confirmation.
// It is written only by the enforcer's own tick and read by the GET
// /api/v1/weather-delay handler, so an ordinary API request never waits on
// a live HTTP read against an FPP host. One instance is shared between the
// enforcer's own private *handlers and the HTTP server's, exactly as
// [WeatherDelayStateKeeper] is shared, by construction in coordinator.go
// before either is built.
type WeatherDelayGateCache struct {
	mu         sync.Mutex
	gates      map[string]weatherDelayGateStatus
	groups     map[string]weatherDelayGroupStatus
	reopened   map[string]time.Time
	heartbeats map[string]time.Time
}

// weatherDelayGateStatus is one FPP instance's most recently read gate
// state. Supported is false when the plugin has no weather gate route
// (404): a caller must report this instance rather than treat it as dark.
type weatherDelayGateStatus struct {
	closed                 bool
	supported              bool
	effectiveOutputPercent int
	observedAt             time.Time
}

// weatherDelayGroupStatus is one power group's own last computed dark
// confirmation, kept so "since" survives across ticks and across whichever
// *handlers instance last computed it.
type weatherDelayGroupStatus struct {
	dark  bool
	since time.Time
}

// NewWeatherDelayGateCache builds an empty cache.
func NewWeatherDelayGateCache() *WeatherDelayGateCache {
	return &WeatherDelayGateCache{
		gates: map[string]weatherDelayGateStatus{}, groups: map[string]weatherDelayGroupStatus{},
		reopened: map[string]time.Time{}, heartbeats: map[string]time.Time{},
	}
}

// heartbeatDue reports whether groupID's dark heartbeat has never been
// published, or was published at least interval ago, and records now as
// the publish time. A group that stops being dark has its own record
// cleared by [WeatherDelayGateCache.recordGroupDark], so the heartbeat
// publishes immediately the next time it becomes dark again rather than
// waiting out the interval from a publish that happened before the gap.
func (c *WeatherDelayGateCache) heartbeatDue(groupID string, now time.Time, interval time.Duration) bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	last, ok := c.heartbeats[groupID]
	if ok && interval > 0 && now.Sub(last) < interval {
		return false
	}
	c.heartbeats[groupID] = now
	return true
}

func (c *WeatherDelayGateCache) setGate(instanceID string, st weatherDelayGateStatus) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gates[instanceID] = st
}

func (c *WeatherDelayGateCache) getGate(instanceID string) (weatherDelayGateStatus, bool) {
	if c == nil {
		return weatherDelayGateStatus{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.gates[instanceID]
	return st, ok
}

// reopenDue reports whether instanceID's stale-closed-gate reopen has
// never been attempted, or was attempted at least minInterval ago, and
// records now as the attempt time. Called only when a reopen is about to
// be sent, so a failed send still counts as an attempt: ADR-053 places no
// requirement on retrying it faster than the throttle.
func (c *WeatherDelayGateCache) reopenDue(instanceID string, now time.Time, minInterval time.Duration) bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	last, ok := c.reopened[instanceID]
	if ok && now.Sub(last) < minInterval {
		return false
	}
	c.reopened[instanceID] = now
	return true
}

// recordGroupDark updates groupID's cached dark state and returns the
// since timestamp: now on a flip from not-dark (or unknown) to dark, the
// already-recorded since while staying dark, and the zero time while not
// dark.
func (c *WeatherDelayGateCache) recordGroupDark(groupID string, dark bool, now time.Time) (since time.Time, changed bool) {
	if c == nil {
		if dark {
			return now, true
		}
		return time.Time{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	prev, existed := c.groups[groupID]
	changed = !existed || prev.dark != dark
	if !dark {
		c.groups[groupID] = weatherDelayGroupStatus{dark: false}
		delete(c.heartbeats, groupID)
		return time.Time{}, changed
	}
	since = prev.since
	if !existed || !prev.dark || since.IsZero() {
		since = now
	}
	c.groups[groupID] = weatherDelayGroupStatus{dark: true, since: since}
	return since, changed
}

// groupDark reads groupID's cached state without recording anything, for
// the GET handler's own read path when the enforcer has not ticked yet.
func (c *WeatherDelayGateCache) groupDark(groupID string) (weatherDelayGroupStatus, bool) {
	if c == nil {
		return weatherDelayGroupStatus{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.groups[groupID]
	return st, ok
}
