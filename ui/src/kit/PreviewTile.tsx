import type { DragEventHandler, ReactNode, Ref } from 'react'
import { StatusPair } from './Status'
import type { Tone } from './tone'

export type PreviewTileState = 'live' | 'idle' | 'connecting' | 'not_found' | 'disabled' | 'error'

const STATE_WORD: Record<PreviewTileState, { tone: Tone; word: string }> = {
  live: { tone: 'good', word: 'Live' },
  idle: { tone: 'pending', word: 'Idle' },
  connecting: { tone: 'pending', word: 'Connecting' },
  not_found: { tone: 'warn', word: 'Not found' },
  disabled: { tone: 'pending', word: 'Disabled' },
  error: { tone: 'bad', word: 'Error' },
}

type Props = {
  label: string
  state: PreviewTileState
  /** The coordinator's own sentence for this state: the fact, then the action. */
  message: string
  /** The picture. Absent, the frame says no picture is showing. */
  media?: ReactNode
  /** Ref to the picture frame, for an on-screen check. */
  mediaRef?: Ref<HTMLDivElement>
  /** The tile's controls, rendered under the caption. */
  actions?: ReactNode
  /** A grab handle in the caption row; present only when the tile can be dragged. */
  handle?: ReactNode
  dragState?: 'dragging' | 'over' | undefined
  draggable?: boolean
  onDragStart?: DragEventHandler<HTMLElement>
  onDragOver?: DragEventHandler<HTMLElement>
  onDrop?: DragEventHandler<HTMLElement>
  onDragEnd?: DragEventHandler<HTMLElement>
}

/** One live source: a 16:9 frame, its name, and a state word with the coordinator's sentence under it. */
export function PreviewTile({ label, state, message, media, mediaRef, actions, handle, dragState, ...drag }: Props) {
  const { tone, word } = STATE_WORD[state]
  const classes = ['sm-preview', dragState === undefined ? '' : `sm-preview--${dragState}`].filter(Boolean).join(' ')
  return (
    <article className={classes} aria-label={label} {...drag}>
      <div className="sm-preview__frame" ref={mediaRef}>
        {media ?? <p className="sm-preview__none">No picture</p>}
      </div>
      <div className="sm-preview__caption">
        {handle}
        <h3 className="sm-preview__label">{label}</h3>
        <StatusPair tone={tone} label={word} appearance="word" />
      </div>
      {message !== '' && <p className="sm-preview__message">{message}</p>}
      {actions !== undefined && <div className="sm-preview__actions">{actions}</div>}
    </article>
  )
}

/** The grid the tiles sit in. One width, set by the page. */
export function PreviewGrid({ children }: { children: ReactNode }) {
  return <div className="sm-preview-grid">{children}</div>
}
