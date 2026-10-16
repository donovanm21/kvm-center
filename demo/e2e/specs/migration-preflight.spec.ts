import { test, expect, type Page } from '@playwright/test';
import { loginAsAdmin } from './_helpers';

const uuid = '11111111-1111-4111-8111-111111111111';
async function fixture(page: Page, state: string, status = 'online', age = 0) {
  const observed = Date.now() - age;
  await page.route('**/api/hosts', async route => {
    const response = await route.fetch();
    const [base] = await response.json();
    await route.fulfill({ json: [
      { ...base, id: 'fixture-source', ip: '192.0.2.10', status, last_seen: new Date(observed).toISOString(), maintenance_mode: false },
      { ...base, id: 'fixture-target', ip: '192.0.2.20', status: 'online', last_seen: new Date(observed).toISOString(), maintenance_mode: false, mem_free_mb: 8192 },
    ] });
  });
  await page.route('**/api/vms', async route => {
    const response = await route.fetch();
    const [base] = await response.json();
    await route.fulfill({ json: [{ ...base, uuid, name: 'fixture-migration-vm', host_id: 'fixture-source', state, mem_total_mb: 256, cpu_mode: 'host-model', disk_type: 'qcow2', snapshot_count: 0, pci_devices: [] }] });
  });
  await page.route('**/api/migrations', route => route.fulfill({ json: [] }));
  await loginAsAdmin(page);
  await page.goto(`/vms/${uuid}`);
  await page.getByRole('button', { name: /^Actions/i }).click();
  await page.getByRole('menuitem', { name: /Migrate/ }).click();
  return page.getByRole('dialog');
}

for (const scenario of [
  { state: 'stopped', status: 'online', age: 0, reason: /Reported state: stopped/ },
  { state: 'unknown', status: 'online', age: 0, reason: /Reported state: unknown/ },
  { state: 'running', status: 'offline', age: 0, reason: /Source host is available/ },
  { state: 'running', status: 'online', age: 180_000, reason: /Source observation is missing or stale/ },
]) test(`live preflight blocks ${scenario.state}/${scenario.status}/age${scenario.age} without changing offline mode`, async ({ page }) => {
  const commands: string[] = [];
  await page.route('**/api/command', async route => { commands.push(route.request().postDataJSON().action); await route.fulfill({ status: 409, json: { error: 'Synthetic request intercepted' } }); });
  const modal = await fixture(page, scenario.state, scenario.status, scenario.age);
  await expect(modal.getByRole('button', { name: 'Shut Down & Migrate', exact: true })).toBeEnabled();
  await modal.getByRole('button', { name: 'Live', exact: true }).click();
  await expect(modal.getByRole('button', { name: 'Live Migrate', exact: true })).toBeDisabled();
  await expect(modal.getByText(scenario.reason)).toBeVisible();
  expect(commands).toEqual([]);
  await modal.getByRole('button', { name: 'Offline (Recommended)', exact: true }).click();
  await expect(modal.getByText(/Shuts down the VM/)).toBeVisible();
  await modal.getByRole('button', { name: 'Shut Down & Migrate', exact: true }).click();
  await expect.poll(() => commands).toEqual(['OFFLINE_MIGRATE']);
});

test('fresh running source submits live action and surfaces execution-time rejection', async ({ page }) => {
  let command: Record<string, unknown> | undefined;
  await page.route('**/api/command', async route => {
    command = route.request().postDataJSON();
    await route.fulfill({ status: 409, json: { success: false, message: 'Source stopped before execution; no live migration started.' } });
  });
  const modal = await fixture(page, 'running');
  await modal.getByRole('button', { name: 'Live', exact: true }).click();
  await expect(modal.getByRole('button', { name: 'Live Migrate', exact: true })).toBeEnabled();
  await modal.getByRole('button', { name: 'Live Migrate', exact: true }).click();
  await expect(page.getByText('MIGRATE failed: Source stopped before execution; no live migration started.', { exact: true })).toBeVisible();
  expect(command).toMatchObject({ action: 'MIGRATE', vm_uuid: uuid, host_id: 'fixture-source', params: { destination: '192.0.2.20' } });
  await expect(modal.getByRole('button', { name: 'Live Migrate', exact: true })).toBeEnabled();
});

test('an open live dialog ages out stale source telemetry', async ({ page }) => {
  await page.clock.install();
  let calls = 0;
  await page.route('**/api/command', async route => { calls++; await route.fulfill({ status: 409, json: { error: 'Synthetic interception' } }); });
  const modal = await fixture(page, 'running');
  await modal.getByRole('button', { name: 'Live', exact: true }).click();
  await expect(modal.getByRole('button', { name: 'Live Migrate', exact: true })).toBeEnabled();
  await page.clock.fastForward(121_000);
  await expect(modal.getByRole('button', { name: 'Live Migrate', exact: true })).toBeDisabled();
  await expect(modal.getByText(/Source observation is missing or stale/)).toBeVisible();
  expect(calls).toBe(0);
});
