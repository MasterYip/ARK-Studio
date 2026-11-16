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
const module = await load(new URL("../../ark_studio/web/js/rich-html.js", import.meta.url));
await module.evaluate();
const { htmlBlockTokens, inlineHtmlTokens } = module.namespace;

test("raw HTML wrapping Markdown retains both boundaries and intervening blocks", () => {
  const source = "<details>\n<summary>Results</summary>\n\n**Evidence** [[C-0001]]\n\n</details>\n";
  const tokens = htmlBlockTokens(source);
  assert.equal(tokens.map((token) => token.raw).join(""), source);
  assert.equal(tokens[0].type, "html");
  assert.equal(tokens.at(-1).type, "html");
  assert.ok(tokens.some((token) => token.type === "paragraph" && token.text.includes("Evidence")));
});

test("nested quote/list HTML is detected while literal code stays literal", () => {
  assert.ok(htmlBlockTokens("> <details>\n> <summary>Result</summary>\n> </details>"));
  assert.ok(htmlBlockTokens("- <strong>Result</strong>"));
  assert.equal(htmlBlockTokens("```text\n<div>Literal</div>\n```"), null);
  assert.equal(inlineHtmlTokens("`<img src=x onerror=alert(1)>`"), null);
});

test("inline HTML keeps paired tags, quoted attributes and Markdown separate", () => {
  const source = 'Before <span title="a > b"><sub>x</sub> **bold**</span> after';
  const tokens = inlineHtmlTokens(source);
  assert.equal(tokens.map((token) => token.raw).join(""), source);
  assert.equal(tokens.filter((token) => token.type === "html").length, 4);
  assert.ok(tokens.some((token) => token.type === "strong"));
  assert.equal(inlineHtmlTokens("a < b and c > d"), null);
});
