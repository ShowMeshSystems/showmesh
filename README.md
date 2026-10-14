# ShowMesh

**An orchestration and observation layer for large holiday light displays**, built around Falcon Player (FPP), xLights, and Resolume Arena. It coordinates them without replacing their scheduling, sequencing, mapping, or playback roles.

ShowMesh is never in the timing path: FPP MultiSync carries timing, so when the coordinator, the broker, or every browser goes away, the show keeps running.

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
![Go](https://img.shields.io/badge/go-1.26-00ADD8.svg)
![Status](https://img.shields.io/badge/status-pre--alpha-orange.svg)

## Install

On Debian 13 or newer (amd64 or arm64), as root:

```sh
curl -fsSL https://github.com/ShowMeshSystems/showmesh/releases/download/v0.3.0/get-showmesh.sh | sudo bash
```

The installer asks what the machine is (`coordinator`, `render`, `audio`, or `coordinator-node`), verifies the release checksums, and starts everything that role needs. A coordinator gets the coordinator, an authenticated Mosquitto broker, the Operator UI on port `8081`, and `showmeshctl`.

To add a node, create an enrollment code on the coordinator and pass it to the installer on the node:

```sh
sudo showmeshctl node enroll <node-id>     # on the coordinator
curl -fsSL https://github.com/ShowMeshSystems/showmesh/releases/download/v0.3.0/get-showmesh.sh \
  | sudo bash -s -- --role render --coordinator http://<coordinator-host>:8080 --code <code>
```

Running the same command again upgrades in place. `sudo showmesh-install --help` lists every option, including unattended installs. Use the matching FPP plugin release on your players; the `0.x` line makes no compatibility promise between versions, so upgrade everything together.

Run it on an isolated show network. Reads on the API are open to anyone who can reach port `8080`; writes require an authenticated user. See [SECURITY.md](SECURITY.md).

**Full instructions:** [docs.showmesh.systems](https://docs.showmesh.systems/getting-started/installation/)

## Status

**Pre-alpha.** Released as `0.x` pre-releases; see the [changelog](CHANGELOG.md) for what each version changed. What is and is not proven on real hardware is tracked on the [maturity page](https://docs.showmesh.systems/reference/maturity/) and in the [research records](docs/research/README.md).

## Documentation

- **Operators:** [docs.showmesh.systems](https://docs.showmesh.systems): installation, adding nodes, running a show night, integrations, troubleshooting, CLI and API reference.
- **API:** [`api/openapi.yaml`](api/openapi.yaml) is the public contract. The Operator UI and `showmeshctl` are both clients of it.
- **Engineering:** [docs index](docs/README.md), [architecture decisions](docs/decisions/README.md), [research records](docs/research/README.md), [build log](docs/build/BUILD-LOG.md), [release procedure](docs/RELEASING.md).

## Building from source

Requires Go 1.26, Node.js, and Docker.

```sh
make build       # coordinator, agent, multisync probe, showmeshctl into ./bin
make check       # format, vet, lint, Go tests, UI lint/test/build, codegen check
make deploy-up   # build and start the Compose bundle in deploy/ from this checkout
```

`make deploy-up` needs `deploy/.env` and a broker password file first; see [`deploy/README.md`](deploy/README.md).

| Path | What's there |
|---|---|
| `cmd/` | `showmesh-coordinator`, `showmesh-agent`, `showmeshctl`, `showmesh-multisync-probe` |
| `internal/` | Coordinator, agent, and CLI implementation |
| `pkg/` | Shared packages: MultiSync codec, observation model, capabilities, MQTT topics |
| `ui/` | Operator UI (TypeScript, React, Vite) and its nginx image |
| `api/openapi.yaml` | Public API contract |
| `deploy/` | Compose bundle, installer, and node agent packaging |
| `docs/` | Architecture, decisions, research, build plan and log |

## Design rules

Each of these is an accepted [architecture decision](docs/decisions/README.md).

- The coordinator is never in the real-time timing or media path. A show survives coordinator and broker loss ([ADR-008](docs/decisions/ADR-008-mqtt-control-plane.md)).
- FPP stays the scheduler. ShowMesh never becomes a second one ([ADR-001](docs/decisions/ADR-001-fpp-is-authoritative.md)).
- A command succeeds only when observed state moves, not when it is sent ([ADR-003](docs/decisions/ADR-003-desired-and-observed-state.md)).
- A value the system cannot see says why, and stale is never healthy ([ADR-011](docs/decisions/ADR-011-context-aware-observability.md)).
- Everything an operator can do goes through the public API, so the CLI and UI stay at parity ([ADR-014](docs/decisions/ADR-014-operator-ui-is-an-api-client.md)).

## Contributing and reporting issues

See [CONTRIBUTING.md](CONTRIBUTING.md). Report bugs and feature requests with the [issue templates](https://github.com/ShowMeshSystems/showmesh/issues/new/choose), documentation problems in [showmesh-docs](https://github.com/ShowMeshSystems/showmesh-docs/issues/new/choose), and security vulnerabilities privately through [SECURITY.md](SECURITY.md).

## License

[Apache-2.0](LICENSE). The NDI runtime is never vendored or linked; it is loaded at runtime only ([ADR-010](docs/decisions/ADR-010-apache-2-license.md)).
