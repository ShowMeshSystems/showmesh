package agent

import (
	"context"
	"sync"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// cueStartsInFlight is shared by the MultiSync START path and cue.activate,
// so whichever arrives second for the same song waits for the first start
// instead of loading and starting the song a second time.
var cueStartsInFlight = newCueStartFlights()

type cueStartFlight struct {
	mediaIdentity string
	done          chan struct{}
}

// cueStartFlights holds the one start in progress per audio session.
type cueStartFlights struct {
	mu      sync.Mutex
	flights map[pkgaudio.SessionID]*cueStartFlight
}

func newCueStartFlights() *cueStartFlights {
	return &cueStartFlights{flights: make(map[pkgaudio.SessionID]*cueStartFlight)}
}

// claim marks a start of mediaIdentity on id as in progress and returns the
// release the caller must run once that start has finished. With wait set, a
// start of the same media already in progress is waited out first; a start
// of different media is never waited for. ok is false only when ctx ends
// while waiting.
func (f *cueStartFlights) claim(ctx context.Context, id pkgaudio.SessionID, mediaIdentity string, wait bool) (release func(), ok bool) {
	for {
		f.mu.Lock()
		current, busy := f.flights[id]
		if !wait || !busy || current.mediaIdentity != mediaIdentity {
			flight := &cueStartFlight{mediaIdentity: mediaIdentity, done: make(chan struct{})}
			f.flights[id] = flight
			f.mu.Unlock()
			return func() { f.finish(id, flight) }, true
		}
		f.mu.Unlock()
		select {
		case <-current.done:
		case <-ctx.Done():
			return nil, false
		}
	}
}

func (f *cueStartFlights) finish(id pkgaudio.SessionID, flight *cueStartFlight) {
	f.mu.Lock()
	if f.flights[id] == flight {
		delete(f.flights, id)
	}
	f.mu.Unlock()
	close(flight.done)
}
