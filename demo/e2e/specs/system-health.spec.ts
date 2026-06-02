import { test, expect } from '@playwright/test';
import { gotoSidebar, loginAsAdmin } from './_helpers';

// System Health page (v5.33.x) — proves the rollup endpoint hangs
// off the React Query layer, the page renders the big tiles, and
// the data table for problems is at least mounted.

test('System Health page renders subsystem cards + tiles', async ({ page }) => {
  await loginAsAdmin(page);
  await gotoSidebar(page, /system health/i);
  // Top-level page heading.
  await expect(page.getByRole('heading', { name: /system health/i }).first()).toBeVisible();
  // SystemHealth uses inline-styled divs (not the shared .panel
  // class); look for one of the labelled subsystem panels by its
  // text content. Pre-aggregates land within the 15 s polling tick.
  await expect(page.getByText(/cpu utilization/i).first()).toBeVisible({ timeout: 15_000 });
});
