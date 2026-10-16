import { expect, type Page } from '@playwright/test';

// No route interception: real browser -> authenticated server -> disposable Mongo.
// Reload below is a browser reload, not a server restart; Mongo reconnect/reload
// is independently covered by TestMongoIntegrationTagAPIWorkflow.
export async function tagWorkflow(page: Page, mobile = false) {
  const inventory = await (await page.request.get('/api/vms')).json();
  const [vm, other] = inventory;
  const path = `/api/vms/${vm.uuid}/tags`;
  const otherPath = `/api/vms/${other.uuid}/tags`;
  const clean = async (url: string) => {
    const current = await (await page.request.get(url)).json();
    expect((await page.request.put(url, { data: { tags: [], expected_tags: current.tags } })).status()).toBe(200);
  };
  await clean(path); await clean(otherPath);
  await page.goto(`${mobile ? '/m' : ''}/vms/${vm.uuid}`);
  const section = page.getByTestId('vm-tags');
  const input = section.getByLabel('Tags, separated by commas');
  try {
    await section.getByRole('button', { name: 'Add tags', exact: true }).click();
    await input.fill('Production, DATABASE, production');
    await section.getByRole('button', { name: 'Save tags', exact: true }).click();
    await expect(section.getByRole('status')).toHaveText('Tags saved');
    expect(await (await page.request.get(path)).json()).toEqual({ tags: ['database', 'production'] });
    await page.reload();
    await expect(section.getByText('production', { exact: true })).toBeVisible();
    // Assign an existing tag to another real fixture VM, using the same UI.
    await page.goto(`${mobile ? '/m' : ''}/vms/${other.uuid}`);
    await section.getByRole('button', { name: 'Add tags', exact: true }).click();
    await expect(section.getByText(/Existing tags:/)).toContainText('database');
    await input.fill('database');
    await section.getByRole('button', { name: 'Save tags', exact: true }).click();
    await expect(section.getByRole('status')).toHaveText('Tags saved');
    const catalog = await (await page.request.get('/api/tags')).json();
    expect(catalog.tags).toContainEqual({ tag: 'database', count: 2 });
    await page.goto(`${mobile ? '/m' : ''}/vms/${vm.uuid}`);
    await section.getByRole('button', { name: 'Edit tags', exact: true }).click();
    await input.fill('bad tag');
    await section.getByRole('button', { name: 'Save tags', exact: true }).click();
    await expect(section.getByRole('alert')).toContainText('tag must');
    await expect(input).toHaveValue('bad tag');
    // An independent writer changes the same fixture while the editor is open.
    expect((await page.request.put(path, { data: { tags: ['concurrent'], expected_tags: ['database', 'production'] } })).status()).toBe(200);
    await input.fill('stale');
    await section.getByRole('button', { name: 'Save tags', exact: true }).click();
    await expect(section.getByRole('alert')).toContainText('tags changed');
    await section.getByRole('button', { name: 'Reload tags', exact: true }).click();
    await expect(input).toHaveValue('concurrent');
    await input.fill('edited');
    await section.getByRole('button', { name: 'Save tags', exact: true }).click();
    await expect(section.getByText('edited', { exact: true })).toBeVisible();
    await page.goto(mobile ? '/m/vms' : '/vms');
    await page.getByPlaceholder(mobile ? 'Filter by name, host, hostname' : 'Search VMs, hosts or tags…').fill('edited');
    await expect(page.getByText(vm.name, { exact: true }).first()).toBeVisible();
    await page.goto(`${mobile ? '/m' : ''}/vms/${vm.uuid}`);
    await section.getByRole('button', { name: 'Edit tags', exact: true }).click();
    await input.fill('');
    await section.getByRole('button', { name: 'Save tags', exact: true }).click();
    await expect(section.getByText('No tags', { exact: true })).toBeVisible();
    await page.reload();
    await expect(section.getByText('No tags', { exact: true })).toBeVisible();
  } finally { await clean(path); await clean(otherPath); }
}
