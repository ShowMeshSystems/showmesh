import { useEffect, useState } from 'react'
import { getShowCue, listConfigObjects } from '../../api'
import { describeApiError, evaluateAnyScope } from '../../app/session'
import { useModelContext } from '../../app/ModelContext'
import { PlannedFeature, UnavailableBlock } from '../../components/SharedLayouts'

// Announcements (Live Control.dc.html, new to this codebase): cues with
// an announcement output, directly fireable, with their duck/interrupt
// policy shown. There is no endpoint to fire a cue directly outside a
// Show Night transition (api/openapi.yaml has no POST /cues/{id}/fire or
// equivalent — show.cue objects are only consumed by night-session
// transitions and node cue-catalog deploys). Owner ruling 2026-08-29: a
// genuinely missing capability the mock draws gets a PlannedFeature
// stamp, not a plain disabled button pretending to be a real control.
// The cue list itself is real, coordinator-sourced data and renders
// underneath the stamp.
const READ_SCOPES = ['show:macro:run', 'config:write']

type CueWithAnnouncement = {
  id: string
  name: string
  policy: 'duck' | 'mix' | 'interrupt'
  duckGainDb: number | undefined
  asset: string
}

type LoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; cues: CueWithAnnouncement[] }

function useAnnouncementCues(showId: string | null, allowed: boolean): LoadState {
  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  useEffect(() => {
    if (!allowed || showId === null) return
    let cancelled = false
    setState({ kind: 'loading' })
    listConfigObjects('show.cue', showId)
      .then(async (response) => {
        const full = await Promise.all(response.objects.map((summary) => getShowCue(summary.id)))
        if (cancelled) return
        const cues = full
          .filter((cue) => cue.payload.outputs.announcement !== undefined)
          .map((cue) => ({
            id: cue.id,
            name: cue.payload.name,
            policy: cue.payload.outputs.announcement!.policy,
            duckGainDb: cue.payload.outputs.announcement!.duckGainDb,
            asset: cue.payload.outputs.audio?.asset ?? '',
          }))
        setState({ kind: 'loaded', cues })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [showId, allowed])
  return state
}

const POLICY_LABEL: Record<'duck' | 'mix' | 'interrupt', string> = {
  duck: 'Ducks the background bed',
  mix: 'Mixes with the background bed',
  interrupt: 'Interrupts the background bed',
}

export function AnnouncementsPanel({ showId }: { showId: string | null }) {
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const state = useAnnouncementCues(showId, readGate.allowed)

  if (!readGate.allowed) {
    return <UnavailableBlock title="Announcements" reason={readGate.reason} headingLevel={3} />
  }
  if (showId === null) {
    return <UnavailableBlock title="Announcements" reason="No active show is configured in the current coordinator projection." headingLevel={3} />
  }
  if (state.kind === 'loading') {
    return (
      <p className="text-muted" role="status">
        Loading cues with an announcement output…
      </p>
    )
  }
  if (state.kind === 'error') {
    return (
      <p className="section-notice notice--error" role="alert">
        {state.message}
      </p>
    )
  }
  if (state.cues.length === 0) {
    return <UnavailableBlock title="Announcements" reason="No configured cue for this show declares an announcement output." headingLevel={3} />
  }
  return (
    <div className="announcements-panel">
      <PlannedFeature
        title="Directly firing an announcement"
        why="No endpoint exists to fire a show.cue announcement outside a Show Night transition (api/openapi.yaml has no POST /cues/{id}/fire or equivalent)."
        preview={<button type="button" className="btn btn--secondary" tabIndex={-1}>Fire</button>}
      />
      <ul className="announcements-panel__list">
        {state.cues.map((cue) => (
          <li key={cue.id} className="announcements-panel__item">
            <div>
              <h3 className="t-subhead">{cue.name}</h3>
              <p className="t-small text-muted">
                {POLICY_LABEL[cue.policy]}
                {cue.policy === 'duck' && cue.duckGainDb !== undefined ? ` to ${cue.duckGainDb} dB` : ''}
                {cue.asset === '' ? '. No audio asset is set on this cue.' : ''}
              </p>
            </div>
          </li>
        ))}
      </ul>
    </div>
  )
}
