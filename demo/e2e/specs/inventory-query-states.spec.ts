import { test, expect, type Page } from '@playwright/test';
import { loginAsAdmin } from './_helpers';

// Only synthetic inventory is returned; no host operations are submitted.
const host = (id: string) => ({ id, ip: '192.0.2.10', agent_port: 8081,
  cpu_usage: 10, cpu_count: 4, mem_usage: 25, mem_total_mb: 4096,
  mem_free_mb: 3072, vm_count: 0, status: 'online', last_seen: '2026-01-15T12:00:00Z' });

function gate() {
  let release!: () => void;
  const promise = new Promise<void>(resolve => { release = resolve; });
  return { promise, release };
}
async function hosts(page: Page) {
  await page.route('**/api/hosts', route => route.fulfill({ json: [host('fixture-a'), host('fixture-b')] }));
}
function panel(page: Page, label: string) {
  return page.locator('.panel').filter({ has: page.getByText(label, { exact: true }) }).first();
}

test('dashboard dependent inventory stays unknown until successful empty responses', async ({ page }) => {
  const vmGate = gate(), poolGate = gate();
  await hosts(page);
  await page.route('**/api/vms', async route => { await vmGate.promise; await route.fulfill({ json: [] }); });
  await page.route('**/api/storage', async route => { await poolGate.promise; await route.fulfill({ json: [] }); });
  try {
    await loginAsAdmin(page);
    await expect(panel(page, 'Total VMs')).toContainText('—');
    await expect(panel(page, 'Running')).not.toContainText('0 stopped');
    await expect(page.getByRole('status').filter({ hasText: 'Loading VM inventory…' })).toBeVisible();
    const card = page.locator('.panel').filter({ has: page.getByRole('link', { name: 'fixture-a', exact: true }) });
    await expect(card).toContainText('— VMs');
    await expect(card).toContainText('— storage pools');
    vmGate.release();
    await expect(card).toContainText('0 VMs');
    await expect(card).toContainText('— storage pools');
    poolGate.release();
    await expect(card).toContainText('0 storage pools');
    await expect(panel(page, 'Running')).toContainText('0 stopped');
  } finally { vmGate.release(); poolGate.release(); }
});

test('initial inventory errors never assert no VMs or pools', async ({ page }) => {
  await hosts(page);
  for (const endpoint of ['vms', 'storage']) {
    await page.route(`**/api/${endpoint}`, route => route.fulfill({ status: 503, json: { error: 'Synthetic unavailable' } }));
  }
  await loginAsAdmin(page);
  await expect(page.getByText('VM inventory unavailable. Retry by refreshing the page.', { exact: true })).toBeVisible();
  await expect(panel(page, 'Total VMs')).toContainText('—');
  await page.goto('/hosts/fixture-a');
  await expect(panel(page, 'VMs')).toContainText('—');
  await expect(panel(page, 'Storage pools')).toContainText('—');
  await page.getByRole('link', { name: 'VMs', exact: true }).click();
  await expect(page.getByText('VM inventory unavailable. Retry by refreshing the page.', { exact: true })).toBeVisible();
  await expect(page.getByText('No VMs on this host.', { exact: true })).toHaveCount(0);
  await page.locator('a[href="/hosts/fixture-a/storage"]').click();
  await expect(page.getByText('Storage inventory unavailable. Retry by refreshing the page.', { exact: true })).toBeVisible();
  await expect(page.getByText(/No storage pools reported/)).toHaveCount(0);
});

test('host tabs distinguish delayed data from confirmed empty inventory', async ({ page }) => {
  await hosts(page);
  await loginAsAdmin(page);
  const vmGate = gate(), poolGate = gate();
  await page.route('**/api/vms', async route => { await vmGate.promise; await route.fulfill({ json: [] }); });
  await page.route('**/api/storage', async route => { await poolGate.promise; await route.fulfill({ json: [] }); });
  try {
    await page.goto('/hosts/fixture-a'); // Fresh query cache on document load.
    await expect(panel(page, 'VMs')).toContainText('—');
    await expect(panel(page, 'Storage pools')).toContainText('—');
    await page.getByRole('link', { name: 'VMs', exact: true }).click();
    await expect(page.getByText('Loading VM inventory…', { exact: true })).toBeVisible();
    await expect(page.getByText('No VMs on this host.', { exact: true })).toHaveCount(0);
    vmGate.release();
    await expect(page.getByText('No VMs on this host.', { exact: true })).toBeVisible();
    await page.locator('a[href="/hosts/fixture-a/storage"]').click();
    await expect(page.getByText('Loading Storage inventory…', { exact: true })).toBeVisible();
    await expect(page.getByText(/No storage pools reported/)).toHaveCount(0);
    poolGate.release();
    await expect(page.getByText(/No storage pools reported/)).toBeVisible();
  } finally { vmGate.release(); poolGate.release(); }
});

for (const count of [0, 1]) test(`cached count ${count} remains visible during refresh and a refresh failure`, async ({ page }) => {
  await page.clock.install();
  let fail = false;
  const refresh = gate();
  await hosts(page);
  await page.route('**/api/vms', async route => {
    if (fail) { await refresh.promise; await route.fulfill({ status: 503, json: { error: 'Synthetic refresh failure' } }); }
    else await route.fulfill({ json: count ? [{ uuid: 'fixture-vm', host_id: 'fixture-a', state: 'stopped' }] : [] });
  });
  await page.route('**/api/storage', route => route.fulfill({ json: [] }));
  await loginAsAdmin(page);
  await expect(panel(page, 'Running')).toContainText(`${count} stopped`);
  fail = true;
  try {
    await page.clock.fastForward(61_000);
    await expect(page.getByText('VM inventory: refreshing; showing last known data.', { exact: true })).toBeVisible();
    await expect(panel(page, 'Running')).toContainText(`${count} stopped`);
    refresh.release();
    // Advance the query's single retry after its first failed response.
    await page.clock.runFor(2000);
    await expect(page.getByText('VM inventory: showing last known data; refresh failed.', { exact: true })).toBeVisible();
    await expect(panel(page, 'Running')).toContainText(`${count} stopped`);
  } finally { refresh.release(); }
});

test('initial host error is unavailable rather than an empty fleet or missing host', async ({ page }) => {
  await page.route('**/api/hosts', route => route.fulfill({ status: 503, json: { error: 'Synthetic unavailable' } }));
  await loginAsAdmin(page);
  await expect(page.getByText('Hypervisors unavailable. Retry by refreshing the page.', { exact: true })).toBeVisible();
  await expect(panel(page, 'Hypervisors')).toContainText('—');
  await expect(page.getByText(/No hypervisors have reported/)).toHaveCount(0);
  await page.goto('/hosts/fixture-a');
  await expect(page.getByText('Hypervisors unavailable. Retry by refreshing the page.', { exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Host not found' })).toHaveCount(0);
});

// SPA navigation preserves the shared query cache; summaries must still select
// only the chosen host's records rather than reuse the previous host's count.
test('switching hosts filters cached VM and pool counts to the selected host', async ({ page }) => {
  await hosts(page);
  await page.route('**/api/vms', route => route.fulfill({ json: [{ uuid: 'fixture-vm', host_id: 'fixture-a', state: 'running' }] }));
  await page.route('**/api/storage', route => route.fulfill({ json: [{ name: 'fixture-pool', host_id: 'fixture-a' }] }));
  await loginAsAdmin(page);
  await page.getByRole('link', { name: 'fixture-a', exact: true }).click();
  await expect(panel(page, 'VMs')).toHaveText('VMs11 running');
  await expect(panel(page, 'Storage pools')).toHaveText('Storage pools1libvirt pools');
  await page.getByRole('link', { name: 'Dashboard', exact: true }).click();
  await page.getByRole('link', { name: 'fixture-b', exact: true }).click();
  await expect(panel(page, 'VMs')).toHaveText('VMs00 running');
  await expect(panel(page, 'Storage pools')).toHaveText('Storage pools0libvirt pools');
});
