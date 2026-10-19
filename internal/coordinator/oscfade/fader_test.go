package oscfade

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"net"
	"sync"
	"testing"
	"time"
)

func TestEncodeOSCFloatPadsAndEncodesBigEndian(t *testing.T) {
	got := encodeOSCFloat(LayerMasterAddress(0), 0.5)
	addr := "/composition/layers/1/master"
	want := append([]byte(addr), 0, 0, 0, 0) // 28 bytes plus 4 to reach the next multiple of 4
	want = append(want, ',', 'f', 0, 0)
	want = binary.BigEndian.AppendUint32(want, math.Float32bits(0.5))
	if !bytes.Equal(got, want) {
		t.Fatalf("encodeOSCFloat = %q, want %q", got, want)
	}
	if len(got)%4 != 0 {
		t.Fatalf("message length %d is not a multiple of 4", len(got))
	}
}

// fakeConn records every datagram written to it.
type fakeConn struct {
	net.Conn
	mu  sync.Mutex
	got [][]byte
}

func (c *fakeConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, append([]byte(nil), b...))
	return len(b), nil
}
func (c *fakeConn) Close() error { return nil }

func (c *fakeConn) values(t *testing.T) []float32 {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]float32, 0, len(c.got))
	for _, m := range c.got {
		out = append(out, math.Float32frombits(binary.BigEndian.Uint32(m[len(m)-4:])))
	}
	return out
}

// newTestFader returns a fader on a fake clock that advances one step per sleep.
func newTestFader(conn *fakeConn) *Fader {
	f := New("127.0.0.1", 0)
	f.dial = func(string) (net.Conn, error) { return conn, nil }
	var mu sync.Mutex
	now := time.Unix(0, 0)
	f.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	f.sleep = func(ctx context.Context, d time.Duration) bool {
		if ctx.Err() != nil {
			return false
		}
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
		return true
	}
	return f
}

func TestFaderRampsToTargetOverTheDuration(t *testing.T) {
	conn := &fakeConn{}
	f := newTestFader(conn)
	done := make(chan struct{})
	if err := f.Fade(2, 1, 0, 400*time.Millisecond, func() { close(done) }); err != nil {
		t.Fatal(err)
	}
	<-done
	vals := conn.values(t)
	if len(vals) != 11 {
		t.Fatalf("sent %d values over 400ms at 40ms steps, want 11: %v", len(vals), vals)
	}
	if vals[0] != 1 || vals[len(vals)-1] != 0 {
		t.Fatalf("ramp went %v to %v, want 1 to 0", vals[0], vals[len(vals)-1])
	}
	for i := 1; i < len(vals); i++ {
		if vals[i] > vals[i-1] {
			t.Fatalf("ramp is not monotonic: %v", vals)
		}
	}
	if !bytes.HasPrefix(conn.got[0], []byte("/composition/layers/3/master")) {
		t.Fatalf("address = %q, want layer 3", conn.got[0])
	}
}

func TestFaderNewerFadeCancelsOlderAndStartsFromLastSent(t *testing.T) {
	conn := &fakeConn{}
	f := newTestFader(conn)
	block := make(chan struct{})
	first := make(chan struct{})
	stepped := make(chan struct{}, 1)
	baseSleep := f.sleep
	f.sleep = func(ctx context.Context, d time.Duration) bool {
		select {
		case stepped <- struct{}{}:
		default:
		}
		<-block
		return baseSleep(ctx, d)
	}
	if err := f.Fade(0, 1, 0, time.Second, func() { close(first) }); err != nil {
		t.Fatal(err)
	}
	<-stepped
	f.sleep = baseSleep
	second := make(chan struct{})
	if err := f.Fade(0, 1, 0.5, 0, func() { close(second) }); err != nil {
		t.Fatal(err)
	}
	close(block)
	<-first
	<-second
	vals := conn.values(t)
	if vals[len(vals)-1] != 0.5 {
		t.Fatalf("last value = %v, want the newer fade's 0.5: %v", vals[len(vals)-1], vals)
	}
	for _, v := range vals {
		if v < 0.5 {
			t.Fatalf("the cancelled fade kept sending after the newer fade: %v", vals)
		}
	}
}

func TestFaderStartsAFreshFadeFromTheGivenLevel(t *testing.T) {
	conn := &fakeConn{}
	f := newTestFader(conn)
	first := make(chan struct{})
	if err := f.Fade(0, 1, 0, 0, func() { close(first) }); err != nil {
		t.Fatal(err)
	}
	<-first
	second := make(chan struct{})
	if err := f.Fade(0, 0.3, 1, 400*time.Millisecond, func() { close(second) }); err != nil {
		t.Fatal(err)
	}
	<-second
	vals := conn.values(t)
	if vals[1] != float32(0.3) {
		t.Fatalf("second fade started at %v, want the live level 0.3: %v", vals[1], vals)
	}
}
