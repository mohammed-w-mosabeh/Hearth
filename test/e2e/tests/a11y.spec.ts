// Accessibility end to end (ROADMAP 8.10, Gate 8): axe-core on every Ember
// page, dark and light, left to right and right to left, with no serious or
// critical violation; a visible focus ring in both themes; the palette, the
// shortcuts list and a confirmation work from the keyboard alone. The drawer's
// keys are in xss.spec.ts. Runs in the itest container against the real
// hearthd, with the strings xss.spec.ts and check.sh seeded.
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { expect, test, type Page } from '@playwright/test';

const axeSource = readFileSync(createRequire(import.meta.url).resolve('axe-core/axe.min.js'), 'utf8');
const base = process.env.HEARTH_URL ?? '';
const origin = new URL(base).origin;

const routes = ['', 'resources', 'disks', 'apps', 'games', 'services', 'docker', 'stacks', 'packages', 'tools', 'space', 'removed', 'hardware', 'network', 'terminal', 'scheduler', 'insight', 'journal', 'alerts', 'security', 'tokens', 'modes', 'automations', 'changes', 'reports', 'settings'];

// Not serial: one page failing must not hide what the other tests find.

let key = '';
let jobId = '';

test.beforeAll(async ({ browser }) => {
  const page = await browser.newPage();
  key = process.env.HEARTH_KEY ?? '';
  expect(key).not.toBe('');
  // A job to ask about deleting; the confirmation is cancelled, never run.
  const job = await page.request.post(`${origin}/api/v1/jobs/user`, {
    headers: { Authorization: `Bearer ${key}`, Origin: origin, 'Content-Type': 'application/json' },
    data: { name: 'a11y-probe', script: '#!/bin/sh\ntrue\n', timeout: 60, notify: false, schedule: { kind: 'calendar', value: '*-*-* 05:00:00' } },
  });
  expect(job.ok(), await job.text()).toBeTruthy();
  jobId = ((await job.json()) as { id: string }).id;
  await page.close();
});

async function start(page: Page, theme: string, lang: string): Promise<void> {
  page.on('dialog', (d) => {
    throw new Error(`a dialog opened: ${d.message()}`);
  });
  await page.goto(`${origin}/#/`);
  await page.evaluate(
    ([k, th, l]) => {
      for (const name of Object.keys(localStorage)) localStorage.removeItem(name);
      localStorage.setItem('hearth.session', k);
      localStorage.setItem('hearth.theme', th);
      localStorage.setItem('hearth.lang', l);
    },
    [key, theme, lang],
  );
}

/** Tab never reaches the page behind an open modal dialog. Past the last
 *  control Chromium hands focus to the browser's own UI, where activeElement
 *  is <body>; that is allowed, any page element outside the dialog is not. */
async function tabStaysInDialog(page: Page): Promise<void> {
  let inside = 0;
  for (let i = 0; i < 8; i++) {
    await page.keyboard.press('Tab');
    const where = await page.evaluate(() => {
      const el = document.activeElement;
      if (!el || el === document.body) return 'browser';
      return el.closest('dialog[open]') ? 'dialog' : `page: ${el.outerHTML.slice(0, 80)}`;
    });
    expect(where, `Tab ${i + 1} stays out of the page`).toMatch(/^(dialog|browser)$/);
    if (where === 'dialog') inside++;
  }
  expect(inside, 'Tab cycles through the dialog').toBeGreaterThan(0);
}

/** Serious and critical axe violations, one line each. */
async function violations(page: Page): Promise<string[]> {
  // Through the DevTools protocol, so the page's CSP (S6) stays as it is.
  await page.evaluate(axeSource);
  const r = await page.evaluate(() => (window as unknown as { axe: { run: (c: Document, o: object) => Promise<{ violations: { id: string; impact: string; nodes: { target: string[] }[] }[] }> } }).axe.run(document, { resultTypes: ['violations'] }));
  return r.violations
    .filter((v) => v.impact === 'serious' || v.impact === 'critical')
    .map((v) => `${v.impact} ${v.id}: ${v.nodes.map((n) => n.target.join(' ')).slice(0, 3).join(', ')}`);
}

for (const theme of ['dark', 'light'])
  for (const lang of ['en', 'ar'])
    test(`axe: every page, ${theme}, ${lang === 'ar' ? 'right to left' : 'left to right'}`, async ({ page }) => {
      test.setTimeout(240_000);
      await start(page, theme, lang);
      const found: string[] = [];
      for (const r of routes) {
        await page.goto(`${origin}/#/${r}`);
        await page.reload();
        await expect(page.locator('html')).toHaveAttribute('dir', lang === 'ar' ? 'rtl' : 'ltr');
        await page.waitForTimeout(800);
        for (const v of await violations(page)) found.push(`#/${r}: ${v}`);
      }
      expect(found).toEqual([]);
    });

for (const theme of ['dark', 'light'])
  for (const lang of ['en', 'ar'])
    test(`axe: an app's drawer, ${theme}, ${lang === 'ar' ? 'right to left' : 'left to right'}`, async ({ page }) => {
      await start(page, theme, lang);
      await page.goto(`${origin}/#/stacks`);
      await page.reload();
      await page.getByRole('button', { name: 'xssapp', exact: true }).click();
      await expect(page.locator('dialog[open]')).toBeVisible({ timeout: 15_000 });
      await page.waitForTimeout(800);
      expect(await violations(page)).toEqual([]);
    });

for (const theme of ['dark', 'light'])
  test(`the focus ring shows in ${theme}`, async ({ page }) => {
    await start(page, theme, 'en');
    await page.reload();
    await expect(page.locator('nav a').first()).toBeVisible();
    for (let i = 0; i < 3; i++) {
      await page.keyboard.press('Tab');
      const ring = await page.evaluate(() => {
        const el = document.activeElement as HTMLElement | null;
        if (!el || el === document.body) return null;
        const s = getComputedStyle(el);
        return { style: s.outlineStyle, width: parseFloat(s.outlineWidth), color: s.outlineColor };
      });
      expect(ring, `Tab ${i + 1} reached something`).not.toBeNull();
      expect(ring?.style).not.toBe('none');
      expect(ring?.width).toBeGreaterThanOrEqual(2);
    }
  });

test('the palette and the shortcuts list work from the keyboard', async ({ page }) => {
  await start(page, 'dark', 'en');
  await page.reload();
  await expect(page.locator('nav a').first()).toBeVisible();
  await page.keyboard.press('Control+k');
  const palette = page.locator('dialog[open]');
  await expect(palette).toBeVisible();
  expect(await page.evaluate(() => !!document.activeElement?.closest('dialog[open]'))).toBe(true);
  await page.keyboard.type('services');
  await page.keyboard.press('Enter');
  await expect(palette).toHaveCount(0);
  expect(page.url()).toContain('#/services');

  await page.locator('main').click({ position: { x: 5, y: 5 } });
  await page.keyboard.press('?');
  const keys = page.locator('dialog[open]');
  await expect(keys).toBeVisible();
  await tabStaysInDialog(page);
  await page.keyboard.press('Escape');
  await expect(keys).toHaveCount(0);
});

test('a confirmation is keyboard operable and Esc cancels it', async ({ page }) => {
  await start(page, 'dark', 'en');
  await page.goto(`${origin}/#/scheduler`);
  await page.reload();
  // The row's ⋯ menu, then Delete in it, all by keyboard.
  const more = page.getByRole('button', { name: 'Actions for a11y-probe', exact: true });
  await expect(more).toBeVisible({ timeout: 15_000 });
  await more.focus();
  await page.keyboard.press('Enter');
  const del = page.locator('[popover]:popover-open').getByRole('button', { name: 'Delete' });
  await expect(del).toBeVisible();
  await del.focus();
  await page.keyboard.press('Enter');
  const dialog = page.locator('dialog[open]');
  await expect(dialog).toBeVisible();
  await tabStaysInDialog(page);
  expect(await violations(page)).toEqual([]);
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
  // Focus goes back to the row's ⋯ button: the menu hands it back as it
  // closes, and the dialog returns it there.
  expect(await page.evaluate(() => document.activeElement?.getAttribute('aria-label') ?? '')).toMatch(/a11y-probe/);
  // Cancelled: the job is still there.
  const list = await page.request.get(`${origin}/api/v1/jobs`, { headers: { Authorization: `Bearer ${key}` } });
  expect(await list.text()).toContain(jobId);
});
