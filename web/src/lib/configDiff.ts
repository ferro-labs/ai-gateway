/*
 * A line-based diff over two pretty-printed configuration documents.
 *
 * Textual rather than structural, and that is sound here: both documents are
 * serialized by the same Go marshaller, so key order is stable and a line that
 * differs is a value that differs rather than the same value written in another
 * order. A structural diff would need a dependency and answer the same question.
 */

/** `gap` is not a document line — it stands in for a run of collapsed ones. */
export type DiffKind = 'context' | 'added' | 'removed' | 'gap'

export interface DiffLine {
  kind: DiffKind
  text: string
}

/**
 * Above this the LCS table is abandoned and the two documents are reported as a
 * wholesale replacement.
 *
 * The table is O(n·m) cells, so an unbounded one on a large enough document
 * allocates hundreds of megabytes and freezes the tab before it can render
 * anything. A coarse diff is readable; a hung browser is not. Lifting the cap
 * means replacing the algorithm — Myers diff is O(n·d) in space.
 */
const MAX_DIFF_CELLS = 4_000_000

/** Unchanged lines kept either side of a change, so a hunk reads in context. */
const CONTEXT_LINES = 3

export function diffLines(before: string[], after: string[]): DiffLine[] {
  if (before.length * after.length > MAX_DIFF_CELLS) {
    return [
      ...before.map((text): DiffLine => ({ kind: 'removed', text })),
      ...after.map((text): DiffLine => ({ kind: 'added', text })),
    ]
  }

  // Longest common subsequence, filled from the end so the backtrack below can
  // walk forwards and emit lines in document order.
  const width = after.length + 1
  const lcs = new Uint32Array((before.length + 1) * width)
  // The sentinel row and column past the end of each document are zero, which
  // is also what an out-of-range read defaults to here.
  const at = (i: number, j: number) => lcs[i * width + j] ?? 0
  for (let i = before.length - 1; i >= 0; i--) {
    for (let j = after.length - 1; j >= 0; j--) {
      lcs[i * width + j] =
        before[i] === after[j] ? at(i + 1, j + 1) + 1 : Math.max(at(i + 1, j), at(i, j + 1))
    }
  }

  const lines: DiffLine[] = []
  let i = 0
  let j = 0
  while (i < before.length && j < after.length) {
    const removed = before[i] ?? ''
    const added = after[j] ?? ''
    if (removed === added) {
      lines.push({ kind: 'context', text: removed })
      i++
      j++
    } else if (at(i + 1, j) >= at(i, j + 1)) {
      lines.push({ kind: 'removed', text: removed })
      i++
    } else {
      lines.push({ kind: 'added', text: added })
      j++
    }
  }
  while (i < before.length) lines.push({ kind: 'removed', text: before[i++] ?? '' })
  while (j < after.length) lines.push({ kind: 'added', text: after[j++] ?? '' })
  return lines
}

/**
 * Replaces long runs of unchanged lines with a single summary line.
 *
 * A gateway configuration is mostly targets and plugin blocks that a rollback
 * does not touch. Printing all of them buries the handful of lines the operator
 * is about to change, which is the one thing this view exists to show.
 */
export function collapseUnchanged(lines: DiffLine[]): DiffLine[] {
  const keep = lines.map(() => false)
  lines.forEach((line, index) => {
    if (line.kind === 'context') return
    const first = Math.max(0, index - CONTEXT_LINES)
    const last = Math.min(lines.length - 1, index + CONTEXT_LINES)
    for (let k = first; k <= last; k++) keep[k] = true
  })

  const collapsed: DiffLine[] = []
  let skipped = 0
  const flush = () => {
    if (skipped === 0) return
    collapsed.push({ kind: 'gap', text: `${skipped} unchanged line${skipped === 1 ? '' : 's'}` })
    skipped = 0
  }
  lines.forEach((line, index) => {
    if (!keep[index]) {
      skipped++
      return
    }
    flush()
    collapsed.push(line)
  })
  flush()
  return collapsed
}

/**
 * Which top-level sections differ — the short form for a confirmation dialog,
 * whose description is a single string and cannot hold the diff itself.
 */
export function changedTopLevelKeys(
  before: Record<string, unknown>,
  after: Record<string, unknown>,
): string[] {
  const keys = new Set([...Object.keys(before), ...Object.keys(after)])
  return [...keys]
    .filter((key) => JSON.stringify(before[key]) !== JSON.stringify(after[key]))
    .sort()
}
