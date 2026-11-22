// Optional browser regression (Playwright):
//   node --test tests/browser/prompt_library.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge>; ARK_SCREENSHOT_DIR=<folder> keeps screenshots)
// Home → More prompts against a stub API that keeps prompts and pins:
// the default pins, pinning and reordering (saved to the server and shown
// on Home), adding a custom prompt with a placeholder, copying it with the
// unit filled in and the token, deleting it; English and Chinese.
const assert = require('node:assert/strict');
const { createServer } = require('node:http');
const { readFile, mkdir } = require('node:fs/promises');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch { /* Optional browser dependency. */ }

const PAGE = `<!doctype html><html><head><link rel="stylesheet" href="/css/app.css"></head>
<body><main class="ov-page" id="host" style="max-width:1100px;margin:20px auto"></main>
<div id="overlay-root"></div><div id="toast-root" class="toast-wrap"></div>
<script type="module">
  const { startPanel } = await import('/js/views/onboarding.js');
  const ctx = { S: { project: { id: 'prj_test', name: 'Test project', settings: {} }, session: { user: 'ana' }, stats: { total: 0 } }, navigate() {} };
  document.getElementById('host').appendChild(startPanel(ctx));
  window.__ready = true;
</script></body></html>`;

function stubServer(web, store) {
  return createServer(async (req, res) => {
    const url = new URL(req.url, 'http://localhost');
    const json = (body) => { res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify(body)); };
    const reply = () => ({ ok: true, personal: store.personal, project: store.project, pins: store.pins,
      can_edit_project: false, rules_version: 'r-test0001', warnings: [] });
    if (url.pathname === '/') { res.setHeader('Content-Type', 'text/html'); res.end(PAGE); return; }
    if (url.pathname.startsWith('/api/')) {
      let body = {};
      if (req.method === 'POST') {
        let raw = '';
        for await (const chunk of req) raw += chunk;
        try { body = JSON.parse(raw || '{}'); } catch { body = {}; }
      }
      if (url.pathname === '/api/prompts') {
        if (req.method === 'GET') return json(reply());
        store.posts.push(body);
        if (body.action === 'pins') store.pins = body.pins;
        if (body.action === 'save') {
          store.personal = [{ id: 'cp-0000aaaa', title: body.prompt.title, description: body.prompt.description, body: body.prompt.body,
            updated_at: '2026-11-22T00:00:00Z' }];
        }
        if (body.action === 'delete') store.personal = store.personal.filter((p) => p.id !== body.id);
        return json(reply());
      }
      if (url.pathname === '/api/agent-rules') {
        return json({ ok: true, effective: { version: 'r-test0001', lines: {
          en: ['CORE RULES (rules r-test0001): test.'], zh: ['核心规则（规则 r-test0001）：测试。'] } } });
      }
      if (url.pathname === '/api/personal/agent-tokens' && req.method === 'GET') return json({ ok: true, tokens: [] });
      if (url.pathname === '/api/personal/agent-tokens' && req.method === 'POST') return json({ ok: true, token: 'arka_lib1', id: 'tok_lib1' });
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

test('Home prompt library: pins, custom prompts, copy', { skip: !chromium, timeout: 120000 }, async () => {
  const web = path.resolve(__dirname, '../../ark_studio/web');
  const shots = process.env.ARK_SCREENSHOT_DIR || '';
  if (shots) await mkdir(shots, { recursive: true });
  let browser;
  const servers = [];
  try {
    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    for (const lang of ['en', 'zh']) {
      const store = { personal: [], project: [], pins: null, posts: [] };
      const server = stubServer(web, store);
      servers.push(server);
      await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
      const base = `http://127.0.0.1:${server.address().port}`;
      const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
      await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: base });
      await context.addInitScript((lang) => {
        localStorage.setItem('ark.prefsV2', '1');
        localStorage.setItem('ark.lang', lang);
        localStorage.setItem('ark.onboarding.hidden:prj_test', '0');
      }, lang);
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', (error) => errors.push(error.message));
      await page.goto(`${base}/?lang=${lang}`);
      await page.waitForFunction(() => window.__ready === true);
      const cards = () => page.evaluate(() => [...document.querySelectorAll('.start-card')].map((c) => c.dataset.prompt));
      const shot = async (name) => { if (shots) await page.screenshot({ path: path.join(shots, `prompt-library-${name}-${lang}.png`) }); };

      // Default pins: the two starter prompts.
      await page.locator('[data-more-prompts]').waitFor();
      assert.deepEqual(await cards(), ['setup', 'team'], lang);
      await shot('home');

      // The library lists the built-in prompts; pin "continue", move it up.
      await page.locator('[data-more-prompts]').click();
      await page.locator('[data-prompt-library-dialog]').waitFor();
      const rows = await page.locator('[data-library-prompt]').count();
      assert.ok(rows >= 10, `${lang}: ${rows} rows`);
      await shot('library');
      await page.locator('[data-library-pin="continue"]').click();
      await page.waitForFunction(() => document.querySelectorAll('.start-card').length === 3);
      assert.deepEqual(store.pins, ['setup', 'team', 'continue'], lang);
      await page.locator('[data-library-up="continue"]').click();
      await page.waitForFunction(() => document.querySelectorAll('.start-card')[1]?.dataset.prompt === 'continue');
      assert.deepEqual(store.pins, ['setup', 'continue', 'team'], lang);
      assert.equal(await page.locator('[data-library-pin="continue"]').getAttribute('aria-pressed'), 'true');

      // Add a custom prompt with a placeholder; it appears under My prompts.
      await page.locator('.plib-dialog .modal-foot .btn.primary').click();
      await page.locator('[data-prompt-editor]').waitFor();
      await page.locator('[data-prompt-field="title"]').fill('Weekly digest');
      await page.locator('[data-prompt-field="description"]').fill('What changed this week');
      await page.locator('[data-prompt-field="body"]').fill('Summarise {project} for {user}, starting from {unit}. Skip {when}.');
      await page.waitForFunction(() => /\{when\}/.test(document.querySelector('[data-prompt-warn]')?.textContent || ''));
      assert.equal(await page.locator('[data-prompt-field="scope"] option').count(), 1, `${lang}: no project scope without admin`);
      await shot('editor');
      await page.locator('.modal:has([data-prompt-editor]) .modal-foot .btn.primary').click();
      await page.locator('[data-library-prompt="cp-0000aaaa"]').waitFor();
      assert.equal(store.posts.find((p) => p.action === 'save')?.scope, 'personal', lang);

      // Pin it, copy it from the library with a unit: rules, filled placeholders, token.
      await page.locator('[data-library-pin="cp-0000aaaa"]').click();
      await page.waitForFunction(() => document.querySelector('.start-card[data-prompt="cp-0000aaaa"]'));
      await page.locator('[data-library-prompt="cp-0000aaaa"] .plib-unit').fill('T-0042');
      await shot('library-custom');
      await page.locator('[data-library-copy="cp-0000aaaa"]').click();
      await page.waitForFunction(() => [...document.querySelectorAll('.modal')].length === 1 && !document.querySelector('[data-prompt-library-dialog]'));
      await page.evaluate(() => [...document.querySelectorAll('.modal .modal-foot .btn.primary')].pop().click());
      await page.waitForFunction(() => document.querySelectorAll('.modal').length === 0, null, { timeout: 5000 });
      const copied = await page.evaluate(() => navigator.clipboard.readText());
      assert.match(copied, /arka_lib1/, lang);
      assert.match(copied, /r-test0001/, lang);
      assert.match(copied, /Summarise Test project \(prj_test\) for ana, starting from T-0042\. Skip \{when\}\./, lang);
      assert.ok(copied.indexOf('r-test0001') < copied.indexOf('Summarise'), lang);

      // Delete it: gone from the library and from Home.
      await page.locator('[data-more-prompts]').click();
      await page.locator('[data-library-delete="cp-0000aaaa"]').click();
      await page.evaluate(() => [...document.querySelectorAll('.modal .modal-foot .btn')].pop().click());
      await page.locator('[data-prompt-library-dialog]').waitFor();
      await page.waitForFunction(() => !document.querySelector('[data-library-prompt="cp-0000aaaa"]'));
      await page.waitForFunction(() => !document.querySelector('.start-card[data-prompt="cp-0000aaaa"]'));
      assert.ok(!store.pins.includes('cp-0000aaaa'), `${lang}: pin pruned`);

      // Restore the default pins.
      await page.locator('.plib-dialog .modal-foot .btn.ghost').nth(2).click();
      await page.waitForFunction(() => document.querySelectorAll('.start-card').length === 2);
      assert.equal(store.pins, null, lang);
      assert.deepEqual(errors, [], lang);
      await context.close();
    }
  } finally {
    await browser?.close();
    for (const server of servers) { server.closeAllConnections?.(); server.close(); }
  }
});
