package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
)

// discardLogger is a *slog.Logger that writes nowhere, for tests that need
// a non-nil logger but do not assert on its output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recordedPublish captures one call to fakePublisher.Publish.
type recordedPublish struct {
	topic   string
	qos     byte
	retain  bool
	payload []byte
}

// fakePublisher is a Publisher that records every call instead of talking
// to a broker, so tests can assert on topic/qos/retain/payload directly and
// can force specific calls to fail (via failOn) without any network
// involved. It also signals notify after recording each call, so a test
// driving runHeartbeat's ticks channel from another goroutine can wait for
// a publish to actually happen without a real sleep.
type fakePublisher struct {
	mu     sync.Mutex
	calls  []recordedPublish
	failOn map[int]bool // zero-based call index -> whether that call should return an error
	notify chan struct{}
}

func newFakePublisher() *fakePublisher {
	return &fakePublisher{notify: make(chan struct{}, 64)}
}

func (f *fakePublisher) Publish(_ context.Context, topic string, qos byte, retain bool, payload []byte) error {
	f.mu.Lock()
	idx := len(f.calls)
	fail := f.failOn[idx]
	f.calls = append(f.calls, recordedPublish{
		topic:   topic,
		qos:     qos,
		retain:  retain,
		payload: append([]byte(nil), payload...),
	})
	f.mu.Unlock()

	f.notify <- struct{}{}

	if fail {
		return errors.New("fakePublisher: simulated publish failure")
	}
	return nil
}

func (f *fakePublisher) snapshot() []recordedPublish {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedPublish, len(f.calls))
	copy(out, f.calls)
	return out
}

// fakeConn adds Disconnect to fakePublisher's recording so shutdown_test.go
// can assert publish-then-disconnect ordering directly, and can force
// either call to fail.
type fakeConn struct {
	*fakePublisher

	mu             sync.Mutex
	order          []string
	disconnectErr  error
	disconnectDone chan struct{}
}

func newFakeConn() *fakeConn {
	return &fakeConn{
		fakePublisher:  newFakePublisher(),
		disconnectDone: make(chan struct{}, 1),
	}
}

func (f *fakeConn) Publish(ctx context.Context, topic string, qos byte, retain bool, payload []byte) error {
	err := f.fakePublisher.Publish(ctx, topic, qos, retain, payload)
	f.mu.Lock()
	f.order = append(f.order, "publish:"+topic)
	f.mu.Unlock()
	return err
}

func (f *fakeConn) Disconnect(context.Context) error {
	f.mu.Lock()
	f.order = append(f.order, "disconnect")
	f.mu.Unlock()
	f.disconnectDone <- struct{}{}
	return f.disconnectErr
}

func (f *fakeConn) callOrder() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.order))
	copy(out, f.order)
	return out
}
