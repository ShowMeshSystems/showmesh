// Package fppmqtt is a second, independent observation source for the same
// FPP instances internal/coordinator/collector/fpp polls over REST: it
// subscribes to the FPP-published MQTT topics under
// "<prefix>/<HostName>/..." (default prefix "falcon/player", contract
// section 1.2) on an operator-owned broker, and renders whatever it has
// received into [observation.Observation] values. It implements
// internal/coordinator/collector.Collector, exactly like the fpp package
// does, so a coordinator wires the two in side by side as independent
// Collectors polled on their own cadences.
//
// # This is a collector source, never the ADR-008 control plane
//
// internal/coordinator/broker owns ShowMesh's own control-plane MQTT
// connection: retained hello/observed/LWT topics under "showmesh/nodes/*",
// QoS 1 commands and results, all defined by ADR-008 and pkg/mqttproto.
// This package's connection is a different, unrelated broker (the
// operator's existing FPP/home-automation broker, e.g.
// see docs/reference-installation.md) speaking FPP's own
// MQTT publish conventions, which ShowMesh does not define and does not
// control.
//
// The two must NEVER be merged into one client, one topic namespace, or one
// code path. They look superficially similar — both are "the coordinator
// talking MQTT" — and collapsing them later would look like a tidy-up. It
// would actually put ShowMesh's control plane (agent liveness, commands,
// results) on a foreign broker this project does not operate, cannot
// secure, and — per the safety rule below — must never publish to. Keep
// internal/coordinator/broker and this package's connection (built in
// mqttclient.go) as two separate autopaho connection managers, permanently.
//
// # Read-only by construction, not by convention (contract section 4.5)
//
// That broker, and any configured the same way, is a
// LIVE broker a real FPP daemon acts on. "falcon/player/<host>/command/run"
// and "falcon/player/<host>/command/preset/triggered" are live command
// topics: a stray publish on either one runs a command on a real display,
// exactly as issuing a non-GET HTTP request to a live FPP would. This
// package's connection is built so that it CANNOT publish, not merely so
// that it happens not to:
//
//   - Every call this package makes ON its live connection — subscribeAll,
//     and nothing subscribeAll's caller does afterward — goes through the
//     unexported [subscriber] interface (mqttclient.go), whose method set
//     is Subscribe and nothing else — no Publish, no PublishWithOptions.
//     [buildClientConfig]'s OnConnectionUp callback narrows its
//     *autopaho.ConnectionManager argument to a subscriber-typed local
//     variable as the very first thing it does, before touching cm any
//     other way, so every line of this package's OWN code downstream of
//     that point sees only Subscribe. This is a narrower, more honest
//     claim than "the only handle this package ever HOLDS": autopaho's
//     own OnConnectionUp field type is
//     func(*autopaho.ConnectionManager, *paho.Connack), which this
//     package does not control, so the callback's cm PARAMETER is still
//     the wide, Publish-capable type for the one statement before it gets
//     narrowed — the Go compiler would not reject a Publish call added
//     on that first line. What actually holds the guarantee is that no
//     such call exists anywhere in this package's source (see
//     readonly_test.go, which asserts the subscriber interface's method
//     set structurally via reflection, and this package's own code, which
//     never widens a subscriber value back to a concrete
//     *autopaho.ConnectionManager anywhere).
//   - No Last Will is ever configured: [buildClientConfig] never sets
//     autopaho.ClientConfig.WillMessage. An LWT is itself a publish (the
//     broker sends it on this client's behalf), so "no Last Will" is part
//     of the same read-only guarantee, not a separate concern. See
//     readonly_test.go's assertion on the built config.
//   - Every subscription this package makes is scoped to
//     "<prefix>/<HostName>/<topic>" for a topic this package actually
//     models (see topics.go); it never subscribes to "falcon/control/#"
//     (the operator's own control surface, contract section 1.2) and never
//     subscribes to a "command/*" topic under any host, even though a
//     broader "<prefix>/<HostName>/#" subscription would technically
//     receive them too. See topics.go's doc comment for why an explicit
//     topic list was chosen over a wildcard.
//
// SHOWMESH_FPP_MQTT_PASSWORD (see internal/coordinator/config) must never
// reach a log line, an error, an observation Reason, or the API. See
// mqttclient.go's connection-failure reason strings and
// internal/coordinator/config's LogValue for the two places this is
// enforced.
//
// # The retained/live distinction (contract section 4.2)
//
// This is the one property every other design decision in this package
// serves, and it has been introduced and caught three times already in
// this project in different disguises (Step 2's broker liveness, Step 3's
// evidence provenance, and now here): an MQTT message delivered with the
// RETAIN flag set is a replay of unknown age — possibly hours or days old,
// possibly from an FPP that no longer exists — and stamping the moment we
// happened to receive it as though that were the moment the underlying
// condition became true manufactures freshness evidence never actually
// observed. See render.go's buildObservation and store.go's message type.
package fppmqtt
