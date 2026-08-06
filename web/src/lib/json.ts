/**
 * Reading and reshaping the configuration document the Admin API serves.
 *
 * The editor on the Configuration page is a plain textarea drawn over a
 * highlighted copy of the same text, so everything here is a pure function over
 * a string: the highlighter cannot be allowed to alter what gets saved.
 */

export type JsonTokenKind = 'key' | 'string' | 'number' | 'literal' | 'plain'

export interface JsonToken {
  kind: JsonTokenKind
  text: string
}

/**
 * Above this, highlighting is skipped and the text is returned as one token.
 *
 * A span per token is cheap until it isn't: a document this large produces tens
 * of thousands of DOM nodes, and the cost lands on every keystroke. The text
 * still renders, just in one colour — which is the right trade for a document
 * nobody is reading by eye at that size.
 */
export const HIGHLIGHT_MAX_CHARS = 200_000

/*
 * Strings are matched first, so a `null` or a digit inside one is consumed as
 * part of the string rather than picked out as a literal of its own.
 */
const JSON_TOKEN = /"(?:\\.|[^"\\])*"|\b(?:true|false|null)\b|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/g

export function tokenizeJson(source: string): JsonToken[] {
  if (source.length > HIGHLIGHT_MAX_CHARS) return [{ kind: 'plain', text: source }]

  const tokens: JsonToken[] = []
  let last = 0
  JSON_TOKEN.lastIndex = 0
  for (let match = JSON_TOKEN.exec(source); match; match = JSON_TOKEN.exec(source)) {
    const text = match[0]
    if (match.index > last) tokens.push({ kind: 'plain', text: source.slice(last, match.index) })
    if (text.startsWith('"')) {
      // What follows decides the role: a quoted run before a colon names a
      // field, the same run anywhere else is a value.
      const isKey = /^\s*:/.test(source.slice(match.index + text.length))
      tokens.push({ kind: isKey ? 'key' : 'string', text })
    } else if (text === 'true' || text === 'false' || text === 'null') {
      tokens.push({ kind: 'literal', text })
    } else {
      tokens.push({ kind: 'number', text })
    }
    last = match.index + text.length
  }
  if (last < source.length) tokens.push({ kind: 'plain', text: source.slice(last) })
  return tokens
}

export interface JsonProblem {
  message: string
  /** 1-based, or 0 when the engine did not report a position. */
  line: number
}

/**
 * Strips the document out of the engine's own message.
 *
 * V8 spells a syntax error two ways. One names a position; the other quotes a
 * slice of the input instead — `Unexpected token '}', "{"a": }" is not valid
 * JSON` — and for a real configuration that slice is a wall of braces in a
 * one-line status bar. The part before the quote is the part that says
 * something.
 */
function withoutQuotedInput(message: string): string {
  const trimmed = /^(.*?),\s*(?:\.\.\.)?".*"\s+is not valid JSON$/s.exec(message)?.[1]
  return trimmed ? `${trimmed}.` : message
}

/**
 * Where the document stops being valid JSON, or null when it is valid.
 *
 * The line is derived rather than trusted: the engine reports a character
 * offset for some errors, a line and column for others, and for the shortest
 * documents neither. Counting newlines up to the offset gives one answer from
 * whichever of the two is present, and `line: 0` means the engine offered no
 * position at all — the message still names the token it choked on.
 */
export function jsonProblem(source: string): JsonProblem | null {
  if (source.trim() === '') return { message: 'The document is empty.', line: 0 }
  try {
    JSON.parse(source)
    return null
  } catch (error) {
    const raw = error instanceof Error ? error.message : 'The document is not valid JSON.'
    const offset = /at position (\d+)/.exec(raw)?.[1]
    const reported = /\bline (\d+)/.exec(raw)?.[1]
    const line =
      offset !== undefined
        ? source.slice(0, Number(offset)).split('\n').length
        : reported !== undefined
          ? Number(reported)
          : 0
    return { message: withoutQuotedInput(raw), line }
  }
}

export const INDENT = 2

export interface Reformatted {
  text: string
  problem: JsonProblem | null
}

/** Re-indents the document, or returns it untouched with the reason it could not. */
export function formatJson(source: string): Reformatted {
  const problem = jsonProblem(source)
  if (problem) return { text: source, problem }
  return { text: JSON.stringify(JSON.parse(source), null, INDENT), problem: null }
}

/** Strips every optional space, for pasting into a shell or a request body. */
export function minifyJson(source: string): Reformatted {
  const problem = jsonProblem(source)
  if (problem) return { text: source, problem }
  return { text: JSON.stringify(JSON.parse(source)), problem: null }
}

export function countLines(source: string): number {
  return source === '' ? 0 : source.split('\n').length
}
