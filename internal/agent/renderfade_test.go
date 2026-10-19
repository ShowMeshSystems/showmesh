package agent

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestRenderSurfaceFadeDecodesAndRampsFromTheCurrentLevel(t *testing.T) {
	now := time.Date(2026, 10, 19, 20, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	o := &renderOperations{}

	res, err := o.fadeSurface(context.Background(), map[string]any{
		"surfaceId": "front", "targetPercent": float64(0), "durationMs": float64(2000),
	}, clock)
	if err != nil || !res.Confirmed {
		t.Fatalf("fade = %+v, %v; want confirmed", res, err)
	}
	src := surfaceHoldBlackSource{o: o, surfaceID: "front"}
	if got := src.Level(now.Add(time.Second)); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("level halfway through a 2 s fade from full = %v, want 0.5", got)
	}

	now = now.Add(time.Second)
	end := now.Add(4 * time.Second).Format(time.RFC3339Nano)
	if _, err := o.fadeSurface(context.Background(), map[string]any{
		"surfaceId": "front", "targetPercent": float64(100), "endsAt": end,
	}, clock); err != nil {
		t.Fatalf("second fade: %v", err)
	}
	if got := src.Level(now); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("a new fade must start from the current level 0.5, got %v", got)
	}
	if got := src.Level(now.Add(4 * time.Second)); got != 1 {
		t.Fatalf("level at endsAt = %v, want 1", got)
	}
	if got := (surfaceHoldBlackSource{o: o, surfaceID: "other"}).Level(now); got != 1 {
		t.Fatalf("an untouched surface reads %v, want full level", got)
	}

	for name, params := range map[string]map[string]any{
		"missing target":  {"surfaceId": "front"},
		"target too high": {"surfaceId": "front", "targetPercent": float64(101)},
		"both end forms":  {"surfaceId": "front", "targetPercent": float64(0), "durationMs": float64(1), "endsAt": end},
		"bad endsAt":      {"surfaceId": "front", "targetPercent": float64(0), "endsAt": "soon"},
		"unknown key":     {"surfaceId": "front", "targetPercent": float64(0), "gain": float64(1)},
	} {
		if _, err := o.fadeSurface(context.Background(), params, clock); err == nil {
			t.Errorf("%s: fade accepted %v", name, params)
		}
	}
}

func TestRenderSurfaceFadeReturnsToFullWhenNoNewerFadeArrives(t *testing.T) {
	now := time.Date(2026, 10, 19, 20, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	o := &renderOperations{}
	if _, err := o.fadeSurface(context.Background(), map[string]any{
		"surfaceId": "front", "targetPercent": float64(0), "durationMs": float64(2000),
	}, clock); err != nil {
		t.Fatal(err)
	}
	src := surfaceHoldBlackSource{o: o, surfaceID: "front"}
	end := now.Add(2 * time.Second)
	if got := src.Level(end.Add(renderFadeDimHold - time.Second)); got != 0 {
		t.Fatalf("level inside the dim hold = %v, want 0", got)
	}
	if got := src.Level(end.Add(renderFadeDimHold)); got != 1 {
		t.Fatalf("level after the dim hold = %v, want full", got)
	}
}
