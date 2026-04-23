import { defineConfig, devices } from '@playwright/test';

// Playwright config for recording kvm-center walkthrough clips
// against the demo stack.
//
// Output shape:
//   ./output/videos/<spec-name>/*.webm   — raw Playwright recording
//   ./output/videos/<spec-name>/trace.zip — for diagnosis if a flow breaks
//   The post-run ffmpeg pass (handled by scripts/record-walkthroughs.sh)
//   converts each .webm into server/docs/media/<flow>.webp.
//
// Viewport / video size is pinned to 1440×900 — big enough for the SPA
// to render its desktop layout cleanly, small enough to produce files
// of ~500 KB per ~10 s clip after webp conversion.

export default defineConfig({
  testDir: './specs',
  timeout: 90_000,
  retries: 0,
  workers: 1,
  reporter: [['list']],
  outputDir: './output/videos',

  use: {
    baseURL: process.env.KVMC_DEMO_URL ?? 'https://localhost:8290',
    ignoreHTTPSErrors: true,
    video: {
      mode: 'on',
      size: { width: 1440, height: 900 },
    },
    viewport: { width: 1440, height: 900 },
    trace: 'retain-on-failure',
    actionTimeout: 15_000,
    navigationTimeout: 20_000,
  },

  projects: [
    {
      name: 'desktop',
      // Take the browser engine from Desktop Chrome but keep the
      // top-level viewport/video size — the device preset's default
      // 1280×720 viewport otherwise renders inside the 1440×900
      // video frame and produces grey padding.
      use: {
        ...devices['Desktop Chrome'],
        viewport: { width: 1440, height: 900 },
      },
    },
  ],
});
