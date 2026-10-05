package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// The night's own light fades write the FPP transition gain directly. A write
// runs off the tick and the tick never waits for it, so a slow player cannot
// delay a launch. The gain must always have a live path back to 100.

const (
	nightLightsStepFadeOut  = "lights-out"
	nightLightsStepShowGain = "lights-show"
	nightLightsStepDark     = "lights-in-dark"
	nightLightsStepFadeIn   = "lights-in"
	nightLightsStepRestore  = "lights-restore"

	nightLightsGainTimeout  = 2 * time.Second
	nightLightsRetryBackoff = 5 * time.Second
	nightLightsRetryWindow  = 2 * time.Minute

	nightEventCategoryLightsGainFailed = "night.lights.gain_failed"
)

// nightLightsInstanceState is one FPP instance's progress through a step.
type nightLightsInstanceState struct {
	inflight    bool
	done        bool
	attempts    int
	attemptedAt time.Time
}

// nightLightsStep is one gain write across the covered instances, with what a
// later tick needs to retry it without re-reading the session.
type nightLightsStep struct {
	key        string
	rec        store.NightSessionRecord
	step       string
	target     int
	seconds    int
	endsAt     time.Time
	instances  []string
	after      string
	oneShot    bool
	retryUntil time.Time
	cancelled  bool
	inst       map[string]*nightLightsInstanceState
}

// nightLightsGainLog is the in-memory record of the night's gain writes. A
// restart loses it: the plugin treats a repeated request id as a no-op, but
// the memory of a fade in progress is gone.
type nightLightsGainLog struct {
	mu       sync.Mutex
	steps    map[string]*nightLightsStep
	fadeOut  map[string]bool
	reported map[string]bool
	wg       sync.WaitGroup
}

func nightLightsKey(rec store.NightSessionRecord, step string) string {
	return fmt.Sprintf("night:%s:%d:%s", rec.ID, rec.Cycle, step)
}

func (l *nightLightsGainLog) markFadeOut(rec store.NightSessionRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fadeOut == nil {
		l.fadeOut = map[string]bool{}
	}
	l.fadeOut[fmt.Sprintf("%s:%d", rec.ID, rec.Cycle)] = true
}

func (l *nightLightsGainLog) fadeOutRan(rec store.NightSessionRecord) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fadeOut[fmt.Sprintf("%s:%d", rec.ID, rec.Cycle)]
}

// stepDone reports whether every instance of the step has landed its write.
func (l *nightLightsGainLog) stepDone(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.steps[key]
	if st == nil {
		return false
	}
	for _, id := range st.instances {
		if !st.inst[id].done {
			return false
		}
	}
	return true
}

// wait blocks until every started write has returned. Only tests call it.
func (l *nightLightsGainLog) wait() { l.wg.Wait() }

// nightLightsInstances lists the FPP instances the night's own fades write
// to: the resting instance, then the show instance when it differs.
func nightLightsInstances(p config.NightSessionPayload) []string {
	var out []string
	for _, id := range []string{p.Resting.FPPInstanceID, p.ShowPlaylist.FPPInstanceID} {
		if id == "" {
			continue
		}
		dup := false
		for _, have := range out {
			dup = dup || have == id
		}
		if !dup {
			out = append(out, id)
		}
	}
	return out
}

func nightLightsConfigured(p config.NightSessionPayload) bool {
	return p.LightsFadeOutMs != nil || p.LightsFadeInMs != nil
}

func nightLightsFadeSeconds(d time.Duration) int {
	return int((d + 500*time.Millisecond) / time.Second)
}

// nightTransitionLeadMs is how long before the resting sequence ends the
// transition into a show begins: the longest cue lead or lights fade-out.
func nightTransitionLeadMs(p config.NightSessionPayload) int64 {
	lead := nightEnterShowLeadMs(p.EnterShow.Cues)
	if p.LightsFadeOutMs != nil && int64(*p.LightsFadeOutMs) > lead {
		lead = int64(*p.LightsFadeOutMs)
	}
	return lead
}

// nightLightsStart registers a step and starts its writes without waiting for
// them. A step already registered under the same key is left to its retries.
// A new step supersedes every earlier step of the session: its retries stop.
func (h *handlers) nightLightsStart(ctx context.Context, now time.Time, rec store.NightSessionRecord, payload config.NightSessionPayload, st nightLightsStep) {
	st.key = nightLightsKey(rec, st.step)
	st.rec = rec
	st.instances = nightLightsInstances(payload)
	st.inst = map[string]*nightLightsInstanceState{}
	for _, id := range st.instances {
		st.inst[id] = &nightLightsInstanceState{}
	}
	if st.retryUntil.IsZero() && !st.oneShot {
		st.retryUntil = now.Add(nightLightsRetryWindow)
	}

	l := &h.nightLightsGain
	l.mu.Lock()
	if l.steps == nil {
		l.steps = map[string]*nightLightsStep{}
	}
	if _, exists := l.steps[st.key]; exists {
		l.mu.Unlock()
		return
	}
	for _, other := range l.steps {
		if other.rec.ID == rec.ID {
			other.cancelled = true
		}
	}
	l.steps[st.key] = &st
	l.mu.Unlock()
	h.nightLightsLaunchDue(ctx, now, &st)
}

// nightLightsAdvance runs on every tick for the current session, whatever its
// state: it retries failed writes and restores the gain once a night is over.
// While an emergency stop holds the night it writes nothing: a stop lights nothing.
func (h *handlers) nightLightsAdvance(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	if nightStopHoldStands(rec) {
		return
	}
	l := &h.nightLightsGain
	l.mu.Lock()
	var due []*nightLightsStep
	for _, st := range l.steps {
		if st.rec.ID == rec.ID {
			due = append(due, st)
		}
	}
	l.mu.Unlock()
	for _, st := range due {
		h.nightLightsLaunchDue(ctx, now, st)
	}
	if rec.State == nightStateStopped {
		h.nightLightsRestoreAtEnd(ctx, now, rec)
	}
}

func (h *handlers) nightLightsLaunchDue(ctx context.Context, now time.Time, st *nightLightsStep) {
	l := &h.nightLightsGain
	type launch struct {
		instance string
		seconds  int
	}
	var launches []launch
	l.mu.Lock()
	if !st.cancelled {
		for _, id := range st.instances {
			is := st.inst[id]
			if is.done || is.inflight {
				continue
			}
			if is.attempts > 0 {
				if st.oneShot || now.Sub(is.attemptedAt) < nightLightsRetryBackoff || now.After(st.retryUntil) {
					continue
				}
				if !st.endsAt.IsZero() && !now.Before(st.endsAt) {
					continue
				}
			}
			if st.after != "" {
				if prev := l.steps[st.after]; prev != nil {
					ps := prev.inst[id]
					if ps != nil && !ps.done && !(prev.oneShot && ps.attempts > 0 && !ps.inflight) {
						continue
					}
				}
			}
			seconds := st.seconds
			if !st.endsAt.IsZero() {
				seconds = nightLightsFadeSeconds(st.endsAt.Sub(now))
			}
			is.inflight, is.attempts, is.attemptedAt = true, is.attempts+1, now
			launches = append(launches, launch{id, seconds})
		}
	}
	l.mu.Unlock()

	for _, ln := range launches {
		l.wg.Add(1)
		go h.nightLightsWrite(context.WithoutCancel(ctx), st, ln.instance, ln.seconds)
	}
}

func (h *handlers) nightLightsWrite(ctx context.Context, st *nightLightsStep, instanceID string, seconds int) {
	l := &h.nightLightsGain
	defer l.wg.Done()
	write := h.nightGainWriter
	if write == nil {
		write = h.writeNightTransitionGain
	}
	wctx, cancel := context.WithTimeout(ctx, nightLightsGainTimeout)
	defer cancel()
	target := config.ShowActionTarget{Integration: config.ShowActionIntegrationFPP, InstanceID: instanceID}
	_, err := write(wctx, target, nightLightingFade{TargetPercent: st.target, FadeSeconds: seconds}, st.key+":"+instanceID)

	l.mu.Lock()
	is := st.inst[instanceID]
	is.inflight = false
	is.done = err == nil
	l.mu.Unlock()
	if err != nil {
		h.nightReportLightsGainFailure(ctx, st, instanceID, err)
	}
}

func nightLightsStepSummary(step, instanceID string) string {
	switch step {
	case nightLightsStepFadeOut:
		return fmt.Sprintf("The lights on %s did not fade out. Check the brightness on that player.", instanceID)
	case nightLightsStepDark:
		return fmt.Sprintf("The lights on %s did not dim before resting. Check the brightness on that player.", instanceID)
	default:
		return fmt.Sprintf("The lights on %s may be dark. Set the brightness on that player.", instanceID)
	}
}

// nightReportLightsGainFailure records a failed write once per night session
// and player, so a player without the plugin does not warn on every show.
func (h *handlers) nightReportLightsGainFailure(ctx context.Context, st *nightLightsStep, instanceID string, cause error) {
	l := &h.nightLightsGain
	reportKey := st.rec.ID + ":" + instanceID
	l.mu.Lock()
	if l.reported == nil {
		l.reported = map[string]bool{}
	}
	already := l.reported[reportKey]
	l.reported[reportKey] = true
	l.mu.Unlock()
	h.logWarn("night loop: lights fade write failed", "sessionId", st.rec.ID, "instanceId", instanceID, "step", st.step, "error", cause)
	if already {
		return
	}
	now := h.clock()
	details, _ := json.Marshal(map[string]any{"sessionId": st.rec.ID, "cycle": st.rec.Cycle, "instanceId": instanceID, "step": st.step, "error": cause.Error()})
	ev := store.EventRecord{
		Source:   "night-loop",
		Resource: observation.ResourceRef{Kind: observation.ResourceFPP, ID: instanceID},
		Category: nightEventCategoryLightsGainFailed, Severity: "warning",
		Summary: nightLightsStepSummary(st.step, instanceID), Details: details, OccurredAt: &now,
	}
	err := h.deps.NightSessions.InTx(ctx, func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.AppendEvent(ctx, ev)
		return err
	})
	if err != nil {
		h.logWarn("night loop: failed to record a lights fade write failure", "sessionId", st.rec.ID, "error", err)
	}
}

// nightAdvanceLightsFadeOut starts the fade-out so it finishes as the resting
// sequence ends at boundaryE. The first show after pre-show has no such end.
func (h *handlers) nightAdvanceLightsFadeOut(ctx context.Context, now time.Time, rec store.NightSessionRecord, payload config.NightSessionPayload, boundaryE time.Time) {
	if payload.LightsFadeOutMs == nil {
		return
	}
	if anchor, has := decodeNightContentAnchor(rec.ContentAnchorJSON); !has || anchor.Purpose != nightAnchorPurposeRestingOneShot {
		return
	}
	fadeOut := time.Duration(*payload.LightsFadeOutMs) * time.Millisecond
	if now.Before(boundaryE.Add(-fadeOut)) || !now.Before(boundaryE) {
		return
	}
	h.nightLightsGain.markFadeOut(rec)
	h.nightLightsStart(ctx, now, rec, payload, nightLightsStep{step: nightLightsStepFadeOut, target: 0, endsAt: boundaryE})
}

// nightRestingStopped reports whether current evidence shows the resting
// playlist is no longer playing.
func (h *handlers) nightRestingStopped(ctx context.Context, now time.Time, payload config.NightSessionPayload) bool {
	if payload.Resting.FPPInstanceID == "" {
		return true
	}
	obs := nightObservePlayback(ctx, h.deps.Observations, payload.Resting.FPPInstanceID, time.Time{}, now)
	if !obs.Current {
		return false
	}
	return obs.Status == fppStatusValueIdle || (obs.PlaylistCurrent && obs.Playlist != payload.Resting.Playlist)
}

// nightRestoreShowGain sets the gain to 100 so the show plays at the ceiling.
// After any fade-out it waits for the resting playlist to be seen stopped, or
// for the start, so the resting look is never lit again.
func (h *handlers) nightRestoreShowGain(ctx context.Context, now time.Time, rec store.NightSessionRecord, payload config.NightSessionPayload, afterStart bool) {
	if !afterStart && h.nightLightsGain.fadeOutRan(rec) && !h.nightRestingStopped(ctx, now, payload) {
		return
	}
	h.nightLightsStart(ctx, now, rec, payload, nightLightsStep{step: nightLightsStepShowGain, target: 100})
}

// nightLightsFadeInDark sets the gain to 0 before the resting playlist starts
// after a show, so the fade-in rises from dark. It is tried once.
func (h *handlers) nightLightsFadeInDark(ctx context.Context, now time.Time, rec store.NightSessionRecord, payload config.NightSessionPayload) {
	if payload.LightsFadeInMs == nil {
		return
	}
	h.nightLightsStart(ctx, now, rec, payload, nightLightsStep{step: nightLightsStepDark, target: 0, oneShot: true})
}

// nightLightsFadeIn starts the fade-in once the resting start is dispatched,
// after the dim to 0 has settled, and retries until it lands.
func (h *handlers) nightLightsFadeIn(ctx context.Context, now time.Time, rec store.NightSessionRecord, payload config.NightSessionPayload) {
	if payload.LightsFadeInMs == nil {
		return
	}
	d := time.Duration(*payload.LightsFadeInMs) * time.Millisecond
	h.nightLightsStart(ctx, now, rec, payload, nightLightsStep{
		step: nightLightsStepFadeIn, target: 100, seconds: nightLightsFadeSeconds(d),
		after: nightLightsKey(rec, nightLightsStepDark),
	})
}

// nightLightsRestore writes the gain back to 100 at once, for the moments a
// fade was left half done: an abandoned transition, a degraded session, the
// end of a night and the next pre-show. It never runs on an emergency stop.
func (h *handlers) nightLightsRestore(ctx context.Context, now time.Time, rec store.NightSessionRecord, payload config.NightSessionPayload, why string) {
	h.nightLightsStart(ctx, now, rec, payload, nightLightsStep{step: nightLightsStepRestore + "-" + why, target: 100})
}

func (h *handlers) nightLightsRestoreIfFading(ctx context.Context, now time.Time, rec store.NightSessionRecord, why string) {
	payload, err := h.getPinnedNightSessionPayload(ctx, rec)
	if err != nil || !nightLightsConfigured(payload) {
		return
	}
	h.nightLightsRestore(ctx, now, rec, payload, why)
}

func (h *handlers) nightLightsRestoreAtEnd(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	if h.nightLightsGain.stepDone(nightLightsKey(rec, nightLightsStepRestore+"-end")) {
		return
	}
	h.nightLightsRestoreIfFading(ctx, now, rec, "end")
}

// nightCheckLightsFades names each configured lights fade and the FPP
// instances it writes to, and warns when the fades cannot both fit in the
// resting sequence. Degraded, never failed: a fade's effect on a real display
// is not yet verified, and a warning must not stop a night from starting.
func (h *handlers) nightCheckLightsFades(ctx context.Context, p config.NightSessionPayload) []nightReadinessCheck {
	var checks []nightReadinessCheck
	ids := nightLightsInstances(p)
	instances := "instance " + strings.Join(ids, ", ")
	if len(ids) > 1 {
		instances = "instances " + strings.Join(ids, ", ")
	}
	const unverified = "This has not been checked on a real display yet, so watch the first transition."
	if p.LightsFadeOutMs != nil {
		checks = append(checks, nightReadinessCheck{name: "lights:fade-out", health: nightHealthDegraded(), reason: fmt.Sprintf(
			"The lights fade out over %d seconds, finishing as the resting sequence ends before every show after the first, on FPP %s. %s",
			nightLightsFadeSeconds(time.Duration(*p.LightsFadeOutMs)*time.Millisecond), instances, unverified)})
	}
	if p.LightsFadeInMs != nil {
		checks = append(checks, nightReadinessCheck{name: "lights:fade-in", health: nightHealthDegraded(), reason: fmt.Sprintf(
			"The lights fade in over %d seconds, starting when resting starts after each show, on FPP %s. %s",
			nightLightsFadeSeconds(time.Duration(*p.LightsFadeInMs)*time.Millisecond), instances, unverified)})
	}
	if p.LightsFadeOutMs == nil && p.LightsFadeInMs == nil {
		return checks
	}
	res := nightResolveFSEQDuration(ctx, h.deps, h.deps.Assets, p.Show, p.Resting.TimelineAsset)
	if res.Reason != "" {
		return checks
	}
	total := 0
	for _, ms := range []*int{p.LightsFadeOutMs, p.LightsFadeInMs} {
		if ms != nil {
			total += *ms
		}
	}
	if int64(total) > res.DurationMS {
		checks = append(checks, nightReadinessCheck{name: "lights:fade-length", health: nightHealthDegraded(), reason: fmt.Sprintf(
			"The lights fade for %d seconds in total but the resting sequence runs %d seconds, so the fades overlap. Shorten a fade or use a longer resting sequence.",
			nightLightsFadeSeconds(time.Duration(total)*time.Millisecond), nightLightsFadeSeconds(time.Duration(res.DurationMS)*time.Millisecond))})
	}
	return checks
}
