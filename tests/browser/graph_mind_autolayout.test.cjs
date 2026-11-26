// Optional browser regression (Playwright):
//   node --test tests/browser/graph_mind_autolayout.test.cjs
// Mind shows the toolbar's Auto layout button; pressing it releases dragged
// cards back into the tree layout (animated), and one undo restores them.
const assert = require('node:assert/strict');
const { createServer } = require('node:http');
const { readFile } = require('node:fs/promises');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch { /* Optional browser dependency. */ }

test('Mind auto layout button re-lays out dragged cards; undo restores them', { skip: !chromium, timeout: 120000 }, async () => {
  const web = path.resolve(__dirname, '../../ark_studio/web');
  const server = createServer(async (req, res) => {
    const pathname = new URL(req.url, 'http://localhost').pathname;
    if (pathname === '/') {
      res.end('<!doctype html><link rel="stylesheet" href="/css/app.css"><div id="g" class="graph-host" style="width:1000px;height:640px"></div>');
      return;
    }
    const file = path.resolve(web, '.' + pathname);
    if (!file.startsWith(web + path.sep)) { res.writeHead(403).end(); return; }
    try {
      res.setHeader('Content-Type', file.endsWith('.js') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : 'text/plain');
      res.end(await readFile(file));
    } catch { res.writeHead(404).end(); }
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  let browser;
  try {
    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    const page = await browser.newPage({ viewport: { width: 1100, height: 720 } });
    const errors = [];
    page.on('pageerror', (error) => errors.push(error.message));
    await page.goto(`http://127.0.0.1:${server.address().port}/`);
    await page.evaluate(async () => {
      localStorage.clear();
      const { GraphView } = await import('/js/graph.js');
      const graph = new GraphView(document.getElementById('g'), { persistLayout: false, persistMode: false, layout: 'mind' });
      window.graph = graph;
      const nodes = ['Root', 'Alpha', 'Beta', 'Gamma', 'Loose idea', 'Delta'].map((title, i) => ({ id: i + 1, uid: `T-${i + 1}`, type: 'idea', status: 'open', title }));
      const edges = [[2, 1], [3, 1], [4, 2], [6, 2]].map(([source, target], i) => ({ id: `e${i}`, source, target, relation: 'part_of' }));
      graph.setData(nodes, edges, { layout: 'mind' });
    });
    const button = page.locator('#g .graph-node-auto');
    assert.equal(await button.isVisible(), true, 'Auto layout button shown in Mind');
    assert.equal(await button.getAttribute('title'), await page.evaluate(async () => (await import('/js/i18n.js')).t('graph.auto_layout_mind')));
    const slots = await page.evaluate(() => Object.fromEntries(graph.nodes.map((n) => [n.id, [n.x, n.y]])));
    // Drag-like edits: two cards moved by hand (as a pointer drag leaves them).
    const dragged = await page.evaluate(() => {
      for (const [id, x, y] of [[3, 700, -300], [5, -600, 500]]) {
        const node = graph.byId.get(id); node.x = x; node.y = y; node.mindManual = true;
      }
      graph._applyLayout(false); // a later layout (e.g. folding) keeps them
      return Object.fromEntries(graph.nodes.map((n) => [n.id, [n.x, n.y]]));
    });
    assert.deepEqual(dragged[3], [700, -300]);
    await button.click();
    await page.waitForFunction(() => !graph._nodeAnimation);
    const after = await page.evaluate(() => Object.fromEntries(graph.nodes.map((n) => [n.id, [n.x, n.y, !!n.mindManual]])));
    for (const [id, [x, y]] of Object.entries(slots)) assert.deepEqual(after[id], [x, y, false], `node ${id} back in its slot`);
    await page.evaluate(() => graph._undoLast());
    await page.waitForFunction(() => !graph._nodeAnimation);
    const undone = await page.evaluate(() => Object.fromEntries(graph.nodes.map((n) => [n.id, [n.x, n.y]])));
    assert.deepEqual(undone, dragged, 'one undo restores the dragged positions');
    // Topic page and Settings preview entry points.
    await page.evaluate(() => graph.autoLayout());
    await page.waitForFunction(() => !graph._nodeAnimation);
    assert.deepEqual(await page.evaluate(() => [graph.byId.get(3).x, graph.byId.get(3).y]), slots[3]);
    // Folding reflows Alpha's tree around Alpha (animated): Beta closes up
    // right under the folded Alpha, the orphan stays; unfolding reopens it;
    // one undo per fold.
    const fold = await page.evaluate(async () => {
      const at = () => Object.fromEntries(graph.nodes.map((n) => [n.id, [n.x, n.y]]));
      const settle = () => new Promise((r) => { const wait = () => (graph._nodeAnimation ? requestAnimationFrame(wait) : r()); wait(); });
      const before = at();
      const alpha = graph.byId.get(2), beta = graph.byId.get(3), gamma = graph.byId.get(4), orphan = graph.byId.get(5);
      graph._toggleNodeCollapse(alpha);
      const animating = !!graph._nodeAnimation;
      await settle();
      const folded = at();
      const gap = (beta.y - beta.h / 2) - (alpha.y + alpha.h / 2);
      graph._toggleNodeCollapse(alpha);
      await settle();
      const opened = at();
      const overlap = Math.abs(gamma.y - beta.y) < (gamma.h + beta.h) / 2 && Math.abs(gamma.x - beta.x) < (gamma.w + beta.w) / 2;
      const clear = orphan.x - orphan.w / 2 > gamma.x + gamma.w / 2;
      await graph._undoLast(); await settle();
      await graph._undoLast(); await settle();
      return { before, folded, opened, gap, overlap, clear, animating, undone: at() };
    });
    assert.equal(fold.animating, true, 'the reflow is animated');
    assert.deepEqual(fold.folded[2], fold.before[2], 'the clicked card stays put');
    assert.deepEqual(fold.folded[5], fold.before[5], 'the orphan stays put');
    assert.ok(Math.abs(fold.gap - 10) < 1e-6, `compact: Beta right under the folded Alpha (gap ${fold.gap})`);
    assert.deepEqual(fold.opened[2], fold.before[2]);
    assert.deepEqual(fold.opened[5], fold.before[5]);
    assert.equal(fold.overlap, false, 'Gamma gets room again');
    assert.equal(fold.clear, true, 'the orphan area starts right of Gamma');
    assert.deepEqual(fold.undone, fold.before, 'undo restores');
    await page.screenshot({ path: process.env.ARK_QA_SHOT || path.join(require('node:os').tmpdir(), 'mind-autolayout.png') });
    await page.evaluate(() => { graph.setLayout('force'); });
    assert.equal(await button.isVisible(), false, 'hidden for Force');
    assert.deepEqual(errors, []);
  } finally {
    if (browser) await browser.close();
    await new Promise((resolve) => server.close(resolve));
  }
});
