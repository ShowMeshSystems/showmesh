# ADR-068: Coordinator events are published to an integration broker

Status: Proposed
Date: 2026-10-21

## Context

- A refused night command (Start night, Start preshow, Run readiness and the
  rest) is answered only to the caller. The refusal is rolled back with the
  command's transaction, so nothing reaches `/events` or `/stream`. The
  automatic readiness rerun inside Start night (when the stored result is
  older than 30 minutes) can refuse the same way, and nobody who is not
  watching the screen finds out the show did not start.
- A site that runs Home Assistant wants to turn ShowMesh happenings into phone
  notifications and automations, and to choose for itself which ones matter.
- The coordinator already connects to integration brokers for MQTT show
  actions (STEP-9-SPEC.md section 2.10): the brokers an operator adds in
  Settings, and the built-in broker through its own `integration` login,
  which may use `showmesh-actions/#` and nothing else (ADR-024, amended
  2026-10-22).
- How outbound notifications are routed is still open in RES-013 and
  OBSERVABILITY.md section 11. This record settles one route, MQTT to an
  integration broker, without closing the rest of that question.

## Decision

1. **A new `events.mqtt` configuration kind** (`default` singleton) holds
   three optional fields: `enabled` (default false), `broker` (the id of one
   integration broker), and `topicPrefix` (default `showmesh`). It is edited on
   a Settings tab in the UI, through the configuration API, and with
   `showmeshctl`. An unset or disabled setting publishes nothing. Any
   integration broker may be named, including `built-in`. On `built-in` the
   topic prefix must start with `showmesh-actions/` (for example
   `showmesh-actions/events`), because its login may use no other topics;
   saving refuses any other prefix there.
2. **Every row the coordinator adds to its event history is published**, not
   retained, at QoS 1, on `<topicPrefix>/events/<category>`. The message is
   JSON with the event's `seq`, `recordedAt`, `occurredAt`, `category`,
   `severity`, `summary`, `source`, `resource` and `details`, the same fields
   `GET /events` returns. Filtering is the subscriber's job.
3. **The current night state is published retained** on
   `<topicPrefix>/night/state` whenever it changes: `state`, `sessionId`,
   `cycle`, `degraded`, the latest readiness `outcome` and `completedAt`, and
   `at`. With no night session, `state` is `inactive`. A subscriber that
   connects late reads the current state at once.
4. **Night commands and readiness become events**, so they reach the event
   history, the UI's event list, `/stream` and MQTT alike:
   - `night.command.refused` (warning): the command, the session, and the same
     reason the operator saw. This includes a refused automatic readiness
     rerun inside Start night and a refusal for a weather delay.
   - `night.command.applied` (informational): the command and the session.
   - `night.readiness.completed` (informational when ready, warning
     otherwise): the outcome and the names of the failing checks.
5. **Publishing never affects the show.** A forwarder follows the event
   history by `seq`, the way the change stream does, starting at the latest
   row when the coordinator starts, so a restart does not replay old events.
   A broker that is down or refuses a message is logged and that message is
   dropped. Commands, the night loop and the HTTP response never wait on it.

## Consequences

- A Home Assistant automation can notify on `night.command.refused` alone, or
  on anything else, by subscribing to `<topicPrefix>/events/#` and
  `<topicPrefix>/night/state`. A shared Home Assistant blueprint that matches
  these categories is planned outside this repository.
- Delivery is at most once. An event recorded while the broker is down is
  never sent, but it stays in `GET /events`.
- Event categories become something a subscriber filters on. Renaming one is
  a visible change for those subscribers and is recorded in the identifier
  register like any other published name.
- The fleet broker's access list does not change.

## Alternatives considered

- **Only refusals and failed readiness.** Rejected by the owner: send
  everything and filter in Home Assistant.
- **An environment variable for the broker and prefix.** Rejected by the
  owner: settings an operator manages belong in the UI. Environment variables
  are for what the installer sets.
- **A static payload on an existing MQTT show action, run on refusal.**
  Cannot carry the refusal reason, and needs one action per kind of event.
- **Publishing on the built-in broker outside `showmesh-actions/`.** The
  `integration` login would need a wider access-list grant, and ADR-024 keeps
  it to that one topic tree so it can never reach a node or FPP command.

## Related

- [ADR-008](ADR-008-mqtt-control-plane.md), the fleet broker's conventions.
- [ADR-024](ADR-024-identity-authorization-and-audit.md), the `integration` login on the built-in broker.
- [ADR-065](ADR-065-site-actions-and-shutdown-sequences.md), night site actions on integration brokers.
- [RES-013](../research/RES-013-telemetry-storage-and-alerting.md), outbound notifications.
