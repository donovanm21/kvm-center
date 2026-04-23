import { test } from '@playwright/test';
import { installCursor, login } from './_helpers';

// Flow 05 — System Health dashboard tour.
// Output: server/docs/media/05-system-health.webp

test('05-system-health', async ({ page }) => {
  test.setTimeout(120_000);
  await installCursor(page);
  await login(page);

  await page.goto('/system-health');
  await page.waitForLoadState('domcontentloaded');

  // Let the initial status tiles paint + the 15 s polling fire at
  // least once so problems + subsystem cards all have data.
  await page.waitForTimeout(3500);

  // Scroll slowly down the page so every subsystem card shows up
  // in frame.
  await page.evaluate(async () => {
    await new Promise<void>((done) => {
      const step = 120;
      const total = document.documentElement.scrollHeight - window.innerHeight;
      let y = 0;
      const id = setInterval(() => {
        y = Math.min(y + step, total);
        window.scrollTo({ top: y, behavior: 'smooth' });
        if (y >= total) { clearInterval(id); done(); }
      }, 220);
    });
  });
  await page.waitForTimeout(1500);

  // Hover the first problem row if any exist.
  const firstProblem = page.locator('table.data-table tbody tr').first();
  if (await firstProblem.count()) {
    await firstProblem.hover().catch(() => {});
    await page.waitForTimeout(1800);
  }

  // Back to the top for the closing frame.
  await page.evaluate(() => window.scrollTo({ top: 0, behavior: 'smooth' }));
  await page.waitForTimeout(2000);
});
