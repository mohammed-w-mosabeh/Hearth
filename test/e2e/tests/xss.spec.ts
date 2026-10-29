// XSS end to end (SECURITY S6, Gate 3): system-derived strings that are
// HTML must show as text on every page, and nothing they contain may run.
// Runs inside the itest container against the installed hearthd, with
// Debian's chromium. HEARTH_URL is the launcher URL (/auth?t=<token>).
import { expect, test, type Page } from '@playwright/test';

// The UI keeps its session key here (web/src/lib/session.ts).
// The same payload the itest seeds everywhere (fakedocker, .deb, API).
const PAYLOAD = '<img src=x onerror="window.__xss=1">';
// Process names are cut to 15 bytes by the kernel.
const COMM = '<svg onload>';

const base = process.env.HEARTH_URL ?? '';
const origin = new URL(base).origin;

async function noScriptRan(page: Page): Promise<void> {
  expect(await page.evaluate(() => (window as unknown as { __xss?: number }).__xss)).toBeUndefined();
  // No element was made from the strings: no <img src=x>, no event handler
  // attribute anywhere in the document.
  expect(await page.locator('img[src="x"]').count()).toBe(0);
  const handlers = await page.evaluate(() =>
    [...document.querySelectorAll('*')].filter((el) => [...el.attributes].some((a) => a.name.startsWith('on'))).length,
  );
  expect(handlers).toBe(0);
}

test.describe.configure({ mode: 'serial' });

let key = '';

test.beforeAll(async ({ browser }) => {
  const page = await browser.newPage();
  await page.goto(base);
  // /auth redirects to /#k=<key>; the UI stores the key and strips the hash.
  await page.waitForFunction(() => localStorage.getItem('hearth.session') !== null);
  key = await page.evaluate(() => localStorage.getItem('hearth.session') ?? '');
  expect(key).not.toBe('');
  const post = (path: string, body: unknown) =>
    page.request.post(`${origin}/api/v1/${path}`, {
      headers: { Authorization: `Bearer ${key}`, Origin: origin, 'Content-Type': 'application/json' },
      data: body,
    });
  const tile = await post('homepage/tiles', { name: PAYLOAD, url: 'http://127.0.0.1:9/', description: PAYLOAD, icon: '', group: PAYLOAD, container: '', health_check: false });
  expect(tile.ok(), await tile.text()).toBeTruthy();
  const rule = await post('alerts/rules', { name: PAYLOAD.slice(0, 80), metric: 'cpu.usage', target: '', op: '>', threshold: 99, for_s: 0, severity: 'info', enabled: false, quiet_from: '', quiet_to: '' });
  expect(rule.ok(), await rule.text()).toBeTruthy();
  const job = await post('jobs/user', { name: PAYLOAD, script: '#!/bin/sh\necho "' + PAYLOAD.replace(/"/g, '\\"') + '"\n', timeout: 60, notify: false, schedule: { kind: 'calendar', value: '*-*-* 04:00:00' } });
  expect(job.ok(), await job.text()).toBeTruthy();
  const { id } = (await job.json()) as { id: string };
  const run = await post('actions/job/run', { target: `user/${id}` });
  expect(run.ok(), await run.text()).toBeTruthy();
  // Its output, the payload, lands in the journal for the history view.
  await expect
    .poll(async () => (await page.request.get(`${origin}/api/v1/jobs/user/${id}/history`, { headers: { Authorization: `Bearer ${key}` } })).text(), { timeout: 20_000 })
    .toContain('onerror');
  await page.close();
});

test.beforeEach(async ({ page }) => {
  page.on('dialog', (d) => {
    throw new Error(`a dialog opened: ${d.message()}`);
  });
  // The session key goes where the UI keeps it, then the app loads.
  await page.goto(`${origin}/#/`);
  await page.evaluate((k) => {
    for (const name of Object.keys(localStorage)) localStorage.removeItem(name);
    localStorage.setItem('hearth.session', k);
  }, key);
});

const pages: { name: string; hash: string; text: string }[] = [
  { name: 'Home tile', hash: '#/', text: PAYLOAD },
  { name: 'Docker container name, image, status and project', hash: '#/docker', text: PAYLOAD },
  { name: 'Scheduler job name', hash: '#/scheduler', text: PAYLOAD },
  { name: 'Alerts rule name', hash: '#/alerts', text: PAYLOAD.slice(0, 80) },
  { name: 'Packages description', hash: '#/packages?q=hearth-xss-probe', text: PAYLOAD },
  { name: 'Network process name', hash: '#/network?q=7778', text: COMM },
];

for (const p of pages) {
  test(`${p.name} renders as text`, async ({ page }) => {
    await page.goto(`${origin}/${p.hash}`);
    await page.reload();
    await expect(page.getByText(p.text, { exact: false }).first()).toBeVisible({ timeout: 15_000 });
    await page.waitForTimeout(500); // anything that would run, has
    await noScriptRan(page);
  });
}

test('the job history shows script output as text', async ({ page }) => {
  await page.goto(`${origin}/#/scheduler`);
  await page.reload();
  await page.getByRole('button', { name: PAYLOAD, exact: true }).click();
  await expect(page.getByRole('dialog').getByText(PAYLOAD).first()).toBeVisible({ timeout: 15_000 });
  await noScriptRan(page);
});
