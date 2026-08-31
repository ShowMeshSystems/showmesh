package api

import (
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// force=true is what lets emergency-stop level 2 force the
// existing graceful-shutdown sequence to start immediately even while a
// show is live, instead of deferring until the current show finishes the
// way an ordinary fade-out-night/power-down-presentation always does
// (RESTING-MODE.md §7.1.1). force=false must reproduce every existing
// caller's behavior unchanged.
func TestApplyNightShutdownEffectForceBypassesLiveDeferral(t *testing.T) {
	now := time.Now()
	rec := store.NightSessionRecord{ID: "night-1", State: nightStateLive}

	next, changed := applyNightShutdownEffect(now, rec, "power-down", true)
	if !changed {
		t.Fatal("applyNightShutdownEffect(force=true) on a live session reported no change")
	}
	if next.State != nightStateFadingOut {
		t.Fatalf("applyNightShutdownEffect(force=true) on a live session left state %q, want %q", next.State, nightStateFadingOut)
	}
	if next.FinalShowRequested {
		t.Fatal("applyNightShutdownEffect(force=true) set FinalShowRequested, which is the DEFERRING path's own effect")
	}
}

func TestApplyNightShutdownEffectWithoutForceStillDefersLiveShow(t *testing.T) {
	now := time.Now()
	rec := store.NightSessionRecord{ID: "night-1", State: nightStateLive}

	next, changed := applyNightShutdownEffect(now, rec, "power-down", false)
	if !changed {
		t.Fatal("applyNightShutdownEffect(force=false) on a live session reported no change")
	}
	if next.State != nightStateLive {
		t.Fatalf("applyNightShutdownEffect(force=false) on a live session changed state to %q, want it to stay %q (deferred)", next.State, nightStateLive)
	}
	if !next.FinalShowRequested {
		t.Fatal("applyNightShutdownEffect(force=false) on a live session did not request the final show: the ordinary deferral never ran")
	}
}

func TestApplyNightShutdownEffectForceOnNonLiveSessionMatchesUnforced(t *testing.T) {
	now := time.Now()
	rec := store.NightSessionRecord{ID: "night-1", State: nightStatePreshow}

	forced, forcedChanged := applyNightShutdownEffect(now, rec, "power-down", true)
	unforced, unforcedChanged := applyNightShutdownEffect(now, rec, "power-down", false)
	if forcedChanged != unforcedChanged || forced.State != unforced.State {
		t.Fatalf("force should make no difference off the live/deferring path: forced=%+v (changed=%v), unforced=%+v (changed=%v)",
			forced, forcedChanged, unforced, unforcedChanged)
	}
}
