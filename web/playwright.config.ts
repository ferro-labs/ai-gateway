import { defineConfig, devices } from '@playwright/test'

/*
 * The real-gateway journey, opt-in with `FERROGW_E2E_GATEWAY=1`.
 *
 * The default suite answers every gateway call from `e2e/fixture.ts`, which is
 * what makes it fast, deterministic and completely blind to the Go API moving
 * underneath it. `gateway.spec.ts` is the one journey that runs against a real
 * `cmd/ferrogw`, so a renamed field or a changed status is a failure here
 * rather than in production.
 *
 * It is opt-in rather than always-on because it needs the Go toolchain, and
 * this directory is otherwise buildable and testable with no Go at all — see
 * README.md. Nothing runs either suite in CI today.
 */
const realGateway = process.env.FERROGW_E2E_GATEWAY === '1'
const liveGateway = process.env.FERROGW_E2E_LIVE === '1'

/** Deliberately not 8080: a developer's own gateway must not be signed in to. */
export const GATEWAY_ORIGIN = 'http://127.0.0.1:8099'

/**
 * The master key the journey signs in with. A sentinel, not a secret: it only
 * ever reaches the throwaway gateway the line below starts.
 */
export const GATEWAY_MASTER_KEY = 'ferrogw-e2e-master-key'

export default defineConfig({
  testDir: './e2e',
  // The journey is the whole run or none of it: it needs a gateway process the
  // mock suite must not pay for, and the mock suite's page.route would answer
  // its calls from fixtures if the two ever shared a run.
  testMatch: realGateway
    ? liveGateway ? '**/live-gateway.spec.ts' : '**/gateway.spec.ts'
    : undefined,
  testIgnore: realGateway ? undefined : ['**/gateway.spec.ts', '**/live-gateway.spec.ts'],
  outputDir: '/tmp/ferrogw-playwright-results',
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  workers: process.env.CI ? 2 : undefined,
  // `slow-test-reporter` rides along with whichever reporter is in use: it only
  // prints a summary, and a test creeping up on its timeout is worth seeing in
  // both a local run and a CI one.
  reporter: process.env.CI
    ? [['github'], ['html', { open: 'never', outputFolder: '/tmp/ferrogw-playwright-report' }], ['./e2e/slow-test-reporter.ts']]
    : [['list'], ['./e2e/slow-test-reporter.ts']],
  use: {
    baseURL: 'http://127.0.0.1:4173',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
  },
  webServer: [{
    // Build on every run, and never reuse a running server.
    //
    // The preview server serves whatever is already in `dist/`, so reusing one
    // silently tests the previous bundle: a stylesheet or component change
    // never reaches the browser and every assertion passes against stale code.
    // That has produced both a false green and a false red in this repo, so the
    // few seconds a rebuild costs buys the guarantee that what is asserted is
    // what was written.
    // `--strictPort` so a second concurrent run fails loudly on the port
    // instead of Vite silently picking 4174 and the suite then testing whatever
    // the first run left on 4173. That produced failures that looked like
    // flakes in the tests and were really two suites sharing one server.
    command: 'npm run build && npm run preview -- --port 4173 --strictPort',
    url: 'http://127.0.0.1:4173/login',
    reuseExistingServer: false,
    timeout: 120_000,
    // Where the preview server forwards `/admin`, `/v1`, `/health` and
    // `/readyz` — `gatewayProxy` in vite.config.ts. The mock suite intercepts
    // all of those in the browser and never reaches it.
    env: { GATEWAY_DEV_URL: GATEWAY_ORIGIN },
  },
  ...(realGateway
    ? [{
      /*
       * A throwaway gateway with no remote catalog fetch. The default contract
       * journey points OpenAI at a discard port; the separately gated live
       * journey uses the real endpoint and a persistent config-history store.
       *
       * `exec` so the pid Playwright tracks is the gateway itself; without it
       * the shell is what gets signalled and the gateway outlives the run,
       * holding the port against the next one.
       */
      command: 'rm -f /tmp/ferrogw-e2e-requests.db* /tmp/ferrogw-e2e-config.db* '
        + '&& go build -o /tmp/ferrogw-e2e ./cmd/ferrogw && exec /tmp/ferrogw-e2e',
      cwd: '..',
      url: `${GATEWAY_ORIGIN}/livez`,
      reuseExistingServer: false,
      timeout: 180_000,
      env: {
        PORT: new URL(GATEWAY_ORIGIN).port,
        MASTER_KEY: GATEWAY_MASTER_KEY,
        // A minimal gateway config: one target, a guardrail, and the request
        // logger at all three stages.
        GATEWAY_CONFIG: liveGateway ? 'web/e2e/live-gateway.yaml' : 'web/e2e/gateway.yaml',
        OPENAI_API_KEY: liveGateway ? process.env.OPENAI_API_KEY ?? '' : 'e2e-not-a-real-credential',
        // Port 9 is discard. A provider call cannot leave the machine even if
        // this journey grows one.
        OPENAI_BASE_URL: liveGateway ? '' : 'http://127.0.0.1:9/v1',
        FERRO_MODEL_CATALOG_TIMEOUT: '0',
        CONFIG_STORE_BACKEND: liveGateway ? 'sqlite' : '',
        CONFIG_STORE_DSN: liveGateway ? '/tmp/ferrogw-e2e-config.db' : '',
        // The one store with no memory backend. Without it every request-log
        // read is a 501, which the browser reports as a console error and the
        // journey's "no runtime errors" assertion cannot tell from a real one.
        REQUEST_LOG_STORE_BACKEND: 'sqlite',
        REQUEST_LOG_STORE_DSN: '/tmp/ferrogw-e2e-requests.db',
      },
    }]
    : []),
  ],
  projects: [
    {
      name: 'desktop-chromium',
      use: { ...devices['Desktop Chrome'], viewport: { width: 1680, height: 945 } },
    },
    {
      name: 'mobile-chromium',
      use: { ...devices['Pixel 7'], viewport: { width: 390, height: 844 } },
    },
  ],
})
