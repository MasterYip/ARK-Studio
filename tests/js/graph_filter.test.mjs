import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
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
const module = await load(new URL("../../web/js/graph-filter.js", import.meta.url));
await module.evaluate();
const { GraphFilterIndex } = module.namespace;
const node = (id, extra = {}) => ({ id, uid: `N-${id}`, title: `Node ${id}`, type: "claim", status: "open",
  importance: "p2", created_at: "2026-01-01", updated_at: "2026-01-01", tags: [], ...extra });

test("relaxing a filter immediately restores nodes beyond the original 700", () => {
  const index = new GraphFilterIndex({ nodes: Array.from({length: 2000}, (_, i) => node(i + 1, { tags: [`batch:${i % 2}`] })) });
  assert.equal(index.filter({ tags: ["batch:0"], limit: 700 }).nodes.length, 700);
  assert.equal(index.filter({ tags: ["batch:1"], limit: 2000 }).nodes.length, 1000);
  assert.equal(index.filter({ limit: 2000 }).nodes.length, 2000);
});

test("list ranking can request every match while canvas limits remain unchanged", () => {
  const index = new GraphFilterIndex({ nodes: Array.from({ length: 10050 }, (_, i) => node(i + 1, { tags: [`batch:${i % 2}`] })) });
  assert.equal(index.filter({ limit: 10000 }).nodes.length, 10000);
  assert.equal(index.filter({ limit: 600 }, "", { all: true }).nodes.length, 10050);
  assert.equal(index.filter({ tags: ["batch:1"] }, "", { all: true }).nodes.length, 5025);
  assert.equal(index.filter({ limit: 600 }).nodes.length, 600);
});

test("context adds exactly one hop, even when edges are ordered as a chain", () => {
  const index = new GraphFilterIndex({ nodes: [node(1), node(2), node(3)],
    edges: [{source:1,target:2}, {source:2,target:3}] });
  const result = index.filter({ q: "N-1", context: 1 });
  assert.deepEqual(Array.from(result.nodes, n => n.id), [1, 2]);
  assert.equal(result.context_count, 1);
  assert.equal(result.nodes[1].context, true);
});

test("tag namespaces use OR within a namespace and AND across namespaces, including unset", () => {
  const index = new GraphFilterIndex({nodes:[node(1,{tags:['topic:a','batch:1']}), node(2,{tags:['topic:b']}), node(3,{tags:['batch:1']})]});
  const ids = query => Array.from(index.filter(query).nodes, n=>n.id).sort();
  assert.deepEqual(ids({tags:['topic:a','topic:b']}), [1,2]);
  assert.deepEqual(ids({tags:['topic:a','batch:1'], tags_unset:['topic']}), [1,3]);
});

test("full-body search and SQLite LIKE wildcards work beyond the excerpt", () => {
  const index = new GraphFilterIndex({nodes:[node(1,{search_body:'x'.repeat(500)+' Hidden_AZ'}),node(2)]});
  assert.equal(index.filter({q:'hidden_a%'}).nodes.length, 1);
  assert.equal(index.filter({q:'no match'}).nodes.length, 0);
});

test("date, type/origin, inherited assignee and empty selections match API semantics", () => {
  const index = new GraphFilterIndex({nodes:[node(1,{start_at:'2026-03-01',end_at:'2026-05-01',assignee_effective:'alice'}),node(2,{type:'idea',origin:'infohub',start_at:'2026-04-02'})]});
  assert.equal(index.filter({from:'2026-04-01',to:'2026-04-30'}).nodes[0].id, 2);
  assert.equal(index.filter({types:['task'],origins:'infohub'}).nodes[0].id, 2);
  assert.equal(index.filter({assignee:['me']}, 'alice').nodes[0].id, 1);
  assert.equal(index.filter({types_none:'1'}).nodes.length, 0);
});
