package audioconfigpush

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// fakeConfigStore is an in-memory [ConfigStore] keyed by (kind, id).
type fakeConfigStore struct {
	objects   map[string]store.ConfigObjectRecord
	revisions map[string]store.ConfigRevisionRecord
}

func newFakeConfigStore() *fakeConfigStore {
	return &fakeConfigStore{
		objects:   map[string]store.ConfigObjectRecord{},
		revisions: map[string]store.ConfigRevisionRecord{},
	}
}

func key(kind, id string) string { return kind + "/" + id }

func revisionKey(kind, id string, revision int64) string {
	return fmt.Sprintf("%s@%d", key(kind, id), revision)
}

func (f *fakeConfigStore) put(kind, id string, revision int64, payloadJSON string) {
	f.objects[key(kind, id)] = store.ConfigObjectRecord{Kind: kind, ID: id, CurrentRevision: revision}
	f.revisions[revisionKey(kind, id, revision)] = store.ConfigRevisionRecord{
		Kind: kind, ObjectID: id, Revision: revision, PayloadJSON: payloadJSON,
	}
}

func (f *fakeConfigStore) GetConfigObject(_ context.Context, kind, id string) (store.ConfigObjectRecord, error) {
	obj, ok := f.objects[key(kind, id)]
	if !ok {
		return store.ConfigObjectRecord{}, store.ErrConfigObjectNotFound
	}
	return obj, nil
}

func (f *fakeConfigStore) GetConfigRevision(_ context.Context, kind, id string, revision int64) (store.ConfigRevisionRecord, error) {
	rev, ok := f.revisions[revisionKey(kind, id, revision)]
	if !ok {
		return store.ConfigRevisionRecord{}, store.ErrConfigObjectNotFound
	}
	return rev, nil
}

// fakePublisher records every publish, keyed by decoded action.
type fakePublisher struct {
	published []mqttproto.CmdPayload
	failNext  bool
}

func (p *fakePublisher) Publish(_ context.Context, _ string, _ byte, _ bool, payload []byte) error {
	if p.failNext {
		p.failNext = false
		return context.DeadlineExceeded
	}
	env, err := mqttproto.DecodeEnvelope(payload)
	if err != nil {
		return err
	}
	cmd, err := mqttproto.DecodeCmdPayload(env)
	if err != nil {
		return err
	}
	p.published = append(p.published, cmd)
	return nil
}

func (p *fakePublisher) actionParams(action string) (map[string]any, bool) {
	for _, c := range p.published {
		if c.Action == action {
			return c.Params, true
		}
	}
	return nil, false
}

// TestToNodePushesConfiguredAudioNode proves a configured audio.node
// binding is pushed with its stored revision and every field intact.
func TestToNodePushesConfiguredAudioNode(t *testing.T) {
	cs := newFakeConfigStore()
	payload := config.AudioNodePayload{
		ProgramRoute: "hw:CARD=X,DEV=0", LTCRoute: "hw:CARD=X,DEV=0",
		ProgramChannels: []int{1, 2}, LTCChannel: 3,
		ClockDomain: "one-interface", ClockDomainProvenance: "single card",
	}
	raw, err := config.EncodeAudioNodePayload(payload)
	if err != nil {
		t.Fatalf("EncodeAudioNodePayload: %v", err)
	}
	cs.put(config.AudioNodeConfigKind, "node-1", 3, raw)

	pub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub, time.Now, "node-1"); err != nil {
		t.Fatalf("ToNode: %v", err)
	}

	params, ok := pub.actionParams("audio.node.configure")
	if !ok {
		t.Fatal("no audio.node.configure command was published")
	}
	if params["programRoute"] != payload.ProgramRoute {
		t.Errorf("programRoute = %v, want %v", params["programRoute"], payload.ProgramRoute)
	}
	rev, ok := params["revision"].(float64)
	if !ok || int64(rev) != 3 {
		t.Errorf("revision = %v, want 3", params["revision"])
	}
}

// TestToNodePushesProgramOnlyAudioNodeWithoutLTCKeys pins the push half
// of a program-only binding. Such a node's stored payload carries no LTC route
// or channel, and the AGENT refuses one of the pair without the other,
// so the push must omit BOTH keys rather than sending an empty route and
// a zero channel. Getting this wrong makes the coordinator's own push be
// rejected by its own agent, and the binding never lands.
func TestToNodePushesProgramOnlyAudioNodeWithoutLTCKeys(t *testing.T) {
	cs := newFakeConfigStore()
	payload := config.AudioNodePayload{
		ProgramRoute:    "hw:CARD=USB,DEV=0",
		ProgramChannels: []int{1, 2},
		ClockDomain:     "solo", ClockDomainProvenance: "two-output interface",
	}
	raw, err := config.EncodeAudioNodePayload(payload)
	if err != nil {
		t.Fatalf("EncodeAudioNodePayload: %v", err)
	}
	cs.put(config.AudioNodeConfigKind, "pi-audio-01", 1, raw)

	pub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub, time.Now, "pi-audio-01"); err != nil {
		t.Fatalf("ToNode: %v", err)
	}

	params, ok := pub.actionParams("audio.node.configure")
	if !ok {
		t.Fatal("no audio.node.configure command was published")
	}
	if _, present := params["ltcRoute"]; present {
		t.Errorf("params carries ltcRoute = %v for a program-only node; the key must be absent, not empty", params["ltcRoute"])
	}
	if _, present := params["ltcChannel"]; present {
		t.Errorf("params carries ltcChannel = %v for a program-only node; the key must be absent, not zero", params["ltcChannel"])
	}
	if params["programRoute"] != payload.ProgramRoute {
		t.Errorf("programRoute = %v, want %v", params["programRoute"], payload.ProgramRoute)
	}
	if params["clockDomain"] != payload.ClockDomain {
		t.Errorf("clockDomain = %v, want %v", params["clockDomain"], payload.ClockDomain)
	}
}

// TestToNodePushesBothLTCKeysWhenDeclared is the other half: a node that
// DOES declare LTC must still get both keys, so the program-only path
// above cannot be satisfied by dropping them unconditionally.
func TestToNodePushesBothLTCKeysWhenDeclared(t *testing.T) {
	cs := newFakeConfigStore()
	payload := config.AudioNodePayload{
		ProgramRoute: "hw:CARD=X,DEV=0", LTCRoute: "hw:CARD=X,DEV=0",
		ProgramChannels: []int{1, 2}, LTCChannel: 3,
		ClockDomain: "one-interface", ClockDomainProvenance: "single card",
	}
	raw, err := config.EncodeAudioNodePayload(payload)
	if err != nil {
		t.Fatalf("EncodeAudioNodePayload: %v", err)
	}
	cs.put(config.AudioNodeConfigKind, "node-1", 1, raw)

	pub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub, time.Now, "node-1"); err != nil {
		t.Fatalf("ToNode: %v", err)
	}
	params, ok := pub.actionParams("audio.node.configure")
	if !ok {
		t.Fatal("no audio.node.configure command was published")
	}
	if params["ltcRoute"] != payload.LTCRoute {
		t.Errorf("ltcRoute = %v, want %v", params["ltcRoute"], payload.LTCRoute)
	}
	ch, ok := params["ltcChannel"].(float64)
	if !ok || int(ch) != payload.LTCChannel {
		t.Errorf("ltcChannel = %v, want %d", params["ltcChannel"], payload.LTCChannel)
	}
}

// TestToNodeSkipsUnconfiguredAudioNode proves a node with no audio.node
// object ever written gets no audio.node.configure push — never an
// error, and never a push carrying a fabricated binding.
func TestToNodeSkipsUnconfiguredAudioNode(t *testing.T) {
	cs := newFakeConfigStore()
	pub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub, time.Now, "unconfigured-node"); err != nil {
		t.Fatalf("ToNode: %v", err)
	}
	if _, ok := pub.actionParams("audio.node.configure"); ok {
		t.Error("audio.node.configure was published for a node with no audio.node config object, want none")
	}
	// audio.settings is always pushed, default or not.
	if _, ok := pub.actionParams("audio.settings.configure"); !ok {
		t.Error("audio.settings.configure was not published, want the default payload pushed regardless")
	}
}

// TestToNodePushesDefaultAudioSettings proves an unconfigured
// audio.settings still pushes [config.AudioSettingsDefaultPayload] at
// revision 0, never omitted and never an error.
func TestToNodePushesDefaultAudioSettings(t *testing.T) {
	cs := newFakeConfigStore()
	pub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub, time.Now, "any-node"); err != nil {
		t.Fatalf("ToNode: %v", err)
	}
	params, ok := pub.actionParams("audio.settings.configure")
	if !ok {
		t.Fatal("audio.settings.configure was not published")
	}
	if params["defaultFadeCurve"] != config.AudioSettingsDefaultPayload.DefaultFadeCurve {
		t.Errorf("defaultFadeCurve = %v, want %v", params["defaultFadeCurve"], config.AudioSettingsDefaultPayload.DefaultFadeCurve)
	}
	// The stored value is decibels; what leaves for the node is the linear
	// multiplier the agent and the engine have always taken.
	wantDuck := float64(pkgaudio.GainFromDb(config.AudioSettingsDefaultPayload.DuckTargetGainDb))
	if params["duckTargetGain"] != wantDuck {
		t.Errorf("duckTargetGain = %v, want the linear %v", params["duckTargetGain"], wantDuck)
	}
	if _, present := params["duckTargetGainDb"]; present {
		t.Error("duckTargetGainDb reached the node; the coordinator-to-agent wire must stay linear")
	}
	wantCeiling := float64(pkgaudio.CeilingFromDb(config.AudioSettingsDefaultPayload.DefaultMaxBackgroundGainDb))
	if params["defaultMaxBackgroundGain"] != wantCeiling {
		t.Errorf("defaultMaxBackgroundGain = %v, want the linear %v", params["defaultMaxBackgroundGain"], wantCeiling)
	}
	if _, present := params["defaultMaxBackgroundGainDb"]; present {
		t.Error("defaultMaxBackgroundGainDb reached the node; the coordinator-to-agent wire must stay linear")
	}
	if rev, _ := params["revision"].(float64); rev != 0 {
		t.Errorf("revision = %v, want 0 (never written)", params["revision"])
	}
}

// TestBestEffortNeverPanicsOnPublishFailure proves BestEffort swallows a
// publish failure rather than propagating or panicking — the write or
// hello that triggered it must never fail because of this push.
func TestBestEffortNeverPanicsOnPublishFailure(t *testing.T) {
	cs := newFakeConfigStore()
	pub := &fakePublisher{failNext: true}
	BestEffort(context.Background(), cs, pub, time.Now, "any-node", nil)
}
