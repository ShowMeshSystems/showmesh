package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/audiosched"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

func validEvidence(nowNs int64) map[string]any {
	return map[string]any{
		"mediaClockValid": true, "mediaClockNowNs": json.Number(strconv.FormatInt(nowNs, 10)),
	}
}

// TestSelectAudioStartInstantRecoversAPanicInRead proves a panicking read
// for one node never takes the whole selection down with it (an
// unrecovered panic in the launched goroutine would crash the whole
// coordinator process): the panicking node's own reading is recorded as
// absent evidence, and a valid reading on the clock holder still lets
// Select succeed. The panic is also reported back, not silently absorbed.
func TestSelectAudioStartInstantRecoversAPanicInRead(t *testing.T) {
	const now = int64(1_700_000_000_000_000_000)
	settings := config.AudioSettingsPayload{ScheduledStartDeliveryBoundMs: 100, ScheduledStartMarginMs: 50}
	read := func(_ context.Context, nodeID string) (AudioStartInstantReading, error) {
		if nodeID == "panics" {
			panic("boom")
		}
		return AudioStartInstantReading{HoldsMediaClock: true, Evidence: validEvidence(now)}, nil
	}
	sel, failures, err := SelectAudioStartInstant(context.Background(), []string{"panics", "holder"}, settings, read)
	if err != nil {
		t.Fatalf("Select = %v, want a usable instant derived from the non-panicking clock holder", err)
	}
	if sel.ClockNodeID != "holder" {
		t.Fatalf("ClockNodeID = %q, want %q", sel.ClockNodeID, "holder")
	}
	if len(failures) != 1 || failures[0].NodeID != "panics" {
		t.Fatalf("failures = %+v, want exactly one entry naming node %q", failures, "panics")
	}
	if !strings.Contains(failures[0].Err.Error(), "boom") {
		t.Fatalf("failures[0].Err = %q, want it to carry the panic's own message", failures[0].Err)
	}
}

// TestSelectAudioStartInstantCarriesAPanicReasonWhenTheHolderPanics
// proves the panicking node's own reason reaches the eventual refusal
// when the panic happens on the CLOCK HOLDER, provided the caller's own
// read (as cueactivationschedule.go's own read closure does) preserves
// HoldsMediaClock across the panic rather than losing it: the reported
// reason then names the panic, never a generic "no evidence" placeholder
// that would leave an operator guessing.
func TestSelectAudioStartInstantCarriesAPanicReasonWhenTheHolderPanics(t *testing.T) {
	settings := config.AudioSettingsPayload{ScheduledStartDeliveryBoundMs: 100, ScheduledStartMarginMs: 50}
	read := func(_ context.Context, nodeID string) (reading AudioStartInstantReading, err error) {
		if nodeID != "holder" {
			return AudioStartInstantReading{}, nil
		}
		holdsClock := true
		defer func() {
			if r := recover(); r != nil {
				reading = AudioStartInstantReading{HoldsMediaClock: holdsClock}
				err = fmt.Errorf("panic reading node %q: %v", nodeID, r)
			}
		}()
		panic("boom: no engine bound")
	}
	_, failures, err := SelectAudioStartInstant(context.Background(), []string{"holder", "other"}, settings, read)
	var noClock *audiosched.ErrNoUsableClock
	if !errors.As(err, &noClock) {
		t.Fatalf("err = %v, want *audiosched.ErrNoUsableClock", err)
	}
	if !strings.Contains(err.Error(), "boom: no engine bound") {
		t.Fatalf("err = %q, want the holder's own panic message, not a generic reason", err)
	}
	if len(failures) != 1 || failures[0].NodeID != "holder" {
		t.Fatalf("failures = %+v, want exactly one entry naming the holder", failures)
	}
}

// TestSelectAudioStartInstantFoldsOnlyTheHoldersOwnProbeElapsed proves
// MANAGER DECISION 2's own scope: the delivery bound widens by the CLOCK
// HOLDER's own measured probe span, never a non-holder's larger one, and
// never a failed read's (zero, by construction).
func TestSelectAudioStartInstantFoldsOnlyTheHoldersOwnProbeElapsed(t *testing.T) {
	const now = int64(1_700_000_000_000_000_000)
	const holderElapsed = 120 * time.Millisecond
	const nonHolderElapsed = 390 * time.Millisecond // larger, must be ignored
	settings := config.AudioSettingsPayload{ScheduledStartDeliveryBoundMs: 100, ScheduledStartMarginMs: 0}
	read := func(_ context.Context, nodeID string) (AudioStartInstantReading, error) {
		switch nodeID {
		case "holder":
			return AudioStartInstantReading{HoldsMediaClock: true, Evidence: validEvidence(now), ProbeElapsed: holderElapsed}, nil
		case "failed":
			return AudioStartInstantReading{}, errors.New("dispatch failed")
		default:
			return AudioStartInstantReading{Evidence: validEvidence(now), ProbeElapsed: nonHolderElapsed}, nil
		}
	}
	sel, failures, err := SelectAudioStartInstant(context.Background(), []string{"holder", "slow-non-holder", "failed"}, settings, read)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(failures) != 1 || failures[0].NodeID != "failed" {
		t.Fatalf("failures = %+v, want exactly one entry naming %q", failures, "failed")
	}
	wantBoundNs := int64(100*1_000_000) + holderElapsed.Nanoseconds()
	if sel.DeliveryBoundNs != wantBoundNs {
		t.Fatalf("DeliveryBoundNs = %d, want exactly %d (configured bound + the HOLDER's own %v, never the slower non-holder's %v)",
			sel.DeliveryBoundNs, wantBoundNs, holderElapsed, nonHolderElapsed)
	}
}

// TestSelectAudioStartInstantClampsTheHoldersOwnProbeElapsed proves the
// clamp: even a holder whose own probe span is somehow measured past
// scheduleProbeMaxDeliveryContribution never widens the delivery bound
// past that ceiling.
func TestSelectAudioStartInstantClampsTheHoldersOwnProbeElapsed(t *testing.T) {
	const now = int64(1_700_000_000_000_000_000)
	oversized := scheduleProbeMaxDeliveryContribution * 10
	settings := config.AudioSettingsPayload{ScheduledStartDeliveryBoundMs: 100, ScheduledStartMarginMs: 0}
	read := func(_ context.Context, nodeID string) (AudioStartInstantReading, error) {
		return AudioStartInstantReading{HoldsMediaClock: true, Evidence: validEvidence(now), ProbeElapsed: oversized}, nil
	}
	sel, _, err := SelectAudioStartInstant(context.Background(), []string{"holder"}, settings, read)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	wantBoundNs := int64(100*1_000_000) + scheduleProbeMaxDeliveryContribution.Nanoseconds()
	if sel.DeliveryBoundNs != wantBoundNs {
		t.Fatalf("DeliveryBoundNs = %d, want exactly %d: an oversized measured span of %v must be clamped to the %v ceiling",
			sel.DeliveryBoundNs, wantBoundNs, oversized, scheduleProbeMaxDeliveryContribution)
	}
}
