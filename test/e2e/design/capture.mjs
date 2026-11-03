// Screenshots for docs/design/screens (ROADMAP 8.1, 8.11), from the Vite dev
// server on fixtures only: a request with no fixture stops the run. Not part
// of `make itest`. The old UI is gone since 8.11, so a before-and-after pair
// takes its start side from a second dev server on a worktree of 9c3b245, the
// last commit with it (BEFORE_URL); without one, each page is Ember alone.
//   cd web && npx vite --port 5199
//   git worktree add /tmp/before 9c3b245 && ln -s "$PWD/web/node_modules" /tmp/before/web/
//   cd /tmp/before/web && npx vite --port 5198
//   cd test/e2e && BEFORE_URL=http://127.0.0.1:5198 BROWSER=/usr/bin/brave-browser node design/capture.mjs <outdir>
import { chromium } from 'playwright-core';
import { mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { api, sample, services, snapshot } from './fixtures.mjs';

const base = process.env.DESIGN_URL ?? 'http://127.0.0.1:5199';
const before = process.env.BEFORE_URL ?? '';
const out = process.argv[2];
if (!out) throw new Error('usage: capture.mjs <outdir>');
mkdirSync(out, { recursive: true });

const browser = await chromium.launch({ executablePath: process.env.BROWSER ?? '/usr/bin/chromium', args: ['--no-sandbox'] });
const size = { width: 1280, height: 800 };
const missing = new Set();

async function open(scheme, density, lang) {
  const ctx = await browser.newContext({ viewport: size, colorScheme: scheme, reducedMotion: 'reduce' });
  await ctx.addInitScript(
    ([d, l, t]) => {
      localStorage.setItem('hearth.session', 'a'.repeat(43));
      localStorage.setItem('hearth.density', d);
      localStorage.setItem('hearth.lang', l);
      localStorage.setItem('hearth.theme', t);
    },
    [density, lang, scheme],
  );
  const page = await ctx.newPage();
  await page.route('**/api/v1/**', (route) => {
    const path = new URL(route.request().url()).pathname.replace('/api/v1', '');
    if (path in api) return route.fulfill({ json: api[path] });
    missing.add(path);
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

const pages = [
  ['home', ''],
  ['containers', 'docker'],
  ['services', 'services'],
  ['network', 'network'],
  ['security', 'security'],
];

async function pair(name, hash, scheme, density, lang) {
  const shots = [];
  for (const url of before ? [before, base] : [base]) {
    const { ctx, page } = await open(scheme, density, lang);
    await page.goto(`${url}/#/${hash}`);
    await page.waitForTimeout(900);
    shots.push((await page.screenshot({ type: 'jpeg', quality: 80 })).toString('base64'));
    await ctx.close();
  }
  // The old UI on the start side, Ember beside it, on one image.
  const { ctx, page } = await open('dark', 'comfortable', 'en');
  await page.setViewportSize({ width: (size.width + 8) * shots.length - 8, height: size.height });
  await page.setContent(
    `<body style="margin:0;background:#888;display:flex;gap:8px">${shots.map((s) => `<img style="display:block" src="data:image/jpeg;base64,${s}">`).join('')}</body>`,
  );
  await page.screenshot({ path: join(out, `${name}.${scheme}.${density}.${lang === 'ar' ? 'rtl' : 'ltr'}.jpg`), type: 'jpeg', quality: 78 });
  await ctx.close();
}

for (const [name, hash] of pages)
  for (const scheme of ['dark', 'light'])
    for (const density of ['comfortable', 'compact'])
      for (const lang of ['en', 'ar']) await pair(name, hash, scheme, density, lang);

const screens = ['apps-list', 'app-drawer', 'app-page', 'import', 'deploy', 'backup', 'modes', 'mode-editor', 'mode-revert', 'approval'];
for (const screen of screens)
  for (const scheme of ['dark', 'light'])
    for (const lang of ['en', 'ar']) {
      const { ctx, page } = await open(scheme, 'comfortable', lang);
      await page.goto(`${base}/design.html?screen=${screen}`);
      await page.waitForTimeout(500);
      await page.screenshot({ path: join(out, `fixture.${screen}.${scheme}.${lang === 'ar' ? 'rtl' : 'ltr'}.jpg`), type: 'jpeg', quality: 80 });
      await ctx.close();
    }

await browser.close();
if (missing.size > 0) {
  console.error('requests with no fixture:', [...missing].join(' '));
  process.exit(1);
}
console.log('done');
