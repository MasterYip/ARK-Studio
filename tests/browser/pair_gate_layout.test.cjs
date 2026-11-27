// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node --test tests/browser/pair_gate_layout.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge> to pick a browser; ARK_TEST_SHOTS=<dir> keeps screenshots)
// The gate pages in a wide window (the desktop app, a desktop browser):
// the pairing card's logo, eyebrow and fact chips sit on one row with their
// icons (2026-11-27 they stacked: their styles were phone-only), and a
// request refused as non-local shows "outside the service range" with no
// pairing form.  The session reply is faked; English and Chinese.
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

function freePort() {
  return new Promise((resolve, reject) => {
    const probe = net.createServer();
    probe.once('error', reject);
    probe.listen(0, '127.0.0.1', () => { const { port } = probe.address(); probe.close(() => resolve(port)); });
  });
}

function session(reason, mode) {
  return { ok: true, required: true, authenticated: false, token_present: false, reason,
    project: { id: 'p', name: 'ARK Studio' }, auth_required: false, cache_id: null, cache_schema: 1,
    connection: { mode, loopback_only: mode === 'loopback', public_url: null, advertised_urls: [], auth_required: false } };
}

// Elements whose vertical centres differ by less than half a line share a row.
async function sameRow(page, selA, selB) {
  const [a, b] = await Promise.all([page.locator(selA).first().boundingBox(), page.locator(selB).first().boundingBox()]);
  assert.ok(a && b, `${selA} / ${selB} visible`);
  return Math.abs((a.y + a.height / 2) - (b.y + b.height / 2)) < 8;
}

test('pairing and out-of-range gates in a wide window', { skip: !chromium || !SERVER, timeout: 240000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-gate-'));
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const server = spawn(SERVER, ['serve', '--root', path.join(dir, 'projects'), '--host', '127.0.0.1', '--port', String(port),
    '--sessions-root', path.join(dir, 'sessions'), '--web-root', path.resolve(__dirname, '../../ark_studio/web')],
  { stdio: ['ignore', 'pipe', 'pipe'], env: { ...process.env, ARK_PRIVATE_DIR: path.join(dir, 'private') } });
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
      for (const [reason, mode] of [['', 'lan'], ['not_local', 'loopback']]) {
        const context = await browser.newContext({ viewport: { width: 1280, height: 860 } });
        await context.addInitScript((l) => { localStorage.setItem('ark.prefsV2', '1'); localStorage.setItem('ark.lang', l); }, lang);
        await context.route('**/api/mobile/session*', (route) => route.fulfill({
          status: 200, contentType: 'application/json', body: JSON.stringify(session(reason, mode)) }));
        const page = await context.newPage();
        const errors = [];
        page.on('pageerror', (error) => errors.push(error.message));
        await page.goto(`${base}/?lang=${lang}`);
        await page.locator('.pair-gate-card').waitFor({ timeout: 30000 });
        if (process.env.ARK_TEST_SHOTS) {
          await page.screenshot({ path: path.join(process.env.ARK_TEST_SHOTS, `gate-${lang}-${reason || 'pair'}.png`) });
        }
        assert.ok(await sameRow(page, '.pair-gate-brand img', '.pair-gate-brand b'), 'logo beside "ARK Studio"');
        assert.ok(await sameRow(page, '.pair-gate-eyebrow .icon', '.pair-gate-eyebrow'), 'eyebrow icon beside its text');
        const title = await page.locator('.pair-gate-card h1').textContent();
        if (reason === 'not_local') {
          assert.match(title, lang === 'zh' ? /不在服务范围内/ : /outside the service range/);
          assert.equal(await page.locator('.pair-gate-input').count(), 0, 'no pairing form');
          assert.equal(await page.locator('.pair-gate-actions button').count(), 1, 'retry only on a loopback-only server');
          assert.match(await page.locator('.pair-gate-hint').textContent(), /127\.0\.0\.1/);
        } else {
          assert.equal(await page.locator('.pair-gate-input').count(), 1, 'phones get the code form');
          assert.ok(await sameRow(page, '.pair-gate-facts span .icon', '.pair-gate-facts span'), 'fact icon beside its text');
          const facts = await page.locator('.pair-gate-facts span').evaluateAll((els) => els.map((e) => e.getBoundingClientRect().height));
          assert.ok(facts.every((h) => h < 32), `fact chips are one line: ${facts}`);
        }
        assert.deepEqual(errors, []);
        await context.close();
      }
    }
  } finally {
    await browser?.close();
    server.kill();
    rmSync(dir, { recursive: true, force: true });
  }
});
