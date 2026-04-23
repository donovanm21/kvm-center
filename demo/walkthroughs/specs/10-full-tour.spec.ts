import { test } from '@playwright/test';
import { installCursor, login } from './_helpers';

// Flow 10 — full UI tour for the README banner.
// Output: server/docs/media/10-full-tour.webp
//        (then copied to docs/assets/ at release time)
//
// Walks every top-level section in ~75 seconds. No create/edit
// actions — the goal is to showcase the breadth of the UI so a
// first-time visitor can see "this is the surface area" at a
// glance. Pacing is deliberately quicker than the per-feature
// clips; the sheer amount of ground means we trade depth for
// breadth.

async function section(page, path: string, pause = 2200) {
  await page.goto(path);
  await page.waitForLoadState('domcontentloaded');
  await page.waitForTimeout(pause);
}

async function scrollThrough(page, ms = 3500) {
  await page.evaluate((ms) => new Promise<void>((done) => {
    const total = document.documentElement.scrollHeight - window.innerHeight;
    if (total <= 0) { setTimeout(done, ms); return; }
    const steps = Math.max(8, Math.floor(ms / 240));
    let i = 0;
    const id = setInterval(() => {
      const y = total * (++i / steps);
      window.scrollTo({ top: y, behavior: 'smooth' });
      if (i >= steps) {
        clearInterval(id);
        setTimeout(() => {
          window.scrollTo({ top: 0, behavior: 'smooth' });
          setTimeout(done, 900);
        }, 600);
      }
    }, 240);
  }), ms);
}

test('10-full-tour', async ({ page }) => {
  test.setTimeout(240_000);
  await installCursor(page);
  await login(page);

  // Dashboard — the landing page. Scroll through all 30 host cards.
  await page.waitForTimeout(1800);
  await scrollThrough(page, 5500);

  // Hosts — table view.
  await section(page, '/hosts', 1800);
  await scrollThrough(page, 3000);

  // Click the first host row for a peek at the detail page.
  const firstHost = page.locator('table.data-table tbody tr').first();
  if (await firstHost.count()) {
    await firstHost.hover().catch(() => {});
    await page.waitForTimeout(600);
    await firstHost.click().catch(() => {});
    await page.waitForLoadState('domcontentloaded');
    await page.waitForTimeout(3000);
  }

  // VMs — the inventory.
  await section(page, '/vms', 1800);
  await scrollThrough(page, 3500);

  // Open a running VM for a beat.
  const vmRow = page.locator('tr', { has: page.locator('.state-pill.running') }).first();
  if (await vmRow.count()) {
    await vmRow.click().catch(() => {});
    await page.waitForLoadState('domcontentloaded');
    await page.waitForTimeout(3500);
  }

  // Snapshots, Storage, Migrations — each a brief scroll.
  await section(page, '/snapshots', 1500);
  await scrollThrough(page, 2500);
  await section(page, '/storage', 1500);
  await scrollThrough(page, 2500);
  await section(page, '/migrations', 1500);
  await scrollThrough(page, 2500);

  // System Health — the operator dashboard.
  await section(page, '/system-health', 2500);
  await scrollThrough(page, 4500);

  // Docs — close on the in-app documentation.
  await section(page, '/docs', 2000);
  await scrollThrough(page, 3500);
});
