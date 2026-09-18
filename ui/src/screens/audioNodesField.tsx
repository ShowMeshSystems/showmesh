import { useEffect, useState } from 'react'
import { getShow, readShowAudioNodes, type AudioNodeSummary } from '../api'
import { Button, ChoiceGroup, RuledStrip } from '../kit'
import { describeApiError } from '../domain/session'
import { installationDefaultAudioNode, resolveAudioNodes, resolvedNodesFact } from './showsModel'

export type AudioNodesState = { kind: 'loading' } | { kind: 'loaded'; nodes: AudioNodeSummary[] } | { kind: 'failed'; reason: string }

export type ShowAudioNodesState = { kind: 'loading' } | { kind: 'loaded'; audioNodes: string[] } | { kind: 'failed'; reason: string }

/**
 * `show.audioNodes` (ADR-049 decision 10), read once per show id for every
 * screen that resolves an audio-bearing object against it. An empty showId
 * (no show picked yet, e.g. a fresh night-session draft) reports no show
 * nodes rather than fetching, the same fallthrough to the installation
 * default an unconfigured show gets once one is finally picked.
 */
export function useShowAudioNodes(showId: string): ShowAudioNodesState {
  const [state, setState] = useState<ShowAudioNodesState>({ kind: 'loading' })
  useEffect(() => {
    if (showId === '') {
      setState({ kind: 'loaded', audioNodes: [] })
      return
    }
    let cancelled = false
    setState({ kind: 'loading' })
    getShow(showId)
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', audioNodes: readShowAudioNodes(response.payload) })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [showId])
  return state
}

/** Id and label joined by a middle dot, collapsed to the bare id when the label adds nothing (ShowsCues.tsx/ShowNight.tsx's own convention). */
function nodeLabel(nodes: readonly AudioNodeSummary[], id: string): string {
  const node = nodes.find((n) => n.id === id)
  if (node === undefined) return id
  return node.label !== '' && node.label !== node.id ? `${node.id} · ${node.label}` : node.id
}

function nodeSecondaryText(node: AudioNodeSummary): string | undefined {
  return node.label !== '' && node.label !== node.id ? node.label : undefined
}

/**
 * ADR-049 decision 10's editor for a cue audio/announcement output, a night
 * bed, or a show.action audio target: while the object carries its own
 * explicit list, the existing checkbox editor stays, with an action to clear
 * it back to the show's list; once empty, it shows the resolved nodes and
 * where they came from, plus per-node Exclude checkboxes when inheriting the
 * show's own non-empty list.
 */
export function AudioNodesResolutionField({
  label,
  ownLabel,
  explicitValue,
  onExplicitChange,
  excludeValue,
  onExcludeChange,
  excludeError,
  showAudioNodesState,
  nodesState,
}: {
  /** The explicit-list checkbox group's own accessible name, e.g. "Audio target nodes". Kept distinct per caller so a cue's audio and announcement groups stay tellable apart. */
  label: string
  /** Names the object in its own copy, e.g. "this cue", "this announcement", "this bed", "this action". */
  ownLabel: string
  explicitValue: readonly string[]
  onExplicitChange: (value: string[]) => void
  excludeValue: readonly string[]
  onExcludeChange: (value: string[]) => void
  excludeError?: string | undefined
  showAudioNodesState: ShowAudioNodesState
  nodesState: AudioNodesState
}) {
  const [editingOwnList, setEditingOwnList] = useState(false)
  const errorNotice = excludeError !== undefined && (
    <span className="sm-field__error">
      <span aria-hidden="true">✕</span>
      {excludeError}
    </span>
  )

  if (nodesState.kind === 'loading') {
    return (
      <>
        <RuledStrip absence="loading" label="Reading" fact="Fetching this deployment's declared audio nodes." />
        {errorNotice}
      </>
    )
  }
  if (nodesState.kind === 'failed') {
    return (
      <>
        <RuledStrip absence="failed" label="Read failed" fact={nodesState.reason} />
        {errorNotice}
      </>
    )
  }
  if (nodesState.nodes.length === 0) {
    return (
      <>
        <RuledStrip absence="empty" label="None" fact="No audio node is declared." />
        {explicitValue.length > 0 && (
          <p className="sm-small sm-faint">
            Stored nodes: <span className="sm-data">{explicitValue.join(', ')}</span>
          </p>
        )}
        {errorNotice}
      </>
    )
  }

  if (explicitValue.length > 0 || editingOwnList) {
    return (
      <div className="sm-inspector__group">
        <ChoiceGroup
          label={label}
          help={`${ownLabel.charAt(0).toUpperCase()}${ownLabel.slice(1)}'s own list. Plays on exactly these nodes.`}
          options={nodesState.nodes.map((node) => ({ value: node.id, label: node.id, secondary: nodeSecondaryText(node) }))}
          value={explicitValue}
          onChange={onExplicitChange}
        />
        <Button
          variant="quiet"
          onClick={() => {
            setEditingOwnList(false)
            onExplicitChange([])
          }}
        >
          Use the show's audio nodes
        </Button>
      </div>
    )
  }

  if (showAudioNodesState.kind === 'loading') {
    return (
      <>
        <RuledStrip absence="loading" label="Reading" fact="Fetching the show's audio nodes." />
        {errorNotice}
      </>
    )
  }
  if (showAudioNodesState.kind === 'failed') {
    return (
      <>
        <RuledStrip absence="failed" label="Read failed" fact={showAudioNodesState.reason} />
        {errorNotice}
      </>
    )
  }

  const showAudioNodes = showAudioNodesState.audioNodes
  const defaultNodeId = installationDefaultAudioNode(nodesState.nodes)
  const resolved = resolveAudioNodes({ explicitList: [], showAudioNodes, excludeNodes: excludeValue, defaultNodeId })
  const resolvedWithLabels = { ...resolved, nodes: resolved.nodes.map((id) => nodeLabel(nodesState.nodes, id)) }

  return (
    <div className="sm-inspector__group">
      <p className="sm-small sm-muted">{resolvedNodesFact(resolvedWithLabels, ownLabel)}</p>
      {showAudioNodes.length > 0 ? (
        <ChoiceGroup
          label={`${label} exclude`}
          help="Ticking a node here stops this playing on it and nothing else."
          error={excludeError}
          options={showAudioNodes.map((id) => {
            const node = nodesState.nodes.find((n) => n.id === id)
            return { value: id, label: id, secondary: node === undefined ? undefined : nodeSecondaryText(node) }
          })}
          value={excludeValue}
          onChange={onExcludeChange}
        />
      ) : (
        errorNotice
      )}
      <Button variant="quiet" onClick={() => setEditingOwnList(true)}>
        Set specific nodes for {ownLabel}
      </Button>
    </div>
  )
}
