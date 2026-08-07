import { lazy, Suspense } from 'react'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router'
import { AuthProvider } from './auth/AuthProvider'
import { AppShell } from './components/AppShell'
import { ErrorBoundary } from './components/ErrorBoundary'
import { ProtectedRoute } from './components/ProtectedRoute'
import { LoadingState } from './components/ui'

const LoginPage = lazy(() => import('./pages/LoginPage'))
const GettingStartedPage = lazy(() => import('./pages/GettingStartedPage'))
const OverviewPage = lazy(() => import('./pages/OverviewPage'))
const KeysPage = lazy(() => import('./pages/KeysPage'))
const LogsPage = lazy(() => import('./pages/LogsPage'))
const AuditPage = lazy(() => import('./pages/AuditPage'))
const ProvidersPage = lazy(() => import('./pages/ProvidersPage'))
const StrategyPage = lazy(() => import('./pages/StrategyPage'))
const PluginsPage = lazy(() => import('./pages/PluginsPage'))
const TracingPage = lazy(() => import('./pages/TracingPage'))
const ConfigPage = lazy(() => import('./pages/ConfigPage'))
const AnalyticsPage = lazy(() => import('./pages/AnalyticsPage'))
const PlaygroundPage = lazy(() => import('./pages/PlaygroundPage'))

function RouteFallback() {
  return <div className="route-loading"><LoadingState label="Loading dashboard" /></div>
}

const routerBase = import.meta.env.BASE_URL === '/'
  ? undefined
  : import.meta.env.BASE_URL.replace(/\/$/, '')

export default function App() {
  return (
    <ErrorBoundary>
      <BrowserRouter basename={routerBase}>
        <AuthProvider>
          <Suspense fallback={<RouteFallback />}>
            <Routes>
              <Route path="/login" element={<LoginPage />} />
              <Route element={<ProtectedRoute />}>
                <Route element={<AppShell />}>
                  <Route index element={<Navigate replace to="/getting-started" />} />
                  <Route path="/getting-started" element={<GettingStartedPage />} />
                  <Route path="/overview" element={<OverviewPage />} />
                  <Route path="/keys" element={<KeysPage />} />
                  <Route path="/logs" element={<LogsPage />} />
                  <Route path="/audit" element={<AuditPage />} />
                  <Route path="/providers" element={<ProvidersPage />} />
                  <Route path="/strategy" element={<StrategyPage />} />
                  <Route path="/plugins" element={<PluginsPage />} />
                  <Route path="/tracing" element={<TracingPage />} />
                  <Route path="/config" element={<ConfigPage />} />
                  <Route path="/analytics" element={<AnalyticsPage />} />
                  <Route path="/playground" element={<PlaygroundPage />} />
                </Route>
              </Route>
              <Route path="*" element={<Navigate replace to="/overview" />} />
            </Routes>
          </Suspense>
        </AuthProvider>
      </BrowserRouter>
    </ErrorBoundary>
  )
}
