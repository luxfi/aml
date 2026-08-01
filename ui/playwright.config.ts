import { defineConfig, devices } from '@playwright/test'

// The bundle is built first, then served by e2e/serve.mjs with the deployed
// CSP. Nothing here relaxes the policy — see e2e/serve.mjs for why the mock API
// can live on the same origin without doing so.
// A port of this harness's own, so a stray server cannot be mistaken for it.
const PORT = 4319

export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: process.env.CI ? 'line' : 'list',
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    trace: 'retain-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: `npm run build && PORT=${PORT} node e2e/serve.mjs`,
    url: `http://127.0.0.1:${PORT}/v1/aml/health`,
    // Never reuse. A server already listening on the port is not this build —
    // it is somebody else's, and a harness that quietly tests somebody else's
    // bundle reports on the wrong thing. This cost an afternoon once.
    reuseExistingServer: false,
    timeout: 180_000,
  },
})
