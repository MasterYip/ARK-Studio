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
const module = await load(new URL("../../web/js/markdown.js", import.meta.url));
await module.evaluate();
const { markdownTokens, markdownInlineTokens } = module.namespace;

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
