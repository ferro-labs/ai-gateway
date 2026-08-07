import { ArrowRight, Eye, EyeOff, Github, KeyRound } from 'lucide-react'
import { useEffect, useState, type FormEvent } from 'react'
import { Navigate, useLocation } from 'react-router'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useAuth } from '../auth/AuthProvider'
import { DiscordIcon, XIcon } from '../components/BrandIcons'
import { Logo } from '../components/Logo'
import { Button } from '../components/ui'
import { errorMessage } from '../lib/format'

interface LocationState {
  from?: string
}

function safeDestination(value: unknown): string {
  return typeof value === 'string' && value.startsWith('/') && !value.startsWith('//') ? value : '/overview'
}

const SOCIAL_LINKS = [
  { href: 'https://github.com/ferro-labs/ai-gateway', icon: <Github size={16} />, label: 'GitHub' },
  { href: 'https://discord.gg/yCAeYvJeDV', icon: <DiscordIcon />, label: 'Discord' },
  { href: 'https://x.com/ferroLabsAI', icon: <XIcon />, label: '@ferroLabsAI' },
]

export default function LoginPage() {
  const { login, status } = useAuth()
  const location = useLocation()
  const [token, setToken] = useState('')
  const [visible, setVisible] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    document.title = 'Sign in · Ferro AI Gateway'
  }, [])

  /*
   * This is the only navigation away from the sign-in page, and it covers both
   * an operator who is already signed in and one who just signed in: `login()`
   * flips the status before the submit handler resumes, so React re-renders
   * here first and any `navigate()` in the handler would lose the race. Routing
   * the destination through this single return is what keeps `safeDestination`
   * on the live path rather than on a branch that never runs.
   */
  if (status === 'authenticated') {
    return <Navigate replace to={safeDestination((location.state as LocationState | null)?.from)} />
  }

  const onSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!token.trim()) {
      setError('Enter an administrator or read-only API key.')
      return
    }
    setSubmitting(true)
    setError('')
    try {
      await login({ token })
    } catch (loginError) {
      setError(errorMessage(loginError, 'The gateway could not verify this key.'))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    /*
     * The faint grid is drawn with two repeating gradients rather than an image.
     * The policy allows no external origins, and a data-URI background would be
     * a second inline-style exception to justify for decoration alone.
     */
    <main className="relative flex min-h-screen flex-col items-center justify-center gap-8 overflow-hidden bg-background px-4 py-12">
      <div
        aria-hidden="true"
        className="pointer-events-none absolute inset-0 opacity-[0.45] [background-image:linear-gradient(to_right,var(--border)_1px,transparent_1px),linear-gradient(to_bottom,var(--border)_1px,transparent_1px)] [background-size:56px_56px] [mask-image:radial-gradient(ellipse_at_center,black,transparent_75%)]"
      />

      <div className="relative flex flex-col items-center gap-3">
        <Logo size="lg" />
        <p className="text-center text-xs font-semibold tracking-[0.18em] text-muted-foreground uppercase">
          Gateway console access
        </p>
      </div>

      <section
        aria-labelledby="login-title"
        className="relative w-full max-w-md rounded-xl border border-border bg-card p-6 shadow-card sm:p-8"
      >
        <p className="text-center text-xs font-semibold tracking-[0.18em] text-muted-foreground uppercase">
          Sign in
        </p>
        <h1 className="mt-2 text-center text-2xl font-semibold tracking-tight text-foreground" id="login-title">
          Welcome back
        </h1>
        <p className="mx-auto mt-3 max-w-sm text-center text-sm text-muted-foreground">
          Use a gateway admin key or a read-only key. It is exchanged for a session token that expires,
          and is never itself kept in the browser.
        </p>

        <form className="mt-6 flex flex-col gap-4" onSubmit={(event) => void onSubmit(event)}>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="gateway-key">Gateway key</Label>
            <div className="relative">
              <KeyRound aria-hidden="true" className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                autoComplete="current-password"
                autoFocus
                className="pr-9 pl-8"
                id="gateway-key"
                name="gateway-key"
                placeholder="fgw_…"
                spellCheck={false}
                type={visible ? 'text' : 'password'}
                value={token}
                onChange={(event) => setToken(event.target.value)}
              />
              <button
                aria-label={visible ? 'Hide key' : 'Show key'}
                className="absolute top-1/2 right-1.5 inline-flex size-6 -translate-y-1/2 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
                type="button"
                onClick={() => setVisible((value) => !value)}
              >
                {visible ? <EyeOff aria-hidden="true" size={16} /> : <Eye aria-hidden="true" size={16} />}
              </button>
            </div>
          </div>

          {error ? (
            <p className="rounded-md border border-danger/30 bg-danger-soft px-3 py-2 text-sm text-danger" role="alert">
              {error}
            </p>
          ) : null}

          <Button className="w-full" disabled={submitting} size="lg" type="submit">
            {submitting ? 'Verifying…' : 'Continue'}
            {submitting ? null : <ArrowRight aria-hidden="true" size={16} />}
          </Button>
        </form>

        <p className="mt-6 border-t border-border pt-5 text-center text-xs leading-relaxed text-muted-foreground">
          Keys are issued per operator from the API Keys page. Run{' '}
          <code className="rounded bg-secondary px-1 py-0.5 font-mono">ferrogw init</code> on the gateway
          host to generate the first one.
        </p>
      </section>

      <div className="relative flex flex-col items-center gap-3">
        <p className="text-xs font-semibold tracking-[0.18em] text-muted-foreground uppercase">Follow us on</p>
        <ul className="flex flex-wrap justify-center gap-2">
          {SOCIAL_LINKS.map((link) => (
            <li key={link.label}>
              <a
                className="inline-flex items-center gap-2 rounded-md border border-border bg-card px-3 py-2 text-sm font-medium text-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
                href={link.href}
                rel="noreferrer"
                target="_blank"
              >
                {link.icon}
                {link.label}
              </a>
            </li>
          ))}
        </ul>
      </div>
    </main>
  )
}
