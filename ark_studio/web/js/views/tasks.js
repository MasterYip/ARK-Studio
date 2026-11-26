import { h, clear, statusColor, impDot, tagChip, fmtDate, relativeTime, dueState, avatarFor } from "../dom.js";
import { t, statusLabel, getLang } from "../i18n.js";
import { icon } from "../icons.js";
import { GanttView, ganttControls } from "../gantt.js";
import { viewToggle, openLinkModal, openDispatchModal, typeLegend, openModal, confirmDialog, canRunWithAgent } from "../components.js";
import { gpuRegistrationNote, openSetupResources } from "../gpu-note.js";
import { sortUnits } from "./ideas.js";
import { renderSessionWorkspace } from "../session-workspace.js";
import { shellActive } from "../workbench.js";
import { sessionStream } from "../session-stream.js";
import { rememberSessionChoice } from "../session-sidebar-model.js";
import { sessionLibraryPanel } from "../session-library.js";
import { api, cachedThenFresh } from "../api.js";
import { gpuOwnership, occupiedView, loadOccupiedOnly, saveOccupiedOnly } from "../gpu-model.js";
import { resourcesGraphPanel } from "../resources-view.js";
import { openServer, connectionStore } from "../resource-open.js";
import { advancedEnabled } from "../advanced.js";
import { modelOptions, modelEfforts } from "../session-model.js";
import {
  launchProviders, disabledProviders, providerCapabilities, modelChoices, defaultEffort, effortChoices, accessChoices, agentChoices,
  loadLaunchDefaults, saveLaunchDefaults, initialChoice, taskOptions, parseTaskValue, contextTask, autoTitle,
  splitPrefillPrompt, firstMessage, CUSTOM_MODEL,
  START_FREE, startOptions, defaultStartMode, startPreview, startPlaceholder, startRequest,
} from "../launch-dialog-model.js";
import { libraryEntries, effectivePins, entryTitle } from "../prompt-library-model.js";
import { sessionStartText, promptLang } from "../starter-prompts.js";
import { assigneeChip } from "../assignee.js";
import { supportDeck } from "../support-panel.js";

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
  // Older links: the agent and library lists moved into Runs and Resources.
  if (S.taskView === "agents") S.taskView = "sessions";
  if (S.taskView === "library") S.taskView = "resources";
  const tasks = (S.units || []).filter((u) => u.type === "task");
  const sessions = S.taskSessions || [];
  const activeSessions = sessions.filter((s) => SESSION_ACTIVE.includes(s.status)).length;
  const phone = typeof window.matchMedia === "function"
    && window.matchMedia("(max-width: 760px)").matches;
  const sessionOnly = phone && S.taskView === "sessions";
  // Execute › Runs on a wide window: a compact header, the conversation below.
  const fullSession = S.taskView === "sessions" && shellActive();
  if (fullSession || sessionOnly) {
    // Execute › Runs on a wide window is the chat: its page actions live in
    // the Sessions view's "+" menu (ctx.runsActions).
    ctx.runsActions = () => [
      { label: t("session.sidebar.new"), icon: "plus", onClick: () => openLaunchSession(ctx) },
      { label: t("tasks.add"), icon: "tasks", onClick: () => ctx.openCreate({ type: "task", status: "open" }) },
      { label: t("assistant.title"), icon: "check", onClick: () => ctx.openAssistant() },
      advancedEnabled() ? { label: t("palette.cmd.import"), icon: "database", onClick: () => ctx.openImport() } : null,
    ].filter(Boolean);
    const page = h("div", { class: `page session-chat-page ${sessionOnly ? "mobile-session-page" : ""}`.trim() }, sessionsView(ctx, S, tasks));
    host.appendChild(page);
    return;
  }
  ctx.runsActions = null;
  const page = h("div", { class: `page ${sessionOnly ? "mobile-session-page" : ""}`.trim() },
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
      S.taskView === "timeline" ? timelineView(ctx, S, tasks) :
      boardView(ctx, S, tasks));
  host.appendChild(page);
}

// --------------------------------------------------------- GPU task monitor
// The page title names the Execute sub-tab that is open (nav name = title).
function executeViewTitle(view) {
  if (view === "sessions") return t("app.subtab.runs");
  if (view === "gpu") return "GPU"; // i18n-ignore: product term, same in both languages
  if (view === "resources") return t("resources.tab");
  return t("view.board");
}

function gpuResourcesPanel(ctx, S, tasks) {
  const body = h("div", { class: "task-gpu-body" },
    h("div", { class: "empty sm" }, t("tasks.gpu.loading")));
  const stamp = h("span", { class: "physics-hint" }, t("tasks.gpu.probe"));
  // Occupied only: servers where this project's running jobs hold a GPU (the
  // blue-highlighted cards), remembered per account and project.
  const store = (() => { try { return localStorage; } catch { return null; } })();
  const prefUser = S.session?.user || "";
  const prefProject = S.project?.id || "";
  let occupiedOnly = loadOccupiedOnly(store, prefUser, prefProject);
  let occupiedCount = null;
  const occupiedBtn = h("button", {
    class: "ghost-btn sm task-gpu-occupied", type: "button", "data-gpu-occupied": "",
    onclick: () => setOccupiedOnly(!occupiedOnly),
  });
  function paintOccupiedBtn() {
    occupiedBtn.classList.toggle("on", occupiedOnly);
    occupiedBtn.setAttribute("aria-pressed", String(occupiedOnly));
    occupiedBtn.textContent = occupiedCount == null
      ? t("tasks.gpu.occupied_only")
      : t("tasks.gpu.occupied_only_n", { n: occupiedCount });
  }
  function setOccupiedOnly(on) {
    occupiedOnly = Boolean(on);
    saveOccupiedOnly(store, prefUser, prefProject, occupiedOnly);
    paintOccupiedBtn();
    if (lastData) render(lastData);
  }
  paintOccupiedBtn();
  const panel = h("section", { class: "panel task-gpu-panel" },
    h("div", { class: "panel-head" },
      h("h3", null, icon("cpu", "sm"), ` ${t("tasks.gpu.title")}`),
      h("div", { class: "row tight" }, stamp, occupiedBtn,
        h("button", {
          class: "ghost-btn sm", type: "button",
          onclick: () => refresh(true),
        }, icon("refresh", "sm"), t("action.refresh")))),
    h("p", { class: "settings-lead" }, t("tasks.gpu.lead")),
    h("div", { class: "physics-hint", style: { marginTop: "-4px", marginBottom: "8px" } },
      t("tasks.gpu.legend")),
    body);
  let busy = false;
  // SSH connections: the project's shared machine records and the viewer's
  // own entries (resource-open.js), the same ones the Resources graph uses.
  const connections = connectionStore();
  let lastData = null;
  let connectionsLoaded = false;
  async function refresh(manual = false, first = false) {
    if (busy) return;
    if (!panel.isConnected) { stopGpuTimer(); return; }
    busy = true;
    if (!connectionsLoaded) {
      connectionsLoaded = true;
      connections.refresh([]).then(() => { if (lastData && panel.isConnected) render(lastData); }).catch(() => {});
    }
    const show = (data) => {
      lastData = data;
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
    const view = occupiedView(servers, active, occupiedOnly);
    occupiedCount = view.count;
    paintOccupiedBtn();
    if (view.empty) {
      body.appendChild(h("div", { class: "empty sm task-gpu-occupied-empty" },
        h("span", null, t("tasks.gpu.occupied_none")), " ",
        h("button", { type: "button", class: "task-gpu-link", onclick: () => setOccupiedOnly(false) },
          t("tasks.gpu.show_all"))));
    }
    const grid = h("div", { class: "task-gpu-grid" });
    for (const { server } of view.shown) {
      const system = server.system || {};
      const cpu = system.cpu_percent ?? system.cpu_load_pct;
      const memTotal = system.memory_total_mb;
      const memUsed = system.memory_used_mb;
      const memPct = memTotal ? Math.min(100, memUsed / memTotal * 100) : null;
      const serverJobs = active.filter((job) => job.server === server.server);
      // The shared machine record and the viewer's own entry first; the
      // server list's address only reaches admins (T-0018).
      const conn = connections.gpuConnection(server.server)
        || (server.host ? { host: server.host, user: server.user, port: server.port } : null);
      const shown = conn?.host ? conn : (server.host ? server : null);
      const card = h("div", { class: "task-gpu-server" },
        h("div", { class: "task-gpu-server-head" },
          h("div", null,
            h("b", null, server.server),
            shown ? h("div", { class: "physics-hint" },
              `${shown.user ? shown.user + "@" : ""}${shown.host}:${shown.port || 22}`) : null),
          !conn ? null : h("div", { class: "row tight" },
            // Through an ~/.ssh/config entry so the person's key is used
            // (resource-open.js openServer); a browser copies the command.
            h("button", {
              type: "button", class: "task-gpu-link",
              title: t("tasks.gpu.open_ssh"),
              onclick: () => openServer("ssh", conn, { name: server.server }),
            }, icon("command", "sm"), t("tasks.gpu.terminal")),
            h("button", {
              type: "button", class: "task-gpu-link",
              title: t("tasks.gpu.open_vscode"),
              onclick: () => openServer("editor", conn, { name: server.server }),
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
    if (view.shown.length) body.appendChild(grid);
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
const phoneNow = () => typeof window.matchMedia === "function" && window.matchMedia("(max-width: 760px)").matches;

function sessionsView(ctx, S, tasks) {
  const docked = shellActive() && Boolean(ctx.sessionPage);
  let ws = null;
  const workspace = renderSessionWorkspace(ctx, {
    tasks,
    docked,
    library: sessionLibraryPanel(ctx, {
      compact: docked || phoneNow(),
      listed: () => (S.taskSessions || []).map((s) => s.id),
      onData: (section) => ws?.setStoredContexts(section),
    }),
    related: docked ? null : h("div", { class: "session-below" }, relatedBoard(ctx, S, tasks)),
    onLaunch: (prefill) => openLaunchSession(ctx, prefill || {}),
  });
  ws = workspace;
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
    // A list read for the project before a switch: the next one follows.
    if (data.project && data.project !== S.project?.id) return;
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

async function launchUser() {
  try {
    const data = await api.me();
    const profile = data?.profile || data || {};
    return String(profile.username || profile.id || profile.name || "");
  } catch {
    return "";
  }
}

function launchStore() {
  try { return globalThis.localStorage || null; } catch { return null; }
}

function launchCatalog(presets) {
  const providers = launchProviders(presets);
  const modelsFor = (preset) => (preset ? modelOptions(preset, providerCapabilities(preset)) : []);
  const effortsFor = (preset, model) => (preset ? modelEfforts(preset, providerCapabilities(preset), model) : []);
  return { providers, modelsFor, effortsFor };
}

function freeWorktrees(worktreeData) {
  return (worktreeData?.worktrees || []).filter((item) => item.managed && !item.main);
}

// Settings › Personal › Coding agents (or the project's), scrolled to the section.
function openPersonalAgents(ctx, section = "personalAgents") {
  if (ctx.openSettingsAt) { ctx.openSettingsAt(section); return; }
  try { localStorage.setItem("ark.settingsTab", section); } catch { /* storage unavailable */ }
  ctx.navigate("settings");
}

// "Run with agent" on a task: starts at once with what this person last
// chose in the dialog for this project (provider, model, reasoning, access,
// worktree), else the first real provider with its defaults.
export async function dispatchTaskSession(ctx, unit) {
  if (ctx.S.project?.demo || ctx.S.project?.id === "ark-demo") {
    ctx.toast(t("tasks.demo_disabled"), "info");
    return;
  }
  const uid = unit.uid || unit.id;
  try {
    const [data, presets, worktreeData, user] = await Promise.all([
      api.dispatch(uid),
      api.sessionPresets().then((value) => value.presets || []).catch(() => []),
      api.worktrees().catch(() => null),
      launchUser(),
    ]);
    const { providers, modelsFor, effortsFor } = launchCatalog(presets);
    const real = providers.filter((preset) => !preset.demo && preset.id !== "shell");
    if (!real.length) {
      ctx.toast(t("tasks.dispatch_no_agent"), "err");
      openLaunchSession(ctx, { task: uid, notice: t("tasks.dispatch_no_agent") });
      return;
    }
    const free = freeWorktrees(worktreeData).filter((item) => !Number(item.active_count || 0)).map((item) => item.path);
    const remembered = loadLaunchDefaults(launchStore(), user, ctx.S.project?.id);
    const choice = initialChoice({ providers: real, remembered, modelsFor, effortsFor, worktrees: free });
    const session = await api.createSession({
      platform: choice.platform,
      title: autoTitle({ task: unit }) || uid,
      task: uid,
      prompt: data.prompt,
      model: choice.model,
      effort: choice.effort,
      access_mode: choice.access,
      worktree: choice.worktree,
      agent: choice.agent,
    });
    ctx.S.selectedSessionId = session.session.id;
    ctx.S.taskView = "sessions";
    ctx.S.sessionThreadMode = "thread";
    ctx.toast(t("tasks.dispatch_started_with", { provider: platformLabel(real.find((p) => p.id === choice.platform) || {}) }), "ok");
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
  let user = "";
  let promptsData = null;
  try {
    const [presetData, worktreeResult, effectiveData, who, library] = await Promise.all([
      api.sessionPresets(),
      api.worktrees().catch(() => ({ available: false, worktrees: [] })),
      api.effectiveSettings().catch(() => null),
      launchUser(),
      api.prompts().catch(() => null),
    ]);
    promptsData = library;
    presets = presetData.presets || [];
    worktreeData = worktreeResult;
    effective = effectiveData;
    user = who;
  } catch (err) {
    ctx.toast(err.message, "err");
    return;
  }
  let catalog = launchCatalog(presets);
  if (!catalog.providers.length) {
    openModal({
      title: t("tasks.launch_title"),
      body: h("div", { class: "launch-form" },
        h("div", { class: "full notice" }, icon("alert", "sm"), h("div", null, t("tasks.launch_no_platforms"))),
        h("div", { class: "full" }, h("button", { class: "btn sm", type: "button", onclick: () => openPersonalAgents(ctx) },
          icon("settings", "sm"), t("tasks.launcher.personal_open")))),
      actions: [{ label: t("action.close"), ghost: true }],
    });
    return;
  }
  const projectId = ctx.S.project?.id || "";
  const store = launchStore();
  const remembered = loadLaunchDefaults(store, user, projectId);
  const managedWorktrees = freeWorktrees(worktreeData);
  const pickableWorktrees = managedWorktrees.filter((item) => !Number(item.active_count || 0)).map((item) => item.path);
  const parent = prefill.parent || null;
  const units = ctx.S.units || [];
  const tasks = taskOptions(units);
  const { prompt: draftPrompt, extra: draftExtra } = splitPrefillPrompt(prefill.prompt || "");
  const choice = initialChoice({
    providers: catalog.providers, remembered, prefill, parent,
    modelsFor: catalog.modelsFor, effortsFor: catalog.effortsFor, worktrees: pickableWorktrees,
  });
  const field = (name, label, ...children) => h("div", { class: "field", "data-launch-field": name },
    h("label", null, label), ...children);
  const hint = (text = "") => h("div", { class: "session-hint" }, text);
  const currentPreset = () => catalog.providers.find((item) => item.id === platformSelect.value) || catalog.providers[0];

  // ---- provider
  const providerLabel = (preset) => {
    if (preset.provider_status === "unauthenticated") return t("tasks.launcher.provider_unauth", { provider: platformLabel(preset) });
    if (preset.provider_status === "unknown") return t("tasks.launcher.provider_auth_unknown", { provider: platformLabel(preset) });
    return platformLabel(preset);
  };
  const platformSelect = h("select", { class: "select", "aria-label": t("tasks.launch_platform") });
  const platformHint = hint();
  const providerRefresh = h("button", { class: "link launch-refresh", type: "button", title: t("tasks.launcher.refresh_providers_title") },
    t("tasks.launcher.refresh_providers"));
  function fillProviders(selected) {
    platformSelect.replaceChildren(...catalog.providers.map((preset) => h("option", { value: preset.id }, providerLabel(preset))));
    platformSelect.value = catalog.providers.some((preset) => preset.id === selected) ? selected : catalog.providers[0].id;
  }

  // ---- model
  const modelSelect = h("select", { class: "select", "aria-label": t("tasks.launch_model") });
  const customModel = h("input", { class: "input hidden", placeholder: t("tasks.launcher.model_custom_placeholder"), autocomplete: "off", spellcheck: "false" });
  function fillModels(model = "", custom = false) {
    const preset = currentPreset();
    const caps = providerCapabilities(preset);
    const options = catalog.modelsFor(preset);
    const rows = modelChoices(options, { t, defaultModel: caps.default_model });
    modelSelect.replaceChildren(...rows.map((row) => h("option", { value: row.value }, row.label)));
    const known = rows.some((row) => row.value === model && row.value !== CUSTOM_MODEL);
    modelSelect.value = custom || (model && !known) ? CUSTOM_MODEL : model;
    customModel.value = modelSelect.value === CUSTOM_MODEL ? model : "";
    customModel.classList.toggle("hidden", modelSelect.value !== CUSTOM_MODEL);
  }
  const chosenModel = () => (modelSelect.value === CUSTOM_MODEL ? customModel.value.trim() : modelSelect.value);

  // ---- reasoning
  const effortSelect = h("select", { class: "select", "aria-label": t("tasks.launcher.effort") });
  const effortField = field("effort", t("tasks.launcher.effort"), effortSelect);
  function fillEfforts(effort = effortSelect.value) {
    const preset = currentPreset();
    const caps = providerCapabilities(preset);
    const model = chosenModel();
    const efforts = catalog.effortsFor(preset, model);
    const rows = effortChoices(efforts, { t, fallback: defaultEffort(catalog.modelsFor(preset), caps, model) });
    effortSelect.replaceChildren(...rows.map((row) => h("option", { value: row.value }, row.label)));
    effortSelect.value = efforts.includes(effort) ? effort : "";
    effortField.classList.toggle("hidden", !rows.length);
  }

  // ---- access
  const accessName = `launch-access-${Date.now()}`;
  const accessBox = h("div", { class: "launch-access", role: "radiogroup", "aria-label": t("tasks.launcher.access") });
  function fillAccess(access) {
    const modes = accessChoices(currentPreset());
    const value = modes.includes(access) ? access : modes[0];
    accessBox.replaceChildren(...modes.map((mode) => h("label", { class: `launch-access-option ${mode === "full_access" ? "risky" : ""}`.trim() },
      h("input", { type: "radio", name: accessName, value: mode, checked: mode === value }),
      h("span", null, h("b", null, t(`tasks.launcher.access_${mode}_title`)), h("small", null, t(`tasks.launcher.access_${mode}_line`))))));
  }
  const chosenAccess = () => accessBox.querySelector("input:checked")?.value || "read_only";

  // ---- task
  const taskListId = `ark-launch-tasks-${Date.now()}`;
  const taskInput = h("input", { class: "input", list: taskListId, placeholder: t("tasks.launcher.task_placeholder"), autocomplete: "off" });
  const taskList = h("datalist", { id: taskListId }, tasks.map((task) => h("option", { value: task.label })));
  const taskClear = h("button", { class: "icon-btn launch-task-clear", type: "button", title: t("tasks.launcher.task_clear"), "aria-label": t("tasks.launcher.task_clear") }, icon("x", "sm"));
  const taskHint = hint();
  const initialTask = contextTask({ prefill, parent, selectedId: ctx.S.drawerOpen ? ctx.S.selectedId : "", units });
  const initialTaskOption = tasks.find((task) => task.uid === initialTask);
  taskInput.value = initialTaskOption ? initialTaskOption.label : initialTask;
  const chosenTask = () => parseTaskValue(taskInput.value, tasks) || (/^[A-Za-z]+-\d+$/.test(taskInput.value.trim()) ? taskInput.value.trim() : "");
  const chosenTaskUnit = () => units.find((unit) => unit.uid === chosenTask()) || null;

  // ---- prompt
  const promptArea = h("textarea", { class: "textarea", style: { minHeight: "88px" }, placeholder: t("tasks.launcher.prompt_placeholder") });
  promptArea.value = draftPrompt;

  // ---- start (T-0121): what the session does first, from the Home prompt
  // library; the server's facts choose the default (GET /api/agent-sessions/start).
  const promptLanguage = promptLang(getLang());
  const startEntries = libraryEntries(promptsData || {}).map((e) => ({ ...e, title: entryTitle(e, promptLanguage) }));
  const startChoices = startOptions(startEntries, effectivePins(promptsData ? promptsData.pins : null));
  const startSelect = h("select", { class: "select", "aria-label": t("tasks.launcher.start") },
    ...startChoices.map((o) => h("option", { value: o.id }, o.id === START_FREE ? t("tasks.launcher.start_free") : o.title)));
  const startPreviewLine = h("div", { class: "session-hint", "data-launch-start-preview": "" });
  let startServer = null;
  let startTouched = false;
  let startSeq = 0;
  const chosenStart = () => startChoices.find((o) => o.id === startSelect.value) || startChoices[startChoices.length - 1];
  function syncStart({ reset = false } = {}) {
    if (reset && !startTouched) {
      startSelect.value = defaultStartMode({ server: startServer, task: chosenTask(), remembered: remembered.start, options: startChoices });
    }
    startPreviewLine.textContent = startPreview({ mode: startSelect.value, option: chosenStart(), server: startServer,
      task: chosenTask(), prompt: promptArea.value, t });
    promptArea.placeholder = startPlaceholder({ mode: startSelect.value, option: chosenStart(), task: chosenTask(), t });
  }
  async function loadStart() {
    const seq = ++startSeq;
    syncStart({ reset: true });
    try {
      const data = await api.sessionStart(chosenTask());
      if (seq === startSeq) {
        startServer = data;
        syncStart({ reset: true });
      }
    } catch { /* an older server: no facts, the default stays */ }
  }
  startSelect.addEventListener("change", () => { startTouched = true; syncStart(); });

  // ---- advanced
  const titleInput = h("input", { class: "input", maxlength: "160" });
  const worktreeSelect = h("select", { class: "select" },
    h("option", { value: "" }, t("tasks.launcher.no_worktree")),
    ...managedWorktrees.map((item) => h("option", {
      value: item.path,
      disabled: Number(item.active_count || 0) > 0,
    }, t(item.clean ? "tasks.launcher.wt_option_clean" : "tasks.launcher.wt_option_changed",
      { branch: item.branch || item.name, n: item.dirty || 0 })
      + (item.active_count ? t("tasks.launcher.wt_option_active", { n: item.active_count }) : ""))));
  const worktreeNote = hint();
  const cwdInput = h("input", { class: "input", placeholder: t("tasks.launcher.workspace_root") });
  const cwdHint = hint(t("tasks.launch_cwd_hint"));
  const agentSelect = h("select", { class: "select" });
  const agentHint = hint();
  const agentField = field("agent", t("tasks.launcher.agent"), agentSelect, agentHint);
  const extraArea = h("textarea", { class: "textarea", style: { minHeight: "72px" } });
  extraArea.value = draftExtra;
  const extraField = h("div", { class: `field full ${draftExtra ? "" : "hidden"}`.trim(), "data-launch-field": "extra" },
    h("label", null, t("tasks.launcher.extra")), extraArea, hint(t("tasks.launcher.extra_hint")));

  function fillAgents(agent = agentSelect.value) {
    const preset = currentPreset();
    const list = agentChoices(preset, { worktree: worktreeSelect.value });
    agentSelect.replaceChildren(h("option", { value: "" }, t("tasks.launcher.agent_none")),
      ...list.map((item) => h("option", { value: item.id, title: item.description || "" },
        item.description ? `${item.label || item.id} — ${item.description}` : (item.label || item.id))));
    agentSelect.value = list.some((item) => item.id === agent) ? agent : "";
    agentField.classList.toggle("hidden", !list.length);
    agentHint.textContent = preset?.id === "codex" ? t("tasks.launcher.agent_hint_codex") : t("tasks.launcher.agent_hint_claude");
  }

  function syncWorktree() {
    const target = managedWorktrees.find((item) => item.path === worktreeSelect.value) || null;
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
      worktreeNote.textContent = worktreeData?.available ? t("tasks.launcher.no_worktree_note_short") : t("tasks.launcher.no_git");
      worktreeNote.classList.remove("danger");
    }
    fillAgents();
  }

  function syncTitle() {
    titleInput.placeholder = autoTitle({ task: chosenTaskUnit(), prompt: promptArea.value }) || t("common.untitled");
    taskHint.textContent = taskInput.value.trim() && !chosenTask() ? t("tasks.launcher.task_unknown") : "";
    taskHint.classList.toggle("danger", Boolean(taskHint.textContent));
  }

  function syncPlatform() {
    const preset = currentPreset();
    const key = `tasks.launcher.platform_hint.${preset.id}`;
    const text = t(key);
    platformHint.textContent = text === key ? "" : text;
  }

  platformSelect.addEventListener("change", () => {
    const keep = chosenAccess();
    syncPlatform();
    fillModels("");
    fillEfforts("");
    fillAccess(keep);
    fillAgents("");
  });
  modelSelect.addEventListener("change", () => {
    customModel.classList.toggle("hidden", modelSelect.value !== CUSTOM_MODEL);
    if (modelSelect.value === CUSTOM_MODEL) customModel.focus();
    fillEfforts();
  });
  customModel.addEventListener("change", () => fillEfforts());
  worktreeSelect.addEventListener("change", syncWorktree);
  taskInput.addEventListener("input", syncTitle);
  taskClear.addEventListener("click", () => { taskInput.value = ""; syncTitle(); loadStart(); taskInput.focus(); });
  taskInput.addEventListener("change", loadStart);
  promptArea.addEventListener("input", () => { syncTitle(); syncStart(); });
  providerRefresh.addEventListener("click", async () => {
    providerRefresh.disabled = true;
    try {
      await api.refreshProviders();
      presets = (await api.sessionPresets()).presets || [];
      catalog = launchCatalog(presets);
      if (!catalog.providers.length) throw new Error(t("tasks.launch_no_platforms"));
      const model = chosenModel();
      const effort = effortSelect.value;
      fillProviders(platformSelect.value);
      syncPlatform();
      fillModels(model);
      fillEfforts(effort);
      fillAccess(chosenAccess());
      fillAgents();
      ctx.toast(t("tasks.launcher.providers_refreshed"), "ok");
    } catch (err) {
      ctx.toast(err.message, "err");
    } finally {
      providerRefresh.disabled = false;
    }
  });

  fillProviders(choice.platform);
  syncPlatform();
  fillModels(choice.model, choice.custom);
  fillEfforts(choice.effort);
  fillAccess(choice.access);
  worktreeSelect.value = choice.worktree;
  agentSelect.value = choice.agent;
  syncWorktree();
  fillAgents(choice.agent);
  titleInput.value = prefill.title || (parent ? t("tasks.launcher.subagent_title", { parent: parent.title || t("common.untitled") }) : "");
  cwdInput.value = prefill.cwd || (parent && parent.cwd !== "." ? parent.cwd : "");
  syncTitle();
  loadStart();

  const personalNeeded = effective?.agents?.personal_config_required;
  const switchedOff = disabledProviders(presets);
  const offByProject = switchedOff.some((preset) => preset.disabled === "project");
  const setupNote = personalNeeded ? t("tasks.launcher.personal_needed")
    : switchedOff.length ? t(offByProject ? "tasks.launcher.disabled_project" : "tasks.launcher.disabled_providers",
      { names: switchedOff.map((preset) => platformLabel(preset)).join(t("tasks.launcher.list_sep")) }) : "";
  const setupTarget = !personalNeeded && offByProject ? "projectAgents" : "personalAgents";
  const advancedOpen = Boolean(draftExtra || prefill.cwd || prefill.title);
  const advanced = h("details", { class: "launch-advanced full", "data-launch-advanced": "", open: advancedOpen || undefined },
    h("summary", null, t("tasks.launcher.advanced")),
    h("div", { class: "launch-form launch-advanced-body" },
      field("title", t("tasks.launch_title_label"), titleInput, hint(t("tasks.launcher.title_hint"))),
      field("worktree", t("tasks.launcher.worktree"), worktreeSelect, worktreeNote),
      field("cwd", t("tasks.launch_cwd"), cwdInput, cwdHint),
      agentField,
      extraField));

  const body = h("div", { class: "launch-form" },
    setupNote
      ? h("div", { class: "full launch-inline-notice", "data-launch-personal": "" }, icon("alert", "sm"),
        h("span", null, setupNote),
        h("button", { class: "link", type: "button", onclick: () => openPersonalAgents(ctx, setupTarget) },
          t(setupTarget === "projectAgents" ? "tasks.launcher.project_open" : "tasks.launcher.personal_open")))
      : null,
    prefill.notice ? h("div", { class: "full launch-inline-notice" }, icon("info", "sm"), h("span", null, prefill.notice)) : null,
    parent ? h("div", { class: "full launch-inline-notice" }, icon("plus", "sm"),
      h("span", null, t("tasks.launcher.subagent_of", { parent: parent.title || parent.id }))) : null,
    field("platform", t("tasks.launch_platform"), platformSelect,
      h("div", { class: "session-hint launch-platform-hint" }, platformHint, providerRefresh)),
    field("model", t("tasks.launch_model"), modelSelect, customModel),
    effortField,
    h("div", { class: "field", "data-launch-field": "task" }, h("label", null, t("tasks.launcher.task")),
      h("div", { class: "launch-task-row" }, taskInput, taskClear), taskList, taskHint),
    h("div", { class: "field", "data-launch-field": "start" }, h("label", null, t("tasks.launcher.start")), startSelect, startPreviewLine),
    h("div", { class: "field full", "data-launch-field": "access" }, h("label", null, t("tasks.launcher.access")), accessBox),
    h("div", { class: "field full", "data-launch-field": "prompt" }, h("label", null, t("tasks.launcher.prompt")), promptArea),
    advanced,
    h("div", { class: "full launch-safety" }, icon("shield", "sm"),
      h("span", null, t("tasks.launcher.safety_short")),
      h("details", { class: "launch-more" }, h("summary", null, t("tasks.launcher.learn_more")),
        h("p", null, t("tasks.launcher.safety_note")))));

  const launchAction = {
    label: t("tasks.launch_start"),
    primary: true,
    onClick: async () => {
      const platform = platformSelect.value;
      const model = chosenModel();
      if (modelSelect.value === CUSTOM_MODEL && !model) {
        ctx.toast(t("tasks.launcher.model_custom_empty"), "err");
        customModel.focus();
        return false;
      }
      const typedTask = taskInput.value.trim();
      const task = chosenTask();
      if (typedTask && !task) {
        ctx.toast(t("tasks.launcher.task_unknown"), "err");
        taskInput.focus();
        return false;
      }
      const prompt = promptArea.value.trim();
      const startOption = chosenStart();
      if (startOption.unit && !task) {
        ctx.toast(t("tasks.launcher.start_preview.continue_pick"), "err");
        taskInput.focus();
        return false;
      }
      const startEntry = startEntries.find((e) => e.id === startOption.id) || null;
      const start = startRequest(startOption, startEntry
        ? sessionStartText(startEntry, ctx.S.project, { lang: promptLanguage, unit: task, user }) : "");
      const worktree = worktreeSelect.value;
      const effort = effortField.classList.contains("hidden") ? "" : effortSelect.value;
      const access = chosenAccess();
      const agent = agentField.classList.contains("hidden") ? "" : agentSelect.value;
      try {
        const data = await api.createSession({
          platform,
          title: titleInput.value.trim() || autoTitle({ task: chosenTaskUnit(), prompt }),
          model,
          effort,
          access_mode: access,
          agent,
          task,
          cwd: worktree ? "" : cwdInput.value.trim(),
          worktree,
          prompt: firstMessage(prompt, extraField.classList.contains("hidden") ? "" : extraArea.value),
          parent_id: parent?.id || "",
          start,
        });
        saveLaunchDefaults(store, user, projectId, { platform, model, effort, access, worktree, agent, start: startOption.id });
        rememberSessionChoice({ platform, model });
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

function boardView(ctx, S, tasks) {
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
  // Workspace support: live sessions and agents, hosts, library and
  // deadlines (support-panel.js, GET /api/project/support).
  const meta = h("span", { class: "task-support-meta" }, t("support.loading"));
  const support = h("details", { class: "task-support", open: true },
    h("summary", null,
      h("span", null, icon("layers", "sm"), ` ${t("tasks.support.title")}`),
      meta),
    supportDeck(ctx, { onMeta: (text) => { meta.textContent = text; } }));
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
