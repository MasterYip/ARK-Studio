// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node --test tests/browser/desktop_link_dialog.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge> to pick a browser)
// "Link this desktop" on a server page inside the ARK Studio desktop app
// (the app's hub is faked here): the dialog stays open while it links, the
// button shows progress, a request that never answers ends after 15 s with
// an error in the dialog and the button enabled again, Cancel always closes
// it, and the dialog sits above the first-run Welcome.  English and Chinese.
const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const { mkdtempSync, rmSync } = require('node:fs');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch { /* Optional browser dependency. */ }

const SERVER = process.env.ARK_TEST_SERVER ? path.resolve(process.env.ARK_TEST_SERVER) : '';

function freePort() {
  return new Promise((resolve, reject) => {
    const probe = net.createServer();
    probe.once('error', reject);
    probe.listen(0, '127.0.0.1', () => { const { port } = probe.address(); probe.close(() => resolve(port)); });
  });
}

// The desktop app's hub as a remote (server) page sees it.  window.__linkMode:
// "ok" (the claim succeeds after a short delay) or "hang" (never answers).
function fakeDesktop() {
  window.__linkCalls = [];
  window.__linkMode = window.__linkMode || 'ok';
  let linked = false;
  window.__ARK_DESKTOP__ = {
    shell: 'tauri', version: '2.0.0',
    hub: {
      context: async () => ({ role: 'remote', version: '2.0.0', server: `${location.origin}/`, link: 1, linked }),
      goLocal: async () => {},
      linkServer: (code) => {
        window.__linkCalls.push(code);
        if (window.__linkMode === 'hang') return new Promise(() => {});
        return new Promise((resolve) => setTimeout(() => { linked = true; resolve({ linked: true, user: 'ana', server: 'test' }); }, 400));
      },
    },
  };
}

const TEXT = {
  en: { action: 'Link this desktop', working: 'Linking…', cancel: 'Cancel', timeout: 'did not answer within 15 seconds', done: 'Linked as ana' },
  zh: { action: '关联此桌面', working: '正在关联…', cancel: '取消', timeout: '15 秒内没有响应', done: '已关联（ana）' },
};

test('Link this desktop: progress, timeout error in the dialog, cancel', { skip: !chromium || !SERVER, timeout: 240000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-link-'));
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const server = spawn(SERVER, ['serve', '--root', path.join(dir, 'projects'), '--host', '127.0.0.1', '--port', String(port),
    '--sessions-root', path.join(dir, 'sessions'), '--web-root', path.resolve(__dirname, '../../ark_studio/web')],
  { stdio: ['ignore', 'pipe', 'pipe'] });
  let log = '';
  server.stdout.on('data', (chunk) => { log += chunk; });
  server.stderr.on('data', (chunk) => { log += chunk; });
  let browser;
  try {
    for (let i = 0; ; i += 1) {
      try { const r = await fetch(`${base}/api/health`); if (r.ok) break; } catch (err) {
        if (i > 100) throw new Error(`server did not start: ${err.message}\n${log}`);
      }
      await new Promise((r) => setTimeout(r, 200));
    }
    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    for (const lang of ['en', 'zh']) {
      const L = TEXT[lang];
      const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
      // No "welcome seen" flag: the demo project shows the first-run Welcome,
      // which the link prompt must not hide behind.
      await context.addInitScript((l) => { localStorage.setItem('ark.prefsV2', '1'); localStorage.setItem('ark.lang', l); }, lang);
      await context.addInitScript(fakeDesktop);
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', (error) => errors.push(error.message));
      const dialog = page.locator('.modal-backdrop.raised .modal');
      const primary = dialog.locator('.modal-foot button.primary');

      // 1. Success: progress while linking, then the dialog closes with a toast.
      await page.goto(`${base}/?ark_link_desktop=1&lang=${lang}`);
      await primary.waitFor({ timeout: 30000 });
      assert.equal(await primary.textContent(), L.action);
      await primary.click(); // clickable even with the Welcome open
      await page.waitForFunction((w) => document.querySelector('.modal-backdrop.raised .modal-foot button.primary')?.textContent === w, L.working);
      assert.equal(await primary.isDisabled(), true, 'disabled while linking');
      await dialog.waitFor({ state: 'detached', timeout: 10000 });
      await page.waitForFunction((d) => document.querySelector('#toast-root')?.textContent.includes(d), L.done);
      assert.equal((await page.evaluate(() => window.__linkCalls)).length, 1);
      assert.match((await page.evaluate(() => window.__linkCalls))[0], /^arkp_/);

      // 2. The claim never answers: after 15 s an error in the dialog, button back.
      await page.evaluate(() => { window.__linkMode = 'hang'; });
      await context.addInitScript(() => { window.__linkMode = 'hang'; });
      await page.goto(`${base}/?ark_link_desktop=1&lang=${lang}`);
      await primary.waitFor({ timeout: 30000 });
      const started = Date.now();
      await primary.click();
      const error = dialog.locator('.desktop-link-error');
      await error.waitFor({ state: 'visible', timeout: 25000 });
      const waited = Date.now() - started;
      assert.ok(waited >= 14000 && waited < 25000, `timeout after ${waited} ms`);
      assert.ok((await error.textContent()).includes(L.timeout), await error.textContent());
      assert.equal(await primary.isDisabled(), false, 'enabled again after the failure');
      assert.equal(await primary.textContent(), L.action);
      await page.screenshot({ path: path.join(process.env.ARK_TEST_SHOTS || dir, `desktop-link-timeout-${lang}.png`) });

      // 3. Cancel while linking closes the dialog at once.
      await primary.click();
      await page.waitForFunction((w) => document.querySelector('.modal-backdrop.raised .modal-foot button.primary')?.textContent === w, L.working);
      await dialog.locator('.modal-foot button', { hasText: L.cancel }).click();
      await dialog.waitFor({ state: 'detached', timeout: 2000 });

      // 4. A code request that never answers (server side) also ends in the dialog.
      await page.route('**/api/desktop/link', () => { /* never answered */ });
      await page.goto(`${base}/?ark_link_desktop=1&lang=${lang}`);
      await primary.waitFor({ timeout: 30000 });
      await primary.click();
      await error.waitFor({ state: 'visible', timeout: 25000 });
      assert.ok((await error.textContent()).includes(L.timeout));
      await dialog.locator('.modal-head .icon-btn').click();
      await dialog.waitFor({ state: 'detached', timeout: 2000 });
      await page.unroute('**/api/desktop/link');

      assert.deepEqual(errors, [], lang);
      await context.close();
    }
  } finally {
    await browser?.close();
    server.kill();
    await new Promise((r) => setTimeout(r, 300));
    try { rmSync(dir, { recursive: true, force: true }); } catch { /* Windows may hold files briefly. */ }
  }
});
