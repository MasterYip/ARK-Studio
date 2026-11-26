// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node --test tests/browser/phone_settings_toasts.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge>; ARK_TEST_SHOTS=<folder>; headless only)
// A paired phone (390×844 and 360×740, zh and en):
//   - toasts sit inside the screen, between the side margins, above the tab
//     bar, and wrap long text (they were centred with translateX(-50%) on a
//     box already spanning the screen, so half of each was off the left edge);
//   - opening Settings and every tab of every scope makes no request the
//     server refuses to a paired device and shows no error toast; sections
//     that need a computer say so in place.
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
    method, headers: { 'Content-Type': 'application/json', Origin: base },
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await res.json();
  if (!res.ok) throw new Error(`${method} ${route}: ${res.status} ${JSON.stringify(data)}`);
  return data;
}

test('paired phone: toasts on screen, Settings without refused calls', { skip: !chromium || !SERVER, timeout: 600000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-phset-'));
  const shots = process.env.ARK_TEST_SHOTS || dir;
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const server = spawn(SERVER, ['serve', '--root', path.join(dir, 'projects'), '--host', '127.0.0.1', '--port', String(port),
    '--no-demo', '--sessions-root', path.join(dir, 'sessions'), '--web-root', path.resolve(__dirname, '../../ark_studio/web')],
  { stdio: 'ignore' });
  let browser;
  try {
    for (let i = 0; ; i += 1) {
      try { await api(base, 'GET', '/api/health'); break; } catch (err) { if (i > 100) throw err; await sleep(200); }
    }
    await api(base, 'POST', '/api/projects', { name: 'Phone settings' });
    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    for (const [w, hgt] of [[390, 844], [360, 740]]) {
      for (const lang of ['zh', 'en']) {
        const tag = `${lang}-${w}`;
        const pair = await api(base, 'POST', '/api/mobile/pair', { public_url: base });
        const code = JSON.stringify(pair).match(/pair=([A-Za-z0-9_\-.~%]+)/)[1];
        const claim = await api(base, 'POST', '/api/mobile/pair/claim', { token: decodeURIComponent(code) });
        const context = await browser.newContext({ ...devices['Pixel 7'], viewport: { width: w, height: hgt } });
        await context.addInitScript(([l, token]) => {
          localStorage.setItem('ark.prefsV2', '1');
          localStorage.setItem('ark.lang', l);
          localStorage.setItem('ark.welcome.v1:local', '1');
          localStorage.setItem('ark.mobile.token', token);
          window.__toasts = [];
          new MutationObserver((records) => {
            for (const record of records) for (const node of record.addedNodes) {
              if (node.classList?.contains('toast')) window.__toasts.push(node.textContent);
            }
          }).observe(document, { childList: true, subtree: true });
        }, [lang, claim.mobile_token]);
        const page = await context.newPage();
        const refused = [];
        const errors = [];
        page.on('pageerror', (error) => errors.push(error.message));
        page.on('response', (r) => {
          const u = new URL(r.url());
          if (u.pathname.startsWith('/api/') && r.status() >= 400) refused.push(`${r.request().method()} ${u.pathname} ${r.status()}`);
        });
        await page.goto(`${base}/?lang=${lang}#/home`);
        await page.locator('.guidance-invite .btn.ghost').tap({ timeout: 4000 }).catch(() => {});
        await page.locator('.mobile-tab', { hasText: lang === 'zh' ? '设置' : 'Settings' }).first().tap({ timeout: 15000 });
        await page.locator('.settings-shell').waitFor({ timeout: 20000 });
        await sleep(2500);
        await page.screenshot({ path: path.join(shots, `phone-settings-${tag}.png`) });
        if (w === 390) {
          // Every tab of every scope.
          const scopes = await page.locator('.settings-scope-btn').count();
          for (let i = 0; i < scopes; i += 1) {
            await page.locator('.settings-scope-btn').nth(i).tap();
            await sleep(500);
            const ids = await page.evaluate(() => [...document.querySelectorAll('.settings-nav-item[data-settings-tab]')]
              .filter((b) => b.offsetParent).map((b) => b.dataset.settingsTab));
            for (const id of ids) {
              await page.locator(`.settings-nav-item[data-settings-tab="${id}"]:visible`).first().tap().catch(() => {});
              await sleep(1200);
            }
          }
        }
        assert.deepEqual([...new Set(refused)], [], `no refused calls (${tag})`);
        assert.deepEqual(await page.evaluate(() => window.__toasts), [], `no toasts from Settings (${tag})`);
        // Toasts: three, one long; each inside the screen and above the tab bar.
        await page.evaluate(async () => {
          const { toast } = await import('/js/components.js');
          toast('Saved', 'ok', { duration: 8000 });
          toast(`Could not reach /api/agent/v1/media/med_${'0123456789abcdef'.repeat(4)}/raw: the request timed out after 30 s.`, 'err', { duration: 8000 });
          toast('已复制到剪贴板，可以粘贴到智能体对话中继续。', '', { duration: 8000 });
        });
        await sleep(400);
        const boxes = await page.evaluate(() => {
          const bar = document.querySelector('.mobile-tabbar, .mobile-nav');
          return {
            vw: document.documentElement.clientWidth,
            bar: bar ? bar.getBoundingClientRect().top : innerHeight,
            toasts: [...document.querySelectorAll('#toast-root .toast')].map((t) => t.getBoundingClientRect().toJSON()),
          };
        });
        await page.screenshot({ path: path.join(shots, `phone-toasts-${tag}.png`) });
        assert.equal(boxes.toasts.length, 3, `three toasts (${tag})`);
        for (const box of boxes.toasts) {
          assert.ok(box.left >= 4 && box.right <= boxes.vw - 4, `toast inside the screen: ${box.left}..${box.right} of ${boxes.vw} (${tag})`);
          assert.ok(box.bottom <= boxes.bar + 1, `toast above the tab bar: ${box.bottom} > ${boxes.bar} (${tag})`);
          assert.ok(Math.abs(box.left - (boxes.vw - box.right)) <= 2, `toast centred (${tag})`);
        }
        assert.ok(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth), `no sideways scroll (${tag})`);
        assert.deepEqual(errors, [], `no page errors (${tag})`);
        await context.close();
      }
    }
  } finally {
    await browser?.close();
    server.kill();
    await sleep(300);
    if (!process.env.ARK_TEST_SHOTS) rmSync(dir, { recursive: true, force: true });
  }
});
