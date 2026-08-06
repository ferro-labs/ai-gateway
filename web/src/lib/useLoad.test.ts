import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useLoad } from './useLoad'

describe('useLoad', () => {
  it('exposes data once the loader resolves', async () => {
    const { result } = renderHook(() => useLoad(() => Promise.resolve('ok'), [], 'failed'))
    expect(result.current.loading).toBe(true)
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.data).toBe('ok')
    expect(result.current.error).toBe('')
  })

  it('reports a failure without clearing what was already shown', async () => {
    let attempt = 0
    const { result } = renderHook(() =>
      useLoad(
        () => (attempt++ === 0 ? Promise.resolve('first') : Promise.reject(new Error('boom'))),
        [],
        'could not load',
      ),
    )
    await waitFor(() => expect(result.current.data).toBe('first'))

    act(() => result.current.refresh())
    await waitFor(() => expect(result.current.error).toBe('boom'))
    // Retaining the previous payload is deliberate; pages label it as stale.
    expect(result.current.data).toBe('first')
    expect(result.current.loading).toBe(false)
  })

  it('clears a previous error when a retry succeeds', async () => {
    let attempt = 0
    const { result } = renderHook(() =>
      useLoad(
        () => (attempt++ === 0 ? Promise.reject(new Error('boom')) : Promise.resolve('recovered')),
        [],
        'could not load',
      ),
    )
    await waitFor(() => expect(result.current.error).toBe('boom'))

    act(() => result.current.refresh())
    await waitFor(() => expect(result.current.data).toBe('recovered'))
    expect(result.current.error).toBe('')
  })

  it('re-runs when a dependency changes', async () => {
    const loader = vi.fn(() => Promise.resolve('x'))
    const { rerender } = renderHook(({ hours }) => useLoad(() => loader(), [hours], 'failed'), {
      initialProps: { hours: 1 },
    })
    await waitFor(() => expect(loader).toHaveBeenCalledTimes(1))

    rerender({ hours: 24 })
    await waitFor(() => expect(loader).toHaveBeenCalledTimes(2))
  })

  it('does not re-run when an unrelated render occurs', async () => {
    const loader = vi.fn(() => Promise.resolve('x'))
    const { rerender } = renderHook(({ hours }) => useLoad(() => loader(), [hours], 'failed'), {
      initialProps: { hours: 1 },
    })
    await waitFor(() => expect(loader).toHaveBeenCalledTimes(1))

    // The loader closure is new on every render; if it were a dependency this
    // would fetch forever.
    rerender({ hours: 1 })
    rerender({ hours: 1 })
    expect(loader).toHaveBeenCalledTimes(1)
  })

  it('aborts the in-flight request on unmount', async () => {
    let captured: AbortSignal | undefined
    const { unmount } = renderHook(() =>
      useLoad((signal) => {
        captured = signal
        return new Promise<string>(() => {
          // Never settles: the abort is the only way this ends.
        })
      }, [], 'failed'),
    )
    await waitFor(() => expect(captured).toBeDefined())
    expect(captured?.aborted).toBe(false)

    unmount()
    expect(captured?.aborted).toBe(true)
  })

  describe('polling', () => {
    // The interval is armed during the first render, so the clock has to be
    // faked before the hook mounts or the timer that gets created is a real one
    // and advancing the fake clock moves nothing.
    beforeEach(() => vi.useFakeTimers())

    afterEach(() => {
      visibility('visible')
      vi.useRealTimers()
    })

    function visibility(state: DocumentVisibilityState): void {
      Object.defineProperty(document, 'visibilityState', { configurable: true, value: state })
    }

    /** Advances the fake clock and lets every promise it released settle. */
    async function tick(ms: number): Promise<void> {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(ms)
      })
    }

    it('re-runs the loader once per interval', async () => {
      const loader = vi.fn(() => Promise.resolve('x'))
      renderHook(() => useLoad(() => loader(), [], 'failed', { refetchInterval: 5000 }))
      await tick(0)
      expect(loader).toHaveBeenCalledTimes(1)

      await tick(5000)
      expect(loader).toHaveBeenCalledTimes(2)

      await tick(5000)
      expect(loader).toHaveBeenCalledTimes(3)
    })

    it('does not stack a second request behind a slow one', async () => {
      const loader = vi.fn(() => new Promise<string>(() => {
        // Never settles: the poll must wait rather than pile on.
      }))
      renderHook(() => useLoad(() => loader(), [], 'failed', { refetchInterval: 1000 }))
      await tick(0)

      await tick(5000)

      expect(loader).toHaveBeenCalledTimes(1)
    })

    it('pauses while the tab is hidden', async () => {
      const loader = vi.fn(() => Promise.resolve('x'))
      renderHook(() => useLoad(() => loader(), [], 'failed', { refetchInterval: 1000 }))
      await tick(0)
      expect(loader).toHaveBeenCalledTimes(1)

      visibility('hidden')
      await tick(10_000)
      expect(loader).toHaveBeenCalledTimes(1)

      visibility('visible')
      await tick(1000)
      expect(loader).toHaveBeenCalledTimes(2)
    })

    it('keeps the data on screen and the spinner down while refreshing', async () => {
      let attempt = 0
      const { result } = renderHook(() =>
        useLoad(() => Promise.resolve(`page-${++attempt}`), [], 'failed', {
          refetchInterval: 1000,
        }),
      )
      await tick(0)
      expect(result.current.data).toBe('page-1')

      await tick(1000)

      // A table that blanks and re-renders its spinner every interval is
      // unreadable; a background refresh only replaces the payload.
      expect(result.current.data).toBe('page-2')
      expect(result.current.loading).toBe(false)
    })

    it('does not poll when no interval is given', async () => {
      const loader = vi.fn(() => Promise.resolve('x'))
      renderHook(() => useLoad(() => loader(), [], 'failed'))
      await tick(0)

      await tick(60_000)
      expect(loader).toHaveBeenCalledTimes(1)
    })
  })

  it('ignores an abort rather than reporting it as an error', () => {
    const { result, unmount } = renderHook(() =>
      useLoad(
        (signal): Promise<string> =>
          new Promise<string>((_resolve, reject) => {
            signal.addEventListener('abort', () =>
              reject(new DOMException('aborted', 'AbortError')),
            )
          }),
        [],
        'failed',
      ),
    )
    unmount()
    // An abort is teardown, not a failure the operator should be shown.
    expect(result.current.error).toBe('')
  })
})
