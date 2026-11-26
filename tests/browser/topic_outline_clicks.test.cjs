// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node tests/browser/topic_outline_clicks.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge>; ARK_TEST_SHOTS=<dir> keeps the screenshots)
// Topic › Document: a click on a heading folds or unfolds it (a heading with
// nothing to fold is only selected); a double click opens Node details for that
// node, leaves the fold as it was and opens no Editor tab; Enter opens details,
// Space and ← / → fold; the context menu still offers "Open in Editor".
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

const until = async (fn, ms = 20000) => {
  const end = Date.now() + ms;
  for (;;) {
    const value = await fn();
    if (value) return value;
    if (Date.now() > end) throw new Error('timed out');
    await new Promise((r) => setTimeout(r, 100));
  }
};

// Editor tabs live in localStorage (ark.editor…): how often `uid` appears there.
const editorMentions = (page, uid) => page.evaluate((uid) => Object.keys(localStorage)
  .filter((key) => key.includes('editor'))
  .reduce((n, key) => n + (String(localStorage.getItem(key)).split(uid).length - 1), 0), uid);

test('topic outline: click folds, double click opens details, menu opens the Editor', { skip: !chromium || !SERVER, timeout: 180000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-outline-'));
  const shots = process.env.ARK_TEST_SHOTS || dir;
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
    await until(() => api(base, 'GET', '/api/health').then(() => true, () => false), 30000)
      .catch(() => { throw new Error(`server did not start\n${log}`); });
    await api(base, 'POST', '/api/projects', { name: 'Outline test' });
    const mk = async (data) => (await api(base, 'POST', '/api/units', data)).unit.uid;
    const parent = await mk({ type: 'idea', title: 'Parent idea' });
    const child = await mk({ type: 'claim', title: 'Child claim', links: [{ target: parent, relation: 'part_of' }] });
    const leaf = await mk({ type: 'task', title: 'Leaf task' });
    const topic = await mk({ type: 'topic', title: 'Outline topic', members: [parent, child, leaf] });

    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    await context.addInitScript(() => {
      localStorage.setItem('ark.prefsV2', '1');
      localStorage.setItem('ark.lang', 'zh');
      localStorage.setItem('ark.welcome.v1:local', '1');
    });
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', (error) => errors.push(error.message));
    await page.goto(`${base}/?lang=zh#/research/topic/${topic}`);
    await page.locator('.guidance-invite .btn.ghost').click({ timeout: 4000 }).catch(() => {});
    const outlineBtn = page.locator('.topic-toolbar button', { hasText: '文档' }).first();
    await outlineBtn.waitFor({ timeout: 30000 });
    await outlineBtn.click();

    const section = (uid) => page.locator(`.topic-art-section[data-unit-ref="${uid}"]`);
    const head = (uid) => section(uid).locator(':scope > .topic-art-head');
    const folded = (uid) => section(uid).evaluate((el) => el.classList.contains('folded'));
    await head(parent).waitFor({ timeout: 20000 });
    await page.locator('[data-fold="unfold"]').click();
    assert.equal(await folded(parent), false);
    const hashBefore = await page.evaluate(() => location.hash);
    assert.equal(await editorMentions(page, parent), 0);

    // 1. Single click toggles the fold, at once.
    await head(parent).click();
    assert.equal(await folded(parent), true, 'click folds');
    assert.equal(await section(child).isVisible(), false, 'children hidden');
    await head(parent).click();
    assert.equal(await folded(parent), false, 'click unfolds');
    await page.waitForTimeout(400); // not a double click with the next one

    // A heading with nothing to fold is selected, nothing else.
    await head(leaf).click();
    assert.equal(await head(leaf).evaluate((el) => el.classList.contains('selected')), true, 'leaf selected');
    assert.equal(await page.locator('.drawer-head .uid', { hasText: leaf }).count(), 0, 'single click: no details');
    await page.waitForTimeout(400);

    // 2. Double click: details for that node, fold unchanged, no Editor tab.
    await head(parent).dblclick();
    await page.locator('.drawer-head .uid', { hasText: parent }).first().waitFor({ timeout: 10000 });
    assert.equal(await folded(parent), false, 'double click leaves the fold as it was');
    assert.equal(await page.evaluate(() => location.hash), hashBefore, 'still on the topic page');
    assert.equal(await editorMentions(page, parent), 0, 'no Editor tab');
    await page.screenshot({ path: path.join(shots, 'outline-dblclick.png') });

    // Folded parent: a double click keeps it folded.
    await head(parent).click();
    assert.equal(await folded(parent), true);
    await page.waitForTimeout(400);
    await head(parent).dblclick();
    assert.equal(await folded(parent), true, 'double click keeps a folded heading folded');
    await page.waitForTimeout(400);

    // A leaf: double click opens its details too.
    await head(leaf).dblclick();
    await page.locator('.drawer-head .uid', { hasText: leaf }).first().waitFor({ timeout: 10000 });
    assert.equal(await editorMentions(page, leaf), 0);

    // 4. Keyboard: Space / → / ← fold, Enter opens details.
    await head(parent).focus();
    await page.keyboard.press('ArrowRight');
    assert.equal(await folded(parent), false, '→ unfolds');
    await page.keyboard.press('ArrowLeft');
    assert.equal(await folded(parent), true, '← folds');
    await page.keyboard.press('Space');
    assert.equal(await folded(parent), false, 'Space toggles');
    await head(child).focus();
    await page.keyboard.press('Enter');
    await page.locator('.drawer-head .uid', { hasText: child }).first().waitFor({ timeout: 10000 });
    assert.equal(await editorMentions(page, child), 0, 'Enter: details, not the Editor');

    // 5. The context menu still opens the Editor.
    await head(leaf).click({ button: 'right' });
    const menu = page.locator('.graph-menu').last();
    await menu.waitFor({ timeout: 5000 });
    const open = menu.locator('button, [role="menuitem"]', { hasText: '在编辑器中打开' }).first();
    assert.equal(await open.count(), 1, 'menu offers 在编辑器中打开');
    await open.click();
    await until(async () => (await editorMentions(page, leaf)) > 0, 10000);
    assert.deepEqual(errors, []);
    await context.close();
  } finally {
    await browser?.close();
    server.kill();
    await new Promise((r) => setTimeout(r, 300));
    if (!process.env.ARK_TEST_SHOTS) rmSync(dir, { recursive: true, force: true });
  }
});
