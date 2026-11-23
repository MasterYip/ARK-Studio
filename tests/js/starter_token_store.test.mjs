// Run: node --experimental-vm-modules --test tests/js/starter_token_store.test.mjs
// The server keeps the Home starter token (sealed) and Copy prompt asks it
// for the project's token; values older builds left in localStorage are
// removed, and the browser never stores one again.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { clearStarterTokens } from "../../ark_studio/web/js/starter-token-store.js";

function memoryStorage() {
  const data = new Map();
  return {
    get length() { return data.size; },
    key: (i) => [...data.keys()][i] ?? null,
    getItem: (k) => (data.has(k) ? data.get(k) : null),
    setItem: (k, v) => data.set(k, String(v)),
    removeItem: (k) => data.delete(k),
    data,
  };
}

test("stored starter values are removed per account or all at once", () => {
  const store = memoryStorage();
  store.setItem("ark.other", "keep");
  store.setItem("ark.starterToken:ann:prj_a", "{}");
  store.setItem("ark.starterToken:ann:prj_b", "{}");
  store.setItem("ark.starterToken:bob:prj_a", "{}");
  clearStarterTokens(store, "ann");
  assert.deepEqual([...store.data.keys()].sort(), ["ark.other", "ark.starterToken:bob:prj_a"]);
  clearStarterTokens(store);
  assert.deepEqual([...store.data.keys()], ["ark.other"]);
  clearStarterTokens(null); // storage disabled
});

test("the Copy flow asks the server for the project's token and stores nothing", () => {
  const src = readFileSync(new URL("../../ark_studio/web/js/views/agent-tokens.js", import.meta.url), "utf8");
  assert.match(src, /clearStarterTokens\(globalThis\.localStorage\)/);
  assert.doesNotMatch(src, /localStorage\.setItem|saveStarterToken/);
  const flow = src.slice(src.indexOf("async function openTokenFlow"));
  assert.match(flow, /mode: "peek"/);
  assert.match(flow, /run\("copy"\)/);
  assert.match(flow, /run\("new"\)/);
  assert.doesNotMatch(flow, /revoke_old|AGENT_TOKEN_LIMIT/);
  const api = readFileSync(new URL("../../ark_studio/web/js/api.js", import.meta.url), "utf8");
  assert.match(api, /starterToken: \(body\) => request\("\/api\/personal\/agent-tokens", \{ method: "POST", body: \{ purpose: "starter"/);
  assert.match(api.slice(api.indexOf("export async function clearClientData")), /clearStarterTokens\(localStorage\)/);
  const onboarding = readFileSync(new URL("../../ark_studio/web/js/views/onboarding.js", import.meta.url), "utf8");
  assert.match(onboarding, /result\?\.copied\) connection\.refresh\(\)/);
});
