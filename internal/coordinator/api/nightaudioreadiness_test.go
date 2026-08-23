package api

import (
	"context"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

// TestNightCheckBackgroundAudioAssets_MissingAssetFails proves a
// configured item with no current asset reports failed, not a silent
// pass.
func TestNightCheckBackgroundAudioAssets_MissingAssetFails(t *testing.T) {
	h, _, _, _ := nightBackgroundAudioTestHandlers(t)
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)

	check := h.nightCheckBackgroundAudioAssets(context.Background(), "halloween", ba)

	if check.health == nightHealthHealthy() {
		t.Fatalf("check = %+v, want failed (no assets were ever registered)", check)
	}
}

// TestNightCheckBackgroundAudioAssets_PresentAssetsPass proves the
// positive case is reachable, not merely always-failed.
func TestNightCheckBackgroundAudioAssets_PresentAssetsPass(t *testing.T) {
	h, st, _, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)

	check := h.nightCheckBackgroundAudioAssets(context.Background(), "halloween", ba)

	if check.health != nightHealthHealthy() {
		t.Fatalf("check = %+v, want healthy", check)
	}
}

// TestNightCheckBackgroundAudioItemTransition defends the closed
// evidence-backed rule: sequential always passes; gapless/crossfade
// always fail, because this build holds no capability signal that could
// ever confirm them. Mutation-checked: flipping outputConfirms's literal
// from false to true in nightCheckBackgroundAudioItemTransition would
// make gapless/crossfade pass, which is exactly the "invented passing
// check" this seam must not produce.
func TestNightCheckBackgroundAudioItemTransition(t *testing.T) {
	cases := []struct {
		transition string
		wantHealth nightCheckState
	}{
		{config.NightSessionItemTransitionSequential, nightHealthHealthy()},
		{config.NightSessionItemTransitionGapless, nightHealthFailed()},
		{config.NightSessionItemTransitionCrossfade, nightHealthFailed()},
	}
	for _, tc := range cases {
		t.Run(tc.transition, func(t *testing.T) {
			ba := &config.NightSessionBackgroundAudio{ItemTransition: tc.transition}
			check := nightCheckBackgroundAudioItemTransition(ba)
			if check.health != tc.wantHealth {
				t.Errorf("itemTransition %q: health = %v, want %v", tc.transition, check.health, tc.wantHealth)
			}
		})
	}
}

// TestNightCheckAudioOutputCapabilities_NeverInventsAPass proves this
// check is always not_verifiable, never healthy - this codebase holds no
// evidence for it, and reporting anything else would be inventing a
// passing check.
func TestNightCheckAudioOutputCapabilities_NeverInventsAPass(t *testing.T) {
	check := nightCheckAudioOutputCapabilities("node-a")
	if check.health != nightCheckStateNotVerifiable {
		t.Fatalf("health = %v, want not_verifiable", check.health)
	}
}

// TestNightCheckAnnouncementAssets_NeverInventsAPass mirrors the same
// rule for announcement content: not_verifiable when one IS configured
// (this coordinator holds no evidence for it), not_configured when none
// is (LOW 14: absent OPTIONAL configuration is a different fact from a
// structurally unverifiable check).
func TestNightCheckAnnouncementAssets_NeverInventsAPass(t *testing.T) {
	withAnnouncement := []config.NightSessionCue{{Name: "thanks", Role: config.NightSessionCueRoleAnnouncement}}
	check := nightCheckAnnouncementAssets(withAnnouncement)
	if check.health != nightCheckStateNotVerifiable {
		t.Fatalf("with an announcement cue: health = %v, want not_verifiable", check.health)
	}

	withoutAnnouncement := []config.NightSessionCue{{Name: "lights", Role: config.NightSessionCueRoleLighting}}
	check = nightCheckAnnouncementAssets(withoutAnnouncement)
	if check.health != nightCheckStateNotConfigured {
		t.Fatalf("with no announcement cue: health = %v, want not_configured", check.health)
	}
}
