# Track H chain bench: FPP, coordinator and a render node, assembled

Bench scaffolding for [Track H](../../docs/build/TRACK-H-cues-and-playlists.md)
seam H7, not part of the product. It runs the multi-sequence case on running
binaries: a containerized FPP advances through a playlist, its ShowMesh plugin
posts each entry, the coordinator resolves the entry to a Cue and dispatches an
activation, and a render node swaps the FSEQ it is rendering.

The runbook, including what each H7 case proved and what it did not, is
[docs/bench/TRACK-H-CHAIN.md](../../docs/bench/TRACK-H-CHAIN.md). This file is
scoped to what has to exist before `run-chain.sh` says anything meaningful.

## What this bench can and cannot close

It can close software behavior for entry identity, reconciliation, activation
dispatch, node-side authorization and the FSEQ swap, and for the refusal
directions: a Cue that disagrees with MultiSync, a stale catalog, a cross-show
observation, an edited playlist, and a sequence regression.

It cannot close anything about real hardware. There is no real FPP host, no
real render node, no NDI output and no wall. A frame counted here was written
into a diagnostic sink. Repeating a container run does not turn it into
hardware evidence.

## Preconditions, in the order they bite

Each of these is a step whose absence produces a failure that looks like
something else. That is why they are listed rather than assumed.

1. **A coordinator, a broker and a render node agent**, all built from the same
   commit. A coordinator older than the node is the first thing to rule out
   when a config push is refused for no visible reason.

2. **A containerized FPP with the plugin installed and provisioned** with a
   coordinator base URL and a credential whose principal holds `fpp:observe`.
   The `scheduler` role is that principal; `operator` deliberately does not
   hold that scope.

3. **The plugin has swept the playlist.** The definition sweep runs at worker
   start, so a playlist created after the plugin loaded is not posted until
   fppd restarts. Until then the coordinator has no definition, and an operator
   trying to bind sees nothing.

4. **MultiSync reaches the node.** Docker's bridge does not carry multicast to
   the host on macOS, so the node never sees a position and renders idle output
   while reporting a running pipeline. Configure FPP with
   `MultiSyncEnabled=1` and `MultiSyncRemotes=<host address the container can
   reach>`; unicast does cross that boundary. Verify by sending a UDP packet
   from inside the container to that address before blaming anything else.

   FPP's settings API answers `{"status":"OK"}` while storing a JSON string
   body with its quotes intact, so a `PUT` of `"1"` lands as `""1""` and every
   reader sees a wrong value. Send the bare value and read
   `/home/fpp/media/settings` back to confirm what landed.

5. **A show, two Cues over two distinct sequences, and an FPP-bound
   `show.playlist`.** The binding's `fpp.playlistHash` must equal the hash the
   plugin posted, exactly. The entries' `fpp.section` must be the playlist
   **definition's** member name, `mainPlaylist`, not FPP's runtime section
   string.

6. **Both sequences' assets uploaded and synced to the node**, and every other
   sequence in that show too. Activation is gated on the node's whole asset
   manifest being ready, not on the activated Cue's own assets, so one
   unrelated missing sequence refuses every Cue on that node with the reason
   `asset-missing`, which names nothing.

7. **A render assignment already applied to the node.** A Cue activation that
   declares a render output fails outright on a node holding no persisted
   assignment: "no surface is currently assigned on this node". Nothing in the
   authoring or deployment flow creates one. Dispatch `render.surface.apply`
   for each surface first.

8. **The cue catalog deployed to the node.** Nothing deploys one on its own.
   `run-chain.sh` does this for you at step 3, but if a Cue changes afterwards
   the node's held revision goes stale and every activation is refused with
   `stale-catalog`.

## Generating the two FSEQ files

`genfseq` writes a small uncompressed FSEQ v2 file whose every frame is one
solid colour, so a rendered frame identifies its source file and the two
sequences cannot be confused:

```sh
go run ./bench/track-h-chain/genfseq Lane14-One.fseq 3072 800 25 255 0 0
go run ./bench/track-h-chain/genfseq Lane14-Two.fseq 3072 800 25 0 255 0
```

Arguments are path, channel count, frame count, step time in milliseconds, then
red, green and blue. The example above is a 32x32 RGB surface, 20 seconds at
40 fps. It verifies each file by reopening it through `pkg/fseq` and printing
what it read back, so a malformed file fails here rather than on the node.

The same bytes must be on the FPP host and uploaded as the ShowMesh asset,
because the Cue's resolved runtime filename is compared against the filename
MultiSync reports and a disagreement is refused.

## Running it

```sh
export ADMIN_TOKEN=...        # config:write and cuecatalog:deploy
export INSTANCE_UUID=...      # the FPP instance UUID the plugin reports
./bench/track-h-chain/run-chain.sh
```

Captures land in `bench/track-h-chain/captures/`, which is git-ignored.
