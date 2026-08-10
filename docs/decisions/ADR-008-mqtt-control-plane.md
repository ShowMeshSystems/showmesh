# ADR-008: MQTT Is the Coordinator↔Agent Control-Plane Transport

Status: Accepted  
Date: 2026-08-10

## Context

The coordinator and node agents need command delivery, desired/observed state exchange, liveness detection, and event fan-out on an isolated show network, with the standing rule that the coordinator (and now its transport) must never be show-critical. FPP natively publishes status over MQTT, so one broker can carry both FPP telemetry and ShowMesh traffic.

## Decision

MQTT is the control-plane transport, with Eclipse Mosquitto as the reference broker, deployed alongside the coordinator (same host/appliance). Payloads are versioned JSON validated by published schemas.

Topic conventions (v1):

- `showmesh/nodes/<node-id>/hello` — retained capability advertisement.
- `showmesh/nodes/<node-id>/observed/...` — retained observed state and health.
- `showmesh/nodes/<node-id>/cmd` — commands to the agent; each carries id, idempotency key, deadline, revision, and confirmation method per the command model.
- `showmesh/nodes/<node-id>/result/<cmd-id>` — command results/evidence.
- `showmesh/nodes/<node-id>/lwt` — broker Last Will marks unexpected disconnect; liveness = LWT + periodic observed-state heartbeats.
- `showmesh/events/...` — coordinator-published lifecycle and alert events.

QoS 1 for commands and results; retained QoS 1 for state; idempotency keys make redelivery safe. FPP's MQTT output is consumed from the same broker for supervision-grade FPP status.

## Consequences

- Retained topics give late-joining coordinators and dashboards current state without a sync protocol.
- Liveness and presence come from the broker (LWT) instead of hand-rolled heartbeat plumbing.
- Broker loss is a management-plane outage only: agents and FPP must continue the running show (extends the ADR-004/coordinator-loss rule to the broker; failure tests in RES-009 must include broker loss).
- Request/reply is convention (cmd/result correlation), not a protocol feature — the command model's deadlines and idempotency carry that weight.
- Real-time timing never traverses MQTT; MultiSync remains the timing path.

## Alternatives considered

gRPC was rejected because presence, fan-out, and retained state would need rebuilding, and FPP integration would remain separate. NATS was rejected as an unfamiliar extra daemon in this community despite technical appeal. REST+WebSocket was rejected because reconnect, queueing, and liveness would all be hand-rolled.

## Related research

[Configuration model](../research/RES-008-configuration-model.md) · [Failure testing](../research/RES-009-failure-mode-testing.md) · [FPP MultiSync](../research/RES-002-fpp-multisync-compatibility.md)
