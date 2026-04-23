import { test } from '@playwright/test';
import { installCursor, login, slowClick } from './_helpers';

// Flow 09 — Mint a hypervisor enrollment token.
// Output: server/docs/media/09-hypervisor-enroll.webp

test('09-hypervisor-enroll', async ({ page }) => {
  test.setTimeout(120_000);
  await installCursor(page);
  await login(page);

  await page.goto('/settings/tls');
  await page.waitForLoadState('domcontentloaded');
  await page.waitForTimeout(2000);

  // Scroll to the Hypervisor PKI section.
  const pki = page.getByText(/hypervisor pki/i).first();
  await pki.scrollIntoViewIfNeeded().catch(() => {});
  await page.waitForTimeout(1000);

  // Fill the Host ID input.
  const hostIdInput = page.getByPlaceholder(/host id|hvnode/i).first();
  await hostIdInput.click();
  await hostIdInput.type('hvnode04', { delay: 80 });
  await page.waitForTimeout(700);

  // Issue.
  await slowClick(page, page.getByRole('button', { name: /issue enrollment token/i }), { pre: 700, post: 2500 });

  // Pause on the displayed token so viewers can see the one-liner.
  const token = page.locator('code').first();
  if (await token.count()) {
    await token.scrollIntoViewIfNeeded().catch(() => {});
    await token.hover().catch(() => {});
    await page.waitForTimeout(4000);
  }
});
