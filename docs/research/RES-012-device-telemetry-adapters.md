# RES-012: Device Telemetry Adapters

[Observability](../architecture/OBSERVABILITY.md#9-projector-and-infrastructure-monitoring) · [Tracker](README.md) · [Configuration research](RES-008-configuration-model.md)

Status: planned (bench) · Risk: high · Verification: L1 — source verified 2026-08-10

## Decision to make

Define the normalized telemetry contract and supported protocol profiles for projectors, network equipment, power systems, and environmental sensors.

## Questions

- Which projector models are deployed and which PJLink, SNMP, HTTP, TCP, or serial functions do they expose?
- Can reachability, power, input, signal, temperature, fan, hours, resolution, and internal errors be read reliably?
- Which fields are direct, derived, vendor-specific, or unavailable?
- Which switch, PoE, UPS, enclosure, weather, and wind signals materially change operator decisions?
- What polling frequency, timeout, rate limit, and backoff are safe for each device class?
- How are unit conversion, enum mapping, counter rollover, authentication, and clock differences handled?
- How does the UI disclose missing capabilities without reporting a device unhealthy?

## Acceptance criteria

- Each supported device profile lists verified fields, units, freshness, error semantics, and unsupported features.
- The normalized model distinguishes unreachable, intentionally off, powered without signal, signal present, presentation-path failure, overheating, and internal error when evidence supports those distinctions.
- Collectors cannot saturate or destabilize managed devices during a full-show soak.
- Raw vendor values remain available for diagnosis alongside normalized state.

## Test matrix and method

Inventory actual models and firmware first. Exercise cold boot, warm-up, input change, no signal, over-temperature simulation where safe, management-interface restart, authentication failure, network loss, counter rollover, and unsupported commands. Compare telemetry with physical state and native management interfaces.

## Evidence and findings

Desk research 2026-08-10 (standards docs, vendor docs, project repos; no hardware). Confidence tags: [doc] standard/vendor doc, [proj] OSS project, [anec] community.

### Projectors — PJLink is the primary adapter

- PJLink (JBMIA standard): ASCII line protocol over **TCP 4352**, optional MD5 challenge auth. **Class 1** (near-universal on LAN-equipped business projectors): power state incl. warm-up/cool-down, input select/list, AV mute, six-field error status (fan/lamp/temp/cover/filter/other), lamp hours, identity. **Class 2** (less uniform): freeze state, current/native resolution, serial, firmware, filter hours, UDP discovery and push notifications ([spec v2.10](https://pjlink.jbmia.or.jp/english/data_cl2/PJLink_5-1.pdf), [certified model list](https://pjlink.jbmia.or.jp/english/list.html)). Adapter must probe `CLSS?` and treat Class 2 fields as optional. [doc]
- **No mature Go PJLink library exists**; the protocol is small — write `pkg/pjlink` in-house ([BYU OIT microservice](https://github.com/byuoitav/pjlink-microservice) as prior art). [proj]
- **All deployed projectors support PJLink — they were purchased specifically for it (operator-confirmed 2026-08-10).** The remaining inventory question is per-model PJLink class (`CLSS?`): Class 1 covers power/input/mute/errors/lamp everywhere; freeze and resolution queries require Class 2. [operator]
- For future/community deployments without PJLink: vendor RS-232 command sets via IP-to-serial bridges (Global Caché iTach class, or ser2net on a Pi) as a second transport behind the same projector capability; SNMP is vendor-MIB-only and inconsistent (Epson traps, NEC enterprise OID 1.3.6.1.4.1.119) — per-model bonus, not a foundation. [anec/doc]

### Network — UniFi

- Official **UniFi Network Integration API** (GA, versioned): API key auth (`X-API-KEY`), local base `https://<controller>/proxy/network/integration/v1/...` ([getting started](https://help.ui.com/hc/en-us/articles/30076656117655-Getting-Started-with-the-Official-UniFi-API), [docs](https://developer.ui.com/network/v10.1.84/gettingstarted)). Device statistics endpoints exist; **per-port PoE wattage and error counters are still richest in the classic private API** (`stat/device` → `port_table[].poe_power`, errors), which the Go library `unpoller/unifi` wraps ([unpoller](https://github.com/unpoller/unpoller), [ubntwiki](https://ubntwiki.com/products/software/unifi-controller/api)). Pragmatic adapter: official API for inventory/link state + unpoller lib for PoE/errors until official coverage catches up. [doc/proj]

### UPS — NUT

- NUT over apcupsd: multi-vendor HCL, protocol standardized as RFC 9271, `apcupsd-ups` bridge subsumes APC ([HCL](https://networkupstools.org/stable-hcl.html)). Small Go clients exist ([robbiet480/go.nut](https://github.com/robbiet480/go.nut) and maintained forks) — vendor/fork one; poll TCP 3493 read-only. [doc/proj]

### Environment sensors — already MQTT-native

- ESPHome (native MQTT + birth/LWT — matches ADR-008 conventions), Zigbee2MQTT (MQTT by design), Ecowitt weather/wind via local HTTP push + [ecowitt2mqtt](https://github.com/bachya/ecowitt2mqtt) (or pollable `get_livedata_info`; newer firmware has native MQTT). The env adapter is a normalization layer over MQTT topics, not a device driver. [doc/proj]
- **Deployed: ESPHome-flashed Emporia Vue** — per-circuit AC power already publishable over MQTT; feeds a `telemetry.power.v1` class (circuit id, watts, voltage?, last_seen) and the RES-011 power-envelope cross-check. [operator]

### Axia xNode

- Vendor states SNMP support + syslog + full web UI; the Synchronization/QoS page shows PTP clock mode/state ([Telos xNodes](https://www.telosalliance.com/Axia/xNodes)). No public MIB — whether PTP lock is machine-readable via SNMP or requires LWRP (TCP 93)/scraping is a bench item. [doc]

### Proposed normalized field sets (v1 candidates)

`telemetry.projector.v1`: power_state, active_input, av_mute, errors{fan,lamp,temp,cover,filter,other}, lamp[{hours,on}], identity, resolution{input?,native?}, freeze?, transport, reachable, last_seen. `telemetry.netport.v1`: link_up, speed, duplex, poe{enabled,delivering,power_w}, counters{rx/tx bytes,errors,drops}. `telemetry.ups.v1`: maps 1:1 to NUT variables (status, battery{charge,runtime}, input/output, load). `telemetry.env.v1`: kind, temperature, humidity?, wind{speed,gust,direction}?, battery, source, last_seen (staleness is the primary health signal). `telemetry.audionode.v1`: reachable, ptp{mode,locked,domain}, streams, last_seen. All share the ADR-003/008 envelope: observed-only, retained topic per device, explicit staleness.

### Open items for bench (L2)

1. Record deployed projector models and probe each with `CLSS?` (Class 1 vs 2); verify ERST/LAMP semantics (laser models often return no lamp hours); PJLink single-session behavior under polite polling; whether auth is enabled per unit.
2. Enumerate in-controller Integration API port fields at the deployed UniFi version; confirm PoE wattage/error coverage; confirm read-only API-key scope.
3. Verify deployed UPS models against `usbhid-ups`; test go.nut fork against NUT ≥2.8.
4. Ecowitt push vs poll staleness behavior under Wi-Fi dropout.
5. Obtain the Axia MIB from Telos; determine whether PTP lock is SNMP-readable.

## Decision, fallback, and revalidation

Direction (pending bench): PJLink as the primary projector adapter with serial-bridge as secondary transport; UniFi official API + unpoller-lib hybrid; NUT for UPS; MQTT-native ingestion for environment sensors. Unsupported devices use reachability-only monitoring, clearly labeled as limited evidence per ADR-011. Revalidate after firmware, credentials, protocol adapter, or topology changes.
