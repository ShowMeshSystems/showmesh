import { Component, type ErrorInfo, type ReactNode } from 'react'

// A React error boundary wraps every panel individually (spec sections
// 6.4 and 6.7, OPERATOR-UI section 9): "put a React error boundary around
// each panel so one panel throwing must not take the page with it." This
// is the acceptance-criterion path for an unrecognized capability panel
// (section 6.4's generic panel) as well as any other panel, since a
// generic panel rendering raw normalized fields from a node this UI has
// never been taught about is exactly the shape most likely to contain a
// surprising value.
//
// React error boundaries must be class components; there is still no
// hook equivalent as of React 19.
interface Props {
  /** Shown in the fallback so an operator knows which panel failed. */
  panelLabel: string
  children: ReactNode
}

interface State {
  error: Error | null
}

export class PanelErrorBoundary extends Component<Props, State> {
  state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // Logged to the console rather than swallowed silently: an operator
    // debugging a blank panel needs the stack, and this project's
    // standing rule is that absent evidence must be stated, not omitted --
    // that applies to a UI defect exactly as much as a coordinator one.
    console.error(`Panel "${this.props.panelLabel}" failed to render`, error, info)
  }

  render() {
    if (this.state.error) {
      return (
        <div className="panel panel--error" role="alert">
          <p className="panel__title">{this.props.panelLabel} failed to render</p>
          <p>
            This panel hit an unexpected error and has been hidden so the rest of the
            page keeps working. This is a display defect, not a report about the
            underlying system.
          </p>
        </div>
      )
    }
    return this.props.children
  }
}
