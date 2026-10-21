package nightstatepush

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

type fakePublisher struct {
	mu   sync.Mutex
	sent []map[string]any
}

func (f *fakePublisher) Publish(_ context.Context, topic string, _ byte, _ bool, payload []byte) error {
	var env struct {
		Payload mqttproto.CmdPayload `json:"payload"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, map[string]any{"topic": topic, "action": env.Payload.Action, "open": env.Payload.Params["open"]})
	return nil
}

func (f *fakePublisher) take() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.sent
	f.sent = nil
	return out
}

func TestLoopSendsNightStateOnCreateStopAndResend(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for _, id := range []string{"node-a", "node-b"} {
		if err := st.UpsertHello(ctx, id, store.HelloRecord{}); err != nil {
			t.Fatalf("hello %s: %v", id, err)
		}
	}
	clock := time.Unix(1000, 0).UTC()
	pub := &fakePublisher{}
	loop := &Loop{Store: st, Pub: pub, Now: func() time.Time { return clock }, Poll: time.Second, Resend: time.Minute}

	expect := func(label string, open bool) {
		t.Helper()
		sent := pub.take()
		if len(sent) != 2 {
			t.Fatalf("%s: sent %d commands, want one per node: %+v", label, len(sent), sent)
		}
		for _, m := range sent {
			if m["action"] != Action || m["open"] != open {
				t.Fatalf("%s: sent %+v, want %s open=%v", label, m, Action, open)
			}
		}
	}

	loop.Pass(ctx)
	expect("first pass", false)
	loop.Pass(ctx)
	if sent := pub.take(); len(sent) != 0 {
		t.Fatalf("unchanged state resent before the resend interval: %+v", sent)
	}

	if err := st.CreateNightSession(ctx, store.NightSessionRecord{ID: "night-1", State: "preshow", StateEnteredAt: clock}, clock); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	loop.Pass(ctx)
	expect("night created", true)

	if err := st.UpdateNightSession(ctx, store.NightSessionRecord{ID: "night-1", State: "stopped", StateEnteredAt: clock}, clock); err != nil {
		t.Fatalf("stop night session: %v", err)
	}
	loop.Pass(ctx)
	expect("night stopped", false)

	clock = clock.Add(time.Minute)
	loop.Pass(ctx)
	expect("periodic resend", false)
}
