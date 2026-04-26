import type { Page } from '@playwright/test';

// Shared helpers for the assertion-driven E2E specs. The demo
// admin/demo-password account ships pre-provisioned via the seed.

export const ADMIN_USERNAME = 'admin';
export const ADMIN_PASSWORD = 'demo-password';

/** Sign in as the seeded admin and wait for the post-login chrome. */
export async function loginAsAdmin(page: Page) {
  await page.goto('/login');
  await page.locator('#login-username').fill(ADMIN_USERNAME);
  await page.locator('#login-password').fill(ADMIN_PASSWORD);
  await page.getByRole('button', { name: /sign in|log in|login/i }).click();
  // Sidebar's Dashboard link only renders post-auth; URL alone is
  // unreliable because the SPA keeps /login visible while the auth
  // round-trip is in flight.
  await page.getByRole('link', { name: /dashboard/i }).waitFor({ state: 'visible' });
}

/** Navigate via the sidebar nav link (matches what an operator does). */
export async function gotoSidebar(page: Page, label: RegExp) {
  await page.getByRole('link', { name: label }).first().click();
  // Settle the URL change before the caller asserts on content.
  await page.waitForLoadState('domcontentloaded');
}

/**
 * Sign in for the mobile /m route tree (v5.46.0). The desktop Login
 * form is reused; after submit we land on /m thanks to the auto-
 * redirect, then the MobileShell renders (data-testid="mobile-shell"
 * is the canonical post-auth chrome marker on mobile).
 */
export async function loginAsAdminMobile(page: Page) {
  await page.goto('/m');
  await page.locator('#login-username').fill(ADMIN_USERNAME);
  await page.locator('#login-password').fill(ADMIN_PASSWORD);
  await page.getByRole('button', { name: /sign in|log in|login/i }).click();
  await page.getByTestId('mobile-shell').waitFor({ state: 'visible' });
}
