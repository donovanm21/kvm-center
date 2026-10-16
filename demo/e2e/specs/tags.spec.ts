import { test, expect } from '@playwright/test';
import { loginAsAdmin } from './_helpers';
import { tagWorkflow } from './_tag_workflow';

test('tags integrated: create, assign, edit, conflict, remove and reload', async ({ page }) => {
  await loginAsAdmin(page);
  await tagWorkflow(page);
});

test('tags mocked failure: retain draft after unavailable save', async ({ page }) => {
  await loginAsAdmin(page);
  const [vm] = await (await page.request.get('/api/vms')).json();
  await page.goto(`/vms/${vm.uuid}`);
  const section = page.getByTestId('vm-tags');
  await section.getByRole('button', { name: /Add tags|Edit tags/ }).click();
  await section.getByLabel('Tags, separated by commas').fill('retry-me');
  await page.route(`**/api/vms/${vm.uuid}/tags`, route => route.fulfill({
    status: 503, contentType: 'application/json', body: JSON.stringify({ error: 'Synthetic unavailable save; retry the identical request' }),
  }));
  await section.getByRole('button', { name: 'Save tags', exact: true }).click();
  await expect(section.getByRole('alert')).toContainText('Synthetic unavailable');
  await expect(section.getByLabel('Tags, separated by commas')).toHaveValue('retry-me');
  await expect(section.getByRole('button', { name: 'Save tags', exact: true })).toBeEnabled();
});

test('tags integrated authorization: viewer can read but cannot edit', async ({ page }) => {
  await page.goto('/login');
  await page.locator('#login-username').fill('viewer');
  await page.locator('#login-password').fill('demo-viewer');
  await page.getByRole('button', { name: /sign in|log in|login/i }).click();
  await page.getByRole('link', { name: /dashboard/i }).waitFor({ state: 'visible' });
  const [vm] = await (await page.request.get('/api/vms')).json();
  await page.goto(`/vms/${vm.uuid}`);
  const section = page.getByTestId('vm-tags');
  await expect(section).toBeVisible();
  await expect(section.getByRole('button', { name: /Add tags|Edit tags/ })).toHaveCount(0);
  expect((await page.request.get(`/api/vms/${vm.uuid}/tags`)).status()).toBe(200);
  expect((await page.request.put(`/api/vms/${vm.uuid}/tags`, { data: { tags: ['denied'], expected_tags: [] } })).status()).toBe(403);
});
