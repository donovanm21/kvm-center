import { test, expect } from '@playwright/test';
import { loginAsAdmin } from './_helpers';

// Real authenticated API on the disposable Mongo-backed server. Collection is
// disabled in this fixture; populated collection/restart semantics are covered
// separately by the Go telemetry -> Mongo reconnect -> HTTP API integration.
test('history integrated API: disabled metadata and explicit range limits', async ({ page }) => {
  await loginAsAdmin(page);
  const [host] = await (await page.request.get('/api/hosts')).json();
  const path = `/api/hosts/${encodeURIComponent(host.id)}/history`;
  const response = await page.request.get(`${path}?range=48h`);
  expect(response.status()).toBe(200);
  const history = await response.json();
  expect(history.collection_enabled).toBe(false);
  expect(history.bucket_seconds).toBe(300);
  expect(history.retention_buckets).toBe(576);
  expect(history.max_hosts).toBe(64);
  expect(history.points).toEqual([]);
  expect((await page.request.get(`${path}?range=1y`)).status()).toBe(400);
  expect((await page.request.post(path)).status()).toBe(405);
});
