// Run: node --experimental-vm-modules --test tests/js/launch_dialog_model.test.mjs
// The "Run an agent in ARK" dialog: providers offered, model / reasoning /
// access choices from provider data, the choice remembered per person and
// project, the task from context and the automatic title.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

async function load(name) {
  const source = await readFile(new URL(`../../ark_studio/web/js/${name}`, import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: name });
  await module.link(() => { throw new Error(`${name} must stay dependency-free`); });
  await module.evaluate();
  return module.namespace;
}

const m = await load("launch-dialog-model.js");
const sessionModel = await load("session-model.js");

function memoryStore() {
  const data = new Map();
  return { getItem: (key) => data.get(key) ?? null, setItem: (key, value) => data.set(key, String(value)), data };
}

const zh = {
  "tasks.launcher.model_default": "默认",
  "tasks.launcher.model_default_named": "默认（{model}）",
  "tasks.launcher.model_custom": "自定义…",
  "tasks.launcher.effort_default": "默认",
  "tasks.launcher.effort_default_named": "默认（{effort}）",
  "tasks.effort.high": "高",
  "tasks.effort.xhigh": "很高",
};
const t = (key, vars = {}) => (zh[key] || key).replace(/\{(\w+)\}/g, (_, name) => vars[name] ?? "");

// What GET /api/agent-sessions/presets returns (trimmed): Claude Code's
// efforts come from the catalog (provider_capabilities), not the preset.
const claude = {
  id: "claude", available: true, efforts: [], models: [], permission_modes: ["read_only", "plan", "full_access"],
  provider_status: "authenticated",
  agent_options: [{ id: "reviewer", label: "reviewer", description: "Reviews diffs", scope: "user" },
    { id: "tester", label: "tester", scope: "project" }],
  provider_capabilities: {
    default_model: "opus-5.5", default_effort: "high", efforts: ["low", "medium", "high", "xhigh", "max"],
    model_options: [
      { id: "opus-5.5", label: "opus-5.5", source: "detected", default: true, efforts: ["low", "medium", "high", "xhigh", "max"], default_effort: "high" },
      { id: "sonnet-5", label: "sonnet-5", source: "detected", efforts: ["low", "medium", "high", "xhigh", "max"] },
    ],
  },
};
const codex = {
  id: "codex", available: true, efforts: [], models: [], permission_modes: ["read_only", "plan", "full_access"],
  provider_status: "authenticated", agent_options: [],
  provider_capabilities: {
    default_model: "gpt-6", default_effort: "high", efforts: ["low", "medium", "high"],
    model_options: [
      { id: "gpt-6", label: "gpt-6", source: "detected", default: true, efforts: ["low", "medium", "high", "xhigh"] },
      { id: "gpt-5.5", label: "GPT-5.5", source: "detected", efforts: ["low", "medium", "high"] },
    ],
  },
};
const dsh = { id: "dsh", available: true, efforts: [], provider_status: "unauthenticated", provider_capabilities: { efforts: [] } };
const echo = { id: "echo", available: true, demo: true, efforts: [], permission_modes: ["read_only", "plan", "ask", "accept_edits", "full_access"] };
const off = { id: "gemini", available: false };
const presets = [echo, off, dsh, codex, claude];
const modelsFor = (p) => (p ? sessionModel.modelOptions(p, m.providerCapabilities(p)) : []);
const effortsFor = (p, model) => (p ? sessionModel.modelEfforts(p, m.providerCapabilities(p), model) : []);

test("only available providers, real agents first, signed-out and demo last", () => {
  assert.deepEqual(m.launchProviders(presets).map((p) => p.id), ["claude", "codex", "dsh", "echo"]);
});

test("Gemini CLI comes right after Claude Code and Codex, with no reasoning field", () => {
  const gemini = {
    id: "gemini", available: true, efforts: [], permission_modes: ["read_only", "plan", "full_access"],
    provider_capabilities: {
      default_model: "auto", efforts: [],
      model_options: [
        { id: "auto", label: "Let Gemini CLI decide the best model for the task", source: "detected", default: true },
        { id: "gemini-2.5-pro", label: "gemini-2.5-pro", source: "detected" },
      ],
    },
  };
  const shell = { id: "shell", available: true, efforts: [] };
  const opencode = { id: "opencode", available: true, efforts: [] };
  assert.deepEqual(m.launchProviders([shell, opencode, gemini, dsh, codex, claude]).map((p) => p.id),
    ["claude", "codex", "gemini", "opencode", "shell", "dsh"]);
  assert.deepEqual(effortsFor(gemini, ""), []);
  assert.deepEqual(m.accessChoices(gemini), ["read_only", "full_access"]);
  const rows = m.modelChoices(modelsFor(gemini), { t });
  assert.equal(rows[0].label, "默认（auto）");
  assert.ok(rows.some((row) => row.value === "gemini-2.5-pro"));
});

test("reasoning effort comes from provider capabilities (was 不支持)", () => {
  assert.deepEqual(effortsFor(claude, ""), ["low", "medium", "high", "xhigh", "max"]);
  assert.deepEqual(effortsFor(codex, "gpt-6"), ["low", "medium", "high", "xhigh"]);
  assert.deepEqual(effortsFor(codex, "gpt-5.5"), ["low", "medium", "high"]);
  const rows = m.effortChoices(effortsFor(claude, ""), { t, fallback: m.defaultEffort(modelsFor(claude), claude.provider_capabilities, "") });
  assert.equal(rows[0].label, "默认（高）");
  assert.deepEqual(rows.map((r) => r.value), ["", "low", "medium", "high", "xhigh", "max"]);
  assert.equal(rows.find((r) => r.value === "xhigh").label, "很高");
  // No levels: the field is hidden.
  assert.deepEqual(m.effortChoices(effortsFor(dsh, ""), { t }), []);
});

test("model list: named default, detected models, custom", () => {
  const rows = m.modelChoices(modelsFor(codex), { t, defaultModel: "gpt-6" });
  assert.deepEqual(rows.map((r) => r.value), ["", "gpt-6", "gpt-5.5", m.CUSTOM_MODEL]);
  assert.equal(rows[0].label, "默认（gpt-6）");
  assert.equal(rows[2].label, "gpt-5.5 — GPT-5.5");
  assert.equal(m.modelChoices([], { t })[0].label, "默认");
});

test("two access choices", () => {
  assert.deepEqual(m.accessChoices(echo), ["read_only", "full_access"]);
  assert.deepEqual(m.accessChoices({ permission_modes: ["read_only", "plan"] }), ["read_only"]);
});

test("agent setups: project agents only with a worktree", () => {
  assert.deepEqual(m.agentChoices(claude).map((a) => a.id), ["reviewer"]);
  assert.deepEqual(m.agentChoices(claude, { worktree: "/wt/a" }).map((a) => a.id), ["reviewer", "tester"]);
  assert.deepEqual(m.agentChoices(dsh), []);
});

test("remembered per person and project", () => {
  const store = memoryStore();
  m.saveLaunchDefaults(store, "alice", "prj_1", { platform: "codex", model: "gpt-5.5", effort: "low", access: "full_access", worktree: "/wt/a" });
  m.saveLaunchDefaults(store, "bob", "prj_1", { platform: "claude" });
  assert.deepEqual(m.loadLaunchDefaults(store, "alice", "prj_1"),
    { platform: "codex", model: "gpt-5.5", effort: "low", access: "full_access", worktree: "/wt/a" });
  assert.deepEqual(m.loadLaunchDefaults(store, "alice", "prj_2"), {});
  assert.deepEqual(m.loadLaunchDefaults(store, "bob", "prj_1"), { platform: "claude" });
  store.setItem(m.LAUNCH_DEFAULTS_KEY, "not json");
  assert.deepEqual(m.loadLaunchDefaults(store, "alice", "prj_1"), {});
});

test("initial choice: remembered, validated against the providers", () => {
  const providers = m.launchProviders(presets);
  const remembered = { platform: "codex", model: "gpt-5.5", effort: "low", access: "full_access", worktree: "/wt/gone" };
  const choice = m.initialChoice({ providers, remembered, modelsFor, effortsFor, worktrees: ["/wt/a"] });
  assert.deepEqual(choice, { platform: "codex", model: "gpt-5.5", custom: false, effort: "low", access: "full_access", worktree: "", agent: "" });
  // Nothing remembered: first provider, defaults, read only.
  assert.deepEqual(m.initialChoice({ providers, modelsFor, effortsFor }),
    { platform: "claude", model: "", custom: false, effort: "", access: "read_only", worktree: "", agent: "" });
  // A level the model does not take is dropped; an unknown model is custom.
  const odd = m.initialChoice({ providers, remembered: { platform: "codex", model: "my-model", effort: "max" }, modelsFor, effortsFor });
  assert.equal(odd.custom, true);
  assert.equal(odd.effort, "");
  // The caller wins (a sub-agent starts like its parent).
  const sub = m.initialChoice({ providers, remembered, parent: { platform: "claude", model: "sonnet-5" }, modelsFor, effortsFor });
  assert.equal(sub.platform, "claude");
  assert.equal(sub.model, "sonnet-5");
  // A provider that went away falls back to the first one.
  assert.equal(m.initialChoice({ providers, remembered: { platform: "gemini" }, modelsFor, effortsFor }).platform, "claude");
  assert.equal(m.initialChoice({ providers, remembered: { platform: "claude", agent: "reviewer" }, modelsFor, effortsFor }).agent, "reviewer");
  assert.equal(m.initialChoice({ providers, remembered: { platform: "claude", agent: "tester" }, modelsFor, effortsFor }).agent, "",
    "a project agent needs a worktree");
});

const units = [
  { uid: "A-0001", type: "agent", title: "Claude-1" },
  { uid: "I-0003", type: "idea", title: "Try curriculum" },
  { uid: "T-0042", type: "task", title: "整理实验日志并写周报" },
  { uid: "T-0050", type: "task", title: "Old", archived: true },
];

test("task picker: tasks first, no agents or archived units, searchable labels", () => {
  const options = m.taskOptions(units);
  assert.deepEqual(options.map((o) => o.uid), ["T-0042", "I-0003"]);
  assert.equal(options[0].label, "T-0042 · 整理实验日志并写周报");
  assert.equal(m.parseTaskValue("T-0042 · 整理实验日志并写周报", options), "T-0042");
  assert.equal(m.parseTaskValue("t-0042", options), "T-0042");
  assert.equal(m.parseTaskValue("周报", options), "");
});

test("task from context and automatic title", () => {
  assert.equal(m.contextTask({ prefill: { task: "T-0042" }, units }), "T-0042");
  assert.equal(m.contextTask({ parent: { task_ref: "I-0003" }, units }), "I-0003");
  assert.equal(m.contextTask({ selectedId: "T-0042", units }), "T-0042");
  assert.equal(m.contextTask({ selectedId: "A-0001", units }), "", "an agent node is not a task");
  assert.equal(m.contextTask({ units }), "");
  assert.equal(m.autoTitle({ task: units[2], prompt: "ignored" }), "整理实验日志并写周报");
  assert.equal(m.autoTitle({ prompt: "\n  Fix the flaky test in ci\nmore" }), "Fix the flaky test in ci");
  assert.equal(m.autoTitle({ prompt: "x".repeat(80) }).length, 60);
});

test("ARK boilerplate leaves the visible prompt", () => {
  assert.deepEqual(m.splitPrefillPrompt("Fix it"), { prompt: "Fix it", extra: "" });
  const brief = "# ARK dispatch · T-0042 — x\n\nYou are receiving one task";
  assert.deepEqual(m.splitPrefillPrompt(brief), { prompt: "", extra: brief });
  assert.equal(m.splitPrefillPrompt("If ARK_SESSION_ID is set and ARK_WAKE=ark ...").prompt, "");
  assert.equal(m.firstMessage("Do it", ""), "Do it");
  assert.equal(m.firstMessage("", "extra"), "extra");
  assert.equal(m.firstMessage("Do it", "extra"), "Do it\n\nextra");
  assert.equal(m.firstMessage("", ""), "", "a session may start without a message");
});

test("codex transcript tools classify like Claude's", () => {
  const command = { name: "command", input: { command: "pytest -q" } };
  const change = { name: "file_change", input: { files: [{ path: "src/a.py", kind: "update" }] } };
  assert.equal(sessionModel.toolKind(command), "command");
  assert.equal(sessionModel.toolCommand(command), "pytest -q");
  assert.equal(sessionModel.toolKind(change), "edit");
  assert.deepEqual(sessionModel.toolPaths(change), ["src/a.py"]);
  assert.equal(sessionModel.toolKind({ name: "Bash", input: { command: "ls" } }), "command");
});
