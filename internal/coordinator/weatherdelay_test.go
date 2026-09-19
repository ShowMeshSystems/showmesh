package coordinator

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

type fakeWeatherDelayPublisher struct {
	mu    sync.Mutex
	count int
}

func (f *fakeWeatherDelayPublisher) Publish(context.Context, string, byte, bool, []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count++
	return nil
}

func (f *fakeWeatherDelayPublisher) publishCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.count
}

func TestRunWeatherDelayPublishesTheStoredStateOnTheFirstPass(t *testing.T) {
	st, err := store.Open(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	pub := &fakeWeatherDelayPublisher{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runWeatherDelay(ctx, st, pub, nil, fixedNow(), discardLogger(), time.Hour)

	waitFor(t, func() bool { return pub.publishCount() > 0 })
}

func TestRunWeatherDelayRepublishesActiveStateAtItsOwnRevision(t *testing.T) {
	st, err := store.Open(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	if err := st.SetWeatherDelayState(context.Background(), store.WeatherDelayStateRecord{
		Active: true, Kind: "delay", StartedAt: now, StartedBy: "op-1", Revision: 3,
	}); err != nil {
		t.Fatalf("SetWeatherDelayState: %v", err)
	}

	var mu sync.Mutex
	var last mqttproto.WeatherDelayMessage
	pub := publishFunc(func(_ context.Context, topic string, _ byte, retain bool, payload []byte) error {
		if topic != mqttproto.WeatherDelayTopic() || !retain {
			t.Errorf("published to %q retain=%v, want %q retained", topic, retain, mqttproto.WeatherDelayTopic())
		}
		msg, err := mqttproto.DecodeWeatherDelayMessage(payload)
		if err != nil {
			t.Errorf("DecodeWeatherDelayMessage: %v", err)
			return nil
		}
		mu.Lock()
		last = msg
		mu.Unlock()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runWeatherDelay(ctx, st, pub, nil, fixedNow(), discardLogger(), time.Hour)

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return last.Revision == 3
	})
	mu.Lock()
	defer mu.Unlock()
	if !last.Active || last.Kind != "delay" || last.StartedBy != "op-1" {
		t.Fatalf("republished %+v, want the stored active state", last)
	}
}

type publishFunc func(ctx context.Context, topic string, qos byte, retain bool, payload []byte) error

func (f publishFunc) Publish(ctx context.Context, topic string, qos byte, retain bool, payload []byte) error {
	return f(ctx, topic, qos, retain, payload)
}
