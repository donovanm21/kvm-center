import { test, expect } from '@playwright/test';
import { ADMIN_PASSWORD, ADMIN_USERNAME } from './_helpers';

// A green browser run against an old/local deployment is not evidence for the
// candidate. The validation runner supplies this SHA and the fixture build
// stamps main.GitSHA. No reusable production credential is involved.
test('fixture runs the exact candidate revision', async ({ request }) => {
  const revision = process.env.KVMC_EXPECTED_SHA;
  expect(revision, 'Set KVMC_EXPECTED_SHA to the candidate git revision').toMatch(/^[a-f0-9]{40}$/);
  const login = await request.post('/api/auth/login', {
    data: { username: ADMIN_USERNAME, password: ADMIN_PASSWORD },
  });
  expect(login.ok()).toBeTruthy();
  const health = await request.get('/api/admin/system-health');
  expect(health.ok()).toBeTruthy();
  const status = await health.json();
  expect(status.control_plane.detail.git_sha).toBe(revision);
  expect(status.database.detail.type).toBe(process.env.KVMC_EXPECTED_BACKEND ?? 'mongo');
});
