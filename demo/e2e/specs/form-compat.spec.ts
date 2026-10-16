import { test, expect, type Page } from '@playwright/test';
import { gotoSidebar, loginAsAdmin } from './_helpers';

// Ordinary form compatibility only. Login and read-only API calls use the local
// fixture. Every later write is intercepted before it can reach the server;
// successful replies below are synthetic UI fixtures, never VM/token operations.
test.use({ serviceWorkers: 'block' });

interface Write { path: string; method: string; body: Record<string, unknown> }
async function interceptWrites(page: Page, respond: (write: Write) => unknown | undefined) {
  const writes: Write[] = [];
  const unexpected: Write[] = [];
  await page.route('**/*', async (route) => {
    const request = route.request();
    if (['GET', 'HEAD', 'OPTIONS'].includes(request.method())) {
      await route.continue();
      return;
    }
    const write = { path: new URL(request.url()).pathname, method: request.method(), body: request.postDataJSON() };
    writes.push(write);
    const response = respond(write);
    if (response === undefined) {
      unexpected.push(write);
      await route.abort('blockedbyclient');
    } else {
      await route.fulfill({ status: 200, json: response });
    }
  });
  return { writes, unexpected };
}

async function openTokens(page: Page) {
  await page.getByRole('button', { name: 'User menu' }).click();
  await page.getByRole('menuitem', { name: /my api tokens/i }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  return dialog;
}

async function openSnapshot(page: Page) {
  await page.getByRole('button', { name: /^actions/i }).click();
  await page.getByRole('menuitem', { name: /snapshot/i }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  return dialog;
}

test.beforeEach(async ({ page }) => {
  await loginAsAdmin(page);
});

test('token form validates repeated edits, trims parsed output and resets after synthetic success/close', async ({ page }) => {
  const pageErrors: string[] = [];
  page.on('pageerror', (error) => pageErrors.push(error.message));
  const { writes, unexpected } = await interceptWrites(page, (write) => {
    if (write.path !== '/api/tokens/me' || write.method !== 'POST') return undefined;
    return {
      token: 'synthetic-form-result-not-a-credential',
      record: { id: 'synthetic', name: write.body.name, kind: 'user', created_at: new Date().toISOString(), revoked: false },
    };
  });
  const dialog = await openTokens(page);
  const name = dialog.getByPlaceholder('Token name (e.g. laptop-kvmctl)');
  const expiry = dialog.getByRole('combobox');
  const submit = dialog.getByRole('button', { name: /^create$/i });
  await expect(name).toHaveValue('');
  await expect(expiry).toHaveValue('30');
  for (const invalid of ['', '  ', 'x'.repeat(65)]) {
    await name.fill(invalid);
    await submit.click();
    await expect(dialog.getByRole('alert')).toHaveText(invalid.length > 64 ? '64 characters maximum' : 'Token name is required');
    expect(writes).toEqual([]);
  }
  await name.fill('  form regression  ');
  await expiry.selectOption('90');
  await submit.click();
  await expect(dialog.getByText('synthetic-form-result-not-a-credential', { exact: true })).toBeVisible();
  expect(writes).toHaveLength(1);
  expect(writes[0].body.name).toBe('form regression');
  const expiryDays = (Date.parse(String(writes[0].body.expires_at)) - Date.now()) / 86400_000;
  expect(expiryDays).toBeGreaterThan(89.9);
  expect(expiryDays).toBeLessThanOrEqual(90.01);
  await expect(name).toHaveValue('');
  await expect(expiry).toHaveValue('90');
  await expect(dialog.getByRole('alert')).not.toBeVisible();
  await submit.click();
  await expect(dialog.getByRole('alert')).toHaveText('Token name is required');
  expect(writes).toHaveLength(1);

  for (const dismiss of ['close', 'escape']) {
    await name.fill('discard this');
    if (dismiss === 'close') await dialog.getByRole('button', { name: /^close$/i }).click();
    else await page.keyboard.press('Escape');
    await expect(dialog).not.toBeVisible();
    await openTokens(page);
    await expect(name).toHaveValue('');
    await expect(expiry).toHaveValue('30');
    await expect(dialog.getByRole('alert')).not.toBeVisible();
    await expect(dialog.getByText('synthetic-form-result-not-a-credential', { exact: true })).not.toBeVisible();
  }
  await name.fill('second valid name');
  await submit.click();
  await expect(name).toHaveValue('');
  expect(writes).toHaveLength(2);
  expect(writes[1].body.name).toBe('second valid name');
  expect(unexpected).toEqual([]);
  expect(pageErrors).toEqual([]);
});

test('snapshot form blur validation and cancel/reopen discard stale errors and values', async ({ page }) => {
  const pageErrors: string[] = [];
  page.on('pageerror', (error) => pageErrors.push(error.message));
  const { writes, unexpected } = await interceptWrites(page, (write) => {
    if (write.path === '/api/command' && write.method === 'POST' && write.body.action === 'SNAPSHOT') return { success: true };
    return undefined;
  });
  await gotoSidebar(page, /virtual machines/i);
  await page.locator('tr', { has: page.locator('.state-pill.running') }).first().click();
  const vmURL = page.url();
  const dialog = await openSnapshot(page);
  const name = dialog.locator('input');
  const submit = dialog.getByRole('button', { name: /^create snapshot$/i });
  await expect(name).toHaveValue(/^snap-\d{8}-\d{4}$/);
  await expect(submit).toBeEnabled();
  for (const [invalid, message] of [
    ['', 'VM name is required'],
    ['bad name', 'Use letters, digits, dot, underscore, hyphen only'],
    ['x'.repeat(65), 'VM name must be 64 characters or fewer'],
  ]) {
    await name.fill(invalid);
    await name.press('Tab');
    await expect(dialog.getByRole('alert')).toHaveText(message);
    await expect(submit).toBeDisabled();
    await name.fill('valid-after-error');
    await name.press('Tab');
    await expect(dialog.getByRole('alert')).not.toBeVisible();
    await expect(submit).toBeEnabled();
  }
  for (const dismiss of ['cancel', 'escape']) {
    await name.fill('invalid again');
    await name.press('Tab');
    await expect(dialog.getByRole('alert')).toBeVisible();
    if (dismiss === 'cancel') await dialog.getByRole('button', { name: /^cancel$/i }).click();
    else await page.keyboard.press('Escape');
    await expect(dialog).not.toBeVisible();
    await expect(page).toHaveURL(vmURL);
    await openSnapshot(page);
    await expect(name).toHaveValue(/^snap-\d{8}-\d{4}$/);
    await expect(dialog.getByRole('alert')).not.toBeVisible();
    await expect(submit).toBeEnabled();
  }
  expect(writes).toEqual([]);
  await name.fill('snapshot-form-regression');
  await name.press('Tab');
  await submit.click();
  await expect(dialog).not.toBeVisible();
  expect(writes).toHaveLength(1);
  expect(writes[0].body.params).toEqual({ name: 'snapshot-form-regression' });
  await openSnapshot(page);
  await expect(name).toHaveValue(/^snap-\d{8}-\d{4}$/);
  await expect(submit).toBeEnabled();
  expect(unexpected).toEqual([]);
  expect(pageErrors).toEqual([]);
});

test('VM form keeps cross-field errors, recovers on corrected values and clears on cancellation', async ({ page }) => {
  const { writes, unexpected } = await interceptWrites(page, (write) => {
    if (write.path === '/api/vm-create' && write.method === 'POST') return { success: true };
    return undefined;
  });
  await gotoSidebar(page, /virtual machines/i);
  await page.getByRole('button', { name: /\+ create vm/i }).click();
  const dialog = page.getByRole('dialog');
  const name = dialog.getByPlaceholder('e.g. ubuntu-web-01');
  const submit = dialog.getByRole('button', { name: /^create vm$/i });
  await expect(submit).toBeEnabled();
  await submit.click();
  await expect(dialog.getByText('VM name is required', { exact: true })).toBeVisible();
  await name.fill('form-regression-vm');
  // The fixture has no agent. Import exercises refinement without relying on
  // any host's ISO catalogue or invoking a real VM action.
  await dialog.getByRole('radio', { name: 'Import existing disk' }).click();
  await submit.click();
  await expect(dialog.getByText('Provide the path to the existing disk image.', { exact: true })).toBeVisible();
  await dialog.getByPlaceholder('/var/lib/libvirt/images/existing.qcow2').fill('   ');
  await submit.click();
  await expect(dialog.getByText('Provide the path to the existing disk image.', { exact: true })).toBeVisible();
  expect(writes).toEqual([]);
  await dialog.getByPlaceholder('/var/lib/libvirt/images/existing.qcow2').fill(' /fixture/synthetic.qcow2 ');
  await submit.click();
  await expect(dialog).not.toBeVisible();
  expect(writes).toHaveLength(1);
  expect(writes[0].body).toMatchObject({ name: 'form-regression-vm', import_disk: '/fixture/synthetic.qcow2', disk_size_gb: 0, memory_mb: 2048, vcpus: 2 });

  for (const dismiss of ['cancel', 'escape']) {
    await page.getByRole('button', { name: /\+ create vm/i }).click();
    await expect(name).toHaveValue('');
    await expect(dialog.getByPlaceholder('/var/lib/libvirt/images/existing.qcow2')).not.toBeVisible();
    await submit.click();
    await expect(dialog.getByText('VM name is required', { exact: true })).toBeVisible();
    await name.fill('discard-vm');
    if (dismiss === 'cancel') await dialog.getByRole('button', { name: /^cancel$/i }).click();
    else await page.keyboard.press('Escape');
    await expect(dialog).not.toBeVisible();
  }
  await page.getByRole('button', { name: /\+ create vm/i }).click();
  await expect(name).toHaveValue('');
  await expect(dialog.getByText('VM name is required', { exact: true })).not.toBeVisible();
  expect(writes).toHaveLength(1);
  expect(unexpected).toEqual([]);
});
