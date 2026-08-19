// Package agent wires the node agent's config, MQTT connection (hello,
// Last Will, health heartbeat), command handling (including asset sync and
// Track B's render pipeline supervision), and clean-shutdown ordering
// together and runs them until shutdown.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	"github.com/showmeshsystems/showmesh/internal/agent/config"
	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
	"github.com/showmeshsystems/showmesh/internal/version"
	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// shutdownTimeout bounds the clean-shutdown path (offline publish followed
// by disconnect), matching internal/coordinator.Run's own shutdown bound:
// a hung publish or disconnect must not block process exit indefinitely.
const shutdownTimeout = 10 * time.Second

// audioSessionWatchInterval bounds how promptly a Playing session's
// natural completion is noticed and advanced — see audio.Manager.
// RunWatcher. SHOWMESH HYPOTHESIS, NOT MEASURED: no bench data exists yet
// for what latency a real show needs between one track ending and the
// next starting; short enough that a two-second FakeEngine test playlist
// (see internal/agent/audio's own tests) advances within a handful of
// ticks.
const audioSessionWatchInterval = 500 * time.Millisecond

// Run loads config, connects to the MQTT broker with a registered Last
// Will, publishes hello and online=true on every connect (including
// reconnects), runs the health heartbeat, and blocks until a shutdown
// signal is received — at which point it publishes online=false and
// disconnects, in that order (see shutdownCleanly). The MQTT connection's
// own context is deliberately kept separate from the shutdown-signal
// context so that ordering holds up in practice, not just on paper — see
// the comment on connCtx below. Run returns a process exit code.
func Run() int {
	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "showmesh-agent: config error: %v\n", err)
		return 1
	}

	logger := newLogger(cfg.LogLevel)

	bootID := uuid.NewString()
	startedAt := time.Now().UTC()

	logger.Info("starting showmesh-agent",
		"version", version.Version,
		"commit", version.Commit,
		"build_date", version.BuildDate,
		"node_id", cfg.NodeID,
		"node_label", cfg.NodeLabel,
		"mqtt_broker", cfg.MQTTBroker,
		"mqtt_client_id", cfg.MQTTClientID,
		"mqtt_username_set", cfg.MQTTUsername != "",
		"log_level", cfg.LogLevel,
		"boot_id", bootID,
		"capability_count", len(cfg.Capabilities),
	)

	// connCtx bounds the MQTT connection manager's lifetime and is
	// DELIBERATELY NOT sigCtx (below), even though sigCtx is what tells this
	// function to start shutting down. autopaho's connection manager treats
	// cancellation of the context passed to newMQTTConn (which it forwards
	// into autopaho.NewConnection) as "tear down now": it immediately sends
	// a normal MQTT DISCONNECT (reason code 0) and, per the MQTT spec, a
	// normal DISCONNECT tells the broker to DISCARD the registered Will —
	// see shutdown.go's shutdownCleanly for why that is exactly backwards
	// here. If connCtx were sigCtx, then the instant SIGTERM arrived,
	// autopaho would race ahead and disconnect on its own, discarding the
	// Will, before shutdownCleanly below ever gets a chance to publish the
	// retained "online: false" message that is supposed to stand in for it.
	// That is precisely the bug this comment exists to prevent someone from
	// reintroducing by "simplifying" this back to one context: connCtx must
	// outlive the signal, and the ONLY thing allowed to end it is the
	// explicit, ordered conn.Disconnect call inside shutdownCleanly, called
	// after the offline publish has already gone out. See
	// TestAgentCleanShutdownGoesOfflinePromptly in
	// test/integration/lifecycle_test.go, which fails against this file
	// when that ordering is violated.
	connCtx, cancelConn := context.WithCancel(context.Background())
	defer cancelConn() // backstop for early-return paths only; normal shutdown ends connCtx via conn.Disconnect inside shutdownCleanly, not via this cancel.

	sigCtx, stopSignal := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignal()

	// heartbeatConnected is buffered so newMQTTConn's OnConnectionUp (which
	// can fire before the heartbeat goroutine below has started selecting
	// on it) never has its non-blocking send silently land on a channel
	// nobody will ever read from; see runHeartbeat's and newMQTTConn's doc
	// comments for why this exists at all.
	heartbeatConnected := make(chan struct{}, 1)

	// A node that has never held an asset still needs a real, empty
	// directory: enumerateAssets reports a missing AssetDir as incomplete,
	// which the coordinator maps to ManifestUnknown, and assetsync only
	// ever dispatches to ManifestNotReady — so a fresh node's manifest
	// state stays unknown forever and it can never receive its first
	// asset. Not fatal, matching sweepAssetStaging below: a permission
	// failure here still leaves the rest of the agent's capabilities
	// (heartbeat, hello, command handling) usable, and enumerateAssets
	// will honestly keep reporting incomplete until the directory exists.
	if err := os.MkdirAll(cfg.AssetDir, 0o755); err != nil {
		logger.Warn("failed to create asset directory at startup", "asset_dir", cfg.AssetDir, "error", err)
	}

	// A staging file left behind by a previous, interrupted process run is
	// never a partially-usable asset; sweep it before anything else touches
	// AssetDir. Not fatal: an agent that cannot clean its own staging area
	// still has a real, possibly-usable set of already-verified assets, and
	// exiting over it would be exactly the kind of avoidable stoppage this
	// project's system goal forbids.
	if err := sweepAssetStaging(cfg.AssetDir); err != nil {
		logger.Warn("failed to sweep asset staging directory at startup", "asset_dir", cfg.AssetDir, "error", err)
	}

	// assetFetchTrigger is buffered for the same non-blocking-send reason as
	// heartbeatConnected: command.go signals it after a completed
	// asset.fetch, and the inventory goroutine below may not have started
	// selecting on it yet.
	assetFetchTrigger := make(chan struct{}, 1)

	// renderTrigger is assetFetchTrigger's counterpart for render.*
	// operations; see command.go's identical non-blocking-send reasoning.
	renderTrigger := make(chan struct{}, 1)

	// sup owns every supervised render pipeline for this node's whole
	// process life — constructed here, outside newMQTTConn, for the same
	// reason cmdHandler is: it must survive a broker reconnect, since a
	// running pipeline must keep rendering through one (Track B build
	// contract standing rule 1: the node renders, the coordinator watches,
	// and a render node whose broker has gone away keeps rendering).
	sup := pipeline.NewSupervisor(time.Now, nil, logger)
	assignmentStore := pipeline.NewAssignmentStore(cfg.AssetDir)

	// timeline is this node's single MultiSync-driven position estimate,
	// shared by every surface's frame writer (ADR-026 N=1 is a renderer
	// scope limit, not a reason to build N timelines; a future N>1 node
	// still tracks one master's position). SetStepTime is called from a
	// surface's own FSEQ file on every apply/resume — see renderops.go's
	// applyTimelineStepTime, which renderOps.applySurface and
	// ResumeAssignment both call; multisync.go's listener never calls
	// SetStepTime itself (it has no file to read one from).
	timeline := multisync.NewTimeline(time.Now, multisync.Config{})

	// multiSyncStatus carries a bind failure (or a mid-session socket
	// failure) into the render report as stated evidence rather than only a
	// log line — finding 7's second half; see multisyncstatus.go.
	multiSyncStatus := newMultiSyncStatus()

	multiSyncDone := make(chan struct{})
	go func() {
		defer close(multiSyncDone)
		runMultiSyncListener(sigCtx, cfg.MultiSyncListenAddr, cfg.MultiSyncInterface, timeline, multiSyncStatus, logger)
	}()

	renderOps := newRenderOperations(sup, assignmentStore, cfg.AssetDir, timeline, logger)

	// Reload every persisted surface assignment and re-apply it, so a node
	// that restarts with no coordinator reachable resumes rendering
	// (Track B build contract ruling 4) rather than sitting idle until a
	// coordinator reappears to resend an assignment it already sent once.
	if persisted, err := assignmentStore.Load(); err != nil {
		logger.Warn("failed to load persisted render assignments at startup; starting with none", "error", err)
	} else {
		for _, a := range persisted {
			var params map[string]any
			if err := json.Unmarshal(a.RawParams, &params); err != nil {
				logger.Warn("skipping a persisted render assignment with unparseable params", "surface_id", a.SurfaceID, "error", err)
				continue
			}
			if err := renderOps.ResumeAssignment(a.SurfaceID, params); err != nil {
				logger.Warn("failed to re-apply a persisted render assignment at startup", "surface_id", a.SurfaceID, "error", err)
				// Finding 9: the node KNOWS it holds an assignment it could
				// not honour (FSEQ missing after a disk restore, a
				// content-hash mismatch, unparseable persisted params); a
				// log line alone leaves no runner for this surface, so the
				// render report omits it entirely and the coordinator
				// cannot distinguish "never assigned" from "assigned and
				// broken." Report it as StateFailed instead of staying
				// silent.
				sup.MarkResumeFailed(a.SurfaceID, fmt.Sprintf("held a persisted assignment at boot but could not resume it: %v", err))
				continue
			}
			logger.Info("resumed a persisted render assignment at startup", "surface_id", a.SurfaceID, "applied_at", a.AppliedAt)
		}
	}

	// audioMgr is this node's audio session engine, against
	// [audio.FakeEngine]: the pipeline backend is an open owner decision
	// (Linear SM-68), so every audio.session.* command this node accepts
	// reports Unconfirmable rather than a working playback outcome — see
	// audio.Manager.gateAvailability's doc comment. RestoreAll rebuilds
	// every session this node held before this process last stopped,
	// matching renderOps' identical persisted-assignment reload above.
	audioMgr := audio.NewManager(audio.NewFakeEngine(time.Now), audio.NewFileSessionStore(cfg.AssetDir), cfg.AssetDir, audio.RealDecoder{}, time.Now, logger)
	if err := audioMgr.RestoreAll(sigCtx); err != nil {
		logger.Warn("failed to restore persisted audio sessions at startup", "error", err)
	}
	audioWatchDone := make(chan struct{})
	go func() {
		defer close(audioWatchDone)
		ticker := time.NewTicker(audioSessionWatchInterval)
		defer ticker.Stop()
		audioMgr.RunWatcher(sigCtx, ticker.C)
	}()

	// cmdHandler is constructed once, outside newMQTTConn, and reused across
	// every reconnect: its idempotency cache and allowlisted operations'
	// state (e.g. agentEchoState's stored value) are this process's memory
	// of what it has already done, and must survive a broker reconnect —
	// only the MQTT plumbing around it (the subscription, the
	// publish-received callback binding) is rebuilt per connect. See
	// mqtt.go's registerCommandHandling.
	cmdHandler := newCommandHandler(cfg.NodeID, cfg.AssetDir, cfg.AgentAPIToken, assetFetchTrigger, renderOps, renderTrigger, audioMgr, time.Now, logger)

	conn, err := newMQTTConn(connCtx, cfg, bootID, startedAt, heartbeatConnected, cmdHandler, logger)
	if err != nil {
		logger.Error("failed to start mqtt connection manager", "error", err)
		return 1
	}

	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(HeartbeatInterval)
		defer ticker.Stop()
		runHeartbeat(sigCtx, conn, cfg.NodeID, bootID, startedAt, time.Now, ticker.C, heartbeatConnected, logger)
	}()

	assetInventoryDone := make(chan struct{})
	go func() {
		defer close(assetInventoryDone)
		ticker := time.NewTicker(cfg.AssetInventoryInterval)
		defer ticker.Stop()
		runAssetInventory(sigCtx, conn, cfg.NodeID, cfg.AssetDir, time.Now, ticker.C, assetFetchTrigger, logger)
	}()

	renderReportDone := make(chan struct{})
	go func() {
		defer close(renderReportDone)
		ticker := time.NewTicker(cfg.RenderReportInterval)
		defer ticker.Stop()
		runRenderReport(sigCtx, conn, cfg.NodeID, sup, multiSyncStatus, time.Now, ticker.C, renderTrigger, logger)
	}()

	// Audio report: hardware discovery evidence (cached) plus a fresh
	// audioMgr session snapshot, on its own cadence — see audioreport.go.
	// No trigger channel: nothing here needs an out-of-cadence publish.
	audioReportDone := make(chan struct{})
	go func() {
		defer close(audioReportDone)
		ticker := time.NewTicker(cfg.AudioReportInterval)
		defer ticker.Stop()
		runAudioReport(sigCtx, conn, cfg.NodeID, audioMgr, time.Now, ticker.C, logger)
	}()

	<-sigCtx.Done()
	logger.Info("shutdown signal received")

	// Stop intercepting the shutdown signals now, not just via the
	// deferred call: restoring default signal behavior here lets an
	// operator send a second Ctrl-C to force-exit a shutdown that hangs
	// below, matching internal/coordinator.Run. The deferred stopSignal()
	// remains as a harmless, idempotent safety net.
	stopSignal()

	// The heartbeat, asset inventory, render report, audio report, audio
	// session watcher, and MultiSync listener loops also select on
	// sigCtx.Done() and exit on their own; wait for all six so none can
	// race the final offline publish below with a publish still in
	// flight.
	<-heartbeatDone
	<-assetInventoryDone
	<-renderReportDone
	<-audioReportDone
	<-audioWatchDone
	<-multiSyncDone

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	// Stop every surface's frame writer before its pipeline: a writer still
	// running against a process the supervisor is about to kill would just
	// see every write fail, so stop the source before the sink.
	renderOps.Shutdown()

	// Stop every supervised pipeline's child process before disconnecting:
	// a clean agent shutdown must not leave an orphaned gst-launch-1.0
	// process behind with nothing left to supervise or report on it. This
	// is deliberately after the shutdown-signal handling above and before
	// the MQTT offline publish, so it happens on every clean exit path.
	sup.Shutdown(shutdownCtx)

	shutdownCleanly(shutdownCtx, conn, cfg.NodeID, logger)

	logger.Info("showmesh-agent exited cleanly")
	return 0
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler)
}
