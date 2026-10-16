import { test, expect } from '@playwright/test';
import { loginAsAdmin, gotoSidebar } from './_helpers';

// UI presentation only. Go integration tests separately exercise real session
// middleware and signed requests to a synthetic agent returning 401/403.
const message = 'agent rejected the server request (upstream 401); check the server-to-agent connection';

test('storage Browse distinguishes agent rejection from browser login', async ({ page }) => {
  await loginAsAdmin(page);
  await page.route(/\/api\/storage\/browse\?/, route => route.fulfill({ status: 502, json: { error: message } }));
  await gotoSidebar(page, /^storage$/i);
  await page.getByRole('button', { name: 'Browse', exact: true }).first().click();
  await expect(page.getByText(`Failed to load: ${message}`, { exact: true })).toBeVisible({ timeout: 15000 });
  await page.getByRole('button', { name: 'Close', exact: true }).last().click();
  await gotoSidebar(page, /virtual machines/i);
  await expect(page.locator('table.data-table tbody tr').first()).toBeVisible();
  expect((await page.request.get('/api/auth/me')).status()).toBe(200);
});

test('VM Snapshots distinguishes agent rejection from browser login', async ({ page }) => {
  await loginAsAdmin(page);
  await page.route(/\/api\/vms\/snapshots\?/, route => route.fulfill({ status: 502, json: { error: message } }));
  await gotoSidebar(page, /virtual machines/i);
  await page.locator('tr', { has: page.locator('.state-pill.running') }).first().click();
  await page.getByRole('link', { name: 'Snapshots', exact: true }).click();
  await expect(page.getByText(`Failed to load snapshots: ${message}`, { exact: true })).toBeVisible({ timeout: 15000 });
  await page.getByRole('link', { name: 'Summary', exact: true }).click();
  await expect(page.getByRole('button', { name: /^actions/i })).toBeVisible();
  expect((await page.request.get('/api/auth/me')).status()).toBe(200);
});
