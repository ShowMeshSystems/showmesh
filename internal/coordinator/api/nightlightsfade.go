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

	// A write that brings the gain back to 100 keeps retrying at this slow
	// interval after the window, for as long as it is the newest write.
	nightLightsSlowRetry = 30 * time.Second

	// The reported gain is checked once a fade has had this long to finish,
	// and a correction is written at most this often per player.
	nightLightsReconcileMargin   = 3 * time.Second
	nightLightsReconcileInterval = 30 * time.Second

	nightLightsSignalTransitionGain = "fpp.brightness.transition_gain"
	nightLightsSignalFadeActive     = "fpp.brightness.fade_active"

	nightEventCategoryLightsGainFailed = "night.lights.gain_failed"
)

// nightLightsInstanceState is one FPP instance's progress through a step.
type nightLightsInstanceState struct {
	done        bool
	attempts    int
	attemptedAt time.Time
	doneAt      time.Time
	requestN    int
	immediate   bool
}

// nightLightsStep is one gain write across the covered instances, with what a
// later tick needs to retry it without re-reading the session.
type nightLightsStep struct {
	key        string
	seq        int
	rec        store.NightSessionRecord
	step       string
	target     int
	seconds    int
	endsAt     time.Time
	instances  []string
	oneShot    bool
	retryUntil time.Time
	cancelled  bool
	inst       map[string]*nightLightsInstanceState
}

// nightLightsSession is everything remembered about one night session.
type nightLightsSession struct {
	steps         map[string]*nightLightsStep
	fadeOut       map[int64]bool
	reported      map[string]bool
	busy          map[string]bool
	lastReconcile map[string]time.Time
	ended         bool
}

// nightLightsGainLog is the in-memory record of the night's gain writes. A
// restart loses it: the plugin treats a repeated request id as a no-op, but
// the memory of a fade in progress is gone.
type nightLightsGainLog struct {
	mu       sync.Mutex
	seq      int
	sessions map[string]*nightLightsSession
	wg       sync.WaitGroup
}

// session returns id's record, creating it. Callers hold l.mu.
func (l *nightLightsGainLog) session(id string) *nightLightsSession {
	if l.sessions == nil {
		l.sessions = map[string]*nightLightsSession{}
	}
	s := l.sessions[id]
	if s == nil {
		s = &nightLightsSession{
			steps: map[string]*nightLightsStep{}, fadeOut: map[int64]bool{}, reported: map[string]bool{},
			busy: map[string]bool{}, lastReconcile: map[string]time.Time{},
		}
		l.sessions[id] = s
	}
	return s
}

func nightLightsKey(rec store.NightSessionRecord, step string) string {
	return fmt.Sprintf("night:%s:%d:%s", rec.ID, rec.Cycle, step)
}

func (l *nightLightsGainLog) markFadeOut(rec store.NightSessionRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.session(rec.ID).fadeOut[rec.Cycle] = true
}

func (l *nightLightsGainLog) fadeOutRan(rec store.NightSessionRecord) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.session(rec.ID).fadeOut[rec.Cycle]
}

// stepDone reports whether every instance of the step has landed its write.
func (l *nightLightsGainLog) stepDone(rec store.NightSessionRecord, key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.session(rec.ID).steps[key]
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
// A new step supersedes every earlier step of the session: only the newest
// desired gain is ever written, and never while an older write to the same
// player is still in flight.
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
	sess := l.session(rec.ID)
	if _, exists := sess.steps[st.key]; exists {
		l.mu.Unlock()
		return
	}
	for _, other := range sess.steps {
		other.cancelled = true
	}
	l.seq++
	st.seq = l.seq
	sess.steps[st.key] = &st
	l.mu.Unlock()
	h.nightLightsLaunchDue(ctx, now, &st)
}

// nightLightsAdvance runs on every tick for the current session, whatever its
// state: it retries failed writes, corrects a gain the player reports below
// the desired value, and restores the gain once a night is over.
// While an emergency stop holds the night it writes nothing: a stop lights nothing.
func (h *handlers) nightLightsAdvance(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	l := &h.nightLightsGain
	l.mu.Lock()
	for id := range l.sessions {
		if id != rec.ID {
			delete(l.sessions, id)
		}
	}
	sess := l.sessions[rec.ID]
	if sess != nil && sess.ended {
		l.mu.Unlock()
		return
	}
	l.mu.Unlock()
	if nightStopHoldStands(rec) {
		return
	}

	if rec.State == nightStateStopped {
		h.nightLightsRestoreAtEnd(ctx, now, rec)
	}
	l.mu.Lock()
	sess = l.sessions[rec.ID]
	var due []*nightLightsStep
	if sess != nil {
		for _, st := range sess.steps {
			due = append(due, st)
		}
	}
	l.mu.Unlock()
	for _, st := range due {
		h.nightLightsLaunchDue(ctx, now, st)
	}
	h.nightLightsReconcile(ctx, now, rec)
}

func (h *handlers) nightLightsLaunchDue(ctx context.Context, now time.Time, st *nightLightsStep) {
	l := &h.nightLightsGain
	type launch struct {
		instance string
		seconds  int
		request  string
	}
	var launches []launch
	l.mu.Lock()
	sess := l.session(st.rec.ID)
	if !st.cancelled {
		for _, id := range st.instances {
			is := st.inst[id]
			lane := st.rec.ID + "|" + id
			if is.done || sess.busy[lane] {
				continue
			}
			late := now.After(st.retryUntil)
			if is.attempts > 0 {
				interval := nightLightsRetryBackoff
				switch {
				case st.oneShot:
					continue
				case !st.endsAt.IsZero() && !now.Before(st.endsAt):
					continue
				case late && st.target != 100:
					continue
				case late:
					interval = nightLightsSlowRetry
				}
				if now.Sub(is.attemptedAt) < interval {
					continue
				}
			}
			seconds := st.seconds
			switch {
			case !st.endsAt.IsZero():
				seconds = nightLightsFadeSeconds(st.endsAt.Sub(now))
			case is.immediate || (late && st.target == 100):
				seconds = 0
			}
			sess.busy[lane] = true
			is.attempts, is.attemptedAt = is.attempts+1, now
			request := st.key + ":" + id
			if is.requestN > 0 {
				request = fmt.Sprintf("%s:r%d", request, is.requestN)
			}
			launches = append(launches, launch{id, seconds, request})
		}
	}
	l.mu.Unlock()

	for _, ln := range launches {
		l.wg.Add(1)
		go h.nightLightsWrite(context.WithoutCancel(ctx), st, ln.instance, ln.seconds, ln.request)
	}
}

func (h *handlers) nightLightsWrite(ctx context.Context, st *nightLightsStep, instanceID string, seconds int, request string) {
	l := &h.nightLightsGain
	defer l.wg.Done()
	write := h.nightGainWriter
	if write == nil {
		write = h.writeNightTransitionGain
	}
	wctx, cancel := context.WithTimeout(ctx, nightLightsGainTimeout)
	defer cancel()
	target := config.ShowActionTarget{Integration: config.ShowActionIntegrationFPP, InstanceID: instanceID}
	_, err := write(wctx, target, nightLightingFade{TargetPercent: st.target, FadeSeconds: seconds}, request)

	l.mu.Lock()
	is := st.inst[instanceID]
	is.done = err == nil
	if err == nil {
		is.doneAt = h.clock()
	}
	delete(l.session(st.rec.ID).busy, st.rec.ID+"|"+instanceID)
	l.mu.Unlock()
	if err != nil {
		h.logWarn("night loop: lights fade write failed", "sessionId", st.rec.ID, "instanceId", instanceID, "step", st.step, "error", err)
		h.nightReportLights(ctx, st.rec, st.step, instanceID, err.Error())
	}
}

func nightLightsStepSummary(step, instanceID string) string {
	switch step {
	case nightLightsStepFadeOut:
		return fmt.Sprintf("The lights on %s did not fade out. Check the brightness on that player.", instanceID)
	case nightLightsStepDark:
		return fmt.Sprintf("The lights on %s did not dim before resting. Check the brightness on that player.", instanceID)
	case nightLightsStepReconcile:
		return fmt.Sprintf("The lights on %s were dimmer than expected, so they were set to full again. Check the brightness on that player if this repeats.", instanceID)
	default:
		return fmt.Sprintf("The lights on %s may be dark. Set the brightness on that player.", instanceID)
	}
}

const nightLightsStepReconcile = "lights-reconcile"

// nightReportLights records an event once per night session, player and
// message, so a harmless report never hides a later one that says the lights
// may be dark.
func (h *handlers) nightReportLights(ctx context.Context, rec store.NightSessionRecord, step, instanceID, cause string) {
	summary := nightLightsStepSummary(step, instanceID)
	l := &h.nightLightsGain
	l.mu.Lock()
	sess := l.session(rec.ID)
	already := sess.reported[instanceID+"|"+summary]
	sess.reported[instanceID+"|"+summary] = true
	l.mu.Unlock()
	if already {
		return
	}
	now := h.clock()
	details, _ := json.Marshal(map[string]any{"sessionId": rec.ID, "cycle": rec.Cycle, "instanceId": instanceID, "step": step, "detail": cause})
	ev := store.EventRecord{
		Source:   "night-loop",
		Resource: observation.ResourceRef{Kind: observation.ResourceFPP, ID: instanceID},
		Category: nightEventCategoryLightsGainFailed, Severity: "warning",
		Summary: summary, Details: details, OccurredAt: &now,
	}
	err := h.deps.NightSessions.InTx(ctx, func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.AppendEvent(ctx, ev)
		return err
	})
	if err != nil {
		h.logWarn("night loop: failed to record a lights fade event", "sessionId", rec.ID, "error", err)
	}
}

// nightLightsReconcile closes what the ordering guard cannot: a request the
// coordinator gave up on that the player applied later. When the newest write
// was meant to bring the gain to 100 and the player reports a lower gain once
// that fade should have finished, it writes 100 again, at most once per
// nightLightsReconcileInterval per player.
func (h *handlers) nightLightsReconcile(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	if h.deps.Observations == nil {
		return
	}
	l := &h.nightLightsGain
	type check struct {
		instance string
		after    time.Time
		st       *nightLightsStep
	}
	var checks []check
	l.mu.Lock()
	sess := l.session(rec.ID)
	var newest *nightLightsStep
	for _, st := range sess.steps {
		if newest == nil || st.seq > newest.seq {
			newest = st
		}
	}
	if newest != nil && newest.target == 100 {
		for _, id := range newest.instances {
			is := newest.inst[id]
			lane := rec.ID + "|" + id
			if !is.done || sess.busy[lane] || now.Sub(sess.lastReconcile[lane]) < nightLightsReconcileInterval {
				continue
			}
			after := is.doneAt.Add(time.Duration(newest.seconds)*time.Second + nightLightsReconcileMargin)
			if now.Before(after) {
				continue
			}
			checks = append(checks, check{id, after, newest})
		}
	}
	l.mu.Unlock()

	for _, c := range checks {
		gain, _, current, _, _ := resolveConfirmationEvidence(ctx, h.deps.Observations, c.instance, nightLightsSignalTransitionGain, c.after, now)
		if !current || nightAsInt64(gain) >= 100 {
			continue
		}
		if active, _, ok, _, _ := resolveConfirmationEvidence(ctx, h.deps.Observations, c.instance, nightLightsSignalFadeActive, c.after, now); ok {
			if running, _ := active.(bool); running {
				continue
			}
		}
		l.mu.Lock()
		is := c.st.inst[c.instance]
		sess.lastReconcile[rec.ID+"|"+c.instance] = now
		is.done, is.attempts, is.immediate = false, 0, true
		is.requestN++
		l.mu.Unlock()
		h.logWarn("night loop: lights read below full after a write to full; writing it again", "sessionId", rec.ID, "instanceId", c.instance, "reportedGain", nightAsInt64(gain))
		h.nightReportLights(ctx, rec, nightLightsStepReconcile, c.instance, fmt.Sprintf("reported gain %d", nightAsInt64(gain)))
		h.nightLightsLaunchDue(ctx, now, c.st)
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
	})
}

// nightLightsRestore writes the gain back to 100 at once, for the moments a
// fade was left half done: an abandoned transition, a degraded session, the
// end of a night and the next pre-show. It never runs on an emergency stop.
func (h *handlers) nightLightsRestore(ctx context.Context, now time.Time, rec store.NightSessionRecord, payload config.NightSessionPayload, why string) {
	h.nightLightsStart(ctx, now, rec, payload, nightLightsStep{step: nightLightsStepRestore + "-" + why, target: 100})
}

// nightLightsRestoreFor writes the gain back to 100 for any night, whatever
// configured its fade.
func (h *handlers) nightLightsRestoreFor(ctx context.Context, now time.Time, rec store.NightSessionRecord, why string) {
	payload, err := h.getPinnedNightSessionPayload(ctx, rec)
	if err != nil {
		return
	}
	h.nightLightsRestore(ctx, now, rec, payload, why)
}

// nightLightsRestoreAtEnd restores the gain once a night has ended, then drops
// the session's memory.
func (h *handlers) nightLightsRestoreAtEnd(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	key := nightLightsKey(rec, nightLightsStepRestore+"-end")
	if !h.nightLightsGain.stepDone(rec, key) {
		h.nightLightsRestoreFor(ctx, now, rec, "end")
		return
	}
	l := &h.nightLightsGain
	l.mu.Lock()
	defer l.mu.Unlock()
	sess := l.session(rec.ID)
	if sess.ended {
		return
	}
	*sess = nightLightsSession{
		steps: map[string]*nightLightsStep{}, fadeOut: map[int64]bool{}, reported: map[string]bool{},
		busy: map[string]bool{}, lastReconcile: map[string]time.Time{}, ended: true,
	}
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
