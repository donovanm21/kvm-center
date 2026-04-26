import { defineConfig, devices } from '@playwright/test';

// Playwright Tier 2 config: assertion-driven SPA tests.
//
// Differences from demo/walkthroughs/playwright.config.ts:
//   - No video by default (only on first retry).
//   - Trace on first retry so a failing CI run gives us a debug
//     timeline without bloating successful runs.
//   - Tighter per-test timeout (45 s vs 90 s) because these are
//     assertion-driven, not slow-paced demo recordings.
//   - Two retries — flaky-test guard for the noisy demo backend.

export default defineConfig({
  testDir: './specs',
  timeout: 45_000,
  expect: { timeout: 8_000 },
  retries: 2,
  workers: 1, // shared demo backend; one test at a time keeps state clean
  reporter: [['list']],
  outputDir: './output',
  use: {
    baseURL: process.env.KVMC_DEMO_URL ?? 'https://localhost:8290',
    ignoreHTTPSErrors: true,
    video: 'retain-on-failure',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    actionTimeout: 12_000,
    navigationTimeout: 20_000,
  },
  projects: [
    {
      name: 'chromium',
      use: {
        ...devices['Desktop Chrome'],
        viewport: { width: 1440, height: 900 },
      },
    },
  ],
});
