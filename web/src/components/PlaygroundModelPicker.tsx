import { RefreshCw } from 'lucide-react'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Button, Notice } from './ui'
import { groupByOwner, type CatalogModel } from '../lib/playground'

/**
 * The model select each tab shares, with the refresh that reloads the list.
 *
 * `value` is derived by the caller from the models it currently has rather than
 * stored, so a refresh that drops the chosen model falls back to the first one
 * instead of leaving the trigger naming a model the gateway no longer serves.
 */
export function ModelPicker({
  id,
  models,
  value,
  loading,
  disabled = false,
  onChange,
  onRefresh,
}: {
  id: string
  models: readonly CatalogModel[]
  value: string
  loading: boolean
  disabled?: boolean
  onChange: (model: string) => void
  onRefresh: () => void
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id}>Model</Label>
      <div className="flex items-center gap-2">
        <div className="min-w-0 flex-1">
          <Select
            disabled={loading || disabled || models.length === 0}
            value={value}
            onValueChange={(next) => onChange(next ?? value)}
          >
            <SelectTrigger aria-label="Model" className="w-full" id={id}>
              <SelectValue placeholder={loading ? 'Loading models…' : 'Select a model'} />
            </SelectTrigger>
            {/*
             * align start + alignItemWithTrigger false drops the list BELOW the
             * trigger instead of pinning the selected item over it, so it no
             * longer overlaps the settings column. The width is freed from the
             * trigger's (--anchor-width) so a long, prefixed id like
             * meta-llama/llama-4-maverick-17b-128e-instruct is not clipped — it
             * grows to fit the content, capped so it never runs off a narrow
             * viewport.
             */}
            <SelectContent
              align="start"
              alignItemWithTrigger={false}
              className="max-h-80 w-auto min-w-[var(--anchor-width)] max-w-[min(34rem,92vw)]"
            >
              {groupByOwner(models).map(([owner, group]) => (
                <SelectGroup key={owner}>
                  <SelectLabel className="sticky top-0 z-10 bg-popover font-medium text-muted-foreground">
                    {owner}
                  </SelectLabel>
                  {group.map((model) => (
                    <SelectItem key={model.id} value={model.id}>
                      <span className="font-mono text-[13px]">{model.id}</span>
                    </SelectItem>
                  ))}
                </SelectGroup>
              ))}
            </SelectContent>
          </Select>
        </div>
        <Button aria-label="Refresh models" size="icon" type="button" variant="outline" onClick={onRefresh}>
          <RefreshCw aria-hidden="true" className={loading ? 'animate-spin' : ''} size={16} />
        </Button>
      </div>
    </div>
  )
}

/**
 * Says that a control the operator is adjusting will not reach the provider.
 *
 * Silence here is the failure this replaces: a temperature slider that moves,
 * a request that succeeds, and an answer produced at the provider's own
 * default — with nothing anywhere to say the value was discarded.
 */
export function UnsupportedParams({ provider, params }: { provider: string; params: readonly string[] }) {
  if (params.length === 0) return null
  return (
    <Notice tone="warning">
      <strong className="font-semibold">{provider}</strong> cannot express{' '}
      <code className="font-mono">{params.join(', ')}</code>. The gateway warns, drops, or rejects
      it according to{' '}
      <code className="font-mono">compatibility.on_unsupported_param</code>.
    </Notice>
  )
}
