import { Check, Copy, LoaderCircle, TriangleAlert, X } from 'lucide-react'
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { cn } from '@/lib/utils'
import { formatNumber } from '../lib/format'

/**
 * Application-level composites.
 *
 * These wrap the generated primitives in `components/ui/` with the shapes this
 * dashboard actually uses, so a page states intent — "this section is empty" —
 * rather than layout. Anything specific to this product lives here; the
 * primitives are edited only where the change is a property of the whole system
 * rather than of one screen — table density, for instance.
 *
 * Focus trapping, Escape handling and focus restoration used to be hand-rolled
 * in this file and were correct. They are now the primitive's responsibility,
 * which is the point of adopting one.
 */

/*
 * Sets no bottom margin, and neither does `Notice` below. Every page lays
 * itself out as a flex column with a gap, so a margin here added to that gap
 * rather than replacing it, and the space under the title came out at twice the
 * page's own rhythm. Spacing between siblings belongs to the parent.
 */
export function PageHeader({
  title,
  description,
  actions,
}: {
  title: string
  description: string
  actions?: ReactNode
}) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
      <div className="min-w-0">
        <h1 className="text-xl font-semibold tracking-tight text-foreground">{title}</h1>
        <p className="mt-0.5 text-sm text-muted-foreground">{description}</p>
      </div>
      {actions ? <div className="flex flex-wrap items-center gap-2">{actions}</div> : null}
    </div>
  )
}

/**
 * The count that rides along in a tab label.
 *
 * A `Badge` was used for this and carried the wrong weight: badges elsewhere
 * mean status, and three of them in a tab strip read as three states rather
 * than three quantities. Fixed-width digits keep the strip from reflowing when
 * a count crosses ten.
 */
export function TabCount({ value }: { value: number }) {
  return (
    <span className="inline-flex min-w-5 items-center justify-center rounded-full bg-foreground/8 px-1.5 text-[11px] leading-5 font-semibold tabular-nums text-muted-foreground">
      {formatNumber(value)}
    </span>
  )
}

export function LoadingState({ label = 'Loading' }: { label?: string }) {
  return (
    <div
      className="flex items-center justify-center gap-3 rounded-lg border border-border bg-card p-8 text-sm text-muted-foreground"
      role="status"
    >
      <LoaderCircle aria-hidden="true" className="size-5 animate-spin" />
      <span>{label}</span>
    </div>
  )
}

export function EmptyState({
  title,
  description,
  action,
}: {
  title: string
  description: string
  action?: ReactNode
}) {
  return (
    <div className="flex flex-col items-center gap-1.5 rounded-lg border border-dashed border-border bg-card px-6 py-8 text-center">
      <h3 className="text-sm font-semibold text-foreground">{title}</h3>
      <p className="max-w-prose text-sm text-muted-foreground">{description}</p>
      {action ? <div className="mt-1.5">{action}</div> : null}
    </div>
  )
}

const NOTICE_TONES = {
  info: 'border-info/30 bg-info-soft text-info',
  success: 'border-success/30 bg-success-soft text-success',
  warning: 'border-warning/30 bg-warning-soft text-warning',
  error: 'border-danger/30 bg-danger-soft text-danger',
} as const

export function Notice({
  tone = 'info',
  children,
}: {
  tone?: keyof typeof NOTICE_TONES
  children: ReactNode
}) {
  const alerting = tone === 'error' || tone === 'warning'
  return (
    <div
      className={cn(
        'flex items-start gap-2 rounded-lg border px-3 py-2 text-sm',
        NOTICE_TONES[tone],
      )}
      // Errors interrupt; anything else is announced when the user is idle.
      role={tone === 'error' ? 'alert' : 'status'}
    >
      {alerting ? <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" /> : null}
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  )
}

const PILL_TONES = {
  success: 'border-success/30 bg-success-soft text-success',
  warning: 'border-warning/30 bg-warning-soft text-warning',
  error: 'border-danger/30 bg-danger-soft text-danger',
  info: 'border-info/30 bg-info-soft text-info',
  neutral: 'border-border bg-muted text-muted-foreground',
} as const

export function StatusPill({
  tone,
  children,
}: {
  tone: keyof typeof PILL_TONES
  children: ReactNode
}) {
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium whitespace-nowrap',
        PILL_TONES[tone],
      )}
    >
      {children}
    </span>
  )
}

export function Modal({
  open,
  title,
  description,
  onClose,
  children,
  footer,
  dismissible = true,
}: {
  open: boolean
  title: string
  description?: string
  onClose: () => void
  children: ReactNode
  footer?: ReactNode
  /**
   * Whether clicking outside or pressing Escape closes the dialog.
   *
   * Set false where the dialog holds something the operator cannot recover. A
   * newly created key is shown once — the gateway stores only its hash — so a
   * stray click beside the panel destroys the only copy and the key has to be
   * rotated again. Those dialogs demand an explicit dismissal.
   */
  dismissible?: boolean
}) {
  return (
    <Dialog
      open={open}
      onOpenChange={(next, details) => {
        if (next) return
        // Escape and an outside click are the two casual dismissals. A
        // non-dismissible dialog cancels them and keeps its explicit controls,
        // so closing stays possible but never accidental.
        if (!dismissible && (details.reason === 'escape-key' || details.reason === 'outside-press')) {
          details.cancel()
          return
        }
        onClose()
      }}
    >
      <DialogContent showCloseButton={dismissible} closeLabel="Close dialog">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {description ? <DialogDescription>{description}</DialogDescription> : null}
        </DialogHeader>
        <div className="min-w-0">{children}</div>
        {footer ? <DialogFooter>{footer}</DialogFooter> : null}
      </DialogContent>
    </Dialog>
  )
}

export function ConfirmDialog({
  open,
  title,
  description,
  confirmLabel,
  dangerous = false,
  busy = false,
  error,
  onCancel,
  onConfirm,
}: {
  open: boolean
  title: string
  description: string
  confirmLabel: string
  dangerous?: boolean
  busy?: boolean
  /**
   * A failure from the previous confirm attempt, rendered inside the dialog.
   *
   * The dialog stays open when an action fails, and a notice rendered by the
   * page sits underneath the backdrop where it cannot be read. The two most
   * likely refusals here — declining to revoke the credential the request was
   * authenticated with, and declining to remove the last admin key — therefore
   * looked exactly like the button doing nothing.
   */
  error?: string
  onCancel: () => void
  onConfirm: () => void
}) {
  return (
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        // Refuse to close mid-flight: the request is already in the air and the
        // outcome still needs somewhere to be reported.
        if (!next && !busy) onCancel()
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        {error ? (
          <p
            className="rounded-md border border-danger/30 bg-danger-soft px-3 py-2 text-sm text-danger"
            role="alert"
          >
            {error}
          </p>
        ) : null}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={busy}
            onClick={(event) => {
              // Keep the dialog mounted so a failure can be shown in place.
              event.preventDefault()
              onConfirm()
            }}
            className={cn(
              dangerous && 'bg-destructive text-destructive-foreground hover:bg-destructive/90',
            )}
          >
            {busy ? 'Working…' : confirmLabel}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

/**
 * Offset paging over a server-counted collection.
 *
 * `returned` is what the server sent for this page, not what the page chose to
 * render: a client-side search that matches nothing must not make the range
 * read "Showing 1–0", and must not remove the controls that are the only way
 * back to a page where there are matches.
 */
export function Pagination({
  offset,
  pageSize,
  returned,
  total,
  busy = false,
  note,
  onOffsetChange,
}: {
  offset: number
  pageSize: number
  returned: number
  total: number
  busy?: boolean
  /** Extra context for the current page, e.g. how many rows a search matched. */
  note?: string
  onOffsetChange: (offset: number) => void
}) {
  const first = returned === 0 ? 0 : offset + 1
  const last = offset + returned
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 border-t border-border px-3 py-2 text-sm text-muted-foreground">
      <span>
        Showing {formatNumber(first)}–{formatNumber(last)} of {formatNumber(total)}
        {note ? ` · ${note}` : ''}
      </span>
      <div className="flex items-center gap-2">
        <Button
          disabled={offset === 0 || busy}
          size="sm"
          type="button"
          variant="outline"
          onClick={() => onOffsetChange(Math.max(0, offset - pageSize))}
        >
          Previous
        </Button>
        <Button
          disabled={offset + pageSize >= total || busy}
          size="sm"
          type="button"
          variant="outline"
          onClick={() => onOffsetChange(offset + pageSize)}
        >
          Next
        </Button>
      </div>
    </div>
  )
}

const COPY_FEEDBACK_MS = 2000

/**
 * Copies a value and says whether it worked.
 *
 * The clipboard is refused outright in some contexts — a page served over plain
 * HTTP, or a browser that has not granted write permission — and the call
 * rejects rather than doing nothing visible. A button that silently fails here
 * costs an operator the one copy of a newly created key, so the outcome is
 * always rendered, and announced for a reader who cannot see the icon change.
 */
export function CopyButton({
  value,
  label,
  size = 'icon-sm',
}: {
  value: string
  /** Names what is being copied, for the accessible label. */
  label: string
  size?: 'icon-xs' | 'icon-sm' | 'icon'
}) {
  const [status, setStatus] = useState<'idle' | 'copied' | 'failed'>('idle')
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current)
  }, [])

  const copy = useCallback(async () => {
    try {
      await navigator.clipboard.writeText(value)
      setStatus('copied')
    } catch {
      setStatus('failed')
    }
    if (timer.current) clearTimeout(timer.current)
    timer.current = setTimeout(() => setStatus('idle'), COPY_FEEDBACK_MS)
  }, [value])

  const message =
    status === 'copied' ? `${label} copied` : status === 'failed' ? `Could not copy ${label}` : ''

  return (
    <>
      <Button
        aria-label={label}
        size={size}
        type="button"
        variant="ghost"
        onClick={() => void copy()}
      >
        {status === 'copied' ? (
          <Check aria-hidden="true" className="text-success" />
        ) : status === 'failed' ? (
          <X aria-hidden="true" className="text-danger" />
        ) : (
          <Copy aria-hidden="true" />
        )}
      </Button>
      <span aria-live="polite" className="sr-only">
        {message}
      </span>
    </>
  )
}

export { Button }
