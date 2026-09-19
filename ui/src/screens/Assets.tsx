import { PageTitle } from '../kit'
import { AssetsSurface } from './AssetsSurface'

/** The rail's /assets destination: every show's assets, on the surface the show tab also renders. */
export function Assets() {
  return (
    <>
      <PageTitle title="Assets" />
      <AssetsSurface scope={{ kind: 'all' }} />
    </>
  )
}
