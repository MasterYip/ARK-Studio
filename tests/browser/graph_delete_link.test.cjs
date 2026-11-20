// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node --test tests/browser/graph_delete_link.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge> to pick a browser)
// Map force graph: clicking a link's midpoint selects it; Delete asks first
// (Cancel keeps it), then removes the link from the view and the project
// through DELETE /api/links/{id}; Ctrl+Z recreates it.  The right-click menu on
// a link offers "Delete link".
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

test('force graph: select a link, Delete removes it (API called), undo and the link menu', { skip: !chromium || !SERVER, timeout: 240000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-dellink-'));
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
    await api(base, 'POST', '/api/projects', { name: 'Delete link test' });
    const mk = async (title) => (await api(base, 'POST', '/api/units', { type: 'idea', title })).unit.uid;
    const a = await mk('Alpha'), b = await mk('Beta'), c = await mk('Gamma');
    await api(base, 'POST', '/api/links', { source: a, target: b, relation: 'supports' });
    await api(base, 'POST', '/api/links', { source: c, target: a, relation: 'cites' });
    const linksOut = async (uid) => (await api(base, 'GET', `/api/units/${uid}`)).unit.links_out.map((l) => l.relation);

    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
    await context.addInitScript(() => { localStorage.setItem('ark.prefsV2', '1'); localStorage.setItem('ark.lang', 'en'); });
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', (error) => errors.push(error.message));
    const deletes = [];
    page.on('request', (req) => { if (req.method() === 'DELETE' && /\/api\/links\/\d+$/.test(req.url())) deletes.push(req.url()); });
    await page.goto(`${base}/`);
    await page.locator('#overlay-root').waitFor({ state: 'attached' });

    // The Map's GraphView with the Map's options (mind.js), on top of the page.
    await page.evaluate(async () => {
      const { GraphView } = await import('/js/graph.js');
      const { mindGraphOptions } = await import('/js/mind.js');
      const host = document.createElement('div');
      host.className = 'graph-host';
      host.style.cssText = 'position:fixed;left:0;top:0;width:900px;height:700px;background:#fff';
      // The app shell steps aside; modals and menus still open on <body>.
      for (const child of document.body.children) if (child.id !== 'overlay-root') child.style.display = 'none';
      document.body.appendChild(host);
      const toasts = [];
      const ctx = { S: {}, graphs: [], toast: (msg, kind) => toasts.push([msg, kind]) };
      const snap = await (await fetch('/api/graph/snapshot')).json();
      const g = new GraphView(host, { ...mindGraphOptions(ctx), layout: 'force', mode: 'read', persistLayout: false, persistMode: false, cameraKey: null });
      ctx.graphs.push(g);
      g.setData(snap.nodes, snap.edges, { layout: 'force' });
      const t0 = performance.now();
      while (g.running && performance.now() - t0 < 20000) await new Promise((r) => setTimeout(r, 25));
      window.__g = g;
      window.__toasts = toasts;
    });
    const midpoint = (src, dst) => page.evaluate(([src, dst]) => {
      const g = window.__g;
      const a = g.nodes.find((n) => n.uid === src), b = g.nodes.find((n) => n.uid === dst);
      const pa = g._screen(a), pb = g._screen(b), rect = g.canvas.getBoundingClientRect();
      return { x: rect.left + (pa.x + pb.x) / 2, y: rect.top + (pa.y + pb.y) / 2 };
    }, [src, dst]);
    const edgeCount = () => page.evaluate(() => window.__g.edges.length);
    // The app's CSP has no 'unsafe-eval', so no page.waitForFunction.
    const waitEdges = async (n) => {
      for (let i = 0; i < 200 && (await edgeCount()) !== n; i += 1) await new Promise((r) => setTimeout(r, 50));
      assert.equal(await edgeCount(), n);
    };
    assert.equal(await edgeCount(), 2);

    // Click the link's midpoint: the link is selected.
    let at = await midpoint(a, b);
    await page.mouse.click(at.x, at.y);
    assert.equal(await page.evaluate(() => window.__g.selectedEdge?.relation), 'supports');
    // Delete asks first; Cancel keeps the link.
    await page.keyboard.press('Delete');
    const modal = page.locator('.modal');
    await modal.waitFor();
    assert.match(await modal.textContent(), /Delete the link/);
    await modal.getByRole('button', { name: 'Cancel' }).click();
    await modal.waitFor({ state: 'detached' });
    assert.equal(await edgeCount(), 2);
    assert.deepEqual(deletes, []);
    // Delete + confirm: gone from the view and the project.
    await page.mouse.click(at.x, at.y);
    await page.keyboard.press('Delete');
    await page.locator('.modal').getByRole('button', { name: 'Delete link' }).click();
    await waitEdges(1);
    assert.equal(deletes.length, 1);
    assert.deepEqual(await linksOut(a), []);
    assert.equal(await page.evaluate(() => window.__g.selectedEdge), null);
    // Ctrl+Z recreates it.
    await page.keyboard.press('Control+z');
    await waitEdges(2);
    assert.deepEqual(await linksOut(a), ['supports']);

    // Right-click on the other link: "Delete link" in the menu.
    at = await midpoint(c, a);
    await page.mouse.click(at.x, at.y, { button: 'right' });
    const item = page.locator('.context-menu, .ctx-menu, [role="menu"]').getByText('Delete link');
    await item.click();
    await page.locator('.modal').getByRole('button', { name: 'Delete link' }).click();
    await waitEdges(1);
    assert.deepEqual(await linksOut(c), []);
    assert.equal(deletes.length, 2);

    // The activity log has the deletions.
    const activity = (await api(base, 'GET', '/api/activity')).activity.filter((row) => row.action === 'link.delete');
    assert.equal(activity.length, 2);
    await page.screenshot({ path: path.join(process.env.ARK_TEST_SHOTS || dir, 'graph-delete-link.png') });
    assert.deepEqual(errors, []);
    await context.close();
  } finally {
    if (browser) await browser.close();
    server.kill();
    await new Promise((r) => (server.exitCode !== null ? r() : server.once('exit', r)));
    rmSync(dir, { recursive: true, force: true });
  }
});
