# Track H chain: what the assembled path does on running binaries

[Track H](../build/TRACK-H-cues-and-playlists.md) ·
[ADR-043](../decisions/ADR-043-show-scoped-cues-and-playlist-authority.md) ·
[bench scaffolding](../../bench/track-h-chain/README.md)

Status: first assembled run 2026-08-26, on a containerized FPP 10.0, a
coordinator and a render node agent all on one laptop. Seams H1 through H5 were
built by four sessions against fakes of each other; this is the first time they
ran together.

Read the bench README first. It lists the preconditions, and most of the time
this path appears broken it is one of those rather than a defect.

## Safety: this bench can start a playlist on whatever FPP it is pointed at

`run-chain.sh` deploys a cue catalog to a node and sends `Start Playlist` to
`$FPP`. Both `COORD` and `FPP` must resolve to loopback (`localhost`,
`127.0.0.0/8`, or `::1`); the script checks the resolved host, not the URL
string, and refuses before any write if either does not. The refusal names
the interlock, the variable that failed, and the value it resolved to.

The only override is `SHOWMESH_BENCH_ALLOW_NON_LOOPBACK=1`, for pointing the
bench at a deliberately non-loopback DEV target. It is never for the live
fleet.

The wait for FPP to report idle is bounded by `FPP_IDLE_TIMEOUT_SECONDS`
(default 120). On timeout the script exits non-zero with a message naming the
timeout, instead of waiting indefinitely and starting a playlist the moment a
real show happens to end.

`ADMIN_TOKEN` is read from the environment throughout, including by the two
capture scripts that call the coordinator, `capture-observations.sh` and
`capture-reconciliation.sh` (`capture-node-assignment.sh` reads no token; it
only reads the node's own persisted assignment off disk). It is never passed
as a command-line argument to any process, so it does not appear in `ps`
output.

## What the chain is

```
FPP advances a playlist entry
  -> the resident plugin derives the entry key and POSTs an observation
  -> the coordinator accepts it and stores it as that instance's latest
  -> the activation loop reconciles it against the show.playlist bound to
     that FPP instance, resolving one entry and one Cue
  -> it authorizes against the active show, its generation, the resolved
     catalog and the node's asset readiness
  -> it publishes cue.activate to the node and waits for the node's result
  -> the node checks the activation against the catalog it actually holds,
     opens and hash-verifies the Cue's resolved FSEQ, persists the new
     assignment, then stops the old frame writer and starts the new one
  -> the node follows FPP's position over MultiSync, which never selects
     content and is only ever compared against it
```

Two properties of that path are worth stating because they are easy to lose.
The coordinator is not in the frame-rate timing path: position comes from
MultiSync, straight from FPP to the node. And a filename never selects content;
the activated Cue is the only selection authority, and MultiSync's filename is
compared against it so a disagreement becomes a stated refusal.

## What the first assembled run found

The chain did not work. Every observation was accepted and then resolved to
`unknown-entry`, permanently, for every entry of every playlist.

FPP's playlist callback passes its **runtime** section string, and FPP 10.0
sets that to `LeadIn`, `MainPlaylist`, `LeadOut` or `New`. The playlist
definition file, which the entry key is anchored to, spells the same sections
`leadIn`, `mainPlaylist` and `leadOut` as JSON member names. The coordinator
derived entry keys from the definition member names; the plugin hashed FPP's
runtime string. Two SHA-256 inputs that differ produce two keys that differ, so
no binding could ever match.

The shared fixtures both repositories run did not catch it and could not have:
they supply a section string and check the derivation, so both sides agree
perfectly on a value neither side is required to produce. The contract fixed
the entry key's five inputs byte for byte and never fixed which of FPP's two
spellings goes on the wire.

The plugin was the side that changed, because the fixtures already used the
definition member name. Its entry lookup was wrong in the same few lines for a
related reason: it read the entry's filenames out of `playlist[section]`, but
the object the callback receives is FPP's `Playlist::GetInfo()`, which carries
no section arrays under any spelling. The corroborating `sequenceFilename` had
been absent from every observation ever sent. Those values are in
`currentEntry`.

## The cases that ran

Each one was run against running binaries with evidence read from the component
that produced it. Where a case exposed a defect, that is recorded here rather
than smoothed over.

### The multi-sequence case

FPP advanced through a two-entry playlist. The plugin reported entry 0 and then
entry 1 twenty seconds apart. The coordinator dispatched each Cue exactly once.
The node's own persisted assignment moved to the first Cue's FSEQ and then to
the second's, each about thirty milliseconds after the dispatch that caused it,
recording the content hash of the file it had opened and verified.

This is the case a node was previously observed to fail: it pinned one FSEQ for
a whole session and rendered sequence one throughout while reporting no fault.

### A Cue that disagrees with MultiSync

A Cue was deliberately repointed at the wrong sequence. The node refused, named
both filenames in its reason, and did not move its assignment. Content did not
switch and the refusal was not reported as success.

### Duplicate filenames at different positions

A third entry reusing the first entry's filename was added and bound to a third
Cue. Positions 0 and 2, identical filenames, resolved to different entries and
different Cues. No filename guessing anywhere.

### An FPP playlist edited behind the binding

Editing the playlist in FPP changed its hash. Reconciliation reported
`stale-import` naming both hashes, held the old binding, activated nothing and
left the node's content alone. That half is right.

Readiness is not. It compares the bound hash against the latest stored
observation, and the last callback of every playlist run is legitimately an
unavailable observation, because FPP empties the playlist name once it goes
idle. So from the moment a playlist finishes until the next one starts,
readiness cannot run that check and reports `ready: true` with a warning. That
window is the whole afternoon, which is exactly when an operator checks.

### A cross-show observation

With a different show made active, the FPP observation resolved to
`cross-show`, naming both the bound playlist's show and the active one, for
every entry. Nothing was dispatched and the node's content did not change.

### Switching the active show

The switch took a new generation immediately and the coordinator resolved a new
catalog. The node's held catalog stopped authorizing anything, which is right.

The node kept rendering the previous show's content, which the transition
policy asks for, and reported `pipeline.state: running` with no qualification.
The policy says such a render is reported as `superseded` and "never reported
as current or healthy". There is no superseded state and no authorization tuple
on the render report, so that half is unimplemented: after a show switch, stale
content on a wall is indistinguishable from current content.

### Broker loss and recovery

The broker was stopped mid-playlist. The node kept rendering and kept following
MultiSync position locally. FPP advanced to the next entry during the outage;
the coordinator's dispatch failed and was recorded as failed rather than
confirmed, and the node did not switch content on the filename change it could
see. When the broker came back and FPP advanced again, the coordinator
dispatched the entry that was actually playing and did not replay the one it
had missed.

Worth stating plainly: for the twenty seconds of that outage the wall was
showing the previous sequence while FPP played the next one. That is the
designed direction, and it is better than switching on a filename, but an
operator will experience it as the wall being wrong.

### Refusals

* **Stale catalog.** A Cue revised without redeploying the catalog left the
  node holding an older revision. It refused every activation with
  `stale-catalog`, from its own evidence, and content did not change.
* **Sequence regression.** An observation carrying a lower sequence than the
  last accepted one was refused with 409, naming both sequences, leaving the
  stored observation untouched.
* **Missing asset.** Refused with `asset-missing`. The reason names nothing:
  not the sequence, not the asset. Worse, the gate is the node's whole asset
  manifest rather than the activated Cue's own assets, so one unrelated missing
  sequence refuses every Cue on that node. This was what blocked the first
  otherwise-working run.

## Cases not run here, and why

* **Unknown cue** is not separately reachable on this bench. Any route to an
  unknown Cue also changes the catalog revision, so the node refuses with
  `stale-catalog` first. It would need a hand-built activation.
* **Resource conflict** cannot be run because the resource claim model does not
  exist. The track document specifies four derived exclusive claim kinds
  checked at authoring, readiness, activation and dispatch; none of it is
  built.
* **Announcements, auxiliary audio concurrency, and LTC** were out of scope for
  this run.

## What no container run closes

Real FPP hardware, a real render node, real NDI output, real audio hardware, a
real Resolume instance, and a wall. On this bench the render surface was
configured with the `hdmi` transport, which has no output sink in this build
and therefore ran the diagnostic fallback sink: frames were counted at 40 fps
and nothing displayed them. The NDI transport was tried first and could not be
used, because `ndisink` resolves on a machine with the GStreamer plugin present
but no NDI runtime behind it, and fails to preroll.

The node-side evidence for a content swap is read off the node's own disk,
because the render report carries no filename and no content hash. Until that
changes, no coordinator route can answer "what is this node rendering".
