import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { ApiError, getFPPPlaylistEntryReconciliation, getFPPPlaylistReadiness, listConfigObjects } from '../api'
import { describeApiError, evaluateAnyScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { DataFreshnessNotice } from '../components/DataFreshnessNotice'
import { OperatorPageHeader } from '../components/SharedLayouts'
import {
  FPPPlaylistReadinessBadge,
  FPPPlaylistReadinessFailingConditionBadge,
  FPPPlaylistReconciliationOutcomeBadge,
} from '../components/DomainBadges'
import type {
  ConfigObjectSummary,
  FPPPlaylistEntryReconciliationResponse,
  FPPPlaylistReadinessResponse,
} from '../app/types'
import '../styles/monitor.css'

// Owner ruling 2026-08-29: Monitor gained a fifth facet, Readiness, at
// `/monitor/readiness`. This file (formerly the Show-night nav group's
// own standalone route) is now owned by the Monitor screen-builder group
// and mounted as that facet's content -- restyled to the design's table
// primitive below, but its data logic (both hooks, both row components)
// is unchanged: it was already correct and reads no invented "N of N
// checks" figure anywhere (the mocks' "14 of 14 checks" number was
// invented during the design session and must never be hard-coded --
// this view already only ever renders a per-playlist/per-instance
// verdict, never a synthesized total, so there is nothing to remove).

// TRACK-H-H2-SPEC.md §5/§6: the show-night question this view exists to
// answer: is every Playlist actually ready to run, and does an FPP
// instance's latest accepted observation still match what the show
// declares. Both routes were reachable only from `showmeshctl fpp` before
// this view; neither was called from ui/src anywhere. Read-only: this
// view never writes anything (openapi's ingest routes for these two
// surfaces are machine-facing and deliberately get no operator control
// here).
//
// Placed in the Show night nav group, not under Configure: readiness is
// the question an operator asks BEFORE a show runs, the same reason
// Night session and Active show live there. It is its own route rather
// than a section grafted onto Night session or Active show: readiness is
// keyed by Playlist (`show.playlist` has no authoring view yet to graft
// onto), and reconciliation is keyed by FPP instance: neither key is
// the show or the night session those two existing screens are about,
// and both of those screens are out of scope for this change regardless.

// Matches Macros.tsx / ShowActions.tsx / RenderSurfacePanel.tsx's own
// CONFIGURED_SURFACE_READ_SCOPES: the show.playlist listing is gated the
// same as every other show.* config-object read (api.go's
// showConfigReadScopes), so an operator-role principal (show:macro:run,
// not config:write) must see this list too, not just an admin.
const PLAYLIST_LIST_READ_SCOPES = ['show:macro:run', 'config:write']

type PlaylistsState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; playlists: ConfigObjectSummary[] }

function usePlaylists(allowed: boolean): PlaylistsState {
  const [state, setState] = useState<PlaylistsState>({ kind: 'loading' })

  useEffect(() => {
    if (!allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    listConfigObjects('show.playlist')
      .then((resp) => {
        if (!cancelled) setState({ kind: 'loaded', playlists: resp.objects })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [allowed])

  return state
}

export function Readiness() {
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, PLAYLIST_LIST_READ_SCOPES)
  const playlists = usePlaylists(readGate.allowed)

  // Both verdicts below are point-in-time answers this view fetches on
  // demand, not resources the snapshot itself carries, so nothing
  // refreshes either one automatically just from being open (the defect
  // this state exists to close: a verdict read at 18:40 still reading
  // unchanged, with no way to tell, at 21:15). `model.snapshotReceivedAt`
  // changes on every resnapshot (the initial connect, every reconnect,
  // and every `stream.reset`, per store.ts's own applySnapshot comment),
  // so wiring it into each row's fetch effect below re-asks the
  // coordinator exactly when this browser's connection to it was
  // re-established, without inventing a second freshness mechanism
  // alongside the one DataFreshnessNotice already uses for the rest of
  // this page. `reloadGeneration` is this page's own explicit "recheck
  // now" control (the same bump-a-counter shape as NightSession.tsx's
  // per-section Reload button). The reconciliation row also has a third
  // trigger: `fppPlaylistEntry.changed` frames
  // (store.ts's applyFppPlaylistEntryChanged) mean FPP itself reported a
  // new entry, so that row also refetches on its own without waiting for
  // either a reconnect or the operator's own click; see ReconciliationRow
  // below. The readiness row has no equivalent live trigger: nothing in
  // the change stream announces a Playlist binding or FPP configuration
  // change, so a manual recheck plus reconnect-triggered refetch remains
  // the only freshness this seam has for it.
  const [reloadGeneration, setReloadGeneration] = useState(0)

  // Every FPP instance the coordinator currently knows a uuid for:
  // reconciliation is keyed by instanceUuid, not by instanceId, and an
  // instance that has never reported a uuid (instance.instanceUuid ===
  // null) has nothing to reconcile yet. Sourced live from model.fpp, the
  // same snapshot/delta-fed list FPPList.tsx renders, not a fetch of its
  // own.
  const reconcilableInstances = model.fpp.filter(
    (instance): instance is typeof instance & { instanceUuid: string } => instance.instanceUuid !== null,
  )

  return (
    <section className="monitor-readiness" aria-label="Readiness">
      <OperatorPageHeader
        eyebrow="Monitor"
        title="Readiness"
        lede="Whether each Playlist bound to an FPP playlist still matches what FPP is configured to play, and whether the latest accepted observation from each FPP instance still matches what the show declares. Read-only: nothing here starts, stops, or imports anything. Each verdict below is its own point-in-time answer, dated by the &ldquo;As of&rdquo; column; it does not update on its own while this tab stays open."
      />
      <DataFreshnessNotice connection={model.connection} snapshotReceivedAt={model.snapshotReceivedAt} />

      {!readGate.allowed && (
        <p className="ruled-strip ruled-strip--no-permission" role="status">
          <span className="ruled-strip__state t-meta">No permission</span>
          <span className="ruled-strip__explanation">{readGate.reason}</span>
        </p>
      )}

      {readGate.allowed && (
        <section className="monitor-section" aria-labelledby="readiness-playlists-title">
          <div className="monitor-section__header">
            <h2 id="readiness-playlists-title">Playlists</h2>
            <button type="button" className="btn btn--secondary btn--compact" onClick={() => setReloadGeneration((g) => g + 1)}>
              Recheck readiness
            </button>
          </div>
          {playlists.kind === 'loading' && <p className="t-small text-muted">Loading configured Playlists…</p>}
          {playlists.kind === 'error' && (
            <p role="alert" className="ruled-strip ruled-strip--failed">
              Could not read the list of configured Playlists: {playlists.message}
            </p>
          )}
          {playlists.kind === 'loaded' && playlists.playlists.length === 0 && (
            <p className="t-small text-muted">No Playlists are configured yet.</p>
          )}
          {playlists.kind === 'loaded' && playlists.playlists.length > 0 && (
            <div className="table-wrap">
              <table className="table" aria-label="Playlist readiness">
                <thead>
                  <tr>
                    <th scope="col">Playlist</th>
                    <th scope="col">Show</th>
                    <th scope="col">Verdict</th>
                    <th scope="col">Reason</th>
                    <th scope="col">As of</th>
                  </tr>
                </thead>
                <tbody>
                  {playlists.playlists.map((playlist) => (
                    <PlaylistReadinessRow
                      key={playlist.id}
                      playlist={playlist}
                      snapshotReceivedAt={model.snapshotReceivedAt}
                      reloadGeneration={reloadGeneration}
                    />
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </section>
      )}

      <section className="monitor-section" aria-labelledby="readiness-fpp-title">
        <div className="monitor-section__header">
          <h2 id="readiness-fpp-title">FPP instance reconciliation</h2>
          <button type="button" className="btn btn--secondary btn--compact" onClick={() => setReloadGeneration((g) => g + 1)}>
            Recheck reconciliation
          </button>
        </div>
        {reconcilableInstances.length === 0 ? (
          <p className="t-small text-muted">
            No FPP instance has reported a SystemUUID yet, so there is nothing to reconcile.
          </p>
        ) : (
          <div className="table-wrap">
            <table className="table" aria-label="FPP instance reconciliation">
              <thead>
                <tr>
                  <th scope="col">Instance</th>
                  <th scope="col">Verdict</th>
                  <th scope="col">Detail</th>
                  <th scope="col">As of</th>
                </tr>
              </thead>
              <tbody>
                {reconcilableInstances.map((instance) => (
                  <ReconciliationRow
                    key={instance.instanceId}
                    instanceId={instance.instanceId}
                    instanceUuid={instance.instanceUuid}
                    snapshotReceivedAt={model.snapshotReceivedAt}
                    reloadGeneration={reloadGeneration}
                  />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </section>
  )
}

type ReadinessRowState =
  | { kind: 'loading' }
  | { kind: 'loaded'; response: FPPPlaylistReadinessResponse }
  // The playlist's own runner is not "fpp": §6 readiness is an
  // FPP-specific concept, so this is a real, distinguishable answer
  // (openapi's own 400), never rendered as "not ready".
  | { kind: 'not-fpp-runner'; detail: string }
  // No show.playlist object with this id has an active revision
  // (openapi's own 404): also a real, distinguishable answer.
  | { kind: 'no-active-revision'; detail: string }
  // A fetch that failed to ask at all: network error, 401/403/500: kept
  // visually and textually distinct from both of the above and from
  // "not ready" per this task's own instruction: "never render a fetch
  // failure as not ready."
  | { kind: 'error'; message: string }

function usePlaylistReadiness(
  playlistId: string,
  snapshotReceivedAt: number | null,
  reloadGeneration: number,
): ReadinessRowState {
  const [state, setState] = useState<ReadinessRowState>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    getFPPPlaylistReadiness(playlistId)
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', response })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        if (err instanceof ApiError && err.status === 400) {
          setState({ kind: 'not-fpp-runner', detail: err.message })
          return
        }
        if (err instanceof ApiError && err.status === 404) {
          setState({ kind: 'no-active-revision', detail: err.message })
          return
        }
        setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
    // snapshotReceivedAt re-asks on every resnapshot (initial connect,
    // reconnect, stream.reset); reloadGeneration re-asks on the
    // operator's own "Recheck readiness" button. Neither is read inside
    // the effect body; both exist purely to retrigger it.
  }, [playlistId, snapshotReceivedAt, reloadGeneration])

  return state
}

function PlaylistReadinessRow({
  playlist,
  snapshotReceivedAt,
  reloadGeneration,
}: {
  playlist: ConfigObjectSummary
  snapshotReceivedAt: number | null
  reloadGeneration: number
}) {
  const readiness = usePlaylistReadiness(playlist.id, snapshotReceivedAt, reloadGeneration)

  return (
    <tr>
      <th scope="row">
        {playlist.label} <span className="text-muted">({playlist.id})</span>
      </th>
      <td>{playlist.show}</td>
      <td>
        {readiness.kind === 'loading' && <span className="text-muted">Checking…</span>}
        {readiness.kind === 'loaded' && <FPPPlaylistReadinessBadge ready={readiness.response.ready} />}
        {readiness.kind === 'not-fpp-runner' && <span className="text-muted">Not FPP-runner</span>}
        {readiness.kind === 'no-active-revision' && <span className="text-muted">No active revision</span>}
        {readiness.kind === 'error' && (
          <span role="alert" className="render-surface__error">
            Could not check
          </span>
        )}
      </td>
      <td>
        {/* This task's own instruction: "it must state the coordinator's
            own reason, not a summarised or reworded one." `reason` and
            `detail` below are rendered verbatim, never paraphrased. */}
        {readiness.kind === 'loaded' && readiness.response.ready && readiness.response.warning !== undefined && (
          <span className="text-muted">{readiness.response.warning}</span>
        )}
        {readiness.kind === 'loaded' && !readiness.response.ready && (
          <>
            {readiness.response.failingCondition !== undefined && (
              <FPPPlaylistReadinessFailingConditionBadge condition={readiness.response.failingCondition} />
            )}{' '}
            {readiness.response.reason}
          </>
        )}
        {readiness.kind === 'not-fpp-runner' && readiness.detail}
        {readiness.kind === 'no-active-revision' && readiness.detail}
        {readiness.kind === 'error' && (
          <span role="alert" className="render-surface__error">
            {readiness.message}
          </span>
        )}
      </td>
      <td className="text-muted">
        {/* ADR-011: every observation carries freshness. `serverTime` is
            the coordinator's own clock at the moment it computed THIS
            verdict, required on every 200 response: the one piece of
            evidence that lets an operator tell a "Ready" verdict read
            hours ago from one read a moment ago. */}
        {readiness.kind === 'loaded' ? formatAbsolute(readiness.response.serverTime) : 'unknown'}
      </td>
    </tr>
  )
}

type ReconciliationRowState =
  | { kind: 'loading' }
  | { kind: 'loaded'; response: FPPPlaylistEntryReconciliationResponse }
  // No accepted playlist-entry observation for this instanceUuid yet
  // (openapi's own 404): "the normal afternoon state, not a fault," the
  // same posture CueCatalogPanel's HeldCatalog takes for "not observed
  // from here yet."
  | { kind: 'no-observation'; detail: string }
  | { kind: 'error'; message: string }

function useReconciliation(
  instanceUuid: string,
  snapshotReceivedAt: number | null,
  reloadGeneration: number,
  // The latest `fppPlaylistEntry.changed` frame's `receivedAt` for
  // this instance (store.ts's applyFppPlaylistEntryChanged), or `null`
  // before this connection has ever seen one. Not read inside the effect
  // body, same as `snapshotReceivedAt`/`reloadGeneration` above; it
  // exists purely so a fresh observation retriggers the refetch below,
  // the same "re-fetch the authoritative state" discipline this hook
  // already uses for a reconnect or the operator's own manual recheck,
  // now also for FPP actually advancing.
  latestObservationReceivedAt: string | null,
): ReconciliationRowState {
  const [state, setState] = useState<ReconciliationRowState>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    getFPPPlaylistEntryReconciliation(instanceUuid)
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', response })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        if (err instanceof ApiError && err.status === 404) {
          setState({ kind: 'no-observation', detail: err.message })
          return
        }
        setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
    // Same reconnect/manual-recheck shape as usePlaylistReadiness above,
    // plus a live retrigger on latestObservationReceivedAt.
  }, [instanceUuid, snapshotReceivedAt, reloadGeneration, latestObservationReceivedAt])

  return state
}

function ReconciliationRow({
  instanceId,
  instanceUuid,
  snapshotReceivedAt,
  reloadGeneration,
}: {
  instanceId: string
  instanceUuid: string
  snapshotReceivedAt: number | null
  reloadGeneration: number
}) {
  const model = useModelContext()
  // Keyed by instanceUuid, matching how `fppPlaylistEntry.changed`
  // itself is keyed (store.ts's applyFppPlaylistEntryChanged), not
  // instanceId, same distinction this file's own reconcilableInstances
  // filter above already draws.
  const latestObservation = model.fppPlaylistEntryObservations.find((o) => o.instanceUuid === instanceUuid)
  const reconciliation = useReconciliation(
    instanceUuid,
    snapshotReceivedAt,
    reloadGeneration,
    latestObservation?.receivedAt ?? null,
  )

  return (
    <tr>
      <th scope="row">
        <Link className="entity-link" to={`/monitor/fleet/fpp/${encodeURIComponent(instanceId)}`}>
          {instanceId}
        </Link>{' '}
        <span className="text-muted">({instanceUuid})</span>
      </th>
      <td>
        {reconciliation.kind === 'loading' && <span className="text-muted">Checking…</span>}
        {reconciliation.kind === 'loaded' && (
          <FPPPlaylistReconciliationOutcomeBadge outcome={reconciliation.response.outcome} />
        )}
        {reconciliation.kind === 'no-observation' && <span className="text-muted">No observation yet</span>}
        {reconciliation.kind === 'error' && (
          <span role="alert" className="render-surface__error">
            Could not check
          </span>
        )}
      </td>
      <td>
        {reconciliation.kind === 'loaded' && (
          <>
            {/* Verbatim, never reworded: same rule as the readiness row
                above. `reason` is always present on this response
                (required field). */}
            <p>{reconciliation.response.reason}</p>
            {reconciliation.response.outcome === 'resolved' && (
              <p className="text-muted">
                Playlist {reconciliation.response.playlistId} (revision{' '}
                {reconciliation.response.playlistRevision}), entry {reconciliation.response.entryId},
                cue {reconciliation.response.cueId} (revision {reconciliation.response.cueRevision})
              </p>
            )}
            {(reconciliation.response.outcome === 'unknown-entry' ||
              reconciliation.response.outcome === 'evidence-mismatch' ||
              reconciliation.response.outcome === 'cross-show') && (
              <p className="text-muted">
                Observed: playlistHash {reconciliation.response.observedPlaylistHash || '(none)'}, entryKey{' '}
                {reconciliation.response.observedEntryKey || '(none)'}, section{' '}
                {reconciliation.response.observedSection || '(none)'}, position{' '}
                {reconciliation.response.observedPosition ?? '(none)'}. Definition available:{' '}
                {reconciliation.response.definitionAvailable ? 'yes' : 'no'}.
              </p>
            )}
          </>
        )}
        {reconciliation.kind === 'no-observation' && reconciliation.detail}
        {reconciliation.kind === 'error' && (
          <span role="alert" className="render-surface__error">
            {reconciliation.message}
          </span>
        )}
      </td>
      <td className="text-muted">
        {/* Same ADR-011 freshness rule as the readiness table's own "As
            of" column: `serverTime` is required on this response. */}
        {reconciliation.kind === 'loaded' ? formatAbsolute(reconciliation.response.serverTime) : 'unknown'}
      </td>
    </tr>
  )
}
