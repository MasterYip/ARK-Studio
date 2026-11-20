// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node --test tests/browser/multiselect_batch.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge> to pick a browser; ARK_TEST_SHOTS=<dir> keeps screenshots)
// Multi-selection on the Map's mind map: three siblings selected with
// click / Ctrl+click show their shared attributes in the inspector ("Mixed"
// where they differ); a status change there updates all three in one batch
// and Undo restores each node's own status.  Ctrl+Enter makes one new parent
// for the three; Ctrl+Z restores the tree in one step.  English and Chinese.
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

const parentOf = async (base, uid) => {
  const unit = (await api(base, 'GET', `/api/units/${uid}`)).unit;
  return (unit.links_out || []).filter((link) => link.relation === 'part_of').map((link) => link.target.uid);
};

async function until(check, what, ms = 15000) {
  const end = Date.now() + ms;
  for (;;) {
    const value = await check();
    if (value) return value;
    if (Date.now() > end) throw new Error(`timed out: ${what}`);
    await new Promise((r) => setTimeout(r, 150));
  }
}

test('mind map multi-select: shared attributes, batch status edit + undo, Ctrl+Enter group + undo',
  { skip: !chromium || !SERVER, timeout: 300000 }, async () => {
    const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-multi-'));
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
        await api(base, 'POST', '/api/projects', { name: `Multi ${lang}` });
        const mk = async (title, extra = {}) => (await api(base, 'POST', '/api/units', { type: 'task', title, ...extra })).unit.uid;
        const root = await mk('Plan', { type: 'note' });
        const a = await mk('Alpha', { tags: ['seq:1', 'shared'] });
        const b = await mk('Beta', { status: 'active', tags: ['seq:2', 'shared', 'only-b'] });
        const c = await mk('Gamma', { tags: ['seq:3', 'shared'] });
        const d = await mk('Delta', { tags: ['seq:4'] });
        for (const uid of [a, b, c, d]) await api(base, 'POST', '/api/links', { source: uid, target: root, relation: 'part_of' });

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
        await page.waitForFunction(() => (window.__ARK__?.graphs || []).some((g) => g.layout === 'mind' && g.byId?.size >= 5),
          null, { timeout: 30000 });
        // Screen point of a node card (GraphView: x * zoom + width / 2 + pan.x).
        const at = (uid) => page.evaluate((uid) => {
          const graph = window.__ARK__.graphs.find((g) => g.layout === 'mind' && g.byId?.size >= 5);
          const node = graph.nodes.find((n) => n.uid === uid);
          const rect = graph.canvas.getBoundingClientRect();
          return { x: rect.left + node.x * graph.zoom + graph.width / 2 + graph.pan.x,
            y: rect.top + node.y * graph.zoom + graph.height / 2 + graph.pan.y };
        }, uid);
        const selectThree = async () => {
          const pa = await at(a); const pb = await at(b); const pc = await at(c);
          await page.mouse.click(pa.x, pa.y);
          await page.keyboard.down('Control');
          await page.mouse.click(pb.x, pb.y);
          await page.mouse.click(pc.x, pc.y);
          await page.keyboard.up('Control');
        };
        // The first-run guide invitation sits over the panel footer.
        await page.locator('.guidance-invite .btn.ghost').click({ timeout: 4000 }).catch(() => {});
        await page.waitForTimeout(800);
        await selectThree();

        // Shared attributes: type is common, status differs ("Mixed").
        const panel = page.locator('[data-multi-inspector]');
        await panel.waitFor({ timeout: 15000 });
        await page.waitForFunction(() => document.querySelector('[data-multi-count]')?.textContent.includes('3'));
        const mixed = lang === 'zh' ? '多个值' : 'Mixed';
        assert.equal(await panel.locator('[data-multi-field="type"]').inputValue(), 'task');
        assert.equal(await panel.locator('[data-multi-field="status"] option:checked').textContent(), mixed);
        assert.deepEqual(await panel.locator('[data-multi-tags] .tag:not(.partial)').evaluateAll((els) => els.map((el) => el.dataset.tag)), ['tag:shared']);
        const partial = await panel.locator('[data-multi-tags] .tag.partial').evaluateAll((els) => els.map((el) => el.dataset.tag));
        assert.ok(partial.includes('tag:only-b') && partial.includes('seq:2'), `tags on some: ${partial}`);
        assert.equal(await panel.locator('[data-multi-nodes] .multi-node').count(), 3);
        await page.screenshot({ path: path.join(shots, `multiselect-inspector-${lang}.png`) });

        // One status edit updates all three in one batch; Undo restores each.
        await panel.locator('[data-multi-field="status"]').selectOption('done');
        await until(async () => {
          for (const uid of [a, b, c]) if ((await api(base, 'GET', `/api/units/${uid}`)).unit.status !== 'done') return false;
          return true;
        }, 'all three done');
        assert.equal((await api(base, 'GET', `/api/units/${d}`)).unit.status, 'open', 'Delta was not selected');
        const activity = (await api(base, 'GET', `/api/units/${a}`)).unit.activity || [];
        assert.ok(activity.some((item) => item.action === 'unit.update' && String(item.detail?.batch || '').startsWith('b_')), 'activity carries the batch id');
        await panel.locator('[data-multi-undo]').click();
        await until(async () => (await api(base, 'GET', `/api/units/${b}`)).unit.status === 'active'
          && (await api(base, 'GET', `/api/units/${a}`)).unit.status === 'open', 'undo restores each status');

        // Add a tag to all three.
        await panel.locator('[data-multi-tag-input]').fill('batch-tag');
        await panel.locator('[data-multi-tag-input]').press('Enter');
        await until(async () => {
          for (const uid of [a, b, c]) if (!((await api(base, 'GET', `/api/units/${uid}`)).unit.tags || []).includes('tag:batch-tag')) return false;
          return true;
        }, 'tag on all three');

        // Ctrl+Enter: one new parent for the three siblings; Esc keeps the
        // default title, Ctrl+Z dissolves it in one step.
        await selectThree();
        await page.keyboard.press('Control+Enter');
        const editor = page.locator('.graph-mind-editor');
        await editor.waitFor({ timeout: 15000 });
        const group = await until(async () => {
          const parents = await parentOf(base, a);
          return parents.length === 1 && parents[0] !== root ? parents[0] : null;
        }, 'Alpha under the new parent');
        assert.deepEqual(await parentOf(base, b), [group]);
        assert.deepEqual(await parentOf(base, c), [group]);
        assert.deepEqual(await parentOf(base, d), [root]);
        assert.deepEqual(await parentOf(base, group), [root]);
        assert.equal((await api(base, 'GET', `/api/units/${group}`)).unit.type, 'task', 'the siblings share type task');
        await editor.press('Escape');
        await page.keyboard.press('Control+z');
        await until(async () => (await parentOf(base, a))[0] === root, 'undo puts Alpha back');
        assert.deepEqual(await parentOf(base, b), [root]);
        assert.deepEqual(await parentOf(base, c), [root]);
        assert.ok((await api(base, 'GET', '/api/trash')).units.some((unit) => unit.uid === group), 'the group node is in the bin');

        // Again with a title: the final structure for the screenshot.
        await selectThree();
        await page.keyboard.press('Control+Enter');
        await editor.waitFor({ timeout: 15000 });
        await editor.fill(lang === 'zh' ? '第一阶段' : 'Phase 1');
        await editor.press('Enter');
        const named = await until(async () => {
          const parents = await parentOf(base, a);
          if (parents[0] === root) return null;
          const unit = (await api(base, 'GET', `/api/units/${parents[0]}`)).unit;
          return unit.title === (lang === 'zh' ? '第一阶段' : 'Phase 1') ? unit : null;
        }, 'titled group');
        assert.deepEqual(await parentOf(base, named.uid), [root]);
        await page.waitForTimeout(400);
        await page.screenshot({ path: path.join(shots, `multiselect-group-${lang}.png`) });

        // Different parents: a hint, nothing changes.
        const pa = await at(a); const pd = await at(named.uid);
        await page.mouse.click(pa.x, pa.y);
        await page.keyboard.down('Control');
        await page.mouse.click(pd.x, pd.y);
        await page.keyboard.up('Control');
        await page.keyboard.press('Control+Enter');
        await page.waitForTimeout(500);
        assert.equal(await page.locator('.graph-mind-editor').count(), 0);
        assert.deepEqual(await parentOf(base, a), [named.uid]);
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
