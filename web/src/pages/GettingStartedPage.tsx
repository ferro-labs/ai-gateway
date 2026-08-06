import { Check, Circle, ExternalLink, KeyRound, Play, ScrollText, ServerCog, Sparkles } from 'lucide-react'
import { type ComponentType } from 'react'
import { Link } from 'react-router'
import { cn } from '@/lib/utils'
import { Button, LoadingState, Notice, PageHeader } from '../components/ui'
import { request } from '../lib/api'
import { useLoad } from '../lib/useLoad'
import type { DashboardSummary } from '../types'

interface Step {
  title: string
  description: string
  to: string
  action: string
  icon: ComponentType<{ size?: number; 'aria-hidden'?: boolean }>
  /**
   * Whether gateway state can confirm this step, as opposed to a browser flag
   * that says nothing about the gateway itself. `LogsPage`/`PlaygroundPage`
   * set `visitedLogs`/`madeRequest` in `localStorage` the moment the page
   * mounts — before any request happens — so with logging disabled an
   * operator could click through, see an error, and still have this page call
   * the step done. Only a verifiable step counts toward the readiness claim;
   * an unverifiable one is offered as something to try, not a checked box.
   */
  verifiable: boolean
  done: boolean
}

export default function GettingStartedPage() {
  const { data: summary, loading, error, refresh } = useLoad(
    (signal) => request<DashboardSummary>('/admin/dashboard', { signal }),
    [],
    'Setup status could not be loaded.',
  )

  const steps: Step[] = summary
    ? [
        {
          title: 'Connect a provider',
          description: 'Add at least one provider credential and confirm that its models are available.',
          to: '/providers',
          action: 'Review providers',
          icon: ServerCog,
          verifiable: true,
          done: summary.providers.available > 0,
        },
        {
          title: 'Create an API key',
          description: 'Issue a scoped key for applications that send traffic through the gateway.',
          to: '/keys',
          action: 'Manage keys',
          icon: KeyRound,
          verifiable: true,
          done: summary.keys.total > 0,
        },
        {
          title: 'Send a test request',
          description: 'Use the built-in Playground to verify routing and model output.',
          to: '/playground',
          action: 'Open Playground',
          icon: Play,
          // The gateway has no signal that distinguishes a Playground request
          // from any other traffic, so this can't be confirmed — it's a
          // suggestion, not a check.
          verifiable: false,
          done: false,
        },
        {
          title: 'Inspect request activity',
          description: 'Confirm requests, tokens, and error events in the persisted logs.',
          to: '/logs',
          action: 'View logs',
          icon: ScrollText,
          verifiable: true,
          done: summary.request_logs.total > 0,
        },
      ]
    : []
  const verifiableSteps = steps.filter((step) => step.verifiable)
  const completed = verifiableSteps.filter((step) => step.done).length
  const allVerified = verifiableSteps.length > 0 && completed === verifiableSteps.length

  return (
    <div className="flex flex-col gap-4">
      <PageHeader description="Bring your gateway online in four verifiable steps." title="Getting Started" />
      {error ? (
        <Notice tone="error">
          {error}{' '}
          <button className="font-medium underline underline-offset-2 hover:no-underline" type="button" onClick={refresh}>
            Try again
          </button>
        </Notice>
      ) : null}
      {loading && !summary ? <LoadingState label="Checking gateway setup" /> : null}
      {summary ? (
        <div className="flex flex-col gap-4">
          <section aria-labelledby="progress-title" className="flex flex-col gap-3 rounded-lg border border-border bg-card p-4 sm:flex-row sm:items-center sm:justify-between">
            <div>
              <h2 className="text-sm font-semibold text-foreground" id="progress-title">Setup progress</h2>
              <p className="text-sm text-muted-foreground">
                {allVerified
                  ? 'Every step that can be verified automatically is complete.'
                  : `${completed} of ${verifiableSteps.length} verifiable steps complete`}
              </p>
            </div>
            {/*
             * A native <progress>, because its fill is set by an attribute
             * rather than a style.
             *
             * This was two divs with the inner one's width set inline. The
             * Content-Security-Policy allows no inline styles, so in a real
             * deployment that width never applied — and an empty block element
             * with width:auto fills its parent, so the bar read fully complete
             * whatever the actual progress was. The dev server sends no policy,
             * which is why it looked right everywhere it was checked.
             */}
            <progress
              aria-label={`${completed} of ${verifiableSteps.length} verifiable steps complete`}
              className="onboarding-progress h-2 w-full sm:w-48"
              max={verifiableSteps.length || 1}
              value={completed}
            />
          </section>

          <ol className="flex flex-col gap-2">
            {steps.map((step, index) => (
              <li
                className={cn(
                  'flex flex-col gap-3 rounded-lg border border-border bg-card px-4 py-3 sm:flex-row sm:items-center',
                  step.done && 'border-success/30 bg-success-soft/30',
                )}
                key={step.title}
              >
                {/*
                 * The tick, the empty circle and the suggestion mark differ only
                 * by shape and colour, so each carries the word it stands for.
                 * Without it a screen reader gets the aggregate progress bar and
                 * no way to tell which step is the outstanding one.
                 */}
                <div className="flex shrink-0 items-center justify-center">
                  {step.done ? (
                    <Check aria-hidden="true" className="text-success" size={20} />
                  ) : step.verifiable ? (
                    <Circle aria-hidden="true" className="text-muted-foreground" size={18} />
                  ) : (
                    <Sparkles aria-hidden="true" className="text-info" size={18} />
                  )}
                  <span className="sr-only">
                    {step.done ? 'Complete' : step.verifiable ? 'Not done yet' : 'Suggested'}
                  </span>
                </div>
                <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-muted text-muted-foreground">
                  <step.icon aria-hidden={true} size={20} />
                </div>
                <div className="min-w-0 flex-1">
                  <span className="text-xs font-medium text-muted-foreground">Step {index + 1}</span>
                  <h2 className="text-sm font-semibold text-foreground">{step.title}</h2>
                  <p className="text-sm text-muted-foreground">{step.description}</p>
                  {!step.verifiable ? (
                    <p className="mt-0.5 text-xs text-muted-foreground italic">Suggested — this can't be checked automatically.</p>
                  ) : null}
                </div>
                <Button nativeButton={false} render={<Link to={step.to} />} variant="outline">{step.action}</Button>
              </li>
            ))}
          </ol>

          <section className="flex flex-col gap-3 rounded-lg border border-dashed border-border p-4 sm:flex-row sm:items-center sm:justify-between">
            <div>
              <h2 className="text-sm font-semibold text-foreground">Configure beyond the dashboard</h2>
              <p className="text-sm text-muted-foreground">
                Provider credentials, routing strategies, plugins, observability, and MCP servers remain configuration-driven.
              </p>
            </div>
            <a
              className="inline-flex shrink-0 items-center gap-1.5 text-sm font-medium text-primary hover:underline"
              href="https://docs.ferrolabs.ai"
              rel="noreferrer"
              target="_blank"
            >
              Read the documentation <ExternalLink aria-hidden="true" size={16} />
            </a>
          </section>
        </div>
      ) : null}
    </div>
  )
}
