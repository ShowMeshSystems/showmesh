package agent

import "context"

// Publisher is the minimal MQTT publish surface the agent's logic needs,
// factored out from the concrete MQTT client so tests can fake it without
// dialing a broker (per the Task D spec's testing requirements). The
// production implementation (mqttConn, in mqtt.go) wraps
// *autopaho.ConnectionManager.
type Publisher interface {
	// Publish sends payload to topic with the given QoS and retain flag.
	// Implementations must return promptly with an error when the
	// underlying connection is currently down rather than blocking until
	// reconnection: the heartbeat loop relies on that to move on to its
	// next tick instead of wedging (see runHeartbeat).
	Publish(ctx context.Context, topic string, qos byte, retain bool, payload []byte) error
}

// Conn is the MQTT connection surface the agent's shutdown path needs: it
// composes Publisher (to send the clean-shutdown offline message) with
// Disconnect (to end the session afterward), so shutdownCleanly's ordering
// guarantee — publish before disconnect — can be asserted directly against
// a fake in tests, not just eyeballed in production code.
type Conn interface {
	Publisher
	Disconnect(ctx context.Context) error
}
