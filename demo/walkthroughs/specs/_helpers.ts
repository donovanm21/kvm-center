import type { Page } from '@playwright/test';

// Shared helpers so the three walkthrough specs stay uniform:
//   * slowClick — hover + pause + click, so the viewer sees the
//                 pointer land on the target before the action fires.
//   * installCursor — inject a synthetic pointer so the WebM
//                     recording shows where clicks go. Playwright's
//                     default video does not include the OS cursor.
//   * login — shared sign-in routine.
//
// Durations are tuned for 10-12 fps docs clips — a full second of
// pause between steps reads naturally in a recording even though it
// feels slow live.

export async function installCursor(page: Page) {
  await page.addInitScript(() => {
    const style = document.createElement('style');
    style.textContent = `
      #__pw_cursor {
        position: fixed;
        top: 0; left: 0;
        width: 20px; height: 20px;
        border-radius: 50%;
        background: rgba(255, 120, 60, 0.85);
        box-shadow: 0 0 0 2px rgba(255,255,255,0.9), 0 2px 8px rgba(0,0,0,0.6);
        pointer-events: none;
        z-index: 2147483647;
        transform: translate(-50%, -50%);
        transition: transform 60ms linear;
      }
      #__pw_cursor.click {
        background: rgba(255, 255, 255, 0.95);
        box-shadow: 0 0 0 3px rgba(255,120,60,0.8);
      }
    `;
    document.head.appendChild(style);
    const dot = document.createElement('div');
    dot.id = '__pw_cursor';
    document.body.appendChild(dot);
    window.addEventListener('mousemove', (e) => {
      dot.style.left = e.clientX + 'px';
      dot.style.top = e.clientY + 'px';
    }, true);
    window.addEventListener('mousedown', () => { dot.classList.add('click'); }, true);
    window.addEventListener('mouseup', () => {
      setTimeout(() => dot.classList.remove('click'), 150);
    }, true);
  });
}

export async function slowClick(page: Page, locator, opts: { pre?: number; post?: number } = {}) {
  const pre = opts.pre ?? 700;
  const post = opts.post ?? 900;
  await locator.scrollIntoViewIfNeeded().catch(() => {});
  await locator.hover();
  await page.waitForTimeout(pre);
  await locator.click();
  await page.waitForTimeout(post);
}

export async function login(page: Page) {
  await page.goto('/login');
  await page.waitForLoadState('domcontentloaded');
  await page.locator('#login-username').fill('admin');
  await page.locator('#login-password').fill('demo-password');
  await page.waitForTimeout(400);
  await page.getByRole('button', { name: /sign in|log in|login/i }).click();
  // Wait for the app shell to appear — URL alone is unreliable
  // because the SPA keeps /login visible while auth is in flight.
  await page.getByRole('link', { name: /dashboard/i }).waitFor({ state: 'visible', timeout: 15_000 });
}
