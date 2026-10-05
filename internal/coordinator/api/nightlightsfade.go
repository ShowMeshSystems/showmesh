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

// The night's own light fades write the FPP transition gain directly, never a
// cue's action. Every write is best effort: a failure is reported and never
// holds back the content that follows it.

const (
	nightLightsStepFadeOut     = "lights-out"
	nightLightsStepShowGain    = "lights-show"
	nightLightsStepDarkBefore  = "lights-in-dark"
	nightLightsStepFadeIn      = "lights-in"
	nightLightsGainTimeout     = 2 * time.Second
	nightLightsShowGainRetries = 2 * time.Minute

	nightEventCategoryLightsGainFailed = "night.lights.gain_failed"
)

// nightLightsGainLog stops a later tick repeating a write or its report. A
// restart loses it and repeats only writes the plugin treats as no-ops.
type nightLightsGainLog struct {
	mu       sync.Mutex
	done     map[string]bool
	failed   map[string]bool
	reported map[string]bool
}

func (l *nightLightsGainLog) hasFailed(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.failed[key] && !l.done[key]
}

func (l *nightLightsGainLog) markFailed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed == nil {
		l.failed = map[string]bool{}
	}
	l.failed[key] = true
}

func (l *nightLightsGainLog) isDone(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.done[key]
}

func (l *nightLightsGainLog) markDone(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done == nil {
		l.done = map[string]bool{}
	}
	l.done[key] = true
}

// markReported returns true only the first time key is reported.
func (l *nightLightsGainLog) markReported(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.reported == nil {
		l.reported = map[string]bool{}
	}
	if l.reported[key] {
		return false
	}
	l.reported[key] = true
	return true
}

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

// nightWriteLightsGain writes one gain step to every covered instance at once,
// bounded by nightLightsGainTimeout, and reports whether every write landed.
// A failed step is reported once.
func (h *handlers) nightWriteLightsGain(ctx context.Context, now time.Time, rec store.NightSessionRecord, payload config.NightSessionPayload, step string, targetPercent int, fadeSeconds int) bool {
	key := fmt.Sprintf("night:%s:%d:%s", rec.ID, rec.Cycle, step)
	if h.nightLightsGain.isDone(key) {
		return true
	}
	write := h.nightGainWriter
	if write == nil {
		write = h.writeNightTransitionGain
	}
	fade := nightLightingFade{TargetPercent: targetPercent, FadeSeconds: fadeSeconds}
	instances := nightLightsInstances(payload)

	wctx, cancel := context.WithTimeout(ctx, nightLightsGainTimeout)
	defer cancel()
	errs := make([]error, len(instances))
	var wg sync.WaitGroup
	for i, id := range instances {
		wg.Add(1)
		go func() {
			defer wg.Done()
			target := config.ShowActionTarget{Integration: config.ShowActionIntegrationFPP, InstanceID: id}
			if _, err := write(wctx, target, fade, key+":"+id); err != nil {
				errs[i] = err
			}
		}()
	}
	wg.Wait()

	allOK := true
	for i, err := range errs {
		if err == nil {
			continue
		}
		allOK = false
		h.nightReportLightsGainFailure(ctx, now, rec, key+":"+instances[i], instances[i], step, err)
	}
	if allOK {
		h.nightLightsGain.markDone(key)
	} else {
		h.nightLightsGain.markFailed(key)
	}
	return allOK
}

func nightLightsStepSummary(step, instanceID string) string {
	switch step {
	case nightLightsStepFadeOut:
		return fmt.Sprintf("The lights did not fade out on %s. The show still starts on time.", instanceID)
	case nightLightsStepShowGain:
		return fmt.Sprintf("The lights were not set to full brightness on %s. The show still started; set the brightness from the FPP screen.", instanceID)
	default:
		return fmt.Sprintf("The lights did not fade in on %s. Resting still started.", instanceID)
	}
}

func (h *handlers) nightReportLightsGainFailure(ctx context.Context, now time.Time, rec store.NightSessionRecord, reportKey, instanceID, step string, cause error) {
	if !h.nightLightsGain.markReported(reportKey) {
		return
	}
	h.logWarn("night loop: lights fade write failed", "sessionId", rec.ID, "instanceId", instanceID, "step", step, "error", cause)
	details, _ := json.Marshal(map[string]any{"sessionId": rec.ID, "cycle": rec.Cycle, "instanceId": instanceID, "step": step, "error": cause.Error()})
	ev := store.EventRecord{
		Source:   "night-loop",
		Resource: observation.ResourceRef{Kind: observation.ResourceFPP, ID: instanceID},
		Category: nightEventCategoryLightsGainFailed, Severity: "warning",
		Summary: nightLightsStepSummary(step, instanceID), Details: details, OccurredAt: &now,
	}
	err := h.deps.NightSessions.InTx(ctx, func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.AppendEvent(ctx, ev)
		return err
	})
	if err != nil {
		h.logWarn("night loop: failed to record a lights fade write failure", "sessionId", rec.ID, "error", err)
	}
}

// nightAdvanceLightsFadeOut starts the fade-out so it finishes as the resting
// sequence ends at boundaryE. The first show after pre-show has no such end.
func (h *handlers) nightAdvanceLightsFadeOut(ctx context.Context, now time.Time, rec store.NightSessionRecord, payload config.NightSessionPayload, boundaryE time.Time) {
	if payload.LightsFadeOutMs == nil || rec.ShowCommitted {
		return
	}
	if anchor, has := decodeNightContentAnchor(rec.ContentAnchorJSON); !has || anchor.Purpose != nightAnchorPurposeRestingOneShot {
		return
	}
	fadeOut := time.Duration(*payload.LightsFadeOutMs) * time.Millisecond
	if now.Before(boundaryE.Add(-fadeOut)) || !now.Before(boundaryE) {
		return
	}
	h.nightWriteLightsGain(ctx, now, rec, payload, nightLightsStepFadeOut, 0, nightLightsFadeSeconds(boundaryE.Sub(now)))
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

// nightLightsFadeOutRan reports whether this cycle's fade-out was written, so
// the gain may still be below 100 while the resting look plays.
func (h *handlers) nightLightsFadeOutRan(rec store.NightSessionRecord) bool {
	return h.nightLightsGain.isDone(fmt.Sprintf("night:%s:%d:%s", rec.ID, rec.Cycle, nightLightsStepFadeOut))
}

// nightRestoreShowGain sets the gain to 100 so the show plays at the ceiling.
// After a fade-out it waits for the resting playlist to stop, or for the start,
// so the resting look is never lit again.
func (h *handlers) nightRestoreShowGain(ctx context.Context, now time.Time, rec store.NightSessionRecord, payload config.NightSessionPayload, afterStart bool) {
	key := fmt.Sprintf("night:%s:%d:%s", rec.ID, rec.Cycle, nightLightsStepShowGain)
	if afterStart && (h.nightLightsGain.isDone(key) || h.nightLightsGain.hasFailed(key)) {
		return
	}
	if !afterStart && h.nightLightsFadeOutRan(rec) && !h.nightRestingStopped(ctx, now, payload) {
		return
	}
	h.nightWriteLightsGain(ctx, now, rec, payload, nightLightsStepShowGain, 100, 0)
}

// nightRetryShowGain repeats a failed show gain write while the show is live.
func (h *handlers) nightRetryShowGain(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	key := fmt.Sprintf("night:%s:%d:%s", rec.ID, rec.Cycle, nightLightsStepShowGain)
	if !h.nightLightsGain.hasFailed(key) || now.Sub(rec.StateEnteredAt) > nightLightsShowGainRetries {
		return
	}
	payload, err := h.getPinnedNightSessionPayload(ctx, rec)
	if err != nil {
		return
	}
	h.nightWriteLightsGain(ctx, now, rec, payload, nightLightsStepShowGain, 100, 0)
}

// nightLightsFadeInDark sets the gain to 0 before the resting playlist starts
// after a show, so the fade-in rises from dark.
func (h *handlers) nightLightsFadeInDark(ctx context.Context, now time.Time, rec store.NightSessionRecord, payload config.NightSessionPayload) {
	if payload.LightsFadeInMs == nil {
		return
	}
	h.nightWriteLightsGain(ctx, now, rec, payload, nightLightsStepDarkBefore, 0, 0)
}

// nightLightsFadeIn starts the fade-in once the resting playlist is confirmed
// playing.
func (h *handlers) nightLightsFadeIn(ctx context.Context, now time.Time, rec store.NightSessionRecord, payload config.NightSessionPayload) {
	if payload.LightsFadeInMs == nil {
		return
	}
	d := time.Duration(*payload.LightsFadeInMs) * time.Millisecond
	h.nightWriteLightsGain(ctx, now, rec, payload, nightLightsStepFadeIn, 100, nightLightsFadeSeconds(d))
}

// nightCheckLightsFades names each configured lights fade and the FPP
// instances it writes to. Degraded, like the cue fade check, because a
// fade's effect on a real display is not yet verified.
func nightCheckLightsFades(p config.NightSessionPayload) []nightReadinessCheck {
	var checks []nightReadinessCheck
	ids := nightLightsInstances(p)
	instances := "instance " + strings.Join(ids, ", ")
	if len(ids) > 1 {
		instances = "instances " + strings.Join(ids, ", ")
	}
	for _, f := range []struct {
		name, what string
		ms         *int
	}{
		{"lights:fade-out", "fade out before each show, finishing as the resting sequence ends", p.LightsFadeOutMs},
		{"lights:fade-in", "fade in after each show, starting when the resting playlist starts", p.LightsFadeInMs},
	} {
		if f.ms == nil {
			continue
		}
		checks = append(checks, nightReadinessCheck{name: f.name, health: nightHealthDegraded(), reason: fmt.Sprintf(
			"The lights %s over %d seconds, set on FPP %s. This has not been checked on a real display yet, so watch the first transition.",
			f.what, nightLightsFadeSeconds(time.Duration(*f.ms)*time.Millisecond), instances)})
	}
	return checks
}
