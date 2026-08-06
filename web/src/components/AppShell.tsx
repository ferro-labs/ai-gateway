import {
  Activity,
  BarChart3,
  BookOpen,
  Bot,
  Boxes,
  CircleUserRound,
  FileClock,
  Github,
  KeyRound,
  LogOut,
  Menu,
  Monitor,
  Moon,
  PanelLeft,
  Puzzle,
  Radar,
  Rocket,
  Route,
  ScrollText,
  Settings2,
  Sun,
  type LucideIcon,
} from 'lucide-react'
import { useEffect, useMemo, useState, type ComponentType } from 'react'
import { NavLink, Outlet, useLocation } from 'react-router-dom'
import { useAuth } from '../auth/AuthProvider'
import { Logo } from './Logo'
import { titleForPath } from '../lib/format'
import { ROUTES } from '../lib/routes'
import { preferredTheme, saveTheme, type Theme } from '../lib/theme'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { cn } from '@/lib/utils'

interface NavItem {
  to: string
  label: string
  icon: ComponentType<{ size?: number; 'aria-hidden'?: boolean }>
}

/**
 * Primary navigation, and the single source of the page titles.
 *
 * `titleForPath` derives the browser tab title from this list, so adding a
 * route here is sufficient. The labels used to be spelled out in two files, and
 * a page added to only one of them fell back to a generic title.
 */
const ICONS: Record<string, NavItem['icon']> = {
  '/getting-started': Rocket,
  '/overview': Boxes,
  '/keys': KeyRound,
  '/logs': FileClock,
  '/audit': ScrollText,
  '/providers': Bot,
  '/strategy': Route,
  '/plugins': Puzzle,
  '/tracing': Radar,
  '/config': Settings2,
  '/analytics': BarChart3,
  '/playground': Activity,
}

const navigation: NavItem[] = ROUTES.map((route) => ({
  to: route.path,
  label: route.label,
  icon: ICONS[route.path] ?? Boxes,
}))

/*
 * The toggle cycles through three states rather than two so an operator who
 * once picked a theme can hand the decision back to the operating system. With
 * only light and dark, the first click is irreversible: the console stops
 * following the machine's day/night switch and nothing on the screen offers a
 * way to resume.
 */
const NEXT_THEME: Record<Theme, Theme> = { system: 'light', light: 'dark', dark: 'system' }
const THEME_ICON: Record<Theme, LucideIcon> = { system: Monitor, light: Sun, dark: Moon }
const THEME_LABEL: Record<Theme, string> = {
  system: 'system',
  light: 'light',
  dark: 'dark',
}

const iconButton =
  'inline-flex size-10 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-foreground'

const sidebarLink =
  'relative flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium text-sidebar-foreground transition-colors hover:bg-secondary hover:text-foreground'

/*
 * The active item carries a brand bar in the gutter as well as a tinted fill.
 * Fill alone is a weak signal on a light sidebar — the tint has to stay pale
 * enough for text to clear AA on it, which leaves it close to the hover state.
 * The bar is unambiguous at a glance and does not depend on colour alone.
 */
const sidebarLinkActive =
  'bg-sidebar-accent font-semibold text-sidebar-accent-foreground before:absolute before:-left-2 before:h-5 before:w-[3px] before:rounded-r-full before:bg-primary before:content-[""]'

function Sidebar({
  collapsed = false,
  onCollapse,
  onNavigate,
}: {
  /** True on the desktop rail when shrunk to icons. The mobile drawer is always false. */
  collapsed?: boolean
  /** Provided only for the desktop rail; the mobile drawer closes with its own X. */
  onCollapse?: () => void
  onNavigate?: () => void
}) {
  return (
    <div className="flex h-full flex-col">
      <div
        className={cn(
          'flex h-(--spacing-topbar) shrink-0 items-center border-b border-sidebar-border text-foreground',
          collapsed ? 'justify-center px-2' : 'gap-2 px-4',
        )}
      >
        {collapsed ? (
          // The rail is too narrow for the wordmark and a separate toggle both,
          // so the brand glyph — the mark cropped to its leftmost square — stays
          // on screen and doubles as the control that expands the rail.
          onCollapse ? (
            <button
              aria-label="Expand sidebar"
              className="flex items-center justify-center rounded-md p-1 transition-colors hover:bg-sidebar-accent"
              title="Expand sidebar"
              type="button"
              onClick={onCollapse}
            >
              <span className="flex w-8 items-center overflow-hidden">
                <Logo />
              </span>
            </button>
          ) : (
            <span className="flex w-8 items-center overflow-hidden">
              <Logo />
            </span>
          )
        ) : (
          <>
            <span className="flex w-fit items-center overflow-hidden">
              <Logo />
            </span>
            {onCollapse ? (
              <button
                aria-label="Collapse sidebar"
                className={cn(iconButton, 'ml-auto size-8')}
                title="Collapse sidebar"
                type="button"
                onClick={onCollapse}
              >
                <PanelLeft aria-hidden="true" size={18} />
              </button>
            ) : null}
          </>
        )}
      </div>

      <nav aria-label="Dashboard navigation" className="flex flex-1 flex-col gap-0.5 overflow-y-auto p-3">
        {navigation.map((item) => (
          <NavLink
            aria-label={collapsed ? item.label : undefined}
            className={({ isActive }) =>
              cn(
                sidebarLink,
                collapsed && 'justify-center px-0',
                isActive && sidebarLinkActive,
                // The gutter bar has nowhere to sit when the rail is collapsed.
                isActive && collapsed && 'before:hidden',
              )
            }
            key={item.to}
            onClick={onNavigate}
            title={collapsed ? item.label : undefined}
            to={item.to}
          >
            <item.icon aria-hidden={true} size={17} />
            {collapsed ? null : <span className="truncate">{item.label}</span>}
          </NavLink>
        ))}
      </nav>

      <div className="flex flex-col gap-0.5 border-t border-sidebar-border p-3">
        <a
          aria-label={collapsed ? 'Documentation' : undefined}
          className={cn(sidebarLink, collapsed && 'justify-center px-0')}
          href="https://docs.ferrolabs.ai"
          rel="noreferrer"
          target="_blank"
          title={collapsed ? 'Documentation' : undefined}
        >
          <BookOpen aria-hidden="true" size={17} />
          {collapsed ? null : <span>Documentation</span>}
        </a>
        <a
          aria-label={collapsed ? 'GitHub' : undefined}
          className={cn(sidebarLink, collapsed && 'justify-center px-0')}
          href="https://github.com/ferro-labs/ai-gateway"
          rel="noreferrer"
          target="_blank"
          title={collapsed ? 'GitHub' : undefined}
        >
          <Github aria-hidden="true" size={17} />
          {collapsed ? null : <span>GitHub</span>}
        </a>
      </div>
    </div>
  )
}

export function AppShell() {
  const { isAdmin, logout, subject } = useAuth()
  const location = useLocation()
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [collapsed, setCollapsed] = useState(
    () => localStorage.getItem('ferrogw.dashboard.sidebar') === 'collapsed',
  )
  const [theme, setTheme] = useState<Theme>(preferredTheme)
  const ThemeIcon = THEME_ICON[theme]
  const pageTitle = useMemo(() => titleForPath(location.pathname), [location.pathname])

  useEffect(() => {
    saveTheme(theme)
  }, [theme])

  useEffect(() => {
    document.title = `${pageTitle} · Ferro AI Gateway`
    setDrawerOpen(false)
  }, [location.pathname, pageTitle])

  useEffect(() => {
    localStorage.setItem('ferrogw.dashboard.sidebar', collapsed ? 'collapsed' : 'expanded')
  }, [collapsed])

  return (
    <div className="min-h-screen bg-background">
      {/*
       * Without this, reaching page content by keyboard costs a tab stop per
       * navigation link plus the two footer links, the collapse control, theme,
       * and logout — on every page, after every navigation.
       */}
      <a
        className="sr-only focus:not-sr-only focus:fixed focus:top-3 focus:left-3 focus:z-50 focus:rounded-md focus:bg-primary focus:px-4 focus:py-2 focus:font-medium focus:text-primary-foreground"
        href="#main-content"
      >
        Skip to main content
      </a>

      {/*
       * Collapsing shrinks the rail to an icon strip rather than hiding it, so
       * the navigation stays on screen and reachable; the header toggle inside
       * flips it back. The page shifts its left padding to match.
       */}
      <aside
        className={cn(
          'fixed inset-y-0 left-0 z-30 hidden border-r border-sidebar-border bg-sidebar lg:block',
          collapsed ? 'w-16' : 'w-(--spacing-sidebar)',
        )}
      >
        <Sidebar collapsed={collapsed} onCollapse={() => setCollapsed((value) => !value)} />
      </aside>

      <div
        className={cn(
          'flex min-h-screen flex-col',
          collapsed ? 'lg:pl-16' : 'lg:pl-(--spacing-sidebar)',
        )}
      >
        <header className="sticky top-0 z-20 flex h-(--spacing-topbar) items-center gap-3 border-b border-border bg-card px-4">
          <button
            aria-expanded={drawerOpen}
            aria-label="Open navigation"
            className={cn(iconButton, 'lg:hidden')}
            type="button"
            onClick={() => setDrawerOpen(true)}
          >
            <Menu aria-hidden="true" size={22} />
          </button>

          <span className="flex items-center lg:hidden">
            <Logo />
          </span>

          <div className="ml-auto flex items-center gap-2">
            <button
              aria-label={`Theme: ${THEME_LABEL[theme]}. Switch to ${THEME_LABEL[NEXT_THEME[theme]]}.`}
              className={iconButton}
              title={`Theme: ${THEME_LABEL[theme]}`}
              type="button"
              onClick={() => setTheme((value) => NEXT_THEME[value])}
            >
              <ThemeIcon aria-hidden="true" size={19} />
            </button>

            {/*
             * Identity and permission are two different questions, so they get
             * two pills. "Admin" alone answers what this session may do and
             * says nothing about which credential is doing it — the one detail
             * that matters when several operators share a screen during an
             * incident, and the one the audit trail records.
             */}
            {subject ? (
              <span className="hidden max-w-45 items-center gap-1.5 rounded-full border border-border px-3 py-1 text-sm text-foreground sm:inline-flex">
                <CircleUserRound aria-hidden="true" size={18} />
                <span className="truncate">{subject}</span>
              </span>
            ) : null}

            <span className="hidden items-center gap-1.5 rounded-full border border-border px-3 py-1 text-sm text-muted-foreground sm:inline-flex">
              {isAdmin ? 'Admin' : 'Read only'}
            </span>

            <button
              aria-label="Logout"
              className="inline-flex items-center gap-2 rounded-md border border-border px-3 py-2 text-sm transition-colors hover:bg-accent"
              type="button"
              onClick={logout}
            >
              <LogOut aria-hidden="true" size={18} />
              <span className="hidden sm:inline">Logout</span>
            </button>
          </div>
        </header>

        <main className="flex-1 p-4 sm:p-5" id="main-content">
          <Outlet />
        </main>
      </div>

      <Dialog open={drawerOpen} onOpenChange={setDrawerOpen}>
        <DialogContent
          className="top-0 left-0 h-full w-(--spacing-sidebar) max-w-[85vw] translate-x-0 translate-y-0 gap-0 overflow-y-auto rounded-none bg-sidebar p-0 ring-0 sm:max-w-[85vw]"
          closeLabel="Close navigation"
        >
          <DialogTitle className="sr-only">Mobile navigation</DialogTitle>
          <Sidebar onNavigate={() => setDrawerOpen(false)} />
        </DialogContent>
      </Dialog>
    </div>
  )
}
