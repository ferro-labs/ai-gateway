import { LogOut } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useAuth } from '../auth/AuthProvider'
import { Button, ConfirmDialog, EmptyState, LoadingState, Notice } from './ui'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { cn } from '@/lib/utils'
import { ApiError, request } from '../lib/api'
import { errorMessage, formatDateTime, formatNumber, timeAgo } from '../lib/format'
import type { DashboardSession } from '../types'

// See KeysPage.tsx for why these exist: below `sm` a row becomes a labelled
// card via `data-label` and a generated `::before`, instead of a table row.
const mobileRow = 'max-sm:mb-3 max-sm:block max-sm:rounded-lg max-sm:border max-sm:border-border max-sm:p-2.5 max-sm:last:mb-0'
const mobileCell =
  'max-sm:flex max-sm:items-baseline max-sm:justify-between max-sm:gap-3 max-sm:border-b max-sm:border-border/60 max-sm:py-1 max-sm:last:border-0 ' +
  'max-sm:before:shrink-0 max-sm:before:text-[11px] max-sm:before:font-semibold max-sm:before:tracking-wide max-sm:before:text-muted-foreground max-sm:before:uppercase max-sm:before:content-[attr(data-label)]'

/**
 * Every operator currently signed in, and the two ways to sign one out.
 *
 * Rendered by the keys page rather than given its own nav entry: a session is a
 * credential too — just a short-lived one minted from an API key — and the
 * navigation already carries eight top-level destinations. Credential
 * management belongs together.
 */
export function SessionsPanel({
  keyNames,
  reloadSignal,
  onNotice,
  onSignOut,
}: {
  /** Credential id → key name, for naming the credential a session came from. */
  keyNames: ReadonlyMap<string, string>
  /** Bumped by the page's Refresh control; re-runs the session query. */
  reloadSignal: number
  onNotice: (message: string) => void
  /**
   * Called after a sign-out lands, so the page re-runs its own queries. That is
   * what surfaces a session this browser just ended: the next request 401s.
   */
  onSignOut: () => void
}) {
  const { isAdmin } = useAuth()
  const [sessions, setSessions] = useState<DashboardSession[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [unsupported, setUnsupported] = useState(false)
  const [revokeTarget, setRevokeTarget] = useState<DashboardSession | null>(null)
  const [revokeBusy, setRevokeBusy] = useState(false)
  const [revokeError, setRevokeError] = useState('')
  const [signOutAllOpen, setSignOutAllOpen] = useState(false)
  const [signOutAllBusy, setSignOutAllBusy] = useState(false)
  const [signOutAllError, setSignOutAllError] = useState('')
  const [reload, setReload] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setError('')
    setUnsupported(false)
    request<{ data: DashboardSession[] }>('/admin/sessions', { signal: controller.signal })
      .then((body) => setSessions(body.data))
      .catch((loadError: unknown) => {
        if (loadError instanceof DOMException && loadError.name === 'AbortError') return
        // 501 means no session store is configured — a deployment choice, not
        // a fault. See LogsPage's identical treatment of its own 501.
        if (loadError instanceof ApiError && loadError.status === 501) {
          setUnsupported(true)
          return
        }
        setError(errorMessage(loadError, 'Dashboard sessions could not be loaded.'))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [reload, reloadSignal])

  const revokeOne = async () => {
    if (!revokeTarget) return
    setRevokeBusy(true)
    setRevokeError('')
    try {
      await request<void>(`/admin/sessions/${encodeURIComponent(revokeTarget.id)}`, { method: 'DELETE' })
      onNotice(`${revokeTarget.subject || 'The session'} was signed out.`)
      setRevokeTarget(null)
      setReload((value) => value + 1)
      onSignOut()
    } catch (revokeFailure) {
      // In the dialog, not the page: the dialog stays open on failure and its
      // own backdrop covers anything the page renders underneath.
      setRevokeError(errorMessage(revokeFailure, 'The dashboard session could not be signed out.'))
    } finally {
      setRevokeBusy(false)
    }
  }

  const signOutAll = async () => {
    setSignOutAllBusy(true)
    setSignOutAllError('')
    try {
      const result = await request<{ revoked: number }>('/admin/sessions', { method: 'DELETE' })
      setSignOutAllOpen(false)
      onNotice(`${formatNumber(result.revoked)} dashboard session(s) were signed out, including this one. Sign in again to continue.`)
      // This DELETE succeeds under the very session that authenticated it, but
      // every session row — this browser's included — is gone the instant it
      // returns. Refreshing is what makes that honest: the queries that follow
      // run with a dead token, 401, and the app's existing SESSION_EXPIRED_EVENT
      // listener bounces to sign-in — the same path any other expired session
      // takes. Nothing here tries to keep this page alive past that point.
      setReload((value) => value + 1)
      onSignOut()
    } catch (signOutError) {
      setSignOutAllError(errorMessage(signOutError, 'Dashboard sessions could not be signed out.'))
    } finally {
      setSignOutAllBusy(false)
    }
  }

  return (
    <section aria-labelledby="sessions-heading" className="rounded-lg border border-border bg-card">
      <div className="flex flex-wrap items-start justify-between gap-3 border-b border-border px-4 py-2.5">
        <div>
          <h2 className="text-sm font-semibold text-foreground" id="sessions-heading">Active dashboard sessions</h2>
          <p className="text-sm text-muted-foreground">Every operator currently signed in to this dashboard.</p>
        </div>
        {isAdmin && !unsupported ? (
          <Button type="button" variant="destructive" onClick={() => { setSignOutAllOpen(true); setSignOutAllError('') }}>
            <LogOut aria-hidden="true" size={17} />
            Sign out all sessions
          </Button>
        ) : null}
      </div>
      {error ? <div className="p-4"><Notice tone="error">{error}</Notice></div> : null}
      {loading && sessions.length === 0 ? (
        <div className="p-4"><LoadingState label="Loading dashboard sessions" /></div>
      ) : null}
      {unsupported ? (
        <div className="p-4">
          <EmptyState
            title="Dashboard sessions are disabled"
            description="Configure a session store on the gateway to track sign-ins and to sign every operator out at once."
          />
        </div>
      ) : null}
      {!unsupported && !loading && sessions.length === 0 && !error ? (
        <div className="p-4">
          <EmptyState title="No active sessions" description="No operator is currently signed in through a dashboard session." />
        </div>
      ) : null}
      {!unsupported && sessions.length > 0 ? (
        <Table className="max-sm:block">
          <TableHeader className="max-sm:hidden">
            <TableRow>
              <TableHead>Subject</TableHead>
              <TableHead>Credential</TableHead>
              <TableHead>Scopes</TableHead>
              <TableHead>Last seen</TableHead>
              <TableHead>Created</TableHead>
              <TableHead>Expires</TableHead>
              <TableHead><span className="sr-only">Actions</span></TableHead>
            </TableRow>
          </TableHeader>
          <TableBody className="max-sm:block">
            {sessions.map((session) => (
              <TableRow className={mobileRow} key={session.id}>
                <TableCell className={mobileCell} data-label="Subject">{session.subject || '—'}</TableCell>
                {/*
                  * The session carries a credential id; the name comes from the
                  * page of keys currently loaded. A session minted from a key on
                  * another page — or from MASTER_KEY, which has no key row at
                  * all — falls back to the id rather than rendering blank.
                  */}
                <TableCell className={mobileCell} data-label="Credential">
                  {keyNames.get(session.credential_id) ?? (
                    <code className="font-mono text-xs text-muted-foreground">{session.credential_id || '—'}</code>
                  )}
                </TableCell>
                <TableCell className={mobileCell} data-label="Scopes">{session.scopes.join(', ') || '—'}</TableCell>
                {/* Relative, not absolute: an abandoned token and an operator
                    working right now are the two things this table has to tell
                    apart, and "3d ago" says it at a glance where a date does
                    not. A session never used since it was minted reads "Never". */}
                <TableCell className={mobileCell} data-label="Last seen">{timeAgo(session.last_seen_at)}</TableCell>
                <TableCell className={mobileCell} data-label="Created">{formatDateTime(session.created_at)}</TableCell>
                <TableCell className={mobileCell} data-label="Expires">{formatDateTime(session.expires_at)}</TableCell>
                <TableCell className={cn(mobileCell, 'max-sm:flex-row max-sm:justify-end max-sm:border-0 max-sm:py-0')}>
                  {isAdmin ? (
                    <Button
                      aria-label={`Revoke session for ${session.subject || session.id}`}
                      className="text-danger hover:bg-danger-soft hover:text-danger"
                      size="sm"
                      type="button"
                      variant="ghost"
                      onClick={() => { setRevokeTarget(session); setRevokeError('') }}
                    >
                      Revoke
                    </Button>
                  ) : null}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      ) : null}

      {/*
        * Per-session revoke exists because the alternative was signing everyone
        * out: one stolen laptop should cost one session, not every operator's.
        */}
      <ConfirmDialog
        busy={revokeBusy}
        confirmLabel="Revoke session"
        dangerous
        description={
          `This ends one dashboard sign-in immediately; ${revokeTarget?.subject || 'that operator'} will need to sign in again. `
          + 'The credential itself and every other session are untouched. If this is your own session, this browser returns to the sign-in screen on its next request.'
        }
        error={revokeError}
        open={revokeTarget != null}
        title={`Revoke the session for ${revokeTarget?.subject || 'this operator'}?`}
        onCancel={() => { if (!revokeBusy) { setRevokeTarget(null); setRevokeError('') } }}
        onConfirm={() => void revokeOne()}
      />

      <ConfirmDialog
        busy={signOutAllBusy}
        confirmLabel="Sign out everyone"
        dangerous
        description="Every active dashboard session is ended immediately, including the one this browser is using right now. Every operator, this one included, will need to sign in again."
        error={signOutAllError}
        open={signOutAllOpen}
        title="Sign out all dashboard sessions?"
        onCancel={() => { if (!signOutAllBusy) { setSignOutAllOpen(false); setSignOutAllError('') } }}
        onConfirm={() => void signOutAll()}
      />
    </section>
  )
}
