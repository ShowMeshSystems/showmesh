import { BlankingPlate, PageTitle } from '../kit'

/**
 * Every route the rebuild has not reached yet. It states the fact rather than
 * rendering an empty page that reads as "nothing here".
 */
export function NotRebuilt({ title, mock }: { title: string; mock: string }) {
  return (
    <>
      <PageTitle title={title} />
      <BlankingPlate
        absence="unavailable"
        stamp="Soon"
        eyebrow={`${title} · not rebuilt`}
        title="This screen has not been rebuilt yet"
        detail={`The operator UI is being rebuilt one screen at a time against ${mock}. This route is still in the queue. Its controls are recorded in docs/ui-rebuild/CONTROL-INVENTORY.md and nothing has been dropped.`}
      />
    </>
  )
}
