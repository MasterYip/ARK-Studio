// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node --test tests/browser/settings_resync.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge>)
// Settings › Project › Agents & AI used to repaint every ~2 s: each paint
// created a phone pairing code (POST /api/mobile/pair), which logged project
// activity, bumped the revision and repainted the page again.  Headless:
//   - the page stays open 30 s: one paint, the project revision unchanged;
//   - Access (Mobile) creates one pairing code, and still no revision change;
//   - with an unsaved draft, writes from elsewhere (units, a settings change)
//     do not repaint the page and the draft stays;
//   - once clean, a settings change from elsewhere repaints it.
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
const WEB = path.resolve(__dirname, '../../ark_studio/web');
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function freePort() {
  return new Promise((resolve, reject) => {
    const probe = net.createServer();
    probe.once('error', reject);
    probe.listen(0, '127.0.0.1', () => { const { port } = probe.address(); probe.close(() => resolve(port)); });
  });
}

async function api(base, method, route, body, project = '') {
  const headers = { Origin: base };
  if (body) headers['Content-Type'] = 'application/json';
  if (project) headers['X-ARK-Project'] = project;
  const res = await fetch(base + route, { method, headers, body: body ? JSON.stringify(body) : undefined });
  const data = await res.json();
  if (!res.ok) throw new Error(`${method} ${route}: ${res.status} ${JSON.stringify(data)}`);
  return data;
}

const until = async (fn, ms = 20000, what = 'condition') => {
  const end = Date.now() + ms;
  for (;;) {
    const value = await fn().catch(() => null);
    if (value) return value;
    if (Date.now() > end) throw new Error(`timed out: ${what}`);
    await sleep(150);
  }
};

test('Settings does not repaint itself; drafts survive remote changes', { skip: !chromium || !SERVER, timeout: 240000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-settings-resync-'));
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  // A public URL makes Settings › Mobile create pairing codes by itself (as on a server).
  const server = spawn(SERVER, ['serve', '--root', path.join(dir, 'projects'), '--host', '127.0.0.1', '--port', String(port),
    '--no-demo', '--sessions-root', path.join(dir, 'sessions'), '--web-root', WEB, '--public-url', 'https://ark.example.test'],
  { stdio: 'ignore' });
  let browser;
  try {
    await until(() => fetch(base + '/api/health').then((r) => r.ok), 30000, 'server');
    const A = (await api(base, 'POST', '/api/projects', { name: 'Alpha' })).summary.id;
    await api(base, 'POST', '/api/units', { type: 'claim', title: 'Root' }, A);
    const revision = async () => (await api(base, 'GET', '/api/revision', null, A)).revision;

    browser = await chromium.launch({ headless: true, executablePath: process.env.ARK_TEST_BROWSER || undefined });
    const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    await context.addInitScript(() => {
      localStorage.setItem('ark.prefsV2', '1');
      localStorage.setItem('ark.lang', 'en');
      localStorage.setItem('ark.welcome.v1:local', '1');
      localStorage.setItem('ark.advanced', '1');
      window.__settingsPaints = 0;
      document.addEventListener('DOMContentLoaded', () => {
        new MutationObserver((muts) => {
          for (const m of muts) for (const n of m.addedNodes) {
            if (n.nodeType === 1 && n.querySelector?.('[data-settings-tab]')) window.__settingsPaints += 1;
          }
        }).observe(document.body, { childList: true, subtree: true });
      });
    });
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', (e) => errors.push(e.message));
    const pairs = [];
    page.on('request', (r) => { if (r.method() === 'POST' && new URL(r.url()).pathname === '/api/mobile/pair') pairs.push(Date.now()); });
    await page.goto(base + '/#/home');
    await until(() => page.evaluate((id) => window.__ARK__?.S?.project?.id === id && !document.getElementById('ark-startup'), A), 30000, 'boot');
    await page.locator('.guidance-invite .btn.ghost').click({ timeout: 3000 }).catch(() => {});
    // (The first visit may still move to Home once: ask again until Settings shows.)
    await until(() => page.evaluate(() => {
      if (!location.hash.startsWith('#/settings')) {
        localStorage.setItem('ark.settingsScope', 'project');
        localStorage.setItem('ark.settingsTab', 'agents');
        location.hash = '#/settings';
        return false;
      }
      return Boolean(document.querySelector('[data-settings-section="projectAgents"] input.input'));
    }), 20000, 'Settings › Agents & AI');
    await sleep(1500);
    // Count paints from here: the page is up (routing to it may paint it twice).
    await page.evaluate(() => { window.__settingsPaints = 1; });
    const paints = () => page.evaluate(() => window.__settingsPaints);

    // ---- 1. idle on Agents & AI for 30 s
    const rev0 = await revision();
    await sleep(30000);
    assert.equal(await paints(), 1, 'no repaint while idle');
    assert.equal(await revision(), rev0, 'the page wrote nothing');
    assert.equal(pairs.length, 0, 'no pairing code outside the Mobile section');

    // ---- 2. Access (Mobile): one code, no revision change
    await page.locator('[data-settings-tab="access"]').click();
    await until(async () => pairs.length >= 1, 10000, 'pairing code');
    await sleep(4000);
    assert.equal(pairs.length, 1, 'one pairing code');
    assert.equal(await revision(), rev0, 'a pairing code is not a project change');
    assert.equal(await paints(), 1);

    // ---- 3. an unsaved draft survives writes from elsewhere
    await page.locator('[data-settings-tab="agents"]').click();
    const field = page.locator('[data-settings-section="projectAgents"] input.input').first();
    await field.fill('MY_TOKEN_ENV');
    await page.locator('h1, .page').first().click({ position: { x: 5, y: 5 } }).catch(() => {});
    await page.evaluate(() => document.activeElement?.blur());
    for (let i = 0; i < 3; i++) {
      await api(base, 'POST', '/api/units', { type: 'note', title: `from elsewhere ${i}` }, A);
      await sleep(1500);
    }
    await api(base, 'PATCH', '/api/project/settings', { assistant: { model: 'other-model' } }, A);
    await sleep(5000);
    assert.equal(await paints(), 1, 'no repaint over an unsaved draft');
    assert.equal(await field.inputValue(), 'MY_TOKEN_ENV', 'the draft stays');
    assert.equal(pairs.length, 1);

    // ---- 4. clean page: a settings change from elsewhere repaints it
    await page.locator('[data-savebar-discard]').click();
    await api(base, 'PATCH', '/api/project/settings', { assistant: { model: 'third-model' } }, A);
    // (A settings-only change moves the revision but not the change cursor:
    // the page learns it with the next change, here a unit write.)
    await api(base, 'POST', '/api/units', { type: 'note', title: 'after the settings change' }, A);
    await until(async () => (await paints()) === 2, 15000, 'repaint for a settings change');
    assert.equal(await page.evaluate(() => window.__ARK__.S.project.settings?.assistant?.model), 'third-model');
    await sleep(4000);
    assert.equal(await paints(), 2, 'one repaint for it');
    assert.equal(pairs.length, 1, 'the repaint shows the code made before');
    assert.deepEqual(errors, []);
  } finally {
    await browser?.close();
    server.kill();
    await sleep(300);
    try { rmSync(dir, { recursive: true, force: true }); } catch { /* Windows may hold files briefly */ }
  }
});
