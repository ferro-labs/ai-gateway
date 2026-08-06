import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import {
  ConfirmDialog,
  CopyButton,
  EmptyState,
  LoadingState,
  Modal,
  Notice,
  PageHeader,
  Pagination,
  StatusPill,
} from './ui'

/*
 * The application composites in `ui.tsx`, exercised on their own rather than
 * through whichever page happens to mount them. Every behaviour asserted here
 * is one a page relies on and cannot restate: the pagination bounds, the
 * clipboard refusal, and the two dialogs' rules about when they may close.
 */

/**
 * Installs a clipboard whose outcome the test chooses.
 *
 * Defined after `userEvent.setup()`, which installs a stub of its own that
 * always succeeds — and the refusal is half of what `CopyButton` exists for.
 */
function stubClipboard(writeText: () => Promise<void>): void {
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText },
  })
}

/** The dialog backdrop, which has no accessible role to be queried by. */
function backdrop(slot: 'dialog' | 'alert-dialog'): HTMLElement {
  const element = document.querySelector(`[data-slot="${slot}-overlay"]`)
  if (!(element instanceof HTMLElement)) throw new Error(`no ${slot} backdrop`)
  return element
}

describe('Pagination', () => {
  it('offers no way back from the first page and no way past the last', async () => {
    const user = userEvent.setup()
    const onOffsetChange = vi.fn()
    render(
      <Pagination offset={0} pageSize={50} returned={50} total={120} onOffsetChange={onOffsetChange} />,
    )

    expect(screen.getByText('Showing 1–50 of 120')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Previous' })).toBeDisabled()

    await user.click(screen.getByRole('button', { name: 'Next' }))
    expect(onOffsetChange).toHaveBeenCalledWith(50)
  })

  it('stops at the last page rather than offering an empty one', async () => {
    const user = userEvent.setup()
    const onOffsetChange = vi.fn()
    render(
      <Pagination offset={100} pageSize={50} returned={20} total={120} onOffsetChange={onOffsetChange} />,
    )

    expect(screen.getByText('Showing 101–120 of 120')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Next' })).toBeDisabled()

    await user.click(screen.getByRole('button', { name: 'Previous' }))
    expect(onOffsetChange).toHaveBeenCalledWith(50)
  })

  it('never asks for a negative offset, which the gateway would reject', async () => {
    const user = userEvent.setup()
    const onOffsetChange = vi.fn()
    // A page size larger than the offset reached: paging back from here has to
    // land on the first page rather than before it.
    render(
      <Pagination offset={20} pageSize={50} returned={10} total={30} onOffsetChange={onOffsetChange} />,
    )

    await user.click(screen.getByRole('button', { name: 'Previous' }))
    expect(onOffsetChange).toHaveBeenCalledWith(0)
  })

  it('disables both controls on a single page of results', () => {
    render(<Pagination offset={0} pageSize={50} returned={3} total={3} onOffsetChange={vi.fn()} />)

    expect(screen.getByText('Showing 1–3 of 3')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Previous' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Next' })).toBeDisabled()
  })

  it('refuses to turn the page while a page is still loading', () => {
    // Two pages in flight at once would race, and the later answer is not
    // necessarily the page the operator asked for last.
    render(<Pagination busy offset={50} pageSize={50} returned={50} total={200} onOffsetChange={vi.fn()} />)

    expect(screen.getByRole('button', { name: 'Previous' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Next' })).toBeDisabled()
  })

  it('reports a page the server returned nothing for as an empty range, not as row one', () => {
    render(<Pagination offset={0} pageSize={50} returned={0} total={0} onOffsetChange={vi.fn()} />)

    expect(screen.getByText('Showing 0–0 of 0')).toBeInTheDocument()
  })

  it('carries a note beside the range, for what a client-side search matched', () => {
    render(
      <Pagination
        note="2 match the search on this page"
        offset={0}
        pageSize={50}
        returned={50}
        total={120}
        onOffsetChange={vi.fn()}
      />,
    )

    // The range still describes the server's page: the search narrowed what is
    // drawn, not what was fetched.
    expect(screen.getByText('Showing 1–50 of 120 · 2 match the search on this page')).toBeInTheDocument()
  })
})

describe('CopyButton', () => {
  it('copies the value and announces that it did', async () => {
    const user = userEvent.setup()
    const writeText = vi.fn<() => Promise<void>>(() => Promise.resolve())
    stubClipboard(writeText)
    render(<CopyButton label="Copy trace ID trace-abc" value="trace-abc" />)

    await user.click(screen.getByRole('button', { name: 'Copy trace ID trace-abc' }))

    expect(writeText).toHaveBeenCalledWith('trace-abc')
    // Announced, not only drawn: the icon swap says nothing to a screen reader.
    expect(await screen.findByText('Copy trace ID trace-abc copied')).toBeInTheDocument()
  })

  it('says so when the clipboard refuses, instead of looking like a dead button', async () => {
    const user = userEvent.setup()
    // What a page served over plain HTTP, or a browser that has not granted
    // write permission, actually does — it rejects rather than doing nothing.
    stubClipboard(() => Promise.reject(new Error('write permission denied')))
    render(<CopyButton label="Copy key" value="fgw_secret" />)

    await user.click(screen.getByRole('button', { name: 'Copy key' }))

    // Silence here costs an operator the one copy of a newly created key.
    expect(await screen.findByText('Could not copy Copy key')).toBeInTheDocument()
  })
})

describe('ConfirmDialog', () => {
  const base = {
    open: true,
    title: 'Revoke this key',
    description: 'The key stops working immediately.',
    confirmLabel: 'Revoke key',
  }

  it('reports a refused action inside the dialog rather than behind its backdrop', async () => {
    render(
      <ConfirmDialog
        {...base}
        error="This key authenticated the request. Use another admin key."
        onCancel={vi.fn()}
        onConfirm={vi.fn()}
      />,
    )

    const dialog = await screen.findByRole('alertdialog')
    expect(within(dialog).getByRole('alert')).toHaveTextContent('Use another admin key')
  })

  it('keeps itself open when confirmed, so a failure has somewhere to be reported', async () => {
    const user = userEvent.setup()
    const onConfirm = vi.fn()
    render(<ConfirmDialog {...base} onCancel={vi.fn()} onConfirm={onConfirm} />)

    const dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: 'Revoke key' }))

    expect(onConfirm).toHaveBeenCalledOnce()
    expect(screen.getByRole('alertdialog')).toBeInTheDocument()
  })

  it('refuses to close mid-flight, because the request is already in the air', async () => {
    const user = userEvent.setup()
    const onCancel = vi.fn()
    render(<ConfirmDialog {...base} busy onCancel={onCancel} onConfirm={vi.fn()} />)

    const dialog = await screen.findByRole('alertdialog')
    expect(within(dialog).getByRole('button', { name: 'Working…' })).toBeDisabled()
    expect(within(dialog).getByRole('button', { name: 'Cancel' })).toBeDisabled()

    await user.keyboard('{Escape}')

    expect(onCancel).not.toHaveBeenCalled()
    expect(screen.getByRole('alertdialog')).toBeInTheDocument()
  })

  it('cancels on Escape when nothing is in flight', async () => {
    const user = userEvent.setup()
    const onCancel = vi.fn()
    render(<ConfirmDialog {...base} onCancel={onCancel} onConfirm={vi.fn()} />)

    await screen.findByRole('alertdialog')
    await user.keyboard('{Escape}')

    expect(onCancel).toHaveBeenCalledOnce()
  })
})

describe('Modal', () => {
  it('closes on Escape and on an outside press when it holds nothing unrecoverable', async () => {
    const user = userEvent.setup()
    const onClose = vi.fn()
    const { rerender } = render(
      <Modal open title="Delete old request logs" onClose={onClose}>
        <p>Body</p>
      </Modal>,
    )

    await screen.findByRole('dialog')
    expect(screen.getByRole('button', { name: 'Close dialog' })).toBeInTheDocument()

    await user.keyboard('{Escape}')
    expect(onClose).toHaveBeenCalledOnce()

    rerender(
      <Modal open title="Delete old request logs" onClose={onClose}>
        <p>Body</p>
      </Modal>,
    )
    await user.click(backdrop('dialog'))
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(2))
  })

  it('cancels a casual dismissal when the dialog holds the only copy of something', async () => {
    const user = userEvent.setup()
    const onClose = vi.fn()
    render(
      <Modal dismissible={false} open title="API key created" onClose={onClose}>
        <p>fgw_shown_once</p>
      </Modal>,
    )

    await screen.findByRole('dialog')
    // No stray-click target at all: the gateway keeps only the key's hash, so a
    // dismissal has to be deliberate.
    expect(screen.queryByRole('button', { name: 'Close dialog' })).toBeNull()

    await user.keyboard('{Escape}')
    await user.click(backdrop('dialog'))

    expect(onClose).not.toHaveBeenCalled()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('still closes through an explicit control while non-dismissible', async () => {
    const user = userEvent.setup()
    const onClose = vi.fn()
    render(
      <Modal
        dismissible={false}
        footer={<button onClick={onClose} type="button">Done</button>}
        open
        title="API key created"
        onClose={onClose}
      >
        <p>fgw_shown_once</p>
      </Modal>,
    )

    await user.click(await screen.findByRole('button', { name: 'Done' }))
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('renders its description for the operator, and omits it when there is none', async () => {
    const { rerender } = render(
      <Modal description="Deletion is permanent." open title="Delete old request logs" onClose={vi.fn()}>
        <p>Body</p>
      </Modal>,
    )

    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Deletion is permanent.')).toBeInTheDocument()

    rerender(
      <Modal open title="Delete old request logs" onClose={vi.fn()}>
        <p>Body</p>
      </Modal>,
    )
    expect(screen.queryByText('Deletion is permanent.')).toBeNull()
  })
})

describe('Notice', () => {
  it('interrupts for an error and waits for idle on everything else', () => {
    // Assistive tech treats the two differently, and only a failure is worth
    // cutting across whatever the operator is reading.
    const { rerender } = render(<Notice tone="error">Request logs could not be loaded.</Notice>)
    expect(screen.getByRole('alert')).toHaveTextContent('Request logs could not be loaded.')

    for (const tone of ['info', 'success', 'warning'] as const) {
      rerender(<Notice tone={tone}>12 entries were deleted.</Notice>)
      expect(screen.getByRole('status')).toHaveTextContent('12 entries were deleted.')
      expect(screen.queryByRole('alert')).toBeNull()
    }
  })

  it('is announced politely by default, since most notices are not failures', () => {
    render(<Notice>This gateway does not report a time series.</Notice>)

    expect(screen.getByRole('status')).toBeInTheDocument()
  })
})

describe('the remaining page furniture', () => {
  it('names the page in a single top-level heading, beside its controls', () => {
    const { rerender } = render(
      <PageHeader
        actions={<button type="button">Refresh</button>}
        description="Trace persisted request events."
        title="Request Logs"
      />,
    )

    expect(screen.getByRole('heading', { level: 1, name: 'Request Logs' })).toBeInTheDocument()
    expect(screen.getByText('Trace persisted request events.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeInTheDocument()

    rerender(<PageHeader description="Trace persisted request events." title="Request Logs" />)
    // A page with nothing to offer gets no empty control strip taking up the
    // gap the layout reserves for one.
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('announces loading as a status rather than leaving a blank panel', () => {
    render(<LoadingState label="Loading request logs" />)

    expect(screen.getByRole('status')).toHaveTextContent('Loading request logs')
  })

  it('says why a section is empty, and offers the way out of it', () => {
    render(
      <EmptyState
        action={<button type="button">Open Playground</button>}
        description="Send a request through the Playground."
        title="No requests in this range"
      />,
    )

    expect(screen.getByRole('heading', { name: 'No requests in this range' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Open Playground' })).toBeInTheDocument()
  })

  it('gives each status pill tone its own colour, so severity is not read off the text alone', () => {
    render(
      <>
        <StatusPill tone="success">Ready</StatusPill>
        <StatusPill tone="warning">Denied</StatusPill>
        <StatusPill tone="error">Open</StatusPill>
        <StatusPill tone="info">Translate</StatusPill>
        <StatusPill tone="neutral">Required</StatusPill>
      </>,
    )

    expect(screen.getByText('Ready').className).toContain('success')
    expect(screen.getByText('Denied').className).toContain('warning')
    expect(screen.getByText('Open').className).toContain('danger')
    expect(screen.getByText('Translate').className).toContain('info')
    expect(screen.getByText('Required').className).toContain('muted')
  })
})
