// Run: node --experimental-vm-modules --test tests/js/session_context_model.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

async function loadModel() {
  const source = await readFile(new URL("../../ark_studio/web/js/session-context-model.js", import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: "session-context-model.js" });
  await module.link(() => { throw new Error("session-context-model must stay dependency-free"); });
  await module.evaluate();
  return module.namespace;
}

const model = await loadModel();

test("session ids are fresh, prefixed and match the server pattern", () => {
  const seen = new Set();
  for (let i = 0; i < 200; i += 1) {
    const id = model.newArkSessionId();
    assert.match(id, /^ark-ses-[a-z0-9]{10}$/);
    assert.ok(model.isArkSessionId(id));
    seen.add(id);
  }
  assert.equal(seen.size, 200);
  const fixed = model.newArkSessionId((n) => new Uint8Array(n));
  assert.equal(fixed, "ark-ses-aaaaaaaaaa");
  assert.equal(model.isArkSessionId("s_2026"), false);
  assert.equal(model.isArkSessionId("ark-ses-../x"), false);
});

test("bytes, links and link parsing", () => {
  assert.equal(model.formatBytes(0), "0 B");
  assert.equal(model.formatBytes(2048), "2.0 KB");
  assert.equal(model.formatBytes(5 * 1024 * 1024), "5.0 MB");
  assert.deepEqual(model.contextLinks({ task_ref: "T-1", linked_units: ["F-2", "T-1"] }), ["T-1", "F-2"]);
  assert.deepEqual(model.parseLinks("T-1, F-2;bad id! T-1"), ["T-1", "F-2", "bad"]);
});

test("filter by source and query", () => {
  const items = [
    { id: "ark-ses-aaa111", source: "external", agent: "claude", title: "Ablation", linked_units: ["T-9"] },
    { id: "s_1", source: "internal", agent: "codex", model: "gpt-x", title: "Fix bug", task_ref: "T-1" },
  ];
  assert.deepEqual(model.filterContexts(items, { source: "internal" }).map((i) => i.id), ["s_1"]);
  assert.deepEqual(model.filterContexts(items, { query: "t-9" }).map((i) => i.id), ["ark-ses-aaa111"]);
  assert.deepEqual(model.filterContexts(items, { query: "GPT" }).map((i) => i.id), ["s_1"]);
  assert.equal(model.filterContexts(items).length, 2);
});

test("continue options distinguish native resume from seeding", () => {
  assert.deepEqual(model.continueOptions({ source: "internal", resume: { native: true, command: "claude --resume x" } }),
    { nativeResume: true, command: "claude --resume x", seed: true, active: false });
  assert.equal(model.continueOptions({ source: "external", resume: { native: true, command: "codex resume y" } }).nativeResume, false);
  assert.equal(model.continueOptions({ source: "internal", active: true }).active, true);
  // Resuming in place is for the creator or an admin; others may only seed.
  assert.equal(model.continueOptions({ source: "internal", can_manage: false, resume: { native: true } }).nativeResume, false);
});

test("visibility label follows the shared flag", () => {
  assert.equal(model.visibilityKey({ shared: true }), "ctx.shared");
  assert.equal(model.visibilityKey({}), "ctx.private");
});

test("prompt block: uploads only offered when the project accepts them", () => {
  const off = model.sessionPromptBlock({ sessionId: "ark-ses-abcdefghij", url: "http://ark", projectId: "prj_1" });
  assert.match(off, /ark-ses-abcdefghij/);
  assert.match(off, /turned off/);
  assert.doesNotMatch(off, /session-watch|watcher/);
  const on = model.sessionPromptBlock({ sessionId: "ark-ses-abcdefghij", url: "http://ark", projectId: "prj_1", uploads: true });
  assert.match(on, /Ask me once/);
  assert.match(on, /If I say no, do not upload/);
  assert.match(on, /--id ark-ses-abcdefghij --server http:\/\/ark --project prj_1 --interval 300/);
  assert.match(on, /\/api\/agent\/v1\/sessions\/watcher\.py/);
  assert.match(on, /\/api\/agent\/v1\/sessions\/ark-ses-abcdefghij\/upload/);
  assert.doesNotMatch(on, /arka_/);
});
