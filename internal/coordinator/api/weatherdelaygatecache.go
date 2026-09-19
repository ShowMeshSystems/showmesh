package api

import (
	"sync"
	"time"
)

// WeatherDelayGateCache is the enforcer's in-memory record of each player's
// gate and each power group's darkness, read by GET /api/v1/weather-delay so
// a request never waits on an FPP host. coordinator.go shares one instance.
type WeatherDelayGateCache struct {
	mu         sync.Mutex
	gates      map[string]weatherDelayGateStatus
	groups     map[string]weatherDelayGroupStatus
	idleReads  map[string]time.Time
	heartbeats map[string]time.Time
	gateWrites map[string]*sync.Mutex
}

// weatherDelayGateStatus is one FPP instance's most recently read gate
// state. Supported is false when the plugin has no weather gate route
// (404): a caller must report this instance rather than treat it as dark.
type weatherDelayGateStatus struct {
	closed                 bool
	supported              bool
	unreadable             bool
	effectiveOutputPercent int
	observedAt             time.Time
	freshFor               time.Duration
}

// freshWindow is how old this reading may be and still count: the idle
// window for a reading taken while no delay was active, else the short one.
func (st weatherDelayGateStatus) freshWindow() time.Duration {
	if st.freshFor > 0 {
		return st.freshFor
	}
	return weatherDelayGateFreshnessWindow
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
		idleReads: map[string]time.Time{}, heartbeats: map[string]time.Time{},
		gateWrites: map[string]*sync.Mutex{},
	}
}

// lockGateWrite serializes gate writes to one player. The enforcer checks
// the state again under it, so its write never lands after a start's
// close or a resume's open that the check did not see.
func (c *WeatherDelayGateCache) lockGateWrite(instanceID string) (unlock func()) {
	if c == nil {
		return func() {}
	}
	c.mu.Lock()
	m, ok := c.gateWrites[instanceID]
	if !ok {
		m = &sync.Mutex{}
		c.gateWrites[instanceID] = m
	}
	c.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// heartbeatDue reports whether groupID's heartbeat is due and records now.
// recordGroupDark clears the record when a group stops being dark, so it
// publishes at once when the group is dark again.
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

// idleReadDue reports whether instanceID's idle gate read is due and
// records now as the attempt, so a failed read still waits out the interval.
func (c *WeatherDelayGateCache) idleReadDue(instanceID string, now time.Time, minInterval time.Duration) bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	last, ok := c.idleReads[instanceID]
	if ok && now.Sub(last) < minInterval {
		return false
	}
	c.idleReads[instanceID] = now
	return true
}

// recordGroupDark stores groupID's darkness and returns since: now on a
// flip to dark, the kept value while dark, zero while not dark.
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
