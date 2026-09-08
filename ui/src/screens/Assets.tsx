import { PageTitle } from '../kit'
import { AssetsSurface } from './AssetsSurface'

/** The rail's /assets destination: every show's assets, on the surface the show tab also renders. */
export function Assets() {
  return (
    <>
      <PageTitle
        title="Assets"
        lede="Every show's assets in one list. Identity is show, sequence, target kind and target; node-by-node sync state is Monitor › Manifest's own facet."
      />
      <AssetsSurface scope={{ kind: 'all' }} />
    </>
  )
}
