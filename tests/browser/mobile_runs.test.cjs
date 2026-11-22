// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node --test tests/browser/mobile_runs.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge>; ARK_TEST_SHOTS=<folder>; headless only)
// Execute › Runs at phone widths (390×844, 360×740; touch; zh and en):
// a full-screen conversation with one compact header row (44 px targets),
// the composer at the bottom, no sideways scrolling; sub-agent tabs as a
// strip; the session list and the session properties as sheets that close
// on the back gesture and on a swipe down; long press opens a row's menu.
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
const FAKE = path.join(__dirname, 'fixtures', 'fake-claude-stream.cjs');
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

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

async function until(fn, ms = 15000) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > end) throw new Error('timed out');
    await sleep(150);
  }
}

// A touch long press on `selector` (pointer events, as on a phone).
async function longPress(page, selector) {
  const box = await page.locator(selector).first().boundingBox();
  const x = box.x + box.width / 2;
  const y = box.y + box.height / 2;
  await page.locator(selector).first().evaluate((el, [px, py]) => {
    el.dispatchEvent(new PointerEvent('pointerdown', { pointerType: 'touch', clientX: px, clientY: py, bubbles: true }));
  }, [x, y]);
  await sleep(700);
  await page.locator(selector).first().evaluate((el, [px, py]) => {
    el.dispatchEvent(new PointerEvent('pointerup', { pointerType: 'touch', clientX: px, clientY: py, bubbles: true }));
  }, [x, y]);
}

// A swipe down on `selector` with touch pointer events.
async function swipeDown(page, selector, dy = 160) {
  await page.locator(selector).first().evaluate((el, d) => {
    const r = el.getBoundingClientRect();
    const x = r.left + r.width / 2;
    const y = r.top + r.height / 2;
    const ev = (type, yy) => el.dispatchEvent(new PointerEvent(type, { pointerType: 'touch', pointerId: 7, clientX: x, clientY: yy, bubbles: true }));
    ev('pointerdown', y);
    ev('pointermove', y + d / 2);
    ev('pointermove', y + d);
    ev('pointerup', y + d);
  }, dy);
}

test('phone Runs: the desktop design for touch', { skip: !chromium || !SERVER, timeout: 420000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-mruns-'));
  const shots = process.env.ARK_TEST_SHOTS || dir;
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const server = spawn(SERVER, ['serve', '--root', path.join(dir, 'projects'), '--host', '127.0.0.1', '--port', String(port),
    '--no-demo', '--sessions-root', path.join(dir, 'sessions'), '--web-root', path.resolve(__dirname, '../../ark_studio/web')],
  { stdio: 'ignore', env: { ...process.env, ARK_TASK_CUSTOM_COMMAND: `"${process.execPath}" "${FAKE}"`, ARK_TASK_CUSTOM_LABEL: 'Recorded Claude stream', ARK_TASK_CUSTOM_FOLLOWUP: '0' } });
  const ids = [];
  let browser;
  try {
    for (let i = 0; ; i += 1) {
      try { await api(base, 'GET', '/api/health'); break; } catch (err) { if (i > 100) throw err; await sleep(200); }
    }
    await api(base, 'POST', '/api/projects', { name: 'Phone runs' });
    const streamed = (await api(base, 'POST', '/api/agent-sessions', { platform: 'custom', title: 'Recorded stream', prompt: 'go' })).session;
    ids.push(streamed.id);
    ids.push((await api(base, 'POST', '/api/agent-sessions', { platform: 'echo', title: 'Echo two', prompt: 'hello' })).session.id);
    await until(async () => (await api(base, 'GET', `/api/agent-sessions/${streamed.id}/transcript`)).items.some((i) => i.id === 'toolu_H'), 30000);
    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    for (const [w, hgt] of [[390, 844], [360, 740]]) {
      for (const lang of ['zh', 'en']) {
        const zh = lang === 'zh';
        const context = await browser.newContext({ ...devices['Pixel 7'], viewport: { width: w, height: hgt } });
        await context.addInitScript((lang) => {
          localStorage.setItem('ark.prefsV2', '1');
          localStorage.setItem('ark.lang', lang);
          localStorage.setItem('ark.welcome.v1:local', '1');
        }, lang);
        const page = await context.newPage();
        const errors = [];
        page.on('pageerror', (error) => errors.push(error.message));
        await page.goto(`${base}/?lang=${lang}&session=${streamed.id}#/execute/sessions`);
        await page.locator('.guidance-invite .btn.ghost').tap({ timeout: 4000 }).catch(() => {});
        const head = page.locator('.sw-phone-head');
        await head.waitFor({ timeout: 30000 });
        const tag = `${lang}-${w}`;
        // Layout: one header row, 44 px targets, composer at the bottom, no sideways scroll.
        assert.equal(await page.locator('.session-chat-page .page-head').count(), 0, 'no page header');
        for (const sel of ['[data-phone-list]', '.sw-phone-head [data-session-menu]', '.sw-send']) {
          const box = await page.locator(sel).first().boundingBox();
          assert.ok(box.height >= 44 && box.width >= 44, `${sel} is ${box.width}x${box.height} (${tag})`);
        }
        await page.screenshot({ path: path.join(shots, `mobile-runs-chat-${tag}.png`) });
        const comp = await page.locator('.sw-composer').boundingBox();
        console.log('LAYOUT', tag, JSON.stringify(await page.evaluate(() => ({
          main: document.querySelector('.sw-main')?.getBoundingClientRect().toJSON(),
          tabbar: document.querySelector('.mobile-tabbar, .mobile-nav')?.getBoundingClientRect().toJSON(),
          page: document.querySelector('.session-chat-page')?.getBoundingClientRect().toJSON(),
          vars: ['--topbar', '--tabbar-space', '--mobile-tabbar', '--sw-vh'].map((v) => getComputedStyle(document.documentElement).getPropertyValue(v)),
        }))));
        const tabbar = await page.evaluate(() => document.querySelector('.mobile-tabbar, .mobile-nav')?.getBoundingClientRect().top || innerHeight);
        assert.ok(comp.y + comp.height <= tabbar + 2 && comp.y + comp.height > tabbar - 40, `composer pinned above the tab bar (${comp.y + comp.height} vs ${tabbar}, ${tag})`);
        const overflow = await page.evaluate(() => document.scrollingElement.scrollWidth - innerWidth);
        assert.ok(overflow <= 1, `no sideways scroll (${overflow}px, ${tag})`);
        assert.equal(await page.locator('[data-composer-hint]').count(), 0, 'no shortcut line');
        await page.screenshot({ path: path.join(shots, `mobile-runs-chat-${tag}.png`) });
        // Session properties sheet from the menu; a sub-agent opens as a tab.
        await page.locator('.sw-phone-head [data-session-menu]').tap();
        await page.locator('.graph-menu-item', { hasText: zh ? '会话属性' : 'Session properties' }).tap();
        const side = page.locator('.sw-side.mobile-open');
        await side.waitFor();
        await sleep(300);
        await page.screenshot({ path: path.join(shots, `mobile-runs-props-${tag}.png`) });
        await side.locator('.sw-side-tab', { hasText: zh ? '子智能体' : 'Subagents' }).tap();
        await side.locator('.sa-row', { hasText: 'Survey session code' }).tap();
        await page.locator('[data-session-tab="toolu_A"].on').waitFor({ timeout: 10000 });
        // Tabs: the strip switches back to the main conversation.
        await page.locator('[data-session-tab="main"]').tap();
        await page.locator('[data-session-tab="main"].on').waitFor();
        // The back gesture closes a sheet first, the page stays.
        await page.locator('.sw-phone-head [data-session-menu]').tap();
        await page.locator('.graph-menu-item', { hasText: zh ? '会话属性' : 'Session properties' }).tap();
        await side.waitFor();
        const url = page.url();
        await page.goBack();
        await side.waitFor({ state: 'detached', timeout: 5000 });
        assert.equal(page.url(), url, 'back closed the sheet, not the page');
        // Session list sheet: compact headers, swipe down closes it.
        await page.locator('[data-phone-list]').tap();
        const rail = page.locator('.sw-rail.mobile-open');
        await rail.waitFor();
        await sleep(300);
        assert.ok(await rail.locator('[data-block="list"]').count(), 'collapsible list block');
        const ctl = await rail.locator('.sw-rail-tools .sw-search').boundingBox();
        assert.ok(ctl.height >= 44, `search height ${ctl.height}`);
        await page.screenshot({ path: path.join(shots, `mobile-runs-list-${tag}.png`) });
        // Long press on a row opens its menu (rename, close, delete…).
        await longPress(page, '.sw-rail.mobile-open .sw-session');
        await page.locator('.graph-menu-item', { hasText: zh ? '重命名' : 'Rename' }).waitFor({ timeout: 5000 });
        await page.keyboard.press('Escape');
        await swipeDown(page, '.sw-rail.mobile-open .sw-sheet-handle');
        await rail.waitFor({ state: 'detached', timeout: 5000 });
        // Long press on the title opens the session menu.
        await longPress(page, '.sw-phone-title');
        await page.locator('.graph-menu-item', { hasText: zh ? '在新标签页中分叉' : 'Fork in new tab' }).waitFor({ timeout: 5000 });
        await page.keyboard.press('Escape');
        if (errors.length) console.log('PAGEERRORS', tag, JSON.stringify(errors));
        assert.deepEqual(errors, [], tag);
        await context.close();
      }
    }
  } finally {
    if (browser) await browser.close();
    for (const id of ids) await api(base, 'POST', `/api/agent-sessions/${id}/stop`, {}).catch(() => {});
    await sleep(1000);
    server.kill();
    await new Promise((r) => (server.exitCode !== null ? r() : server.once('exit', r)));
    try { rmSync(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 500 }); } catch { /* see above */ }
  }
});
