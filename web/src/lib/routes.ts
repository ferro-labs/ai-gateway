/**
 * Route labels, in navigation order.
 *
 * One list feeds both the sidebar and the browser tab title. They were
 * previously separate copies of the same eight strings keyed differently — one
 * by path, one by trailing segment — so adding a page to only one of them left
 * the tab reading "Dashboard".
 *
 * Order matters: the sidebar renders these top to bottom.
 */
export const ROUTES = [
  // Observability first: what is the gateway doing, at three depths — the
  // getting-started summary, the overview, the analytics, and the traces behind
  // them — then the read-only trails (request log, audit) that record it.
  { path: '/getting-started', label: 'Getting Started' },
  { path: '/overview', label: 'Overview' },
  { path: '/analytics', label: 'Analytics' },
  { path: '/tracing', label: 'Tracing' },
  { path: '/logs', label: 'Request Logs' },
  { path: '/audit', label: 'Audit Trail' },
  // Then the request path — providers, routing, middleware — and the playground
  // that exercises it, above the configuration and credentials that govern it.
  { path: '/providers', label: 'Providers' },
  { path: '/strategy', label: 'Routing Strategies' },
  { path: '/plugins', label: 'Plugins' },
  { path: '/playground', label: 'Playground' },
  { path: '/config', label: 'Configuration' },
  { path: '/keys', label: 'API Keys' },
] as const

/** Routes outside the sidebar that still need a title. */
const UNLISTED: Record<string, string> = {
  login: 'Sign in',
}

/** The page title for a pathname, falling back for anything unrecognised. */
export function labelForPath(pathname: string): string {
  const segment = pathname.split('/').filter(Boolean).at(-1) || 'overview'
  const listed = ROUTES.find((route) => route.path === `/${segment}`)
  return listed?.label ?? UNLISTED[segment] ?? 'Dashboard'
}
