package previewgateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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
	for d.Unavailable() != msgNoAvahi && time.Now().Before(deadline) {
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

func TestNDIRuntimeCheck(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  string
	}{
		{"current runtime", []string{"libndi.so.6"}, ""},
		{"older runtime", []string{"libndi.so.5"}, ""},
		{"runtime beside a misnamed copy", []string{"libndi.so", "libndi.so.6"}, ""},
		{"misnamed runtime", []string{"libndi.so"}, "The NDI runtime is named libndi.so. Rename or link it to libndi.so.6."},
		{"versioned file only", []string{"libndi.so.6.1.1"}, "The NDI runtime is named libndi.so.6.1.1. Rename or link it to libndi.so.6."},
		{"no runtime", []string{"README.txt"}, msgNoNDIRuntime},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range c.files {
				if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			err := checkNDIRuntime([]string{t.TempDir(), dir})
			got := ""
			if err != nil {
				got = err.Error()
			}
			if got != c.want {
				t.Fatalf("checkNDIRuntime = %q, want %q", got, c.want)
			}
		})
	}
}

func TestNDIRuntimeCheckIgnoresABrokenLink(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, "libndi.so.6")); err != nil {
		t.Fatal(err)
	}
	if err := checkNDIRuntime([]string{dir}); err == nil || err.Error() != msgNoNDIRuntime {
		t.Fatalf("checkNDIRuntime = %v", err)
	}
}

func TestDiscoveryReportsAMisnamedRuntime(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "libndi.so"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LD_LIBRARY_PATH", dir)
	d := NewDiscovery(quietLog())
	d.Avahi = func() error { return nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Loop(ctx)
	want := "The NDI runtime is named libndi.so. Rename or link it to libndi.so.6."
	deadline := time.Now().Add(2 * time.Second)
	for d.Unavailable() != want && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := d.Unavailable(); got != want {
		t.Fatalf("Unavailable = %q, want %q", got, want)
	}
}

func TestDiscoveryThatFailsAtStartIsNotRunning(t *testing.T) {
	oldRetry, oldGrace := retryDelay, startGrace
	retryDelay, startGrace = time.Millisecond, time.Hour
	t.Cleanup(func() { retryDelay, startGrace = oldRetry, oldGrace })

	d := NewDiscovery(quietLog())
	d.Available = func(context.Context) error { return nil }
	d.Avahi = func() error { return nil }
	var runs atomic.Int32
	var sawRunning atomic.Bool
	d.Monitor = func(context.Context, io.Writer) error {
		if runs.Add(1) > 1 {
			if _, ok := d.Announced(); ok {
				sawRunning.Store(true)
			}
		}
		return errors.New("exit status 255")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); d.Loop(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for runs.Load() < 5 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// Read before cancelling: shutdown ends the last run, which reads as a restart.
	got := d.Unavailable()
	cancel()
	<-done
	if runs.Load() < 5 {
		t.Fatalf("monitor ran %d times, want at least 5", runs.Load())
	}
	if sawRunning.Load() {
		t.Fatal("discovery reported running while the monitor kept failing at start")
	}
	if got != msgCannotStart {
		t.Fatalf("Unavailable = %q, want %q", got, msgCannotStart)
	}
}

func TestDiscoveryRunsAgainOnceTheMonitorStaysUp(t *testing.T) {
	oldRetry, oldGrace := retryDelay, startGrace
	retryDelay, startGrace = time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { retryDelay, startGrace = oldRetry, oldGrace })

	d := NewDiscovery(quietLog())
	d.Available = func(context.Context) error { return nil }
	d.Avahi = func() error { return nil }
	var runs atomic.Int32
	d.Monitor = func(ctx context.Context, _ io.Writer) error {
		if runs.Add(1) == 1 {
			return errors.New("exit status 255")
		}
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); d.Loop(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(2 * time.Second)
	for runs.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := d.Unavailable(); got != msgCannotStart {
		t.Fatalf("during the grace period Unavailable = %q, want %q", got, msgCannotStart)
	}
	for d.Unavailable() != "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if _, ok := d.Announced(); !ok {
		t.Fatalf("discovery did not report running after the monitor stayed up: %q", d.Unavailable())
	}
}
