// Package oscfade is ADR-064's Resolume layer fade: Arena's OSC input is
// positional, write-only and silent (RES-001), so a fade is a ramp of float32
// messages to /composition/layers/<n>/master that is never confirmed. It also
// sends every other action for an instance controlled over OSC only (ADR-066).
package oscfade

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"sync"
	"time"
)

// DefaultPort is Arena's default OSC input port (docs/bench/resolume-control-surface.md).
const DefaultPort = 7000

// oscFadeStep is the ramp interval, about 25 messages a second per layer.
const oscFadeStep = 40 * time.Millisecond

// encodeOSCFloat builds one OSC message carrying a single float32 argument.
func encodeOSCFloat(address string, v float32) []byte {
	buf := appendOSCString(nil, address)
	buf = appendOSCString(buf, ",f")
	return binary.BigEndian.AppendUint32(buf, math.Float32bits(v))
}

// appendOSCString appends s null-terminated and padded to a multiple of four bytes.
func appendOSCString(buf []byte, s string) []byte {
	buf = append(buf, s...)
	pad := 4 - len(s)%4
	return append(buf, make([]byte, pad)...)
}

// layerMasterOSCAddress is the positional OSC address of a layer's master
// fader. layerIndex is the composition's 0-based layerIndex; OSC counts from 1.
func LayerMasterAddress(layerIndex int) string {
	return fmt.Sprintf("/composition/layers/%d/master", layerIndex+1)
}

// Fader ramps layer master faders over OSC. A newer fade on a layer
// cancels the older one and continues from the last value it sent.
type Fader struct {
	addr  string
	dial  func(addr string) (net.Conn, error)
	now   func() time.Time
	sleep func(context.Context, time.Duration) bool

	mu       sync.Mutex
	conn     net.Conn
	cancels  map[int]context.CancelFunc
	lastSent map[int]float64
	gen      map[int]uint64
}

// New returns a fader that sends to host:port over UDP.
func New(host string, port int) *Fader {
	if port <= 0 {
		port = DefaultPort
	}
	return &Fader{
		addr:     net.JoinHostPort(host, fmt.Sprint(port)),
		dial:     func(addr string) (net.Conn, error) { return net.Dial("udp", addr) },
		now:      time.Now,
		sleep:    sleepCtx,
		cancels:  map[int]context.CancelFunc{},
		lastSent: map[int]float64{},
		gen:      map[int]uint64{},
	}
}

// connectLocked dials the UDP socket on first use. Called with f.mu held.
func (f *Fader) connectLocked() error {
	if f.conn != nil {
		return nil
	}
	conn, err := f.dial(f.addr)
	if err != nil {
		return err
	}
	f.conn = conn
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Fade starts ramping layerIndex's master from from, or from the last value
// sent when it replaces a running fade, to target over d, and returns at
// once. done, when non-nil, is called after the ramp ends or is cancelled.
func (f *Fader) Fade(layerIndex int, from, target float64, d time.Duration, done func()) error {
	f.mu.Lock()
	if err := f.connectLocked(); err != nil {
		f.mu.Unlock()
		return err
	}
	if cancel, ok := f.cancels[layerIndex]; ok {
		cancel()
		if last, ok := f.lastSent[layerIndex]; ok {
			from = last
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.cancels[layerIndex] = cancel
	f.gen[layerIndex]++
	gen := f.gen[layerIndex]
	f.mu.Unlock()

	go func() {
		defer func() {
			f.mu.Lock()
			if f.gen[layerIndex] == gen {
				delete(f.cancels, layerIndex)
			}
			f.mu.Unlock()
			cancel()
			if done != nil {
				done()
			}
		}()
		f.ramp(ctx, layerIndex, from, target, d)
	}()
	return nil
}

func (f *Fader) ramp(ctx context.Context, layerIndex int, from, target float64, d time.Duration) {
	start := f.now()
	for {
		frac := 1.0
		if d > 0 {
			frac = math.Min(1, float64(f.now().Sub(start))/float64(d))
		}
		v := from + (target-from)*frac
		if !f.send(ctx, layerIndex, v) || frac >= 1 {
			return
		}
		if !f.sleep(ctx, oscFadeStep) {
			return
		}
	}
}

// send writes one value unless ctx was cancelled by a newer fade first.
func (f *Fader) send(ctx context.Context, layerIndex int, v float64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ctx.Err() != nil {
		return false
	}
	f.lastSent[layerIndex] = v
	_, _ = f.conn.Write(encodeOSCFloat(LayerMasterAddress(layerIndex), float32(v)))
	return true
}

// Close cancels every running fade and closes the socket.
func (f *Fader) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, cancel := range f.cancels {
		cancel()
	}
	if f.conn != nil {
		_ = f.conn.Close()
		f.conn = nil
	}
}
