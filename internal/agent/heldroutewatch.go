package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/config"
	"github.com/showmeshsystems/showmesh/pkg/capability"
)

// heldRouteRedetectInterval is how often a node with a configured audio
// route lists its interfaces again, so an unplugged one is noticed
// without a reconnect or a rebind.
const heldRouteRedetectInterval = 30 * time.Second

// runHeldRouteRedetect calls detect on every tick while the node has a
// configured route, whether or not its engine is healthy. It only lists:
// it never opens a device, rebinds the engine or restarts a session.
func runHeldRouteRedetect(ctx context.Context, ticks <-chan time.Time, configured func() (audioNodeConfig, bool), detect func()) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
			if node, ok := configured(); ok && (node.ProgramRoute != "" || node.LTCRoute != "") {
				detect()
			}
		}
	}
}

// scheduleHeldRouteRedetection is the watch's listing-only detection. It
// skips the tick while a detection is running, and republishes hello only
// when the advertised routes changed: an unchanged hello would still make
// the coordinator resend this node's audio settings every tick.
func scheduleHeldRouteRedetection(ctx context.Context, pub Publisher, cfg config.Config, bootID string, startedAt time.Time, status *fppConnectHTTPStatus, logger *slog.Logger) {
	if len(cfg.Capabilities) != 0 {
		return
	}
	capabilityGate.triggerIfIdle(func(gen uint64) {
		runHeldRouteRedetection(ctx, pub, cfg, bootID, startedAt, status, logger, gen)
	})
}

func runHeldRouteRedetection(ctx context.Context, pub Publisher, cfg config.Config, bootID string, startedAt time.Time, status *fppConnectHTTPStatus, logger *slog.Logger, gen uint64) {
	detectCtx, cancel := context.WithTimeout(ctx, capabilityDetectionTimeout)
	defer cancel()
	audioCaps, ok := redetectHeldRouteCapabilities(detectCtx)

	cached, have := detectedCapabilityCache.snapshot()
	if !ok || !have || !capabilityGate.isCurrent(gen) {
		return
	}
	if advertisedAudioRoutes(cached) == advertisedAudioRoutes(audioCaps) {
		return
	}
	caps := withAudioCapabilities(cached, audioCaps)
	detectedCapabilityCache.store(caps)

	pubCtx, cancel2 := context.WithTimeout(ctx, advertiseTimeout)
	defer cancel2()
	inboundListener := resolveInboundListener(cfg, status, logger)
	if n, err := publishHello(pubCtx, pub, cfg, bootID, startedAt, caps, inboundListener); err != nil {
		logPublishFailure(logger, "hello (audio route change)", cfg.NodeID, err)
	} else {
		logger.Info("republished hello after an audio route change", "node_id", cfg.NodeID, "capability_count", n)
	}
}

// withAudioCapabilities returns set with its audio capabilities replaced by
// audioCaps and everything else kept as last detected.
func withAudioCapabilities(set, audioCaps capability.Set) capability.Set {
	out := make(capability.Set, 0, len(set)+len(audioCaps))
	for _, c := range set {
		if !strings.HasPrefix(string(c.ID), "audio.") {
			out = append(out, c)
		}
	}
	return append(out, audioCaps...)
}

// advertisedAudioRoutes renders the routes and missing routes set
// advertises in a stable order, for comparing two detection passes.
func advertisedAudioRoutes(set capability.Set) string {
	local, _ := set.Lookup("audio.output.local")
	ltc, _ := set.Lookup("audio.output.ltc")
	b, _ := json.Marshal([]any{
		sortedRouteNames(local.Attributes["routes"]),
		local.Attributes["missingRoutes"],
		sortedRouteNames(ltc.Attributes["routes"]),
	})
	return string(b)
}

func sortedRouteNames(v any) []string {
	names, _ := v.([]string)
	out := append([]string{}, names...)
	sort.Strings(out)
	return out
}
