import { test } from '@playwright/test';
import { installCursor } from './_helpers';

// Flow 01 — login + dashboard tour.
// Output: server/docs/media/01-login-dashboard.webp

test('01-login-dashboard', async ({ page }) => {
  await installCursor(page);
  await page.goto('/login');
  await page.waitForLoadState('domcontentloaded');
  await page.waitForTimeout(800);

  const username = page.locator('#login-username');
  const password = page.locator('#login-password');
  await username.hover();
  await page.waitForTimeout(400);
  await username.click();
  await username.type('admin', { delay: 80 });
  await page.waitForTimeout(500);
  await password.hover();
  await page.waitForTimeout(300);
  await password.click();
  await password.type('demo-password', { delay: 80 });
  await page.waitForTimeout(700);

  const signIn = page.getByRole('button', { name: /sign in|log in|login/i });
  await signIn.hover();
  await page.waitForTimeout(400);
  await signIn.click();

  await page.getByRole('link', { name: /dashboard/i }).waitFor({ state: 'visible', timeout: 15_000 });
  await page.waitForLoadState('domcontentloaded');
  await page.waitForTimeout(2500);

  // Scroll through hypervisor cards.
  const lastCard = page.locator('.panel').last();
  await lastCard.scrollIntoViewIfNeeded().catch(() => {});
  await page.waitForTimeout(1800);

  // Hover one card to show the hover state.
  await page.locator('.panel').first().hover().catch(() => {});
  await page.waitForTimeout(1800);

  // Scroll back to top.
  await page.evaluate(() => window.scrollTo({ top: 0, behavior: 'smooth' }));
  await page.waitForTimeout(2500);
});
