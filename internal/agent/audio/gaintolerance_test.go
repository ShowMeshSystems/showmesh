package audio

import (
	"context"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// float32RoundTripEngine wraps FakeEngine and makes SetGain report the
// gain it was given as it would come back from a real backend holding
// the value in a 32-bit float: close to, but never exactly, the
// requested float64, reproducing the gap gainEpsilon exists to absorb
// that the exact-value FakeEngine cannot.
type float32RoundTripEngine struct{ *FakeEngine }

func (float32RoundTripEngine) Available() (bool, string) { return true, "" }

func (e float32RoundTripEngine) SetGain(ctx context.Context, handle EngineHandle, gain pkgaudio.Gain) (EngineObservation, error) {
	return e.FakeEngine.SetGain(ctx, handle, pkgaudio.Gain(float32(gain)))
}

// mutation target: applyEffectiveGainLocked's obs.Gain comparison
// against effective. A gain that only differs from what was requested
// by a float32 round trip (around 1e-8) must confirm as OutcomeGain, not
// OutcomeUnconfirmable: this is the exact defect reported from
// showmesh-node-01, where every gain change read unconfirmable even
// though the engine honored it.
func TestGainSetToleratesFloat32RoundTripError(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	m := NewManager(float32RoundTripEngine{NewFakeEngine(c.now)}, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Start(ctx, id, "inv-start", 2)

	r := m.GainSet(ctx, id, "inv-gain", 3, pkgaudio.Gain(0.5997910762555093))
	if r.Outcome != pkgaudio.OutcomeGain {
		t.Fatalf("outcome = %+v, want gain (float32 round trip must not read as unconfirmable)", r)
	}
}

// mismatchEngine reports a gain offset well past gainEpsilon from
// whatever was requested, standing in for an engine that genuinely
// refused or altered the requested value.
type mismatchEngine struct{ *FakeEngine }

func (mismatchEngine) Available() (bool, string) { return true, "" }

func (e mismatchEngine) SetGain(ctx context.Context, handle EngineHandle, gain pkgaudio.Gain) (EngineObservation, error) {
	return e.FakeEngine.SetGain(ctx, handle, gain+pkgaudio.Gain(0.05))
}

// A gain the engine genuinely did not honor, well outside gainEpsilon,
// must still report OutcomeUnconfirmable: the tolerance fix must not
// mask a real mismatch.
func TestGainSetStillUnconfirmableForGenuineMismatch(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	m := NewManager(mismatchEngine{NewFakeEngine(c.now)}, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()
	const id = pkgaudio.SessionID("s1")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("x"))
	m.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Start(ctx, id, "inv-start", 2)

	r := m.GainSet(ctx, id, "inv-gain", 3, pkgaudio.Gain(0.5))
	if r.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Fatalf("outcome = %+v, want unconfirmable for a genuine mismatch", r)
	}
}
