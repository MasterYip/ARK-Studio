const assert = require('node:assert/strict');
const { createServer } = require('node:http');
const { readFile, mkdir } = require('node:fs/promises');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch { /* Optional browser dependency. */ }

test('history wraps aligned rows, scrolls both sides and highlights inline changes safely', { skip: !chromium }, async () => {
  const web = path.resolve(__dirname, '../../ark_studio/web');
  const server = createServer(async (req, res) => {
    const pathname = new URL(req.url, 'http://localhost').pathname;
    if (pathname === '/') { res.end('<!doctype html><html><head><link rel="stylesheet" href="/css/app.css"></head><body></body></html>'); return; }
    const file = path.resolve(web, '.' + pathname);
    if (!file.startsWith(web + path.sep)) { res.writeHead(403).end(); return; }
    try {
      const body = await readFile(file);
      res.setHeader('Content-Type', file.endsWith('.js') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : 'text/plain');
      res.end(body);
    } catch { res.writeHead(404).end(); }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  let browser;
  try {
    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(`http://127.0.0.1:${server.address().port}/`);
    const source = [
      'diff --git a/body.md b/body.md', '--- a/body.md', '+++ b/body.md', '@@ -1,83 +1,83 @@',
      '-Measured **1.9 cm** with 3 seeds.', '+Measured **1.7 cm** with 3 seeds.',
      '-https://example.test/' + 'x'.repeat(1200) + ' old end',
      '+https://example.test/' + 'x'.repeat(350) + ' new end',
      ...Array.from({ length: 80 }, (_, i) => ` Context ${i}: shared evidence 研究🙂`),
      '-<img src=x onerror="window.BAD=1"> old', '+<img src=x onerror="window.BAD=2"> new', '',
    ].join('\n');
    await page.evaluate(async (source) => {
      const { h } = await import('/js/dom.js');
      const { renderHistoryDiff } = await import('/js/history-diff-view.js');
      document.documentElement.dataset.theme = 'light';
      document.body.replaceChildren(h('div', { class: 'history-content', tabindex: '0',
        style: 'width:1100px;max-width:calc(100vw - 32px);height:400px;margin:16px' }, renderHistoryDiff(source)));
    }, source);
    const root = page.locator('.history-content');
    const split = root.locator('.history-diff-split');
    assert.equal(await split.isVisible(), true);
    assert.equal(await root.locator('.history-diff-unified').isVisible(), false);
    const firstChange = split.locator('.history-diff-pair').nth(2);
    assert.deepEqual(await firstChange.locator('.history-word-change').allTextContents(), ['9', '7']);
    const initial = await root.evaluate(element => {
      const pairs = [...element.querySelectorAll('.history-diff-pair')];
      const misaligned = pairs.filter(pair => {
        const [before, after] = [...pair.children].map(cell => cell.getBoundingClientRect());
        return Math.abs(before.y - after.y) > 1 || Math.abs(before.height - after.height) > 1;
      }).length;
      const long = pairs[3].querySelector('.history-line-text');
      const range = document.createRange(); range.selectNodeContents(long);
      return { misaligned, overflow: element.scrollWidth > element.clientWidth + 1,
        wrapped: range.getClientRects().length > 3,
        words: [...pairs[2].querySelectorAll('mark')].map(mark => ({ text: mark.textContent,
          foreground: getComputedStyle(mark).color, background: getComputedStyle(mark).backgroundColor,
          line: getComputedStyle(mark.closest('.history-diff-row')).backgroundColor })) };
    });
    assert.equal(initial.misaligned, 0);
    assert.equal(initial.overflow, false);
    assert.equal(initial.wrapped, true);
    assert.ok(initial.words.every(mark => mark.background !== mark.line && mark.background !== 'rgba(0, 0, 0, 0)'));
    for (const fraction of [0.25, 0.75]) {
      await root.evaluate(element => { element.scrollTop = 0; });
      const box = await root.boundingBox();
      await page.mouse.move(box.x + box.width * fraction, box.y + 150);
      await page.mouse.wheel(0, 350);
      await page.waitForTimeout(100);
      const scrolled = await root.evaluate(element => ({ top: element.scrollTop,
        positions: [...element.querySelector('.history-diff-pair:nth-child(5)').children].map(cell => cell.getBoundingClientRect().y) }));
      assert.ok(scrolled.top > 100, JSON.stringify(scrolled));
      assert.equal(scrolled.positions[0], scrolled.positions[1]);
    }
    await root.evaluate(element => { element.scrollTop = 0; });
    if (process.env.ARK_TEST_SCREENSHOTS) {
      await mkdir(process.env.ARK_TEST_SCREENSHOTS, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARK_TEST_SCREENSHOTS, 'diff-wrap-light.png') });
    }
    await page.evaluate(() => { document.documentElement.dataset.theme = 'dark'; document.documentElement.dataset.contrast = 'high'; document.documentElement.style.setProperty('--font-scale', '1.5'); });
    const dark = await root.evaluate(element => ({ overflow: element.scrollWidth > element.clientWidth + 1,
      aligned: [...element.querySelectorAll('.history-diff-pair')].every(pair => Math.abs(pair.children[0].getBoundingClientRect().height - pair.children[1].getBoundingClientRect().height) < 1) }));
    assert.equal(dark.overflow, false);
    assert.equal(dark.aligned, true);
    if (process.env.ARK_TEST_SCREENSHOTS) await page.screenshot({ path: path.join(process.env.ARK_TEST_SCREENSHOTS, 'diff-wrap-dark-150.png') });
    await page.setViewportSize({ width: 390, height: 844 });
    assert.equal(await split.isVisible(), false);
    assert.equal(await root.locator('.history-diff-unified').isVisible(), true);
    assert.ok(await root.locator('.history-diff-unified mark').count() > 2);
    assert.equal(await root.evaluate(element => element.scrollWidth > element.clientWidth + 1), false);
    const prefixAligned = await root.locator('.history-unified-line.removed').nth(1).evaluate(element => {
      const prefix = element.querySelector('.history-unified-sign').getBoundingClientRect();
      const text = element.querySelector('.history-unified-text').getBoundingClientRect();
      return Math.abs(prefix.y - text.y) < 1;
    });
    assert.equal(prefixAligned, true);
    if (process.env.ARK_TEST_SCREENSHOTS) await page.screenshot({ path: path.join(process.env.ARK_TEST_SCREENSHOTS, 'diff-wrap-mobile.png') });
    assert.equal(await root.locator('img,script,[onerror]').count(), 0);
    assert.equal(await page.evaluate(() => window.BAD), undefined);
    assert.deepEqual(errors, []);
    // Apply arrows expose file/block identities in split and unified layouts.
    await page.evaluate(async (source) => {
      const { renderHistoryDiff } = await import('/js/history-diff-view.js');
      window.applied = [];
      document.querySelector('.history-content').replaceChildren(renderHistoryDiff(source, {
        canApply: true, onApply: change => window.applied.push(change),
      }));
    }, source);
    await root.locator('.history-diff-unified button[data-change="0"]').click();
    assert.deepEqual(await page.evaluate(() => window.applied), [{ file: 'body.md', change: 0 }]);
    await page.setViewportSize({ width: 1440, height: 900 });
    await root.evaluate(element => { element.scrollTop = 0; });
    await split.locator('button[data-change="file"]').click();
    assert.deepEqual(await page.evaluate(() => window.applied), [
      { file: 'body.md', change: 0 }, { file: 'body.md', change: null },
    ]);
    await page.evaluate(async (source) => {
      const { renderHistoryDiff } = await import('/js/history-diff-view.js');
      document.querySelector('.history-content').replaceChildren(renderHistoryDiff(source, { onApply() {} }));
    }, source);
    assert.equal(await split.locator('.history-apply:enabled').count(), 0);
    const oldParagraphs = ['- **Robustness.** Table R1 and Figure R1 compare each tracker with its student.',
      '- **Responsiveness.** We measure response delay and recovery time under a sustained pull.'];
    const newParagraphs = ['- **Physical consistency.** Predicted and executed states agree within the measured error bounds.',
      oldParagraphs[0].replace('Figure R1', 'Figure R3'), oldParagraphs[1].replace('recovery time', 'recovery duration')];
    const paragraphSource = ['diff --git a/body.md b/body.md', '--- a/body.md', '+++ b/body.md', '@@ -1,2 +1,3 @@',
      ...oldParagraphs.map(line => '-' + line), ...newParagraphs.map(line => '+' + line)].join('\n');
    await page.evaluate(async (source) => {
      const { renderHistoryDiff } = await import('/js/history-diff-view.js');
      document.documentElement.dataset.theme = 'light';
      document.documentElement.dataset.contrast = 'normal';
      document.documentElement.style.setProperty('--font-scale', '1');
      document.querySelector('.history-content').replaceChildren(renderHistoryDiff(source, { canApply: true, onApply() {} }));
    }, paragraphSource);
    const paragraphRows = split.locator('.history-diff-pair');
    assert.equal(await paragraphRows.nth(2).locator('[data-side=before]').getAttribute('class'), 'history-diff-row placeholder');
    assert.ok((await paragraphRows.nth(2).locator('[data-side=after]').innerText()).includes('Physical consistency'));
    assert.deepEqual(await paragraphRows.nth(3).locator('mark').allTextContents(), ['1', '3']);
    if (process.env.ARK_TEST_SCREENSHOTS) await page.screenshot({ path: path.join(process.env.ARK_TEST_SCREENSHOTS, 'diff-inserted-paragraph.png') });
  } finally {
    if (browser) await browser.close();
    await new Promise(resolve => server.close(resolve));
  }
});
