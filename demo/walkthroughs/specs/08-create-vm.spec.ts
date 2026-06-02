import { test } from '@playwright/test';
import { installCursor, login, slowClick } from './_helpers';

// Flow 08 — Create VM wizard tour.
// Output: server/docs/media/08-create-vm.webp
//
// Drives the wizard form as far as the user cares to see. The final
// submit is intentionally skipped — the demo-agent does not honour
// CREATE_VM commands, so clicking "Create VM" would produce an
// error toast that distracts from the walkthrough.

test('08-create-vm', async ({ page }) => {
  test.setTimeout(120_000);
  await installCursor(page);
  await login(page);

  await page.goto('/vms');
  await page.waitForLoadState('domcontentloaded');
  await page.waitForTimeout(1500);

  // Open the wizard.
  await slowClick(page, page.getByRole('button', { name: /create vm/i }).first(), { pre: 700, post: 1500 });

  // Pick the first real host.
  const hostSelect = page.locator('select').first();
  const opts = await hostSelect.locator('option:not([disabled])').all();
  for (const o of opts) {
    const v = await o.getAttribute('value');
    if (v) { await hostSelect.selectOption(v); break; }
  }
  await page.waitForTimeout(1500);

  // Name.
  const nameInput = page.getByPlaceholder(/ubuntu-web|vm name|e\.g\./i).first();
  await nameInput.click();
  await nameInput.type('demo-vm-01', { delay: 60 });
  await page.waitForTimeout(600);

  // Bump vCPU + memory.
  const numbers = page.locator('input[type="number"]');
  if (await numbers.count() >= 2) {
    const vcpu = numbers.nth(0);
    await vcpu.click(); await vcpu.press('Control+A'); await vcpu.type('4', { delay: 60 });
    await page.waitForTimeout(500);
    const mem = numbers.nth(1);
    await mem.click(); await mem.press('Control+A'); await mem.type('4096', { delay: 60 });
    await page.waitForTimeout(500);
  }

  // Leave source defaulted to ISO; hover the Create button so the
  // viewer sees the full wizard state before the clip ends.
  const createBtn = page.getByRole('button', { name: /^create vm$/i }).last();
  await createBtn.scrollIntoViewIfNeeded().catch(() => {});
  await createBtn.hover().catch(() => {});
  await page.waitForTimeout(2500);
});
