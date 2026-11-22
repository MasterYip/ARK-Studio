// Run: node --experimental-vm-modules --test tests/js/prompt_library.test.mjs
// Home → More prompts: the built-in library, pins and their defaults,
// custom prompts with placeholders, export / import, and the UI strings.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

const WEB = new URL("../../ark_studio/web/", import.meta.url);
const MODULES = new Map();
async function loadModule(name) {
  if (MODULES.has(name)) return MODULES.get(name);
  const source = await readFile(new URL(`js/${name}`, WEB), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: name });
  MODULES.set(name, module);
  await module.link(async (specifier) => {
    const dep = specifier.replace(/^\.\//, "");
    if (!["session-context-model.js", "starter-prompts.js"].includes(dep)) throw new Error(`${name} must stay DOM-free (imports ${specifier})`);
    return loadModule(dep);
  });
  return module;
}
const model = await loadModule("prompt-library-model.js");
await model.evaluate();
const L = model.namespace;
const P = MODULES.get("starter-prompts.js").namespace;
const read = (path) => readFile(new URL(path, WEB), "utf8");

const PROJECT = { id: "prj_1", name: "Demo", settings: {} };
const STATE = {
  personal: [{ id: "cp-0000aaaa", title: "Weekly digest", description: "Mine", body: "Summarise {project} for {user}; look at {unit}. POST {\"body\": \"x\"} stays." }],
  project: [{ id: "cp-0000bbbb", title: "Team review", description: "Ours", body: "Review the work of {project}." }],
};

test("the library has the starter prompts and the built-in jobs, in both languages", () => {
  const ids = P.BUILTIN_PROMPTS.map((p) => p.id);
  for (const id of ["setup", "team", "organize", "review-backlog", "continue", "plan-idea", "status", "resume", "device-setup", "add-prompt"]) {
    assert.ok(ids.includes(id), id);
  }
  for (const item of P.BUILTIN_PROMPTS) {
    for (const key of ["title", "desc"]) {
      assert.ok(item[key]?.en && /[一-鿿]/.test(item[key].zh), `${item.id}.${key}`);
    }
    // Short: a prompt adds at most 8 lines to the shared part.
    const lines = Object.values(item.sections).flat().length;
    assert.ok(item.id === "setup" || item.id === "team" || lines <= 8, `${item.id}: ${lines} lines`);
  }
  assert.deepEqual(P.DEFAULT_PINS, ["setup", "team"]);
});

test("every library prompt carries the shared part and the core rules", () => {
  const rules = { version: "r-test0001", lines: { en: ["CORE RULES (rules r-test0001): x", "C1. y"], zh: ["核心规则（规则 r-test0001）：x", "C1. y"] } };
  for (const lang of ["en", "zh"]) {
    for (const item of P.BUILTIN_PROMPTS) {
      const text = P.libraryPromptText(item, PROJECT, { url: "http://ark:9", token: "arka_t", sessionId: "ark-ses-abcdefghij", lang, rules });
      assert.ok(text.includes("arka_t") && text.includes("r-test0001") && text.includes("/api/agent/v1/docs"), `${item.id} ${lang}`);
      assert.ok(text.indexOf("r-test0001") < text.indexOf(P.pickLine(Object.values(item.sections)[0][0], lang).slice(0, 5)), item.id);
      assert.ok(text.includes("ark-ses-abcdefghij"), `${item.id} session block`);
      assert.doesNotMatch(text, /\{\{\w+\}\}/, `${item.id} unfilled`);
    }
  }
  const organize = P.libraryPromptText(P.BUILTIN_PROMPTS.find((p) => p.id === "organize"), PROJECT, { lang: "en" });
  assert.match(organize, /GET \/api\/agent\/v1\/docs\/agents\/project-content\.md/);
  assert.match(organize, /1\. Inspect[\s\S]*2\. Plan[\s\S]*approval[\s\S]*3\. Apply[\s\S]*4\. Report/);
  const cont = P.BUILTIN_PROMPTS.find((p) => p.id === "continue");
  assert.match(P.libraryPromptText(cont, PROJECT, { lang: "en", unit: "T-0042" }), /continue T-0042\. GET \/api\/agent\/v1\/units\/T-0042 /);
  assert.match(P.libraryPromptText(cont, PROJECT, { lang: "zh" }), /继续 <任务 ID>/);
  assert.match(P.libraryPromptText(P.BUILTIN_PROMPTS.find((p) => p.id === "add-prompt"), PROJECT, { lang: "en" }),
    /POST \/api\/agent\/v1\/prompts [\s\S]*personal by default[\s\S]*"scope": "project"/);
  // The old entry point still renders the starter prompts.
  assert.equal(P.starterPromptText("team", PROJECT, { lang: "en" }), P.libraryPromptText(P.STARTER_PROMPTS[1], PROJECT, { lang: "en" }));
});

test("custom prompts: placeholders filled, other braces kept, rules first", () => {
  const entries = L.libraryEntries(STATE);
  const mine = entries.find((e) => e.id === "cp-0000aaaa");
  assert.equal(mine.scope, "personal");
  assert.equal(mine.unit, true);
  assert.equal(entries.find((e) => e.id === "cp-0000bbbb").scope, "project");
  const text = P.libraryPromptText(mine, PROJECT, { lang: "en", unit: "T-0007", user: "ana" });
  assert.match(text, /TASK \(Weekly digest\):\nSummarise Demo \(prj_1\) for ana; look at T-0007\. POST \{"body": "x"\} stays\./);
  assert.ok(text.indexOf("CORE RULES") < text.indexOf("TASK (Weekly digest)"));
  assert.match(P.libraryPromptText(mine, PROJECT, { lang: "zh" }), /任务（Weekly digest）：\n[\s\S]*look at <任务 ID>/);
  assert.deepEqual(L.unknownPlaceholders("a {project} {when} {when} {unit}"), ["when"]);
  assert.equal(L.entryTitle(P.BUILTIN_PROMPTS[0], "zh"), "配置当前环境");
  assert.equal(L.entryTitle(mine, "zh"), "Weekly digest");
});

test("pins: defaults, order, toggle, prune, limit", () => {
  const entries = L.libraryEntries(STATE);
  assert.deepEqual(L.pinnedEntries(null, entries).map((e) => e.id), ["setup", "team"]);
  assert.deepEqual(L.pinnedEntries([], entries), []);
  let pins = L.togglePin(null, "cp-0000aaaa");
  assert.deepEqual(pins, ["setup", "team", "cp-0000aaaa"]);
  pins = L.movePin(pins, "cp-0000aaaa", -1);
  assert.deepEqual(pins, ["setup", "cp-0000aaaa", "team"]);
  assert.deepEqual(L.movePin(pins, "setup", -1), pins, "first stays first");
  pins = L.togglePin(pins, "setup");
  assert.deepEqual(L.pinnedEntries(pins, entries).map((e) => e.id), ["cp-0000aaaa", "team"]);
  assert.deepEqual(L.pinnedEntries(["gone", "team"], entries).map((e) => e.id), ["team"]);
  assert.deepEqual(L.prunePins(["gone", "team"], entries), ["team"]);
  assert.equal(L.prunePins(null, entries), null);
  let many = [];
  for (let i = 0; i < 20; i++) many = L.togglePin(many, `p${i}`);
  assert.equal(many.length, L.MAX_PINS);
});

test("export and import round trip; bad files are refused", () => {
  const text = L.exportPrompts([...STATE.personal, ...STATE.project], { project: "prj_1" });
  const data = JSON.parse(text);
  assert.equal(data.format, "ark-prompts");
  assert.deepEqual(Object.keys(data.prompts[0]).sort(), ["body", "description", "title"]);
  assert.deepEqual(L.parseImport(text).map((p) => p.title), ["Weekly digest", "Team review"]);
  assert.equal(L.parseImport(JSON.stringify([{ title: "a", body: "b" }])).length, 1);
  for (const bad of ["nope", "{}", JSON.stringify({ format: "other", prompts: [{ title: "a", body: "b" }] }),
    JSON.stringify({ prompts: [{ title: "a" }] }), JSON.stringify({ prompts: [] })]) {
    assert.throws(() => L.parseImport(bad), bad);
  }
});

test("Home and the library dialog: pinned cards, More prompts, editor fields, strings in both languages", async () => {
  const home = await read("js/views/onboarding.js");
  assert.match(home, /pinnedEntries\(lib\.pins, libraryEntries\(lib\.state \|\| \{\}\)\)/);
  assert.match(home, /"data-more-prompts": ""/);
  assert.match(home, /api\.promptsPost\(\{ action: "pins", pins \}\)/);
  const dialog = await read("js/views/prompt-library.js");
  for (const attr of ["data-library-copy", "data-library-pin", "data-library-up", "data-library-down", "data-library-edit",
    "data-library-delete", "data-prompt-editor", "data-prompt-field"]) {
    assert.ok(dialog.includes(attr), attr);
  }
  // Project prompts are offered in the editor only to project admins.
  assert.match(dialog, /canProject \|\| entry\?\.scope === "project"/);
  const en = await read("js/i18n-en.js");
  const zh = await read("js/i18n-zh.js");
  const used = new Set();
  for (const src of [home, dialog]) {
    for (const [, key] of src.matchAll(/t\("(prompts\.[a-z_.]+)"/g)) used.add(key);
    for (const [, key] of src.matchAll(/"(prompts\.[a-z_.]+)"/g)) used.add(key);
  }
  for (const g of ["builtin", "personal", "project"]) used.add(`prompts.group.${g}`);
  for (const s of ["personal", "project"]) used.add(`prompts.scope.${s}`);
  assert.ok(used.size >= 30, String(used.size));
  for (const key of used) {
    assert.ok(en.includes(`"${key}"`), `en ${key}`);
    assert.ok(zh.includes(`"${key}"`), `zh ${key}`);
  }
  assert.match(zh, /"prompts\.more": "更多提示词"/);
});
