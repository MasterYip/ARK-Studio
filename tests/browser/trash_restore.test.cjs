// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node --test tests/browser/trash_restore.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge> to pick a browser)
// Recycle bin: a deleted node shows in Settings > Project > Recycle bin;
// Restore brings it back with its part_of link; Delete permanently and Empty
// recycle bin ask first and remove nodes for good.  English and Chinese.
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

async function api(base, method, route, body) {
  const res = await fetch(base + route, {
    method, headers: body ? { 'Content-Type': 'application/json', Origin: base } : { Origin: base },
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await res.json();
  if (!res.ok) throw new Error(`${method} ${route}: ${res.status} ${JSON.stringify(data)}`);
  return data;
}

test('recycle bin: delete, restore with links, delete permanently, empty', { skip: !chromium || !SERVER, timeout: 240000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-trash-'));
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const server = spawn(SERVER, ['serve', '--root', path.join(dir, 'projects'), '--host', '127.0.0.1', '--port', String(port),
    '--no-demo', '--sessions-root', path.join(dir, 'sessions'), '--web-root', path.resolve(__dirname, '../../ark_studio/web')],
  { stdio: ['ignore', 'pipe', 'pipe'] });
  let log = '';
  server.stdout.on('data', (chunk) => { log += chunk; });
  server.stderr.on('data', (chunk) => { log += chunk; });
  let browser;
  try {
    for (let i = 0; ; i += 1) {
      try { await api(base, 'GET', '/api/health'); break; } catch (err) {
        if (i > 100) throw new Error(`server did not start: ${err.message}\n${log}`);
        await new Promise((r) => setTimeout(r, 200));
      }
    }
    await api(base, 'POST', '/api/projects', { name: 'Trash test' });
    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    for (const lang of ['en', 'zh']) {
      const mk = async (title) => (await api(base, 'POST', '/api/units', { type: 'idea', title: `${title} ${lang}` })).unit.uid;
      const parent = await mk('Parent'), child = await mk('Child'), other = await mk('Other');
      await api(base, 'POST', '/api/links', { source: child, target: parent, relation: 'part_of' });
      for (const uid of [child, other]) await api(base, 'DELETE', `/api/units/${uid}`);

      const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
      await context.addInitScript((lang) => {
        localStorage.setItem('ark.prefsV2', '1');
        localStorage.setItem('ark.lang', lang);
      }, lang);
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', (error) => errors.push(error.message));
      await page.goto(`${base}/?settings=trash&lang=${lang}#/settings`);
      const panel = page.locator('[data-trash-panel]');
      await panel.waitFor({ timeout: 30000 });
      await page.locator(`[data-trash-uid="${child}"]`).waitFor();
      const title = await panel.locator('h3').textContent();
      assert.equal(title, lang === 'zh' ? '回收站' : 'Recycle bin');
      assert.equal(await page.locator('.trash-item').count(), 2, lang);
      await page.screenshot({ path: path.join(process.env.ARK_TEST_SHOTS || dir, `trash-${lang}.png`), fullPage: false });

      // Restore: the row leaves, the node and its part_of link are back.
      await page.locator(`[data-trash-restore="${child}"]`).click();
      await page.locator(`[data-trash-uid="${child}"]`).waitFor({ state: 'detached' });
      const unit = (await api(base, 'GET', `/api/units/${child}`)).unit;
      assert.equal(unit.archived, false);
      assert.deepEqual((unit.links_out || []).map((link) => link.relation), ['part_of']);

      // Delete permanently asks first; Cancel keeps the node.
      await page.locator(`[data-trash-purge="${other}"]`).click();
      const modal = page.locator('.modal');
      await modal.waitFor();
      await modal.getByRole('button', { name: lang === 'zh' ? '取消' : 'Cancel' }).click();
      assert.equal(await page.locator(`[data-trash-uid="${other}"]`).count(), 1);
      await page.locator(`[data-trash-purge="${other}"]`).click();
      await page.locator('.modal').getByRole('button', { name: lang === 'zh' ? '彻底删除' : 'Delete permanently' }).click();
      await page.locator(`[data-trash-uid="${other}"]`).waitFor({ state: 'detached' });
      assert.equal((await api(base, 'GET', '/api/trash')).total, 0);

      // Empty recycle bin.
      await api(base, 'DELETE', `/api/units/${parent}`);
      await page.reload();
      await page.locator(`[data-trash-uid="${parent}"]`).waitFor();
      await page.locator('[data-trash-empty]').click();
      await page.locator('.modal').getByRole('button', { name: lang === 'zh' ? '清空回收站' : 'Empty recycle bin' }).click();
      await page.locator('.trash-item').first().waitFor({ state: 'detached' });
      assert.equal((await api(base, 'GET', '/api/trash')).total, 0);
      assert.deepEqual(errors, [], lang);
      await context.close();
    }
  } finally {
    if (browser) await browser.close();
    server.kill();
    await new Promise((r) => (server.exitCode !== null ? r() : server.once('exit', r)));
    rmSync(dir, { recursive: true, force: true });
  }
});
