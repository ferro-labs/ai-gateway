import { useCallback, useEffect, useRef, useState } from 'react'
import { errorMessage } from './format'

export interface Load<T> {
  data: T | null
  loading: boolean
  error: string
  /** Re-runs the loader. Safe to pass straight to an onClick. */
  refresh: () => void
}

export interface LoadOptions {
  /**
   * Milliseconds between background refreshes. Omitted, zero, or negative
   * disables polling entirely, which is the default and what every existing
   * caller gets.
   */
  refetchInterval?: number
  /**
   * When polling is enabled, immediately refresh stale data when a hidden tab
   * becomes visible. Defaults to true.
   */
  refetchOnWindowFocus?: boolean
}

/**
 * Runs an async loader on mount and whenever `deps` change.
 *
 * Seven pages previously repeated the same four pieces of state and the same
 * thirteen-line effect, differing only in the request and the failure message.
 * Collapsing them removes the opportunity for one copy to drift: the abort on
 * unmount, the AbortError guard, and clearing the previous error on retry all
 * happen once here instead of being re-derived per page.
 *
 * The loader receives an AbortSignal and must pass it to `request`. Aborting is
 * what stops a slow response from a page the operator has already left from
 * writing into state after unmount.
 *
 * Not every page fits: one seeds an editor buffer from its response, another
 * shares a loader between an effect and a button and shares error state with a
 * streaming request. Those keep their own effects deliberately rather than
 * being bent into this shape.
 */
export function useLoad<T>(
  loader: (signal: AbortSignal) => Promise<T>,
  deps: readonly unknown[],
  failureMessage: string,
  options?: LoadOptions,
): Load<T> {
  const [data, setData] = useState<T | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [reload, setReload] = useState(0)

  /*
   * A background run is one the operator did not ask for. It differs only in
   * what it is allowed to disturb: it never raises the spinner and never blanks
   * the error, because a table that empties and refills on every tick is
   * unreadable, and re-rendering the spinner over data that is already correct
   * reads as a fault rather than a refresh.
   */
  const background = useRef(false)
  const inFlight = useRef(false)
  const activeRequest = useRef(0)
  const lastSettledAt = useRef(Date.now())

  const refresh = useCallback(() => {
    background.current = false
    setReload((value) => value + 1)
  }, [])

  useEffect(() => {
    const quiet = background.current
    background.current = false
    const controller = new AbortController()
    const requestID = ++activeRequest.current
    inFlight.current = true
    if (!quiet) {
      setLoading(true)
      setError('')
    }
    loader(controller.signal)
      .then((result) => {
        if (controller.signal.aborted) return
        setData(result)
        // A poll that succeeds retires the banner the previous one raised.
        setError('')
      })
      .catch((loadError: unknown) => {
        // An abort is this effect being cleaned up, not a failure to report.
        if (loadError instanceof DOMException && loadError.name === 'AbortError') return
        setError(errorMessage(loadError, failureMessage))
      })
      .finally(() => {
        // A dependency change aborts the old run and immediately starts another.
        // The old promise may settle last; only the current run may mark the
        // loader idle or a poll can overlap the replacement request.
        if (activeRequest.current === requestID) {
          inFlight.current = false
          lastSettledAt.current = Date.now()
        }
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
    // `loader` is redefined on every render by every call site, so including it
    // would re-fetch continuously. The caller's `deps` describe what the loader
    // actually reads, which is the dependency that matters.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, reload])

  const refetchInterval = options?.refetchInterval ?? 0
  const refetchOnWindowFocus = options?.refetchOnWindowFocus ?? true

  useEffect(() => {
    if (refetchInterval <= 0) return
    const queueBackgroundRefresh = () => {
      // A response slower than the interval would otherwise stack requests
      // behind each other until the gateway that is already struggling fails.
      if (inFlight.current) return
      background.current = true
      setReload((value) => value + 1)
    }
    const poll = () => {
      // A tab left open overnight must not keep calling an operator's gateway.
      if (document.visibilityState === 'hidden') return
      queueBackgroundRefresh()
    }
    const onVisibilityChange = () => {
      if (
        refetchOnWindowFocus
        && document.visibilityState === 'visible'
        && Date.now() - lastSettledAt.current >= refetchInterval
      ) {
        queueBackgroundRefresh()
      }
    }
    const timer = setInterval(poll, refetchInterval)
    document.addEventListener('visibilitychange', onVisibilityChange)
    return () => {
      clearInterval(timer)
      document.removeEventListener('visibilitychange', onVisibilityChange)
    }
  }, [refetchInterval, refetchOnWindowFocus])

  return { data, loading, error, refresh }
}
