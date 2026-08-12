package command

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateIdempotencyKeyRejectsEmpty(t *testing.T) {
	if err := ValidateIdempotencyKey(""); !errors.Is(err, ErrEmptyIdempotencyKey) {
		t.Fatalf("ValidateIdempotencyKey(\"\") = %v, want ErrEmptyIdempotencyKey", err)
	}
}

func TestValidateIdempotencyKeyRejectsTooLong(t *testing.T) {
	long := strings.Repeat("a", MaxIdempotencyKeyLength+1)
	if err := ValidateIdempotencyKey(long); !errors.Is(err, ErrIdempotencyKeyTooLong) {
		t.Fatalf("ValidateIdempotencyKey(<%d bytes>) = %v, want ErrIdempotencyKeyTooLong", len(long), err)
	}
}

func TestValidateIdempotencyKeyAcceptsOrdinaryValue(t *testing.T) {
	if err := ValidateIdempotencyKey(NewIdempotencyKey()); err != nil {
		t.Fatalf("ValidateIdempotencyKey(NewIdempotencyKey()) = %v, want nil", err)
	}
	if err := ValidateIdempotencyKey(strings.Repeat("a", MaxIdempotencyKeyLength)); err != nil {
		t.Fatalf("ValidateIdempotencyKey(<exactly max length>) = %v, want nil (boundary must be accepted)", err)
	}
}

// TestNewIdempotencyKeyMintsDistinctValues is this test's name as a claim:
// if NewIdempotencyKey ever regressed to a constant or a poorly-seeded
// generator, two consecutive calls could collide. RES-015 section 7.3's
// whole reason for this function to exist is that FPP supplies nothing to
// derive a key from, so ShowMesh's own minting is the only thing standing
// between two independent invocations and a false replay detection.
func TestNewIdempotencyKeyMintsDistinctValues(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		k := NewIdempotencyKey()
		if err := ValidateIdempotencyKey(k); err != nil {
			t.Fatalf("NewIdempotencyKey produced an invalid key %q: %v", k, err)
		}
		if seen[k] {
			t.Fatalf("NewIdempotencyKey produced a duplicate value %q across 1000 calls", k)
		}
		seen[k] = true
	}
}
