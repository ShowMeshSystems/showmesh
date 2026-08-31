package api

import (
	"sync"
	"testing"
	"time"
)

// Pure unit coverage for [emergencyStopArmStore]: no HTTP, no store, no
// identity — the deliberate-intent gate's own state machine in isolation.

func TestEmergencyStopArmStoreConsumeRequiresPriorArm(t *testing.T) {
	s := newEmergencyStopArmStore()
	now := time.Now()
	if got := s.consume("principal-1", "any-token", now); got != emergencyStopArmConsumeNotArmed {
		t.Fatalf("consume with no prior arm = %v, want emergencyStopArmConsumeNotArmed", got)
	}
}

func TestEmergencyStopArmStoreConsumeRejectsWrongToken(t *testing.T) {
	s := newEmergencyStopArmStore()
	now := time.Now()
	token, _, err := s.arm("principal-1", now)
	if err != nil {
		t.Fatalf("arm returned %v", err)
	}
	if got := s.consume("principal-1", token+"-wrong", now); got != emergencyStopArmConsumeNotArmed {
		t.Fatalf("consume with wrong token = %v, want emergencyStopArmConsumeNotArmed", got)
	}
	// The real token is still live and unconsumed after a wrong guess.
	if got := s.consume("principal-1", token, now); got != emergencyStopArmConsumeOK {
		t.Fatalf("consume with the real token after a wrong guess = %v, want emergencyStopArmConsumeOK", got)
	}
}

func TestEmergencyStopArmStoreConsumeSucceedsOnce(t *testing.T) {
	s := newEmergencyStopArmStore()
	now := time.Now()
	token, _, err := s.arm("principal-1", now)
	if err != nil {
		t.Fatalf("arm returned %v", err)
	}
	if got := s.consume("principal-1", token, now); got != emergencyStopArmConsumeOK {
		t.Fatalf("first consume = %v, want emergencyStopArmConsumeOK", got)
	}
	if got := s.consume("principal-1", token, now); got != emergencyStopArmConsumeAlreadyConsumed {
		t.Fatalf("second consume of the SAME token = %v, want emergencyStopArmConsumeAlreadyConsumed (this is the redelivery/retry protection)", got)
	}
}

func TestEmergencyStopArmStoreExpiredTokenIsRefused(t *testing.T) {
	s := newEmergencyStopArmStore()
	armedAt := time.Now()
	token, expiresAt, err := s.arm("principal-1", armedAt)
	if err != nil {
		t.Fatalf("arm returned %v", err)
	}
	afterExpiry := expiresAt.Add(time.Millisecond)
	if got := s.consume("principal-1", token, afterExpiry); got != emergencyStopArmConsumeNotArmed {
		t.Fatalf("consume after expiry = %v, want emergencyStopArmConsumeNotArmed", got)
	}
}

func TestEmergencyStopArmStoreConsumeAtExactExpiryIsRefused(t *testing.T) {
	s := newEmergencyStopArmStore()
	armedAt := time.Now()
	token, expiresAt, err := s.arm("principal-1", armedAt)
	if err != nil {
		t.Fatalf("arm returned %v", err)
	}
	// now.Before(expiresAt) is false exactly AT expiresAt: the token must
	// not still be fireable at the instant it expires.
	if got := s.consume("principal-1", token, expiresAt); got != emergencyStopArmConsumeNotArmed {
		t.Fatalf("consume exactly at expiresAt = %v, want emergencyStopArmConsumeNotArmed", got)
	}
}

// Re-arming before the previous token is consumed invalidates it
// immediately: at most one live token per principal (orchestrator-endorsed
// hardening).
func TestEmergencyStopArmStoreReArmingInvalidatesThePreviousToken(t *testing.T) {
	s := newEmergencyStopArmStore()
	now := time.Now()
	firstToken, _, err := s.arm("principal-1", now)
	if err != nil {
		t.Fatalf("first arm returned %v", err)
	}
	secondToken, _, err := s.arm("principal-1", now)
	if err != nil {
		t.Fatalf("second arm returned %v", err)
	}
	if firstToken == secondToken {
		t.Fatal("arming twice produced the same token")
	}
	if got := s.consume("principal-1", firstToken, now); got != emergencyStopArmConsumeNotArmed {
		t.Fatalf("consume of the FIRST (superseded) token = %v, want emergencyStopArmConsumeNotArmed", got)
	}
	if got := s.consume("principal-1", secondToken, now); got != emergencyStopArmConsumeOK {
		t.Fatalf("consume of the SECOND (current) token = %v, want emergencyStopArmConsumeOK", got)
	}
}

// Two principals' own tokens never interfere.
func TestEmergencyStopArmStoreIsPerPrincipal(t *testing.T) {
	s := newEmergencyStopArmStore()
	now := time.Now()
	tokenA, _, err := s.arm("principal-a", now)
	if err != nil {
		t.Fatalf("arm(a) returned %v", err)
	}
	tokenB, _, err := s.arm("principal-b", now)
	if err != nil {
		t.Fatalf("arm(b) returned %v", err)
	}
	if got := s.consume("principal-b", tokenA, now); got != emergencyStopArmConsumeNotArmed {
		t.Fatalf("principal-b consuming principal-a's own token = %v, want emergencyStopArmConsumeNotArmed", got)
	}
	if got := s.consume("principal-a", tokenA, now); got != emergencyStopArmConsumeOK {
		t.Fatalf("principal-a consuming its own token = %v, want emergencyStopArmConsumeOK", got)
	}
	if got := s.consume("principal-b", tokenB, now); got != emergencyStopArmConsumeOK {
		t.Fatalf("principal-b consuming its own token = %v, want emergencyStopArmConsumeOK", got)
	}
}

// The compare-and-swap property: of N goroutines racing to consume the
// IDENTICAL token, exactly one may see emergencyStopArmConsumeOK.
func TestEmergencyStopArmStoreConcurrentConsumeOnlyOneWins(t *testing.T) {
	s := newEmergencyStopArmStore()
	now := time.Now()
	token, _, err := s.arm("principal-1", now)
	if err != nil {
		t.Fatalf("arm returned %v", err)
	}

	const racers = 50
	var wg sync.WaitGroup
	var mu sync.Mutex
	oks := 0
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func() {
			defer wg.Done()
			if s.consume("principal-1", token, now) == emergencyStopArmConsumeOK {
				mu.Lock()
				oks++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if oks != 1 {
		t.Fatalf("%d of %d concurrent consumers saw OK, want exactly 1 — a lost compare-and-swap race must never let two fires both win", oks, racers)
	}
}
