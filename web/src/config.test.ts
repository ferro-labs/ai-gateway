import { describe, expect, it, vi } from 'vitest'
import { loadRuntimeConfig } from './config'

describe('runtime configuration', () => {
  it('loads the gateway origin without build-time substitution', async () => {
    const fetchConfig = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      gatewayBaseUrl: 'https://gateway.example.com',
    }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))

    await expect(loadRuntimeConfig(fetchConfig as typeof fetch)).resolves.toEqual({
      gatewayBaseUrl: 'https://gateway.example.com',
    })
    expect(fetchConfig).toHaveBeenCalledWith('/config.json', expect.objectContaining({
      cache: 'no-store',
    }))
  })

  it('defaults to same-origin APIs when the origin is omitted', async () => {
    const fetchConfig = vi.fn().mockResolvedValue(new Response('{}', { status: 200 }))
    await expect(loadRuntimeConfig(fetchConfig as typeof fetch)).resolves.toEqual({
      gatewayBaseUrl: '',
    })
  })

  it('rejects invalid runtime configuration', async () => {
    const fetchConfig = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      gatewayBaseUrl: 42,
    }), { status: 200 }))
    await expect(loadRuntimeConfig(fetchConfig as typeof fetch)).rejects.toThrow('must be a string')
  })
})
