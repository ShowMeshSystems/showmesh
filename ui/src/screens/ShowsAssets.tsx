import { useParams } from 'react-router-dom'
import { AssetsSurface } from './AssetsSurface'

/** This show's assets, using the surface shared with the /assets library. */
export function ShowsAssets() {
  const { id: showId = '' } = useParams<{ id: string }>()
  return <AssetsSurface scope={{ kind: 'show', showId }} />
}
