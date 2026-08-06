import { BookOpen } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { cn } from '@/lib/utils'
import { CopyButton, EmptyState, Notice, PageHeader } from '../components/ui'

/**
 * How to export the gateway's OpenTelemetry traces to a backend.
 *
 * Every routed request emits a `gateway.request` span over OTLP. This page names
 * three backends the export is commonly pointed at and shows the exact
 * `config.yaml` for each — it configures nothing itself, so it reads from no
 * endpoint and needs no permission beyond a signed-in session.
 *
 * The full reference lives in observability/README.md; the header links to it.
 */

const OBSERVABILITY_DOCS =
  'https://github.com/ferro-labs/ai-gateway/blob/main/observability/README.md'

/** A vendored single-path brand mark, tinted with the surrounding text colour. */
function Mark({ viewBox, d }: { viewBox: string; d: string }) {
  return (
    <svg aria-hidden="true" className="size-6" fill="currentColor" viewBox={viewBox}>
      <path d={d} />
    </svg>
  )
}

interface Backend {
  id: string
  name: string
  /** The thumbnail mark shown in the card and the config panel. */
  mark: ReactNode
  tagline: string
  summary: string
  endpoint: string
  protocol: string
  /** Human labels for the auth headers, empty for a backend that needs none. */
  auth: string[]
  freeTier: string
  /** Environment variables the ${...} references below resolve from. */
  envVars: string[]
  /** The observability.tracing block to paste into config.yaml. */
  config: string
  /** Optional collector to run first (Jaeger), with its UI URL. */
  collector?: { command: string; ui: string }
  verify: string
}

const BACKENDS: Backend[] = [
  {
    id: 'newrelic',
    name: 'New Relic',
    mark: (
      <Mark
        viewBox="0 0 24 24"
        d="M8.0015 14.3091v7.384L12.0008 24V12.0008L1.6078 5.9996v4.6167ZM12.0008 0 2.8232 5.2976 6.8209 7.606l5.1799-2.9893 6.3936 3.6913v7.384l-5.1783 2.9908v4.6167l9.176-5.2991V5.9996Z"
      />
    ),
    tagline: 'Managed APM — OTLP over HTTP',
    summary: 'Send spans to New Relic over OTLP/HTTP, authenticated with an ingest license key.',
    endpoint: 'https://otlp.nr-data.net',
    protocol: 'http/protobuf',
    auth: ['api-key: <ingest license key>'],
    freeTier: 'Free tier includes 100 GB/month of ingest.',
    envVars: ['NEW_RELIC_LICENSE_KEY'],
    config: `observability:
  tracing:
    enabled: true
    endpoint: https://otlp.nr-data.net   # EU account: https://otlp.eu01.nr-data.net
    protocol: http/protobuf
    service_name: ferrogw
    headers:
      api-key: \${NEW_RELIC_LICENSE_KEY}`,
    verify:
      'Find spans under APM & Services → your service → Distributed tracing. The credential must be an INGEST · LICENSE key — a User key is refused.',
  },
  {
    id: 'langsmith',
    name: 'LangSmith',
    mark: (
      <Mark
        viewBox="0 0 24 24"
        d="M13.796 0a6.93 6.93 0 0 0-4.91 2.019L5.451 5.455l3.273 3.27 3.432-3.432a2.284 2.284 0 0 1 3.277 0 2.28 2.28 0 0 1 0 3.275L12 12.001l3.273 3.273 3.433-3.435c2.692-2.692 2.692-7.127 0-9.82A6.92 6.92 0 0 0 13.796 0m-5.07 8.728-3.433 3.434c-2.692 2.693-2.692 7.126 0 9.819A6.92 6.92 0 0 0 10.203 24a6.93 6.93 0 0 0 4.911-2.02l3.432-3.432-3.271-3.272-3.433 3.433a2.284 2.284 0 0 1-3.277 0 2.28 2.28 0 0 1 0-3.276L12 12z"
      />
    ),
    tagline: 'LLM tracing — OTLP over HTTP',
    summary: 'Send spans to LangSmith over OTLP/HTTP; the traces path is appended automatically.',
    endpoint: 'https://api.smith.langchain.com/otel',
    protocol: 'http/protobuf',
    auth: ['x-api-key: <LangSmith API key>', 'Langsmith-Project: <project name>'],
    freeTier: 'Free Developer tier includes 5,000 traces/month.',
    envVars: ['LANGSMITH_API_KEY'],
    config: `observability:
  tracing:
    enabled: true
    endpoint: https://api.smith.langchain.com/otel
    protocol: http/protobuf
    service_name: ferrogw
    headers:
      x-api-key: \${LANGSMITH_API_KEY}
      Langsmith-Project: ferrogw`,
    verify: 'Open the LangSmith project named in the Langsmith-Project header to see the traces.',
  },
  {
    id: 'jaeger',
    name: 'Jaeger',
    mark: (
      <span aria-hidden="true" className="text-lg font-bold tracking-tight">
        J
      </span>
    ),
    tagline: 'Self-hosted — OTLP over gRPC',
    summary: 'Run Jaeger locally and export over plaintext OTLP/gRPC — no credentials, nothing billed.',
    endpoint: 'localhost:4317',
    protocol: 'grpc',
    auth: [],
    freeTier: 'Open source; runs anywhere Docker does.',
    envVars: [],
    config: `observability:
  tracing:
    enabled: true
    endpoint: localhost:4317
    protocol: grpc
    service_name: ferrogw`,
    collector: {
      command:
        'docker run -d --name jaeger -p 16686:16686 -p 4317:4317 jaegertracing/all-in-one:latest',
      ui: 'http://localhost:16686',
    },
    verify: 'Send a request, then open the Jaeger UI and search for the service ferrogw.',
  },
]

function CodeBlock({ code, label }: { code: string; label: string }) {
  return (
    <div className="relative">
      <pre className="overflow-x-auto rounded-lg border border-border bg-muted/40 p-3 pr-12 text-xs leading-relaxed text-foreground">
        <code>{code}</code>
      </pre>
      <div className="absolute top-1.5 right-1.5">
        <CopyButton label={label} size="icon-sm" value={code} />
      </div>
    </div>
  )
}

function Fact({ term, children }: { term: string; children: ReactNode }) {
  return (
    <div className="rounded-md border border-border bg-muted/30 px-3 py-2">
      <dt className="text-xs font-medium text-muted-foreground">{term}</dt>
      <dd className="mt-0.5 text-sm break-words text-foreground">{children}</dd>
    </div>
  )
}

function BackendConfig({ backend }: { backend: Backend }) {
  return (
    <div className="flex flex-col gap-4 rounded-lg border border-border bg-card p-4">
      <div className="flex items-center gap-3">
        <span className="flex size-9 shrink-0 items-center justify-center rounded-md bg-muted text-foreground">
          {backend.mark}
        </span>
        <div className="min-w-0">
          <h2 className="text-base font-semibold text-foreground">{backend.name}</h2>
          <p className="text-sm text-muted-foreground">{backend.summary}</p>
        </div>
      </div>

      <dl className="grid gap-2 sm:grid-cols-2">
        <Fact term="Endpoint">
          <code>{backend.endpoint}</code>
        </Fact>
        <Fact term="Protocol">
          <code>{backend.protocol}</code>
        </Fact>
        <Fact term="Authentication">
          {backend.auth.length === 0 ? (
            'None — local collector'
          ) : (
            <ul className="flex flex-col gap-0.5">
              {backend.auth.map((header) => (
                <li key={header}>
                  <code>{header}</code>
                </li>
              ))}
            </ul>
          )}
        </Fact>
        <Fact term="Cost">{backend.freeTier}</Fact>
      </dl>

      {backend.collector ? (
        <div className="flex flex-col gap-2">
          <h3 className="text-sm font-semibold text-foreground">1. Start the collector</h3>
          <CodeBlock code={backend.collector.command} label="Jaeger docker command" />
          <p className="text-sm text-muted-foreground">
            The UI is at{' '}
            <a
              className="font-medium text-primary hover:underline"
              href={backend.collector.ui}
              rel="noreferrer"
              target="_blank"
            >
              {backend.collector.ui}
            </a>
            .
          </p>
        </div>
      ) : null}

      <div className="flex flex-col gap-2">
        <h3 className="text-sm font-semibold text-foreground">
          {backend.collector ? '2. ' : '1. '}Point the gateway at {backend.name}
        </h3>
        <p className="text-sm text-muted-foreground">
          Add this to <code>config.yaml</code>, then restart the gateway.
        </p>
        <CodeBlock code={backend.config} label={`${backend.name} config`} />
      </div>

      {backend.envVars.length > 0 ? (
        <div className="flex flex-col gap-1.5">
          <h3 className="text-sm font-semibold text-foreground">
            {backend.collector ? '3. ' : '2. '}Provide the credential
          </h3>
          <p className="text-sm text-muted-foreground">
            The <code>{'${...}'}</code> reference is resolved from the environment when the
            exporter is built, so the secret is never written to the config file or the config
            store. Set:
          </p>
          <ul className="flex flex-wrap gap-2">
            {backend.envVars.map((name) => (
              <li
                key={name}
                className="rounded-md border border-border bg-muted/40 px-2 py-1 font-mono text-xs text-foreground"
              >
                {name}
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      <Notice tone="info">{backend.verify}</Notice>
    </div>
  )
}

export default function TracingPage() {
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const selected = BACKENDS.find((backend) => backend.id === selectedId) ?? null

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        actions={
          <a
            className="inline-flex items-center gap-2 rounded-md border border-border px-3 py-2 text-sm font-medium transition-colors hover:bg-accent"
            href={OBSERVABILITY_DOCS}
            rel="noreferrer"
            target="_blank"
          >
            <BookOpen aria-hidden="true" size={16} />
            Tracing reference
          </a>
        }
        description="Export the gateway's OpenTelemetry traces to an observability backend. Pick one to see its configuration."
        title="Tracing"
      />

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {BACKENDS.map((backend) => {
          const active = backend.id === selectedId
          return (
            <button
              key={backend.id}
              aria-pressed={active}
              className={cn(
                'flex items-center gap-3 rounded-lg border p-4 text-left transition-colors',
                active ? 'border-primary bg-accent' : 'border-border bg-card hover:bg-accent',
              )}
              type="button"
              onClick={() => setSelectedId(backend.id)}
            >
              <span className="flex size-11 shrink-0 items-center justify-center rounded-md bg-muted text-foreground">
                {backend.mark}
              </span>
              <span className="min-w-0">
                <span className="block font-semibold text-foreground">{backend.name}</span>
                <span className="block text-sm text-muted-foreground">{backend.tagline}</span>
              </span>
            </button>
          )
        })}
      </div>

      {selected ? (
        <BackendConfig backend={selected} />
      ) : (
        <EmptyState
          description="Select New Relic, LangSmith, or Jaeger above to see the exact config.yaml and credentials for that backend."
          title="Choose a backend"
        />
      )}
    </div>
  )
}
