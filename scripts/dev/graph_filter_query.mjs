// Node harness for SQL parity and repeatable filter-engine benchmarks.
import { readFile } from "node:fs/promises";
import { performance } from "node:perf_hooks";
import vm from "node:vm";

const input = JSON.parse(await readFile(process.argv[2], "utf8"));
const context = vm.createContext({});
const cache = new Map();
async function load(url) {
  if (cache.has(url.href)) return cache.get(url.href);
  const module = new vm.SourceTextModule(await readFile(url, "utf8"), { context, identifier:url.href });
  cache.set(url.href, module);
  await module.link((specifier, parent) => load(new URL(specifier, parent.identifier)));
  return module;
}
const module = await load(new URL("../../ark_studio/web/js/graph-filter.js", import.meta.url));
await module.evaluate();
const start = performance.now();
const index = new module.namespace.GraphFilterIndex(input.snapshot);
const build_ms = performance.now() - start;
const summary = process.argv.includes('--summary');
const results = input.queries.map(query => {
  const begin = performance.now();
  const result = index.filter(query, input.me || "");
  return { ...(summary ? {nodes:result.nodes.length,edge_count:result.edges.length}
    : {ids:result.nodes.map(n=>n.id),edges:result.edges.map(e=>e.id)}), context:result.context_count,
    total:result.total,ms:performance.now()-begin};
});
console.log(JSON.stringify({build_ms, results}));
