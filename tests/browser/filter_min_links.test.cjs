// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node tests/browser/filter_min_links.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge>; ARK_TEST_SHOTS=<dir> keeps the screenshots)
// Filters rail: (1) toggling a tag chip, removing an active chip and opening
// a collapsed group keep the rail's scroll offset (in the workbench the view
// around the rail scrolls, not the rail); a group opened by hand stays open.
// (2) Min. links: the slider (0 .. the largest link count) drops nodes from
// the Map graph and the Map list, shows "Links >= N x" and is remembered.
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
    await new Promise((r) => setTimeout(r, 150));
  }
};

// Uids drawn in the Map graph.
const mapUids = (page) => page.evaluate(async () => {
  const { liveGraphs } = await import(performance.getEntriesByType('resource').map((e) => e.name).find((n) => /\/js\/graph\.js(\?|$)/.test(n)) || '/js/graph.js'); // the app's instance (?v=)
  const graph = liveGraphs().find((g) => g.opts?.mapGraph && g.host?.isConnected);
  return graph ? graph.nodes.map((n) => n.uid).sort() : null;
});

// The element that scrolls the rail: the rail itself or an ancestor.
const scroller = (page) => page.evaluate(() => {
  let el = document.querySelector('.rail');
  while (el && el !== document.body) {
    const o = getComputedStyle(el).overflowY;
    if (o === 'auto' || o === 'scroll') break;
    el = el.parentElement;
  }
  window.__railScroller = el;
  return { cls: el?.className || '', top: el?.scrollTop || 0, max: el ? el.scrollHeight - el.clientHeight : 0 };
});
const scrollTop = (page) => page.evaluate(() => window.__railScroller.scrollTop);
const setScroll = (page, top) => page.evaluate((top) => { window.__railScroller.scrollTop = top; return window.__railScroller.scrollTop; }, top);

test('filters rail keeps its scroll offset; Min. links filters graph and list', { skip: !chromium || !SERVER, timeout: 300000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-minlinks-'));
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
    await api(base, 'POST', '/api/projects', { name: 'Min links test' });
    const mk = async (data) => (await api(base, 'POST', '/api/units', data)).unit.uid;
    // Many tag namespaces make the rail long enough to scroll; "bulk" has
    // 12 values, so it starts collapsed.
    const spaces = ['alpha', 'bravo', 'charlie', 'delta', 'echo', 'foxtrot', 'golf', 'hotel', 'india', 'juliet', 'kilo', 'lima'];
    const tagsFor = (i) => [...spaces.map((ns, k) => `${ns}:v${(i + k) % 3}`), `bulk:b${i % 12}`];
    // A hub with 6 links, two nodes with 3, the rest with 1 or 0.
    const hub = await mk({ type: 'claim', title: 'Hub', tags: tagsFor(0) });
    const mid = [];
    for (let i = 0; i < 2; i += 1) mid.push(await mk({ type: 'claim', title: `Mid ${i}`, tags: tagsFor(i + 1), links: [{ target: hub, relation: 'supports' }] }));
    const leaves = [];
    for (let i = 0; i < 4; i += 1) leaves.push(await mk({ type: 'evidence', title: `Leaf ${i}`, tags: tagsFor(i + 3), links: [{ target: mid[i % 2], relation: 'supports' }] }));
    for (let i = 0; i < 4; i += 1) await api(base, 'POST', '/api/links', { source: hub, target: leaves[i], relation: 'relates_to' });
    const lonely = [];
    for (let i = 0; i < 9; i += 1) lonely.push(await mk({ type: 'note', title: `Lonely ${i}`, tags: tagsFor(i + 7) }));
    const all = [hub, ...mid, ...leaves, ...lonely].sort();
    // Links: hub 2 + 4 = 6; mid 1 + 2 = 3; leaves 2; lonely 0.

    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    for (const lang of ['zh', 'en']) {
      const zh = lang === 'zh';
      const context = await browser.newContext({ viewport: { width: 1440, height: 760 } });
      await context.addInitScript((lang) => {
        localStorage.setItem('ark.prefsV2', '1');
        localStorage.setItem('ark.lang', lang);
        localStorage.setItem('ark.welcome.v1:local', '1');
      }, lang);
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', (error) => errors.push(error.message));
      await page.goto(`${base}/?lang=${lang}#/map`);
      await page.locator('.guidance-invite .btn.ghost').click({ timeout: 4000 }).catch(() => {});
      // The project's starter note (no links) is drawn too.
      await until(async () => (await mapUids(page))?.length >= all.length, 30000)
        .catch(async () => { throw new Error(`map: ${JSON.stringify(await mapUids(page))}`); });
      const total = (await mapUids(page)).length;
      const slider = page.locator('.rail .min-links-input');
      await slider.waitFor({ state: 'attached', timeout: 20000 });
      if (!(await slider.isVisible())) await page.locator('[data-view-icon="filters"]').click();
      await slider.waitFor({ timeout: 10000 });

      // --- 1. Scroll position across tag changes.
      const sc = await scroller(page);
      assert.ok(sc.max > 300, `the rail scrolls (${JSON.stringify(sc)})`);
      const chip = page.locator('.rail [data-filter-group="tag:juliet"][data-value="juliet:v1"]');
      await chip.scrollIntoViewIfNeeded();
      await page.mouse.wheel(0, 40);
      await page.waitForTimeout(200);
      const before = await scrollTop(page);
      assert.ok(before > 200, `scrolled down (${before})`);
      await chip.click();
      await until(async () => (await page.locator('.rail [data-filter-group="tag:juliet"][data-value="juliet:v1"].on').count()) === 1);
      await page.waitForTimeout(700); // facet counts arrive and repaint the rail
      const afterToggle = await scrollTop(page);
      assert.ok(Math.abs(afterToggle - before) <= 4, `toggle keeps scroll: ${before} -> ${afterToggle}`);
      // Keyboard position: the replaced chip has focus again.
      assert.equal(await page.evaluate(() => document.activeElement?.dataset?.value || ''), 'juliet:v1', 'focus stays on the chip');
      // A second chip in the same spot, toggled off again.
      await page.locator('.rail [data-filter-group="tag:juliet"][data-value="juliet:v1"]').click();
      await until(async () => (await page.locator('.rail .tag-filter.on').count()) === 0);
      await page.waitForTimeout(700);
      assert.ok(Math.abs((await scrollTop(page)) - before) <= 4, 'toggle off keeps scroll');

      // Opening the collapsed "bulk" group by hand keeps it open after a change.
      const bulk = page.locator('.rail details.tag-filter-wrap', { has: page.locator('[data-focus-key="ns:bulk"]') });
      assert.equal(await bulk.evaluate((d) => d.open), false, 'bulk starts collapsed');
      await bulk.locator('summary').scrollIntoViewIfNeeded();
      await bulk.locator('summary').click();
      const bulkTop = await scrollTop(page);
      await page.locator('.rail [data-filter-group="tag:kilo"][data-value="kilo:v2"]').click();
      await page.waitForTimeout(700);
      assert.equal(await bulk.evaluate((d) => d.open), true, 'bulk stays open');
      assert.ok(Math.abs((await scrollTop(page)) - bulkTop) <= 4, 'group + chip keep scroll');
      // Removing the active chip at the top of the tag section.
      const active = page.locator('.rail .chips .chip.on').first();
      await active.scrollIntoViewIfNeeded();
      const chipTop = await scrollTop(page);
      assert.ok(chipTop > 100, `still scrolled (${chipTop})`);
      await active.click();
      await until(async () => (await page.locator('.rail .tag-filter.on').count()) === 0);
      await page.waitForTimeout(700);
      const afterRemove = await scrollTop(page);
      assert.ok(Math.abs(afterRemove - chipTop) <= 4, `removing the active chip keeps scroll: ${chipTop} -> ${afterRemove}`);

      // --- 2. Min. links.
      await setScroll(page, 0);
      assert.equal(await slider.getAttribute('max'), '6', 'slider end = largest link count');
      assert.equal(await slider.inputValue(), '0');
      const section = page.locator('.rail .min-links');
      assert.match(await section.textContent(), zh ? /最少链接.*最大 6/ : /Min\. links.*max 6/);
      const label = await page.evaluate(() => {
        const labels = [...document.querySelectorAll('.rail .rail-label')].map((el) => el.textContent.trim());
        return labels;
      });
      const timeAt = label.findIndex((text) => text.startsWith(zh ? '时间' : 'Time'));
      assert.ok(timeAt >= 0 && /^(最少链接|Min\. links)/.test(label[timeAt + 1]), `Min. links follows Time: ${label.join(' | ')}`);
      const drag = async (value) => slider.evaluate((el, value) => {
        el.value = String(value);
        el.dispatchEvent(new Event('input', { bubbles: true }));
        el.dispatchEvent(new Event('change', { bubbles: true }));
      }, value);
      await drag(2);
      await until(async () => (await mapUids(page))?.length === 7)
        .catch(async () => { throw new Error(`>=2: ${JSON.stringify(await mapUids(page))}`); });
      await drag(3);
      await until(async () => JSON.stringify(await mapUids(page)) === JSON.stringify([hub, ...mid].sort()))
        .catch(async () => { throw new Error(`>=3: ${JSON.stringify(await mapUids(page))}`); });
      // Counts follow the slider (computed in the browser, like the filter).
      const typeCount = (type) => page.locator(`.rail [data-filter-group="types"][data-value="${type}"] .n`).textContent();
      // (The Map leaves out types with no nodes: Evidence and Note go.)
      await until(async () => (await typeCount('claim')) === '3'
        && (await page.locator('.rail [data-filter-group="types"][data-value="evidence"]').count()) === 0);
      const linksChip = page.locator('.rail .min-links-chip');
      assert.match(await linksChip.textContent(), zh ? /链接 ≥ 3/ : /Links ≥ 3/);
      assert.match(await page.locator('.rail .min-links-value').textContent(), /≥ 3/);
      // AND with a tag: the hub has alpha:v0, the mids alpha:v1 / alpha:v2.
      await page.locator('.rail [data-filter-group="tag:alpha"][data-value="alpha:v0"]').click();
      await until(async () => JSON.stringify(await mapUids(page)) === JSON.stringify([hub]));
      await page.locator('.rail [data-filter-group="tag:alpha"][data-value="alpha:v0"]').click();
      await until(async () => (await mapUids(page))?.length === 3);
      await page.waitForTimeout(500);
      await page.locator('.rail .min-links').scrollIntoViewIfNeeded();
      const box = await page.evaluate(() => {
        const r = window.__railScroller.getBoundingClientRect();
        return { x: r.left, y: r.top, width: r.width, height: r.height };
      });
      await page.screenshot({ path: path.join(shots, `filter-min-links-${lang}.png`), clip: box });
      await page.screenshot({ path: path.join(shots, `map-min-links-${lang}.png`) });

      // Remembered for the Map tab: reload keeps 3.
      await page.reload();
      await until(async () => (await mapUids(page))?.length === 3, 30000)
        .catch(async () => { throw new Error(`after reload: ${JSON.stringify(await mapUids(page))}`); });
      assert.equal(await page.locator('.rail .min-links-input').inputValue(), '3');
      await until(async () => /^3\s/.test(await page.evaluate(() => document.querySelector('.toolbar .count')?.textContent || '')))
        .catch(async () => { throw new Error('after reload the toolbar counts 3 nodes: ' + await page.evaluate(() => [...document.querySelectorAll('.toolbar .count')].map((e) => e.textContent).join('/'))); });

      // The Map list uses the same filter.
      const listButton = page.locator(`.toolbar .view-toggle button[title="${zh ? '列表' : 'List'}"]`);
      await until(async () => {
        if (await page.locator('.map-list-items').count()) return true;
        await listButton.click().catch(() => {});
        await page.waitForTimeout(500);
        return false;
      });
      await page.locator('.map-list-items').waitFor({ timeout: 20000 }).catch(async (e) => { await page.screenshot({ path: path.join(shots, 'list-fail.png') }); throw e; });
      const listCount = await page.evaluate(() => document.querySelector('.toolbar .count')?.textContent || '');
      assert.match(listCount, /^3\b/, `list shows 3 units: ${listCount}`);

      // The chip's x clears it.
      await page.locator('.rail .min-links-chip').click();
      await until(async () => (await page.evaluate(() => document.querySelector('.toolbar .count')?.textContent || '')).startsWith(String(total)));
      assert.equal(await page.locator('.rail .min-links-chip').count(), 0);
      assert.deepEqual(errors, [], 'no page errors');
      await context.close();
    }
  } finally {
    await browser?.close();
    server.kill();
    if (!process.env.ARK_TEST_SHOTS) { try { rmSync(dir, { recursive: true, force: true }); } catch { /* busy */ } }
  }
});
