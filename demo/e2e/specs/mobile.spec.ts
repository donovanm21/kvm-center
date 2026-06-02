import { test, expect, devices } from '@playwright/test';
import { loginAsAdminMobile } from './_helpers';

// Mobile /m route tree walkthrough (v5.46.0). Run as the `iphone`
// project from playwright.config.ts. Confirms the shell renders,
// the four tab destinations load real content, and detail screens
// are reachable from their list cards.

test.use({ ...devices['iPhone 15'] });

test.describe('mobile (/m)', () => {
  test('iPhone-shaped viewport auto-redirects from / to /m', async ({ page }) => {
    await page.goto('/');
    await loginAsAdminMobile(page);
    // After login, MobileShell + bottom tab bar should be the chrome.
    await expect(page.getByTestId('mobile-shell')).toBeVisible();
    await expect(page.getByRole('navigation', { name: /primary navigation/i })).toBeVisible();
  });

  test('overview shows fleet stats + recent activity link', async ({ page }) => {
    await loginAsAdminMobile(page);
    await expect(page.getByTestId('mobile-dashboard')).toBeVisible();
    await expect(page.getByRole('heading', { name: /overview/i, level: 1 })).toBeVisible();
    // Four stat tiles render even on an empty cluster (values can be 0).
    await expect(page.locator('.m-stat')).toHaveCount(4);
  });

  test('hosts tab lists hosts and a card opens host detail', async ({ page }) => {
    await loginAsAdminMobile(page);
    await page.getByRole('link', { name: /hosts/i }).first().click();
    await expect(page.getByTestId('mobile-hosts')).toBeVisible();
    await expect(page.getByRole('heading', { name: /^hosts$/i, level: 1 })).toBeVisible();
    // The demo seed always provisions hosts; tap the first card.
    const firstHost = page.locator('.m-card-link').first();
    await expect(firstHost).toBeVisible();
    await firstHost.click();
    await expect(page.getByTestId('mobile-host-detail')).toBeVisible();
    // Back button returns to the list.
    await page.getByRole('button', { name: /hosts/i }).first().click();
    await expect(page.getByTestId('mobile-hosts')).toBeVisible();
  });

  test('vms tab lists VMs, filters, and opens VM detail', async ({ page }) => {
    await loginAsAdminMobile(page);
    await page.getByRole('link', { name: /^vms$/i }).first().click();
    await expect(page.getByTestId('mobile-vms')).toBeVisible();
    // Type into the filter — the demo cluster has many VMs, so any
    // single character should narrow the list.
    await page.locator('input.m-search').fill('a');
    // First card after filtering is the drill-in target.
    const firstVM = page.locator('.m-card-link').first();
    await firstVM.waitFor({ state: 'visible' });
    await firstVM.click();
    await expect(page.getByTestId('mobile-vm-detail')).toBeVisible();
    // Resources card always renders.
    await expect(page.getByText(/resources/i).first()).toBeVisible();
  });

  test('activity tab renders the audit feed', async ({ page }) => {
    await loginAsAdminMobile(page);
    await page.getByRole('link', { name: /activity/i }).first().click();
    await expect(page.getByTestId('mobile-activity')).toBeVisible();
    // login event from this very test must be in the recent feed.
    await expect(page.locator('.m-card').first()).toBeVisible();
  });

  test('header has no switch-to-desktop button — mobile is canonical', async ({ page }) => {
    await loginAsAdminMobile(page);
    await expect(page.getByRole('button', { name: /switch to desktop/i })).toHaveCount(0);
    // Sign-out remains.
    await expect(page.getByRole('button', { name: /sign out/i })).toBeVisible();
  });
});
