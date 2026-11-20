// Ideas › Import from another ARK project: the source picker, the mapping
// of ARK's filter model onto the source graph request, the preview rows,
// the [[prj_xxx/UID]] references and the strings in both languages.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import vm from "node:vm";

import { EN } from "../../ark_studio/web/js/i18n-en.js";
import { ZH } from "../../ark_studio/web/js/i18n-zh.js";


const context = vm.createContext({});
const modules = new Map();
async function load(url) {
  if (modules.has(url.href)) return modules.get(url.href);
  const module = new vm.SourceTextModule(await readFile(url, "utf8"), { context, identifier: url.href });
  modules.set(url.href, module);
  await module.link((specifier, parent) => load(new URL(specifier, parent.identifier)));
  return module;
}
// The model imports state.js (the filter model), which reads a few browser
// globals when it loads: run it in its own context, as filter_store.test does.
const browser = vm.createContext({ URLSearchParams, location: { search: "" },
  localStorage: { getItem() { return null; }, setItem() {}, removeItem() {} }, document: { documentElement: { dataset: {} } } });
const loaded = new Map();
async function loadIn(url) {
  if (loaded.has(url.href)) return loaded.get(url.href);
  const module = new vm.SourceTextModule(await readFile(url, "utf8"), { context: browser, identifier: url.href });
  loaded.set(url.href, module);
  await module.link((specifier, parent) => {
    // As in filter_store.test: no theme or language machinery.
    if (specifier === "./appearance.js") return new vm.SyntheticModule(["applyAppearance"], function () {
      this.setExport("applyAppearance", () => {});
    }, { context: browser });
    if (specifier === "./i18n.js") return new vm.SyntheticModule(["getLang", "setLang"], function () {
      this.setExport("getLang", () => "en");
      this.setExport("setLang", (lang) => lang);
    }, { context: browser });
    return loadIn(new URL(specifier, parent.identifier));
  });
  return module;
}
const model = await loadIn(new URL("../../ark_studio/web/js/import-ark-model.js", import.meta.url));
await model.evaluate();
const stateModule = await loadIn(new URL("../../ark_studio/web/js/state.js", import.meta.url));
const {
  INFOHUB_SOURCE, importSources, pickImportSource, isArkSource, importGraphParams, importRequest, importSummaryRows, parseCrossRef,
  markImported, IMPORTED_COLOR,
} = model.namespace;
const { emptyFilters } = stateModule.namespace;
// Values from the vm realm compare by JSON (another realm's Array and Object).
const same = (a, b) => assert.equal(JSON.stringify(a), JSON.stringify(b));
const markdown = await load(new URL("../../ark_studio/web/js/markdown.js", import.meta.url));
await markdown.evaluate();
const { markdownInlineTokens } = markdown.namespace;

test("source picker: InfoHub first when shown, other projects, no demo or current", () => {
  const projects = [{ id: "prj_a", name: "A" }, { id: "prj_b", name: "B" }, { id: "ark-demo", name: "Demo", demo: true }];
  same(importSources(projects, "prj_a").map((s) => s.id), [INFOHUB_SOURCE, "prj_b"]);
  same(importSources(projects, "prj_a", { infohub: false }).map((s) => s.id), ["prj_b"]);
  const sources = importSources(projects, "prj_a");
  assert.equal(pickImportSource("prj_b", sources), "prj_b");
  assert.equal(pickImportSource("prj_gone", sources), INFOHUB_SOURCE);
  assert.equal(pickImportSource("", []), "");
  assert.equal(isArkSource("prj_b"), true);
  assert.equal(isArkSource(INFOHUB_SOURCE), false);
  assert.equal(isArkSource(""), false);
});

test("ARK filters map onto the source graph request", () => {
  same(importGraphParams("prj_b", emptyFilters()), { project: "prj_b", limit: 1000 });
  const f = { ...emptyFilters(), q: "tokenizer", types: ["idea", "task"], statuses: ["open"], importanceLevels: ["p1"],
    tags: ["topic:nlp"], assignees: ["me"], timeMode: "overdue", context: true, roots: true };
  const p = importGraphParams("prj_b", f);
  assert.equal(p.project, "prj_b");
  assert.equal(p.q, "tokenizer");
  same(p.types, ["idea", "task"]);
  same(p.statuses, ["open"]);
  same(p.importance, ["p1"]);
  same(p.tags, ["topic:nlp"]);
  same(p.assignee, ["me"]);
  assert.equal(p.overdue, "1");
  assert.equal(p.roots, "1");
  assert.equal(p.context, "1");
});

test("request body and preview rows", () => {
  same(importRequest({ project: "prj_b", uids: ["I-0001", "I-0001", "", "T-0002"], withChildren: 1, lang: "zh" }),
    { project: "prj_b", units: ["I-0001", "T-0002"], with_children: true, lang: "zh" });
  const keys = (s) => importSummaryRows(s).map((r) => r[0]);
  same(keys({ unit_count: 2 }), ["import_ark.sum_units", "import_ark.sum_keep"]);
  same(keys({ unit_count: 2, renumbered: 2, links: 1, media: 1, bytes: 2048, skipped: 1, files_missing: 1 }),
    ["import_ark.sum_units", "import_ark.sum_renumber", "import_ark.sum_links", "import_ark.sum_media", "import_ark.sum_skipped", "import_ark.sum_missing"]);
  assert.equal(importSummaryRows({ media: 1, bytes: 2048 })[2][1].size, "2.0 KB");
});

test("cross-project references parse and render as references", () => {
  same(parseCrossRef("prj_0a1b2c3d4e/T-0042"), { project: "prj_0a1b2c3d4e", uid: "T-0042" });
  assert.equal(parseCrossRef("T-0042"), null);
  const refs = markdownInlineTokens("from [[prj_ab12/I-0003]] and [[T-0001]]").filter((tk) => tk.type === "arkReference");
  assert.equal(refs.map((tk) => tk.text).join(" "), "prj_ab12/I-0003 T-0001"); // the vm realm has its own Array
});

test("every import string exists in English and Chinese", () => {
  const keys = Object.keys(EN).filter((k) => k.startsWith("import_ark.")).concat("refs.other_project", "ideas.import_toggle_title");
  assert.ok(keys.length > 20);
  for (const key of keys) assert.ok(ZH[key], `missing zh: ${key}`);
  for (const key of Object.keys(ZH).filter((k) => k.startsWith("import_ark."))) assert.ok(EN[key], `missing en: ${key}`);
  assert.equal(ZH["ideas.import"], "导入");
  assert.equal(EN["ideas.import"], "Import");
});

test("source nodes with a copy here are greyed and name the copy; others stay as they are", () => {
  const nodes = [{ uid: "I-0001", title: "a" }, { uid: "I-0002", title: "b", customColor: "#123" }];
  const out = markImported(nodes, { "I-0002": "I-0007" });
  assert.equal(out.count, 1);
  assert.equal(out.nodes[0], nodes[0]);
  assert.equal(out.nodes[1].importedAs, "I-0007");
  assert.equal(out.nodes[1].customColor, IMPORTED_COLOR);
  assert.equal(nodes[1].customColor, "#123", "input not mutated");
  assert.equal(markImported(nodes, null).count, 0);
});

