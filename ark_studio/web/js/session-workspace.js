// Tasks → Sessions workspace: session rail, real conversation thread, details rail.
//
// The transport is line-buffered stdout/stderr, so the thread renders complete
// lines from GET /api/agent-sessions/{id}/transcript (polled).  New lines are
// patched into a keyed conversation column; the UI never pretends to have
// token-level streaming and shows the honest session state.

import { h, clear, richText, emptyState, fmtDateTime, relativeTime, copyText, download, highlightCode } from "./dom.js";
import { t, locale, localizeServer } from "./i18n.js";
import { icon } from "./icons.js";
import { openImageLightbox as openSharedLightbox } from "./image-viewer.js";
import { api, peekCached } from "./api.js";
import { confirmDialog, openModal } from "./components.js";
import {
  filterSessions, groupSessions as groupSessionsEn, isActiveSession, mediaKind,
  nextSelection, sessionAncestors, sessionDescendants,
  sessionTitle as sessionTitleEn, sortSessions, formatBytes,
  SESSION_TAB_STORE_KEY, activateFileTab, closeFileTab, composerMode as composerModeEn,
  fileLanguage, fileViewKind, formatTokens, normalizeTabState, openFileTab,
  steerLabel as steerLabelEn, tabLabel as tabLabelEn, worktreeLabel, worktreeConflict,
  approvalRequestLabel as approvalRequestLabelEn, approvalStatusLabel as approvalStatusLabelEn,
  canDecideApproval, editableSteer,
  forkRelationLabel as forkRelationLabelEn, isApprovalMode, pendingApprovals,
  permissionModeDescription as permissionModeDescriptionEn,
  permissionModeLabel as permissionModeLabelEn, queuedSteerItems, sessionAccessMode, sessionPermissionModes,
  sessionSupportsApproval, steerMoveOrder,
  blockSignature, liveActivity, threadBlocks, sessionPhase, shownStatus, busyWork, busyReason, pendingSteerItems, toolCommand, toolGroupSummary, toolKind, toolPaths,
  CUSTOM_MODEL_VALUE, defaultModelOption, modelEfforts, modelOptionLabel as modelOptionLabelEn, modelOptions,
  modelPickerGroups, modelSourceSummary,
} from "./session-model.js";
import {
  sessionStream, attachSessionContext, sendSessionMessage, stopAgentSession, sendQueuedNow, fullToolItem, sessionImages, toolImage,
  renameAgentSession,
} from "./session-stream.js";
import { TRANSCRIPT_SHARE_MS, transcriptKey } from "./session-stream-model.js";
import { capFirst, fmtDuration, textValue, renderToolGroup as renderToolGroupRow, inlineRename } from "./session-thread-parts.js";
import { createActivityChips, activityRow } from "./session-activity.js";
import { activityList, blockKeyFor } from "./session-activity-model.js";
import {
  composerKeyAction, composerHintKey, sendNowMode, imageContext, windowBlocks, drawLimit, turnRunning, stopLabelKey, stopBackgroundMode,
  loadAgentTabs, saveAgentTabs, openAgentTab as openAgentTabIn, closeAgentTab, activateAgentTab,
  tabRoot, openForkTab,
} from "./session-sidebar-model.js";
import { readImages, releaseImages, imageTray, bindImageInput, imagePicker, turnImages } from "./session-images.js";
import { openContextMenu, contextMenuOpen } from "./context-menu.js";
import { closeDeleteItems, stopBackgroundItems } from "./session-menu.js";
import { longPressMenu, swipeDownToClose, watchKeyboard } from "./session-touch.js";
import { loadBlocks, toggleBlock } from "./session-sidebar-model.js";
import { openTranscript } from "./session-library.js";
import { delegateImageContextMenu } from "./image-copy.js";

// session-model.js is dependency-free; inject the app translator here.
const withT = { t };
const groupSessions = (sessions, groupBy) => groupSessionsEn(sessions, groupBy, withT);
const sessionTitle = (session) => sessionTitleEn(session, withT);
const composerMode = (session) => composerModeEn(session, withT);
const steerLabel = (item) => steerLabelEn(item, withT);
const tabLabel = (path) => tabLabelEn(path, withT);
const approvalRequestLabel = (item) => approvalRequestLabelEn(item, withT);
const approvalStatusLabel = (item) => approvalStatusLabelEn(item, withT);
const forkRelationLabel = (session) => forkRelationLabelEn(session, withT);
const permissionModeLabel = (mode) => permissionModeLabelEn(mode, withT);
const permissionModeDescription = (mode) => permissionModeDescriptionEn(mode, withT);

// Labels are i18n keys, resolved with t() at render time.
const FILTERS = [["all", "action.all"], ["active", "session.group.active"],
  ["completed", "session.group.completed"], ["subagents", "session.filter.subagents"]];
const GROUPS = [["status", "filters.status"], ["worktree", "session.group_by.worktree"],
  ["agent", "tasks.filter_agent"], ["task", "type.task"]];
const RAIL_TABS = [
  ["overview", "nav.overview", null],
  ["subagents", "session.filter.subagents", "count"],
  ["background", "session.activity.background_title", "count"],
  ["media", "inspector.media", "count"],
  ["docs", "session.tab.docs", "count"],
  ["artifacts", "tasks.session_artifacts", null],
];
const STREAM_ACTIVE = ["starting", "running", "idle", "stopping"];

function statusText(status) {
  const key = `tasks.status.${status}`;
  return t(key) === key ? status : t(key);
}

function platformLabel(session) {
  const id = session?.platform || session?.id || "";
  const key = `tasks.platform.${id}`;
  const translated = t(key);
  return translated === key ? (session?.platform_label || id) : translated;
}

function statusDot(status) {
  return h("span", { class: `sw-dot ${status}`.trim(), title: statusText(status) });
}

function statusPill(status) {
  return h("span", { class: `sw-status ${status}`.trim() }, h("i"), statusText(status));
}

function sessionCwd(session) {
  if (session?.worktree?.path) return session.worktree.path;
  const cwd = session?.cwd || "";
  return !cwd || cwd === "." ? t("tasks.workspace_root") : cwd;
}

function sessionTime(value) {
  if (!value) return "—";
  return relativeTime(value);
}




export function renderSessionWorkspace(ctx, { onLaunch, tasks = [], related: relatedNode = null, library = null, docked = false } = {}) {
  const S = ctx.S;
  const bootQuery = new URLSearchParams(location.search);
  let sessions = sortSessions(S.taskSessions || []);
  let groups = Array.isArray(S.sessionGroups) ? S.sessionGroups : [];
  let selectedId = nextSelection(sessions, bootQuery.get("session") || S.selectedSessionId);
  let query = S.sessionQuery || "";
  let filter = S.sessionFilter || "all";
  let groupBy = S.sessionGroupBy || "status";
  let railTab = ["overview", "subagents", "background", "media", "docs", "artifacts"].includes(bootQuery.get("rail"))
    ? bootQuery.get("rail") : (S.sessionRailTab || "overview");
  let threadMode = S.sessionThreadMode || "thread";
  let pendingFiles = bootQuery.getAll("file").filter(Boolean);
  if (railTab !== "overview") S.sessionRailTab = railTab;

  let transcript = null;
  let transcriptFor = "";
  let transcriptLoading = false;
  let transcriptError = "";
  let transcriptSeq = 0;
  let detail = null;
  let detailFor = "";
  let detailLoading = false;
  let media = null;
  let mediaFor = "";
  let mediaLoading = false;
  let mediaError = "";
  let taskArtifacts = null;
  let taskArtifactsFor = "";
  let taskArtifactsLoading = false;
  let taskArtifactsError = "";
  const openTools = new Set();
  const blockNodes = new Map();
  let stickToBottom = true;
  let unseen = false;
  let liveTimer = null;
  let liveState = null;
  let composerInput = null;
  let collapsedGroups = new Set();
  let revealedToken = "";
  let contextOpen = false;
  let steerEditing = null;
  const approvalBusy = new Set();
  let artifactState = { path: "", for: "", data: null, loading: false, error: "", preview: null, previewLoading: false };
  let destroyed = false;
  let lastSessionSignature = "";
  let composerSignature = "";
  let pollingTranscript = false;
  let rawState = { stream: "stdout", offsets: { stdout: 0, stderr: 0 }, entries: [], following: true, loading: false, error: "", for: "" };
  let presets = [];
  let presetsLoaded = false;
  let presetsError = "";
  let draftBySession = {};
  // Shared with the Sessions sidebar view: context attached there shows here.
  const contextBySession = sessionStream.contexts;
  const composerChoices = new Map();
  let contextPickerOpen = false;
  let composerConfigOpen = false;
  let reasoningOpen = false;
  const fileTabs = loadTabStore();
  const fileCache = new Map();
  const fileModes = new Map();

  function loadTabStore() {
    try {
      const raw = JSON.parse(localStorage.getItem(SESSION_TAB_STORE_KEY) || "{}");
      if (!raw || typeof raw !== "object" || Array.isArray(raw)) return {};
      const out = {};
      for (const [id, value] of Object.entries(raw)) {
        const state = normalizeTabState(value);
        if (state.tabs.length) out[id] = state;
      }
      return out;
    } catch {
      return {};
    }
  }

  function persistTabStore() {
    try { localStorage.setItem(SESSION_TAB_STORE_KEY, JSON.stringify(fileTabs)); } catch { /* storage disabled */ }
  }

  function tabState(session) {
    return normalizeTabState(fileTabs[session.id]);
  }

  function setTabState(session, state) {
    const normalized = normalizeTabState(state);
    if (normalized.tabs.length) fileTabs[session.id] = normalized;
    else delete fileTabs[session.id];
    persistTabStore();
  }

  function cacheFileKey(session, path) {
    return `${session.id}\u0000${path}`;
  }

  function patchSessionPayload(payload) {
    if (!payload?.id) return;
    const index = sessions.findIndex((session) => session.id === payload.id);
    if (index < 0) return;
    sessions[index] = { ...sessions[index], ...payload };
    const source = (S.taskSessions || []).find((session) => session.id === payload.id);
    if (source) Object.assign(source, payload);
    paintList();
    paintMain();
  }

  const listCount = h("span", { class: "sw-count" });
  const groupBar = h("div", { class: "sw-group-summary hidden" });
  const listBody = h("div", { class: "sw-list" });
  const searchInput = h("input", {
    class: "input sw-search", type: "search", value: query,
    placeholder: t("session.rail.search_placeholder"), "aria-label": t("session.rail.search_label"),
    oninput: (event) => {
      query = event.target.value;
      S.sessionQuery = query;
      paintList();
    },
  });
  const filterButtons = {};
  const filterBar = h("div", { class: "sw-filterbar" });
  for (const [id, labelKey] of FILTERS) {
    const button = h("button", {
      class: `sw-filter ${filter === id ? "on" : ""}`.trim(), type: "button",
      onclick: () => {
        filter = id;
        S.sessionFilter = id;
        for (const [key, item] of Object.entries(filterButtons)) item.classList.toggle("on", key === id);
        paintList();
      },
    }, t(labelKey));
    filterButtons[id] = button;
    filterBar.appendChild(button);
  }
  const groupSelect = h("select", {
    class: "select sw-group-select", "aria-label": t("session.rail.group_label"),
    onchange: (event) => { groupBy = event.target.value; S.sessionGroupBy = groupBy; paintList(); },
  }, GROUPS.map(([id, labelKey]) => h("option", { value: id, selected: groupBy === id },
    t("session.rail.group_option", { label: t(labelKey) }))));

  const rail = h("aside", { class: "sw-rail", "aria-label": t("session.rail.label") },
    h("div", { class: "sw-rail-head" },
      h("div", { class: "sw-rail-title" }, h("span", null, t("tasks.sessions")), listCount),
      h("div", { class: "sw-rail-actions" },
        h("button", {
          class: "icon-btn sw-mobile-btn", type: "button", title: t("session.rail.close"),
          "aria-label": t("session.rail.close"), onclick: () => closeMobilePanels(),
        }, icon("x", "sm")),
        h("button", { class: "icon-btn", title: t("tasks.launch"), "aria-label": t("tasks.launch"), onclick: () => onLaunch() }, icon("plus", "sm")),
        h("button", { class: "icon-btn", title: t("action.refresh"), "aria-label": t("action.refresh"), onclick: () => refreshSessions() }, icon("refresh", "sm")))),
    h("div", { class: "sw-rail-tools" }, searchInput, filterBar, groupSelect),
    groupBar,
    listBody,
    h("div", { class: "sw-rail-foot" }, h("span", { class: "sw-live-dot" }), t("session.rail.foot")));

  const mainHeader = h("header", { class: "sw-main-head" });
  const mainBar = h("div", { class: "sw-main-bar" });
  const thread = h("div", { class: "sw-thread", role: "log", "aria-live": "polite" });
  const column = h("div", { class: "sw-column" });
  const liveLabelNode = h("span", { class: "sw-live-label" });
  const liveTimeNode = h("span", { class: "sw-live-time" });
  const liveRow = h("div", { class: "sw-live-row hidden", role: "status" },
    h("span", { class: "sw-live-spark" }, h("i"), h("i"), h("i")), liveLabelNode, liveTimeNode);
  const jumpLabel = h("span", null, "");
  const jumpButton = h("button", {
    class: "sw-jump", type: "button", onclick: () => scrollThread(true, true),
  }, icon("arrowDown", "sm"), jumpLabel);
  jumpButton.classList.add("hidden");
  const threadWrap = h("div", { class: "sw-thread-wrap" }, thread, jumpButton);
  const composerHost = h("div", { class: "sw-composer" });
  // Sub-agents and background tasks of the open session (Paseo-style chips).
  const activityChips = createActivityChips({
    onOpenItem: (itemId) => revealThreadItem(itemId),
    onOpenSession: (id) => select(id),
    onOpenAgent: (entry) => openAgent(entry.itemId, entry.label),
  });
  // Main conversation and the sub-agents opened from it, as tabs.
  const tabStrip = h("div", { class: "sw-tabs", role: "tablist", hidden: true });
  const main = h("section", { class: "sw-main" }, mainHeader, mainBar, tabStrip, threadWrap, activityChips.el, composerHost);

  const sideHead = h("div", { class: "sw-side-head" });
  const sideTabs = h("div", { class: "sw-side-tabs", role: "tablist" });
  const sideBody = h("div", { class: "sw-side-body", role: "tabpanel" });
  const side = h("aside", { class: "sw-side", "aria-label": t("session.details.title") }, sideHead, sideTabs, sideBody);

  // Phone drawers: the rail and details panel become off-canvas sheets at
  // <=760px while the conversation keeps the full width.  Desktop layout is
  // untouched by these classes.
  const mobileScrim = h("div", {
    class: "sw-mobile-scrim hidden", role: "presentation",
    onclick: () => closeMobilePanels(),
  });
  const mobileQuery = typeof window.matchMedia === "function"
    ? window.matchMedia("(max-width: 760px)") : null;
  const isPhone = () => Boolean(mobileQuery?.matches);
  // An open sheet is a history entry: the Android back gesture closes it
  // before it leaves the page.
  function closeMobilePanels(fromHistory = false) {
    const open = rail.classList.contains("mobile-open") || side.classList.contains("mobile-open");
    rail.classList.remove("mobile-open");
    side.classList.remove("mobile-open");
    mobileScrim.classList.add("hidden");
    if (open && !fromHistory && history.state?.swSheet) history.back();
  }
  function openMobilePanel(panel) {
    if (!isPhone()) return;
    rail.classList.toggle("mobile-open", panel === "rail");
    side.classList.toggle("mobile-open", panel === "side");
    mobileScrim.classList.toggle("hidden", !panel);
    if (panel && !history.state?.swSheet) history.pushState({ ...(history.state || {}), swSheet: panel }, "");
  }
  const onHistory = () => {
    if (destroyed) { window.removeEventListener("popstate", onHistory); return; }
    if (!history.state?.swSheet) closeMobilePanels(true);
  };
  window.addEventListener("popstate", onHistory);
  const railHandle = h("div", { class: "sw-sheet-handle", "aria-hidden": "true" });
  const sideHandle = h("div", { class: "sw-sheet-handle", "aria-hidden": "true" });
  rail.prepend(railHandle);
  side.prepend(sideHandle);
  swipeDownToClose(rail, [railHandle], () => closeMobilePanels());
  swipeDownToClose(side, [sideHandle, sideHead], () => closeMobilePanels());
  if (isPhone()) watchKeyboard();

  // Stored session contexts (ARK and uploaded) sit under the session list.
  if (library) rail.appendChild(library);
  // In the Sessions view: the view's header is the only header; the list and
  // the grouping info are collapsible blocks (state remembered).
  if (docked || isPhone()) {
    const tools = rail.querySelector(".sw-rail-tools");
    const foot = rail.querySelector(".sw-rail-foot");
    listCount.title = t("session.rail.foot");
    const block = (key, label, extra, ...parts) => {
      const state = loadBlocks();
      const head = h("button", { class: "sw-block-head", type: "button", "aria-expanded": state[key] ? "true" : "false",
        dataset: { block: key } }, icon("chevronRight", "sm"), h("span", null, label), extra);
      const body = h("div", { class: "sw-block-body", hidden: !state[key] }, ...parts);
      head.addEventListener("click", () => {
        const next = toggleBlock(key);
        body.hidden = !next[key];
        head.setAttribute("aria-expanded", next[key] ? "true" : "false");
      });
      return h("div", { class: `sw-block ${key}` }, head, body);
    };
    const listBlock = block("list", t("session.block.list"), listCount, tools, listBody);
    const groupBlock = block("groups", t("session.block.groups"), null, groupBar);
    if (docked) rail.querySelector(".sw-rail-head")?.remove();   // the view's header is the header
    foot?.remove();
    rail.prepend(listBlock, groupBlock);
    // Phone sheet: its handle and its header (close, launch, refresh) on top.
    const railHead = rail.querySelector(".sw-rail-head");
    if (railHead) rail.prepend(railHead);
    rail.prepend(railHandle);
  }
  const root = h("div", { class: `sw-workspace ${docked ? "sw-docked" : ""}`.trim() }, rail, main, side);
  // Wide window: the list goes to the Sessions view, the details to the
  // Session properties view; the conversation takes the page.
  if (docked) {
    rail.classList.add("sw-docked-part");
    side.classList.add("sw-docked-part");
    ctx.sessionPage?.mount({ list: rail, props: side });
  }
  const resizer = h("div", {
    class: "sw-resizer", role: "separator", "aria-orientation": "horizontal",
    title: t("session.resize_hint"),
  }, h("span", { class: "sw-resizer-grip" }));
  const wrap = h("div", { class: "sw-shell" }, root, resizer, mobileScrim, relatedNode);
  const storedHeight = Number(localStorage.getItem("ark.sessionWorkspaceHeight") || 0);
  if (storedHeight >= 360) root.style.height = `${storedHeight}px`;
  resizer.addEventListener("dblclick", () => {
    root.style.height = "";
    localStorage.removeItem("ark.sessionWorkspaceHeight");
  });
  resizer.addEventListener("pointerdown", (event) => {
    event.preventDefault();
    const startY = event.clientY;
    const startH = root.getBoundingClientRect().height;
    const min = 360;
    // The default CSS height is the previous maximum; the manual ceiling is
    // raised beyond it so the user can deliberately make the workspace taller.
    const max = Math.max(min, Math.min(1600, window.innerHeight - 60));
    resizer.classList.add("dragging");
    document.body.classList.add("sw-resizing");
    const move = (ev) => {
      const height = Math.max(min, Math.min(max, startH + ev.clientY - startY));
      root.style.height = `${Math.round(height)}px`;
    };
    const up = () => {
      resizer.classList.remove("dragging");
      document.body.classList.remove("sw-resizing");
      localStorage.setItem("ark.sessionWorkspaceHeight", String(Math.round(root.getBoundingClientRect().height)));
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  });

  // --------------------------------------------------------------- selection
  function current() {
    return sessions.find((session) => session.id === selectedId) || null;
  }

  function select(id) {
    if (!id) return;
    if (id === selectedId) {
      closeMobilePanels();
      return;
    }
    selectedId = id;
    S.selectedSessionId = id;
    transcript = null;
    transcriptFor = "";
    transcriptError = "";
    transcriptLoading = true;
    transcriptSeq += 1;
    detail = null;
    detailFor = "";
    media = null;
    mediaFor = "";
    mediaError = "";
    taskArtifacts = null;
    taskArtifactsFor = "";
    revealedToken = "";
    contextOpen = false;
    contextPickerOpen = false;
    steerEditing = null;
    composerSignature = "";
    artifactState = { path: "", for: "", data: null, loading: false, error: "", preview: null, previewLoading: false };
    rawState = { stream: "stdout", offsets: { stdout: 0, stderr: 0 }, entries: [], following: true, loading: false, error: "", for: id };
    openTools.clear();
    activityCount = "";
    mainData = null;
    pageLimit = PAGE_BLOCKS;
    activityChips.update([], { session: null });
    paintList();
    paintMain();
    paintSide();
    ctx.updateTopbar?.();
    closeMobilePanels();
    if (threadMode === "thread") loadTranscript(true);
    else loadRaw(true);
    loadDetail();
    ensureTabData();
    ensureActiveFile();
    applyPendingFile();
  }

  function applyPendingFile() {
    const session = current();
    if (!pendingFiles.length || !session) return;
    const targets = pendingFiles;
    pendingFiles = [];
    let state = tabState(session);
    for (const target of targets) state = openFileTab(state, target);
    setTabState(session, state);
    if (railTab !== "docs" && railTab !== "artifacts") {
      railTab = "docs";
      S.sessionRailTab = railTab;
    }
    composerSignature = "";
    paintSide();
    for (const target of targets) ensureFile(session, target);
  }

  // ----------------------------------------------------------------- session
  function applySessions(nextSessions, nextGroups) {
    if (destroyed) return;
    sessions = sortSessions(nextSessions || []);
    S.taskSessions = nextSessions || [];
    if (Array.isArray(nextGroups)) {
      groups = nextGroups;
      S.sessionGroups = nextGroups;
    }
    S.sessionsLoaded = true;
    paintGroups();
    const signature = sessionSignature(sessions);
    const changed = signature !== lastSessionSignature;
    const nextId = nextSelection(sessions, selectedId);
    if (nextId !== selectedId) {
      selectedId = nextId;
      S.selectedSessionId = selectedId || null;
      transcript = null;
      transcriptFor = "";
      transcriptError = "";
      transcriptLoading = true;
      detail = null;
      detailFor = "";
      media = null;
      mediaFor = "";
      taskArtifacts = null;
      taskArtifactsFor = "";
      revealedToken = "";
      contextOpen = false;
      contextPickerOpen = false;
      composerSignature = "";
      artifactState = { path: "", for: "", data: null, loading: false, error: "", preview: null, previewLoading: false };
      rawState = { stream: "stdout", offsets: { stdout: 0, stderr: 0 }, entries: [], following: true, loading: false, error: "", for: selectedId };
      lastSessionSignature = signature;
      paintAll();
      ctx.updateTopbar?.();
      if (selectedId) {
        if (threadMode === "thread") loadTranscript(true);
        else loadRaw(true);
        loadDetail();
        ensureTabData();
        ensureActiveFile();
        applyPendingFile();
      }
      return;
    }
    if (signature !== lastSessionSignature) {
      lastSessionSignature = signature;
      paintList();
      paintMain();
      syncComposer();
      if (railTab === "overview") paintSide();
      if (mainData) refreshActivity(mainData.activity || mainData.items || [], current());
    }
    ctx.updateTopbar?.();
    if (selectedId) {
      const session = current();
      const needsTranscript = changed || isActiveSession(session) || !transcript || transcriptFor !== selectedId;
      if (threadMode === "thread" && needsTranscript) loadTranscript(false);
      else if (threadMode === "raw" && (changed || isActiveSession(session))) loadRaw(false);
    }
  }

  function sessionSignature(items) {
    return items.map((session) => [
      session.id, session.status, session.title || "", session.last_activity_at,
      session.exit_code, session.error, session.counts?.stdout || 0, session.counts?.stderr || 0,
    ].join(":")).join("|");
  }

  async function refreshSessions() {
    try {
      const data = await sessionStream.refreshSessions();
      applySessions(data.sessions || [], data.groups || []);
    } catch (err) { ctx.toast(err.message, "err"); }
  }

  function openSideChat(parent) {
    if (!parent) return;
    const preset = presetById(parent.platform) || null;
    const modes = sessionPermissionModes(parent, preset);
    const inheritedMode = sessionAccessMode(parent, modes);
    const promptArea = h("textarea", {
      class: "textarea", rows: "4", style: { minHeight: "92px" },
      placeholder: t("session.side_chat.prompt_placeholder"),
    });
    const modeSelect = h("select", { class: "select", "aria-label": t("session.side_chat.mode_label") },
      modes.map((mode) => h("option", {
        value: mode, selected: mode === inheritedMode, title: permissionModeDescription(mode),
      }, permissionModeLabel(mode))));
    const body = h("div", { class: "launch-form" },
      h("div", { class: "field full" },
        h("label", null, t("session.side_chat.prompt_label")), promptArea),
      h("div", { class: "field" },
        h("label", null, t("session.provider")),
        h("div", { class: "session-hint" },
          `${parent.platform_label || parent.platform} · ${parent.model || t("session.side_chat.default_model")}`)),
      h("div", { class: "field" },
        h("label", null, t("session.permission_mode")), modeSelect,
        h("div", { class: "session-hint" },
          t("session.side_chat.mode_hint"))),
      h("div", { class: "full notice" }, icon("git", "sm"),
        h("div", null, t("session.side_chat.notice", { id: parent.id }))));
    openModal({
      title: t("session.side_chat.title", { title: sessionTitle(parent) }),
      body,
      wide: true,
      actions: [
        { label: t("action.cancel"), ghost: true },
        {
          label: t("session.side_chat.start"), primary: true,
          onClick: async () => {
            const prompt = promptArea.value.trim();
            if (!prompt) { promptArea.focus(); return false; }
            try {
              const data = await api.forkSession(parent.id, {
                prompt, access_mode: modeSelect.value,
              });
              await refreshSessions();
              select(data.session.id);
              ctx.toast(t("session.toast.side_chat_started"), "ok");
              return true;
            } catch (err) {
              ctx.toast(err.message, "err");
              return false;
            }
          },
        },
      ],
    });
  }

  async function stopSession() {
    const session = current();
    if (!session) return;
    try {
      await stopAgentSession(session.id);
      session.status = "stopping";
      paintList();
      paintMain();
      ctx.toast(t("tasks.status.stopping"), "ok");
    } catch (err) { ctx.toast(err.message, "err"); }
  }

  async function relaunchSession() {
    const session = current();
    if (!session) return;
    try {
      const data = await api.relaunchSession(session.id);
      await refreshSessions();
      select(data.session.id);
      ctx.toast(t("session.toast.relaunched"), "ok");
    } catch (err) { ctx.toast(err.message, "err"); }
  }

  async function archiveSession() {
    const session = current();
    if (!session) return;
    try {
      if (!(await confirmDialog(t("tasks.archive_confirm"), {
        title: t("tasks.archive"), confirmLabel: t("tasks.archive"),
      }))) return;
      await api.archiveSession(session.id);
      ctx.toast(t("tasks.archived_toast"), "ok");
      await refreshSessions();
    } catch (err) { ctx.toast(err.message, "err"); }
  }

  async function deleteSession() {
    const session = current();
    if (!session) return;
    try {
      if (!(await confirmDialog(t("tasks.delete_confirm"), {
        title: t("tasks.delete"), confirmLabel: t("tasks.delete"), danger: true,
      }))) return;
      await api.deleteSession(session.id);
      ctx.toast(t("tasks.deleted_toast"), "ok");
      selectedId = "";
      S.selectedSessionId = null;
      await refreshSessions();
    } catch (err) { ctx.toast(err.message, "err"); }
  }

  // -------------------------------------------------------------------- list
  function paintGroups() {
    clear(groupBar);
    const activeGroups = (groups || []).filter((group) => group.active > 0);
    const conflicts = activeGroups.filter((group) => group.conflict);
    const busyWorktrees = activeGroups.filter((group) => group.kind === "worktree").length;
    if (!activeGroups.length) {
      groupBar.classList.add("hidden");
      return;
    }
    groupBar.classList.remove("hidden");
    groupBar.classList.toggle("has-conflict", conflicts.length > 0);
    groupBar.appendChild(h("div", { class: "sw-group-summary-head" },
      icon(conflicts.length ? "alert" : "layers", "sm"),
      h("b", null, t(activeGroups.length === 1 ? "session.groups.active_one" : "session.groups.active_n", { n: activeGroups.length })),
      h("span", null, busyWorktrees
        ? t(busyWorktrees === 1 ? "session.groups.worktrees_one" : "session.groups.worktrees_n", { n: busyWorktrees })
        : t("session.groups.private_only"))));
    for (const group of activeGroups.slice(0, 3)) {
      groupBar.appendChild(h("div", { class: `sw-group-summary-row ${group.conflict ? "conflict" : ""}`.trim() },
        h("span", { class: "sw-mini-tag" }, t(group.kind === "worktree" ? "session.tag.worktree" : "session.tag.project")),
        h("span", { class: "sw-group-summary-label", title: group.worktree?.path || group.project?.path || "" },
          group.label || group.id),
        h("span", { class: "sw-group-summary-count" }, `${group.active}`),
        group.conflict ? h("span", { class: "sw-group-summary-warn" }, t("session.groups.conflict")) : null));
    }
  }

  // Polls repaint the list and the header; not while a menu or a rename is
  // open on them (the repaint would close or drop it).
  let renaming = false;
  let heldPaint = false;
  function held() {
    if (!renaming && !contextMenuOpen()) return false;
    if (!heldPaint) {
      heldPaint = true;
      const retry = setInterval(() => {
        if (renaming || contextMenuOpen()) return;
        clearInterval(retry);
        heldPaint = false;
        paintList();
        paintMain();
      }, 250);
    }
    return true;
  }

  function paintList() {
    if (held()) return;
    clear(listBody);
    const visible = filterSessions(sessions, { query, filter });
    listCount.textContent = visible.length === sessions.length
      ? String(sessions.length) : `${visible.length}/${sessions.length}`;
    if (!sessions.length) {
      listBody.appendChild(emptyState(t("tasks.sessions_empty"), t("tasks.sessions_empty_hint"),
        h("button", { class: "btn primary", onclick: () => onLaunch() }, icon("play", "sm"), t("tasks.launch"))));
      return;
    }
    if (!visible.length) {
      listBody.appendChild(emptyState(t("session.rail.no_match"), t("session.rail.no_match_hint")));
      return;
    }
    const groups = groupSessions(visible, groupBy);
    for (const group of groups) {
      const collapsed = collapsedGroups.has(group.id);
      listBody.appendChild(h("div", { class: "sw-group" },
        h("button", {
          class: `sw-group-head ${collapsed ? "collapsed" : ""}`.trim(), type: "button",
          "aria-expanded": collapsed ? "false" : "true",
          onclick: () => {
            if (collapsed) collapsedGroups.delete(group.id);
            else collapsedGroups.add(group.id);
            paintList();
          },
        },
          icon("chevron", "sm"),
          h("span", { class: "sw-group-label" }, group.label),
          h("span", { class: "sw-group-count" }, group.sessions.length)),
        collapsed ? null : h("div", { class: "sw-group-items" },
          group.sessions.map((session) => sessionRow(session)))));
    }
  }

  async function saveTitle(session, title) {
    try {
      await renameAgentSession(session.id, title);
      const local = sessions.find((item) => item.id === session.id);
      if (local) local.title = title;
      paintList();
      paintMain();
    } catch (err) { ctx.toast(err.message, "err"); }
  }

  function startRename(host, session) {
    if (!host) return;
    renaming = true;
    const input = inlineRename(host, session.title || "", (title) => saveTitle(session, title), { label: t("session.menu.rename") });
    input.addEventListener("blur", () => { renaming = false; });
    input.addEventListener("keydown", (event) => { if (event.key === "Enter" || event.key === "Escape") renaming = false; });
  }

  function renameRow(session) {
    startRename(listBody.querySelector(`[data-session-row="${CSS.escape(session.id)}"] .sw-session-title`), session);
  }

  function renameHeader(session) {
    startRename(mainHeader.querySelector("h2"), session);
  }

  // Stored context of listed sessions (sizes, "View transcript").
  let storedSizes = {};
  let storedContexts = {};
  function setStoredContexts(section) {
    storedSizes = section?.sizes || {};
    storedContexts = Object.fromEntries((section?.listed || []).map((item) => [item.id, item]));
    paintList();
  }

  function sessionRow(session) {
    const row = sessionRowEl(session);
    longPressMenu(row);
    return row;
  }

  function sessionRowEl(session) {
    const active = isActiveSession(session);
    const depth = Math.max(0, Number(session.depth || 0));
    return h("button", {
      class: `sw-session ${session.id === selectedId ? "on" : ""} ${session.status} ${session.parent_id ? "subagent" : ""}`.trim(),
      type: "button",
      "aria-current": session.id === selectedId ? "true" : undefined,
      style: depth ? { paddingLeft: `${9 + depth * 11}px` } : undefined,
      title: session.title || session.id,
      dataset: { sessionRow: session.id },
      onclick: () => select(session.id),
      oncontextmenu: (event) => {
        event.preventDefault();
        openContextMenu({ x: event.clientX, y: event.clientY, returnFocus: event.currentTarget, items: [
          { label: t("session.menu.rename"), icon: "edit", onClick: () => renameRow(session) },
          storedContexts[session.id] ? { label: t("ctx.view"), icon: "file", onClick: () => openTranscript(storedContexts[session.id]) } : null,
          { label: t("session.copy_id"), icon: "copy", onClick: () => copyText(session.id) },
          ...closeDeleteItems(ctx, session, sessionTitle(session)),
        ].filter(Boolean) });
      },
      onkeydown: (event) => {
        if (event.key === "F2") { event.preventDefault(); renameRow(session); }
      },
    },
      h("div", { class: "sw-session-top" },
        statusDot(shownStatus(session, activityOf(session))),
        h("span", { class: "sw-session-title" }, sessionTitle(session)),
        h("span", { class: "sw-session-time", title: session.last_activity_at || "" }, sessionTime(session.last_activity_at))),
      h("div", { class: "sw-session-meta" },
        busyNote(session, { reasonOnly: true }),
        session.parent_id ? h("span", { class: "sw-mini-tag sub" }, t("session.tag.subagent")) : null,
        forkRelationLabel(session) ? h("span", { class: "sw-mini-tag side" }, forkRelationLabel(session)) : null,
        h("span", null, platformLabel(session)),
        session.closed ? h("span", { class: "sw-mini-tag closed" }, t("session.tag.closed")) : null,
        storedSizes[session.id] ? h("span", { class: "sw-sep" }, "·") : null,
        storedSizes[session.id] ? h("span", { class: "sw-mono", title: t("ctx.stored") }, formatBytes(storedSizes[session.id])) : null,
        session.model ? h("span", { class: "sw-sep" }, "·") : null,
        session.model ? h("span", { class: "sw-mono" }, session.model) : null,
        session.agent ? h("span", { class: "sw-sep" }, "·") : null,
        session.agent ? h("span", null, session.agent) : null),
      h("div", { class: "sw-session-foot" },
        worktreeLabel(session)
          ? h("span", {
            class: `sw-mini-tag branch ${worktreeConflict(session) ? "conflict" : ""}`.trim(),
            title: session.worktree?.path || "",
          }, worktreeConflict(session) ? t("session.tag.shared") : worktreeLabel(session))
          : null,
        session.task_ref ? h("span", { class: "sw-mini-tag task" }, session.task_ref) : h("span", { class: "sw-cwd" }, sessionCwd(session)),
        h("span", { class: "spacer" }),
        active ? h("span", { class: "sw-stream-hint" }, h("i"), t("session.tag.live")) : h("span", { class: "sw-ended" }, statusText(session.status))));
  }

  // ------------------------------------------------------------------- main
  function paintAll() {
    paintList();
    paintMain();
    paintSide();
  }

  function paintMain() {
    if (held()) {
      paintThread();
      syncComposer();
      return;
    }
    clear(mainHeader);
    clear(mainBar);
    const session = current();
    if (!session) {
      mainHeader.appendChild(h("div", { class: "sw-main-empty sw-empty-line" },
        h("span", null, t("session.empty_line")),
        h("button", { class: "btn sm primary", type: "button", "data-new-session": "", onclick: () => onLaunch() },
          icon("plus", "sm"), t("session.sidebar.new"))));
      threadWrap.classList.add("hidden");
      composerHost.classList.add("hidden");
      if (isPhone()) openMobilePanel("rail");
      return;
    }
    threadWrap.classList.remove("hidden");
    composerHost.classList.remove("hidden");
    const active = isActiveSession(session);
    const title = sessionTitle(session);
    const pendingCount = pendingApprovals(session).length;
    if (isPhone()) {
      // Phone: one compact row; everything else is in the menu.
      const more = h("button", {
        class: "icon-btn sw-more sw-phone-btn", type: "button", title: t("session.menu.more"), "aria-label": t("session.menu.more"),
        "aria-haspopup": "menu", "data-session-menu": "",
        onclick: (event) => {
          const rect = event.currentTarget.getBoundingClientRect();
          openSessionMenu(rect.left, rect.bottom + 4, event.currentTarget);
        },
      }, icon("more", "sm"));
      const titleEl = h("div", { class: "sw-phone-title", title },
        statusDot(shownStatus(session, activityOf(session))), h("h2", null, title), busyNote(session, { reasonOnly: true }));
      mainHeader.appendChild(h("div", { class: "sw-phone-head" },
        h("button", {
          class: "sw-mobile-btn sw-phone-btn", type: "button", title: t("session.rail.open"), "aria-label": t("session.rail.open"),
          "data-phone-list": "", onclick: () => openMobilePanel("rail"),
        }, icon("list", "sm")),
        titleEl,
        pendingCount ? h("button", {
          class: "sw-mobile-btn sw-phone-btn attention", type: "button", title: t("session.details.open"),
          onclick: () => openMobilePanel("side"),
        }, icon("shield", "sm"), String(pendingCount)) : null,
        more));
      longPressMenu(titleEl);
      titleEl.addEventListener("contextmenu", (event) => { event.preventDefault(); openSessionMenu(event.clientX, event.clientY, titleEl); });
    } else {
      mainHeader.appendChild(h("div", { class: "sw-title-line" },
        statusDot(shownStatus(session, activityOf(session))),
        h("h2", { title }, title),
        statusPill(shownStatus(session, activityOf(session))),
        busyNote(session, { reasonOnly: true }),
        session.parent_id ? h("span", { class: "sw-mini-tag sub" }, t("session.tag.subagent")) : null,
        forkRelationLabel(session) ? h("span", { class: "sw-mini-tag side" }, forkRelationLabel(session)) : null,
        h("span", { class: "spacer" }),
        h("div", { class: "sw-main-actions" },
          active ? h("button", { class: "btn sm danger", title: t(`${stopLabelKey(session)}_hint`), onclick: stopSession },
            icon("x", "sm"), t(stopLabelKey(session))) : null,
          h("button", {
            class: "icon-btn sw-more", type: "button", title: t("session.menu.more"), "aria-label": t("session.menu.more"),
            "aria-haspopup": "menu", "data-session-menu": "",
            onclick: (event) => {
              const rect = event.currentTarget.getBoundingClientRect();
              openSessionMenu(rect.left, rect.bottom + 4, event.currentTarget);
            },
          }, icon("more", "sm")))));
      mainHeader.appendChild(h("div", { class: "sw-title-meta" },
        h("span", { class: "sw-meta-item" }, icon("cpu", "sm"), platformLabel(session)),
        session.model ? h("span", { class: "sw-meta-item" }, icon("sparkles", "sm"), session.model) : null,
        session.effort ? h("span", { class: "sw-meta-item" }, icon("sparkles", "sm"), t("session.meta.effort", { effort: session.effort })) : null,
        session.worktree
          ? h("span", {
            class: `sw-meta-item sw-branch ${worktreeConflict(session) ? "conflict" : ""}`.trim(),
            title: session.worktree.dirty
              ? t("session.meta.worktree_dirty", { path: session.worktree.path || "", n: session.worktree.dirty })
              : (session.worktree.path || ""),
          }, icon("git", "sm"), worktreeLabel(session) || t("session.tag.worktree"),
            worktreeConflict(session) ? h("b", { class: "sw-branch-warn" }, t("session.tag.shared")) : null)
          : null,
        h("span", { class: `sw-access ${session.access_mode || "read_only"}`.trim(), title: t("session.meta.access_hint") },
          icon("shield", "sm"), session.access_label || permissionModeLabel(session.access_mode === "full_access" ? "full_access" : "read_only")),
        session.agent ? h("span", { class: "sw-meta-item" }, icon("user", "sm"), session.agent) : null,
        // The task chip has the node menu (unit-menu.js): Locate in research map and the rest.
        session.task_ref ? h("span", { class: "sw-meta-item sw-task-ref", dataset: { unitRef: session.task_ref } }, icon("target", "sm"), session.task_ref) : null,
        h("span", { class: "sw-meta-item sw-mono" }, icon("folder", "sm"), sessionCwd(session)),
        h("span", { class: "spacer" }),
        h("span", { class: "sw-meta-item" }, icon("clock", "sm"), session.started_at ? fmtDateTime(session.started_at) : t("session.meta.not_started"))));
    }

    mainBar.appendChild(h("div", { class: "sw-mode" },
      h("button", {
        class: `sw-mode-btn ${threadMode === "thread" ? "on" : ""}`.trim(), type: "button",
        onclick: () => setThreadMode("thread"),
      }, icon("message", "sm"), t("session.mode.thread")),
      h("button", {
        class: `sw-mode-btn ${threadMode === "raw" ? "on" : ""}`.trim(), type: "button",
        onclick: () => setThreadMode("raw"),
      }, icon("command", "sm"), t("session.mode.raw"))));
    mainBar.appendChild(h("span", { class: "spacer" }));
    mainBar.appendChild(usageBadge());
    mainBar.appendChild(h("span", { class: "sw-session-id sw-mono", title: session.id }, session.id));
    mainBar.appendChild(liveBadge(session));
    paintTabs();
    composerHost.classList.toggle("hidden", Boolean(activeTab()));
    paintThread();
    syncComposer();
  }

  // The session's actions: "⋯", right-click on the header or the main tab,
  // Shift+F10 / the context-menu key on the header.
  function sessionMenuItems(session) {
    const active = isActiveSession(session);
    const copyReplies = async () => {
      const items = (mainData || transcript)?.items || [];
      const text = items.filter((item) => item.type === "message" && item.role !== "user")
        .map((item) => item.text || "").filter(Boolean).join("\n\n");
      if (!text) { ctx.toast(t("session.toast.no_replies"), "err"); return; }
      const ok = await copyText(text);
      ctx.toast(ok ? t("action.copied") : t("action.copy"), ok ? "ok" : "err");
    };
    const phone = isPhone() ? [
      { label: t("session.props.open"), icon: "info", onClick: () => openMobilePanel("side") },
      { label: t(threadMode === "thread" ? "session.mode.raw" : "session.mode.thread"), icon: "command",
        onClick: () => setThreadMode(threadMode === "thread" ? "raw" : "thread") },
    ] : [];
    return [
      ...phone,
      { label: t("session.menu.rename"), icon: "edit", onClick: () => renameHeader(session) },
      session.task_ref ? { label: t("tasks.open_task"), icon: "target", onClick: () => ctx.openUnit(session.task_ref) } : null,
      { label: t("session.fork_tab"), icon: "git", onClick: () => forkInTab(session) },
      { label: t("session.side_chat.button"), icon: "message", onClick: () => openSideChat(session) },
      { label: t("session.copy_id"), icon: "copy", onClick: async () => {
        const ok = await copyText(session.id);
        ctx.toast(ok ? t("action.copied") : t("action.copy"), ok ? "ok" : "err");
      } },
      { label: t("session.copy_replies"), icon: "copy", onClick: copyReplies },
      active ? { label: t("tasks.stop"), icon: "x", danger: true, onClick: stopSession }
        : { label: t("session.relaunch"), icon: "refresh", onClick: relaunchSession },
      ...closeDeleteItems(ctx, session, sessionTitle(session)),
    ].filter(Boolean);
  }

  function openSessionMenu(x, y, returnFocus = null) {
    const session = current();
    if (!session) return;
    openContextMenu({ x, y, items: sessionMenuItems(session), returnFocus });
  }

  mainHeader.tabIndex = -1;
  mainHeader.addEventListener("contextmenu", (event) => {
    if (event.target.closest("input, textarea, a, [data-unit-ref]")) return;
    event.preventDefault();
    openSessionMenu(event.clientX, event.clientY, mainHeader);
  });
  mainHeader.addEventListener("keydown", (event) => {
    if (event.key === "ContextMenu" || (event.shiftKey && event.key === "F10")) {
      event.preventDefault();
      const rect = mainHeader.getBoundingClientRect();
      openSessionMenu(rect.left + 24, rect.top + 32, mainHeader);
    }
  });

  function usageBadge() {
    const usage = transcript?.usage;
    if (!usage) return h("span", { class: "sw-usage pending", title: t("session.usage.pending_hint") }, icon("sparkles", "sm"), t("session.usage.pending"));
    const prefix = usage.estimated ? "≈" : "";
    const parts = [
      t(usage.source === "reported" ? "session.usage.source_reported" : "session.usage.source_estimated"),
      t("session.usage.input", { n: formatTokens(usage.input_tokens) }),
      t("session.usage.output", { n: formatTokens(usage.output_tokens) }),
      t("session.usage.total", { n: formatTokens(usage.total_tokens) }),
    ];
    if (usage.reasoning_tokens) parts.push(t("session.usage.reasoning", { n: formatTokens(usage.reasoning_tokens) }));
    if (usage.cache_read_input_tokens) parts.push(t("session.usage.cached", { n: formatTokens(usage.cache_read_input_tokens) }));
    return h("span", {
      class: `sw-usage ${usage.estimated ? "estimated" : "reported"}`.trim(),
      title: parts.join(" · "),
      onclick: () => openUsageDetail(),
    }, icon("sparkles", "sm"), t("session.usage.badge", { prefix, n: formatTokens(usage.total_tokens) }));
  }

  function refreshUsageBadge() {
    const nodes = [
      ...mainBar.querySelectorAll(".sw-usage"),
      ...composerHost.querySelectorAll(".sw-usage"),
    ];
    for (const node of nodes) node.replaceWith(usageBadge());
  }

  function openUsageDetail() {
    const usage = transcript?.usage;
    if (!usage) return;
    railTab = "overview";
    S.sessionRailTab = railTab;
    paintSide();
    ensureTabData();
    requestAnimationFrame(() => {
      const node = sideBody.querySelector(".sw-usage-detail");
      if (node) node.scrollIntoView({ block: "nearest" });
    });
  }

  function liveBadge(session) {
    const active = isActiveSession(session);
    if (!active) {
      const errored = session.status === "errored";
      return h("span", { class: `sw-live ${errored ? "error" : "done"}`.trim() },
        h("i"), errored ? (session.error || statusText("errored")) : t("session.live.completed", { time: sessionTime(session.ended_at) }));
    }
    const busyText = busyNote(session)?.textContent || "";
    const label = busyText || t(sessionPhase(session) === "waiting" ? "session.live.waiting" : session.status === "idle" ? "session.live.idle"
      : session.status === "starting" ? "session.live.starting"
        : session.status === "stopping" ? "session.live.stopping" : "session.live.running");
    return h("span", { class: "sw-live running" }, h("i"), label,
      h("span", { class: "sw-live-sep" }, "·"),
      t("session.live.last_line", { time: sessionTime(session.last_activity_at) }));
  }

  function setThreadMode(mode) {
    if (threadMode === mode) return;
    threadMode = mode;
    S.sessionThreadMode = mode;
    paintMain();
    if (mode === "raw") loadRaw(true);
    else loadTranscript(true);
  }

  // ---------------------------------------------------------------- transcript
  // The open tab: the main conversation ("") or a sub-agent (its call id).
  let agentTabs = loadAgentTabs();
  // The strip belongs to the group root: the session, or the session a fork
  // came from.  A fork tab shows that session itself (with its composer); a
  // sub-agent tab shows the sub-agent of the selected session.
  function stripOwner() {
    return selectedId ? tabRoot(agentTabs, selectedId) : "";
  }
  function activeTab() {
    // A sub-agent of the selected session (its call id), or "".
    const entry = selectedId ? agentTabs[selectedId] : null;
    const tab = entry?.tabs.find((item) => item.id === entry.active);
    return tab && tab.kind !== "session" ? tab.id : "";
  }
  function viewKey() {
    return selectedId ? transcriptKey(selectedId, activeTab()) : "";
  }

  // Transcripts come from the shared stream (one request per tick for the
  // page and the sidebar).  The main conversation is always followed (the
  // activity chips need it); the open sub-agent tab too.
  let mainData = null;
  const follows = new Map();   // key -> unsubscribe
  function follow() {
    const want = new Set([selectedId, viewKey()].filter(Boolean));
    for (const [key, off] of follows) if (!want.has(key)) { off(); follows.delete(key); }
    for (const key of want) {
      if (follows.has(key)) continue;
      follows.set(key, sessionStream.subscribeTranscript(key, (data) => onTranscript(key, data)));
    }
  }

  function onTranscript(key, data) {
    if (destroyed || !data) return;
    if (key === selectedId) {
      mainData = data;
      refreshActivity(data.activity || data.items || [], current());
      refreshUsageBadge();
      if (railTab === "overview") paintSide();
    }
    if (key === viewKey() && data !== transcript) {
      transcript = data;
      transcriptFor = selectedId;
      transcriptError = "";
      transcriptLoading = false;
      paintThread();
    }
  }

  async function loadTranscript(initial = false) {
    const id = selectedId;
    if (!id || destroyed) return;
    follow();
    const key = viewKey();
    if (initial) {
      const cached = sessionStream.cachedTranscript(key);
      transcript = cached;
      transcriptFor = cached ? id : "";
      transcriptLoading = !cached;
      transcriptError = "";
      paintThread(true);
    }
    try {
      const data = await sessionStream.transcript(key, { maxAge: initial ? 0 : TRANSCRIPT_SHARE_MS });
      if (destroyed || selectedId !== id || key !== viewKey() || !data) return;
      if (data !== transcript) onTranscript(key, data);
      if (initial) requestAnimationFrame(() => scrollThread(true));
    } catch (err) {
      if (destroyed || selectedId !== id || key !== viewKey()) return;
      transcriptLoading = false;
      transcriptError = err.message;
      if (initial || !transcript) paintThread(true);
    } finally {
      transcriptLoading = false;
    }
  }

  // ----------------------------------------------------------------- tabs
  function setTabs(next) {
    agentTabs = next;
    saveAgentTabs(next);
  }

  function openAgent(itemId, label = "") {
    if (!selectedId || !itemId) return;
    closeMobilePanels();
    if (threadMode !== "thread") setThreadMode("thread");
    setTabs(openAgentTabIn(agentTabs, selectedId, { id: itemId, label }));
    showConversationTab();
  }

  function showConversationTab() {
    openTools.clear();
    pageLimit = PAGE_BLOCKS;
    stickToBottom = true;
    paintTabs();
    composerHost.classList.toggle("hidden", Boolean(activeTab()));
    loadTranscript(true);
  }

  function paintTabs() {
    clear(tabStrip);
    const owner = stripOwner();
    const entry = owner ? agentTabs[owner] : null;
    tabStrip.hidden = !entry?.tabs?.length || threadMode !== "thread";
    if (tabStrip.hidden) return;
    const statusOf = (item) => {
      if (item.kind === "session") return shownStatusOf(sessions.find((s) => s.id === item.id));
      return activityChips.activity.entries.find((entry2) => entry2.itemId === item.id)?.status || "";
    };
    // Which tab is on: a fork tab when the fork is selected; else the root's own state.
    const onId = selectedId !== owner ? selectedId : activeTab();
    const tab = (item, label, status) => {
      const id = item?.id || "";
      const on = onId === id;
      return h("div", {
        class: `sw-tab ${on ? "on" : ""} ${item?.kind === "session" ? "fork" : ""}`.trim(), role: "tab", tabindex: "0",
        "aria-selected": on ? "true" : "false", dataset: { sessionTab: id || "main", tabKind: item?.kind || "main" },
        onclick: () => {
          if (item?.kind === "session") {
            setTabs(activateAgentTab(agentTabs, owner, id));
            select(id);
            return;
          }
          setTabs(activateAgentTab(agentTabs, owner, id));
          if (selectedId !== owner) { select(owner); return; }
          showConversationTab();
        },
        onkeydown: (event) => {
          if (event.key === "Enter" || event.key === " ") { event.preventDefault(); event.currentTarget.click(); }
          if (event.key === "ContextMenu" || (event.shiftKey && event.key === "F10")) {
            event.preventDefault();
            const rect = event.currentTarget.getBoundingClientRect();
            openSessionMenu(rect.left, rect.bottom + 4, event.currentTarget);
          }
        },
        oncontextmenu: item?.kind === "agent" ? null : (event) => {
          event.preventDefault();
          if (item?.kind === "session" && selectedId !== id) { event.currentTarget.click(); return; }
          openSessionMenu(event.clientX, event.clientY, event.currentTarget);
        },
      },
        status ? h("i", { class: `sa-dot ${status}` }) : icon(item?.kind === "session" ? "git" : "message", "sm"),
        h("span", { class: "sw-tab-label", title: label }, label),
        id ? h("button", {
          class: "sw-tab-x", type: "button", title: t("action.close"), "aria-label": t("session.tabs.close", { label }),
          onclick: (event) => {
            event.stopPropagation();
            setTabs(closeAgentTab(agentTabs, owner, id));
            if (selectedId === id) { select(owner); return; }
            showConversationTab();
          },
        }, icon("x", "sm")) : null);
    };
    tabStrip.append(tab(null, t("session.tabs.main"), ""),
      ...entry.tabs.map((item) => tab(item, item.label || t(item.kind === "session" ? "session.tabs.fork" : "session.activity.kind_subagent"), statusOf(item))));
    for (const el of tabStrip.children) longPressMenu(el);
    tabStrip.querySelector(".sw-tab.on")?.scrollIntoView?.({ inline: "nearest", block: "nearest" });
  }

  function shownStatusOf(session) {
    if (!session) return "";
    const status = shownStatus(session);
    return status === "running" ? "running" : status === "errored" ? "failed" : status === "idle" ? "" : "completed";
  }

  // Fork in new tab: the provider forks the conversation itself (Claude
  // Code, Codex), otherwise the fork starts with a summary of it.
  async function forkInTab(session) {
    if (!session) return;
    try {
      const data = await api.forkSession(session.id, { mode: "fork" });
      const fork = data.session;
      if (!fork?.id) return;
      sessions = [fork, ...sessions.filter((item) => item.id !== fork.id)];
      const owner = stripOwner();
      setTabs(openForkTab(agentTabs, owner, { id: fork.id, label: sessionTitle(fork) }));
      ctx.toast(t(fork.fork_method === "native" ? "session.toast.forked" : "session.toast.forked_seeded"), "ok");
      await refreshSessions();
      select(fork.id);
    } catch (err) { ctx.toast(err.message, "err"); }
  }

  // The thread is a keyed list: every display block (user turn, assistant
  // reply, grouped tool activity, error, …) keeps its DOM node across polls and
  // is only rebuilt when its own content changes.  A streaming reply is patched
  // in place, so new lines grow the column without re-rendering or jumping it.
  let activityCount = "";
  let activityFor = "";
  let busyKey = "";
  function refreshActivity(items, session) {
    const activity = activityChips.update(items, { session, sessions });
    activityFor = session?.id || "";
    const count = `${activity.subagents.length}:${activity.background.length}:${activity.runningSubagents}:${activity.runningBackground}`;
    const busy = session ? `${session.id}:${shownStatus(session, activity)}:${JSON.stringify(busyReason(session, activity))}` : "";
    if (count !== activityCount) {
      activityCount = count;
      if (session) paintSide();
      paintTabs();
    }
    if (busy !== busyKey) {
      const repaint = busyKey !== "";
      busyKey = busy;
      if (repaint) { paintList(); paintMain(); if (session) paintSide(); }
      repaintSend();
    }
  }
  // The open session's sub-agent / background activity (fresher than the server's `busy`).
  function activityOf(session) {
    return session && session.id === activityFor ? activityChips.activity : null;
  }
  // "Running · 2 sub-agents" when the turn has ended but work goes on (reasonOnly: "2 sub-agents").
  function busyNote(session, { reasonOnly = false } = {}) {
    const reasons = busyReason(session, activityOf(session));
    if (!reasons.length) return null;
    const text = reasons.map((r) => t(r.key, { n: r.n })).join(" · ");
    return h("span", { class: "sw-busy-note", "data-busy-note": "" }, reasonOnly ? text : `${t("session.busy.running")} · ${text}`);
  }
  let repaintSend = () => {};

  // Show a transcript item (a sub-agent's call, a background task notice):
  // open its tool group and scroll it into view.
  function revealThreadItem(itemId) {
    closeMobilePanels();
    if (threadMode !== "thread") setThreadMode("thread");
    if (activeTab()) {
      setTabs(activateAgentTab(agentTabs, selectedId, ""));
      showConversationTab();
    }
    const all = threadBlocks(transcript?.items || []);
    const key = blockKeyFor(all, itemId);
    const index = all.findIndex((block) => block.key === key);
    if (index >= 0 && all.length - index > pageLimit) pageLimit = all.length - index + 10;
    if (!key) return;
    if (key.startsWith("tools-")) openTools.add(key);
    blockNodes.delete(key);
    stickToBottom = false;
    paintThread();
    const node = blockNodes.get(key)?.node;
    if (!node) return;
    node.scrollIntoView({ block: "center", behavior: "smooth" });
    node.classList.add("sa-flash");
    setTimeout(() => node.classList.remove("sa-flash"), 1600);
  }

  // Long conversations draw their last PAGE_BLOCKS blocks; "Show earlier"
  // draws more, and asks the server for older items when the page has none.
  const PAGE_BLOCKS = 120;
  let pageLimit = PAGE_BLOCKS;
  let drawn = 0;
  let drawTimer = null;
  const earlierButton = h("button", { class: "ssb-earlier sw-earlier", type: "button", onclick: () => showEarlier() });
  function showEarlier() {
    const before = thread.scrollHeight;
    const blocks = threadBlocks(transcript?.items || []);
    if (blocks.length > pageLimit) pageLimit += PAGE_BLOCKS;
    else if (transcript?.offset > 0) {
      pageLimit += PAGE_BLOCKS;
      sessionStream.setTail(viewKey(), (transcript.items?.length || 0) + 400).catch(() => {});
    }
    stickToBottom = false;
    paintThread();
    thread.scrollTop += thread.scrollHeight - before;
  }

  function resetThreadDom() {
    blockNodes.clear();
    clear(column);
  }

  function showThreadState(node) {
    resetThreadDom();
    clear(thread);
    if (node) thread.appendChild(node);
    stopLiveTimer();
    updateJump();
  }

  function paintThread(forceBottom = false) {
    const session = current();
    if (forceBottom) {
      stickToBottom = true;
      unseen = false;
    }
    if (!session) {
      refreshActivity([], null);
      showThreadState(null);
      return;
    }
    if (threadMode === "raw") {
      showThreadState(null);
      paintRawThread();
      return;
    }
    if (transcriptLoading && (!transcript || transcriptFor !== selectedId)) {
      showThreadState(h("div", { class: "sw-column" }, h("div", { class: "sw-skeleton" },
        h("div", { class: "sw-skeleton-row short right" }),
        h("div", { class: "sw-skeleton-row wide" }),
        h("div", { class: "sw-skeleton-row" }),
        h("div", { class: "sw-skeleton-note" }, t("tasks.thread.loading")))));
      return;
    }
    if (transcriptError && !transcript) {
      showThreadState(h("div", { class: "sw-column" }, h("div", { class: "sw-err-card", role: "alert" },
        h("span", { class: "sw-err-icon" }, icon("info", "sm")),
        h("div", { class: "sw-err-main" },
          h("b", null, t("tasks.thread.load_failed")),
          h("div", { class: "sw-err-text" }, transcriptError),
          h("button", { class: "btn sm", onclick: () => loadTranscript(true) }, icon("refresh", "sm"), t("action.retry"))))));
      return;
    }
    const items = transcript?.items || [];
    const live = activeTab() ? null : liveActivity(items, session);
    if (!items.length && !live) {
      showThreadState(h("div", { class: "sw-thread-empty" }, icon("message", "lg"),
        h("b", null, t("tasks.thread.empty")),
        h("div", null, t("tasks.thread.empty_hint"))));
      return;
    }
    if (thread.firstChild !== column || thread.childNodes.length !== 1) {
      clear(thread);
      thread.appendChild(column);
    }
    const followBottom = stickToBottom;
    const previousHeight = thread.scrollHeight;
    const animate = blockNodes.size > 0;
    // Progressive drawing: a new view starts with the newest blocks.
    if (!blockNodes.size) drawn = 0;
    drawn = drawLimit(drawn, pageLimit);
    const allBlocks = threadBlocks(items);
    const windowed = windowBlocks(allBlocks, drawn);
    const more = Math.min(pageLimit, allBlocks.length) > drawn;
    const blocks = windowed.blocks;
    const earlier = Math.max(0, allBlocks.length - pageLimit) + Number(transcript?.offset || 0);
    earlierButton.textContent = earlier ? t("session.sidebar.earlier", { n: earlier }) : "";
    const desired = [];
    const seen = new Set();
    for (const block of blocks) {
      seen.add(block.key);
      const signature = `${blockSignature(block)}#${blockExtraSignature(block)}`;
      let entry = blockNodes.get(block.key);
      if (!entry || entry.signature !== signature) {
        const node = entry && block.kind === "assistant" && entry.kind === "assistant"
          ? patchAssistant(entry.node, block.item)
          : renderBlock(block);
        if (entry?.node && entry.node !== node && entry.node.parentNode === column) entry.node.replaceWith(node);
        if (!entry && animate) node.classList.add("sw-enter");
        entry = { signature, node, kind: block.kind };
        blockNodes.set(block.key, entry);
      }
      desired.push(entry.node);
    }
    for (const key of [...blockNodes.keys()]) {
      if (!seen.has(key)) blockNodes.delete(key);
    }
    paintLiveRow(session, live);
    if (earlier) desired.unshift(earlierButton);
    desired.push(liveRow);
    let ref = column.firstChild;
    for (const node of desired) {
      if (node === ref) ref = ref.nextSibling;
      else column.insertBefore(node, ref);
    }
    while (ref) {
      const next = ref.nextSibling;
      column.removeChild(ref);
      ref = next;
    }
    if (more && !drawTimer) {
      drawTimer = setTimeout(() => {
        drawTimer = null;
        if (destroyed) return;
        const height = thread.scrollHeight;
        const top = thread.scrollTop;
        const bottom = stickToBottom;
        paintThread();
        if (!bottom) thread.scrollTop = top + (thread.scrollHeight - height);
      }, 16);
    }
    if (followBottom) {
      scrollThread(true);
      // Images, KaTeX and code highlighting can settle a frame later.
      requestAnimationFrame(() => { if (!destroyed && stickToBottom) scrollThread(true); });
    } else if (thread.scrollHeight > previousHeight + 2) {
      unseen = true;
    }
    updateJump();
  }

  function blockExtraSignature(block) {
    if (block.kind === "permission") {
      return `${approvalBusy.has(block.item.request_id) ? 1 : 0}:${isActiveSession(current()) ? 1 : 0}`;
    }
    if (block.kind === "tools") return openTools.has(block.key) ? "open" : "";
    return "";
  }

  function renderBlock(block) {
    if (block.kind === "separator") return renderSeparator(block.at);
    if (block.kind === "user") return renderUser(block.item);
    if (block.kind === "assistant") return patchAssistant(null, block.item);
    if (block.kind === "tools") return renderToolGroup(block);
    if (block.kind === "error") return renderError(block.item);
    if (block.kind === "permission") return h("div", { class: "sw-block-wide" }, renderPermission(block.item));
    if (block.kind === "reasoning") return renderReasoning(block.item);
    return h("div", { class: `sw-note ${block.item.level || "info"}`.trim() },
      icon(block.item.level === "error" ? "alert" : "info", "sm"), h("span", null, block.item.text));
  }

  function activeLabel(session) {
    return session && STREAM_ACTIVE.includes(session.status);
  }

  function turnTime(at) {
    const date = new Date(at);
    if (!at || !Number.isFinite(date.getTime())) return "";
    const now = new Date();
    const time = date.toLocaleTimeString(locale(), { hour: "2-digit", minute: "2-digit", hour12: false });
    const yesterday = new Date(now);
    yesterday.setDate(now.getDate() - 1);
    if (date.toDateString() === now.toDateString()) return `${t("tasks.thread.today")} ${time}`;
    if (date.toDateString() === yesterday.toDateString()) return `${t("tasks.thread.yesterday")} ${time}`;
    const sameYear = date.getFullYear() === now.getFullYear();
    return `${date.toLocaleDateString(locale(), sameYear
      ? { month: "short", day: "numeric" } : { year: "numeric", month: "short", day: "numeric" })} ${time}`;
  }

  function renderSeparator(at) {
    return h("div", { class: "sw-turn-sep", role: "separator" },
      h("span", { title: fmtDateTime(at) }, turnTime(at)));
  }

  function actionButton(iconName, label, onClick) {
    return h("button", {
      class: "sw-act-btn", type: "button", title: label, "aria-label": label,
      onclick: (event) => { event.stopPropagation(); onClick(event); },
    }, icon(iconName, "sm"));
  }

  async function copyWithToast(text) {
    const ok = await copyText(text || "");
    ctx.toast(ok ? t("action.copied") : t("action.copy"), ok ? "ok" : "err");
  }

  function renderUser(item) {
    const source = item.source === "prompt" ? t("tasks.thread.initial_prompt")
      : item.source === "stdin" ? t("tasks.thread.steer") : "";
    return h("article", { class: "sw-turn-user", dataset: { itemId: item.id } },
      turnImages(item.images, (path) => api.sessionArtifactUrl(selectedId, path, true), (src, path) => openImageLightbox(src, path.split("/").pop(), selectedId, path)),
      h("div", { class: "sw-bubble" }, item.text),
      h("div", { class: "sw-msg-actions" },
        source ? h("span", { class: "sw-msg-source" }, source) : null,
        item.at ? h("time", { class: "sw-msg-time", title: fmtDateTime(item.at) }, turnTime(item.at)) : null,
        actionButton("copy", t("tasks.thread.copy"), () => copyWithToast(item.text)),
        actionButton("edit", t("tasks.thread.edit_in_composer"), () => editInComposer(item.text))));
  }

  function editInComposer(text) {
    const session = current();
    if (!session) return;
    draftBySession[session.id] = String(text || "");
    composerSignature = "";
    syncComposer();
    requestAnimationFrame(() => {
      if (!composerInput) return;
      composerInput.focus();
      composerInput.setSelectionRange(composerInput.value.length, composerInput.value.length);
    });
  }

  function patchAssistant(node, item) {
    const prose = h("div", { class: "sw-prose" }, markdownWithArtifacts(item.text));
    if (!node) {
      node = h("article", { class: "sw-turn-assistant", dataset: { itemId: item.id } },
        prose,
        h("div", { class: "sw-msg-actions" },
          actionButton("copy", t("tasks.thread.copy"), () => copyWithToast(node.__text)),
          h("time", { class: "sw-msg-time" })));
    } else {
      node.querySelector(":scope > .sw-prose")?.replaceWith(prose);
    }
    node.__text = item.text || "";
    node.classList.toggle("streaming", Boolean(item.streaming));
    const time = node.querySelector(".sw-msg-time");
    if (time) {
      time.textContent = item.at ? turnTime(item.at) : "";
      time.title = item.at ? fmtDateTime(item.at) : "";
    }
    return node;
  }

  function renderError(item) {
    const meta = item.source === "meta";
    return h("div", { class: "sw-err-card", role: "alert" },
      h("span", { class: "sw-err-icon" }, icon("info", "sm")),
      h("div", { class: "sw-err-main" },
        h("b", null, meta ? t("tasks.thread.session_error") : t("tasks.thread.error")),
        h("div", { class: "sw-err-text" }, item.text),
        item.at ? h("span", { class: "sw-msg-time", title: fmtDateTime(item.at) }, turnTime(item.at)) : null),
      actionButton("copy", t("tasks.thread.copy"), () => copyWithToast(item.text)));
  }

  function reasoningSummary(text) {
    const lines = String(text || "").split(/\n+/).map((line) => line.trim()).filter(Boolean);
    const last = lines[lines.length - 1] || "";
    const clean = last.replace(/^[-*#>\s]+/, "").replace(/[*_`]/g, "");
    return clean.length > 110 ? clean.slice(0, 110) + "…" : clean;
  }

  function renderReasoning(item) {
    const summary = reasoningSummary(item.text);
    return h("details", {
      class: `sw-act sw-thought ${item.streaming ? "running" : ""}`.trim(),
      open: reasoningOpen ? true : undefined,
      ontoggle: (event) => { reasoningOpen = event.target.open; },
    },
      h("summary", { class: "sw-act-head" },
        h("span", { class: "sw-act-icon" }, icon("sparkles", "sm")),
        h("span", { class: "sw-act-label" }, item.streaming ? t("tasks.thread.thinking") : t("tasks.thread.thought")),
        summary ? h("span", { class: "sw-act-preview" }, summary) : null,
        h("span", { class: "sw-act-chev" }, icon("chevronRight", "sm"))),
      h("div", { class: "sw-act-body sw-thought-body" }, richText(item.text || "")));
  }

  function renderToolGroup(block) {
    return renderToolGroupRow(block, {
      openSet: openTools,
      summary: toolGroupSummary(block.items),
      onToggle: () => {
        const entry = blockNodes.get(block.key);
        if (entry) entry.signature = `${blockSignature(block)}#${blockExtraSignature(block)}`;
      },
      onFull: (itemId) => fullToolItem(selectedId, itemId),
      onImage: (itemId, n) => toolImage(selectedId, itemId, n),
      onOpenAgent: activeTab() ? null : (item) => openAgent(item.id, item.input?.description || item.input?.subagent_type || ""),
    });
  }

  function liveLabel(live) {
    // A running tool row already shimmers its own label; keep the footer generic.
    const phase = live.phase === "running" || live.phase === "editing" ? "working" : live.phase;
    return t(`tasks.thread.phase_${phase}`);
  }

  function paintLiveRow(session, live) {
    liveState = live;
    if (!live) {
      liveRow.classList.add("hidden");
      stopLiveTimer();
      return;
    }
    liveRow.classList.remove("hidden");
    liveRow.dataset.phase = live.phase;
    liveLabelNode.textContent = liveLabel(live);
    tickLive();
    if (!liveTimer) liveTimer = setInterval(tickLive, 1000);
  }

  function tickLive() {
    if (destroyed || !liveState) {
      stopLiveTimer();
      return;
    }
    liveTimeNode.textContent = liveState.since ? fmtDuration(liveState.since) : "";
  }

  function stopLiveTimer() {
    if (liveTimer) clearInterval(liveTimer);
    liveTimer = null;
    liveState = null;
  }

  function markdownWithArtifacts(text) {
    const value = String(text || "").replace(/!\[([^\]]*)\]\(([^)\s]+)\)/g, (match, alt, src) => {
      if (/^(https?:|data:|\/)/i.test(src)) return match;
      return `![${alt}](${api.sessionArtifactUrl(selectedId, src, true)})`;
    });
    return richText(value);
  }

  function permissionValue(label, value, mono = false) {
    return h("div", { class: "sw-perm-kv" },
      h("span", { class: "k" }, label),
      mono ? h("pre", { class: "sw-perm-code" }, value) : h("code", null, value));
  }

  function renderPermission(item) {
    const session = current();
    const active = Boolean(session && isActiveSession(session));
    const status = item.status === "pending" && !active ? "expired" : (item.status || "pending");
    const busy = approvalBusy.has(item.request_id);
    const canDecide = canDecideApproval(item, active);
    const iconName = status === "allowed" ? "check"
      : ["denied", "failed", "expired"].includes(status) ? "alert" : "shield";
    const head = h("div", { class: "sw-perm-head" },
      h("span", { class: `sw-perm-icon ${status}`.trim() }, icon(iconName, "sm")),
      h("div", { class: "sw-perm-title" },
        h("b", null, t(status === "pending" ? "session.approval.required" : "session.approval.request")),
        h("span", { class: `sw-perm-status ${status}`.trim() }, approvalStatusLabel({ ...item, status }))),
      h("span", { class: "spacer" }),
      item.at ? h("span", { class: "sw-item-time", title: fmtDateTime(item.at) }, sessionTime(item.at)) : null);
    const tags = h("div", { class: "sw-perm-tags" },
      h("span", { class: "sw-mini-tag kind" }, item.kind || t("session.approval.kind_tool")),
      item.tool ? h("span", { class: "sw-mini-tag tool" }, item.tool) : null,
      item.protocol ? h("span", {
        class: "sw-mini-tag proto", title: t("session.approval.protocol_hint"),
      }, item.protocol) : null,
      item.auto ? h("span", { class: "sw-mini-tag auto" }, t("session.approval.auto_tag")) : null);
    const body = h("div", { class: "sw-perm-body" });
    if (item.description) body.appendChild(h("div", { class: "sw-perm-desc" }, item.description));
    if (item.command) body.appendChild(permissionValue(t("session.approval.kind_command"), item.command, true));
    if (item.path) body.appendChild(permissionValue(t("session.approval.field_path"), item.path));
    if (!item.command && !item.path && item.input !== null && item.input !== undefined) {
      body.appendChild(permissionValue(t("session.approval.field_request"), textValue(item.input), true));
    } else if (item.input !== null && item.input !== undefined) {
      body.appendChild(h("details", { class: "sw-perm-input" },
        h("summary", null, t("session.approval.payload")),
        h("pre", { class: "sw-tool-pre" }, textValue(item.input))));
    }
    const actions = h("div", { class: "sw-perm-actions" });
    if (canDecide && !busy) {
      actions.append(
        h("button", {
          class: "btn sm ghost danger", type: "button",
          onclick: () => decideApproval(session, item, "deny"),
        }, icon("x", "sm"), t("session.approval.deny")),
        h("button", {
          class: "btn sm ghost", type: "button",
          title: t("session.approval.always_hint"),
          onclick: () => decideApproval(session, item, "always"),
        }, icon("check", "sm"), t("session.approval.always")),
        h("button", {
          class: "btn sm primary", type: "button",
          onclick: () => decideApproval(session, item, "allow"),
        }, icon("check", "sm"), t("session.approval.allow_once")));
    } else if (busy) {
      actions.appendChild(h("span", { class: "sw-perm-busy" },
        h("span", { class: "spinner" }), t("session.approval.sending")));
    }
    const footerText = status === "pending"
      ? t("session.approval.foot_pending")
      : status === "expired"
        ? (item.message || t("session.approval.foot_expired"))
        : [
          approvalStatusLabel({ ...item, status }),
          item.decided_by ? t("session.approval.by", { who: item.decided_by }) : "",
          item.decided_at ? sessionTime(item.decided_at) : "",
          item.message && item.message !== "approved by user" && item.message !== "denied by user"
            ? item.message : "",
        ].filter(Boolean).join(" · ");
    return h("article", { class: `sw-permission ${status}`.trim(), dataset: { requestId: item.request_id || "" } },
      head, tags,
      body.childNodes.length ? body : null,
      actions.childNodes.length ? actions : null,
      h("div", { class: "sw-perm-foot" }, footerText));
  }

  function nearBottom() {
    return thread.scrollTop + thread.clientHeight >= thread.scrollHeight - 48;
  }

  function scrollThread(force, smooth = false) {
    if (force) {
      stickToBottom = true;
      unseen = false;
      if (smooth && typeof thread.scrollTo === "function") {
        thread.scrollTo({ top: thread.scrollHeight, behavior: "smooth" });
      } else {
        thread.scrollTop = thread.scrollHeight;
      }
    }
    updateJump();
  }

  function updateJump() {
    const hidden = threadMode === "raw" || stickToBottom || nearBottom();
    jumpButton.classList.toggle("hidden", hidden);
    jumpButton.classList.toggle("unseen", unseen);
    jumpLabel.textContent = unseen ? t("tasks.thread.new_messages") : t("tasks.thread.jump_latest");
  }

  // ----------------------------------------------------------------- raw logs
  function paintRawThread() {
    const session = current();
    if (!session) return;
    if (rawState.for !== selectedId) {
      rawState = { stream: rawState.stream, offsets: { stdout: 0, stderr: 0 }, entries: [], following: true, loading: false, error: "", for: selectedId };
    }
    const streamButtons = {};
    const controls = h("div", { class: "sw-raw-controls" },
      h("div", { class: "sw-segmented" }, ["stdout", "stderr"].map((stream) => { // i18n-ignore: stream names
        const button = h("button", {
          class: `sw-seg-btn ${rawState.stream === stream ? "on" : ""}`.trim(), type: "button",
          onclick: () => {
            rawState.stream = stream;
            rawState.offsets[stream] = 0;
            rawState.entries = [];
            paintThread();
            loadRaw(true);
          },
        }, stream);
        streamButtons[stream] = button;
        return button;
      })),
      h("label", { class: "sw-check" },
        h("input", {
          type: "checkbox", checked: rawState.following,
          onchange: (event) => { rawState.following = event.target.checked; },
        }), t("tasks.follow")),
      h("span", { class: "spacer" }),
      h("span", { class: "sw-raw-hint" }, t("session.raw.hint")),
      h("button", { class: "btn sm ghost", onclick: copyLog }, icon("copy", "sm"), t("action.copy")),
      h("button", { class: "btn sm ghost", onclick: downloadLog }, icon("download", "sm"), t("action.download")));
    const box = h("div", { class: "sw-raw-box" });
    if (rawState.loading && !rawState.entries.length) {
      box.appendChild(h("div", { class: "sw-raw-empty" }, t("session.raw.loading")));
    } else if (rawState.error) {
      box.appendChild(h("div", { class: "sw-raw-empty error" }, rawState.error));
    } else if (!rawState.entries.length) {
      box.appendChild(h("div", { class: "sw-raw-empty" }, t("tasks.log_empty")));
    } else {
      for (const entry of rawState.entries) {
        box.appendChild(h("div", {
          class: `sw-raw-line ${rawState.stream === "stderr" ? "err" : ""} ${entry.kind === "input" ? "input" : ""}`.trim(),
          title: entry.t || "",
        }, entry.line));
      }
    }
    thread.appendChild(h("div", { class: "sw-raw" }, controls, box));
    requestAnimationFrame(() => {
      if (rawState.following) box.scrollTop = box.scrollHeight;
    });
  }

  async function loadRaw(initial = false) {
    if (threadMode !== "raw" || !selectedId || rawState.loading) return;
    const id = selectedId;
    if (rawState.for !== id) {
      rawState = { stream: rawState.stream, offsets: { stdout: 0, stderr: 0 }, entries: [], following: true, loading: false, error: "", for: id };
    }
    rawState.loading = true;
    if (initial) {
      rawState.offsets[rawState.stream] = 0;
      rawState.entries = [];
      paintThread(true);
    }
    try {
      const data = await api.sessionLogs(id, rawState.stream, rawState.offsets[rawState.stream], 300);
      if (destroyed || selectedId !== id || threadMode !== "raw") return;
      if (data.total < rawState.offsets[rawState.stream]) {
        rawState.offsets[rawState.stream] = 0;
        rawState.entries = [];
      }
      rawState.entries.push(...(data.entries || []));
      if (rawState.entries.length > 1600) rawState.entries = rawState.entries.slice(-1600);
      rawState.offsets[rawState.stream] = Number.isFinite(data.offset)
        ? data.offset : rawState.offsets[rawState.stream] + (data.entries || []).length;
      rawState.error = "";
    } catch (err) {
      if (selectedId === id) rawState.error = err.message;
    } finally {
      rawState.loading = false;
      if (!destroyed && selectedId === id && threadMode === "raw") paintThread();
    }
  }

  async function collectLog() {
    if (!selectedId) return "";
    const stream = rawState.stream;
    let offset = 0;
    const lines = [];
    for (let i = 0; i < 40; i += 1) {
      const data = await api.sessionLogs(selectedId, stream, offset, 500);
      lines.push(...(data.entries || []).map((entry) => entry.line));
      if (!data.entries?.length || data.offset >= data.total) break;
      offset = data.offset;
    }
    return lines.join("\n");
  }

  async function copyLog() {
    try {
      const ok = await copyText(await collectLog());
      ctx.toast(ok ? t("action.copied") : t("action.copy"), ok ? "ok" : "err");
    } catch (err) { ctx.toast(err.message, "err"); }
  }

  async function downloadLog() {
    try {
      const text = await collectLog();
      download(`${selectedId}-${rawState.stream}.log`, text || "", "text/plain;charset=utf-8");
    } catch (err) { ctx.toast(err.message, "err"); }
  }

  thread.addEventListener("scroll", () => {
    stickToBottom = nearBottom();
    if (stickToBottom) unseen = false;
    updateJump();
  }, { passive: true });
  thread.addEventListener("click", async (event) => {
    // file: links are handled globally (app.js), before this listener runs.
    const image = event.target.closest?.("img.rich-img");
    if (image) openThreadImage(image);
  });
  // Right-click on an image in the thread or the side panel: copy / open.
  delegateImageContextMenu(thread, "img.rich-img");
  delegateImageContextMenu(sideBody, "img.rich-img, img.sw-file-image");
  sideBody.addEventListener("click", (event) => {
    const image = event.target.closest?.("img.rich-img, img.sw-file-image");
    if (!image) return;
    const path = image.classList.contains("sw-file-image")
      ? tabState(current() || {}).active : "";
    openImageLightbox(image.currentSrc || image.src, image.alt || "image", selectedId, path);
  });

  // ---------------------------------------------------------------- composer
  let presetsLoading = false;
  // Sessions whose model picker is in free-text "Custom…" mode.
  const customModelSessions = new Set();
  let modelClock = 0;

  function presetById(id) {
    return presets.find((preset) => preset.id === id) || null;
  }

  function choicesFor(session) {
    let choices = composerChoices.get(session.id);
    if (!choices) {
      choices = {
        platform: session.platform || "",
        model: session.model || "",
        effort: session.effort || "",
        access_mode: session.access_mode || "read_only",
      };
      composerChoices.set(session.id, choices);
    }
    return choices;
  }

  function contextItemsFor(session) {
    return contextBySession.get(session.id) || [];
  }

  function contextPayload(session) {
    return contextItemsFor(session).map((item) => ({ ...item.attachment }));
  }

  async function loadPresets(force = false) {
    if ((presetsLoaded && !force) || presetsLoading) return;
    presetsLoading = true;
    if (force) {
      composerSignature = "";
      if (!destroyed) syncComposer();
      try { await api.refreshProviders(); } catch (err) { presetsError = err.message; }
    }
    try {
      const data = await api.sessionPresets();
      presets = data.presets || [];
      presetsError = presetsError && !presets.length ? presetsError : "";
      S.sessionPresets = presets;
    } catch (err) {
      presets = [];
      presetsError = err.message;
    } finally {
      presetsLoading = false;
      presetsLoaded = true;
      modelClock += 1;
      composerSignature = "";
      if (!destroyed) syncComposer();
    }
  }

  function modelSourceLabel(source) {
    return t(`tasks.models.group.${source}`);
  }

  function modelOptionLabel(item) {
    return modelOptionLabelEn(item, withT);
  }

  function modelStatusText(capabilities) {
    const info = modelSourceSummary(capabilities);
    const when = info.checkedAt ? relativeTime(info.checkedAt) : "";
    if ((info.source === "detected" || info.source === "available") && info.kind) {
      return t(`tasks.models.status.${info.kind}`, { via: info.via, when: when || "—" });
    }
    if (info.source === "configured") return t("tasks.models.status.configured", { when: when || "—" });
    if (info.source === "builtin") {
      const reasonKey = `tasks.models.reason.${info.reason || "not_run"}`;
      return t("tasks.models.status.builtin", { reason: t(reasonKey) });
    }
    return t("tasks.models.status.none");
  }

  function renderModelPicker(session, preset, capabilities, choices, active) {
    const options = modelOptions(preset, capabilities);
    const fallback = defaultModelOption(options);
    const picker = modelPickerGroups(options, choices.model);
    const custom = picker.currentIsCustom || customModelSessions.has(session.id);
    const locked = active || !presetsLoaded;
    const select = h("select", {
      class: "sw-select sw-model-select", "aria-label": t("tasks.models.label"),
      title: locked && active ? t("tasks.models.locked") : t("tasks.models.title"),
      disabled: locked,
      onchange: (event) => {
        const value = event.target.value;
        if (value === CUSTOM_MODEL_VALUE) {
          customModelSessions.add(session.id);
        } else {
          customModelSessions.delete(session.id);
          choices.model = value;
        }
        const nextEfforts = modelEfforts(preset, capabilities, choices.model);
        if (choices.effort && !nextEfforts.includes(choices.effort)) choices.effort = "";
        composerSignature = "";
        syncComposer();
        if (value === CUSTOM_MODEL_VALUE) composerHost.querySelector(".sw-model-input")?.focus();
      },
    },
      h("option", { value: "", selected: !custom && !choices.model },
        fallback ? t("tasks.models.provider_default_named", { model: fallback.id })
          : t("tasks.models.provider_default")),
      ...picker.groups.map((group) => h("optgroup", { label: modelSourceLabel(group.source) },
        ...group.items.map((item) => h("option", {
          value: item.id, selected: !custom && item.id === choices.model,
          title: item.efforts?.length ? t("session.effort_option", { effort: item.efforts.join(" / ") }) : "",
        }, modelOptionLabel(item))))),
      h("option", { value: CUSTOM_MODEL_VALUE, selected: custom }, t("tasks.models.custom")));
    const input = custom ? h("input", {
      class: "sw-select sw-model-input", value: choices.model,
      placeholder: t("tasks.models.custom_placeholder"),
      "aria-label": t("tasks.models.custom_label"), autocomplete: "off", spellcheck: "false",
      disabled: locked,
      oninput: (event) => { choices.model = event.target.value.trim(); },
      onchange: () => { composerSignature = ""; syncComposer(); },
    }) : null;
    const refresh = h("button", {
      class: "icon-btn sw-model-refresh", type: "button",
      title: t("tasks.models.refresh"), "aria-label": t("tasks.models.refresh"),
      disabled: presetsLoading || active,
      onclick: () => loadPresets(true),
    }, icon("refresh", "sm"));
    const info = modelSourceSummary(capabilities);
    const status = h("div", {
      class: `sw-model-status ${info.source}${info.error ? " has-error" : ""}`.trim(),
      title: [localizeServer(info.detail), info.error].filter(Boolean).join(" — "),
    },
      h("span", { class: `sw-model-source-tag ${info.source}` }, modelSourceLabel(
        info.source === "provider_default" ? "none" : info.source)),
      h("span", { class: "sw-model-status-text" },
        presetsLoading ? t("tasks.models.checking") : modelStatusText(capabilities)),
      info.error ? h("span", { class: "sw-model-status-error" }, icon("alert", "sm"), info.error) : null,
      h("span", { class: "spacer" }),
      refresh);
    return { control: h("div", { class: "sw-model-picker" }, select, input), status };
  }

  function providerOptionLabel(preset) {
    const key = `tasks.platform.${preset.id}`;
    const translated = t(key);
    const label = translated === key ? preset.label : translated;
    if (!preset.available) return t("session.provider_option", { label, status: t("tasks.launch_unavailable") });
    if (preset.provider_status === "unauthenticated") {
      return t("session.provider_option", { label, status: t("session.provider.unauthenticated") });
    }
    if (preset.provider_status === "unknown") {
      return t("session.provider_option", { label, status: t("session.provider.auth_unknown") });
    }
    return label;
  }

  async function attachContext(session, attachment) {
    try {
      const item = await attachSessionContext(session.id, attachment);
      composerSignature = "";
      syncComposer();
      if (railTab === "overview") paintSide();
      ctx.toast(t("session.toast.attached", { kind: item.kind, label: item.label }), "ok");
    } catch (err) {
      ctx.toast(err.message, "err");
    }
  }

  function removeContext(session, item) {
    const list = contextItemsFor(session).filter((entry) => entry.id !== item.id);
    if (list.length) contextBySession.set(session.id, list);
    else contextBySession.delete(session.id);
    composerSignature = "";
    syncComposer();
    if (railTab === "overview") paintSide();
  }

  function clearContext(session) {
    contextBySession.delete(session.id);
  }

  async function removeSteer(session, item) {
    try {
      const data = await api.sessionRemoveSteer(session.id, item.id);
      if (data.session) patchSessionPayload(data.session);
      if (steerEditing && steerEditing.id === item.id) steerEditing = null;
      composerSignature = "";
      syncComposer();
    } catch (err) { ctx.toast(err.message, "err"); }
  }

  async function saveSteer(session, item) {
    if (!steerEditing || steerEditing.id !== item.id) return;
    const text = String(steerEditing.text || "").trim();
    if (!text) { ctx.toast(t("session.toast.steer_required"), "err"); return; }
    try {
      const data = await api.sessionUpdateSteer(session.id, item.id, text);
      if (data.session) patchSessionPayload(data.session);
      steerEditing = null;
      composerSignature = "";
      syncComposer();
    } catch (err) { ctx.toast(err.message, "err"); }
  }

  async function moveSteer(session, item, direction) {
    const order = steerMoveOrder(session, item.id, direction);
    if (!order) return;
    try {
      const data = await api.sessionReorderSteer(session.id, order);
      if (data.session) patchSessionPayload(data.session);
      composerSignature = "";
      syncComposer();
    } catch (err) { ctx.toast(err.message, "err"); }
  }

  async function decideApproval(session, item, decision) {
    if (approvalBusy.has(item.request_id)) return;
    approvalBusy.add(item.request_id);
    composerSignature = "";
    syncComposer();
    paintThread();
    try {
      const data = await api.sessionApproval(session.id, item.request_id, decision);
      if (data.session) patchSessionPayload(data.session);
      ctx.toast(t(decision === "deny" ? "session.toast.approval_denied"
        : decision === "always" ? "session.toast.always_allowed" : "session.toast.approved_once"), "ok");
      await loadTranscript(true);
      await refreshSessions();
    } catch (err) {
      ctx.toast(err.message, "err");
      await loadTranscript(true);
    } finally {
      approvalBusy.delete(item.request_id);
      composerSignature = "";
      syncComposer();
      paintThread();
    }
  }

  function steerQueuePanel(session) {
    const queue = pendingSteerItems(session);
    if (!queue.length) return null;   // nothing waiting: no card
    const queued = queuedSteerItems(session);
    const rows = queue.map((item, index) => {
      const editable = editableSteer(item);
      const editing = Boolean(steerEditing && steerEditing.id === item.id);
      const queuedIndex = queued.findIndex((entry) => entry.id === item.id);
      const actions = [];
      if (editable && !editing) {
        actions.push(h("button", {
          class: "icon-btn", type: "button", title: t("session.steer.edit_hint"),
          "aria-label": t("session.steer.edit_label"),
          onclick: () => {
            steerEditing = { id: item.id, text: item.text };
            composerSignature = "";
            syncComposer();
          },
        }, icon("edit", "sm")));
      }
      if (editable && queuedIndex >= 0) {
        actions.push(
          h("button", {
            class: "sw-steer-move", type: "button", title: t("session.steer.move_up"),
            "aria-label": t("session.steer.move_up_label"), disabled: queuedIndex === 0,
            onclick: () => moveSteer(session, item, -1),
          }, "↑"),
          h("button", {
            class: "sw-steer-move", type: "button", title: t("session.steer.move_down"),
            "aria-label": t("session.steer.move_down_label"), disabled: queuedIndex === queued.length - 1,
            onclick: () => moveSteer(session, item, 1),
          }, "↓"));
      }
      if (item.status === "queued" || item.status === "failed") {
        const retry = item.status === "failed";
        const interrupt = !retry && sendNowMode(session) === "interrupt" && isActiveSession(session);
        actions.push(h("button", {
          class: "btn sm ghost sw-steer-now", type: "button", "data-steer-now": item.id,
          title: t(interrupt ? "session.steer.send_now_interrupt" : "session.steer.send_now_hint"),
          onclick: async (event) => {
            event.currentTarget.disabled = true;
            try {
              const data = await sendQueuedNow(session.id, item.id);
              ctx.toast(t(data.mode === "interrupt" ? "session.toast.sent_now_interrupt" : "session.toast.sent_now"), "ok");
              stickToBottom = true;
              await refreshSessions();
              loadTranscript(false);
            } catch (err) { ctx.toast(err.message, "err"); }
          },
        }, icon(retry ? "refresh" : "send", "sm"), t(retry ? "session.steer.retry" : interrupt ? "session.steer.send_now_interrupt_short" : "session.steer.send_now")));
      }
      actions.push(h("button", {
        class: "sw-chip-x", type: "button", title: t(editable ? "session.steer.remove" : "session.steer.remove_record"),
        "aria-label": t("session.steer.remove"),
        onclick: () => removeSteer(session, item),
      }, "×"));
      return h("div", { class: `sw-steer-item ${item.status}`.trim() },
        h("span", { class: "sw-steer-item-index" }, String(index + 1)),
        h("div", { class: "sw-steer-item-main" },
          editing
            ? h("div", { class: "sw-steer-edit" },
              h("textarea", {
                class: "textarea sw-steer-edit-input", rows: "2", value: steerEditing.text,
                oninput: (event) => { steerEditing.text = event.target.value; },
              }),
              h("div", { class: "sw-steer-edit-actions" },
                h("button", { class: "btn sm primary", type: "button", onclick: () => saveSteer(session, item) },
                  icon("save", "sm"), t("action.save")),
                h("button", {
                  class: "btn sm ghost", type: "button",
                  onclick: () => { steerEditing = null; composerSignature = ""; syncComposer(); },
                }, t("action.cancel"))))
            : h("div", { class: "sw-steer-item-body" },
              h("span", { class: "sw-steer-item-text" }, item.text),
              h("span", { class: "sw-steer-item-meta" },
                steerLabel(item), item.edited_at ? ` · ${t("session.steer.edited")}` : "",
                item.error ? ` · ${item.error}` : "")),
          editing ? null : h("div", { class: "sw-steer-item-actions" }, actions)));
    });
    return h("details", { class: "sw-steer-panel", open: true },
      h("summary", null, icon("send", "sm"), t("session.steer.queue_title", { n: queue.length }),
        queued.length ? h("span", { class: "sw-steer-panel-hint" },
          t("session.steer.queue_waiting", { n: queued.length })) : null),
      h("div", { class: "sw-steer-list" }, rows));
  }

  function contextChips(session) {
    const items = contextItemsFor(session);
    if (!items.length) return null;
    return h("div", { class: "sw-ctx-row", "aria-label": t("session.context.attached") },
      h("span", { class: "sw-ctx-row-label" }, icon("layers", "sm"), t("session.context.count", { n: items.length })),
      items.map((item) => h("span", {
        class: `sw-ctx-chip ${item.kind}`.trim(), title: item.label,
      },
        icon(item.kind === "unit" ? "target" : item.kind === "media" ? "image" : item.kind === "file" ? "file" : "edit", "sm"),
        h("span", { class: "sw-ctx-chip-text" }, item.label),
        h("button", {
          class: "sw-chip-x", type: "button", title: t("session.context.remove"),
          "aria-label": t("session.context.remove_named", { label: item.label }),
          onclick: () => removeContext(session, item),
        }, "×"))));
  }

  function approvalBanner(session) {
    const pending = pendingApprovals(session);
    if (!pending.length) return null;
    const first = pending[0];
    return h("div", { class: "sw-approval-banner", role: "status" },
      icon("shield", "sm"),
      h("div", { class: "sw-approval-banner-main" },
        h("b", null, t(pending.length === 1 ? "session.approval.waiting_one" : "session.approval.waiting_n", { n: pending.length })),
        h("span", null, t("session.approval.banner_hint", { request: approvalRequestLabel(first) }))),
      h("span", { class: "spacer" }),
      h("button", {
        class: "btn sm ghost", type: "button",
        onclick: () => {
          const card = thread.querySelector(".sw-permission.pending");
          if (card) card.scrollIntoView({ block: "center", behavior: "smooth" });
        },
      }, icon("arrowRight", "sm"), t("session.approval.review")));
  }

  function syncComposer() {
    const session = current();
    if (!session) {
      clear(composerHost);
      composerSignature = "";
      return;
    }
    if (!presetsLoaded) loadPresets();
    const mode = composerMode(session);
    const choices = choicesFor(session);
    const signature = [
      session.id, session.status, mode.mode, presetsLoaded, presetsError, presetsLoading,
      (session.steer_queue || []).map((item) => `${item.id}:${item.status}:${String(item.text || "").slice(0, 16)}`).join(","),
      (session.approvals?.items || []).map((item) => `${item.id}:${item.status}`).join(","),
      contextItemsFor(session).map((item) => item.id).join(","),
      (sessionImages.get(session.id) || []).map((item) => item.id).join(","),
      choices.platform, choices.model, choices.effort, choices.access_mode,
      customModelSessions.has(session.id), modelClock,
      contextPickerOpen, composerConfigOpen, steerEditing?.id || "", String(steerEditing?.text || "").slice(0, 24),
    ].join("|");
    if (signature === composerSignature) return;
    composerSignature = signature;
    clear(composerHost);
    if (!mode.canSend) {
      composerHost.appendChild(h("div", { class: "sw-composer-disabled" },
        icon("clock", "sm"), mode.hint || t("session.composer.cannot_continue")));
      return;
    }
    const active = isActiveSession(session);
    const preset = presetById(choices.platform) || presetById(session.platform);
    const allowedAccess = sessionPermissionModes(session, preset);
    if (!allowedAccess.length) allowedAccess.push("read_only");
    if (!allowedAccess.includes(choices.access_mode)) {
      choices.access_mode = sessionAccessMode(session, allowedAccess);
    }
    const approvalSupported = sessionSupportsApproval(session, preset);
    const capabilities = preset?.provider_capabilities || {};
    const efforts = modelEfforts(preset, capabilities, choices.model);
    if (choices.effort && !efforts.includes(choices.effort)) choices.effort = "";

    const providerSelect = h("select", {
      class: "sw-select", "aria-label": t("session.provider"), title: active
        ? t("session.composer.provider_locked") : t("session.composer.provider_next"),
      disabled: active || !presetsLoaded,
      onchange: (event) => {
        const next = presetById(event.target.value);
        if (!next) return;
        choices.platform = next.id;
        choices.model = "";
        choices.effort = "";
        const nextModes = next.permission_modes || next.access_modes || ["read_only"];
        choices.access_mode = nextModes.includes(choices.access_mode) ? choices.access_mode
          : nextModes.includes("read_only") ? "read_only" : (nextModes[0] || "read_only");
        composerSignature = "";
        syncComposer();
      },
    },
      !presetsLoaded
        ? h("option", { value: choices.platform || session.platform }, presetsError ? t("session.composer.providers_unavailable") : t("session.composer.loading"))
        : null,
      ...(presetsLoaded ? presets.map((item) => h("option", {
        value: item.id, selected: item.id === choices.platform, disabled: !item.available,
      }, providerOptionLabel(item))) : []),
      presetsLoaded && !presetById(choices.platform)
        ? h("option", { value: choices.platform }, platformLabel({ id: choices.platform, platform_label: session.platform_label }))
        : null,
    );

    const modelPicker = renderModelPicker(session, preset, capabilities, choices, active);

    const effortSelect = h("select", {
      class: "sw-select", "aria-label": t("session.composer.effort_label"),
      title: efforts.length
        ? (active ? t("session.composer.effort_locked") : t("session.composer.effort_next"))
        : t("tasks.models.no_effort"),
      disabled: active || !efforts.length,
      onchange: (event) => { choices.effort = event.target.value; },
    },
      efforts.length
        ? [h("option", { value: "" }, t("session.composer.effort_default")),
          ...efforts.map((effort) => h("option", {
            value: effort, selected: effort === choices.effort,
          }, t("session.effort_option", { effort })))]
        : h("option", { value: "" }, t("session.composer.effort_unsupported")));

    const accessSelect = h("select", {
      class: `sw-select sw-access-select ${choices.access_mode}`.trim(),
      "aria-label": t("session.permission_mode"),
      title: active
        ? t("session.composer.mode_locked")
        : t("session.composer.mode_next"),
      disabled: active || allowedAccess.length < 2,
      onchange: (event) => {
        choices.access_mode = event.target.value;
        composerSignature = "";
        syncComposer();
      },
    }, allowedAccess.map((modeValue) => h("option", {
      value: modeValue, selected: modeValue === choices.access_mode,
      title: permissionModeDescription(modeValue),
    }, permissionModeLabel(modeValue))));

    const selectors = h("div", { class: "sw-composer-selectors" },
      h("span", { class: "sw-selector-label" }, icon("cpu", "sm"), t("session.composer.sel_provider")),
      providerSelect,
      h("button", {
        class: "icon-btn sw-provider-refresh", type: "button",
        title: t("session.composer.recheck"),
        disabled: presetsLoading || active,
        onclick: () => loadPresets(true),
      }, icon("refresh", "sm")),
      h("span", { class: "sw-selector-label" }, icon("sparkles", "sm"), t("session.composer.sel_model")),
      modelPicker.control,
      h("span", { class: "sw-selector-label" }, icon("clock", "sm"), t("session.composer.sel_effort")),
      effortSelect,
      h("span", { class: "sw-selector-label" }, icon("shield", "sm"), t("session.composer.sel_access")),
      accessSelect,
      h("span", { class: "spacer" }),
      usageBadge(),
      active
        ? h("button", { class: "btn sm danger sw-composer-stop", type: "button", onclick: stopSession },
          icon("x", "sm"), t("tasks.stop"))
        : h("button", { class: "btn sm ghost sw-composer-relaunch", type: "button", onclick: relaunchSession },
          icon("refresh", "sm"), t("session.relaunch")));

    const providerName = preset?.label
      ? platformLabel({ id: preset.id, platform_label: preset.label })
      : platformLabel({ id: choices.platform, platform_label: session.platform_label });
    const hadFocus = Boolean(composerInput && document.activeElement === composerInput);
    const caret = hadFocus ? [composerInput.selectionStart, composerInput.selectionEnd] : null;
    const input = h("textarea", {
      class: "sw-composer-input", rows: "1",
      value: draftBySession[session.id] || "",
      "aria-label": t("tasks.thread.composer_label"),
      title: t("tasks.thread.composer_keys"),
      placeholder: mode.mode === "steer"
        ? t(mode.channel === "stdin" ? "tasks.thread.steer_placeholder" : "tasks.thread.queue_placeholder", { provider: providerName })
        : t("tasks.thread.work_with", { provider: providerName }),
    });
    composerInput = input;
    const autoGrow = () => {
      input.style.height = "auto";
      input.style.height = `${Math.min(input.scrollHeight, isPhone() ? Math.round(window.innerHeight * 0.4) : 220)}px`;
    };

    const sendButton = h("button", { class: "sw-send", type: "button" });
    let stopMode = false;
    let stopBackground = false;
    const images = () => sessionImages.get(session.id) || [];
    const paintSendButton = () => {
      const live = current()?.id === session.id ? current() : session;
      const hasContent = Boolean(input.value.trim() || contextItemsFor(session).length || images().length);
      stopBackground = stopBackgroundMode(live, busyWork(live, activityOf(live)), { hasContent });
      stopMode = (turnRunning(live) && !hasContent) || stopBackground;
      const label = stopBackground ? t("session.stop_background") : stopMode ? t("tasks.stop")
        : mode.mode === "steer" ? t("tasks.thread.send_steer") : t("tasks.thread.send");
      sendButton.classList.toggle("stop", stopMode);
      sendButton.classList.toggle("empty", !stopMode && !hasContent);
      sendButton.toggleAttribute("data-stop-background", stopBackground);
      // The keys live in the tooltips, not in a row of their own.
      sendButton.title = `${label} · ${t(composerHintKey(turnRunning(session)))}`;
      input.title = t(composerHintKey(turnRunning(session)));
      sendButton.setAttribute("aria-label", label);
      clear(sendButton);
      sendButton.appendChild(icon(stopMode ? "stop" : "arrowUp", "sm"));
    };
    input.addEventListener("input", () => {
      draftBySession[session.id] = input.value;
      autoGrow();
      paintSendButton();
    });
    paintSendButton();
    repaintSend = () => { if (sendButton.isConnected) paintSendButton(); };
    let busy = false;
    const send = async (now = false) => {
      const text = input.value.trim();
      const context = [...contextPayload(session), ...imageContext(images())];
      if ((!text && !context.length) || busy) return;
      busy = true;
      sendButton.disabled = true;
      try {
        const data = await sendSessionMessage(session, text, { mode: mode.mode, context, choices, now: now && mode.mode === "steer" });
        const payload = data.session;
        if (data.mode) {
          ctx.toast(t(data.mode === "interrupt" ? "session.toast.sent_now_interrupt" : "session.toast.sent_now"), "ok");
        } else if (mode.mode === "steer") {
          const status = data.steer?.status;
          ctx.toast(t(status === "delivered" ? "session.toast.steer_delivered"
            : status === "queued" ? "session.toast.steer_queued" : "session.toast.steer_failed"), status === "failed" ? "err" : "ok");
        } else {
          ctx.toast(t("session.toast.continue_started"), "ok");
        }
        if (payload) patchSessionPayload(payload);
        draftBySession[session.id] = "";
        input.value = "";
        stickToBottom = true;
        clearContext(session);
        releaseImages(images());
        sessionImages.delete(session.id);
        composerSignature = "";
        syncComposer();
        await loadTranscript(true);
        await refreshSessions();
        loadDetail();
      } catch (err) {
        ctx.toast(err.message, "err");
      } finally {
        busy = false;
        sendButton.disabled = false;
        input.focus();
      }
    };
    sendButton.addEventListener("click", (event) => {
      if (stopBackground) {
        const rect = event.currentTarget.getBoundingClientRect();
        openContextMenu({ x: rect.right, y: rect.top, returnFocus: event.currentTarget, items: stopBackgroundItems(current() || session, {
          onStopped: () => { loadTranscript(true); refreshSessions(); },
          onError: (err) => ctx.toast(err.message, "err"),
        }) });
        return;
      }
      if (stopMode) stopSession(); else send();
    });
    input.addEventListener("keydown", (event) => {
      const action = composerKeyAction(event, { running: turnRunning(session) });
      if (!action || action === "newline") return;
      event.preventDefault();
      send(action === "now");
    });
    // Images: paste, drop, or the image button.
    const addImages = async (files) => {
      const { images: added, errors } = await readImages(files, images().length);
      for (const message of errors) ctx.toast(message, "err");
      if (!added.length) return;
      sessionImages.set(session.id, [...images(), ...added]);
      composerSignature = "";
      syncComposer();
    };
    const picker = imagePicker(addImages);
    bindImageInput(input, addImages);

    let accessWarning = null;
    if (choices.access_mode === "full_access") {
      accessWarning = h("div", { class: "sw-access-warning" }, icon("shield", "sm"),
        h("div", null, h("b", null, t("session.access.full_title")),
          ` — ${t("session.access.full_warning")}`));
    } else if (isApprovalMode(choices.access_mode)) {
      accessWarning = h("div", { class: `sw-mode-note ${approvalSupported ? "approval" : "unsupported"}`.trim() },
        icon(approvalSupported ? "shield" : "alert", "sm"),
        h("div", null,
          h("b", null, permissionModeLabel(choices.access_mode)),
          ` — ${permissionModeDescription(choices.access_mode)} `,
          approvalSupported
            ? t("session.access.approval_supported")
            : t("session.access.approval_unsupported")));
    } else if (choices.access_mode === "plan") {
      accessWarning = h("div", { class: "sw-mode-note plan" }, icon("shield", "sm"),
        h("div", null, h("b", null, t("session.access.plan_title")),
          ` — ${t("session.access.plan_warning")}`));
    }
    const lockHint = active
      ? h("div", { class: "sw-selector-lock" }, icon("clock", "sm"),
        mode.mode === "steer" && !session.capabilities?.followup
          ? t("session.composer.one_shot_hint")
          : t("session.composer.locked_hint"))
      : null;

    const fallbackModel = defaultModelOption(modelOptions(preset, capabilities));
    const configLabel = `${choices.model || (fallbackModel
      ? t("tasks.models.chip_default", { model: fallbackModel.id })
      : t("tasks.thread.default_model"))}${choices.effort ? ` · ${choices.effort}` : ""}`;
    const configButton = h("button", {
      class: "sw-chip sw-config-btn", type: "button",
      title: t(active ? "session.composer.config_locked" : "session.composer.config_next"),
      "aria-expanded": composerConfigOpen ? "true" : "false",
      onclick: () => { composerConfigOpen = !composerConfigOpen; contextPickerOpen = false; composerSignature = ""; syncComposer(); },
    }, icon("sparkles", "sm"), h("span", { class: "sw-config-label" }, configLabel), icon("chevron", "sm"));
    const configPanel = composerConfigOpen ? h("div", { class: "sw-composer-config" },
      h("div", { class: "sw-config-row" }, h("span", { class: "sw-config-icon" }, icon("cpu", "sm")),
        providerSelect,
        h("button", {
          class: "icon-btn sw-provider-refresh", type: "button",
          title: t("session.composer.recheck"),
          disabled: presetsLoading || active,
          onclick: () => loadPresets(true),
        }, icon("refresh", "sm"))),
      h("div", { class: "sw-config-row" }, h("span", { class: "sw-config-icon" }, icon("sparkles", "sm")),
        modelPicker.control),
      modelPicker.status,
      h("div", { class: "sw-config-row" }, h("span", { class: "sw-config-icon" }, icon("clock", "sm")),
        effortSelect,
        h("span", { class: "spacer" }),
        h("span", { class: "sw-config-hint" }, t(active ? "session.composer.locked_short" : "session.composer.next_turn_short"))),
      accessWarning,
      lockHint) : null;
    for (const option of accessSelect.options) option.textContent = capFirst(option.textContent);
    const accessChip = h("label", {
      class: `sw-chip sw-access-chip ${choices.access_mode}`.trim(), title: accessSelect.title,
    }, icon(choices.access_mode === "full_access" ? "shield" : choices.access_mode === "read_only" ? "eye" : "shield", "sm"),
      accessSelect);
    const box = h("div", { class: "sw-composer-box" },
      picker.input,
      contextPickerOpen ? renderContextPicker(session) : null,
      approvalBanner(session),
      steerQueuePanel(session),
      configPanel,
      contextChips(session),
      imageTray(images(), (image) => {
        releaseImages([image]);
        sessionImages.set(session.id, images().filter((entry) => entry.id !== image.id));
        composerSignature = "";
        syncComposer();
      }),
      input,
      h("div", { class: "sw-composer-tools" },
        h("button", {
          class: `sw-chip sw-icon-tool ${contextPickerOpen ? "on" : ""}`.trim(), type: "button",
          title: t("tasks.thread.attach"),
          "aria-label": t("tasks.thread.attach"),
          onclick: () => { contextPickerOpen = !contextPickerOpen; composerConfigOpen = false; composerSignature = ""; syncComposer(); },
        }, icon("plus", "sm"),
          contextItemsFor(session).length ? h("b", null, contextItemsFor(session).length) : null),
        h("button", {
          class: "sw-chip sw-icon-tool", type: "button", title: t("session.images.add"), "aria-label": t("session.images.add"),
          "data-add-image": "", onclick: () => picker.open(),
        }, icon("image", "sm")),
        h("span", { class: "spacer" }),
        accessChip,
        configButton,
        sendButton));
    const dock = h("div", { class: "sw-composer-dock" }, box);
    composerHost.appendChild(dock);
    requestAnimationFrame(autoGrow);
    if (hadFocus) {
      input.focus();
      if (caret) input.setSelectionRange(caret[0], caret[1]);
    }
  }

  function renderContextPicker(session) {
    const unitSearch = h("input", {
      class: "input sw-ctx-search", type: "search", placeholder: t("session.context.search_units_placeholder"),
      "aria-label": t("session.context.search_units"),
    });
    const unitBody = h("div", { class: "sw-ctx-list" });
    const units = (ctx.S.units || []).filter((unit) => !unit.archived);
    const paintUnits = () => {
      clear(unitBody);
      const query = unitSearch.value.trim().toLowerCase();
      const rows = units.filter((unit) => !query
        || `${unit.uid} ${unit.title || ""}`.toLowerCase().includes(query)).slice(0, 24);
      if (!rows.length) {
        unitBody.appendChild(h("div", { class: "sw-ctx-empty" }, t("session.context.no_units")));
        return;
      }
      for (const unit of rows) {
        unitBody.appendChild(h("button", {
          class: "sw-ctx-option", type: "button", title: `${unit.uid} · ${unit.title || ""}`,
          onclick: () => attachContext(session, { kind: "unit", uid: unit.uid }),
        }, icon(unit.type === "task" ? "tasks" : "target", "sm"),
          h("span", { class: "sw-ctx-option-main" }, h("b", null, unit.uid), unit.title || ""),
          h("span", { class: "sw-ctx-option-meta" }, unit.type || t("session.context.unit")),
          icon("plus", "sm")));
      }
    };
    unitSearch.addEventListener("input", paintUnits);
    paintUnits();

    const fileBody = h("div", { class: "sw-ctx-list" });
    const files = [];
    for (const entry of media?.documents || []) files.push({ ...entry, kind: "file" });
    for (const entry of media?.media || []) files.push({ ...entry, kind: "media" });
    for (const entry of media?.other || []) files.push({ ...entry, kind: "file" });
    if (mediaFor === session.id) {
      if (!files.length) fileBody.appendChild(h("div", { class: "sw-ctx-empty" }, t("session.context.no_files")));
      for (const entry of files.slice(0, 24)) {
        fileBody.appendChild(h("button", {
          class: "sw-ctx-option", type: "button", title: entry.path,
          onclick: () => attachContext(session, { kind: entry.kind, path: entry.path }),
        }, icon(entry.kind === "media" ? "image" : "file", "sm"),
          h("span", { class: "sw-ctx-option-main" }, h("b", null, entry.name), entry.path),
          h("span", { class: "sw-ctx-option-meta" }, formatBytes(entry.size)),
          icon("plus", "sm")));
      }
    } else {
      fileBody.appendChild(h("div", { class: "sw-side-loading" }, h("span", { class: "spinner" }), t("session.workspace.reading_session")));
      setTimeout(() => { if (!destroyed && selectedId === session.id && railTab) loadMedia(); }, 0);
    }

    const snippet = h("textarea", { class: "textarea sw-ctx-snippet", rows: "3", placeholder: t("session.context.snippet_placeholder") });
    const snippetLabel = h("input", { class: "input sw-ctx-snippet-label", placeholder: t("session.context.snippet_label") });
    const addSnippet = () => {
      const text = snippet.value.trim();
      if (!text) { snippet.focus(); return; }
      attachContext(session, { kind: "text", text, label: snippetLabel.value.trim() });
      snippet.value = "";
      snippetLabel.value = "";
    };
    return h("div", { class: "sw-context-pop" },
      h("div", { class: "sw-context-pop-head" },
        h("b", null, t("session.context.add")),
        h("span", { class: "sw-muted" }, t("session.context.add_hint")),
        h("span", { class: "spacer" }),
        h("button", { class: "icon-btn", type: "button", title: t("action.close"), "aria-label": t("session.context.close_picker"), onclick: () => { contextPickerOpen = false; composerSignature = ""; syncComposer(); } }, icon("x", "sm"))),
      h("div", { class: "sw-context-pop-cols" },
        h("section", { class: "sw-ctx-section" },
          h("h5", null, icon("target", "sm"), t("session.context.units")),
          unitSearch, unitBody),
        h("section", { class: "sw-ctx-section" },
          h("h5", null, icon("folder", "sm"), t("session.workspace.files")),
          fileBody),
        h("section", { class: "sw-ctx-section" },
          h("h5", null, icon("edit", "sm"), t("session.context.snippet")),
          snippetLabel, snippet,
          h("button", { class: "btn sm primary", type: "button", onclick: addSnippet }, icon("plus", "sm"), t("session.context.attach_snippet")))));
  }

  // ------------------------------------------------------------- side: data
  async function loadDetail(force = false) {
    const id = selectedId;
    if (!id || detailLoading || (!force && detailFor === id)) return;
    detailLoading = true;
    if (railTab === "overview") paintSide();
    try {
      const cached = detailFor === id ? null
        : await peekCached(`/api/agent-sessions/${encodeURIComponent(id)}`).catch(() => null);
      if (cached?.session && !destroyed && selectedId === id) {
        detail = cached.session;
        if (railTab === "overview") paintSide();
      }
      const data = await api.agentSession(id, { fresh: true });
      if (destroyed || selectedId !== id) return;
      detail = data.session || null;
      detailFor = id;
    } catch {
      detail = null;
    } finally {
      detailLoading = false;
      if (!destroyed && selectedId === id && railTab === "overview") paintSide();
    }
  }

  async function loadMedia(force = false) {
    const id = selectedId;
    if (!id || mediaLoading) return;
    if (!force && mediaFor === id) return;
    mediaLoading = true;
    mediaError = "";
    if (railTab === "media" || railTab === "docs") paintSide();
    try {
      media = await api.sessionMedia(id);
      mediaFor = id;
    } catch (err) {
      media = null;
      mediaError = err.message;
      mediaFor = id;
    } finally {
      mediaLoading = false;
      if (!destroyed && selectedId === id && (railTab === "media" || railTab === "docs")) paintSide();
    }
  }

  async function loadTaskArtifacts(force = false) {
    const session = current();
    if (!session?.task_ref || taskArtifactsLoading) return;
    if (!force && taskArtifactsFor === session.task_ref) return;
    taskArtifactsLoading = true;
    taskArtifactsError = "";
    taskArtifactsFor = session.task_ref;
    if (railTab === "artifacts") paintSide();
    try {
      const data = await api.unit(session.task_ref);
      const unit = data.unit || {};
      const related = [...(unit.links_out || []), ...(unit.links_in || [])]
        .map((link) => link.target || link.source)
        .filter((target) => target && ["artifact", "resource"].includes(target.type));
      const unique = new Map(related.map((target) => [target.uid, target]));
      taskArtifacts = [...unique.values()];
    } catch (err) {
      taskArtifacts = [];
      taskArtifactsError = err.message;
    } finally {
      taskArtifactsLoading = false;
      if (!destroyed && railTab === "artifacts") paintSide();
    }
  }

  async function loadArtifacts(path = "", force = false) {
    const id = selectedId;
    if (!id || artifactState.loading) return;
    if (!force && artifactState.for === id && artifactState.path === path && artifactState.data) return;
    artifactState = {
      ...artifactState, for: id, path, loading: true, error: "", preview: null, previewLoading: false,
    };
    paintSide();
    try {
      const data = await api.sessionArtifacts(id, path);
      if (destroyed || selectedId !== id) return;
      artifactState = { ...artifactState, for: id, path: data.path || "", data, loading: false, error: "" };
    } catch (err) {
      if (selectedId !== id) return;
      artifactState = { ...artifactState, for: id, loading: false, error: err.message };
    } finally {
      if (!destroyed && selectedId === id && railTab === "artifacts") paintSide();
    }
  }

  function ensureTabData() {
    const session = current();
    if (!session) return;
    if (railTab === "overview") loadDetail();
    if (railTab === "media" || railTab === "docs") loadMedia();
    if (railTab === "artifacts") {
      loadTaskArtifacts();
      loadArtifacts(artifactState.for === session.id ? artifactState.path : "");
    }
    if ((railTab === "docs" || railTab === "artifacts") && tabState(session).active) {
      ensureActiveFile();
    }
  }

  // ------------------------------------------------------------- side: paint
  function paintSide() {
    clear(sideHead);
    clear(sideTabs);
    clear(sideBody);
    const session = current();
    if (!session) {
      sideHead.appendChild(h("div", { class: "sw-side-title" }, h("span", { class: "sw-eyebrow" }, t("session.details.title")), h("h3", null, t("session.details.none"))));
      sideBody.appendChild(h("div", { class: "sw-side-empty" }, t("session.details.none_hint")));
      return;
    }
    const descendants = sessionDescendants(sessions, session.id);
    const own = activityChips.activity;
    const counts = {
      subagents: descendants.length + own.subagents.filter((entry) => entry.source !== "session").length,
      background: own.background.length,
      media: mediaFor === session.id ? (media?.media?.length || 0) : null,
      docs: mediaFor === session.id ? (media?.documents?.length || 0) : null,
    };
    sideHead.append(
      h("div", { class: "sw-side-title" },
        h("span", { class: "sw-eyebrow" }, t("session.details.title")),
        h("h3", { title: sessionTitle(session) }, sessionTitle(session))),
      h("div", { class: "sw-side-status" }, statusPill(shownStatus(session, activityOf(session))), busyNote(session, { reasonOnly: true })),
      h("button", {
        class: "icon-btn sw-mobile-btn", type: "button", title: t("session.details.close"),
        "aria-label": t("session.details.close_label"), onclick: () => closeMobilePanels(),
      }, icon("x", "sm")));
    for (const [id, labelKey, countKey] of RAIL_TABS) {
      const count = countKey ? counts[countKey] : null;
      sideTabs.appendChild(h("button", {
        class: `sw-side-tab ${railTab === id ? "on" : ""}`.trim(), type: "button", role: "tab",
        "aria-selected": railTab === id ? "true" : "false",
        onclick: () => switchTab(id),
      }, t(labelKey), count !== null && count !== undefined ? h("span", { class: "sw-tab-count" }, count) : null));
    }
    if (railTab === "overview") renderOverview(session);
    else if (railTab === "subagents") renderSubagents(session, descendants);
    else if (railTab === "background") renderBackground();
    else if (railTab === "media") renderMedia(session);
    else if (railTab === "docs") renderDocs(session);
    else if (railTab === "artifacts") renderArtifacts(session);
  }

  function switchTab(tab) {
    if (railTab === tab) return;
    railTab = tab;
    S.sessionRailTab = tab;
    paintSide();
    ensureTabData();
  }

  function sideSection(title, body, action = null) {
    return h("section", { class: "sw-side-section" },
      h("div", { class: "sw-side-section-head" }, h("h4", null, title), action),
      body);
  }

  function fact(label, value, mono = "") {
    return h("div", { class: "sw-fact" }, h("span", { class: "k" }, label),
      h("span", { class: `v ${mono}`.trim() }, value ?? "—"));
  }

  function progressStep(label, at, state) {
    return h("div", { class: `sw-progress-step ${state}`.trim() },
      h("span", { class: "sw-progress-dot" }),
      h("div", { class: "sw-progress-label" }, label,
        h("small", null, at ? fmtDateTime(at) : "—")));
  }

  function usageSection() {
    const usage = transcript?.usage;
    if (!usage) {
      return sideSection(t("session.section.token_usage"),
        h("div", { class: "sw-side-empty" }, t("session.usage.loading")));
    }
    const confidence = usage.estimated
      ? t("session.usage.estimated_note")
      : t("session.usage.reported_note", { n: usage.reported_events });
    return sideSection(t("session.section.token_usage"),
      h("div", { class: "sw-usage-detail" },
        h("div", { class: "sw-facts" },
          fact(t("session.fact.total"), t("session.usage.badge", { prefix: usage.estimated ? "≈" : "", n: formatTokens(usage.total_tokens) }), "mono"),
          fact(t("session.fact.input"), t("session.usage.tokens", { n: formatTokens(usage.input_tokens) }), "mono"),
          fact(t("session.fact.output"), t("session.usage.tokens", { n: formatTokens(usage.output_tokens) }), "mono"),
          usage.reasoning_tokens ? fact(t("session.fact.reasoning"), t("session.usage.tokens", { n: formatTokens(usage.reasoning_tokens) }), "mono") : null,
          usage.cache_read_input_tokens ? fact(t("session.fact.cached_input"), t("session.usage.tokens", { n: formatTokens(usage.cache_read_input_tokens) }), "mono") : null,
          fact(t("session.fact.source"), t(usage.estimated ? "session.usage.estimated" : "session.usage.reported"), "mono")),
        h("div", { class: "sw-usage-note" }, icon("info", "sm"), confidence)),
      h("span", { class: `sw-usage-pill ${usage.estimated ? "estimated" : "reported"}`.trim() },
        t("session.usage.badge", { prefix: usage.estimated ? "≈" : "", n: formatTokens(usage.total_tokens) })));
  }

  function permissionHistorySection(session) {
    const entries = session.permission_history || [];
    if (!entries.length) return null;
    return sideSection(t("session.section.permission_history"), h("div", { class: "sw-perm-history" },
      entries.slice(-8).map((entry) => h("div", { class: "sw-perm-history-row" },
        h("span", { class: "sw-perm-history-turn" }, t("session.turn_n", { n: entry.turn || "?" })),
        h("span", { class: `sw-mini-tag mode ${entry.mode || ""}`.trim() }, permissionModeLabel(entry.mode)),
        h("span", { class: "sw-muted" }, entry.source || ""),
        h("span", { class: "spacer" }),
        h("span", { class: "sw-item-time" }, entry.at ? sessionTime(entry.at) : "")))));
  }

  function approvalSection(session) {
    const supported = Boolean(session.capabilities?.supports_approval);
    const items = session.approvals?.items || [];
    const pending = items.filter((item) => item.status === "pending");
    const rules = session.approvals?.rules || [];
    if (!supported && !items.length) {
      return sideSection(t("session.section.approvals"), h("div", { class: "sw-boundary" }, icon("info", "sm"),
        h("div", null, t("session.approval.unsupported_note"))));
    }
    const recent = items.slice(-6).reverse();
    return sideSection(t("session.section.approvals"), h("div", { class: "sw-approvals" },
      h("div", { class: "sw-facts" },
        fact(t("session.fact.support"), t(supported ? "session.approval.via_stdin" : "session.approval.unsupported")),
        fact(t("session.fact.protocol"), session.approvals?.protocol || "ark-permission-v1", "mono"),
        fact(t("session.fact.pending"), String(pending.length)),
        fact(t("session.fact.always_rules"), String(rules.length))),
      recent.length ? h("div", { class: "sw-approval-mini-list" }, recent.map((item) => h("div", {
        class: `sw-approval-mini ${item.status}`.trim(),
      },
        h("span", { class: "sw-approval-mini-label", title: approvalRequestLabel(item) },
          approvalRequestLabel(item)),
        h("span", { class: `sw-mini-tag status ${item.status}`.trim() },
          approvalStatusLabel(item))))) : h("div", { class: "sw-side-empty" },
        t("session.approval.none")),
      rules.length ? h("div", { class: "sw-approval-rules" }, rules.map((rule) => h("span", {
        class: "sw-mini-tag rule",
      }, t("session.approval.rule", { kind: rule.kind || t("session.approval.kind_tool") }) + (rule.tool ? ` · ${rule.tool}` : "")))) : null,
      h("div", { class: "sw-boundary" }, icon("info", "sm"),
        h("div", null, supported
          ? t("session.approval.supported_note")
          : t("session.approval.no_stdin_note")))));
  }

  function permissionBoundary(session) {
    const mode = sessionAccessMode(session);
    if (mode === "full_access") {
      return sideSection(t("session.section.full_access_boundary"), h("div", { class: "sw-boundary danger" }, icon("shield", "sm"),
        h("div", null, t("session.boundary.full_access"))));
    }
    if (isApprovalMode(mode)) {
      return sideSection(t("session.section.approval_boundary"), h("div", { class: "sw-boundary" }, icon("shield", "sm"),
        h("div", null,
          t("session.boundary.mode", { mode: permissionModeLabel(mode), description: permissionModeDescription(mode) }),
          " ",
          t("session.boundary.approval"))));
    }
    if (mode === "plan") {
      return sideSection(t("session.section.plan_boundary"), h("div", { class: "sw-boundary" }, icon("shield", "sm"),
        h("div", null, t("session.boundary.plan"))));
    }
    return sideSection(t("session.section.read_only_boundary"), h("div", { class: "sw-boundary" }, icon("shield", "sm"),
      h("div", null, t("session.boundary.read_only"))));
  }

  function sideChatSection(session) {
    if (!session.side_chat_of) return null;
    const parent = sessions.find((item) => item.id === session.side_chat_of);
    return sideSection(t("session.side_chat.button"), h("div", { class: "sw-side-chat" },
      h("div", { class: "sw-facts" },
        fact(t("session.fact.relation"), forkRelationLabel(session) || t("session.relation.side_chat")),
        fact(t("session.fact.parent"), session.side_chat_of, "mono")),
      parent ? h("button", {
        class: "sw-inline-action", type: "button", onclick: () => select(parent.id),
      }, icon("git", "sm"), t("session.side_chat.open_parent", { title: sessionTitle(parent) })) : null,
      h("div", { class: "sw-muted" }, t("session.side_chat.process_note"))));
  }

  function activitySection(session) {
    const items = transcriptFor === session.id ? (transcript?.items || []) : [];
    const tools = items.filter((item) => item.type === "tool");
    const summary = toolGroupSummary(tools);
    const sources = contextItemsFor(session);
    const errors = items.filter((item) => item.type === "error").length;
    const stat = (iconName, label, value) => h("div", { class: `sw-activity-stat ${value ? "" : "zero"}`.trim() },
      icon(iconName, "sm"), h("b", null, String(value)), h("span", null, label));
    return sideSection(t("tasks.thread.activity"), h("div", { class: "sw-activity" },
      h("div", { class: "sw-activity-stats" },
        stat("terminal", t("tasks.thread.stat_commands"), summary.counts.command),
        stat("edit", t("tasks.thread.stat_edits"), summary.edited),
        stat("search", t("tasks.thread.stat_searches"), summary.counts.search),
        stat("file", t("tasks.thread.stat_reads"), summary.counts.read),
        summary.counts.other ? stat("command", t("tasks.thread.stat_other"), summary.counts.other) : null,
        errors ? stat("info", t("tasks.thread.stat_errors"), errors) : null),
      summary.files.length ? h("div", { class: "sw-activity-files" },
        h("div", { class: "sw-activity-head" }, t("tasks.thread.edited_files")),
        summary.files.slice(0, 12).map((path) => h("div", { class: "sw-activity-file", title: path },
          icon("file", "sm"), h("span", null, tabLabel(path)), h("small", null, path)))) : null,
      h("div", { class: "sw-activity-files" },
        h("div", { class: "sw-activity-head" }, t("tasks.thread.sources")),
        sources.length
          ? sources.map((item) => h("div", { class: "sw-activity-file", title: item.label },
            icon(item.kind === "unit" ? "target" : item.kind === "media" ? "image" : item.kind === "file" ? "file" : "edit", "sm"),
            h("span", null, item.label), h("small", null, item.kind)))
          : h("div", { class: "sw-side-empty" }, t("tasks.thread.no_sources")))));
  }

  function renderOverview(session) {
    const task = tasks.find((unit) => unit.uid === session.task_ref) || null;
    const active = isActiveSession(session);
    const finished = session.ended_at || (!active && session.status !== "archived");
    const prompt = detail?.prompt || transcript?.items?.find((item) => item.role === "user")?.text || "";
    const command = detail?.command_preview || "";
    const progress = h("div", { class: "sw-progress" },
      progressStep(t("overview.created"), session.created_at, "done"),
      progressStep(t("tasks.started"), session.started_at, session.started_at ? (active ? "active" : "done") : ""),
      progressStep(t(session.status === "errored" ? "session.group.errored" : "session.progress.finished"), session.ended_at,
        session.status === "errored" ? "error" : finished ? "done" : ""));
    const taskBody = session.task_ref
      ? (task
        ? h("button", { class: "sw-task-link", type: "button", onclick: () => ctx.openUnit(task.uid) },
          h("div", { class: "sw-task-meta" },
            h("span", { class: "sw-mini-tag task" }, task.uid),
            h("span", { class: `sw-task-status ${task.status}`.trim() }, task.status)),
          h("div", { class: "sw-task-title" }, task.title))
        : h("div", { class: "sw-task-link static" }, h("span", { class: "sw-mini-tag task" }, session.task_ref),
          h("div", { class: "sw-muted" }, t("session.task_not_loaded"))))
      : h("div", { class: "sw-side-empty" }, t("tasks.no_task"));
    sideBody.append(
      activitySection(session),
      sideSection(t("type.task"), taskBody),
      sideSection(t("session.section.progress"), progress),
      sideSection(t("filters.status"), h("div", { class: "sw-facts" },
        fact(t("session.fact.status"), [statusText(shownStatus(session, activityOf(session))),
          busyNote(session, { reasonOnly: true })?.textContent].filter(Boolean).join(" · ")),
        fact(t("session.fact.duration"), fmtDuration(session.started_at || session.created_at, session.ended_at)),
        fact(t("session.fact.last_activity"), sessionTime(session.last_activity_at)),
        fact(t("session.fact.exit"), session.exit_code === null || session.exit_code === undefined ? "—" : String(session.exit_code), "mono"),
        fact("stdout", t("session.lines_n", { n: session.counts?.stdout || 0 }), "mono"), // i18n-ignore: stream name
        fact("stderr", t("session.lines_n", { n: session.counts?.stderr || 0 }), "mono"), // i18n-ignore: stream name
        session.pid ? fact("pid", String(session.pid), "mono") : null)),
      sideSection(t("type.agent"), h("div", { class: "sw-facts" },
        fact(t("session.fact.platform"), platformLabel(session)),
        session.model ? fact(t("session.fact.model"), session.model, "mono") : null,
        session.effort ? fact(t("session.fact.effort"), session.effort, "mono") : null,
        fact(t("session.fact.permission"), session.permission_label || session.access_label
          || permissionModeLabel(sessionAccessMode(session))),
        fact(t("session.fact.turn"), `#${session.turn || 1}`),
        fact(t("session.fact.approvals"), t(session.capabilities?.supports_approval ? "session.approval.via_stdin" : "session.approval.unsupported")),
        session.agent ? fact(t("session.fact.agent"), session.agent) : null,
        fact(t("session.fact.project"), (session.project || {}).name || "—"),
        fact(t("session.fact.workspace"), sessionCwd(session), "mono"),
        session.parent_id ? fact(t("session.fact.parent"), session.parent_id, "mono") : null,
        session.relation ? fact(t("session.fact.relation"), forkRelationLabel(session)) : null)),
      permissionHistorySection(session),
      approvalSection(session),
      sideChatSection(session),
      session.worktree ? sideSection(t("session.group_by.worktree"), h("div", { class: "sw-worktree-detail" },
        h("div", { class: "sw-facts" },
          fact(t("session.fact.branch"), worktreeLabel(session) || "—", "mono"),
          fact(t("session.fact.path"), session.worktree.path || "—", "mono"),
          session.worktree.base ? fact(t("session.fact.base"), session.worktree.base, "mono") : null,
          session.worktree.head ? fact(t("session.fact.head"), session.worktree.head, "mono") : null,
          fact(t("session.fact.state"), worktreeConflict(session) ? t("session.worktree.shared")
            : session.worktree.dirty ? t("session.worktree.dirty", { n: session.worktree.dirty }) : t("session.worktree.clean")),
          session.worktree.active_count ? fact(t("session.fact.active_sessions"), String(session.worktree.active_count), "mono") : null,
          session.worktree.repository?.root ? fact(t("session.fact.repository"), session.worktree.repository.root, "mono") : null),
        worktreeConflict(session)
          ? h("div", { class: "sw-boundary danger" }, icon("alert", "sm"),
            h("div", null, t("session.worktree.conflict_note")))
          : h("div", { class: "sw-boundary" }, icon("shield", "sm"),
            h("div", null, t("session.worktree.linked_note"))),
        h("button", {
          class: "sw-inline-action", type: "button",
          onclick: async () => {
            const ok = await copyText(session.worktree.path || "");
            ctx.toast(ok ? t("action.copied") : t("action.copy"), ok ? "ok" : "err");
          },
        }, icon("copy", "sm"), t("session.worktree.copy_path")))) : null,
      usageSection(),
      sideSection(t("tasks.thread.initial_prompt"),
        detailLoading && !detail
          ? h("div", { class: "sw-side-empty" }, t("session.details.loading"))
          : prompt
            ? h("details", { class: "sw-prompt" },
              h("summary", null, t("session.prompt.show")),
              h("pre", { class: "sw-prompt-pre" }, prompt))
            : h("div", { class: "sw-side-empty" }, t("session.prompt.none")),
        prompt ? h("button", {
          class: "sw-inline-action", type: "button",
          onclick: async () => {
            const ok = await copyText(prompt);
            ctx.toast(ok ? t("action.copied") : t("action.copy"), ok ? "ok" : "err");
          },
        }, icon("copy", "sm"), t("action.copy")) : null),
      command ? sideSection(t("settings.command"), h("pre", { class: "sw-prompt-pre" }, command)) : null,
      permissionBoundary(session),
      sideSection(t("tasks.session_context"), readOnlyContext(session)));
  }

  function readOnlyContext(session) {
    const apiBase = session.context?.api?.base || "/api/task-agent/v1";
    const absoluteBase = /^https?:/i.test(apiBase) ? apiBase : `${location.origin}${apiBase}`;
    const endpoints = h("div", { class: "sw-endpoints" },
      ["/context", "/project", "/units", "/links", "/snapshot", "/session"].map((path) =>
        h("div", { class: "sw-endpoint" },
          h("span", { class: "m" }, "GET"), // i18n-ignore: HTTP method
          h("code", null, `/api/task-agent/v1${path}`))));
    const tokenHost = h("div", { class: "sw-token" },
      revealedToken
        ? h("code", null, revealedToken)
        : h("span", { class: "sw-muted" }, t("session.token.hidden")));
    return h("details", {
      class: "sw-context",
      open: contextOpen || Boolean(revealedToken),
      ontoggle: (event) => { contextOpen = event.target.open; },
    },
      h("summary", null, icon("shield", "sm"), t("session.token.context_summary")),
      h("div", { class: "sw-context-body" },
        h("div", { class: "sw-facts" },
          fact(t("session.fact.api"), absoluteBase, "mono"),
          fact(t("session.fact.snapshot"), "workspace/ark-context.json", "mono"),
          fact(t("session.fact.contract"), "workspace/ARK-TASK.md", "mono")),
        endpoints,
        h("div", { class: "sw-token-row" },
          h("span", { class: "sw-eyebrow" }, t("session.token.read_token")),
          h("button", {
            class: "sw-inline-action", type: "button",
            onclick: async () => {
              try {
                const token = detail?.read_token || (await api.agentSession(session.id)).session?.read_token || "";
                revealedToken = token;
                paintSide();
              } catch (err) { ctx.toast(err.message, "err"); }
            },
          }, icon("shield", "sm"), revealedToken ? t("action.refresh") : t("session.token.reveal")),
          revealedToken ? h("button", {
            class: "sw-inline-action", type: "button",
            onclick: async () => {
              const ok = await copyText(revealedToken);
              ctx.toast(ok ? t("action.copied") : t("action.copy"), ok ? "ok" : "err");
            },
          }, icon("copy", "sm"), t("action.copy")) : null),
        tokenHost));
  }

  function renderSubagents(session, descendants) {
    const ancestors = sessionAncestors(sessions, session.id);
    if (ancestors.length) {
      sideBody.appendChild(sideSection(t("session.section.parent_chain"), h("div", { class: "sw-breadcrumb" },
        ancestors.map((parent, index) => h("button", {
          class: "sw-breadcrumb-item", type: "button", onclick: () => select(parent.id),
        }, index ? icon("chevronRight", "sm") : null, sessionTitle(parent))))));
    }
    // Sub-agents the agent started itself (from its stream); background
    // tasks have their own tab.
    const own = activityList(activityChips.activity, "subagent").filter((entry) => entry.source !== "session");
    if (own.length) {
      sideBody.appendChild(sideSection(t("session.activity.from_stream", { n: own.length }),
        h("div", { class: "sa-side-list" }, own.map((entry) => activityRow(entry, {
          onOpenItem: revealThreadItem, onOpenSession: select, onOpenAgent: (item) => openAgent(item.itemId, item.label),
        })))));
    }
    const tree = h("div", { class: "sw-subagent-tree" });
    if (!descendants.length) {
      tree.appendChild(h("div", { class: "sw-side-empty" },
        t(own.length ? "session.subagents.empty_sessions" : "session.subagents.empty")));
    } else {
      for (const { session: child, depth } of descendants) {
        tree.appendChild(h("button", {
          class: `sw-subagent ${child.id === selectedId ? "on" : ""}`.trim(), type: "button",
          style: { paddingLeft: `${8 + (depth - 1) * 13}px` },
          onclick: () => select(child.id),
        },
          statusDot(shownStatus(child)),
          h("span", { class: "sw-subagent-title" }, sessionTitle(child)),
          h("span", { class: "sw-subagent-meta" }, platformLabel(child),
            child.model ? ` · ${child.model}` : "",
            ` · ${sessionTime(child.last_activity_at)}`),
          h("span", { class: `sw-subagent-state ${child.status}`.trim() }, statusText(child.status))));
      }
    }
    sideBody.append(
      sideSection(t("session.subagents.title", { n: descendants.length }), tree,
        h("button", { class: "btn sm ghost", onclick: () => onLaunch({ parent: session }) },
          icon("plus", "sm"), t("session.subagents.add"))));
  }

  function renderBackground() {
    const rows = activityList(activityChips.activity, "background");
    sideBody.appendChild(sideSection(t("session.activity.background_title"),
      rows.length
        ? h("div", { class: "sa-side-list" }, rows.map((entry) => activityRow(entry, { onOpenItem: revealThreadItem })))
        : h("div", { class: "sw-side-empty" }, t("session.activity.background_empty"))));
  }

  function renderMedia(session) {
    if (mediaFor !== session.id) {
      sideBody.appendChild(h("div", { class: "sw-side-loading" }, h("span", { class: "spinner" }), t("session.workspace.scanning")));
      setTimeout(() => { if (!destroyed && selectedId === session.id && railTab === "media") loadMedia(); }, 0);
      return;
    }
    if (mediaError) {
      sideBody.appendChild(h("div", { class: "sw-side-error" }, icon("alert", "sm"), mediaError));
      return;
    }
    const items = media?.media || [];
    sideBody.appendChild(sideSection(t("session.media.title", { n: items.length }),
      items.length ? h("div", { class: "sw-media-grid" }, items.map((entry) => mediaCard(entry)))
        : h("div", { class: "sw-side-empty" }, t("session.media.empty")),
      h("button", { class: "sw-inline-action", type: "button", onclick: () => loadMedia(true) },
        icon("refresh", "sm"), t("session.workspace.rescan"))));
    if (media?.truncated) {
      sideBody.appendChild(h("div", { class: "sw-muted" }, t("session.workspace.truncated")));
    }
  }

  function mediaCard(entry) {
    const kind = mediaKind(entry.path, entry.mime);
    const url = api.sessionArtifactUrl(selectedId, entry.path, true);
    const open = () => openMediaPreview(entry);
    return h("article", { class: `sw-media-card ${kind}`.trim() },
      h("button", { class: "sw-media-preview", type: "button", onclick: open, title: entry.path },
        kind === "image" ? h("img", { src: url, alt: entry.name, loading: "lazy" })
          : kind === "video" ? h("video", { src: url, muted: true, playsinline: true, preload: "metadata" })
            : kind === "audio" ? h("div", { class: "sw-audio-art" }, icon("send", "lg"))
              : h("div", { class: "sw-media-file" }, icon("file", "lg"))),
      h("div", { class: "sw-media-cap" },
        h("span", { class: "sw-media-name", title: entry.path }, entry.name),
        h("span", { class: "sw-media-size" }, formatBytes(entry.size))),
      h("div", { class: "sw-media-actions" },
        h("button", { class: "sw-inline-action", type: "button", onclick: open }, t("action.preview")),
        h("a", { class: "sw-inline-action", href: url, download: entry.name }, t("action.download"))));
  }

  function openMediaPreview(entry) {
    const kind = mediaKind(entry.path, entry.mime);
    const url = api.sessionArtifactUrl(selectedId, entry.path, true);
    if (kind === "image") {
      openImageLightbox(url, entry.name, selectedId, entry.path, formatBytes(entry.size));
      return;
    }
    const overlayRoot = document.getElementById("overlay-root") || document.body;
    let closed = false;
    const close = () => {
      if (closed) return;
      closed = true;
      overlay.remove();
      document.removeEventListener("keydown", onKey);
    };
    const onKey = (event) => { if (event.key === "Escape") close(); };
    const body = kind === "image" ? h("img", { class: "sw-lightbox-media", src: url, alt: entry.name })
      : kind === "video" ? h("video", { class: "sw-lightbox-media", src: url, controls: true, autoplay: true })
        : h("audio", { class: "sw-lightbox-audio", src: url, controls: true, autoplay: true });
    const overlay = h("div", { class: "sw-lightbox", onclick: (event) => { if (event.target === overlay) close(); } },
      h("div", { class: "sw-lightbox-box" },
        h("div", { class: "sw-lightbox-head" },
          h("span", { class: "sw-lightbox-name", title: entry.path }, entry.name),
          h("span", { class: "sw-lightbox-size" }, formatBytes(entry.size)),
          h("span", { class: "spacer" }),
          h("button", {
            class: "btn sm ghost", type: "button",
            onclick: () => api.openExternal({ session: selectedId, path: entry.path })
              .then(() => ctx.toast(t("toast.opened"), "ok"))
              .catch((err) => ctx.toast(err.message, "err")),
          }, icon("external", "sm"), t("action.open_external")),
          h("button", { class: "icon-btn", type: "button", title: t("action.close"), onclick: close }, icon("x", "sm"))),
        body));
    overlayRoot.appendChild(overlay);
    document.addEventListener("keydown", onKey);
  }

  function openImageLightbox(src, name, sessionId, path = "", meta = "") {
    const actions = [];
    if (path && sessionId) {
      actions.push(h("a", {
        class: "btn sm ghost", href: api.sessionArtifactUrl(sessionId, path, true), download: true,
      }, icon("download", "sm"), t("action.download")));
      actions.push(h("button", {
        class: "btn sm ghost", type: "button",
        onclick: () => api.openExternal({ session: sessionId, path })
          .then(() => ctx.toast(t("toast.opened"), "ok"))
          .catch((err) => ctx.toast(err.message, "err")),
      }, icon("external", "sm"), t("action.open_external")));
    }
    openSharedLightbox({ src, alt: name, title: name, meta, actions, className: "sw-image-lightbox" });
  }

  function openThreadImage(img) {
    const src = img.currentSrc || img.src;
    if (!src) return;
    openImageLightbox(src, img.alt || tabLabel(src), selectedId, img.dataset.path || "");
  }
  function fileCacheFor(session, path) {
    return fileCache.get(cacheFileKey(session, path));
  }

  function ensureFile(session, path, force = false) {
    const key = cacheFileKey(session, path);
    const existing = fileCache.get(key);
    if (!force && existing && (existing.loading || existing.data || existing.error)) return;
    const kind = fileViewKind(path);
    fileCache.set(key, { loading: true, error: "", data: null, kind });
    paintSide();
    if (kind === "image" || kind === "video" || kind === "audio" || kind === "pdf") {
      fileCache.set(key, {
        loading: false, error: "", kind,
        data: { media: true, path, name: tabLabel(path) },
      });
      if (!destroyed && selectedId === session.id) paintSide();
      return;
    }
    api.sessionArtifactRead(session.id, path).then((data) => {
      if (destroyed) return;
      fileCache.set(key, { loading: false, error: "", kind, data });
      if (selectedId === session.id) paintSide();
    }).catch((err) => {
      if (destroyed) return;
      fileCache.set(key, { loading: false, error: err.message, kind, data: null });
      if (selectedId === session.id) paintSide();
    });
  }

  function ensureActiveFile() {
    const session = current();
    if (!session) return;
    const state = tabState(session);
    if (state.active) ensureFile(session, state.active);
  }

  function openWorkspaceFile(session, entryOrPath) {
    const path = typeof entryOrPath === "string" ? entryOrPath : entryOrPath?.path;
    if (!path) return;
    setTabState(session, openFileTab(tabState(session), path));
    paintSide();
    ensureFile(session, path);
  }

  function closeWorkspaceFile(session, path) {
    const state = closeFileTab(tabState(session), path);
    setTabState(session, state);
    paintSide();
    if (state.active) ensureFile(session, state.active);
  }

  function activateWorkspaceFile(session, path) {
    const state = activateFileTab(tabState(session), path);
    setTabState(session, state);
    paintSide();
    ensureFile(session, path);
  }

  function fileTabsStrip(session) {
    const state = tabState(session);
    if (!state.tabs.length) return null;
    const strip = h("div", { class: "sw-file-tabs", role: "tablist", "aria-label": t("session.files.open_tabs") },
      state.tabs.map((path) => {
        const viewKind = fileViewKind(path);
        return h("div", { class: `sw-file-tab-wrap ${path === state.active ? "on" : ""}`.trim() },
          h("button", {
            class: "sw-file-tab", type: "button", role: "tab",
            "aria-selected": path === state.active ? "true" : "false",
            title: path, onclick: () => activateWorkspaceFile(session, path),
          },
            icon(viewKind === "markdown" ? "book" : viewKind === "code" ? "file" : viewKind === "image" ? "image" : "file", "sm"),
            h("span", { class: "sw-file-tab-label" }, tabLabel(path))),
          h("button", {
            class: "sw-file-tab-close", type: "button",
            title: t("session.files.close", { name: tabLabel(path) }), "aria-label": t("session.files.close", { name: tabLabel(path) }),
            onclick: (event) => { event.stopPropagation(); closeWorkspaceFile(session, path); },
          }, "×"));
      }),
      h("button", {
        class: "sw-file-tab-clear", type: "button", title: t("session.files.close_all"),
        "aria-label": t("session.files.close_all"),
        onclick: () => { setTabState(session, { tabs: [], active: "" }); paintSide(); },
      }, icon("x", "sm")));
    return h("div", { class: "sw-file-tabs-wrap" }, strip,
      h("span", { class: "sw-file-tabs-meta" }, t("session.files.open_count", { n: state.tabs.length })));
  }

  function renderFileView(session) {
    const state = tabState(session);
    if (!state.active) return null;
    const path = state.active;
    const cache = fileCacheFor(session, path);
    const viewKind = cache?.kind || fileViewKind(path);
    const modeKey = `${session.id}\u0000${path}`;
    const mode = fileModes.get(modeKey) || "rendered";
    const data = cache?.data;
    const head = h("div", { class: "sw-file-head" },
      h("span", { class: `sw-file-kind ${viewKind}`.trim() },
        icon(viewKind === "markdown" ? "book" : viewKind === "code" ? "command" : "file", "sm"),
        viewKind === "code" ? (data?.language || fileLanguage(path) || "code") : viewKind),
      h("span", { class: "sw-file-path", title: path }, path),
      h("span", { class: "spacer" }));
    if (data?.content !== undefined) {
      head.appendChild(h("span", { class: "sw-file-size" },
        `${formatBytes(data.size || String(data.content).length)} · ${t("session.lines_n", { n: data.lines || 0 })}`));
      if (viewKind === "markdown") {
        head.appendChild(h("button", {
          class: "sw-inline-action", type: "button",
          onclick: () => {
            fileModes.set(modeKey, mode === "rendered" ? "source" : "rendered");
            paintSide();
          },
        }, icon("edit", "sm"), t(mode === "rendered" ? "editor.source" : "editor.rendered")));
      }
      head.appendChild(h("button", {
        class: "sw-inline-action", type: "button",
        onclick: async () => {
          const ok = await copyText(data.content);
          ctx.toast(ok ? t("action.copied") : t("action.copy"), ok ? "ok" : "err");
        },
      }, icon("copy", "sm"), t("action.copy")));
    }
    head.appendChild(h("a", {
      class: "sw-inline-action", href: api.sessionArtifactUrl(session.id, path, true), download: true,
    }, icon("download", "sm"), t("action.download")));
    head.appendChild(h("button", {
      class: "sw-inline-action", type: "button",
      onclick: () => openArtifactExternal({ path, name: tabLabel(path) }),
    }, icon("external", "sm"), t("action.open")));

    let body = null;
    if (!cache || cache.loading) {
      body = h("div", { class: "sw-file-loading" }, h("span", { class: "spinner" }), t("session.files.loading"));
    } else if (cache.error) {
      body = h("div", { class: "sw-side-error" }, icon("alert", "sm"),
        h("div", null, cache.error,
          h("button", { class: "btn sm", type: "button", onclick: () => ensureFile(session, path, true) },
            icon("refresh", "sm"), t("action.retry"))));
    } else if (viewKind === "image") {
      const url = api.sessionArtifactUrl(session.id, path, true);
      body = h("figure", { class: "sw-file-figure" },
        h("img", {
          class: "sw-file-image", src: url, alt: tabLabel(path), loading: "lazy",
        }),
        h("figcaption", null, tabLabel(path)));
    } else if (viewKind === "video") {
      body = h("video", {
        class: "sw-file-video", src: api.sessionArtifactUrl(session.id, path, true),
        controls: true, playsinline: true, preload: "metadata",
      });
    } else if (viewKind === "audio") {
      body = h("audio", {
        class: "sw-file-audio", src: api.sessionArtifactUrl(session.id, path, true), controls: true,
      });
    } else if (viewKind === "pdf") {
      body = h("div", { class: "sw-file-pdf" }, icon("file", "lg"),
        h("b", null, tabLabel(path)), h("span", null, t("session.files.pdf_hint")));
    } else if (data?.content !== undefined) {
      if (viewKind === "markdown" && mode === "rendered") {
        body = h("div", { class: "sw-file-rich rich-wrap" }, richText(data.content));
      } else {
        const language = data.language || fileLanguage(path);
        body = h("div", { class: "sw-file-code" },
          h("div", { class: "sw-file-code-head" },
            h("span", { class: "code-lang" }, language || "text"), // i18n-ignore: language id
            h("span", { class: "sw-muted" }, t("session.lines_n", { n: data.lines || 0 })),
            h("span", { class: "spacer" }),
            h("button", {
              class: "sw-inline-action", type: "button",
              onclick: async () => {
                const ok = await copyText(data.content);
                ctx.toast(ok ? t("action.copied") : t("action.copy"), ok ? "ok" : "err");
              },
            }, icon("copy", "sm"), t("action.copy"))),
          h("pre", { class: "sw-file-pre" },
            h("code", { class: language ? `language-${language}` : "" },
              highlightCode(data.content, language))));
      }
    } else {
      body = h("div", { class: "sw-side-empty" }, t("session.files.no_preview"));
    }
    return h("section", { class: "sw-file-view" }, head, body);
  }

  function renderDocs(session) {
    const strip = fileTabsStrip(session);
    if (strip) sideBody.append(strip, renderFileView(session));
    if (mediaFor !== session.id) {
      sideBody.appendChild(h("div", { class: "sw-side-loading" }, h("span", { class: "spinner" }), t("session.workspace.scanning")));
      setTimeout(() => { if (!destroyed && selectedId === session.id && railTab === "docs") loadMedia(); }, 0);
      return;
    }
    if (mediaError) {
      sideBody.appendChild(h("div", { class: "sw-side-error" }, icon("alert", "sm"), mediaError));
      return;
    }
    const docs = media?.documents || [];
    sideBody.appendChild(sideSection(t("session.docs.title", { n: docs.length }),
      docs.length ? h("div", { class: "sw-doc-list" }, docs.map((entry) => h("div", {
        class: `sw-doc-row-wrap ${tabState(session).active === entry.path ? "on" : ""}`.trim(),
      },
        h("button", {
          class: "sw-doc-row", type: "button", onclick: () => openWorkspaceFile(session, entry),
        },
          icon(entry.kind === "pdf" ? "file" : /\.(md|markdown|mdx)$/i.test(entry.path) ? "book" : "file", "sm"),
          h("span", { class: "sw-doc-row-name", title: entry.path }, entry.name),
          h("span", { class: "sw-doc-row-meta" }, formatBytes(entry.size)),
          icon("chevronRight", "sm")))))
        : h("div", { class: "sw-side-empty" }, t("session.docs.empty")),
      h("button", { class: "sw-inline-action", type: "button", onclick: () => loadMedia(true) },
        icon("refresh", "sm"), t("session.workspace.rescan"))));
  }

  function renderArtifacts(session) {
    const strip = fileTabsStrip(session);
    if (strip) sideBody.append(strip, renderFileView(session));
    const taskSection = h("div", { class: "sw-task-artifacts" });
    if (!session.task_ref) {
      taskSection.appendChild(h("div", { class: "sw-side-empty" }, t("session.artifacts.no_task")));
    } else if (taskArtifactsLoading && !taskArtifacts) {
      taskSection.appendChild(h("div", { class: "sw-side-loading" }, h("span", { class: "spinner" }), t("session.artifacts.loading")));
      setTimeout(() => { if (!destroyed && selectedId === session.id && railTab === "artifacts") loadTaskArtifacts(); }, 0);
    } else if (taskArtifactsError) {
      taskSection.appendChild(h("div", { class: "sw-side-error" }, icon("alert", "sm"), taskArtifactsError));
    } else if (!taskArtifacts?.length) {
      taskSection.appendChild(h("div", { class: "sw-side-empty" }, t("session.artifacts.empty")));
    } else {
      for (const unit of taskArtifacts) {
        taskSection.appendChild(h("button", {
          class: "sw-artifact-link", type: "button", onclick: () => ctx.openUnit(unit.uid),
        }, icon(unit.type === "resource" ? "database" : "file", "sm"),
          h("span", { class: "sw-artifact-link-title" }, `${unit.uid} · ${unit.title}`),
          icon("chevronRight", "sm")));
      }
    }
    sideBody.appendChild(sideSection(t("session.section.task_artifacts"), taskSection,
      session.task_ref ? h("button", { class: "sw-inline-action", type: "button", onclick: () => loadTaskArtifacts(true) },
        icon("refresh", "sm"), t("action.refresh")) : null));

    const browse = h("div", { class: "sw-artifact-browse" });
    if (artifactState.for !== session.id) {
      browse.appendChild(h("div", { class: "sw-side-loading" }, h("span", { class: "spinner" }), t("session.workspace.reading")));
      setTimeout(() => { if (!destroyed && selectedId === session.id && railTab === "artifacts") loadArtifacts(""); }, 0);
    } else if (artifactState.loading) {
      browse.appendChild(h("div", { class: "sw-side-loading" }, h("span", { class: "spinner" }), t("session.workspace.reading")));
    } else {
      browse.appendChild(renderArtifactCrumb());
      if (artifactState.error) {
        browse.appendChild(h("div", { class: "sw-side-error" }, icon("alert", "sm"), artifactState.error));
      } else if (!artifactState.data?.entries?.length) {
        browse.appendChild(h("div", { class: "sw-side-empty" }, t("tasks.artifacts_empty")));
      } else {
        const list = h("div", { class: "sw-artifact-list" });
        for (const entry of artifactState.data.entries) {
          list.appendChild(h("div", { class: "sw-artifact-row-wrap" },
            h("button", {
              class: `sw-artifact-row ${entry.type}`.trim(), type: "button",
              onclick: () => entry.type === "dir" ? loadArtifacts(entry.path, true) : openWorkspaceFile(session, entry),
            },
              icon(entry.type === "dir" ? "folder" : "file", "sm"),
              h("span", { class: "sw-artifact-name" }, entry.name),
              h("span", { class: "sw-artifact-size" }, entry.type === "dir" ? "" : formatBytes(entry.size))),
            entry.type === "file" ? h("button", {
              class: "sw-artifact-open", type: "button", title: t("tasks.artifact_open_external"),
              onclick: () => openArtifactExternal(entry),
            }, icon("external", "sm")) : null));
        }
        browse.appendChild(list);
      }
    }
    sideBody.appendChild(sideSection(t("session.workspace.files"), browse,
      h("button", { class: "sw-inline-action", type: "button", onclick: () => loadArtifacts(artifactState.path, true) },
        icon("refresh", "sm"), t("tasks.artifact_refresh"))));
  }

  function renderArtifactCrumb() {
    const crumb = h("div", { class: "sw-artifact-crumb" });
    crumb.appendChild(h("button", { class: "sw-crumb", type: "button", onclick: () => loadArtifacts("", true) },
      icon("folder", "sm"), t("tasks.artifacts_root")));
    const parts = artifactState.path ? artifactState.path.split("/") : [];
    let prefix = "";
    parts.forEach((part, index) => {
      prefix = prefix ? `${prefix}/${part}` : part;
      const target = prefix;
      crumb.appendChild(h("span", { class: "sw-crumb-sep" }, "/"));
      crumb.appendChild(h("button", {
        class: `sw-crumb ${index === parts.length - 1 ? "on" : ""}`.trim(), type: "button",
        onclick: () => loadArtifacts(target, true),
      }, part));
    });
    return crumb;
  }

  async function openArtifactExternal(entry) {
    try {
      await api.openExternal({ session: selectedId, path: entry.path });
      ctx.toast(t("toast.opened"), "ok");
    } catch (err) { ctx.toast(err.message, "err"); }
  }

  // Context attached from the Sessions sidebar shows in this composer too.
  const stopContextWatch = sessionStream.subscribeContext((id) => {
    if (destroyed || !wrap.isConnected) { stopContextWatch(); return; }
    if (id !== selectedId) return;
    composerSignature = "";
    syncComposer();
  });

  // ------------------------------------------------------------------ boot
  S.selectedSessionId = selectedId || null;
  lastSessionSignature = sessionSignature(sessions);
  transcriptLoading = Boolean(selectedId);
  paintAll();
  ctx.updateTopbar?.();
  requestAnimationFrame(() => {
    if (!selectedId) return;
    loadTranscript(true);
    loadDetail();
    ensureTabData();
    applyPendingFile();
  });
  loadPresets();

  return {
    el: wrap,
    applySessions,
    refreshSessions,
    setStoredContexts,
    select,
    destroy() {
      destroyed = true;
      stopLiveTimer();
      stopContextWatch();
      for (const off of follows.values()) off();
      follows.clear();
    },
    get selectedId() { return selectedId; },
  };
}
