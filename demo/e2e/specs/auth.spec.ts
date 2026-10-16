import { test, expect } from '@playwright/test';
import { ADMIN_USERNAME, loginAsAdmin } from './_helpers';

// Auth journeys — login happy path, bad password, post-login chrome,
// and logout. Together they exercise the session-cookie + middleware
// surface that gates every other test.

test.describe('auth', () => {
  test('login with valid credentials lands on the dashboard', async ({ page }) => {
    await loginAsAdmin(page);
    // Sidebar nav and the dashboard's hypervisor cards both prove
    // we're past the login wall.
    await expect(page.getByRole('link', { name: /dashboard/i })).toBeVisible();
    await expect(page.locator('.panel').first()).toBeVisible();
  });

  test('login with a bad password does not land on the dashboard', async ({ page }) => {
    await page.goto('/login');
    await page.locator('#login-username').fill(ADMIN_USERNAME);
    await page.locator('#login-password').fill('definitely-not-the-password');
    const [response] = await Promise.all([
      page.waitForResponse((r) => r.url().endsWith('/api/auth/login') && r.request().method() === 'POST'),
      page.getByRole('button', { name: /sign in|log in|login/i }).click(),
    ]);
    expect(response.status()).toBe(401);
    // Assert after the rejection, rather than passing before the request finishes.
    await expect(
      page.getByRole('link', { name: /dashboard/i }),
    ).not.toBeVisible({ timeout: 5000 });
    // Sign-in button is still the focus — the form re-renders, not
    // the app shell.
    await expect(page.getByRole('button', { name: /sign in|log in|login/i })).toBeVisible();
  });

  test('logout drops the session and bounces back to /login', async ({ page }) => {
    await loginAsAdmin(page);
    // UserMenu (components/UserMenu.tsx): an avatar button with
    // aria-label="User menu" opens a dropdown of role="menuitem"
    // entries — Sign out is the third one.
    await page.getByRole('button', { name: /user menu/i }).click();
    await page.getByRole('menuitem', { name: /sign out/i }).click();
    // useAuth.logout() resets user/permissions in place; the router
    // doesn't navigate, but the protected layout drops back to the
    // login form on the next render. Wait on that, not on the URL.
    await expect(page.locator('#login-username')).toBeVisible();
  });
});

test('refresh preserves the session and logout revokes protected access', async ({ page, context }) => {
  await loginAsAdmin(page);
  await page.reload();
  await expect(page.getByRole('link', { name: /dashboard/i })).toBeVisible();
  expect((await context.request.get('/api/hosts')).status()).toBe(200);
  await page.getByRole('button', { name: /user menu/i }).click();
  await page.getByRole('menuitem', { name: /sign out/i }).click();
  await expect(page.locator('#login-username')).toBeVisible();
  expect((await context.request.get('/api/hosts')).status()).toBe(401);
  await page.goto('/vms');
  await expect(page.locator('#login-username')).toBeVisible();
  await page.reload();
  await expect(page.locator('#login-username')).toBeVisible();
});
