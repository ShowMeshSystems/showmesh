package fppreconcile

import (
	"context"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

func putProgramOnlyAudioNode(t *testing.T, st *store.Store, nodeID string) {
	t.Helper()
	raw, err := config.EncodeAudioNodePayload(config.AudioNodePayload{
		ProgramRoute:    "usb-interface",
		ProgramChannels: []int{1, 2},
		Role:            config.AudioNodeRoleProgram,
	})
	if err != nil {
		t.Fatalf("encode program-only audio.node payload: %v", err)
	}
	putConfig(t, st, config.AudioNodeConfigKind, nodeID, raw)
}

func putCueWithOutputs(t *testing.T, st *store.Store, cueID, showID string, outputs config.ShowCueOutputs) {
	t.Helper()
	payload, err := config.EncodeShowCuePayload(config.ShowCuePayload{Show: showID, Name: cueID, Outputs: outputs})
	if err != nil {
		t.Fatalf("encode show.cue payload: %v", err)
	}
	putConfig(t, st, config.ShowCueConfigKind, cueID, payload)
}

func putShowWithAudioNodes(t *testing.T, st *store.Store, id, name string, audioNodes []string) {
	t.Helper()
	payload, err := config.EncodeShowPayload(config.ShowPayload{Name: name, AudioNodes: audioNodes})
	if err != nil {
		t.Fatalf("encode show payload: %v", err)
	}
	putConfig(t, st, config.ShowConfigKind, id, payload)
}

func playlistWithCue(showID, cueID string) config.ShowPlaylistPayload {
	return config.ShowPlaylistPayload{
		Show: showID, Name: "Main", Runner: "showmesh",
		Entries: []config.ShowPlaylistEntry{{ID: "e1", Cue: cueID}},
	}
}

// TestAudioTargetReadinessRefusesTwoLTCEmitters proves ADR-045 decision 2
// is checked against the store, not only at authoring time.
func TestAudioTargetReadinessRefusesTwoLTCEmitters(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putAudioNode(t, st, "m4")
	putAudioNode(t, st, "mac-mini")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a"},
	})

	cond, reason, _, err := audioTargetReadiness(context.Background(), st, nil, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetReadiness: %v", err)
	}
	if cond != ReadinessAudioLTCEmitterAmbiguous {
		t.Fatalf("condition = %q, want %q", cond, ReadinessAudioLTCEmitterAmbiguous)
	}
	if !strings.Contains(reason, "m4") || !strings.Contains(reason, "mac-mini") {
		t.Errorf("reason = %q, want it to name both nodes", reason)
	}
}

// TestAudioTargetReadinessRefusesUnboundTarget proves an output pointing at
// a node that lost its audio.node is named rather than silently dropped.
func TestAudioTargetReadinessRefusesUnboundTarget(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putAudioNode(t, st, "m4")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a", Targets: []string{"pi"}},
	})

	cond, reason, _, err := audioTargetReadiness(context.Background(), st, nil, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetReadiness: %v", err)
	}
	if cond != ReadinessAudioTargetUnbound {
		t.Fatalf("condition = %q, want %q", cond, ReadinessAudioTargetUnbound)
	}
	if !strings.Contains(reason, "pi") || !strings.Contains(reason, "cue-1") {
		t.Errorf("reason = %q, want it to name the cue and the target node", reason)
	}
}

// TestAudioTargetReadinessRefusesUnresolvableUntargetedOutput proves the
// ambiguity the catalog leaves unresolved is reported rather than silently
// producing a Cue that reaches no node at all.
func TestAudioTargetReadinessRefusesUnresolvableUntargetedOutput(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putProgramOnlyAudioNode(t, st, "pi-a")
	putProgramOnlyAudioNode(t, st, "pi-b")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a"},
	})

	cond, reason, _, err := audioTargetReadiness(context.Background(), st, nil, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetReadiness: %v", err)
	}
	if cond != ReadinessAudioTargetUnresolved {
		t.Fatalf("condition = %q, want %q", cond, ReadinessAudioTargetUnresolved)
	}
	if !strings.Contains(reason, "outputs.audio") {
		t.Errorf("reason = %q, want it to name which output", reason)
	}
}

// TestAudioTargetReadinessPassesTheReferenceInstallation proves the shape
// ADR-045 exists to allow is ready: one program+ltc node, one program node
// named by a targeted output.
func TestAudioTargetReadinessPassesTheReferenceInstallation(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putAudioNode(t, st, "m4")
	putProgramOnlyAudioNode(t, st, "pi")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a", Targets: []string{"pi"}},
		LTC:   &config.ShowCueLTCOutput{},
	})

	cond, reason, _, err := audioTargetReadiness(context.Background(), st, nil, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetReadiness: %v", err)
	}
	if cond != "" {
		t.Fatalf("condition = %q (%s), want ready", cond, reason)
	}
}

// TestAudioTargetReadinessExplicitEmptyTargetsResolvesToProgramLTCNode is
// TestAudioTargetReadinessPassesTheReferenceInstallation's own proof that
// an explicit "targets": [] on a two-node installation resolves to the
// sole program+ltc node exactly like an absent targets list does, rather
// than being read as "every node" or "no node".
func TestAudioTargetReadinessExplicitEmptyTargetsResolvesToProgramLTCNode(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putAudioNode(t, st, "m4")
	putProgramOnlyAudioNode(t, st, "pi")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a", Targets: []string{}},
	})

	cond, reason, _, err := audioTargetReadiness(context.Background(), st, nil, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetReadiness: %v", err)
	}
	if cond != "" {
		t.Fatalf("condition = %q (%s), want ready", cond, reason)
	}
}

// TestAudioTargetReadinessIgnoresACueWithNoAudioOutputs proves a
// render-only Show is never failed by an audio condition.
func TestAudioTargetReadinessIgnoresACueWithNoAudioOutputs(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Render: &config.ShowCueRenderOutput{Sequence: "seq"},
	})

	cond, _, _, err := audioTargetReadiness(context.Background(), st, nil, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetReadiness: %v", err)
	}
	if cond != "" {
		t.Fatalf("condition = %q, want ready: this cue declares no audio, LTC or announcement output", cond)
	}
}

// TestAudioTargetReadinessWarnsWhenMultiNodeTargetsExcludeProgramLTC proves
// ADR-049 decision 5's second readiness rule: a Cue naming more than one
// audio target that does not include the installation's program+ltc node
// can never start aligned (decision 3's one instant is read off that
// node's clock), so it warns naming the cue rather than either failing
// readiness or staying silent.
func TestAudioTargetReadinessWarnsWhenMultiNodeTargetsExcludeProgramLTC(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putAudioNode(t, st, "m4")
	putProgramOnlyAudioNode(t, st, "pi")
	putProgramOnlyAudioNode(t, st, "zone")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a", Targets: []string{"pi", "zone"}},
	})

	cond, _, warning, err := audioTargetReadiness(context.Background(), st, nil, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetReadiness: %v", err)
	}
	if cond != "" {
		t.Fatalf("condition = %q, want ready: an unaligned start is a warning, never a failure", cond)
	}
	if !strings.Contains(warning, "cue-1") {
		t.Errorf("warning = %q, want it to name the cue", warning)
	}
}

// TestAudioTargetReadinessNoWarningWhenMultiNodeTargetsIncludeProgramLTC
// proves the warning is specific to excluding the program+ltc node: the
// same two-target shape, with the program+ltc node as one of the targets,
// stays silent.
func TestAudioTargetReadinessNoWarningWhenMultiNodeTargetsIncludeProgramLTC(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putAudioNode(t, st, "m4")
	putProgramOnlyAudioNode(t, st, "pi")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a", Targets: []string{"m4", "pi"}},
	})

	cond, reason, warning, err := audioTargetReadiness(context.Background(), st, nil, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetReadiness: %v", err)
	}
	if cond != "" {
		t.Fatalf("condition = %q (%s), want ready", cond, reason)
	}
	if warning != "" {
		t.Errorf("warning = %q, want empty: the targets include the program+ltc node m4", warning)
	}
}

// TestAudioTargetReadinessWarnsWhenAudioAndAnnouncementTogetherExcludeProgramLTC
// proves the union is computed across BOTH outputs, not one at a time: a
// Cue whose outputs.audio names one node and outputs.announcement names a
// different one reaches two nodes together, even though neither output
// alone names more than one, and the pair excludes the program+ltc node.
func TestAudioTargetReadinessWarnsWhenAudioAndAnnouncementTogetherExcludeProgramLTC(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putAudioNode(t, st, "c")
	putProgramOnlyAudioNode(t, st, "a")
	putProgramOnlyAudioNode(t, st, "b")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio:        &config.ShowCueAudioOutput{Asset: "x", Targets: []string{"a"}},
		Announcement: &config.ShowCueAnnouncementOutput{Policy: config.ShowCueAnnouncementPolicyDuck, Targets: []string{"b"}},
	})

	cond, _, warning, err := audioTargetReadiness(context.Background(), st, nil, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetReadiness: %v", err)
	}
	if cond != "" {
		t.Fatalf("condition = %q, want ready: an unaligned start is a warning, never a failure", cond)
	}
	if !strings.Contains(warning, "cue-1") {
		t.Errorf("warning = %q, want it to name the cue: audio targets a, announcement targets b, together excluding program+ltc node c", warning)
	}
}

// TestAudioTargetReadinessNoWarningWhenAudioAndAnnouncementTogetherIncludeProgramLTC
// proves the reverse: outputs.audio alone names more than one node, but
// the pair's union includes the program+ltc node (named by
// outputs.announcement), so the Cue CAN start aligned and must not warn.
func TestAudioTargetReadinessNoWarningWhenAudioAndAnnouncementTogetherIncludeProgramLTC(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putAudioNode(t, st, "c")
	putProgramOnlyAudioNode(t, st, "a")
	putProgramOnlyAudioNode(t, st, "b")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio:        &config.ShowCueAudioOutput{Asset: "x", Targets: []string{"a", "b"}},
		Announcement: &config.ShowCueAnnouncementOutput{Policy: config.ShowCueAnnouncementPolicyDuck, Targets: []string{"c"}},
	})

	cond, reason, warning, err := audioTargetReadiness(context.Background(), st, nil, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetReadiness: %v", err)
	}
	if cond != "" {
		t.Fatalf("condition = %q (%s), want ready", cond, reason)
	}
	if warning != "" {
		t.Errorf("warning = %q, want empty: the union {a,b,c} includes program+ltc node c, even though outputs.audio alone does not", warning)
	}
}

// TestAudioTargetReadinessWarnsDistinctlyWhenNoNodeHoldsProgramLTC proves
// the warning text is honest about WHY a multi-node Cue cannot align: with
// no audio.node holding role program+ltc at all, "excludes the program+ltc
// node" would be false (there is none to exclude), so this case gets its
// own wording saying no such node exists.
func TestAudioTargetReadinessWarnsDistinctlyWhenNoNodeHoldsProgramLTC(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putProgramOnlyAudioNode(t, st, "a")
	putProgramOnlyAudioNode(t, st, "b")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio:        &config.ShowCueAudioOutput{Asset: "x", Targets: []string{"a"}},
		Announcement: &config.ShowCueAnnouncementOutput{Policy: config.ShowCueAnnouncementPolicyDuck, Targets: []string{"b"}},
	})

	cond, _, warning, err := audioTargetReadiness(context.Background(), st, nil, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetReadiness: %v", err)
	}
	if cond != "" {
		t.Fatalf("condition = %q, want ready: an unaligned start is a warning, never a failure", cond)
	}
	if !strings.Contains(warning, "cue-1") || !strings.Contains(warning, "no node has the program+LTC role") {
		t.Errorf("warning = %q, want it to name the cue and say no node holds the program+ltc role", warning)
	}
	if strings.Contains(warning, "do not include the node with the program+LTC role") {
		t.Errorf("warning = %q, want it distinct from the excludes-the-program+ltc-node wording: there is no such node to exclude", warning)
	}
}

// TestAudioTargetReadinessEmptyTargetsResolveToTheSoleAudioNode proves the
// empty-list default rule's OTHER branch (TestAudioTargetReadinessPassesTheReferenceInstallation
// covers the program+ltc branch): with no node holding program+ltc at
// all, an installation with exactly one declared audio.node still
// resolves an untargeted output to it, and stays ready. ADR-049 decision
// 10: since the show has no audioNodes of its own, this now also warns
// (audio-nodes-defaulted) rather than staying silent, since the output
// only reaches pi because of the installation-wide fallback.
func TestAudioTargetReadinessEmptyTargetsResolveToTheSoleAudioNode(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putProgramOnlyAudioNode(t, st, "pi")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a"},
	})

	cond, reason, warning, err := audioTargetReadiness(context.Background(), st, nil, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetReadiness: %v", err)
	}
	if cond != "" {
		t.Fatalf("condition = %q (%s), want ready: pi is the sole audio.node, so the untargeted output resolves to it", cond, reason)
	}
	if !strings.Contains(warning, "cue-1") {
		t.Errorf("warning = %q, want it to name the cue (audio-nodes-defaulted)", warning)
	}
}

// TestAudioTargetReadinessChecksEveryListedTarget is ADR-049's own proof:
// a targets list naming a bound node first and an unbound one second is
// still refused, naming the unbound one, rather than stopping at the first
// element that happens to check out.
func TestAudioTargetReadinessChecksEveryListedTarget(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putAudioNode(t, st, "m4")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a", Targets: []string{"m4", "pi"}},
	})

	cond, reason, _, err := audioTargetReadiness(context.Background(), st, nil, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetReadiness: %v", err)
	}
	if cond != ReadinessAudioTargetUnbound {
		t.Fatalf("condition = %q, want %q", cond, ReadinessAudioTargetUnbound)
	}
	if !strings.Contains(reason, "pi") || !strings.Contains(reason, "cue-1") {
		t.Errorf("reason = %q, want it to name the cue and the unbound target node", reason)
	}
}

// --- ADR-049 decision 10: show.audioNodes / excludeNodes at the readiness layer. ---

// TestAudioTargetReadinessUntargetedCueResolvesToShowAudioNodes proves
// decision 10 step 2: with the show's own audioNodes list set, a cue with
// no explicit targets plays on every listed node, not the installation
// default, and does not warn audio-nodes-defaulted.
func TestAudioTargetReadinessUntargetedCueResolvesToShowAudioNodes(t *testing.T) {
	st := openTestStore(t)
	putAudioNode(t, st, "m4")
	putProgramOnlyAudioNode(t, st, "pi")
	putShowWithAudioNodes(t, st, "show-1", "Show One", []string{"m4", "pi"})
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a"},
	})

	cond, reason, warning, err := audioTargetReadiness(context.Background(), st, nil, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetReadiness: %v", err)
	}
	if cond != "" {
		t.Fatalf("condition = %q (%s), want ready", cond, reason)
	}
	if strings.Contains(warning, "audio node list") {
		t.Errorf("warning = %q, want no audio-nodes-defaulted warning: the show has its own audioNodes list", warning)
	}
}

// TestAudioTargetReadinessExcludeNodesNarrowsShowAudioNodes proves
// decision 10's per-output excludeNodes: a cue excluding one of the
// show's two audio nodes plays only on the other, so an unbound-target
// check against the excluded node's own name never fires.
func TestAudioTargetReadinessExcludeNodesNarrowsShowAudioNodes(t *testing.T) {
	st := openTestStore(t)
	putAudioNode(t, st, "m4")
	putProgramOnlyAudioNode(t, st, "pi")
	putShowWithAudioNodes(t, st, "show-1", "Show One", []string{"m4", "pi"})
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a", ExcludeNodes: []string{"pi"}},
	})

	cond, reason, _, err := audioTargetReadiness(context.Background(), st, nil, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetReadiness: %v", err)
	}
	if cond != "" {
		t.Fatalf("condition = %q (%s), want ready: excludeNodes only narrows the show's own list", cond, reason)
	}
}
