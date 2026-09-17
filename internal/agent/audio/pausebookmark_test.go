package audio

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// TestPauseWithBookmarkReadsItsOwnBookmarkAtomically proves R1's own
// contract: a successful pause's bookmark evidence is read from the
// exact same locked operation as the pause itself, never a second,
// separately locked read a concurrent Clear for the same session could
// land inside of. Known must be true whenever Outcome is OutcomePosition
// (a real bookmark was just produced), across many iterations racing a
// concurrent Clear, under -race.
func TestPauseWithBookmarkReadsItsOwnBookmarkAtomically(t *testing.T) {
	const iterations = 200
	for iter := 0; iter < iterations; iter++ {
		dir := t.TempDir()
		c := newClock(time.Now())
		m := newTestManagerInDir(dir, c)
		ctx := context.Background()
		id := pkgaudio.SessionID(fmt.Sprintf("bed-race-%d", iter))

		ref := writeTestAsset(t, dir, fmt.Sprintf("race-%d.wav", iter), fmt.Sprintf("asset-race-%d", iter), []byte("content"))
		startPlaying(t, m, ctx, id, ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

		var wg sync.WaitGroup
		var result PauseResult
		wg.Add(2)
		go func() {
			defer wg.Done()
			result = m.PauseWithBookmark(ctx, id, pkgaudio.InvocationID(fmt.Sprintf("pause-%d", iter)), 3)
		}()
		go func() {
			defer wg.Done()
			m.Clear(ctx, id, pkgaudio.InvocationID(fmt.Sprintf("clear-%d", iter)), 4)
		}()
		wg.Wait()

		if result.Outcome.Outcome == pkgaudio.OutcomePosition && !result.Known {
			t.Fatalf("iter %d: pause succeeded (outcome=%s) but its own result reports bookmarkKnown=false, want true", iter, result.Outcome.Outcome)
		}
	}
}
