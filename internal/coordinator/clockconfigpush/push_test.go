package clockconfigpush

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// fakeConfigStore mirrors audioconfigpush's own identical test helper —
// an in-memory [ConfigStore] keyed by (kind, id).
type fakeConfigStore struct {
	objects   map[string]store.ConfigObjectRecord
	revisions map[string]store.ConfigRevisionRecord
}

func newFakeConfigStore() *fakeConfigStore {
	return &fakeConfigStore{objects: map[string]store.ConfigObjectRecord{}, revisions: map[string]store.ConfigRevisionRecord{}}
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

func TestToNodeNoConfigIsANoOp(t *testing.T) {
	cs := newFakeConfigStore()
	pub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub, time.Now, "node-1"); err != nil {
		t.Fatalf("ToNode with no config: %v", err)
	}
	if len(pub.published) != 0 {
		t.Fatalf("expected no publish, got %d", len(pub.published))
	}
}

func TestToNodePushesManagedConfig(t *testing.T) {
	cs := newFakeConfigStore()
	payload, err := config.EncodeNodeClockPayload(config.NodeClockPayload{
		Provider: "managed", Interface: "eth0", Domain: 24, ClientOnly: true,
		HoldoverLimitSeconds: 90, Priority1: 100, HardwareTimestamping: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	cs.put(config.NodeClockConfigKind, "node-1", 3, payload)

	pub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub, time.Now, "node-1"); err != nil {
		t.Fatalf("ToNode: %v", err)
	}
	if len(pub.published) != 1 {
		t.Fatalf("expected exactly one publish, got %d", len(pub.published))
	}
	cmd := pub.published[0]
	if cmd.Action != "node.clock.configure" {
		t.Fatalf("action = %q, want node.clock.configure", cmd.Action)
	}
	if cmd.Params["schema"] != clockConfigSchema {
		t.Fatalf("schema = %v, want %q", cmd.Params["schema"], clockConfigSchema)
	}
	if cmd.Params["provider"] != "managed" || cmd.Params["interface"] != "eth0" {
		t.Fatalf("params = %+v", cmd.Params)
	}
	if cmd.Params["clientOnly"] != true {
		t.Fatalf("clientOnly missing from params: %+v", cmd.Params)
	}
	if cmd.Params["hardwareTimestamping"] != true {
		t.Fatalf("hardwareTimestamping missing from params: %+v", cmd.Params)
	}
	if int(cmd.Params["revision"].(float64)) != 3 {
		t.Fatalf("revision = %v, want 3", cmd.Params["revision"])
	}
}

func TestToNodePushesFPPConfigWithBaseURL(t *testing.T) {
	cs := newFakeConfigStore()
	payload, err := config.EncodeNodeClockPayload(config.NodeClockPayload{
		Provider: "fpp", Interface: "eth0", Domain: 0, FPPBaseURL: "http://fpp-host.local",
	})
	if err != nil {
		t.Fatal(err)
	}
	cs.put(config.NodeClockConfigKind, "node-1", 1, payload)

	pub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub, time.Now, "node-1"); err != nil {
		t.Fatalf("ToNode: %v", err)
	}
	cmd := pub.published[0]
	if cmd.Params["fppBaseUrl"] != "http://fpp-host.local" {
		t.Fatalf("fppBaseUrl = %v", cmd.Params["fppBaseUrl"])
	}
}

func TestBestEffortNeverPropagatesFailure(t *testing.T) {
	cs := newFakeConfigStore()
	payload, err := config.EncodeNodeClockPayload(config.NodeClockPayload{Provider: "managed", Interface: "eth0", Domain: 24})
	if err != nil {
		t.Fatal(err)
	}
	cs.put(config.NodeClockConfigKind, "node-1", 1, payload)

	pub := &fakePublisher{failNext: true}
	// Must not panic and must return (logger is nil, matching the
	// production "no logger wired" tolerance BestEffort documents).
	BestEffort(context.Background(), cs, pub, time.Now, "node-1", nil)
}
