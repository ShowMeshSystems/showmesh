# Control inventory of the pre-rebuild operator UI

Recorded 2026-08-29 from `feature/operator-ui-overhaul-2` at commit `48ac03d`,
immediately before the old UI is deleted. This is the backlog: every control the
current build has, so that a control missing from a mock is a decision Eric
makes rather than something lost in a deletion.

**This file is not a parity checklist.** The rebuild targets the mocks. A row
here that has no home in a mock stays parked until Eric rules on it in
`OPEN-DECISIONS.md`.

Columns: what the control does · the scope it needs · the API operation behind
it. A blank scope means the control is read-only or gated by the page.

---

## Operate

### `/` Dashboard  → `Dashboard.dc.html` (Readiness · Needs you · System health)

| Control / section | Scope | Behind it |
|---|---|---|
| Presentation path summary | — | model |
| Needs you attention list, per FPP / node / Resolume health | — | derived from health fields |
| Recent activity list | — | model events |
| Tonight's lifecycle strip | — | `useNightSessionState` |
| System health summary | — | model |
| Clock skew warning | — | `effectiveServerTimeIso` |
| Data freshness notice | — | model poll age |

The mock has three blocks. Presentation path, Recent activity, Tonight's
lifecycle and the two notices are the candidates with no home.

### `/control` Live Control → `Live Control.dc.html`

| Control / section | Scope | Behind it |
|---|---|---|
| Active show summary | — | `activeShowDetail` |
| Resolume instance picker | — | model |
| Resolume actions: launch clip, launch column, select deck, clear layer, layer bypass, layer master, blackout | `resolume:action` | `launchResolumeClip`, `launchResolumeColumn`, `selectResolumeDeck`, `clearResolumeLayer`, `setResolumeLayerBypass`, `setResolumeLayerMaster`, `blackoutResolume` |
| Show action list and invoke | `config:write` | `useShowActionObjects`, action invoke |
| Macro list and run | `config:write` | `useMacroObjects` |
| EMERGENCY STOP button | — | **no coordinator capability exists.** Guide §10 item 1 says state its absence |
| FPP transport: pause, resume, prev, next | `fpp:command` | `pauseFPPPlaylist`, `resumeFPPPlaylist`, `prevFPPPlaylistItem`, `nextFPPPlaylistItem` |
| FPP start / stop / stop gracefully / set volume | `fpp:command` | `startFPPPlaylist`, `stopFPPPlaylist`, `stopFPPPlaylistGracefully`, `setFPPVolume` |
| Audio session: prepare, start, pause, resume, stop, advance, seek, gain, fade, mute, unmute, apply, clear | `audio:command` | `AudioSessionPanel` |
| Announcement cues, Fire | `config:write` | `useAnnouncementCues` |

### `/night` Show Night → `Show Night.dc.html` (Cycle · Now playing · Next transition · Lifecycle commands · Run of Show · Evidence)

| Control / section | Scope | Behind it |
|---|---|---|
| Now, Next transition, Lifecycle, Run of Show, Evidence | — | `useNightSessionState` |
| Lifecycle commands: Prepare, Start, End the night, Recovery (provisional) | `night:command` | `dispatchNightCommand`, 202 accepted |
| Final-cycle status, Transition evidence, Power phase evidence | — | model |
| Readiness, Background audio, Degraded state, Authorization | — | model |
| Edit definition, Reload | — | navigation |
| Active definition panel, Activate a different one, Clear the pointer | `config:write` | `getNightSessionActiveConfig`, `putNightSessionActiveConfig` |
| Activation history | `config:write` | `getNightSessionActiveConfigRevisions` |

### `shows/:showId/night-sessions*` → folds into Show Night (D-003)

| Control / section | Scope | Behind it |
|---|---|---|
| Definition list | `config:write` | `getNightSessionConfig` |
| Definition editor: identity and show playlist, resting, site control and interlocks | `config:write` | `putNightSessionConfig` |
| Cue rows: add, update, remove | `config:write` | same payload |
| Revision history, view a revision | `config:write` | `getNightSessionConfigRevisions`, `getNightSessionConfigRevision` |

---

## Author

### `/shows`, `/shows/new`, `/shows/:id` → `Shows.dc.html`

| Control / section | Scope | Behind it |
|---|---|---|
| Show list, all shows | — | model |
| Activate a show, picker and confirm | `config:write` | `putShowActive` |
| Activation history | `config:write` | `getShowActiveRevisions` |
| Show identity editor | `config:write` | `putShow` |
| What this show contains | — | `useShowWorkspaceData` |
| Delete show | `config:write` | show delete |

### `shows/:id/playlists*` → `Show Authoring.dc.html`

| Control / section | Scope | Behind it |
|---|---|---|
| Playlist list | `config:write` | `getShowPlaylist` |
| Entries: add, remove, move up, move down (max entries enforced) | `config:write` | `putShowPlaylist` |
| Mismatch policy choice, safe cue reference | `config:write` | same payload |
| ShowMesh audio enable | `config:write` | same payload |
| Re-import | `config:write` | FPP import |
| Revision history | `config:write` | `getShowPlaylistRevisions` |
| **Playlist readiness** (folded in, D-003): readiness verdicts, FPP instance reconciliation, two Recheck buttons | `config:write` | `getFPPPlaylistReadiness`, `getFPPPlaylistEntryReconciliation` |
| **FPP playlist definitions** (folded in, D-003): definition detail and entries | — | `getFPPPlaylistDefinition`, `getFPPPlaylistDefinitionEntries` |

### `shows/:id/cues*` → `Show Cues.dc.html`

| Control / section | Scope | Behind it |
|---|---|---|
| Cue list, output filter | `config:write` | `getShowCue` |
| Cue editor, "What does this cue do?" | `config:write` | `putShowCue` |
| Announcement policy choice | `config:write` | same payload |
| Revision history | `config:write` | `getShowCueRevisions` |

### `shows/:id/assets` and `/assets` → `Show Assets.dc.html`

| Control / section | Scope | Behind it |
|---|---|---|
| Asset list, kind filter | — | `listAssets` |
| Upload asset | `asset:write` | `uploadAsset` |
| Sequence coverage findings, Upload for a sequence, Run sync | `asset:write` | manifest derivation |
| Asset history, make current, re-sync to node | `asset:write`, `node:read` | `uploadAsset` |
| Per-node manifest verdicts, wide-gap nodes | `config:write` | `getAssetManifest` |

### `shows/:id/presentation*` → `Show Presentation.dc.html`

| Control / section | Scope | Behind it |
|---|---|---|
| Surface list, virtual matrix extent | `config:write` | `getShowSurface` |
| Surface editor: geometry, channel range, output | `config:write` | `putShowSurface` |
| Move to another show | `config:write` | same payload |
| Revision history | `config:write` | `getShowSurfaceRevisions` |

### `shows/:id/automation*` → `Show Automation.dc.html`

| Control / section | Scope | Behind it |
|---|---|---|
| Needs you, Macros, Actions, In a macro, Not in a macro | `config:write` | `useActionFacts`, `listActionBindings` |
| Macro readiness and consequence rollups | — | `deriveMacroReadiness`, `deriveMacroConsequence` |
| Macro step editor: add, remove, move up, move down (max steps) | `config:write` | `putShowMacro` |
| Move macro to another show | `config:write` | same payload |
| Run macro | `config:write` | macro run |
| Macro run detail and steps, expandable step rows | `config:write` | `getMacroRun`, `listMacroRuns` |
| Action editor, including Resolume deck and clip pickers and audio node selection | `config:write` | `putShowAction`, `useResolumeComposition` |
| Revision history, last run | `config:write` | `getShowActionRevisions`, `getShowMacroRevisions` |

Audio action authoring, which the pre-rebuild action form did not offer, is
built: Shows › Automation offers `integration: 'audio'` in both the new-action
and edit-action forms, with its own target editor.

---

## System

### `/monitor/fleet` → `Monitor.dc.html`

| Control / section | Scope | Behind it |
|---|---|---|
| Needs an operator | — | derived |
| Fleet table, kind filter | — | model |
| Node discovery panel, declare a node | `config:write` | `declareNode` |
| FPP version skew notice | — | derived |
| Playlist definitions received | — | `listFPPPlaylistDefinitions` |

### `/monitor/signals` · `/monitor/activity` · `/monitor/capabilities`

| Control / section | Scope | Behind it |
|---|---|---|
| Signals list | — | `presentModelObservations` |
| Activity stream, load older | — | `useAuditLog` |
| Audit rows inside Activity | `audit:read` | `listAudit` |
| Capabilities grouped by capability | — | `groupByCapability` |

### `/monitor/manifest` → new facet (D-003)

| Control / section | Scope | Behind it |
|---|---|---|
| Asset manifest per node | `config:write` | `getAssetManifest` |

### `/monitor/fleet/node/:id` and `/fpp/:id` → `Node.dc.html`

Node detail is now a 960px drawer over Monitor › Fleet (D-023). Three rows below
are marked **not carried**: they are not in that drawer, and the reason is given
beside each. Checked against `ui/src/screens/NodeDetail.tsx` on 2026-09-01.

| Control / section | Scope | Behind it |
|---|---|---|
| Identity, control-plane evidence, capabilities | — | model |
| Cue catalog, deploy | `cuecatalog:deploy` | `getNodeCueCatalog`, `deployNodeCueCatalog` |
| Cue catalog Reload | | **Not carried.** The catalog is read when the drawer opens and re-read after a deploy, so there is no separate Reload control. |
| Surfaces on this node: apply, clear | `config:write`, `render:command` | `applyRenderSurface`, `clearRenderSurface` |
| Render settings panel and revisions | `config:write` | **Not carried.** Render settings and their revision history live on Settings › Render recovery. |
| Audio section | `config:write` | **Not carried.** Audio node configuration lives on Settings › Node routing. The drawer states only that a node advertising no `audio.*` capability has no audio routing to configure. |
| Assets held locally, Re-sync all | `config:write` | `getNodeAssetManifest` |
| Remove this node | `config:write` | `deleteNodeDeclaration` |
| FPP detail: commands, recovery, warnings, pixel ports, observations, current playback | `fpp:command` | see Live Control FPP rows |
| Pending instance uuid change, acknowledge | `config:write` | `acknowledgeFPPInstanceUUIDChange` |
| Clear stored observation | `fpp:command` | `deleteFPPPlaylistEntryObservation` |

### `/monitor/fleet/resolume*` → `Resolume Config.dc.html`

| Control / section | Scope | Behind it |
|---|---|---|
| What Arena is reporting | — | observations |
| Stored composition, upload | `config:write` | `uploadResolumeComposition`, `getResolumeComposition` |
| Clips that cannot be named | — | `groupClipObservations` |
| Crash recovery On / Off, restore | `config:write` | `getResolumeRecovery`, `putResolumeRecoveryConfig`, `restoreResolumeRecovery` |
| Controller, Test connection | `resolume:action` | `listResolumeActions` |
| Recovery revision history | `config:write` | `getResolumeRecoveryConfigRevisions` |

### `/settings/*` → `Settings.dc.html`, seven tabs

| Tab | Control / section | Scope | Behind it |
|---|---|---|---|
| Connections | FPP endpoints rows: add, remove | `config:write` | `putFPPEndpointsConfig` |
| Connections | FPP MQTT | `config:write` | `putFPPMQTTConfig` |
| Connections | Resolume instances: add host, remove host | `config:write` | `putResolumeInstancesConfig` |
| Connections | Event feed | `config:write` | config |
| Content delivery | Asset store settings | `config:write` | `putAssetsSettingsConfig` |
| Render recovery | Render settings and revisions | `config:write` | `putRenderSettingsConfig` |
| Appearance | Theme picker | — | `useTheme` |
| Appearance | Coordinator build string | — | service descriptor (D-002 sends it here or to Capabilities) |
| Audio defaults | Audio settings and revisions | `config:write` | `putAudioSettingsConfig` |
| Node routing | Where this node's audio leaves the building | `config:write` | `putAudioNode` |
| Node routing | Audio node list, not-declared list, new audio node | `config:write` | `getAudioNode`, `putAudioNode` |
| Node routing | Clock verification | `config:write` | audio node config |
| Mode | Show mode picker and revisions | `config:write` | `getShowModeConfig`, `putShowModeConfig` |

Every settings panel has its own Retry after a load failure and its own revision
history disclosure.

### `/access` → `Access.dc.html`

| Control / section | Scope | Behind it |
|---|---|---|
| Principal list | `principal:read` | `listPrincipals` |
| New principal | `principal:write` | `createPrincipal` |
| Enable / disable a principal | `principal:write` | `enablePrincipal`, `disablePrincipal` |
| Set role | `principal:write` | `setPrincipalRole` |
| Reset password (disclosure) | `principal:write` | `resetPrincipalPassword` |
| Tokens list (disclosure), issue, revoke | `principal:write` | `listPrincipalTokens`, `issuePrincipalToken`, `revokePrincipalToken` |
| Attribution | — | model |
| Retry | — | reload |

---

## Session and shell

### `Session States.dc.html`

| Control / section | Scope | Behind it |
|---|---|---|
| Signed out on this device band | — | `GET /session` 200 `authenticated:false` |
| Sign in form | — | `submitToken` / password sign-in |
| Paste a machine token | — | `submitToken` |
| Clear stored token | — | `clearToken` |
| Sign out | — | session |
| No administrator exists on this coordinator, break-glass claim | — | `BootstrapClaimForm` |
| Connect prompt | — | `TokenPrompt` |
| Connection banner | — | `ConnectionState` |
| Not found: old address → new address map | — | static |

### Chrome and rail

| Control / section | Behind it |
|---|---|
| Show picker | `useShowList` |
| Mode badge | `ShowModeIndicator` |
| Now playing: title, position, cycle, time to next transition | model |
| Connection pill | `ConnectionState` |
| Principal | session |
| Position progressbar | model |
| Rail: seven destinations, attention badges | derived attention counts |

---

## Views that no route reaches today

Recorded because they are code that will be deleted and may hold a control
nobody can currently see: `AudioNodes.tsx`, `FPPPlaylistDefinitions.tsx`,
`Macros.tsx`, `ShowActive.tsx`. Their capabilities are covered by the rows above
(Settings › Node routing, the playlist configuration page, Show Automation, and
Shows respectively). Nothing is lost by deleting them.

---

## Behaviour that lives only in code being deleted

These rules are not in the mocks, the guide, or the OpenAPI contract. Each is
re-derived from a named source during the rebuild, or it goes to
`OPEN-DECISIONS.md`.

| Rule | Where it is today | Source to re-derive from |
|---|---|---|
| Freshness and absence classification | `app/evidenceState.ts` | that file plus guide §4; it survives as a rule, not as a component |
| Server time and age formatting | `app/time.ts` | same |
| FPP signal lookup and grouping | `app/fppSignals.ts`, `app/fppDashboard.ts` | same |
| Observation presentation | `app/observationPresentation.ts` | same |
| Resolume composition parsing and evidence sanitising | `app/resolumeComposition.ts` | same |
| Start-playlist conflict classification | `components/FPPStartPlaylistControl.tsx` | that file |
| Scope-denial wording | `components/ScopedButton.tsx` | that file plus guide §6 |
| Macro readiness and consequence rollups | `views/automation/*` | those files plus guide §7 |

The rebuild reads these files for their rules before deleting them. Where a rule
cannot be traced to the contract or the guide, it is written up rather than
reinvented.
