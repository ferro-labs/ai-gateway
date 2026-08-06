import { Navigate, Outlet, useLocation } from 'react-router-dom'
import { useAuth } from '../auth/AuthProvider'
import { LoadingState } from './ui'

export function ProtectedRoute() {
  const { status } = useAuth()
  const location = useLocation()

  if (status === 'checking') {
    return (
      <main className="session-check">
        <LoadingState label="Verifying session" />
      </main>
    )
  }

  if (status !== 'authenticated') {
    // Search and hash carry the view, not just the page: an expired session on
    // /logs?provider=openai&hours=1 has to come back to that filtered view, not
    // to an unfiltered /logs in the middle of an incident.
    const from = `${location.pathname}${location.search}${location.hash}`
    return <Navigate replace state={{ from }} to="/login" />
  }

  return <Outlet />
}
