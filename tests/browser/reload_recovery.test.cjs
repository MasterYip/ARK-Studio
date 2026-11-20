// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node --test tests/browser/reload_recovery.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge>)
// Headless only.  A view that blocks the main thread on every load (the hash
// route reopens it): F5 commits only once the blocking task ends, and the
// reloaded page must then start at Home and stay responsive instead of
// reopening the view and hanging again.  "Reopen" brings the view back;
// ?safe=1 starts at Home on request.
const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const { mkdtempSync, rmSync } = require('node:fs');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch {
  try { ({ chromium } = require('playwright-core')); } catch { /* Optional browser dependency. */ }
}

const SERVER = process.env.ARK_TEST_SERVER ? path.resolve(process.env.ARK_TEST_SERVER) : '';
const HANG_MS = 6000;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function freePort() {
  return new Promise((resolve, reject) => {
    const probe = net.createServer();
    probe.once('error', reject);
    probe.listen(0, '127.0.0.1', () => { const { port } = probe.address(); probe.close(() => resolve(port)); });
  });
}

async function until(fn, ms) {
  const end = Date.now() + ms;
  for (;;) {
    const value = await fn().catch(() => null);
    if (value) return value;
    if (Date.now() > end) return null;
    await sleep(150);
  }
}

// page.evaluate waits forever on a blocked page: bound every probe.
const probe = (page, fn, arg) => Promise.race([page.evaluate(fn, arg), sleep(1000).then(() => null)]).catch(() => null);
const ready = (page) => probe(page, () => !document.getElementById('ark-startup') && !!document.querySelector('#topbar *') && location.hash);

test('reload after a hang starts at Home and stays responsive', { skip: !chromium || !SERVER, timeout: 180000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-reload-'));
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const server = spawn(SERVER, ['serve', '--root', path.join(dir, 'projects'), '--host', '127.0.0.1', '--port', String(port),
    '--no-demo', '--sessions-root', path.join(dir, 'sessions'), '--web-root', path.resolve(__dirname, '../../ark_studio/web')],
  { stdio: 'ignore' });
  let browser;
  try {
    assert.ok(await until(() => fetch(base + '/api/health').then((r) => r.ok), 30000), 'server did not start');
    await fetch(base + '/api/projects', { method: 'POST', headers: { 'Content-Type': 'application/json', Origin: base }, body: JSON.stringify({ name: 'Reload test' }) });
    browser = await chromium.launch({ headless: true, executablePath: process.env.ARK_TEST_BROWSER || undefined });
    const context = await browser.newContext({ viewport: { width: 1280, height: 800 } });
    await context.addInitScript((hangMs) => {
      localStorage.setItem('ark.prefsV2', '1');
      localStorage.setItem('ark.welcome.v1:local', '1');
      // The "heavy view": opening the Execute workspace blocks the page.
      const heavy = () => location.hash.startsWith('#/execute');
      const block = () => { const end = Date.now() + hangMs; while (Date.now() < end) { /* busy */ } };
      addEventListener('load', () => setTimeout(() => { if (heavy()) block(); }, 1500));
      addEventListener('hashchange', () => setTimeout(() => { if (heavy()) block(); }, 300));
    }, HANG_MS);
    const page = await context.newPage();
    await page.goto(base + '/#/execute/board');
    // The first load blocks once the view opened; F5 while it is blocked.
    await sleep(2500);
    const cdp = await context.newCDPSession(page);
    await cdp.send('Page.enable');
    let committedAt = 0;
    const reloadAt = Date.now();
    cdp.on('Page.frameNavigated', (event) => { if (!event.frame.parentId) committedAt = Date.now() - reloadAt; });
    cdp.send('Page.reload').catch(() => {});
    // The reload cannot commit before the blocking task ends ...
    assert.ok(await until(async () => committedAt, HANG_MS + 15000), 'reload never committed');
    // ... then the page starts at Home instead of reopening the blocked view.
    const hash = await until(() => ready(page), 20000);
    assert.equal(hash, '#/overview');
    // Responsive after the time the heavy view would have blocked it again.
    await sleep(2500);
    const t0 = Date.now();
    assert.equal(await probe(page, () => 1), 1, 'page blocked after the reload');
    assert.ok(Date.now() - t0 < 500);
    const toast = page.locator('.toast .toast-action');
    await toast.waitFor({ timeout: 5000 });
    // A plain reload now restores the route as usual.
    await page.reload();
    assert.equal(await until(() => ready(page), 20000), '#/overview');
    assert.equal(await page.locator('.toast .toast-action').count(), 0);

    // ?safe=1: Home without the view parameters, even without a hang.
    const fresh = await context.newPage();
    await fresh.goto(base + '/?safe=1#/map');
    assert.equal(await until(() => ready(fresh), 20000), '#/overview');
    assert.equal(await fresh.evaluate(() => location.search), '');
    await fresh.close();

    // "Reopen" goes back to the blocked view (and it blocks again: the
    // person asked for it).
    await page.goto(base + '/#/execute/board');
    await sleep(2500);
    const cdp2 = await context.newCDPSession(page);
    await cdp2.send('Page.enable');
    let committed2 = false;
    cdp2.on('Page.frameNavigated', (event) => { if (!event.frame.parentId) committed2 = true; });
    cdp2.send('Page.reload').catch(() => {});
    assert.ok(await until(async () => committed2, HANG_MS + 15000));
    assert.equal(await until(() => ready(page), 20000), '#/overview');
    await page.locator('.toast .toast-action').click({ timeout: 5000 });
    assert.equal(await until(() => probe(page, () => location.hash.startsWith('#/execute') && location.hash), HANG_MS + 5000), '#/execute/board');
  } finally {
    await browser?.close().catch(() => {});
    server.kill();
    await new Promise((r) => (server.exitCode !== null ? r() : server.once('exit', r)));
    rmSync(dir, { recursive: true, force: true });
  }
});
