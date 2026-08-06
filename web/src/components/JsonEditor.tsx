import { Braces, CheckCircle2, Minimize2, TriangleAlert, WrapText } from 'lucide-react'
import { useCallback, useId, useMemo, useRef, type ReactNode } from 'react'
import { cn } from '@/lib/utils'
import { countLines, formatJson, INDENT, jsonProblem, minifyJson, tokenizeJson, type JsonTokenKind } from '../lib/json'
import { formatNumber } from '../lib/format'
import { Button, CopyButton } from './ui'

/**
 * A JSON viewer that can also be typed into.
 *
 * Highlighting is drawn on a `<pre>` and the caret belongs to a transparent
 * `<textarea>` laid exactly over it, which is why both layers share one class
 * string: any divergence in font, padding or line height shows up as text that
 * drifts away from its own caret. The alternative — an editor component — costs
 * a few hundred kilobytes and, in the case of the popular ones, a web worker
 * and a stylesheet injected at runtime, which this dashboard's
 * Content-Security-Policy would refuse.
 *
 * The layers scroll together because neither scrolls: the surrounding box does.
 * The textarea is sized to the highlighted text rather than to the viewport, so
 * it never has a scroll position of its own to keep in sync.
 */

const TOKEN_CLASS: Record<JsonTokenKind, string> = {
  key: 'text-code-key',
  string: 'text-code-string',
  number: 'text-code-number',
  literal: 'text-code-literal',
  plain: 'text-code-punct',
}

/** Font, size, rhythm and padding shared by the highlight and the caret layer. */
const LAYER = 'px-3 py-2.5 font-mono text-xs leading-[1.6] whitespace-pre'

/**
 * `hidden` only when a textarea is laid over this: then the caret layer holds
 * the same text and announcing both reads the document twice. Read-only, this
 * is the only copy of it on the page, and hiding it would leave a screen reader
 * with an empty panel.
 */
function Highlight({ value, hidden }: { value: string; hidden: boolean }) {
  const tokens = useMemo(() => tokenizeJson(value), [value])
  return (
    <pre aria-hidden={hidden || undefined} className={cn(LAYER, 'min-w-full text-foreground')}>
      {tokens.map((token, index) => (
        <span className={TOKEN_CLASS[token.kind]} key={index}>
          {token.text}
        </span>
      ))}
      {/* The last line has no newline of its own, so without this the caret on
          it sits outside the box the highlight drew. */}
      {'\n'}
    </pre>
  )
}

function Gutter({ lines }: { lines: number }) {
  const numbers = useMemo(() => Array.from({ length: Math.max(lines, 1) }, (_, index) => index + 1).join('\n'), [lines])
  return (
    <pre
      aria-hidden="true"
      className={cn(
        LAYER,
        // Sticky so the numbers survive a horizontal scroll; the vertical
        // scroll is the shared one and needs no help.
        'sticky left-0 z-10 shrink-0 select-none border-r border-border bg-muted/60 px-2.5 text-right text-muted-foreground',
      )}
    >
      {numbers}
      {'\n'}
    </pre>
  )
}

export interface JsonEditorProps {
  value: string
  /** Absent means read-only: no caret layer is rendered at all. */
  onChange?: (value: string) => void
  /**
   * Accessible name — of the editable field, or of the scrollable panel when
   * read-only. Read-only it is also what makes the panel reachable by keyboard:
   * the document scrolls, and a scrollable box that cannot be focused cannot be
   * scrolled without a pointer.
   */
  label: string
  /** Names what the copy button puts on the clipboard. */
  copyLabel: string
  /** Rendered at the right of the toolbar, before the copy button. */
  actions?: ReactNode
  className?: string
}

export function JsonEditor({ value, onChange, label, copyLabel, actions, className }: JsonEditorProps) {
  const editing = onChange != null
  const textarea = useRef<HTMLTextAreaElement>(null)
  const statusId = useId()
  const problem = useMemo(() => jsonProblem(value), [value])
  const lines = countLines(value)

  const replace = useCallback(
    (next: string) => {
      if (next !== value) onChange?.(next)
      // Returning focus keeps the keyboard where it was: reformatting is a step
      // in an edit, not the end of one.
      textarea.current?.focus()
    },
    [onChange, value],
  )

  /*
   * Tab indents instead of leaving the field. A textarea is the one control
   * where that trade is right — the document is indentation-significant to
   * read, and Escape then Tab still reaches the next control, which is what
   * keeps the page navigable by keyboard.
   */
  const onKeyDown = (event: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key !== 'Tab' || event.shiftKey || !onChange) return
    event.preventDefault()
    const field = event.currentTarget
    const { selectionStart, selectionEnd } = field
    const indent = ' '.repeat(INDENT)
    onChange(`${value.slice(0, selectionStart)}${indent}${value.slice(selectionEnd)}`)
    requestAnimationFrame(() => {
      field.selectionStart = field.selectionEnd = selectionStart + INDENT
    })
  }

  return (
    <div className={cn('flex flex-col gap-2', className)}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="flex items-center gap-1.5 text-xs text-muted-foreground" id={statusId}>
          {problem ? (
            <TriangleAlert aria-hidden="true" className="size-3.5 text-warning" />
          ) : (
            <CheckCircle2 aria-hidden="true" className="size-3.5 text-success" />
          )}
          {problem ? (
            <span className="text-warning">
              {problem.line > 0 ? `Line ${formatNumber(problem.line)}: ` : ''}
              {problem.message}
            </span>
          ) : (
            <span>
              Valid JSON · {formatNumber(lines)} {lines === 1 ? 'line' : 'lines'} · {formatNumber(value.length)} characters
            </span>
          )}
        </p>
        <div className="flex flex-wrap items-center gap-1.5">
          {editing ? (
            <>
              <Button
                disabled={problem != null}
                size="sm"
                type="button"
                variant="outline"
                onClick={() => replace(formatJson(value).text)}
              >
                <WrapText aria-hidden="true" size={14} />
                Beautify
              </Button>
              <Button
                disabled={problem != null}
                size="sm"
                type="button"
                variant="outline"
                onClick={() => replace(minifyJson(value).text)}
              >
                <Minimize2 aria-hidden="true" size={14} />
                Minify
              </Button>
            </>
          ) : null}
          {actions}
          <CopyButton label={copyLabel} value={value} />
        </div>
      </div>

      <div
        aria-describedby={editing ? undefined : statusId}
        aria-label={editing ? undefined : label}
        className={cn(
          'relative max-h-[28rem] overflow-auto rounded-md border bg-muted/40 focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none',
          problem && editing ? 'border-warning/50' : 'border-border',
        )}
        role={editing ? undefined : 'group'}
        tabIndex={editing ? undefined : 0}
      >
        <div className="flex min-w-full">
          <Gutter lines={lines} />
          <div className="relative min-w-0 grow">
            <Highlight hidden={editing} value={value} />
            {editing ? (
              <textarea
                aria-describedby={statusId}
                aria-label={label}
                autoCapitalize="off"
                autoCorrect="off"
                className={cn(
                  LAYER,
                  // Inset over the highlight rather than beside it: same box,
                  // same metrics, invisible glyphs, visible caret.
                  'absolute inset-0 size-full resize-none overflow-hidden border-0 bg-transparent text-transparent caret-foreground outline-none selection:bg-primary/30 focus-visible:ring-0',
                )}
                onChange={(event) => onChange?.(event.target.value)}
                onKeyDown={onKeyDown}
                ref={textarea}
                spellCheck={false}
                value={value}
                wrap="off"
              />
            ) : null}
          </div>
        </div>
      </div>

      {editing ? (
        <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <Braces aria-hidden="true" className="size-3.5" />
          Tab inserts {INDENT} spaces. Press Escape first to move on to the next control.
        </p>
      ) : null}
    </div>
  )
}
