import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App'
import { loadRuntimeConfig } from './config'
import { configureGateway } from './lib/api'
import { applyTheme, preferredTheme } from './lib/theme'
import './styles.css'

// Before anything renders, so the sign-in page honours the operator's theme and
// nothing flashes in the wrong one.
applyTheme(preferredTheme())

const root = document.getElementById('root')
if (!root) throw new Error('Dashboard root element is missing')

const application = createRoot(root)

function renderConfigurationError(error: unknown): void {
  const message = error instanceof Error ? error.message : 'Unknown configuration error'
  console.error('Web application configuration failed', error)
  application.render(
    <main className="fatal-error" role="alert">
      <div>
        <p className="eyeless-label">Configuration error</p>
        <h1>The web application could not start.</h1>
        <p>{message}</p>
        <button className="button button-primary" type="button" onClick={() => window.location.reload()}>
          Retry
        </button>
      </div>
    </main>,
  )
}

async function start(): Promise<void> {
  const config = await loadRuntimeConfig()
  configureGateway(config.gatewayBaseUrl)
  application.render(
    <StrictMode>
      <App />
    </StrictMode>,
  )
}

void start().catch(renderConfigurationError)
