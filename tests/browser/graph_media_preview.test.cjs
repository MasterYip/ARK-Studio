// Optional browser regression (Playwright):
//   node --test tests/browser/graph_media_preview.test.cjs
// Clicking a card thumbnail (Mind and node graph) opens the media lightbox
// over the node's attachments (next/previous, video, Esc); a click elsewhere
// on the card selects it; a double-click on a thumbnail starts no title edit;
// right-click keeps the thumbnail menu; dragging the pinned main image (edit
// mode) moves it without a preview.
const assert = require('node:assert/strict');
const { createServer } = require('node:http');
const { readFile } = require('node:fs/promises');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch { /* Optional browser dependency. */ }

const svg = (color) => `data:image/svg+xml,${encodeURIComponent(`<svg xmlns="http://www.w3.org/2000/svg" width="80" height="60"><rect width="80" height="60" fill="${color}"/></svg>`)}`;

// Screen point on a thumbnail: scan the canvas with the graph's own hit test.
function findThumb(want) {
  const rect = window.graph.canvas.getBoundingClientRect();
  for (let y = rect.top; y < rect.bottom; y += 3) {
    for (let x = rect.left; x < rect.right; x += 3) {
      const hit = window.graph._hitMindImage({ clientX: x, clientY: y });
      if (!hit) continue;
      if (want === 'pinned' ? hit.pinned && !hit.corner : hit.item.id === want) return { x: x + 6, y: y + 6 };
    }
  }
  return null;
}

test('card thumbnails open the media preview', { skip: !chromium, timeout: 240000 }, async () => {
  const web = path.resolve(__dirname, '../../web');
  const server = createServer(async (req, res) => {
    const pathname = new URL(req.url, 'http://localhost').pathname;
    if (pathname === '/') {
      res.end('<!doctype html><link rel="stylesheet" href="/css/app.css"><div id="overlay-root"></div><div id="g" class="graph-host" style="width:1000px;height:640px"></div>');
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
    for (const layout of ['mind', 'node']) {
      const page = await browser.newPage({ viewport: { width: 1100, height: 720 } });
      const errors = [];
      page.on('pageerror', (error) => errors.push(error.message));
      await page.goto(`http://127.0.0.1:${server.address().port}/`);
      await page.evaluate(async ({ layout, images }) => {
        localStorage.clear();
        const { GraphView } = await import('/js/graph.js');
        const graph = new GraphView(document.getElementById('g'), { persistLayout: false, persistMode: false, layout });
        window.graph = graph;
        const media = [
          { id: 11, url: images[0], is_image: true, caption: 'Red' },
          { id: 12, url: images[1], is_image: true, caption: 'Blue' },
          { id: 13, url: 'clip.mp4', kind: 'video', caption: 'Clip' },
        ];
        graph.setData([
          { id: 1, uid: 'T-1', type: 'idea', status: 'open', title: 'Card with media', media },
          { id: 2, uid: 'T-2', type: 'idea', status: 'open', title: 'Other card' },
        ], [{ id: 'e', source: 2, target: 1, relation: 'part_of' }], { layout });
        graph.setMode('read');
        graph.fit();
        await new Promise((r) => setTimeout(r, 400));
        graph._draw();
      }, { layout, images: [svg('#d33'), svg('#36c')] });
      const thumb = await page.evaluate(findThumb, 11);
      const second = await page.evaluate(findThumb, 12);
      assert.ok(thumb && second, `${layout}: thumbnails are drawn and hit`);
      const title = await page.evaluate(() => {
        const rect = graph.canvas.getBoundingClientRect();
        const node = graph.byId.get(1);
        const tr = graph._transform();
        return { x: rect.left + (node.x - node.w / 2 + 30) * tr.z + tr.x, y: rect.top + node.y * tr.z + tr.y };
      });
      // A click elsewhere on the card selects it, no preview.
      await page.mouse.click(title.x, title.y);
      assert.equal(await page.locator('.iv-media-overlay').count(), 0, `${layout}: clicking the title opens no preview`);
      assert.deepEqual(await page.evaluate(() => [...graph.selectedNodes]), [1]);
      // A click on the first thumbnail: lightbox at 1 / 3.
      await page.mouse.click(thumb.x, thumb.y);
      await page.waitForSelector('.iv-media-overlay');
      assert.equal(await page.locator('.iv-media .iv-meta').textContent(), '1 / 3');
      assert.equal(await page.locator('.iv-media-stage').getAttribute('data-kind'), 'image');
      await page.keyboard.press('ArrowRight');
      assert.equal(await page.locator('.iv-media .iv-meta').textContent(), '2 / 3');
      await page.locator('.iv-media .iv-next').click();
      assert.equal(await page.locator('.iv-media-stage').getAttribute('data-kind'), 'video', 'the video plays in the lightbox');
      assert.equal(await page.locator('.iv-media video').count(), 1);
      await page.locator('.iv-media .iv-prev').click();
      assert.equal(await page.locator('.iv-media .iv-meta').textContent(), '2 / 3');
      if (layout === 'mind') await page.screenshot({ path: process.env.ARK_QA_SHOT || path.join(require('node:os').tmpdir(), 'media-preview.png') });
      await page.keyboard.press('Escape');
      assert.equal(await page.locator('.iv-media-overlay').count(), 0, 'Esc closes');
      // A double-click on a thumbnail in edit mode: the preview, no title editor.
      await page.evaluate(() => graph.setMode('edit'));
      await page.mouse.dblclick(second.x, second.y);
      await page.waitForSelector('.iv-media-overlay');
      assert.equal(await page.evaluate(() => graph.editingNodeId), null, `${layout}: no title edit from a thumbnail double-click`);
      assert.equal(await page.locator('.iv-media .iv-meta').textContent(), '2 / 3');
      await page.keyboard.press('Escape');
      // Right-click keeps the thumbnail menu.
      await page.mouse.click(thumb.x, thumb.y, { button: 'right' });
      assert.ok(await page.locator('.ctx-menu, .context-menu, [role="menu"]').count() > 0, 'right-click opens the thumbnail menu');
      assert.equal(await page.locator('.iv-media-overlay').count(), 0);
      await page.keyboard.press('Escape');
      if (layout === 'mind') {
        // Pin the first image as the main image, then drag it to the other side.
        await page.evaluate(() => {
          const node = graph.byId.get(1);
          node.pinnedImage = { mediaId: 11, side: 'right', w: 120, h: 90, aspect: 4 / 3 };
          graph._applyLayout(false);
        });
        const pinned = await page.evaluate(findThumb, 'pinned');
        assert.ok(pinned, 'the pinned image is hit');
        await page.mouse.move(pinned.x, pinned.y);
        await page.mouse.down();
        await page.mouse.move(pinned.x - 300, pinned.y, { steps: 8 });
        await page.mouse.up();
        assert.equal(await page.locator('.iv-media-overlay').count(), 0, 'dragging the pinned image opens no preview');
        assert.equal(await page.evaluate(() => graph.byId.get(1).pinnedImage.side), 'left', 'the drag moved the main image');
        // A click on the pinned image previews it.
        const again = await page.evaluate(findThumb, 'pinned');
        await page.mouse.click(again.x, again.y);
        await page.waitForSelector('.iv-media-overlay');
        assert.equal(await page.locator('.iv-media .iv-meta').textContent(), '1 / 3');
        await page.keyboard.press('Escape');
      }
      assert.deepEqual(errors, []);
      await page.close();
    }
  } finally {
    if (browser) await browser.close();
    await new Promise((resolve) => server.close(resolve));
  }
});
