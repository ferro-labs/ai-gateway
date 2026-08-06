/**
 * The token GET /admin/config substitutes for secret material, and the paths it
 * can appear at.
 *
 * Mirrors `RedactedSecretField` in internal/admin/repository/scrub.go, path for
 * path. PUT refuses a body where any of them still holds a token, so checking
 * the same paths here lets Save explain itself before the round trip instead of
 * after a 400.
 */
export const REDACTED_PLACEHOLDER = '[REDACTED]'

/**
 * The prefix every redaction token starts with — the whole-value placeholder and
 * the narrower ones ("[REDACTED_BEARER_TOKEN]", "[REDACTED:OPENAI_API_KEY]").
 *
 * A value the Admin API judged safe to serve keeps its shape and loses only its
 * secret parts, so a token arrives surrounded by the text it was cut from.
 * Matching the prefix wherever it appears mirrors `redactedMarker` on the server
 * and keeps Save refusing exactly what PUT would refuse.
 */
export const REDACTED_MARKER = '[REDACTED'

export function containsRedactedValue(value: unknown): boolean {
  if (typeof value === 'string') return value.includes(REDACTED_MARKER)
  if (Array.isArray(value)) return value.some(containsRedactedValue)
  if (value && typeof value === 'object') {
    return Object.values(value as Record<string, unknown>).some(containsRedactedValue)
  }
  return false
}

export function findRedactedFields(config: Record<string, unknown>): string[] {
  const fields: string[] = []
  const observability = config.observability as Record<string, unknown> | undefined
  const tracing = observability?.tracing as Record<string, unknown> | undefined
  if (containsRedactedValue(tracing?.headers)) fields.push('observability.tracing.headers')

  const mcpServers = config.mcp_servers as Array<Record<string, unknown>> | undefined
  mcpServers?.forEach((server, index) => {
    if (containsRedactedValue(server?.headers)) fields.push(`mcp_servers[${index}].headers`)
    if (containsRedactedValue(server?.env)) fields.push(`mcp_servers[${index}].env`)
  })

  const exporters = observability?.exporters as Array<Record<string, unknown>> | undefined
  exporters?.forEach((exporter, index) => {
    if (containsRedactedValue(exporter?.config)) fields.push(`observability.exporters[${index}].config`)
  })

  const plugins = config.plugins as Array<Record<string, unknown>> | undefined
  plugins?.forEach((plugin, index) => {
    if (containsRedactedValue(plugin?.config)) fields.push(`plugins[${index}].config`)
  })

  return fields
}

export function redactedFieldsMessage(fields: string[]): string {
  const list = fields.join(', ')
  const verb = fields.length === 1 ? 'contains' : 'contain'
  return `${list} still ${verb} the "${REDACTED_PLACEHOLDER}" placeholder from the last read. Replace it with the real value or a \${VAR} reference before saving.`
}
