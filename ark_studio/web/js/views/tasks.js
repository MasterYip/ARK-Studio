import { h, clear, emptyState, statusColor, impDot, tagChip, fmtDate, relativeTime, dueState, avatarFor } from "../dom.js";
import { t, typeLabel, statusLabel } from "../i18n.js";
import { icon } from "../icons.js";
import { GanttView, ganttControls } from "../gantt.js";
import { unitCard, viewToggle, openLinkModal, openDispatchModal, typeLegend, openModal, confirmDialog, canRunWithAgent } from "../components.js";
import { gpuRegistrationNote, openSetupResources } from "../gpu-note.js";
import { sortUnits } from "./ideas.js";
import { renderSessionWorkspace } from "../session-workspace.js";
import { shellActive } from "../workbench.js";
import { sessionStream } from "../session-stream.js";
import { rememberSessionChoice } from "../session-sidebar-model.js";
import { sessionLibraryPanel } from "../session-library.js";
import { api, cachedThenFresh } from "../api.js";
import { gpuOwnership } from "../gpu-model.js";
import { resourcesGraphPanel } from "../resources-view.js";
import { advancedEnabled } from "../advanced.js";
import { PERMISSION_MODES, permissionModeLabel, permissionModeDescription } from "../session-model.js";
import { assigneeChip } from "../assignee.js";

const COLUMNS = ["inbox", "open", "active", "review", "blocked", "done"];
const SESSION_ACTIVE = ["starting", "running", "idle", "stopping"];

let sessionsTimer = null;     // unsubscribes the Sessions page from the shared stream
let gpuTimer = null;

function stopSessionsTimer() {
  if (sessionsTimer) {
    sessionsTimer();
    sessionsTimer = null;
  }
}

export function stopTaskSessions() {
  stopSessionsTimer();
  if (gpuTimer) {
    clearInterval(gpuTimer);
    gpuTimer = null;
  }
}

export function renderTasks(host, ctx) {
  stopSessionsTimer();
  if (gpuTimer) { clearInterval(gpuTimer); gpuTimer = null; }
  const S = ctx.S;
  // Execute › Files moved into the Editor.
  if (S.taskView === "files") S.taskView = "board";
  clear(host);
  const tasks = (S.units || []).filter((u) => u.type === "task");
  if (!tasks.length && S.taskView === "gpu") S.taskView = "sessions";
  const agents = (S.units || []).filter((u) => u.type === "agent");
  const hosts = (S.units || []).filter((u) => u.type === "host");
  const artifacts = (S.units || []).filter((u) => ["artifact", "resource"].includes(u.type));
  const sessions = S.taskSessions || [];
  const activeSessions = sessions.filter((s) => SESSION_ACTIVE.includes(s.status)).length;
  const phone = typeof window.matchMedia === "function"
    && window.matchMedia("(max-width: 760px)").matches;
  const sessionOnly = phone && S.taskView === "sessions";
  // Execute › Runs on a wide window: a compact header, the conversation below.
  const fullSession = S.taskView === "sessions" && shellActive();
  const page = h("div", { class: `page ${sessionOnly ? "mobile-session-page" : ""} ${fullSession ? "session-full-page" : ""}`.trim() },
    h("div", { class: "page-head" },
      h("div", null,
        h("div", { class: "eyebrow" }, t("nav.execute")),
        h("h1", { class: "page-title" }, executeViewTitle(S.taskView)),
        h("p", { class: "page-sub" }, t("tasks.subtitle"))),
      h("div", { class: "head-actions" },
        h("button", { class: "ghost-btn", onclick: () => ctx.openAssistant() }, icon("check", "sm"), t("assistant.title")),
        advancedEnabled() ? h("button", { class: "ghost-btn", onclick: () => ctx.openImport() }, icon("database", "sm"), t("palette.cmd.import")) : null,
        // Advanced: ARK starts and supervises the process; the recommended
        // path is the user's own agent (Home prompt), so New task leads.
        h("button", { class: "ghost-btn", "data-launch-agent": "", title: t("tasks.launch_hint"), onclick: () => openLaunchSession(ctx) }, icon("play", "sm"), t("tasks.launch")),
        h("button", { class: "primary-btn", onclick: () => ctx.openCreate({ type: "task", status: "open" }) }, icon("plus", "sm"), t("tasks.add")))),
    S.taskView === "sessions" ? sessionsView(ctx, S, tasks) :
      S.taskView === "gpu" ? gpuResourcesPanel(ctx, S, tasks) :
      S.taskView === "resources" ? resourcesGraphPanel(ctx) :
      S.taskView === "board" ? boardView(ctx, S, tasks, agents, hosts, artifacts) :
      S.taskView === "timeline" ? timelineView(ctx, S, tasks) :
      S.taskView === "agents" ? agentsView(ctx, S, agents, hosts, tasks) :
      libraryView(ctx, S, artifacts));
  host.appendChild(page);
}

// --------------------------------------------------------- GPU task monitor
// The page title names the Execute sub-tab that is open (nav name = title).
function executeViewTitle(view) {
  if (view === "sessions") return t("app.subtab.runs");
  if (view === "gpu") return "GPU"; // i18n-ignore: product term, same in both languages
  if (view === "resources") return t("resources.tab");
  if (view === "agents") return t("tasks.agents");
  if (view === "library") return t("tasks.library");
  return t("view.board");
}

function gpuResourcesPanel(ctx, S, tasks) {
  const body = h("div", { class: "task-gpu-body" },
    h("div", { class: "empty sm" }, t("tasks.gpu.loading")));
  const stamp = h("span", { class: "physics-hint" }, t("tasks.gpu.probe"));
  const panel = h("section", { class: "panel task-gpu-panel" },
    h("div", { class: "panel-head" },
      h("h3", null, icon("cpu", "sm"), ` ${t("tasks.gpu.title")}`),
      h("div", { class: "row tight" }, stamp,
        h("button", {
          class: "ghost-btn sm", type: "button",
          onclick: () => refresh(true),
        }, icon("refresh", "sm"), t("action.refresh")))),
    h("p", { class: "settings-lead" }, t("tasks.gpu.lead")),
    h("div", { class: "physics-hint", style: { marginTop: "-4px", marginBottom: "8px" } },
      t("tasks.gpu.legend")),
    body);
  let busy = false;
  async function refresh(manual = false, first = false) {
    if (busy) return;
    if (!panel.isConnected) { stopGpuTimer(); return; }
    busy = true;
    const show = (data) => {
      render(data);
      stamp.textContent = data.checked_at
        ? t("tasks.gpu.checked", { time: relativeTime(data.checked_at) }) : t("tasks.gpu.probe");
    };
    try {
      // First paint from the last probe this account saw (its time is shown).
      if (first) await cachedThenFresh("/api/gpu/dashboard", (options) => api.gpuDashboard(options), show);
      else show(await api.gpuDashboard({ fresh: true }));
    } catch (err) {
      clear(body);
      body.appendChild(h("div", { class: "settings-note" }, icon("info", "sm"),
        h("div", null, err.message)));
      stamp.textContent = t("tasks.gpu.unavailable");
      if (manual) ctx.toast?.(err.message, "err");
    } finally { busy = false; }
  }
  function stopGpuTimer() {
    if (gpuTimer) { clearInterval(gpuTimer); gpuTimer = null; }
  }
  function taskFor(job) {
    if (!job.task) return null;
    return tasks.find((task) => String(task.uid || task.id) === String(job.task)
      || String(task.id) === String(job.task) || String(task.uid) === String(job.task)) || null;
  }
  function activeJobs(jobs) {
    return jobs.filter((job) => ["starting", "running"].includes(job.status));
  }
  function render(data) {
    clear(body);
    const barColor = (pct) => pct >= 85 ? "#ef4444" : pct >= 60 ? "#f59e0b" : "#22c55e";
    const trackBg = "rgba(148,163,184,0.28)";
    const probe = data.probe || {};
    const servers = (probe.servers || []).filter((server) => server.ok);
    const jobs = data.jobs || [];
    const active = activeJobs(jobs);
    const queued = jobs.filter((job) => job.status === "queued");
    if (!servers.length) {
      // T-0018: no GPU server registered in this project's setup.
      body.appendChild(gpuRegistrationNote(data.registration, () => openSetupResources(ctx))
        || h("div", { class: "settings-note" }, icon("alert", "sm"), h("div", null, t("tasks.gpu.no_server"))));
      return;
    }
    const grid = h("div", { class: "task-gpu-grid" });
    for (const server of servers) {
      const system = server.system || {};
      const cpu = system.cpu_percent ?? system.cpu_load_pct;
      const memTotal = system.memory_total_mb;
      const memUsed = system.memory_used_mb;
      const memPct = memTotal ? Math.min(100, memUsed / memTotal * 100) : null;
      const serverJobs = active.filter((job) => job.server === server.server);
      const card = h("div", { class: "task-gpu-server" },
        h("div", { class: "task-gpu-server-head" },
          h("div", null,
            h("b", null, server.server),
            // Only the server admin (or local mode) gets the address (T-0018).
            server.host ? h("div", { class: "physics-hint" },
              `${server.user ? server.user + "@" : ""}${server.host}:${server.port}`) : null),
          !server.host ? null : h("div", { class: "row tight" },
            h("a", {
              class: "task-gpu-link", href: `ssh://${server.user ? server.user + "@" : ""}${server.host}:${server.port || 22}`,
              title: t("tasks.gpu.open_ssh"),
            }, icon("command", "sm"), "SSH"),
            h("a", {
              class: "task-gpu-link",
              href: `vscode://vscode-remote/ssh-remote+${server.user ? server.user + "@" : ""}${server.host}:${server.port || 22}/`,
              title: t("tasks.gpu.open_vscode"),
            }, icon("external", "sm"), "VS Code"))),
        h("div", { class: "task-gpu-meter", style: { display: "flex", alignItems: "center", gap: "7px", margin: "5px 0" } },
          h("span", { style: { color: "var(--sub)", fontSize: "9px", fontWeight: "600", width: "26px" } }, "CPU"),
          h("div", { style: { position: "relative", flex: "1", minWidth: "0", height: "8px", borderRadius: "4px", background: trackBg, overflow: "hidden" } },
            h("i", { style: { position: "absolute", left: "0", top: "0", height: "100%", borderRadius: "4px", background: barColor(cpu == null ? 0 : cpu), width: `${cpu == null ? 0 : cpu}%` } })),
          h("b", { style: { fontSize: "9px", minWidth: "32px", textAlign: "right" } }, cpu == null ? "—" : `${Math.round(cpu)}%`)),
        h("div", { class: "task-gpu-meter", style: { display: "flex", alignItems: "center", gap: "7px", margin: "5px 0" } },
          h("span", { style: { color: "var(--sub)", fontSize: "9px", fontWeight: "600", width: "26px" } }, "RAM"), // i18n-ignore: unit abbreviation
          h("div", { style: { position: "relative", flex: "1", minWidth: "0", height: "8px", borderRadius: "4px", background: trackBg, overflow: "hidden" } },
            h("i", { style: { position: "absolute", left: "0", top: "0", height: "100%", borderRadius: "4px", background: barColor(memPct == null ? 0 : memPct), width: `${memPct == null ? 0 : memPct}%` } })),
          h("b", { style: { fontSize: "9px", whiteSpace: "nowrap", textAlign: "right" } }, memTotal ? `${(memUsed / 1024).toFixed(1)} / ${(memTotal / 1024).toFixed(1)}G` : "—")),
        h("div", { class: "task-gpu-chips", style: { display: "grid", gridTemplateColumns: "repeat(2, minmax(0, 1fr))", gap: "6px", marginTop: "8px" } },
          ...(server.gpus || []).map((gpu) => {
            const used = gpu.memory_used_mb || 0;
            const total = gpu.memory_total_mb || 0;
            const own = gpuOwnership(gpu, system.processes || [], serverJobs,
              { processUtilAvailable: Boolean(system.process_util_available) });
            const memPct = own.totalMemPct;
            const util = own.totalUtilPct;
            const ownedMemPct = own.memPct;
            const ownedGpuPct = own.utilPct;
            const live = own.jobs.map((job) => taskFor(job)?.title || job?.task || job?.job).filter(Boolean);
            const ownNote = live.length ? [
              t("tasks.gpu.own_mem", { gb: Math.round(own.memMb / 1024 * 10) / 10 }),
              t(own.utilEstimated ? "tasks.gpu.own_util_estimated" : "tasks.gpu.own_util_measured",
                { pct: Math.round(own.utilPct) }),
              own.unknownMemory ? t("tasks.gpu.own_unknown_mem", { n: own.unknownMemory }) : "",
            ].filter(Boolean).join(" · ") : "";
            const metric = (label, pct, owned, className = "") =>
              h("div", {
                class: `task-gpu-metric ${className}`.trim(),
                style: { display: "grid", gridTemplateColumns: "28px 1fr", gap: "6px", alignItems: "center", marginTop: "6px" },
              },
                h("span", { style: { color: "var(--sub)", fontWeight: "600", fontSize: "9px" } }, label),
                h("div", { style: { position: "relative", minWidth: "0", height: "8px", borderRadius: "4px", background: trackBg, overflow: "hidden" } },
                  h("i", { style: { position: "absolute", left: "0", top: "0", height: "100%", borderRadius: "4px", background: barColor(pct), width: `${Math.max(0, Math.min(100, pct))}%` } }),
                  h("i", { style: { position: "absolute", left: "0", top: "0", height: "100%", borderRadius: "4px", background: "var(--blue)", zIndex: 2, width: `${Math.max(0, Math.min(100, owned))}%` } })));
            const memText = total ? `${(used / 1024).toFixed(1)}/${(total / 1024).toFixed(0)}G` : "—";
            return h("div", {
              class: `task-gpu-chip ${live.length ? "active" : ""}`.trim(),
              title: live.length
                ? t("tasks.gpu.in_use", { tasks: live.join(", "), note: ownNote })
                : t("tasks.gpu.free", { name: gpu.name || "GPU", mb: Math.round(gpu.free_memory_mb || 0) }),
            },
              h("div", { class: "task-gpu-chip-head", style: { display: "flex", alignItems: "baseline", justifyContent: "space-between", gap: "6px", marginBottom: "2px" } },
                h("b", { style: { fontSize: "10px", whiteSpace: "nowrap" } }, `GPU ${gpu.index}`),
                h("span", { style: { color: "var(--sub)", fontSize: "8.5px", whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" } },
                  `${Math.round(util)}% · ${memText}`)),
              metric(own.utilEstimated ? "UTIL~" : "UTIL", util, ownedGpuPct, "gpu"),
              metric("MEM", memPct, ownedMemPct, "mem"),
              live.length ? h("div", { class: "task-gpu-owner" }, icon("tasks", "sm"), live[0]) : null);
          })));
      grid.appendChild(card);
    }
    body.appendChild(grid);
    if (data.queue_enabled) {
      const activeAll = [...active, ...queued];
      body.appendChild(h("div", { class: "task-gpu-queue-head" },
        h("h4", null, t("tasks.gpu.queue")),
        h("span", { class: "pill" }, t("tasks.gpu.queue_counts", { running: active.length, queued: queued.length }))));
      if (!activeAll.length) {
        body.appendChild(h("div", { class: "empty sm" }, t("tasks.gpu.no_jobs")));
      } else {
        const list = h("div", { class: "stack" });
        for (const job of activeAll) {
          const task = taskFor(job);
          const done = task && ["done", "abandoned", "archived"].includes(task.status);
          list.appendChild(h("div", { class: "task-gpu-job" },
            h("div", null,
              h("b", null, task?.title || job.task || job.job),
              h("div", { class: "physics-hint" },
                `${job.server || t("tasks.gpu.job_queued")}${job.gpu_indices?.length ? ` · GPU ${job.gpu_indices.join(",")}` : ""}`
                + ` · ${job.owner || t("tasks.gpu.job_agent")}${job.pid ? ` · pid ${job.pid}` : ""}`)), // i18n-ignore: pid
            h("div", { class: "row tight" },
              h("span", { class: `pill ${job.status === "running" ? "ok" : "warn"}` }, gpuJobStatus(job.status)),
              done ? h("span", { class: "pill warn" }, t("tasks.gpu.task_done")) : null,
              ctx.S.session?.authenticated && ["queued", "starting", "running"].includes(job.status) // i18n-ignore: API values
                ? h("button", {
                  class: "ghost-btn sm danger", type: "button",
                  onclick: async () => {
                    try { await api.gpuCancel(job.id); await refresh(true); }
                    catch (err) { ctx.toast?.(err.message, "err"); }
                  },
                }, icon("ban", "sm"), t("action.cancel")) : null)));
        }
        body.appendChild(list);
      }
    }
  }
  if (!gpuTimer) gpuTimer = setInterval(() => refresh(false), 15000);
  setTimeout(() => refresh(false, true), 0);
  return panel;
}

function gpuJobStatus(status) {
  const key = `tasks.gpu.status.${status}`;
  const label = t(key);
  return label === key ? status : label;
}

// ------------------------------------------------------------- agent sessions
function sessionsView(ctx, S, tasks) {
  const docked = shellActive() && Boolean(ctx.sessionPage);
  const workspace = renderSessionWorkspace(ctx, {
    tasks,
    docked,
    library: sessionLibraryPanel(ctx, { skip: (item) => item.source !== "external" && (S.taskSessions || []).some((s) => s.id === item.id) }),
    related: docked ? null : h("div", { class: "session-below" }, relatedBoard(ctx, S, tasks)),
    onLaunch: (prefill) => openLaunchSession(ctx, prefill || {}),
  });
  const wrap = workspace.el;
  const showing = () => wrap.isConnected && S.tab === "tasks" && S.taskView === "sessions";
  // The session list comes from the shared stream: the Sessions sidebar view
  // and this page make one request per tick between them.
  let mounted = false;
  const unsubscribe = sessionStream.subscribeSessions((data) => {
    if (!mounted && !wrap.isConnected) return;    // not in the page yet
    mounted = true;
    if (!showing()) {
      if (sessionsTimer === unsubscribe) stopSessionsTimer();
      else unsubscribe();
      return;
    }
    workspace.applySessions(data.sessions || [], data.groups || []);
  }, { active: showing, immediate: false });
  stopSessionsTimer();
  sessionsTimer = unsubscribe;
  setTimeout(() => { if (showing()) sessionStream.refreshSessions().catch(() => {}); }, 120);
  return wrap;
}

function relatedBoard(ctx2, S2, tasks2) {
  const counts = {};
  for (const task of tasks2) counts[task.status] = (counts[task.status] || 0) + 1;
  return h("div", { class: "session-related" },
    h("span", { class: "session-related-label" }, icon("tasks", "sm"), t("tasks.related_board")),
    ...COLUMNS.map((status) => h("span", { class: "board-mini" },
      h("i", { style: { background: statusColor(status) } }),
      statusLabel(status),
      h("b", null, counts[status] || 0))),
    h("span", { class: "spacer" }),
    h("button", { class: "link", onclick: () => ctx2.setTaskView("board") }, t("tasks.open_board"), icon("chevronRight", "sm")));
}

function platformLabel(preset) {
  const id = preset.id || preset.platform;
  const key = `tasks.platform.${id}`;
  const translated = t(key);
  return translated === key ? (preset.label || preset.platform_label || id) : translated;
}

export async function dispatchTaskSession(ctx, unit) {
  if (ctx.S.project?.demo || ctx.S.project?.id === "ark-demo") {
    ctx.toast(t("tasks.demo_disabled"), "info");
    return;
  }
  try {
    const [data, presets] = await Promise.all([
      api.dispatch(unit.uid || unit.id),
      api.sessionPresets().then((value) => value.presets || []).catch(() => []),
    ]);
    const available = presets.filter((preset) => preset.available);
    const realAgent = available.find((preset) => ["dsh", "codex", "claude", "opencode", "cursor", "gemini"].includes(preset.id));
    const agent = realAgent || available.find((preset) => preset.id === "echo") || available[0];
    if (!agent) {
      ctx.toast(t("tasks.dispatch_no_agent"), "err");
      openLaunchSession(ctx, {
        title: `${unit.uid} · ${unit.title}`,
        task: unit.uid,
        prompt: data.prompt,
        notice: t("tasks.dispatch_no_agent"),
      });
      return;
    }
    const session = await api.createSession({
      platform: agent.id,
      title: `${unit.uid} · ${unit.title}`,
      task: unit.uid,
      prompt: data.prompt,
    });
    ctx.S.selectedSessionId = session.session.id;
    ctx.S.taskView = "sessions";
    ctx.S.sessionThreadMode = "thread";
    ctx.toast(realAgent ? t("tasks.dispatch_started") : t("tasks.dispatch_demo"), "ok");
    await ctx.refresh();
  } catch (err) {
    ctx.toast(err.message, "err");
  }
}

export async function openLaunchSession(ctx, prefill = {}) {
  if (ctx.S.project?.demo || ctx.S.project?.id === "ark-demo") {
    openModal({
      title: t("tasks.demo_disabled_title"),
      body: h("div", { class: "notice" }, icon("info", "sm"),
        h("div", null, t("tasks.demo_disabled"))),
      actions: [{ label: t("action.close"), ghost: true }],
    });
    return;
  }
  let presets = [];
  let worktreeData = null;
  let effective = null;
  try {
    const [presetData, worktreeResult, effectiveData] = await Promise.all([
      api.sessionPresets(),
      api.worktrees().catch((err) => ({ available: false, reason: err.message, worktrees: [] })),
      api.effectiveSettings().catch(() => null),
    ]);
    presets = presetData.presets || [];
    worktreeData = worktreeResult;
    effective = effectiveData;
  } catch (err) {
    ctx.toast(err.message, "err");
    return;
  }
  const available = presets.filter((preset) => preset.available);
  if (!available.length) {
    openModal({
      title: t("tasks.launch_title"),
      body: h("div", { class: "notice" }, icon("alert", "sm"), h("div", null, t("tasks.launch_no_platforms"))),
      actions: [{ label: t("action.close"), ghost: true }],
    });
    return;
  }
  let ordered = [...presets].sort((a, b) => Number(b.available) - Number(a.available));
  const managedWorktrees = (worktreeData?.worktrees || []).filter((item) => item.managed && !item.main);
  const agents = (ctx.S.units || []).filter((u) => u.type === "agent");
  const tasks = (ctx.S.units || []).filter((u) => u.type === "task").slice(0, 200);
  const parent = prefill.parent || null;

  const worktreeSelect = h("select", { class: "select" },
    h("option", { value: "" }, t("tasks.launcher.no_worktree")),
    ...managedWorktrees.map((item) => h("option", {
      value: item.path,
      disabled: Number(item.active_count || 0) > 0,
    }, t(item.clean ? "tasks.launcher.wt_option_clean" : "tasks.launcher.wt_option_changed",
      { branch: item.branch || item.name, n: item.dirty || 0 })
      + (item.active_count ? t("tasks.launcher.wt_option_active", { n: item.active_count }) : ""))));
  const worktreeNote = h("div", { class: "session-hint" });
  const launchProviderLabel = (preset) => {
    if (!preset.available) return `${platformLabel(preset)} — ${t("tasks.launch_unavailable")}`;
    if (preset.provider_status === "unauthenticated") return t("tasks.launcher.provider_unauth", { provider: platformLabel(preset) });
    if (preset.provider_status === "unknown") return t("tasks.launcher.provider_auth_unknown", { provider: platformLabel(preset) });
    return platformLabel(preset);
  };
  const platformSelect = h("select", { class: "select" },
    ordered.map((preset) => h("option", {
      value: preset.id,
      disabled: !preset.available,
    }, launchProviderLabel(preset))));
  const providerRefresh = h("button", { class: "btn sm ghost", type: "button", title: t("tasks.launcher.refresh_providers_title") },
    icon("refresh", "sm"), t("tasks.launcher.refresh_providers"));
  const titleInput = h("input", { class: "input", placeholder: t("common.untitled") });
  const modelInput = h("input", { class: "input", list: "ark-session-models", placeholder: t("tasks.models.default_tag"), autocomplete: "off" });
  const modelList = h("datalist", { id: "ark-session-models" });
  const effortSelect = h("select", { class: "select" }, h("option", { value: "" }, t("tasks.models.default_tag")));
  const accessSelect = h("select", { class: "select" },
    h("option", { value: "read_only" }, t("tasks.launcher.access_read_only")),
    h("option", { value: "full_access" }, t("tasks.launcher.access_full")));
  const accessNote = h("div", { class: "session-hint" });
  const agentInput = h("input", { class: "input", list: "ark-session-agents", placeholder: t("tasks.launch_agent") });
  const agentList = h("datalist", { id: "ark-session-agents" },
    agents.map((agent) => h("option", { value: agent.title })));
  const taskSelect = h("select", { class: "select" },
    h("option", { value: "" }, t("tasks.no_task")),
    tasks.map((task) => h("option", { value: task.uid }, `${task.uid} · ${task.title}`)));
  const cwdInput = h("input", { class: "input", placeholder: t("tasks.launcher.workspace_root") });
  const promptArea = h("textarea", { class: "textarea", style: { minHeight: "96px" }, value: prefill.prompt || "" });
  const description = h("div", { class: "session-hint" });
  const platformNote = h("div", { class: "session-hint" });
  const cwdHint = h("div", { class: "session-hint" }, t("tasks.launch_cwd_hint"));

  function selectedWorktree() {
    return managedWorktrees.find((item) => item.path === worktreeSelect.value) || null;
  }

  function syncWorktree() {
    const target = selectedWorktree();
    cwdInput.disabled = Boolean(target);
    cwdInput.placeholder = target
      ? t("tasks.launcher.worktree_root", { path: target.path }) : t("tasks.launcher.workspace_root");
    if (target) {
      cwdHint.textContent = t("tasks.launcher.worktree_cwd_hint");
      worktreeNote.textContent = t(target.clean ? "tasks.launcher.wt_note_clean" : "tasks.launcher.wt_note_dirty",
        { branch: target.branch || target.name, head: target.head || "—", n: target.dirty || 0 })
        + (target.active_count ? t("tasks.launcher.wt_note_busy") : "");
      worktreeNote.classList.toggle("danger", Number(target.dirty || 0) > 0);
    } else {
      cwdHint.textContent = t("tasks.launch_cwd_hint");
      if (worktreeData?.available) {
        worktreeNote.textContent = t("tasks.launcher.no_worktree_note");
      } else {
        worktreeNote.textContent = worktreeData?.reason || t("tasks.launcher.no_git");
      }
      worktreeNote.classList.remove("danger");
    }
  }

  function syncPreset() {
    const preset = ordered.find((item) => item.id === platformSelect.value);
    description.textContent = preset?.description || "";
    const models = preset?.models || [];
    modelInput.value = models[0] || "";
    modelInput.placeholder = models[0] || t("tasks.launcher.provider_default");
    modelInput.title = models.length
      ? t("tasks.launcher.configured_models", { models: models.join(", ") })
      : t("tasks.launcher.no_model_presets");
    modelList.replaceChildren(...models.map((model) => h("option", { value: model })));
    const efforts = preset?.efforts || [];
    effortSelect.replaceChildren(
      h("option", { value: "" }, efforts.length ? t("tasks.models.default_tag") : t("tasks.launcher.not_supported")),
      ...efforts.map((effort) => h("option", { value: effort }, effort)));
    effortSelect.disabled = !efforts.length;
    const modes = (preset?.permission_modes || preset?.access_modes || ["read_only", "full_access"])
      .filter((mode) => PERMISSION_MODES.includes(mode));
    accessSelect.replaceChildren(...modes.map((mode) => h("option", {
      value: mode, title: permissionModeDescription(mode, { t }),
    }, `${permissionModeLabel(mode, { t })} — ${permissionModeDescription(mode, { t })}`)));
    accessSelect.value = modes.includes("read_only") ? "read_only" : (modes[0] || "read_only");
    accessSelect.disabled = modes.length < 2;
    platformNote.textContent = preset?.followup
      ? t("tasks.followup_hint")
      : t("tasks.followup_disabled");
    syncAccessNote();
  }
  function syncAccessNote() {
    const preset = ordered.find((item) => item.id === platformSelect.value);
    const mode = accessSelect.value;
    if (mode === "full_access") {
      accessNote.textContent = t("tasks.launcher.note_full");
    } else if (mode === "ask" || mode === "accept_edits") {
      const supported = Boolean(preset?.supports_approval);
      accessNote.textContent = supported
        ? t("tasks.launcher.note_approval", { desc: permissionModeDescription(mode, { t }) })
        : t("tasks.launcher.note_no_approval", { mode: permissionModeLabel(mode, { t }) });
    } else if (mode === "plan") {
      accessNote.textContent = t("tasks.launcher.note_plan");
    } else {
      accessNote.textContent = t("tasks.launcher.note_read_only");
    }
    accessNote.classList.toggle("danger", mode === "full_access" || (
      (mode === "ask" || mode === "accept_edits") && !preset?.supports_approval));
  }
  accessSelect.addEventListener("change", syncAccessNote);
  platformSelect.addEventListener("change", syncPreset);
  worktreeSelect.addEventListener("change", syncWorktree);
  providerRefresh.addEventListener("click", async () => {
    providerRefresh.disabled = true;
    try {
      await api.refreshProviders();
      presets = (await api.sessionPresets()).presets || [];
      ordered = [...presets].sort((a, b) => Number(b.available) - Number(a.available));
      const current = platformSelect.value;
      platformSelect.replaceChildren(...ordered
        .map((preset) => h("option", {
          value: preset.id, disabled: !preset.available,
        }, launchProviderLabel(preset))));
      platformSelect.value = ordered.some((preset) => preset.id === current && preset.available)
        ? current : (ordered.find((preset) => preset.available)?.id || current);
      syncPreset();
      ctx.toast(t("tasks.launcher.providers_refreshed"), "ok");
    } catch (err) {
      ctx.toast(err.message, "err");
    } finally {
      providerRefresh.disabled = false;
    }
  });
  syncPreset();
  if (prefill.platform || parent?.platform) platformSelect.value = prefill.platform || parent.platform;
  syncPreset();
  if (prefill.worktree) worktreeSelect.value = prefill.worktree;
  syncWorktree();
  titleInput.value = prefill.title || (parent ? `Subagent · ${parent.title || "session"}` : "");
  taskSelect.value = prefill.task || parent?.task_ref || "";
  cwdInput.value = prefill.cwd || (parent && parent.cwd !== "." ? parent.cwd : "");
  promptArea.value = prefill.prompt || "";
  if (prefill.notice) description.textContent = prefill.notice;

  const body = h("div", { class: "launch-form" },
    effective?.agents?.personal_config_required
      ? h("div", { class: "full notice" }, icon("alert", "sm"), h("div", null,
        h("b", null, t("common.sentence_lead", { text: t("settings.personal_config_required") })),
        t("settings.personal_config_required_hint"),
        h("button", {
          class: "btn sm ghost", type: "button", style: { marginLeft: "8px" },
          onclick: () => { ctx.navigate("settings"); },
        }, icon("settings", "sm"), t("settings.personal_agents"))))
      : null,
    h("div", { class: "field" }, h("label", null, t("tasks.launch_platform")), platformSelect,
      h("div", { class: "session-hint launch-provider-actions" }, providerRefresh), description, platformNote),
    h("div", { class: "field" }, h("label", null, t("tasks.launch_title_label")), titleInput),
    h("div", { class: "field" }, h("label", null, t("tasks.launch_model")), modelInput, modelList),
    h("div", { class: "field" }, h("label", null, t("tasks.launcher.effort")), effortSelect),
    h("div", { class: "field" }, h("label", null, t("tasks.launcher.access")), accessSelect, accessNote),
    h("div", { class: "field" }, h("label", null, t("tasks.launcher.worktree")), worktreeSelect, worktreeNote),
    h("div", { class: "field" }, h("label", null, t("tasks.launch_agent")), agentInput, agentList),
    h("div", { class: "field" }, h("label", null, t("tasks.launch_task")), taskSelect),
    h("div", { class: "field" }, h("label", null, t("tasks.launch_cwd")), cwdInput, cwdHint),
    parent ? h("div", { class: "full notice" }, icon("plus", "sm"), h("div", null,
      t("tasks.launcher.subagent_of", { parent: parent.title || parent.id }))) : null,
    h("div", { class: "field full" }, h("label", null, t("tasks.launch_prompt")), promptArea),
    h("div", { class: "full notice" }, icon("shield", "sm"), h("div", null,
      t("tasks.launcher.safety_note"))));

  const launchAction = {
    label: t("tasks.launch"),
    primary: true,
    onClick: async () => {
      try {
        const prompt = promptArea.value.trim();
        const worktree = worktreeSelect.value;
        const data = await api.createSession({
          platform: platformSelect.value,
          title: titleInput.value.trim(),
          model: modelInput.value.trim(),
          effort: effortSelect.value,
          access_mode: accessSelect.value,
          agent: agentInput.value.trim(),
          task: taskSelect.value,
          cwd: worktree ? "" : cwdInput.value.trim(),
          worktree,
          prompt,
          parent_id: parent?.id || "",
        });
        rememberSessionChoice({ platform: platformSelect.value, model: modelInput.value.trim() });
        ctx.S.selectedSessionId = data.session.id;
        ctx.S.taskView = "sessions";
        ctx.S.sessionThreadMode = "thread";
        ctx.toast(t("tasks.launch_started"), "ok");
        ctx.refresh();
        return true;
      } catch (err) {
        ctx.toast(err.message, "err");
        return false;
      }
    },
  };
  openModal({
    title: t("tasks.launch_title"),
    body,
    wide: true,
    actions: [
      { label: t("action.cancel"), ghost: true },
      launchAction,
    ],
  });
}

function boardView(ctx, S, tasks, agents, hosts, artifacts) {
  const board = h("div", { class: "board" });
  for (const status of COLUMNS) {
    const items = sortUnits(tasks.filter((u) => u.status === status), "importance");
    const col = h("div", { class: "board-col", dataset: { status } });
    const head = h("div", { class: "board-head" },
      h("span", { class: "dot", style: { background: statusColor(status) } }),
      h("span", { class: "t" }, statusLabel(status)),
      h("span", { class: "n" }, items.length));
    const body = h("div", { class: "board-body" });
    if (!items.length) body.appendChild(h("div", { class: "task-empty" }, t("tasks.empty")));
    for (const unit of items) body.appendChild(taskCard(ctx, unit));
    col.append(head, body);
    col.addEventListener("dragover", (e) => { e.preventDefault(); col.classList.add("drag-over"); });
    col.addEventListener("dragleave", () => col.classList.remove("drag-over"));
    col.addEventListener("drop", async (e) => {
      e.preventDefault(); col.classList.remove("drag-over");
      const uid = e.dataTransfer.getData("text/plain");
      if (!uid) return;
      const unit = tasks.find((x) => x.uid === uid);
      if (!unit || unit.status === status) return;
      try {
        await ctx.moveTask(unit, status);
      } catch (err) { ctx.toast(err.message, "err"); }
    });
    board.appendChild(col);
  }
  // Agent fleet / Hosts / Library: shown when the project has them, or with
  // advanced features on.
  const showSupport = advancedEnabled() || agents.length || hosts.length || artifacts.length;
  const support = !showSupport ? null : h("details", { class: "task-support", open: true },
    h("summary", null,
      h("span", null, icon("layers", "sm"), ` ${t("tasks.support.title")}`),
      h("span", { class: "task-support-meta" },
        t("tasks.support.meta", { agents: agents.length, hosts: hosts.length, library: artifacts.length }))),
    taskAside(ctx, S, tasks, agents, hosts, artifacts));
  return h("div", null, board, support);
}

function taskCard(ctx, unit) {
  const due = dueState(unit);
  const card = h("article", {
    class: "task-card", draggable: "true",
    dataset: { uid: unit.uid, unitRef: unit.uid },
    onclick: (e) => { if (!e.target.closest("button")) ctx.openUnit(unit.uid); },
    ondragstart: (e) => { e.dataTransfer.setData("text/plain", unit.uid); e.dataTransfer.effectAllowed = "move"; card.classList.add("dragging"); },
    ondragend: () => card.classList.remove("dragging"),
  },
    h("div", { class: "row", style: { justifyContent: "space-between", gap: "4px" } },
      h("span", { class: "uid" }, unit.uid),
      h("span", { class: "row tight" }, assigneeChip(unit), impDot(unit.importance))),
    h("div", { class: "t" }, unit.title),
    h("div", { class: "m" },
      unit.end_at ? h("span", { class: due === "overdue" ? "overdue" : "" }, icon("calendar", "sm"), " ", fmtDate(unit.end_at)) : null,
      unit.out_count + unit.in_count ? h("span", null, icon("link", "sm"), " ", unit.out_count + unit.in_count) : null,
      h("span", { class: "right" }, unit.assignees?.length ? h("span", { class: "assignees" }, unit.assignees.map((a) => avatarFor(a))) : h("span", { class: "muted" }, t("common.unassigned")))),
    (unit.tags || []).filter((tag) => /^(phase|risk|gate|gpu|task):/.test(tag)).slice(0, 2).length
      ? h("div", { class: "tags" }, (unit.tags || []).filter((tag) => /^(phase|risk|gate|gpu|task):/.test(tag)).slice(0, 2).map((tag) => tagChip(tag)))
      : null,
    canRunWithAgent(unit) ? h("div", { class: "row", style: { marginTop: "6px" } },
      h("button", { class: "btn sm ghost", "data-run-with-agent": "", onclick: (e) => { e.stopPropagation(); (ctx.dispatchTaskSession ? ctx.dispatchTaskSession(unit) : openDispatchModal(ctx, unit)); } }, t("tasks.dispatch"))) : null);
  return card;
}

function taskAside(ctx, S, tasks, agents, hosts, artifacts) {
  const active = tasks.filter((u) => ["active", "open", "review"].includes(u.status));
  const agentCards = h("div", { class: "stack", style: { gap: "7px" } });
  for (const agent of agents.slice(0, 4)) {
    const load = tasks.filter((u) => (u.assignees || []).some((a) => a.id === agent.id) && u.status !== "done").length;
    agentCards.appendChild(h("div", { class: "agent-card", dataset: { unitRef: agent.uid }, onclick: () => ctx.openUnit(agent.uid) },
      avatarFor(agent),
      h("div", { class: "main" },
        h("div", { class: "t" }, agent.title.split("·")[0].trim()),
        h("div", { class: "role" }, roleOf(agent) || typeLabel("agent")),
        h("div", { class: "load" }, t("tasks.agent_load"),
          h("span", { class: "load-dots" }, [0, 1, 2, 3].map((i) => h("i", { class: i < Math.min(4, load) ? "on" : "" })))))));
  }
  if (!agents.length) agentCards.appendChild(h("div", { class: "empty sm" }, t("tasks.no_agents")));
  const hostRows = hosts.map((host) => h("div", { class: "host-row", dataset: { unitRef: host.uid }, onclick: () => ctx.openUnit(host.uid) },
    h("span", { class: "status-dot" }),
    h("span", { class: "t" }, host.title),
    ...(host.tags || []).filter((tag) => /^(os|gpu):/.test(tag)).slice(0, 2).map((tag) => h("span", { class: "tag" }, tag.split(":")[1]))));
  const due = active.filter((u) => u.end_at).sort((a, b) => a.end_at.localeCompare(b.end_at)).slice(0, 3);
  const dueList = h("div", { class: "action-list" });
  for (const u of due) {
    dueList.appendChild(h("div", { class: "action-item", dataset: { unitRef: u.uid }, onclick: () => ctx.openUnit(u.uid) },
      h("span", { class: "ic" }, icon("clock", "sm")),
      h("div", { class: "txt" },
        h("div", { class: "t" }, u.title),
        h("div", { class: "d" }, h("span", { class: dueState(u) === "overdue" ? "overdue" : "" }, fmtDate(u.end_at))))));
  }
  return h("aside", { class: "task-aside" },
    h("div", { class: "panel" },
      h("div", { class: "panel-head" }, h("h3", null, t("tasks.fleet")),
        h("button", { class: "link", onclick: () => ctx.setTaskView("agents") }, t("action.see_all"))),
      agentCards),
    h("div", { class: "panel" },
      h("div", { class: "panel-head" }, h("h3", null, t("tasks.hosts")),
        h("span", { class: "muted", style: { fontSize: "10px" } }, hosts.length)),
      hostRows.length ? hostRows : h("div", { class: "empty sm" }, t("tasks.no_hosts"))),
    h("div", { class: "panel" },
      h("div", { class: "panel-head" }, h("h3", null, t("tasks.long_term")),
        h("button", { class: "link", onclick: () => ctx.setTaskView("library") }, t("action.see_all"))),
      h("div", { class: "row", style: { gap: "7px" } },
        h("div", { class: "role-tile", style: { flex: 1 } },
          h("div", { class: "k" }, t("tasks.artifacts")),
          h("div", { class: "v" }, artifacts.filter((x) => x.type === "artifact").length)),
        h("div", { class: "role-tile", style: { flex: 1 } },
          h("div", { class: "k" }, t("tasks.resources")),
          h("div", { class: "v" }, artifacts.filter((x) => x.type === "resource").length)))),
    h("div", { class: "panel" },
      h("div", { class: "panel-head" }, h("h3", null, t("tasks.due_soon"))),
      dueList));
}

function timelineView(ctx, S, tasks) {
  const items = [...tasks, ...(S.units || []).filter((u) => u.type === "milestone")];
  const bar = h("div", { class: "toolbar", style: { marginBottom: "10px" } });
  const host = h("div");
  const gantt = new GanttView(host, { items, groupBy: S.ganttGroup || "type", zoom: S.ganttZoom || 1, onSelect: (u) => ctx.openUnit(u.uid) });
  ctx.trackGantt(gantt);
  bar.append(...ganttControls(host, gantt, {
    group: S.ganttGroup || "type",
    onGroup: (g) => { S.ganttGroup = g; },
    onZoom: (z) => { S.ganttZoom = z; },
  }));
  return h("div", null, bar, host);
}

function agentsView(ctx, S, agents, hosts, tasks) {
  const wrap = h("div", { class: "stack", style: { gap: "16px" } });
  wrap.appendChild(h("div", { class: "notice" }, icon("info", "sm"),
    h("div", null, t("tasks.dispatch_hint"))));
  const grid = h("div", { class: "cards three" });
  for (const agent of agents) {
    const assigned = tasks.filter((u) => (u.assignees || []).some((a) => a.id === agent.id));
    const active = assigned.filter((u) => u.status !== "done");
    const host = (agent.tags || []).find((x) => x.startsWith("host:"));
    grid.appendChild(h("div", { class: "panel", style: { padding: "12px", cursor: "pointer" }, onclick: () => ctx.openUnit(agent.uid) },
      h("div", { class: "row", style: { marginBottom: "8px" } }, avatarFor(agent), h("div", { class: "flex1" },
        h("div", { style: { fontSize: "13px", fontWeight: "700" } }, agent.title.split("·")[0].trim()),
        h("div", { style: { fontSize: "10px", color: "var(--mute)" } }, roleOf(agent) || typeLabel("agent"))),
        h("span", { class: `status-dot ${agent.status === "active" ? "" : agent.status === "inbox" ? "idle" : "idle"}`.trim() })),
      h("div", { style: { fontSize: "11.5px", color: "var(--sub)", lineHeight: "1.55", minHeight: "34px" } }, (agent.excerpt || agent.body || "").slice(0, 130)),
      h("div", { class: "row", style: { marginTop: "8px", gap: "5px", fontSize: "10px" } },
        ...[...new Set((agent.tags || []).filter((x) => /^(model|host|role):/.test(x)))].map((x) => h("span", { class: "pill" }, x)),
        h("span", { class: "right", color: "var(--mute)" }, t("tasks.active_count", { n: active.length }))),
      h("div", { class: "load-dots", style: { marginTop: "8px" } }, [0, 1, 2, 3, 4, 5].map((i) => h("i", { class: i < Math.min(6, active.length) ? "on" : "" })))));
  }
  wrap.appendChild(grid);
  wrap.appendChild(h("div", { class: "section-head" }, h("h2", null, t("tasks.hosts"))));
  const hostGrid = h("div", { class: "cards" });
  for (const host of hosts) {
    const tags = (host.tags || []).filter((x) => /^(os|gpu|device|status):/.test(x));
    hostGrid.appendChild(h("div", { class: "panel", style: { cursor: "pointer" }, onclick: () => ctx.openUnit(host.uid) },
      h("div", { class: "row", style: { marginBottom: "6px" } }, icon("cpu", "sm"), h("b", null, host.title), h("span", { class: "right status-dot" })),
      h("div", { style: { fontSize: "11px", color: "var(--sub)", lineHeight: "1.55" } }, (host.excerpt || host.body || "").slice(0, 150)),
      h("div", { class: "row", style: { marginTop: "8px" } }, tags.map((x) => h("span", { class: "tag" }, x)))));
  }
  wrap.appendChild(hostGrid);
  return wrap;
}

function libraryView(ctx, S, items) {
  if (!items.length) return emptyState(t("tasks.library_empty"));
  const wrap = h("div", { class: "stack", style: { gap: "16px" } });
  const groups = { artifact: [], resource: [] };
  for (const item of items) (groups[item.type] || groups.artifact).push(item);
  for (const [type, list] of Object.entries(groups)) {
    if (!list.length) continue;
    wrap.appendChild(h("section", null,
      h("div", { class: "section-head" }, h("h2", null, typeLabel(type)), h("span", { class: "sub" }, `${list.length}`)),
      h("div", { class: "cards" }, list.map((unit) => unitCard(unit, ctx, { onTag: (tag) => ctx.toggleFilterValue("tags", tag) })))));
  }
  return wrap;
}

function roleOf(agent) {
  const tag = (agent.tags || []).find((x) => x.startsWith("role:"));
  return tag ? tag.split(":")[1] : "";
}
