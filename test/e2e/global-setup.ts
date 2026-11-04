// The launch link works once (SECURITY S2: the token rotates on use), so it
// is exchanged here, once, by the UI itself, and every spec reads the session
// key from HEARTH_KEY.
import { chromium, expect, type FullConfig } from '@playwright/test';

export default async function globalSetup(config: FullConfig): Promise<void> {
  const base = process.env.HEARTH_URL ?? '';
  const browser = await chromium.launch(config.projects[0]?.use.launchOptions ?? { executablePath: '/usr/bin/chromium', args: ['--no-sandbox'] });
  const page = await browser.newPage();
  await page.goto(base);
  // /auth redirects to /#k=<key>; the UI stores the key and strips the hash.
  await page.waitForFunction(() => localStorage.getItem('hearth.session') !== null);
  const key = await page.evaluate(() => localStorage.getItem('hearth.session') ?? '');
  expect(page.url()).not.toContain('#k=');
  expect(key).not.toBe('');
  process.env.HEARTH_KEY = key;
  await browser.close();
}
