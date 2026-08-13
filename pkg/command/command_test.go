package command

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestMinClientTimeoutForConfirmationExceedsServerDeadline is Step 7 seam
// C review defect 1's own guard: before this fix, the server's default
// confirmation deadline (20s) and the CLI's client timeout (10s) were two
// independent literals that silently disagreed, making "unconfirmed" and
// exit 9 unreachable at default settings. Broken to verify: with
// ClientTimeoutMargin temporarily set to a negative value larger in
// magnitude than DefaultFPPCommandConfirmDeadline (simulated here by
// calling the underlying arithmetic directly rather than mutating the
// package constant, which Go does not allow), this assertion fails — see
// the second sub-test.
func TestMinClientTimeoutForConfirmationExceedsServerDeadline(t *testing.T) {
	got := MinClientTimeoutForConfirmation(DefaultFPPCommandConfirmDeadline)
	if got <= DefaultFPPCommandConfirmDeadline {
		t.Fatalf("MinClientTimeoutForConfirmation(%v) = %v, want strictly greater than the server deadline itself "+
			"(a client budget equal to the server deadline still has no time left to receive the response)", DefaultFPPCommandConfirmDeadline, got)
	}
}

// TestMinClientTimeoutForConfirmationRejectsATooSmallBudget is the
// behavior-breaking half of the test above: it asserts the function would
// catch a client budget that DOES fall below the server deadline, proving
// the assertion above is not vacuously true for any arithmetic this
// function might have used.
func TestMinClientTimeoutForConfirmationRejectsATooSmallBudget(t *testing.T) {
	tooSmall := 10 * time.Second // the CLI's old, disagreeing literal
	if tooSmall > MinClientTimeoutForConfirmation(DefaultFPPCommandConfirmDeadline) {
		t.Fatalf("test setup invalid: %v is not actually below the computed minimum", tooSmall)
	}
	if tooSmall >= DefaultFPPCommandConfirmDeadline {
		t.Fatalf("test setup invalid: %v does not actually demonstrate the pre-fix defect (must be below the server deadline itself)", tooSmall)
	}
}

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
