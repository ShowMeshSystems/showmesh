package assetstore

import (
	"testing"
	"time"
)

// coordinatorReadTimeout mirrors internal/coordinator/httpapi/server.go's
// ReadTimeout (line 90). It is a literal here rather than an import
// because httpapi does not export that value — the same trade-off that
// file's own notFoundAPIVersionHeaderValue doc comment documents for the
// mirror in the other direction.
const coordinatorReadTimeout = 10 * time.Second

func TestUploadBudgetMonotonic(t *testing.T) {
	sizes := []int64{0, 1, 1024, 1 << 20, 1 << 30, DefaultMaxUploadBytes}
	prev := UploadBudget(sizes[0])
	for _, s := range sizes[1:] {
		got := UploadBudget(s)
		if got < prev {
			t.Fatalf("UploadBudget(%d) = %v is less than the budget for a smaller size (%v)", s, got, prev)
		}
		prev = got
	}
}

func TestUploadBudgetRespectsCeiling(t *testing.T) {
	huge := DefaultMaxUploadBytes * 1000
	got := UploadBudget(huge)
	if got != uploadBudgetCeiling {
		t.Fatalf("UploadBudget(%d) = %v, want the %v ceiling", huge, got, uploadBudgetCeiling)
	}
}

func TestUploadBudgetComfortablyExceedsServerReadTimeout(t *testing.T) {
	for _, s := range []int64{0, 1, DefaultMaxUploadBytes} {
		got := UploadBudget(s)
		if got <= coordinatorReadTimeout {
			t.Fatalf("UploadBudget(%d) = %v, want > server ReadTimeout %v", s, got, coordinatorReadTimeout)
		}
	}
}

func TestUploadBudgetClampsNegativeSize(t *testing.T) {
	if got, want := UploadBudget(-1), UploadBudget(0); got != want {
		t.Fatalf("UploadBudget(-1) = %v, want the size-0 budget %v", got, want)
	}
}
