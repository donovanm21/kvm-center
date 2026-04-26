import { test, expect } from '@playwright/test';
import { gotoSidebar, loginAsAdmin } from './_helpers';

// Inventory journeys: dashboard tour, hosts list, VMs list, opening
// a host detail and a VM detail page. Asserts the new <StatePill>
// extraction (v5.37.0) is rendering for both VMs and hosts.

test.describe('inventory', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsAdmin(page);
  });

  test('dashboard shows hypervisor cards with state pills', async ({ page }) => {
    // We're already on / (loginAsAdmin lands on dashboard). The
    // demo seeds 30 hypervisors so at least one card must paint.
    await expect(page.locator('.panel').first()).toBeVisible();
    // The new HostStatusPill renders state-pill class with an
    // aria-label starting "State:".
    const firstPill = page.locator('.state-pill').first();
    await expect(firstPill).toBeVisible();
    await expect(firstPill).toHaveAttribute('aria-label', /^State:/);
  });

  test('VMs list renders rows with running/stopped pills', async ({ page }) => {
    await gotoSidebar(page, /virtual machines/i);
    await page.locator('table.data-table tbody tr').first().waitFor({ state: 'visible' });
    // A demo deployment should always have at least one running VM.
    const runningPill = page.locator('.state-pill.running').first();
    await expect(runningPill).toBeVisible();
    await expect(runningPill).toHaveAttribute('aria-label', /running/i);
  });

  test('opening a VM detail page mounts the actions menu', async ({ page }) => {
    await gotoSidebar(page, /virtual machines/i);
    const runningRow = page
      .locator('tr', { has: page.locator('.state-pill.running') })
      .first();
    await runningRow.waitFor({ state: 'visible' });
    await runningRow.click();
    await page.waitForURL(/\/vms\//);
    // Detail page renders the Actions dropdown trigger.
    await expect(page.getByRole('button', { name: /^actions/i })).toBeVisible();
    // And the state pill survives the page transition.
    await expect(page.locator('.state-pill').first()).toBeVisible();
  });

  test('hosts page lists hypervisors with status pills', async ({ page }) => {
    await gotoSidebar(page, /^hosts$/i);
    await page.locator('table.data-table tbody tr').first().waitFor({ state: 'visible' });
    // HostStatusPill renders with a glyph + aria-label.
    const pill = page.locator('.state-pill').first();
    await expect(pill).toBeVisible();
    await expect(pill).toHaveAttribute('aria-label', /^State:/);
  });
});
