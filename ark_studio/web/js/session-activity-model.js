// Sub-agents and background tasks of one agent session, derived from its
// transcript alone (no extra requests; a resumed session rebuilds the same
// list).  Two kinds, kept apart everywhere:
//   - sub-agents: Task / Agent calls (in the foreground or the background)
//     and ARK sessions started as sub-agents of this one;
//   - background tasks: commands that run in the background (Bash with
//     run_in_background, a command moved to the background, monitors).
// Dependency-free (tests load it in a bare VM).
//
// Input: transcript items, or the reply's `activity` list (the same items
// without their outputs).  The server keeps Claude Code's task events on the
// tool call that started the task (`task`: id, type, status, background,
// description, last, summary, total_tokens, …); a task whose call is not in
// the transcript is an item {type: "task"}.  Other agents only have the
// calls: a run_in_background input, a KillShell / BashOutput call, an
// "<task-notification>" text.
//
// Entry: { key, id, kind: "subagent" | "background", source: "tool" | "task"
//   | "session", label, detail, model, status: running | completed | failed
//   | stopped, startedAt, endedAt, durationMs, lastOutput, tokens, itemId,
//   sessionId, background }

const AGENT_TOOLS = new Set(["task", "agent", "spawn_agent", "subagent", "dispatch_agent"]);
const COMMAND_TOOLS = new Set(["bash", "shell", "command", "exec", "execute", "terminal", "powershell", "monitor"]);
const KILL_TOOLS = new Set(["killshell", "killbash", "taskstop", "kill_shell", "stop_task"]);
const OUTPUT_TOOLS = new Set(["bashoutput", "taskoutput", "bash_output", "task_output"]);
const ACTIVE_SESSION = ["starting", "running", "idle", "stopping"];
const STATUS_ORDER = { running: 0, failed: 1, stopped: 2, completed: 3 };
// The ARK update watcher (feed listener) is plumbing: it shows in the
// background-task chip but does not keep a session busy.
const WATCHER_RE = /ark[-_ ]?watch|\/api\/agent\/v1\/updates|agentwatch|ark_wake/i;

/** Is this background entry the ARK watcher? */
export function isWatcherEntry(entry) {
  return entry?.kind === "background" && [entry.label, entry.detail, entry.command].some((text) => text && WATCHER_RE.test(text));
}

const lower = (value) => String(value ?? "").trim().toLowerCase();
const obj = (value) => (value && typeof value === "object" && !Array.isArray(value) ? value : {});

function textOf(value) {
  if (value == null) return "";
  if (typeof value === "string") return value;
  if (Array.isArray(value)) return value.map((part) => (typeof part === "string" ? part : part?.text || "")).join("\n");
  if (typeof value === "object" && typeof value.text === "string") return value.text;
  try { return JSON.stringify(value); } catch { return String(value); }
}

export function lastLine(value, limit = 200) {
  const lines = textOf(value).replace(/<\/?[a-z_-]+>/g, "\n").split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
  const line = lines[lines.length - 1] || "";
  return line.length > limit ? `${line.slice(0, limit - 1)}…` : line;
}

function firstLine(value, limit = 120) {
  const line = textOf(value).split(/\r?\n/).map((part) => part.trim()).find(Boolean) || "";
  return line.length > limit ? `${line.slice(0, limit - 1)}…` : line;
}

/** completed | failed | stopped | running from the many spellings agents use. */
export function normalizeTaskStatus(value) {
  const status = lower(value);
  if (!status) return "";
  if (["completed", "complete", "done", "success", "succeeded", "ok", "finished", "exited"].includes(status)) return "completed";
  if (["failed", "error", "errored", "failure", "timeout", "timed_out"].includes(status)) return "failed";
  if (["killed", "stopped", "cancelled", "canceled", "interrupted", "aborted"].includes(status)) return "stopped";
  if (["running", "started", "in_progress", "pending", "queued", "async_launched", "launched"].includes(status)) return "running";
  return "";
}

function backgroundId(item) {
  const meta = obj(item.meta);
  const task = obj(item.task);
  const direct = task.id || meta.backgroundTaskId || meta.background_task_id || meta.shellId || meta.bash_id || meta.task_id
    || meta.agentId || meta.agent_id;
  if (direct) return String(direct);
  const output = textOf(item.output);
  const match = output.match(/\bwith ID:\s*([A-Za-z0-9_.:-]+?)[.,]?(?:\s|$)/i) || output.match(/\bagentId:\s*([A-Za-z0-9_.:-]+)/i)
    || output.match(/\b(?:shell|task) id:\s*([A-Za-z0-9_.:-]+)/i);
  return match ? match[1] : "";
}

function targetId(input) {
  const src = obj(input);
  return String(src.shell_id || src.bash_id || src.task_id || src.agent_id || src.id || "");
}

function outputStatus(output) {
  const text = textOf(output);
  const tag = text.match(/<status>\s*([a-z_]+)\s*<\/status>/i);
  if (tag) return normalizeTaskStatus(tag[1]);
  const field = text.match(/"status"\s*:\s*"([a-z_]+)"/i) || text.match(/\bstatus:\s*([a-z_]+)/i);
  return field ? normalizeTaskStatus(field[1]) : "";
}

function durationMs(start, end, now) {
  const from = Date.parse(start || "");
  const to = end ? Date.parse(end) : now;
  return Number.isFinite(from) && Number.isFinite(to) ? Math.max(0, to - from) : null;
}

/** Is this tool item a sub-agent call, a background command, or neither? */
export function toolActivityKind(item) {
  const name = lower(item?.name);
  const task = obj(item?.task);
  const input = obj(item?.input);
  if (AGENT_TOOLS.has(name) || lower(task.type) === "agent") return "subagent";
  const background = Boolean(task.background || input.run_in_background || obj(item?.meta).isAsync);
  if (background && (COMMAND_TOOLS.has(name) || lower(task.type) === "command" || !AGENT_TOOLS.has(name))) return "background";
  return "";
}

/**
 * Sub-agents and background tasks of `session` from its transcript `items`;
 * `sessions` (optional) adds ARK child sessions.  Returns
 * { entries, subagents, background, runningSubagents, runningBackground }.
 */
export function sessionActivity(items = [], { session = null, sessions = [], now = Date.now() } = {}) {
  const entries = [];
  const byTask = new Map();   // task / shell id -> entry
  const sessionActive = !session || ACTIVE_SESSION.includes(session.status);

  const remember = (entry) => {
    entries.push(entry);
    if (entry.id) byTask.set(entry.id, entry);
    return entry;
  };
  const finish = (entry, status, at, summary = "") => {
    if (!entry || !status) return;
    entry.status = status;
    if (status !== "running") entry.endedAt = at || entry.endedAt || null;
    if (summary) entry.lastOutput = lastLine(summary);
  };

  for (const item of items || []) {
    if (!item) continue;
    if (item.type === "task") {
      // A task whose call is not in this transcript (a resumed session).
      const task = obj(item.task);
      const id = String(task.id || "");
      const existing = id ? byTask.get(id) : null;
      if (existing) { finish(existing, normalizeTaskStatus(task.status), task.ended_at, task.summary); continue; }
      const kind = lower(task.type) === "agent" ? "subagent" : "background";
      remember({
        key: `task:${id || item.id}`, id, kind, source: "task", background: kind === "background" || Boolean(task.background),
        label: firstLine(task.description || task.summary || id), detail: String(task.subagent_type || ""), model: "",
        status: normalizeTaskStatus(task.status) || "running", startedAt: task.started_at || item.at || null,
        endedAt: task.ended_at || null, lastOutput: lastLine(task.summary || task.last || ""),
        tokens: Number(task.total_tokens) || null, itemId: item.id,
      });
      continue;
    }
    if (item.type === "notice" && item.level === "task") {
      // Older transcripts kept task events as notices.
      const entry = byTask.get(String(item.task_id || ""));
      finish(entry, normalizeTaskStatus(item.task_status), item.at, item.summary || item.text);
      continue;
    }
    if (item.type !== "tool") continue;
    const name = lower(item.name);
    const input = obj(item.input);
    if (KILL_TOOLS.has(name)) {
      if (item.status !== "error") finish(byTask.get(targetId(input)), "stopped", item.completed_at || item.at);
      continue;
    }
    if (OUTPUT_TOOLS.has(name)) {
      const entry = byTask.get(targetId(input));
      if (entry && item.output != null) {
        const line = lastLine(item.output);
        if (line) entry.lastOutput = line;
        finish(entry, outputStatus(item.output), item.completed_at || item.at);
      }
      continue;
    }
    const kind = toolActivityKind(item);
    if (!kind) continue;
    const task = obj(item.task);
    const meta = obj(item.meta);
    const background = kind === "background" || Boolean(task.background || input.run_in_background || meta.isAsync);
    const toolStatus = item.status === "error" ? "failed" : item.status === "running" ? "running" : "completed";
    // A task record knows best; a background call returned at once, so
    // without one it runs until a notification says otherwise.
    let status = normalizeTaskStatus(task.status) || (background ? (toolStatus === "failed" ? "failed" : "running") : toolStatus);
    if (!normalizeTaskStatus(task.status) && !background) status = toolStatus;
    const entry = {
      key: `tool:${item.id}`, id: backgroundId(item), kind, source: "tool", background,
      label: kind === "subagent" ? firstLine(input.description || task.description || input.prompt || item.name)
        : firstLine(input.description || task.description || input.command || item.name),
      command: String(input.command || ""),
      detail: kind === "subagent" ? String(input.subagent_type || input.agent_type || task.subagent_type || "") : firstLine(input.command || ""),
      model: String(input.model || meta.model || ""),
      status, startedAt: task.started_at || item.at || null,
      endedAt: status === "running" ? null : (task.ended_at || item.completed_at || null),
      lastOutput: lastLine(task.summary || "") || String(item.last_output || task.last || item.child_tool || ""),
      tokens: Number(meta.totalTokens || task.total_tokens || item.child_tokens) || null,
      itemId: item.id,
    };
    if (!entry.lastOutput && status !== "running" && item.output != null && !background) entry.lastOutput = lastLine(item.output);
    remember(entry);
  }

  // When the session has ended, nothing it started is still running.
  if (!sessionActive) {
    for (const entry of entries) if (entry.status === "running") finish(entry, "stopped", session?.ended_at || null);
  }

  // ARK sessions launched as sub-agents of this one.
  if (session?.id) {
    for (const child of sessions || []) {
      if (!child || child.parent_id !== session.id) continue;
      const status = ACTIVE_SESSION.includes(child.status) ? "running"
        : child.status === "errored" ? "failed" : child.status === "stopped" && child.exit_code != null && child.exit_code !== 0 ? "stopped" : "completed";
      entries.push({
        key: `session:${child.id}`, id: child.id, kind: "subagent", source: "session", background: false,
        label: child.title || child.id, detail: child.platform_label || child.platform || "", model: child.model || "",
        status, startedAt: child.started_at || child.created_at || null, endedAt: child.ended_at || null,
        lastOutput: child.error || "", tokens: null, itemId: "", sessionId: child.id,
      });
    }
  }

  for (const entry of entries) entry.durationMs = durationMs(entry.startedAt, entry.endedAt, now);
  const subagents = entries.filter((entry) => entry.kind === "subagent");
  const background = entries.filter((entry) => entry.kind === "background");
  for (const entry of background) if (isWatcherEntry(entry)) entry.watcher = true;
  const runningBackground = background.filter((entry) => entry.status === "running");
  return {
    entries, subagents, background,
    runningSubagents: subagents.filter((entry) => entry.status === "running").length,
    runningBackground: runningBackground.length,
    // what keeps the session busy between turns (the watcher does not)
    busyBackground: runningBackground.filter((entry) => !entry.watcher).length,
  };
}

/** Entries of one kind ("subagent" | "background" | "all"): running first, newest first. */
export function activityList(activity, kind = "all") {
  const list = (activity?.entries || []).filter((entry) => kind === "all" || entry.kind === kind);
  return list.slice().sort((a, b) => (STATUS_ORDER[a.status] ?? 9) - (STATUS_ORDER[b.status] ?? 9)
    || String(b.startedAt || "").localeCompare(String(a.startedAt || "")));
}

/** Cheap change detector for repainting chips and lists. */
export function activitySignature(activity) {
  return (activity?.entries || []).map((entry) => [entry.key, entry.status, entry.lastOutput, entry.tokens || ""].join(":")).join("|");
}

/** The transcript block (threadBlocks key) that holds item `itemId`. */
export function blockKeyFor(blocks = [], itemId = "") {
  if (!itemId) return "";
  for (const block of blocks) {
    if (block.key === itemId || block.item?.id === itemId) return block.key;
    if (block.items?.some((item) => item.id === itemId)) return block.key;
  }
  return "";
}

/** A tool item's task line for its row: "Background · running · Reading p3.md". */
export function taskBadge(item) {
  const task = obj(item?.task);
  const kind = toolActivityKind(item);
  if (!kind && !task.id) return null;
  const status = normalizeTaskStatus(task.status) || (item?.status === "running" ? "running" : "");
  return {
    kind: kind || "command", background: Boolean(task.background || obj(item?.input).run_in_background),
    status, text: lastLine(task.summary || task.last || item?.last_output || item?.child_tool || "", 160),
    tokens: Number(obj(item?.meta).totalTokens || task.total_tokens || item?.child_tokens) || null,
  };
}
