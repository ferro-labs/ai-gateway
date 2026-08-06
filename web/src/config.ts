export interface RuntimeConfig {
  gatewayBaseUrl: string
}

const configURL = `${import.meta.env.BASE_URL}config.json`

function parseRuntimeConfig(value: unknown): RuntimeConfig {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new Error('config.json must contain a JSON object')
  }

  const gatewayBaseUrl = (value as Record<string, unknown>).gatewayBaseUrl
  if (gatewayBaseUrl !== undefined && typeof gatewayBaseUrl !== 'string') {
    throw new Error('config.json gatewayBaseUrl must be a string')
  }
  return { gatewayBaseUrl: gatewayBaseUrl ?? '' }
}

export async function loadRuntimeConfig(fetchConfig: typeof fetch = fetch): Promise<RuntimeConfig> {
  const response = await fetchConfig(configURL, {
    cache: 'no-store',
    credentials: 'same-origin',
    headers: { Accept: 'application/json' },
  })
  if (!response.ok) {
    throw new Error(`Could not load runtime configuration (HTTP ${response.status})`)
  }
  return parseRuntimeConfig(await response.json())
}
