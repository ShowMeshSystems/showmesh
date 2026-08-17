import type { ComponentType } from 'react'
import { CapabilityPanel, RenderSurfaceCapabilityPanel } from './CapabilityPanel'
import type { CapabilityPanelProps } from './CapabilityPanel'

// Kept in its own module, not inside CapabilityPanel.tsx, purely so that
// file exports components only (react-refresh/only-export-components) —
// this lookup table and resolveCapabilityPanel are plain values/functions,
// not components.
//
// capabilityPanels is the lookup table CapabilityPanel.tsx's own doc
// comment promises: a capability-specific renderer keyed by capability id,
// with [CapabilityPanel] as the fallback for every id absent from it —
// never a hardcoded node or capability class, and never a reason for an
// unknown id to blank the view.
const capabilityPanels: Record<string, ComponentType<CapabilityPanelProps>> = {
  'render.surface': RenderSurfaceCapabilityPanel,
}

// resolveCapabilityPanel picks the component a caller should render for a
// given capability id.
export function resolveCapabilityPanel(id: string): ComponentType<CapabilityPanelProps> {
  return capabilityPanels[id] ?? CapabilityPanel
}
