import { getLang, setLang } from "./i18n.js";
import { createFilterStore, normalizeAssigneeValues } from "./filter-store.js";
import { applyAppearance } from "./appearance.js";

const queryTheme = new URLSearchParams(location.search).get("theme");
const storedTheme = queryTheme || localStorage.getItem("ark.theme");
const theme = storedTheme === "dark" ? "dark" : "light";
export const FILTER_MODEL_VERSION = 3;

document.documentElement.dataset.theme = theme;
applyAppearance();
document.documentElement.lang = getLang() === "zh" ? "zh-CN" : "en";
document.documentElement.dataset.lang = getLang();

export const S = {
  booted: false,
  loading: false,
  error: "",
  project: null,
  projects: [],
  vocab: null,
  stats: null,
  activity: [],
  nextActions: [],
  needsYou: [],
  dueTasks: [],
  sort: "updated",
  updatedAt: null,
  tab: "overview",
  workspace: "home",
  sub: "story",
  view: "list",
  taskView: "sessions",
  taskSessions: [],
  sessionGroups: [],
  sessionsLoaded: false,
  selectedSessionId: null,
  sessionQuery: "",
  sessionFilter: "all",
  sessionGroupBy: "status",
  sessionRailTab: "overview",
  sessionThreadMode: "thread",
  filters: emptyFilters(),
  filterTab: "ark",
  infoHubFilters: { q: "", kinds: ["topic"], tags: [], status: "", days: 3, mine: false, roots: false, context: false, connected: false, order: "activity" },
  infoHubPath: localStorage.getItem("ark.infoHubPath") || "",
  infoHubData: null,
  infoHubGraphVisible: localStorage.getItem("ark.infoHubGraph") === "1",
  infoHubMaxDays: 365,
  units: [],
  total: 0,
  facets: { types: {}, statuses: {}, importance: {}, namespaces: {} },
  graph: { nodes: [], edges: [] },
  graphIndex: null,
  graphLimit: Math.max(1, Math.min(Number(new URLSearchParams(location.search).get("graphLimit")) || 700, 10000)),
  graphBase: null,
  graphBaseKey: "",
  unitsBase: null,
  unitsBaseKey: "",
  timeline: [],
  selectedId: null,
  detail: null,
  drawerOpen: false,
  drawerAnchor: null,
  filtersVisible: localStorage.getItem("ark.filtersVisible") !== "0",
  inspectorPane: localStorage.getItem("ark.inspectorPane") !== "0",
  sessionDock: localStorage.getItem("ark.sessionDock") === "1",
  railWidth: Number(localStorage.getItem("ark.railWidth")) || 288,
  inspectorWidth: Number(localStorage.getItem("ark.inspectorWidth")) || 430,
  sessionHeight: Number(localStorage.getItem("ark.sessionHeight")) || 260,
  inspectorPinned: false,
  assistantOpen: false,
  railOpen: false,
  paletteOpen: false,
  stale: true,
  theme,
  lang: getLang(),
  ganttGroup: "type",
  ganttZoom: 1,
  lastRoute: "",
};

export function emptyFilters() {
  return {
    v: FILTER_MODEL_VERSION,
    q: "",
    types: [],
    typesNone: false,
    tags: [],
    tagsUnset: [],
    statuses: [],
    statusesNone: false,
    importanceLevels: [],
    importanceNone: false,
    time: "",
    timeMode: "all",
    recentDays: 7,
    from: "",
    to: "",
    roots: false,
    context: false,
    assignees: [],
  };
}

/**
 * Normalize a filter object from an older tab/session or an API-shaped payload.
 * The v1 model had no tagMode; it inherited SQL's AND semantics.  New UI filters
 * default to "any", while the backend keeps "all" as the legacy default.
 */
export function normalizeFilters(raw = {}) {
  const out = emptyFilters();
  if (!raw || typeof raw !== "object") return out;
  out.q = String(raw.q || "").trim();
  out.types = Array.isArray(raw.types) ? [...new Set(raw.types.map(String).filter(Boolean))] : [];
  out.typesNone = Boolean(raw.typesNone ?? raw.types_none);
  out.tags = Array.isArray(raw.tags) ? [...new Set(raw.tags.map(String).filter(Boolean))] : [];
  out.tagsUnset = Array.isArray(raw.tagsUnset ?? raw.tags_unset)
    ? [...new Set((raw.tagsUnset ?? raw.tags_unset).map(String).filter(Boolean))]
    : [];
  const legacyStatus = raw.status ? [String(raw.status)] : [];
  out.statuses = Array.isArray(raw.statuses) ? [...new Set(raw.statuses.map(String).filter(Boolean))] : legacyStatus;
  out.statusesNone = Boolean(raw.statusesNone ?? raw.statuses_none);
  const legacyImportance = raw.importance && typeof raw.importance === "string" ? [raw.importance] : [];
  out.importanceLevels = Array.isArray(raw.importanceLevels)
    ? [...new Set(raw.importanceLevels.map(String).filter(Boolean))]
    : Array.isArray(raw.importance)
      ? [...new Set(raw.importance.map(String).filter(Boolean))]
      : legacyImportance;
  out.importanceNone = Boolean(raw.importanceNone ?? raw.importance_none);
  out.time = ["week", "month", "dates"].includes(raw.time) ? raw.time : "";
  const dateTime = (value) => {
    const text = String(value || "").trim();
    return /^\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?)?$/.test(text)
      && !Number.isNaN(Date.parse(text)) ? text : "";
  };
  out.from = dateTime(raw.from);
  out.to = dateTime(raw.to);
  const timeMode = String(raw.timeMode ?? raw.time_mode ?? "").toLowerCase();
  out.timeMode = ["all", "timeline", "recent", "overdue"].includes(timeMode)
    ? timeMode
    : (out.from || out.to ? "timeline" : "all");
  const recentDays = Number(raw.recentDays ?? raw.recent_days ?? 7);
  out.recentDays = [1, 3, 7, 30].includes(recentDays) ? recentDays : 7;
  out.roots = Boolean(raw.roots ?? raw.top_level ?? raw.topLevel);
  out.context = Boolean(raw.context ?? raw.linked_context ?? raw.linkedContext);
  out.assignees = normalizeAssigneeValues(raw.assignees ?? raw.assignee);
  return out;
}

export const VIEW_BY_TAB = {
  overview: "list",
  ideas: "graph",
  "plan/story": "outline",
  "plan/related": "list",
  "plan/method": "list",
  "plan/experiment": "list",
  "plan/topic": "canvas",
  map: "graph",
  tasks: "sessions",
  production: "list",
  distribution: "gantt",
  chat: "chat",
  settings: "settings",
};

const TOPIC_TYPES = ["idea", "question", "claim", "evidence", "story", "related_work", "topic", "method", "experiment",
  "run", "task", "milestone", "artifact", "resource", "agent", "host", "production", "outreach", "decision", "source", "note"];

export const TAB_TYPES = {
  overview: [],
  ideas: ["idea", "source", "question"],
  "plan/story": TOPIC_TYPES,
  "plan/related": TOPIC_TYPES,
  "plan/method": TOPIC_TYPES,
  "plan/experiment": TOPIC_TYPES,
  "plan/topic": TOPIC_TYPES,
  map: ["idea", "question", "claim", "evidence", "story", "topic", "method", "experiment", "task", "decision", "artifact", "resource"],
  tasks: ["task", "milestone", "agent", "host", "artifact", "resource"],
  production: ["production", "artifact", "resource"],
  distribution: ["outreach", "milestone", "production"],
  chat: [],
  settings: [],
};

export const PLAN_SUBS = ["story", "related", "method", "experiment", "topic"];

export function routeKey(tab = S.tab, sub = S.sub) {
  return tab === "plan" ? `plan/${sub}` : tab;
}

export function baseTypes(tab = S.tab, sub = S.sub) {
  return TAB_TYPES[routeKey(tab, sub)] || [];
}

export function selectedTypes(filters = S.filters, base = baseTypes()) {
  if (filters.typesNone) return [];
  return filters.types.length ? filters.types : base;
}

export function toggleTypeFilter(filters, type, base = baseTypes()) {
  const selected = selectedTypes(filters, base);
  const next = selected.includes(type) ? selected.filter((value) => value !== type) : [...selected, type];
  filters.typesNone = next.length === 0;
  // Keep [] as the legacy unrestricted scope, including imported context in Ideas.
  filters.types = base.every((value) => next.includes(value)) ? [] : next;
}

// Filters are remembered per project and tab (see filter-store.js for the
// localStorage layout).  `filterScope` records which project/tab the live
// S.filters belong to, so a project switch never saves them under the new one.
function browserStorage() {
  try { return typeof localStorage === "undefined" ? null : localStorage; } catch { return null; }
}
const filterStore = createFilterStore({
  storage: browserStorage(), empty: emptyFilters, normalize: normalizeFilters, version: FILTER_MODEL_VERSION,
});
let filterScope = null;

function currentScope(tab = S.tab, sub = S.sub) {
  return { project: S.project?.id ?? S.project?.slug ?? "", tab: routeKey(tab, sub) };
}

/** Save the live filters for the tab they belong to (call after every change). */
export function persistFilters() {
  if (filterScope) filterStore.save(filterScope.project, filterScope.tab, S.filters);
}

export function setTab(tab, sub = null) {
  persistFilters();
  S.tab = tab;
  if (tab === "plan") S.sub = PLAN_SUBS.includes(sub) ? sub : S.sub || "story";
  S.view = VIEW_BY_TAB[routeKey(tab, S.sub)] || "list";
  filterScope = currentScope();
  S.filters = filterStore.load(filterScope.project, filterScope.tab);
  S.railOpen = false;
}

const scopeNow = () => filterScope || currentScope();

/** Reset this tab's filters to its default: saved user default, else built-in. */
export function resetFilters() {
  const scope = scopeNow();
  S.filters = filterStore.resolveDefault(scope.project, scope.tab);
  persistFilters();
  return S.filters;
}

export function saveFiltersAsDefault() {
  const scope = scopeNow();
  filterStore.setDefault(scope.project, scope.tab, S.filters);
}

/** Forget the saved default; the built-in emptyFilters() applies again. */
export function clearFilterDefault() {
  const scope = scopeNow();
  filterStore.clearDefault(scope.project, scope.tab);
}

export function hasFilterDefault() {
  const scope = scopeNow();
  return filterStore.hasSavedDefault(scope.project, scope.tab);
}

/** Filter keys that differ from this tab's default (empty = at default). */
export function filterDiffFromDefault(filters = S.filters) {
  const scope = scopeNow();
  return filterStore.diffFromDefault(scope.project, scope.tab, filters);
}

export function filtersDifferFromDefault(filters = S.filters) {
  return filterDiffFromDefault(filters).length > 0;
}

function syncAndroidTheme() {
  try {
    const bridge = window.ARKAndroidTheme;
    if (!bridge?.setTheme) return;
    const bg = getComputedStyle(document.documentElement)
      .getPropertyValue("--bg").trim();
    bridge.setTheme(S.theme, bg);
  } catch { /* browser / bridge unavailable */ }
}

// Theme/language changes are announced so profile.js can persist them to the
// signed-in account (localStorage stays the fast-path fallback).
function announcePref(detail) {
  try { window.dispatchEvent(new CustomEvent("ark:pref-change", { detail })); } catch { /* non-browser */ }
}

export function setTheme(theme, { announce = true } = {}) {
  S.theme = theme === "light" ? "light" : "dark";
  document.documentElement.dataset.theme = S.theme;
  localStorage.setItem("ark.theme", S.theme);
  applyAppearance();
  syncAndroidTheme();
  if (announce) announcePref({ theme: S.theme });
  window.dispatchEvent(new CustomEvent("ark:theme-applied"));
}

export function toggleTheme() {
  setTheme(S.theme === "dark" ? "light" : "dark");
}

export function setLanguage(lang, { announce = true } = {}) {
  S.lang = setLang(lang);
  if (announce) announcePref({ lang: S.lang });
}

export function selectedStatuses(filters = S.filters, base = []) {
  if (filters.statusesNone) return [];
  return filters.statuses?.length ? filters.statuses : base;
}

export function selectedImportance(filters = S.filters, base = []) {
  if (filters.importanceNone) return [];
  return filters.importanceLevels?.length ? filters.importanceLevels : base;
}

export function toggleStatusFilter(filters, status, base = []) {
  const selected = selectedStatuses(filters, base);
  const next = selected.includes(status) ? selected.filter((value) => value !== status) : [...selected, status];
  filters.statusesNone = next.length === 0;
  filters.statuses = base.every((value) => next.includes(value)) ? [] : next;
}

export function toggleImportanceFilter(filters, importance, base = []) {
  const selected = selectedImportance(filters, base);
  const next = selected.includes(importance) ? selected.filter((value) => value !== importance) : [...selected, importance];
  filters.importanceNone = next.length === 0;
  filters.importanceLevels = base.every((value) => next.includes(value)) ? [] : next;
}

export function activeFilterCount(filters = S.filters) {
  return (filters.q ? 1 : 0) + (filters.typesNone ? 1 : filters.types.length)
    + filters.tags.length + (filters.tagsUnset?.length || 0)
    + (filters.statusesNone ? 1 : (filters.statuses?.length || 0))
    + (filters.importanceNone ? 1 : (filters.importanceLevels?.length || 0))
    + (filters.timeMode === "timeline" && (filters.from || filters.to) ? 1 : 0)
    + (filters.timeMode === "recent" ? 1 : 0)
    + (filters.timeMode === "overdue" ? 1 : 0)
    + (filters.time && filters.timeMode === "all" ? 1 : 0)
    + (filters.roots ? 1 : 0) + (filters.context ? 1 : 0)
    + (filters.assignees?.length || 0);
}

export function filtersToParams(filters = S.filters, extra = {}) {
  const params = { ...extra };
  if (filters.q) params.q = filters.q;
  if (filters.types?.length) params.types = filters.types;
  if (filters.typesNone) params.types_none = "1";
  if (filters.tags?.length) params.tags = filters.tags;
  if (filters.tagsUnset?.length) params.tags_unset = filters.tagsUnset;
  if (filters.statusesNone) params.statuses = "__none__";
  else if (filters.statuses?.length) params.statuses = filters.statuses;
  if (filters.importanceNone) params.importance = "__none__";
  else if (filters.importanceLevels?.length) params.importance = filters.importanceLevels;
  if (filters.roots) params.roots = "1";
  if (filters.assignees?.length) params.assignee = filters.assignees;
  if (filters.timeMode === "timeline") {
    if (filters.from) params.from = filters.from;
    if (filters.to) params.to = filters.to;
  }
  if (filters.timeMode === "recent") {
    const days = [1, 3, 7, 30].includes(Number(filters.recentDays)) ? Number(filters.recentDays) : 7;
    const since = new Date(Date.now() - days * 86400000).toISOString();
    // Recent is based on when the unit entered ARK.  updated_at is bumped by
    // imports/migrations and made "last 1 day" return the whole project.
    params.created_since = since;
  }
  // Past its end date and not done (Home's Overdue count).
  if (filters.timeMode === "overdue") params.overdue = "1";
  if (filters.time && filters.timeMode === "all") {
    const today = new Date();
    const fmt = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
    if (filters.time === "week") {
      const start = new Date(today); start.setDate(today.getDate() - 7); params.from = fmt(start);
    } else if (filters.time === "month") {
      const start = new Date(today); start.setMonth(today.getMonth() - 1); params.from = fmt(start);
    } else if (filters.time === "dates") {
      params.has_dates = "1";
    }
  }
  return params;
}

export function filtersActive(filters = S.filters) {
  return activeFilterCount(filters) > 0;
}

// Keep the Android status/navigation bars in step with the web theme.  The
// interface is provided by MainActivity; in a normal browser this is a no-op.
if (typeof window !== "undefined") {
  window.addEventListener?.("load", syncAndroidTheme, { once: true });
  setTimeout(syncAndroidTheme, 0);
}
