import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import TracingPage from './TracingPage'

/** Click a backend card by the name it renders. */
function selectBackend(name: RegExp): void {
  fireEvent.click(screen.getByRole('button', { name }))
}

describe('TracingPage', () => {
  it('offers all three backends and links to the tracing reference', () => {
    render(<TracingPage />)

    expect(screen.getByRole('heading', { name: 'Tracing' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /New Relic/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /LangSmith/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Jaeger/ })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /Tracing reference/ })).toHaveAttribute(
      'href',
      'https://github.com/ferro-labs/ai-gateway/blob/main/observability/README.md',
    )
  })

  it('shows nothing configured until a backend is chosen', () => {
    render(<TracingPage />)
    expect(screen.getByText('Choose a backend')).toBeInTheDocument()
    expect(screen.queryByText('NEW_RELIC_LICENSE_KEY')).toBeNull()
  })

  it('shows the credentialed HTTP config for a managed backend', () => {
    render(<TracingPage />)
    selectBackend(/New Relic/)

    // The config panel, not the card, is the heading.
    expect(screen.getByRole('heading', { name: 'New Relic' })).toBeInTheDocument()
    // The env var is offered as a credential to set, resolved via ${...}.
    expect(screen.getByText('NEW_RELIC_LICENSE_KEY')).toBeInTheDocument()
    expect(screen.getByText(/INGEST . LICENSE/)).toBeInTheDocument()
    // No local collector to start for a managed backend.
    expect(screen.queryByText(/docker run/)).toBeNull()
  })

  it('shows the LangSmith project header and credential', () => {
    render(<TracingPage />)
    selectBackend(/LangSmith/)

    expect(screen.getByRole('heading', { name: 'LangSmith' })).toBeInTheDocument()
    expect(screen.getByText('LANGSMITH_API_KEY')).toBeInTheDocument()
    expect(screen.getByText('Langsmith-Project: <project name>')).toBeInTheDocument()
  })

  it('shows the collector command and no credential for the self-hosted backend', () => {
    render(<TracingPage />)
    selectBackend(/Jaeger/)

    expect(screen.getByText(/docker run -d --name jaeger/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'http://localhost:16686' })).toBeInTheDocument()
    expect(screen.getByText('None — local collector')).toBeInTheDocument()
    // A plaintext local collector takes no credential.
    expect(screen.queryByText('NEW_RELIC_LICENSE_KEY')).toBeNull()
  })
})
