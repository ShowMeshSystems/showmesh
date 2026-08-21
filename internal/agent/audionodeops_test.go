package agent

import "testing"

// validAudioNodeConfig and validAudioSettingsConfig are minimal, valid
// payloads for exercising [audioBinding.applyNode]/[audioBinding.
// applySettings] directly, independent of decodeAudioNodeConfig/
// decodeAudioSettingsConfig's own wire-shape validation.
func validAudioNodeConfig(revision int64) audioNodeConfig {
	return audioNodeConfig{
		ProgramRoute: "hw:0,0", LTCRoute: "hw:0,0",
		ProgramChannels: []int{1, 2}, LTCChannel: 3,
		ClockDomain: "d", ClockDomainProvenance: "p",
		Revision: revision,
	}
}

func validAudioSettingsConfig(revision int64) audioSettingsConfig {
	return audioSettingsConfig{
		DriftIgnoreThresholdMs: 50, DefaultFadeCurve: "linear",
		DefaultFadeDurationMs: 500, DefaultMaxBackgroundGain: 0.8,
		LTCFrameRate: "30", LTCDefaultStartOffset: "00:00:00:00",
		Revision: revision,
	}
}

// TestAudioBindingApplyNodeRefusesOlderRevision proves a revision older
// than the currently held one is refused, with the held state (and the
// callback) untouched.
func TestAudioBindingApplyNodeRefusesOlderRevision(t *testing.T) {
	var calls int
	b := newAudioBinding(func(audioNodeConfig) { calls++ }, nil)

	if err := b.applyNode(validAudioNodeConfig(5)); err != nil {
		t.Fatalf("applyNode(5): unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls after first apply = %d, want 1", calls)
	}

	if err := b.applyNode(validAudioNodeConfig(3)); err == nil {
		t.Fatal("applyNode(3) after revision 5: want an error, got nil")
	}
	if calls != 1 {
		t.Fatalf("calls after refused older revision = %d, want still 1 (no re-apply)", calls)
	}
	if rev, have := b.currentNodeRevision(); !have || rev != 5 {
		t.Fatalf("currentNodeRevision after refused older revision = (%d, %v), want (5, true)", rev, have)
	}
}

// TestAudioBindingApplyNodeReplayIsNoOp is the coverage gap the review
// found: TestRealCommandReachesRealAudioEngine exercises a same-revision
// replay end to end but only asserts the confirmed outcome, never that
// onNode was NOT invoked a second time — a rebuild side effect on replay
// would still report confirmed. This asserts the callback count directly,
// so a future change that starts re-applying on an exact replay fails
// here rather than shipping green.
func TestAudioBindingApplyNodeReplayIsNoOp(t *testing.T) {
	var calls int
	b := newAudioBinding(func(audioNodeConfig) { calls++ }, nil)

	if err := b.applyNode(validAudioNodeConfig(5)); err != nil {
		t.Fatalf("applyNode(5): unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls after first apply = %d, want 1", calls)
	}

	if err := b.applyNode(validAudioNodeConfig(5)); err != nil {
		t.Fatalf("applyNode(5) replay: unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls after exact-revision replay = %d, want still 1 (no re-apply)", calls)
	}
}

// TestAudioBindingApplySettingsRefusesOlderRevision mirrors
// TestAudioBindingApplyNodeRefusesOlderRevision for audio.settings.
func TestAudioBindingApplySettingsRefusesOlderRevision(t *testing.T) {
	var calls int
	b := newAudioBinding(nil, func(audioSettingsConfig) { calls++ })

	if err := b.applySettings(validAudioSettingsConfig(5)); err != nil {
		t.Fatalf("applySettings(5): unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls after first apply = %d, want 1", calls)
	}

	if err := b.applySettings(validAudioSettingsConfig(3)); err == nil {
		t.Fatal("applySettings(3) after revision 5: want an error, got nil")
	}
	if calls != 1 {
		t.Fatalf("calls after refused older revision = %d, want still 1 (no re-apply)", calls)
	}
	if rev, have := b.currentSettingsRevision(); !have || rev != 5 {
		t.Fatalf("currentSettingsRevision after refused older revision = (%d, %v), want (5, true)", rev, have)
	}
}

// TestAudioBindingApplySettingsReplayIsNoOp mirrors
// TestAudioBindingApplyNodeReplayIsNoOp for audio.settings.
func TestAudioBindingApplySettingsReplayIsNoOp(t *testing.T) {
	var calls int
	b := newAudioBinding(nil, func(audioSettingsConfig) { calls++ })

	if err := b.applySettings(validAudioSettingsConfig(5)); err != nil {
		t.Fatalf("applySettings(5): unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls after first apply = %d, want 1", calls)
	}

	if err := b.applySettings(validAudioSettingsConfig(5)); err != nil {
		t.Fatalf("applySettings(5) replay: unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls after exact-revision replay = %d, want still 1 (no re-apply)", calls)
	}
}
