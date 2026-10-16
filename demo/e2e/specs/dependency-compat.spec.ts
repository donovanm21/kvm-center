import { test, expect } from '@playwright/test';
import { gotoSidebar, loginAsAdmin } from './_helpers';

// Compatibility coverage for React 19, Query 5 and Router 7. These journeys
// use a real server/session and read-only inventory; no command is submitted.
test.beforeEach(async ({ page }) => {
  await loginAsAdmin(page);
});

test('host tabs preserve browser history and survive direct reloads', async ({ page }) => {
  await gotoSidebar(page, /^hosts$/i);
  const hostLink = page.locator('a[href^="/hosts/"]').first();
  await expect(hostLink).toBeVisible();
  const hostPath = await hostLink.getAttribute('href');
  expect(hostPath).toBeTruthy();
  await hostLink.click();
  await expect(page).toHaveURL(new RegExp(`${hostPath}$`));
  await page.getByRole('link', { name: /^VMs$/ }).click();
  await expect(page).toHaveURL(new RegExp(`${hostPath}/vms$`));
  await expect(page.getByRole('link', { name: /^VMs$/ })).toHaveAttribute('aria-current', 'page');
  await page.goBack();
  await expect(page).toHaveURL(new RegExp(`${hostPath}$`));
  await expect(page.getByRole('link', { name: /^Summary$/ })).toHaveAttribute('aria-current', 'page');
  await page.goForward();
  await page.reload();
  await expect(page).toHaveURL(new RegExp(`${hostPath}/vms$`));
  await expect(page.getByRole('link', { name: /^VMs$/ })).toHaveAttribute('aria-current', 'page');
  await expect(page.locator('#login-username')).not.toBeVisible();
});

test('VM tab navigation and repeated modal dismissal do not submit commands', async ({ page }) => {
  await gotoSidebar(page, /virtual machines/i);
  const runningRow = page.locator('tr', { has: page.locator('.state-pill.running') }).first();
  await runningRow.click();
  await expect(page).toHaveURL(/\/vms\/[^/]+$/);
  const vmURL = page.url();
  await page.getByRole('link', { name: /^Hardware$/ }).click();
  await expect(page).toHaveURL(`${vmURL}/hardware`);
  await expect(page.getByRole('link', { name: /^Hardware$/ })).toHaveAttribute('aria-current', 'page');
  await page.goBack();
  await expect(page).toHaveURL(vmURL);
  await page.goForward();
  await page.reload();
  await expect(page.getByRole('link', { name: /^Hardware$/ })).toHaveAttribute('aria-current', 'page');

  const writes: string[] = [];
  page.on('request', (request) => {
    if (/^(POST|PUT|PATCH|DELETE)$/.test(request.method())) writes.push(request.url());
  });
  for (const dismiss of ['cancel', 'escape']) {
    await page.getByRole('button', { name: /^actions/i }).click();
    await page.getByRole('menuitem', { name: /snapshot/i }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toBeVisible();
    await dialog.locator('input').fill('compatibility-cancelled');
    if (dismiss === 'cancel') await dialog.getByRole('button', { name: /^cancel$/i }).click();
    else await page.keyboard.press('Escape');
    await expect(dialog).not.toBeVisible();
    await expect(page).toHaveURL(`${vmURL}/hardware`);
  }
  await page.goBack();
  await expect(page).toHaveURL(vmURL);
  await expect(page.getByRole('button', { name: /^actions/i })).toBeVisible();
  expect(writes).toEqual([]);
});

test('docs and nested settings links retain route and active state', async ({ page }) => {
  await page.goto('/docs');
  await expect(page.locator('article.docs-prose')).toBeVisible();
  const docLinks = page.locator('aside a[href^="/docs/"]');
  await expect(docLinks.nth(1)).toBeVisible();
  const firstPath = await docLinks.nth(0).getAttribute('href');
  const secondPath = await docLinks.nth(1).getAttribute('href');
  await docLinks.nth(0).click();
  await expect(page).toHaveURL(new RegExp(`${firstPath}$`));
  await docLinks.nth(1).click();
  await expect(page).toHaveURL(new RegExp(`${secondPath}$`));
  await expect(page.locator('article.docs-prose')).toBeVisible();
  await page.goBack();
  await expect(page).toHaveURL(new RegExp(`${firstPath}$`));
  await page.goForward();
  await page.reload();
  await expect(page).toHaveURL(new RegExp(`${secondPath}$`));
  await expect(page.locator('article.docs-prose')).toBeVisible();

  await page.goto('/settings');
  await expect(page).toHaveURL(/\/settings\/general$/);
  await page.locator('.settings-tabs').getByRole('link', { name: /^TLS$/ }).click();
  await expect(page).toHaveURL(/\/settings\/tls$/);
  await expect(page.locator('.settings-tabs').getByRole('link', { name: /^TLS$/ })).toHaveAttribute('aria-current', 'page');
  await page.reload();
  await expect(page.locator('.settings-tabs').getByRole('link', { name: /^TLS$/ })).toHaveAttribute('aria-current', 'page');
  await expect(page.getByRole('heading', { name: /^Settings$/ })).toBeVisible();
});

test('unknown routes replace to dashboard without a redirect loop', async ({ page }) => {
  await page.goto('/not-a-real-route/dependency-compatibility');
  await expect(page).toHaveURL(/\/$/);
  await expect(page.locator('.panel').first()).toBeVisible();
  await gotoSidebar(page, /^hosts$/i);
  await expect(page).toHaveURL(/\/hosts$/);
  await page.goBack();
  await expect(page).toHaveURL(/\/$/);
  await expect(page.locator('.panel').first()).toBeVisible();
});
