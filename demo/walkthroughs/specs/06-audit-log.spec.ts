import { test } from '@playwright/test';
import { installCursor, login, slowClick } from './_helpers';

// Flow 06 — Audit log filter + inspect.
// Output: server/docs/media/06-audit-log.webp

test('06-audit-log', async ({ page }) => {
  test.setTimeout(120_000);
  await installCursor(page);
  await login(page);

  await page.goto('/settings/audit');
  await page.waitForLoadState('domcontentloaded');
  await page.locator('table.data-table tbody tr').first().waitFor({ state: 'visible', timeout: 30_000 });
  await page.waitForTimeout(2000);

  // Scroll the filter toolbar into view — the Audit shipping panel
  // sits above it and otherwise dominates the opening frame. The
  // filter selects (Action, Outcome, Page size) are separate from
  // the ShippingPanel's HTTP/Syslog select; pick the one whose
  // options include VM/migration events.
  let actionSelect = null as import('@playwright/test').Locator | null;
  const candidates = await page.locator('select').all();
  for (const sel of candidates) {
    const opts = await sel.locator('option').all();
    for (const o of opts) {
      const v = (await o.getAttribute('value')) || '';
      if (v.startsWith('vm.') || v.startsWith('migration')) { actionSelect = sel; break; }
    }
    if (actionSelect) break;
  }
  if (!actionSelect) throw new Error('audit action select not found');
  await actionSelect.scrollIntoViewIfNeeded().catch(() => {});
  await actionSelect.hover();
  await page.waitForTimeout(800);

  const opts = await actionSelect.locator('option').all();
  let picked = '';
  for (const o of opts) {
    const v = (await o.getAttribute('value')) || '';
    if (v.startsWith('vm.') || v.startsWith('migration')) { picked = v; break; }
  }
  if (!picked && opts.length > 1) picked = (await opts[1].getAttribute('value')) || '';
  if (picked) await actionSelect.selectOption(picked);
  await page.waitForTimeout(2500);

  // Narrow further to Success outcome — find by option values.
  for (const sel of await page.locator('select').all()) {
    const vals = await sel.locator('option').evaluateAll((os) => os.map((o) => (o as HTMLOptionElement).value));
    if (vals.includes('true') && vals.includes('false') && vals.includes('')) {
      await sel.selectOption('true').catch(() => {});
      break;
    }
  }
  await page.waitForTimeout(2500);

  // Hover the first row so the viewer sees the Detail column.
  const firstRow = page.locator('table.data-table tbody tr').first();
  await firstRow.hover().catch(() => {});
  await page.waitForTimeout(2500);

  // Reset.
  await slowClick(page, page.getByRole('button', { name: /^reset$/i }), { pre: 600, post: 2000 });
});
