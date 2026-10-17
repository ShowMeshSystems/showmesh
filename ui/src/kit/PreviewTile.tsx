import type { DragEventHandler, ReactNode, Ref } from 'react'
import { Button } from './Button'
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

/** A picture with no new frame for this long is marked Stale. */
export const STALE_FRAME_SECONDS = 10

function frameAgeWords(seconds: number): string {
  return seconds < 60 ? `Frame ${seconds} s ago` : `Frame ${Math.floor(seconds / 60)} min ago`
}

type Props = {
  label: string
  state: PreviewTileState
  /** The coordinator's own sentence for this state: the fact, then the action. Empty shows none. */
  message: string
  /** The picture. Absent, the frame says no picture is showing. */
  media?: ReactNode
  /** Whole seconds since the picture last changed; given only while a picture shows. */
  frameAgeSeconds?: number | undefined
  /** Ref to the picture frame, for an on-screen check. */
  mediaRef?: Ref<HTMLDivElement>
  /** The tile's controls, rendered under the caption. */
  actions?: ReactNode
  /** A grab handle in the caption row; present only when the tile can be dragged. */
  handle?: ReactNode
  /** Shows an enlarge icon in the caption row that calls this. */
  onEnlarge?: () => void
  dragState?: 'dragging' | 'over' | undefined
  draggable?: boolean
  onDragStart?: DragEventHandler<HTMLElement>
  onDragOver?: DragEventHandler<HTMLElement>
  onDrop?: DragEventHandler<HTMLElement>
  onDragEnd?: DragEventHandler<HTMLElement>
}

/** One live source: a 16:9 frame, its name, and a state word with the coordinator's sentence under it. */
export function PreviewTile({ label, state, message, media, frameAgeSeconds, mediaRef, actions, handle, onEnlarge, dragState, ...drag }: Props) {
  const stale = frameAgeSeconds !== undefined && frameAgeSeconds >= STALE_FRAME_SECONDS
  const { tone, word } = stale ? { tone: 'warn' as const, word: 'Stale' } : STATE_WORD[state]
  const classes = ['sm-preview', dragState === undefined ? '' : `sm-preview--${dragState}`].filter(Boolean).join(' ')
  return (
    <article className={classes} aria-label={label} {...drag}>
      <div className="sm-preview__frame" ref={mediaRef}>
        {media ?? <p className="sm-preview__none sm-meta sm-faint">No picture</p>}
      </div>
      <div className="sm-preview__caption">
        {handle}
        <h3 className="sm-preview__label sm-subhead sm-truncate">{label}</h3>
        {frameAgeSeconds !== undefined && <span className="sm-small sm-muted">{frameAgeWords(frameAgeSeconds)}</span>}
        <StatusPair tone={tone} label={word} appearance="word" />
        {onEnlarge !== undefined && (
          <Button size="icon" variant="quiet" aria-label={`Enlarge ${label}`} title="Enlarge" onClick={onEnlarge}>
            <span aria-hidden="true">⤢</span>
          </Button>
        )}
      </div>
      {message !== '' && <p className="sm-preview__message sm-small sm-muted">{message}</p>}
      {actions !== undefined && <div className="sm-preview__actions">{actions}</div>}
    </article>
  )
}

/** The grid the tiles sit in. One width, set by the page. */
export function PreviewGrid({ children }: { children: ReactNode }) {
  return <div className="sm-preview-grid">{children}</div>
}
