package clock

import (
	"testing"
	"time"
)

// TestRealtimeReaderNowReadsSystemTime proves Now reads a real, current
// wall-clock time and never fails -- unlike [PHCReader], there is no
// device to open, so there is nothing to fail against.
func TestRealtimeReaderNowReadsSystemTime(t *testing.T) {
	r := NewRealtimeReader()
	before := time.Now()
	got, err := r.Now()
	after := time.Now()
	if err != nil {
		t.Fatalf("Now() returned an error: %v", err)
	}
	if got.Before(before) || got.After(after) {
		t.Fatalf("Now() = %s, want between %s and %s", got, before, after)
	}
}

// TestRealtimeReaderCloseIsANoOp proves Close never fails, matching a
// reader that never opened anything to release.
func TestRealtimeReaderCloseIsANoOp(t *testing.T) {
	r := NewRealtimeReader()
	if err := r.Close(); err != nil {
		t.Fatalf("Close() returned an error: %v", err)
	}
}
