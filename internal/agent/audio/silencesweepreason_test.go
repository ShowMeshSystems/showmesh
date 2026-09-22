package audio

import (
	"context"
	"errors"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// unfinishedSweepEngine is a [FakeEngine] whose final sweep reports that
// it ran out of time, the shape a real engine reports when a branch
// overruns its share of the stop.
type unfinishedSweepEngine struct {
	*FakeEngine
}

func (e *unfinishedSweepEngine) ReleaseAll(ctx context.Context, except ...EngineHandle) ([]EngineHandle, error) {
	released, _ := e.FakeEngine.ReleaseAll(ctx, except...)
	return released, errors.New("gstengine: a branch did not finish its teardown within this stop's per-branch budget")
}

// TestSilenceAllTellsAnOperatorWhichWayItsSweepFellShort proves the three
// outcomes an emergency stop's final sweep can have are reported apart:
// clean, no engine connected, and a shutdown that ran out of time. A stop
// that overran must not be reported as an engine that was never there.
func TestSilenceAllTellsAnOperatorWhichWayItsSweepFellShort(t *testing.T) {
	newManager := func(t *testing.T, engine Engine) *Manager {
		t.Helper()
		c := newClock(time.Now())
		dir := t.TempDir()
		return NewManager(engine, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	}

	t.Run("clean", func(t *testing.T) {
		m := newManager(t, NewFakeEngine(time.Now))
		if _, _, reason := m.SilenceAll(context.Background()); reason != "" {
			t.Fatalf("reason = %q, want a clean sweep", reason)
		}
	})

	t.Run("no engine", func(t *testing.T) {
		m := newManager(t, NewSwitchableEngine())
		_, _, reason := m.SilenceAll(context.Background())
		if reason != SilenceSweepNoEngineReason {
			t.Fatalf("reason = %q, want the not-connected sentence", reason)
		}
	})

	t.Run("out of time", func(t *testing.T) {
		engine := &unfinishedSweepEngine{FakeEngine: NewFakeEngine(time.Now)}
		m := newManager(t, engine)
		id := pkgaudio.SessionID("overrunning")
		ref := writeTestAsset(t, m.assetDir, "overrunning.wav", "overrunning-asset", []byte("bytes"))
		startPlaying(t, m, context.Background(), id, ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

		_, _, reason := m.SilenceAll(context.Background())
		if reason != SilenceSweepUnfinishedReason {
			t.Fatalf("reason = %q, want the out-of-time sentence", reason)
		}
	})
}
