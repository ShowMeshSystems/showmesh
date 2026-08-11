import { createContext, useContext } from 'react'
import type { Model } from './types'

// One context carries the live Model to every view/component in this
// seam. This keeps the "seam B does not exist yet" dependency confined to
// exactly one file, App.tsx, which is the only place that imports
// `useModel` from `../api` (spec section 6): every other file in
// src/app, src/views, and src/components reads the model from here
// instead, typed against this seam's own `Model` (app/types.ts), and so
// compiles and tests today independent of seam B's landing.
export const ModelContext = createContext<Model | null>(null)

export function useModelContext(): Model {
  const model = useContext(ModelContext)
  if (model === null) {
    throw new Error('useModelContext() called outside <ModelContext.Provider>. ' +
      'App.tsx must wrap the router in <ModelContext.Provider value={useModel()}>.')
  }
  return model
}
