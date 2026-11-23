import {
  api, exportUrl, getMobileToken, setMobileToken, clearMobileToken, setTabProject, tabProjectId, setPairedDevice,
  configureClientCache, clearClientData, clientCache, prefetchUnits, prefetchRoute, qs,
  startEarlyGet, adoptBootRequests, prefetchProject, dropProjectPrefetch, checkConnectivity, syncState,
} from "./api.js";
import { liveSync } from "./live-sync.js";
import { loadPresence, setPresenceEnabled } from "./presence.js";
import { installSyncStatus, markFirstRender } from "./sync-status.js";
import { installSyncNow, requestSync, syncTopButton } from "./sync-now.js";
import { installPullToSync } from "./pull-to-sync.js";
import { cacheRoute, prefetchPlan, slowConnection } from "./client-cache-model.js";
import { UNIT_SAVED_EVENT, syncUnitLocally } from "./unit-edit.js";
import { S, setTab, persistFilters, resetFilters, saveFiltersAsDefault, clearFilterDefault, setLanguage, toggleTheme, activeFilterCount, filtersActive, filtersToParams, baseTypes, routeKey, PLAN_SUBS, emptyFilters, toggleTypeFilter, toggleStatusFilter, toggleImportanceFilter, RECENT_DAYS } from "./state.js";
import { applyProjectPalette } from "./appearance.js";
import { appearanceOverrides, resolveAppearance, APPEARANCE_CHANGED } from "./appearance-overrides.js";
import { t, getLang } from "./i18n.js";
import { h, clear, copyText, fmtDate, relativeTime, typeColor, typeBadge, slug } from "./dom.js";
import { fileUrlToPath, isLoopbackHost, isLikelyLocalPath, localOpenRoute } from "./rich-model.js";
import { icon } from "./icons.js";
import {
  MOBILE_PRIMARY_TABS, MOBILE_SECONDARY_TABS, parsePairHash,
} from "./mobile-core.js";
import {
  initPwa, renderPairGate, promptInstall, pwaSnapshot, syncThemeColor, watchKeyboard, confirmCodeOverlay,
} from "./mobile-ui.js";
import {
  toast, openModal, openProjectModal, openCreateUnit, openImportModal, openAssistant, openLinkModal, openDispatchModal,
  openExtractModal, openRelationModal, renderFilterRail, closeOverlay, viewToggle, confirmDialog, actorLabel,
} from "./components.js";
import { openInspector, closeInspector, showInspectorPlaceholder } from "./inspector.js";
import { openMultiInspector, multiInspectorWanted } from "./inspector-multi.js";
import { renderOverview } from "./views/overview.js";
import { saveFiltersAsTopic } from "./topic-actions.js";
import { ruleFromFilters } from "./topic-model.js";
// Research, Map and the Editor load on demand (first visit elsewhere does not
// download them; idle prefetch fetches them after the first screen).
import { renderEditor, editorExplorerVisible, setExplorerVisible, EXPLORER_CHANGED, editorKeyDown, openInEditor, editorHasDirty, focusQuickOpen, loadEditor } from "./editor-entry.js";
import { keyFromRoute } from "./editor-model.js";
import { assignedCount, assignedEntries, needsCount } from "./needs-model.js";
import { installUnitContextMenu, openUnitInEditor } from "./unit-menu.js";
import { projectMoveCommands } from "./project-move.js";
import { focusLocated, isLocateShortcut, setLocateContext, syncRevealNotice } from "./locate-map.js";
import { revealList } from "./locate-map-model.js";
import { applyRailGroup } from "./filter-group-model.js";
import { setActiveGraphPhysics, setActiveGraphArrows, setMindNodeStore, graphCameraEntries, seedGraphCameras } from "./graph.js";
import {
  createProjectStateStore, restorePlan, restorableRoute, sessionPatch, scrollSelector, projectCameras,
} from "./project-state-model.js";
import { desktopHub } from "./desktop-hub-model.js";
import { otherProjectsNew, withReviewBadge } from "./project-picker-model.js";
import { REVIEW_SEEN, refreshAssigned } from "./needs-ui.js";
import { getMindSettings, setActiveGraphDisplay } from "./mind-settings.js";
import { setMarkerGroups } from "./markers.js";
import { createMindNodeStore } from "./mind-node-state.js";
import { GraphFilterIndex } from "./graph-filter.js";
import { mountGuidance } from "./guidance.js";
import { maybeShowWelcome } from "./views/welcome.js";
import { startReloadGuard, safeUrl, reopenTarget } from "./reload-guard.js";
import { getShortcut, formatShortcut, shortcutMatches, shortcutLabel, SHORTCUTS_CHANGED } from "./shortcuts.js";
import { applyLayoutPrefs, mountSessionDock, relocalizeSessionDock, toggleSessionDock, clearAssistantHistory } from "./session-dock.js";
import {
  initWorkbench, viewHost, shellActive, render as renderWorkbench, toggleWorkbenchContainer,
  containerState, containerTitle, workbenchKeyDown, viewContainerVisible, WORKBENCH_CHANGED, showView, revealView,
  setActivityFooter,
} from "./workbench.js";
import { createTerminalPanel } from "./terminal-panel.js";
import { createSessionSidebar } from "./session-sidebar.js";
import { initPluginHost, registerBuiltinView, loadUserPlugins } from "./plugin-host.js";
import { createChangesPanel } from "./changes-panel.js";
import { createSearchPanel } from "./search-view.js";
import { createLocalGraphPanel } from "./local-graph-view.js";
import { initLayoutPresets, openLayoutMenu, presetCommands } from "./layout-presets.js";
import { assigneeMatches } from "./filter-store.js";
import { setUserDirectory } from "./users.js";
import {
  accountMenuButton, homeFilterQuery, initProfilePrefs, mindLayoutEntry, saveMindLayoutEntry, PROFILE_REFRESHED,
} from "./profile.js";
import { trackActivationPoint } from "./floating-position.js";
import { installDesktopShell } from "./desktop-shell.js";
import { withDesktopGroups, applyDesktopQuery, confirmLinkDesktop } from "./desktop-hub.js";
import { advancedEnabled, ADVANCED_CHANGED } from "./advanced.js";
import { liveProjectPicker, initProjectPrefs, withKnownActivity, PROJECTS_LISTED } from "./project-picker.js";
import { contextMenuOpen, openContextMenu } from "./context-menu.js";
import { reviewHash, reviewView } from "./needs-model.js";
import { parseJoinHash, openJoinDialog } from "./invites.js";
import { aboutActivityButton } from "./about.js";
import { watchUpdates } from "./update-state.js";
import { settingsTarget } from "./settings-tabs.js";
import { settingsNeedsRepaint, settingsViewKey } from "./settings-sync-model.js";
import { rememberMode } from "./read-edit-mode.js";

// Inside the ARK Studio desktop app (borderless window): top bar = title bar.
installDesktopShell();
// The desktop app's update state (local page only) for the About dot and Home's banner.
watchUpdates().catch(() => { /* browser or server page */ });

// A load right after the page hung (or crashed), or with ?safe=1, starts at
// Home instead of reopening the view that hung it (reload-guard.js).
const startGuard = startReloadGuard();
if (startGuard.safe && !parsePairHash(location.hash)) {
  try { history.replaceState(null, "", safeUrl(location.href)); } catch { /* ignore */ }
} else startGuard.safe = false;
const appRoot = document.getElementById("app");
const topbar = document.getElementById("topbar");
const mobileTabbar = document.getElementById("mobile-tabbar");
const currentActivationPoint = trackActivationPoint();
let loadSeq = 0;
let graphs = [];
let gantts = [];
let mobileMenuEl = null;
let mobileMenuClose = null;
let showingPairGate = false;
let tasksModule = null;
async function loadTasks() { return tasksModule ||= await import("./views/tasks.js"); }

// The preview shell groups the legacy views into six user-intent workspaces.
// Existing routes remain valid aliases so deep links and saved tabs keep working.
const WORKSPACES = [
  ["home", "nav.home", "home"],
  ["research", "nav.research", "bulb"],
  ["map", "nav.map", "map"],
  ["execute", "nav.execute", "play"],
  ["output", "nav.output", "layers"],
  ["editor", "nav.editor", "edit"],
];

const WORKSPACE_TABS = {
  home: ["overview"],
  research: ["ideas", "plan", "review"],
  map: ["map"],
  execute: ["tasks"],
  output: ["production", "distribution"],
  editor: ["editor"],
  assistant: ["chat"],
  settings: ["settings"],
};

function workspaceForTab(tab = S.tab) {
  for (const [workspace, tabs] of Object.entries(WORKSPACE_TABS)) {
    if (tabs.includes(tab)) return workspace;
  }
  return "home";
}

function workspaceLabel(workspace) {
  const item = WORKSPACES.find(([id]) => id === workspace) || WORKSPACES[0];
  return t(item[1]);
}

// Execute › Files moved into the Editor (explorer, Changes view with diffs,
// Terminal view); old #/execute/files links open the Editor.
const EXECUTE_VIEWS = ["board", "sessions", "gpu", "resources", "agents", "library"];
const EXECUTE_VIEW_ALIASES = { board: "board", runs: "sessions", sessions: "sessions", gpu: "gpu", resources: "resources", agents: "agents", library: "library" };

function workspaceHashForRoute(tab = S.tab, sub = S.sub) {
  if (tab === "overview") return "#/home";
  if (tab === "ideas") return "#/research/idea";
  if (tab === "plan") return `#/research/${PLAN_SUBS.includes(sub) ? sub : "story"}`;
  if (tab === "review") return reviewHash(S.reviewView);
  if (tab === "map") return "#/map";
  if (tab === "tasks") {
    const view = EXECUTE_VIEWS.includes(S.taskView) ? S.taskView : "board";
    return `#/execute/${view}`;
  }
  if (tab === "production") return "#/output/assets";
  if (tab === "distribution") return "#/output/distribution";
  if (tab === "chat") return "#/assistant";
  if (tab === "editor") return S.editorKey ? `#/editor/${encodeURIComponent(S.editorKey)}` : "#/editor";
  return "#/settings";
}

function navigateWorkspace(workspace, detail = null) {
  let tab = "overview";
  let sub = "story";
  let taskView = null;
  let review = null;
  if (workspace === "research") {
    const stage = String(detail || "").replace(/^plan\//, "");
    if (detail === "idea" || detail === "capture") tab = "ideas";
    else if (/^review(\/|$)/.test(String(detail || ""))) {
      // Research › Review: "review" (waiting) or "review/history".
      tab = "review";
      review = reviewView(String(detail).split("/")[1]);
    } else if (PLAN_SUBS.includes(stage)) {
      tab = "plan";
      sub = stage;
    } else {
      tab = "plan";
      sub = S.sub || "story";
    }
  } else if (workspace === "map") tab = "map";
  else if (workspace === "execute") {
    tab = "tasks";
    taskView = EXECUTE_VIEW_ALIASES[detail] || "board";
  } else if (workspace === "output") {
    tab = detail === "distribution" ? "distribution" : "production";
  } else if (workspace === "assistant") tab = "chat";
  else if (workspace === "settings") tab = "settings";
  else if (workspace === "editor") {
    tab = "editor";
    S.editorKey = detail ? String(detail) : "";
  }
  if (tab === "editor" && S.tab === "editor") {
    // Already in the Editor: the view itself switches tabs without a reload.
    renderCurrentOnly();
    return;
  }
  if (S.tab === tab && (tab !== "plan" || S.sub === sub)
      && (!taskView || S.taskView === taskView)
      && (!review || reviewView(S.reviewView) === review)
      && workspaceForTab(S.tab) === workspace) {
    syncTopbar();
    return;
  }
  setTab(tab, sub);
  if (taskView) S.taskView = EXECUTE_VIEWS.includes(taskView) ? taskView : "board";
  if (review) S.reviewView = review;
  S.workspace = workspace;
  const hash = workspace === "research"
    ? (tab === "ideas" ? "#/research/idea" : tab === "review" ? reviewHash(S.reviewView) : `#/research/${sub}`)
    : workspace === "execute" ? `#/execute/${S.taskView}`
    : workspace === "output" ? workspaceHashForRoute(tab)
    : workspace === "editor" ? workspaceHashForRoute("editor")
    : `#/${workspace}`;
  if (location.hash !== hash) history.pushState(null, "", hash);
  window.scrollTo({ top: 0, behavior: "smooth" });
  renderRoute();
}

const ctx = {
  S,
  navigate,
  navigateWorkspace,
  canStartGuidance: () => {
    if (!editorHasDirty()) return true;
    toast(t("tour.save_editor"), "err");
    return false;
  },
  refresh: () => renderRoute({ keepScroll: true }),
  // Data changed in this tab: graph views update in place (refreshData).
  refreshData: () => refreshData(),
  // Units saved in this tab (assignee picker, Assign to ▸): patch them where
  // they are shown, then reconcile without rebuilding a graph.
  unitsChanged: (units) => {
    for (const unit of units || []) if (unit) syncUnitLocally(ctx, unit);
    return refreshData();
  },
  changeLanguage: (lang) => changeLanguage(lang),
  renderTopbar: () => renderTopbar(),
  get graphs() { return graphs; },
  get gantts() { return gantts; },
  reloadDetail: async () => { if (S.selectedId) await openInspector(ctx, S.selectedId); },
  // `persist: false`: an edit action switched on its own (or a graph only
  // reported its start mode); keep the person's remembered choice.
  syncInspectorMode: async (mode = "", { persist = true } = {}) => {
    if (mode) {
      S.graphMode = mode === "read" ? "read" : "edit";
      if (persist) rememberMode(S.graphMode);
      if (ctx._syncingGraphMode) return;
      ctx._syncingGraphMode = true;
      try {
        for (const graph of graphs) {
          if (graph.readOnly || graph.mode === S.graphMode) continue;
          graph.setMode(S.graphMode, { persist: false });
        }
      } finally {
        ctx._syncingGraphMode = false;
      }
    }
    if (S.drawerOpen && S.multi) {
      await openMultiInspector(ctx, S.multi.refs, { graph: S.multi.graph, forceFloating: S.drawerFloating });
      return;
    }
    if (!S.drawerOpen || !S.detail?.id) return;
    await openInspector(ctx, S.detail.id, S.drawerAnchor, { forceFloating: S.drawerFloating });
  },
  closeUnit: () => closeInspector(ctx),
  navigateWorkspace: (workspace, detail = null) => navigateWorkspace(workspace, detail),
  openInEditor: (source, options = {}) => openInEditor(ctx, source, options),
  openUnitInEditor: (unit, options = {}) => openUnitInEditor(ctx, unit, options),
  openUnit: async (ref, anchor = null, options = {}) => {
    if (!ref) return;
    rememberOpened(ref);
    anchor ||= currentActivationPoint();
    document.getElementById("overlay-root")?.querySelectorAll(".modal-backdrop").forEach((el) => el.remove());
    S.railOpen = false;
    await openInspector(ctx, ref, anchor, options);
    prefetchDetails({ neighboursOf: S.detail?.id });
    // Opening the docked panel changes the canvas width. Re-fit after the
    // browser has applied the new layout so the graph can never be left blank.
    setTimeout(() => {
      for (const graph of graphs) graph.repairView?.();
    }, 80);
  },
  openUnitFloating: async (ref, anchor = null) => {
    if (!ref) return;
    anchor ||= currentActivationPoint();
    document.getElementById("overlay-root")?.querySelectorAll(".modal-backdrop").forEach((el) => el.remove());
    S.railOpen = false;
    await openInspector(ctx, ref, anchor, { forceFloating: true });
  },
  openCreate: (defaults = {}) => openCreateUnit(ctx, defaults),
  // "Locate in research map" (every node's menu, Alt+M; locate-map.js) and
  // the inspector's "Show on map".
  locateInMap: (uid) => locateInMap(uid),
  showOnMap: (uid) => locateInMap(uid),
  clearMapReveal: () => {
    if (!S.mapReveal?.length) return;
    S.mapReveal = [];
    applyFiltersIncremental({ silent: true, repaintRail: false, fit: false });
  },
  // A topic opens in Research > Topics; #/research/topic/<uid> deep-links it.
  openTopic: (ref, { keepTrail = false } = {}) => {
    if (!ref) return;
    // Already on the Topics page: swap the topic in place.
    if (S.tab === "plan" && ctx.topicPage?.mounted()) {
      ctx.topicPage.openRef(ref, { keepTrail });
      return;
    }
    // The breadcrumb only follows drilling into nested topics.
    if (!keepTrail) S.topicTrail = [];
    S.topicRef = String(ref);
    const hash = `#/research/topic/${encodeURIComponent(S.topicRef)}`;
    document.getElementById("overlay-root")?.querySelectorAll(".modal-backdrop").forEach((el) => el.remove());
    if (S.tab === "plan" && S.sub === "topic") {
      if (location.hash !== hash) history.pushState(null, "", hash);
      renderCurrentOnly();
      return;
    }
    navigateWorkspace("research", "topic");
    history.replaceState(null, "", hash);
  },
  saveFiltersAsTopic: () => saveFiltersAsTopic(ctx, ruleFromFilters(S.filters, baseTypes())),
  openImport: () => openImportModal(ctx),
  openExtract: () => openExtractModal(ctx),
  // Bulk edit (graph context menu): the multi-selection inspector.
  openBulkEdit: (nodes) => {
    const list = (nodes || []).filter((node) => node?.uid);
    if (list.length === 1) return ctx.openUnit(list[0].uid);
    const graph = graphs.find((item) => list.some((node) => item.selectedNodes?.has(node.id))) || null;
    return openMultiInspector(ctx, list.map((node) => node.id), { graph, anchor: currentActivationPoint() });
  },
  openEdgeRelation: (edge, source, target) => openRelationModal(ctx, edge, source, target),
  openAssistant: () => openAssistant(ctx),
  openLinkModal: (source, target) => openLinkModal(ctx, source, target),
  updateEdgeRelation: (edge, relation) => {
    if (!edge) return;
    for (const graph of graphs) graph.updateEdge?.(edge.id, { relation });
    if (S.detail?.links) {
      for (const group of ["links_in", "links_out"]) {
        for (const item of S.detail[group] || []) {
          if (item.id === edge.id) item.relation = relation;
        }
      }
    }
  },
  openDispatch: (unit) => openDispatchModal(ctx, unit),
  dispatchTaskSession: (unit) => loadTasks().then((view) => view.dispatchTaskSession(ctx, unit)).catch((err) => toast(err.message, "err")),
  openSessionLaunch: (prefill) => loadTasks().then((view) => view.openLaunchSession(ctx, prefill)).catch((err) => toast(err.message, "err")),
  openSettingsAt: (id) => openSettingsAt(id),
  openProject: () => openProjectModal(ctx, "open"),
  openPalette: () => openPalette(),
  openExport,
  setView: (view) => { S.view = view; renderRoute({ keepScroll: true }); },
  setTaskView: (view) => {
    if (view === "files") { openProjectFiles(); return; }
    S.taskView = EXECUTE_VIEWS.includes(view) ? view : "board";
    S.workspace = "execute";
    const hash = `#/execute/${S.taskView}`;
    if (location.hash !== hash) history.replaceState(null, "", hash);
    renderCurrentOnly();
  },
  rerenderTasks: () => { if (S.tab === "tasks") renderCurrentOnly(); },
  setSort: (sort) => { S.sort = sort; renderRoute({ keepScroll: true }); },
  setFilter: (key, value) => {
    S.filters[key] = value;
    paintFiltersNow();
    applyFiltersIncremental();
  },
  setTimeMode: (mode) => {
    S.filters.timeMode = ["all", "timeline", "recent", "overdue"].includes(mode) ? mode : "all";
    paintFiltersNow();
    applyFiltersIncremental();
  },
  setRecentDays: (days) => {
    S.filters.recentDays = RECENT_DAYS.includes(Number(days)) ? Number(days) : 7;
    S.filters.timeMode = "recent";
    paintFiltersNow();
    applyFiltersIncremental();
  },
  setTimeRange: (from, to, options = {}) => {
    const valid = (value) => {
      const text = String(value || "").trim();
      return !text || (/^\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?)?$/.test(text)
        && !Number.isNaN(Date.parse(text)));
    };
    S.filters.timeMode = "timeline";
    S.filters.from = valid(from) ? from : "";
    S.filters.to = valid(to) ? to : "";
    // A live drag must not rebuild the rail and replace the slider under the
    // pointer; paint it only on the first change or reset.
    if (options.repaint !== false) paintFiltersNow();
    applyFiltersIncremental({ repaintRail: options.repaintRail !== false });
  },
  infoHubChatContext: () => ({
    path: S.infoHubPath || "",
    filters: { ...(S.infoHubFilters || {}) },
    visible: Boolean(S.infoHubGraphVisible),
    node_count: S.infoHubData?.nodes?.length || 0,
  }),
  toggleInfoHubGraph: () => {
    S.infoHubGraphVisible = !S.infoHubGraphVisible;
    S.filterTab = S.infoHubGraphVisible ? "infohub" : "ark";
    localStorage.setItem("ark.infoHubGraph", S.infoHubGraphVisible ? "1" : "0");
    renderRoute({ keepScroll: true });
  },
  setFilterTab: (tab) => {
    if (tab === "infohub" && !S.infoHubGraphVisible) {
      S.infoHubGraphVisible = true;
      S.filterTab = "infohub";
      localStorage.setItem("ark.infoHubGraph", "1");
      renderRoute({ keepScroll: true });
      return;
    }
    S.filterTab = tab === "infohub" ? "infohub" : "ark";
    const rail = document.querySelector(".rail");
    if (rail) {
      const top = rail.scrollTop;
      renderFilterRail(rail, ctx);
      requestAnimationFrame(() => { rail.scrollTop = top; });
    }
  },
  toggleFilterValue: (key, value, sourceEl = null) => {
    if (key === "types") {
      toggleTypeFilter(S.filters, value);
      paintFiltersNow();
      applyFiltersIncremental();
      return;
    }
    const list = S.filters[key] || [];
    const active = !list.includes(value);
    S.filters[key] = active ? [...list, value] : list.filter((x) => x !== value);
    if (sourceEl) sourceEl.classList.toggle("on", active);
    paintFiltersNow();
    applyFiltersIncremental();
  },
  // A chip's menu or Alt+click: Only this / Select all / Invert in its group (filter-group-model.js).
  applyFilterGroup: (group, action, base, value) => {
    applyRailGroup(S.filters, group, action, base, value);
    paintFiltersNow();
    applyFiltersIncremental();
  },
  toggleAllTypes: (selected) => {
    S.filters.types = [];
    S.filters.typesNone = !selected;
    paintFiltersNow();
    applyFiltersIncremental();
  },
  toggleStatus: (status, baseValues = null) => {
    const base = baseValues || (S.vocab?.statuses || []).map((item) => item.id);
    toggleStatusFilter(S.filters, status, base);
    paintFiltersNow();
    applyFiltersIncremental();
  },
  toggleAllStatuses: (selected) => {
    S.filters.statuses = [];
    S.filters.statusesNone = !selected;
    paintFiltersNow();
    applyFiltersIncremental();
  },
  toggleImportance: (importance, baseValues = null) => {
    const base = baseValues || (S.vocab?.importance || []).map((item) => item.id);
    toggleImportanceFilter(S.filters, importance, base);
    paintFiltersNow();
    applyFiltersIncremental();
  },
  toggleAllImportance: (selected) => {
    S.filters.importanceLevels = [];
    S.filters.importanceNone = !selected;
    paintFiltersNow();
    applyFiltersIncremental();
  },
  clearFilters: () => {
    S.filters = emptyFilters();
    paintFiltersNow();
    applyFiltersIncremental();
  },
  // Reset = this tab's default (saved "Set as default", else built-in empty).
  resetFilters: () => {
    resetFilters();
    paintFiltersNow();
    applyFiltersIncremental();
  },
  saveFiltersAsDefault: () => {
    saveFiltersAsDefault();
    paintFiltersNow();
    toast(t("filters.default_saved"), "ok");
  },
  clearFiltersDefault: () => {
    clearFilterDefault();
    resetFilters();
    paintFiltersNow();
    applyFiltersIncremental();
    toast(t("filters.default_restored"), "ok");
  },
  toggleFilters: () => setFilterOverlay(!S.railOpen),
  closeFilters: () => setFilterOverlay(false),
  setGraphContext: (value) => ctx.setFilter("context", Boolean(value)),
  setGanttGroup: (group) => { S.ganttGroup = group; },
  moveTask: async (unit, status) => {
    await api.updateUnit(unit.uid, { status });
    unit.status = status;
    toast(t("toast.status", { status }), "ok");
    await renderRoute({ keepScroll: true });
  },
  applyBootstrap: (data, reset = false) => applyBootstrap(data, reset),
  switchProject: (project, options) => switchProject(project, options),
  applyProject: (data) => applyProject(data),
  applySettings: (data) => applySettings(data),
  applyPalette: (palette) => applyProjectPalette(palette),
  applyAppearance: () => applyEffectiveAppearance(),
  renderTopbar,
  updateTopbar: () => syncTopbar(),
  syncNeedsBadge: () => syncNeedsBadge(),
  trackGraph: (g) => {
    graphs.push(g);
    // Project graphs feed their selection to the multi-selection inspector.
    if (g?.opts?.projectUnits) g.onSelectionChange = (nodes) => onGraphSelection(g, nodes);
  },
  untrackGraph: (g) => { graphs = graphs.filter((item) => item !== g); },
  trackGantt: (g) => gantts.push(g),
  refreshFilters: () => paintFiltersNow(),
  toast,
};

// The open inspector again (single unit or multi-selection), e.g. after the
// docked pane was toggled.
function reopenInspector() {
  if (S.multi) return openMultiInspector(ctx, S.multi.refs, { graph: S.multi.graph });
  if (S.detail?.id) return openInspector(ctx, S.detail.id, S.drawerAnchor);
  return null;
}

const sameRefs = (a = [], b = []) => a.length === b.length && a.every((id, i) => String(id) === String(b[i]));

// A project graph's selection changed (GraphView.onSelectionChange): two or
// more nodes show their shared attributes when the inspector is open or
// docked; going back to one node shows that node, none closes the panel.
function onGraphSelection(graph, nodes) {
  if (!graphs.includes(graph)) return;
  const refs = nodes.map((node) => node.id).filter((id) => !String(id).startsWith("local-"));
  if (refs.length >= 2) {
    if (!multiInspectorWanted(S) || (S.multi && sameRefs(S.multi.refs, refs))) return;
    openMultiInspector(ctx, refs, { graph, forceFloating: S.drawerOpen && S.drawerFloating });
    return;
  }
  if (!S.multi || S.multi.graph !== graph) return;
  if (refs.length === 1) ctx.openUnit(nodes.find((node) => node.id === refs[0])?.uid || refs[0]);
  else closeInspector(ctx);
}

if (typeof window !== "undefined") {
  window.__ARK__ = {
    S, ctx,
    get graphs() { return graphs; },
    get gantts() { return gantts; },
    reload: () => renderRoute(),
  };
}

// --------------------------------------------------------------- remote sync
// Changes reach this tab two ways (docs/development/client-cache.md): the
// revision stream (live-sync.js) pulls a delta from GET /api/changes into the
// cache, and background revalidation of a cached response finds newer data.
// Both end here: the visible view is refreshed in place from the cache, never
// while someone is typing or a popup/inspector is open.
let remoteSyncBusy = false;
let refreshTimer = 0;
let refreshToast = false;

// `manual`: the person asked for the sync (Sync now), so an open inspector,
// context menu or preview does not hold it back; typing and dialogs still do.
function editingRemoteTarget({ manual = false } = {}) {
  const active = document.activeElement;
  const tag = String(active?.tagName || "").toLowerCase();
  if (active?.isContentEditable) return true;
  if (tag === "input" || tag === "textarea" || tag === "select") {
    // A field in the page would lose focus or text on a repaint.  An empty
    // field elsewhere (the Search sidebar focuses itself on start) would not.
    if (appRoot.contains(active) || String(active.value || "").trim()) return true;
  }
  if (document.querySelector('.ai-composer[aria-busy="true"]')
      || [...document.querySelectorAll('.ai-composer-input')].some((input) => input.value.trim())) return true;
  if (document.querySelector(".modal-backdrop, .palette-backdrop, .iv-overlay, .guidance-layer")) return true;
  if (!manual && document.querySelector(".drawer:not(.inspector-empty), .graph-menu, .unit-ref-preview, .infohub-preview")) return true;
  return false;
}

function scheduleRefresh({ announce = false } = {}) {
  refreshToast ||= announce;
  clearTimeout(refreshTimer);
  refreshTimer = setTimeout(runRefresh, 150);
}

async function runRefresh() {
  refreshTimer = 0;
  if (!S.booted) return;
  // Keep the change pending while a person works; re-check shortly.
  if (remoteSyncBusy || editingRemoteTarget()
      || Date.now() - Number(window.__ARK_LAST_ACTIVITY__ || 0) < 1500) {
    refreshTimer = setTimeout(runRefresh, 1500);
    return;
  }
  const announce = refreshToast;
  refreshToast = false;
  if (await refreshFromRemote() && announce) toast(remoteUpdateText(), "ok");
}

// M5: say who changed the project (the newest activity row) instead of
// assuming "another device"; an agent's write names the agent.
function remoteUpdateText() {
  const actor = String(S.activity?.[0]?.actor || "");
  const me = String(S.session?.user || "");
  if (!actor || actor === "human:ui" || actor.startsWith("human:plugin:") || actor === me || actor === "system" || actor.startsWith("system:")) {
    return t("home.remote_update");
  }
  return t("home.remote_update_by", { who: actorLabel(actor) });
}

// `manual` (Sync now): waits for a background refresh instead of skipping,
// and failures are thrown for the caller to report.
async function refreshFromRemote({ manual = false } = {}) {
  while (manual && remoteSyncBusy) await new Promise((resolve) => setTimeout(resolve, 100));
  if (remoteSyncBusy || editingRemoteTarget({ manual })) return false;
  remoteSyncBusy = true;
  const routeSeq = loadSeq, projectId = S.project?.id;
  const interrupted = () => editingRemoteTarget({ manual }) || routeSeq !== loadSeq || projectId !== S.project?.id;
  try {
    const boot = await api.bootstrap({ fresh: true });
    if (interrupted()) return false;
    applyBootstrap(boot);
    if (projectId !== S.project?.id) return true;
    const graphView = S.view === "graph" || S.view === "split";
    const targets = graphs.filter((graph) => graph.clipboardSource !== "infohub");
    if (graphView && ["ideas", "map", "plan"].includes(S.tab) && targets.length && S.tab !== "plan") {
      const snapshot = await api.graphSnapshot().catch(() => null);
      if (interrupted()) return false;
      S.graphIndex = snapshot ? await graphIndexFor(snapshot) : null;
      // Reuse the same in-place path as a tag/filter change: nodes and edges are
      // replaced on the existing GraphView, so camera, selection and the rest of
      // the page survive a server-side change.
      await applyFiltersIncremental({ silent: true, remote: true });
      syncTopbar();
    } else if (S.tab === "plan" && ctx.topicPage?.mounted()) {
      await ctx.topicPage.applyFilters?.({ repaintRail: true, remote: true });
      syncTopbar();
    } else if (["chat", "editor"].includes(S.tab)) {
      // Live surfaces with their own state: only the chrome follows.
      const rail = document.querySelector(".rail");
      if (rail) renderFilterRail(rail, ctx);
      syncTopbar();
    } else if (S.tab === "settings" && ctx.settingsPage?.mounted()
        && !settingsNeedsRepaint({ key: settingsViewKey(S), shownKey: ctx.settingsPage.shownKey,
          dirty: ctx.settingsPage.dirty(), manual })) {
      // Settings shows none of what changed (or has unsaved edits): the
      // page and its drafts stay; only the chrome follows.
      syncTopbar();
      return manual;
    } else {
      // Load first, then swap the page in one step (no loading flash).
      const data = await loadRouteData({ fresh: true });
      if (interrupted()) return false;
      await renderRoute({ keepScroll: true, preloaded: data });
    }
    return true;
  } catch (err) {
    if (manual) throw err;
    return false; // the next change or reconnect retries
  } finally {
    remoteSyncBusy = false;
  }
}

// Sync now (top-bar button, Alt+R, the status bar's sync item): read the
// project from the server again, bypassing the cache, and repaint the
// current page in place (same path as a change from elsewhere: the camera,
// selection and scroll position stay).  A stream that is down reconnects.
async function syncNow() {
  if (!(await checkConnectivity())) {
    throw new Error(t(syncState.connectivity === "unreachable" ? "sync.unreachable" : "sync.offline"));
  }
  liveSync.reconnect();
  // Every cached entry checked before now is read from the server once.
  syncState.syncRequestedAt = Date.now();
  if (!S.project) {
    applyBootstrap(await api.bootstrap({ fresh: true }));
    return;
  }
  // Open nodes (inspector, Editor tabs) revalidate; edited ones keep drafts.
  window.dispatchEvent(new CustomEvent("ark:units-touched", { detail: { ids: null } }));
  if (S.drawerOpen && S.detail?.id != null) api.unit(S.detail.id).catch(() => {});
  if (!(await refreshFromRemote({ manual: true }))) {
    // Someone is typing or a dialog is open: the cache is fresh now and the
    // page repaints as soon as they are done.
    await api.bootstrap({ fresh: true });
    scheduleRefresh();
  }
}
installSyncNow(syncNow);
installPullToSync(appRoot, requestSync, () => Boolean(S.project) && !showingPairGate);

// Data changed through this tab (a reassignment, a removed link, an archived
// node): bring the visible view up to date.  A Map/Ideas graph is never
// rebuilt for it: the snapshot is reloaded and re-filtered through
// GraphView.setData's in-place path, so nodes whose attributes changed are
// patched where they stand (graph-change.js) and only added or removed nodes
// move; the camera stays.  Other views re-render as before.
async function refreshData() {
  const graphView = (S.view === "graph" || S.view === "split") && ["ideas", "map"].includes(S.tab)
    && graphs.some((graph) => graph.clipboardSource !== "infohub");
  if (!graphView) return renderRoute({ keepScroll: true });
  const routeSeq = loadSeq, projectId = S.project?.id;
  const snapshot = await api.graphSnapshot({ fresh: true }).catch(() => null);
  if (routeSeq !== loadSeq || projectId !== S.project?.id) return undefined;
  S.graphIndex = snapshot ? await graphIndexFor(snapshot) : null;
  await applyFiltersIncremental({ silent: true, remote: true });
  syncTopbar();
  return undefined;
}

// A cached response was revalidated and the server had newer data.
window.addEventListener("ark:cache-updated", (event) => {
  const { kind, data } = event.detail || {};
  if (kind === "unit" && data?.unit) {
    // Clean inspector/Editor tabs take it; tabs with edits flag a conflict.
    window.dispatchEvent(new CustomEvent(UNIT_SAVED_EVENT, { detail: { unit: data.unit, origin: "remote" } }));
    return;
  }
  if (["comments", "topic", "me", "settings"].includes(kind)) return;
  // Execute/Editor panels (sessions, GPU, resources, files) poll and repaint
  // themselves; rebuilding the page for them would loop on their timestamps.
  if (cacheRoute(event.detail?.route || "")?.volatile) return;
  scheduleRefresh();
});

// The server answers again after an outage: catch up on what changed.
window.addEventListener("ark:reconnected", () => scheduleRefresh());

// This account lost access to the project the tab showed (grant revoked or
// project removed): its cache is gone; show what the server serves now.
window.addEventListener("ark:project-access-lost", async (event) => {
  if (event.detail?.project !== S.project?.id) return;
  toast(t("cache.access_lost"), "err");
  setTabProject("");
  try {
    applyBootstrap(await api.bootstrap({ fresh: true }), true);
    liveSync.restart();
  } catch { /* the auth/no-project handlers take over */ }
});

function startLiveSync() {
  loadPresence();
  liveSync.start({
    onChange: ({ own, delta }) => {
      // Open units (inspector, Editor tabs) revalidate now; a clean panel
      // repaints in place, an edited one keeps its draft.
      const touched = delta?.reset ? null : (delta?.touched || []);
      window.dispatchEvent(new CustomEvent("ark:units-touched", { detail: { ids: touched } }));
      if (S.drawerOpen && S.detail?.id != null && (!touched || touched.includes(S.detail.id))) {
        api.unit(S.detail.id).catch(() => {});
      }
      if (!own) scheduleRefresh({ announce: true });
    },
    onDenied: async ({ project }) => {
      await clientCache.dropProject(project);
      window.dispatchEvent(new CustomEvent("ark:project-access-lost", { detail: { project } }));
    },
    onAuth: async () => {
      await clearClientData();
      window.dispatchEvent(new CustomEvent("ark:auth-required", { detail: { code: "AUTH_REQUIRED" } }));
    },
  });
}

// ------------------------------------------------------------- prefetch
// After first paint, at idle: data for the other workspaces and details of
// the units a person is likely to open next, within a small budget.  Slow or
// data-saver connections only prefetch the selected unit's neighbours.
const PREFETCH_KEY = "ark.recentUnits";
let prefetchTimer = 0;

function rememberOpened(ref) {
  try {
    const list = JSON.parse(sessionStorage.getItem(PREFETCH_KEY) || "[]").filter((item) => item !== String(ref));
    list.unshift(String(ref));
    sessionStorage.setItem(PREFETCH_KEY, JSON.stringify(list.slice(0, 20)));
  } catch { /* storage disabled */ }
}

function connectionIsSlow() {
  return slowConnection(navigator.connection);
}

function idle(fn, timeout = 2500) {
  if (window.requestIdleCallback) requestIdleCallback(fn, { timeout });
  else setTimeout(fn, 200);
}

function prefetchDetails({ neighboursOf = null } = {}) {
  clearTimeout(prefetchTimer);
  prefetchTimer = setTimeout(() => idle(async () => {
    const slow = connectionIsSlow();
    const neighbours = neighboursOf != null && S.graphIndex
      ? [...(S.graphIndex.neighbors.get(Number(neighboursOf)) || [])] : [];
    let recent = [];
    try { recent = JSON.parse(sessionStorage.getItem(PREFETCH_KEY) || "[]"); } catch { /* ignore */ }
    const plan = prefetchPlan({
      neighbours,
      needs: (S.needsYou || []).filter((item) => item.kind !== "review_group").map((item) => item.id || item.uid),
      recent,
      visible: (S.units || []).slice(0, 30).map((unit) => unit.id),
    }, { cached: clientCache.cachedUnitIds(), budget: slow ? 12 : 30, slow });
    if (plan.length) prefetchUnits(plan).catch(() => {});
  }), 300);
}

const PREFETCH_SNAPSHOT_MAX_NODES = 3000;
let workspacePrefetchDone = false;
function prefetchWorkspaces() {
  if (workspacePrefetchDone || connectionIsSlow()) return;
  workspacePrefetchDone = true;
  idle(async () => {
    // Entry data of the other workspaces (Home, Research, Map, Execute), each
    // fetched only if nothing is cached for it yet; later visits render from
    // the cache and revalidate in the background.
    const units = (tab, sort, extra = {}) => `/api/units${qs(filtersToParams(emptyFilters(), {
      types: baseTypes(tab, "story"), limit: 600, sort: S.sort || sort, facets: 1, ...extra }))}`;
    // A large project's graph snapshot (megabytes; parsing and caching it
    // blocks the page for a moment) waits until the Map is opened.
    const smallGraph = Number(S.stats?.total || 0) <= PREFETCH_SNAPSHOT_MAX_NODES;
    const paths = [
      `/api/project/overview${qs(homeFilterQuery(S.project?.id))}`, "/api/topics",
      ...(smallGraph ? ["/api/graph/snapshot?bodies=0"] : []),
      units("ideas", "updated", { origins: "infohub", slim: 1 }),
      units("tasks", "importance", { slim: 1 }),
      // Execute: agent sessions (Board/Runs), presets, session context
      // (Library), resources.  The GPU board is not prefetched: it probes.
      "/api/agent-sessions", "/api/agent-sessions/presets", "/api/session-contexts", "/api/resources/graph",
    ];
    for (const path of paths) {
      if (document.hidden) break;
      try { await prefetchRoute(path); } catch { /* optional */ }
    }
  }, 4000);
}

// Hover intent: a unit row or card hovered for 150 ms prefetches its details.
let hoverTimer = 0;
document.addEventListener("pointerover", (event) => {
  const el = event.target.closest?.("[data-unit-ref], [data-unit-id]");
  clearTimeout(hoverTimer);
  if (!el) return;
  const ref = el.dataset.unitRef || el.dataset.unitId;
  hoverTimer = setTimeout(() => {
    if (!clientCache.cachedUnitIds().has(String(ref))) prefetchUnits([ref]).catch(() => {});
  }, 150);
}, { passive: true });

window.addEventListener("ark:graph-hover", (event) => {
  const ref = event.detail?.id;
  clearTimeout(hoverTimer);
  if (ref == null) return;
  hoverTimer = setTimeout(() => {
    if (!clientCache.cachedUnitIds().has(String(ref))) prefetchUnits([ref]).catch(() => {});
  }, 150);
});

// A new ARK version was installed in the background.  Offer a reload instead
// of forcing one; the update also applies on the next open or reload.
let updateBar = null;
window.addEventListener("ark:update-ready", () => {
  if (updateBar?.isConnected) return;
  updateBar = h("div", { class: "update-bar", role: "status" },
    h("span", null, t("app.update.ready")),
    h("button", { class: "btn sm primary", type: "button", onclick: () => location.reload() }, t("app.update.reload")),
    h("button", {
      class: "icon-btn", type: "button", title: t("app.update.later"), "aria-label": t("app.update.later"),
      onclick: () => { updateBar?.remove(); },
    }, icon("x", "sm")));
  document.body.appendChild(updateBar);
});

// Local file links in rendered Markdown ([Open folder](file:///E:/...)).
// Browsers refuse to navigate from http(s) to file:, so ARK opens them: on
// this computer through the host's file manager, otherwise it shows the
// path to copy (the files live on the ARK server, not on this device).
document.addEventListener("click", async (event) => {
  const link = event.target.closest?.("a[data-local-href]");
  if (!link || event.defaultPrevented) return;
  event.preventDefault();
  event.stopPropagation();
  const href = link.dataset.localHref;
  const path = fileUrlToPath(href);
  if (isLoopbackHost(location.hostname)) {
    try {
      await api.openExternal({ url: href });
      toast(t("links.local.opened"), "ok");
      return;
    } catch (err) {
      toast(t("links.local.open_failed", { error: err.message }), "err");
    }
  }
  showLocalPath(path);
}, true);

async function openLocalPathHere(path) {
  // The page is served by the remote ARK host; the browser is on the user's computer.  The
  // ARK Studio desktop app opens it itself (asking outside trusted folders); a browser
  // posts to the local tray's loopback bridge, which opens paths only inside its roots.
  try {
    const route = localOpenRoute(window.__ARK_DESKTOP__);
    if (route.kind === "desktop") {
      await window.__ARK_DESKTOP__.openLocal(path);
      toast(t("links.local.opened"), "ok");
      return true;
    }
    const response = await fetch(route.url, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ path }),
    });
    const data = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(data?.error?.message || `HTTP ${response.status}`);
    toast(t("links.local.opened"), "ok");
    return true;
  } catch (err) {
    toast(t("links.local.open_failed", { error: err?.message || String(err) }), "err");
    return false;
  }
}

function showLocalPath(path) {
  const local = isLikelyLocalPath(path, { userAgent: navigator.userAgent, desktop: window.__ARK_DESKTOP__ });
  const actions = [
    { label: t("action.close"), ghost: true },
    {
      label: t("links.local.copy"), primary: !local,
      onClick: async () => { toast((await copyText(path)) ? t("links.local.copied") : t("links.local.copy_failed"), "ok"); },
    },
  ];
  if (local) {
    actions.unshift({
      label: t("links.local.open_here"), primary: true,
      onClick: async () => await openLocalPathHere(path),
    });
  }
  openModal({
    title: t("links.local.title"),
    body: h("div", null,
      h("p", { class: "muted", style: { marginTop: "0" } }, local ? t("links.local.local_hint") : t("links.local.remote_hint")),
      h("pre", { class: "code-box", style: { whiteSpace: "pre-wrap", wordBreak: "break-all" } }, path)),
    actions,
  });
}

// Android hardware/gesture back: close the topmost web surface first.  The
// native shell arms a second back to leave the WebView for the server list.
function androidBack() {
  if (mobileMenuEl) {
    mobileMenuClose?.({ restore: false });
    return true;
  }
  if (S.railOpen || document.querySelector(".rail.open")) {
    setFilterOverlay(false);
    return true;
  }
  const chatSide = document.querySelector(".chat-side.mobile-open");
  if (chatSide) {
    chatSide.classList.remove("mobile-open");
    document.querySelector(".chat-scrim")?.classList.add("hidden");
    return true;
  }
  const sessionPanel = document.querySelector(".sw-rail.mobile-open, .sw-side.mobile-open");
  if (sessionPanel) {
    sessionPanel.classList.remove("mobile-open");
    document.querySelector(".sw-mobile-scrim")?.classList.add("hidden");
    return true;
  }
  if (S.drawerOpen || document.querySelector("#overlay-root .drawer")) {
    closeInspector(ctx);
    return true;
  }
  if (document.querySelector(".modal-backdrop, .palette-backdrop, .drawer-backdrop, .infohub-preview")) {
    closeOverlay();
    return true;
  }
  const graphHelp = document.querySelector(".graph-help:not(.hidden)");
  if (graphHelp) {
    graphHelp.classList.add("hidden");
    return true;
  }
  return false;
}

if (typeof window !== "undefined") window.ARKAndroidBack = androidBack;

// --------------------------------------------------------------------- boot
init();

async function init() {
  window.ARKBoot?.progress(1);
  watchKeyboard();
  renderMobileNav();
  topbar.innerHTML = "";
  appRoot.innerHTML = `<div class="loading" style="padding-top:18vh"><span class="spinner"></span>${t("action.loading")}</div>`;
  try {
    await window.ARKBoot?.styles;
    // Account sessions: bootstrap (and Home's overview) travel alongside the
    // session check.  Pairing flows keep the strict order.
    if (!parsePairHash(location.hash) && !getMobileToken()) {
      guessTabProject();
      adoptBootRequests();
      startEarlyGet("/api/bootstrap");
      if (tabProjectId() && parseHash().tab === "overview") startEarlyGet("/api/project/overview");
    }
    let unreachable = false;
    const session = await api.mobileSession().catch((error) => { unreachable = Boolean(error.network); return null; });
    setPairedDevice(session?.authenticated && session?.token_present);
    await bindClientCache(session, { unreachable });
    // A stale/expired token must not poison the loopback desktop UI: if this
    // request did not need pairing, drop the unusable token and continue.
    if (session?.token_present && !session.authenticated && !session.required) {
      clearMobileToken();
    }
    const pairToken = parsePairHash(location.hash);
    if (pairToken) {
      const outcome = await claimPairCode(pairToken, session);
      if (outcome === "claimed") return; // claim reloads with the token stored
      if (outcome === "failed") return;
    }
    if (session?.required && !session.authenticated) {
      showPairGate(session);
      return;
    }
    await bootWorkbench();
  } catch (err) {
    if (window.ARKBoot) { window.ARKBoot.fail(err.message); return; }
    topbar.innerHTML = "";
    appRoot.innerHTML = "";
    appRoot.appendChild(h("div", { class: "page" }, h("div", { class: "empty", style: { marginTop: "10vh" } },
      h("b", null, t("error.load")),
      h("div", null, err.message),
      h("button", { class: "btn primary", onclick: () => location.reload() }, t("action.retry")))));
  }
}

// The cache belongs to the account the server just confirmed (cache_id), so
// cached data is never shown before the server says who is signed in.  A
// new tab resumes the project this browser last showed for that account.
const LAST_PROJECT_KEY = "ark.cache.lastProject";

// Offline start: the server cannot confirm the account, so the account this
// browser last confirmed is used, read-only (cleared on sign-out and on 401).
const CACHE_IDENTITY_KEY = "ark.cache.identity";

async function bindClientCache(session, { unreachable = false } = {}) {
  installSyncStatus();
  let cacheId = session?.cache_id || "";
  let schema = session?.cache_schema || 0;
  if (!cacheId && unreachable) {
    try { ({ id: cacheId = "", schema = 0 } = JSON.parse(localStorage.getItem(CACHE_IDENTITY_KEY) || "{}")); } catch { /* none */ }
    // The failed session check already entered the offline state and
    // started the health probe (api.js connectivity).
  }
  if (!cacheId) {
    if (session && !session.cache_id) localStorage.removeItem(CACHE_IDENTITY_KEY);
    return;
  }
  try { localStorage.setItem(CACHE_IDENTITY_KEY, JSON.stringify({ id: cacheId, schema })); } catch { /* ignore */ }
  let last = {};
  try { last = JSON.parse(localStorage.getItem(LAST_PROJECT_KEY) || "{}"); } catch { /* ignore */ }
  if (!tabProjectId() && last.id === cacheId && last.project && !getMobileToken()) setTabProject(last.project);
  try {
    await configureClientCache({ cacheId, schema, project: tabProjectId() });
  } catch { /* no cache: everything still loads from the network */ }
}

// A new tab resumes the project this browser showed last (the server falls
// back when the account cannot read it; bindClientCache checks the account).
function guessTabProject() {
  if (tabProjectId()) return;
  try {
    const last = JSON.parse(localStorage.getItem(LAST_PROJECT_KEY) || "{}");
    if (last.project) setTabProject(last.project);
  } catch { /* ignore */ }
}

function rememberProject() {
  const cacheId = clientCache.cacheId;
  if (!cacheId || !S.project?.id) return;
  try { localStorage.setItem(LAST_PROJECT_KEY, JSON.stringify({ id: cacheId, project: S.project.id })); } catch { /* ignore */ }
}

async function claimPairCode(token, session) {
  const prompt = confirmCodeOverlay();
  try {
    let claim;
    try {
      claim = await api.claimPairing(token, prompt);
    } finally {
      prompt.close();
    }
    setMobileToken(claim.mobile_token, claim);
    clearPairHash();
    try { localStorage.setItem("ark.mobile.paired_at", new Date().toISOString()); } catch { /* ignore */ }
    toast(t(claim.read_only ? "app.toast.phone_paired_read_only" : "app.toast.phone_paired_full"), "ok");
    // A hard reload is the simplest way to boot every module with the new
    // header and discard any partially loaded unauthenticated state.
    location.reload();
    return "claimed";
  } catch (err) {
    if (err.cancelled) {
      clearPairHash();
      return "ignored";
    }
    if (session?.required) {
      showPairGate(session, err.message);
      return "failed";
    }
    toast(t("app.toast.pairing_link_error", { error: err.message }), "err");
    clearPairHash();
    return "ignored";
  }
}

function clearPairHash() {
  const raw = location.hash || "";
  let cleaned = raw.replace(/([#?&])pair=[^&]*/g, "").replace(/[?&]$/, "");
  if (!cleaned || cleaned === "#" || cleaned === "#/") cleaned = "#/overview";
  try {
    history.replaceState(null, "", location.pathname + location.search + cleaned);
  } catch { /* ignore */ }
}

function showPairGate(session, error = "") {
  if (showingPairGate) return;
  showingPairGate = true;
  clear(mobileTabbar);
  document.body.classList.add("pairing-gate");
  topbar.innerHTML = "";
  appRoot.innerHTML = "";
  renderPairGate(appRoot, {
    session,
    api,
    error,
    onConnected: async (claim) => {
      setMobileToken(claim.mobile_token, claim);
      clearPairHash();
      toast(t(claim.read_only ? "app.toast.phone_paired_read_only" : "app.toast.phone_paired_full"), "ok");
      location.reload();
    },
  });
  window.ARKBoot?.finish();
  initPwa();
}

window.addEventListener("ark:mobile-auth-required", async () => {
  if (showingPairGate) return;
  const token = getMobileToken();
  if (token) clearMobileToken();
  const session = await api.mobileSession().catch(() => null);
  if (session?.required) {
    showPairGate(session, token
      ? "This pairing expired or was revoked. Generate a new QR in Settings → Mobile."
      : "");
  }
});

// The account session can expire while the page is open.  Send the browser
// back through the login page with the current hash route/fragment preserved.
window.addEventListener("ark:auth-required", () => {
  if (location.pathname.endsWith("/login")) return;
  const next = location.pathname + location.search + location.hash;
  location.assign("./login?next=" + encodeURIComponent(next));
});

async function bootWorkbench() {
  window.ARKBoot?.progress(2);
  const data = await api.bootstrap();
  applyBootstrap(data);
  initProfilePrefs(data.profile);
  mindNodeStore.reset();
  applyLayoutPrefs();
  document.body.classList.toggle("assistant-dock-open", Boolean(S.sessionDock));
  rememberProject();
  // After a hang the project's saved page could hang again: forget it.
  if (startGuard.safe) projectStates.drop(S.project?.id);
  // The desktop app opens a server's page without a route when it moves
  // between this computer and a server: show the project as it was left.
  let bootPlan = null;
  if (!startGuard.safe && desktopHub() && /^#?\/?$/.test(location.hash) && !parsePairHash(location.hash)) {
    bootPlan = restorePlan(projectStates.get(S.project?.id));
    if (bootPlan) {
      restoring = { project: S.project?.id };
      prepareRestore(bootPlan);
      history.replaceState(null, "", location.pathname + location.search + bootPlan.route);
    }
  }
  const route = parseHash();
  setTab(route.tab, route.sub);
  if (route.topicRef) S.topicRef = route.topicRef;
  S.workspace = route.workspace || workspaceForTab(route.tab);
  if (route.taskView) S.taskView = route.taskView;
  if (route.reviewView) S.reviewView = route.reviewView;
  if (route.tab === "editor") S.editorKey = route.editorKey || "";
  applyViewQuery();
  applyFilterTabQuery();
  if (bootPlan?.view) S.view = bootPlan.view;
  window.ARKBoot?.progress(3);
  setupWorkbench();
  initLayoutPresets(data.profile);
  initProjectPrefs(data.profile, { toast });
  // The picker ranks projects by the person's own activity, which only GET
  // /api/projects carries: load it once now so the first menu is in order.
  projectsReadAt = Date.now();
  api.projects().then((list) => {
    if (Array.isArray(list?.projects)) S.projects = list.projects;
    syncReviewDot();
  }).catch(() => { /* the bootstrap list stays */ });
  await renderRoute();
  markFirstRender();
  if (bootPlan) finishRestore(S.project?.id, bootPlan);
  // An invite link (#/join/<token>) opens its Join dialog; the Welcome waits for the next visit.
  const joining = maybeOpenJoin();
  mountSessionDock(ctx);
  installUnitContextMenu(ctx);
  setLocateContext(ctx);
  // First run: the Welcome speaks first; the guide invitation waits for it.
  if (!joining) maybeShowWelcome(ctx);
  ctx.guidance = mountGuidance(ctx);
  window.ARKBoot?.finish();
  if (startGuard.safe) offerReopen();
  prefetchDetails();
  prefetchWorkspaces();
  prefetchViewModules();
  // Prepare offline assets after the initial view is usable.
  if (window.requestIdleCallback) requestIdleCallback(() => initPwa(), { timeout: 2000 });
  else setTimeout(() => initPwa(), 0);
  const unitRef = new URLSearchParams(location.search).get("unit");
  if (unitRef) setTimeout(() => ctx.openUnit(unitRef), 80);
  // ?ark_open_project= / ?ark_new_project=1 (the desktop app opening a server).
  applyDesktopQuery({
    currentProjectId: () => S.project?.id,
    openProjectById: (id) => switchProject({ id, name: (S.projects || []).find((p) => p.id === id)?.name || "" }),
    openNewProject: () => openProjectModal(ctx, "new"),
    linkDesktop: () => confirmLinkDesktop({ modal: openModal, toast }),
    toast,
  });
  window.addEventListener("hashchange", onHashChange);
  window.addEventListener("hashchange", saveProjectStateSoon);
  window.addEventListener("popstate", onHashChange);
  // "System" theme preference following the OS (profile.js).
  window.addEventListener("ark:theme-applied", () => { renderTopbar(); graphs.forEach((g) => g._draw?.()); });
  // Advanced features shown/hidden: sub-tabs and page panels change.
  window.addEventListener(ADVANCED_CHANGED, () => { renderTopbar(); if (S.tab !== "settings") renderRoute({ keepScroll: true }); });
  window.addEventListener("keydown", onKeyDown);
  window.addEventListener("beforeunload", (event) => {
    if (!editorHasDirty()) return;
    event.preventDefault();
    event.returnValue = "";
  });
  document.addEventListener("pointerdown", (event) => {
    if (!S.drawerOpen || S.inspectorPane) return;
    if (event.target.closest?.(".drawer, .modal, .modal-backdrop, .palette-backdrop")) return;
    closeInspector(ctx);
  }, true);
  window.addEventListener("resize", () => { /* graph ResizeObserver handles layout */ });
  const markActivity = () => { window.__ARK_LAST_ACTIVITY__ = Date.now(); };
  document.addEventListener("pointerdown", markActivity, true);
  document.addEventListener("keydown", markActivity, true);
  startLiveSync();
}

function applyFilterTabQuery() {
  if (new URLSearchParams(location.search).get("filter") === "infohub") S.filterTab = "infohub";
}

// A signed-in member may legitimately have no project grant yet.  Show a clear
// state instead of rendering an empty workbench or another user's data.
function renderNoProjectState() {
  clear(appRoot);
  syncTopbar();
  const user = S.session?.user || "";
  const admin = Boolean(S.session?.is_admin);
  appRoot.appendChild(h("div", { class: "page" },
    h("div", { class: "panel settings-panel" },
      h("div", { class: "panel-head" },
        h("h3", null, t("settings.no_project_title")),
        h("span", { class: "pill warn" }, S.session?.role || "member")), // i18n-ignore: role value, shown raw like other roles
      h("p", { class: "settings-lead" }, t("settings.no_project_hint")),
      h("div", { class: "settings-note" }, icon("info", "sm"),
        h("div", null,
          h("b", null, user ? `${user}. ` : ""),
          admin
            ? t("app.no_project_admin_hint")
            : t("settings.no_project_grant"))),
      h("div", { class: "head-actions" },
        h("button", {
          class: "primary-btn", type: "button",
          onclick: () => openProjectModal(ctx, "new"),
        }, icon("plus", "sm"), t("project.create")),
        h("button", {
          class: "ghost-btn", type: "button",
          onclick: async () => {
            try { await api.logout(); } catch { /* ignore */ }
            location.assign("./login");
          },
        }, t("settings.sign_out"))))));
}

function applyViewQuery() {
  const value = new URLSearchParams(location.search).get("view");
  if (value && ["list", "graph", "split", "outline", "timeline", "gantt", "board", "sessions", "gpu", "resources", "agents", "library"].includes(value)) {
    S.view = value;
    if (S.tab === "tasks" && ["sessions", "gpu", "resources", "board", "timeline", "agents", "library"].includes(value)) {
      S.taskView = value;
    }
  }
}

// Project palettes and reading preferences share native theme tokens.

function applyBootstrap(data, reset = false) {
  const previous = S.project?.id;
  bootstrapProject = data?.summary ? { at: Date.now(), data } : null;
  S.project = data.summary ? { ...data.summary } : null;
  // This tab keeps showing this project until the person switches here.
  setTabProject(S.project?.id || "");
  S.projects = withKnownActivity(data.projects, S.projects) || S.projects;
  S.session = data.session || S.session;
  S.org = data.org || S.org;
  if (Array.isArray(data.users)) setUserDirectory(data.users, data.profile);
  S.vocab = data.vocabulary || S.vocab;
  S.palettePresets = data.palette_presets || S.palettePresets || {};
  S.stats = data.stats || S.stats;
  S.activity = data.activity || [];
  S.nextActions = data.next_actions || [];
  S.needsYou = data.needs_you || [];
  S.needsAssigned = [];
  refreshAssigned(ctx);
  S.updatedAt = data.updated_at || S.project?.updated;
  S.dueTasks = data.dueTasks || S.dueTasks || [];
  S.booted = true;
  applyEffectiveAppearance();
  const switched = Boolean(reset || (previous && S.project?.id && previous !== S.project.id));
  if (switched && previous && previous !== S.project?.id) {
    rememberProject();
    liveSync.restart();
    // View plugins: session write grants end; "project" event subscribers hear it.
    window.dispatchEvent(new CustomEvent("ark:project-switched", { detail: { id: S.project?.id || "", name: S.project?.name || "" } }));
  }
  if (switched) {
    // A topic of the previous project means nothing here.
    S.topicRef = "";
    S.topicTrail = [];
    S.taskSessions = [];
    S.sessionsLoaded = false;
    S.selectedSessionId = null;
    // Back to a project seen before: its page as it was left.
    const plan = restorePlan(projectStates.get(S.project?.id));
    if (plan) {
      applyRestorePlan(plan, { push: true });
      return;
    }
    setTab("overview");
    history.pushState(null, "", "#/overview");
    renderRoute();
  }
}

// ------------------------------------------------------------ project switch
// Clicking another project gives feedback at once (the menu closes, the
// picker shows the target, the old page is inert), stops the old project's
// graphs and route loads, and reads the new Home's data together with
// POST /api/projects/open instead of after it.  The latest click wins: an
// earlier switch that answers later is dropped.
const SWITCH_ROUTES = ["/api/project/overview", "/api/skills/setup/state", "/api/workspace/info"];
let switchSeq = 0;

async function switchProject(project, { beforeOpen = null } = {}) {
  const seq = ++switchSeq;
  // Back to the shown project while another switch is on its way counts as
  // a switch too: that one already stopped the page.
  const same = project.id === S.project?.id && !document.body.classList.contains("project-switching");
  // The page being left, for when the person comes back (before the menu
  // closes the inspector).
  if (!same && !document.body.classList.contains("project-switching")) saveProjectState();
  beforeOpen?.(project);
  if (!same) {
    showSwitching(project);
    // Nothing of the old project keeps running while the new one loads.
    ++loadSeq;
    destroyVisuals();
    // A first visit lands on Home: read its data in parallel with the open.
    // (A local server has one current project for every tab: nothing to gain.)
    if (!S.session?.local && !projectStates.get(project.id)?.route) prefetchProject(project.id, SWITCH_ROUTES);
  }
  try {
    const data = await api.openProject({ id: project.id });
    if (seq !== switchSeq) return;
    endSwitching();
    applyBootstrap(data, !same);
    toast(t("project.toast.opened", { name: project.name }), "ok");
  } catch (err) {
    if (seq !== switchSeq) return;
    dropProjectPrefetch(project.id);
    endSwitching();
    if (!same) {
      // Back to the project this tab still shows.
      syncTopbar();
      renderRoute({ keepScroll: true });
    }
    toast(err.message, "err");
  }
}

function showSwitching(project) {
  document.body.classList.add("project-switching");
  const picker = topbar.querySelector(".project-picker");
  if (!picker) return;
  picker.setAttribute("aria-busy", "true");
  picker.querySelector(".p-name").textContent = project.name || t("common.project");
  picker.querySelector(".project-dot")?.classList.toggle("demo", Boolean(project.demo));
}

function endSwitching() {
  document.body.classList.remove("project-switching");
  topbar.querySelector(".project-picker")?.removeAttribute("aria-busy");
}

// ------------------------------------------------------------ review badges
// The project selector shows a dot while another project has Needs you
// cards new for the person (GET /api/projects review_new; the cards in the
// picker show the counts).  The list is read again when the page comes
// back into view and every few minutes while it is shown.
const PROJECTS_REFRESH_MS = 3 * 60 * 1000;
let projectsReadAt = 0;

function syncReviewDot(picker = topbar.querySelector(".project-picker")) {
  if (!picker) return;
  const n = otherProjectsNew(S.projects, S.project?.id);
  let dot = picker.querySelector(".project-new-dot");
  if (!dot && n) {
    dot = h("span", { class: "project-new-dot", "aria-hidden": "true" });
    picker.querySelector(".p-name")?.after(dot);
  }
  if (dot) dot.hidden = !n;
  picker.dataset.reviewNew = String(n);
  const name = S.project?.name || t("common.project");
  const note = n ? t("app.menu.projects_new", { n }) : "";
  picker.title = [S.project?.path || name, note].filter(Boolean).join("\n");
  picker.setAttribute("aria-label", [`${t("app.menu.projects")}: ${name}`, note].filter(Boolean).join(". "));
}

function refreshProjectList({ force = false } = {}) {
  if (!S.booted || document.hidden || (!force && Date.now() - projectsReadAt < 30000)) return;
  projectsReadAt = Date.now();
  api.projects().then((list) => {
    if (!Array.isArray(list?.projects)) return;
    S.projects = list.projects;
    syncReviewDot();
  }).catch(() => { /* keep the known list */ });
}

document.addEventListener("visibilitychange", () => { if (!document.hidden) refreshProjectList(); });
setInterval(() => refreshProjectList({ force: true }), PROJECTS_REFRESH_MS);
// POST /api/needs/seen answered with this project's new counts (needs-ui.js).
window.addEventListener(PROJECTS_LISTED, () => syncReviewDot());
window.addEventListener(REVIEW_SEEN, (event) => {
  const { project, counts } = event.detail || {};
  if (!project) return;
  S.projects = withReviewBadge(S.projects, project, counts);
  syncReviewDot();
});

// ------------------------------------------------------------ project state
// Each project's page as the person left it (project-state-model.js): saved
// on a switch, when the page is hidden and, debounced, while they work;
// restored when they switch back (and, in the desktop app, when a page of
// this server opens without a route: the app moving between servers).
const projectStates = createProjectStateStore({
  storage: (() => { try { return localStorage; } catch { return null; } })(),
  user: () => S.session?.user || "",
});
let restoring = null;       // { project } while a restore finishes (no saves meanwhile)
let saveTimer = 0;
const scrolled = new Set(); // elements the person scrolled (scroll positions to keep)

function captureProjectState() {
  if (!S.booted || !S.project?.id || showingPairGate) return null;
  const body = S.drawerOpen && S.detail ? document.querySelector(".drawer:not(.inspector-empty) .drawer-body") : null;
  const scrolls = [];
  for (const el of scrolled) {
    if (!el.isConnected || !appRoot.contains(el)) { scrolled.delete(el); continue; }
    if (!(el.scrollTop > 0) || scrolls.length >= 6) continue;
    const sel = scrollSelector(el.tagName, el.classList);
    if (!sel) continue;
    const index = [...appRoot.querySelectorAll(sel)].indexOf(el);
    if (index >= 0) scrolls.push({ sel, index, top: el.scrollTop });
  }
  let graphLayout = "";
  try { graphLayout = localStorage.getItem("ark.graphLayout") || ""; } catch { /* storage disabled */ }
  return {
    v: 1,
    route: restorableRoute(location.hash),
    view: S.view,
    graphLayout,
    unit: S.drawerOpen && S.detail && !S.multi
      ? { ref: S.detail.uid || S.detail.id, floating: Boolean(S.drawerFloating), scroll: body?.scrollTop || 0 } : null,
    sessions: {
      selected: S.selectedSessionId || "", query: S.sessionQuery || "", filter: S.sessionFilter || "",
      groupBy: S.sessionGroupBy || "", railTab: S.sessionRailTab || "", threadMode: S.sessionThreadMode || "",
    },
    cameras: projectCameras(graphCameraEntries(), S.project.id),
    scrollY: window.scrollY || 0,
    scrolls,
  };
}

function saveProjectState() {
  clearTimeout(saveTimer);
  if (restoring || document.body.classList.contains("project-switching")) return;
  const snapshot = captureProjectState();
  if (snapshot) projectStates.put(S.project.id, snapshot);
}

function saveProjectStateSoon() {
  clearTimeout(saveTimer);
  saveTimer = setTimeout(saveProjectState, 1500);
}

document.addEventListener("scroll", (event) => {
  if (event.target instanceof Element && appRoot.contains(event.target)) scrolled.add(event.target);
  saveProjectStateSoon();
}, { capture: true, passive: true });
window.addEventListener("ark:selection", saveProjectStateSoon);
window.addEventListener("pagehide", saveProjectState);
document.addEventListener("visibilitychange", () => { if (document.hidden) saveProjectState(); });

// Lightweight state first (route, view, cameras, session list), so the page
// paints at once; the inspector and the scroll positions follow it.
function prepareRestore(plan) {
  if (plan.graphLayout) {
    try { localStorage.setItem("ark.graphLayout", plan.graphLayout); } catch { /* storage disabled */ }
  }
  seedGraphCameras(plan.cameras);
  Object.assign(S, sessionPatch(plan.sessions));
}

function applyRestorePlan(plan, { push = false } = {}) {
  const project = S.project?.id;
  restoring = { project };
  prepareRestore(plan);
  history[push ? "pushState" : "replaceState"](null, "", plan.route);
  const route = parseHash();
  if (route.tab === "editor") S.editorKey = route.editorKey || "";
  setTab(route.tab, route.sub);
  if (route.topicRef) S.topicRef = route.topicRef;
  S.workspace = route.workspace || workspaceForTab(route.tab);
  if (route.taskView) S.taskView = route.taskView;
  if (route.reviewView) S.reviewView = route.reviewView;
  if (plan.view) S.view = plan.view;
  renderRoute().then(() => finishRestore(project, plan), () => { restoring = null; });
}

async function finishRestore(project, plan) {
  const current = () => S.project?.id === project && restoring?.project === project;
  try {
    if (!current()) return;
    if (plan.unit) {
      // A unit deleted meanwhile just stays closed (quiet: no error toast).
      await ctx.openUnit(plan.unit.ref, null, { quiet: true, forceFloating: plan.unit.floating || undefined });
      if (!current()) return;
      if (S.detail?.id != null) for (const graph of graphs) if (graph.byId?.has(S.detail.id)) graph.setSelected(S.detail.id);
    }
    // Lists fill in as their data arrives: try a few times.
    for (const wait of [0, 200, 800]) {
      await new Promise((resolve) => setTimeout(resolve, wait));
      if (!current()) return;
      if (plan.scrollY && Math.abs(window.scrollY - plan.scrollY) > 2) window.scrollTo({ top: plan.scrollY });
      for (const item of plan.scrolls || []) {
        const el = appRoot.querySelectorAll(item.sel)[item.index];
        if (el && Math.abs(el.scrollTop - item.top) > 2) el.scrollTop = item.top;
      }
      const body = plan.unit?.scroll ? document.querySelector(".drawer:not(.inspector-empty) .drawer-body") : null;
      if (body && Math.abs(body.scrollTop - plan.unit.scroll) > 2) body.scrollTop = plan.unit.scroll;
    }
  } finally {
    if (restoring?.project === project) restoring = null;
  }
}

function applyProject(data) {
  if (data.summary) S.project = { ...S.project, ...data.summary };
  applyEffectiveAppearance();
  S.stats = data.stats || S.stats;
  S.activity = data.activity || S.activity;
  S.nextActions = data.next_actions || S.nextActions;
  S.needsYou = data.needs_you || S.needsYou;
  refreshAssigned(ctx);
  syncTopbar();
  renderRoute({ keepScroll: true });
}

// Mind card widths and pinned images: per project for this person.
const mindNodeStore = createMindNodeStore({
  projectId: () => S.project?.id,
  loadEntry: mindLayoutEntry,
  saveEntry: saveMindLayoutEntry,
  storage: (() => { try { return localStorage; } catch { return null; } })(),
});
setMindNodeStore(mindNodeStore);
window.addEventListener("pagehide", () => mindNodeStore.flush());
// A fresher account profile (profile.js refreshAccountPrefs) may carry other
// Mind card widths and pins than the cached one: the next graph data uses it.
window.addEventListener(PROFILE_REFRESHED, () => mindNodeStore.reset());

// Project palette, graph physics, line ends and graph display settings,
// with this person's overrides on top.
function applyEffectiveAppearance() {
  const effective = resolveAppearance(S.project?.settings, appearanceOverrides());
  setActiveGraphPhysics(effective.graph);
  setActiveGraphArrows(effective.arrows);
  const before = getMindSettings();
  const after = setActiveGraphDisplay(effective.graph_display.values);
  // Agent presence is an overlay (graph-presence.js): toggling it redraws
  // no graph and relayouts nothing.
  const drawn = ({ agentPresence: _presence, ...rest }) => JSON.stringify(rest);
  if (drawn(before) !== drawn(after)) {
    // Alignment, auto layout and images change the Mind layout itself.
    for (const graph of graphs) {
      if (graph.layout === "mind") graph._applyLayout?.(false);
      else graph._draw?.();
    }
    window.dispatchEvent(new CustomEvent("ark:graph-display", { detail: after }));
  }
  if (before.agentPresence !== after.agentPresence) setPresenceEnabled(after.agentPresence);
  // The project's marker groups (Settings › Appearance › Node markers).
  if (setMarkerGroups(S.project?.settings?.markers)) for (const graph of graphs) graph._draw?.();
  applyProjectPalette(effective.palette);
}

window.addEventListener(APPEARANCE_CHANGED, () => {
  applyEffectiveAppearance();
  for (const graph of graphs) graph._draw?.();
});

function applySettings(data) {
  if (data.summary) S.project = { ...S.project, ...data.summary };
  if (data.settings) S.project = { ...S.project, settings: data.settings };
  if (data.updated_at) S.project = { ...S.project, updated: data.updated_at };
  applyEffectiveAppearance();
  renderTopbar();
}

function offerReopen() {
  const target = reopenTarget(startGuard.previousUrl, location.href);
  toast(t(startGuard.reason === "param" ? "app.safe_start.param" : "app.safe_start.recovered"), "", {
    duration: 15000,
    action: target ? { label: t("app.safe_start.reopen"), onClick: () => location.assign(target) } : null,
  });
}

// -------------------------------------------------------------------- route
function parseHash() {
  const raw = (location.hash || "").replace(/^#\/?/, "").trim();
  if (!raw) return { tab: "overview", sub: "story", workspace: "home" };
  const parts = raw.split("/");
  const workspace = parts[0];
  if (workspace === "home") {
    return { tab: "overview", sub: "story", workspace: "home" };
  }
  if (workspace === "research") {
    const view = parts[1] || "story";
    if (view === "idea" || view === "capture") return { tab: "ideas", sub: "story", workspace };
    if (view === "graph") return { tab: "map", sub: "story", workspace: "map" };
    if (view === "review") return { tab: "review", sub: "story", workspace, reviewView: reviewView(parts[2]) };
    const stage = PLAN_SUBS.includes(view) ? view : "story";
    if (stage === "topic" && parts[2]) {
      return { tab: "plan", sub: stage, workspace, topicRef: decodeURIComponent(parts[2]) };
    }
    return { tab: "plan", sub: stage, workspace };
  }
  if (workspace === "map") return { tab: "map", sub: "story", workspace: "map" };
  if (workspace === "execute") {
    if (parts[1] === "files") return { tab: "editor", sub: "story", workspace: "editor", editorKey: "" };
    const view = EXECUTE_VIEW_ALIASES[parts[1]] || "board";
    return { tab: "tasks", sub: "story", taskView: view, workspace };
  }
  if (workspace === "output") {
    return { tab: parts[1] === "distribution" ? "distribution" : "production", sub: "story", workspace };
  }
  if (workspace === "assistant") return { tab: "chat", sub: "story", workspace };
  if (workspace === "settings") return { tab: "settings", sub: "story", workspace };
  if (workspace === "editor") {
    return { tab: "editor", sub: "story", workspace, editorKey: keyFromRoute(parts.slice(1).join("/")) };
  }
  const known = ["overview", "ideas", "plan", "map", "tasks", "production", "distribution", "chat", "settings"];
  if (!known.includes(workspace)) {
    return { tab: "overview", sub: "story", workspace: "home" };
  }
  const sub = workspace === "plan" && PLAN_SUBS.includes(parts[1]) ? parts[1] : "story";
  return { tab: workspace, sub, workspace: workspaceForTab(workspace) };
}

let pairClaimBusy = false;

// <server>/#/join/<token>: an invite link opened while signed in (signed
// out, the login page shows it first and keeps the hash through sign-in).
let joinBusy = false;
function maybeOpenJoin() {
  const token = parseJoinHash(location.hash);
  if (!token) return false;
  if (joinBusy) return true;
  joinBusy = true;
  // The token leaves the address bar and the history at once.
  try { history.replaceState(null, "", location.pathname + location.search + "#/home"); } catch { /* ignore */ }
  openJoinDialog(token, {
    onJoined: async (project, result) => {
      if (result && result.already_member === false) toast(t("join.joined", { project: project.name || "" }), "ok");
      api.projects().then((list) => { if (Array.isArray(list?.projects)) S.projects = list.projects; }).catch(() => {});
      await switchProject({ id: project.id, name: project.name || "" });
    },
    onDone: () => { joinBusy = false; },
  });
  return true;
}

async function onHashChange() {
  if (maybeOpenJoin()) return;
  // A QR scanned while this PWA/WebView is already open changes only the hash.
  // Claim it here too; otherwise the old (possibly read-only) mobile token
  // would survive and a newly scanned full-access QR would appear ignored.
  const pairToken = parsePairHash(location.hash);
  if (pairToken && !pairClaimBusy) {
    pairClaimBusy = true;
    try {
      await claimPairCode(pairToken, null);
    } finally {
      pairClaimBusy = false;
    }
    return;
  }
  const route = parseHash();
  if (route.tab === "editor" && S.tab === "editor") {
    if ((route.editorKey || "") !== (S.editorKey || "")) {
      S.editorKey = route.editorKey || "";
      renderCurrentOnly();
    }
    return;
  }
  if (route.tab === "editor") S.editorKey = route.editorKey || "";
  // Back/forward between topics stays inside the Topics page.
  if (route.tab === "plan" && S.tab === "plan" && ctx.topicPage?.mounted() && ctx.topicPage.openRoute(route)) return;
  if (route.topicRef && route.topicRef !== S.topicRef) {
    S.topicRef = route.topicRef;
    if (S.tab === "plan" && S.sub === "topic") {
      renderCurrentOnly();
      return;
    }
  }
  if (route.tab === S.tab
      && (route.tab !== "plan" || route.sub === S.sub)
      && (!route.taskView || route.taskView === S.taskView)
      && (!route.reviewView || route.reviewView === reviewView(S.reviewView))
      && (route.workspace || workspaceForTab(route.tab)) === S.workspace) return;
  setTab(route.tab, route.sub);
  S.workspace = route.workspace || workspaceForTab(route.tab);
  if (route.taskView) S.taskView = route.taskView;
  if (route.reviewView) S.reviewView = route.reviewView;
  applyViewQuery();
  applyFilterTabQuery();
  renderRoute();
}

// `filters` (optional) replaces the target tab's filters, e.g. Home's
// "View history" opens the Map filtered to every needs: tag.
// Open the Map's graph with `uid` shown (even when the filters hide it),
// selected, centred and open in the inspector.  The Map view loads lazily
// the first time; an open Map graph is updated in place.
async function locateInMap(ref) {
  const uid = String(ref ?? "").trim();
  if (!uid || !S.project) return;
  // The command palette and dialogs give way to the Map.
  paletteEl?.closePalette?.();
  document.getElementById("overlay-root")?.querySelectorAll(".modal-backdrop").forEach((el) => el.remove());
  S.mapReveal = revealList(uid);
  S.mapFocus = uid;
  const graph = graphs.find((item) => item.opts?.mapGraph && item.host?.isConnected);
  if (S.tab === "map" && S.view === "graph" && graph) {
    await applyFiltersIncremental({ silent: true, repaintRail: false, fit: false });
    focusLocated(ctx, graph, uid);
    return;
  }
  if (S.tab === "map") ctx.setView("graph");
  else navigate("map");
}

function navigate(tab, sub = null, filters = null) {
  if (tab === "plan" && !PLAN_SUBS.includes(sub)) sub = S.sub || "story";
  setTab(tab, sub);
  if (filters) {
    S.filters = { ...emptyFilters(), ...filters };
    persistFilters();
  }
  S.workspace = workspaceForTab(tab);
  const hash = workspaceHashForRoute(tab, sub);
  if (location.hash !== hash) history.pushState(null, "", hash);
  window.scrollTo({ top: 0, behavior: "smooth" });
  renderRoute();
}

function renderWorkspaceBar(host) {
  const workspace = workspaceForTab(S.tab);
  let options = [];
  let onSelect = null;
  if (workspace === "research") {
    options = [["capture", t("stage.capture")], ["plan", t("nav.plan")], ["graph", t("view.graph")]];
    onSelect = (value) => navigateWorkspace("research", value);
  } else if (workspace === "output") {
    options = [["assets", t("production.assets")], ["distribution", t("nav.distribution")]];
    onSelect = (value) => navigateWorkspace("output", value);
  }
  if (!options.length) return;
  const active = workspace === "research"
    ? (S.tab === "plan" ? "plan" : S.tab === "map" ? "graph" : "capture")
    : (S.tab === "distribution" ? "distribution" : "assets");
  const nav = h("nav", { class: "workspace-subnav", "aria-label": t("app.workspace_views", { name: workspaceLabel(workspace) }) },
    options.map(([id, label]) => h("button", {
      class: `workspace-view ${active === id ? "on" : ""}`.trim(),
      type: "button", "data-workspace-view": id,
      "aria-current": active === id ? "page" : undefined,
      onclick: () => onSelect(id),
    }, label)));
  host.appendChild(h("div", { class: "workspace-bar" },
    h("span", { class: "workspace-kicker" }, workspaceLabel(workspace)),
    nav));
}

function paintFiltersNow() {
  persistFilters();
  const rail = document.querySelector(".rail");
  if (!rail) return;
  const top = rail.scrollTop;
  renderFilterRail(rail, ctx);
  requestAnimationFrame(() => { rail.scrollTop = top; });
}

async function renderRoute({ keepScroll = false, preloaded = null } = {}) {
  if (!S.project) {
    renderNoProjectState();
    return;
  }
  document.body.dataset.tab = S.tab;
  if (!filtersActive(S.filters)) {
    S.graphBase = null;
    S.unitsBase = null;
  }
  const scrollY = window.scrollY;
  const seq = ++loadSeq;
  preloadView(S.tab);
  destroyVisuals();
  if (topbar.dataset.workspace !== workspaceForTab(S.tab)
      || topbar.dataset.tab !== S.tab) renderTopbar();
  syncTopbar();
  const filterTab = ["ideas", "plan", "map"].includes(S.tab);
  // Workbench shell (desktop): the filter rail is the "Filters" view of
  // whichever sidebar/panel holds it; the page keeps a single column.
  const shellRail = shellActive() && filterTab;
  const hasRail = !shellActive() && filterTab && S.filtersVisible !== false;
  document.body.classList.toggle("rail-hidden", !hasRail);
  clear(appRoot);
  const filtersHost = viewHost("filters");
  if (filtersHost) clear(filtersHost);
  const content = h("div", { class: "content" });
  let rail = null;
  if (shellRail) {
    const infoMode = S.tab === "ideas" && S.filterTab === "infohub";
    rail = h("aside", { class: `rail ${infoMode ? "info-rail" : ""}`.trim() });
    filtersHost.appendChild(rail);
    appRoot.appendChild(h("div", { class: "workspace no-rail" }, content));
    renderFilterRail(rail, ctx);
  } else if (hasRail) {
    const infoMode = S.tab === "ideas" && S.filterTab === "infohub";
    rail = h("aside", { class: `rail ${S.railOpen ? "open" : ""} ${infoMode ? "info-rail" : ""}`.trim() });
    const workspace = h("div", { class: `workspace ${infoMode ? "info-mode" : ""}`.trim() }, rail, content);
    const railResizer = h("div", { class: "rail-resizer", title: t("app.resize_filters") });
    railResizer.addEventListener("pointerdown", (e) => {
      e.preventDefault();
      const startX = e.clientX;
      const startW = S.railWidth;
      const move = (ev) => {
        S.railWidth = Math.round(Math.max(220, Math.min(470, startW + ev.clientX - startX)));
        document.documentElement.style.setProperty("--rail-w", `${S.railWidth}px`);
      };
      const up = () => {
        localStorage.setItem("ark.railWidth", String(S.railWidth));
        window.removeEventListener("pointermove", move);
        window.removeEventListener("pointerup", up);
      };
      window.addEventListener("pointermove", move);
      window.addEventListener("pointerup", up);
    });
    workspace.appendChild(railResizer);
    appRoot.appendChild(workspace);
    renderFilterRail(rail, ctx);
    if (S.railOpen) {
      const backdrop = h("div", { class: "rail-backdrop", onclick: () => { S.railOpen = false; rail.classList.remove("open"); backdrop.remove(); } });
      appRoot.appendChild(backdrop);
    }
  } else {
    appRoot.appendChild(content);
  }
  renderWorkbench();
  if (!preloaded) content.appendChild(h("div", { class: "loading", style: { paddingTop: "12vh" } }, h("span", { class: "spinner" }), t("action.loading")));
  try {
    const data = preloaded || await loadRouteData();
    if (seq !== loadSeq) return;
    Object.assign(S, data);
    if (S.tab === "overview" && data.project) S.project = { ...S.project, ...data.project };
    syncTopbar();
    // Do not rebuild the topbar again after the async route load: doing so can
    // replace a tab button between pointerdown and click (lost navigation) and
    // makes the active tab visibly flicker. The topbar was already rendered at
    // the start of this route change.
    if (rail) renderFilterRail(rail, ctx);
    clear(content);
    const viewHost = h("div", { class: "workspace-view-host" });
    content.appendChild(viewHost);
    await renderCurrentView(viewHost);
    if (seq !== loadSeq) return;
    showInspectorPlaceholder(ctx);
    // Selection-following views (Local graph) re-read the route's focus.
    window.dispatchEvent(new CustomEvent("ark:selection"));
    if (!keepScroll) window.scrollTo({ top: 0 });
    else window.scrollTo({ top: scrollY });
    document.title = `ARK Studio · ${S.project?.name || "Studio"} · ${t(`nav.${S.tab === "plan" ? S.sub : S.tab}`)}`;
  } catch (err) {
    if (seq !== loadSeq) return;
    clear(content);
    content.appendChild(h("div", { class: "page" }, h("div", { class: "empty", style: { marginTop: "8vh" } },
      h("b", null, t("error.load")),
      h("div", null, err.message),
      h("button", { class: "btn primary", style: { marginTop: "10px" }, onclick: () => renderRoute() }, t("action.retry")))));
  }
}

function renderCurrentOnly() {
  destroyVisuals();
  syncTopbar();
  const content = appRoot.querySelector(".content");
  if (!content) { renderRoute({ keepScroll: true }); return; }
  clear(content);
  const viewHost = h("div", { class: "workspace-view-host" });
  content.appendChild(viewHost);
  renderCurrentView(viewHost);
  showInspectorPlaceholder(ctx);
  renderWorkbench();
}

function renderCurrentView(host) {
  switch (S.tab) {
    case "overview": return renderOverview(host, ctx);
    case "ideas": return lazyView(host, VIEW_MODULES.ideas, "renderIdeas");
    case "plan": return lazyView(host, VIEW_MODULES.plan, "renderPlan");
    case "review": return lazyView(host, VIEW_MODULES.review, "renderReview");
    case "map": return lazyView(host, VIEW_MODULES.map, "renderMap");
    case "tasks": return lazyView(host, loadTasks, "renderTasks");
    case "production": return lazyView(host, () => import("./views/production.js"), "renderProduction");
    case "distribution": return lazyView(host, () => import("./views/distribution.js"), "renderDistribution");
    case "settings": return lazyView(host, () => import("./views/settings.js"), "renderSettings");
    case "chat": return lazyView(host, () => import("./views/chat.js"), "renderChat");
    case "editor": return renderEditor(host, ctx);
    default: return renderOverview(host, ctx);
  }
}

// Views outside the first-screen module graph.  renderRoute starts the
// module download together with the route's data.
const VIEW_MODULES = {
  ideas: () => import("./views/ideas.js"),
  plan: () => import("./views/plan.js"),
  review: () => import("./views/review.js"),
  map: () => import("./views/map.js"),
  tasks: loadTasks,
  production: () => import("./views/production.js"),
  distribution: () => import("./views/distribution.js"),
  settings: () => import("./views/settings.js"),
  chat: () => import("./views/chat.js"),
  editor: loadEditor,
};

// After the first screen, at idle: the code of the other workspaces, so
// later switches do not wait for it (skipped on data-saver / 2g links).
function prefetchViewModules() {
  if (connectionIsSlow()) return;
  idle(() => {
    for (const tab of ["map", "plan", "ideas", "tasks", "editor"]) preloadView(tab);
  }, 6000);
}

function preloadView(tab) {
  VIEW_MODULES[tab]?.().catch(() => { /* lazyView shows the retry state */ });
}

async function lazyView(host, load, name) {
  const tab = S.tab;
  clear(host);
  host.appendChild(h("div", { class: "loading" }, t("action.loading")));
  try {
    const view = await load();
    if (!host.isConnected || S.tab !== tab) return;
    clear(host);
    return await view[name](host, ctx);
  } catch (error) {
    if (!host.isConnected) return;
    clear(host);
    host.appendChild(h("div", { class: "empty" }, error.message,
      h("button", { class: "btn", onclick: () => lazyView(host, load, name) }, t("action.retry"))));
  }
}

// Graph filtering over the (lite) snapshot; text queries load node bodies first.
async function graphIndexFor(snapshot, { needBodies = Boolean(S.filters.q) } = {}) {
  const index = new GraphFilterIndex(snapshot);
  if (!index.hasBodies) {
    const attach = () => api.graphBodies().then((bodies) => index.attachBodies(bodies));
    if (needBodies) await attach().catch(() => {});
    else if (!connectionIsSlow()) idle(() => attach().catch(() => {}), 5000);
  }
  return index;
}

// GET /api/bootstrap contains the GET /api/project payload; reuse it instead
// of a second request.  Live sync refreshes the bootstrap after changes.
let bootstrapProject = null;
// The bootstrap reply doubles as the first Home load; after a few seconds
// (or for a fresh read) Home asks GET /api/project again, so its counts and
// getting-started checklist follow this tab's own writes.
const BOOTSTRAP_REUSE_MS = 5000;

function projectFromBootstrap() {
  if (!bootstrapProject) return null;
  if (bootstrapProject.data.summary?.id !== S.project?.id) return null;
  if (Date.now() - bootstrapProject.at > BOOTSTRAP_REUSE_MS) return null;
  return bootstrapProject.data;
}

async function projectData(opts) {
  return (!opts?.fresh && projectFromBootstrap()) || api.project(opts);
}

async function loadRouteData({ fresh = false } = {}) {
  const opts = fresh ? { fresh: true } : {};
  if (S.tab === "plan") {
    // The open topic supplies its members and facets; project-wide results
    // must never replace a topic's curated subgraph when filters change.
    return { units: [], total: 0, graphIndex: null, facets: {} };
  }
  if (S.tab === "chat" || S.tab === "editor") {
    return { units: [], total: 0 };
  }
  if (S.tab === "review") {
    const project = await projectData(opts);
    return { needsYou: project.needs_you || [], units: [], total: 0 };
  }
  if (S.tab === "settings") {
    const project = await projectData(opts);
    return {
      stats: project.stats,
      activity: project.activity,
      nextActions: project.next_actions,
      needsYou: project.needs_you || [],
      project: project.summary,
      units: [],
      total: 0,
    };
  }
  if (S.tab === "overview") {
    // One round trip for the dashboard (GET /api/project/overview); an
    // older server without it still gets Needs you and the header.
    const [project, overview] = await Promise.all([
      projectData(opts),
      api.overview(opts, homeFilterQuery(S.project?.id)).catch(() => null),
    ]);
    return {
      stats: project.stats,
      activity: overview?.activity || project.activity || [],
      nextActions: project.next_actions,
      needsYou: project.needs_you || [],
      project: project.summary,
      overview,
      units: [],
      total: 0,
    };
  }
  const base = baseTypes(S.tab, S.sub);
  if (S.tab === "map" && S.view === "list") {
    const query = currentGraphQuery();
    const [snapshot, metadata] = await Promise.all([
      api.graphSnapshot(opts), api.units({ ...query, limit: 1, facets: 1 }, opts),
    ]);
    const graphIndex = await graphIndexFor(snapshot);
    const data = graphIndex.filter(query, S.session?.user || "", { all: true });
    return { units: data.nodes, total: data.total, facets: metadata.facets, graphIndex,
      graph: { nodes: [], edges: [], context_count: 0 } };
  }
  const params = filtersToParams(S.filters, {
    types: S.filters.types.length ? S.filters.types : base,
    limit: 600,
    sort: S.sort || (S.tab === "tasks" ? "importance" : "updated"),
    facets: 1,
  });
  if (S.tab === "ideas" && !S.filters.types.length) params.origins = "infohub";
  const needsGraph = (S.tab === "ideas" && S.view === "graph")
    || (S.tab === "map" && S.view === "graph")
    || (S.tab === "plan" && (S.view === "graph" || S.view === "split"));
  const graphQuery = currentGraphQuery();
  let graphIndex = null;
  const [unitsData, graphData, sessionData] = await Promise.all([
    api.units({ ...params, slim: S.tab === "production" ? 0 : 1 }, opts),
    needsGraph ? api.graphSnapshot(opts).then(async (snapshot) => {
      graphIndex = await graphIndexFor(snapshot);
      return graphIndex.filter(withMapReveal(graphQuery), S.session?.user || "");
    }).catch(() => api.graph(graphQuery)) : Promise.resolve(null),
    S.tab === "tasks"
      ? api.agentSessions().catch(() => ({ sessions: [] }))
      : Promise.resolve(null),
  ]);
  return {
    units: unitsData.items,
    total: unitsData.total,
    facets: unitsData.facets,
    graphIndex,
    graph: graphData ? { nodes: graphData.nodes, edges: graphData.edges, context_count: graphData.context_count } : { nodes: [], edges: [], context_count: 0 },
    ...(sessionData ? { taskSessions: sessionData.sessions || [], sessionsLoaded: true } : {}),
  };
}

let filterRequestSeq = 0;
let filterFacetController = null;
let filterFacetTimer = null;

// A node located on the Map ("Locate in research map") stays shown while
// the filters would hide it (S.mapReveal, cleared on leaving the Map).
function withMapReveal(query) {
  return S.tab === "map" && S.mapReveal?.length ? { ...query, reveal: S.mapReveal } : query;
}

function currentGraphQuery() {
  const base = baseTypes(S.tab, S.sub);
  const params = filtersToParams(S.filters, {
    types: S.filters.types.length ? S.filters.types : base,
    limit: 600,
    sort: S.sort || (S.tab === "tasks" ? "importance" : "updated"),
    facets: 1,
  });
  if (S.tab === "ideas" && !S.filters.types.length) params.origins = "infohub";
  const planType = { story: "story", related: "related_work", method: "method", experiment: "experiment", topic: "topic" }[S.sub];
  let graphTypes = S.tab === "plan" && planType ? [planType] : (S.filters.types.length ? S.filters.types : base);
  let graphContext = Boolean(S.filters.context);
  if (S.tab === "plan" && S.sub === "story") {
    graphTypes = ["story", "claim", "experiment"];
    graphContext = false;
  }
  return {
    ...params,
    types: graphTypes,
    limit: S.graphLimit,
    context: graphContext ? 1 : 0,
    sort: S.sort || "updated",
  };
}

function ensureUnitsBase() {
  const key = `${S.project?.id || ""}:${S.tab}:${S.sub || ""}`;
  if (!S.unitsBase || S.unitsBaseKey !== key) {
    S.unitsBase = [...(S.units || [])];
    S.unitsBaseKey = key;
  }
  return S.unitsBase;
}

function ensureGraphBase() {
  const key = `${S.project?.id || ""}:${S.tab}:${S.sub || ""}`;
  if (!S.graphBase || S.graphBaseKey !== key) {
    S.graphBase = {
      nodes: [...(S.graph?.nodes || [])],
      edges: [...(S.graph?.edges || [])],
    };
    S.graphBaseKey = key;
  }
  return S.graphBase;
}

function locallyFilterGraph(base, filters) {
  const nodes = base.nodes || [];
  const selectedTypes = filters.typesNone
    ? []
    : (filters.types?.length ? filters.types : null);
  const statuses = filters.statusesNone
    ? new Set(["__none__"])
    : (filters.statuses?.length ? new Set(filters.statuses) : null);
  const importance = filters.importanceNone
    ? new Set(["__none__"])
    : (filters.importanceLevels?.length ? new Set(filters.importanceLevels) : null);
  const tagGroups = new Map();
  for (const tag of filters.tags || []) {
    const index = String(tag).indexOf(":");
    const ns = index >= 0 ? tag.slice(0, index) : "tag";
    if (!tagGroups.has(ns)) tagGroups.set(ns, new Set());
    tagGroups.get(ns).add(tag);
  }
  const unsetGroups = new Set(filters.tagsUnset || []);
  const query = String(filters.q || "").trim().toLowerCase();
  const since = filters.timeMode === "recent"
    ? new Date(Date.now() - (Number(filters.recentDays) || 7) * 86400000).toISOString()
    : "";

  const matches = (node) => {
    if (selectedTypes && !selectedTypes.includes(node.type)) return false;
    if (statuses && !statuses.has(node.status)) return false;
    if (importance && !importance.has(node.importance)) return false;
    if (query) {
      const haystack = `${node.title || ""} ${node.uid || ""} ${node.body || node.excerpt || ""}`.toLowerCase();
      if (!haystack.includes(query)) return false;
    }
    if (filters.roots && (node.parents || []).length) return false;
    if (!assigneeMatches(node, filters.assignees, S.session?.user || "")) return false;
    if (filters.timeMode === "timeline") {
      const from = Date.parse(filters.from || "");
      const to = Date.parse(filters.to || "");
      const start = Date.parse(node.start_at || "");
      const end = Date.parse(node.end_at || "");
      if (Number.isFinite(from) || Number.isFinite(to)) {
        // A node is selected when either endpoint falls inside the interval.
        // This is endpoint membership, not whole-span overlap, so the filter
        // follows the two handles exactly.
        const inRange = (value) => Number.isFinite(value)
          && (!Number.isFinite(from) || value >= from)
          && (!Number.isFinite(to) || value <= to);
        if (!inRange(start) && !inRange(end)) return false;
      }
    }
    if (since) {
      const created = node.created_at || node.created || "";
      const at = Date.parse(created);
      if (!Number.isFinite(at) || at < Date.parse(since)) return false;
    }
    if (filters.timeMode === "overdue"
        && !(node.end_at && node.end_at < new Date().toISOString().slice(0, 10) && !["done", "abandoned", "archived"].includes(node.status))) return false;
    if (!filters.context && node.context) return false;
    if (tagGroups.size || unsetGroups.size) {
      const nodeTags = new Set(node.tags || []);
      const namespaces = new Set([...tagGroups.keys(), ...unsetGroups]);
      for (const ns of namespaces) {
        const values = tagGroups.get(ns);
        const hasValue = values && node.tags?.some((tag) => values.has(tag));
        const noValue = !node.tags?.some((tag) => String(tag).startsWith(`${ns}:`));
        if (!hasValue && !(unsetGroups.has(ns) && noValue)) return false;
      }
    }
    return true;
  };

  let visible = new Set(nodes.filter(matches).map((node) => node.id));
  if (filters.context && visible.size) {
    for (const edge of base.edges || []) {
      if (visible.has(edge.source)) visible.add(edge.target);
      if (visible.has(edge.target)) visible.add(edge.source);
    }
  }
  const outNodes = nodes.filter((node) => visible.has(node.id));
  const outEdges = (base.edges || []).filter((edge) => visible.has(edge.source) && visible.has(edge.target));
  return { nodes: outNodes, edges: outEdges };
}

// `remote`: data from another device/user: keep the camera and positions
// (GraphView.setData's remote mode); never fit.
function paintGraphData(data, { preserveCamera = true, animate = false, fit = false, warm = !animate, remote = false } = {}) {
  const targets = graphs.filter((graph) => graph.clipboardSource !== "infohub");
  for (const graph of targets) {
    const selected = [...graph.selectedNodes][0] || null;
    graph.setData(data.nodes, data.edges, {
      selected: selected && data.nodes.some((node) => node.id === selected) ? selected : null,
      layout: graph.layout,
      preserveCamera: preserveCamera || remote,
      animate,
      warm,
      remote,
    });
    if (fit && !remote && graph.nodes?.length) graph.fit({ animate: true });
  }
  syncRevealNotice(ctx);
  return targets.length;
}

function graphShapeKey(nodes, edges) {
  const nodeIds = (nodes || []).map((node) => String(node.id)).sort().join(",");
  const edgeIds = (edges || []).map((edge) => `${edge.source}>${edge.target}:${edge.relation || ""}`).sort().join(",");
  return `${nodeIds}|${edgeIds}`;
}

// A filter change refits the view (`fit`, default with the rail); a remote
// update (`remote`) keeps the camera and positions.
async function applyFiltersIncremental({ silent = false, repaintRail = true, remote = false, fit = repaintRail && !remote } = {}) {
  const seq = ++filterRequestSeq;
  clearTimeout(filterFacetTimer);
  filterFacetController?.abort();
  if (S.tab === "plan" && ctx.topicPage?.mounted()) {
    await ctx.topicPage.applyFilters({ repaintRail, remote });
    return;
  }
  const graphView = S.view === "graph" || S.view === "split";
  const targets = graphs.filter((graph) => graph.clipboardSource !== "infohub");
  const graphTarget = graphView && ["ideas", "map", "plan"].includes(S.tab) && targets.length;
  if (S.graphIndex && !S.graphIndex.hasBodies && S.filters.q) {
    await api.graphBodies().then((bodies) => S.graphIndex?.attachBodies(bodies)).catch(() => {});
    if (seq !== filterRequestSeq) return;
  }
  if (S.tab === "map" && S.view === "list" && S.graphIndex) {
    const data = S.graphIndex.filter(currentGraphQuery(), S.session?.user || "", { all: true });
    S.units = data.nodes;
    S.total = data.total;
    renderCurrentOnly();
    return;
  }
  if (!graphTarget) {
    // Lists/tables filter against the already-loaded unit set immediately.
    // No server round trip is needed to repaint a local list; the backend is
    // reconciled on the next full route load.
    const local = locallyFilterGraph({ nodes: ensureUnitsBase(), edges: [] }, S.filters).nodes;
    S.units = local;
    S.total = local.length;
    renderCurrentOnly();
    return;
  }
  if (S.graphIndex) {
    const query = currentGraphQuery();
    const data = S.graphIndex.filter(withMapReveal(query), S.session?.user || "");
    S.graph = data;
    S.total = data.total;
    paintGraphData(data, { animate: true, fit, warm: false, remote });
    if (repaintRail) {
      // Facets are secondary UI: coalesce rapid changes and never delay nodes.
      filterFacetTimer = setTimeout(async () => {
        const controller = new AbortController();
        filterFacetController = controller;
        try {
          const facetData = await api.units({ ...query, limit: 1, facets: 1 }, { signal: controller.signal });
          if (seq !== filterRequestSeq || !facetData?.facets) return;
          S.facets = facetData.facets;
          const rail = document.querySelector(".rail");
          if (rail) {
            const top = rail.scrollTop;
            renderFilterRail(rail, ctx);
            rail.scrollTop = top;
          }
        } catch { /* Keep the current counts when offline or superseded. */ }
      }, 120);
    }
    return;
  }
  const base = ensureGraphBase();
  const local = locallyFilterGraph(base, S.filters);
  paintGraphData(local, { animate: true, fit, remote });
  const localIds = graphShapeKey(local.nodes, local.edges);
  try {
    const query = currentGraphQuery();
    const facetQuery = { ...query, limit: 1, facets: 1 };
    delete facetQuery.context;
    const [data, facetData] = await Promise.all([
      api.graph(query),
      repaintRail ? api.units(facetQuery).catch(() => null) : Promise.resolve(null),
    ]);
    if (seq !== filterRequestSeq) return;
    S.graph = { nodes: data.nodes, edges: data.edges, context_count: data.context_count };
    if (facetData?.facets) S.facets = facetData.facets;
    if (graphShapeKey(data.nodes, data.edges) !== localIds) {
      for (const graph of targets) {
        const selected = [...graph.selectedNodes][0] || null;
        graph.setData(data.nodes, data.edges, {
          selected: selected && data.nodes.some((node) => node.id === selected) ? selected : null,
          layout: graph.layout,
          preserveCamera: true,
          animate: true,
          remote,
        });
        if (fit && graph.nodes?.length) graph.fit({ animate: true });
      }
    }
    if (repaintRail) {
      const rail = document.querySelector(".rail");
      if (rail) {
        const top = rail.scrollTop;
        renderFilterRail(rail, ctx);
        requestAnimationFrame(() => { rail.scrollTop = top; });
      }
      // Fit starts with the data transition above, not after it.  Live
      // timeline drags skip this (repaintRail=false); the release event fits.
    }
  } catch {
    if (!silent) renderRoute({ keepScroll: true });
  }
}

function destroyVisuals() {
  ++filterRequestSeq;
  clearTimeout(filterFacetTimer);
  filterFacetController?.abort();
  tasksModule?.stopTaskSessions();
  for (const g of graphs) { try { g.destroy(); } catch { /* noop */ } }
  for (const g of gantts) { try { g.destroy?.(); } catch { /* noop */ } }
  graphs = [];
  gantts = [];
  // The Gantt view has no listeners besides DOM; dropping references is enough.
}

// ------------------------------------------------------------------- mobile nav
function renderMobileNav() {
  if (!mobileTabbar) return;
  clear(mobileTabbar);
  mobileTabbar.setAttribute("aria-label", t("app.mobile.tabbar_label"));
  for (const item of MOBILE_PRIMARY_TABS) {
    mobileTabbar.appendChild(h("button", {
      class: "mobile-tab", type: "button", "data-tab": item.id,
      "aria-label": t(item.labelKey),
      onclick: () => item.workspace ? navigateWorkspace(item.workspace) : navigate(item.id, item.id === "plan" ? S.sub : null),
    }, icon(item.icon, "sm"), h("span", { class: "mobile-tab-label" }, t(item.labelKey))));
  }
  syncMobileNav();
}

function syncMobileNav() {
  if (!mobileTabbar) return;
  const current = workspaceForTab(S.tab) === "editor" ? "execute" : workspaceForTab(S.tab);
  for (const button of mobileTabbar.querySelectorAll(".mobile-tab[data-tab]")) {
    const active = button.dataset.tab === current;
    button.classList.toggle("active", active);
    if (active) button.setAttribute("aria-current", "page");
    else button.removeAttribute("aria-current");
  }
}

// The ARK brand opens one floating menu.  Phones get the navigation sheet;
// desktop gets a centred project chooser (the top bar has no project selector
// or New button any more).  Both share the dialog shell, Escape/click-outside,
// the focus trap and returning focus to whatever opened the menu.
function isMobileViewport() {
  return Boolean(window.matchMedia?.("(max-width: 760px)")?.matches);
}

function openAppMenu({ trigger = document.activeElement, projects = false } = {}) {
  if (mobileMenuEl?.isConnected) return;
  // Something else cleared #overlay-root; drop the stale menu state first.
  if (mobileMenuEl) mobileMenuClose?.({ restore: false });
  // The top-bar project selector always opens the project list.
  const desktop = projects || !isMobileViewport();
  const returnFocus = trigger instanceof HTMLElement && trigger !== document.body ? trigger : null;
  const brand = trigger?.closest?.(".project-picker") || topbar.querySelector(".brand");
  const close = ({ restore = true } = {}) => {
    if (!mobileMenuEl) return;
    mobileMenuEl.remove();
    mobileMenuEl = null;
    mobileMenuClose = null;
    document.removeEventListener("keydown", onKey, true);
    brand?.setAttribute("aria-expanded", "false");
    if (restore && returnFocus?.isConnected) returnFocus.focus();
  };
  // An action that opens another surface (modal, route) takes focus itself.
  const act = (fn) => () => { close({ restore: false }); fn(); };
  const backdrop = h("div", {
    class: `mobile-menu-backdrop ${desktop ? "app-menu-backdrop" : ""}`.trim(), role: "presentation",
    onclick: (event) => { if (event.target === backdrop) close(); },
  });
  const sheet = desktop ? projectMenuSheet(act, close) : mobileMenuSheet(act);
  sheet.setAttribute("aria-modal", "true");
  sheet.tabIndex = -1;
  const focusables = () => [...sheet.querySelectorAll("button:not([disabled]), a[href], input, select, textarea")]
    .filter((el) => el.getClientRects().length);
  const onKey = (event) => {
    // A project card's context menu handles its own keys (Escape closes it only).
    if (contextMenuOpen()) return;
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      close();
      return;
    }
    if (event.key === "Tab") {
      const items = focusables();
      if (!items.length) { event.preventDefault(); sheet.focus(); return; }
      const first = items[0], last = items[items.length - 1];
      const active = document.activeElement;
      const inside = sheet.contains(active) && active !== sheet;
      if (event.shiftKey && (!inside || active === first)) {
        event.preventDefault(); last.focus();
      } else if (!event.shiftKey && (!inside || active === last)) {
        event.preventDefault(); first.focus();
      }
      return;
    }
    // Arrow keys walk the project list (desktop) or the navigation grid.
    if (["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) {
      const group = document.activeElement?.closest?.("[data-menu-group]");
      if (!group || !sheet.contains(group)) return;
      const items = [...group.querySelectorAll("button:not([disabled])")];
      const index = items.indexOf(document.activeElement);
      if (index < 0) return;
      event.preventDefault();
      const next = event.key === "Home" ? 0 : event.key === "End" ? items.length - 1
        : (index + (event.key === "ArrowDown" ? 1 : -1) + items.length) % items.length;
      items[next].focus();
    }
  };
  backdrop.appendChild(sheet);
  document.getElementById("overlay-root").appendChild(backdrop);
  mobileMenuEl = backdrop;
  mobileMenuClose = close;
  brand?.setAttribute("aria-expanded", "true");
  document.addEventListener("keydown", onKey, true);
  requestAnimationFrame(() => {
    const start = sheet.querySelector("[data-menu-autofocus]") || focusables()[0] || sheet;
    start.focus();
  });
}

function projectMenuSheet(act, close) {
  const current = S.project || {};
  // Cards in picker order (pins, then the person's own activity); refreshed
  // in the background with cards keeping their places, so a click in
  // progress stays on its project.
  const picker = liveProjectPicker(ctx, {
    label: t("app.menu.projects"),
    beforeOpen: () => { closeInspector(ctx); close({ restore: false }); },
  });
  const list = picker.el;
  list.classList.add("app-menu-projects");
  picker.focusCurrent()?.setAttribute("data-menu-autofocus", "");
  return h("div", { class: "mobile-menu-sheet app-menu-sheet", role: "dialog", "aria-labelledby": "app-menu-title" },
    h("div", { class: "app-menu-head" },
      h("img", { class: "app-menu-mark", src: "./brand/ark-mark.svg", alt: "", width: 22, height: 22 }),
      h("div", { class: "mobile-menu-title", id: "app-menu-title" }, t("app.menu.projects")),
      h("button", {
        class: "icon-btn", type: "button", title: t("action.close"), "aria-label": t("action.close"),
        onclick: () => close(),
      }, icon("x", "sm"))),
    h("div", { class: "app-menu-current" },
      h("span", { class: `project-dot ${current.demo ? "demo" : ""}`.trim() }),
      h("div", { class: "app-menu-current-copy" },
        h("small", null, t("project.current")),
        h("b", null, current.name || t("common.project")),
        h("span", { class: "app-menu-path", title: current.path || "" }, current.path || "—"))),
    withDesktopGroups(list, { close: () => close({ restore: false }), toast, modal: openModal }),
    // Two actions only: create a project, or open another one (the list
    // above already switches between the known ones).
    h("div", { class: "mobile-menu-actions app-menu-actions" },
      h("button", {
        class: "btn primary", type: "button", "data-menu-action": "new-project",
        onclick: act(() => { closeInspector(ctx); openProjectModal(ctx, "new"); }),
      }, icon("plus", "sm"), t("project.new")),
      h("button", {
        class: "btn ghost", type: "button", "data-menu-action": "open-project",
        onclick: act(() => { closeInspector(ctx); openProjectModal(ctx, "open"); }),
      }, icon("folder", "sm"), t("project.open_other"))));
}

function mobileMenuSheet(act) {
  const pwa = pwaSnapshot();
  const items = [...MOBILE_PRIMARY_TABS, ...MOBILE_SECONDARY_TABS]
    .filter((item) => item.id !== "distribution" || advancedEnabled() || S.tab === "distribution");
  return h("div", { class: "mobile-menu-sheet", role: "dialog", "aria-label": t("app.mobile.nav_label") },
    h("div", { class: "mobile-menu-grab" }),
    h("div", { class: "mobile-menu-title" }, icon("overview", "sm"), t("app.mobile.menu_title")),
    h("div", { class: "mobile-menu-grid", "data-menu-group": "nav" }, items.map((item) => h("button", {
      class: `mobile-menu-item ${workspaceForTab(S.tab) === (item.workspace || item.id) ? "active" : ""}`.trim(),
      type: "button", "data-tab": item.id,
      onclick: act(() => { item.workspace ? navigateWorkspace(item.workspace) : navigate(item.id, item.id === "plan" ? S.sub : null); }),
    }, icon(item.icon, "sm"), h("span", null, t(item.labelKey))))),
    h("div", { class: "mobile-menu-actions" },
      h("button", { class: "btn ghost", onclick: act(() => openCreateUnit(ctx, {})) }, icon("plus", "sm"), t("action.new_node")),
      h("button", { class: "btn ghost", onclick: act(() => openProjectModal(ctx)) }, icon("folder", "sm"), t("app.mobile.project")),
      h("button", {
        class: "btn ghost",
        onclick: act(() => {
          toggleTheme(); renderTopbar();
          graphs.forEach((g) => g._draw?.());
        }),
      }, icon(S.theme === "dark" ? "sun" : "moon", "sm"), t("app.mobile.theme")),
      h("button", {
        class: "btn ghost",
        onclick: act(() => changeLanguage(getLang() === "zh" ? "en" : "zh")),
      }, icon("globe", "sm"), getLang() === "zh" ? "English" : "中文"), // i18n-ignore: language names shown in their own language
      h("button", { class: "btn ghost", onclick: act(() => openExport()) }, icon("download", "sm"), t("action.export")),
      S.tab === "tasks" ? h("button", {
        class: "btn ghost",
        onclick: act(() => ctx.setTaskView("board")),
      }, icon("tasks", "sm"), t("tasks.related_board")) : null,
      S.tab === "tasks" ? h("button", {
        class: "btn ghost",
        onclick: act(() => openProjectFiles()),
      }, icon("folder", "sm"), t("tasks.files")) : null,
      pwa.canInstall
        ? h("button", {
          class: "btn ghost",
          onclick: act(async () => {
            const accepted = await promptInstall();
            toast(t(accepted ? "app.pwa.installed_toast" : "app.pwa.install_dismissed"), accepted ? "ok" : "err");
          }),
        }, icon("download", "sm"), t("app.pwa.install"))
        : null),
    h("p", { class: "mobile-menu-note" },
      pwa.installed ? t("app.pwa.running_installed")
        : pwa.canCache ? t("app.pwa.add_home")
          : t("app.pwa.http_unavailable")));
}

// ------------------------------------------------------------------- topbar
// Mobile filter is a sheet over the current page; opening it must not reload
// the route or recreate the graph canvas.
function setFilterOverlay(open) {
  const rail = document.querySelector(".rail");
  if (!rail) {
    renderRoute({ keepScroll: true });
    return;
  }
  S.railOpen = Boolean(open);
  rail.classList.toggle("open", S.railOpen);
  let backdrop = document.querySelector(".rail-backdrop");
  if (S.railOpen) {
    if (!backdrop) {
      backdrop = h("div", {
        class: "rail-backdrop", role: "presentation",
        onclick: () => setFilterOverlay(false),
      });
      appRoot.appendChild(backdrop);
    }
  } else if (backdrop) {
    backdrop.remove();
  }
}

function mobileNavTitle() {
  if (S.tab === "chat") return t("chat.title");
  const session = S.tab === "tasks" && S.taskView === "sessions"
    ? (S.taskSessions || []).find((item) => item.id === S.selectedSessionId)
    : null;
  if (session) return session.title || session.id || t("nav.tasks");
  if (S.tab === "ideas") return t("ideas.title");
  if (S.tab === "review") return t("review.title");
  if (S.tab === "map") return t("map.title");
  if (S.tab === "tasks") return t("tasks.title");
  if (S.tab === "editor") return t("nav.editor");
  return t(`nav.${S.tab === "plan" ? S.sub : S.tab}`) || "ARK";
}

function workspaceSubtabs(workspace) {
  const items = [];
  const add = (label, active, onclick, data, stage = false) => items.push({ label, active, onclick, data, stage });
  if (workspace === "research") {
    add(t("nav.ideas"), S.tab === "ideas", () => navigateWorkspace("research", "idea"), { "data-workspace-view": "idea" });
    // Story, Related, Method and Experiment are default topics; every topic
    // (built-in or not) opens from the topic bar on this page.
    add(t("nav.topic"), S.tab === "plan", () => { if (S.tab !== "plan") navigateWorkspace("research", "topic"); },
      { "data-workspace-stage": "topic" });
    add(t("nav.review"), S.tab === "review", () => navigateWorkspace("research", "review"), { "data-workspace-view": "review" });
  } else if (workspace === "execute") {
    const active = EXECUTE_VIEW_ALIASES[S.taskView] || "board";
    // GPU and Resources are everyday tools for this team: always shown
    // (the advanced switch only folds rarely used views).
    for (const [id, label] of [["board", t("view.board")], ["runs", t("app.subtab.runs")], ["gpu", "GPU"], ["resources", t("resources.tab")]]) {
      add(label, active === (EXECUTE_VIEW_ALIASES[id] || id),
        () => navigateWorkspace("execute", id), { "data-workspace-view": id });
    }
  } else if (workspace === "output") {
    const active = S.tab === "distribution" ? "distribution" : "assets";
    add(t("production.assets"), active === "assets", () => navigateWorkspace("output", "assets"), { "data-workspace-view": "assets" });
    if (advancedEnabled() || active === "distribution") {
      add(t("nav.distribution"), active === "distribution", () => navigateWorkspace("output", "distribution"), { "data-workspace-view": "distribution" });
    }
  }
  return items;
}

function subtabButton(item, extraClass = "") {
  return h("button", {
    class: `top-subtab ${extraClass} ${item.stage ? "stage" : ""} ${item.active ? "on" : ""}`.replace(/\s+/g, " ").trim(),
    type: "button", ...item.data,
    "aria-current": item.active ? "page" : undefined,
    onclick: item.onclick,
  }, item.label);
}

// Keep a sub-tab visible inside its horizontal scroller (desktop sub-nav or
// the phone title row) and refresh the fade-edge state.
function revealSubtab(el) {
  const scroller = el?.closest(".top-subnav-scroll, .mobile-nav-title");
  if (!scroller) return;
  const box = scroller.getBoundingClientRect();
  const rect = el.getBoundingClientRect();
  if (rect.left < box.left + 8) scroller.scrollLeft += rect.left - box.left - 28;
  else if (rect.right > box.right - 8) scroller.scrollLeft += rect.right - box.right + 28;
  updateScrollFades(scroller);
}

function updateScrollFades(scroller) {
  const max = scroller.scrollWidth - scroller.clientWidth;
  scroller.classList.toggle("fade-start", scroller.scrollLeft > 2);
  scroller.classList.toggle("fade-end", max - scroller.scrollLeft > 2);
}

// Only the sub-tab strip scrolls; the main workspace tabs stay fixed. The
// vertical mouse wheel scrolls it sideways, touch scrolls natively.
function makeSubnavScroller(scroller) {
  scroller.addEventListener("wheel", (event) => {
    if (Math.abs(event.deltaY) <= Math.abs(event.deltaX)) return;
    if (scroller.scrollWidth <= scroller.clientWidth) return;
    event.preventDefault();
    scroller.scrollLeft += event.deltaY;
  }, { passive: false });
  scroller.addEventListener("scroll", () => updateScrollFades(scroller), { passive: true });
  if (typeof ResizeObserver === "function") {
    const observer = new ResizeObserver(() => {
      if (!scroller.isConnected) { observer.disconnect(); return; }
      updateScrollFades(scroller);
    });
    observer.observe(scroller);
  }
  requestAnimationFrame(() => {
    revealSubtab(scroller.querySelector(".top-subtab.on"));
    updateScrollFades(scroller);
  });
  return scroller;
}

// The Editor has no top-bar sub-tabs: its open files live in its own tab bar.
function topbarWorkspaceSubnav(workspace) {
  const items = workspaceSubtabs(workspace);
  if (!items.length) return null;
  const scroller = makeSubnavScroller(h("div", { class: "top-subnav-scroll" }, ...items.map((item) => subtabButton(item))));
  return h("div", {
    class: "top-subnav", role: "group",
    "aria-label": t("app.workspace_views", { name: workspaceLabel(workspace) }),
  }, h("span", { class: "top-subtab-divider" }), scroller);
}

// On phones the page title becomes the workspace's sub-tabs (the desktop
// sub-nav is hidden there); workspaces without sub-tabs keep a plain title.
function renderMobileNavTitle(host) {
  const workspace = workspaceForTab(S.tab);
  const items = workspaceSubtabs(workspace);
  const key = items.length ? `${workspace}|${items.map((item) => `${item.label}:${item.active ? 1 : 0}`).join(",")}` : `title|${mobileNavTitle()}`;
  if (host.dataset.key === key) return;
  host.dataset.key = key;
  clear(host);
  host.classList.toggle("has-subtabs", Boolean(items.length));
  if (!items.length) {
    host.textContent = mobileNavTitle();
    host.removeAttribute("role");
    return;
  }
  host.setAttribute("role", "tablist");
  host.setAttribute("aria-label", t("app.workspace_views", { name: workspaceLabel(workspace) }));
  for (const item of items) {
    const button = subtabButton(item, "mobile-subtab");
    button.setAttribute("role", "tab");
    button.setAttribute("aria-selected", item.active ? "true" : "false");
    host.appendChild(button);
  }
  if (workspace === "execute") {
    // Phones: Editor is a sub-option of Execute (no free bottom-bar slot).
    host.appendChild(h("button", {
      class: "top-subtab mobile-subtab mobile-editor-chip", type: "button", "data-workspace-view": "editor",
      onclick: () => navigateWorkspace("editor"),
    }, icon("edit", "sm"), t("nav.editor")));
  }
  const active = host.querySelector(".mobile-subtab.on");
  if (active) requestAnimationFrame(() => revealSubtab(active));
}

function searchTitle() {
  const spec = getShortcut("palette");
  return spec ? `${t("action.search")} (${shortcutLabel(spec)})` : t("action.search");
}

// Keep the search hint equal to the configured palette shortcut.
window.addEventListener(SHORTCUTS_CHANGED, () => {
  const spec = getShortcut("palette");
  for (const kbd of document.querySelectorAll(".palette-kbd")) {
    kbd.textContent = shortcutLabel(spec);
    kbd.hidden = !spec;
  }
  for (const button of document.querySelectorAll(".search-btn")) button.title = searchTitle();
});

// The brand images are created once and moved into each rebuilt top bar, so
// a re-render (theme toggle, language switch, navigation) never shows an
// empty, still-loading logo for a frame.
let brandImages = null;
function brandImage(name) {
  if (!brandImages) {
    brandImages = {
      mark: h("img", { src: "./brand/ark-mark.svg", alt: "", width: 28, height: 28 }),
      light: h("img", { class: "ark-lockup ark-lockup-light", src: "./brand/ark-lockup-corner.svg", alt: "", width: 141.4384, height: 30 }),
      dark: h("img", { class: "ark-lockup ark-lockup-dark", src: "./brand/ark-lockup-corner-dark.svg", alt: "", width: 141.4384, height: 30 }),
    };
  }
  return brandImages[name];
}

function renderTopbar() {
  if (!S.booted) return;
  clear(topbar);
  topbar.setAttribute("aria-label", t("app.topbar_label"));
  // The brand opens the project menu (desktop) or the navigation sheet (phone).
  const brandTitle = S.project?.name ? `${t("app.menu.brand_title")} · ${S.project.name}` : t("app.menu.brand_title");
  topbar.appendChild(h("div", {
    class: "brand", role: "button", tabindex: "0", title: brandTitle, "aria-label": `ARK Studio · ${brandTitle}`,
    "aria-haspopup": "dialog", "aria-expanded": mobileMenuEl ? "true" : "false",
    onclick: (event) => openAppMenu({ trigger: event.currentTarget }),
    onkeydown: (event) => {
      if (event.key !== "Enter" && event.key !== " ") return;
      event.preventDefault();
      openAppMenu({ trigger: event.currentTarget });
    },
  },
    h("div", { class: "brand-mark brand-prism" }, brandImage("mark")),
    h("div", { class: "brand-word" }, brandImage("light"), brandImage("dark"))));
  // Compact project selector next to the brand; same menu as the brand.
  // Hidden on narrow screens, where the brand menu carries the project list.
  topbar.appendChild(h("button", {
    class: "project-picker", type: "button", title: S.project?.path || S.project?.name || "",
    "aria-haspopup": "dialog", "aria-expanded": "false",
    "aria-label": `${t("app.menu.projects")}: ${S.project?.name || t("common.project")}`,
    onclick: (event) => openAppMenu({ trigger: event.currentTarget, projects: true }),
  },
    h("span", { class: `project-dot ${S.project?.demo ? "demo" : ""}`.trim() }),
    h("span", { class: "p-name" }, S.project?.name || t("common.project")),
    icon("chevron", "sm")));
  const mobileTitle = h("div", { class: "mobile-nav-title" });
  renderMobileNavTitle(mobileTitle);
  topbar.appendChild(mobileTitle);

  const tabs = h("nav", { class: "tabs", "aria-label": t("app.nav_primary") });
  const mainTabs = h("div", { class: "tabs-main" });
  tabs.appendChild(mainTabs);
  for (const [id, label, iconName] of WORKSPACES) {
    mainTabs.appendChild(h("button", {
      class: `tab ${workspaceForTab(S.tab) === id ? "active" : ""}`.trim(),
      "data-workspace": id,
      onclick: () => navigateWorkspace(id),
      "aria-current": workspaceForTab(S.tab) === id ? "page" : undefined,
      title: t(label),
    },
      h("span", { class: "tab-label" }, t(label)),
      id === "home" ? needsBadge() : null));
  }
  const subnav = topbarWorkspaceSubnav(workspaceForTab(S.tab));
  if (subnav) tabs.appendChild(subnav);
  topbar.appendChild(tabs);
  topbar.dataset.workspace = workspaceForTab(S.tab);
  topbar.dataset.tab = S.tab;

  // Every control on the right is one `top-ctl`: same height, radius, idle,
  // hover and active look (app.css "Top-bar controls").
  const layoutToggle = (name) => {
    const info = layoutToggleInfo(name);
    return h("button", {
      class: `icon-btn top-ctl layout-toggle ${info.active ? "on" : ""}`.trim(), type: "button", title: info.label,
      "aria-pressed": info.active ? "true" : "false",
      "aria-label": info.label, "aria-disabled": info.disabled ? "true" : undefined, "data-layout": name,
      onclick: () => onLayoutToggle(name),
    }, icon(name));
  };
  // Panel toggles (classic layout only; the workbench hides them) and the
  // layout presets (workbench only).
  const layoutToggles = h("div", { class: "layout-toggles" },
    layoutToggle("panel-left"), layoutToggle("panel-right"), layoutToggle("panel-bottom"),
    h("button", {
      class: "icon-btn top-ctl layout-toggle layout-presets-btn", type: "button", title: t("workbench.preset.menu"),
      "aria-label": t("workbench.preset.menu"), "aria-haspopup": "menu",
      onclick: (event) => openLayoutMenu(event.currentTarget),
    }, icon("layers")));
  const sync = syncTopButton();
  sync.classList.add("top-ctl");
  const account = accountMenuButton(ctx);
  account?.classList.add("top-ctl");
  // Left to right: search, sync, Assistant, layout, theme, language.  On the
  // desktop workbench Settings and the account sit at the bottom of the
  // activity bar; without it (narrower windows) they end the top bar, and
  // phones reach Settings from the bottom tab bar.
  const actions = h("div", { class: "top-actions" },
    h("button", { class: "search-btn top-ctl", type: "button", onclick: () => openPalette(), title: searchTitle() },
      icon("search"), h("span", null, t("action.search")),
      h("span", { class: "kbd palette-kbd", hidden: !getShortcut("palette") }, shortcutLabel(getShortcut("palette")))),
    h("button", {
      class: `icon-btn top-ctl mobile-filter-btn ${["ideas", "plan", "map"].includes(S.tab) ? "" : "hidden"}`.trim(),
      type: "button", title: t("action.filters"), "aria-label": t("action.filters"),
      onclick: () => setFilterOverlay(!S.railOpen),
    }, icon("filter")),
    sync,
    workspaceButton("assistant", "assistant-top-btn top-ctl", `${t("nav.assistant")} (${formatShortcut(getShortcut("chat"))})`),
    layoutToggles,
    h("button", {
      class: "icon-btn top-ctl theme-top-btn", type: "button", title: t("palette.cmd.theme"), "aria-label": t("palette.cmd.theme"),
      onclick: () => { toggleTheme(); renderTopbar(); graphs.forEach((g) => g._draw?.()); },
    }, icon(S.theme === "dark" ? "sun" : "moon")),
    // One toggle: shows the current language, a click switches to the other.
    h("button", {
      class: "lang-toggle top-ctl", type: "button", lang: getLang() === "zh" ? "zh" : "en",
      title: t("topbar.lang_switch"), "aria-label": t("topbar.lang_switch"),
      onclick: () => changeLanguage(getLang() === "zh" ? "en" : "zh"),
    }, getLang() === "zh" ? "中" : "EN"), // i18n-ignore: language codes
    workspaceButton("settings", "settings-top-btn top-ctl"),
    account,
    h("button", {
      class: "mobile-menu-btn", type: "button", title: t("action.more"), "aria-label": t("app.more_nav"),
      onclick: (event) => openAppMenu({ trigger: event.currentTarget }),
    }, icon("more")));
  topbar.appendChild(actions);
  renderActivityFoot();
  syncThemeColor(S.theme);
}

// Assistant and Settings each have their own button (no shared pill); the
// Settings one in the top bar only shows where there is no activity bar.
// The Assistant is the star everywhere; Sessions keep the chat bubble.
const WORKSPACE_BUTTONS = { assistant: ["nav.assistant", "sparkles"], settings: ["nav.settings", "settings"] };

function workspaceButton(id, extraClass, title = null, { side = false } = {}) {
  const [label, iconName] = WORKSPACE_BUTTONS[id];
  const active = workspaceForTab(S.tab) === id;
  return h("button", {
    class: `${side ? "wb-act" : "icon-btn"} ${extraClass} workspace-btn ${active ? (side ? "on" : "active") : ""}`.trim(),
    type: "button", "data-workspace": id, title: title || t(label), "aria-label": t(label),
    "aria-current": active ? "page" : undefined,
    onclick: () => navigateWorkspace(id),
  }, icon(iconName, side ? "sm" : undefined));
}

// Settings at one tab (or section): the About button, the palette.
function openSettingsAt(id) {
  const target = settingsTarget(id);
  if (!target) return;
  try {
    localStorage.setItem("ark.settingsScope", target.scope);
    // A section id opens its tab scrolled to the section.
    localStorage.setItem("ark.settingsTab", target.section || target.tab);
  } catch { /* storage unavailable */ }
  if (S.tab !== "settings") { navigateWorkspace("settings"); return; }
  // A #/settings?settings=… link would win over the stored tab.
  if (String(location.hash).includes("?")) history.replaceState(history.state, "", `${location.pathname}${location.search}#/settings`);
  renderRoute({ keepScroll: false });
}

// Bottom of the workbench activity bar (as in VS Code): About, Settings, then
// the account avatar, whose menu opens to the right of the bar.
const activityFoot = h("div", { class: "wb-activity-foot" });
function renderActivityFoot() {
  clear(activityFoot);
  activityFoot.append(aboutActivityButton(() => openSettingsAt("about")));
  activityFoot.append(workspaceButton("settings", "wb-foot-btn", null, { side: true }));
  const account = accountMenuButton(ctx, { side: true });
  if (account) activityFoot.append(account);
  setActivityFooter(activityFoot);
}

function syncWorkspaceButtons() {
  const activeWorkspace = workspaceForTab(S.tab);
  for (const button of document.querySelectorAll(".workspace-btn[data-workspace]")) {
    const active = button.dataset.workspace === activeWorkspace;
    button.classList.toggle(button.classList.contains("wb-act") ? "on" : "active", active);
    if (active) button.setAttribute("aria-current", "page");
    else button.removeAttribute("aria-current");
  }
}

// Count of items waiting on the user, shown on the Home tab.
function needsBadge() {
  const n = needsCount(S.needsYou) + assignedCount(assignedEntries(S.needsAssigned || [], S.needsYou));
  return h("span", {
    class: "tab-badge needs-badge", hidden: n ? undefined : true,
    title: `${t("needs.title")}: ${n}`, "aria-label": `${t("needs.title")}: ${n}`,
  }, String(n));
}

// Update in place: replacing nodes inside a tab between pointerdown and click
// can swallow the navigation (see the note in renderRoute).
function syncNeedsBadge() {
  const badge = topbar.querySelector('.tab[data-workspace="home"] .needs-badge');
  if (!badge) return;
  const fresh = needsBadge();
  badge.textContent = fresh.textContent;
  badge.hidden = fresh.hidden;
  badge.title = fresh.title;
  badge.setAttribute("aria-label", fresh.getAttribute("aria-label"));
}

// The top-bar left layout toggle controls whichever left sidebar the current
// view has: the filter rail (Research/Map) or the Editor's Explorer. Each keeps
// its own remembered state; other views have no left panel.
// Top-bar layout toggles.  With the workbench shell (desktop) they show/hide
// the left sidebar, right sidebar and bottom panel, whatever views those
// hold; in the classic layout (980px and below) they keep their old meaning.
function layoutToggleInfo(name) {
  const place = name.replace("panel-", "");
  if (shellActive()) {
    const state = containerState(place);
    return {
      active: state.shown, disabled: !state.available,
      label: state.available ? t("workbench.toggle_container", { place: containerTitle(place) })
        : t("workbench.container_empty", { place: containerTitle(place) }),
    };
  }
  if (name === "panel-left") {
    const left = leftPanel();
    return { active: left.visible, disabled: left.kind === "none", label: left.label };
  }
  if (name === "panel-right") return { active: S.inspectorPane !== false, disabled: false, label: t("app.toggle.inspector") };
  return { active: Boolean(S.sessionDock), disabled: false, label: t("app.toggle.session_dock") };
}

async function onLayoutToggle(name) {
  const place = name.replace("panel-", "");
  if (shellActive()) {
    if (containerState(place).available) toggleWorkbenchContainer(place);
    return;
  }
  if (name === "panel-left") {
    const panel = leftPanel();
    if (panel.kind === "filters") {
      S.filtersVisible = S.filtersVisible === false;
      localStorage.setItem("ark.filtersVisible", S.filtersVisible ? "1" : "0");
      renderRoute({ keepScroll: true });
    } else if (panel.kind === "explorer") {
      setExplorerVisible(!editorExplorerVisible());
    }
    return;
  }
  if (name === "panel-right") {
    S.inspectorPane = S.inspectorPane === false;
    localStorage.setItem("ark.inspectorPane", S.inspectorPane ? "1" : "0");
    document.body.classList.toggle("inspector-pinned", S.inspectorPane && S.drawerOpen);
    if (!S.inspectorPane) closeInspector(ctx);
    else if (S.drawerOpen && (S.multi || S.detail?.id)) await reopenInspector();
    else showInspectorPlaceholder(ctx);
    renderTopbar();
    return;
  }
  toggleSessionDock(ctx);
  renderTopbar();
}

// ------------------------------------------------------------- workbench
// Register the existing panels as workbench views and keep the legacy
// panel flags (filtersVisible, inspectorPane, sessionDock) mirroring the
// container that holds each view, so code that reads them keeps working.
//
// The built-in views are first-party view plugins (view-plugins.js,
// plugin-host.js): the same registry and lifecycle as user plugins.  Panel
// views mount their element; Filters, Explorer, Details and the Assistant
// are rendered into their container by their owners (viewHost(id)).
// `update({ reason: "show" })` is the former onShow hook.
const FILTER_TABS = ["ideas", "plan", "map"];
const panelPlugin = (panel) => ({
  mount: (container) => container.appendChild(panel.el),
  update: (state) => { if (state.reason === "show") panel.shown?.(); },
});
function setupWorkbench() {
  initPluginHost(ctx);
  registerBuiltinView("filters", {}, { applies: (state) => FILTER_TABS.includes(state.tab) });
  registerBuiltinView("explorer", {}, { applies: (state) => state.tab === "editor" });
  registerBuiltinView("details", {}, { applies: (state) => Boolean(state.drawerOpen) || !["tasks", "editor"].includes(state.tab) });
  registerBuiltinView("assistant", {}, {
    applies: (state) => state.tab !== "chat",
    tooltip: () => t("session.dock.subtitle"),
    actions: () => [{ icon: "sparkles", title: t("session.dock.full_chat"), onClick: () => navigate("chat") }],
    menu: () => [{ label: t("chat.clear_history"), icon: "trash", danger: true, onClick: () => clearAssistantHistory(ctx) }],
  });
  // Former Execute › Files: git review and the project terminal.
  const changes = createChangesPanel({
    onOpenDiff: (path, base) => openInEditor(ctx, { kind: "diff", path, base }, { preview: true }),
    onCount: () => renderWorkbench(),
  });
  registerBuiltinView("changes", panelPlugin(changes), { applies: (state) => state.tab === "editor", badge: () => changes.count() || "" });
  // Local graph sits with Details (right sidebar): both follow the selection.
  const localGraph = createLocalGraphPanel(ctx);
  registerBuiltinView("localgraph", panelPlugin(localGraph), {
    // In the Editor only while a node tab is open (or a node is in Details).
    applies: (state) => Boolean(state.drawerOpen)
      || (state.tab === "editor" ? String(state.editorKey || "").startsWith("unit:") : !["tasks", "settings", "chat"].includes(state.tab)),
  });
  window.addEventListener("ark:selection", () => renderWorkbench());
  searchPanel = createSearchPanel(ctx);
  registerBuiltinView("search", panelPlugin(searchPanel));
  const terminal = createTerminalPanel();
  registerBuiltinView("terminal", panelPlugin(terminal), { applies: (state) => ["editor", "tasks"].includes(state.tab) });
  // Agent sessions as a chat beside the work; shares one stream with Execute › Sessions.
  sessionSidebar = createSessionSidebar(ctx);
  // Execute › Runs: its list shows in the Sessions view, its details in Session properties.
  registerBuiltinView("sessionprops", {}, { applies: (state) => state.tab === "tasks" && state.taskView === "sessions" });
  // The Sessions view's header buttons follow its mode (list or conversation).
  window.addEventListener("ark:view-actions", () => renderWorkbench());
  ctx.sessionPage = {
    mount({ list, props }) {
      sessionSidebar.showList(list);
      const host = viewHost("sessionprops");
      if (host) host.replaceChildren(props);
      // The list is how the page is used: open its view; show the properties when their bar is open.
      requestAnimationFrame(() => { showView("sessions", true); revealView("sessionprops"); });
    },
  };
  registerBuiltinView("sessions", panelPlugin(sessionSidebar), {
    tooltip: () => t("session.sidebar.tooltip"),
    // The list (Execute › Runs): launch and refresh; elsewhere a new chat and the page.
    actions: () => (sessionSidebar.listMode ? [
      { icon: "plus", title: t("session.runs_menu"), onClick: (event) => {
        const items = ctx.runsActions?.() || [{ label: t("session.sidebar.new"), icon: "plus", onClick: () => ctx.openSessionLaunch?.({}) }];
        const rect = event?.currentTarget?.getBoundingClientRect?.() || { left: 80, bottom: 80 };
        openContextMenu({ x: rect.left, y: rect.bottom + 4, items, returnFocus: event?.currentTarget || null });
      } },
      { icon: "refresh", title: t("action.refresh"), onClick: () => sessionSidebar.refreshList() },
    ] : [
      { icon: "plus", title: t("session.sidebar.new"), onClick: () => sessionSidebar.newSession() },
      { icon: "external", title: t("session.sidebar.open_page"), onClick: () => sessionSidebar.openPage() },
    ]),
  });
  initWorkbench(ctx, { onLayoutChange: onWorkbenchChange });
  if (shellActive()) syncPanelFlags();
  // The user's own plugins (Settings > Plugins), sandboxed; off unless enabled.
  loadUserPlugins().catch(() => {});
}

// "Files" (old Execute tab, mobile menu, deep links) now means the Editor
// with its explorer shown.
function openProjectFiles() {
  navigateWorkspace("editor");
  if (shellActive()) requestAnimationFrame(() => setExplorerVisible(true));
}

let searchPanel = null;
let sessionSidebar = null;

// Palette "Sessions sidebar": the docked chat on a wide window, the
// Sessions page on a narrow one (the workbench shell is desktop-only).
function openSessionSidebar() {
  if (shellActive()) showView("sessions", true);
  else navigateWorkspace("execute", "sessions");
}

// Ctrl+Shift+F: Search view (prefilled with the selected text); F4 /
// Shift+F4 step through its results.
function searchKeyDown(event) {
  if (!searchPanel) return false;
  const mod = event.ctrlKey || event.metaKey;
  if (mod && event.shiftKey && !event.altKey && (event.code === "KeyF" || event.key === "F" || event.key === "f")) {
    event.preventDefault();
    const selected = String(window.getSelection?.() || "").trim();
    if (shellActive()) showView("search", true);
    else if (!document.querySelector(".wb-search")?.offsetParent) return true;
    requestAnimationFrame(() => searchPanel.focus(selected.includes("\n") ? "" : selected.slice(0, 200)));
    return true;
  }
  if (event.key === "F4" && !mod && !event.altKey && searchPanel.hasResults()) {
    event.preventDefault();
    searchPanel.step(event.shiftKey ? -1 : 1);
    return true;
  }
  return false;
}

function syncPanelFlags() {
  S.filtersVisible = viewContainerVisible("filters");
  S.inspectorPane = viewContainerVisible("details");
  S.sessionDock = viewContainerVisible("assistant");
  try {
    localStorage.setItem("ark.filtersVisible", S.filtersVisible ? "1" : "0");
    localStorage.setItem("ark.inspectorPane", S.inspectorPane ? "1" : "0");
    localStorage.setItem("ark.sessionDock", S.sessionDock ? "1" : "0");
  } catch { /* storage disabled */ }
}

async function onWorkbenchChange({ reason }) {
  if (reason === "breakpoint") {
    // Crossing 980px switches between the shell and the classic layout:
    // re-place the filter rail, the inspector and the assistant dock.
    if (shellActive()) syncPanelFlags();
    mountSessionDock(ctx);
    await renderRoute({ keepScroll: true });
    if (S.drawerOpen && (S.multi || S.detail?.id)) await reopenInspector();
    renderTopbar();
    return;
  }
  if (!shellActive()) return;
  const before = { inspector: S.inspectorPane, dock: S.sessionDock };
  syncPanelFlags();
  if (S.sessionDock !== before.dock) mountSessionDock(ctx);
  if (S.inspectorPane !== before.inspector) {
    if (!S.inspectorPane && S.drawerOpen) closeInspector(ctx);
    else if (S.inspectorPane && S.drawerOpen && (S.multi || S.detail?.id)) await reopenInspector();
    else showInspectorPlaceholder(ctx);
  }
  syncTopbar();
}

function leftPanel() {
  if (["ideas", "plan", "map"].includes(S.tab)) {
    return { kind: "filters", visible: S.filtersVisible !== false, label: t("app.toggle.filters") };
  }
  if (S.tab === "editor") return { kind: "explorer", visible: editorExplorerVisible(), label: t("app.toggle.explorer") };
  return { kind: "none", visible: false, label: t("app.toggle.no_left_panel") };
}

window.addEventListener(EXPLORER_CHANGED, () => syncTopbar());
window.addEventListener(WORKBENCH_CHANGED, () => syncTopbar());

function syncTopbar() {
  if (!topbar.firstElementChild) {
    renderTopbar();
    return;
  }
  syncNeedsBadge();
  const activeWorkspace = workspaceForTab(S.tab);
  syncWorkspaceButtons();
  for (const button of topbar.querySelectorAll(".tab[data-workspace]")) {
    const active = button.dataset.workspace === activeWorkspace;
    button.classList.toggle("active", active);
    if (active) button.setAttribute("aria-current", "page");
    else button.removeAttribute("aria-current");
  }
  for (const button of topbar.querySelectorAll(".layout-toggle[data-layout]")) {
    const info = layoutToggleInfo(button.dataset.layout);
    button.classList.toggle("on", info.active);
    button.setAttribute("aria-pressed", info.active ? "true" : "false");
    button.title = info.label;
    button.setAttribute("aria-label", info.label);
    if (info.disabled) button.setAttribute("aria-disabled", "true");
    else button.removeAttribute("aria-disabled");
  }
  const brand = topbar.querySelector(".brand");
  if (brand) {
    const brandTitle = S.project?.name ? `${t("app.menu.brand_title")} · ${S.project.name}` : t("app.menu.brand_title");
    brand.title = brandTitle;
    brand.setAttribute("aria-label", `ARK Studio · ${brandTitle}`);
  }
  const picker = topbar.querySelector(".project-picker");
  if (picker) {
    const name = S.project?.name || t("common.project");
    picker.querySelector(".p-name").textContent = name;
    picker.title = S.project?.path || name;
    picker.setAttribute("aria-label", `${t("app.menu.projects")}: ${name}`);
    picker.querySelector(".project-dot")?.classList.toggle("demo", Boolean(S.project?.demo));
    syncReviewDot(picker);
  }
  for (const button of topbar.querySelectorAll(".top-subtab[data-workspace-view]")) {
    const id = button.dataset.workspaceView;
    let active = false;
    if (activeWorkspace === "research") active = (id === "idea" && S.tab === "ideas") || (id === "review" && S.tab === "review");
    else if (activeWorkspace === "execute") active = (EXECUTE_VIEW_ALIASES[S.taskView] || "board") === (EXECUTE_VIEW_ALIASES[id] || id);
    else if (activeWorkspace === "output") active = (id === "distribution" ? "distribution" : "assets") === (S.tab === "distribution" ? "distribution" : "assets");
    button.classList.toggle("on", active);
    if (active) button.setAttribute("aria-current", "page"); else button.removeAttribute("aria-current");
  }
  for (const button of topbar.querySelectorAll(".top-subtab[data-workspace-stage]")) {
    const active = S.tab === "plan";
    button.classList.toggle("on", active);
    if (active) button.setAttribute("aria-current", "page"); else button.removeAttribute("aria-current");
  }
  const mobileFilter = topbar.querySelector(".mobile-filter-btn");
  if (mobileFilter) mobileFilter.classList.toggle("hidden", !["ideas", "plan", "map"].includes(S.tab));
  const mobileTitle = topbar.querySelector(".mobile-nav-title");
  if (mobileTitle) renderMobileNavTitle(mobileTitle);
  syncMobileNav();
}

// A language whose strings were not loaded yet (setLang) arrives later.
window.addEventListener("ark:lang-loaded", (event) => {
  if (event.detail?.lang) changeLanguage(event.detail.lang);
});

async function changeLanguage(lang) {
  setLanguage(lang);
  renderMobileNav();
  renderTopbar();
  relocalizeSessionDock(ctx);
  sessionSidebar?.relocalize();
  await renderRoute({ keepScroll: true });
  // The inspector lives outside the route; rebuild it so its labels follow the language.
  if (S.drawerOpen && S.selectedId) await ctx.reloadDetail();
  ctx.guidance?.relocalize();
}

function onKeyDown(e) {
  // Editor save/close work inside the code textarea too.
  if (editorKeyDown(e)) return;
  if (workbenchKeyDown(e)) return;
  if (searchKeyDown(e)) return;
  if (shortcutMatches(e, getShortcut("palette"))) {
    e.preventDefault();
    openPalette();
    return;
  }
  if (shortcutMatches(e, getShortcut("chat"))) {
    e.preventDefault();
    document.querySelectorAll(".modal-backdrop, .drawer-scrim").forEach((el) => el.remove());
    if (S.tab === "chat") { focusChat(); return; }
    navigate("chat");
    setTimeout(focusChat, 80);
    return;
  }
  // Also from a text field: Alt+R types nothing, and a field being edited
  // holds the repaint back until it is done (refreshFromRemote).
  if (shortcutMatches(e, getShortcut("sync"))) {
    e.preventDefault();
    requestSync();
    return;
  }
  if (e.key === "Escape") {
    const palette = document.querySelector(".palette-backdrop");
    if (palette) { palette.remove(); return; }
    // Let modal dialogs consume Escape themselves.
    if (document.querySelector(".modal-backdrop")) return;
    const floating = document.querySelector("#overlay-root .drawer.floating, #overlay-root .drawer:not(.docked)");
    if (floating) {
      e.preventDefault();
      closeInspector(ctx);
      return;
    }
    return;
  }
  const target = e.target;
  const typing = target && (["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName) || target.isContentEditable);
  if (typing) return;
  if (shortcutMatches(e, getShortcut("newUnit"))) {
    e.preventDefault();
    openCreateUnit(ctx, {});
  }
}

// ------------------------------------------------------------------ palette
function focusChat() {
  const input = document.querySelector(".chat-input");
  if (input) {
    input.focus();
    input.setSelectionRange?.(input.value.length, input.value.length);
  }
}

let paletteEl = null;
function openPalette() {
  if (paletteEl) return;
  const commands = [
    { id: "overview", label: t("palette.cmd.overview"), cat: t("palette.commands"), run: () => navigate("overview") },
    { id: "ideas", label: t("palette.cmd.ideas"), cat: t("palette.commands"), run: () => navigate("ideas") },
    { id: "sync", label: t("sync.now"), cat: t("palette.commands"), run: () => requestSync() },
    { id: "plan", label: t("palette.cmd.plan"), cat: t("palette.commands"), run: () => navigate("plan", "story") },
    { id: "map", label: t("palette.cmd.map"), cat: t("palette.commands"), run: () => navigate("map") },
    { id: "tasks", label: t("palette.cmd.tasks"), cat: t("palette.commands"), run: () => navigate("tasks") },
    { id: "production", label: t("palette.cmd.production"), cat: t("palette.commands"), run: () => navigate("production") },
    { id: "distribution", label: t("palette.cmd.distribution"), cat: t("palette.commands"), run: () => navigate("distribution") },
    { id: "chat", label: t("palette.cmd.chat"), cat: t("palette.commands"), run: () => { navigate("chat"); setTimeout(focusChat, 60); } },
    { id: "settings", label: t("palette.cmd.settings"), cat: t("palette.commands"), run: () => navigate("settings") },
    { id: "about", label: t("about.button"), cat: t("palette.commands"), run: () => openSettingsAt("about") },
    { id: "editor", label: t("palette.cmd.editor"), cat: t("palette.commands"), run: () => navigateWorkspace("editor") },
    { id: "editor-open", label: t("palette.cmd.editor_open"), cat: t("palette.commands"), run: () => { navigateWorkspace("editor"); setTimeout(focusQuickOpen, 120); } },
    { id: "new", label: t("palette.cmd.new"), cat: t("palette.commands"), run: () => openCreateUnit(ctx, {}) },
    { id: "import", label: t("palette.cmd.import"), cat: t("palette.commands"), run: () => openImportModal(ctx) },
    { id: "assistant", label: t("palette.cmd.assistant"), cat: t("palette.commands"), run: () => openAssistant(ctx) },
    { id: "sessions-view", label: t("palette.cmd.sessions_view"), cat: t("palette.commands"), run: () => openSessionSidebar() },
    { id: "theme", label: t("palette.cmd.theme"), cat: t("palette.commands"), run: () => { toggleTheme(); renderTopbar(); } },
    { id: "lang", label: t("palette.cmd.lang"), cat: t("palette.commands"), run: () => changeLanguage(getLang() === "zh" ? "en" : "zh") },
    { id: "export", label: t("palette.cmd.export"), cat: t("palette.commands"), run: () => openExport() },
    ...presetCommands(t("workbench.preset.menu")),
    ...projectMoveCommands(ctx),
  ];
  let items = commands.map((c) => ({ type: "command", ...c }));
  let active = 0;
  const input = h("input", { placeholder: t("palette.placeholder"), autofocus: true });
  const list = h("div", { class: "palette-list" });
  const backdrop = h("div", { class: "palette-backdrop", onclick: (e) => { if (e.target === backdrop) close(); } });
  const box = h("div", { class: "palette-box", onclick: (e) => e.stopPropagation() },
    h("div", { class: "palette-input" }, icon("search", "sm"), input, h("span", { class: "kbd" }, "Esc")),
    list,
    h("div", { class: "palette-foot" }, h("span", null, t("palette.hint")), h("span", { style: { marginLeft: "auto" } }, "ARK Studio")));
  backdrop.appendChild(box);
  document.getElementById("overlay-root").appendChild(backdrop);
  paletteEl = backdrop;
  backdrop.closePalette = () => close();

  const render = () => {
    clear(list);
    if (!items.length) {
      list.appendChild(h("div", { class: "empty sm", style: { border: "0" } }, t("palette.empty")));
      return;
    }
    items.slice(0, 40).forEach((item, index) => {
      list.appendChild(h("div", {
        class: `palette-item ${index === active ? "on" : ""}`.trim(),
        // A unit result has the node menu (unit-menu.js), Locate in map included.
        dataset: item.type === "unit" ? { unitRef: item.unit.uid } : {},
        // Only move the highlight: re-rendering here would replace the row under
        // the pointer between mousedown and mouseup, so the click never fired.
        onmouseenter: (e) => {
          if (active === index) return;
          active = index;
          for (const row of list.children) row.classList.remove("on");
          e.currentTarget.classList.add("on");
        },
        onclick: () => choose(item),
      },
        item.type === "unit" ? typeBadge(item.unit.type) : icon("command", "sm"),
        h("span", { class: "t" }, item.type === "unit" ? `${item.unit.uid} · ${item.unit.title}` : item.label),
        h("span", { class: "cat" }, item.type === "unit" ? t(`type.${item.unit.type}`) : item.cat)));
    });
  };
  let timer = null;
  input.addEventListener("input", () => {
    clearTimeout(timer);
    timer = setTimeout(async () => {
      const q = input.value.trim();
      if (!q) { items = commands.map((c) => ({ type: "command", ...c })); active = 0; render(); return; }
      const matches = commands.filter((c) => c.label.toLowerCase().includes(q.toLowerCase()));
      try {
        const data = await api.units({ q, limit: 8, facets: 0 });
        items = [...matches.map((c) => ({ type: "command", ...c })), ...data.items.map((u) => ({ type: "unit", unit: u, label: u.title }))];
      } catch {
        items = matches.map((c) => ({ type: "command", ...c }));
      }
      active = 0;
      render();
    }, 160);
  });
  input.addEventListener("keydown", (e) => {
    if (e.key === "ArrowDown") { e.preventDefault(); active = Math.min(items.length - 1, active + 1); render(); }
    else if (e.key === "ArrowUp") { e.preventDefault(); active = Math.max(0, active - 1); render(); }
    else if (e.key === "Enter") { e.preventDefault(); if (items[active]) choose(items[active]); }
    else if (e.key === "Escape") { e.preventDefault(); close(); }
    else if (items[active]?.type === "unit" && isLocateShortcut(e)) { e.preventDefault(); e.stopPropagation(); close(); locateInMap(items[active].unit.uid); }
  });
  function choose(item) {
    close();
    if (item.type === "unit") ctx.openUnit(item.unit.uid);
    else item.run?.();
  }
  function close() {
    backdrop.remove();
    paletteEl = null;
    document.removeEventListener("keydown", onEsc);
  }
  function onEsc(e) { if (e.key === "Escape") close(); }
  document.addEventListener("keydown", onEsc);
  render();
  setTimeout(() => input.focus(), 20);
}

// ------------------------------------------------------------------- export
function openExport() {
  const options = [
    ["json", "JSON", t("app.export.json_desc")],
    ["markdown", "Markdown", t("app.export.markdown_desc")],
    ["csv", "CSV", t("app.export.csv_desc")],
    ["svg", "SVG", t("app.export.svg_desc")],
  ];
  const body = h("div", { class: "stack", style: { gap: "7px" } }, options.map(([format, label, desc]) => h("button", {
    class: "project-row", style: { textAlign: "left" }, onclick: () => { window.location.href = exportUrl(format, S.project?.id || ""); modal.close(); },
  }, h("div", { class: "main" }, h("div", { class: "t" }, label), h("div", { class: "d" }, desc)), icon("download", "sm"))));
  const modal = openModal({
    title: t("action.export"),
    body,
    actions: [{ label: t("action.close"), ghost: true }],
  });
}
