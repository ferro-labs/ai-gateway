import { render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useAuth } from '../auth/AuthProvider'
import { ProtectedRoute } from './ProtectedRoute'

vi.mock('../auth/AuthProvider', () => ({ useAuth: vi.fn() }))

const authMock = vi.mocked(useAuth)

type AuthValue = ReturnType<typeof useAuth>

function signedIn(status: AuthValue['status']): AuthValue {
  return {
    status,
    session: null,
    isAdmin: status === 'authenticated',
    subject: status === 'authenticated' ? 'ops-laptop' : null,
    login: vi.fn(),
    logout: vi.fn(),
  }
}

/** Surfaces where /login was told to send the operator back to. */
function DestinationProbe() {
  const state: unknown = useLocation().state
  const from = (state as { from?: string } | null)?.from
  return <span data-testid="return-to">{from ?? 'nowhere'}</span>
}

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route element={<ProtectedRoute />}>
          <Route element={<h1>API keys</h1>} path="/keys" />
        </Route>
        <Route
          element={
            <>
              <h1>Sign in</h1>
              <DestinationProbe />
            </>
          }
          path="/login"
        />
      </Routes>
    </MemoryRouter>,
  )
}

beforeEach(() => {
  authMock.mockReset()
})

describe('ProtectedRoute', () => {
  it('sends a visitor with no session to the sign-in form', () => {
    authMock.mockReturnValue(signedIn('unauthenticated'))

    renderAt('/keys')

    expect(screen.getByRole('heading', { name: 'Sign in' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'API keys' })).toBeNull()
  })

  it('lets an authenticated operator reach the page they asked for', () => {
    authMock.mockReturnValue(signedIn('authenticated'))

    renderAt('/keys')

    expect(screen.getByRole('heading', { name: 'API keys' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Sign in' })).toBeNull()
  })

  /*
   * A stored session is only known good once the gateway has confirmed it, and
   * that is a round trip. Falling through to /login in the meantime would flash
   * a login form at an operator who is signed in — and, worse, teach them to
   * retype a credential at a form that appeared unprompted.
   */
  it('shows neither the page nor the login form while a stored session is still being verified', () => {
    authMock.mockReturnValue(signedIn('checking'))

    renderAt('/keys')

    expect(screen.queryByRole('heading', { name: 'Sign in' })).toBeNull()
    expect(screen.queryByRole('heading', { name: 'API keys' })).toBeNull()
    expect(screen.getByRole('status')).toHaveTextContent('Verifying session')
  })

  it('carries the intended destination to /login, so signing in resumes the interrupted page', () => {
    authMock.mockReturnValue(signedIn('unauthenticated'))

    renderAt('/keys')

    // LoginPage reads `state.from` to decide where to land after the exchange;
    // without it every expired session dumps the operator on the default page.
    expect(screen.getByTestId('return-to')).toHaveTextContent('/keys')
  })

  it('carries the query string too, so a filtered view is not silently widened on the way back', () => {
    authMock.mockReturnValue(signedIn('unauthenticated'))

    renderAt('/keys?active=false&offset=20')

    // The filters are the view. Returning to a bare /keys after re-authenticating
    // shows a different set of rows than the one the operator was reading.
    expect(screen.getByTestId('return-to')).toHaveTextContent('/keys?active=false&offset=20')
  })
})
