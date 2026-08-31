import { useParams } from 'react-router-dom'
import { NightSessionDefinitions } from './ShowNight'

/** Authoring belongs to the Show workspace; Show Night only operates the running session. */
export function ShowsNightSession() {
  const { id = '' } = useParams<{ id: string }>()
  return <NightSessionDefinitions showId={id} />
}
