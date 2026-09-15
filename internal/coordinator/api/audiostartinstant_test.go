package api

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

// TestSelectAudioStartInstantRecoversAPanicInRead proves a panicking read
// for one node never takes the whole selection down with it (an
// unrecovered panic in the launched goroutine would crash the whole
// coordinator process): the panicking node's own reading is recorded as
// absent evidence, and a valid reading on the clock holder still lets
// Select succeed.
func TestSelectAudioStartInstantRecoversAPanicInRead(t *testing.T) {
	const now = int64(1_700_000_000_000_000_000)
	settings := config.AudioSettingsPayload{ScheduledStartDeliveryBoundMs: 100, ScheduledStartMarginMs: 50}
	read := func(_ context.Context, nodeID string) (AudioStartInstantReading, error) {
		if nodeID == "panics" {
			panic("boom")
		}
		return AudioStartInstantReading{HoldsMediaClock: true, Evidence: map[string]any{
			"mediaClockValid": true, "mediaClockNowNs": json.Number(strconv.FormatInt(now, 10)),
		}}, nil
	}
	sel, err := SelectAudioStartInstant(context.Background(), []string{"panics", "holder"}, settings, read)
	if err != nil {
		t.Fatalf("Select = %v, want a usable instant derived from the non-panicking clock holder", err)
	}
	if sel.ClockNodeID != "holder" {
		t.Fatalf("ClockNodeID = %q, want %q", sel.ClockNodeID, "holder")
	}
}

// TestSelectAudioStartInstantAddsTheMeasuredRoundTripToTheDeliveryBound
// proves ADR-049 decision 3's own delivery-bound fix: the slowest node's
// own measured read round trip is folded into the delivery bound Select
// uses, on top of settings' own configured bound, so a Cue activation's
// extra probe-and-dispatch hop (which alignedstart.go's own single
// prepare-and-go never pays) does not routinely land T0 in the past.
func TestSelectAudioStartInstantAddsTheMeasuredRoundTripToTheDeliveryBound(t *testing.T) {
	const now = int64(1_700_000_000_000_000_000)
	const slowRead = 120 * time.Millisecond
	settings := config.AudioSettingsPayload{ScheduledStartDeliveryBoundMs: 100, ScheduledStartMarginMs: 0}
	read := func(_ context.Context, nodeID string) (AudioStartInstantReading, error) {
		if nodeID == "slow" {
			time.Sleep(slowRead)
		}
		return AudioStartInstantReading{HoldsMediaClock: nodeID == "holder", Evidence: map[string]any{
			"mediaClockValid": true, "mediaClockNowNs": json.Number(strconv.FormatInt(now, 10)),
		}}, nil
	}
	sel, err := SelectAudioStartInstant(context.Background(), []string{"slow", "holder"}, settings, read)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	// The bare configured bound (100ms) alone would put DeliveryBoundNs at
	// 100e6. The measured slow read (120ms) must widen it well past that,
	// proving the round trip was actually folded in rather than ignored.
	const bareBoundNs = int64(100 * 1_000_000)
	if sel.DeliveryBoundNs <= bareBoundNs {
		t.Fatalf("DeliveryBoundNs = %d, want it widened past the bare configured bound %d by the slow node's measured round trip", sel.DeliveryBoundNs, bareBoundNs)
	}
	if sel.ScheduledAtNs <= now+bareBoundNs {
		t.Fatalf("ScheduledAtNs = %d, want it pushed later than now+bareBound (%d) by the measured round trip", sel.ScheduledAtNs, now+bareBoundNs)
	}
}
