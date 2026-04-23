import { test } from '@playwright/test';
import { installCursor, login, slowClick } from './_helpers';

// Flow 03 — offline migration with progress arc.
// Output: server/docs/media/03-live-migration.webp
//
// Uses the "Offline (Recommended)" mode because only that path
// surfaces the /migrate-status polling arc in demo. The watch
// window is kept short (≈45 s of animation) so the final clip stays
// under a few MB after ffmpeg compresses the static tail.

test('03-live-migration', async ({ page }) => {
  test.setTimeout(300_000);
  await installCursor(page);
  await login(page);

  await page.goto('/vms');
  await page.waitForLoadState('domcontentloaded');
  await page.locator('table.data-table tbody tr').first().waitFor({ state: 'visible', timeout: 30_000 });
  await page.waitForTimeout(1500);

  const runningRow = page.locator('tr', { has: page.locator('.state-pill.running') }).first();
  await runningRow.waitFor({ state: 'visible', timeout: 30_000 });
  await slowClick(page, runningRow, { pre: 900, post: 2500 });
  await page.waitForURL('**/vms/**');
  await page.waitForTimeout(1500);

  // Actions → Migrate.
  await slowClick(page, page.getByRole('button', { name: /^actions/i }), { pre: 600, post: 900 });
  await slowClick(page, page.getByRole('menuitem', { name: /migrate/i }), { pre: 900, post: 1800 });

  // Pick first selectable destination.
  const hostSelect = page.locator('select').first();
  const opts = await hostSelect.locator('option:not([disabled])').all();
  for (const o of opts) {
    const v = await o.getAttribute('value');
    if (v) { await hostSelect.selectOption(v); break; }
  }
  await page.waitForTimeout(1500);

  await slowClick(page, page.getByRole('button', { name: /shut down & migrate/i }), { pre: 800, post: 0 });

  // Watch the progress bar for ~45s — enough to show meaningful arc
  // without bloating the clip.
  await page.waitForTimeout(45_000);

  // Close on the Migrations page.
  await page.goto('/migrations');
  await page.waitForLoadState('domcontentloaded');
  await page.waitForTimeout(3000);
});
