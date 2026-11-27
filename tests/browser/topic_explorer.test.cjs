// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node tests/browser/topic_explorer.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge>; ARK_TEST_SHOTS=<dir> keeps the screenshots)
// The Topics workbench view (left sidebar, its own activity-bar button):
// topics nested by part_of, lines as branch paths, and the open topic's
// outline with the topic itself as level 0.  An outline row selects the node
// in the graph and opens its details; folding works by chevron and Ctrl+K
// chords.  Elsewhere a click opens the topic.  The topic page has no strip
// and no Canvas view; its head shows a short plain-text body only, and the
// body is always level 0 of Document.  Without the workbench shell the view
// is a drawer on the topic page.
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

test('topics view: sidebar view, trees, outline, document level 0, head body, drawer', { skip: !chromium || !SERVER, timeout: 300000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-topic-ex-'));
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
    await api(base, 'POST', '/api/projects', { name: 'Explorer test' });
    const mk = async (data) => (await api(base, 'POST', '/api/units', data)).unit.uid;
    const parent = await mk({ type: 'idea', title: 'Parent idea', tags: ['branch:alpha'] });
    const child = await mk({ type: 'claim', title: 'Child claim', tags: ['branch:alpha/beta'], links: [{ target: parent, relation: 'part_of' }] });
    const leaf = await mk({ type: 'task', title: 'Leaf task', tags: ['branch:gamma/delta'] });
    const outer = await mk({ type: 'topic', title: 'Outer topic', body: 'Why this topic exists: **the plan** for the pilot.', members: [parent, child, leaf] });
    const inner = await mk({ type: 'topic', title: 'Inner topic', body: '## Plan\n\nA heading makes this rich text.', members: [leaf], links: [{ target: outer, relation: 'part_of' }] });
    const long = await mk({ type: 'topic', title: 'Long topic', body: 'A long plain paragraph. '.repeat(40), members: [leaf] });
    const listy = await mk({ type: 'topic', title: 'List topic', body: '- one\n- two', members: [leaf] });

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
    await page.goto(`${base}/?lang=zh#/research/topic/${outer}`);
    await page.locator('.guidance-invite .btn.ghost').click({ timeout: 4000 }).catch(() => {});
    const explorer = page.locator('.wb-view[data-view="topics"] [data-topic-explorer]');
    await explorer.locator(`[data-outline-row="${child}"]`).waitFor({ timeout: 30000 });

    // A workbench view with its own activity-bar button, not a panel in the page.
    assert.equal(await page.locator('[data-view-icon="topics"]').count(), 1);
    assert.equal(await page.locator('.topic-page [data-topic-explorer], .topic-page [data-topic-ex-toggle]').count(), 0);
    // No topic strip, no Canvas view.
    assert.equal(await page.locator('.topic-bar, .topic-pill, .topic-body-expand').count(), 0);
    const views = await page.locator('.topic-toolbar .view-toggle button').allTextContents();
    assert.deepEqual(views.map((text) => text.trim()), ['图', '文档', '时间线']);
    assert.equal(await page.locator('.topic-canvas canvas').count() > 0, true, 'the graph is the main view');
    // A short plain-text body shows under the title, in full.
    const headBody = page.locator('.topic-head-body');
    await until(async () => (await headBody.isVisible()) && /the plan/.test(await headBody.textContent()), 10000);
    const layoutSel = page.locator('.topic-canvas select.graph-sel').first();
    assert.equal(await layoutSel.inputValue(), 'force', 'a topic opens in the Force layout');
    assert.equal(await page.locator('[data-topic-auto-layout]').isVisible(), false, 'no Auto layout for Force');
    // A picked layout is kept for every topic of the project.
    await layoutSel.selectOption('node');
    assert.equal(await page.locator('[data-topic-auto-layout]').isVisible(), true);

    // Topics: the inner topic sits under the outer one (part_of).
    const topicRows = await explorer.locator('[data-topic-row]').evaluateAll((rows) => rows.map((row) => [row.dataset.topicRow, row.getAttribute('aria-level')]));
    assert.deepEqual(topicRows.filter(([uid]) => uid === outer || uid === inner), [[outer, '1'], [inner, '2']]);
    assert.equal(await explorer.locator(`[data-topic-row="${outer}"]`).getAttribute('aria-selected'), 'true');

    // Lines: a tree by path, a parent path gets its own row.
    const lineRows = await explorer.locator('[data-line-row]').evaluateAll((rows) => rows.map((row) => row.dataset.lineRow));
    assert.deepEqual(lineRows, ['alpha', 'alpha/beta', 'gamma', 'gamma/delta']);

    // Outline: the topic is level 0, the child sits under the parent; a
    // click selects it and opens details.
    assert.equal(await explorer.locator('[data-outline-root]').getAttribute('data-outline-row'), outer);
    assert.equal(await explorer.locator(`[data-outline-row="${parent}"]`).getAttribute('aria-level'), '2');
    assert.equal(await explorer.locator(`[data-outline-row="${child}"]`).getAttribute('aria-level'), '3');
    await explorer.locator(`[data-outline-row="${child}"]`).click();
    await page.locator('.drawer-head .uid', { hasText: child }).first().waitFor({ timeout: 10000 });
    assert.equal(await explorer.locator(`[data-outline-row="${child}"]`).getAttribute('aria-selected'), 'true');
    await page.screenshot({ path: path.join(shots, 'explorer-outline-select.png') });

    // Folding: the chevron, then Ctrl+K Ctrl+J / Ctrl+K Ctrl+0.
    await explorer.locator(`[data-outline-row="${parent}"] .topic-ex-chevron`).click();
    assert.equal(await explorer.locator(`[data-outline-row="${child}"]`).count(), 0, 'chevron folds');
    await page.locator('.topic-page-title').click();
    await page.keyboard.press('Control+k');
    await page.keyboard.press('Control+j');
    await explorer.locator(`[data-outline-row="${child}"]`).waitFor({ timeout: 5000 });
    await page.keyboard.press('Control+k');
    await page.keyboard.press('Control+0');
    await until(async () => (await explorer.locator(`[data-outline-row="${child}"]`).count()) === 0, 5000);

    // Document: the topic's body is the level-0 section, in full; Edit opens its details.
    await page.locator('.topic-toolbar .view-toggle button', { hasText: '文档' }).click();
    const root = page.locator('.topic-art-root');
    await root.waitFor({ timeout: 10000 });
    assert.equal((await root.locator('h1.topic-art-title').textContent()).trim(), 'Outer topic');
    assert.match(await root.locator('.topic-art-body').textContent(), /Why this topic exists/);
    assert.equal(await page.locator('.topic-art-section').first().getAttribute('data-unit-ref'), outer, 'level 0 comes first');
    await root.locator('[data-topic-edit-body]').click();
    await page.locator('.drawer-head .uid', { hasText: outer }).first().waitFor({ timeout: 10000 });
    await page.screenshot({ path: path.join(shots, 'document-level0.png') });
    await page.locator('.topic-toolbar .view-toggle button', { hasText: '图' }).click();

    // A line opens its automatic topic.
    await explorer.locator('[data-line-row="alpha"]').click();
    await until(async () => decodeURIComponent(await page.evaluate(() => location.hash)).includes('branch:alpha'), 10000);
    await explorer.locator(`[data-outline-row="${child}"]`).waitFor({ timeout: 10000 });
    assert.equal(await explorer.locator('[data-line-row="alpha"]').getAttribute('aria-selected'), 'true');

    // The nested topic opens from the tree, in Force (its own default); its
    // body has a heading, so the head shows no body; Document still has it.
    await explorer.locator(`[data-topic-row="${inner}"]`).click();
    await explorer.locator(`[data-outline-row="${leaf}"]`).waitFor({ timeout: 10000 });
    assert.equal(await explorer.locator(`[data-outline-row="${parent}"]`).count(), 0);
    assert.equal(await page.locator('.topic-canvas select.graph-sel').first().inputValue(), 'node', 'another topic keeps the picked layout');
    assert.equal(await page.locator('.topic-head-body').isVisible(), false, 'a heading keeps the body out of the head');
    // A long plain body and a list stay out of the head too.
    for (const uid of [long, listy]) {
      await explorer.locator(`[data-topic-row="${uid}"]`).click();
      await until(async () => (await page.locator('.topic-title-row .uid').textContent()) === uid, 10000);
      await page.waitForTimeout(300);
      assert.equal(await page.locator('.topic-head-body').isVisible(), false, `${uid}: no body in the head`);
    }
    await explorer.locator(`[data-topic-row="${outer}"]`).click();
    await explorer.locator(`[data-outline-row="${parent}"]`).waitFor({ timeout: 10000 });
    assert.equal(await page.locator('.topic-canvas select.graph-sel').first().inputValue(), 'node', 'back on the outer topic: still Node');

    // Elsewhere: the outline has its empty state; a topic click opens the page.
    await page.goto(`${base}/?lang=zh#/map`);
    await explorer.locator('[data-topic-row]').first().waitFor({ timeout: 30000 });
    await until(async () => (await explorer.locator('[data-outline-row]').count()) === 0, 10000);
    assert.match(await explorer.locator('[data-sec="outline"] .ed-tree-note').textContent(), /打开一个专题/);
    await explorer.locator(`[data-topic-row="${outer}"]`).click();
    await until(async () => (await page.evaluate(() => location.hash)).includes(outer), 10000);
    await explorer.locator(`[data-outline-row="${leaf}"]`).waitFor({ timeout: 10000 });

    // Hidden like any view: the left sidebar closes and stays closed after a reload.
    await page.locator('[data-view-icon="topics"]').click();
    assert.equal(await explorer.isVisible(), false);
    await page.reload();
    await page.locator('[data-view-icon="topics"]').waitFor({ timeout: 30000 });
    await page.waitForTimeout(800);
    assert.equal(await explorer.isVisible(), false, 'hidden state is kept');
    await page.locator('[data-view-icon="topics"]').click();
    assert.equal(await explorer.isVisible(), true);

    // Without the shell (phone): a drawer on the topic page, closed by the backdrop.
    await page.setViewportSize({ width: 390, height: 844 });
    const drawerBtn = page.locator('.topic-page [data-topic-ex-toggle]');
    await drawerBtn.waitFor({ timeout: 20000 });
    const drawer = page.locator('.topic-page [data-topic-explorer]');
    assert.equal(await drawer.isVisible(), false, 'closed on a phone');
    await drawerBtn.click();
    const box = await drawer.boundingBox();
    assert.ok(box && box.x > 0 && box.x + box.width <= 391, 'drawer inside the screen');
    await drawer.locator(`[data-outline-row="${leaf}"]`).waitFor({ timeout: 10000 });
    await page.screenshot({ path: path.join(shots, 'explorer-phone.png') });
    await page.mouse.click(20, 400);
    assert.equal(await drawer.isVisible(), false, 'backdrop closes it');
    // Back to a wide window: the view returns to the sidebar.
    await page.setViewportSize({ width: 1440, height: 900 });
    await explorer.waitFor({ timeout: 20000 });
    assert.deepEqual(errors, []);
    await context.close();
  } finally {
    await browser?.close();
    server.kill();
    await new Promise((r) => setTimeout(r, 300));
    if (!process.env.ARK_TEST_SHOTS) rmSync(dir, { recursive: true, force: true });
  }
});
