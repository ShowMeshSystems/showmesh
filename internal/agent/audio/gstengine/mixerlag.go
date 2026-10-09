//go:build cgo

package gstengine

import (
	"sync"
	"time"
)

// mixerLagFloorSlots and mixerLagFloorSlot set the window a lag must
// stand for before it moves a start: 3s in six slots. MEASURED: a busy
// host's mixer was back in pace within 1s of 800ms of lag.
const (
	mixerLagFloorSlots = 6
	mixerLagFloorSlot  = 500 * time.Millisecond
)

// mixerLagSampleInterval is how often watchBus records the mixers' lag.
const mixerLagSampleInterval = 200 * time.Millisecond

// mixerLagFloor keeps the smallest mixer lag recorded in each recent slot
// of an engine's life. Lag that stands has a high floor; lag from a busy
// moment does not, because some reading in the window caught the mixer in pace.
type mixerLagFloor struct {
	mu sync.Mutex
	// One more than the window, for the slot still being filled.
	slots [mixerLagFloorSlots + 1]mixerLagSlot
}

type mixerLagSlot struct {
	index  int64
	lowest time.Duration
	filled bool
}

// record notes lag as read at age, the engine's own age at the reading.
func (f *mixerLagFloor) record(age, lag time.Duration) {
	index := int64(age / mixerLagFloorSlot)
	f.mu.Lock()
	defer f.mu.Unlock()
	slot := &f.slots[index%int64(len(f.slots))]
	if !slot.filled || slot.index != index || lag < slot.lowest {
		*slot = mixerLagSlot{index: index, lowest: lag, filled: true}
	}
}

// standing returns the lag every whole slot of the window before age
// recorded, capped at now, the reading taken at age itself. A slot with
// no reading counts as no lag, so an unproven lag never moves a start.
func (f *mixerLagFloor) standing(age, now time.Duration) time.Duration {
	current := int64(age / mixerLagFloorSlot)
	f.mu.Lock()
	defer f.mu.Unlock()
	floor := now
	for back := int64(1); back <= mixerLagFloorSlots; back++ {
		index := current - back
		if index < 0 {
			return 0
		}
		slot := f.slots[index%int64(len(f.slots))]
		if !slot.filled || slot.index != index {
			return 0
		}
		floor = min(floor, slot.lowest)
	}
	return floor
}

// sampleMixerLag records the mixers' lag for [mixerLagFloor]. It holds
// e.mu across the read because Close releases the pipeline under it.
func (e *Engine) sampleMixerLag() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pipeline == nil {
		return
	}
	if _, lag, ok := e.mixerLag(); ok {
		e.lagFloor.record(time.Since(e.startedAt), lag)
	}
}
