// Optional browser regression (Playwright):
//   node --test tests/browser/home_start_panel.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge> to pick a browser)
// The Home "Start with an agent" panel against a stub API: rapid Preview
// clicks open exactly one dialog that closes cleanly; rapid Copy clicks run
// one token flow (one token list request, one dialog, one token minted even
// on a double-click of Confirm); Preview never mints a token; the main thread
// never blocks for more than 200 ms; no keyboard listener is left behind.
// English and Chinese, with and without an existing personal token.
const assert = require('node:assert/strict');
const { createServer } = require('node:http');
const { readFile } = require('node:fs/promises');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch { /* Optional browser dependency. */ }

const PAGE = `<!doctype html><html><head><link rel="stylesheet" href="/css/app.css"></head>
<body><main class="ov-page" id="host" style="max-width:1100px;margin:20px auto"></main>
<div id="overlay-root"></div><div id="toast-root" class="toast-wrap"></div>
<script type="module">
  const { startPanel } = await import('/js/views/onboarding.js');
  const ctx = { S: { project: { id: 'prj_test', name: 'Test project', settings: {} }, stats: { total: 0 } }, navigate() {} };
  document.getElementById('host').appendChild(startPanel(ctx));
  window.__ready = true;
</script></body></html>`;

function stubServer(web, calls, state) {
  return createServer(async (req, res) => {
    const url = new URL(req.url, 'http://localhost');
    const json = (body) => { res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify(body)); };
    if (url.pathname === '/') { res.setHeader('Content-Type', 'text/html'); res.end(PAGE); return; }
    if (url.pathname.startsWith('/api/')) {
      const key = `${req.method} ${url.pathname}`;
      calls[key] = (calls[key] || 0) + 1;
      if (key === 'GET /api/skills/setup/state') {
        return json({ ok: true, state: 'proposed', host: { label: 'test-host' }, readiness: { ready: false },
          layers: { project: { state: 'proposed', digest: 'd1', can_confirm: true }, user: { state: 'unset' } } });
      }
      if (key === 'GET /api/personal/agent-tokens') {
        // A slow list makes rapid Copy clicks overlap the request.
        await new Promise((r) => setTimeout(r, 250));
        return json({ ok: true, tokens: state.existing ? [{ id: 'tok1', label: 'Home starter prompt – old', prefix: 'arka_old' }] : [] });
      }
      if (key === 'POST /api/personal/agent-tokens') {
        await new Promise((r) => setTimeout(r, 200));
        return json({ ok: true, token: `arka_test${calls[key]}` });
      }
      if (req.method === 'DELETE' && url.pathname.startsWith('/api/personal/agent-tokens/')) return json({ ok: true });
      return json({ ok: true });
    }
    const file = path.resolve(web, '.' + url.pathname);
    if (!file.startsWith(web + path.sep)) { res.writeHead(403).end(); return; }
    try {
      res.setHeader('Content-Type', file.endsWith('.js') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : 'text/plain');
      res.end(await readFile(file));
    } catch { res.writeHead(404).end(); }
  });
}

test('Home start panel: one preview dialog, one token flow, responsive', { skip: !chromium, timeout: 240000 }, async () => {
  const web = path.resolve(__dirname, '../../web');
  const calls = {};
  const state = { existing: false };
  const server = stubServer(web, calls, state);
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  const base = `http://127.0.0.1:${server.address().port}`;
  let browser;
  try {
    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    for (const lang of ['en', 'zh']) {
      for (const existing of [false, true]) {
        state.existing = existing;
        for (const key of Object.keys(calls)) delete calls[key];
        const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
        await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: base });
        await context.addInitScript((lang) => {
          localStorage.setItem('ark.prefsV2', '1');
          localStorage.setItem('ark.lang', lang);
          localStorage.setItem('ark.onboarding.hidden:prj_test', '0');
          window.__long = [];
          new PerformanceObserver((list) => { for (const e of list.getEntries()) window.__long.push(Math.round(e.duration)); })
            .observe({ type: 'longtask', buffered: true });
          // Count live document keydown listeners (modal key handlers).
          const add = document.addEventListener.bind(document);
          const remove = document.removeEventListener.bind(document);
          const live = new Set();
          document.addEventListener = (type, fn, opts) => { if (type === 'keydown') live.add(fn); return add(type, fn, opts); };
          document.removeEventListener = (type, fn, opts) => { if (type === 'keydown') live.delete(fn); return remove(type, fn, opts); };
          window.__keydown = () => live.size;
        }, lang);
        const page = await context.newPage();
        const errors = [];
        page.on('pageerror', (error) => errors.push(error.message));
        await page.goto(`${base}/?lang=${lang}`);
        await page.waitForFunction(() => window.__ready === true);
        await page.locator('[data-setup-review]').waitFor();
        const where = `${lang}${existing ? ' +token' : ''}`;
        const baseline = await page.evaluate(() => window.__keydown());
        await page.evaluate(() => { window.__long = []; });

        // One compact setup element, two cards, nothing stacked under them.
        const panel = await page.evaluate(() => ({
          cards: document.querySelectorAll('.start-card').length,
          setup: document.querySelectorAll('.start-status > *').length,
          notes: document.querySelectorAll('.start-panel .notice, .start-foot, .start-rules, .start-session').length,
        }));
        assert.deepEqual(panel, { cards: 2, setup: 1, notes: 0 }, where);

        // Ten Preview clicks as fast as possible, both cards.
        await page.evaluate(() => {
          for (let i = 0; i < 10; i += 1) document.querySelector(`[data-start-preview="${i % 2 ? 'team' : 'setup'}"]`).click();
        });
        await page.waitForTimeout(300);
        let snap = await page.evaluate(() => ({
          modals: document.querySelectorAll('.modal').length,
          dialogs: document.querySelectorAll('[data-start-preview-dialog]').length,
          prompt: document.querySelector('[data-start-preview-dialog]')?.dataset.prompt,
          text: document.querySelector('[data-start-preview-text]')?.textContent || '',
        }));
        assert.equal(snap.modals, 1, `${where}: one dialog`);
        assert.equal(snap.dialogs, 1, where);
        assert.equal(snap.prompt, 'team', where); // the last click wins, in place
        assert.match(snap.text, lang === 'en' ? /^You are my research agent/ : /^你是我的研究智能体/, where);
        assert.doesNotMatch(snap.text, /arka_/, `${where}: preview has no token`);
        assert.equal(calls['POST /api/personal/agent-tokens'] || 0, 0, `${where}: preview mints nothing`);
        assert.equal(calls['GET /api/personal/agent-tokens'] || 0, 0, `${where}: preview lists no tokens`);
        // Playwright clicks on the page behind are blocked by the backdrop.
        await page.locator('[data-start-preview="setup"]').click({ force: true, timeout: 2000 }).catch(() => {});
        assert.equal(await page.locator('.modal').count(), 1, where);
        await page.keyboard.press('Escape');
        await page.waitForTimeout(100);
        assert.equal(await page.locator('.modal').count(), 0, `${where}: Escape closes`);
        assert.equal(await page.evaluate(() => window.__keydown()), baseline, `${where}: no key handler left`);

        // Preview again, close with the button.
        await page.locator('[data-start-preview="setup"]').click();
        await page.locator('.start-preview-dialog .modal-foot .btn.ghost').click();
        assert.equal(await page.locator('.modal').count(), 0, `${where}: Close closes`);

        // Copy four times quickly: one token list request, one dialog.
        await page.evaluate(() => { for (let i = 0; i < 4; i += 1) document.querySelector('[data-copy-token="setup"]').click(); });
        await page.locator('.modal').waitFor();
        await page.waitForTimeout(400);
        assert.equal(calls['GET /api/personal/agent-tokens'], 1, `${where}: one token list request`);
        assert.equal(await page.locator('.modal').count(), 1, `${where}: one token dialog`);
        assert.equal(await page.locator('.agent-token-revoke-old').count(), existing ? 1 : 0, where);
        // Double-click Confirm: still one token.
        await page.evaluate(() => {
          const button = [...document.querySelectorAll('.modal .modal-foot .btn.primary')].pop();
          button.click(); button.click();
        });
        await page.waitForFunction(() => document.querySelectorAll('.modal').length === 0, null, { timeout: 5000 });
        assert.equal(calls['POST /api/personal/agent-tokens'], 1, `${where}: one token minted`);
        assert.equal(calls['DELETE /api/personal/agent-tokens/tok1'] || 0, existing ? 1 : 0, where);
        const copied = await page.evaluate(() => navigator.clipboard.readText());
        assert.match(copied, /arka_test1/, where);
        assert.match(copied, lang === 'en' ? /^You are my research agent/ : /^你是我的研究智能体/, where);

        // Copy from inside the preview dialog: the token dialog replaces it.
        await page.locator('[data-start-preview="team"]').click();
        await page.locator('.start-preview-dialog .modal-foot .btn.primary').click();
        await page.waitForFunction(() => !document.querySelector('[data-start-preview-dialog]') && document.querySelectorAll('.modal').length === 1);
        await page.keyboard.press('Escape');
        await page.waitForFunction(() => document.querySelectorAll('.modal').length === 0);
        // The flow is released: Copy works again.
        await page.locator('[data-copy-token="team"]').click();
        await page.locator('.modal').waitFor();
        await page.keyboard.press('Escape');
        await page.waitForFunction(() => document.querySelectorAll('.modal').length === 0);
        assert.equal(await page.evaluate(() => window.__keydown()), baseline, `${where}: no key handler left`);

        const long = await page.evaluate(() => window.__long.slice());
        assert.ok(long.every((ms) => ms <= 200), `${where}: long tasks ${JSON.stringify(long)}`);
        const t0 = Date.now();
        await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => r())));
        assert.ok(Date.now() - t0 < 1000, `${where}: page responsive`);
        assert.deepEqual(errors, [], where);
        await context.close();
      }
    }
  } finally {
    await browser?.close();
    server.close();
  }
});
