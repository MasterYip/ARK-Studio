// Run: node --test tests/js/graph_mode_memory.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";

const web = new URL("../../ark_studio/web/js/", import.meta.url);

test("Read/Edit is one remembered choice for all layouts, Read by default", async () => {
  const graph = await readFile(new URL("graph.js", web), "utf8");
  assert.doesNotMatch(graph, /ark\.graphMode\.\$\{/, "no per-layout mode keys");
  assert.match(graph, /return saved === "edit" \? "edit" : "read";/);
  assert.doesNotMatch(graph, /layout === "mind" \? "edit" : "read"/, "Mind no longer defaults to Edit");
});

test("Node details follows the remembered mode, Read without a graph", async () => {
  const src = await readFile(new URL("inspector.js", web), "utf8");
  assert.match(src, /const mode = ctx\.S\.graphMode \|\| \(graph \? graph\.mode : savedMode\);/);
  assert.match(src, /readOnly = options\.readOnly \?\? \(mode !== "edit"\)/);
  const app = await readFile(new URL("app.js", web), "utf8");
  assert.match(app, /localStorage\.setItem\("ark\.graphMode", S\.graphMode\)/);
});
