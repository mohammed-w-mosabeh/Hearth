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
let jobUnit = '';

test.beforeAll(async ({ browser }) => {
  const page = await browser.newPage();
  // Exchanged once by global-setup.ts: the launch link works only once.
  key = process.env.HEARTH_KEY ?? '';
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
  jobUnit = `user/hearth-job-${id}.service`;
  const run = await post('actions/job/run', { target: `user/${id}` });
  expect(run.ok(), await run.text()).toBeTruthy();
  // Its output, the payload, lands in the journal for the history view.
  await expect
    .poll(async () => (await page.request.get(`${origin}/api/v1/jobs/user/${id}/history`, { headers: { Authorization: `Bearer ${key}` } })).text(), { timeout: 20_000 })
    .toContain('onerror');
  const snap = await post('changes/snapshots', { label: PAYLOAD });
  expect(snap.ok(), await snap.text()).toBeTruthy();
  const profile = await post('profiles', { name: PAYLOAD, items: [{ kind: 'app', target: 'org.example.Probe', do: 'start' }] });
  expect(profile.ok(), await profile.text()).toBeTruthy();
  // An automation's name, step label and notification text (ROADMAP 12.9).
  const automation = await post('automations', {
    name: PAYLOAD,
    enabled: false,
    spec: { trigger: { kind: 'login' }, then: [{ step: 'notify', channels: ['desktop'], text: PAYLOAD, label: PAYLOAD }], options: { cooldown_s: 300, max_per_hour: 6 } },
  });
  expect(automation.ok(), await automation.text()).toBeTruthy();
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
  { name: 'Apps detected Compose project', hash: '#/stacks', text: PAYLOAD },
  { name: 'Scheduler job name', hash: '#/scheduler', text: PAYLOAD },
  { name: 'Hardware device name', hash: '#/hardware', text: PAYLOAD },
  { name: 'Alerts rule name', hash: '#/alerts', text: PAYLOAD.slice(0, 80) },
  { name: 'Packages description', hash: '#/packages?q=hearth-xss-probe', text: PAYLOAD },
  { name: 'Network process name', hash: '#/network?q=7778', text: COMM },
  { name: 'Games name', hash: '#/games', text: PAYLOAD },
  { name: 'Settings autostart entry', hash: '#/settings', text: PAYLOAD },
  { name: 'Modes name', hash: '#/modes', text: PAYLOAD },
  { name: 'Automations name', hash: '#/automations', text: PAYLOAD },
  { name: 'Space advisor cache folder', hash: '#/space', text: PAYLOAD },
  { name: 'Uninstall plan of a hostile game', hash: '#/removed?item=game:steam.9', text: PAYLOAD },
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

test('an import dry-run of a hostile project shows its name as text', async ({ page }) => {
  await page.goto(`${origin}/#/stacks`);
  await page.reload();
  const card = page.locator('li.card').filter({ hasText: PAYLOAD }).first();
  await card.getByRole('button', { name: 'Check import' }).click();
  await expect(page.locator('.importing').getByText('Refused')).toBeVisible({ timeout: 15_000 });
  await page.waitForTimeout(500);
  await noScriptRan(page);
});

test("a managed app's drawer shows its spec as text", async ({ page }) => {
  await page.goto(`${origin}/#/stacks`);
  await page.reload();
  await page.getByRole('button', { name: 'xssapp', exact: true }).click();
  await expect(page.getByText(PAYLOAD, { exact: false }).first()).toBeVisible({ timeout: 15_000 });
  await page.waitForTimeout(500);
  await noScriptRan(page);
});

test('the job history shows script output as text', async ({ page }) => {
  await page.goto(`${origin}/#/scheduler`);
  await page.reload();
  await page.getByRole('button', { name: PAYLOAD, exact: true }).click();
  await expect(page.getByRole('dialog').getByText(PAYLOAD).first()).toBeVisible({ timeout: 15_000 });
  await noScriptRan(page);
});

test('the journal shows messages as text', async ({ page }) => {
  await page.goto(`${origin}/#/journal`);
  await page.reload();
  await page.locator('label:has-text("Severity") select').selectOption({ label: 'Everything' });
  await page.locator('label:has-text("When") select').selectOption({ label: 'Any time' });
  await page.getByLabel('Search').fill('onerror');
  await page.getByRole('button', { name: 'Show', exact: true }).click();
  await expect(page.locator('.entries').getByText(PAYLOAD).first()).toBeVisible();
  await noScriptRan(page);
});

test('a snapshot label shows as text', async ({ page }) => {
  await page.goto(`${origin}/#/changes`);
  await page.reload();
  await page.getByRole('radio', { name: 'Snapshots' }).click();
  await expect(page.locator('.kept').getByText(PAYLOAD)).toBeVisible();
  await noScriptRan(page);
});

test('a chart event shows as text', async ({ page }) => {
  // A rule that fires at once, so an alert named with the payload is a marker.
  const made = await page.request.post(`${origin}/api/v1/alerts/rules`, {
    headers: { Authorization: `Bearer ${key}`, Origin: origin, 'Content-Type': 'application/json' },
    data: { name: PAYLOAD.slice(0, 80), metric: 'cpu.usage', target: '', op: '>', threshold: 0, for_s: 0, severity: 'info', enabled: true, quiet_from: '', quiet_to: '' },
  });
  expect(made.ok(), await made.text()).toBeTruthy();
  await page.goto(`${origin}/#/resources`);
  await page.reload();
  await expect
    .poll(
      async () => {
        const end = Date.now();
        return (await page.request.get(`${origin}/api/v1/markers?from=${end - 3_600_000}&to=${end}`, { headers: { Authorization: `Bearer ${key}` } })).text();
      },
      { timeout: 20_000 },
    )
    .toContain('onerror');
  await page.reload();
  await page.locator('details.markers summary').click();
  await expect(page.locator('details.markers').getByText(PAYLOAD).first()).toBeVisible();
  await noScriptRan(page);
});

test('a crash shows as text', async ({ page }) => {
  await page.goto(`${origin}/#/journal`);
  await page.reload();
  await expect(page.locator('.crashes').getByText(PAYLOAD).first()).toBeVisible();
  await noScriptRan(page);
});

test('an inhibitor shows as text', async ({ page }) => {
  await page.goto(`${origin}/#/settings`);
  await page.reload();
  await expect(page.locator('.sleep').getByText(PAYLOAD).first()).toBeVisible();
  await noScriptRan(page);
});

test('the log explorer shows lines as text', async ({ page }) => {
  await page.goto(`${origin}/#/journal`);
  await page.reload();
  await page.getByRole('radio', { name: 'Log explorer' }).click();
  await page.getByLabel('Service, like system/cups.service').fill(jobUnit);
  await page.getByRole('button', { name: 'Add', exact: true }).click();
  await expect(page.locator('.lines').getByText(PAYLOAD).first()).toBeVisible({ timeout: 15_000 });
  await noScriptRan(page);
});

test('a firewall rule shows as text', async ({ page }) => {
  await page.goto(`${origin}/#/network`);
  await page.reload();
  const card = page.locator('section[aria-labelledby="fw-title"]');
  await expect(card.getByText('onerror').first()).toBeVisible({ timeout: 15_000 });
  await expect(card.getByRole('button', { name: 'Delete' })).toHaveCount(0); // read-only
  await noScriptRan(page);
});

test('an integrity change shows as text', async ({ page }) => {
  await page.goto(`${origin}/#/security`);
  await page.reload();
  const card = page.locator('section[aria-labelledby="integrity-title"]');
  await expect(card.getByText(PAYLOAD).first()).toBeVisible({ timeout: 15_000 });
  await noScriptRan(page);
});

test('a hostile group member shows as text', async ({ page }) => {
  await page.goto(`${origin}/#/security`);
  await page.reload();
  const card = page.locator('section[aria-labelledby="access-title"]');
  await expect(card.getByText(PAYLOAD).first()).toBeVisible({ timeout: 15_000 });
  await noScriptRan(page);
});

test('a Lynis finding shows as text', async ({ page }) => {
  await page.goto(`${origin}/#/security`);
  await page.reload();
  const card = page.locator('section[aria-labelledby="lynis-title"]');
  await expect(card.getByText(PAYLOAD).first()).toBeVisible({ timeout: 15_000 });
  // Only the planted row: real Lynis may also report findings that have a page.
  const row = card.locator('li').filter({ hasText: 'AUTH-9999' }).first();
  await expect(row.getByRole('link', { name: 'Open in Hearth' })).toHaveCount(0); // AUTH has no page
  await noScriptRan(page);
});

test('every sidebar page is its real page', async ({ page }) => {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(e.message));
  await page.goto(`${origin}/#/`);
  await page.reload();
  const links = page.locator('nav ul a');
  await expect(links.first()).toBeVisible();
  const hrefs = await links.evaluateAll((els) => els.map((a) => a.getAttribute('href') ?? ''));
  expect(hrefs.length).toBeGreaterThanOrEqual(21);
  for (const h of hrefs) {
    await page.goto(`${origin}/${h}`);
    await page.waitForTimeout(300);
    const text = (await page.locator('main').textContent()) ?? '';
    expect(text, h).not.toMatch(/arrives in phase|Page not found/);
    expect(text.trim().length, h).toBeGreaterThan(0);
  }
  expect(errors).toEqual([]);
});

test('Settings shows the build version', async ({ page }) => {
  await page.goto(`${origin}/#/settings`);
  await page.reload();
  await expect(page.getByTestId('version')).toHaveText(process.env.HEARTH_VERSION ?? 'unset');
});

// ROADMAP 5.9: every page, right to left, with the bundled font.
test('every page works right to left in Arabic', async ({ page }) => {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(e.message));
  page.on('console', (m) => {
    if (m.type() === 'error' && /Content Security Policy|font/i.test(m.text())) errors.push(m.text());
  });
  await page.evaluate(() => localStorage.setItem('hearth.lang', 'ar'));
  await page.goto(`${origin}/#/`);
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
  await expect(page.locator('html')).toHaveAttribute('lang', 'ar');
  const links = page.locator('nav ul a');
  await expect(links.first()).toBeVisible();
  const hrefs = await links.evaluateAll((els) => els.map((a) => a.getAttribute('href') ?? ''));
  expect(hrefs.length).toBeGreaterThanOrEqual(21);
  for (const h of hrefs) {
    await page.goto(`${origin}/${h}`);
    await page.waitForTimeout(300);
    const shape = await page.evaluate(() => ({
      arabic: /[\u0600-\u06FF]/.test(document.querySelector('main')?.textContent ?? ''),
      sideways: document.documentElement.scrollWidth - document.documentElement.clientWidth,
      nav: document.querySelector('nav')!.getBoundingClientRect().left,
      main: document.querySelector('main')!.getBoundingClientRect().left,
    }));
    expect(shape.arabic, `${h}: the page is in Arabic`).toBe(true);
    expect(shape.sideways, `${h}: no sideways scroll`).toBeLessThanOrEqual(0);
    expect(shape.nav, `${h}: the sidebar is on the right`).toBeGreaterThan(shape.main);
  }
  await page.evaluate(() => document.fonts.ready);
  const font = await page.evaluate(() => [...document.fonts].some((f) => f.family.includes('Plex') && f.status === 'loaded'));
  expect(font, 'the bundled font loaded').toBe(true);
  expect(errors).toEqual([]);
});

test('the language switch goes back to English', async ({ page }) => {
  await page.evaluate(() => localStorage.setItem('hearth.lang', 'ar'));
  await page.goto(`${origin}/#/settings`);
  await page.reload();
  await page.getByTestId('lang-en').click();
  await expect(page.locator('html')).toHaveAttribute('dir', 'ltr');
  await expect(page.locator('main h1')).toHaveText('Settings');
});

// ROADMAP 8.7: the Ember inspector drawer shows the same strings as text,
// survives a reload, closes on Esc and gives focus back.
test('the drawer shows a listener, its process and a package as text', async ({ page }) => {
  await page.goto(`${origin}/#/network?q=7778`);
  await page.reload();
  const what = page.locator('button.what').first();
  await what.click();
  const drawer = page.locator('dialog.drawer[open]');
  await expect(drawer.getByText(COMM).first()).toBeVisible({ timeout: 15_000 });
  await noScriptRan(page);

  await page.goto(`${origin}/#/packages?inspect=package:hearth-xss-probe`);
  await page.reload();
  await expect(drawer.getByText(PAYLOAD).first()).toBeVisible({ timeout: 15_000 });
  await page.waitForTimeout(500);
  await noScriptRan(page);
});

test('the drawer shows a container as text', async ({ page }) => {
  await page.goto(`${origin}/#/docker`);
  await page.reload();
  await page.getByRole('button', { name: PAYLOAD }).first().click();
  await expect(page.locator('dialog.drawer[open] #inspect-title')).toHaveText(PAYLOAD, { timeout: 15_000 });
  await noScriptRan(page);
});

test("a container's details show it as text", async ({ page }) => {
  // Details in the row's menu: logs, environment, labels and inspect.
  await page.goto(`${origin}/#/docker`);
  await page.reload();
  await page.getByRole('button', { name: `Actions for ${PAYLOAD}`, exact: true }).first().click();
  await page.locator('[popover]:popover-open').getByRole('button', { name: 'Details' }).click();
  await expect(page.locator('dialog[open] #detail-title')).toHaveText(PAYLOAD, { timeout: 15_000 });
  await page.waitForTimeout(500);
  await noScriptRan(page);
});

test('the drawer is a link, closes on Esc and gives focus back', async ({ page }) => {
  await page.goto(`${origin}/#/network?q=7778`);
  await page.reload();
  const what = page.locator('button.what').first();
  await what.focus();
  await page.keyboard.press('Enter');
  const drawer = page.locator('dialog.drawer[open]');
  await expect(drawer).toBeVisible();
  expect(page.url()).toContain('inspect=port');
  const title = await drawer.locator('#inspect-title').textContent();

  // Focus stays in the drawer.
  for (let i = 0; i < 12; i++) await page.keyboard.press('Tab');
  expect(await page.evaluate(() => !!document.activeElement?.closest('dialog.drawer'))).toBe(true);

  await page.reload();
  await expect(drawer.locator('#inspect-title')).toHaveText(title ?? '');

  await page.keyboard.press('Escape');
  await expect(drawer).toHaveCount(0);
  // The dialog closes at once; its close event, which clears the link and
  // gives focus back, runs a task later.
  await expect(page).not.toHaveURL(/inspect=/);

  await what.focus();
  await page.keyboard.press('Enter');
  await expect(drawer).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(drawer).toHaveCount(0);
  await expect.poll(() => page.evaluate(() => document.activeElement?.classList.contains('what'))).toBe(true);

  // A lineage link opens that item in the drawer; Back returns.
  await what.click();
  const link = drawer.locator('.chain a').first();
  if ((await link.count()) > 0) {
    await link.click();
    await expect(drawer.locator('#inspect-title')).not.toHaveText(title ?? '');
    await page.goBack();
    await expect(drawer.locator('#inspect-title')).toHaveText(title ?? '');
  }
});

// ROADMAP 8.8: one row is a tab stop, the arrow keys move between rows and
// Enter opens the row in the drawer; the header stays in view.
test('list rows move with the keyboard and keep their header', async ({ page }) => {
  await page.goto(`${origin}/#/services`);
  await page.reload();
  const rows = page.locator('tbody tr');
  await expect(rows.first()).toBeVisible({ timeout: 15_000 });
  expect(await page.locator('tbody tr[tabindex="0"]').count()).toBe(1);
  await rows.first().focus();
  await page.keyboard.press('ArrowDown');
  // One evaluation: the list refreshes live and may reorder between two reads.
  const [name, index] = await page.evaluate(() => {
    const a = document.activeElement;
    return [a?.getAttribute('aria-label'), a?.parentElement ? [...a.parentElement.children].indexOf(a) : -1];
  });
  expect(index).toBe(1);
  await page.keyboard.press('Enter');
  await expect(page.locator('dialog.drawer[open] #inspect-title')).toHaveText(name ?? '');
  await page.keyboard.press('Escape');
  expect(await page.evaluate(() => document.activeElement?.getAttribute('aria-label'))).toBe(name);

  const box = page.locator('.table-scroll');
  await box.evaluate((el) => (el.scrollTop = el.scrollHeight));
  const stuck = await page.evaluate(() => {
    const s = document.querySelector('.table-scroll');
    const th = s?.querySelector('thead th');
    return !!s && !!th && Math.abs(th.getBoundingClientRect().top - s.getBoundingClientRect().top) < 2;
  });
  expect(stuck).toBe(true);
});
