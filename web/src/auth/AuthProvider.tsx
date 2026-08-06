import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import {
  clearSession,
  endSession,
  exchangeCredentialForSession,
  loadSession,
  requestWithToken,
  saveSession,
  SCOPE_PROBE_PATH,
  SCOPES_STALE_EVENT,
  SESSION_EXPIRED_EVENT,
} from '../lib/api'
import { resetPluginCatalog } from '../lib/plugins'
import type { AdminHealth, Scope, Session } from '../types'

type AuthStatus = 'checking' | 'authenticated' | 'unauthenticated'

interface LoginInput {
  token: string
}

interface AuthValue {
  status: AuthStatus
  session: Session | null
  isAdmin: boolean
  /**
   * Who is signed in — the display name of the exchanged credential. Null on a
   * session restored from an earlier build, which stored only the token.
   */
  subject: string | null
  login: (input: LoginInput) => Promise<void>
  logout: () => Promise<void>
}

const AuthContext = createContext<AuthValue | null>(null)

/**
 * Scopes as reported by the gateway, defaulting to none.
 *
 * A response that omits scopes used to be read as full admin. The server is
 * authoritative either way — it refuses what the credential cannot do — so the
 * effect was cosmetic, but a fail-open default sitting in the authentication
 * path is the wrong shape. Showing too few controls is recoverable by signing
 * in again; showing controls that then fail is not explicable to the operator.
 */
function normalizeScopes(scopes: Scope[] | undefined): Scope[] {
  return scopes ?? []
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [session, setSession] = useState<Session | null>(() => loadSession())
  const [status, setStatus] = useState<AuthStatus>(() => (session ? 'checking' : 'unauthenticated'))

  // A 401 already means the server rejected this token — clearing locally,
  // with no further network call, avoids re-triggering the same 401 (and thus
  // this same event) via a doomed-to-fail DELETE with the token that just failed.
  //
  // The plugin catalog is discarded on the way out, here and in logout. It is
  // cached at module scope for the lifetime of the tab, so without this the next
  // operator to sign in on the same tab inherits the previous session's copy —
  // and on a shared operations workstation that is the ordinary case, not an
  // edge one.
  const forceLogout = useCallback(() => {
    clearSession()
    resetPluginCatalog()
    setSession(null)
    setStatus('unauthenticated')
  }, [])

  const logout = useCallback(async () => {
    await endSession()
    resetPluginCatalog()
    setSession(null)
    setStatus('unauthenticated')
  }, [])

  /**
   * Re-reads the scopes the gateway currently grants this session.
   *
   * `isAdmin` is derived from a snapshot taken once at sign-in, so a credential
   * de-scoped mid-session keeps showing controls that then answer 403. The 403
   * itself is the signal, and this is the response to it: read the scopes again
   * and let the controls disappear.
   *
   * The stored session is read rather than closed over, so this callback stays
   * stable and can never act on a stale token. A failed re-check changes
   * nothing — it is not evidence the scopes changed, and a 401 signs the tab
   * out through its own event.
   */
  const refreshScopes = useCallback(() => {
    const current = loadSession()
    if (!current) return
    requestWithToken<AdminHealth>(SCOPE_PROBE_PATH, current.token)
      .then((health) => {
        const verified = { ...current, scopes: normalizeScopes(health.scopes) }
        saveSession(verified)
        setSession(verified)
      })
      .catch(() => {})
  }, [])

  const login = useCallback(async ({ token }: LoginInput) => {
    // The credential is sent once, here, to be exchanged — it is never itself saved.
    const nextSession = await exchangeCredentialForSession(token.trim())
    saveSession(nextSession)
    setSession(nextSession)
    setStatus('authenticated')
  }, [])

  useEffect(() => {
    window.addEventListener(SESSION_EXPIRED_EVENT, forceLogout)
    window.addEventListener(SCOPES_STALE_EVENT, refreshScopes)
    return () => {
      window.removeEventListener(SESSION_EXPIRED_EVENT, forceLogout)
      window.removeEventListener(SCOPES_STALE_EVENT, refreshScopes)
    }
  }, [forceLogout, refreshScopes])

  useEffect(() => {
    if (!session || status !== 'checking') return
    const controller = new AbortController()
    requestWithToken<AdminHealth>('/admin/health', session.token, { signal: controller.signal })
      .then((health) => {
        const verified = { ...session, scopes: normalizeScopes(health.scopes) }
        saveSession(verified)
        setSession(verified)
        setStatus('authenticated')
      })
      .catch((error: unknown) => {
        if (error instanceof DOMException && error.name === 'AbortError') return
        forceLogout()
      })
    return () => controller.abort()
  }, [forceLogout, session, status])

  const value = useMemo<AuthValue>(
    () => ({
      status,
      session,
      isAdmin: session?.scopes.includes('admin') ?? false,
      subject: session?.subject || null,
      login,
      logout,
    }),
    [login, logout, session, status],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

// eslint-disable-next-line react-refresh/only-export-components
export function useAuth(): AuthValue {
  const value = useContext(AuthContext)
  if (!value) throw new Error('useAuth must be used inside AuthProvider')
  return value
}
