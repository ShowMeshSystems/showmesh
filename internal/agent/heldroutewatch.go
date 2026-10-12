package agent

import (
	"context"
	"time"
)

// heldRouteRedetectInterval is how often a node holding an audio route
// re-enumerates its interfaces, so an unplugged one is noticed without a
// reconnect or a rebind.
const heldRouteRedetectInterval = 30 * time.Second

// runHeldRouteRedetect calls detect on every tick while the engine holds a
// route. It only detects: it never rebinds the engine or restarts a session.
func runHeldRouteRedetect(ctx context.Context, ticks <-chan time.Time, held func() (audioNodeConfig, bool), detect func()) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
			if node, ok := held(); ok && (node.ProgramRoute != "" || node.LTCRoute != "") {
				detect()
			}
		}
	}
}
