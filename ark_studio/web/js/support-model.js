// What supports the work of a project, for the Board's "Workspace support"
// panel and the Home "Agents and hosts" card (support-panel.js).  The data
// is GET /api/project/support (routes_support.go): the project's ARK
// sessions, agents that wrote through the agent API, hosts from the
// Resources graph and setup, library counts and deadlines.  Units the page
// already has add due dates and the agent / host / resource nodes of older
// projects.  Pure, no DOM, no imports.

const RUNNING = new Set(["starting", "running", "stopping"]);
const CLOSED_UNIT = new Set(["done", "archived", "cancelled", "dropped"]);
const STATE_ORDER = { running: 0, approval: 1, review: 2, error: 3, idle: 4, ended: 5 };
const HOST_ORDER = { ark: 0, host: 1, "gpu-server": 2, compute: 3, node: 4 };
const DAY = 86_400_000;

/** Display state of an ARK session: running, approval, review, error, idle or ended. */
export function sessionState(session, unitStatus = "") {
  if (!session) return "ended";
  const status = String(session.status || "");
  if (status === "errored" || status === "conflict") return "error";
  if (Number(session.approvals_pending || 0) > 0) return "approval";
  if (RUNNING.has(status) || (status === "idle" && session.busy)) return "running";
  if (unitStatus === "review") return "review";
  if (status === "idle") return "idle";
  return "ended";
}

function norm(text) {
  return String(text || "").split("·")[0].trim().toLowerCase();
}

function unitIndex(units) {
  const byUid = new Map();
  for (const u of units || []) if (u && u.uid) byUid.set(String(u.uid), u);
  return byUid;
}

/** The unit a session works on: its task, else its first linked unit. */
export function sessionUnit(session, byUid) {
  const refs = [session?.task_ref, ...(Array.isArray(session?.linked_units) ? session.linked_units : [])];
  for (const ref of refs) {
    const uid = typeof ref === "string" ? ref : ref?.uid;
    if (!uid) continue;
    const unit = byUid.get(String(uid));
    const fallback = typeof ref === "object" ? ref.title || "" : ref === session.task_ref ? session.task_title || "" : "";
    return { uid: String(uid), title: unit?.title || fallback, status: unit?.status || "" };
  }
  return null;
}

function hostLabels(support) {
  const out = new Map();
  for (const host of support?.hosts || []) out.set(String(host.id), String(host.label || host.id));
  return out;
}

/**
 * Agent rows: ARK sessions (running first), then agents that wrote through
 * the agent API, then agent nodes of older projects that match neither.
 * Token agents the server linked to a session ("linked_agents") are part of
 * that session's row (`writes`, `viaToken`), not rows of their own; an
 * unlinked one that probably is a session keeps its row with `likely`.
 * `legacy` are the project's agent-type units.
 */
export function agentRows(support, { units = [], legacy = [] } = {}) {
  const byUid = unitIndex(units);
  const hosts = hostLabels(support);
  const sessions = (support?.agents?.sessions || []).map((s) => {
    // Token agents the server tied to this session (its own token or an
    // X-ARK-Session header): the same agent, so one row with their writes.
    const linked = Array.isArray(s.linked_agents) ? s.linked_agents : [];
    const latest = linked.reduce((best, a) => (!best || String(a.last_at || "") > String(best.last_at || "") ? a : best), null);
    let unit = sessionUnit(s, byUid);
    if (!unit && latest) {
      const uid = latest.live?.unit_uid || latest.unit_uid || "";
      if (uid) unit = { uid: String(uid), title: String((latest.live?.unit_uid ? latest.live.unit_title : latest.unit_title) || byUid.get(String(uid))?.title || ""), status: byUid.get(String(uid))?.status || "" };
    }
    const lastWrite = latest ? String(latest.last_at || "") : "";
    const at = s.last_activity_at || s.created_at || "";
    return {
      key: `session:${s.id}`, kind: "session", id: String(s.id), title: String(s.title || s.id),
      provider: String(s.platform_label || s.platform || ""), model: String(s.model || ""),
      state: sessionState(s, unit?.status), unit, host: hosts.get(String(s.device || "ark")) || "",
      at: lastWrite > String(at) ? lastWrite : at, children: Number(s.children || 0),
      writes: linked.reduce((n, a) => n + Number(a.writes || 0), 0), viaToken: linked.length > 0,
      busy: s.busy ? { subagents: Number(s.busy.subagents || 0), background: Number(s.busy.background || 0) } : null,
      error: String(s.error || ""),
    };
  }).sort((a, b) => (STATE_ORDER[a.state] - STATE_ORDER[b.state]) || String(b.at).localeCompare(String(a.at)));
  const external = (support?.agents?.external || []).map((a) => {
    const live = a.live || null;
    const uid = live?.unit_uid || a.unit_uid || "";
    return {
      key: `agent:${a.actor}`, kind: "external", id: String(a.actor), title: String(a.label || a.actor),
      provider: "", model: "", state: live ? (live.kind === "working" ? "working" : "active") : "recent",
      unit: uid ? { uid: String(uid), title: String((live?.unit_uid ? live.unit_title : a.unit_title) || byUid.get(String(uid))?.title || "") } : null,
      host: "", at: a.last_at || live?.at || "", writes: Number(a.writes || 0), note: String(live?.note || ""),
      // Not tied to a session for certain; the server's guess is only named.
      likely: a.likely_session?.id ? { id: String(a.likely_session.id), title: String(a.likely_session.title || a.likely_session.id) } : null,
    };
  }).sort((a, b) => String(b.at).localeCompare(String(a.at)));
  const names = new Set([...sessions, ...external].flatMap((r) => [norm(r.title), norm(r.id.replace(/^agent:/, ""))]));
  const nodes = (legacy || []).filter((u) => u && !names.has(norm(u.title))).map((u) => ({
    key: `node:${u.uid}`, kind: "node", id: String(u.uid), title: String(u.title || u.uid).split("·")[0].trim(),
    provider: "", model: "", state: "node", unit: null, host: "", at: u.updated_at || "",
  }));
  return [...sessions, ...external, ...nodes];
}

/**
 * Host rows: the ARK server, setup devices, GPU servers and compute from the
 * reply, then host nodes of older projects.  A host node whose title names
 * a listed host (label or device id) is merged into it (`unit`).
 */
export function hostRows(support, { legacy = [] } = {}) {
  const rows = (support?.hosts || []).map((x) => ({
    key: String(x.id), id: String(x.id), kind: String(x.kind || "host"), label: String(x.label || x.id),
    state: String(x.state || "unknown"), lastSeen: x.last_seen || "", sessions: Number(x.sessions_running || 0),
    gpu: x.gpu && Number.isFinite(Number(x.gpu.total)) ? { total: Number(x.gpu.total || 0), free: Number(x.gpu.free || 0) } : null,
    jobs: x.jobs ? { running: Number(x.jobs.running || 0), queued: Number(x.jobs.queued || 0) } : null,
    probedAt: x.probed_at || "", current: x.current === true, deviceId: String(x.device_id || ""),
    pathsMissing: x.paths_missing == null ? null : Number(x.paths_missing), mine: x.mine === true, unit: null,
  }));
  const byName = new Map();
  for (const row of rows) {
    byName.set(norm(row.label), row);
    if (row.deviceId) byName.set(norm(row.deviceId), row);
  }
  const nodes = [];
  for (const u of legacy || []) {
    if (!u) continue;
    const match = byName.get(norm(u.title));
    if (match) { if (!match.unit) match.unit = String(u.uid); continue; }
    nodes.push({
      key: `node:${u.uid}`, id: String(u.uid), kind: "node", label: String(u.title || u.uid), state: "node", lastSeen: "",
      sessions: 0, gpu: null, jobs: null, probedAt: "", current: false, deviceId: "", pathsMissing: null, mine: false,
      unit: String(u.uid), tags: (u.tags || []).filter((tag) => /^(os|gpu):/.test(tag)).map((tag) => tag.split(":")[1]),
    });
  }
  rows.sort((a, b) => (HOST_ORDER[a.kind] ?? 9) - (HOST_ORDER[b.kind] ?? 9)
    || (a.state === "online" ? 0 : 1) - (b.state === "online" ? 0 : 1)
    || String(b.lastSeen).localeCompare(String(a.lastSeen)));
  return [...rows, ...nodes];
}

/** Library counts: artifacts (units) and the setup resources by group. */
export function libraryCounts(support, units = []) {
  const lib = support?.library || {};
  const n = (k) => Number(lib[k] || 0);
  const artifacts = (units || []).filter((u) => u?.type === "artifact").length;
  const resourceNodes = (units || []).filter((u) => u?.type === "resource").length;
  const repositories = n("repository");
  const workspaces = n("task_space") + n("worktree");
  const storage = n("storage") + n("database");
  const other = n("environment") + n("compute");
  return {
    artifacts, resourceNodes, repositories, workspaces, storage, other,
    resources: repositories + n("task_space") + storage + other,
    total: artifacts + resourceNodes + repositories + n("task_space") + storage + other,
    bytes: Number.isFinite(Number(lib.bytes)) && lib.bytes != null ? Number(lib.bytes) : null,
  };
}

/**
 * Deadlines, soonest (or longest waiting) first: units due within
 * `days` or overdue, then what the server lists — agent tokens that expire,
 * GPU jobs' time limits, reviews that have waited.  Each row has `at` (ISO)
 * and `overdue`.
 */
export function deadlineRows(support, units = [], { now = Date.now(), days = 14, limit = 6 } = {}) {
  const rows = [];
  const horizon = now + days * DAY;
  for (const u of units || []) {
    if (!u?.end_at || CLOSED_UNIT.has(u.status) || u.archived) continue;
    const at = Date.parse(u.end_at);
    if (!Number.isFinite(at) || at > horizon) continue;
    rows.push({ key: `unit:${u.uid}`, kind: "unit", uid: String(u.uid), label: String(u.title || u.uid), at: u.end_at, overdue: String(u.end_at).slice(0, 10) < localDay(now) });
  }
  for (const d of support?.deadlines || []) {
    const at = Date.parse(d.at || "");
    if (!Number.isFinite(at)) continue;
    if (d.kind === "token") {
      rows.push({ key: `token:${d.id}`, kind: "token", id: String(d.id), label: String(d.label || d.prefix || d.id), at: d.at, overdue: false, starter: d.starter === true });
    } else if (d.kind === "gpu_job") {
      rows.push({ key: `gpu:${d.id}`, kind: "gpu_job", id: String(d.id), label: String(d.label || d.id), server: String(d.server || ""), uid: d.task || "", at: d.at, overdue: at < now });
    } else if (d.kind === "review") {
      rows.push({ key: `review:${d.uid}`, kind: "review", uid: String(d.uid), label: String(d.label || d.uid), at: d.at, overdue: false,
        waitDays: Math.max(1, Math.floor((now - at) / DAY)) });
    }
  }
  rows.sort((a, b) => Date.parse(a.at) - Date.parse(b.at));
  return rows.slice(0, limit);
}

function localDay(ms) {
  const d = new Date(ms);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

/** Days from now to `iso` (ceil; 0 today, negative when past). */
export function daysUntil(iso, now = Date.now()) {
  const at = Date.parse(iso || "");
  if (!Number.isFinite(at)) return null;
  return Math.ceil((at - now) / DAY);
}

/** Header counts from the same rows the cards show. */
export function supportSummary(agents, hosts, library) {
  return {
    agents: (agents || []).filter((r) => r.kind !== "session" || r.state !== "ended").length,
    hosts: (hosts || []).length,
    library: library?.total || 0,
  };
}

/** Counts of the session states shown in the agents card head. */
export function sessionCounts(agents) {
  const out = { running: 0, approval: 0, review: 0, error: 0, idle: 0, external: 0 };
  for (const r of agents || []) {
    if (r.kind === "external") out.external += 1;
    else if (r.kind === "session" && r.state in out) out[r.state] += 1;
  }
  return out;
}

/**
 * The empty line of a card: an i18n key and, for people who may act, the
 * action that fixes it ("launch" opens the Run dialog, "settings" opens
 * Settings › Resources).  null when the card has rows.
 */
export function cardEmpty(card, { rows = 0, canWrite = false, loading = false } = {}) {
  if (rows > 0) return null;
  if (loading) return { key: "support.loading", action: null };
  switch (card) {
    case "agents": return { key: "support.agents.empty", action: canWrite ? "launch" : null };
    case "hosts": return { key: "support.hosts.empty", action: canWrite ? "settings" : null };
    case "library": return { key: "support.library.empty", action: canWrite ? "settings" : null };
    case "due": return { key: "support.due.empty", action: null };
    default: return { key: "support.empty", action: null };
  }
}

/** Human size: bytes to { n, unit } with unit B, KB, MB, GB or TB. */
export function sizeParts(bytes) {
  if (bytes == null || !Number.isFinite(Number(bytes))) return null;
  let n = Number(bytes);
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i += 1; }
  return { n: i === 0 ? Math.round(n) : Math.round(n * 10) / 10, unit: units[i] };
}
