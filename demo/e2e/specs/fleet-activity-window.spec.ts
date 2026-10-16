import { test, expect } from '@playwright/test';
import { loginAsAdmin } from './_helpers';

// Synthetic API response: backend cohort/boundaries are independently tested in
// handlers_tasks_stats_test.go. These assertions cover the rendered semantics.
test('fleet activity includes the oldest partial hour and separates pending work', async ({ page }) => {
  const now = Date.parse('2026-01-15T12:30:00Z') / 1000;
  const since = now - 86400;
  await page.route('**/api/tasks/stats', route => route.fulfill({ json: {
    now, since, running: 2,
    by_outcome: { failed: 1, success: 1, pending: 1 },
    by_action: [{ action: 'START', count: 3, avg_ms: 60000 }],
    hourly: { [since - 1800]: 1, [now - 1800]: 2 },
  } }));
  await loginAsAdmin(page);
  const widget = page.locator('.fleet-activity-widget');
  await expect(widget).toContainText('submitted in last 24 h');
  for (const [label, count] of [['Pending now', '2'], ['Submitted (24 h)', '3'], ['Failed submissions (24 h)', '1']]) {
    await expect(widget.locator('.fleet-activity-stat').filter({ hasText: label }).locator('.fleet-activity-stat-value')).toHaveText(count);
  }
  await expect(widget.locator('svg rect')).toHaveCount(25);
  await expect(widget.locator('tbody tr td')).toHaveText(['START', '3', '1m 0s']);
});

test('fleet activity describes an empty window without claiming no history', async ({ page }) => {
  const now = Date.parse('2026-01-15T12:00:00Z') / 1000;
  await page.route('**/api/tasks/stats', route => route.fulfill({ json: {
    now, since: now - 86400, running: 0, by_outcome: {}, by_action: [], hourly: {},
  } }));
  await loginAsAdmin(page);
  await expect(page.locator('.fleet-activity-widget')).toHaveText('Fleet activityNo tasks submitted in the last 24 hours and no pending tasks.');
});
