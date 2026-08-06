import { describe, expect, it } from 'vitest'
import {
  countLines,
  formatJson,
  HIGHLIGHT_MAX_CHARS,
  jsonProblem,
  minifyJson,
  tokenizeJson,
  type JsonToken,
} from './json'

/** Reassembles the token stream, which must always be the input verbatim. */
function joined(tokens: JsonToken[]): string {
  return tokens.map((token) => token.text).join('')
}

function kinds(tokens: JsonToken[], text: string): string[] {
  return tokens.filter((token) => token.text === text).map((token) => token.kind)
}

describe('tokenizeJson', () => {
  it('never alters the document it highlights', () => {
    const source = '{\n  "a": [1, true, null, "x"]\n}'

    expect(joined(tokenizeJson(source))).toBe(source)
  })

  it('separates a field name from a value spelled identically', () => {
    // `{"mode": "mode"}` is the case that catches a highlighter keying off the
    // quotes alone: the same characters are a key once and a value once.
    const tokens = tokenizeJson('{"mode": "mode"}')

    expect(kinds(tokens, '"mode"')).toEqual(['key', 'string'])
  })

  it('leaves a literal inside a string alone', () => {
    // Colouring the `null` inside "nullable" would break the word in half on
    // screen, and the same regex would do it to every model id containing a
    // digit.
    const tokens = tokenizeJson('{"note": "nullable, true, 42"}')

    expect(tokens.filter((token) => token.kind === 'literal')).toEqual([])
    expect(tokens.filter((token) => token.kind === 'number')).toEqual([])
  })

  it('marks numbers, booleans and null outside strings', () => {
    const tokens = tokenizeJson('{"n": -1.5e3, "b": false, "z": null}')

    expect(kinds(tokens, '-1.5e3')).toEqual(['number'])
    expect(kinds(tokens, 'false')).toEqual(['literal'])
    expect(kinds(tokens, 'null')).toEqual(['literal'])
  })

  it('gives up rather than emitting a span per token for an enormous document', () => {
    const huge = `{"pad": "${'x'.repeat(HIGHLIGHT_MAX_CHARS)}"}`

    expect(tokenizeJson(huge)).toEqual([{ kind: 'plain', text: huge }])
  })
})

describe('jsonProblem', () => {
  it('reports nothing for a valid document', () => {
    expect(jsonProblem('{"a": 1}')).toBeNull()
  })

  it('calls an empty document empty rather than quoting a parser', () => {
    // JSON.parse('') says "Unexpected end of JSON input", which reads as
    // corruption to someone who has simply selected all and deleted.
    expect(jsonProblem('   ')).toEqual({ message: 'The document is empty.', line: 0 })
  })

  it('counts the line itself rather than trusting the engine to name one', () => {
    const problem = jsonProblem('{\n  "targets": [],\n  "strategy": { "mode" }\n}')

    expect(problem?.line).toBe(3)
  })

  it('says no line rather than a wrong one when the engine reports no position', () => {
    // V8 answers the shortest documents with a quoted snippet and no offset.
    // Guessing a line from that would point the operator at the wrong place.
    expect(jsonProblem('{"a": }')?.line).toBe(0)
  })

  it('keeps the document out of the message it shows', () => {
    // The engine's own text embeds a slice of the input, which for a real
    // configuration is a wall of braces in a one-line status bar.
    const problem = jsonProblem('{"a": }')

    expect(problem?.message).toBe("Unexpected token '}'.")
    expect(problem?.message).not.toContain('is not valid JSON')
  })
})

describe('formatJson and minifyJson', () => {
  it('re-indents a valid document', () => {
    expect(formatJson('{"a":[1,2]}').text).toBe('{\n  "a": [\n    1,\n    2\n  ]\n}')
  })

  it('strips every optional space', () => {
    expect(minifyJson('{\n  "a": 1\n}').text).toBe('{"a":1}')
  })

  it('returns an invalid document untouched, with the reason', () => {
    // Reformatting past a syntax error would either throw away the operator's
    // work or silently "fix" it into something they did not write.
    const broken = '{"a": }'

    expect(formatJson(broken).text).toBe(broken)
    expect(formatJson(broken).problem).not.toBeNull()
    expect(minifyJson(broken).text).toBe(broken)
  })
})

describe('countLines', () => {
  it('counts an empty document as no lines, not as one', () => {
    expect(countLines('')).toBe(0)
    expect(countLines('a')).toBe(1)
    expect(countLines('a\nb\n')).toBe(3)
  })
})
