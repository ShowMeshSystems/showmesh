# Observability and Alerting Specification

[Documentation index](../README.md) · [Architecture specification](ARCHITECTURE.md) · [Operator UI specification](OPERATOR-UI.md) · [Research tracker](../research/README.md) · [ADR-011](../decisions/ADR-011-context-aware-observability.md)

Status: Draft architecture baseline  
Audience: Operators, maintainers, integration authors, and contributors

## 1. Purpose

The observability subsystem answers four operational questions from one place:

1. Is the show in the expected state?
2. Is every critical component ready and producing the expected result?
3. If something is wrong, where is the fault and what should the operator check first?
4. What happened before, during, and after the fault?

It monitors the complete presentation path rather than treating a reachable host or running process as proof of health. FPP, Resolume, controllers, projectors, media nodes, audio devices, transports, network equipment, power systems, and environmental sensors contribute evidence to one correlated operational model.

## 2. Principles

### 2.1 Operational, not decorative

Every dashboard element must help an operator decide whether to continue, investigate, inhibit, or recover the show. Decorative charts and raw metric walls are not part of the default interface.

### 2.2 Evidence has provenance and freshness

Every observation records its source, subject, measurement time, collection time, units, quality, and expiration policy. A stale successful observation becomes `unknown`; it must not remain green indefinitely.

### 2.3 Context changes meaning

A powered-off projector is expected during daytime and critical during a live set. A static preview may be correct in resting mode and a frozen feed during a motion-heavy sequence. Health and alerts are evaluated against lifecycle state, sequence position, maintenance windows, device expectations, and diagnostic mode.

See [ADR-011](../decisions/ADR-011-context-aware-observability.md).

### 2.4 Layered evidence localizes faults

Reachability, process state, signal presence, transport frames, preview content, and physical-device telemetry are distinct signals. The system should preserve those distinctions so it can report “projector reachable but no input signal” instead of the less useful “projection failed.”

### 2.5 Read-only monitoring comes first

Initial integrations should observe before they control. Automated remediation is added only after detection accuracy, safe-state behavior, and operator recovery are proven.

### 2.6 The coordinator is not the only witness

Collectors and node agents should timestamp observations close to their sources. Coordinator loss must not interrupt show execution, and buffered critical events should be reported after reconnection where practical.

## 3. Observability architecture

```text
FPP       Resolume       Pixel controllers       Projectors
 |            |                  |                    |
 +------------+------------------+--------------------+
                              collectors/adapters
Media nodes       Network/UPS/PoE       Environment sensors
    |                    |                       |
    +--------------------+-----------------------+
                              |
                              v
                 normalization and correlation
                              |
             +----------------+----------------+
             |                |                |
       current state      event history    metric history
             |                |                |
             +----------------+----------------+
                              |
                  rules, readiness, alerts
                              |
            web dashboard and notification adapters
```

The logical boundaries are required; their storage and process deployment are not yet fixed. See [telemetry storage and alerting research](../research/RES-013-telemetry-storage-and-alerting.md).

## 4. Signal model

### 4.1 Observations

An observation contains at least:

```yaml
observation_id: unique-id
resource_id: projector-front-left
signal: projector.input.signal_present
value: true
unit: null
observed_at: 2026-10-31T19:14:22.481-05:00
collected_at: 2026-10-31T19:14:22.620-05:00
source: pjlink-collector
quality: direct
valid_for: 15s
show_context:
  lifecycle: live
  playlist: halloween-main
  sequence: opening.fseq
  position_ms: 42180
```

`quality` distinguishes direct device telemetry, derived measurements, inferred state, and operator input.

### 4.2 Resource health

Resource health is derived from observations and expectations. Standard states are:

- `healthy`: current evidence meets expectations;
- `degraded`: service continues outside its desired operating range;
- `failed`: a required result is absent or unsafe;
- `unknown`: evidence is missing, stale, contradictory, or insufficient;
- `suppressed`: the condition is expected under a maintenance or lifecycle policy.

An aggregate may not report `healthy` when a critical child is `unknown` unless the policy explicitly permits it.

### 4.3 Events

Events record changes and actions rather than sampled values. Each event includes timestamp, source, affected resource, lifecycle state, playlist, sequence and position when available, category, severity, summary, structured measurements, correlation identifier, and related command or diagnostic run.

Examples include sequence start, projector warm-up, timecode lock change, alert creation, operator acknowledgement, process restart, diagnostic result, and return to resting mode.

### 4.4 Correlation

The coordinator correlates events by resource topology, time window, lifecycle transition, command execution, and diagnostic run. It should preserve the raw evidence even when several symptoms are grouped into one incident.

## 5. Collection and normalization

Collectors normalize vendor-specific interfaces without discarding raw status or error codes. Initial sources include:

- FPP status, playlist, sequence, position, MultiSync, and command results;
- Resolume composition, output, clip, timecode, and preview state;
- media-node heartbeat, process status, media cache, renderer statistics, output modes, EDID, and dropped frames;
- pixel-controller reachability, voltage, per-port current, and controller faults;
- projector reachability, power, input, signal, temperature, fan, lamp or light-source hours, resolution, and internal errors;
- NDI, HDMI, capture, and preview-gateway health;
- managed-switch port, link, error, PoE, and latency data where available;
- UPS, power, enclosure temperature and humidity, weather, and wind observations.

Polling rates, protocol support, and safe controller load are research outputs. Collectors must apply bounded timeouts, backoff, and concurrency limits so monitoring cannot impair show devices.

## 6. Operator dashboard

This section defines **what the operator surface must show**. How the client that shows it is built — isolation, the API contract, real-time transport, reconnection and staleness behavior, responsiveness, controls, and authorization — is defined in [OPERATOR-UI.md](OPERATOR-UI.md). Requirements belong in exactly one of the two documents; restating either list in the other will drift.

### 6.1 Global behavior

The web dashboard is the primary monitoring interface. It must show current data age, distinguish unknown from healthy, support a show-time high-contrast mode, and keep critical controls separate from exploratory views. Every aggregate health indicator must allow drill-down to its contributing evidence.

### 6.2 Main overview

The overview shows:

- lifecycle state and active macro;
- current FPP schedule, playlist, sequence, and playback position;
- audio playback and route state;
- SMPTE source, frame rate, offset, and lock state;
- FPP, Resolume, controller, pixel-port, projector, node, transport, and network health;
- active faults and inhibited readiness checks;
- weather, wind, enclosure temperature, and humidity when configured;
- data freshness and last successful collection.

The default view prioritizes active critical conditions, then readiness blockers, warnings, and informational activity.

### 6.3 House topology map

The map represents physical and logical relationships among projection surfaces, projectors, props, controllers, ports, differential receivers, power supplies, media nodes, network links, switches, and enclosures.

Health is overlaid without relying on color alone. Selecting an item opens current telemetry, dependencies, assigned capabilities, recent events and faults, maintenance state, and relevant first-response actions.

### 6.4 Detail views

Resource detail views show desired state, observed state, freshness, raw and normalized telemetry, related resources, current assignment, configuration revision, recent commands, diagnostic history, alerts, and logs or evidence links. Confirmation and authorization requirements for destructive or show-affecting actions are defined in [OPERATOR-UI §11](OPERATOR-UI.md#11-controls-and-safety).

## 7. Projection preview monitoring

### 7.1 Preview wall

The reference installation requires six simultaneous low-bandwidth projector previews. A tile includes:

- projector and surface name;
- live preview and preview age;
- Resolume output and layer/composition state;
- source frame rate and preview frame rate;
- media-node and renderer health;
- HDMI, DisplayPort, capture, or EDID state;
- projector power, input, signal, temperature, and light-source hours;
- transport and network latency where measurable;
- enclosure temperature and current fault.

Suggested starting targets are 320×180 or 480×270 at 5–10 frames per second. These are hypotheses to validate, not fixed platform limits. Browser delivery and gateway technology remain open in [RES-010](../research/RES-010-projection-preview-monitoring.md).

### 7.2 Derived content signals

Preview analysis may derive:

- frame arrival interval and effective frame rate;
- repeated-frame duration or perceptual-hash stability;
- mean luminance and black-frame likelihood;
- color variance and solid-frame likelihood;
- received resolution and aspect ratio;
- optional motion score;
- mismatch with the expected presentation profile.

Analysis runs outside the browser. Full preview frames are not stored in the operational database by default.

### 7.3 Contextual detection

Frozen-feed detection requires an expectation such as `motion_expected`, a permitted static interval, or a known resting-scene profile. Absence of context produces an observation or warning, not an automatic critical frozen-feed conclusion.

The system distinguishes at least:

- preview source absent;
- preview path failed while primary output may still be healthy;
- renderer not producing frames;
- transport not delivering frames;
- projector has no signal;
- signal exists but presentation appears frozen or black;
- intentional static or blackout state.

## 8. Pixel-current diagnostics

### 8.1 Known-load readiness test

Before the show, a diagnostic workflow may:

1. Enter an explicitly visible diagnostic state.
2. Apply a known pattern at controlled brightness.
3. Read voltage and per-port current.
4. Compare readings with versioned baselines.
5. Retry suspicious readings after a bounded interval.
6. Classify persistent deviations.
7. Restore the previous safe output state.
8. Record evidence and either pass, warn, or inhibit readiness according to policy.

This workflow changes physical outputs and is not part of passive monitoring. It requires scheduling safeguards and a compensating action.

### 8.2 Baselines

A baseline records controller, port, mapped prop, pixel count and type, diagnostic pattern, brightness, expected current, permitted range, input voltage, optional ambient temperature, collection method, configuration revision, and last verified date.

An initial tolerance such as 10–15 percent may be tested, but no universal threshold is accepted before [RES-011](../research/RES-011-pixel-current-diagnostics.md) is completed.

### 8.3 Fault classification

- Near-zero current may indicate a dead output, fuse, cable, receiver, or missing power.
- Reduced current may indicate a missing branch, dead pixel section, partial power loss, or failed injection.
- Excessive current may indicate a short, wiring fault, incorrect configuration, or water intrusion.
- Unstable current may indicate a loose connection, voltage sag, failing supply, receiver, or cable.

These are diagnostic hypotheses. The alert must present the measured evidence and likely checks without claiming a root cause that has not been confirmed.

## 9. Projector and infrastructure monitoring

### 9.1 Normalized projector model

The common model includes reachability, power state, input source, signal presence, native and current resolution, temperature, fan state, light-source hours, internal error, and management latency. Adapters may use PJLink, SNMP, HTTP, vendor TCP, networked serial, or other documented protocols.

Health logic distinguishes:

- unreachable;
- reachable and intentionally off;
- powered on without signal;
- signal present but presentation path appears frozen;
- temperature elevated or unsafe;
- internal hardware error.

Protocol coverage is tracked in [RES-012](../research/RES-012-device-telemetry-adapters.md).

### 9.2 Network, power, and environment

Where supported, the platform collects switch-port state and errors, PoE consumption, UPS state, input voltage, enclosure temperature and humidity, weather, and wind. Safety-related values may trigger alerts independent of show state; exact policies are configurable and require device-specific validation.

## 10. Readiness and lifecycle evidence

Readiness is a persisted workflow whose result includes every check, observation, timestamp, retry, override, and operator decision.

### T minus 15 minutes

- Wake required nodes and projectors.
- Verify control-network connectivity.
- Verify FPP playlist and Resolume composition.
- Verify required media and checksums.
- Verify controller availability.
- Read environmental and power conditions.

### T minus 10 minutes

- Run the authorized pixel-current diagnostic.
- Verify display outputs, EDID, resolution, and refresh rate.
- Start preview monitoring.
- Verify projector input signals.

### T minus 5 minutes

- Confirm media preload.
- Select and verify the intended Resolume composition.
- Confirm SMPTE receiver lock and acceptable offset.
- Validate the intended audio device and route.
- Measure program-to-LTC alignment on the audio node and confirm both resolve to one clock domain, per [ADR-018](../decisions/ADR-018-program-and-ltc-share-a-clock-domain.md). The acceptable bound is a bench output of [RES-007](../research/RES-007-audio-node-architecture.md) and is not yet known; until it is, this check records the measurement rather than passing or failing against a threshold.
- Resolve, override, or inhibit all critical readiness failures.

### Show start and live operation

The dashboard enters live mode, alert policies change to live expectations, and events are correlated with the active sequence and position.

### Post-show

The workflow verifies sequence completion, resting-scene activation, projector cooldown before shutdown, pixel outputs off, and expected idle states. It records runtime, unresolved faults, overrides, and a telemetry summary.

Timing is configurable; the ordering and evidence requirements are the architectural contract.

## 11. Alert model

### 11.1 Lifecycle

An alert progresses through `pending`, `active`, `acknowledged`, `resolved`, or `suppressed`. Repeated observations update one correlated alert rather than creating an unbounded notification stream. Resolution records the clearing evidence and whether it was automatic or operator-confirmed.

### 11.2 Severity

- **Informational:** expected transitions such as sequence start, resting-scene activation, projector warm-up, or completed preload.
- **Warning:** degraded but serviceable conditions such as current deviation, dropped frames, elevated temperature, degraded preview, elevated latency, or a noncritical offline resource.
- **Critical:** conditions that threaten safety or the live presentation, including excessive current, unsafe enclosure temperature, lost SMPTE lock, failed audio, missing main output, frozen active feed, offline required controller, or multiple failed media nodes.

Loss of an audience-facing audio output device is always critical and is never downgraded by lifecycle state, because [ADR-019](../decisions/ADR-019-audio-device-loss-fails-silent.md) makes the system's response silence rather than recovery. The alert is the only thing that turns that silence into an actionable condition, and recovery is operator work.

Severity is policy-driven and may change with lifecycle state. Safety conditions cannot be downgraded merely because the show is idle.

### 11.3 Alert content

Every actionable alert includes:

- affected resource and physical location;
- controller/port, prop, projector, surface, or dependency where applicable;
- lifecycle state, playlist, sequence, and position;
- first occurrence, most recent occurrence, and observation freshness;
- failure type, relevant measurements, thresholds, and evidence quality;
- likely impact and suggested first troubleshooting step;
- acknowledgement, suppression, and escalation state.

### 11.4 Noise control

Policies support debounce, minimum duration, hysteresis, deduplication, dependency-aware grouping, maintenance windows, expected-offline schedules, warm-up/cooldown suppression, diagnostic-mode handling, static-scene context, and per-lifecycle thresholds.

Suppression never deletes the underlying observation or event. Operators can see why a condition did not notify.

### 11.5 Destinations

The dashboard always shows active alerts. Candidate outbound adapters include Discord, Home Assistant, Hermes, email, generic webhooks, and push services. Initial deployment targets dashboard banners and Discord; credentials, retry policy, delivery status, and rate limiting must be observable.

Notification technology and routing storage remain open in [RES-013](../research/RES-013-telemetry-storage-and-alerting.md).

## 12. History and retention

The platform stores current state, events, faults, acknowledgements, maintenance windows, diagnostic results, baselines, and sufficient metric history for troubleshooting and trend analysis.

Retention may differ by data class. Raw high-rate metrics may be downsampled; incident evidence and configuration-linked diagnostic results require longer retention. Preview video is not retained by default. Operators must be able to reconstruct which sequence, configuration revision, and lifecycle state were active during a fault.

## 13. Security and privacy

Monitoring credentials are least-privilege and stored separately from exported configuration. Preview streams and telemetry require authenticated access. Alerts and logs must avoid embedding secrets. Control actions from observability views use the same authorization and audit requirements as other commands.

## 14. Delivery phases

### Phase O1 — Inventory and read-only monitoring

- Inventory and topology.
- FPP, Resolume, controller, projector, and node polling.
- Basic dashboard, freshness, active faults, and event history.
- Telemetry persistence without automated control.

### Phase O2 — Projection visibility and alerts

- Six preview tiles for the reference deployment.
- Browser-compatible low-bandwidth delivery.
- Missing, degraded, black, and frozen-feed candidates.
- Projector signal-state correlation.
- Dashboard and Discord alerts.

### Phase O3 — Pixel diagnostics

- Per-port current collection and topology mapping.
- Baseline capture and versioning.
- Known-load readiness workflow.
- Deviation classification and readiness gating.

### Phase O4 — Coordinated readiness

- Timed pre-show and post-show workflows.
- Maintenance windows and lifecycle-aware policies.
- Unified event correlation and telemetry summaries.
- Verified manual recovery guidance.

### Phase O5 — Resilient operations

- Fault injection and L4 verification.
- Dependency-aware alert grouping.
- Carefully bounded automatic remediation where evidence supports it.
- Long-term trends and capacity planning.

## 15. Monitoring-first MVP

The first useful observability release includes:

- central web dashboard;
- FPP and Resolume status;
- six low-resolution projection previews for the reference show;
- projector online, power, input, and signal state;
- three pixel-controller status panels;
- per-port current display and baseline warnings;
- active alert list and dashboard banner;
- Discord notification integration;
- historical event log correlated with sequence and lifecycle state.

This MVP may remain read-only except for alert acknowledgement and explicitly safe diagnostic initiation.

## 16. Open research

The observability design depends on:

- [RES-010: projection preview monitoring](../research/RES-010-projection-preview-monitoring.md)
- [RES-011: pixel-current diagnostics](../research/RES-011-pixel-current-diagnostics.md)
- [RES-012: device telemetry adapters](../research/RES-012-device-telemetry-adapters.md)
- [RES-013: telemetry storage and alerting](../research/RES-013-telemetry-storage-and-alerting.md)
- [RES-009: failure-mode testing](../research/RES-009-failure-mode-testing.md)
