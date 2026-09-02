import type { ReactNode } from 'react'
import { Button } from './Button'

export type LifecycleCommandSpec = {
  /** Stable identifier; also selects fade-out-night's warn-button styling. */
  command: string
  label: string
  /** One-line consequence. Replaced by `disabledReason` while disabled. */
  detail: string
  disabled?: boolean
  /** Shown as the button's title, and in place of `detail`, while disabled. */
  disabledReason?: string | undefined
  onRun: () => void
  /**
   * Renders under the consequence line, inside this command's own cell --
   * for example Start night's "skip the enter-show lead" checkbox. Never
   * floats beside the button.
   */
  options?: ReactNode
}

export type LifecycleCommandGroup = {
  id: string
  /** Omit for one flat, ungrouped grid. Set it for a titled subsection. */
  title?: string
  commands: readonly LifecycleCommandSpec[]
}

function LifecycleCommandCell({ command, label, detail, disabled = false, disabledReason, onRun, options }: LifecycleCommandSpec) {
  return (
    <div className={`sm-lifecycle-command sm-lifecycle-command--${command}`}>
      <Button size="gloved" disabled={disabled} title={disabled ? disabledReason : undefined} onClick={onRun}>
        {label}
      </Button>
      <p className="sm-small sm-muted">{disabled && disabledReason !== undefined ? disabledReason : detail}</p>
      {options !== undefined && <div className="sm-lifecycle-command__options">{options}</div>}
    </div>
  )
}

/**
 * The night lifecycle commands, one shared element. A command's `options`
 * render inside its own cell, under its consequence line, never beside
 * the button.
 */
export function LifecycleCommands({ groups }: { groups: readonly LifecycleCommandGroup[] }) {
  return (
    <>
      {groups.map((group) =>
        group.title === undefined ? (
          <div key={group.id} className="sm-grid sm-grid--auto sm-lifecycle-commands">
            {group.commands.map((spec) => (
              <LifecycleCommandCell key={spec.command} {...spec} />
            ))}
          </div>
        ) : (
          <section key={group.id} aria-labelledby={group.id} className="sm-subsection">
            <h3 id={group.id} className="sm-subsection__title">
              {group.title}
            </h3>
            <div className="sm-grid sm-grid--auto sm-control-grid">
              {group.commands.map((spec) => (
                <LifecycleCommandCell key={spec.command} {...spec} />
              ))}
            </div>
          </section>
        ),
      )}
    </>
  )
}
