// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node tests/browser/graph_filter_view.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge>; ARK_TEST_SHOTS=<dir> keeps the screenshots;
//    ARK_TEST_WEB=<web root> runs against another checkout's web files)
// 1. Filter changes on the Map and on a topic page, in every layout (and 3D
//    Force): the camera stays while the view still shows nodes, glides to
//    the nodes when the view would show none, and an empty result keeps the
//    empty state.
// 2. A fullscreen graph shows node details as a popover inside it: a click
//    (read or edit mode) opens it, arrow keys move it to the next node, Esc
//    or an empty-canvas click closes it, leaving fullscreen closes it and
//    the docked inspector shows the node.
const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const { mkdtempSync, rmSync, mkdirSync } = require('node:fs');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch { /* Optional browser dependency. */ }

const SERVER = process.env.ARK_TEST_SERVER ? path.resolve(process.env.ARK_TEST_SERVER) : '';
const WEB = path.resolve(process.env.ARK_TEST_WEB || path.resolve(__dirname, '../../ark_studio/web'));
// Only the filter part runs against an old checkout (ARK_TEST_ONLY=filters).
const ONLY = process.env.ARK_TEST_ONLY || '';

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

// In the page: the live graph, how many node centres are on screen, settled.
const PAGE_HELPERS = () => {
  window.__gv = {
    graph: () => window.__ARK__.graphs.find((g) => g.host?.isConnected && !g.opts?.backdrop && g.nodes),
    onScreen(g) {
      const inset = Math.min(g.width, g.height) * 0.06;
      const inside = (x, y, w, h) => x >= inset && x <= w - inset && y >= inset && y <= h - inset;
      if (g._view3d) {
        const v = g._view3d, p = v.proj;
        let n = 0;
        for (let i = 0; i < (v.sim?.n || 0); i++) if (p.w[i] > 0 && inside(p.x[i], p.y[i], v.width, v.height)) n++;
        return n;
      }
      const shown = g.layout === 'mind' ? g._visibleMindNodes() : g.nodes;
      return shown.filter((node) => { const s = g._screen(node); return inside(s.x, s.y, g.width, g.height); }).length;
    },
    camera(g) {
      if (g._view3d) { const v = g._view3d.view; return { r: Math.round(v.radius), x: Math.round(v.target.x), y: Math.round(v.target.y), z: Math.round(v.target.z) }; }
      return { zoom: +g.zoom.toFixed(4), x: Math.round(g.pan.x), y: Math.round(g.pan.y) };
    },
    settled(g) {
      if (g._view3d) return !g._view3d.running && !g._view3d.flight && !g._view3d._viewCheck;
      return !g.running && !g._cameraFrame && !g._transitionFrame && !g._nodeAnimation?.frame;
    },
  };
};

async function startServer() {
  const dir = mkdtempSync(path.join(process.env.ARK_TEST_TMP || os.tmpdir(), 'ark-filter-view-'));
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const server = spawn(SERVER, ['serve', '--root', path.join(dir, 'projects'), '--host', '127.0.0.1', '--port', String(port),
    '--no-demo', '--sessions-root', path.join(dir, 'sessions'), '--web-root', WEB], { stdio: ['ignore', 'pipe', 'pipe'] });
  let log = '';
  server.stdout.on('data', (chunk) => { log += chunk; });
  server.stderr.on('data', (chunk) => { log += chunk; });
  await until(() => api(base, 'GET', '/api/health').then(() => true, () => false), 30000)
    .catch(() => { throw new Error(`server did not start\n${log}`); });
  await api(base, 'POST', '/api/projects', { name: 'Filter view test' });
  // Two clusters on two research lines, dated over a year, chained by part_of.
  const TYPES = ['idea', 'claim', 'task', 'evidence'];
  const uids = [];
  const units = [];
  for (let i = 0; i < 40; i++) {
    const type = TYPES[i % 4];
    const line = i < 20 ? 'branch:alpha' : 'branch:beta';
    const month = String(1 + (i % 12)).padStart(2, '0');
    const links = i % 20 ? [{ target: uids[i - 1], relation: 'part_of' }] : [];
    const unit = (await api(base, 'POST', '/api/units', {
      type, title: `${type} ${i}`, status: i % 3 ? 'open' : 'done', tags: [line], start_at: `2026-${month}-${String(1 + (i % 27)).padStart(2, '0')}`, links,
    })).unit;
    uids.push(unit.uid);
    units.push(unit);
  }
  const topic = (await api(base, 'POST', '/api/units', { type: 'topic', title: 'Filter topic', members: uids })).unit.uid;
  return { dir, base, server, topic, units };
}

async function openPage(browser, base, hash) {
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  await context.addInitScript(() => {
    localStorage.setItem('ark.prefsV2', '1');
    localStorage.setItem('ark.lang', 'zh');
    localStorage.setItem('ark.welcome.v1:local', '1');
  });
  const page = await context.newPage();
  const errors = [];
  page.on('pageerror', (error) => errors.push(error.message));
  await page.goto(`${base}/?lang=zh${hash}`);
  await page.locator('.guidance-invite .btn.ghost').click({ timeout: 4000 }).catch(() => {});
  await page.evaluate(PAGE_HELPERS);
  await until(() => page.evaluate(() => (window.__gv.graph()?.nodes.length || 0) >= 30), 30000);
  return { page, context, errors };
}

const settle = (page, ms = 20000) => until(() => page.evaluate(() => window.__gv.settled(window.__gv.graph())), ms)
  .then(() => page.waitForTimeout(400))
  .then(() => until(() => page.evaluate(() => window.__gv.settled(window.__gv.graph())), ms))
  .catch(async (error) => {
    const why = await page.evaluate(() => { const g = window.__gv.graph(); return { layout: g.layout, running: g.running, camera: Boolean(g._cameraFrame),
      transition: Boolean(g._transitionFrame), nodes: Boolean(g._nodeAnimation?.frame), v3d: g._view3d && { running: g._view3d.running, flight: Boolean(g._view3d.flight), check: Boolean(g._view3d._viewCheck) } }; });
    throw new Error(`not settled: ${JSON.stringify(why)} (${error.message})`);
  });

// Zoom in close on the first node of `type` (2D), or fly to it (3D).
async function zoomOnType(page, type) {
  await page.evaluate((type) => {
    const g = window.__gv.graph();
    const node = g.nodes.find((n) => n.type === type);
    if (g._view3d) {
      // Close up on the node: only it (and maybe a neighbour) is in view.
      const v = g._view3d, i = v.sim.index.get(node.id);
      v.flight = null;
      v.view.target.set(v.sim.x[i], v.sim.y[i], v.sim.z[i]);
      v.view.radius = 25;
      v._kick();
      return;
    }
    g._cancelCameraAnimation();
    g.zoom = 2.4;
    g.pan = { x: -node.x * g.zoom, y: -node.y * g.zoom };
    g._userInteracted = true;
    g._draw();
  }, type);
  await settle(page);
}

const state = (page) => page.evaluate(() => {
  const g = window.__gv.graph();
  return { n: g.nodes.length, onScreen: window.__gv.onScreen(g), camera: window.__gv.camera(g),
    empty: Boolean(g.emptyEl && !g.emptyEl.classList.contains('hidden')), checks: g.viewChecks || [], counts: { ...(g.counts || {}) } };
});

async function filterScenario(page, label, shots, results) {
  const ctx = 'window.__ARK__.ctx';
  const setTypes = (types) => page.evaluate(`${ctx}.setFilter('types', ${JSON.stringify(types)})`);
  // a) What the user looks at stays: the camera does not move.
  await zoomOnType(page, 'task');
  const before = await state(page);
  await setTypes(['task', 'claim']);
  await settle(page);
  const kept = await state(page);
  // b) Everything in view is filtered out: the view goes to the rest.
  await setTypes([]);
  await settle(page);
  await zoomOnType(page, 'evidence');
  await setTypes(['idea']);
  await settle(page);
  const flown = await state(page);
  await page.screenshot({ path: path.join(shots, `${label}-filtered.png`) });
  // c) Nothing matches: the empty state, no blank pan.
  await page.evaluate(`${ctx}.toggleAllTypes(false)`);
  await settle(page).catch(() => {});
  const empty = await state(page);
  // d) Back to everything.
  await page.evaluate(`${ctx}.toggleAllTypes(true)`);
  await settle(page);
  const back = await state(page);
  results.push({ label, keptChecks: kept.checks.slice(-3), keptCounts: kept.counts, before: before.camera, kept: kept.camera, keptOnScreen: kept.onScreen, flownOnScreen: flown.onScreen,
    flownN: flown.n, empty: empty.empty, emptyN: empty.n, backOnScreen: back.onScreen });
  return { before, kept, flown, empty, back };
}

const LAYOUTS = process.env.ARK_TEST_LAYOUTS ? process.env.ARK_TEST_LAYOUTS.split(',') : ['force', 'mind', 'node', 'timeline', '3d'];
// ARK_TEST_RECORD=1: report the numbers without failing (the "before" run).
const RECORD = process.env.ARK_TEST_RECORD === '1';

test('filter changes keep the graph in view on the Map and topic pages', { skip: !chromium || !SERVER, timeout: 600000 }, async () => {
  const env = await startServer();
  const shots = process.env.ARK_TEST_SHOTS || env.dir;
  mkdirSync(shots, { recursive: true });
  let browser;
  const results = [];
  try {
    browser = await chromium.launch({ headless: true, args: ['--use-angle=swiftshader', '--enable-unsafe-swiftshader'],
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    for (const [where, hash] of [['map', '#/map'], ['topic', `#/research/topic/${env.topic}`]]) {
      const { page, context, errors } = await openPage(browser, env.base, hash);
      for (const layout of LAYOUTS) {
        await page.evaluate(async (layout) => {
          const g = window.__gv.graph();
          if (layout === '3d') { if (g.layout !== 'force') g.setLayout('force'); await g.set3D(true); return; }
          if (g._view3d) await g.set3D(false);
          g.setLayout(layout);
        }, layout);
        await settle(page);
        const label = `${where}-${layout}`;
        const run = await filterScenario(page, label, shots, results);
        if (RECORD) continue;
        assert.ok(run.kept.onScreen > 0, `${label}: nodes stay on screen when the looked-at node stays`);
        assert.deepEqual(run.kept.camera, run.before.camera, `${label}: the camera does not jump while it shows nodes`);
        assert.ok(run.flown.n > 0 && run.flown.onScreen > 0, `${label}: after filtering away the view, the rest is on screen`);
        assert.equal(run.empty.n, 0, `${label}: nothing matches`);
        assert.ok(run.empty.empty, `${label}: the empty state shows`);
        assert.ok(run.back.onScreen > 0, `${label}: everything back on screen`);
      }
      if (await page.evaluate(() => Boolean(window.__gv.graph()._view3d))) await page.evaluate(() => window.__gv.graph().set3D(false));
      if (!RECORD) assert.deepEqual(errors, []);
      await context.close();
    }
  } finally {
    console.log(JSON.stringify(results, null, 1));
    await browser?.close();
    env.server.kill();
    if (!process.env.ARK_TEST_SHOTS) rmSync(env.dir, { recursive: true, force: true });
  }
});

// Screen point (page coordinates) of the node nearest the canvas centre.
const nodePoint = (page, skip = []) => page.evaluate((skip) => {
  const g = window.__gv.graph();
  const rect = (g._view3d?.canvas || g.canvas).getBoundingClientRect();
  let best = null;
  const consider = (node, x, y) => {
    if (skip.includes(node.uid)) return;
    const d = Math.hypot(x - rect.width / 2, y - rect.height / 2);
    if (x > 40 && y > 60 && x < rect.width - 40 && y < rect.height - 40 && (!best || d < best.d)) best = { d, x: rect.left + x, y: rect.top + y, uid: node.uid, title: node.title };
  };
  if (g._view3d) {
    const v = g._view3d, p = v.proj;
    for (let i = 0; i < v.sim.n; i++) if (p.w[i] > 0 && p.r[i] > 3) consider(v.nodes[i], p.x[i], p.y[i]);
  } else {
    for (const node of (g.layout === 'mind' ? g._visibleMindNodes() : g.nodes)) { const s = g._screen(node); consider(node, s.x, s.y); }
  }
  return best;
}, skip);

// An empty spot of the canvas (no node within 140 px).
const emptyPoint = (page) => page.evaluate(() => {
  const g = window.__gv.graph();
  const rect = (g._view3d?.canvas || g.canvas).getBoundingClientRect();
  const pts = [];
  if (g._view3d) { const v = g._view3d; for (let i = 0; i < v.sim.n; i++) if (v.proj.w[i] > 0) pts.push([v.proj.x[i], v.proj.y[i]]); }
  else for (const node of g.nodes) { const s = g._screen(node); pts.push([s.x, s.y]); }
  for (let y = rect.height - 80; y > 80; y -= 37) for (let x = 80; x < rect.width - 80; x += 41) {
    if (pts.every(([px, py]) => Math.hypot(px - x, py - y) > 140)) return { x: rect.left + x, y: rect.top + y };
  }
  return null;
});

const POPOVER = '.graph-popover-layer .drawer.graph-popover';
const popoverGone = (page) => until(() => page.locator(POPOVER).count().then((n) => n === 0), 5000);

async function popoverChecks(page, label, shots) {
  // Read mode: a click opens the popover with the node's details.
  const first = await nodePoint(page);
  assert.ok(first, `${label}: a node to click`);
  await page.mouse.click(first.x, first.y);
  await page.locator(POPOVER).waitFor({ timeout: 10000 });
  const title = await page.locator(`${POPOVER} .read-title`).first().textContent();
  assert.equal(title.trim(), first.title, `${label}: the popover shows the clicked node`);
  assert.ok(await page.locator(`${POPOVER} .drawer-close`).first().isVisible(), `${label}: closeable`);
  const box = await page.locator(POPOVER).boundingBox();
  assert.ok(box.x >= 0 && box.y >= 0 && box.x + box.width <= 1440 && box.y + box.height <= 900, `${label}: within the screen`);
  await page.screenshot({ path: path.join(shots, `${label}-fullscreen-popover.png`) });
  // Dragging the head moves it.
  const head = page.locator(`${POPOVER} .drawer-identity`);
  const hb = await head.boundingBox();
  await page.mouse.move(hb.x + hb.width + 6, hb.y + hb.height / 2);
  await page.mouse.down();
  await page.mouse.move(hb.x + hb.width - 114, hb.y + hb.height / 2 + 40, { steps: 6 });
  await page.mouse.up();
  const moved = await page.locator(POPOVER).boundingBox();
  assert.ok(Math.abs(moved.y - (box.y + 40)) < 4 || Math.abs(moved.x - box.x) > 20, `${label}: dragged (${box.x},${box.y} -> ${moved.x},${moved.y})`);
  // Esc closes it and keeps fullscreen.
  await page.keyboard.press('Escape');
  await popoverGone(page);
  assert.ok(await page.evaluate(() => Boolean(document.fullscreenElement)), `${label}: still fullscreen after Esc`);
  // Edit mode: a click opens it too; an empty-canvas click closes it.
  await page.evaluate(() => window.__ARK__.ctx.syncInspectorMode('edit'));
  const second = await nodePoint(page);
  await page.mouse.click(second.x, second.y);
  await page.locator(POPOVER).waitFor({ timeout: 10000 });
  const spot = await emptyPoint(page);
  assert.ok(spot, `${label}: an empty spot`);
  await page.mouse.click(spot.x, spot.y);
  await popoverGone(page);
  await page.evaluate(() => window.__ARK__.ctx.syncInspectorMode('read'));
  await page.waitForTimeout(300);
  // Keyboard (2D): an arrow key moves the selection and the popover follows.
  const third = await nodePoint(page);
  await page.mouse.click(third.x, third.y);
  await page.locator(POPOVER).waitFor({ timeout: 10000 });
  if (!label.endsWith('3d')) {
    await page.evaluate(() => window.__gv.graph().host.focus());
    for (const key of ['ArrowRight', 'ArrowDown', 'ArrowLeft', 'ArrowUp']) {
      await page.keyboard.press(key);
      await page.waitForTimeout(400);
      if (await page.evaluate((uid) => { const g = window.__gv.graph(); return g.byId.get(g.selected)?.uid !== uid; }, third.uid)) break;
    }
    const selected = await page.evaluate(() => { const g = window.__gv.graph(); return g.byId.get(g.selected)?.uid; });
    assert.notEqual(selected, third.uid, `${label}: an arrow key moved the selection`);
    await until(() => page.evaluate((uid) => window.__ARK__.S.detail?.uid === uid
      && Boolean(document.querySelector('.graph-popover-layer .drawer.graph-popover')), selected), 8000);
  }
  return page.evaluate(() => window.__ARK__.S.detail?.uid);
}

test('a fullscreen graph shows node details as a popover', { skip: !chromium || !SERVER || ONLY === 'filters', timeout: 600000 }, async () => {
  const env = await startServer();
  const shots = process.env.ARK_TEST_SHOTS || env.dir;
  mkdirSync(shots, { recursive: true });
  let browser;
  try {
    browser = await chromium.launch({ headless: true, args: ['--headless=new', '--use-angle=swiftshader', '--enable-unsafe-swiftshader'], // element fullscreen needs the new headless mode
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    for (const [where, hash] of [['map', '#/map'], ['topic', `#/research/topic/${env.topic}`]]) {
      const { page, context, errors } = await openPage(browser, env.base, hash);
      for (const layout of ['force', 'timeline', 'mind', 'node', '3d']) {
        const label = `${where}-${layout}`;
        await page.evaluate(async (layout) => {
          const g = window.__gv.graph();
          if (layout === '3d') { if (g.layout !== 'force') g.setLayout('force'); await g.set3D(true); return; }
          if (g._view3d) await g.set3D(false);
          g.setLayout(layout);
        }, layout);
        await settle(page, 3000).catch(() => page.waitForTimeout(300));
        // The graph's own fullscreen button (a user gesture).
        await page.evaluate(() => window.__gv.graph().fullscreenBtn.scrollIntoView());
        await page.evaluate(() => window.__gv.graph().fullscreenBtn.getBoundingClientRect().toJSON())
          .then((r) => page.mouse.click(r.x + r.width / 2, r.y + r.height / 2));
        await until(() => page.evaluate(() => Boolean(document.fullscreenElement?.querySelector('.graph-popover-layer'))), 10000);
        await page.waitForTimeout(400);
        await page.evaluate(() => window.__gv.graph().fit());
        await settle(page, 3000).catch(() => page.waitForTimeout(300));
        const shown = await popoverChecks(page, label, shots);
        // Leaving fullscreen closes the popover; the docked inspector shows the node.
        await page.evaluate(() => document.exitFullscreen());
        await until(() => page.evaluate(() => !document.fullscreenElement), 5000);
        await popoverGone(page).catch(() => assert.fail(`${label}: no popover outside fullscreen`));
        await until(() => page.evaluate((uid) => window.__ARK__.S.detail?.uid === uid
          && Boolean(document.querySelector('.drawer.docked:not(.inspector-empty)')), shown), 8000);
        if (layout === 'force') await page.screenshot({ path: path.join(shots, `${label}-after-fullscreen.png`) });
        await page.evaluate(() => window.__ARK__.ctx.closeUnit());
        if (layout === '3d') await page.evaluate(() => window.__gv.graph().set3D(false));
      }
      assert.deepEqual(errors, []);
      await context.close();
    }
  } finally {
    await browser?.close();
    env.server.kill();
    if (!process.env.ARK_TEST_SHOTS) rmSync(env.dir, { recursive: true, force: true });
  }
});
