// Run: node --experimental-vm-modules --test tests/js/rich_model.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

async function load() {
  const source = await readFile(new URL("../../ark_studio/web/js/rich-model.js", import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: "rich-model.js" });
  await module.link(() => { throw new Error("rich-model must stay dependency-free"); });
  await module.evaluate();
  return module.namespace;
}

const m = await load();

test("URL resolution: remote, media, project-relative and rejected schemes", () => {
  assert.deepEqual(m.resolveRichUrl("https://x.org/a.png"), { kind: "remote", href: "https://x.org/a.png" });
  assert.deepEqual(m.resolveRichUrl("/api/media/12/raw"), { kind: "media", mediaId: 12, href: "/api/media/12/raw" });
  assert.deepEqual(m.resolveRichUrl("media:7"), { kind: "media", mediaId: 7, href: "/api/media/7/raw" });
  assert.equal(m.resolveRichUrl("figures/a b.png").href, "/api/workspace/raw?path=figures%2Fa%20b.png");
  assert.equal(m.resolveRichUrl("./img/x.png", { baseDir: "docs/notes" }).path, "docs/notes/img/x.png");
  assert.equal(m.resolveRichUrl("../x.png", { baseDir: "docs/notes" }).path, "docs/x.png");
  assert.equal(m.resolveRichUrl("../../../etc/passwd", { baseDir: "docs" }), null);
  assert.equal(m.resolveRichUrl("javascript:alert(1)"), null);
  assert.equal(m.resolveRichUrl("JaVaScRiPt:alert(1)"), null);
  assert.equal(m.resolveRichUrl("data:text/html,<script>"), null);
  assert.equal(m.resolveRichUrl("vbscript:x"), null);
  assert.equal(m.resolveRichUrl("//evil.example/x.png"), null);
  assert.equal(m.resolveRichUrl(""), null);
  assert.equal(m.resolveRichUrl("#sec").kind, "anchor");
  assert.deepEqual(m.resolveRichUrl("?move_plan=pl_abc"), { kind: "query", href: "?move_plan=pl_abc" });
  assert.equal(m.resolveRichUrl("file:///C:/a.mp4").kind, "file");
  assert.equal(m.dirOf("docs/a/b.md"), "docs/a");
  assert.equal(m.dirOf("README.md"), "");
});

test("embed kind from extension, media kind map or label", () => {
  assert.equal(m.embedKind("clip.mp4"), "video");
  assert.equal(m.embedKind("https://x.org/v.webm?t=3"), "video");
  assert.equal(m.embedKind("song.mp3"), "audio");
  assert.equal(m.embedKind("a.PNG"), "image");
  assert.equal(m.embedKind("paper.pdf"), "");
  assert.equal(m.embedKind("/api/media/4/raw"), "");
  assert.equal(m.embedKind("/api/media/4/raw", { kinds: new Map([[4, "video"]]) }), "video");
  assert.equal(m.embedKind("/api/media/4/raw", { kinds: { 4: "image" } }), "image");
  assert.equal(m.embedKind("/api/media/4/raw", { label: "demo.mov" }), "video");
  assert.equal(m.embedKind("javascript:x.mp4"), "");
});

test("fenced block splitting finds mermaid and html visualisations", () => {
  const text = "# T\n```mermaid\ngraph TD\nA-->B\n```\ntext $x$\n```html\n<canvas></canvas>\n```\n```py\nprint(1)\n```\n$$\int f$$";
  const blocks = m.fencedBlocks(text);
  assert.deepEqual(blocks.map((b) => b.lang), ["mermaid", "html", "py"]);
  assert.equal(blocks[0].source, "graph TD\nA-->B");
  assert.equal(blocks[1].source, "<canvas></canvas>");
  assert.equal(m.fencedBlocks("```mermaid\nunclosed")[0].source, "unclosed");
  assert.ok(m.isVizLang("HTML"));
  assert.ok(!m.isVizLang("js"));
  assert.deepEqual(m.richSummary(text), { mermaid: 1, viz: 1, math: 2 });
  assert.equal(m.hashText("abc"), m.hashText("abc"));
  assert.notEqual(m.hashText("abc"), m.hashText("abd"));
});

test("viz frame messages are validated by source window, shape and size cap", () => {
  const frameA = {};
  const frameB = {};
  const frames = new Map([[frameA, "v1"], [frameB, "v2"]]);
  const ok = m.acceptVizMessage({ source: frameA, data: { type: "ark-viz-size", id: "v1", height: 240.4 } }, frames);
  assert.deepEqual(ok, { id: "v1", height: 240 });
  // Wrong window, spoofed id, unknown source, wrong type, bad heights.
  assert.equal(m.acceptVizMessage({ source: frameB, data: { type: "ark-viz-size", id: "v1", height: 10 } }, frames), null);
  assert.equal(m.acceptVizMessage({ source: {}, data: { type: "ark-viz-size", id: "v1", height: 10 } }, frames), null);
  assert.equal(m.acceptVizMessage({ source: frameA, data: { type: "other", id: "v1", height: 10 } }, frames), null);
  assert.equal(m.acceptVizMessage({ source: frameA, data: { type: "ark-viz-size", id: "v1", height: "300" } }, frames), null);
  assert.equal(m.acceptVizMessage({ source: frameA, data: { type: "ark-viz-size", id: "v1", height: NaN } }, frames), null);
  assert.equal(m.acceptVizMessage({ source: frameA, data: { type: "ark-viz-size", id: "v1", height: Infinity } }, frames), null);
  assert.equal(m.acceptVizMessage({ source: frameA, data: "ark-viz-size" }, frames), null);
  assert.equal(m.acceptVizMessage(null, frames), null);
  assert.equal(m.acceptVizMessage({ source: frameA, data: { type: "ark-viz-size", id: "v1", height: 1e9 } }, frames).height, m.VIZ_MAX_HEIGHT);
  assert.equal(m.acceptVizMessage({ source: frameA, data: { type: "ark-viz-size", id: "v1", height: -5 } }, frames).height, m.VIZ_MIN_HEIGHT);
  const msg = m.vizRenderMessage("v1", "x".repeat(m.VIZ_MAX_SOURCE + 10), "dark");
  assert.equal(msg.type, "ark-viz-render");
  assert.equal(msg.html.length, m.VIZ_MAX_SOURCE);
  assert.equal(msg.theme, "dark");
  assert.equal(m.vizRenderMessage("v", "", "evil").theme, "light");
});

test("desktop app links and local file paths", () => {
  assert.deepEqual(m.resolveRichUrl("vscode://file/E:/repo/tasks/T-1"), { kind: "app", href: "vscode://file/E:/repo/tasks/T-1" });
  assert.equal(m.resolveRichUrl("cursor://file/x").kind, "app");
  assert.equal(m.resolveRichUrl("obsidian://open?vault=a").kind, "app");
  assert.equal(m.resolveRichUrl("javascript:alert(1)"), null);
  assert.equal(m.resolveRichUrl("data:text/html,x"), null);
  assert.equal(m.resolveRichUrl("vscodex://evil"), null);
  assert.equal(m.resolveRichUrl("file:///C:/Research/a%20b").kind, "file");
  assert.equal(m.fileUrlToPath("file:///C:/Research/a%20b"), "C:/Research/a b");
  assert.equal(m.fileUrlToPath("file:///home/u/x"), "/home/u/x");
  assert.equal(m.fileUrlToPath("file://server/share/x"), "server/share/x");
  for (const host of ["localhost", "127.0.0.1", "127.1.2.3", "[::1]", "::1"]) assert.equal(m.isLoopbackHost(host), true, host);
  for (const host of ["192.168.1.20", "192.0.2.20", "ark.example.com", ""]) assert.equal(m.isLoopbackHost(host), false, host);
});
