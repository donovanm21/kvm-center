import { test } from '@playwright/test';
import { installCursor, login } from './_helpers';

// Flow 10 — full UI tour for the README banner.
// Output: server/docs/media/10-full-tour.webp
//        (then copied to docs/assets/kvm-center-tour.webp)
//
// Deliberately brisk: the README clip is an at-a-glance reference
// so a visitor can see the surface area in ~40 seconds. Per-feature
// clips cover individual flows at a readable pace.

async function section(page, path: string, pause = 1100) {
  await page.goto(path);
  await page.waitForLoadState('domcontentloaded');
  await page.waitForTimeout(pause);
}

async function scrollThrough(page, ms = 1800) {
  await page.evaluate((ms) => new Promise<void>((done) => {
    const total = document.documentElement.scrollHeight - window.innerHeight;
    if (total <= 0) { setTimeout(done, ms); return; }
    const steps = Math.max(6, Math.floor(ms / 160));
    let i = 0;
    const id = setInterval(() => {
      const y = total * (++i / steps);
      window.scrollTo({ top: y, behavior: 'smooth' });
      if (i >= steps) {
        clearInterval(id);
        setTimeout(() => {
          window.scrollTo({ top: 0, behavior: 'smooth' });
          setTimeout(done, 400);
        }, 300);
      }
    }, 160);
  }), ms);
}

test('10-full-tour', async ({ page }) => {
  test.setTimeout(180_000);
  await installCursor(page);
  await login(page);

  // Dashboard.
  await page.waitForTimeout(900);
  await scrollThrough(page, 3000);

  // Hosts list.
  await section(page, '/hosts', 900);
  await scrollThrough(page, 1800);

  // Skip host + VM detail detours — they add duration without
  // advancing the tour's at-a-glance story.

  // VMs list.
  await section(page, '/vms', 900);
  await scrollThrough(page, 2200);

  // Snapshots, Storage, Migrations.
  await section(page, '/snapshots', 800);
  await scrollThrough(page, 1600);
  await section(page, '/storage', 800);
  await scrollThrough(page, 1600);
  await section(page, '/migrations', 800);
  await scrollThrough(page, 1600);

  // System Health.
  await section(page, '/system-health', 1100);
  await scrollThrough(page, 2500);

  // Docs — closing frame.
  await section(page, '/docs', 900);
  await scrollThrough(page, 2000);
});
