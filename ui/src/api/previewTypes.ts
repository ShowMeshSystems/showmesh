/**
 * Dashboard video previews, shaped exactly as docs/build/TRACK-PREVIEW-WALL.md
 * specifies. Replaced by the generated schema types once api/openapi.yaml
 * carries these routes.
 */
export type PreviewGatewayState = 'not_configured' | 'reachable' | 'unreachable'

export type PreviewTileState = 'live' | 'idle' | 'connecting' | 'not_found' | 'disabled' | 'error'

export type PreviewGateway = {
  state: PreviewGatewayState
  message: string
  observedAt: string
}

export type PreviewTile = {
  key: string
  kind: 'ndi' | 'camera'
  label: string
  nodeId?: string
  surfaceId?: string
  cameraId?: string
  state: PreviewTileState
  message: string
  hidden: boolean
  observedAt: string
}

export type PreviewsResponse = {
  gateway: PreviewGateway
  tiles: PreviewTile[]
}

type ConfigEnvelope<K extends string, P> = {
  serverTime: string
  kind: K
  revision: number
  payload: P
  updatedAt: string
  createdByPrincipalId: string | null
  createdByPrincipalName: string | null
}

export type ConfigPreviewGatewayPayload = { url: string; tokenSet: boolean }
export type ConfigPreviewGatewayPutRequest = { url?: string; token?: string }
export type PreviewGatewayConfigResponse = ConfigEnvelope<'preview.gateway', ConfigPreviewGatewayPayload>

export type ConfigPreviewCameraPayload = { name: string; enabled: boolean; urlSet: boolean; urlDisplay: string }
export type ConfigPreviewCameraPutRequest = { name: string; enabled: boolean; url?: string }
export type PreviewCameraConfigResponse = ConfigEnvelope<'preview.camera', ConfigPreviewCameraPayload>

export type PreviewCameraListResponse = {
  serverTime: string
  kind: 'preview.camera'
  objects: { id: string }[]
}

export type DashboardLayoutPayload = { previews: { order: string[]; hidden: string[] } }
export type DashboardLayoutConfigResponse = ConfigEnvelope<'dashboard.layout', DashboardLayoutPayload>
