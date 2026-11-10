// Accessibility audit on fixtures (ROADMAP 8.10): axe-core on every page of
// Ember and every Phase 9 and 10 fixture screen, dark and light, left to right
// and right to left. Fails on any serious or critical violation. Pages with no
// fixture show their error state, which is audited too. Not part of
// `make itest`; tests/a11y.spec.ts runs the same check on the real hearthd.
//   cd web && npx vite --port 5199
//   cd test/e2e && BROWSER=/usr/bin/brave-browser node design/a11y.mjs [states] [route...]
import { chromium } from 'playwright-core';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { api, sample, services, snapshot } from './fixtures.mjs';

const axeSource = readFileSync(createRequire(import.meta.url).resolve('axe-core/axe.min.js'), 'utf8');
const base = process.env.DESIGN_URL ?? 'http://127.0.0.1:5199';
// No argument: everything. "states": the opened states only. Otherwise the
// routes named.
const args = process.argv.slice(2);
const all = args.length === 0;
const routes = all
  ? ['', 'resources', 'disks', 'apps', 'games', 'services', 'docker', 'packages', 'tools', 'space', 'hardware', 'network', 'terminal', 'scheduler', 'insight', 'journal', 'alerts', 'security', 'tokens', 'modes', 'changes', 'reports', 'settings']
  : args.filter((a) => a !== 'states');
const screens = ['apps-list', 'app-drawer', 'app-page', 'import', 'deploy', 'backup', 'modes', 'mode-editor', 'mode-revert', 'approval'];

const browser = await chromium.launch({ executablePath: process.env.BROWSER ?? '/usr/bin/chromium', args: ['--no-sandbox'] });

async function open(scheme, lang) {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 }, colorScheme: scheme, reducedMotion: 'reduce' });
  await ctx.addInitScript(
    ([l, t]) => {
      localStorage.setItem('hearth.session', 'a'.repeat(43));
      localStorage.setItem('hearth.lang', l);
      localStorage.setItem('hearth.theme', t);
    },
    [lang, scheme],
  );
  const page = await ctx.newPage();
  await page.route('**/api/v1/**', (route) => {
    const path = new URL(route.request().url()).pathname.replace('/api/v1', '');
    if (path in api) return route.fulfill({ json: api[path] });
    return route.fulfill({ status: 404, json: { error: 'no fixture' } });
  });
  await page.routeWebSocket(/\/api\/v1\/ws$/, (ws) => {
    ws.onMessage((raw) => {
      const m = JSON.parse(String(raw));
      if (m.op !== 'sub') return;
      const data = { metrics: sample, services, docker: snapshot }[m.topic];
      if (data) ws.send(JSON.stringify({ topic: m.topic, data }));
    });
  });
  return { ctx, page };
}

/** Serious and critical violations on the page as it stands. */
async function audit(page) {
  await page.evaluate(axeSource);
  const results = await page.evaluate(() => window.axe.run(document, { resultTypes: ['violations'] }));
  return results.violations
    .filter((v) => v.impact === 'serious' || v.impact === 'critical')
    .map((v) => `${v.impact} ${v.id}: ${v.help}\n${v.nodes.slice(0, 4).map((n) => `      ${n.target.join(' ')} ${n.failureSummary?.split('\n')[1]?.trim() ?? ''}`).join('\n')}`);
}

// Opened states: the drawer, the palette, the shortcuts dialog, a row menu.
const states = [
  ['drawer unit', '#/services?inspect=unit:system/ssh.service'],
  ['drawer container', `#/docker?inspect=container:${'a1'.repeat(32)}`],
  ['drawer port', '#/network?inspect=port:tcp/22'],
  ['palette', '#/', (page) => page.keyboard.press('Control+k')],
  ['shortcuts', '#/', (page) => page.keyboard.press('?')],
  ['row menu', '#/services', (page) => page.locator('tbody button[aria-label]').first().click()],
];

let failed = 0;
async function check(label, url, act) {
  for (const scheme of ['dark', 'light'])
    for (const lang of ['en', 'ar']) {
      const { ctx, page } = await open(scheme, lang);
      await page.goto(url);
      await page.waitForTimeout(700);
      if (act) {
        await act(page);
        await page.waitForTimeout(300);
      }
      // An opened state must really be open, or its audit proves nothing.
      if (url.includes('inspect=') || act) {
        const open = await page.locator('dialog[open], [role="dialog"]:visible, [popover]:popover-open').count();
        if (!open) {
          failed++;
          console.log(`✗ ${label} ${scheme} ${lang}: nothing opened`);
        }
      }
      const found = await audit(page);
      if (found.length) {
        failed += found.length;
        console.log(`✗ ${label} ${scheme} ${lang}\n  ${found.join('\n  ')}`);
      } else console.log(`✓ ${label} ${scheme} ${lang}`);
      await ctx.close();
    }
}

for (const r of routes) await check(`#/${r}`, `${base}/#/${r}`);
if (all || args.includes('states')) for (const [label, hash, act] of states) await check(label, `${base}/${hash}`, act);
if (all) for (const s of screens) await check(`fixture ${s}`, `${base}/design.html?screen=${s}`);

await browser.close();
if (failed) {
  console.error(`${failed} serious or critical violation(s)`);
  process.exit(1);
}
console.log('no serious or critical violations');
