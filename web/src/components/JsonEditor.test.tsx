import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import { JsonEditor } from './JsonEditor'

/** A caller that actually owns the value, since every control here edits it. */
function Editable({ initial }: { initial: string }) {
  const [value, setValue] = useState(initial)
  return <JsonEditor copyLabel="Document" label="Document JSON" value={value} onChange={setValue} />
}

function field(): HTMLTextAreaElement {
  return screen.getByLabelText<HTMLTextAreaElement>('Document JSON')
}

describe('JsonEditor read-only', () => {
  it('exposes the document rather than hiding it behind the caret layer', () => {
    // Read-only there is no textarea, so the highlighted copy is the only one on
    // the page. Marking it aria-hidden — which is right while editing — would
    // leave a screen reader with an empty panel.
    render(<JsonEditor copyLabel="Document" label="Active configuration" value='{"mode": "fallback"}' />)

    expect(screen.getByLabelText('Active configuration')).toHaveTextContent('"mode": "fallback"')
  })

  it('makes the scrolling panel reachable by keyboard', () => {
    // The panel scrolls at 28rem. A scrollable box that cannot be focused
    // cannot be scrolled without a pointer.
    render(<JsonEditor copyLabel="Document" label="Active configuration" value='{"a": 1}' />)

    expect(screen.getByLabelText('Active configuration')).toHaveAttribute('tabindex', '0')
  })

  it('offers no reformatting controls, which would suggest an edit it cannot save', () => {
    render(<JsonEditor copyLabel="Document" label="Active configuration" value='{"a": 1}' />)

    expect(screen.queryByRole('button', { name: 'Beautify' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Minify' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Document' })).toBeInTheDocument()
  })
})

describe('JsonEditor validity', () => {
  it('counts the document when it parses', () => {
    render(<JsonEditor copyLabel="Document" label="Active configuration" value={'{\n  "a": 1\n}'} />)

    expect(screen.getByText(/Valid JSON · 3 lines · 12 characters/)).toBeInTheDocument()
  })

  it('names the failure instead of silently refusing to save', async () => {
    render(<Editable initial='{"a": 1}' />)
    await userEvent.clear(field())
    await userEvent.type(field(), '{{"a": }')

    expect(await screen.findByText(/Unexpected token/)).toBeInTheDocument()
  })

  it('withholds reformatting while the document does not parse', async () => {
    render(<Editable initial='{"a": 1}' />)
    await userEvent.type(field(), 'x')

    expect(screen.getByRole('button', { name: 'Beautify' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Minify' })).toBeDisabled()
  })
})

describe('JsonEditor reformatting', () => {
  it('beautifies in place and leaves the caret in the document', async () => {
    render(<Editable initial='{"a":[1,2]}' />)

    await userEvent.click(screen.getByRole('button', { name: 'Beautify' }))

    expect(field().value).toBe('{\n  "a": [\n    1,\n    2\n  ]\n}')
    // Reformatting is a step in an edit, not the end of one: sending focus to
    // the toolbar would cost a keyboard user their place every time.
    expect(field()).toHaveFocus()
  })

  it('minifies for pasting elsewhere', async () => {
    render(<Editable initial={'{\n  "a": 1\n}'} />)

    await userEvent.click(screen.getByRole('button', { name: 'Minify' }))

    expect(field().value).toBe('{"a":1}')
  })
})

describe('JsonEditor typing', () => {
  it('indents with Tab rather than leaving the field', async () => {
    render(<Editable initial="" />)
    field().focus()

    await userEvent.keyboard('{Tab}')

    expect(field().value).toBe('  ')
    expect(field()).toHaveFocus()
  })

  it('lets Shift+Tab out, so the page stays navigable by keyboard', async () => {
    render(<Editable initial="" />)
    field().focus()

    await userEvent.keyboard('{Shift>}{Tab}{/Shift}')

    expect(field()).not.toHaveFocus()
  })
})
