package agent

import (
	"testing"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
)

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
		DuckTargetGain: 0.2,
		LTCFrameRate:   "30", LTCDefaultStartOffset: "00:00:00:00",
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

// TestResolveNodeChannelCount exercises the resolver in isolation from
// buildGstEngineConfig/discovery plumbing, covering the field-choice bug
// directly: [audio.RouteEvidence.Channels] alone is an unconstrained
// probe's own default fixation, not the device's real capability, so the
// resolver must prefer LTCChannels -- an explicit, achieved-count probe
// -- whenever it is the wider evidence.
func TestResolveNodeChannelCount(t *testing.T) {
	const route = "hw:1,0"

	cases := []struct {
		name         string
		routes       []audio.RouteEvidence
		bindingCount int
		want         int
	}{
		{
			name: "LTCChannels wider than the unconstrained probe wins",
			routes: []audio.RouteEvidence{
				{Device: route, ProbeResult: audio.ProbeResult{Available: true, Channels: 2}, LTCChannels: 4},
			},
			bindingCount: 3,
			want:         4,
		},
		{
			name: "no explicit probe evidence falls back to the unconstrained Channels",
			routes: []audio.RouteEvidence{
				{Device: route, ProbeResult: audio.ProbeResult{Available: true, Channels: 4}},
			},
			bindingCount: 3,
			want:         4,
		},
		{
			name: "neither probe exceeds the binding floor, floor wins",
			routes: []audio.RouteEvidence{
				{Device: route, ProbeResult: audio.ProbeResult{Available: true, Channels: 2}, LTCChannels: 3},
			},
			bindingCount: 3,
			want:         3,
		},
		{
			name: "an unavailable route's evidence is never used, and is not evidence at all",
			routes: []audio.RouteEvidence{
				{Device: route, ProbeResult: audio.ProbeResult{Available: false, Channels: 8}, LTCChannels: 8},
			},
			bindingCount: 3,
			want:         0,
		},
		{
			name:         "no matching route at all reports no evidence rather than the binding floor",
			routes:       nil,
			bindingCount: 2,
			want:         0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := audio.Discovery{Routes: tc.routes}
			got, source := resolveNodeChannelCount(d, route, tc.bindingCount)
			if got != tc.want {
				t.Errorf("resolveNodeChannelCount() = %d (%s), want %d", got, source, tc.want)
			}
			if source == "" {
				t.Error("source is empty, want a stated reason")
			}
		})
	}
}

// TestAudioBindingApplyNodeReplayRebuildsWhenEngineBroken reproduces the
// gap left by a coordinator's hello push resending the SAME revision
// this node already holds. Before this test, that replay was
// unconditionally a no-op (TestAudioBindingApplyNodeReplayIsNoOp), so a
// broken output pipeline never got rebuilt by anything short of an
// artificial revision bump or an agent restart. Once
// [audioBinding.SetNodeBrokenCheck] reports the engine broken, the exact
// same replay must call onNode again.
func TestAudioBindingApplyNodeReplayRebuildsWhenEngineBroken(t *testing.T) {
	var calls int
	b := newAudioBinding(func(audioNodeConfig) { calls++ }, nil)
	broken := false
	b.SetNodeBrokenCheck(func() bool { return broken })

	if err := b.applyNode(validAudioNodeConfig(5)); err != nil {
		t.Fatalf("applyNode(5): unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls after first apply = %d, want 1", calls)
	}

	broken = true
	if err := b.applyNode(validAudioNodeConfig(5)); err != nil {
		t.Fatalf("applyNode(5) replay while broken: unexpected error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls after exact-revision replay while broken = %d, want 2 (the replay must act as a rebuild request)", calls)
	}

	broken = false
	if err := b.applyNode(validAudioNodeConfig(5)); err != nil {
		t.Fatalf("applyNode(5) replay while healthy: unexpected error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls after exact-revision replay while healthy = %d, want still 2 (a healthy engine's replay stays a no-op)", calls)
	}
}

// programOnlyNodeParams is an audio.node.configure params map with no
// LTC route or channel at all: the shape the coordinator pushes for a
// two-output interface, which has no channel to spare for LTC.
func programOnlyNodeParams() map[string]any {
	return map[string]any{
		"programRoute":          "hw:CARD=USB,DEV=0",
		"programChannels":       []any{float64(1), float64(2)},
		"clockDomain":           "solo",
		"clockDomainProvenance": "single interface",
		"revision":              float64(1),
	}
}

// TestDecodeAudioNodeConfigAcceptsProgramOnly proves the agent accepts a
// binding with no LTC. gstengine.Config.Validate already permits
// LTCChannel 0 and wires unclaimed channels to silence, so the agent's
// own wire-shape check was the only thing refusing it.
func TestDecodeAudioNodeConfigAcceptsProgramOnly(t *testing.T) {
	p, err := decodeAudioNodeConfig(programOnlyNodeParams())
	if err != nil {
		t.Fatalf("decodeAudioNodeConfig = %v, want nil", err)
	}
	if p.LTCRoute != "" || p.LTCChannel != 0 {
		t.Errorf("decoded LTC where none was declared: route %q channel %d", p.LTCRoute, p.LTCChannel)
	}
	if got := audioNodeChannelCount(p); got != 2 {
		t.Errorf("audioNodeChannelCount = %d, want 2: a program-only binding must not ask the device for an LTC channel", got)
	}
}

// TestDecodeAudioNodeConfigRejectsHalfDeclaredLTC proves one of the pair
// without the other is refused, matching the coordinator's own decode
// rather than silently binding half an LTC route.
func TestDecodeAudioNodeConfigRejectsHalfDeclaredLTC(t *testing.T) {
	cases := []struct {
		name  string
		mutry func(map[string]any)
	}{
		{"route without channel", func(m map[string]any) { m["ltcRoute"] = "hw:CARD=USB,DEV=0" }},
		{"channel without route", func(m map[string]any) { m["ltcChannel"] = float64(3) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := programOnlyNodeParams()
			tc.mutry(params)
			if _, err := decodeAudioNodeConfig(params); err == nil {
				t.Fatal("decodeAudioNodeConfig accepted half a declared LTC pair")
			}
		})
	}
}

// TestDecodeAudioNodeConfigStillRejectsBadLTCWhenDeclared proves making
// the pair optional did not weaken the declared-LTC case.
func TestDecodeAudioNodeConfigStillRejectsBadLTCWhenDeclared(t *testing.T) {
	params := programOnlyNodeParams()
	params["ltcRoute"] = "hw:CARD=USB,DEV=0"
	params["ltcChannel"] = float64(0)
	if _, err := decodeAudioNodeConfig(params); err == nil {
		t.Fatal("decodeAudioNodeConfig accepted a declared ltcChannel of 0")
	}
}
