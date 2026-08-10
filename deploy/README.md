# ShowMesh coordinator appliance bundle

Docker Compose bundle for the coordinator and its Mosquitto broker (ADR-008), suitable for the appliance deployment described in ARCHITECTURE.md section 10.1. Node agents are not part of this bundle; they run natively on media-node hosts (section 10.2).

## Bring the stack up

```sh
cd deploy
cp .env.example .env   # edit as needed, especially before exposing beyond an isolated show VLAN
docker compose up -d --build
```

This builds the coordinator image from the repo root Dockerfile and starts it alongside the bundled Mosquitto broker. Check status with `docker compose ps` and `curl -fsS localhost:8080/healthz`.

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

ADR-012 requires the stack to start and run with no internet access once images are present, but the bring-up path in this document (`docker compose up -d --build`) needs the Go module proxy to fetch dependencies, and the backup commands above pull the `alpine` image, so neither is offline-capable as written. Genuinely offline installation is possible today only by preparing the bundle on a connected machine first:

```sh
# On a connected machine:
docker build -t showmesh-coordinator:offline ..
docker save showmesh-coordinator:offline eclipse-mosquitto:2.0.22 -o showmesh-bundle.tar

# Transfer showmesh-bundle.tar to the offline host, then:
docker load -i showmesh-bundle.tar
```

Then, on the offline host, edit `docker-compose.yml` to comment out `build:` and set `image: showmesh-coordinator:offline` (or a tag of your choosing) for the coordinator service, and run `docker compose up -d`, which only needs the images already loaded, not a build. This is a manual, unpublished-image workaround, not the polished tag-pull workflow described above; expect to redo it for every version until published images exist.
