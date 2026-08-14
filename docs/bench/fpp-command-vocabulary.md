# FPP command vocabulary — bench capture

[Documentation index](../README.md) · [Build plan](../build/BUILD-PLAN.md) · [Build log](../build/BUILD-LOG.md)

This is the Step 8 deliverable BUILD-PLAN names first and calls "a deliverable,
not a preliminary": FPP's actual command list, its actual argument encoding, and
its actual behaviour, captured from a real `fppd` before any command was named in
a specification.

It exists because Step 7's plan named a command FPP does not have (`Stop
Playlist`) and a confirmation signal the collector does not emit
(`fpp.status.player_state`). Both read as entirely plausible. **A plan may name an
external system's vocabulary only from that system's own output.**

The second half of this document is the exclusion register: every command in the
capture that Step 8 does *not* ship, with the reason. That is also a deliverable
rather than a silent omission, by the owner's decision on 2026-08-13.

## Provenance

| | |
|---|---|
| Source | `bench/fpp-multisync/`'s containerized `fppd`, service `fpp-master` |
| FPP version | `9.5.3`, git tag pin, `LocalGitVersion 7979a4bb0` |
| Platform | `Debian` / `Variant: Docker` / `OSVersion v2025-11` / bookworm, kernel `6.12.65-linuxkit`, **aarch64** |
| Mode | `player` |
| Captured | 2026-08-13 |
| Raw artifact | [`bench/fpp-multisync/captures/fpp-9.5.3-commands.json`](../../bench/fpp-multisync/captures/fpp-9.5.3-commands.json) — `GET /api/commands`, 51 commands, sorted for diffability |

**Nothing in this document was captured from the deployed fleet.** Step 8's target
discipline is Step 7's, unrelaxed: no write, no command, no restart, no settings
change and no MQTT publish against a deployed FPP host. See §7 for why that
matters more here than it looks.

Evidence level: **L1 for FPP 9.5.3 on this platform.** The deployed fleet runs 9.4
on two hosts and a master-branch build on the third, and §6 establishes that the
command list is assembled at runtime rather than fixed by version, so this capture
does not describe those hosts and must not be treated as if it does.

## 1. The argument encoding, proven rather than assumed

FPP accepts a command in two forms. Both were exercised.

### 1.1 GET, positional path segments — `GET /api/command/{name}/{arg1}/{arg2}/...`

This is the form Step 7's `Client.Invoke` already builds for the zero-argument
case. Arguments are **positional path segments**, not query parameters and not a
body.

```
GET /api/command/Volume%20Set/55        -> 200  "Volume Set"    volume becomes 55
GET /api/command/Volume%20Set?volume=33 -> 500  "Not found"     volume unchanged
```

The query form is not a variant that happens to be unidiomatic. It does not work
at all, and it fails as a **500**, not a 4xx.

### 1.2 POST, JSON body — `POST /api/command` with `{"command": ..., "args": [...]}`

```
POST /api/command  {"command":"Volume Set","args":["22"]}  -> 200  volume becomes 22
POST /api/command  {"command":"Volume Set","args":[70]}    -> 200  volume becomes 70
```

`args` accepts JSON strings and JSON numbers alike. Every one of the eight
primitives §4 ships was then dispatched against the bench in exactly the encoding
ShowMesh emits — `{"command": ..., "args": [...]}` with `args` always present and
always an array of strings, including `[]` for the five zero-argument primitives —
and each produced its captured effect. The zero-argument POST case is measured,
not inferred from the GET form's truncation rule.

This form is confirmed to be
FPP's own canonical internal representation, not a translation layer: when a
command arrives over the GET path form, `fppd` republishes it to its own MQTT
`command/run` topic as exactly this shape, with the path segments already
decomposed into a positional string array.

```
[Command] Commands.cpp:393: GET MQTT Publishing command: command/run,
  payload: {"args":["1000","RGB Cycle"],"command":"Test Start","trigger":"api-get"}
```

### 1.3 The GET form cannot carry an argument containing `/`, and that is Apache's decision, not FPP's

```
GET  /api/command/Start%20Playlist/foo%2Fbar   -> 404, Apache's own HTML error page
POST /api/command {"command":"Start Playlist","args":["foo/bar"]} -> 200, reaches fppd
```

`AllowEncodedSlashes` is off in FPP's shipped Apache configuration, so a
percent-encoded slash is rejected at the web server before `fppd` sees the
request. Any argument that may contain a path separator — media filenames under a
subdirectory, script arguments — is unreachable through the GET form.

**Decision for Step 8: ShowMesh encodes arguments using the POST form.** Not
because it is more idiomatic, but because it is the only form with no
argument-value it cannot express, and because it is the shape `fppd` normalizes to
regardless. The GET form's escaping rules are a property of a web server
configuration ShowMesh does not control on a host it does not own.

This is a change of HTTP method on ShowMesh's outbound path to FPP. It has no
bearing on the collector, which remains GET-only with `CheckRedirect` forced to
refuse; the two clients stay separate and the existing test that fails if they are
ever merged stays load-bearing.

Choosing the form that *can* carry a `/` is not the same as passing one through.
FPP resolves a playlist name to `/home/fpp/media/playlists/{name}.json`, so
ShowMesh rejects `/`, `\`, and `..` in a playlist name before dispatch. The
encoding decision is about not having an argument value the transport cannot
express; the traversal rule is a separate, ShowMesh-side restriction on what it is
willing to ask for.

### 1.4 Arity, and what `optional` actually means

Required arguments must all be present. Optional arguments may be **truncated from
the right** and never skipped in the middle — they are positional.

```
GET /api/command/Volume%20Set                          -> 500 "Not found"   (0 of 1 required)
GET /api/command/Volume%20Set/44/99                    -> 500 "Not found"   (1 required, 2 given)
GET /api/command/Start%20Playlist/showmesh-test        -> 200               (1 required, 3 optional omitted)
GET /api/command/Start%20Playlist/showmesh-test/false  -> 200
GET /api/command/Start%20Playlist/showmesh-test/false/false/false -> 200
POST /api/command {"command":"Volume Set","args":[]}   -> 500 "Not found"
POST /api/command {"command":"Volume Set"}             -> 500 "Not found"
POST /api/command {"command":"Volume Set","args":null} -> 500 "Not found"
```

Note the last three: absent `args`, `args: []`, and `args: null` are all rejected
identically by FPP. That is FPP being consistent, and it is not licence for
ShowMesh to be — see §4.

### 1.5 FPP does not validate argument values. It coerces them.

```
GET /api/command/Volume%20Set/999  -> 200  volume becomes 100   (clamped, silently)
GET /api/command/Volume%20Set/abc  -> 200  volume becomes 0     (coerced, silently)
```

A garbage argument is not an error. It is a successful command with a value the
operator did not ask for, and for volume specifically the coercion target is
*zero*. **ShowMesh must validate every argument itself before dispatch**; there is
no version of "let FPP reject it" that works.

Boolean arguments are parsed as `args[n] == "true" || args[n] == "1"` — read
directly from `PlaylistCommands.cpp`. Everything else, including `"TRUE"`,
`"yes"`, and `""`, is false.

### 1.6 Error shapes

| Situation | Status | Body |
|---|---|---|
| Unknown command, GET path form | `404` | `Not Found` |
| Unknown command, POST form | `500` | `No Command: Stop Playlist` |
| Wrong arity, either form | `500` | `Not found` |
| Argument missing/empty where required | `200` | `Playlist is a requirement argument` |

The last row is not a typo. See §2.

## 2. The finding that governs this entire step: FPP's 200 means nothing

Every command below returned HTTP 200 with an encouraging body and changed nothing
at all.

```
GET /api/command/Start%20Playlist/no-such-playlist  -> 200 "Playlist Starting"  status stays idle
GET /api/command/Start%20Playlist/                  -> 200 "Playlist is a requirement argument"
GET /api/command/Pause%20Playlist    (while idle)   -> 200 "Playlist Paused"    status stays idle
GET /api/command/Resume%20Playlist   (while idle)   -> 200 "Playlist Restarted" status stays idle
GET /api/command/Next%20Playlist%20Item (while idle)-> 200 "Next Item Playing"  status stays idle
GET /api/command/Prev%20Playlist%20Item (while idle)-> 200 "Prev Item Playing"  status stays idle
```

`Start Playlist` against a playlist that does not exist reports `Playlist
Starting`. Read `StartPlaylistCommand::run` and it is obvious why: the result
string is constructed unconditionally, after a call whose failure is not consulted.

[ADR-003](../decisions/ADR-003-desired-and-observed-state.md)'s
confirmation-by-evidence is not a refinement on top of FPP's response here. It is
the only thing standing between the operator and a typo that reports success.

## 3. Behaviour captured per command family

All observations below are from `GET /api/fppd/status`, which is the document the
FPP REST collector already reads.

### 3.1 `status_name`, the complete vocabulary

Read from `httpAPI.cpp`, not inferred: `testing`, `idle`, `playing`, `stopping
gracefully`, `stopping gracefully after loop`, `stopping now`, `paused`,
`unknown`.

### 3.2 Start

`Start Playlist` **always replaces whatever is running.** Captured live: with
`showmesh-bench-3item` playing at item 2, `Start Playlist/showmesh-test` left
`status=playing playlist=showmesh-test index=1/1`. The implementation stops the
current playlist first, waits 150 ms, and starts the new one.

`ifNotRunning` does **not** mean "only start if nothing is running." The guard is
`if (!iNR || args[0] != Player::INSTANCE.GetPlaylistName())` — it means *if this
playlist is not the one already running*. Captured both ways:

- `showmesh-test` playing, `Start Playlist/showmesh-bench-3item/false/true` → swapped to `showmesh-bench-3item`. The flag did not protect the running show.
- `showmesh-bench-3item` playing at item 2, `Start Playlist/showmesh-bench-3item/false/true` → **stayed at item 2**, not restarted. The flag did protect it.

So `ifNotRunning` is a restart-suppressor for the same playlist, and nothing else.
An operator reading the name and expecting the show to be safe would be wrong, and
this is the sharpest reason the "start against a busy host" question is a decision
rather than an implementation detail (§5).

`scheduleProtected` maps to `StartPlaylist(..., !scheduleProtected)`: setting it
true asks FPP's scheduler not to override the playlist ShowMesh started.

### 3.3 Stop

| Command | Immediate `status_name` | Terminal `status_name` |
|---|---|---|
| `Stop Now` | `idle` | `idle` |
| `Stop Gracefully/false` | `stopping gracefully` | `idle`, **after the current item finishes** |
| `Stop Gracefully/true` | `stopping gracefully after loop` | `idle`, after the current loop finishes |

Measured: with a 120-second pause item running, `Stop Gracefully/false` held
`stopping gracefully` indefinitely — still there after 5 seconds, still there when
the capture moved on, cleared only by a subsequent `Stop Now`.

**A graceful stop's terminal state is bounded by show content, not by any number
ShowMesh can choose.** Its confirmation predicate cannot be "reached idle". This is
the concrete case behind BUILD-PLAN's "a confirmation predicate and deadline per
primitive — these are not one rule."

`Stop Gracefully/false` issued while already idle left `status_name` at `stopping
gracefully`, which is a state FPP will sit in with nothing playing.

### 3.4 Pause and resume

With `showmesh-test` playing: `Pause Playlist` → `paused`; `Resume Playlist` →
`playing`. Both are no-ops with a 200 while idle (§2). `Resume Playlist` answers
`Playlist Restarted`, which is FPP's wording and not evidence that it restarts
anything — the observed index did not move.

### 3.5 Item navigation, and the hazard in it

On the 3-item bench playlist: index moved `1 → 2 → 3` on successive `Next Playlist
Item`, and `3 → 2` on `Prev Playlist Item`. `Restart Playlist Item` held the index.

**`Next Playlist Item` past the last item ends the playlist.** Captured: at index
`3/3`, one more `Next Playlist Item` left `status=idle playlist='' index=0/0`. Same
on a one-item playlist: a single `Next` stopped the show. "Skip forward" and "stop
the show" are the same command at the last item, and FPP's response is `Next Item
Playing` in both cases.

### 3.6 Volume

`Volume Set/50` → 50. `Volume Increase/10` → 60. `Volume Decrease/25` → 35.
`Volume Adjust/5` → 40. `Volume Adjust/-5` → 35. All report `Volume Set`.
The value is visible as `volume` in `/api/fppd/status`, which the collector reads
as `fpp.volume`. Volume did **not** survive an `fppd` restart during this capture
(70 before, 0 after).

### 3.7 Toggle

`Toggle Playlist/showmesh-test/false/Now` from idle started it; the identical call
while it was playing stopped it (`Playlist Stopping`, then `idle`). The `stop`
argument is a closed set: `Gracefully`, `After Loop`, `Now`.

### 3.8 `Test Start` crashed `fppd`

```
GET /api/command/Test%20Start/1000/RGB%20Cycle   -> 502, then fppd gone
fppd.log: Crash handler called in thread 68: signal=11 (SIGSEGV: Segmentation fault)
```

The container stayed up; `fppd` did not, and the whole FPP web API returned 503
until it was restarted. Reproduced from the documented argument values —
`UpdateInterval` within its declared `min`/`max`, `TestPattern` taken verbatim from
the `contentListUrl` FPP's own metadata points at (`api/fppd/testing/tests/`).

This is FPP 9.5.3 on aarch64 in Docker. It has not been reproduced elsewhere and no
upstream issue was filed as part of this capture. Recorded because it is the one
command in this vocabulary observed to take an FPP host off the air, and because
the argument type involved, `subcommand`, appears nowhere else in the list.

### 3.9 `Outputs On` / `Outputs Off` do not move `channelOutputsEnabled`

Both returned `200 OK` and `channelOutputsEnabled` stayed `false` throughout.
Reading the source explains it: `channelOutputsEnabled` is `HasChannelOutputs()` —
whether any channel output is *configured* — while `Outputs On`/`Outputs Off` drive
`OutputMonitor`'s per-port enable, surfaced in `/api/fppd/ports` as each port's
`enabled` field. The collector does read that, as `fpp.port.<key>.enabled`. The
bench `fppd` has no cape, so `/api/fppd/ports` returns `[]` and there is no port to
confirm against here.

**The signal name that looks like it reports this command's effect reports
something else, and it decodes without error.** That is the same shape as Step 3's
`MultiSyncEnabled` finding: correct-looking, wrong, and its test would pass either
way.

## 4. What Step 8 ships

Eight primitives, of which one already existed. Every one is confirmed through a
signal the collector already collects, on evidence post-dating dispatch.

| ShowMesh action | FPP command | Arguments | Confirming signal |
|---|---|---|---|
| `startPlaylist` | `Start Playlist` | `name`, `repeat`, `ifNotRunning` | `fpp.status` = `playing` **and** `fpp.playlist.name` = `name` |
| `stopPlaylist` | `Stop Now` | — | `fpp.status` = `idle` |
| `stopPlaylistGracefully` | `Stop Gracefully` | `afterLoop` | `fpp.status` entered a stop state — see below |
| `pausePlaylist` | `Pause Playlist` | — | `fpp.status` = `paused` |
| `resumePlaylist` | `Resume Playlist` | — | `fpp.status` = `playing` |
| `nextPlaylistItem` | `Next Playlist Item` | — | `fpp.playlist.index` moved, **or** `fpp.status` = `idle` |
| `prevPlaylistItem` | `Prev Playlist Item` | — | `fpp.playlist.index` moved |
| `setVolume` | `Volume Set` | `volume` | `fpp.volume` = `volume` |

Three of those predicates need their reasoning on the record.

**`startPlaylist` confirms on identity, not just on `playing`.** FPP is the
authoritative scheduler and may start a playlist by itself between dispatch and
poll. `status == playing` alone would credit ShowMesh with FPP's own scheduled
start — the exact mirror of Step 7's 179-microsecond defect. Requiring
`fpp.playlist.name` to equal the requested name makes that coincidence
vanishingly unlikely rather than routine.

**`stopPlaylistGracefully` confirms on entering the stop state, never on reaching
idle**, because §3.3 measured the terminal state as bounded by show content. Its
success condition is that FPP accepted the graceful stop and is now winding down;
its outcome reason says so explicitly, so the operator is never told the show has
stopped when it has not.

**`nextPlaylistItem` accepts `idle` as confirmation** because §3.5 established that
next-past-the-end is how a playlist ends. A predicate that only accepted a moved
index would report `unconfirmed` for the one case where the command had the
largest possible effect.

Two limits are stated rather than hidden.

`nextPlaylistItem` and `prevPlaylistItem` confirm on a counter FPP also advances by
itself. The evidence post-dates dispatch and shows movement, which is the rule
ADR-003 states, but the movement is **not uniquely attributable** to ShowMesh: an
item boundary falling inside the confirmation window produces the same reading.
Both carry that limit in their outcome reason. The primitives whose predicate names
a specific value — start-by-identity, pause, resume, set-volume, stop — do not have
this problem, and the two navigation commands cannot be given a predicate that
does, because their only observable is an integer.

`scheduleProtected` is deliberately not exposed on `startPlaylist`. It exists to
stop FPP's scheduler overriding a playlist, and
[ADR-001](../decisions/ADR-001-fpp-is-authoritative.md) makes FPP the authoritative
scheduler. ShowMesh sends three arguments and leaves the fourth at FPP's own
default, which is the scheduler keeping authority. Exposing it would be ShowMesh
overriding FPP's schedule through an argument, which is the constraint ADR-001
exists to prevent, arrived at sideways.

## 5. The design question, answered rather than discovered

**What a start means when issued against a host already playing something else.**

FPP's own answer, from §3.2, is that it silently replaces the running show, and
that `ifNotRunning` does not change this. ShowMesh's answer is different and is a
decision:

**`startPlaylist` never silently replaces a running show.** The request carries an
explicit `ifBusy` field with two values:

- `refuse` — the default, and what an absent field means. If a playlist other than
  the requested one is running at dispatch time, the command is refused before
  anything is sent to FPP, and the response names what is playing.
- `replace` — the operator has said, in this request, that they mean to interrupt
  the running show. ShowMesh dispatches, and FPP does what §3.2 describes.

Two reasons this is the default rather than a mirror of FPP's behaviour. Starting
the wrong playlist over a running show is this project's first write whose failure
mode is *the display doing something* rather than stopping, and BUILD-PLAN says the
direction reverses in this step. And the operator's realistic mistake — a stale UI,
a macro fired twice, a scheduler running while someone hits a button — is exactly
the case where FPP's silence is worst.

`refuse` is evaluated against the collector's current evidence, which can be stale.
It is therefore a guard, not a lock: it reduces the accident, and it cannot prevent
a race against FPP's own scheduler. Stated here so nobody later mistakes it for
mutual exclusion. When the evidence is not current, `refuse` refuses and says the
evidence was not current — it never proceeds on the grounds that it could not tell.

## 6. The command list is assembled at runtime, not fixed by version

`GET /api/commands` on the bench returns 51 commands. `Set Port Status` is
implemented in `OutputMonitor.cpp` on this same build and is **not** in that list,
because the bench has no cape for it to register against. Plugins register commands
too.

So the vocabulary is a property of *a host as configured*, not of an FPP version.
Two consequences, both real:

- The exclusion register below is complete for this bench host and is not a
  complete account of any deployed host.
- A ShowMesh action must be reported as unsupported against a host that does not
  register its command, rather than assumed present. Step 8 does not ship that
  check; §8 records it.

## 7. Every command FPP runs is republished to MQTT

From the crash log in §3.8: `fppd` publishes each executed command to its own
`command/run` topic, including commands that arrived over REST, tagged
`"trigger":"api-get"`.

The deployed fleet publishes to `mqtt.bartos.media`, the operator's live
home-automation broker, and `falcon/player/<host>/command/run` is a topic FPP
*acts on*. A ShowMesh command sent to a deployed host would appear on the
operator's live broker as a command message. The standing rule — bench only, never
the fleet — is unchanged, and this is one more mechanism by which breaking it would
propagate.

## 8. Exclusion register

Forty-three of the 51 captured commands are not shipped. The owner asked for two
groups. The capture produced a third, and forcing those entries into either of the
first two would have misreported why they were excluded, so they are labelled
honestly instead.

### 8a. Excluded because ShowMesh does not collect the signal yet

ShowMesh's own work. Each records what would confirm it and where that would come
from. These are candidates for a later step.

| Command(s) | Effect | Confirming signal ShowMesh would need | Source |
|---|---|---|---|
| `Outputs On`, `Outputs Off` | Per-port output enable | `fpp.port.<key>.enabled` — **already collected**; the bench `fppd` has no cape, so `/api/fppd/ports` is `[]` and there is nothing to demonstrate against (§3.9) | `GET /api/fppd/ports`, already polled |
| `All Lights Off` | Blanks all channel data | None. Channel data is not observed at all | Would need channel-range or output-data observation; FPP exposes `/api/channel/...` reads |
| `Effect Start`, `Effect Stop`, `Effects Stop`, `FSEQ Effect Start`, `FSEQ Effect Stop` | Running effects | `fpp.effects.running` — does not exist | `GET /api/effects` on the host |
| `Play Media`, `Stop Media`, `Stop All Media` | Background media playback | A background-media signal. `fpp.media.filename` is the playlist item's media and is mode-governed; it does not report background media | `/api/fppd/status` does not carry it; needs investigation before a step commits |
| `Volume Increase`, `Volume Decrease`, `Volume Adjust` | Relative volume | `fpp.volume` **is** collected. Excluded for a different reason — see §8c | — |
| `Trigger Command Preset`, `Trigger Command Preset Slot`, `Trigger Command Preset In Future`, `Trigger Multiple Command Presets`, `Trigger Multiple Command Preset Slots` | Runs an operator-defined preset | Nothing. A preset's effect is whatever the operator put in it, so there is no single signal — confirmation would have to be per-preset | Would require reading `/api/configfile/...` preset definitions and confirming each preset's own effect. Large. |
| `Run Script`, `Remote Run Script` | Executes a shell script | Nothing. Same reason as presets, and worse | — |
| `GPIO` | Sets a GPIO pin | `fpp.gpio.<pin>` — does not exist | `/api/gpio` |
| `Extend Schedule` | Extends the running scheduled item | `fpp.scheduler.next_start_time` is collected but reports the *next* item, not the current one's extension | Needs a scheduler-detail read beyond `/api/fppd/status` |
| `Start Next Scheduled Item` | Advances the scheduler | Partially observable via `fpp.playlist.name`, but which playlist *should* start is not known to ShowMesh before dispatch, so there is no target to confirm against | Would need `/api/schedule` |
| `Clear Locale Cache` | Clears a cache | Nothing, and nothing meaningful to observe | — |
| `Insert Playlist After Current`, `Insert Playlist Immediate`, `Insert Random Item From Playlist` | Splices a playlist into the running one | Partially observable via `fpp.playlist.name`/`index`, but the *resumption* of the original playlist afterwards is the substance of the command and is not observed | Needs playlist-stack observation FPP does not clearly expose |
| `Switch To Player Mode`, `Switch To Remote Mode` | Changes FPP's operating mode | `fpp.mode` **is** collected and would confirm it. Not shipped because it was not demonstrated — the `Test Start` crash (§3.8) took `fppd` down mid-capture and these two were never exercised. Shipping a primitive on an untested predicate is exactly what this document exists to prevent | `/api/fppd/status` `mode_name`, already polled. **This is the cheapest item on this list and the strongest candidate for the next step.** |
| Every `Remote *` command (`Remote Effect Start`, `Remote Effect Stop`, `Remote FSEQ Effect Start`, `Remote Playlist Start`, `Remote Run Script`, `Remote Trigger Command Preset`, `Remote Trigger Command Preset Slot`) | Acts on a *different* FPP host, proxied through this one | The effect lands on another instance. ShowMesh already models every FPP instance separately and can address each one directly, so proxying through a peer would produce a command whose confirmation belongs to a resource the request did not name | Not a missing signal — a modelling mismatch. ShowMesh should dispatch to the target instance itself. |

### 8b. Excluded because FPP does not expose the effect at all

**These need upstream FPP work and are out of scope for the foreseeable future**,
which is the owner's standing position. Recorded so that is a decision rather than
an oversight.

| Command | Effect | What FPP would have to add |
|---|---|---|
| `All Lights Off` | Blanks output | A "blanked" state. FPP writes zeros to the output; nothing reports that it did, and the next frame overwrites it. There is no observable distinguishing "blanked" from "playing black". |
| `Run Script`, `Remote Run Script` | Runs an arbitrary script | Script exit status, or a record of the run. FPP fires and forgets; the API reports neither that a script ran nor how it ended. |
| `Clear Locale Cache` | Clears a cache | Any observable at all. There is none, and there is no plausible signal to add. |
| `GPIO` | Drives a pin | Readback of the *output* pin state. FPP's GPIO API reports configured inputs; a pin ShowMesh set is not read back. |
| `Test Start`, `Test Stop` | Runs a test pattern | `status_name` does carry `testing`, so the *state* is observable. Excluded regardless: `Test Start` segfaulted `fppd` on this build (§3.8). It stays out of ShowMesh's vocabulary until that is understood upstream, independently of confirmation. |

### 8c. Excluded by ShowMesh's own rules, with the signal present

Not FPP's fault and not a missing collector signal. These are excluded by a rule
this project has already decided, and they would be defects if shipped.

| Command(s) | Signal | Why not shipped |
|---|---|---|
| `Volume Increase`, `Volume Decrease`, `Volume Adjust` | `fpp.volume` | **Relative, so they are not idempotent.** A replayed idempotency key that reached FPP twice compounds — this is precisely the failure ADR-024 decision 11's replay handling exists to prevent, and it cannot be prevented for an operation with no absolute target. Their confirmation predicate would also have to read pre-dispatch volume and add to it, which races FPP's own changes. `setVolume` expresses every one of them safely. |
| `Toggle Playlist` | `fpp.status`, `fpp.playlist.name` | **The desired state is not knowable before dispatch.** ADR-003 splits desired from observed state; a toggle's desired state is "whatever the opposite of the current state turns out to be", so there is no value to record in `desired_state` and no predicate to confirm against. `startPlaylist` and `stopPlaylist` express it with the desired state stated. |
| `Restart Playlist Item` | `fpp.playlist.index` | **Its correct behaviour is an unchanged observable.** Captured: the index held at 2 across a restart. A predicate that confirms on "nothing moved" is satisfied by the command never arriving, which is the definition of confirming a dispatch rather than an effect. A position signal would fix this; `fpp.position.seconds` exists but is mode-governed and was not exercised here. |
| `Start Playlist At Item`, `Start Playlist At Random Item` | `fpp.status`, `fpp.playlist.name`, `fpp.playlist.index` | Confirmable in principle, and deliberately deferred rather than excluded on principle: they multiply `startPlaylist`'s argument surface and its `ifBusy` decision (§5) before either has been exercised once against real evidence. Candidates for the step after this one, at which point `startPlaylist`'s rules will have been proven rather than proposed. |
| `Pause Playlist` / `Resume Playlist` while idle | `fpp.status` | Not an exclusion — noted because both report 200 while doing nothing (§2). ShowMesh ships them and they report `unconfirmed` with a stated reason in that case, which is the correct outcome and the one FPP's own response would have hidden. |

## 9. Open items this capture did not close

- The deployed fleet's command lists are unknown, and §6 establishes they may differ from this one. No capture was taken, deliberately.
- `Switch To Player Mode` / `Switch To Remote Mode` were never exercised (§8a).
- `Test Start`'s segfault has no upstream issue and no root cause (§3.8).
- Whether `Play Media` is observable anywhere in `/api/fppd/status` was not determined; no media file exists on the bench.
- `Outputs On`/`Outputs Off` could be confirmed through `fpp.port.<key>.enabled` on a host with a cape. The bench has none.
