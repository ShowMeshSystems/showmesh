package agent

import (
	"context"
	"log/slog"
)

// shutdownReason is the Reason recorded on the LWT payload shutdownCleanly
// publishes before disconnecting, distinguishing a planned stop from
// whatever willDisconnectReason describes.
const shutdownReason = "clean shutdown"

// shutdownCleanly publishes a retained "online: false" LWT payload with
// shutdownReason, THEN disconnects conn.
//
// This ordering is load-bearing, not stylistic — it is the single most
// important thing this file does, and it looks redundant to anyone who has
// not thought it through. An MQTT DISCONNECT with a normal reason code
// tells the broker to DISCARD the client's registered Will (see
// newMQTTConn's WillMessage), so a cleanly stopping agent's Will is never
// published; from the broker's point of view a graceful stop and "still
// connected" are indistinguishable until something else says otherwise.
// Without this explicit publish first, a planned restart would leave the
// retained LWT topic claiming "online: true" while nothing is running, and
// the coordinator's liveness rule would then have no way to notice short of
// heartbeat staleness — the full HeartbeatInterval-derived staleness
// window, not the near-instant signal a Last Will is supposed to provide.
// Publishing this message before disconnecting is what makes a clean stop
// and an unexpected crash look the same to the coordinator: either way, an
// "online: false" message arrives on the LWT topic.
//
// conn.Disconnect is always called, even if the publish fails: a failed
// offline notification is not a reason to also leave the TCP connection
// open past shutdown.
func shutdownCleanly(ctx context.Context, conn Conn, nodeID string, logger *slog.Logger) {
	if err := publishOffline(ctx, conn, nodeID, shutdownReason); err != nil {
		logger.Warn("failed to publish clean-shutdown offline message; disconnecting anyway", "node_id", nodeID, "error", err)
	} else {
		logger.Info("published lwt online=false (clean shutdown)", "node_id", nodeID)
	}

	if err := conn.Disconnect(ctx); err != nil {
		logger.Warn("mqtt disconnect error", "node_id", nodeID, "error", err)
	}
}
