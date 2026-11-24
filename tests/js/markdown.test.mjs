import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import vm from "node:vm";

const context = vm.createContext({});
const modules = new Map();
async function load(url) {
  if (modules.has(url.href)) return modules.get(url.href);
  const module = new vm.SourceTextModule(await readFile(url, "utf8"), { context, identifier: url.href });
  modules.set(url.href, module);
  await module.link((specifier, parent) => load(new URL(specifier, parent.identifier)));
  return module;
}
const module = await load(new URL("../../ark_studio/web/js/markdown.js", import.meta.url));
await module.evaluate();
const { markdownTokens, markdownInlineTokens, bareUrlEnd } = module.namespace;

test("Marked handles combined and nested emphasis, escapes and punctuation", () => {
  for (const source of ["***both***", "**_both_**", "__*both*__"]) {
    const tokens = markdownInlineTokens(source);
    const all = JSON.stringify(tokens);
    assert.ok(all.includes('"type":"strong"'), source);
    assert.ok(all.includes('"type":"em"'), source);
    assert.equal(tokens[0].raw, source);
  }
  const source = "**Bold with *Figure R3* and *Figure R4***.";
  const tokens = markdownInlineTokens(source);
  assert.equal(tokens[0].type, "strong");
  assert.equal(tokens[0].tokens.filter((t) => t.type === "em").length, 2);
  assert.equal(markdownInlineTokens("`***literal***`")[0].type, "codespan");
});

test("ARK references, math, citations and media survive inside standard Markdown", () => {
  const tokens = markdownInlineTokens("**[[F-0045]]** $a*b$ [12] /api/media/7/raw \\[[C-0001]]");
  assert.equal(tokens[0].tokens[0].type, "arkReference");
  for (const type of ["arkMath", "arkCitation", "arkMedia", "arkLiteral"]) {
    assert.ok(tokens.some((token) => token.type === type), type);
  }
  assert.equal(markdownInlineTokens("[12](https://example.com)")[0].type, "link");
});

test("GFM nested lists, tasks, tables, reference links and block math are parsed", () => {
  const tokens = markdownTokens("- [x] Outer\n  - **Inner**\n\n| left | right |\n| :--- | ---: |\n| x | y |\n\n$$\na^2+b^2\n$$\n\n[link][1]\n\n[1]: https://example.com\n");
  assert.ok(tokens[0].items[0].task);
  assert.ok(tokens[0].items[0].tokens.some((t) => t.type === "list"));
  assert.ok(tokens.some((t) => t.type === "table"));
  assert.ok(tokens.some((t) => t.type === "arkMathBlock"));
  assert.ok(tokens.some((t) => t.type === "paragraph" && t.tokens[0].type === "link"));
});

const autoLinks = (src) => markdownInlineTokens(src).filter((t) => t.type === "link").map((t) => t.href).join(" | ");

test("bare URL stops at CJK and full-width punctuation", () => {
  assert.equal(autoLinks("备用地址：https://ark-bkp.example.com:8443（Let's Encrypt 证书"), "https://ark-bkp.example.com:8443");
  assert.equal(autoLinks("见 https://example.com/a，然后"), "https://example.com/a");
  assert.equal(autoLinks("打开https://example.com/x。"), "https://example.com/x");
  assert.equal(autoLinks("www.example.com中文"), "http://www.example.com");
});

test("ASCII bare URLs keep Marked's behaviour", () => {
  assert.equal(autoLinks("see https://example.com/a_(b) now"), "https://example.com/a_(b)");
  assert.equal(autoLinks("see https://example.com/path."), "https://example.com/path");
  assert.equal(bareUrlEnd("https://x.example/a)。"), "https://x.example/a");
});
test("a bare task: path is a link; a word ending in task: is not", () => {
  const tokens = markdownInlineTokens("see task:20261124_T-0117/artifacts/out.mp4。然后 multitask:x");
  const links = tokens.filter((t) => t.type === "link");
  assert.equal(links.length, 1);
  assert.equal(links[0].href, "task:20261124_T-0117/artifacts/out.mp4");
  const linked = markdownInlineTokens("[Task folder](task:20261124_T-0117/)").find((t) => t.type === "link");
  assert.equal(linked.href, "task:20261124_T-0117/");
});
