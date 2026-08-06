/// <reference types="vitest/config" />

import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

/*
 * Every gateway path the application calls. A path missing here is not a
 * visible failure in development: the dev server answers it with index.html,
 * so the response parses as neither JSON nor an error and the panel reading it
 * degrades to its "not reported" branch. `/health` was absent while nothing
 * called it, and the provider circuit column silently read as unknown.
 */
const gatewayProxy = {
  '/admin': process.env.GATEWAY_DEV_URL || 'http://127.0.0.1:8080',
  '/health': process.env.GATEWAY_DEV_URL || 'http://127.0.0.1:8080',
  '/livez': process.env.GATEWAY_DEV_URL || 'http://127.0.0.1:8080',
  '/readyz': process.env.GATEWAY_DEV_URL || 'http://127.0.0.1:8080',
  '/v1': process.env.GATEWAY_DEV_URL || 'http://127.0.0.1:8080',
}

function basePath(): string {
  const configured = process.env.VITE_BASE_PATH?.trim() || '/'
  if (!configured.startsWith('/') || configured.includes('?') || configured.includes('#')) {
    throw new Error('VITE_BASE_PATH must be an absolute URL path')
  }
  return configured.endsWith('/') ? configured : `${configured}/`
}

export default defineConfig({
  base: basePath(),
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  server: {
    proxy: gatewayProxy,
  },
  preview: {
    proxy: gatewayProxy,
  },
  build: {
    assetsDir: 'assets',
    emptyOutDir: true,
    outDir: 'dist',
    sourcemap: false,
    target: 'es2022',
  },
  test: {
    include: ['src/**/*.{test,spec}.{ts,tsx}'],
    environment: 'jsdom',
    setupFiles: './src/test/setup.ts',
    css: true,
    /*
     * Vitest's 5s default is sized for unit tests. These drive whole pages
     * through userEvent in jsdom, where the honest cost of one test is
     * measured in seconds: the slowest run 3.2s and 2.6s on an idle machine.
     * Against a 5s ceiling that is barely 1.5x headroom, and the suite runs 37
     * files in parallel — so under load the slowest tests crossed the line and
     * the whole suite failed at random, on a different test each run, every one
     * of them passing in isolation.
     *
     * A gate that is red at random is worse than a slow one: it teaches
     * everyone to re-run rather than read. This is a threshold sized from the
     * measured worst case, not a speedup — the tests are still slow, and making
     * them faster is worth doing separately.
     */
    testTimeout: 20_000,
    hookTimeout: 20_000,
    coverage: {
      provider: 'v8',
      reporter: ['text-summary', 'html'],
      include: ['src/**/*.{ts,tsx}'],
      /*
       * Excluded because a threshold over them measures nothing useful, not
       * because they do not matter.
       *
       * main.tsx mounts the app and vite-env.d.ts is types only, so neither has
       * behaviour a unit test can reach — main.tsx's one real branch, the
       * configuration-failure screen, is covered through config.ts instead.
       * components/ui/* is generated primitive code owned upstream; the
       * application composites that wrap it in components/ui.tsx are measured.
       */
      exclude: [
        'src/main.tsx',
        'src/vite-env.d.ts',
        'src/components/ui/**',
        'src/test/**',
        'src/**/*.{test,spec}.{ts,tsx}',
      ],
      thresholds: {
        lines: 80,
        statements: 80,
        functions: 80,
        branches: 80,
      },
    },
  },
})
