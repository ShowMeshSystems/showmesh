package audio

import (
	"context"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// duckDepth is the gain m's current settings drive a ducked session to,
// for a session whose configured gain sits above that depth.
func duckDepth(m *Manager) pkgaudio.Gain {
	return m.SettingsSnapshot().DuckTargetGain
}

// mutation target: effectiveGainLocked's duck branch. Replace
// Settings.DuckTargetGain there with a constant and this fails. How far
// a bed drops under an announcement is an operator setting, not a
// package constant, and full silence is one value an operator may choose
// rather than the only behavior available.
func TestDuckDepthFollowsConfiguredSetting(t *testing.T) {
	for _, depth := range []pkgaudio.Gain{0, 0.1, 0.5} {
		c := newClock(time.Now())
		m := newTestManager(t, c)
		ctx := context.Background()
		bedRef := writeTestAsset(t, m.assetDir, "bed.wav", "bed-asset", []byte("x"))
		annRef := writeTestAsset(t, m.assetDir, "ann.wav", "ann-asset", []byte("y"))

		s := DefaultSettings
		s.DuckTargetGain = depth
		m.SetSettings(s)

		startPlaying(t, m, ctx, "night-bg", bedRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
		if r := m.GainSet(ctx, "night-bg", "bg-gain", 3, pkgaudio.Gain(0.6)); r.Outcome == pkgaudio.OutcomeRefused {
			t.Fatalf("gain.set refused: %+v", r)
		}
		startPlaying(t, m, ctx, "announcement-1", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)

		ducked, duckers := nightBedGain(t, m, "night-bg")
		if duckers != 1 || ducked != depth {
			t.Fatalf("bed under the announcement = %v with %d ducker(s), want %v with exactly one ducker", ducked, duckers, depth)
		}

		m.Stop(ctx, "announcement-1", "ann-stop", 3)
		restored, duckers := nightBedGain(t, m, "night-bg")
		if duckers != 0 || restored != pkgaudio.Gain(0.6) {
			t.Fatalf("bed after the announcement = %v with %d ducker(s), want the configured 0.6 and no duckers", restored, duckers)
		}
	}
}

// mutation target: the `duck < g` comparison in effectiveGainLocked.
// Drop it and a bed already quieter than the duck depth gets LOUDER
// under an announcement, which is the one thing a duck must never do.
func TestDuckNeverRaisesASessionAlreadyBelowTheDuckDepth(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	bedRef := writeTestAsset(t, m.assetDir, "bed.wav", "bed-asset", []byte("x"))
	annRef := writeTestAsset(t, m.assetDir, "ann.wav", "ann-asset", []byte("y"))

	s := DefaultSettings
	s.DuckTargetGain = pkgaudio.Gain(0.5)
	m.SetSettings(s)

	startPlaying(t, m, ctx, "night-bg", bedRef, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)
	if r := m.GainSet(ctx, "night-bg", "bg-gain", 3, pkgaudio.Gain(0.05)); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("gain.set refused: %+v", r)
	}
	startPlaying(t, m, ctx, "announcement-1", annRef, pkgaudio.SourceRoleAnnouncement, pkgaudio.MixPolicyDuck)

	ducked, _ := nightBedGain(t, m, "night-bg")
	if ducked != pkgaudio.Gain(0.05) {
		t.Fatalf("bed quieter than the duck depth reads %v under the announcement, want its own configured 0.05", ducked)
	}
}

// mutation target: SetSettings' DuckTargetGain fallback. A duck depth of
// 1 or more does not duck anything, so it must never land as given.
func TestSetSettingsRejectsDuckTargetGainAtOrAboveUnity(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)

	bad := DefaultSettings
	bad.DuckTargetGain = pkgaudio.Gain(1)
	m.SetSettings(bad)

	if got := m.SettingsSnapshot().DuckTargetGain; got != DefaultSettings.DuckTargetGain {
		t.Fatalf("DuckTargetGain = %v, want the default %v substituted for the rejected 1", got, DefaultSettings.DuckTargetGain)
	}
	if len(m.SettingsValidationIssues()) == 0 {
		t.Fatal("a rejected field must be observable, not a silent substitution")
	}
}
