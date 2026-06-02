import { test } from '@playwright/test';
import { installCursor, login, slowClick } from './_helpers';

// Flow 04 — create a snapshot policy.
// Output: server/docs/media/04-snapshot-policy.webp

test('04-snapshot-policy', async ({ page }) => {
  test.setTimeout(180_000);
  await installCursor(page);
  await login(page);

  await page.goto('/snapshots/policies');
  await page.waitForLoadState('domcontentloaded');
  await page.waitForTimeout(2000);

  // Open the "+ New policy" modal.
  await slowClick(page, page.getByRole('button', { name: /new policy/i }), { pre: 800, post: 1200 });

  // Name the policy — the modal's "Name" heading is a styled div,
  // not a real <label>, so target by placeholder.
  const name = page.getByPlaceholder(/daily production|snapshot policy|policy name/i).first();
  await name.click();
  await name.type('nightly', { delay: 80 });
  await page.waitForTimeout(700);

  // Pick the Daily preset.
  await slowClick(page, page.getByRole('button', { name: /^daily$/i }), { pre: 700, post: 1000 });

  // Bump retention to 7. Retention is the second number input in
  // the modal (first is custom-minutes).
  const numbers = page.locator('input[type="number"]');
  const retention = numbers.nth(1);
  await retention.click();
  await retention.press('Control+A');
  await retention.type('7', { delay: 80 });
  await page.waitForTimeout(700);

  // Tick the first few VMs.
  const vmBoxes = page.locator('input[type="checkbox"]');
  const count = Math.min(4, await vmBoxes.count());
  for (let i = 0; i < count; i++) {
    await slowClick(page, vmBoxes.nth(i), { pre: 300, post: 500 });
  }

  // Submit.
  await slowClick(page, page.getByRole('button', { name: /create policy/i }), { pre: 700, post: 3000 });
});
