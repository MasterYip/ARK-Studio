// Optional DOM regression: npm install --no-save playwright, then
// node --test tests/browser/rich_html.test.cjs
const assert = require('node:assert/strict');
const { createServer } = require('node:http');
const { readFile } = require('node:fs/promises');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch { /* Optional browser dependency. */ }

test('HTML shares Markdown rendering without executing or leaking document markup', { skip: !chromium }, async () => {
  const web = path.resolve(__dirname, '../../web');
  const server = createServer(async (req, res) => {
    const pathname = new URL(req.url, 'http://localhost').pathname;
    if (pathname === '/') { res.end('<!doctype html><html><body></body></html>'); return; }
    const file = path.resolve(web, '.' + pathname);
    if (!file.startsWith(web + path.sep)) { res.writeHead(403).end(); return; }
    try {
      const body = await readFile(file);
      res.setHeader('Content-Type', file.endsWith('.js') ? 'text/javascript' : 'text/plain');
      res.end(body);
    } catch { res.writeHead(404).end(); }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  let browser;
  try {
    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    const page = await browser.newPage();
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(`http://127.0.0.1:${server.address().port}/`);
    const result = await page.evaluate(async () => {
      const { richText } = await import('/js/dom.js');
      const source = [
        'Inline <strong>HTML</strong> <sub>sub</sub> **Markdown** [[C-0001]] `literal <img>`.', '',
        '<details open><summary>Summary</summary>', '', '**Inside** [[N-0001]]', '', '</details>', '',
        '<table><tr><td>Value</td><td>42</td></tr></table>', '',
        '<a href="notes/result.md">Workspace</a><img src="fig.png" alt="Figure">', '',
        '<p><span title="colours" style="color:#d21f3c;background:#e9f8ee;border-color:rgb(20,80,30);position:fixed;inset:0;z-index:99999;background-image:url(https://example.com/bad.png)">Colours</span></p>',
        '<p><font color="#123456">Legacy colour</font></p>', '',
        '<video src="clip.mp4"></video><audio><source src="clip.wav"></audio>', '',
        '```text', '<img src=x onerror=alert(1)>', '```', '',
        '<script>window.__htmlXss=true</script>',
        '<svg onload="window.__htmlXss=true"><script>window.__htmlXss=true</script></svg>',
        '<img src="javascript:alert(1)" onerror="window.__htmlXss=true" style="position:fixed" id="bad">',
        '<a href="java&#x09;script:alert(1)">Bad</a><iframe src="https://example.com"></iframe>',
        '<form><input name="location"><button>Submit</button></form>',
      ].join('\n');
      const root = richText(source, { baseDir: 'reports' });
      document.body.replaceChildren(root);
      const resourcePath = (selector) => {
        const value = root.querySelector(selector)?.getAttribute('src');
        if (!value) return value;
        const url = new URL(value, location.href);
        return url.pathname + url.search;
      };
      const info = {
        strong: [...root.querySelectorAll('strong')].map(node => node.textContent),
        sub: root.querySelector('sub')?.textContent,
        details: root.querySelector('details strong')?.textContent,
        cells: [...root.querySelectorAll('td')].map(node => node.textContent),
        refs: [...root.querySelectorAll('.rich-unit-link')].map(node => node.dataset.unitRef),
        href: root.querySelector('a.rich-ws-link')?.dataset.wsPath,
        image: resourcePath('img[alt=Figure]'),
        video: resourcePath('video'),
        audio: resourcePath('audio source'),
        controls: [...root.querySelectorAll('video,audio')].every(node => node.controls),
        code: [...root.querySelectorAll('code')].map(node => node.textContent),
        forbidden: root.querySelectorAll('script,iframe,svg:not(.icon),form,input,button:not(.code-copy),[onerror],[onload],[style*="position"],#bad').length,
        unsafe: [...root.querySelectorAll('[href],[src]')].some(node => /^(?:javascript|data|vbscript):/i.test(node.getAttribute('href') || node.getAttribute('src'))),
      };
      const quote = richText('> <details>\n> <summary>Quoted</summary>\n> </details>');
      info.quoted = quote.querySelector('blockquote details summary')?.textContent;
      const math = richText('a < b and c > d');
      info.plain = math.textContent;
      const coloured = root.querySelector('[title=colours]');
      info.colours = { color: getComputedStyle(coloured).color,
        background: getComputedStyle(coloured).backgroundColor, border: coloured.style.borderTopColor,
        properties: [...coloured.style], legacy: root.querySelector('font')?.getAttribute('color') };
      const multiline = richText('Text <span title="multiline">\n**nested**\n</span> end');
      info.multiline = multiline.querySelector('span[title=multiline] strong')?.textContent;
      const combined = richText('***Both*** **Bold *italic* [[C-0001]]** __*underscores*__');
      info.combined = [...combined.querySelectorAll('em')].map(node => node.textContent);
      info.combinedReference = combined.querySelector('strong .rich-unit-link')?.dataset.unitRef;
      const rawReferences = richText('<div>HTML [[T-0011]] <code>[[C-0001]]</code> \\[[N-0001]]</div>');
      info.rawReferences = [...rawReferences.querySelectorAll('.rich-unit-link')].map(node => node.dataset.unitRef);
      info.literalReference = rawReferences.querySelector('code')?.textContent;
      const tasks = richText('- [x] Finished\n- [ ] Pending\n  - Nested');
      info.tasks = { checkboxes: tasks.querySelectorAll('input[type=checkbox]:disabled').length,
        checked: tasks.querySelectorAll('input:checked').length,
        text: tasks.textContent, nested: tasks.querySelectorAll('ul ul').length };
      const standard = richText('Heading\n=======\n\n[**Link**][paper]\n\n[paper]: https://example.com\n\n---');
      info.standard = { heading: standard.querySelector('h1')?.textContent,
        link: standard.querySelector('a strong')?.textContent, hr: standard.querySelectorAll('hr').length };
      return info;
    });
    assert.ok(result.strong.includes('HTML') && result.strong.includes('Markdown'));
    assert.equal(result.sub, 'sub');
    assert.equal(result.details, 'Inside');
    assert.deepEqual(result.cells, ['Value', '42']);
    assert.deepEqual(result.refs, ['C-0001', 'N-0001']);
    assert.equal(result.href, 'reports/notes/result.md');
    assert.equal(result.image, '/api/workspace/raw?path=reports%2Ffig.png');
    assert.equal(result.video, '/api/workspace/raw?path=reports%2Fclip.mp4');
    assert.equal(result.audio, '/api/workspace/raw?path=reports%2Fclip.wav');
    assert.equal(result.controls, true);
    assert.ok(result.code.includes('literal <img>') && result.code.includes('<img src=x onerror=alert(1)>'));
    assert.equal(result.forbidden, 0);
    assert.equal(result.unsafe, false);
    assert.equal(result.quoted, 'Quoted');
    assert.equal(result.plain, 'a < b and c > d');
    assert.equal(result.multiline, 'nested');
    assert.deepEqual(result.combined, ['Both', 'italic', 'underscores']);
    assert.equal(result.combinedReference, 'C-0001');
    assert.deepEqual(result.rawReferences, ['T-0011']);
    assert.equal(result.literalReference, '[[C-0001]]');
    assert.equal(result.tasks.checkboxes, 2);
    assert.equal(result.tasks.checked, 1);
    assert.equal(result.tasks.nested, 1);
    assert.ok(!result.tasks.text.includes('[x]') && !result.tasks.text.includes('[ ]'));
    assert.deepEqual(result.standard, { heading: 'Heading', link: 'Link', hr: 1 });
    assert.equal(result.colours.color, 'rgb(210, 31, 60)');
    assert.equal(result.colours.background, 'rgb(233, 248, 238)');
    assert.equal(result.colours.border, 'rgb(20, 80, 30)');
    assert.equal(result.colours.legacy, '#123456');
    assert.ok(result.colours.properties.every(property => property === 'color' || property.endsWith('-color')));
    await page.waitForTimeout(50);
    assert.equal(await page.evaluate(() => window.__htmlXss), undefined);
    assert.deepEqual(errors, []);
  } finally {
    if (browser) await browser.close();
    await new Promise(resolve => server.close(resolve));
  }
});
