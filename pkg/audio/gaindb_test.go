package audio

import (
	"math"
	"testing"
)

// The three anchors the decibel ruling is judged against: unity, a halving,
// and the silence floor.
func TestGainFromDbAnchors(t *testing.T) {
	if got := GainFromDb(UnityDb); got != 1.0 {
		t.Fatalf("GainFromDb(0) = %v, want exactly 1.0", got)
	}
	if got := GainFromDb(-6.02); math.Abs(float64(got)-0.5) > 1e-4 {
		t.Fatalf("GainFromDb(-6.02) = %v, want 0.5 within 1e-4", got)
	}
	if got := GainFromDb(SilenceFloorDb); got != 0 {
		t.Fatalf("GainFromDb(%v) = %v, want exactly 0", SilenceFloorDb, got)
	}
	if got := GainFromDb(SilenceFloorDb - 40); got != 0 {
		t.Fatalf("GainFromDb(-100) = %v, want exactly 0", got)
	}
	if got := GainFromDb(math.NaN()); got != 0 {
		t.Fatalf("GainFromDb(NaN) = %v, want exactly 0", got)
	}
	if got := GainFromDb(6.0206); math.Abs(float64(got)-2.0) > 1e-4 {
		t.Fatalf("GainFromDb(6.0206) = %v, want 2.0 within 1e-4", got)
	}
}

func TestGainToDbAnchors(t *testing.T) {
	if got := GainToDb(1.0); got != 0 {
		t.Fatalf("GainToDb(1.0) = %v, want exactly 0", got)
	}
	if got := GainToDb(0.5); math.Abs(got-(-6.0206)) > 1e-3 {
		t.Fatalf("GainToDb(0.5) = %v, want -6.0206 within 1e-3", got)
	}
	if got := GainToDb(0); got != SilenceFloorDb {
		t.Fatalf("GainToDb(0) = %v, want %v", got, SilenceFloorDb)
	}
	if got := GainToDb(Gain(math.Pow(10, -80.0/20))); got != SilenceFloorDb {
		t.Fatalf("GainToDb(-80 dB linear) = %v, want the floor %v", got, SilenceFloorDb)
	}
}

func TestGainDbRoundTrip(t *testing.T) {
	for _, db := range []float64{0, -3, -6.0206, -12, -24, -59.9, 6, 12} {
		back := GainToDb(GainFromDb(db))
		if math.Abs(back-db) > 1e-9 {
			t.Fatalf("round trip of %v dB came back as %v dB", db, back)
		}
	}
}

// A Ceiling never floors to silence: Ceiling.Validate refuses zero on
// purpose, so CeilingFromDb must never hand it one.
func TestCeilingFromDbNeverSilent(t *testing.T) {
	for _, db := range []float64{0, -6.0206, SilenceFloorDb, -120} {
		c := CeilingFromDb(db)
		if err := c.Validate(); err != nil {
			t.Fatalf("CeilingFromDb(%v) = %v, which does not validate: %v", db, c, err)
		}
	}
	if got := CeilingFromDb(UnityDb); got != 1.0 {
		t.Fatalf("CeilingFromDb(0) = %v, want exactly 1.0", got)
	}
	if got := CeilingToDb(CeilingFromDb(12)); math.Abs(got-12) > 1e-9 {
		t.Fatalf("ceiling round trip of 12 dB came back as %v dB", got)
	}
}
