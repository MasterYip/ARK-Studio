// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node --test tests/browser/import_ark_project.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge> to pick a browser, ARK_TEST_SHOTS=<dir> for the screenshots)
// Ideas › Import from another ARK project: the Import button opens the
// source pane, the source picker offers the other project, the filter tab
// holds ARK's filters for that project (a type chip narrows the source
// graph), "Import shown…" previews and copies into this project with the
// provenance line; the source is unchanged.  English and Chinese.
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

// Poll in Node: the app's CSP refuses page.waitForFunction's eval.
async function until(fn, what, ms = 20000) {
  const end = Date.now() + ms;
  for (;;) {
    if (await fn()) return;
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 150));
  }
}

async function api(base, method, route, body, project) {
  const headers = { Origin: base };
  if (body) headers['Content-Type'] = 'application/json';
  if (project) headers['X-ARK-Project'] = project;
  const res = await fetch(base + route, { method, headers, body: body ? JSON.stringify(body) : undefined });
  const data = await res.json();
  if (!res.ok) throw new Error(`${method} ${route}: ${res.status} ${JSON.stringify(data)}`);
  return data;
}

test('ideas: import from another ARK project', { skip: !chromium || !SERVER, timeout: 300000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-import-'));
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
    // Source project: two ideas (one under the other), a task.
    const srcId = (await api(base, 'POST', '/api/projects', { name: 'Tokenizer notes' })).summary.id;
    const mk = async (type, title, body = '') => (await api(base, 'POST', '/api/units', { type, title, body }, srcId)).unit.uid;
    const a = await mk('idea', 'Try a unigram tokenizer', 'Compare with BPE.');
    const b = await mk('idea', 'Vocabulary of 32k', `Part of [[${a}]].`);
    await mk('task', 'Run the BPE baseline');
    await api(base, 'POST', '/api/links', { source: b, target: a, relation: 'part_of' }, srcId);
    // A local server has one open project: read the source through the
    // import route once another project is open.
    const srcBefore = JSON.stringify((await api(base, 'GET', '/api/units?limit=50', null, srcId)).items
      .map((u) => [u.uid, u.title, u.updated_at]).sort());

    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    for (const lang of ['en', 'zh']) {
      // Target project: created last, so it is the open one.
      const targetId = (await api(base, 'POST', '/api/projects', { name: `Lab ${lang}` })).summary.id;
      const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
      await context.addInitScript((lang) => {
        localStorage.setItem('ark.prefsV2', '1');
        localStorage.setItem('ark.lang', lang);
        localStorage.setItem('ark.welcome.v1:local', '1');
      }, lang);
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', (error) => errors.push(error.message));
      await page.goto(`${base}/?lang=${lang}#/research/idea`);
      const button = page.locator('[data-ideas-import]');
      await button.waitFor({ timeout: 30000 });
      assert.equal((await button.textContent()).trim(), lang === 'zh' ? '导入' : 'Import');
      await button.click();
      const pane = page.locator('.import-ark-pane');
      await pane.waitFor({ timeout: 15000 });
      let select = pane.locator('select.import-source');
      assert.ok((await select.locator('option').allTextContents()).includes('Tokenizer notes'));
      // The second round also offers the first round's project: pick the source.
      if (await select.inputValue() !== srcId) {
        await select.selectOption(srcId);
        await until(async () => (await page.locator(`.import-ark-pane[data-import-source="${srcId}"]`).count()) === 1, 'the chosen source');
        select = page.locator('.import-ark-pane select.import-source');
      }
      assert.equal(await select.inputValue(), srcId);
      const pill = page.locator('.import-ark-pane .ideas-pane-head .pill');
      await until(async () => /^[1-9]/.test((await pill.textContent()) || ''), 'the source graph');
      const countAll = Number(await pill.textContent());
      assert.equal(countAll, 4, `${lang}: three units and the Start here note`);
      // The second filter tab is the source with ARK's filters (open the
      // Filters view of the side bar first).
      const filtersIcon = page.locator('[data-view-icon="filters"]').first();
      if (await filtersIcon.count()) await filtersIcon.click();
      const tabs = page.locator('.rail .rail-tab');
      if (await tabs.count()) {
        assert.equal((await tabs.nth(1).textContent()).trim(), 'Tokenizer notes');
        const ideaChip = page.locator('.rail .type-chip', { hasText: lang === 'zh' ? '想法' : 'Idea' }).first();
        // The Filters view may be folded away in the side bar; when shown,
        // an ARK type chip narrows the source graph.
        if (await ideaChip.count() && await ideaChip.isVisible()) {
          await page.screenshot({ path: path.join(shots, `import-filters-${lang}.png`) });
          await ideaChip.click();
          await until(async () => /^[0-9]+$/.test((await pill.textContent()) || '') && (await pill.textContent()) !== '4', 'the filtered graph');
        }
      }
      await page.screenshot({ path: path.join(shots, `import-pane-${lang}.png`) });
      const shownBefore = Number(await page.locator('.import-ark-pane .ideas-pane-head .pill').textContent());
      // Import what is shown, with the preview first.
      await page.locator('.import-ark-pane [data-import-filtered]').click();
      const modal = page.locator('.modal');
      await modal.waitFor();
      const text = await modal.textContent();
      assert.match(text, lang === 'zh' ? /复制 \d+ 个节点到本项目/ : /nodes are copied into this project/);
      await page.screenshot({ path: path.join(shots, `import-preview-${lang}.png`) });
      await modal.locator('.btn.primary').click();
      await until(async () => (await page.locator('.modal').count()) === 0, 'the preview to close');
      // Each pane shows its own project: the left still the source (as
      // before, the copies greyed), the right this project, named in its title.
      await until(async () => !(await page.locator('.import-ark-imported').first().isHidden()), 'the imported marker');
      assert.equal(Number(await page.locator('.import-ark-pane .ideas-pane-head .pill').textContent()), shownBefore, `${lang}: source pane`);
      const rightTitle = (await page.locator('.ark-pane .ideas-pane-title').textContent()).trim();
      assert.equal(rightTitle, lang === 'zh' ? `本项目：Lab ${lang}` : `This project: Lab ${lang}`);
      const targetIdeas = (await api(base, 'GET', '/api/graph?types=idea&limit=700', null, targetId)).nodes.length;
      await until(async () => Number((await page.locator('.ark-pane .ideas-pane-head .pill').textContent()) || -1) === targetIdeas,
        'the target pane count');
      // The copies are here, with their provenance.
      let items = [];
      for (let i = 0; i < 50; i += 1) {
        items = (await api(base, 'GET', '/api/units?limit=50', null, targetId)).items.filter((u) => u.origin === 'ark-import');
        if (items.length) break;
        await new Promise((r) => setTimeout(r, 200));
      }
      assert.ok(items.length >= 1, `${lang}: imported units`);
      const one = await api(base, 'GET', `/api/units/${items[0].uid}`, null, targetId);
      assert.match(one.unit.body, new RegExp(`(Imported from|导入自) Tokenizer notes \\[\\[${srcId}/`));
      await page.screenshot({ path: path.join(shots, `import-done-${lang}.png`) });
      assert.deepEqual(errors, [], `${lang}: page errors`);
      await context.close();
    }
    const srcAfter = JSON.stringify((await api(base, 'GET', `/api/import-ark/graph?project=${srcId}`)).nodes
      .map((u) => [u.uid, u.title, u.updated_at]).sort());
    assert.equal(srcAfter, srcBefore, 'source unchanged');
  } finally {
    await browser?.close();
    server.kill();
    if (!process.env.ARK_TEST_SHOTS) rmSync(dir, { recursive: true, force: true });
  }
});
