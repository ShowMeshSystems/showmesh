# ShowMesh coordinator appliance bundle

Docker Compose bundle for the coordinator and its Mosquitto broker (ADR-008), suitable for the appliance deployment described in ARCHITECTURE.md section 10.1. Node agents are not part of this bundle; they run natively on media-node hosts (section 10.2).

## Bring the stack up

```sh
cd deploy
cp .env.example .env   # edit as needed, especially before exposing beyond an isolated show VLAN
./mosquitto/generate-credentials.sh   # REQUIRED, once, before the first `up` — see below
docker compose up -d --build
```

This builds the coordinator image from the repo root Dockerfile, the Operator UI image from `../ui/Dockerfile`, and starts both alongside the bundled Mosquitto broker. Check status with `docker compose ps` and `curl -fsS localhost:8080/healthz`.

The `generate-credentials.sh` step is not optional: the bundled Mosquitto now requires authentication (`allow_anonymous false`), and it will not start with a usable password file until that script has run at least once. If you skip it, `docker compose ps` will show the `mosquitto` service crash-looping and its own log will say plainly why (`Error: /mosquitto/config/passwd is not a file.`) — run the script and `docker compose up -d` again.

## MQTT broker credentials (ADR-024)

**This changed from earlier releases, and it changes what an existing deployment must do before upgrading.** The bundled Mosquitto used to accept anonymous connections (`allow_anonymous true`); it now requires a credential and enforces an access-control list, per [ADR-024](../docs/decisions/ADR-024-identity-authorization-and-audit.md) decision 10.

**What breaks on upgrade, stated plainly, because this record claims to know what it breaks:**

- **The bundled broker's own container healthcheck**, which used to run an unauthenticated `mosquitto_sub` against `$SYS/#`. It now needs a credential (see below); an upgraded deployment that has not run `generate-credentials.sh` will see the `mosquitto` service report unhealthy, or fail to start at all if the password file does not exist yet.
- **Every ShowMesh agent**, which used to connect anonymously. Each one now needs its own broker credential (`./mosquitto/add-agent-credential.sh <node-id>`, run once per node) and `SHOWMESH_MQTT_USERNAME`/`SHOWMESH_MQTT_PASSWORD` set in that node's own agent configuration — not in this bundle's `.env`, since agents run natively on media-node hosts, not in this Compose bundle. An agent with no credential, or the wrong one, does **not** exit: it keeps running and keeps retrying, exactly as it already did for an unreachable broker, but now logs a distinct `mqtt broker rejected connection: not authorized` message instead of the generic "will retry" line, and appears in the coordinator's inventory as control-plane offline, not as a crashed process.
- **FPP's own MQTT output**, if you followed [ADR-008](../docs/decisions/ADR-008-mqtt-control-plane.md)'s recommendation to point FPP at this same broker rather than running a second one. FPP needs the dedicated `fpp` publisher-role credential this script also generates (printed once — see below), set in FPP's own MQTT configuration (System Configuration → MQTT in FPP's UI), or its status output silently stops arriving at this broker the moment anonymous access closes.

**How credentials are generated, and why never by hand-editing a file in this repository:** run, once, before the first `docker compose up`:

```sh
./mosquitto/generate-credentials.sh
```

It creates `mosquitto/passwd` (gitignored — see this repository's `.gitignore` — bcrypt-hashed via Mosquitto's own `mosquitto_passwd`, using the exact `eclipse-mosquitto` image version this bundle runs) with three fixed roles this bundle itself needs: `coordinator` and `healthcheck`, whose freshly generated passwords it writes directly into `deploy/.env` for you, and `fpp` (the publisher role above), whose password it prints once to the terminal for you to copy into FPP's own configuration. It is idempotent — safe to run again, it never rotates an existing password — and it never authors a credential into anything checked into version control: a password file committed to the repository would be identical in every ShowMesh installation, which is precisely the failure [ADR-021](../docs/decisions/ADR-021-read-api-authentication-posture.md) named when it rejected a mandatory shared secret with no distribution mechanism, and which [ADR-024](../docs/decisions/ADR-024-identity-authorization-and-audit.md) decision 10 repeats for the broker specifically.

On every run it also rebuilds the gitignored `mosquitto/acl.generated.conf` from the committed fixed-role base (`mosquitto/acl.conf`) and the existing password-file usernames. That migration is intentional: older bundles used a Mosquitto `pattern` grant for agents, but Mosquitto applies a pattern to every authenticated user, including `coordinator`, `fpp`, and `healthcheck`. The generated file has a separate explicit block for every agent and no pattern grants, so those fixed users receive only their documented role permissions. If an existing password file contains a non-fixed username that is not a valid ShowMesh node ID, the script stops before replacing the generated ACL and names the entry to repair; it will not guess whether that account was intended as an agent.

**Each ShowMesh agent (node) needs its own credential**, provisioned individually as that node is set up:

```sh
./mosquitto/add-agent-credential.sh <node-id>
```

`<node-id>` must be the exact node id that node's agent will present as `SHOWMESH_NODE_ID` — the script enforces the same character rule `pkg/mqttproto` validates a node id against (lowercase letters, digits, and internal hyphens only) before ever writing to the password file, because the broker username being provisioned is what the ACL's per-agent rules trust to equal the node's own id (see `mosquitto/acl.conf`'s header comment for exactly why that matters and what goes wrong if it is not enforced). The printed password is shown once; set it as that node's own `SHOWMESH_MQTT_USERNAME`/`SHOWMESH_MQTT_PASSWORD` (not in this bundle's `.env` — see "The full picture" below).

**Access control**, not just authentication: `mosquitto/acl.conf` is the committed base for fixed roles, and the generated effective file adds a separate explicit block for each agent. Each agent may publish only to its own hello/lwt/observed/result topics (explicitly **excluding** its own `cmd` topic) and subscribe only to its own `cmd` topic; only the coordinator may publish to any node's `cmd` topic; the `fpp` role is confined to FPP's own status topics (`falcon/player/#` by default) with no access to any `showmesh/` topic at all; and the `healthcheck` principal can only read `$SYS`. Agent node IDs `coordinator`, `fpp`, and `healthcheck` are reserved and rejected by the provisioning script, because they name these fixed roles.

**Upgrade sequence for a running older bundle:** older tooling allowed node IDs named `coordinator`, `fpp`, or `healthcheck`, even though those usernames also carried the fixed-role grants. The password file cannot reveal which meaning an operator intended, so migration deliberately refuses to guess. First confirm those three entries are the bundle's fixed roles and rename any legacy agent using one of those node IDs. Then run `./mosquitto/generate-credentials.sh --migrate-existing`; it preserves the passwords, renders explicit agent blocks for every other valid username, and writes a private migration marker distinct from the generated ACL itself. An empty or manually copied ACL file is therefore not mistaken for acknowledgement. Finally run `docker compose up -d --force-recreate mosquitto` once, because an older container bind-mounts the former single ACL path and cannot see the generated file. Future plain `generate-credentials.sh` and `add-agent-credential.sh` runs rebuild the ACL atomically and HUP a current directory-mounted Compose broker automatically; if the broker is not running, the next start reads the generated file.

**What "revocable" does and does not mean here — two limits, stated so an upgrade does not surprise you at showtime:**

- Mosquitto re-reads `passwd` and `acl.generated.conf` on `SIGHUP` but does **not** re-authenticate a client that is already connected. The provisioning script reloads a current Compose broker after it atomically rebuilds the generated file; rotating a compromised or retired agent's credential still takes effect only when that agent's connection actually drops and it reconnects, or when the broker itself restarts (which flips every node's control-plane state to offline at once — a bigger hammer, use deliberately).
- Every credential this bundle provisions is a **hand-provisioned, permanent-in-practice** credential, not a rotating one. A node agent's credential lives in that node's own configuration on a controller that may be mounted in a yard; rotating it means editing that node's config and restarting its agent, which for a physically deployed node means a visit. This will not change until node-enrollment automation exists (not part of this release — see ADR-024's supersession section).

**Residual exposure, recorded rather than glossed over:** the ACL bounds what a compromised *agent* credential can do (a stolen node, per ADR-024 decision 10's own reasoning: "a node is a Pi in a weatherproof box in a front yard, physically reachable by anyone walking past"). It does not defend against a compromised *coordinator* broker credential: anyone holding it can publish a forged command to any node's `cmd` topic, and an agent has no way to tell. Message-level command authentication would close that and is deliberately not built here (see ADR-024's own consequences and alternatives sections). If FPP's own broker credential — whether on this bundle's broker or, as in the reference installation, a separate home-automation broker — is ever a shared, general-purpose account with publish rights rather than a dedicated read-only or FPP-scoped one, that account is a larger command-authority exposure than anything this ACL restricts; see ADR-024 decision 10's own closing paragraphs.

## The read-only control API

The coordinator serves a versioned, public, read-only API at `/api/v1` on the same HTTP port as the health endpoints, with a Server-Sent Events change stream at `/api/v1/stream`. It is a documented contract designed to be used without any browser ([ADR-014](../docs/decisions/ADR-014-operator-ui-is-an-api-client.md), [ADR-020](../docs/decisions/ADR-020-control-api-shape-and-change-stream.md)), so a CLI, a script, or an automation system is a first-class client:

```sh
curl -s  localhost:8080/api/v1/nodes | jq
curl -s  localhost:8080/api/v1/fpp   | jq
curl -N  localhost:8080/api/v1/stream        # watch changes as they happen
```

`showmeshctl`, built from this repository, is the same thing with readable output: `showmeshctl nodes`, `showmeshctl fpp`, `showmeshctl events`, `showmeshctl watch`. The machine-readable contract is `api/openapi.yaml`.

Everything the API reports carries provenance and freshness, and absent evidence is stated rather than omitted ([ADR-011](../docs/decisions/ADR-011-context-aware-observability.md)). A field you cannot see a value for will tell you whether it was never collected, whether collection failed, whether the source does not support it, or whether the value has gone stale. Two of those states are worth knowing about specifically:

- `unknown_age` means a value exists but its observation time genuinely is not known, which happens when the coordinator learns something from a retained MQTT message after a restart. It is never treated as fresh.
- A node reported as `controlPlane.state: offline` has lost its **control-plane connection**. It does not mean the node is dead or the show has stopped: a running show survives coordinator loss and broker loss by design.

### Security posture, stated plainly

**By default the read API is open to anyone who can reach this port**, and the coordinator logs a warning saying so at startup. Setting `SHOWMESH_API_CLOSE_READS=true` in `.env` closes it, requiring an authenticated principal. Writes always require one and cannot be opened.

**Upgrading: remove `SHOWMESH_API_TOKEN` from your environment before you start this release.** [ADR-024](../docs/decisions/ADR-024-identity-authorization-and-audit.md) retired it, and a coordinator that still sees it set **refuses to start**, naming the migration in the error. That is deliberate and it is the most operator-visible hazard in this upgrade: ignoring the variable would silently reopen a read API you had deliberately closed, on nothing more than a container tag change. Set `SHOWMESH_API_CLOSE_READS=true` instead, then create your first administrator from the one-time bootstrap code the coordinator writes to its data volume (`POST /api/v1/bootstrap`, or `showmesh-coordinator bootstrap` against the volume). Machine credentials for `showmeshctl` and automation come from `showmesh-coordinator issue-token`.

[ADR-021](../docs/decisions/ADR-021-read-api-authentication-posture.md) records what that token is and is not. It is one shared secret with no identity, no roles, and no audit attribution, so it does not satisfy ARCHITECTURE section 10.4. **The show VLAN remains the actual security boundary.**

*This paragraph is narrower than it used to be, and on purpose.* Earlier revisions of this document also named the bundled Mosquitto's anonymous access as "the larger exposure of the two". That is no longer accurate: the bundled broker now requires authentication and enforces an access-control list ([ADR-024](../docs/decisions/ADR-024-identity-authorization-and-audit.md) decision 10) — see "MQTT broker credentials" above for what that means and what it does not close (a compromised *coordinator* broker credential can still forge a command to any node; that residual exposure is recorded there, not solved here).

There are no write operations at all in this release. Nothing reachable through this API can change a device, a playlist, or a show.

## The Operator UI

The `ui` service is a separate container built from `../ui/Dockerfile`: a static TypeScript SPA served by nginx, which also proxies `/api/*` to the coordinator so the browser sees one origin ([ADR-014](../docs/decisions/ADR-014-operator-ui-is-an-api-client.md), [ADR-015](../docs/decisions/ADR-015-typescript-spa-frontend.md), [ADR-022](../docs/decisions/ADR-022-operator-ui-serves-the-api-same-origin.md)). Point a browser at:

```
http://localhost:8081
```

(the published host port; override with `SHOWMESH_UI_PORT` in `.env`.)

**The browser talks only to this container**, never to the coordinator's own port directly. The UI container forwards every `/api/*` request through unchanged and forwards responses back unchanged; it performs no aggregation, retry, rewriting, or caching of API responses ([ADR-022](../docs/decisions/ADR-022-operator-ui-serves-the-api-same-origin.md)). The browser signs in against the coordinator and holds an `HttpOnly` session cookie the coordinator mints, which this proxy forwards along with `Set-Cookie` unchanged. **The UI container is deliberately never given a credential of its own** and cannot be configured with one, because a proxy that held one would make reaching the UI equivalent to reaching the API ([ADR-022](../docs/decisions/ADR-022-operator-ui-serves-the-api-same-origin.md) decision 2). A pasted machine token remains available as a break-glass path when the session path is broken. Do not add `proxy_cookie_path` or `proxy_cookie_domain` to `nginx.conf`: either one breaks sign-in in a way that presents as a session that does not stick.

**ShowMesh terminates no TLS**, in this container or the coordinator's. A deployment that needs TLS puts a reverse proxy of its own in front of the `ui` service; nothing here does it.

This container is a client, not a dependency: the coordinator, the broker, and any running show are unaffected by the `ui` service being stopped, restarted, rebuilt, or removed from `docker-compose.yml` entirely. There is no `depends_on` between them in either direction, and its healthcheck never contacts the coordinator — a coordinator outage shows up as the *application* reporting disconnected, not as this container reporting unhealthy.

## Using an external broker

If you already run Mosquitto (or another MQTT broker) elsewhere on the show network, point the coordinator at it instead of the bundled one:

1. Set `SHOWMESH_MQTT_BROKER=tcp://<host>:<port>` in `.env`.
2. Remove or comment out the `mosquitto` service and its `depends_on` reference in `docker-compose.yml`, and drop the `mosquitto-data`/`mosquitto-log` volumes if unused.

## Where data lives

The coordinator's SQLite database (ADR-009) and other runtime state live in the named volume `showmesh-data`, mounted at `/var/lib/showmesh` inside the container. The bundled broker's persistence and logs live in `mosquitto-data` and `mosquitto-log`. Named volumes are used instead of bind mounts so ownership and filesystem semantics stay consistent across host platforms.

## Backup

Compose prefixes every volume name with the project name (the directory name by default, `deploy` if you brought the stack up as shown above, or whatever you pass via `-p`/`COMPOSE_PROJECT_NAME`). The named volume is therefore not literally `showmesh-data` on disk; running a backup command against that literal name silently creates a brand-new, empty volume and writes an empty tarball with exit code 0, so always discover the real name first:

```sh
docker volume ls --filter name=showmesh-data
```

That prints the actual volume, typically `deploy_showmesh-data` for the default project name. Named volumes back up with a plain volume copy; no database needs to be quiesced beyond SQLite's own WAL consistency:

```sh
docker run --rm -v deploy_showmesh-data:/data -v "$(pwd)":/backup alpine \
  tar czf /backup/showmesh-data-$(date +%Y%m%d).tar.gz -C /data .
```

Replace `deploy_showmesh-data` with whatever `docker volume ls` actually printed if you used a different project name. Confirm the tarball is non-empty and contains what you expect (`tar tzf backup.tar.gz`) before trusting it as a disaster-recovery artifact; an empty tarball with exit code 0 gives no other signal that the volume name was wrong.

Restore by extracting the same tarball back into the same real volume name before starting the coordinator, for example:

```sh
docker run --rm -v deploy_showmesh-data:/data -v "$(pwd)":/backup alpine \
  sh -c "cd /data && tar xzf /backup/showmesh-data-YYYYMMDD.tar.gz"
```

**After any restore, invalidate every session:**

```sh
docker compose exec coordinator showmesh-coordinator invalidate-all-sessions -yes
```

This is not optional and it is not automatic. Session validity is bounded by a per-principal generation counter that lives **inside** the database, so restoring a backup rolls the counter back along with everything else and every session revoked after that backup point comes back to life. Verified during Step 6: a session killed by a password change authenticated again as `admin` after the data directory was restored from an earlier copy. Auto-detecting a restore from inside the restored database is structurally impossible for the same reason, which is why this is an operator step ([ADR-024](../docs/decisions/ADR-024-identity-authorization-and-audit.md) decision 5).

Prefer the coordinator's own YAML export (ADR-009) for reviewable, secret-free configuration backups; the volume copy above is the full-fidelity disaster-recovery path.

## Upgrade and rollback

No images are published yet (see ADR-012's consequences), so this bundle only ever builds the coordinator from source; `SHOWMESH_VERSION` currently does nothing but stamp the build's `-ldflags` version string, it does not select what gets built. Concretely, today, "upgrading" or "rolling back" means checking out the corresponding git ref and rebuilding:

```sh
git checkout <ref>          # the version you want running
cd deploy
docker compose up -d --build
```

The named data volume persists across this untouched. SQLite schema migrations in ADR-009 are forward-only and refuse to downgrade, so rolling back to an older git ref against a database that a newer ref has already migrated forward is not supported without restoring a pre-upgrade volume backup (see Backup, above).

Do not set `SHOWMESH_VERSION` expecting it to select which code runs; it only labels whatever is in the working tree at build time, and `/version` will report that label even if it does not match the tree.

**Once published images exist** (not yet available), the intended workflow is a pure image-tag change: set `SHOWMESH_VERSION` in `.env` to the desired published tag, uncomment the `image:` line and remove/comment the `build:` block in `docker-compose.yml`, and run `docker compose up -d`. Rollback becomes setting `SHOWMESH_VERSION` back to the previous known-good tag and re-running the same command, still subject to the same forward-only migration constraint above. This paragraph describes intent, not current behavior; do not follow it until images are actually published.

## Preparing for an offline install

ADR-012 requires the stack to start and run with no internet access once images are present, and [ADR-015](../docs/decisions/ADR-015-typescript-spa-frontend.md) extends the same requirement to the Operator UI: no CDN fonts, scripts, or stylesheets fetched at runtime, everything shipped in the image. Neither the bring-up path in this document (`docker compose up -d --build`) nor the UI build is offline-capable *as written*, though: the coordinator build needs the Go module proxy, the UI build needs the npm registry (`npm ci` in `../ui/Dockerfile`), and the backup commands above pull the `alpine` image. Genuinely offline installation is possible today only by preparing the bundle on a connected machine first:

```sh
# On a connected machine:
docker build -t showmesh-coordinator:offline ..
docker build -t showmesh-operator-ui:offline ../ui
docker save showmesh-coordinator:offline showmesh-operator-ui:offline eclipse-mosquitto:2.0.22 -o showmesh-bundle.tar

# Transfer showmesh-bundle.tar to the offline host, then:
docker load -i showmesh-bundle.tar
```

Then, on the offline host, edit `docker-compose.yml` to comment out `build:` and set `image: showmesh-coordinator:offline` for the coordinator service and `image: showmesh-operator-ui:offline` for the `ui` service (or tags of your choosing), and run `docker compose up -d`, which only needs the images already loaded, not a build. This is a manual, unpublished-image workaround, not a polished tag-pull workflow; expect to redo it for every version until published images exist. Runtime operation of the built UI image genuinely needs no network access — it is the *build* step that does, and only on a connected machine.
