import { test, expect } from '@playwright/test';
import { loginAsAdmin } from './_helpers';

// Docs route (v5.14+) plus the v5.35.x scroll-restart + back-to-top
// bits. Verifies the in-app docs render markdown, the sidebar
// navigates between pages, and clip media URLs resolve.

test('docs page renders sidebar tree + page HTML', async ({ page }) => {
  await loginAsAdmin(page);
  await page.goto('/docs');
  // Sidebar shows at least one top-level entry.
  await expect(page.getByRole('link', { name: /overview/i }).first()).toBeVisible();
  // Markdown content has rendered.
  await expect(page.locator('article.docs-prose')).toBeVisible();
});

test('docs media endpoint serves an embedded WebP', async ({ request }) => {
  // Bypass the SPA — hit the API route directly so we don't have to
  // shoot the page DOM. Status 200 + non-trivial body length is
  // enough to prove the embedded asset is reachable.
  const r = await request.get('/api/docs/media/01-login-dashboard.webp');
  expect(r.status()).toBe(200);
  expect(r.headers()['content-type']).toContain('image/webp');
  const body = await r.body();
  expect(body.length).toBeGreaterThan(10_000);
});
