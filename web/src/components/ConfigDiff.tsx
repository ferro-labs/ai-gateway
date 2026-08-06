import { useMemo } from 'react'
import { cn } from '@/lib/utils'
import { collapseUnchanged, diffLines, type DiffKind } from '../lib/configDiff'

const KIND_CLASS: Record<DiffKind, string> = {
  added: 'bg-success-soft text-success',
  removed: 'bg-danger-soft text-danger',
  context: 'text-muted-foreground',
  gap: 'bg-muted/40 text-muted-foreground italic',
}

const KIND_MARKER: Record<DiffKind, string> = {
  added: '+',
  removed: '-',
  context: ' ',
  gap: '⋯',
}

/**
 * Colour is not the only signal: every changed line carries a marker glyph and
 * an off-screen word, so the diff survives a monochrome display and reads
 * correctly to a screen reader.
 */
const KIND_LABEL: Record<DiffKind, string> = {
  added: 'Added:',
  removed: 'Removed:',
  context: '',
  // The gap line already says "N unchanged lines" in its own text.
  gap: '',
}

export function ConfigDiff({
  before,
  after,
  emptyLabel,
}: {
  /** Pretty-printed document the change starts from. */
  before: string
  /** Pretty-printed document the change ends at. */
  after: string
  /** Shown instead of an empty diff when the two documents match. */
  emptyLabel: string
}) {
  const lines = useMemo(() => diffLines(before.split('\n'), after.split('\n')), [before, after])
  const added = lines.filter((line) => line.kind === 'added').length
  const removed = lines.filter((line) => line.kind === 'removed').length
  const shown = useMemo(() => collapseUnchanged(lines), [lines])

  if (added === 0 && removed === 0) {
    return <p className="text-sm text-muted-foreground">{emptyLabel}</p>
  }

  return (
    <div className="flex flex-col gap-1.5">
      <p className="text-xs text-muted-foreground">
        {added} line{added === 1 ? '' : 's'} added · {removed} line{removed === 1 ? '' : 's'} removed
      </p>
      <ol className="max-h-80 list-none overflow-auto rounded-md border border-border bg-muted/20 py-1 font-mono text-xs">
        {shown.map((line, index) => (
          <li className={cn('whitespace-pre px-2', KIND_CLASS[line.kind])} key={`${index}-${line.text}`}>
            {KIND_LABEL[line.kind] ? <span className="sr-only">{KIND_LABEL[line.kind]} </span> : null}
            <span aria-hidden="true" className="select-none">{KIND_MARKER[line.kind]} </span>
            {line.text}
          </li>
        ))}
      </ol>
    </div>
  )
}
