import { Component, type ErrorInfo, type ReactNode } from 'react'

interface Props {
  children: ReactNode
}

interface State {
  failed: boolean
}

export class ErrorBoundary extends Component<Props, State> {
  state: State = { failed: false }

  static getDerivedStateFromError(): State {
    return { failed: true }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('Dashboard render failed', error, info.componentStack)
  }

  render() {
    if (this.state.failed) {
      return (
        <main className="fatal-error" role="alert">
          <div>
            <p className="eyeless-label">Dashboard error</p>
            <h1>This screen could not be rendered.</h1>
            <p>Reload the application. If the problem continues, check the browser console.</p>
            <button className="button button-primary" type="button" onClick={() => window.location.reload()}>
              Reload dashboard
            </button>
          </div>
        </main>
      )
    }
    return this.props.children
  }
}
