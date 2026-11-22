// "Move to another project": request bodies, preview rows, targets, the
// [[prj_xxx/UID]] reference syntax and its strings in both languages.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import vm from "node:vm";

import {
  clusterOfRoot, crossProjectRows, formatBytes, mergeRequest, moveTargets, parseCrossRef, planRows, splitCandidates, splitRequest,
} from "../../ark_studio/web/js/project-move-model.js";
import { EN } from "../../ark_studio/web/js/i18n-en.js";
import { ZH } from "../../ark_studio/web/js/i18n-zh.js";

// markdown.js imports the vendored Marked ESM build, which Node treats as
// CommonJS: load it as source text modules (as markdown.test.mjs does).
const context = vm.createContext({});
const modules = new Map();
async function load(url) {
  if (modules.has(url.href)) return modules.get(url.href);
  const module = new vm.SourceTextModule(await readFile(url, "utf8"), { context, identifier: url.href });
  modules.set(url.href, module);
  await module.link((specifier, parent) => load(new URL(specifier, parent.identifier)));
  return module;
}
const markdown = await load(new URL("../../ark_studio/web/js/markdown.js", import.meta.url));
await markdown.evaluate();
const { markdownInlineTokens } = markdown.namespace;

test("cross-project references parse and tokenize", () => {
  assert.deepEqual(parseCrossRef("prj_4ff3b6973d/T-0042"), { project: "prj_4ff3b6973d", uid: "T-0042" });
  assert.equal(parseCrossRef("T-0042"), null);
  assert.equal(parseCrossRef("prj_x/../T-1"), null);
  const tokens = markdownInlineTokens("see [[prj_ab12/T-0042]] and [[T-0001]]");
  const refs = tokens.filter((tk) => tk.type === "arkReference").map((tk) => tk.text);
  assert.equal(refs.join(" "), "prj_ab12/T-0042 T-0001"); // the vm realm has its own Array
});

test("request bodies for a split and a merge", () => {
  assert.deepEqual(splitRequest({ roots: ["T-0001", "S-0002"], newName: "  Split " }),
    { mode: "split", units: ["T-0001", "S-0002"], cross_links: "keep", new_project: { name: "Split" } });
  assert.deepEqual(splitRequest({ roots: ["T-0001"], newName: "x", crossLinks: "drop", planHash: "abc" }).cross_links, "drop");
  assert.deepEqual(mergeRequest({ target: "prj_b", planHash: "h" }), { mode: "merge", target_project: "prj_b", plan_hash: "h" });
});

test("split candidates: separable groups first, no one-unit leftovers", () => {
  const data = { units: 315, clusters: [
    { units: 130, cut_links: 85, hub_links: 0, roots: [{ uid: "S-0001" }] },
    { units: 128, cut_links: 2, hub_links: 0, roots: [{ uid: "S-0002" }, { uid: "M-0020" }] },
    { units: 30, cut_links: 75, hub_links: 0, roots: [{ uid: "E-0039" }] },
    { units: 5, cut_links: 1, hub_links: 1, roots: [{ uid: "E-0012" }] },
    { units: 1, cut_links: 0, hub_links: 0, roots: [{ uid: "N-0001" }] },
  ] };
  const out = splitCandidates(data);
  assert.deepEqual(out.map((c) => c.roots[0].uid), ["S-0002", "E-0012", "S-0001", "E-0039"]);
  assert.deepEqual(out.map((c) => c.weak), [true, true, false, false]);
  assert.equal(clusterOfRoot(out, "M-0020").index, 1);
  assert.equal(clusterOfRoot(out, "X-1"), null);
});

test("targets leave out the current project and the demo", () => {
  const list = [{ id: "a", name: "A" }, { id: "b", name: "B" }, { id: "demo", name: "Demo", demo: true }];
  assert.deepEqual(moveTargets(list, "a"), [{ id: "b", name: "B" }]);
});

test("preview rows show notes only when they apply", () => {
  const plan = {
    unit_count: 3, renumbered: 1, links: 2, media: 2, files: 2, bytes: 2048, comments: 1, media_borrowed: 1,
    units: [{ uid: "T-0001", new_uid: "T-0003" }, { uid: "N-0001", new_uid: "N-0001" }], cross_links: [{ uid: "T-0001" }],
  };
  const out = planRows(plan);
  assert.equal(out.rows[1][0], "project_move.sum_renumber");
  assert.deepEqual(out.notes.map((n) => n[0]), ["project_move.note_borrowed"]);
  assert.equal(out.renamed.length, 1);
  assert.equal(out.cross.length, 1);
  assert.equal(formatBytes(2048), "2.0 KB");
  assert.equal(formatBytes(5), "5 B");
});

test("every move string exists in English and Chinese", () => {
  const keys = Object.keys(EN).filter((k) => k.startsWith("project_move.")).concat("refs.other_project");
  assert.ok(keys.length > 20);
  for (const key of keys) assert.ok(ZH[key], `missing zh: ${key}`);
  const zhKeys = Object.keys(ZH).filter((k) => k.startsWith("project_move."));
  assert.equal(zhKeys.length, keys.length - 1);
  for (const key of zhKeys) assert.ok(EN[key], `missing en: ${key}`);
});

test("node details: moved, imported and cross-project link rows", () => {
  assert.deepEqual(crossProjectRows(null), []);
  const rows = crossProjectRows({
    moved_from: { project: "prj_a", project_name: "A", uid: "T-0001", at: "x" },
    imported_from: { project: "prj_b", uid: "I-0002" },
    external_links: [{ project: "prj_a", project_name: "A", uid: "T-0009", relation: "supports", direction: "in", title: "t" }, { relation: "x" }],
  });
  assert.deepEqual(rows.map((r) => [r.kind, r.project, r.uid, r.name]),
    [["moved_from", "prj_a", "T-0001", "A"], ["imported_from", "prj_b", "I-0002", "prj_b"], ["link", "prj_a", "T-0009", "A"]]);
  assert.equal(rows[2].direction, "in");
  for (const key of ["cross_project.title", "cross_project.moved_from", "cross_project.moved_to", "cross_project.imported_from",
    "cross_project.link_out", "cross_project.link_in", "project_move.plan_import"]) {
    assert.ok(EN[key] && ZH[key], key);
  }
});

