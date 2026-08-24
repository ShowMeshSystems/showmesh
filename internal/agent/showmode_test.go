package agent

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// A node that has never been told the mode reads unknown, and unknown
// behaves as show (ADR-033 decision 5). Unknown is NOT rewritten to show:
// a caller has to be able to tell "I was told show" from "nobody has told
// me anything".
func TestShowModeHolderNeverReceivedIsUnknownAndBehavesAsShow(t *testing.T) {
	h := NewShowModeHolder(func() time.Time { return time.Date(2026, 8, 23, 21, 0, 0, 0, time.UTC) })

	s := h.Current()
	if s.Mode != ShowModeUnknown {
		t.Fatalf("Mode = %q, want %q", s.Mode, ShowModeUnknown)
	}
	if !s.BehavesAsShow() {
		t.Fatal("unknown must behave as show")
	}
	if !s.Held {
		t.Fatal("a value that was never received must be reported as held, never as current")
	}
	if !s.ReceivedAt.IsZero() {
		t.Fatalf("ReceivedAt = %v, want zero", s.ReceivedAt)
	}
}

// The fresh-install coordinator default is program. A node told program
// reports program and does NOT behave as show: those are two different
// facts and collapsing them would make the whole build pointless.
func TestShowModeHolderReportsWhatItWasTold(t *testing.T) {
	now := time.Date(2026, 8, 23, 21, 0, 0, 0, time.UTC)
	h := NewShowModeHolder(func() time.Time { return now })

	if !h.Set(mqttproto.ShowModeProgram, 3, now) {
		t.Fatal("Set(program) was refused")
	}
	s := h.Current()
	if s.Mode != mqttproto.ShowModeProgram || s.Revision != 3 {
		t.Fatalf("Current() = %+v", s)
	}
	if s.BehavesAsShow() {
		t.Fatal("program must not behave as show")
	}
	if s.Held {
		t.Fatal("a value just received must be reported as current, not held")
	}

	if !h.Set(mqttproto.ShowModeShow, 4, now) {
		t.Fatal("Set(show) was refused")
	}
	if s := h.Current(); s.Mode != mqttproto.ShowModeShow || !s.BehavesAsShow() {
		t.Fatalf("Current() after show = %+v", s)
	}
}

// ADR-033 decision 5's core rule: a node that loses the coordinator KEEPS
// the last mode it knew and says the value is held rather than current. It
// never falls back to a default, because reverting to program because the
// coordinator went away would turn a coordinator outage into a live
// behaviour change mid-show.
func TestShowModeHolderKeepsTheLastModeAndReportsItHeldAfterCoordinatorLoss(t *testing.T) {
	base := time.Date(2026, 8, 23, 21, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	now := base
	h := NewShowModeHolder(func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	})

	h.Set(mqttproto.ShowModeShow, 9, base)
	if s := h.Current(); s.Held {
		t.Fatalf("fresh value reported held: %+v", s)
	}

	// Inside the window: still current.
	mu.Lock()
	now = base.Add(ShowModeFreshnessWindow - time.Second)
	mu.Unlock()
	if s := h.Current(); s.Held {
		t.Fatalf("value inside the freshness window reported held: %+v", s)
	}

	// Past the window: held, and the VALUE IS UNCHANGED.
	mu.Lock()
	now = base.Add(ShowModeFreshnessWindow + time.Second)
	mu.Unlock()
	s := h.Current()
	if !s.Held {
		t.Fatalf("value past the freshness window reported current: %+v", s)
	}
	if s.Mode != mqttproto.ShowModeShow {
		t.Fatalf("Mode = %q after coordinator loss, want the mode it was last told (show)", s.Mode)
	}
	if s.Revision != 9 {
		t.Fatalf("Revision = %d, want the revision it was last told", s.Revision)
	}

	// And a coordinator that comes back makes it current again, without any
	// intervening fallback.
	mu.Lock()
	now = base.Add(2 * ShowModeFreshnessWindow)
	resume := now
	mu.Unlock()
	h.Set(mqttproto.ShowModeShow, 9, resume)
	if s := h.Current(); s.Held {
		t.Fatalf("value reported held after the coordinator resumed: %+v", s)
	}
}

// Held is about a coordinator that stopped confirming, not about which
// mode is held: a held PROGRAM stays program, it does not quietly become
// show. Only a mode that was never received is unknown.
func TestShowModeHolderHeldProgramStaysProgram(t *testing.T) {
	base := time.Date(2026, 8, 23, 21, 0, 0, 0, time.UTC)
	now := base
	h := NewShowModeHolder(func() time.Time { return now })

	h.Set(mqttproto.ShowModeProgram, 1, base)
	now = base.Add(ShowModeFreshnessWindow + time.Minute)

	s := h.Current()
	if s.Mode != mqttproto.ShowModeProgram {
		t.Fatalf("Mode = %q, want program", s.Mode)
	}
	if !s.Held {
		t.Fatal("want held")
	}
	if s.BehavesAsShow() {
		t.Fatal("a held program mode must not silently become show")
	}
}

// A value outside ADR-033's closed vocabulary changes nothing. "unknown" in
// particular is a receiver's word and can never arrive as a value.
func TestShowModeHolderIgnoresValuesOutsideTheVocabulary(t *testing.T) {
	base := time.Date(2026, 8, 23, 21, 0, 0, 0, time.UTC)
	h := NewShowModeHolder(func() time.Time { return base })
	h.Set(mqttproto.ShowModeShow, 2, base)

	for _, bad := range []string{"unknown", "", "setup", "SHOW"} {
		if h.Set(bad, 3, base) {
			t.Fatalf("Set(%q) was accepted", bad)
		}
		if s := h.Current(); s.Mode != mqttproto.ShowModeShow || s.Revision != 2 {
			t.Fatalf("Set(%q) changed the held value: %+v", bad, s)
		}
	}
}

// Current is a point-of-decision read, so it must be safe to call from any
// goroutine while messages arrive.
func TestShowModeHolderIsSafeForConcurrentUse(t *testing.T) {
	base := time.Date(2026, 8, 23, 21, 0, 0, 0, time.UTC)
	h := NewShowModeHolder(func() time.Time { return base })

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			mode := mqttproto.ShowModeShow
			if i%2 == 0 {
				mode = mqttproto.ShowModeProgram
			}
			for j := 0; j < 200; j++ {
				h.Set(mode, int64(j), base)
			}
		}(i)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = h.Current().BehavesAsShow()
			}
		}()
	}
	wg.Wait()
}

// lockedBuffer is capturingLogger's buffer made safe to read while a
// goroutine under test is still writing to it. capturingLogger's own buffer
// has no locking, which its doc comment says is fine because every existing
// caller uses it synchronously; runShowModeWatch runs in its own goroutine,
// so this test needs the locked version.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func recordingLogger() (*slog.Logger, *lockedBuffer) {
	b := &lockedBuffer{}
	return slog.New(slog.NewTextHandler(b, &slog.HandlerOptions{Level: slog.LevelDebug})), b
}

func waitForLog(t *testing.T, b *lockedBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(b.String(), want) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("log never contained %q; got:\n%s", want, b.String())
}

func TestRunShowModeWatchLogsTheHeldTransitionAndReturnsOnCancel(t *testing.T) {
	base := time.Date(2026, 8, 23, 21, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	now := base
	h := NewShowModeHolder(func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	})
	h.Set(mqttproto.ShowModeShow, 1, base)

	logger, records := recordingLogger()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runShowModeWatch(ctx, h, logger, time.Millisecond)
	}()

	waitForLog(t, records, "show mode is current")

	mu.Lock()
	now = base.Add(ShowModeFreshnessWindow + time.Second)
	mu.Unlock()
	waitForLog(t, records, "show mode is HELD")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runShowModeWatch did not return after its context was cancelled")
	}
}
