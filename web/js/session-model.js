// Pure helpers for the Tasks → Sessions workspace.
// Kept DOM-free so the grouping/filtering rules can be tested with Node.

export const ACTIVE_STATUSES = ["starting", "running", "idle", "stopping"];

// English defaults for the labels below. UI callers inject the app translator
// (`{ t }` from i18n.js); Node tests use these strings unchanged.
/* i18n-ignore-start */
const EN_TEXT = {
  "tasks.unassigned": "Unassigned",
  "session.group.no_task": "No task",
  "session.group.no_worktree": "No worktree",
  "session.group.active": "Active",
  "session.group.errored": "Errored",
  "session.group.completed": "Completed",
  "session.provider.ready": "ready",
  "tasks.launch_unavailable": "not installed",
  "session.provider.unauthenticated": "not signed in",
  "session.provider.auth_unknown": "auth unknown",
  "common.unknown": "unknown",
  "session.untitled": "Untitled session",
  "session.file_fallback": "file",
  "session.composer.hint_stdin": "Running — this message is queued as a steer and written to the process stdin.",
  "session.composer.hint_next_turn": "Running — this one-shot agent cannot read stdin; the steer waits for the next continue.",
  "session.composer.hint_continue": "The process is stopped. Continue starts one more turn with the original prompt plus this message.",
  "session.steer.delivered_next_turn": "delivered next turn",
  "session.steer.delivered": "delivered",
  "session.steer.failed": "failed",
  "session.steer.failed_error": "failed: {error}",
  "session.steer.queued_next_turn": "queued for next turn",
  "session.steer.queued": "queued",
  "session.perm.read_only": "read-only",
  "session.perm.plan": "plan",
  "session.perm.ask": "ask to write",
  "session.perm.accept_edits": "accept edits",
  "session.perm.full_access": "full access",
  "session.perm_desc.read_only": "Read the project through the read-only task API; no write token.",
  "session.perm_desc.plan": "Read-only planning turn with a plan deliverable; no write token.",
  "session.perm_desc.ask": "Every mutating tool or command waits for your approval.",
  "session.perm_desc.accept_edits": "File edits are auto-approved by ARK; commands still ask.",
  "session.perm_desc.full_access": "No prompts; the turn receives ARK's write token.",
  "session.approval.pending": "waiting for your decision",
  "session.approval.allowed_always": "allowed always",
  "session.approval.auto_approved": "auto-approved",
  "session.approval.allowed_once": "allowed once",
  "session.approval.blocked_by_mode": "blocked by mode",
  "session.approval.denied": "denied",
  "session.approval.expired": "expired",
  "session.approval.failed": "response not delivered",
  "session.approval.cancelled": "cancelled by the adapter",
  "session.approval.resolved": "resolved by the adapter",
  "session.approval.deciding": "sending decision…",
  "session.approval.kind_command": "command",
  "session.approval.kind_file_edit": "file edit",
  "session.approval.kind_tool": "tool",
  "session.relation.fork": "fork",
  "session.relation.side_chat": "side chat",
};
/* i18n-ignore-end */

/** English translator used when no `t` is injected. Same `{var}` syntax as i18n.js. */
export function enText(key, vars = {}) {
  let text = EN_TEXT[key] ?? key;
  for (const [name, value] of Object.entries(vars || {})) text = text.replaceAll(`{${name}}`, String(value));
  return text;
}

export function isActiveSession(session) {
  return Boolean(session) && ACTIVE_STATUSES.includes(session.status);
}

export function isErroredSession(session) {
  return Boolean(session) && session.status === "errored";
}

export function statusBucket(session) {
  if (isActiveSession(session)) return "active";
  if (isErroredSession(session)) return "errored";
  return "completed";
}

export function sessionSortKey(session) {
  const rank = { running: 0, idle: 1, starting: 2, stopping: 3, errored: 4, stopped: 5, archived: 6 };
  return rank[session?.status] ?? 9;
}

export function sortSessions(sessions = []) {
  return [...sessions].sort((a, b) => sessionSortKey(a) - sessionSortKey(b)
    || String(b.last_activity_at || "").localeCompare(String(a.last_activity_at || "")));
}

export function sessionSearchText(session) {
  return [session.title, session.id, session.task_ref, session.model, session.agent,
    session.platform_label, session.platform, session.parent_id, session.error,
    session.worktree?.branch, session.worktree?.path]
    .filter(Boolean).join(" ").toLowerCase();
}

export function filterSessions(sessions = [], { query = "", filter = "all" } = {}) {
  const text = String(query || "").trim().toLowerCase();
  return sessions.filter((session) => {
    const bucket = statusBucket(session);
    if (filter === "active" && bucket !== "active") return false;
    if (filter === "completed" && bucket === "active") return false;
    if (filter === "errors" && bucket !== "errored") return false;
    if (filter === "subagents" && !session.parent_id) return false;
    if (!text) return true;
    return sessionSearchText(session).includes(text);
  });
}

export function groupSessions(sessions = [], groupBy = "status", { t = enText } = {}) {
  const groups = new Map();
  for (const session of sortSessions(sessions)) {
    let key;
    let label;
    if (groupBy === "agent") {
      key = session.agent || session.platform_label || "Unassigned"; // i18n-ignore: stable group id
      label = session.agent || session.platform_label || t("tasks.unassigned");
    } else if (groupBy === "task") {
      key = session.task_ref || "No task"; // i18n-ignore: stable group id
      label = session.task_ref || t("session.group.no_task");
    } else if (groupBy === "worktree") {
      key = session.worktree?.branch || session.worktree?.path || "No worktree"; // i18n-ignore: stable group id
      label = session.worktree?.branch || session.worktree?.path || t("session.group.no_worktree");
    } else {
      key = statusBucket(session);
      label = t(key === "active" ? "session.group.active" : key === "errored" ? "session.group.errored" : "session.group.completed");
    }
    if (!groups.has(key)) groups.set(key, { id: String(key), label, sessions: [] });
    groups.get(key).sessions.push(session);
  }
  const order = { active: 0, errored: 1, completed: 2 };
  return [...groups.values()].sort((a, b) => {
    if (groupBy === "status") return (order[a.id] ?? 9) - (order[b.id] ?? 9);
    return a.label.localeCompare(b.label);
  });
}

export function worktreeLabel(session) {
  const worktree = session?.worktree;
  if (!worktree) return "";
  return worktree.branch || worktree.path || "";
}

export function worktreeConflict(session) {
  return Boolean(session?.worktree?.conflict);
}

export function providerStatusLabel(status, { t = enText } = {}) {
  if (status === "ready") return t("session.provider.ready");
  if (status === "missing") return t("tasks.launch_unavailable");
  if (status === "unauthenticated") return t("session.provider.unauthenticated");
  if (status === "unknown") return t("session.provider.auth_unknown");
  return t("common.unknown");
}

export function sessionChildren(sessions = [], id) {
  return sortSessions(sessions.filter((session) => session.parent_id === id));
}

export function sessionDescendants(sessions = [], id, maxDepth = 3) {
  const out = [];
  const visited = new Set([id]);
  const visit = (parentId, depth) => {
    if (depth > maxDepth) return;
    for (const child of sessionChildren(sessions, parentId)) {
      if (visited.has(child.id)) continue;
      visited.add(child.id);
      out.push({ session: child, depth });
      visit(child.id, depth + 1);
    }
  };
  visit(id, 1);
  return out;
}

export function sessionAncestors(sessions = [], id) {
  const byId = new Map(sessions.map((session) => [session.id, session]));
  const out = [];
  let current = byId.get(id);
  const seen = new Set([id]);
  while (current?.parent_id && !seen.has(current.parent_id)) {
    seen.add(current.parent_id);
    const parent = byId.get(current.parent_id);
    if (!parent) break;
    out.unshift(parent);
    current = parent;
  }
  return out;
}

export function sessionRoots(sessions = []) {
  return sortSessions(sessions.filter((session) => !session.parent_id));
}

export function sessionTitle(session, { t = enText } = {}) {
  return session?.title || t("session.untitled");
}

export function mediaKind(pathOrName = "", mime = "") {
  const type = String(mime || "").toLowerCase();
  if (type.startsWith("image/")) return "image";
  if (type.startsWith("video/")) return "video";
  if (type.startsWith("audio/")) return "audio";
  if (type === "application/pdf") return "pdf";
  const suffix = String(pathOrName).toLowerCase().match(/\.([a-z0-9]+)$/)?.[1] || "";
  if (["png", "jpg", "jpeg", "webp", "gif", "svg", "avif", "bmp", "ico"].includes(suffix)) return "image";
  if (["mp4", "webm", "mov", "m4v", "ogv"].includes(suffix)) return "video";
  if (["mp3", "wav", "ogg", "m4a", "flac", "aac"].includes(suffix)) return "audio";
  if (suffix === "pdf") return "pdf";
  return "document";
}

export function formatBytes(bytes) {
  const value = Number(bytes) || 0;
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`;
  return `${(value / (1024 * 1024)).toFixed(1)} MB`;
}

export function transcriptMeta(items = []) {
  return {
    messages: items.filter((item) => item.type === "message").length,
    tools: items.filter((item) => item.type === "tool").length,
    errors: items.filter((item) => item.type === "error").length,
    reasoning: items.filter((item) => item.type === "notice" && item.level === "reasoning").length,
  };
}

export function nextSelection(sessions = [], selectedId = "") {
  if (sessions.some((session) => session.id === selectedId)) return selectedId;
  const activeRoot = sessions.find((session) => isActiveSession(session) && !session.parent_id);
  if (activeRoot) return activeRoot.id;
  const active = sessions.find((session) => isActiveSession(session));
  return (active || sessions.find((session) => !session.parent_id) || sortSessions(sessions)[0] || {}).id || "";
}

// ------------------------------------------------------- file tab strip state
export const FILE_TAB_LIMIT = 8;
export const SESSION_TAB_STORE_KEY = "ark.session.fileTabs.v1";

const FILE_LANGUAGES = {
  bat: "bat", c: "c", cfg: "ini", conf: "ini", cpp: "cpp", cs: "csharp",
  css: "css", csv: "csv", go: "go", h: "c", hpp: "cpp", htm: "html",
  html: "html", ini: "ini", java: "java", js: "javascript", json: "json", // i18n-ignore: language ids
  jsonl: "jsonl", jsx: "jsx", log: "log", md: "markdown", markdown: "markdown",
  mdx: "markdown", mjs: "javascript", ps1: "powershell", py: "python",
  rb: "ruby", rs: "rust", rst: "rst", sh: "bash", sql: "sql", svg: "xml",
  tex: "latex", toml: "toml", ts: "typescript", tsx: "tsx", txt: "text",
  xml: "xml", yaml: "yaml", yml: "yaml",
};

export function fileLanguage(path = "") {
  const suffix = String(path).toLowerCase().match(/\.([a-z0-9]+)$/)?.[1] || "";
  return FILE_LANGUAGES[suffix] || "";
}

export function tabLabel(path = "", { t = enText } = {}) {
  const text = String(path || "").replace(/\\/g, "/");
  return text.split("/").filter(Boolean).pop() || text || t("session.file_fallback");
}

export function normalizeTabState(value) {
  const source = value && typeof value === "object" ? value : {};
  const rawTabs = Array.isArray(source.tabs) ? source.tabs : [];
  const tabs = [...new Set(rawTabs.map((item) => String(item || "").trim()).filter(Boolean))]
    .slice(0, FILE_TAB_LIMIT);
  let active = String(source.active || "").trim();
  if (!tabs.includes(active)) active = tabs[tabs.length - 1] || "";
  return { tabs, active };
}

export function openFileTab(state, path) {
  const target = String(path || "").trim();
  const next = normalizeTabState(state);
  if (!target) return next;
  next.tabs = next.tabs.filter((item) => item !== target);
  next.tabs.push(target);
  while (next.tabs.length > FILE_TAB_LIMIT) next.tabs.shift();
  next.active = target;
  return next;
}

export function closeFileTab(state, path) {
  const target = String(path || "").trim();
  const next = normalizeTabState(state);
  const index = next.tabs.indexOf(target);
  if (index < 0) return next;
  next.tabs.splice(index, 1);
  if (next.active === target) {
    next.active = next.tabs[index] || next.tabs[index - 1] || "";
  }
  return normalizeTabState(next);
}

export function activateFileTab(state, path) {
  const target = String(path || "").trim();
  const next = normalizeTabState(state);
  if (!next.tabs.includes(target)) return next;
  next.active = target;
  return next;
}

export function fileViewKind(path = "", mime = "") {
  const kind = mediaKind(path, mime);
  if (kind !== "document") return kind;
  if (/\.(md|markdown|mdx)$/i.test(path)) return "markdown";
  if (fileLanguage(path)) return "code";
  return "text";
}

// ------------------------------------------------------------ composer state
export function composerMode(session, { t = enText } = {}) {
  if (!session) return { mode: "none", canSend: false, canAttach: false, hint: "" };
  if (isActiveSession(session)) {
    const stdin = Boolean(session.capabilities?.followup);
    return {
      mode: "steer",
      canSend: true,
      canAttach: true,
      channel: stdin ? "stdin" : "next_turn",
      hint: stdin
        ? t("session.composer.hint_stdin")
        : t("session.composer.hint_next_turn"),
    };
  }
  return {
    mode: "continue",
    canSend: true,
    canAttach: true,
    channel: "continue",
    hint: t("session.composer.hint_continue"),
  };
}

export function steerLabel(item = {}, { t = enText } = {}) {
  if (item.status === "delivered") return t(item.channel === "continue" ? "session.steer.delivered_next_turn" : "session.steer.delivered");
  if (item.status === "failed") return item.error ? t("session.steer.failed_error", { error: item.error }) : t("session.steer.failed");
  return t(item.channel === "next_turn" ? "session.steer.queued_next_turn" : "session.steer.queued");
}

// ---------------------------------------------------- permission modes/approvals
export const PERMISSION_MODES = ["read_only", "plan", "ask", "accept_edits", "full_access"];
export const APPROVAL_MODES = ["ask", "accept_edits"];
// English tables kept for callers that import them directly; prefer the
// translator-aware permissionModeLabel/permissionModeDescription below.
export const PERMISSION_LABELS = Object.fromEntries(
  PERMISSION_MODES.map((mode) => [mode, EN_TEXT[`session.perm.${mode}`]]));
export const PERMISSION_DESCRIPTIONS = Object.fromEntries(
  PERMISSION_MODES.map((mode) => [mode, EN_TEXT[`session.perm_desc.${mode}`]]));

export function permissionModeLabel(mode, { t = enText } = {}) {
  if (PERMISSION_LABELS[mode]) return t(`session.perm.${mode}`);
  return mode || t("session.perm.read_only");
}

export function permissionModeDescription(mode, { t = enText } = {}) {
  return PERMISSION_DESCRIPTIONS[mode] ? t(`session.perm_desc.${mode}`) : "";
}

export function isApprovalMode(mode) {
  return APPROVAL_MODES.includes(mode);
}

export function sessionPermissionModes(session = {}, preset = null) {
  const raw = preset?.permission_modes || session?.permission_modes
    || session?.capabilities?.permission_modes || session?.capabilities?.access_modes
    || ["read_only", "full_access"];
  const source = Array.isArray(raw) ? raw : [];
  return source.filter((mode, index) => PERMISSION_MODES.includes(mode) && source.indexOf(mode) === index);
}

export function sessionSupportsApproval(session = {}, preset = null) {
  if (preset?.supports_approval !== undefined) return Boolean(preset.supports_approval);
  if (session?.capabilities?.supports_approval !== undefined) return Boolean(session.capabilities.supports_approval);
  return false;
}

export function sessionAccessMode(session = {}, allowed = null) {
  const modes = allowed || sessionPermissionModes(session);
  const mode = session?.access_mode || session?.permission_mode || "read_only";
  return modes.includes(mode) ? mode : (modes[0] || "read_only");
}

export function pendingApprovals(session = {}) {
  return (session?.approvals?.items || []).filter((item) => item?.status === "pending");
}

export function approvalCounts(approvals = null) {
  const items = Array.isArray(approvals) ? approvals
    : (approvals?.items || approvals?.approvals?.items || []);
  const counts = { total: items.length, pending: 0, allowed: 0, denied: 0, expired: 0, failed: 0, other: 0 };
  for (const item of items) {
    counts[item?.status] = (counts[item?.status] || 0) + 1;
  }
  return counts;
}

export function approvalStatusLabel(item = {}, { t = enText } = {}) {
  if (item.status === "pending") return t("session.approval.pending");
  if (item.status === "allowed") return t(item.scope === "always" ? "session.approval.allowed_always" : item.auto ? "session.approval.auto_approved" : "session.approval.allowed_once");
  if (item.status === "denied") return t(item.auto ? "session.approval.blocked_by_mode" : "session.approval.denied");
  if (item.status === "expired") return t("session.approval.expired");
  if (item.status === "failed") return t("session.approval.failed");
  if (item.status === "cancelled") return t("session.approval.cancelled");
  if (item.status === "resolved") return t("session.approval.resolved");
  if (item.status === "deciding") return t("session.approval.deciding");
  return item.status || t("common.unknown");
}

export function canDecideApproval(item = {}, active = false) {
  return Boolean(active) && item.status === "pending";
}

export function approvalRequestLabel(item = {}, { t = enText } = {}) {
  if (item.kind === "command") return item.command || item.tool || t("session.approval.kind_command");
  if (item.kind === "file_edit") return item.path || item.tool || t("session.approval.kind_file_edit");
  return item.command || item.path || item.description || item.tool || item.kind || t("session.approval.kind_tool");
}

// --------------------------------------------------------------- steer queue
export function queuedSteerItems(session = {}) {
  return (session?.steer_queue || []).filter((item) => item.status !== "delivered");
}

export function editableSteer(item = {}) {
  return Boolean(item?.id) && item.status !== "delivered";
}

export function steerMoveOrder(session = {}, steerId = "", direction = -1) {
  const queue = queuedSteerItems(session);
  const index = queue.findIndex((item) => item.id === steerId);
  const target = index + Number(direction || 0);
  if (index < 0 || target < 0 || target >= queue.length) return null;
  const order = queue.map((item) => item.id);
  [order[index], order[target]] = [order[target], order[index]];
  return order;
}

export function forkRelationLabel(session = {}, { t = enText } = {}) {
  if (session.relation === "fork") return t("session.relation.fork");
  if (session.relation === "side_chat" || session.side_chat_of) return t("session.relation.side_chat");
  return "";
}

export function formatTokens(value) {
  const number = Math.max(0, Number(value) || 0);
  if (number < 1000) return String(number);
  if (number < 1000000) return `${(number / 1000).toFixed(number < 10000 ? 1 : 0)}k`;
  return `${(number / 1000000).toFixed(1)}m`;
}

// ------------------------------------------------------------ thread blocks
// The conversation column groups transcript items into display blocks:
// consecutive tool calls collapse into one activity row, and every user turn
// is preceded by a timestamp separator.  Keys are derived from the stable
// transcript item ids so the DOM can be reconciled instead of rebuilt.

function toolInputObject(item = {}) {
  const input = item.input;
  if (input && typeof input === "object" && !Array.isArray(input)) return input;
  if (typeof input === "string") {
    const text = input.trim();
    if (text.startsWith("{")) {
      try {
        const parsed = JSON.parse(text);
        if (parsed && typeof parsed === "object") return parsed;
      } catch { /* plain string input */ }
    }
  }
  return {};
}

export function toolCommand(item = {}) {
  const input = toolInputObject(item);
  const value = input.command ?? input.cmd ?? input.script ?? input.argv;
  if (Array.isArray(value)) return value.map(String).join(" ");
  if (typeof value === "string") return value;
  if (typeof item.input === "string" && /^(shell|bash|exec|command|run|terminal|powershell|sh)$/i.test(item.name || "")) {
    return item.input;
  }
  return "";
}

export function toolPaths(item = {}) {
  const input = toolInputObject(item);
  const out = [];
  for (const key of ["path", "file_path", "file", "filename", "target_file", "notebook_path"]) {
    if (typeof input[key] === "string" && input[key].trim()) out.push(input[key].trim());
  }
  if (Array.isArray(input.paths)) out.push(...input.paths.filter((p) => typeof p === "string"));
  if (Array.isArray(input.files)) {
    for (const file of input.files) {
      if (typeof file === "string") out.push(file);
      else if (file && typeof file.path === "string") out.push(file.path);
    }
  }
  const patch = typeof input.patch === "string" ? input.patch
    : typeof input.input === "string" ? input.input : "";
  for (const match of patch.matchAll(/^\*\*\* (?:Update|Add|Delete) File: (.+)$/gm)) out.push(match[1].trim());
  return [...new Set(out)];
}

export function toolKind(item = {}) {
  const name = String(item.name || "").toLowerCase();
  if (/(apply_?patch|edit|write|patch|str_replace|create_file|multiedit|notebookedit|file_change)/.test(name)) return "edit";
  if (/(grep|search|glob|find|ripgrep|^rg$|^ls$|list_dir|web_search|websearch)/.test(name)) return "search";
  if (/(read|view|^cat$|open_file|fetch|webfetch)/.test(name)) return "read";
  if (/(shell|bash|exec|command|^run|terminal|powershell|^sh$|local_shell)/.test(name) || toolCommand(item)) return "command";
  return "other";
}

export function toolGroupSummary(tools = []) {
  const counts = { command: 0, edit: 0, search: 0, read: 0, other: 0 };
  const files = new Set();
  let running = 0;
  let failed = 0;
  let lastKind = "other";
  for (const tool of tools) {
    const kind = toolKind(tool);
    counts[kind] += 1;
    lastKind = kind;
    if (kind === "edit") for (const path of toolPaths(tool)) files.add(path);
    if (tool.status === "running") running += 1;
    if (tool.status === "error") failed += 1;
  }
  const runningTool = [...tools].reverse().find((tool) => tool.status === "running") || null;
  return {
    counts,
    files: [...files],
    edited: files.size || counts.edit,
    running,
    failed,
    total: tools.length,
    kind: runningTool ? toolKind(runningTool) : lastKind,
    primary: counts.edit ? "edit" : counts.command ? "command" : counts.search ? "search"
      : counts.read ? "read" : "other",
  };
}

export function threadBlocks(items = []) {
  const blocks = [];
  let tools = null;
  const separator = (item) => {
    if (!item?.at || blocks[blocks.length - 1]?.kind === "separator") return;
    blocks.push({ kind: "separator", key: `sep-${item.id}`, at: item.at });
  };
  items.forEach((item, index) => {
    if (item.type === "tool") {
      if (!tools) {
        tools = { kind: "tools", key: `tools-${item.id}`, items: [] };
        if (index === 0) separator(item);
        blocks.push(tools);
      }
      tools.items.push(item);
      return;
    }
    tools = null;
    if (item.type === "message" && item.role === "user") {
      separator(item);
      blocks.push({ kind: "user", key: item.id, item });
      return;
    }
    if (index === 0) separator(item);
    if (item.type === "message") blocks.push({ kind: "assistant", key: item.id, item });
    else if (item.type === "error") blocks.push({ kind: "error", key: item.id, item });
    else if (item.type === "permission") blocks.push({ kind: "permission", key: item.id, item });
    else if (item.type === "notice" && item.level === "reasoning") blocks.push({ kind: "reasoning", key: item.id, item });
    else blocks.push({ kind: "notice", key: item.id, item });
  });
  return blocks;
}

export function blockSignature(block = {}) {
  const parts = (item = {}) => [item.id, item.status, item.streaming ? 1 : 0,
    String(item.text || "").length, item.at || "", item.completed_at || "",
    item.output === null || item.output === undefined ? -1 : JSON.stringify(item.output).length,
    item.message || "", item.decided_at || ""].join(":");
  if (block.kind === "tools") return block.items.map(parts).join("|");
  if (block.kind === "separator") return `sep:${block.at}`;
  return parts(block.item);
}

export function liveActivity(items = [], session = {}) {
  if (!isActiveSession(session)) return null;
  const last = items[items.length - 1] || null;
  let phase = session.status === "idle" ? "waiting" : "working";
  if (session.status === "starting") phase = "starting";
  else if (session.status === "stopping") phase = "stopping";
  else if (last?.type === "notice" && last.level === "reasoning" && last.streaming) phase = "thinking";
  else if (last?.type === "tool" && last.status === "running") phase = toolKind(last) === "edit" ? "editing" : "running";
  else if (last?.type === "message" && last.role === "assistant" && last.streaming) phase = "writing";
  const userTurn = [...items].reverse().find((item) => item.type === "message" && item.role === "user" && item.at);
  const candidates = [userTurn?.at, session.started_at].filter(Boolean)
    .map((value) => new Date(value).getTime()).filter(Number.isFinite);
  const since = candidates.length ? new Date(Math.max(...candidates)).toISOString() : "";
  return { phase, since };
}

// ---------------------------------------------------------------- models
// Provider model catalog helpers.  ``capabilities`` is the provider catalog's
// capability record (``preset.provider_capabilities``): ``model_options`` rows
// are ``{id, label, source: configured|detected|builtin, default?, efforts?}``.

export const MODEL_SOURCE_ORDER = ["configured", "detected", "builtin"];
export const CUSTOM_MODEL_VALUE = "__custom__";

export function modelOptions(preset = {}, capabilities = {}) {
  const rows = Array.isArray(capabilities?.model_options) ? capabilities.model_options : [];
  const out = [];
  const seen = new Set();
  for (const id of preset?.models || []) {
    const value = String(id || "").trim();
    if (value && !seen.has(value)) {
      seen.add(value);
      out.push({ id: value, label: value, source: "configured" });
    }
  }
  for (const row of rows) {
    const value = String(row?.id || "").trim();
    if (!value) continue;
    if (seen.has(value)) {
      const existing = out.find((item) => item.id === value);
      if (existing && row.default) existing.default = true;
      if (existing && !existing.efforts && row.efforts) existing.efforts = row.efforts;
      continue;
    }
    seen.add(value);
    const source = MODEL_SOURCE_ORDER.includes(row.source) ? row.source : "detected";
    out.push({ ...row, id: value, label: String(row.label || value), source });
  }
  // Older catalogs only expose ``models`` as a list of ids.
  if (!rows.length) {
    for (const id of capabilities?.models || []) {
      const value = String(id || "").trim();
      if (value && !seen.has(value)) {
        seen.add(value);
        out.push({ id: value, label: value, source: "detected" });
      }
    }
  }
  return out;
}

export function defaultModelOption(options = []) {
  return options.find((item) => item.default) || null;
}

/** Groups for the picker: provider default first, then configured/detected/builtin. */
export function modelPickerGroups(options = [], current = "") {
  const groups = MODEL_SOURCE_ORDER
    .map((source) => ({
      source,
      items: options
        .filter((item) => item.source === source)
        .sort((a, b) => Number(Boolean(b.default)) - Number(Boolean(a.default))),
    }))
    .filter((group) => group.items.length);
  const value = String(current || "").trim();
  const known = !value || options.some((item) => item.id === value);
  return { groups, currentIsCustom: !known, current: value };
}

/** Reasoning levels valid for ``modelId``; mirrors SessionManager.effort_choices. */
export function modelEfforts(preset = {}, capabilities = {}, modelId = "") {
  if (preset?.efforts?.length) return [...preset.efforts];
  const options = modelOptions(preset, capabilities);
  const value = String(modelId || "").trim();
  const match = value ? options.find((item) => item.id === value) : defaultModelOption(options);
  if (match?.efforts?.length) return [...match.efforts];
  return [...(capabilities?.efforts || [])];
}

/** Short, language-neutral description of where the list came from. */
export function modelSourceSummary(capabilities = {}) {
  const methods = capabilities?.models_methods || [];
  const via = methods.find((item) => String(item).startsWith("cli:"))
    || methods.find((item) => String(item).startsWith("api:"))
    || methods.find((item) => String(item).startsWith("config:")) || "";
  return {
    source: capabilities?.models_source || "provider_default",
    via: String(via).replace(/^(cli|api|config):/, ""),
    kind: String(via).split(":")[0] || "",
    checkedAt: capabilities?.models_checked_at || "",
    detail: capabilities?.models_detail || "",
    reason: capabilities?.models_reason || "",
    error: capabilities?.models_error || "",
  };
}
