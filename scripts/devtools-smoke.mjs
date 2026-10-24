// Optional browser smoke test. Requires Playwright and its Chromium browser.
// Run from the repository root. PLAYWRIGHT_PATH may point to an installed package.
import assert from 'node:assert/strict';
import { execFileSync, spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createInterface } from 'node:readline';

const require = createRequire(import.meta.url);
const { chromium } = require(process.env.PLAYWRIGHT_PATH || 'playwright');
const directory = await mkdtemp(join(tmpdir(), 'nodra-devtools-'));
const binary = join(directory, process.platform === 'win32' ? 'devtools.exe' : 'devtools');
let browser;
const children = [];

async function demo(restore) {
  const child = spawn(binary, ['-interval=1h', ...(restore ? ['-restore'] : [])], { stdio: ['ignore', 'pipe', 'pipe'] });
  children.push(child);
  const lines = createInterface({ input: child.stdout });
  const [url] = await Promise.race([
    once(lines, 'line'),
    once(child, 'exit').then(() => { throw new Error('Demo exited before starting'); })
  ]);
  lines.close();
  assert.match(url, /^http:\/\/127\.0\.0\.1:\d+\/#/);
  return url;
}

try {
  execFileSync('go', ['build', '-o', binary, './examples/devtools']);
  browser = await chromium.launch({ headless: true, ...(process.env.DEVTOOLS_CHROMIUM_PATH ? { executablePath: process.env.DEVTOOLS_CHROMIUM_PATH } : {}) });
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('dialog', dialog => dialog.accept());
  await page.goto(await demo(true));
  await page.getByText('Connected · local session', { exact: true }).waitFor();
  assert.equal(await page.locator('.store').count(), 1);
  assert.match(await page.locator('#state').innerText(), /"count": 1/);
  await page.locator('.event').filter({ hasText: 'rejected example' }).click();
  await page.locator('#tab-event').click();
  assert.match(await page.locator('#event').innerText(), /example validation failed/);
  await page.locator('#filter').fill('rejected');
  assert.equal(await page.locator('.event').count(), 1);
  await page.locator('#filter').fill('');
  await page.locator('#tab-state').click();
  await page.locator('.event[data-sequence="1"]').click();
  await page.locator('#restore').click();
  await page.waitForFunction(() => document.querySelector('#state').textContent.includes('"count": 0') && document.querySelector('#detail-meta').textContent.includes('Live state'));
  await page.locator('#editor summary').click();
  await page.locator('#json-input').fill('{"count":9}');
  await page.locator('#apply').click();
  await page.waitForFunction(() => document.querySelector('#state').textContent.includes('"count": 9'));
  await page.locator('#json-input').fill('{"count":"wrong"}');
  await page.locator('#apply').click();
  await page.locator('#message').filter({ hasText: 'invalid' }).waitFor();
  assert.match(await page.locator('#state').innerText(), /"count": 9/);
  await page.locator('#json-input').fill('{"count":123}');
  await page.locator('#detail-title').click();
  // Verify that polling does not discard an unsaved edit after blur.
  await page.waitForTimeout(1200);
  assert.equal(await page.locator('#json-input').inputValue(), '{"count":123}');
  const downloading = page.waitForEvent('download');
  await page.locator('#export').click();
  const download = await downloading;
  const exported = JSON.parse(await readFile(await download.path(), 'utf8'));
  assert.equal(exported.format, 1);
  assert.equal(exported.stores[0].state.count, 9);
  assert.ok(exported.events.some(event => event.error === 'example validation failed'));
  await page.locator('.event').first().click();
  await page.locator('#tab-changes').click();
  assert.match(await page.locator('#changes').innerText(), /\+ 9/);
  await page.locator('#editor summary').click();
  if (process.env.DEVTOOLS_SCREENSHOT) await page.screenshot({ path: process.env.DEVTOOLS_SCREENSHOT });
  await page.locator('#clear').click();
  await page.waitForFunction(() => document.querySelectorAll('.event').length === 0);
  assert.equal(await page.locator('.store').count(), 1);
  const readonly = await browser.newPage();
  readonly.on('pageerror', error => errors.push(error.message));
  await readonly.goto(await demo(false));
  await readonly.getByText('Connected · local session', { exact: true }).waitFor();
  assert.equal(await readonly.locator('#apply').isDisabled(), true);
  await readonly.locator('.event[data-sequence="1"]').click();
  assert.equal(await readonly.locator('#restore').isDisabled(), true);
  assert.deepEqual(errors, []);
  console.log('DevTools browser smoke passed: inspection, action errors, filtering, restore, JSON validation, retained edits, export, diff, clear and read-only mode.');
} finally {
  if (browser) await browser.close();
  for (const child of children) {
    if (child.exitCode === null) {
      const exited = once(child, 'exit');
      child.kill('SIGINT');
      await exited;
    }
  }
  await rm(directory, { recursive: true, force: true });
}
