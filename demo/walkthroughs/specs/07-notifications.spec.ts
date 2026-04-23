import { test } from '@playwright/test';
import { installCursor, login, slowClick } from './_helpers';

// Flow 07 — Configure a webhook notification channel.
// Output: server/docs/media/07-notifications.webp

test('07-notifications', async ({ page }) => {
  test.setTimeout(120_000);
  await installCursor(page);
  await login(page);

  await page.goto('/settings/notifications');
  await page.waitForLoadState('domcontentloaded');
  await page.waitForTimeout(2000);

  // Add a webhook channel.
  await slowClick(page, page.getByRole('button', { name: /add webhook/i }), { pre: 700, post: 1200 });

  // Fill name + URL on the new card (last card on the page).
  const nameInputs = page.locator('input[type="text"]');
  const lastName = nameInputs.last();
  await lastName.click();
  await lastName.type('ops-discord', { delay: 80 });
  await page.waitForTimeout(500);

  const urlInput = page.locator('input[type="url"]').last();
  await urlInput.click();
  await urlInput.type('https://discord.com/api/webhooks/999/demo-token-abc', { delay: 35 });
  await page.waitForTimeout(600);

  // Expand the event routing panel.
  await slowClick(page, page.getByRole('button', { name: /event routing/i }).last(), { pre: 700, post: 1500 });

  // Tick a couple of event types.
  const evBoxes = page.locator('input[type="checkbox"]');
  const ct = Math.min(3, await evBoxes.count());
  for (let i = 0; i < ct; i++) {
    await slowClick(page, evBoxes.nth(i), { pre: 300, post: 450 });
  }

  // Save.
  await slowClick(page, page.getByRole('button', { name: /save settings/i }), { pre: 700, post: 3000 });
});
