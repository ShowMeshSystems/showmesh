package store

import (
	"context"
	"testing"
	"time"
)

// TestPendingWeatherDelayDecisionRoundTrip proves the pending decision row
// survives a set/get/clear cycle and reads as absent before any set and
// after a clear.
func TestPendingWeatherDelayDecisionRoundTrip(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, ok, err := st.GetPendingWeatherDelayDecision(ctx); err != nil || ok {
		t.Fatalf("GetPendingWeatherDelayDecision before any set = (ok=%v, err=%v), want ok=false, err=nil", ok, err)
	}

	asked := mustTime(t, "2026-09-19T20:00:00Z")
	deadline := mustTime(t, "2026-09-19T20:00:30Z")
	rec := PendingWeatherDelayDecisionRecord{
		ID: "dec-1", Source: "nws", Reason: "A severe thunderstorm warning is in effect.",
		Question: "delay", DefaultAction: "delay", AskedAt: asked, Deadline: deadline,
		WarningKey: "nws:warning:Severe Thunderstorm Warning",
	}
	if stored, err := st.SetPendingWeatherDelayDecision(ctx, rec); err != nil || !stored {
		t.Fatalf("SetPendingWeatherDelayDecision = (stored=%v, err=%v), want stored=true", stored, err)
	}

	got, ok, err := st.GetPendingWeatherDelayDecision(ctx)
	if err != nil || !ok {
		t.Fatalf("GetPendingWeatherDelayDecision after set = (ok=%v, err=%v), want ok=true, err=nil", ok, err)
	}
	if got != rec {
		t.Fatalf("GetPendingWeatherDelayDecision = %+v, want %+v", got, rec)
	}

	// A second set never displaces the first: whichever source asked
	// first keeps the question and its own deadline.
	rec2 := rec
	rec2.ID, rec2.Question, rec2.DefaultAction = "dec-2", "delayOrCancel", "cancelNight"
	if stored, err := st.SetPendingWeatherDelayDecision(ctx, rec2); err != nil || stored {
		t.Fatalf("SetPendingWeatherDelayDecision over a pending one = (stored=%v, err=%v), want stored=false", stored, err)
	}
	got, ok, err = st.GetPendingWeatherDelayDecision(ctx)
	if err != nil || !ok || got.ID != "dec-1" {
		t.Fatalf("GetPendingWeatherDelayDecision after a losing set = %+v, ok=%v, err=%v, want dec-1", got, ok, err)
	}

	// Clearing under a different id claims nothing and removes nothing.
	if claimed, err := st.ClearPendingWeatherDelayDecision(ctx, "dec-2"); err != nil || claimed {
		t.Fatalf("ClearPendingWeatherDelayDecision under the wrong id = (claimed=%v, err=%v), want claimed=false", claimed, err)
	}
	if _, ok, err := st.GetPendingWeatherDelayDecision(ctx); err != nil || !ok {
		t.Fatalf("GetPendingWeatherDelayDecision after a wrong-id clear = (ok=%v, err=%v), want it still pending", ok, err)
	}

	if claimed, err := st.ClearPendingWeatherDelayDecision(ctx, "dec-1"); err != nil || !claimed {
		t.Fatalf("ClearPendingWeatherDelayDecision = (claimed=%v, err=%v), want claimed=true", claimed, err)
	}
	if _, ok, err := st.GetPendingWeatherDelayDecision(ctx); err != nil || ok {
		t.Fatalf("GetPendingWeatherDelayDecision after clear = (ok=%v, err=%v), want ok=false, err=nil", ok, err)
	}

	// Exactly one caller claims a decision: a second clear of the same id
	// removes nothing, which is what makes an answer and its own deadline
	// race safely.
	if claimed, err := st.ClearPendingWeatherDelayDecision(ctx, "dec-1"); err != nil || claimed {
		t.Fatalf("ClearPendingWeatherDelayDecision (already claimed) = (claimed=%v, err=%v), want claimed=false", claimed, err)
	}
}

// TestWeatherDelayTriggerSuppressionRoundTrip proves one source's
// suppression row survives a set/get cycle and a second source gets its
// own independent row.
func TestWeatherDelayTriggerSuppressionRoundTrip(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, ok, err := st.GetWeatherDelayTriggerSuppression(ctx, "nws"); err != nil || ok {
		t.Fatalf("GetWeatherDelayTriggerSuppression before any set = (ok=%v, err=%v), want ok=false, err=nil", ok, err)
	}

	until := mustTime(t, "2026-09-19T20:30:00Z")
	rec := WeatherDelayTriggerSuppressionRecord{Source: "nws", WarningKey: "nws:warning:Severe Thunderstorm Warning", Until: until}
	if err := st.SetWeatherDelayTriggerSuppression(ctx, rec); err != nil {
		t.Fatalf("SetWeatherDelayTriggerSuppression: %v", err)
	}
	got, ok, err := st.GetWeatherDelayTriggerSuppression(ctx, "nws")
	if err != nil || !ok || got != rec {
		t.Fatalf("GetWeatherDelayTriggerSuppression = %+v, ok=%v, err=%v, want %+v, true, nil", got, ok, err, rec)
	}

	if _, ok, err := st.GetWeatherDelayTriggerSuppression(ctx, "lightning-01"); err != nil || ok {
		t.Fatalf("GetWeatherDelayTriggerSuppression for a different source = (ok=%v, err=%v), want ok=false, err=nil", ok, err)
	}

	rec.Until = until.Add(30 * time.Minute)
	if err := st.SetWeatherDelayTriggerSuppression(ctx, rec); err != nil {
		t.Fatalf("SetWeatherDelayTriggerSuppression (overwrite): %v", err)
	}
	got, ok, err = st.GetWeatherDelayTriggerSuppression(ctx, "nws")
	if err != nil || !ok || !got.Until.Equal(rec.Until) {
		t.Fatalf("GetWeatherDelayTriggerSuppression after overwrite = %+v, ok=%v, err=%v, want until %v", got, ok, err, rec.Until)
	}
}
