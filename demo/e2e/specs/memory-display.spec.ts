import { test, expect } from '@playwright/test';
import { loginAsAdmin } from './_helpers';

// Presentation regression only: inventory responses are mocked with synthetic
// memory values. This does not establish telemetry/backend accuracy.
for (const fixture of [
  { total: 256, used: 256, label: '256 / 256 MiB' },
  { total: 1536, used: 1024, label: '1.0 / 1.5 GiB' },
]) {
  test(`memory presentation: ${fixture.total} MiB in list and Monitor`, async ({ page }) => {
    let uuid = '';
    await page.route('**/api/vms', async route => {
      const response = await route.fetch();
      const [vm] = await response.json();
      uuid = vm.uuid;
      await route.fulfill({ response, json: [{ ...vm, name: 'synthetic-memory-fixture', mem_total_mb: fixture.total, mem_usage_mb: fixture.used }] });
    });
    await loginAsAdmin(page);
    await page.goto('/vms');
    const row = page.getByRole('row').filter({ hasText: 'synthetic-memory-fixture' });
    await expect(row).toContainText(fixture.label);
    await row.getByRole('link', { name: 'synthetic-memory-fixture', exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`/vms/${encodeURIComponent(uuid)}`));
    await page.getByRole('link', { name: 'Monitor', exact: true }).click();
    await expect(page.getByText(fixture.label, { exact: true })).toBeVisible();
  });
}
