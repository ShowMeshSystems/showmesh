import { Fragment } from 'react'
import type { ComponentType } from 'react'
import type { Capability } from '../app/types'

// The generic capability panel (spec section 6.4, OPERATOR-UI section 9,
// acceptance criterion 4): "an unrecognized capability identifier
// degrades to a generic panel showing raw normalized fields... it must
// never blank the view or fail the render."
//
// This build has no capability-specific renderer for any identifier --
// none is owned by this step, and building one for a capability the
// coordinator does not yet report structured telemetry for would be
// inventing UI ahead of the backend behavior it depends on. Every
// capability, known or not, renders through this component today. That
// is not a placeholder for a future `if (capability.id === ...)` branch:
// composition is driven by what a capability declares (ADR-002), never by
// a hardcoded node or capability class, so a future specific renderer
// must be selected by a lookup table keyed on capability id, with this
// component remaining the fallback for anything absent from it.
export interface CapabilityPanelProps {
  capability: Capability
}

function formatRawValue(value: unknown): string {
  if (value === null || value === undefined) return '—'
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}

export function CapabilityPanel({ capability }: CapabilityPanelProps) {
  const attributeEntries = Object.entries(capability.attributes ?? {})
  return (
    <div className="panel" data-testid="capability-panel">
      <p className="panel__title">{capability.id}</p>
      <dl className="field-list">
        <dt>version</dt>
        <dd>{capability.version}</dd>
        {attributeEntries.length === 0 ? (
          <>
            <dt>attributes</dt>
            <dd className="text-muted">none advertised</dd>
          </>
        ) : (
          attributeEntries.map(([key, value]) => (
            <Fragment key={key}>
              <dt>{key}</dt>
              <dd>{formatRawValue(value)}</dd>
            </Fragment>
          ))
        )}
      </dl>
    </div>
  )
}

// RenderSurfaceCapabilityPanel is Track B seam B4's one entry in the
// lookup table this file's own doc comment describes: "render.surface"
// carries evidence (pixelFormats — the formats internal/agent/
// capabilities.go actually verified with a real PLAYING transition, per
// capability id/attributes) worth a dedicated, minimal layout rather than
// the generic key/value dump. Nothing else about this capability id is
// special-cased; an unrecognized or missing pixelFormats attribute falls
// back to "none advertised" rather than throwing.
export function RenderSurfaceCapabilityPanel({ capability }: CapabilityPanelProps) {
  const rawFormats = capability.attributes?.pixelFormats
  const pixelFormats = Array.isArray(rawFormats) ? rawFormats.map(String) : []
  return (
    <div className="panel" data-testid="capability-panel">
      <p className="panel__title">{capability.id}</p>
      <dl className="field-list">
        <dt>version</dt>
        <dd>{capability.version}</dd>
        <dt>pixel formats</dt>
        <dd className={pixelFormats.length === 0 ? 'text-muted' : undefined}>
          {pixelFormats.length > 0 ? pixelFormats.join(', ') : 'none advertised'}
        </dd>
      </dl>
    </div>
  )
}

// capabilityPanels is the lookup table this file's own doc comment
// promises: a capability-specific renderer keyed by capability id, with
// [CapabilityPanel] as the fallback for every id absent from it — never a
// hardcoded node or capability class, and never a reason for an unknown id
// to blank the view.
const capabilityPanels: Record<string, ComponentType<CapabilityPanelProps>> = {
  'render.surface': RenderSurfaceCapabilityPanel,
}

// resolveCapabilityPanel picks the component a caller should render for a
// given capability id.
export function resolveCapabilityPanel(id: string): ComponentType<CapabilityPanelProps> {
  return capabilityPanels[id] ?? CapabilityPanel
}
