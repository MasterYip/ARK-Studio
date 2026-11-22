// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node --test tests/browser/node_markers.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge> to pick a browser; ARK_TEST_SHOTS=<dir> keeps screenshots)
// Node markers on the Map's mind map: a star added from the context menu is
// checked there; choosing it again removes it (never two of one star);
// another star replaces it (exclusive group) while a symbol stays; Ctrl+Z
// undoes; the inspector chip removes a marker; two selected nodes get a
// flag in one batch.  English and Chinese.
const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const { mkdtempSync } = require('node:fs');
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

async function until(check, what, ms = 15000) {
  const end = Date.now() + ms;
  let last;
  for (;;) {
    last = await check();
    if (last) return last;
    if (Date.now() > end) throw new Error(`timed out: ${what}`);
    await new Promise((r) => setTimeout(r, 150));
  }
}

const TEXT = {
  en: { markers: 'Markers', yellow: 'Yellow star', red: 'Red star', question: 'Question', flag: 'Red flag', clear: 'Clear markers' },
  zh: { markers: '标记', yellow: '黄色星标', red: '红色星标', question: '问题', flag: '红旗', clear: '清除所有标记' },
};

test('node markers: toggle, one per group, undo, inspector chip, batch', { skip: !chromium || !SERVER, timeout: 300000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-markers-'));
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
    for (let i = 0; ; i += 1) {
      try { await api(base, 'GET', '/api/health'); break; } catch (err) {
        if (i > 100) throw new Error(`server did not start: ${err.message}\n${log}`);
        await new Promise((r) => setTimeout(r, 200));
      }
    }
    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    for (const lang of ['en', 'zh']) {
      const L = TEXT[lang];
      await api(base, 'POST', '/api/projects', { name: `Markers ${lang}` });
      const mk = async (title, extra = {}) => (await api(base, 'POST', '/api/units', { type: 'task', title, ...extra })).unit.uid;
      const root = await mk('Plan', { type: 'note' });
      const a = await mk('Alpha', { tags: ['seq:1'] });
      const b = await mk('Beta', { tags: ['seq:2'] });
      for (const uid of [a, b]) await api(base, 'POST', '/api/links', { source: uid, target: root, relation: 'part_of' });
      const markersOf = async (uid) => (await api(base, 'GET', `/api/units/${uid}`)).unit.tags.filter((tag) => tag.startsWith('marker:'));

      const context = await browser.newContext({ viewport: { width: 1400, height: 900 }, bypassCSP: true });
      await context.addInitScript((lang) => {
        localStorage.setItem('ark.prefsV2', '1');
        localStorage.setItem('ark.lang', lang);
        localStorage.setItem('ark.graphMode', 'edit');
        localStorage.setItem('ark.graphLayout', 'mind');
        localStorage.setItem('ark.inspectorPane', '1');
      }, lang);
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', (error) => errors.push(error.message));
      await page.goto(`${base}/?lang=${lang}&graph=mind#/map`);
      const findGraph = () => page.waitForFunction(() => (window.__ARK__?.graphs || []).some((g) => g.layout === 'mind' && g.byId?.size >= 3),
        null, { timeout: 30000 });
      await findGraph();
      const at = (uid) => page.evaluate((uid) => {
        const graph = window.__ARK__.graphs.find((g) => g.layout === 'mind' && g.byId?.size >= 3);
        const node = graph.nodes.find((n) => n.uid === uid);
        const rect = graph.canvas.getBoundingClientRect();
        return { x: rect.left + node.x * graph.zoom + graph.width / 2 + graph.pan.x,
          y: rect.top + node.y * graph.zoom + graph.height / 2 + graph.pan.y };
      }, uid);
      const drawnMarkers = (uid) => page.evaluate((uid) => {
        const graph = window.__ARK__.graphs.find((g) => g.layout === 'mind' && g.byId?.size >= 3);
        return (graph.nodes.find((n) => n.uid === uid).tags || []).filter((tag) => tag.startsWith('marker:'));
      }, uid);
      await page.locator('.guidance-invite .btn.ghost').click({ timeout: 4000 }).catch(() => {});
      await page.waitForTimeout(800);

      // Open the node menu › Markers and return the submenu item for `label`.
      const markerItem = async (uid, label) => {
        const p = await at(uid);
        await page.mouse.click(p.x, p.y);
        await page.mouse.click(p.x, p.y, { button: 'right' });
        const menu = page.locator('.graph-menu').first();
        await menu.waitFor({ timeout: 5000 });
        await menu.locator(':scope > .graph-menu-item', { hasText: L.markers }).first().hover();
        const item = page.locator('.graph-menu-sub .graph-menu-item', { hasText: label }).first();
        await item.waitFor({ timeout: 5000 });
        return item;
      };
      const closeMenu = () => page.keyboard.press('Escape');

      // 1. Add a yellow star.
      await (await markerItem(a, L.yellow)).click();
      await until(async () => (await markersOf(a)).join() === 'marker:star-yellow', 'yellow star saved');
      // 2. The menu shows it checked; choosing it again removes it (no second star).
      let item = await markerItem(a, L.yellow);
      assert.equal(await item.getAttribute('aria-checked'), 'true');
      await page.screenshot({ path: path.join(shots, `markers-menu-${lang}.png`) });
      await item.click();
      await until(async () => (await markersOf(a)).length === 0, 'yellow star removed');
      assert.deepEqual(await drawnMarkers(a), []);
      // 3. Yellow star + question, then a red star replaces the yellow one.
      await (await markerItem(a, L.yellow)).click();
      await until(async () => (await markersOf(a)).length === 1, 'yellow again');
      await (await markerItem(a, L.question)).click();
      await until(async () => (await markersOf(a)).length === 2, 'question added');
      await (await markerItem(a, L.red)).click();
      await until(async () => (await markersOf(a)).sort().join() === 'marker:question,marker:star-red', 'red replaces yellow');
      assert.deepEqual((await drawnMarkers(a)).sort(), ['marker:question', 'marker:star-red']);
      // 4. Ctrl+Z puts the yellow star back.
      const pa = await at(a);
      await page.mouse.click(pa.x, pa.y);
      await page.keyboard.press('Control+z');
      await until(async () => (await markersOf(a)).sort().join() === 'marker:question,marker:star-yellow', 'undo');
      // 5. The inspector lists the markers; clicking a chip removes that marker.
      const chips = page.locator('[data-markers] .marker-chip');
      await until(async () => (await chips.count()) === 2, 'inspector marker chips');
      await page.screenshot({ path: path.join(shots, `markers-inspector-${lang}.png`) });
      await page.locator('[data-markers] .marker-chip[data-marker="marker:star-yellow"]').click();
      await until(async () => (await markersOf(a)).join() === 'marker:question', 'chip removed the star');
      // 6. Two selected nodes: one flag on both, then Clear markers on both.
      const pb = await at(b);
      await page.mouse.click(pa.x, pa.y);
      await page.keyboard.down('Control');
      await page.mouse.click(pb.x, pb.y);
      await page.keyboard.up('Control');
      await page.mouse.click(pb.x, pb.y, { button: 'right' });
      await page.locator('.graph-menu > .graph-menu-item', { hasText: L.markers }).first().hover();
      await page.locator('.graph-menu-sub .graph-menu-item', { hasText: L.flag }).first().click();
      await until(async () => (await markersOf(a)).includes('marker:flag-red') && (await markersOf(b)).join() === 'marker:flag-red', 'batch flag');
      await page.mouse.click(pb.x, pb.y, { button: 'right' });
      await page.locator('.graph-menu > .graph-menu-item', { hasText: L.markers }).first().hover();
      await page.locator('.graph-menu-sub .graph-menu-item', { hasText: L.clear }).first().click();
      await until(async () => !(await markersOf(a)).length && !(await markersOf(b)).length, 'cleared');
      await closeMenu();

      // 7. Settings › Appearance › Node markers lists the groups.
      await page.goto(`${base}/?lang=${lang}&settings=markers#/settings`);
      await page.locator('[data-settings-markers]').waitFor({ timeout: 15000 });
      await page.waitForTimeout(1500); // Settings repaints once the project settings load
      await until(async () => (await page.locator('[data-marker-group]').count()) === 5, 'five default groups');
      assert.equal(await page.locator('[data-group-exclusive="star"]').isChecked(), true);
      assert.equal(await page.locator('[data-group-exclusive="symbol"]').isChecked(), false);
      await page.locator('[data-settings-markers]').scrollIntoViewIfNeeded().catch(() => {});
      await page.screenshot({ path: path.join(shots, `markers-settings-${lang}.png`) });
      // Make Symbols one-per-node and save: the project setting holds the groups.
      await page.locator('[data-group-exclusive="symbol"]').check();
      await page.locator('[data-savebar-save]').click();
      await until(async () => {
        const groups = (await api(base, 'GET', '/api/project/settings')).settings.markers?.groups || [];
        return groups.find((group) => group.id === 'symbol')?.exclusive === true;
      }, 'marker groups saved');
      const sym = await api(base, 'POST', '/api/units', { type: 'note', title: 'S', tags: ['marker:question', 'marker:check-green'] });
      assert.deepEqual(sym.unit.tags.filter((tag) => tag.startsWith('marker:')), ['marker:check-green']);
      assert.deepEqual(errors, []);
      await context.close();
    }
  } finally {
    await browser?.close();
    server.kill();
  }
});
