package previewgateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiscoveryReportsAvahiDown(t *testing.T) {
	d := NewDiscovery(quietLog())
	d.Avahi = func() error { return errNoAvahi }
	if err := d.Available(context.Background()); err != errNoAvahi {
		t.Fatalf("Available = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go d.Loop(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for d.Unavailable() == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if d.Unavailable() != msgNoAvahi {
		t.Fatalf("Unavailable = %q", d.Unavailable())
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestDiscoveryLogsAMonitorThatKeepsDyingOnce(t *testing.T) {
	old := retryDelay
	retryDelay = time.Millisecond
	t.Cleanup(func() { retryDelay = old })

	var out syncBuffer
	d := NewDiscovery(slog.New(slog.NewTextHandler(&out, nil)))
	d.Available = func(context.Context) error { return nil }
	d.Avahi = func() error { return nil }
	var runs atomic.Int32
	d.Monitor = func(context.Context, io.Writer) error {
		runs.Add(1)
		return errors.New("plugin failed to load")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); d.Loop(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for runs.Load() < 5 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if runs.Load() < 5 {
		t.Fatalf("monitor ran %d times, want at least 5", runs.Load())
	}
	if n := strings.Count(out.String(), "NDI discovery stopped"); n != 1 {
		t.Fatalf("logged the restart %d times over %d runs, want once", n, runs.Load())
	}
}
