// Pure helpers for the "Run an agent in ARK" dialog (views/tasks.js
// openLaunchSession) and the one-click "Run with agent" entries: which
// providers to offer, the model / reasoning / access choices, the choice
// remembered per person and project, the task taken from context and the
// automatic title.
// No DOM and no imports (tests load it in a bare VM).

export const LAUNCH_DEFAULTS_KEY = "ark.launchDefaults.v1";
export const CUSTOM_MODEL = "__custom__";
export const ACCESS_CHOICES = ["read_only", "full_access"];
// Real coding agents first, in this order; demo and local presets last.
const PROVIDER_ORDER = ["claude", "codex", "dsh", "opencode", "cursor", "gemini", "custom", "shell", "echo"];
const MAX_REMEMBERED = 60;

const str = (value) => (value === null || value === undefined ? "" : String(value)).trim();

function rank(preset) {
  const index = PROVIDER_ORDER.indexOf(preset.id);
  const auth = preset.provider_status === "unauthenticated" ? 1 : 0;
  return [preset.demo ? 2 : 0, auth, index < 0 ? PROVIDER_ORDER.length - 3 : index];
}

/** Providers a session can start with: available and not switched off, best first. */
export function launchProviders(presets = []) {
  return presets
    .filter((preset) => preset && preset.available)
    .map((preset, order) => ({ preset, order, key: rank(preset) }))
    .sort((a, b) => a.key[0] - b.key[0] || a.key[1] - b.key[1] || a.key[2] - b.key[2] || a.order - b.order)
    .map((entry) => entry.preset);
}

/** Coding agents switched off in this person's settings (named in the dialog). */
export function disabledProviders(presets = []) {
  return presets.filter((preset) => preset && !preset.available && preset.disabled && !preset.demo
    && preset.provider_status !== "missing");
}

export function providerCapabilities(preset) {
  return (preset && preset.provider_capabilities) || {};
}

/**
 * Model picker rows from modelOptions(): the provider default first (named
 * when known: "Default (opus-5.5)"), then the known models, then "Custom…".
 */
export function modelChoices(options = [], { t = (key, vars) => key, defaultModel = "" } = {}) {
  const def = options.find((item) => item.default) || null;
  const name = str(defaultModel) || (def ? def.id : "");
  const rows = [{ value: "", label: name ? t("tasks.launcher.model_default_named", { model: name }) : t("tasks.launcher.model_default") }];
  const seen = new Set();
  for (const item of options) {
    const id = str(item.id);
    if (!id || seen.has(id)) continue;
    seen.add(id);
    const extra = str(item.label);
    rows.push({ value: id, label: extra && extra !== id && !extra.startsWith(id) ? `${id} — ${extra}` : (extra || id) });
  }
  rows.push({ value: CUSTOM_MODEL, label: t("tasks.launcher.model_custom") });
  return rows;
}

/** The reasoning level the provider uses when none is chosen ("" when unknown). */
export function defaultEffort(options = [], capabilities = {}, model = "") {
  const id = str(model);
  const match = id ? options.find((item) => item.id === id) : options.find((item) => item.default);
  return str((match && match.default_effort) || (!id || (match && match.default) ? capabilities.default_effort : ""));
}

/** Reasoning picker rows; empty when the provider has no levels (the field is hidden). */
export function effortChoices(efforts = [], { t = (key, vars) => key, fallback = "" } = {}) {
  if (!efforts.length) return [];
  const label = (effort) => {
    const key = `tasks.effort.${effort}`;
    const text = t(key);
    return text === key ? effort : text;
  };
  const def = str(fallback);
  return [
    { value: "", label: def ? t("tasks.launcher.effort_default_named", { effort: label(def) }) : t("tasks.launcher.effort_default") },
    ...efforts.map((effort) => ({ value: effort, label: label(effort) })),
  ];
}

/** The two access modes the dialog offers, as far as the provider has them. */
export function accessChoices(preset = {}) {
  const modes = preset.permission_modes || preset.access_modes || ACCESS_CHOICES;
  const out = ACCESS_CHOICES.filter((mode) => modes.includes(mode));
  return out.length ? out : ["read_only"];
}

/**
 * Named agent setups of the provider (Claude Code agents, Codex profiles).
 * Project agents live in the repository, so they are offered only with a
 * worktree (the private workspace does not contain them).
 */
export function agentChoices(preset = {}, { worktree = "" } = {}) {
  const list = Array.isArray(preset.agent_options) ? preset.agent_options : [];
  return list.filter((item) => item && item.id && (item.scope !== "project" || worktree));
}

// ------------------------------------------------------------ remembered
export function launchStoreKey(user = "", project = "") {
  return `${str(user) || "local"}|${str(project) || "-"}`;
}

function readAll(store) {
  try {
    const value = JSON.parse((store && store.getItem(LAUNCH_DEFAULTS_KEY)) || "{}");
    return value && typeof value === "object" && !Array.isArray(value) ? value : {};
  } catch {
    return {};
  }
}

function cleanChoice(raw = {}) {
  const out = {};
  for (const key of ["platform", "model", "effort", "access", "worktree", "agent", "start"]) {
    const value = str(raw[key]).slice(0, 400);
    if (value) out[key] = value;
  }
  return out;
}

export function loadLaunchDefaults(store, user, project) {
  return cleanChoice(readAll(store)[launchStoreKey(user, project)] || {});
}

export function saveLaunchDefaults(store, user, project, choice) {
  const all = readAll(store);
  const key = launchStoreKey(user, project);
  delete all[key];
  all[key] = { ...cleanChoice(choice), at: Date.now() };
  const keys = Object.keys(all);
  for (const old of keys.slice(0, Math.max(0, keys.length - MAX_REMEMBERED))) delete all[old];
  try { store && store.setItem(LAUNCH_DEFAULTS_KEY, JSON.stringify(all)); } catch { /* storage disabled */ }
  return all[key];
}

/**
 * The dialog's starting choice: what the caller asks for, else what this
 * person last used in this project (when the provider still offers it),
 * else the first provider with its defaults.
 *   modelsFor(preset) -> modelOptions rows, effortsFor(preset, model) -> levels,
 *   worktrees -> paths that can be chosen.
 */
export function initialChoice({ providers = [], remembered = {}, prefill = {}, parent = null,
  modelsFor = () => [], effortsFor = () => [], worktrees = [] } = {}) {
  const ids = providers.map((preset) => preset.id);
  const wanted = [prefill.platform, parent && parent.platform, remembered.platform].map(str).find((id) => id && ids.includes(id));
  const platform = wanted || ids[0] || "";
  const preset = providers.find((item) => item.id === platform) || null;
  const same = remembered.platform === platform;
  let model = str(prefill.model !== undefined ? prefill.model : (parent && parent.platform === platform ? parent.model : (same ? remembered.model : "")));
  const known = (modelsFor(preset) || []).some((item) => item.id === model);
  const custom = Boolean(model) && !known;
  const efforts = effortsFor(preset, model) || [];
  let effort = str(prefill.effort !== undefined ? prefill.effort : (same ? remembered.effort : ""));
  if (!efforts.includes(effort)) effort = "";
  const accesses = accessChoices(preset || {});
  let access = str(prefill.access || remembered.access || "read_only");
  if (!accesses.includes(access)) access = accesses.includes("read_only") ? "read_only" : accesses[0];
  let worktree = str(prefill.worktree !== undefined ? prefill.worktree : remembered.worktree);
  if (!worktrees.includes(worktree)) worktree = "";
  const agents = agentChoices(preset || {}, { worktree });
  let agent = str(prefill.agent !== undefined ? prefill.agent : (same ? remembered.agent : ""));
  if (!agents.some((item) => item.id === agent)) agent = "";
  return { platform, model, custom, effort, access, worktree, agent };
}

// ------------------------------------------------------------ task + title
const PICKABLE_TYPES = ["task", "experiment", "question", "claim", "idea", "topic", "decision", "todo"];

/** Units offered in the task picker: tasks first, then other work items; not archived. */
export function taskOptions(units = [], limit = 400) {
  const order = (unit) => {
    const i = PICKABLE_TYPES.indexOf(unit.type);
    return i < 0 ? PICKABLE_TYPES.length : i;
  };
  return units
    .filter((unit) => unit && unit.uid && !unit.archived && unit.type !== "agent")
    .map((unit, index) => ({ unit, index }))
    .sort((a, b) => order(a.unit) - order(b.unit) || a.index - b.index)
    .slice(0, limit)
    .map(({ unit }) => ({ uid: unit.uid, title: str(unit.title), type: unit.type, label: taskLabel(unit) }));
}

export function taskLabel(unit) {
  const title = str(unit && unit.title);
  return title ? `${unit.uid} · ${title}` : str(unit && unit.uid);
}

/** The uid a picker value names ("T-0042 · Title", "T-0042" or "t-0042"); "" otherwise. */
export function parseTaskValue(value, options = []) {
  const text = str(value);
  if (!text) return "";
  const head = text.split(" · ")[0].trim().toLowerCase();
  const hit = options.find((item) => item.uid.toLowerCase() === head)
    || options.find((item) => item.label.toLowerCase() === text.toLowerCase());
  return hit ? hit.uid : "";
}

/**
 * The task a new session is about: the one the caller names, the parent
 * session's, else the unit open in the details panel.
 */
export function contextTask({ prefill = {}, parent = null, selectedId = "", units = [] } = {}) {
  for (const candidate of [prefill.task, parent && parent.task_ref]) {
    if (str(candidate)) return str(candidate);
  }
  const selected = str(selectedId);
  if (!selected) return "";
  const unit = units.find((item) => item.uid === selected || String(item.id) === selected);
  return unit && unit.type !== "agent" && !unit.archived ? unit.uid : "";
}

/** Automatic title: the task's title, else the prompt's first line. */
export function autoTitle({ task = null, prompt = "", max = 60 } = {}) {
  const base = str(task && task.title) || (str(prompt).split(/\r?\n/).map((line) => line.trim()).find(Boolean) || "");
  return base.length > max ? `${base.slice(0, max - 1).trimEnd()}…` : base;
}

// A draft carried into the dialog may hold ARK's own instructions (the
// dispatch brief, the watcher lines of the setup prompt).  ARK gives a
// session that context itself (ARK-TASK.md, ARK_SESSION_ID), so the visible
// prompt starts without it; the advanced section keeps it as extra text.
const BOILERPLATE = [/^#\s*ARK dispatch\b/m, /\bARK_SESSION_ID\b/, /\bARK_WAKE\b/];

export function splitPrefillPrompt(text = "") {
  const value = String(text || "");
  if (!value.trim()) return { prompt: "", extra: "" };
  return BOILERPLATE.some((re) => re.test(value)) ? { prompt: "", extra: value.trim() } : { prompt: value, extra: "" };
}

/** The first message: the prompt, then any extra text the advanced section kept. */
export function firstMessage(prompt = "", extra = "") {
  return [str(prompt), str(extra)].filter(Boolean).join("\n\n");
}

// ---- what the session does first (T-0121)
// The Run dialog's "Start" option: the Home prompt library (built-in
// prompts, then the person's and the project's custom prompts, pinned ones
// first) plus "free" (follow the first message, or wait for it).  The
// chosen prompt's text goes into the session block; the server adds live
// facts for setup, device setup and continue (GET /api/agent-sessions/start).

export const START_FREE = "free";
// Built-in ids the server knows the default for.
const START_NEEDS = new Set(["setup", "device-setup"]);

/**
 * Options in order: pinned prompts (pin order), the other built-ins, custom
 * prompts, then "free".  `entries` are library entries ({id, title,
 * custom, unit}); `pins` the effective pin list.
 */
export function startOptions(entries = [], pins = []) {
  const list = (entries || []).filter((e) => e && e.id);
  const byId = new Map(list.map((e) => [e.id, e]));
  const pinned = (pins || []).map((id) => byId.get(id)).filter(Boolean);
  const seen = new Set(pinned.map((e) => e.id));
  const rest = list.filter((e) => !seen.has(e.id));
  const opt = (e, isPinned) => ({ id: e.id, title: str(e.title) || e.id, custom: Boolean(e.custom), unit: Boolean(e.unit), pinned: isPinned });
  return [
    ...pinned.map((e) => opt(e, true)),
    ...rest.filter((e) => !e.custom).map((e) => opt(e, false)),
    ...rest.filter((e) => e.custom).map((e) => opt(e, false)),
    { id: START_FREE, title: "", custom: false, unit: false, pinned: false },
  ];
}

/**
 * The option selected when the dialog opens or the task changes: setup when
 * the server says setup is missing on this device, continue when a task is
 * chosen, else what this person last chose in this project (not setup or
 * continue, which follow the facts), else free.
 */
export function defaultStartMode({ server = null, task = "", remembered = "", options = [] } = {}) {
  const ids = new Set((options || []).map((o) => o.id));
  const mode = str(server && server.mode);
  if (START_NEEDS.has(mode) && ids.has(mode)) return mode;
  if (str(task) && ids.has("continue")) return "continue";
  const last = str(remembered);
  if (last && ids.has(last) && !START_NEEDS.has(last) && last !== "continue") return last;
  return START_FREE;
}

/**
 * One line under the option: what the session does first.  `t` is the
 * translator; `option` the chosen option; `server` the reply of GET
 * /api/agent-sessions/start for the chosen task.
 */
export function startPreview({ mode = START_FREE, option = null, server = null, task = "", prompt = "", t = (k) => k } = {}) {
  const info = (server && server.task) || null;
  if (mode === "setup") return t("tasks.launcher.start_preview.setup");
  if (mode === "device-setup") {
    const missing = (server && server.setup && server.setup.needs_location) || [];
    return t("tasks.launcher.start_preview.device", { n: missing.length });
  }
  if (mode === "continue") {
    if (!str(task)) return t("tasks.launcher.start_preview.continue_pick");
    const sendBacks = Number(info && info.uid === task ? info.send_backs || 0 : 0);
    const open = Number(info && info.uid === task ? info.open_comments || 0 : 0);
    if (sendBacks) return t("tasks.launcher.start_preview.continue_sendback", { uid: task, n: sendBacks });
    if (open) return t("tasks.launcher.start_preview.continue_open", { uid: task, n: open });
    return t("tasks.launcher.start_preview.continue", { uid: task });
  }
  if (mode === START_FREE) {
    return str(prompt) ? t("tasks.launcher.start_preview.follow") : t("tasks.launcher.start_preview.wait");
  }
  return t("tasks.launcher.start_preview.prompt", { title: (option && option.title) || mode });
}

/** The first-message placeholder for the chosen start. */
export function startPlaceholder({ mode = START_FREE, option = null, task = "", t = (k) => k } = {}) {
  if (mode === "setup") return t("tasks.launcher.start_placeholder.setup");
  if (mode === "device-setup") return t("tasks.launcher.start_placeholder.device");
  if (mode === "continue") {
    return str(task) ? t("tasks.launcher.start_placeholder.continue", { uid: task }) : t("tasks.launcher.start_preview.continue_pick");
  }
  if (mode === START_FREE) return t("tasks.launcher.start_placeholder.free");
  return t("tasks.launcher.start_placeholder.prompt", { title: (option && option.title) || mode });
}

/** The "start" of the launch request: {id, title, text} ("free": id only). */
export function startRequest(option, text = "") {
  if (!option || option.id === START_FREE) return { id: START_FREE };
  return { id: option.id, title: str(option.title), text: str(text) };
}
