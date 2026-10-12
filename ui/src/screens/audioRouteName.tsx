import type { AudioNodeSummary } from '../api'
import { audioNodeRouteText, useFleetNodes } from './audioRouteModel'

/** An audio node's route in short form, with the full device string on hover. */
export function AudioNodeRoute({ node }: { node: AudioNodeSummary }) {
  const fleet = useFleetNodes()
  return <span title={node.label}>{audioNodeRouteText(node, fleet)}</span>
}
