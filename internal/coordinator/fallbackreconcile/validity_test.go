package fallbackreconcile

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/fallbackcompile"
)

// A coordinator can be lost at any moment. Whenever that is, the program
// it last published must still have long enough left to carry the show
// that was playing, and the rest of that night, from the plugin: its end
// is the fallback's cutoff. Thirty hours of passes on the default interval,
// with content that never changes, is the case most likely to let a copy
// run down.
func TestThePublishedProgramAlwaysHasAtLeastHalfItsValidityLeft(t *testing.T) {
	st, now := newPublishableFixture(t)
	svc := NewService(st, fakeSigner{}, nil, nil, DefaultInterval)
	svc.now = func() time.Time { return now }

	const nightsWorth = 11 * time.Hour
	if floor := fallbackcompile.ProgramTTL/2 - DefaultInterval; floor < nightsWorth {
		t.Fatalf("a copy held when the coordinator is lost is only promised %s, want at least %s", floor, nightsWorth)
	}

	shortest := fallbackcompile.ProgramTTL
	var firstPackage string
	for end := now.Add(30 * time.Hour); now.Before(end); now = now.Add(DefaultInterval) {
		svc.reconcileOnce(context.Background())
		rec, err := st.GetFallbackProgram(context.Background(), testInstanceUUID)
		if err != nil {
			t.Fatalf("GetFallbackProgram: %v", err)
		}
		if firstPackage == "" {
			firstPackage = rec.PackageID
		}
		if rec.PackageID != firstPackage {
			t.Fatalf("unchanged content was published as a new package %q after %q", rec.PackageID, firstPackage)
		}
		// The coordinator may stop just before its next pass.
		if left := rec.ExpiresAt.Sub(now.Add(DefaultInterval)); left < shortest {
			shortest = left
		}
	}
	if floor := fallbackcompile.ProgramTTL/2 - DefaultInterval; shortest < floor {
		t.Fatalf("at its lowest the published program had %s left, want at least %s", shortest, floor)
	}
}
