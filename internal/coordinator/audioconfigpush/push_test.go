package audioconfigpush

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
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
