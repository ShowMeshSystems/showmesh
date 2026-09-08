import { createContext, useContext } from 'react'
import type { Model } from '../api'

/** The live model, provided once by App and read by every screen. */
export const ModelContext = createContext<Model | null>(null)

export function useModelContext(): Model {
  const model = useContext(ModelContext)
  if (model === null) {
    throw new Error('useModelContext() called outside <ModelContext.Provider>.')
  }
  return model
}
