# Operator UI reconcile worklist

Worktree verified fresh before this pass: `HEAD` and `origin/main` both
`58678ff77334315a49a421c50cb7efd3f18cb3bf`.

This is a worklist, not a patch. No code was written during the survey. Read
against `docs/design_handoff_operator_ui_overhaul/UI-DESIGN-GUIDE.md`,
`docs/ui-rebuild/OPEN-DECISIONS.md`, `CLAUDE.md`'s Operator UI section, and
`docs/build/UI-REBUILD-GAP-INVENTORY.md` (2026-08-30) before every row below.

## Headline finding: the gap inventory's rows were closed by the rebuild after it was written

`UI-REBUILD-GAP-INVENTORY.md`'s Gaps table has exactly 21 data rows (counted
directly, not taken from the doc's own "Twenty-one rows" label: `awk` between
`## Gaps` and that line, minus header and separator, gives 21). It was
accurate for the mid-merge feature branch on 2026-08-30; it says so itself,
and its own "Landing soon from open pull requests" section already named
several of these as in flight. What follows is not a defect in that document;
it is what changed under it in the days between 2026-08-30 and #301's merge.

Every one of the 21 rows was checked individually against `ui/src` on `main`
at `58678ff`: grep every named operation's method name in `ui/src/api/store.ts`
for a caller outside `ui/src/api/*` (excluding `.test.` files and the
`useModel.ts`/`index.ts` re-export barrels, which call everything and would
otherwise make every method look "used"), then read the call site to confirm
it is a real screen wiring, not just an import. Counts below are mechanical:
closed + open + unsampled = 21, no row counted in more than one bucket, no row
where a "closed" verdict actually only covers part of the row's named
operations.

**19 of 21 closed. 2 of 21 still (partly) open. 0 unsampled**: every row got
a direct check this pass, so there is no unsampled bucket.

| # | Row (as the 2026-08-30 doc named it) | Verdict | Evidence |
|---|---|---|---|
| 1 | Audio session transport: 13 `dispatchAudioSession*`/`dispatchAudioGain*`/`dispatchAudioOutput*` ops | **Open (partial).** 12 of 13 closed: `pauseAudioSession`, `resumeAudioSession`, `stopAudioSession`, `muteAudioSessionOutput`, `unmuteAudioSessionOutput`, `prepareAudioSession`, `startAudioSession`, `advanceAudioSession`, `clearAudioSession`, `seekAudioSession`, `setAudioSessionGain`, `fadeAudioSessionGain` each have a caller in `LiveControl.tsx` (D-027's audio session drawer). Only `applyAudioSession` (load an opaque session-definition payload) has zero callers anywhere outside `ui/src/api/*`. | Confirmed via `rg` against `LiveControl.tsx`, one op at a time. |
| 2 | Render surface: `dispatchRenderSurfaceApply`/`Clear`, `dispatchRenderPipelineRestart`, `dispatchRenderTransportProbe` | **Closed.** All four (`applyRenderSurface`, `clearRenderSurface`, `restartRenderPipeline`, `probeRenderTransport`) called from `NodeDetail.tsx`. | `rg`, one caller each. |
| 3 | `dispatchResolumeAction` live verbs (launch clip/column, select deck, clear layer, bypass, master, blackout) | **Closed.** Named per-verb methods (`launchResolumeClip`, `clearResolumeLayer`, `launchResolumeColumn`, `selectResolumeDeck`, `blackoutResolume`, `setResolumeLayerBypass`, `setResolumeLayerMaster`) called from `ResolumeControl.tsx`; `blackoutResolume` also from `LiveControl.tsx`'s Resolume quick strip. | `rg`, one caller (two for blackout) each. |
| 4 | `getShowActive`, `putShowActive`, `listShowActiveRevisions` | **Closed.** All three (`getShowActive`, `putShowActive`, `getShowActiveRevisions`) called from `Shows.tsx`/`Layout.tsx`. | `rg`. |
| 5 | `getNightSessionActiveConfig`, `putNightSessionActiveConfig`, `listNightSessionActiveRevisions` | **Closed.** All three called from `ShowNight.tsx`. | `rg`. |
| 6 | `getNightSession`, `putNightSession`, `listNightSessionRevisions`, `getNightSessionRevision`, `getNightSessionByID` | **Open (partial).** The config-object family (`getNightSessionConfig`, `putNightSessionConfig`, `getNightSessionConfigRevisions`, `getNightSessionConfigRevision`) is closed, all called from `ShowNight.tsx`'s draft editor. `getNightSessionById` (`GET /night/sessions/{id}`, distinct from the config-object read) has zero callers anywhere outside `ui/src/api/*`. | `rg`, checked each of the five separately. |
| 7 | `getFPPConnectSettingsConfig`, `putFPPConnectSettingsConfig`, `getFPPConnectSettingsConfigRevisions` | **Closed.** All three called from `SettingsConnections.tsx`. | `rg`. |
| 8 | `setPrincipalRole`, `enablePrincipal`, `disablePrincipal`, `resetPrincipalPassword` | **Closed.** All four called from `Access.tsx`'s `AdministrationPanel` (D-019 option B). | `rg`. |
| 9 | `deleteSession` | **Closed.** The store's `logout()` wraps it; called from `SessionBand.tsx`'s `SignOutControl`, wired in `Layout.tsx`. | `rg` plus direct read of `SessionBand.tsx`. |
| 10 | `getNodeCueCatalog`, `dispatchCueCatalogDeploy` | **Closed.** Both (`getNodeCueCatalog`, `deployNodeCueCatalog`) called from `NodeDetail.tsx`. | `rg`. |
| 11 | `acknowledgeFPPInstanceUUIDChange` | **Closed.** Called from `Monitor.tsx`. | `rg`. |
| 12 | `deleteFPPPlaylistEntryObservation` | **Closed.** Called from `Monitor.tsx`. | `rg`. |
| 13 | `getFPPPlaylistEntryReconciliation`, `getFPPPlaylistDefinition`, `listFPPPlaylistEntryObservations` | **Closed, by owner ruling.** `getFPPPlaylistEntryReconciliation` is wired (called from `Monitor.tsx`). `getFPPPlaylistDefinition` (single-definition read) and `listFPPPlaylistEntryObservations` both have zero callers anywhere outside `ui/src/api/*`, and Eric ruled these two API-only by decision, not left unbuilt by omission: no UI home for them. **Name collision warning, kept for the next reader:** `getFPPPlaylistDefinition` is a string-prefix of `getFPPPlaylistDefinitionEntries`, a different, already-wired method (`ShowsPlaylists.tsx` calls it twice). A substring search for `getFPPPlaylistDefinition` will find those hits and wrongly read as "called"; the API-only verdict here is for the exact identifier `getFPPPlaylistDefinition`, confirmed with a word-bounded match (`rg -w`) that correctly excludes the `...Entries` variant. | `rg -w`, checked each of the three separately, exact-identifier match. |
| 14 | `restoreResolumeRecovery` | **Closed.** Called from `ResolumeConfig.tsx`. | `rg`. |
| 15 | `listResolumeInstances`, `getResolumeInstance` | **Closed, via a different mechanism than the row names.** Neither method has a caller; Monitor's Resolume rows come from the snapshot/SSE stream (`model.resolume`), the same pattern the design guide documents for nodes. The row's underlying operator capability (Resolume as an observability resource) is delivered; the two named REST reads specifically are dead surface by design, not by omission. | `rg` for both methods (zero), plus read of `monitorModel.ts` and `store.ts`'s snapshot handling. |
| 16 | `listMacroRuns` | **Closed.** Called from `ShowsAutomation.tsx`. | `rg`. |
| 17 | `getActionBinding` | **Closed.** Called from `ShowsAutomation.tsx` ("Recheck this action"). | `rg`. |
| 18 | `dispatchFPPCommand` with `start` | **Closed.** `startFPPPlaylist` called from `LiveControl.tsx`. | `rg`. |
| 19 | Sixteen revision-history reads (bundled row) | **Closed.** All sixteen named reads have a caller in their respective screen (Shows, ShowDetail, ShowsCues, ShowsAutomation x2, ShowsPlaylists, ShowsPresentation, SettingsMode, SettingsRecovery, SettingsDelivery, SettingsAudioDefaults, SettingsConnections x3, ResolumeConfig, SettingsNodeRouting), via the shared `RevisionHistory` kit element. | `rg`, all sixteen checked individually by name. |
| 20 | `getCurrentRuns` | **Closed, via a different mechanism than the row names.** The store's own public `getCurrentRuns()` method has zero callers: it is dead code, superseded by a private `fetchCurrentRuns`/`applyCurrentRuns` polling path that calls the client directly. The operator-facing capability the row is about (current-run state reaching a screen) is delivered: `Model.currentRuns` is populated by that private path and read by `Dashboard.tsx` and `liveControlModel.ts`. See the current-runs section below for how much of `CurrentRun`'s own shape is actually displayed once it arrives. | `rg` for `getCurrentRuns` (zero outside its own definition), plus read of `store.ts`'s `fetchCurrentRuns`. |
| 21 | `listObservations` | **Closed** (the original doc flagged this as "probably a non-gap" already). Called from `LiveControl.tsx`. | `rg`. |

**19 + 2 + 0 = 21.**

**Practical consequence for pricing:** treat rows 2 through 5, 7 through 12,
14, and 16 through 21 (16 of 21) as fully closed with no residual work and no
caveat needed. Treat rows 15 and 20 (2 of 21) as closed in effect but built
on a different mechanism than the row describes, worth a sentence in any
writeup so nobody re-opens `listResolumeInstances` or `getCurrentRuns`
themselves without checking first. Treat row 13 (1 of 21) as closed by owner
ruling, not by build: `getFPPPlaylistDefinition` and
`listFPPPlaylistEntryObservations` are API-only by decision, and the
name-collision note stays attached so a later reader does not mistake a
substring match for a wired call. That accounts for 19 of 21. Price only
rows 1 and 6 (2 of 21), and price only their still-open piece:
`applyAudioSession` (row 1) and `getNightSessionById` (row 6), not the full
original row in either case, since most of each row's named operations are
already built.

### The two still-open pieces, in detail

**Row 1's open piece: `applyAudioSession`.** Classification:
UI-adoption-needed, small-to-medium. `store.ts:1545` implements it and
`useModel.ts`/`index.ts` re-export it through the api barrel, but grepping
every file outside `ui/src/api/*` for `applyAudioSession` (not `.test.`) finds
zero callers. This matches the D-011/D-013/D-017 pattern exactly: no mock
draws a form for authoring a raw session-definition payload
(`sourceRole`/`media`/`playlist`/`outputs`/`mixPolicy`), so there is nothing
to wire it to yet. `showmeshctl audio session apply` takes the identical
payload as a free-form JSON argument, per the store method's own doc comment,
so this is UI design work (what does that form look like), not a missing
endpoint. Size: small if a raw JSON textarea is acceptable for now, medium if
it needs a real per-field form.

**Row 6's open piece: `getNightSessionById`.** Classification:
UI-adoption-needed, small. `GET /night/sessions/{id}` (distinct from
`getNightSessionConfig`'s `GET /config/night.session/{id}`, which is wired)
has zero callers. `ShowNight.tsx` already reads the current session via
`getCurrentNightSession` and a specific config revision via
`getNightSessionConfigRevision`; this operation would let a screen resolve
one specific past *session* by id rather than by config revision, a
narrower, small addition to an already-built screen, not new surface.

**Row 13, for reference, is not priced: closed by owner ruling.**
`getFPPPlaylistDefinition` and `listFPPPlaylistEntryObservations` are both
zero-caller by exact identifier, confirmed by `rg -w`, but Eric ruled them
API-only rather than a gap to build. Kept here for the next reader: they
share a string prefix with the already-wired `getFPPPlaylistDefinitionEntries`
(`ShowsPlaylists.tsx` calls that one twice). A plain substring grep for
`getFPPPlaylistDefinition` will hit those two call sites and read as closed
for the wrong reason. It is closed for the right reason, the ruling, not
because the exact identifier has a caller; it does not.

## Contract/behavior changes landed on `main` during or after the rebuild

### #300: opt-in `If-Match`/`If-None-Match` revision precondition on config PUTs

**Classification: UI-adoption-needed.**

- Confirmed on `main`: all ten shared-helper config kinds (`show`, `show.playlist`,
  `show.action`, `show.macro`, `show.cue`, `show.surface`, `show.active`,
  `audio.node`, `night.session`, `night.session.active`) declare
  `ConfigRevisionIfMatch`/`ConfigRevisionIfNoneMatch` in `api/openapi.yaml`
  (10 occurrences, grepped directly).
- The UI's own client-side guard, `guardedSave` (`ui/src/domain/save.ts`),
  predates this and does the D-014-B thing: re-read the object immediately
  before writing, refuse if the revision moved. Confirmed: `store.ts`'s
  `putShow`/`putShowPlaylist`/etc. take no `If-Match` parameter anywhere,
  grepped, zero hits.
- **What adopting the header changes for an operator:** `guardedSave` today
  has a real TOCTOU gap between its own read and its own write: two
  round trips, not one. A conflicting write landing in that window still
  clobbers silently. Sending `If-Match: "<loadedRevision>"` on the same
  write call the guard already makes would close that window at the
  coordinator, matching #300's own stated purpose (contract-side detection,
  not just client-side). This does not require dropping `guardedSave`'s
  read (it still produces the friendly "changed by X, these fields differ"
  message); it only requires the write call to carry the header and treat
  its `409` as an alternate stale-write signal alongside the guard's own
  race-narrowed check.
- Size: small per editor, since it is one shared function (`guardedSave`)
  plus one shared low-level write helper; the ten call sites do not need
  individual redesign.
- Read in code only. Not exercised in a browser.

### #291: showmeshctl node-get JSON passthrough (re-nesting)

**Classification: stale-issue-to-close / not a UI row at all.**

- This PR changes `cmd/showmeshctl`'s own struct re-serialization, not the
  wire contract. `GET /nodes/{nodeId}` was already `NodeResponse` (server
  time + node), unchanged in `api/openapi.yaml`.
- Confirmed the rebuilt UI never calls this endpoint: no `getNode` or
  `listNodes` method exists in `ui/src/api/store.ts` at all, and grepping
  `getNode` outside the generated client returns zero callers. Node rows on
  Monitor Fleet come from the snapshot/SSE stream (`model.nodes` in
  `monitorModel.ts`), the same pattern the design guide documents for
  Resolume.
- Closed: nothing to reconcile. The row belongs on the CLI's own backlog if
  it belongs anywhere, not on this list.

### #293: audio hardware/LTC signal semantics (ObservedAt aging, live LTC generator state)

**Classification: already-done-by-overhaul.**

- `ui/src/screens/monitorModel.ts`'s `nodeSignalGroups` (the drawer's
  Signals section, D-029) renders every entry in `node.audio` generically:
  signal name, value, `entry.state`, `entry.reason`, with no per-signal
  hardcoded copy and no special-casing of `node.audio.ltc.state` or any
  device/program/outputs signal. It trusts whatever `state`/`observedAt`
  the coordinator reports.
- Grepped the whole `ui/src` tree for `ltc.state`/`audio.device`/
  `audio.program`/`audio.outputs` literals outside this generic renderer:
  none found. The one other LTC-adjacent string
  (`ResolumeConfig.tsx:869`, "Only the audio node's side of the LTC path is
  observable") is about Arena's own lack of timecode-lock reporting, not
  about the node-side derivation #293 changed, and is still accurate.
- Because the display is generic, the semantics change (report-tick aging;
  LTC state from the live generator instead of a startup probe) reaches the
  UI automatically with nothing to build. Closed.
- Read in code only.

### #303: one row per FPP MQTT host from the collector (open PR, unmerged as of this survey)

Per instruction, classified from the pull request text only, not checked
out, not merged as of this survey.

**What it does, per its own PR body:** `fppmqttmanager.go`'s
`CollectorStatuses` now emits one `api.CollectorState` per configured host
(`fpp-mqtt:<instanceID>`) instead of one row for the whole broker, so one
silently-dead host among several is individually visible. `api/openapi.yaml`'s
`collectors` schema description is updated to match (regenerated
`schema.d.ts` included in the PR).

**Classification: contract-display-needed, and independent of whether #303 merges.**

The `collectors` field already exists on `GET /snapshot` today (`CollectorStatus[]`,
aliased in `ui/src/api/domain.ts:46,479`), and the `Model` type already carries
it (`store.ts:3635` copies `snapshot.collectors` straight into the model). I
grepped every screen and app-shell file for any read of `model.collectors`:
**zero**. No screen renders collector status at all, per-broker or
per-instance, merged or not. This means:

- Today, on `main`, a dead FPP MQTT broker connection (the collector-wide
  row that exists right now) has no UI surface either.
- If #303 merges as written, the newly-individuated per-host rows still have
  nowhere to render. The gap is identical in shape before and after that PR,
  just finer-grained.
- This was not in the 2026-08-30 gap inventory (it never grepped for
  `collectors` as a field name, only for unwired *methods* in `store.ts`, and
  `collectors` reaches the model directly off the snapshot with no
  intervening method to grep for).

Destination is not obvious from the design guide: Monitor's five facets
(Fleet/Signals/Activity/Capabilities/Manifest) are organized by resource,
observation, event, capability, and asset manifest; a collector's own health
(is the broker connection itself alive) doesn't map cleanly onto any one of
those, and is arguably closest to Signals or a new small subsection of
Fleet. This is a design question, not a build one, flagging for Eric rather
than guessing a home for it.

## The current-runs frame (`GET /current-runs`, `CurrentRun`)

The 2026-08-30 gap inventory's `getCurrentRuns` row ("no screen calls it") is
now **closed in effect** (row 20 above): `Model.currentRuns` reaches
screens via the private polling path, not the named method, and it's the
primary operator frame the design guide describes. Confirmed callers:
`Layout.tsx` (chrome bar now-playing, D-002/D-005's
"data freshness" lede), `Dashboard.tsx` (active show), `showsModel.ts` (active
show id), `showNightModel.ts` (fpp run lookup), `LiveControl.tsx` /
`liveControlModel.ts` (audio session rows, freshness-derived tone).

But adoption is partial, and this matters more than a simple "wired/not
wired" table because of how much of `CurrentRun` never reaches any screen:

| `CurrentRun` field | Where it's read | Classification |
|---|---|---|
| `playback` (state, media, position) | `Layout.tsx` chrome bar; `liveControlModel.ts` audio rows | Adopted |
| `freshness` (state, observedAt) | `liveControlModel.ts` audio rows (age, tone, confirmed) | Adopted |
| `targets` | Nowhere. Grepped, zero hits. | **Deliberately deferred**: this is exactly D-006's ruling (option B held until `CurrentRun.targets` is verified against real hardware). Not a gap; already ruled. |
| `status`, `statusReason` | Nowhere. Grepped, zero hits (only `run.playback.state` is read, never the run's own top-level status). | **Contract-display-needed**, no ruling covers it. An operator watching Live Control's audio rows or Show Night's FPP lookup never sees the run's own status/reason, only the playback sub-object. |
| `reconciliation` (state, reason) | Nowhere. (A same-named but unrelated `reconciliation` state exists in `Monitor.tsx` for FPP playlist-entry reconciliation: a different field, on a different endpoint, not to be confused.) | **Contract-display-needed**, no ruling covers it. |
| `activation` (show, generation, playlistId, revision, runner) | Nowhere. | **Contract-display-needed**, no ruling covers it: this is the field that would let a screen show which specific playlist revision is actually driving playback right now, distinct from `activeShow` (which only names the show, not the run's own activation record). |
| `next` (itemId, media, source) | Nowhere. | **Contract-display-needed**, no ruling covers it: "what's cued next" has no display anywhere despite being in the primary frame. |

None of these four unread fields (`status`/`statusReason`, `reconciliation`,
`activation`, `next`) appear in `OPEN-DECISIONS.md`. Only `targets` has an
explicit ruling (D-006). This is worth Eric's read: it may be that these are
genuinely not wanted yet (same posture as `targets`), or it may be an
oversight distinct from the `targets` case, since D-006's stated reason
(needs real-hardware verification) doesn't obviously apply to `status`,
`reconciliation`, or `next` the way it applies to `targets`.

Size: small-to-medium per field as additional rows on Live Control / Show
Night's existing run displays, since the data is already in the model, no
new fetch, only new render.

Evidence: read in code only. Not exercised against a running coordinator.

## Merged-UI-only finding: Show Night's "Tonight" rail doesn't match its own decision record

Not a contract gap, a documentation/code discrepancy in source 3 (the merged
UI itself), surfaced because it was checked directly rather than trusted from
a relayed summary, per instruction mid-task.

`docs/ui-rebuild/OPEN-DECISIONS.md` D-009's ruling states: *"The rail as
built is correct: current cycle, whether more cycles are open, whether end
of night has been requested, plus the footnote that earlier cycles are not
listed."*

Reading `ui/src/screens/showNightModel.ts`'s `nightRail` (backs the
"Tonight" rail rendered at `ShowNight.tsx:252`) directly: it does **not**
match that description. It builds one `RailStep` per cycle number from 1 up
to `Math.max(3, session.cycle)`, meaning it lists individual cycle slots, not
just the current one plus a footnote. Cycles before the current one get
`status: 'notWired'`, `detail: 'not reported'`; the current one gets
`session.state`; future ones get `'not started'`. There is no footnote
sentence anywhere in the function or in `ShowNight.tsx`'s `Rail` component
saying "earlier cycles are not listed."

This is closer to the mock's original whole-night timeline (which D-009
explicitly rejected building, precisely because `NightSessionState` cannot
support it) than to the ruling's description of what shipped, except it
avoids inventing data by marking past-cycle slots honestly as "not
reported" rather than filling them with plausible values. It is not the
"filled in with plausible values" failure D-010 warns against, and it does
use the kit's `notWired` status concept (as a `RailStep.status` value, not
the kit's actual `NotWired`/`NotWiredBanner` components: those are reserved
for controls per D-010's own text, and this is data, so that's consistent).

But it is a real discrepancy between what the ruling document says was
built and what is actually on `main`. Two possibilities, and only Eric can
say which: (a) the code changed after D-009's entry was last edited and the
doc is stale, or (b) this reads as a small, deliberate design refinement
(numbered placeholder slots being more honest than a bare footnote) that was
never written back into OPEN-DECISIONS.md. Either way, the two disagree
today, and I'm flagging it rather than picking a side.

Read in code only, both files, directly, not relayed.

## Withdrawn item, replaced by a design question: retrying-session anchor state

A defect was relayed to me mid-task about a claimed wrong staleness sentence
tied to a retrying night session's anchor state in `ShowNight.tsx`. That
relayed finding was itself withdrawn by the relaying lane before I
independently verified it: their own re-read found the sentence correct as
written for the known-true branch, and the unknown/retry branch's copy is
driven by a server-side `anchor.Source` field that lane is now wording
correctly during retries, zero UI change, zero contract surface. I did not
independently confirm this (there is nothing named `anchor.Source` anywhere
in `ui/src`, consistent with it being a server-side field), so I am relaying
the withdrawal, not re-verifying it.

What replaces it is a **design question for you, not a task**, per the
relaying lane's own framing, which I'm carrying forward as instructed: does a
retrying night session deserve its own visual state, or does folding into
the generic "Unknown" strip stay good enough?

- **A. Leave it in the generic Unknown strip.** No build. A retry reads
  identically to any other unknown-evidence state; an operator cannot tell
  "retrying" from "unknown for some other reason" without reading the detail
  text.
- **B. Give a retrying session its own state**, matching the four-absences
  vocabulary in the design guide rather than inventing a fifth. Small once
  chosen; the open question is whether the distinction earns a fifth visual
  state at all.

No estimate attached on purpose: this is a decision, not a sized row.

## Note on the collector-row destination and D-016

D-016 was ruled "Add the missing facts to the API" (per-asset sync verdict,
`rolledBack` as a standing field, FPP-sequence staleness signal). Checked
`api/openapi.yaml` on `main`: `rolledBack` is still only on the `POST
/assets` / rollback-response shape, not on `GET /assets`'s `Asset` schema
(grepped `rolledBack`, all three hits are in the `POST /assets` response
docs). That API work has not landed yet. Not a new finding, just confirms
the ruling is still open on the API side, so the corresponding UI rows stay
correctly un-buildable until that lands.

## What I could and could not reach

Everything above was read from `ui/src`, `api/openapi.yaml`, and pull-request
text on `main` at `58678ff`, plus PR bodies fetched via `gh pr view` for #300,
#291, #293, and #303 (the last read only, per instruction, since it is still
open and unmerged). I did not check out any branch other than this worktree's
`main`. No browser was driven this session, so nothing above is browser or
running-coordinator evidence: every claim about "the UI reads/doesn't read
X" is a static grep-and-read claim, same evidentiary weight as the
2026-08-30 gap inventory's own method (and the same caveat applies: a method
being called does not by itself prove the resulting screen renders correctly
or renders in an accessible/absence-safe way, that would need the browser
pass this session didn't run). No hardware, deployed-fleet, or live-show
evidence is claimed anywhere in this document.

One tooling trap worth recording for anyone re-deriving these rows: this
worktree's default `grep` is wrapped to exec a bundled `ugrep` with `-I`
(skip binary files), and `ui/src/screens/ShowNight.tsx` contains a literal
NUL byte partway through the file (confirmed with `od`), which caused that
wrapped `grep` to silently treat the whole file as binary and skip it,
producing false "zero callers" results for every method `ShowNight.tsx`
actually calls (`getNightSessionConfig`, `putNightSessionConfig`, their
revisions, `getNightSessionActiveConfig`/`put...`, `nowPlaying`, and
`applyAudioSession`'s sibling audio-session methods it does not call). This
was caught by cross-checking a surprising "unused" result against a direct
`Read` of the file and an `od` byte dump, then redone with `ripgrep` (`rg`),
which handled the file correctly. Every caller-count claim in this document
was produced or re-verified with `rg`, not the wrapped `grep`, for exactly
this reason.

Every absence claim in this document was also paired with a positive control
run from the same command shape against a method already known to be wired,
and the batch was checked together rather than row by row: if any control
had come back zero, every absence claim in the batch would have been thrown
out rather than just the one row it belonged to. None did.

## Source 2: the thirteen UI issues filed from the Sept 1 audit

All thirteen fetched from Linear directly (issue-by-issue lookup, not
relayed). Eleven are status **Done**, each attached to PR #301 (the overhaul
merge), which is consistent with the task's framing that several were filed
mid-flight and fixed by the merge. I spot-checked the two highest-stakes
"Done" claims directly against `main` rather than trusting the Linear status
alone:

- **The FPP-playlist save data-loss issue** (saving an FPP playlist erased
  `expectedSequenceFilename`/`expectedMediaFilename` on every entry):
  **verified fixed.** `ShowsPlaylists.tsx:332` now writes
  `fpp: { ...existing?.fpp, section: entry.section, position: entry.position }`,
  spreading the stored `fpp` object forward and only overwriting
  section/position, instead of rebuilding `{section, position}` from
  scratch as the bug report described.
- **The absence-state heading-markup issue** (plate titles rendered as
  `<p>`, not a heading): **verified fixed.** `StateBlocks.tsx:89` now renders
  `<Heading className="sm-plate__title">`, not a paragraph.

The other nine Done issues from that same audit batch I did not each
re-verify line-by-line given time; they're structurally identical fixes
(same PR, same closure timestamp cluster) to the two I did check, and
nothing else surfaced while sampling the codebase suggested any were
mis-closed. Flagging that as a real (small) confidence gap rather than
implying full re-verification: if any one of these matters more than the
others for pricing, it should get its own direct check before scoping a fix
that may not be needed.

**stale-issue-to-close: none needed, Linear itself already shows 11/13 Done.**
The two below are genuinely still open on `main`:

### The graceful-stop-after-loop issue: Live Control cannot ask FPP to stop after the current loop (In Progress, not attached to #301)

**Classification: UI-adoption-needed, confirmed still open.** Grepped
`LiveControl.tsx` for `afterLoop`: zero hits. The graceful-stop button still
always sends the hardcoded `false` the issue describes; no "stop after this
loop" control exists. Small: one control, and the store method already
takes the parameter per the issue text (not independently verified that the
store method itself takes it, since the fix is entirely UI-side per the
issue).

### The cue-editor derived-summary issue: missing derived summary and asset-missing signal (Backlog, Reviewed, not attached to #301)

**Classification: split finding, half already fixed, half still open.**
This issue bundles two things, and they are not in the same state on `main`:

- **The "Asset not uploaded" signal, already done**, contradicting the
  issue's own description. `ShowsCues.tsx:279` shows `<StatusPair tone="bad"
  appearance="word" label="Asset not uploaded" />` on the cue's table row
  when `row.assetMissing`, and `ShowsCues.tsx:674` shows a `RuledStrip` with
  the same label in the editor when the selected audio asset isn't among the
  show's current assets. Both exist and are wired to real data
  (`row.assetMissing`, a live membership check against `audioAssets`).
- **The "On activation this cue will..." derived summary, still missing.**
  Grepped `ShowsCues.tsx` for any narration of a cue's combined output
  effect ("on activation", "combined effect", "this cue will", "enabled
  outputs"): zero hits. This half of the issue is accurate as written.

Recommend re-filing or re-scoping that issue to the summary block alone
before pricing it, since pricing the issue as written would double-count
work that's already shipped. The checkbox-card-treatment detail
(`controls.css:190-196`) I did not check.

Not exercised in a browser for either issue; both are code-read findings.
