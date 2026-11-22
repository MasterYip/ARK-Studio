// Per-project workspace state (dependency-free; tests/js/project_state_model.test.mjs).
//
// When the person switches from project A to B and back, A comes back as
// they left it: the page (hash route) and its view, the unit open in the
// inspector and how far it was scrolled, the graph cameras and layout, the
// Runs page's session and its list filters, and the scroll positions of the
// page.  The Sessions view keeps its session per project itself
// (session-sidebar-model.js).  Filters are already kept per project and
// tab (filter-store.js); the workbench layout (panels, sizes, docks) follows
// the account across projects (workbench.js), so neither is stored here.
//
// One localStorage value per browser and account:
//   ark.projectState.v1.<user>  { v: 1, projects: { <project id>: snapshot } }
// Snapshots are written when the person leaves a project (switch, page
// hide) and, debounced, while they work.  At most MAX_PROJECTS projects
// (least recently saved dropped first) and MAX_BYTES in total.
//
// A safe start after a hang (reload-guard.js) drops the snapshot of the
// project the tab showed, so switching back cannot reopen the view that hung.

export const PROJECT_STATE_PREFIX = "ark.projectState.v1";
export const SNAPSHOT_VERSION = 1;
export const MAX_PROJECTS = 30;
export const MAX_BYTES = 64 * 1024;
export const MAX_CAMERAS = 8;
export const MAX_SCROLLS = 6;

const ROUTE_RE = /^#\/[A-Za-z0-9._~%/!$&'()*+,;=:@-]{0,400}$/;
// Hash routes never restored: one-off actions, not places.
const ONE_OFF_ROUTE = /^#\/?(join|pair)\b|[#?&]pair=/;
const LAYOUTS = new Set(["force", "tree", "mind", "node"]);

const text = (value, max) => (typeof value === "string" ? value.slice(0, max) : "");
const finite = (value, fallback = 0) => (Number.isFinite(Number(value)) ? Number(value) : fallback);
const choice = (value, max = 40) => (typeof value === "string" && /^[A-Za-z0-9_.:-]{1,80}$/.test(value) ? value.slice(0, max) : "");

export function storageKey(user = "") {
  return `${PROJECT_STATE_PREFIX}.${encodeURIComponent(String(user || "_"))}`;
}

/** A hash route worth reopening ("" for none: Home, empty, one-off links). */
export function restorableRoute(hash) {
  const value = String(hash || "");
  if (!ROUTE_RE.test(value) || ONE_OFF_ROUTE.test(value)) return "";
  if (/^#\/?(home|overview)?\/?$/.test(value)) return "";
  return value;
}

function normalizeCamera(raw) {
  if (!raw || typeof raw !== "object") return null;
  const zoom = finite(raw.zoom, NaN);
  const x = finite(raw.pan?.x, NaN), y = finite(raw.pan?.y, NaN);
  if (!(zoom > 0 && zoom < 100) || !Number.isFinite(x) || !Number.isFinite(y)) return null;
  const layout = LAYOUTS.has(raw.layout) ? raw.layout : "";
  return { zoom, pan: { x, y }, layout };
}

/** Clean a stored snapshot; null when nothing usable is left. */
export function normalizeSnapshot(raw) {
  if (!raw || typeof raw !== "object" || raw.v !== SNAPSHOT_VERSION) return null;
  const out = { v: SNAPSHOT_VERSION, at: finite(raw.at), route: restorableRoute(raw.route) };
  out.view = choice(raw.view);
  out.graphLayout = LAYOUTS.has(raw.graphLayout) ? raw.graphLayout : "";
  const unit = raw.unit && typeof raw.unit === "object" ? raw.unit : null;
  out.unit = unit && choice(String(unit.ref ?? ""), 80)
    ? { ref: choice(String(unit.ref), 80), floating: Boolean(unit.floating), scroll: Math.max(0, Math.round(finite(unit.scroll))) }
    : null;
  const s = raw.sessions && typeof raw.sessions === "object" ? raw.sessions : {};
  out.sessions = {
    selected: choice(s.selected, 80), query: text(s.query, 200), filter: choice(s.filter),
    groupBy: choice(s.groupBy), railTab: choice(s.railTab), threadMode: choice(s.threadMode),
  };
  out.cameras = {};
  for (const [key, value] of Object.entries(raw.cameras && typeof raw.cameras === "object" ? raw.cameras : {}).slice(0, MAX_CAMERAS)) {
    const camera = normalizeCamera(value);
    if (camera && typeof key === "string" && key.length <= 200) out.cameras[key] = camera;
  }
  out.scrollY = Math.max(0, Math.round(finite(raw.scrollY)));
  out.scrolls = (Array.isArray(raw.scrolls) ? raw.scrolls : []).slice(0, MAX_SCROLLS)
    .filter((item) => item && typeof item.sel === "string" && item.sel.length <= 200 && /^[a-z][a-z0-9]*(\.[A-Za-z0-9_-]+)*$/.test(item.sel))
    .map((item) => ({ sel: item.sel, index: Math.max(0, Math.round(finite(item.index))), top: Math.max(0, Math.round(finite(item.top))) }));
  return out;
}

/** The snapshot has nothing that differs from a first visit. */
export function emptySnapshot(snapshot) {
  if (!snapshot) return true;
  const s = snapshot.sessions || {};
  return !snapshot.route && !snapshot.unit && !s.selected && !s.query
    && !Object.keys(snapshot.cameras || {}).length && !snapshot.scrollY && !(snapshot.scrolls || []).length;
}

function readAll(storage, key) {
  try {
    const raw = JSON.parse(storage?.getItem(key) || "null");
    return raw && typeof raw === "object" && raw.projects && typeof raw.projects === "object" ? raw.projects : {};
  } catch { return {}; }
}

/**
 * The store: get(id), put(id, snapshot), drop(id).  `user()` names the
 * account (snapshots of another account on this browser are never read).
 */
export function createProjectStateStore({ storage = null, user = () => "", now = () => Date.now(),
  maxProjects = MAX_PROJECTS, maxBytes = MAX_BYTES } = {}) {
  const key = () => storageKey(user());
  function write(projects) {
    // Newest first; drop the oldest until both caps hold.
    let entries = Object.entries(projects).sort((a, b) => finite(b[1]?.at) - finite(a[1]?.at)).slice(0, maxProjects);
    let json = "";
    for (;;) {
      json = JSON.stringify({ v: SNAPSHOT_VERSION, projects: Object.fromEntries(entries) });
      if (json.length <= maxBytes || entries.length <= 1) break;
      entries = entries.slice(0, -1);
    }
    try { storage?.setItem(key(), json); } catch { /* storage full or disabled */ }
  }
  return {
    get(projectId) {
      if (!projectId) return null;
      return normalizeSnapshot(readAll(storage, key())[projectId]);
    },
    put(projectId, snapshot) {
      if (!projectId) return;
      const clean = normalizeSnapshot({ ...snapshot, v: SNAPSHOT_VERSION, at: now() });
      const projects = readAll(storage, key());
      if (!clean || emptySnapshot(clean)) delete projects[projectId];
      else projects[projectId] = clean;
      write(projects);
    },
    drop(projectId) {
      const projects = readAll(storage, key());
      if (!(projectId in projects)) return;
      delete projects[projectId];
      write(projects);
    },
  };
}

/**
 * What to restore for a project.  `safe`: this load is a safe start after a
 * hang (the snapshot is ignored).  Returns null for a first visit (defaults).
 */
export function restorePlan(snapshot, { safe = false } = {}) {
  if (safe || !snapshot || emptySnapshot(snapshot)) return null;
  return {
    // First paint: the route, its view and the session list state.
    route: snapshot.route || "#/home",
    view: snapshot.view,
    graphLayout: snapshot.graphLayout,
    sessions: snapshot.sessions,
    cameras: snapshot.cameras,
    // After the page painted: the inspector and the scroll positions.
    unit: snapshot.unit,
    scrollY: snapshot.scrollY,
    scrolls: snapshot.scrolls,
  };
}

/** The S fields a plan sets for the Runs page (only known values). */
export function sessionPatch(sessions = {}) {
  const patch = {};
  if (sessions.selected) patch.selectedSessionId = sessions.selected;
  if (sessions.query) patch.sessionQuery = sessions.query;
  if (sessions.filter) patch.sessionFilter = sessions.filter;
  if (sessions.groupBy) patch.sessionGroupBy = sessions.groupBy;
  if (sessions.railTab) patch.sessionRailTab = sessions.railTab;
  if (sessions.threadMode) patch.sessionThreadMode = sessions.threadMode;
  return patch;
}

/** Element descriptor for a scroll position: "div.list.wide" (tag and classes). */
export function scrollSelector(tag, classes = []) {
  const name = String(tag || "").toLowerCase();
  if (!/^[a-z][a-z0-9]*$/.test(name)) return "";
  const list = [...classes].filter((c) => /^[A-Za-z0-9_-]+$/.test(c) && !/^(on|open|active|hover|focus|loading|is-|has-)/.test(c));
  return [name, ...list.slice(0, 4)].join(".");
}

/** The cameras of one project (keys start with "<project>:"), newest list capped. */
export function projectCameras(entries, projectId) {
  const out = {};
  const prefix = `${projectId}:`;
  for (const [key, camera] of entries || []) {
    if (!String(key).startsWith(prefix)) continue;
    const clean = normalizeCamera(camera);
    if (clean) out[key] = clean;
  }
  return Object.fromEntries(Object.entries(out).slice(-MAX_CAMERAS));
}
