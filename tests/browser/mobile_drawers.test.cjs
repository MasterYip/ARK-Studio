// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node --test tests/browser/mobile_drawers.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge>; headless only)
// Phone width, touch: the session list and session details sheets on
// Execute › Runs take taps (they once opened under their own backdrop):
// tapping a session opens it, a details tab switches, the composer takes
// text, a tap on the backdrop closes the sheet, and nothing behind an open
// sheet takes the tap.
const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const { mkdtempSync, rmSync } = require('node:fs');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
let chromium;
let devices;
try { ({ chromium, devices } = require('playwright')); } catch { /* Optional browser dependency. */ }

const SERVER = process.env.ARK_TEST_SERVER ? path.resolve(process.env.ARK_TEST_SERVER) : '';

function freePort() {
  return new Promise((resolve, reject) => {
    const probe = net.createServer();
    probe.once('error', reject);
    probe.listen(0, '127.0.0.1', () => { const { port } = probe.address(); probe.close(() => resolve(port)); });
  });
}

async function api(base, method, route, body) {
  const res = await fetch(base + route, {
    method, headers: body ? { 'Content-Type': 'application/json', Origin: base } : { Origin: base },
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await res.json();
  if (!res.ok) throw new Error(`${method} ${route}: ${res.status} ${JSON.stringify(data)}`);
  return data;
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// The element on top at the centre of `selector` is inside it.
async function onTop(page, selector) {
  return page.evaluate((sel) => {
    const el = document.querySelector(sel);
    if (!el) return false;
    const r = el.getBoundingClientRect();
    const top = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
    return el === top || el.contains(top);
  }, selector);
}

test('phone: session sheets on Runs take taps', { skip: !chromium || !SERVER, timeout: 240000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-mob-'));
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const server = spawn(SERVER, ['serve', '--root', path.join(dir, 'projects'), '--host', '127.0.0.1', '--port', String(port),
    '--no-demo', '--sessions-root', path.join(dir, 'sessions'), '--web-root', path.resolve(__dirname, '../../ark_studio/web')],
  { stdio: 'ignore' });
  const ids = [];
  let browser;
  try {
    for (let i = 0; ; i += 1) {
      try { await api(base, 'GET', '/api/health'); break; } catch (err) { if (i > 100) throw err; await sleep(200); }
    }
    await api(base, 'POST', '/api/projects', { name: 'Phone' });
    for (const title of ['First', 'Second']) {
      ids.push((await api(base, 'POST', '/api/agent-sessions', { platform: 'echo', title, prompt: 'hello' })).session.id);
    }
    await sleep(1500);
    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    for (const lang of ['zh', 'en']) {
      const context = await browser.newContext({ ...devices['Pixel 7'], viewport: { width: 390, height: 844 } });
      await context.addInitScript((lang) => {
        localStorage.setItem('ark.prefsV2', '1');
        localStorage.setItem('ark.lang', lang);
        localStorage.setItem('ark.welcome.v1:local', '1');
      }, lang);
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', (error) => errors.push(error.message));
      await page.goto(`${base}/?lang=${lang}&session=${ids[0]}#/execute/sessions`);
      await page.locator('.guidance-invite .btn.ghost').tap({ timeout: 4000 }).catch(() => {});
      const listBtn = page.locator('[data-phone-list]');
      const openDetails = async () => {
        await page.locator('.sw-phone-head [data-session-menu]').tap();
        await page.locator('.graph-menu-item', { hasText: lang === 'zh' ? '会话属性' : 'Session properties' }).tap();
      };
      await listBtn.waitFor({ timeout: 30000 });
      const shown = async () => page.locator('.sw-session.on .sw-session-title').first().textContent();
      // Session list sheet: on top of its backdrop; tapping a session opens it.
      await listBtn.tap();
      await page.locator('.sw-rail.mobile-open').waitFor();
      await sleep(400);
      assert.ok(await onTop(page, '.sw-rail.mobile-open .sw-session'), 'a session row is on top');
      assert.ok(await onTop(page, '.sw-rail.mobile-open .sw-rail-tools .sw-search'), 'the search box is on top');
      await page.locator('.sw-rail.mobile-open .sw-rail-tools .sw-search').tap();
      await page.keyboard.type('Sec');
      assert.equal(await page.locator('.sw-rail.mobile-open .sw-rail-tools .sw-search').inputValue(), 'Sec', 'the search box takes text');
      await page.locator('.sw-rail.mobile-open .sw-session', { hasText: 'Second' }).tap();
      for (let i = 0; i < 50 && (await shown()) !== 'Second'; i += 1) await sleep(100);
      assert.equal(await shown(), 'Second', 'tapping a session opens it');
      await page.locator('.sw-rail.mobile-open').waitFor({ state: 'detached', timeout: 5000 }).catch(() => {});
      assert.equal(await page.locator('.sw-rail.mobile-open').count(), 0, 'picking a session closes the sheet');
      // Details sheet: its tabs switch; a backdrop tap closes it; nothing behind takes the tap.
      await openDetails();
      await page.locator('.sw-side.mobile-open').waitFor();
      await sleep(400);
      const tabs = page.locator('.sw-side.mobile-open .sw-side-tab');
      assert.ok(await onTop(page, '.sw-side.mobile-open .sw-side-tab'), 'a details tab is on top');
      await tabs.nth(1).tap();
      assert.equal(await tabs.nth(1).getAttribute('aria-selected'), 'true', 'the tab switched');
      assert.equal(await onTop(page, '.sw-composer-input'), false, 'the conversation behind the sheet is covered');
      // The backdrop shows above the bottom sheet.
      const sheetTop = (await page.locator('.sw-side.mobile-open').boundingBox()).y;
      await page.touchscreen.tap(20, Math.max(60, sheetTop - 20));
      await page.locator('.sw-side.mobile-open').waitFor({ state: 'detached', timeout: 5000 });
      // The composer works once the sheets are closed.
      const input = page.locator('.sw-composer-input');
      await input.tap();
      await page.keyboard.type('hi');
      assert.equal(await input.inputValue(), 'hi');
      await page.screenshot({ path: path.join(process.env.ARK_TEST_SHOTS || dir, `mobile-runs-${lang}.png`) });
      assert.deepEqual(errors, [], lang);
      await context.close();
    }
  } finally {
    if (browser) await browser.close();
    for (const id of ids) await api(base, 'POST', `/api/agent-sessions/${id}/stop`, {}).catch(() => {});
    await sleep(1000);
    server.kill();
    await new Promise((r) => (server.exitCode !== null ? r() : server.once('exit', r)));
    try { rmSync(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 500 }); } catch { /* a stopped agent may hold its folder briefly */ }
  }
});
