// App-wide workbench shell (VS Code style) around the current workspace:
// activity bar + left sidebar, right sidebar, bottom panel and status bar.
//
// The layout itself (which view sits where, visibility, sizes, persistence)
// is the pure ./workbench-model.js.  This module renders it and gives every
// registered view one persistent element (`viewHost(id)`) that existing
// panels mount into; moving a view re-parents that element, so a panel keeps
// its state.  The shell is a desktop feature: at 980px and below (and on
// phones) the classic layout applies unchanged.

import { h, clear } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { openContextMenu } from "./context-menu.js";
import {
  WORKBENCH_STORE_KEY, VIEW_DEFAULTS, CONTAINERS, DEFAULT_SIZES, SECTION_AXIS, parseWorkbench, serializeWorkbench,
  moveViewToSection, splitView, moveSection, mergeSection, activateView, setContainerVisible, toggleContainer,
  setContainerSize, toggleMaximized, toggleSectionCollapsed, toggleSectionMaximized, setSectionSizes, equalizeSections,
  resizePair, containerOf, sectionOf, applicableViews, sectionDisplay, displayedViews, containerShown, clampSize, fitSidebars,
  normalizePresetStore, presetLayout, defaultLayout, placeView, shiftView, canShiftView, slotSpot,
  normalizeWorkbench, removeViewSlot,
} from "./workbench-model.js";

const SHELL_QUERY = "(min-width: 981px)";
const DRAG_TYPE = "text/x-ark-view";
const views = new Map();        // id -> { id, icon, titleKey, applies, el, badge }
let layout = null;
let appCtx = null;
let onChange = null;
let shell = null;               // DOM of the shell
let statusSegment = null;       // element a workspace shows in the status bar
const statusItems = new Map();  // app-wide status items (sync state), by id
let activityFoot = null;        // bottom-of-activity-bar controls (setActivityFooter)
let gitInfo = { project: "", data: null, loading: false };
let shownViews = new Set();     // views displayed after the last render

export const WORKBENCH_CHANGED = "ark:workbench-changed";

export function shellActive() {
  return Boolean(globalThis.matchMedia?.(SHELL_QUERY)?.matches);
}

const containerLabel = (name) => t(`workbench.container.${name}`);

// ------------------------------------------------------------- registry
// def: { id, applies?(S) -> bool, badge?() -> string|number, onShow?(),
//        actions?() -> [{ icon, title, onClick }], tooltip?() -> string,
//        menu?() -> context-menu items for the view's "…" menu }
// onShow runs whenever the view becomes visible (first show included).
// A hosted view never draws its own title bar or close button: the
// container header is its only chrome, and `actions` are the view-specific
// buttons shown there (VS Code view toolbar).
// `title` (a function) may replace `titleKey` (user plugins name themselves).
export function registerView(def) {
  const meta = catalog.find((item) => item.id === def.id) || VIEW_DEFAULTS.find((item) => item.id === def.id) || {};
  const existing = views.get(def.id);
  const el = existing?.el || h("div", { class: "wb-view", dataset: { view: def.id }, hidden: true });
  views.set(def.id, { ...meta, ...existing, ...def, el });
  return el;
}

// Remove a view (a disabled or removed plugin).  Its place in the layout
// stays unless `forget` (the plugin was removed).
export function unregisterView(id, { forget = false } = {}) {
  const view = views.get(id);
  if (!view) return;
  views.delete(id);
  view.el.remove();
  shownViews.delete(id);
  if (forget && layout) commit(removeViewSlot(layout, id), { reason: `remove:${id}` });
  else render();
}

// The views the layout model places (built-ins plus registered plugins,
// see view-plugins.js): a new entry gets its default place.
let catalog = VIEW_DEFAULTS;

export function setViewCatalog(list) {
  catalog = Array.isArray(list) && list.length ? list : VIEW_DEFAULTS;
  if (!layout) return;
  const next = normalizeWorkbench(layout, catalog);
  if (serializeWorkbench(next) !== serializeWorkbench(layout)) commit(next, { reason: "catalog" });
  else render();
}

export function viewHost(id) {
  return views.get(id)?.el || null;
}

// For hosts outside the shell (the classic layout) that display a view.
export function viewShown(id) {
  const view = views.get(id);
  if (view?.onShow) requestAnimationFrame(() => { try { view.onShow(); } catch { /* view errors stay local */ } });
}

function viewTitle(id) {
  const view = views.get(id);
  if (typeof view?.title === "function") return view.title();
  return view?.titleKey ? t(view.titleKey) : id;
}

function applies(id) {
  const view = views.get(id);
  if (!view) return false;
  try { return view.applies ? Boolean(view.applies(appCtx?.S || {})) : true; } catch { return false; }
}

// ---------------------------------------------------------------- state
function legacyPrefs() {
  const read = (key) => { try { return localStorage.getItem(key); } catch { return null; } };
  return {
    filtersVisible: read("ark.filtersVisible"), inspectorPane: read("ark.inspectorPane"),
    sessionDock: read("ark.sessionDock"), railWidth: read("ark.railWidth"),
    inspectorWidth: read("ark.inspectorWidth"), sessionHeight: read("ark.sessionHeight"),
  };
}

export function workbenchLayout() {
  return layout;
}

// The layout follows the account (layout-presets.js syncs it as
// prefs.workbench.layout, like the saved presets); browser storage is the
// cache and the fallback without a savable profile.
let layoutSync = null;

export function setLayoutSync(fn) {
  layoutSync = fn || null;
}

// The account's layout (on sign-in / profile load).  Applied unless it is
// what the page already shows; it is not synced back.
let pendingAccountLayout = null;
export function applyAccountLayout(raw) {
  if (!raw || typeof raw !== "object") return;
  if (!layout) { pendingAccountLayout = raw; return; }
  const next = normalizeWorkbench(raw, catalog);
  if (serializeWorkbench(next) === serializeWorkbench(layout)) return;
  commit(next, { reason: "account", sync: false });
}

function commit(next, { reason = "layout", sync = true } = {}) {
  if (!next || next === layout) return;
  const previous = layout;
  layout = next;
  try { localStorage.setItem(WORKBENCH_STORE_KEY, serializeWorkbench(layout)); } catch { /* storage disabled */ }
  if (sync) layoutSync?.(layout, reason);
  render();
  onChange?.({ previous, layout, reason });
  window.dispatchEvent(new CustomEvent(WORKBENCH_CHANGED, { detail: { reason } }));
}

// Is the container that holds `id` open (regardless of the workspace)?
export function viewContainerVisible(id) {
  const name = layout && containerOf(layout, id);
  return Boolean(name && layout.containers[name].visible);
}

export function isViewDisplayed(id) {
  if (!layout || !shellActive()) return false;
  const name = containerOf(layout, id);
  return Boolean(name && layout.containers[name].visible && displayedViews(layout, name, applies).includes(id));
}

// Show (activate + open its container) or hide (close its container) a view.
export function showView(id, visible = true) {
  if (!layout) return;
  const name = containerOf(layout, id);
  if (!name) return;
  commit(visible ? activateView(layout, id) : setContainerVisible(layout, name, false), { reason: `view:${id}` });
}

// Make `id` its section's displayed view without opening a closed
// container.  `unless`: views that may stay on top in that section (e.g. the
// Local graph sharing a section with Details keeps showing while the
// selection changes).  Nothing happens when `id` is already on screen.
export function revealView(id, { unless = [] } = {}) {
  if (!layout) return;
  const { container: name, index } = sectionOf(layout, id);
  if (!name || !layout.containers[name].visible || displayedViews(layout, name, applies).includes(id)) return;
  const info = sectionDisplay(layout, name, applies).find((item) => item.index === index);
  if (info && !info.collapsed && unless.includes(info.shown)) return;
  commit(activateView(layout, id), { reason: `view:${id}` });
}

export function toggleWorkbenchContainer(name, force = null) {
  if (!layout?.containers[name]) return;
  const next = force === null ? toggleContainer(layout, name) : setContainerVisible(layout, name, force);
  commit(next, { reason: `container:${name}` });
}

// For the top-bar toggles and the status bar: is the container open, and
// does it hold anything for the current workspace?
export function containerState(name) {
  if (!layout?.containers[name]) return { visible: false, available: false, shown: false };
  const available = applicableViews(layout, name, applies).length > 0;
  return { visible: layout.containers[name].visible, available, shown: containerShown(layout, name, applies) };
}

export function containerTitle(name) {
  return containerLabel(name);
}

// ------------------------------------------------------------ rendering
const splitKeys = (name) => (SECTION_AXIS[name] === "x"
  ? ["workbench.split_left", "workbench.split_right"] : ["workbench.split_above", "workbench.split_below"]);

function viewMenu(id, event) {
  event.preventDefault();
  const current = containerOf(layout, id);
  const { index } = sectionOf(layout, id);
  const box = layout.containers[current];
  const display = sectionDisplay(layout, current, applies);
  const stacked = display.length > 1;
  const mine = display.find((item) => item.index === index);
  const places = [];
  for (const name of CONTAINERS) {
    const target = layout.containers[name];
    const [above, below] = splitKeys(name);
    // Splitting in its own container only makes sense when something else is there.
    const canSplit = name !== current || box.sections[index].views.length > 1 || box.sections.length > 1;
    if (name !== current) {
      places.push({ label: t("workbench.move_to", { place: containerLabel(name) }), icon: `panel-${name}`,
        onClick: () => moveTo(id, name) });
    }
    if (canSplit) {
      places.push({ label: t(above, { place: containerLabel(name) }), icon: `panel-${name}`,
        onClick: () => commit(splitView(layout, id, name, 0), { reason: `split:${id}` }) });
      places.push({ label: t(below, { place: containerLabel(name) }), icon: `panel-${name}`,
        onClick: () => commit(splitView(layout, id, name, target.sections.length), { reason: `split:${id}` }) });
    }
  }
  const sectionItems = stacked && mine ? [
    { label: t(mine.collapsed ? "workbench.section_expand" : "workbench.section_collapse"), icon: mine.collapsed ? "chevron" : "chevronRight",
      onClick: () => commit(toggleSectionCollapsed(layout, current, index), { reason: "section" }) },
    { label: t(mine.maximized ? "workbench.section_restore" : "workbench.section_maximize"), icon: mine.maximized ? "minimize" : "maximize",
      onClick: () => commit(toggleSectionMaximized(layout, current, index), { reason: "section" }) },
    { label: t("workbench.section_equalize"), icon: "list",
      onClick: () => commit(equalizeSections(layout, current), { reason: "section" }) },
    { separator: true },
  ] : [];
  // Up/down in the activity bar; left/right (earlier/later) in tab strips.
  const vertical = current === "left";
  const order = [
    { label: t(vertical ? "workbench.move_up" : "workbench.move_left"), icon: vertical ? "arrowUp" : "arrowLeft",
      shortcut: vertical ? "Alt+↑" : "Alt+←", disabled: !canShiftView(layout, id, -1, applies), onClick: () => shiftAndFocus(id, -1) },
    { label: t(vertical ? "workbench.move_down" : "workbench.move_right"), icon: vertical ? "arrowDown" : "arrowRight",
      shortcut: vertical ? "Alt+↓" : "Alt+→", disabled: !canShiftView(layout, id, 1, applies), onClick: () => shiftAndFocus(id, 1) },
  ];
  openContextMenu({
    x: event.clientX, y: event.clientY,
    items: [
      { heading: viewTitle(id) },
      ...(views.get(id)?.menu?.() || []),
      ...(views.get(id)?.menu ? [{ separator: true }] : []),
      ...sectionItems,
      ...order,
      ...places,
      { separator: true },
      { label: t("workbench.hide_container", { place: containerLabel(current) }), icon: "x",
        onClick: () => toggleWorkbenchContainer(current, false) },
      { label: t("workbench.reset_views"), icon: "refresh", onClick: () => resetViews() },
    ],
  });
}

// Saved layout presets (layout-presets.js keeps them in sync with the
// account profile); "Reset layout" returns to the default preset.
let presetStore = normalizePresetStore(null);

export function setPresetStore(store) {
  presetStore = normalizePresetStore(store);
}

export function currentPresetStore() {
  return presetStore;
}

export function applyPresetLayout(id) {
  const next = presetLayout(id, presetStore, layout, catalog);
  if (next) commit(next, { reason: `preset:${id}` });
}

// "Reset view layout": the default layout (the user's default preset, else
// the built-in Default).
export function resetViews() {
  commit(defaultLayout(presetStore, layout, catalog), { reason: "reset" });
}


// ------------------------------------------------------------ drag & drop
// What is being dragged: { kind: "view", id } (an activity icon, a tab or a
// section title) or { kind: "section", container, index } (a section
// header).  Kept here because dragover cannot read the transfer data.
//
// Every target uses the same rules: the target bar is highlighted, an
// insertion line (or the "into" box) shows where the drop lands, and a
// spot that would change nothing (or is not a target) shows nothing and
// refuses the drop.  Mouse drags use HTML5 drag & drop; touch and pen use a
// long press followed by a pointer drag (touchDrag below) through the same
// planDrop/showDrop functions.
let dragging = null;

function clearDropMarks() {
  for (const el of document.querySelectorAll(".wb-drop")) el.classList.remove("wb-drop");
  if (!shell) return;
  for (const name of CONTAINERS) shell.containers[name].indicator.hidden = true;
  shell.activityLine.hidden = true;
}

function endDrag() {
  dragging = null;
  document.body.classList.remove("wb-dragging");
  clearDropMarks();
}

function startDrag(event, what) {
  event.stopPropagation();
  dragging = what;
  event.dataTransfer.setData(DRAG_TYPE, what.kind === "view" ? what.id : `${what.container}:${what.index}`);
  event.dataTransfer.effectAllowed = "move";
  document.body.classList.add("wb-dragging");
}

function dragProps(id) {
  return {
    draggable: "true",
    ondragstart: (event) => startDrag(event, { kind: "view", id }),
    ondragend: endDrag,
    onpointerdown: (event) => touchDragStart(event, { kind: "view", id }),
  };
}

// Escape cancels a drag natively (dragend fires); also clear on blur.
window.addEventListener("blur", () => { if (dragging) endDrag(); });

// Where a drop at (x, y) over container `name` lands:
//   { type: "tab", anchor, side }     - into a tab strip, before/after a tab
//   { type: "split", at, ref }        - a new section before section `at`
//   { type: "into", section }         - into a section as a tab
// `point` is a drag event or { target, clientX, clientY } (touch drags).
function dropSpot(name, point) {
  const box = shell.containers[name];
  const tab = point.target?.closest?.("[data-view-tab]");
  if (tab && box.el.contains(tab)) {
    const strip = [...(tab.closest("[role=tablist]") || tab.parentElement).querySelectorAll("[data-view-tab]")];
    const slot = slotSpot(rowItems(strip, "viewTab", "x"), dragging?.kind === "view" ? dragging.id : "", point.clientX);
    if (!slot) return null;
    const el = strip.find((item) => item.dataset.viewTab === slot.anchor) || tab;
    return { type: "tab", anchor: slot.anchor, side: slot.side, el, rect: el.getBoundingClientRect() };
  }
  const display = sectionDisplay(layout, name, applies);
  if (!display.length) return null;
  if (box.head.contains(point.target)) return { type: "into", section: display[0].index, ref: display[0], rect: box.head.getBoundingClientRect() };
  const axis = SECTION_AXIS[name];
  for (let k = 0; k < display.length; k += 1) {
    const item = display[k];
    const el = box.sections.get(item.index);
    if (!el) continue;
    const rect = el.getBoundingClientRect();
    const start = axis === "x" ? rect.left : rect.top;
    const length = axis === "x" ? rect.width : rect.height;
    const pos = (axis === "x" ? point.clientX : point.clientY) - start;
    if (pos < 0 || pos > length) continue;
    // Collapsed sections are only a header: split before/after by halves.
    const edge = item.collapsed ? 0.5 : 0.25;
    if (pos < length * edge) return { type: "split", at: item.index, ref: item, side: "before", rect };
    if (pos > length * (1 - edge)) {
      const next = layout.containers[name].sections.length > item.index + 1 ? item.index + 1 : layout.containers[name].sections.length;
      return { type: "split", at: next, ref: item, side: "after", rect };
    }
    return { type: "into", section: item.index, ref: item, rect };
  }
  const last = display[display.length - 1];
  return { type: "split", at: layout.containers[name].sections.length, ref: last, side: "after",
    rect: box.sections.get(last.index)?.getBoundingClientRect() };
}

// Icons or tabs as slotSpot() items along `axis`, in screen order.
function rowItems(els, key, axis) {
  return els.map((el) => {
    const r = el.getBoundingClientRect();
    return axis === "x" ? { id: el.dataset[key], start: r.left, end: r.right } : { id: el.dataset[key], start: r.top, end: r.bottom };
  });
}

// Activity bar: the dragged icon takes the place of the icon under the
// pointer (slotSpot), or goes after the last icon below them.
function activitySpot(point) {
  const icons = [...shell.activity.querySelectorAll("[data-view-icon]")];
  if (!icons.length) return { type: "activity", anchor: "", side: "after" };
  const slot = slotSpot(rowItems(icons, "viewIcon", "y"), dragging?.kind === "view" ? dragging.id : "", point.clientY);
  if (!slot) return null;
  return { type: "activity", ...slot, el: icons.find((el) => el.dataset.viewIcon === slot.anchor) };
}

// Same arrangement as far as the user can see?  Only the views of the
// current workspace count (moving an icon past hidden ones changes
// nothing on screen), and what is focused is ignored.
function samePlacement(a, b) {
  const seen = (layoutOf, name) => layoutOf.containers[name].sections
    .map((section) => section.views.filter((id) => applies(id)).join(" ")).filter(Boolean);
  return CONTAINERS.every((name) => seen(a, name).join(" | ") === seen(b, name).join(" | "));
}

// The layout a drop of the current drag at `spot` (in container `name`, or
// the activity bar) produces; null when it is not a valid move.
function planDrop(name, spot) {
  const what = dragging;
  if (!what || !spot || !layout) return null;
  let next = layout;
  if (spot.type === "activity") {
    next = what.kind === "view"
      ? placeView(layout, what.id, "left", spot.anchor ? { anchor: spot.anchor, side: spot.side } : {})
      : moveSection(layout, what.container, what.index, "left");
  } else if (what.kind === "view") {
    const id = what.id;
    if (spot.type === "tab") next = placeView(layout, id, name, { anchor: spot.anchor, side: spot.side });
    else if (spot.type === "split") next = splitView(layout, id, name, spot.at);
    else next = moveViewToSection(layout, id, name, spot.section);
  } else if (spot.type === "split") {
    next = moveSection(layout, what.container, what.index, name, spot.at);
  } else if (spot.type === "into") {
    next = mergeSection(layout, what.container, what.index, name, spot.section);
  } else {
    // A section dropped on a tab joins that tab's section.
    const { container, index } = sectionOf(layout, spot.anchor);
    next = container ? mergeSection(layout, what.container, what.index, container, index) : layout;
  }
  return next && next !== layout && !samePlacement(next, layout) ? next : null;
}

// The insertion line for a spot between icons or tabs (`axis` "y": a
// horizontal line across the activity bar; "x": a vertical line between tabs).
function placeLine(line, host, r, side, axis) {
  const box = host.getBoundingClientRect();
  line.hidden = false;
  line.className = `wb-drop-ind line ${axis} ${side} slot`;
  line.textContent = "";
  line.dataset.label = "";
  if (axis === "y") {
    const y = side === "before" ? r.top : r.bottom;
    Object.assign(line.style, { left: `${r.left - box.left + 4}px`, top: `${y - box.top - 1}px`, width: `${Math.max(8, r.width - 8)}px`, height: "3px" });
  } else {
    const x = side === "before" ? r.left : r.right;
    Object.assign(line.style, { left: `${x - box.left - 1}px`, top: `${r.top - box.top + 3}px`, width: "3px", height: `${Math.max(8, r.height - 6)}px` });
  }
}

// Show where the drop lands; `name` is a container or "activity".
function showDrop(name, spot) {
  clearDropMarks();
  if (!spot) return;
  if (name === "activity") {
    shell.activity.classList.add("wb-drop");
    if (spot.el) placeLine(shell.activityLine, shell.activity, spot.el.getBoundingClientRect(), spot.side, "y");
    return;
  }
  const { el, indicator } = shell.containers[name];
  el.classList.add("wb-drop");
  if (spot.type === "tab") {
    placeLine(indicator, el, spot.el.getBoundingClientRect(), spot.side, "x");
    return;
  }
  if (!spot.rect) return;
  const host = el.getBoundingClientRect();
  const r = spot.rect;
  const title = viewTitle(spot.ref.shown);
  const axis = SECTION_AXIS[name];
  indicator.hidden = false;
  indicator.className = `wb-drop-ind ${spot.type === "into" ? "into" : `line ${axis} ${spot.side}`}`;
  const style = indicator.style;
  if (spot.type === "into") {
    Object.assign(style, { left: `${r.left - host.left}px`, top: `${r.top - host.top}px`, width: `${r.width}px`, height: `${r.height}px` });
    indicator.dataset.label = t("workbench.drop_into", { view: title });
    indicator.textContent = indicator.dataset.label;
  } else if (axis === "x") {
    const x = spot.side === "before" ? r.left : r.right;
    Object.assign(style, { left: `${x - host.left - 2}px`, top: `${r.top - host.top}px`, width: "4px", height: `${r.height}px` });
    indicator.dataset.label = t(spot.side === "before" ? "workbench.drop_left" : "workbench.drop_right", { view: title });
    indicator.textContent = "";
  } else {
    const y = spot.side === "before" ? r.top : r.bottom;
    Object.assign(style, { left: `${r.left - host.left}px`, top: `${y - host.top - 2}px`, width: `${r.width}px`, height: "4px" });
    indicator.dataset.label = t(spot.side === "before" ? "workbench.drop_above" : "workbench.drop_below", { view: title });
    indicator.textContent = "";
  }
}

// The target under a point: "activity", a container name, or "".
function targetAt(el) {
  if (!shell || !el) return "";
  if (shell.activity.contains(el)) return "activity";
  return CONTAINERS.find((name) => shell.containers[name].el.classList.contains("open") && shell.containers[name].el.contains(el)) || "";
}

// One step of a drag over a target: returns the planned layout (or null)
// and paints the indicators accordingly.
function hover(name, point) {
  if (!name) { clearDropMarks(); return null; }
  const spot = name === "activity" ? activitySpot(point) : dropSpot(name, point);
  const next = planDrop(name, spot);
  showDrop(name, next ? spot : null);
  return next;
}

function dropTarget(el, name) {
  el.addEventListener("dragover", (event) => {
    if (!dragging || !event.dataTransfer.types.includes(DRAG_TYPE)) return;
    event.stopPropagation();
    if (hover(name, event)) {
      event.preventDefault();
      event.dataTransfer.dropEffect = "move";
    } else event.dataTransfer.dropEffect = "none";
  });
  // dragleave also fires between children, sometimes without a
  // relatedTarget or coordinates: only clear when the pointer really left.
  el.addEventListener("dragleave", (event) => {
    const r = el.getBoundingClientRect();
    const x = event.clientX, y = event.clientY;
    const inside = event.relatedTarget ? el.contains(event.relatedTarget)
      : !(x || y) || (x > r.left && x < r.right && y > r.top && y < r.bottom);
    if (!inside) clearDropMarks();
  });
  el.addEventListener("drop", (event) => {
    if (!dragging) return;
    event.preventDefault();
    event.stopPropagation();
    const next = hover(name, event);
    endDrag();
    if (next) commit(next, { reason: "drop" });
  });
}

// --------------------------------------------------------- touch dragging
// Touch and pen: a long press (TOUCH_HOLD ms without moving) picks the item
// up; a ghost follows the finger and the same targets and indicators apply.
// Moving before the hold elapses is a normal scroll/tap.
const TOUCH_HOLD = 350;
const TOUCH_SLOP = 8;
let touch = null;   // { what, pointerId, x, y, timer, el, ghost, active, target }

function touchDragStart(event, what) {
  if (event.pointerType === "mouse" || event.button > 0 || !shellActive()) return;
  touchCancel();
  const el = event.currentTarget;
  touch = { what, pointerId: event.pointerId, x: event.clientX, y: event.clientY, el, ghost: null, active: false, next: null };
  touch.timer = setTimeout(() => touchActivate(), TOUCH_HOLD);
  window.addEventListener("pointermove", touchMove, { passive: false });
  window.addEventListener("pointerup", touchEnd);
  window.addEventListener("pointercancel", touchCancel);
}

function touchActivate() {
  if (!touch) return;
  touch.active = true;
  dragging = touch.what;
  document.body.classList.add("wb-dragging", "wb-touch-dragging");
  const label = touch.what.kind === "view" ? viewTitle(touch.what.id) : containerLabel(touch.what.container);
  touch.ghost = h("div", { class: "wb-drag-ghost", "aria-hidden": "true" },
    touch.what.kind === "view" ? icon(views.get(touch.what.id)?.icon || "file", "sm") : null, h("span", null, label));
  document.body.appendChild(touch.ghost);
  touchPlaceGhost(touch.x, touch.y);
  navigator.vibrate?.(10);
}

function touchPlaceGhost(x, y) {
  if (touch?.ghost) Object.assign(touch.ghost.style, { left: `${x + 12}px`, top: `${y + 12}px` });
}

function touchMove(event) {
  if (!touch || event.pointerId !== touch.pointerId) return;
  if (!touch.active) {
    if (Math.hypot(event.clientX - touch.x, event.clientY - touch.y) > TOUCH_SLOP) touchCancel();
    return;
  }
  event.preventDefault();
  touchPlaceGhost(event.clientX, event.clientY);
  const el = document.elementFromPoint(event.clientX, event.clientY);
  touch.next = hover(targetAt(el), { target: el, clientX: event.clientX, clientY: event.clientY });
}

function touchEnd(event) {
  if (!touch || event.pointerId !== touch.pointerId) return;
  const { active, next } = touch;
  touchCancel();
  if (!active) return;
  // The click that follows a long press must not toggle the view.
  const swallow = (ev) => { ev.stopPropagation(); ev.preventDefault(); };
  window.addEventListener("click", swallow, { capture: true, once: true });
  setTimeout(() => window.removeEventListener("click", swallow, { capture: true }), 400);
  if (next) commit(next, { reason: "drop" });
}

function touchCancel() {
  if (!touch) return;
  clearTimeout(touch.timer);
  touch.ghost?.remove();
  const wasActive = touch.active;
  touch = null;
  window.removeEventListener("pointermove", touchMove);
  window.removeEventListener("pointerup", touchEnd);
  window.removeEventListener("pointercancel", touchCancel);
  if (wasActive) {
    document.body.classList.remove("wb-touch-dragging");
    endDrag();
  }
}

// A long press also raises the context menu on touch screens: not while
// a touch drag is under way.
window.addEventListener("contextmenu", (event) => {
  if (touch?.active || document.body.classList.contains("wb-touch-dragging")) { event.preventDefault(); event.stopPropagation(); }
}, true);

// Once a touch drag is under way the page must not scroll under it.
window.addEventListener("touchmove", (event) => { if (touch?.active) event.preventDefault(); }, { passive: false });

// The activity bar takes views (and sections) into the left sidebar, at
// the position of the insertion line.
function activityDropTarget(el) {
  el.addEventListener("dragover", (event) => {
    if (!dragging || !event.dataTransfer.types.includes(DRAG_TYPE)) return;
    if (hover("activity", event)) {
      event.preventDefault();
      event.dataTransfer.dropEffect = "move";
    } else event.dataTransfer.dropEffect = "none";
  });
  el.addEventListener("dragleave", (event) => {
    if (!el.contains(event.relatedTarget)) clearDropMarks();
  });
  el.addEventListener("drop", (event) => {
    if (!dragging) return;
    event.preventDefault();
    const next = hover("activity", event);
    endDrag();
    if (next) commit(next, { reason: "drop" });
  });
}

// ------------------------------------------------------- keyboard moves
// Alt+Arrow on a focused activity icon or tab moves the view one place
// (Up/Left earlier, Down/Right later); the context menu has the same
// commands plus "Move to …".
function moveKeyDown(event, id) {
  if (!event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
  const delta = { ArrowUp: -1, ArrowLeft: -1, ArrowDown: 1, ArrowRight: 1 }[event.key];
  if (!delta) return;
  event.preventDefault();
  event.stopPropagation();
  shiftAndFocus(id, delta);
}

function shiftAndFocus(id, delta) {
  commit(shiftView(layout, id, delta, applies), { reason: `move:${id}` });
  focusViewItem(id);
}

function focusViewItem(id) {
  requestAnimationFrame(() => {
    const sel = `[data-view-icon="${CSS.escape(id)}"], [data-view-tab="${CSS.escape(id)}"]`;
    shell?.activity.querySelector(sel)?.focus() || document.querySelector(`.wb-container ${sel}`)?.focus();
  });
}

function moveTo(id, name) {
  commit(placeView(layout, id, name), { reason: `move:${id}` });
  focusViewItem(id);
}

function activityButton(id, active) {
  const view = views.get(id);
  const badge = view.badge?.();
  return h("button", {
    class: `wb-act ${active ? "on" : ""}`.trim(), type: "button", title: viewTooltip(id), "aria-label": viewTitle(id),
    "aria-pressed": active ? "true" : "false", dataset: { viewIcon: id },
    onclick: () => {
      const box = layout.containers.left;
      if (active && box.visible) toggleWorkbenchContainer("left", false);
      else commit(activateView(layout, id), { reason: `view:${id}` });
    },
    oncontextmenu: (event) => viewMenu(id, event),
    onkeydown: (event) => moveKeyDown(event, id),
    ...dragProps(id),
  }, icon(view.icon || "file", "sm"), badge ? h("span", { class: "wb-badge" }, String(badge)) : null);
}

function viewTooltip(id) {
  const extra = views.get(id)?.tooltip?.();
  return extra ? `${viewTitle(id)} · ${extra}` : viewTitle(id);
}

function viewTab(sectionIndex, id, active) {
  return h("button", {
    class: `wb-tab ${active ? "on" : ""}`.trim(), type: "button", role: "tab",
    "aria-selected": active ? "true" : "false", dataset: { viewTab: id, section: String(sectionIndex) }, title: viewTooltip(id),
    onclick: (event) => { event.stopPropagation(); commit(activateView(layout, id), { reason: `view:${id}` }); },
    oncontextmenu: (event) => { event.stopPropagation(); viewMenu(id, event); },
    onkeydown: (event) => moveKeyDown(event, id),
    ...dragProps(id),
  }, icon(views.get(id)?.icon || "file", "sm"), h("span", null, viewTitle(id)));
}

function actionButton(title, iconName, onClick) {
  return h("button", {
    class: "icon-btn", type: "button", title, "aria-label": title,
    onclick: (event) => { event.stopPropagation(); onClick(event); },
  }, icon(iconName, "sm"));
}

// The shown view's own buttons plus its "…" menu.
function viewActions(shownId) {
  const out = [];
  for (const action of (shownId && views.get(shownId)?.actions?.()) || []) {
    out.push(h("button", {
      class: "icon-btn wb-view-action", type: "button", title: action.title, "aria-label": action.title,
      onclick: (event) => { event.stopPropagation(); action.onClick?.(event); },
    }, icon(action.icon || "more", "sm")));
  }
  if (shownId) {
    out.push(actionButton(t("workbench.view_menu"), "more", (event) => {
      const rect = event.currentTarget.getBoundingClientRect();
      viewMenu(shownId, { preventDefault() {}, clientX: rect.left, clientY: rect.bottom + 2 });
    }));
  }
  return out;
}

// Container-level buttons: maximize (bottom panel) and hide.
function containerActions(name) {
  const out = [];
  if (name === "bottom") {
    const max = Boolean(layout.containers.bottom.maximized);
    out.push(actionButton(t(max ? "workbench.restore" : "workbench.maximize"), max ? "minimize" : "maximize",
      () => commit(toggleMaximized(layout, "bottom"), { reason: "maximize" })));
  }
  out.push(actionButton(t("workbench.hide_container", { place: containerLabel(name) }), "x",
    () => toggleWorkbenchContainer(name, false)));
  return out;
}

function resizer(name) {
  const handle = h("div", { class: `wb-resizer ${name}`, title: t("workbench.resize"), "aria-hidden": "true" });
  handle.addEventListener("dblclick", () => commit(setContainerSize(layout, name, DEFAULT_SIZES[name]), { reason: "resize" }));
  handle.addEventListener("pointerdown", (event) => {
    event.preventDefault();
    const start = name === "bottom" ? event.clientY : event.clientX;
    // Start from the drawn width (a narrow window may draw it below its size).
    const drawn = name === "bottom" ? 0 : Math.round(shell.containers[name]?.el.getBoundingClientRect().width || 0);
    const startSize = drawn || layout.containers[name].size;
    let size = startSize;
    document.body.classList.add("wb-resizing");
    const move = (ev) => {
      const now = name === "bottom" ? ev.clientY : ev.clientX;
      const delta = name === "left" ? now - start : start - now;
      size = clampSize(name, startSize + delta);
      document.documentElement.style.setProperty(`--wb-${name === "bottom" ? "bottom-h" : `${name}-w`}`, `${size}px`);
    };
    const up = () => {
      document.body.classList.remove("wb-resizing");
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      commit(setContainerSize(layout, name, size), { reason: "resize" });
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  });
  return handle;
}

// The splitter on the leading edge of a section: resizes it against the
// previous expanded section.  Sizes of every expanded section are taken
// from the screen so the stored weights match what the user sees.
function sashDown(name, index, event) {
  event.preventDefault();
  event.stopPropagation();
  const axis = SECTION_AXIS[name];
  const expanded = sectionDisplay(layout, name, applies).filter((item) => !item.collapsed).map((item) => item.index);
  const j = expanded.indexOf(index);
  if (j <= 0) return;
  const els = expanded.map((i) => shell.containers[name].sections.get(i));
  const startSizes = els.map((el) => { const r = el.getBoundingClientRect(); return axis === "x" ? r.width : r.height; });
  const start = axis === "x" ? event.clientX : event.clientY;
  let sizes = startSizes;
  document.body.classList.add("wb-resizing", `wb-resizing-${axis}`);
  const move = (ev) => {
    sizes = resizePair(startSizes, j - 1, j, (axis === "x" ? ev.clientX : ev.clientY) - start);
    els.forEach((el, k) => { el.style.flexGrow = String(sizes[k]); });
  };
  const up = () => {
    document.body.classList.remove("wb-resizing", `wb-resizing-${axis}`);
    window.removeEventListener("pointermove", move);
    window.removeEventListener("pointerup", up);
    const patch = {};
    expanded.forEach((i, k) => { patch[i] = sizes[k]; });
    commit(setSectionSizes(layout, name, patch), { reason: "resize" });
  };
  window.addEventListener("pointermove", move);
  window.addEventListener("pointerup", up);
}

function buildSection(name) {
  const sash = h("div", { class: "wb-sash", title: t("workbench.section_resize"), "aria-hidden": "true" });
  const head = h("div", { class: "wb-shead" });
  const body = h("div", { class: "wb-body wb-sbody" });
  const el = h("section", { class: "wb-section" }, sash, head, body);
  sash.addEventListener("pointerdown", (event) => sashDown(name, Number(el.dataset.section), event));
  sash.addEventListener("dblclick", (event) => { event.stopPropagation(); commit(equalizeSections(layout, name), { reason: "resize" }); });
  head.addEventListener("click", () => commit(toggleSectionCollapsed(layout, name, Number(el.dataset.section)), { reason: "section" }));
  head.addEventListener("dragstart", (event) => {
    if (event.target !== head) return;
    startDrag(event, { kind: "section", container: name, index: Number(el.dataset.section) });
  });
  head.addEventListener("dragend", endDrag);
  return { el, sash, head, body };
}

function buildShell() {
  const activityLine = h("div", { class: "wb-drop-ind", hidden: true, "aria-hidden": "true" });
  const activity = h("nav", { class: "wb-activity", "aria-label": t("workbench.activity_bar") });
  activityDropTarget(activity);
  const containers = {};
  for (const name of CONTAINERS) {
    const head = h("div", { class: "wb-head" });
    const stack = h("div", { class: `wb-stack ${SECTION_AXIS[name]}` });
    const indicator = h("div", { class: "wb-drop-ind", hidden: true, "aria-hidden": "true" });
    const el = h("aside", { class: `wb-container ${name}`, dataset: { container: name }, "aria-label": containerLabel(name) },
      head, stack, indicator, resizer(name));
    dropTarget(el, name);
    // Section elements are reused across renders (a view element is only
    // re-parented when its section really changes).
    containers[name] = { el, head, stack, indicator, sections: new Map(), pool: [] };
  }
  const status = h("footer", { class: "wb-status", "aria-label": t("workbench.status_bar") });
  document.body.append(activity, containers.left.el, containers.right.el, containers.bottom.el, status);
  return { activity, activityLine, containers, status };
}

// Reuse the section element that already hosts one of the section's views.
function sectionElements(name) {
  const box = shell.containers[name];
  const previous = [...box.stack.children].map((el) => el.__wb).filter(Boolean);
  const free = new Set(previous);
  const result = [];
  for (const section of layout.containers[name].sections) {
    let pick = null;
    for (const id of [section.active, ...section.views]) {
      const host = views.get(id)?.el.parentElement;
      const owner = previous.find((item) => item.body === host && free.has(item));
      if (owner) { pick = owner; break; }
    }
    if (!pick) pick = [...free].find((item) => !layout.containers[name].sections.some((s) => s.views.some((id) => views.get(id)?.el.parentElement === item.body))) || null;
    if (!pick) pick = box.pool.pop() || buildSection(name);
    if (!pick.el.__wb) pick.el.__wb = pick;
    free.delete(pick);
    result.push(pick);
  }
  for (const item of free) { item.el.remove(); box.pool.push(item); }
  // Order the stack (moves only what is out of place).
  result.forEach((item, k) => {
    if (box.stack.children[k] !== item.el) box.stack.insertBefore(item.el, box.stack.children[k] || null);
  });
  return result;
}

function renderContainer(name) {
  const box = layout.containers[name];
  const shellBox = shell.containers[name];
  const { el, head } = shellBox;
  const display = sectionDisplay(layout, name, applies);
  const shown = box.visible && display.length > 0;
  const stacked = display.length > 1;
  el.classList.toggle("open", shown);
  el.classList.toggle("stacked", stacked);
  el.classList.toggle("maximized", name === "bottom" && Boolean(box.maximized));
  if (name === "bottom") document.documentElement.style.setProperty("--wb-bottom-h", shown ? `${box.size}px` : "0px");
  else sideWidths[name] = shown ? box.size : 0;
  const parts = sectionElements(name);
  shellBox.sections = new Map();
  let firstVisible = true;
  box.sections.forEach((section, index) => {
    const part = parts[index];
    shellBox.sections.set(index, part.el);
    part.el.dataset.section = String(index);
    const info = display.find((item) => item.index === index);
    for (const id of section.views) {
      const view = views.get(id);
      if (!view) continue;
      if (view.el.parentElement !== part.body) part.body.appendChild(view.el);
      view.el.hidden = !info || info.collapsed || id !== info.shown;
    }
    part.el.hidden = !info;
    if (!info) return;
    part.el.classList.toggle("collapsed", info.collapsed);
    part.el.classList.toggle("first", firstVisible);
    firstVisible = false;
    part.el.style.flexGrow = info.collapsed ? "0" : String(section.size);
    // Section header (only when the container shows several sections).
    part.head.hidden = !stacked;
    clear(part.head);
    if (stacked) {
      part.head.draggable = true;
      part.head.title = viewTooltip(info.shown);
      part.head.append(
        icon(info.collapsed ? "chevronRight" : "chevron", "sm"),
        info.views.length > 1
          ? h("div", { class: "wb-tabs", role: "tablist" }, info.views.map((id) => viewTab(index, id, id === info.shown)))
          : h("div", { class: "wb-stitle" }, viewTitle(info.shown)),
        h("div", { class: "wb-head-actions" },
          ...(info.collapsed ? [] : viewActions(info.shown)),
          actionButton(t(info.maximized ? "workbench.section_restore" : "workbench.section_maximize"),
            info.maximized ? "minimize" : "maximize",
            () => commit(toggleSectionMaximized(layout, name, index), { reason: "section" }))),
      );
      part.head.oncontextmenu = (event) => viewMenu(info.shown, event);
    }
  });
  // Container header: the lone section's tabs/title, or the container name.
  clear(head);
  const lone = !stacked ? display[0] : null;
  if (stacked) {
    head.append(h("div", { class: "wb-title" }, containerLabel(name)), h("div", { class: "wb-head-actions" }, ...containerActions(name)));
  } else if (name === "left") {
    const shownId = lone?.shown || "";
    head.append(
      h("div", { class: "wb-title", title: shownId ? viewTooltip(shownId) : "", ...(shownId ? dragProps(shownId) : {}) }, shownId ? viewTitle(shownId) : containerLabel(name)),
      h("div", { class: "wb-head-actions" }, ...viewActions(shownId), ...containerActions(name)));
  } else {
    const tabs = h("div", { class: "wb-tabs", role: "tablist" },
      (lone?.views || []).map((id) => viewTab(lone.index, id, id === lone.shown)));
    head.append(tabs, h("div", { class: "wb-head-actions" }, ...viewActions(lone?.shown || ""), ...containerActions(name)));
  }
}

// Stored widths of the shown sidebars (0 = hidden); drawn narrower when the
// window cannot fit them plus MIN_CONTENT_W of workspace.
const sideWidths = { left: 0, right: 0 };

function applySideWidths() {
  const fit = fitSidebars(sideWidths.left, sideWidths.right, window.innerWidth);
  const root = document.documentElement.style;
  root.setProperty("--wb-left-w", `${fit.left}px`);
  root.setProperty("--wb-right-w", `${fit.right}px`);
}

export function render() {
  if (!layout || !shell) return;
  const on = shellActive();
  document.body.classList.toggle("wb-on", on);
  const root = document.documentElement.style;
  if (!on) {
    shownViews = new Set();
    for (const name of ["left-w", "right-w", "bottom-h"]) root.setProperty(`--wb-${name}`, "0px");
    // Classic layout: hand every view element back (hidden) so panels that
    // mounted into it can be re-mounted elsewhere by their owners.
    return;
  }
  for (const name of CONTAINERS) renderContainer(name);
  applySideWidths();
  // Tell views that just became visible (lazy loading, terminal resize).
  const nowShown = new Set(CONTAINERS.filter((name) => layout.containers[name].visible)
    .flatMap((name) => displayedViews(layout, name, applies)));
  for (const id of nowShown) {
    if (!shownViews.has(id)) {
      const view = views.get(id);
      if (view?.onShow) requestAnimationFrame(() => { try { view.onShow(); } catch { /* view errors stay local */ } });
    }
  }
  shownViews = nowShown;
  // Activity bar: the left sidebar's views for this workspace.
  clear(shell.activity);
  const leftShown = displayedViews(layout, "left", applies);
  for (const id of applicableViews(layout, "left", applies)) {
    shell.activity.appendChild(activityButton(id, layout.containers.left.visible && leftShown.includes(id)));
  }
  shell.activityLine.hidden = true;
  shell.activity.appendChild(shell.activityLine);
  if (activityFoot) shell.activity.appendChild(activityFoot);
  renderStatus();
}

// App-wide controls pinned to the bottom of the activity bar (Settings and
// the account avatar, as in VS Code).  The element is kept and updated by
// its owner (app.js); it is moved back in after every re-render.
export function setActivityFooter(el) {
  activityFoot = el || null;
  if (!shell) return;
  shell.activity.querySelector(":scope > .wb-activity-foot")?.remove();
  if (activityFoot && shellActive()) shell.activity.appendChild(activityFoot);
}

// ------------------------------------------------------------ status bar
// A workspace (the Editor) can show its own segment (file info) here.
export function setStatusSegment(el) {
  statusSegment = el || null;
  renderStatus();
}

// App-wide items on the right of the status bar (sync-status.js).  The
// element is kept and updated by its owner; null removes it.
export function setStatusItem(id, el) {
  if (el) statusItems.set(id, el);
  else statusItems.delete(id);
  renderStatus();
}

function loadGitInfo() {
  const project = String(appCtx?.S?.project?.id || "");
  if (!project || gitInfo.loading || gitInfo.project === project) return;
  gitInfo = { project, data: null, loading: true };
  import("./api.js").then(({ api }) => api.workspaceInfo())
    .then((data) => { if (gitInfo.project === project) gitInfo = { project, data, loading: false }; })
    .catch(() => { if (gitInfo.project === project) gitInfo = { project, data: null, loading: false }; })
    .finally(() => renderStatus());
}

function renderStatus() {
  if (!shell) return;
  const bar = shell.status;
  clear(bar);
  if (!shellActive()) return;
  loadGitInfo();
  const S = appCtx?.S || {};
  const left = h("div", { class: "wb-status-left" });
  if (S.project?.name) {
    left.appendChild(h("span", { class: "wb-status-item", title: gitInfo.data?.path || S.project.path || "" },
      icon("folder", "sm"), S.project.name));
  }
  const git = gitInfo.data?.git;
  if (git?.is_repo && git.branch) {
    left.appendChild(h("span", { class: "wb-status-item", title: git.root || "" }, icon("git", "sm"), git.branch));
  }
  const right = h("div", { class: "wb-status-right" });
  if (statusSegment && S.tab === "editor") right.appendChild(statusSegment);
  for (const el of statusItems.values()) right.appendChild(el);
  const toggles = h("div", { class: "wb-status-toggles" });
  for (const name of CONTAINERS) {
    const state = containerState(name);
    toggles.appendChild(h("button", {
      class: `wb-status-toggle ${state.shown ? "on" : ""}`.trim(), type: "button",
      title: state.available ? t("workbench.toggle_container", { place: containerLabel(name) }) : t("workbench.container_empty", { place: containerLabel(name) }),
      "aria-pressed": state.shown ? "true" : "false", "aria-disabled": state.available ? undefined : "true",
      onclick: () => { if (state.available) toggleWorkbenchContainer(name); },
    }, icon(`panel-${name}`, "sm")));
  }
  right.appendChild(toggles);
  bar.append(left, right);
}

// --------------------------------------------------------------- startup
export function initWorkbench(ctx, { onLayoutChange } = {}) {
  appCtx = ctx;
  onChange = onLayoutChange || null;
  let stored = null;
  try { stored = localStorage.getItem(WORKBENCH_STORE_KEY); } catch { stored = null; }
  layout = parseWorkbench(stored, legacyPrefs(), catalog);
  if (pendingAccountLayout) {
    layout = normalizeWorkbench(pendingAccountLayout, catalog);
    pendingAccountLayout = null;
    try { localStorage.setItem(WORKBENCH_STORE_KEY, serializeWorkbench(layout)); } catch { /* storage disabled */ }
  }
  if (!shell) {
    shell = buildShell();
    const query = globalThis.matchMedia?.(SHELL_QUERY);
    let fitFrame = 0;
    window.addEventListener("resize", () => {
      if (fitFrame || !shellActive()) return;
      fitFrame = requestAnimationFrame(() => { fitFrame = 0; applySideWidths(); });
    });
    query?.addEventListener?.("change", () => {
      render();
      onChange?.({ previous: layout, layout, reason: "breakpoint" });
      window.dispatchEvent(new CustomEvent(WORKBENCH_CHANGED, { detail: { reason: "breakpoint" } }));
    });
  }
  render();
  return layout;
}

// Keyboard: Ctrl+B left sidebar, Ctrl+Alt+B right sidebar, Ctrl+J bottom panel.
export function workbenchKeyDown(event) {
  if (!shellActive() || !layout) return false;
  if (!(event.ctrlKey || event.metaKey) || event.shiftKey) return false;
  const key = String(event.key || "").toLowerCase();
  let name = "";
  if (key === "b" || event.code === "KeyB") name = event.altKey ? "right" : "left";
  else if ((key === "j" || event.code === "KeyJ") && !event.altKey) name = "bottom";
  if (!name) return false;
  event.preventDefault();
  if (containerState(name).available) toggleWorkbenchContainer(name);
  return true;
}
