// Run: node --experimental-vm-modules --test tests/js/launch_start.test.mjs
// The Run dialog's "Start" option (T-0121): the options are the Home prompt
// library (pins first, built-ins, custom prompts) plus "free"; the default
// follows the server's facts; the preview line; the prompt text sent to the
// session is the library's, with {unit} filled.
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
    if (!["session-context-model.js", "starter-prompts.js"].includes(dep)) throw new Error(`${name} imports ${specifier}`);
    return loadModule(dep);
  });
  return module;
}
const lib = await loadModule("prompt-library-model.js");
await lib.evaluate();
const L = lib.namespace;
const P = MODULES.get("starter-prompts.js").namespace;
const dlg = await loadModule("launch-dialog-model.js");
await dlg.evaluate();
const D = dlg.namespace;

const state = {
  personal: [{ id: "p_mine", title: "Weekly figures", body: "Redo the figures of {unit} for {project}." }],
  project: [{ id: "p_team", title: "Team review", body: "Review the open results." }],
  pins: ["review-backlog", "p_mine", "setup"],
};
const entries = L.libraryEntries(state).map((e) => ({ ...e, title: L.entryTitle(e, "zh") }));
const options = D.startOptions(entries, L.effectivePins(state.pins));

test("options: the prompt library, pinned first, then built-ins, custom prompts and free", () => {
  const ids = options.map((o) => o.id);
  assert.deepEqual(ids.slice(0, 3), ["review-backlog", "p_mine", "setup"]);
  assert.equal(ids.at(-1), D.START_FREE);
  const builtins = P.BUILTIN_PROMPTS.map((p) => p.id);
  assert.deepEqual(new Set(ids.slice(0, -1)), new Set([...builtins, "p_mine", "p_team"]), "every library entry once");
  assert.equal(ids.length, builtins.length + 3);
  const afterPins = ids.slice(3, -1);
  assert.deepEqual(afterPins.slice(-1), ["p_team"], "custom prompts after the built-ins");
  for (const id of ["plan-idea", "continue", "resume", "status", "organize"]) assert.ok(ids.includes(id), id);
  assert.equal(options.find((o) => o.id === "continue").unit, true);
  assert.equal(options.find((o) => o.id === "plan-idea").title, "规划想法");
  // Default pins when none were chosen.
  assert.deepEqual(D.startOptions(entries, L.effectivePins(null)).slice(0, 2).map((o) => o.id), P.DEFAULT_PINS);
});

test("default: setup when missing, continue with a task, else the remembered choice, else free", () => {
  const def = (server, task = "", remembered = "") => D.defaultStartMode({ server, task, remembered, options });
  assert.equal(def({ mode: "setup" }, "T-0042", "status"), "setup");
  assert.equal(def({ mode: "device-setup" }, "T-0042"), "device-setup");
  assert.equal(def({ mode: "continue" }, "T-0042", "status"), "continue");
  assert.equal(def({ mode: "free" }, "", "status"), "status");
  assert.equal(def({ mode: "free" }, "", "continue"), D.START_FREE, "continue is not remembered without a task");
  assert.equal(def({ mode: "free" }, "", "setup"), D.START_FREE, "setup follows the facts");
  assert.equal(def(null, "", "gone-prompt"), D.START_FREE);
  assert.equal(def(null, ""), D.START_FREE);
});

test("preview line", () => {
  const t = (key, vars = {}) => `${key}${Object.keys(vars).length ? JSON.stringify(vars) : ""}`;
  const server = { mode: "continue", task: { uid: "T-0042", send_backs: 1, open_comments: 2 }, setup: { needs_location: ["a", "b"] } };
  assert.equal(D.startPreview({ mode: "continue", task: "T-0042", server, t }),
    'tasks.launcher.start_preview.continue_sendback{"uid":"T-0042","n":1}');
  assert.equal(D.startPreview({ mode: "continue", task: "T-0043", server, t }), 'tasks.launcher.start_preview.continue{"uid":"T-0043"}');
  assert.equal(D.startPreview({ mode: "continue", task: "", t }), "tasks.launcher.start_preview.continue_pick");
  assert.equal(D.startPreview({ mode: "setup", t }), "tasks.launcher.start_preview.setup");
  assert.equal(D.startPreview({ mode: "device-setup", server, t }), 'tasks.launcher.start_preview.device{"n":2}');
  assert.equal(D.startPreview({ mode: D.START_FREE, t }), "tasks.launcher.start_preview.wait");
  assert.equal(D.startPreview({ mode: D.START_FREE, prompt: "fix it", t }), "tasks.launcher.start_preview.follow");
  const opt = options.find((o) => o.id === "plan-idea");
  assert.equal(D.startPreview({ mode: "plan-idea", option: opt, t }), 'tasks.launcher.start_preview.prompt{"title":"规划想法"}');
});

test("the session gets the library text with placeholders filled, without rules or token", () => {
  const project = { id: "prj_1", name: "Demo" };
  const cont = entries.find((e) => e.id === "continue");
  const text = P.sessionStartText(cont, project, { lang: "en", unit: "T-0042" });
  assert.match(text, /^TASK: continue T-0042\. GET \/api\/agent\/v1\/units\/T-0042/);
  assert.doesNotMatch(text, /\{\{|CORE RULES|arka_/);
  // The Home copy contains the same text.
  assert.ok(P.libraryPromptText(cont, project, { lang: "en", unit: "T-0042", token: "arka_x" }).includes(text));
  const mine = entries.find((e) => e.id === "p_mine");
  assert.equal(P.sessionStartText(mine, project, { lang: "en", unit: "T-0042" }),
    "TASK (Weekly figures):\nRedo the figures of T-0042 for Demo (prj_1).");
  const req = D.startRequest(options.find((o) => o.id === "continue"), text);
  assert.deepEqual(req, { id: "continue", title: "继续某个任务", text });
  assert.deepEqual(D.startRequest(options.at(-1), "ignored"), { id: D.START_FREE });
});

test("the first-message placeholder follows the start", () => {
  const t = (key, vars = {}) => `${key}${Object.keys(vars).length ? JSON.stringify(vars) : ""}`;
  assert.equal(D.startPlaceholder({ mode: "setup", t }), "tasks.launcher.start_placeholder.setup");
  assert.equal(D.startPlaceholder({ mode: "device-setup", t }), "tasks.launcher.start_placeholder.device");
  assert.equal(D.startPlaceholder({ mode: "continue", task: "T-0001", t }), 'tasks.launcher.start_placeholder.continue{"uid":"T-0001"}');
  assert.equal(D.startPlaceholder({ mode: "continue", t }), "tasks.launcher.start_preview.continue_pick");
  assert.equal(D.startPlaceholder({ mode: D.START_FREE, t }), "tasks.launcher.start_placeholder.free");
  assert.equal(D.startPlaceholder({ mode: "status", option: { title: "Check" }, t }), 'tasks.launcher.start_placeholder.prompt{"title":"Check"}');
});

test("the choice is remembered per person and project", () => {
  const mem = new Map();
  const store = { getItem: (k) => mem.get(k) ?? null, setItem: (k, v) => mem.set(k, v) };
  D.saveLaunchDefaults(store, "ann", "prj_1", { platform: "claude", start: "plan-idea" });
  assert.equal(D.loadLaunchDefaults(store, "ann", "prj_1").start, "plan-idea");
  assert.equal(D.loadLaunchDefaults(store, "ann", "prj_2").start, undefined);
});
