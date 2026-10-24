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
let fixtureRevision = 100;
function changesFixture(snapshot) {
  return { ...snapshot, tasks:snapshot.tasks || [], tasksChanged:true, revision: ++fixtureRevision, cursor: Math.max(0, ...snapshot.events.map(event => event.sequence)), oldest: snapshot.events[0]?.sequence || 1, reset: true, more: false, storeIds: snapshot.stores.map(store => store.id), capture: { paused: false, sampleIntervalMS: 0, skipped: 0 } };
}

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
  await page.getByText('Connected', { exact: true }).waitFor();
  assert.equal(await page.locator('.store').count(), 1);
  assert.match(await page.locator('#state').innerText(), /"count": 1/);
  // Exercise type filters together with the text search.
  await page.locator('[data-kind=mutation]').click();
  assert.equal(await page.locator('.event').count(), 1);
  await page.locator('[data-kind=action]').click();
  assert.equal(await page.locator('.event').count(), 2);
  await page.locator('[data-kind=error]').click();
  assert.equal(await page.locator('.event').count(), 1);
  await page.locator('[data-kind=all]').click();
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
  await page.locator('#copy-state').click();
  assert.deepEqual(JSON.parse(await page.evaluate(() => navigator.clipboard.readText())), { count: 1 });
  await page.locator('#tab-state').focus();
  await page.keyboard.press('ArrowRight');
  assert.equal(await page.locator('#tab-changes').getAttribute('aria-selected'), 'true');
  await page.keyboard.press('End');
  assert.equal(await page.locator('#tab-event').getAttribute('aria-selected'), 'true');
  await page.keyboard.press('Home');
  assert.equal(await page.locator('#tab-state').getAttribute('aria-selected'), 'true');
  await page.locator('#theme').click();
  await page.reload();
  await page.getByText('Connected', { exact: true }).waitFor();
  assert.equal(await page.locator('html').getAttribute('data-theme'), 'dark');
  await page.locator('#theme').click();
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
  assert.match(await page.locator('#changes').innerText(), /\+\s*9/);
  await page.locator('#editor summary').click();
  if (process.env.DEVTOOLS_SCREENSHOT) {
    await page.screenshot({ path: process.env.DEVTOOLS_SCREENSHOT });
    await page.locator('#theme').click();
    await page.screenshot({ path: process.env.DEVTOOLS_SCREENSHOT.replace(/\.png$/, '-dark.png') });
    await page.locator('#theme').click();
  }
  // Panels must remain usable without page-level horizontal scrolling.
  for (const width of [1024, 768, 390]) {
    await page.setViewportSize({ width, height: 900 });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, `Overflow at ${width}px`);
    await page.locator('#tab-state').click();
    assert.equal(await page.locator('#state').isVisible(), true);
    if (width === 390 && process.env.DEVTOOLS_SCREENSHOT) await page.screenshot({ path: process.env.DEVTOOLS_SCREENSHOT.replace(/\.png$/, '-mobile.png'), fullPage: true });
  }
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.locator('#clear').click();
  await page.waitForFunction(() => document.querySelectorAll('.event').length === 0);
  assert.equal(await page.locator('.store').count(), 1);
  // Capture controls affect diagnostics, while real application writes continue.
  await page.locator('#capture-pause').click();
  await page.getByRole('button', { name: 'Resume capture', exact: true }).waitFor();
  async function replaceCount(target, count) {
    await target.evaluate(async value => {
      const response = await fetch('/api/restore', { method: 'POST', headers: { Authorization: `Bearer ${location.hash.slice(1)}`, 'Content-Type': 'application/json' }, body: JSON.stringify({ storeId: 'counter', state: { count: value } }) });
      if (!response.ok) throw new Error(await response.text());
    }, count);
  }
  await replaceCount(page, 10);
  await page.locator('#refresh').click();
  assert.match(await page.locator('#state').innerText(), /"count": 9/);
  assert.equal(await page.locator('.event').count(), 0);
  await page.locator('#capture-pause').click();
  await page.waitForFunction(() => document.querySelector('#state').textContent.includes('"count": 10'));
  await page.locator('#capture-rate').selectOption('1000');
  for (let count = 11; count <= 15; count++) await replaceCount(page, count);
  await page.locator('#refresh').click();
  assert.match(await page.locator('#capture-status').innerText(), /skipped/);
  await page.locator('#capture-rate').selectOption('0');
  await page.waitForFunction(() => document.querySelector('#state').textContent.includes('"count": 15'));
  // A fresh session loads a bounded latest page; export still contains all retained history.
  const paging = await browser.newPage();
  paging.on('pageerror', error => errors.push(error.message));
  const pagingURL = await demo(true);
  await paging.goto(pagingURL);
  await paging.getByText('Connected', { exact: true }).waitFor();
  for (let count = 2; count < 122; count++) await replaceCount(paging, count);
  await paging.reload();
  await paging.waitForFunction(() => document.querySelectorAll('.event').length === 50);
  await paging.locator('#history-more').click();
  await paging.waitForFunction(() => document.querySelectorAll('.event').length === 100);
  const fullDownload = paging.waitForEvent('download');
  await paging.locator('#export').click();
  const fullSnapshot = JSON.parse(await readFile(await (await fullDownload).path(), 'utf8'));
  assert.equal(fullSnapshot.events.length, 124);
  await paging.close();
  const readonly = await browser.newPage();
  readonly.on('pageerror', error => errors.push(error.message));
  const readonlyURL = await demo(false);
  await readonly.goto(readonlyURL);
  await readonly.getByText('Connected', { exact: true }).waitFor();
  assert.equal(await readonly.locator('#apply').isDisabled(), true);
  await readonly.locator('.event[data-sequence="1"]').click();
  assert.equal(await readonly.locator('#restore').isDisabled(), true);
  // Rendering edge cases: long IDs/values, literal markup and null snapshots.
  const edge = await browser.newPage({ viewport: { width: 768, height: 900 } });
  edge.on('pageerror', error => errors.push(error.message));
  const storeID = 'store-with-a-very-long-name-'.repeat(8);
  const fixture = {
    stores: [{ id: storeID, version: 2, generation: 1, state: { text: '</script><img src=x onerror=alert(1)>', long: 'x'.repeat(500), ready: true } }],
    events: [{ storeId: storeID, kind: 'mutation', mutation: 'direct', version: 1, generation: 1, sequence: 1, at: new Date().toISOString(), previous: { ready: false }, state: null }]
  };
  await edge.route('**/api/changes**', route => route.fulfill({ json: changesFixture(fixture) }));
  await edge.goto(readonlyURL);
  await edge.getByText('Connected', { exact: true }).waitFor();
  assert.equal(await edge.locator('.json-string').first().textContent(), '"</script><img src=x onerror=alert(1)>"');
  assert.equal(await edge.locator('#state img').count(), 0);
  assert.equal(await edge.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, JSON.stringify(await edge.evaluate(() => [...document.querySelectorAll('body *')].filter(node => node.getBoundingClientRect().right > innerWidth + 1 && getComputedStyle(node).position !== 'absolute').map(node => ({ tag: node.tagName, id: node.id, class: node.className, width: node.getBoundingClientRect().width })).slice(0, 12))));
  await edge.locator('.event').click();
  assert.equal(await edge.locator('#state .code-text').textContent(), 'null');
  await edge.locator('#tab-changes').click();
  assert.match(await edge.locator('#changes').innerText(), /null/);
  await edge.unroute('**/api/changes**');
  await edge.route('**/api/changes**', route => route.fulfill({ json: changesFixture({ stores: [], events: [] }) }));
  await edge.locator('#refresh').click();
  await edge.locator('#stores').getByText('No stores attached', { exact: true }).waitFor();
  assert.equal(await edge.locator('#copy-state').isDisabled(), true);
  await edge.unroute('**/api/changes**');
  await edge.route('**/api/changes**', route => route.fulfill({ status: 503, body: 'Session unavailable' }));
  await edge.locator('#refresh').click();
  await edge.getByText('Disconnected', { exact: true }).waitFor();
  assert.match(await edge.locator('#message').innerText(), /Session unavailable/);
  // Large payloads stay bounded until the user requests more rows.
  const large = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  large.on('pageerror', error => errors.push(error.message));
  const largeState = { preferences: { theme: 'violet', language: 'English' }, entries: Array.from({ length: 2500 }, (_, n) => ({ name: n === 2499 ? 'needle-value' : `Word ${n}`, count: n })) };
  const largeFixture = { stores: [{ id: 'dictionary', generation: 1, version: 7, state: largeState }], events: [], retention: { bytes: 2048, maxBytes: 16777216, maxEvents: 200, dropped: 8 } };
  await large.route('**/api/changes**', route => route.fulfill({ json: changesFixture(largeFixture) }));
  await large.goto(readonlyURL);
  await large.getByText('Connected', { exact: true }).waitFor();
  assert.equal(await large.locator('#state .code-line').count(), 1000);
  await large.locator('#state-more').click();
  assert.equal(await large.locator('#state .code-line').count(), 2000);
  await large.locator('#view-tree').click();
  assert.equal(await large.locator('#state-tree .tree-node').count(), 3);
  await large.locator('#state-tree').getByText('"entries"', { exact: true }).click();
  await large.waitForFunction(() => document.querySelectorAll('#state-tree .tree-node').length === 103);
  await large.locator('#state-tree .tree-more').click();
  assert.equal(await large.locator('#state-tree .tree-node').count(), 203);
  await large.locator('#state-search').fill('needle-value');
  assert.equal(await large.locator('.search-hit').count(), 1);
  assert.match(await large.locator('.search-hit').innerText(), /2499/);
  await large.locator('#state-search').fill('entries');
  assert.equal(await large.locator('.search-hit').count(), 200);
  assert.match(await large.locator('#state-search-status').innerText(), /limit reached/);
  await large.locator('#state-search').fill('');
  await large.locator('#collapse-state').click();
  assert.equal(await large.locator('#state-tree details[open]').count(), 0);
  await large.locator('#state-tree > details > summary').click();
  await large.locator('#state-tree').getByText('"preferences"', { exact: true }).click();
  await large.waitForFunction(() => document.querySelectorAll('#state-tree .tree-leaf').length === 2);
  if (process.env.DEVTOOLS_SCREENSHOT) {
    await large.screenshot({ path: process.env.DEVTOOLS_SCREENSHOT.replace(/\.png$/, '-tree.png') });
    await large.locator('#theme').click();
    await large.screenshot({ path: process.env.DEVTOOLS_SCREENSHOT.replace(/\.png$/, '-tree-dark.png') });
  }
  await large.locator('#state-search').fill('needle-value');
  await large.locator('#refresh').click();
  assert.equal(await large.locator('#state-search').inputValue(), 'needle-value');
  // The diff must announce truncation instead of claiming a complete comparison.
  largeFixture.events = [{ sequence: 1, generation: 1, storeId: 'dictionary', kind: 'mutation', at: new Date().toISOString(), previous: Array(600).fill(0), state: Array(600).fill(1) }];
  await large.locator('#refresh').click();
  await large.locator('.event').click();
  await large.locator('#tab-changes').click();
  assert.equal(await large.locator('.diff-block').count(), 500);
  assert.match(await large.locator('#diff-summary').innerText(), /Comparison limited/);
  largeFixture.events = [{ sequence: 2, generation: 1, storeId: 'dictionary', kind: 'mutation', at: new Date().toISOString(), previous: Array(20001).fill(0), state: [...Array(20000).fill(0), 1] }];
  await large.locator('#refresh').click();
  await large.locator('.event').click();
  assert.match(await large.locator('#changes').innerText(), /More changes may exist/);
  assert.doesNotMatch(await large.locator('#changes').innerText(), /identical/);

  const tasks = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  tasks.on('pageerror', error => errors.push(error.message));
  const now = new Date().toISOString();
  const taskFixture = {
    stores: [{ id: 'workspace', version: 4, generation: 1, state: { matches: 12 }, tasksDropped: 2, taskProgressCoalesced: 98 }],
    events: [
      { sequence: 1, generation: 1, storeId: 'workspace', actionId: 21, runId: 1, kind: 'task', name: 'Index workspace', phase: 'running', progress: .4, at: now },
      { sequence: 2, generation: 1, storeId: 'workspace', actionId: 21, kind: 'mutation', mutation: 'action', name: 'Index workspace', version: 4, previous: { matches: 0 }, state: { matches: 12 }, at: now },
      { sequence: 3, generation: 1, storeId: 'workspace', actionId: 22, kind: 'task', name: 'Read metadata', phase: 'queued', at: now }
    ],
    tasks: [
      { actionId: 21, runId: 1, generation: 1, storeId: 'workspace', name: 'Index workspace', key: 'documents', phase: 'running', progress: .4, at: now, metrics: { debounce: 150e6, queue: 25e6, work: 800e6, commit: 0, queueDepth: 1, activeWorkers: 2 } },
      { actionId: 22, runId: 2, generation: 1, storeId: 'workspace', name: 'Read metadata', key: 'photos', phase: 'queued', progress: 0, at: now },
      { actionId: 20, runId: 3, generation: 1, storeId: 'workspace', name: 'Save preferences', phase: 'failed', error: 'Disk is full', at: now }
    ]
  };
  await tasks.route('**/api/changes**', route => route.fulfill({ json: changesFixture(taskFixture) }));
  await tasks.goto(readonlyURL);
  await tasks.getByText('Connected', { exact: true }).waitFor();
  await tasks.locator('#tab-tasks').click();
  assert.equal(await tasks.locator('.task-card').count(), 3);
  assert.match(await tasks.locator('.task-timing').innerText(), /Debounce 150.00 ms.*Queue 25.00 ms.*Work 800.00 ms.*Commit 0.00 ms/);
  assert.match(await tasks.locator('.task-card[data-action-id="21"]').innerText(), /1 waiting.*2 active workers/);
  assert.match(await tasks.locator('#task-summary').innerText(), /2 active.*2 summaries omitted.*98 progress updates coalesced/);
  assert.equal(await tasks.getByRole('progressbar', { name: 'Index workspace progress' }).getAttribute('value'), '0.4');
  await tasks.locator('.task-card[data-action-id="21"]').click();
  assert.equal(await tasks.locator('.event').count(), 2);
  await tasks.locator('#task-clear-filter').click();
  assert.equal(await tasks.locator('.event').count(), 3);
  if (process.env.DEVTOOLS_SCREENSHOT) {
    await tasks.screenshot({ path: process.env.DEVTOOLS_SCREENSHOT.replace(/\.png$/, '-tasks.png') });
    await tasks.locator('#theme').click();
    await tasks.screenshot({ path: process.env.DEVTOOLS_SCREENSHOT.replace(/\.png$/, '-tasks-dark.png') });
  }
  for (const width of [768, 390]) {
    await tasks.setViewportSize({ width, height: 900 });
    assert.equal(await tasks.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, `Tasks overflow at ${width}px`);
    assert.equal(await tasks.locator('#tasks').isVisible(), true);
  }
  taskFixture.tasks[0] = { ...taskFixture.tasks[0], phase: 'succeeded', progress: 1, committed: true, version: 4 };
  taskFixture.tasks[1] = { ...taskFixture.tasks[1], phase: 'unobserved' };
  await tasks.locator('#refresh').click();
  await tasks.getByText('Task ended outside capture; outcome unavailable.', { exact: true }).waitFor();
  assert.match(await tasks.locator('.task-card[data-action-id="21"]').innerText(), /committed v4/);
  assert.equal(await tasks.locator('#task-count').innerText(), '0');

  assert.deepEqual(errors, []);
  console.log('DevTools browser smoke passed: inspection, errors, type/text filters, clipboard, keyboard tabs, saved theme, responsive layouts, restore, JSON validation, retained edits, complete export, diff, clear, read-only mode, lazy trees, paginated JSON/history, incremental updates, pause/resume, sampling, state search diff limits, task progress/outcomes, related-event filtering and responsive task cards.');
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
