import { test } from '@playwright/test';
import { installCursor, login, slowClick } from './_helpers';

// Flow 02 — VM lifecycle (Shutdown → Start → Snapshot).
// Output: server/docs/media/02-vm-lifecycle.webp
//
// Uses installCursor so the viewer can track the pointer, and
// slowClick for a consistent hover → pause → click cadence around
// menu interactions.

test('02-vm-lifecycle', async ({ page }) => {
  test.setTimeout(180_000);
  await installCursor(page);
  await login(page);

  await page.goto('/vms');
  await page.waitForLoadState('domcontentloaded');
  await page.waitForTimeout(2000);

  // Click the first running VM.
  const runningRow = page.locator('tr', { has: page.locator('.state-pill.running') }).first();
  await runningRow.waitFor({ state: 'visible', timeout: 30_000 });
  await slowClick(page, runningRow, { pre: 900, post: 2500 });
  await page.waitForURL('**/vms/**');
  await page.waitForTimeout(1500);

  const actionsBtn = page.getByRole('button', { name: /^actions/i });

  // Shutdown.
  await slowClick(page, actionsBtn, { pre: 600, post: 900 });
  await slowClick(page, page.getByRole('menuitem', { name: /shutdown/i }), { pre: 900, post: 4500 });

  // Start.
  await slowClick(page, actionsBtn, { pre: 600, post: 900 });
  await slowClick(page, page.getByRole('menuitem', { name: '▶ Start', exact: true }), { pre: 900, post: 4500 });

  // Snapshot.
  await slowClick(page, actionsBtn, { pre: 600, post: 900 });
  await slowClick(page, page.getByRole('menuitem', { name: /snapshot/i }), { pre: 900, post: 1800 });
  await slowClick(page, page.getByRole('button', { name: /create snapshot/i }), { pre: 700, post: 3000 });
});
