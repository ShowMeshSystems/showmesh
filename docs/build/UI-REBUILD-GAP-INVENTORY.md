# Operator UI rebuild: capability with no home in the new UI

`docs/build/UI-RECONCILE-WORKLIST.md` re-checked every row in this document's
Gaps table individually against `main` on 2026-09-02, after the operator UI
overhaul merged as #301. Most rows below were closed by the rebuild in the
days after this document was written. Read that file first for the current
state of each row; the content below is left as originally written.

Compared the API contract against the rebuilt UI on 2026-08-30. The contract
side is `origin/main:api/openapi.yaml` at `ff86986` (147 operations) unioned
with `HEAD:api/openapi.yaml` at `3a787ce` (148 operations); the only difference
between the two is `getCurrentRuns`, which this branch added and `main` does not
have yet. `git diff b579b4f origin/main -- api/openapi.yaml` adds no path and no
operation at all: everything `main` shipped since the fork point tightened
existing audio-session request schemas and added `resting.backgroundAudio.maxGainDb`
to the night-session read. The gaps below are therefore almost entirely surface
the rebuild has not reached yet, not surface that moved underneath it. The UI
side is `ui/src` on `feature/operator-ui-overhaul-2`, read from the working tree
while a merge of `origin/main` was in flight; where that mattered the fact is
stated against `HEAD` explicitly.

Method, so the rows can be re-derived: `ui/src/api/store.ts` defines 137 request
methods, one per contract operation it covers, and `ui/src/api/useModel.ts`
re-exports them. Every screen reaches the API only through that barrel. Grepping
each method name across everything outside `ui/src/api` finds 68 of the 137 with
no caller in any screen, app-shell, or domain file. That set, plus three
operations `store.ts` does not implement at all, is the raw material for the
table. Verified by grep and by reading the screens' import lists; not verified in
a browser and not verified against a running coordinator.

## Gaps

| API surface | What an operator would do with it | Destination | Size | Blocked on |
|---|---|---|---|---|
| `dispatchAudioSessionApply`, `Prepare`, `Start`, `Pause`, `Resume`, `Seek`, `Advance`, `Stop`, `Clear`, `dispatchAudioGainSet`, `dispatchAudioGainFade`, `dispatchAudioOutputMute`, `dispatchAudioOutputUnmute` (13 ops under `POST /nodes/{nodeId}/audio/sessions/{sessionId}/...`) | Drive a node's audio playback live: load a session, start it, pause, seek, advance an item, ride gain, fade, mute the output | Live Control | Large | Nothing in the contract. PR #234 re-keys sessions to `(node_id, id)` and adds per-target resolution, so the target picker should be designed against #234 |
| `dispatchRenderSurfaceApply`, `dispatchRenderSurfaceClear`, `dispatchRenderPipelineRestart`, `dispatchRenderTransportProbe` (`POST /nodes/{nodeId}/render/surfaces/{surfaceId}/...`) | Push a sequence to a projection surface, clear it, restart a wedged pipeline, probe the transport | Nodes (node detail) | Medium | Nothing |
| `dispatchResolumeAction` (`POST /resolume/actions`) for the live verbs: launch clip, launch column, select deck, clear layer, layer bypass, layer master, blackout | Operate Arena during a show. `listResolumeActions` is called today only by the action editor on Shows, never as a control surface | Live Control | Medium | Nothing |
| `getShowActive`, `putShowActive`, `listShowActiveRevisions` (`/config/show.active`) | Activate a show, see which show is active and the activation history | Shows | Small | Nothing |
| `getNightSessionActiveConfig`, `putNightSessionActiveConfig`, `listNightSessionActiveRevisions` (`/config/night.session.active`) | Activate a night session definition, clear the pointer, read activation history | Show Night | Small | Nothing |
| `getNightSession`, `putNightSession`, `listNightSessionRevisions`, `getNightSessionRevision` (`/config/night.session/{id}`), plus `getNightSessionByID` (`GET /night/sessions/{id}`) | Author a night session: identity, show playlist, resting, site control and interlocks, cue rows; view an earlier revision | Show Night | Large | Nothing |
| `getFPPConnectSettingsConfig`, `putFPPConnectSettingsConfig`, `getFPPConnectSettingsConfigRevisions` (`/config/fppconnect.settings`) | Turn the xLights ingestion listener on or off and set the per-file and total asset-directory byte caps. `store.ts` has no method for these at all, and `cmd/showmeshctl/cmd_fppconnect.go` already covers them, so this is a live API-first parity gap | Settings | Small | Nothing. Predates the fork; the old UI had no tab for it either |
| `setPrincipalRole`, `enablePrincipal`, `disablePrincipal`, `resetPrincipalPassword` | Change a principal's role, take one out of service, put it back, reset a password | Access | Medium | D-019, self-ruled A. Option B needs a drawn design first |
| `deleteSession` (`DELETE /session`) | Sign out. No screen or shell element calls it, and the string "sign out" does not appear anywhere in `ui/src` | Shell (or Access) | Small | Nothing |
| `getNodeCueCatalog`, `dispatchCueCatalogDeploy` (`/nodes/{nodeId}/cue-catalog`, `.../deploy`) | See the Cue catalog this coordinator resolves for a node and deploy it | Nodes | Small | Nothing |
| `acknowledgeFPPInstanceUUIDChange` (`POST /fpp/{instanceId}/instance-uuid/acknowledge`) | Clear the conflict marker after an SD card clone, restored backup, or swapped controller | Nodes | Small | Nothing |
| `deleteFPPPlaylistEntryObservation` (`DELETE /integrations/fpp/playlist-entry-observations/{instanceUuid}`) | Clear a wedged stored observation and its sequence anchor, which otherwise refuses every later legitimate observation for that instance | Nodes | Small | Nothing |
| `getFPPPlaylistEntryReconciliation`, `getFPPPlaylistDefinition`, `listFPPPlaylistEntryObservations` | See what the coordinator makes of an instance's latest accepted observation, and the named refusal reason when it resolves to nothing | Shows (Playlists) or Monitor | Small | Nothing. `getFPPPlaylistReadiness` and `getFPPPlaylistDefinitionEntries` are already wired on `ShowsPlaylists.tsx` |
| `restoreResolumeRecovery` (`POST /resolume/recovery/restore`) | Run the crash-recovery restore on demand. `ResolumeConfig.tsx` reads and writes the recovery config but never offers the restore | Resolume Config | Small | Nothing |
| `listResolumeInstances`, `getResolumeInstance` (`/resolume/instances`) | Read Resolume as an observability resource alongside nodes and FPP | Monitor | Small | Nothing |
| `listMacroRuns` (`GET /macro-runs`) | Look at macro run history. `getMacroRun` is wired on `ShowsAutomation.tsx`, so only the list is missing | Shows (Automation) | Small | Nothing |
| `getActionBinding` (`GET /actions/{id}/binding`) | Re-resolve one action's stored target on demand. `listActionBindings` is wired; the single-action recheck is not | Shows (Automation) | Small | Nothing |
| `dispatchFPPCommand` with `start` (`POST /fpp/{instanceId}/commands`, `store.startFPPPlaylist`) | Start an FPP playlist. Live Control wires stop, stop gracefully, pause, resume, prev, next and volume, but not start | Live Control | Small | Nothing. The old UI carried start-conflict classification in `components/FPPStartPlaylistControl.tsx`, now deleted |
| Sixteen revision-history reads: `listShowRevisions`, `listShowSurfaceRevisions`, `listShowCueRevisions`, `listShowPlaylistRevisions`, `listShowActionRevisions`, `listShowMacroRevisions`, `getShowModeConfigRevisions`, `getRenderSettingsConfigRevisions`, `getAssetsSettingsConfigRevisions`, `getAudioSettingsConfigRevisions`, `getFPPMQTTConfigRevisions`, `listFPPEndpointsConfigRevisions`, `getResolumeInstancesConfigRevisions`, `getResolumeRecoveryConfigRevisions`, `listShowActiveRevisions`, `listNightSessionActiveRevisions` | See who changed a configuration object, when, and what it looked like before. Only `getAudioNodeConfigRevisions` is wired, on Settings, Node routing. The D-014 stale-write guard re-reads the object itself, not its revision list, so it does not cover this | Every editor across Shows, Settings, Show Night, Resolume Config | Medium as one shared disclosure, small per screen | Nothing |
| `getCurrentRuns` (`GET /current-runs`, branch-only) | Show authoritative runner playback on the Dashboard. This branch added the operation and no screen calls it | Dashboard | Small | D-006 holds option B until `CurrentRun.targets` is verified on real hardware |
| `listObservations` (`GET /observations`) | Flat filtered observation list. `store.ts` has no method; Monitor, Signals renders observations out of the snapshot instead. Probably no work needed, listed so it is not mistaken for an oversight | Monitor (Signals) | Small | Nothing. Likely a deliberate non-gap |

Twenty-one rows. Operations that are not operator-facing were excluded rather
than counted: `postNodeCueCatalogAcknowledge` (`node:observe`, a node reporting
about itself), `postFPPPlaylistEntryObservation` and `postFPPPlaylistDefinition`
(the FPP plugin reporting about itself).

## Already stamped in the rebuilt UI

The rebuild marks invented or not-yet-real design with the `NotWired` and
`NotWiredBanner` kit elements rather than a `PlannedFeature` component; grep for
`PlannedFeature` in `ui/src` returns nothing. Every occurrence, with its own
stated reason:

| Where | What is inert | Reason recorded in the code |
|---|---|---|
| `LiveControl.tsx:442` | Firing an announcement | `POST /cues/{id}/fire` does not exist. An announcement runs only when its Show Night transition runs (D-008) |
| `ShowNight.tsx:131` | The night timeline rail | `GET /night/session/cycles` does not exist; the session reports only the cycle it is in (D-009) |
| `NodeDetail.tsx:597` | Re-syncing every asset on a node | No endpoint triggers an asset sync to a node |
| `SettingsConnections.tsx:260` | Test a configured connection | No live connection-test endpoint; health comes from the coordinator's own polling |
| `ResolumeConfig.tsx:326` | Test a Resolume address on demand | Same, for Resolume |
| `SettingsNodeRouting.tsx:283` | Output groups picker | Nothing sends `outputGroups`; the agent does not advertise groups yet |
| `SettingsDelivery.tsx:127` | Asset store backend and path | No stored field names a backend or a path |
| `ShowDetail.tsx:289` | Delete a show | No delete endpoint on `/config/show/{id}` |
| `ShowsPlaylists.tsx:475` | Mismatch control | D-015 A: stays inert with a note |

Six of these nine are API gaps, not UI gaps: a fire endpoint, a cycle-history
read, an asset re-sync trigger, a connection test, a show delete, and an
`outputGroups` field. They belong on the API backlog, not this one.

Two items are recorded elsewhere and are not in the table above:

- D-016 item 1, the FPP-sequence staleness signal for a cue, ruled B (add the
  fact to the API) and tracked as SM-383. Waiting on Eric.
- `audio.settings` gained `duckFadeDurationMs` and `duckRestoreFadeDurationMs`
  in `ff86986`, after this branch deleted the old `AudioSettings.tsx`. At
  `HEAD` (`3a787ce`) `SettingsAudioDefaults.tsx` does not carry them; the
  in-flight merge has already added both to the working tree, so treat this as
  closed by that merge rather than as an open gap.

## Landing soon from open pull requests

Read from the PR bodies only; none was checked out.

- **#234, fold multi-node audio to main.** Per-target cue output resolution,
  readiness refusals for a second LTC emitter and for unbound targets, an
  `audio_sessions (node_id, id)` re-key at schema v21, night bed and
  announcements addressed to a list of target nodes, an assets-missing
  readiness condition, and a never-uploaded-sequence refusal. Draft, gated on
  the two-node hardware session. This changes the shape of the audio row in the
  table above: build the audio transport surface against a target list, not a
  single node.
- **#233, Track J fallback-program compiler.** A new `/api/v1/fallback-programs`
  surface (list, per-host read, acknowledge) behind the reserved `fpp:fallback`
  scope, plus a compiler that refuses with six named reasons. The PR body
  records the two GET paths as having no `showmeshctl` read verb yet. When it
  lands, an operator will want to ask before a show whether each FPP host holds
  a valid signed program: that is a Monitor or Nodes read, and it is new
  surface, not a rebuild gap.
- **#229, audio output capability declaration.** Twelve `audio.playback.*`,
  `audio.mix.*` and `audio.transition.*` capability IDs advertised from real
  engine evidence, so `resting:background-audio-output-capabilities:<node>`
  returns healthy, failed, or unknown instead of always `not_verifiable`. This
  is read-only surface that Monitor, Capabilities already renders generically;
  expect it to appear there with no UI work, and expect Show Night readiness to
  get more truthful.
- **#228, audit unavailability never refuses an action.** Adds a standing
  `auditStore.state` / `auditStore.reason` field to `GET /snapshot` and an
  `AuditStoreBanner` in the old UI. That banner component is in the deleted
  tree, so the rebuilt shell needs its own standing banner when this lands.
  Checks were green at `5df8df1`.

## Recommended order

The API is close to frozen but not frozen, so take the rows whose contract is
already settled first and leave the ones that #234 or a design pass will move.

1. **Sign out** (`DELETE /session`). One control, no design question, and the
   UI currently has no way to end a session.
2. **Activate a show** (`putShowActive`) and **activate a night session**
   (`putNightSessionActiveConfig`). Two small writes that make Shows and Show
   Night usable end to end, and the chrome bar's show picker is already drawn
   against the first one.
3. **FPP start playlist**. One button in a block that already exists, and the
   only transport verb missing from Live Control.
4. **The shared revision-history disclosure.** One kit element reused by every
   editor turns sixteen unwired reads into one change, and the D-014 guard has
   already put revision numbers in front of every editor.
5. **Render surface apply, clear, restart, probe** on node detail. Settled
   contract, self-contained block, and it is the projection path.
6. **Night session authoring** (`putNightSession` and its revisions). Large,
   but nothing else unblocks it and Show Night is otherwise read-only.
7. **Resolume live actions** on Live Control, then **restore recovery** on
   Resolume Config. Settled contract; group them so the Arena story lands once.
8. **The small node and FPP hygiene reads and writes**: cue catalog deploy,
   instance-uuid acknowledge, clear stored observation, reconciliation read.
   Individually tiny, collectively the difference between fixing a wedged node
   from the UI and reaching for `showmeshctl`.
9. **FPP Connect settings.** Small, and closes a real API-first parity gap
   against `showmeshctl`.
10. **Audio session transport.** Last of the large items deliberately: wait for
    #234 so the surface is built against per-target sessions rather than being
    built twice.
11. **Access role, enable, disable, password**, only after D-019 gets a drawing.
    Adding four undrawn controls to the credentials screen is the pattern the
    rebuild exists to stop.

Not scheduled here: `getCurrentRuns` (waits on the D-006 hardware check),
`listObservations` (probably not wanted), and the six API-side gaps behind the
`NotWired` markers.
