package resolume

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// syncBuffer is a concurrency-safe bytes.Buffer: a test's own goroutine
// reads it (via String()) while a slog handler writes to it from whatever
// goroutine is running the code under test (e.g. Adapter.Run's own
// goroutine). A bare bytes.Buffer is not safe for that, and go test -race
// catches the difference immediately.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// loadTestdata reads a file from testdata/, failing the test on any error.
// Mirrors internal/coordinator/collector/fpp/testserver_test.go's helper
// of the same name.
func loadTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("loadTestdata(%q): %v", name, err)
	}
	return b
}

// fixedClock returns a func() time.Time that always reads *now — the same
// pattern internal/coordinator/collector/fpp/fpp_test.go uses so backoff
// and staleness assertions never depend on real wall-clock time.
func fixedClock(now *time.Time) func() time.Time {
	return func() time.Time { return *now }
}

// findSignal locates the observation for sig in obs, failing the test if
// it is not present at all. Mirrors
// internal/coordinator/collector/fpp/fpp_test.go's helper.
func findSignal(t *testing.T, obs []observation.Observation, sig observation.SignalID) observation.Observation {
	t.Helper()
	for _, o := range obs {
		if o.Signal == sig {
			return o
		}
	}
	t.Fatalf("signal %q not found among %d observation(s)", sig, len(obs))
	return observation.Observation{}
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
