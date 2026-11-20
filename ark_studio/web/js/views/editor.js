// Editor workspace: VS Code style tabs for project files and node attachments.
//
// Tab bookkeeping (open/preview/close/persist/type mapping) lives in the pure
// ../editor-model.js; this module owns the DOM, file loading and saving.
// Open tabs live in the Editor's own tab bar above the viewer (VS Code
// style); the view shows the explorer, the tab bar, the active file's viewer
// and a status bar.
//
// Only project workspace text files are editable (through the existing
// /api/workspace/file read/write API with its size limit and conflict check).
// Attachments served by /api/media/{id}/raw are always read-only.
//
// Split editor: the editor area holds up to four editor groups (VS Code
// style, see ../editor-layout.js), each with its own tab bar, active and
// preview tab.  A document open in two groups shares one buffer (keyed by tab
// key), so edits, dirty state, save and revert stay in step; Preview/Source
// is per group.  Tabs are dragged onto another group's tab bar (insert) or
// onto a group's left/right/top/bottom half (split there) or centre (move).
// Below 900px only the focused group is shown, with a group switcher.
//
// The Editor also hosts what used to be Execute › Files: git diffs of
// changed files open as read-only "diff" tabs (from the Changes view), the
// project terminal is a workbench view (or a drawer in the classic layout),
// and open tabs can be grouped per task session (the explorer's scope).

import { h, clear, richText, highlightCode, fmtDateTime, typesetMermaid } from "../dom.js";
import { dirOf } from "../rich-model.js";
import { vizBlock } from "../viz.js";
import { createImageViewer, openImageLightbox } from "../image-viewer.js";
import { t, localizeServer } from "../i18n.js";
import { icon } from "../icons.js";
import { api, authenticatedUrl, mediaUrl } from "../api.js";
import { confirmDialog, toast } from "../components.js";
import { openContextMenu } from "../context-menu.js";
import { getShortcut, shortcutMatches, shortcutLabel } from "../shortcuts.js";
import { renderNodeTab, saveNodeTab, dropNodeTab } from "./node-editor.js";
import {
  registerSurface, restoreLine, rememberLine, caretLine, caretLineOf, previewAnchors, textareaAnchors,
  syncScrollEnabled, setSyncScroll,
} from "../scroll-sync.js";
import { shellActive, viewHost, showView, isViewDisplayed, setStatusSegment, viewShown } from "../workbench.js";
import { parseDiffRows, diffStat } from "../files-model.js";
import { crumbSegments, dropdownRows, moveSelection } from "../breadcrumb-model.js";
import {
  createState, openTab, pinTab, activateTab, closeTargets, dirtyAmong,
  findTab, activeTab, storeKey,
  pushRecent, viewerFor, isEditable, isTextViewer, languageFor, baseName,
  routeForKey, parseKey, makeTab, prettyJson, parseCsv, csvDelimiter, TEXT_LIMIT_BYTES,
} from "../editor-model.js";
import {
  createLayout, getGroup, groupOrder, focusGroup, updateGroup, splitGroup, removeGroup,
  moveTabToGroup, closeInGroup, setDirtyEverywhere, renameEverywhere, openInGroup, groupsWithKey,
  hasDirty, resizeSplit, equalizeSplit, joinGroups, neighbourGroup, serializeLayout, parseStoredLayout,
  isDirtyAnywhere, MAX_GROUPS,
} from "../editor-layout.js";

const SIDE_KEY = "ark.editor.side";
const EMPTY_STATE = createState();
const DRAG_TYPE = "text/x-ark-editor-tab";
const session = {
  projectKey: null,
  layout: createLayout(),
  // The focused group's tab state (most actions target it).
  get state() { return getGroup(this.layout) || EMPTY_STATE; },
  recent: [],
  buffers: new Map(),   // key -> buffer (see loadBuffer), shared by all groups
  modes: new Map(),     // `${group}|${key}` -> "preview" | "source" (per group)
  views: new Map(),     // group id -> mounted group view (see groupElement)
  structure: "",        // signature of the rendered grid
  drag: null,           // { key, group } while an editor tab is dragged
  tree: new Map(),      // dir path -> { entries, loading, error }
  expanded: new Set([""]),
  quick: "",
  quickResults: null,
  diffs: new Map(),     // diff tab key -> { loading, data, error }
  // Open tabs can be grouped per task session ("" = the project's own set).
  scope: "",
  scopeStates: new Map(),   // scope -> { layout, recent } kept while switching
  sessions: null,       // task sessions for the scope picker (lazy)
  sideMode: "files",    // classic layout: explorer column shows files | changes
  termOpen: false,      // classic layout: terminal drawer under the viewer
  // Phones start with the explorer hidden (it overlays the viewer there).
  sideOpen: localStorage.getItem(SIDE_KEY) === null ? !isPhone() : localStorage.getItem(SIDE_KEY) !== "0",
  ctx: null,
  host: null,
};
const stripHosts = new Set();

function isPhone() {
  return Boolean(globalThis.matchMedia?.("(max-width: 760px)")?.matches);
}

// Below 900px the editor shows one group at a time (no splits).
function isNarrow() {
  return Boolean(globalThis.matchMedia?.("(max-width: 899px)")?.matches);
}

// ------------------------------------------------------------ state glue
function projectKeyOf(project) {
  return String(project?.id || project?.path || project?.name || "default");
}

function ensureProject(ctx) {
  session.ctx = ctx || session.ctx;
  const key = projectKeyOf(session.ctx?.S?.project);
  if (key === session.projectKey) return;
  session.projectKey = key;
  session.scope = "";
  session.scopeStates.clear();
  session.sessions = null;
  session.diffs.clear();
  let stored = null;
  try { stored = localStorage.getItem(storeKey(key)); } catch { stored = null; }
  const { layout, recent } = parseStoredLayout(stored);
  session.layout = layout;
  session.recent = recent;
  session.buffers.clear();
  session.modes.clear();
  session.tree.clear();
  session.expanded = new Set([""]);
  session.quickResults = null;
}

function scopedStoreKey(scope = session.scope) {
  return storeKey(scope ? `${session.projectKey}::session:${scope}` : session.projectKey);
}

function persist() {
  try { localStorage.setItem(scopedStoreKey(), serializeLayout(session.layout, session.recent)); }
  catch { /* storage full or unavailable */ }
}

// Switch the open-tab set between the project and a task session (what the
// Files tab called the session workspace).  Unsaved buffers are kept in
// memory per tab, so an edit survives switching back and forth.
function switchScope(scope) {
  const next = String(scope || "");
  if (next === session.scope) return;
  persist();
  session.scopeStates.set(session.scope, { layout: session.layout, recent: session.recent });
  session.scope = next;
  const kept = session.scopeStates.get(next);
  if (kept) {
    session.layout = kept.layout;
    session.recent = kept.recent;
  } else {
    let stored = null;
    try { stored = localStorage.getItem(scopedStoreKey(next)); } catch { stored = null; }
    const parsed = parseStoredLayout(stored);
    session.layout = parsed.layout;
    session.recent = parsed.recent;
  }
  refreshStrips();
  syncRoute();
  paintSide();
  paintView();
}

async function loadScopes() {
  if (session.sessions) return;
  session.sessions = [];
  try {
    const data = await api.agentSessions();
    session.sessions = (data.sessions || []).map((item) => ({ id: item.id, title: item.title || item.id, status: item.status || "" }));
  } catch { session.sessions = []; }
  explorerSide()?.querySelector(".ed-scope-row")?.replaceWith(scopePicker());
  paintSide();
}

// `next` is the new tab state of `group` (the focused group by default).
function commit(next, { paint = true, group = session.layout.focused } = {}) {
  commitLayout(updateGroup(session.layout, group, () => next), { paint });
}

function commitLayout(next, { paint = true } = {}) {
  const previous = session.layout;
  if (next === previous) return;
  session.layout = next;
  persist();
  refreshStrips();
  syncRoute();
  if (paint) paintChanges(previous);
  else markFocus();
}

// Set a document's dirty flag in every group that shows it.
function commitDirty(key, dirty) {
  if (!groupsWithKey(session.layout, key).some((id) => {
    const tab = findTab(getGroup(session.layout, id), key);
    return Boolean(tab.dirty) !== Boolean(dirty) || (dirty && tab.preview);
  })) return;
  commitLayout(setDirtyEverywhere(session.layout, key, dirty), { paint: false });
}

function editorVisible() {
  return session.ctx?.S?.tab === "editor" && session.host?.isConnected;
}

function syncRoute() {
  if (session.ctx?.S?.tab !== "editor") return;
  const changed = session.ctx.S.editorKey !== (session.state.active || "");
  session.ctx.S.editorKey = session.state.active || "";
  // The local graph follows the node tab open in the focused group.
  if (changed) window.dispatchEvent(new CustomEvent("ark:selection"));
  const hash = routeForKey(session.state.active);
  if (location.hash !== hash) history.replaceState(null, "", hash);
}

export function editorHasDirty() {
  return hasDirty(session.layout) || [...session.scopeStates.values()].some((item) => hasDirty(item.layout));
}

// ---------------------------------------------------------- public opening
// source: { kind: "ws", path } | { kind: "media", mediaId, mediaUrl, rawUrl, name,
//           caption, unit, unitTitle } | { kind: "url", url }
// `line` (1-based) opens a text file at that line (source view, line
// selected), e.g. from a Search result.
export function openInEditor(ctx, source, { preview = false, line = 0 } = {}) {
  ensureProject(ctx);
  const tab = makeTab(source);
  if (!tab) return;
  if (Number(line) > 0) {
    session.pendingLine = { key: tab.key, line: Number(line) };
    session.modes.set(`${session.layout.focused}|${tab.key}`, "source");
  }
  if (isPhone() && session.sideOpen && session.host?.isConnected) {
    session.sideOpen = false;
    session.host.classList.add("side-closed");
  }
  const previous = session.layout;
  session.layout = openInGroup(session.layout, session.layout.focused, tab, { preview });
  session.recent = pushRecent(session.recent, tab);
  persist();
  refreshStrips();
  if (ctx.S.tab === "editor") {
    syncRoute();
    paintChanges(previous);
  } else {
    ctx.navigateWorkspace("editor", tab.key);
  }
}

// Attachment sources live in ../editor-media.js (used without the Editor).
export { mediaSource, mediaPreviewable } from "../editor-media.js";

// Callbacks the node editor (./node-editor.js) uses to talk to the tab state.
const nodeHost = {
  get ctx() { return session.ctx; },
  isActive: (key) => editorVisible() && shownGroups().some((id) => getGroup(session.layout, id)?.active === key),
  setDirty: (key, dirty) => commitDirty(key, dirty),
  rename: (key, title) => {
    if (!title || !groupsWithKey(session.layout, key).some((id) => findTab(getGroup(session.layout, id), key).title !== title)) return;
    commitLayout(renameEverywhere(session.layout, key, title), { paint: false });
  },
  pin: (key) => {
    let next = session.layout;
    for (const id of groupsWithKey(next, key)) next = updateGroup(next, id, (group) => pinTab(group, key));
    commitLayout(next, { paint: false });
  },
  open: (source, options) => openInEditor(session.ctx, source, options),
  // Repaint the groups showing node tabs (after load/save/conflict).
  repaint: () => {
    if (!editorVisible()) return;
    for (const id of shownGroups()) {
      if (activeTab(getGroup(session.layout, id))?.kind === "unit") paintGroup(id);
    }
  },
  // A node page was edited: refresh other groups showing the same node.
  edited: (key) => scheduleMirror(key),
};

// ---------------------------------------------------------------- closing
async function requestClose(action, key = session.state.active, groupId = session.layout.focused) {
  const group = getGroup(session.layout, groupId);
  if (!group) return false;
  const keys = closeTargets(group, action, key);
  if (!keys.length) return false;
  // A document still open in another group keeps its unsaved edits: only
  // closing its last tab asks.
  const dirty = dirtyAmong(group, keys).filter((tab) => groupsWithKey(session.layout, tab.key).length < 2);
  if (dirty.length) {
    const names = dirty.map((tab) => tab.title).join(", ");
    const ok = await confirmDialog(
      t(dirty.length === 1 ? "editor.confirm_close_one" : "editor.confirm_close_many",
        { name: names, count: dirty.length }),
      { title: t("editor.unsaved"), danger: true, confirmLabel: t("editor.discard_close") });
    if (!ok) return false;
  }
  const next = closeInGroup(session.layout, groupId, keys);
  for (const k of keys) {
    session.modes.delete(`${groupId}|${k}`);
    if (groupsWithKey(next, k).length) continue;
    session.buffers.delete(k);
    session.diffs.delete(k);
    dropNodeTab(k);
  }
  commitLayout(next);
  return true;
}

function activate(key, groupId = session.layout.focused) {
  const focused = focusGroup(session.layout, groupId);
  const group = getGroup(focused, groupId);
  if (!group) return;
  commitLayout(group.active === key ? focused : updateGroup(focused, groupId, (state) => activateTab(state, key)));
}

// ------------------------------------------------------------ split groups
function canSplit() {
  return !isNarrow() && session.layout.groups.length < MAX_GROUPS;
}

// Split a group, copying (or moving) one of its tabs into the new group.
function splitEditor(side = "right", { groupId = session.layout.focused, key = getGroup(session.layout, groupId)?.active || "", move = false, fromId = groupId } = {}) {
  if (!canSplit()) return;
  ensureEditorRoute();
  commitLayout(splitGroup(session.layout, groupId, side, { key, fromId, move }));
}

function moveToGroup(key, fromId, toId, index = null, { copy = false } = {}) {
  commitLayout(moveTabToGroup(session.layout, fromId, key, toId, index, { copy }));
}

async function closeGroup(groupId) {
  const group = getGroup(session.layout, groupId);
  if (!group) return;
  if (!group.tabs.length) { commitLayout(removeGroup(session.layout, groupId)); return; }
  await requestClose("all", group.active, groupId);
}

function focusGroupView(groupId) {
  if (!getGroup(session.layout, groupId) || session.layout.focused === groupId) return;
  session.layout = focusGroup(session.layout, groupId);
  persist();
  syncRoute();
  if (gridSignature() !== session.structure) paintView();
  else markFocus();
  paintSide(false);
}

function focusGroupAt(index) {
  const id = groupOrder(session.layout)[index];
  if (!id) return false;
  focusGroupView(id);
  const view = session.views.get(id);
  (view?.editor?.textarea || view?.el.querySelector(".ed-tab.on"))?.focus();
  return true;
}

// ---------------------------------------------------------------- tab strip
function tabContextMenu(event, key, groupId = session.layout.focused) {
  event.preventDefault();
  const group = getGroup(session.layout, groupId) || EMPTY_STATE;
  const tab = findTab(group, key);
  // Every bulk close goes through requestClose (unsaved tabs prompt as for a
  // single close); actions that would close nothing are disabled.
  const close = (action, label, extra = {}) => ({
    label: t(label), ...extra, disabled: !closeTargets(group, action, key).length,
    onClick: () => requestClose(action, key, groupId),
  });
  const others = groupOrder(session.layout).filter((id) => id !== groupId);
  const split = isNarrow() ? [] : [
    { separator: true },
    { label: t("editor.split_right"), icon: "panel-right", shortcut: "Ctrl+\\", disabled: !canSplit(),
      onClick: () => splitEditor("right", { groupId, key }) },
    { label: t("editor.split_down"), icon: "panel-bottom", disabled: !canSplit(),
      onClick: () => splitEditor("down", { groupId, key }) },
    ...others.map((id) => ({
      label: t("editor.move_to_group", { n: groupOrder(session.layout).indexOf(id) + 1 }), icon: "arrowRight",
      onClick: () => moveToGroup(key, groupId, id),
    })),
    !others.length ? { label: t("editor.move_to_new_group"), icon: "arrowRight", disabled: !canSplit() || group.tabs.length < 2,
      onClick: () => splitEditor("right", { groupId, key, move: true }) } : null,
  ];
  openContextMenu({
    x: event.clientX, y: event.clientY,
    items: [
      close("close", "editor.close", { icon: "x" }),
      close("others", "editor.close_others"),
      close("right", "editor.close_right"),
      close("left", "editor.close_left"),
      close("saved", "editor.close_saved"),
      close("all", "editor.close_all", { danger: true }),
      ...split,
      tab?.preview || tab?.path ? { separator: true } : null,
      tab?.preview ? { label: t("editor.keep_open"), icon: "pin", onClick: () => commit(pinTab(group, key), { group: groupId }) } : null,
      tab?.path ? { label: t("editor.copy_path"), icon: "copy", onClick: () => navigator.clipboard?.writeText(tab.path) } : null,
      { separator: true },
      syncScrollItem(),
    ].filter(Boolean),
  });
}

function tabIcon(tab) {
  const viewer = viewerFor(tab);
  if (viewer === "node") return "node";
  if (viewer === "diff") return "git";
  if (viewer === "image") return "image";
  if (viewer === "video" || viewer === "audio") return "play";
  if (viewer === "markdown") return "book";
  if (viewer === "mermaid") return "graph";
  if (viewer === "html") return "shield";
  if (viewer === "json" || viewer === "csv") return "database";
  if (viewer === "link") return "link";
  return "file";
}

function tabButton(tab, groupId) {
  const active = tab.key === getGroup(session.layout, groupId)?.active;
  const el = h("div", {
    class: `top-subtab ed-tab ${active ? "on" : ""} ${tab.preview ? "preview" : ""} ${tab.dirty ? "dirty" : ""}`
      .replace(/\s+/g, " ").trim(),
    role: "tab", tabindex: "0", draggable: "true",
    "aria-selected": active ? "true" : "false",
    "aria-current": active ? "page" : undefined,
    title: `${tab.path || tab.caption || tab.title}${tab.kind === "media" ? ` · ${t("editor.read_only")}` : ""}${tab.preview ? ` · ${t("editor.preview_tab")}` : ""}`,
    dataset: { editorKey: tab.key },
    onclick: () => { ensureEditorRoute(); activate(tab.key, groupId); },
    ondblclick: () => commit(pinTab(getGroup(session.layout, groupId), tab.key), { group: groupId }),
    onauxclick: (event) => {
      if (event.button !== 1) return;
      event.preventDefault();
      holdTabWidths(event.currentTarget);
      requestClose("close", tab.key, groupId);
    },
    // Middle-click closes; a double-click pins: neither may select text.
    onmousedown: (event) => { if (event.button === 1 || event.detail > 1) event.preventDefault(); },
    oncontextmenu: (event) => tabContextMenu(event, tab.key, groupId),
    onkeydown: (event) => {
      if (event.key === "Enter" || event.key === " ") { event.preventDefault(); ensureEditorRoute(); activate(tab.key, groupId); }
      if (event.key === "Delete") { event.preventDefault(); requestClose("close", tab.key, groupId); }
      if (["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) {
        const all = [...event.currentTarget.parentElement.querySelectorAll(".ed-tab")];
        const at = all.indexOf(event.currentTarget);
        const next = event.key === "Home" ? 0 : event.key === "End" ? all.length - 1
          : Math.max(0, Math.min(all.length - 1, at + (event.key === "ArrowLeft" ? -1 : 1)));
        if (all[next] && next !== at) { event.preventDefault(); all[next].focus(); }
      }
    },
    ondragstart: (event) => startTabDrag(event, tab.key, groupId),
    ondragend: () => endTabDrag(),
    ondragover: (event) => {
      if (!event.dataTransfer.types.includes(DRAG_TYPE)) return;
      event.preventDefault();
      event.stopPropagation();
      event.dataTransfer.dropEffect = event.ctrlKey && session.drag?.group !== groupId ? "copy" : "move";
      markStripDrop(event.currentTarget, event);
    },
    ondragleave: (event) => event.currentTarget.classList.remove("drop-before", "drop-after"),
    ondrop: (event) => {
      const drag = readDrag(event);
      event.currentTarget.classList.remove("drop-before", "drop-after");
      if (!drag) return;
      event.preventDefault();
      event.stopPropagation();
      const tabs = getGroup(session.layout, groupId)?.tabs || [];
      let index = tabs.findIndex((item) => item.key === tab.key);
      const rect = event.currentTarget.getBoundingClientRect();
      if (event.clientX > rect.left + rect.width / 2) index += 1;
      if (drag.group === groupId) {
        const from = tabs.findIndex((item) => item.key === drag.key);
        if (from < index) index -= 1;
        if (drag.key === tab.key) return;
      }
      endTabDrag();
      moveToGroup(drag.key, drag.group, groupId, index, { copy: event.ctrlKey && drag.group !== groupId });
    },
  },
    icon(tabIcon(tab), "sm"),
    h("span", { class: "ed-tab-label" }, tab.title),
    h("button", {
      class: "ed-tab-close", type: "button", tabindex: "-1",
      title: `${t("editor.close")} (${shortcutLabel(getShortcut("editorClose"))})`,
      "aria-label": `${t("editor.close")} ${tab.title}`,
      onclick: (event) => {
        event.stopPropagation();
        // Mouse clicks (detail > 0) keep the widths so the next close button
        // slides under the pointer; keyboard activation does not.
        if (event.detail > 0) holdTabWidths(el);
        requestClose("close", tab.key);
      },
    }, h("span", { class: "ed-dirty-dot", "aria-hidden": "true" }, "●"), h("span", { class: "ed-x", "aria-hidden": "true" }, "×")));
  return el;
}

// The tab strip sits in the Editor's tab bar; it re-renders itself whenever
// tab state changes.
function paintStrip(host) {
  clear(host);
  const groupId = host.dataset.group;
  const tabs = getGroup(session.layout, groupId)?.tabs || [];
  if (!tabs.length) {
    host.appendChild(h("span", { class: "ed-strip-empty" }, t("editor.no_open")));
    requestAnimationFrame(() => host.parentElement?.dispatchEvent(new Event("subnavchange")));
    return;
  }
  const hold = host.classList.contains("hold");
  tabs.forEach((tab, index) => {
    const el = tabButton(tab, groupId);
    if (hold && tab.key === host.dataset.holdActive) el.classList.add("hold-on");
    if (hold && String(index) === host.dataset.holdIndex) el.classList.add("hold-at");
    host.appendChild(el);
  });
  const active = host.querySelector(".ed-tab.on");
  requestAnimationFrame(() => {
    if (active?.isConnected) revealInStrip(active);
    // Tab count changes do not resize the scroller: refresh its fade edges.
    host.parentElement?.dispatchEvent(new Event("subnavchange"));
  });
}

function revealInStrip(el) {
  const scroller = el.closest(".ed-tabs-scroll");
  if (!scroller) return;
  const box = scroller.getBoundingClientRect();
  const rect = el.getBoundingClientRect();
  if (rect.width + 56 > box.width) scroller.scrollLeft += rect.left + rect.width / 2 - box.left - box.width / 2;
  else if (rect.left < box.left + 8) scroller.scrollLeft += rect.left - box.left - 28;
  else if (rect.right > box.right - 8) scroller.scrollLeft += rect.right - box.right + 28;
}

// Desktop tabs share the strip width (CSS flex) and re-expand as tabs close.
// Like Chrome, a mouse close freezes the current tab widths until the pointer
// leaves the tab bar, so repeated clicks close successive tabs. The (wider)
// active tab keeps its width, and the tab that slides into the clicked slot
// takes the clicked tab's width so its close button lands under the pointer.
function holdTabWidths(tabEl) {
  const host = tabEl?.closest(".ed-strip");
  const area = host?.closest(".ed-tabbar");
  if (!area) return;
  host.dataset.holdIndex = String([...host.querySelectorAll(".ed-tab")].indexOf(tabEl));
  host.style.setProperty("--ed-tab-hold-at", `${tabEl.getBoundingClientRect().width}px`);
  if (host.classList.contains("hold")) return;
  const active = host.querySelector(".ed-tab.on");
  const inactive = host.querySelector(".ed-tab:not(.on)") || tabEl;
  host.style.setProperty("--ed-tab-hold", `${inactive.getBoundingClientRect().width}px`);
  host.style.setProperty("--ed-tab-hold-on", `${(active || tabEl).getBoundingClientRect().width}px`);
  host.dataset.holdActive = active?.dataset.editorKey || "";
  host.classList.add("hold");
  active?.classList.add("hold-on");
  area.addEventListener("pointerleave", () => {
    host.classList.remove("hold");
    host.style.removeProperty("--ed-tab-hold");
    host.style.removeProperty("--ed-tab-hold-on");
    host.style.removeProperty("--ed-tab-hold-at");
    delete host.dataset.holdActive;
    delete host.dataset.holdIndex;
    for (const el of host.querySelectorAll(".hold-on, .hold-at")) el.classList.remove("hold-on", "hold-at");
  }, { once: true });
}

// Source <-> Preview scroll sync between views of the same document.
function syncScrollItem() {
  const on = syncScrollEnabled();
  return { label: t("editor.sync_scroll"), icon: "refresh", checked: on, onClick: () => setSyncScroll(!on) };
}

// Scroll-sync views of the open document per group (`${group}|${key}`).
const syncViews = new Map();

// "List all tabs" menu next to the strip (bulk closes live in the tab
// context menu).
function tabListButton(groupId) {
  return h("button", {
    class: "ed-tab-list", type: "button", title: t("editor.open_files"), "aria-label": t("editor.open_files"),
    "aria-haspopup": "menu",
    onclick: (event) => {
      const rect = event.currentTarget.getBoundingClientRect();
      openContextMenu({
        x: rect.right - 260, y: rect.bottom + 4, className: "ed-tab-menu",
        items: [...(getGroup(session.layout, groupId)?.tabs || []).map((tab) => ({
          label: tab.title, icon: tabIcon(tab), title: tab.path || tab.caption || tab.title,
          checked: tab.key === getGroup(session.layout, groupId)?.active ? true : undefined,
          hint: tab.dirty ? t("editor.unsaved") : undefined,
          onClick: () => { ensureEditorRoute(); activate(tab.key, groupId); },
        })), { separator: true }, syncScrollItem()],
      });
      const current = document.querySelector(".ed-tab-menu [aria-checked='true']");
      current?.focus();
      current?.scrollIntoView({ block: "nearest" });
    },
  }, icon("chevron", "sm"));
}

function updateScrollFades(scroller) {
  const max = scroller.scrollWidth - scroller.clientWidth;
  scroller.classList.toggle("fade-start", scroller.scrollLeft > 2);
  scroller.classList.toggle("fade-end", max - scroller.scrollLeft > 2);
}

// The Editor's tab bar: a horizontally scrolling strip (the vertical wheel
// scrolls it sideways, fade edges hint at hidden tabs) plus the all-tabs menu.
function tabBar(groupId) {
  const host = h("div", { class: "ed-strip", role: "tablist", "aria-label": t("editor.open_files"), dataset: { group: groupId } });
  const scroller = h("div", { class: "ed-tabs-scroll" }, host);
  scroller.addEventListener("wheel", (event) => {
    if (Math.abs(event.deltaY) <= Math.abs(event.deltaX)) return;
    if (scroller.scrollWidth <= scroller.clientWidth) return;
    event.preventDefault();
    scroller.scrollLeft += event.deltaY;
  }, { passive: false });
  scroller.addEventListener("scroll", () => updateScrollFades(scroller), { passive: true });
  scroller.addEventListener("subnavchange", () => updateScrollFades(scroller));
  if (typeof ResizeObserver === "function") {
    const observer = new ResizeObserver(() => {
      if (!scroller.isConnected) { observer.disconnect(); return; }
      updateScrollFades(scroller);
    });
    observer.observe(scroller);
  }
  // Dropping on the empty part of the strip appends the tab to this group.
  scroller.addEventListener("dragover", (event) => {
    if (!event.dataTransfer.types.includes(DRAG_TYPE)) return;
    event.preventDefault();
  });
  scroller.addEventListener("drop", (event) => {
    const drag = readDrag(event);
    if (!drag) return;
    event.preventDefault();
    endTabDrag();
    if (drag.group === groupId) {
      const count = getGroup(session.layout, groupId)?.tabs.length || 0;
      moveToGroup(drag.key, groupId, groupId, count - 1);
    } else moveToGroup(drag.key, drag.group, groupId, null, { copy: event.ctrlKey });
  });
  stripHosts.add(host);
  paintStrip(host);
  const order = groupOrder(session.layout);
  const first = groupId === order[0];
  // Narrow screens: one group at a time, with a switcher.
  const switcher = isNarrow() && order.length > 1 ? h("div", { class: "ed-group-switch", role: "group", "aria-label": t("editor.groups") },
    order.map((id, index) => h("button", {
      class: id === session.layout.focused ? "on" : "", type: "button", title: t("editor.focus_group", { n: index + 1 }),
      "aria-pressed": id === session.layout.focused ? "true" : "false", dataset: { groupSwitch: id },
      onclick: () => focusGroupView(id),
    }, String(index + 1)))) : null;
  // Editor title button: split right (Alt/Shift+click splits down).
  const splitBtn = isNarrow() ? null : h("button", {
    class: "icon-btn ed-split-btn", type: "button", title: `${t("editor.split_right")} (Ctrl+\\) · ${t("editor.split_down_hint")}`,
    "aria-label": t("editor.split_right"), disabled: !canSplit() || !getGroup(session.layout, groupId)?.active,
    onclick: (event) => splitEditor(event.altKey || event.shiftKey ? "down" : "right", { groupId }),
  }, icon("panel-right", "sm"));
  const closeGroupBtn = order.length > 1 && !isNarrow() ? h("button", {
    class: "icon-btn ed-group-close", type: "button", title: t("editor.close_group"), "aria-label": t("editor.close_group"),
    onclick: () => closeGroup(groupId),
  }, icon("x", "sm")) : null;
  // Classic layout (no workbench panel): the terminal opens as a drawer.
  const terminalToggle = shellActive() || (!first && !isNarrow()) ? null : h("button", {
    class: `icon-btn ed-term-toggle ${session.termOpen ? "on" : ""}`.trim(), type: "button",
    title: t("editor.terminal_toggle"), "aria-label": t("editor.terminal_toggle"),
    "aria-pressed": session.termOpen ? "true" : "false",
    onclick: () => setTerminalDrawer(!session.termOpen),
  }, icon("terminal", "sm"));
  return h("div", { class: "ed-tabbar" }, switcher, scroller, tabListButton(groupId), splitBtn, closeGroupBtn, terminalToggle);
}

// --------------------------------------------------------------- dragging
function startTabDrag(event, key, groupId) {
  event.dataTransfer.setData(DRAG_TYPE, key);
  event.dataTransfer.effectAllowed = "copyMove";
  session.drag = { key, group: groupId };
  // Let the drop layers catch the pointer over iframes/videos as well.
  requestAnimationFrame(() => session.host?.classList.add("ed-dragging"));
}

function endTabDrag() {
  session.drag = null;
  session.host?.classList.remove("ed-dragging");
  for (const el of document.querySelectorAll(".ed-drop-layer.show, .ed-tab.drop-before, .ed-tab.drop-after")) {
    el.classList.remove("show", "drop-before", "drop-after");
    delete el.dataset.zone;
  }
}

function readDrag(event) {
  const key = event.dataTransfer.getData(DRAG_TYPE);
  if (!key) return null;
  if (session.drag?.key === key) return session.drag;
  const group = groupOrder(session.layout).find((id) => findTab(getGroup(session.layout, id), key));
  return group ? { key, group } : null;
}

function markStripDrop(el, event) {
  const rect = el.getBoundingClientRect();
  const after = event.clientX > rect.left + rect.width / 2;
  el.classList.toggle("drop-after", after);
  el.classList.toggle("drop-before", !after);
}

// Which part of a group the pointer is over: an edge (split there) or the
// centre (move into the group).  Edges are the outer quarter of each side.
function dropZone(rect, x, y) {
  const fx = (x - rect.left) / Math.max(1, rect.width);
  const fy = (y - rect.top) / Math.max(1, rect.height);
  const edge = Math.min(fx, 1 - fx, fy, 1 - fy);
  if (edge > 0.25) return "center";
  if (edge === fx) return "left";
  if (edge === 1 - fx) return "right";
  return edge === fy ? "up" : "down";
}

// A transparent layer over each group's content: shows a translucent
// overlay for the half (split) or the whole group (move) under the pointer.
function dropLayer(groupId) {
  const layer = h("div", { class: "ed-drop-layer", "aria-hidden": "true" }, h("div", { class: "ed-drop-hint" }));
  const zoneFor = (event) => {
    const drag = session.drag;
    if (!drag) return "";
    let zone = dropZone(layer.getBoundingClientRect(), event.clientX, event.clientY);
    const sameGroup = drag.group === groupId;
    const sourceTabs = getGroup(session.layout, drag.group)?.tabs.length || 0;
    if (zone !== "center" && (!canSplit() || (sameGroup && sourceTabs < 2 && !event.ctrlKey))) zone = "center";
    if (zone === "center" && sameGroup) return "";
    return zone;
  };
  layer.addEventListener("dragover", (event) => {
    if (!event.dataTransfer.types.includes(DRAG_TYPE)) return;
    const zone = zoneFor(event);
    layer.classList.toggle("show", Boolean(zone));
    if (!zone) { delete layer.dataset.zone; return; }
    event.preventDefault();
    event.dataTransfer.dropEffect = event.ctrlKey ? "copy" : "move";
    layer.dataset.zone = zone;
  });
  layer.addEventListener("dragleave", (event) => {
    if (layer.contains(event.relatedTarget)) return;
    layer.classList.remove("show");
    delete layer.dataset.zone;
  });
  layer.addEventListener("drop", (event) => {
    const drag = readDrag(event);
    const zone = layer.dataset.zone;
    endTabDrag();
    if (!drag || !zone) return;
    event.preventDefault();
    const copy = event.ctrlKey;
    if (zone === "center") moveToGroup(drag.key, drag.group, groupId, null, { copy });
    else splitEditor(zone, { groupId, key: drag.key, fromId: drag.group, move: !copy });
  });
  return layer;
}

function setTerminalDrawer(open) {
  session.termOpen = Boolean(open);
  const main = session.host?.querySelector(".ed-main");
  const toggle = main?.querySelector(".ed-term-toggle");
  toggle?.classList.toggle("on", session.termOpen);
  toggle?.setAttribute("aria-pressed", session.termOpen ? "true" : "false");
  main?.querySelector(".ed-term-drawer")?.remove();
  const view = viewHost("terminal");
  if (!main || !view || !session.termOpen) return;
  view.hidden = false;
  main.appendChild(h("div", { class: "ed-term-drawer" }, view));
  viewShown("terminal");
}

function refreshStrips() {
  for (const host of [...stripHosts]) {
    if (!host.isConnected) { stripHosts.delete(host); continue; }
    paintStrip(host);
  }
}

function ensureEditorRoute() {
  const ctx = session.ctx;
  if (ctx && ctx.S.tab !== "editor") ctx.navigateWorkspace("editor", session.state.active);
}

// Groups currently on screen (one on narrow screens).
function shownGroups() {
  return [...session.views.keys()];
}

// ---------------------------------------------------------- loading files
function rawUrlFor(tab, { download = false } = {}) {
  if (tab.kind === "ws") {
    return authenticatedUrl(`/api/workspace/raw?path=${encodeURIComponent(tab.path)}${download ? "&download=1" : ""}`);
  }
  if (tab.kind === "media") {
    const url = mediaUrl({ url: tab.mediaUrl || "", raw_url: tab.rawUrl || `/api/media/${tab.mediaId}/raw` });
    if (!download || !/\/api\/media\//.test(url)) return url;
    return `${url}${url.includes("?") ? "&" : "?"}download=1`;
  }
  return tab.url;
}

function errorKind(err) {
  if (err?.status === 413) return "too_large";
  if (err?.status === 415) return "binary";
  if (err?.status === 404) return "missing";
  return "error";
}

async function loadBuffer(tab, { force = false } = {}) {
  const current = session.buffers.get(tab.key);
  if (current && !force && (current.status === "ready" || current.status === "loading")) return current;
  const buffer = { status: "loading" };
  session.buffers.set(tab.key, buffer);
  try {
    if (tab.kind === "ws") {
      const data = await api.workspaceFile(tab.path);
      Object.assign(buffer, {
        status: "ready", content: data.content, saved: data.content, hash: data.hash,
        mtimeNs: data.mtime_ns, size: data.size, mtime: data.mtime, language: data.language,
        eol: data.eol, readOnly: Boolean(data.read_only),
      });
    } else if (tab.kind === "media") {
      const response = await fetch(rawUrlFor(tab), { headers: { Range: `bytes=0-${TEXT_LIMIT_BYTES - 1}` } });
      if (!response.ok) {
        const error = new Error(`HTTP ${response.status}`);
        error.status = response.status;
        throw error;
      }
      const total = Number((response.headers.get("Content-Range") || "").split("/")[1]) || Number(response.headers.get("Content-Length")) || 0;
      const text = await response.text();
      Object.assign(buffer, {
        status: "ready", content: text, saved: text, size: total, readOnly: true,
        truncated: response.status === 206 && total > TEXT_LIMIT_BYTES,
      });
    }
  } catch (err) {
    Object.assign(buffer, { status: "error", error: err.message || String(err), errorKind: errorKind(err) });
  }
  if (session.buffers.get(tab.key) === buffer && editorVisible()) repaintKey(tab.key);
  return buffer;
}

// ---------------------------------------------------------------- saving
async function saveActive(groupId = session.layout.focused) {
  const tab = activeTab(getGroup(session.layout, groupId));
  if (!tab) return;
  if (tab.kind === "unit") { await saveNodeTab(tab.key); return; }
  const viewer = viewerFor(tab);
  const buffer = session.buffers.get(tab.key);
  if (!isEditable(tab, viewer) || !buffer || buffer.status !== "ready") {
    if (tab.kind === "media") toast(t("editor.read_only_hint"), "err");
    return;
  }
  if (buffer.content === buffer.saved) return;
  buffer.saving = true;
  paintToolbarState();
  try {
    const saved = await api.workspaceSave({
      path: tab.path, content: buffer.content, base_hash: buffer.hash, base_mtime: buffer.mtimeNs,
    });
    Object.assign(buffer, { saved: buffer.content, hash: saved.hash, mtimeNs: saved.mtime_ns, size: saved.size, mtime: saved.mtime, conflict: false });
    commitDirty(tab.key, false);
    toast(t("editor.saved", { name: tab.title }), "ok");
  } catch (err) {
    if (err.status === 409) buffer.conflict = true;
    toast(err.message, "err");
  } finally {
    buffer.saving = false;
    if (editorVisible()) repaintKey(tab.key, { keepEditor: true });
  }
}

function revertActive(groupId = session.layout.focused) {
  const tab = activeTab(getGroup(session.layout, groupId));
  const buffer = tab && session.buffers.get(tab.key);
  if (!buffer || buffer.status !== "ready") return;
  buffer.content = buffer.saved;
  commitDirty(tab.key, false);
  repaintKey(tab.key);
}

async function reloadActive(groupId = session.layout.focused) {
  const tab = activeTab(getGroup(session.layout, groupId));
  if (!tab) return;
  if (tab.dirty && !(await confirmDialog(t("editor.reload_confirm"), { danger: true }))) return;
  await loadBuffer(tab, { force: true });
  commitDirty(tab.key, false);
  repaintKey(tab.key);
}

// Repaint every visible group whose active tab is `key`.
function repaintKey(key, { keepEditor = false } = {}) {
  for (const id of shownGroups()) {
    if (getGroup(session.layout, id)?.active === key) paintGroup(id, { keepEditor });
  }
}

// Edits in one group: other groups showing the same document follow (code
// views update in place, previews re-render shortly after typing pauses).
const mirrorTimers = new Map();
function syncOtherViews(key, sourceGroup) {
  const buffer = session.buffers.get(key);
  for (const id of shownGroups()) {
    if (id === sourceGroup || getGroup(session.layout, id)?.active !== key) continue;
    const view = session.views.get(id);
    if (view?.editor?.key === key && buffer) view.editor.setValue(buffer.content);
    else scheduleMirror(key, id);
  }
  paintToolbarState();
}

function scheduleMirror(key, onlyGroup = "") {
  clearTimeout(mirrorTimers.get(key));
  mirrorTimers.set(key, setTimeout(() => {
    mirrorTimers.delete(key);
    for (const id of shownGroups()) {
      if (onlyGroup && id !== onlyGroup) continue;
      const view = session.views.get(id);
      if (getGroup(session.layout, id)?.active !== key || view?.el.contains(document.activeElement)) continue;
      paintGroup(id);
    }
  }, 260));
}

// ------------------------------------------------------------- shortcuts
// Called by the app's global keydown handler while the Editor is visible.
export function editorKeyDown(event) {
  if (session.ctx?.S?.tab !== "editor") return false;
  if (document.querySelector(".modal-backdrop")) return false;
  if (shortcutMatches(event, getShortcut("editorSave"))) {
    event.preventDefault();
    saveActive();
    return true;
  }
  if (shortcutMatches(event, getShortcut("editorClose"))) {
    event.preventDefault();
    if (session.state.active) requestClose("close");
    return true;
  }
  const mod = event.ctrlKey || event.metaKey;
  // Ctrl+Shift+. focuses the breadcrumbs (VS Code).
  if (mod && event.shiftKey && !event.altKey && (event.code === "Period" || event.key === ">" || event.key === ".")) {
    if (focusBreadcrumbs()) { event.preventDefault(); return true; }
    return false;
  }
  // Ctrl+\ splits the active editor to the right.
  if (mod && !event.altKey && !event.shiftKey && (event.key === "\\" || event.code === "Backslash")) {
    event.preventDefault();
    splitEditor("right");
    return true;
  }
  // Ctrl+1..4 (or Alt+1..4 where the browser keeps Ctrl+digit) focus a group.
  const digit = /^Digit([1-4])$/.exec(event.code || "")?.[1];
  if (digit && !event.shiftKey && ((mod && !event.altKey) || (event.altKey && !mod))) {
    if (focusGroupAt(Number(digit) - 1)) { event.preventDefault(); return true; }
    return false;
  }
  // Ctrl+Alt+Left/Right move the active editor to the previous/next group.
  if (mod && event.altKey && (event.key === "ArrowLeft" || event.key === "ArrowRight")) {
    const from = session.layout.focused;
    const key = session.state.active;
    const target = neighbourGroup(session.layout, from, event.key === "ArrowLeft" ? -1 : 1);
    if (!key) return false;
    event.preventDefault();
    if (target) moveToGroup(key, from, target);
    else if (event.key === "ArrowRight") splitEditor("right", { key, move: true });
    return true;
  }
  return false;
}

// ---------------------------------------------------------------- viewers
function infoCard(tab, { title, message, icon: iconName = "file" } = {}) {
  const actions = h("div", { class: "ed-info-actions" });
  if (tab.kind !== "url") {
    actions.appendChild(h("button", { class: "btn primary sm", type: "button", onclick: () => openSystem(tab) },
      icon("external", "sm"), t("editor.open_system")));
    actions.appendChild(h("a", { class: "btn ghost sm", href: rawUrlFor(tab, { download: true }), download: tab.title },
      icon("download", "sm"), t("editor.download")));
  } else {
    actions.appendChild(h("button", { class: "btn primary sm", type: "button", onclick: () => openSystem(tab) },
      icon("external", "sm"), t("editor.open_link")));
    actions.appendChild(h("button", { class: "btn ghost sm", type: "button", onclick: () => navigator.clipboard?.writeText(tab.url) },
      icon("copy", "sm"), t("editor.copy_link")));
  }
  return h("div", { class: "ed-info" },
    h("div", { class: "ed-info-icon" }, icon(iconName, "lg")),
    h("h3", null, title || tab.title),
    h("div", { class: "ed-info-path mono" }, tab.path || tab.url || tab.mediaUrl || ""),
    message ? h("p", null, message) : null,
    actions);
}

async function openSystem(tab) {
  let body;
  if (tab.kind === "ws") body = { path: tab.path };
  else if (tab.kind === "url") body = { url: tab.url };
  else {
    const url = String(tab.mediaUrl || "");
    const remote = /^[a-z][a-z0-9+.-]*:\/\//i.test(url) && !/^file:\/\//i.test(url);
    body = remote ? { url } : { path: url.replace(/^file:\/\//i, "") };
  }
  try {
    await api.openExternal(body);
    toast(t("toast.opened"), "ok");
  } catch (err) {
    toast(err.message, "err");
  }
}

function lineNumbers(count) {
  let out = "";
  for (let i = 1; i <= count; i += 1) out += `${i}\n`;
  return out;
}

function countLines(text) {
  let n = 1;
  for (let i = 0; i < text.length; i += 1) if (text.charCodeAt(i) === 10) n += 1;
  return n;
}

// Plain textarea over a highlighted <pre>: inert spans only (highlightCode),
// native undo/IME/selection, line numbers in a synced gutter.
function codeEditor(tab, buffer, { editable, language, onCursor, groupId = "" }) {
  const gutterText = h("pre", { class: "ed-gutter-text", "aria-hidden": "true" });
  const gutter = h("div", { class: "ed-gutter" }, gutterText);
  const highlight = h("pre", { class: "ed-highlight", "aria-hidden": "true" });
  const textarea = h("textarea", {
    class: "ed-textarea", spellcheck: "false", wrap: "off", autocomplete: "off",
    autocapitalize: "off", "aria-label": tab.title, readonly: editable ? undefined : true,
  });
  textarea.value = buffer.content || "";
  let lines = 0;
  let frame = 0;
  const paintHighlight = () => {
    frame = 0;
    clear(highlight);
    // A trailing newline needs a placeholder so the last empty line exists.
    highlight.appendChild(highlightCode(`${textarea.value}\n`, language));
    const next = countLines(textarea.value);
    if (next !== lines) { lines = next; gutterText.textContent = lineNumbers(lines); }
  };
  const syncScroll = () => {
    highlight.style.transform = `translate(${-textarea.scrollLeft}px, ${-textarea.scrollTop}px)`;
    gutterText.style.transform = `translateY(${-textarea.scrollTop}px)`;
  };
  const cursor = () => {
    const upto = textarea.value.slice(0, textarea.selectionStart);
    const line = countLines(upto);
    const col = upto.length - upto.lastIndexOf("\n");
    onCursor?.(line, col);
  };
  paintHighlight();
  textarea.addEventListener("scroll", syncScroll);
  textarea.addEventListener("input", () => {
    buffer.content = textarea.value;
    if (!frame) frame = requestAnimationFrame(() => { paintHighlight(); syncScroll(); });
    commitDirty(tab.key, buffer.content !== buffer.saved);
    if (groupId) syncOtherViews(tab.key, groupId);
    else paintToolbarState();
    cursor();
  });
  textarea.addEventListener("keydown", (event) => {
    if (event.key === "Tab" && !event.ctrlKey && !event.altKey && !event.metaKey && editable) {
      event.preventDefault();
      // execCommand keeps the native undo stack intact.
      if (!document.execCommand?.("insertText", false, "  ")) textarea.setRangeText("  ", textarea.selectionStart, textarea.selectionEnd, "end");
      textarea.dispatchEvent(new Event("input"));
    }
  });
  for (const name of ["keyup", "click", "focus"]) textarea.addEventListener(name, cursor);
  const shell = h("div", { class: `ed-code ${editable ? "" : "readonly"}`.trim() },
    gutter, h("div", { class: "ed-code-main" }, highlight, textarea));
  // Opened at a line (Search result): select that line and centre it.
  if (session.pendingLine?.key === tab.key) {
    const target = session.pendingLine.line;
    session.pendingLine = null;
    requestAnimationFrame(() => {
      const text = textarea.value;
      let start = 0;
      for (let n = 1; n < target && start >= 0; n += 1) start = text.indexOf("\n", start) + 1 || -1;
      if (start < 0) return;
      const end = text.indexOf("\n", start);
      textarea.focus({ preventScroll: true });
      textarea.setSelectionRange(start, end < 0 ? text.length : end);
      const lineHeight = parseFloat(getComputedStyle(textarea).lineHeight) || 20;
      textarea.scrollTop = Math.max(0, (target - 1) * lineHeight - textarea.clientHeight / 3);
      syncScroll();
      cursor();
    });
  }
  // Another group edited the same buffer: take its text, keep our caret.
  const setValue = (text) => {
    if (textarea.value === text) return;
    const { selectionStart, selectionEnd, scrollTop, scrollLeft } = textarea;
    textarea.value = text;
    textarea.setSelectionRange(Math.min(selectionStart, text.length), Math.min(selectionEnd, text.length));
    textarea.scrollTop = scrollTop;
    textarea.scrollLeft = scrollLeft;
    if (!frame) frame = requestAnimationFrame(() => { paintHighlight(); syncScroll(); });
  };
  return { el: shell, textarea, focus: () => textarea.focus(), setValue };
}

function mediaViewer(tab, viewer, view) {
  const setStatus = (text) => setViewStatus(view, text);
  const url = rawUrlFor(tab);
  if (viewer === "image") {
    // Shared zoom/pan viewer; embedded, so a plain wheel only zooms once the
    // image has focus (click it) or with Ctrl held.
    const viewer = createImageViewer({
      src: url, alt: tab.title, className: "ed-image-stage", imgClass: "ed-image",
      onLoad: (img) => setStatus(`${img.naturalWidth} × ${img.naturalHeight}px`),
      onError: () => {
        viewer.destroy();
        viewer.el.replaceWith(infoCard(tab, { message: t("editor.load_failed"), icon: "alert" }));
      },
    });
    return viewer.el;
  }
  if (viewer === "video") {
    const video = h("video", { class: "ed-video", src: url, controls: true, preload: "metadata", playsinline: true });
    video.addEventListener("loadedmetadata", () => {
      const secs = Math.round(video.duration || 0);
      setStatus(`${video.videoWidth} × ${video.videoHeight} · ${Math.floor(secs / 60)}:${String(secs % 60).padStart(2, "0")}`);
    });
    video.addEventListener("error", () => setStatus(t("editor.media_unsupported")));
    return h("div", { class: "ed-media-stage" }, video);
  }
  if (viewer === "audio") {
    return h("div", { class: "ed-media-stage audio" }, icon("play", "lg"),
      h("div", { class: "ed-audio-name" }, tab.title),
      h("audio", { src: url, controls: true, preload: "metadata" }));
  }
  if (viewer === "pdf") {
    return h("div", { class: "ed-pdf-stage" },
      h("iframe", { class: "ed-pdf", src: url, title: tab.title }),
      h("div", { class: "ed-pdf-note" }, t("editor.pdf_note"), " ",
        h("button", { class: "linklike", type: "button", onclick: () => openSystem(tab) }, t("editor.open_system"))));
  }
  return null;
}

// Markdown files: relative links/images resolve against the file's folder;
// project links open in the Editor (Ctrl+click pins a new tab).
function markdownPreview(tab, text) {
  const wrap = h("div", { class: "ed-markdown" },
    richText(text, { baseDir: tab.kind === "ws" ? dirOf(tab.path) : "", vizScope: tab.key, sourceLines: true }));
  wrap.addEventListener("click", (event) => {
    const link = event.target.closest?.("a[data-ws-path]");
    if (!link) {
      const img = event.target.closest?.("img.rich-img");
      if (img) openImageLightbox({ src: img.currentSrc || img.src, alt: img.alt, title: img.alt || tab.title });
      return;
    }
    event.preventDefault();
    openInEditor(session.ctx, { kind: "ws", path: link.dataset.wsPath }, { preview: !(event.ctrlKey || event.metaKey) });
  });
  return wrap;
}

function csvTable(text, name, view) {
  const { rows, truncated } = parseCsv(text, { delimiter: csvDelimiter(name, text) });
  if (!rows.length) return h("div", { class: "ed-info" }, h("p", null, t("editor.empty_file")));
  const [head, ...body] = rows;
  const table = h("table", { class: "ed-table" },
    h("thead", null, h("tr", null, h("th", { class: "ed-rownum" }, "#"), head.map((cell) => h("th", null, cell)))),
    h("tbody", null, body.map((row, index) => h("tr", null,
      h("td", { class: "ed-rownum" }, String(index + 1)), row.map((cell) => h("td", null, cell))))));
  setViewStatus(view, `${body.length} ${t("editor.rows")} · ${head.length} ${t("editor.columns")}${truncated ? ` · ${t("editor.truncated_rows")}` : ""}`);
  return h("div", { class: "ed-table-wrap" }, table);
}

// ------------------------------------------------------------------- view
function setViewStatus(view, text) { if (view?.statusEl) view.statusEl.textContent = text; }

// Save/Revert enabled state of every visible group.
function paintToolbarState() {
  for (const view of session.views.values()) {
    const toolbar = view.toolbar;
    if (!toolbar) continue;
    const tab = activeTab(getGroup(session.layout, view.id));
    const buffer = tab && session.buffers.get(tab.key);
    const dirty = Boolean(tab?.dirty);
    if (toolbar.save) {
      toolbar.save.disabled = !dirty || Boolean(buffer?.saving);
      toolbar.save.classList.toggle("primary", dirty);
    }
    if (toolbar.revert) toolbar.revert.disabled = !dirty;
  }
}

async function openUnitRelatedMenu(tab, anchor) {
  const ctx = session.ctx;
  const uid = String(tab.unit || "").trim();
  if (!uid) return;
  try {
    const payload = await api.unit(uid);
    const unit = payload?.unit || payload || {};
    const items = [];
    const attachments = unit.media || [];
    if (attachments.length) {
      items.push({ heading: t("editor.attachments") });
      for (const item of attachments) {
        items.push({
          label: item.caption || baseName(item.url || `attachment-${item.id}`),
          hint: item.kind || "",
          icon: "file",
          onClick: () => ctx.openInEditor(mediaSource(item, unit), { preview: true }),
        });
      }
    }
    const children = (unit.links_in || []).filter((link) => link.relation === "part_of");
    if (children.length) {
      items.push({ heading: t("editor.child_nodes") });
      for (const link of children) {
        items.push({
          label: `${link.uid} · ${link.title}`,
          hint: link.relation,
          icon: "gitBranch",
          onClick: () => ctx.openUnitInEditor({ uid: link.uid, title: link.title }),
        });
      }
    }
    const outgoing = unit.links_out || [];
    if (outgoing.length) {
      items.push({ heading: t("editor.outgoing_nodes") });
      for (const link of outgoing) {
        items.push({
          label: `${link.uid} · ${link.title}`,
          hint: link.relation,
          icon: "external",
          onClick: () => ctx.openUnitInEditor({ uid: link.uid, title: link.title }),
        });
      }
    }
    const incoming = (unit.links_in || []).filter((link) => link.relation !== "part_of");
    if (incoming.length) {
      items.push({ heading: t("editor.incoming_nodes") });
      for (const link of incoming) {
        items.push({
          label: `${link.uid} · ${link.title}`,
          hint: link.relation,
          icon: "target",
          onClick: () => ctx.openUnitInEditor({ uid: link.uid, title: link.title }),
        });
      }
    }
    if (!items.length) {
      items.push({ label: t("editor.no_related"), disabled: true });
    }
    const rect = anchor.getBoundingClientRect();
    openContextMenu({ x: rect.left, y: rect.bottom + 2, items });
  } catch (err) {
    toast(err.message, "err");
  }
}

// ------------------------------------------------------------ breadcrumbs
// VS Code style: every folder and the file name is a button that opens a
// dropdown with that folder's contents (the file lists its siblings), shown
// as a small tree (folders expand inline) with type-to-filter and keyboard
// navigation.  Uses the Explorer's workspace tree API and cache.
let crumbMenu = null;

function closeCrumbMenu({ focusAnchor = false } = {}) {
  if (!crumbMenu) return;
  const { el, anchor, cleanup } = crumbMenu;
  crumbMenu = null;
  cleanup();
  el.remove();
  anchor?.classList.remove("open");
  if (focusAnchor && anchor?.isConnected) anchor.focus();
}

function openCrumbMenu(anchor, dir, highlight, groupId) {
  const reopen = crumbMenu?.anchor === anchor;
  closeCrumbMenu();
  if (reopen) return;
  const state = { filter: "", expanded: new Set(), index: -1, rows: [] };
  const filterEl = h("div", { class: "ed-crumb-filter", "aria-live": "polite" });
  const list = h("div", { class: "ed-crumb-list", role: "tree", "aria-label": dir || t("files.tree_root") });
  const el = h("div", { class: "ed-crumb-menu", role: "dialog", tabindex: "-1", "aria-label": t("editor.breadcrumbs") }, filterEl, list);
  const openFile = (path) => {
    closeCrumbMenu();
    focusGroupView(groupId);
    openInEditor(session.ctx, { kind: "ws", path }, { preview: true });
  };
  const toggle = (path, open = !state.expanded.has(path)) => {
    if (open) state.expanded.add(path); else state.expanded.delete(path);
    paint();
  };
  const activateRow = (row) => {
    if (!row) return;
    if (row.kind === "dir") toggle(row.path);
    else if (row.kind === "file") openFile(row.path);
  };
  function paint() {
    if (!el.isConnected) return;
    state.rows = dropdownRows(dir, session.tree, state.expanded, state.filter);
    for (const row of state.rows) {
      if (row.kind === "pending") loadDir(row.path).then(() => paint());
    }
    if (state.index < 0 || state.index >= state.rows.length) {
      const current = state.rows.findIndex((row) => row.path === highlight);
      state.index = current >= 0 ? current : moveSelection(state.rows, -1, 1);
    }
    filterEl.textContent = state.filter ? t("editor.crumb_filter", { text: state.filter }) : "";
    filterEl.hidden = !state.filter;
    clear(list);
    state.rows.forEach((row, index) => {
      const pad = { paddingLeft: `${8 + row.depth * 14}px` };
      if (row.kind === "pending" || row.kind === "loading") {
        list.appendChild(h("div", { class: "ed-crumb-note", style: pad }, t("action.loading")));
        return;
      }
      if (row.kind === "error") { list.appendChild(h("div", { class: "ed-crumb-note err", style: pad }, row.error)); return; }
      if (row.kind === "truncated") { list.appendChild(h("div", { class: "ed-crumb-note", style: pad }, t("files.tree_truncated"))); return; }
      const isDir = row.kind === "dir";
      const item = h("button", {
        class: `ed-crumb-row ${isDir ? "dir" : "file"} ${row.path === highlight ? "current" : ""} ${index === state.index ? "sel" : ""}`.replace(/\s+/g, " ").trim(),
        type: "button", role: "treeitem", tabindex: "-1", title: row.path, style: pad,
        "aria-expanded": isDir ? String(Boolean(row.open)) : undefined,
        "aria-selected": index === state.index ? "true" : "false",
        dataset: { path: row.path },
        // Keep keyboard focus on the dropdown (rows are repainted).
        onmousedown: (event) => event.preventDefault(),
        onclick: (event) => { event.stopPropagation(); state.index = index; activateRow(row); },
      },
        isDir ? icon(row.open ? "chevron" : "chevronRight", "sm") : h("span", { class: "ed-tree-spacer" }),
        icon(isDir ? "folder" : tabIcon({ title: row.entry.name, path: row.path, kind: "ws" }), "sm"),
        h("span", { class: "name" }, row.entry.name));
      list.appendChild(item);
    });
    if (!state.rows.some((row) => row.kind === "dir" || row.kind === "file") && !state.rows.some((row) => row.kind !== "truncated")) {
      list.appendChild(h("div", { class: "ed-crumb-note" }, state.filter ? t("editor.no_matches") : t("files.tree_empty")));
    }
    list.querySelector(".ed-crumb-row.sel")?.scrollIntoView({ block: "nearest" });
    if (document.activeElement !== el) el.focus({ preventScroll: true });
  }
  el.addEventListener("keydown", (event) => {
    const row = state.rows[state.index];
    const key = event.key;
    if (key === "Escape") { event.preventDefault(); event.stopPropagation(); closeCrumbMenu({ focusAnchor: true }); return; }
    if (key === "ArrowDown" || key === "ArrowUp") {
      event.preventDefault();
      state.index = moveSelection(state.rows, state.index, key === "ArrowDown" ? 1 : -1);
      paint();
      return;
    }
    if (key === "Home" || key === "End") {
      event.preventDefault();
      state.index = moveSelection(state.rows, -1, key === "Home" ? 1 : -1);
      paint();
      return;
    }
    if (key === "Enter") { event.preventDefault(); activateRow(row); return; }
    if (key === "ArrowRight" && row?.kind === "dir") { event.preventDefault(); toggle(row.path, true); return; }
    if (key === "ArrowLeft" && row?.kind === "dir" && row.open) { event.preventDefault(); toggle(row.path, false); return; }
    if (key === "Backspace") { event.preventDefault(); state.filter = state.filter.slice(0, -1); state.index = -1; paint(); return; }
    if (key.length === 1 && !event.ctrlKey && !event.metaKey && !event.altKey) {
      event.preventDefault();
      state.filter += key;
      state.index = -1;
      paint();
    }
  });
  document.body.appendChild(el);
  const rect = anchor.getBoundingClientRect();
  el.style.left = `${Math.max(4, Math.min(rect.left, window.innerWidth - el.offsetWidth - 4))}px`;
  el.style.top = `${rect.bottom + 4}px`;
  el.style.maxHeight = `${Math.max(160, Math.min(window.innerHeight * 0.6, window.innerHeight - rect.bottom - 16))}px`;
  const outside = (event) => { if (!el.contains(event.target) && event.target !== anchor && !anchor.contains(event.target)) closeCrumbMenu(); };
  const onResize = () => closeCrumbMenu();
  document.addEventListener("pointerdown", outside, true);
  window.addEventListener("resize", onResize);
  crumbMenu = { el, anchor, cleanup: () => { document.removeEventListener("pointerdown", outside, true); window.removeEventListener("resize", onResize); } };
  anchor.classList.add("open");
  paint();
  el.focus();
}

function workspaceCrumbs(tab, groupId) {
  const segments = crumbSegments(tab.path);
  const button = (label, dir, highlight, extra = {}) => h("button", {
    class: `ed-crumb-seg ${extra.leaf ? "leaf" : ""}`.trim(), type: "button", "aria-haspopup": "dialog",
    title: extra.title || label, dataset: { crumbPath: extra.path ?? dir },
    onclick: (event) => { event.stopPropagation(); openCrumbMenu(event.currentTarget, dir, highlight, groupId); },
    onkeydown: (event) => {
      if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
      const all = [...event.currentTarget.parentElement.querySelectorAll(".ed-crumb-seg")];
      const next = all[all.indexOf(event.currentTarget) + (event.key === "ArrowLeft" ? -1 : 1)];
      if (next) { event.preventDefault(); next.focus(); }
    },
  }, extra.icon ? icon(extra.icon, "sm") : null, label ? h("span", null, label) : null);
  const items = [button("", "", segments[0]?.path || "", { icon: "folder", title: t("files.tree_root"), path: "" })];
  segments.forEach((segment, index) => {
    items.push(h("span", { class: "sep", "aria-hidden": "true" }, "›"));
    items.push(button(segment.label, segment.list, segment.highlight, { leaf: index === segments.length - 1, path: segment.path,
      icon: index === segments.length - 1 ? tabIcon(tab) : null }));
  });
  return h("nav", { class: "ed-crumb ed-crumbs", "aria-label": t("editor.breadcrumbs") }, items);
}

// Ctrl+Shift+. focuses the focused group's breadcrumbs (opens the file one).
function focusBreadcrumbs() {
  const view = session.views.get(session.layout.focused);
  const last = view?.main.querySelector(".ed-crumbs .ed-crumb-seg:last-of-type");
  if (!last) return false;
  last.focus();
  last.click();
  return true;
}

function sourceLabel(tab, groupId = session.layout.focused) {
  if (tab.kind === "ws") return workspaceCrumbs(tab, groupId);
  if (tab.kind === "media") {
    const label = tab.unit ? `${tab.unit}${tab.unitTitle ? ` · ${tab.unitTitle}` : ""}` : t("editor.attachment");
    const unitCrumb = tab.unit
      ? h("button", { class: "ed-crumb-link", type: "button", title: t("editor.open_node_editor"),
          onclick: (event) => { event.stopPropagation(); session.ctx.openUnitInEditor({ uid: tab.unit }); } }, label)
      : h("span", null, label);
    return h("span", { class: "ed-crumb" }, icon("link", "sm"), unitCrumb,
      h("button", { class: "ed-crumb-sep", type: "button", title: t("editor.expand_parent"),
        "aria-label": t("editor.expand_parent"),
        onclick: (event) => { event.stopPropagation(); openUnitRelatedMenu(tab, event.currentTarget); } }, "›"),
      h("span", { class: "leaf" }, tab.caption || tab.title));
  }
  return h("span", { class: "ed-crumb" }, icon("external", "sm"), h("span", { class: "leaf" }, tab.url));
}

function modeToggle(tab, viewer, groupId) {
  const labels = {
    markdown: ["editor.preview", "editor.source"], json: ["editor.pretty", "editor.source"], csv: ["editor.table", "editor.source"],
    mermaid: ["editor.rendered", "editor.source"], html: ["editor.rendered", "editor.source"],
  }[viewer];
  if (!labels) return null;
  const mode = session.modes.get(`${groupId}|${tab.key}`) || "preview";
  return h("div", { class: "ed-seg", role: "group" },
    ["preview", "source"].map((id, index) => h("button", { // i18n-ignore: mode ids
      class: mode === id ? "on" : "", type: "button", "aria-pressed": mode === id ? "true" : "false",
      dataset: { editorMode: id },
      onclick: () => {
        const current = syncViews.get(`${groupId}|${tab.key}`);
        if (current) rememberLine(current);
        session.modes.set(`${groupId}|${tab.key}`, id);
        paintGroup(groupId);
      },
    }, t(labels[index]))));
}

// ------------------------------------------------------------- the grid
function gridSignature() {
  if (isNarrow() || session.layout.groups.length < 2) return `one:${isNarrow() ? session.layout.focused : groupOrder(session.layout)[0]}`;
  const walk = (node) => (node.type === "group" ? node.id : `${node.dir}(${node.children.map(walk).join(",")})`);
  return walk(session.layout.tree);
}

function groupElement(groupId) {
  const main = h("div", { class: "ed-view" });
  const el = h("section", { class: "ed-group", dataset: { group: groupId } },
    tabBar(groupId), h("div", { class: "ed-group-body" }, main, dropLayer(groupId)));
  el.addEventListener("pointerdown", () => focusGroupView(groupId), true);
  el.addEventListener("focusin", () => focusGroupView(groupId));
  session.views.set(groupId, { id: groupId, el, main, status: null, statusEl: null, cursorEl: null, toolbar: null, editor: null });
  return el;
}

// Min sizes for a group while dragging a divider.
const MIN_GROUP_W = 240;
const MIN_GROUP_H = 140;

function nodeElement(node) {
  if (node.type === "group") return groupElement(node.id);
  const container = h("div", { class: `ed-split is-${node.dir}`, dataset: { split: node.id } });
  node.children.forEach((child, index) => {
    if (index > 0) container.appendChild(divider(node, index, container));
    const cell = h("div", { class: "ed-cell", style: { flexGrow: String(node.sizes[index]) } }, nodeElement(child));
    container.appendChild(cell);
  });
  return container;
}

// Divider between cells index-1 and index of a split: drag to resize (with
// minimum sizes), double-click to make all cells equal.
function divider(node, index, container) {
  const el = h("div", { class: `ed-divider is-${node.dir}`, role: "separator", "aria-orientation": node.dir === "row" ? "vertical" : "horizontal",
    title: t("editor.resize_groups") });
  el.addEventListener("dblclick", () => commitLayout(equalizeSplit(session.layout, node.id), { paint: false }) || paintView());
  el.addEventListener("pointerdown", (event) => {
    event.preventDefault();
    const row = node.dir === "row";
    const cells = [...container.children].filter((child) => child.classList.contains("ed-cell"));
    const total = row ? container.clientWidth : container.clientHeight;
    const sizes = cells.map((cell) => (row ? cell.getBoundingClientRect().width : cell.getBoundingClientRect().height));
    const start = row ? event.clientX : event.clientY;
    const pair = sizes[index - 1] + sizes[index];
    const min = row ? MIN_GROUP_W : MIN_GROUP_H;
    let next = sizes.slice();
    session.host?.classList.add("ed-resizing", row ? "is-row" : "is-column");
    const move = (ev) => {
      const delta = (row ? ev.clientX : ev.clientY) - start;
      const first = Math.max(min, Math.min(pair - min, sizes[index - 1] + delta));
      next = sizes.slice();
      next[index - 1] = first;
      next[index] = pair - first;
      cells.forEach((cell, i) => { cell.style.flexGrow = String(next[i] / Math.max(1, total)); });
    };
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      session.host?.classList.remove("ed-resizing", "is-row", "is-column");
      session.layout = resizeSplit(session.layout, node.id, next);
      persist();
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  });
  return el;
}

// Rebuild the editor area (groups and dividers) and paint every group.
function paintView() {
  const host = session.host;
  if (!host?.isConnected) return;
  const grid = host.querySelector(".ed-grid");
  if (!grid) return;
  session.views.clear();
  clear(grid);
  session.structure = gridSignature();
  const single = session.structure.startsWith("one:");
  grid.appendChild(single ? groupElement(isNarrow() ? session.layout.focused : groupOrder(session.layout)[0]) : nodeElement(session.layout.tree));
  host.classList.toggle("split", !single);
  for (const id of session.views.keys()) paintGroup(id);
  markFocus();
}

// Repaint what changed after a layout commit.
function paintChanges(previous) {
  if (!editorVisible()) return;
  if (gridSignature() !== session.structure) { paintView(); return; }
  for (const id of shownGroups()) {
    if (getGroup(previous, id) !== getGroup(session.layout, id)) paintGroup(id);
  }
  markFocus();
}

function markFocus() {
  const multi = session.views.size > 1;
  for (const view of session.views.values()) {
    view.el.classList.toggle("focused", multi && view.id === session.layout.focused);
  }
  if (!shellActive() || !editorVisible()) return;
  setStatusSegment(session.views.get(session.layout.focused)?.status || null);
}

function paintGroup(groupId, { keepEditor = false } = {}) {
  const view = session.views.get(groupId);
  if (!view?.main.isConnected) return;
  const tab = activeTab(getGroup(session.layout, groupId));
  if (keepEditor && view.editor && tab && view.editor.key === tab.key) {
    paintToolbarState();
    return;
  }
  const main = view.main;
  clear(main);
  Object.assign(view, { editor: null, toolbar: null, statusEl: null, cursorEl: null, status: null });
  const finish = () => { if (groupId === session.layout.focused) markFocus(); };
  if (!tab) {
    main.appendChild(emptyState());
    finish();
    return;
  }
  const viewer = viewerFor(tab);
  if (viewer === "diff") {
    paintDiffTab(main, tab, view);
    finish();
    return;
  }
  if (viewer === "node") {
    renderNodeTab(main, tab, nodeHost);
    // Shell: the node page's status line joins the workbench status bar.
    const nodeStatus = main.querySelector(":scope > .ed-statusbar");
    if (nodeStatus && shellActive()) { nodeStatus.remove(); view.status = nodeStatus; }
    finish();
    return;
  }
  const editable = isEditable(tab, viewer);
  const textual = isTextViewer(viewer);
  let buffer = textual ? session.buffers.get(tab.key) : null;
  if (textual && (!buffer || buffer.status === "loading")) {
    if (!buffer) loadBuffer(tab);
    main.appendChild(h("div", { class: "ed-loading" }, h("span", { class: "spinner" }), t("action.loading")));
    finish();
    return;
  }
  const save = editable ? h("button", { class: "btn sm ed-save", type: "button", onclick: () => saveActive(groupId), title: shortcutLabel(getShortcut("editorSave")) },
    icon("save", "sm"), t("files.save")) : null;
  const revert = editable ? h("button", { class: "btn sm ghost", type: "button", onclick: () => revertActive(groupId) }, icon("refresh", "sm"), t("files.revert")) : null;
  view.toolbar = { save, revert };
  const toolbar = h("div", { class: "ed-toolbar" },
    sourceLabel(tab, groupId),
    tab.preview ? h("span", { class: "ed-badge preview" }, t("editor.preview_tab")) : null,
    !editable && tab.kind !== "url" ? h("span", { class: "ed-badge ro", title: t("editor.read_only_hint") }, icon("ban", "sm"), t("editor.read_only")) : null,
    h("span", { class: "spacer" }),
    textual && buffer?.status === "ready" ? modeToggle(tab, viewer, groupId) : null,
    textual && tab.kind === "ws" ? h("button", { class: "btn sm ghost", type: "button", title: t("files.reload"), onclick: () => reloadActive(groupId) }, icon("refresh", "sm")) : null,
    revert, save,
    tab.kind !== "url" ? h("button", { class: "icon-btn", type: "button", title: t("editor.open_system"), "aria-label": t("editor.open_system"), onclick: () => openSystem(tab) }, icon("external", "sm")) : null,
    tab.kind !== "url" ? h("a", { class: "icon-btn", href: rawUrlFor(tab, { download: true }), download: tab.title, title: t("editor.download"), "aria-label": t("editor.download") }, icon("download", "sm")) : null);
  main.appendChild(toolbar);
  paintToolbarState();

  const banners = h("div", { class: "ed-banners" });
  if (tab.kind === "media" && textual) banners.appendChild(h("div", { class: "ed-banner info" }, icon("info", "sm"), t("editor.read_only_hint")));
  if (buffer?.truncated) banners.appendChild(h("div", { class: "ed-banner warn" }, icon("alert", "sm"), t("editor.truncated")));
  if (buffer?.conflict) {
    banners.appendChild(h("div", { class: "ed-banner warn" }, icon("alert", "sm"), t("files.conflict"),
      h("button", { class: "btn sm", type: "button", onclick: () => reloadActive(groupId) }, t("files.reload"))));
  }
  if (banners.childElementCount) main.appendChild(banners);

  const body = h("div", { class: `ed-body viewer-${viewer}` });
  main.appendChild(body);
  const statusEl = h("span", { class: "ed-status-msg" });
  const cursorEl = h("span", { class: "ed-status-cursor" });
  Object.assign(view, { statusEl, cursorEl });
  const language = buffer?.language || languageFor(tab.title || tab.path) || viewer;
  const status = h("div", { class: "ed-statusbar" },
    h("span", null, t(`editor.viewer.${viewer}`)),
    textual ? h("span", null, language) : null,
    buffer?.size ? h("span", null, formatSize(buffer.size)) : null,
    buffer?.eol ? h("span", null, buffer.eol.toUpperCase()) : null,
    buffer?.mtime ? h("span", null, fmtDateTime(buffer.mtime)) : null,
    statusEl, h("span", { class: "spacer" }), cursorEl);
  // Shell: the focused group's file info goes to the workbench status bar.
  if (shellActive()) view.status = status;
  else main.appendChild(status);
  finish();

  if (textual && buffer.status === "error") {
    const messages = {
      too_large: t("editor.too_large"), binary: t("editor.binary"), missing: t("editor.missing"),
    };
    clear(body);
    body.appendChild(infoCard(tab, { message: messages[buffer.errorKind] || buffer.error, icon: "alert" }));
    return;
  }
  if (!textual) {
    if (viewer === "link") body.appendChild(infoCard(tab, { message: t("editor.link_hint"), icon: "link" }));
    else if (viewer === "unsupported") body.appendChild(infoCard(tab, { message: t("editor.unsupported"), icon: "file" }));
    else body.appendChild(mediaViewer(tab, viewer, view));
    return;
  }
  const mode = session.modes.get(`${groupId}|${tab.key}`) || "preview";
  const useSource = viewer === "code" || mode === "source";
  syncViews.delete(`${groupId}|${tab.key}`);
  if (!useSource) {
    if (viewer === "markdown") {
      const preview = markdownPreview(tab, buffer.content);
      body.appendChild(preview);
      const rich = preview.querySelector(".rich") || preview;
      const surface = registerSurface({
        doc: tab.key, group: groupId, kind: "preview", scrollEl: () => body, watch: rich,
        anchors: () => previewAnchors(body, rich, countLines(buffer.content)),
      });
      syncViews.set(`${groupId}|${tab.key}`, surface);
      restoreLine(surface);
    }
    else if (viewer === "mermaid") {
      const diagram = h("div", { class: "rich ed-mermaid" }, h("div", { class: "mermaid-block", "data-mermaid": buffer.content }, buffer.content));
      body.appendChild(h("div", { class: "ed-markdown" }, diagram));
      typesetMermaid(diagram);
    } else if (viewer === "html") {
      // Never inserted into ARK's DOM: sandboxed opaque-origin frame, opt-in.
      body.appendChild(h("div", { class: "ed-markdown ed-html" }, vizBlock(buffer.content, { key: tab.key })));
    }
    else if (viewer === "json") {
      const pretty = prettyJson(buffer.content);
      if (!pretty.ok) body.appendChild(h("div", { class: "ed-banner warn" }, icon("alert", "sm"), `${t("editor.json_invalid")}: ${pretty.error}`));
      body.appendChild(codeEditor(tab, { content: pretty.text }, { editable: false, language: "json" }).el);
    } else if (viewer === "csv") body.appendChild(csvTable(buffer.content, tab.title, view));
    return;
  }
  const bound = codeEditor(tab, buffer, {
    editable: editable && !buffer.readOnly, groupId,
    language: viewer === "markdown" ? "markdown" : viewer === "json" ? "json" : language,
    onCursor: (line, col) => { cursorEl.textContent = t("editor.cursor", { line, col }); },
  });
  view.editor = { key: tab.key, ...bound };
  body.appendChild(bound.el);
  if (viewer === "markdown") {
    const textarea = bound.el.querySelector("textarea");
    const surface = registerSurface({
      doc: tab.key, group: groupId, kind: "source", scrollEl: () => textarea, anchors: () => textareaAnchors(textarea),
    });
    syncViews.set(`${groupId}|${tab.key}`, surface);
    for (const name of ["keyup", "click"]) textarea.addEventListener(name, () => caretLine(tab.key, caretLineOf(textarea)));
    restoreLine(surface);
  }
  // Focus the code of the focused group only (never steal it from another).
  if (editable && groupId === session.layout.focused && !document.activeElement?.closest?.(".ed-group:not([data-group='" + groupId + "']), .wb-container, .modal")) {
    requestAnimationFrame(() => { if (view.editor?.key === tab.key && session.layout.focused === groupId) bound.focus(); });
  }
}

// A git diff of one project file against a base ref (from the Changes view).
async function loadDiff(tab, { force = false } = {}) {
  const current = session.diffs.get(tab.key);
  if (current && !force && (current.loading || current.data)) return;
  session.diffs.set(tab.key, { loading: true, data: null, error: "" });
  let next;
  try {
    next = { loading: false, data: await api.workspaceDiff(tab.path, tab.base || "HEAD"), error: "" };
  } catch (err) {
    next = { loading: false, data: null, error: err.message };
  }
  session.diffs.set(tab.key, next);
  if (editorVisible()) repaintKey(tab.key);
}

function paintDiffTab(main, tab, view) {
  const cache = session.diffs.get(tab.key);
  if (!cache) loadDiff(tab);
  const data = cache?.data;
  const stat = data ? diffStat(data.additions, data.deletions) : { label: "±" };
  main.appendChild(h("div", { class: "ed-toolbar" },
    h("span", { class: "ed-crumb" }, icon("git", "sm"), h("span", { class: "leaf mono" }, tab.path)),
    h("span", { class: "ed-badge" }, t("files.base_badge", { ref: tab.base || "HEAD" })),
    tab.preview ? h("span", { class: "ed-badge preview" }, t("editor.preview_tab")) : null,
    h("span", { class: `fw-diff-stat ${stat.additions === null ? "binary" : ""}`.trim() }, data?.binary ? t("files.binary_short") : stat.label),
    h("span", { class: "spacer" }),
    h("button", { class: "btn sm ghost", type: "button", title: t("action.refresh"), onclick: () => loadDiff(tab, { force: true }) }, icon("refresh", "sm")),
    h("button", { class: "btn sm primary", type: "button", onclick: () => openInEditor(session.ctx, { kind: "ws", path: tab.path }) },
      icon("edit", "sm"), t("files.open"))));
  const body = h("div", { class: "ed-body viewer-diff" });
  main.appendChild(body);
  const status = h("div", { class: "ed-statusbar" }, h("span", null, t("editor.viewer.diff")),
    h("span", null, t("files.base_badge", { ref: tab.base || "HEAD" })));
  if (shellActive()) view.status = status;
  else main.appendChild(status);
  if (!cache || cache.loading) {
    body.appendChild(h("div", { class: "ed-loading" }, h("span", { class: "spinner" }), t("action.loading")));
    return;
  }
  if (cache.error) { body.appendChild(infoCard(tab, { message: cache.error, icon: "alert" })); return; }
  if (data?.binary || !data?.diff) {
    body.appendChild(h("div", { class: "fw-diff-empty" }, icon(data?.binary ? "file" : "check", "lg"),
      h("b", null, data?.binary ? t("files.binary") : (localizeServer(data?.message) || t("files.no_changes", { base: tab.base || "HEAD" })))));
    return;
  }
  const rows = parseDiffRows(data.diff);
  const table = h("div", { class: "fw-diff", role: "table", "aria-label": t("files.diff_aria", { path: tab.path }) });
  for (const row of rows) {
    table.appendChild(h("div", { class: `fw-diff-row ${row.kind}`.trim(), role: "row" },
      h("span", { class: "fw-diff-text", role: "cell" }, row.text || " ")));
  }
  body.appendChild(h("div", { class: "fw-diff-scroll" }, table));
  status.append(h("span", null, t("files.n_diff_lines", { n: rows.length })), h("span", null, stat.label));
  if (data.truncated) status.appendChild(h("span", null, t("files.diff_truncated")));
}

function formatSize(bytes) {
  const value = Number(bytes) || 0;
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`;
  return `${(value / (1024 * 1024)).toFixed(1)} MB`;
}

function emptyState() {
  const shortcut = shortcutLabel(getShortcut("palette"));
  const recent = session.recent.filter((item) => !findTab(session.state, item.key));
  return h("div", { class: "ed-empty" },
    h("div", { class: "ed-empty-mark" }, icon("edit", "lg")),
    h("h2", null, t("editor.empty_title")),
    h("p", null, t("editor.empty_hint")),
    h("ul", { class: "ed-how" },
      h("li", null, icon("folder", "sm"), t("editor.how_tree")),
      h("li", null, icon("link", "sm"), t("editor.how_inspector")),
      h("li", null, icon("play", "sm"), t("editor.how_files")),
      h("li", null, icon("search", "sm"), t("editor.how_quick", { keys: shortcut }))),
    recent.length ? h("div", { class: "ed-recent" },
      h("h3", null, t("editor.recent")),
      recent.map((item) => h("button", {
        class: "ed-recent-item", type: "button", dataset: { editorRecent: item.key },
        onclick: () => openInEditor(session.ctx, item),
      }, icon(tabIcon(item), "sm"), h("span", { class: "name" }, item.title),
        h("span", { class: "path mono" }, item.path || item.caption || item.unit || item.url || "")))) : null);
}

// --------------------------------------------------------------- explorer
async function loadDir(path) {
  const node = session.tree.get(path);
  if (node && (node.loading || node.entries)) return;
  session.tree.set(path, { loading: true });
  try {
    const data = await api.workspaceTree(path, 1);
    session.tree.set(path, { entries: data.entries || [], truncated: Boolean(data.truncated) });
  } catch (err) {
    session.tree.set(path, { entries: [], error: err.message });
  }
  paintSide();
}

function treeRows(path, depth, container) {
  const node = session.tree.get(path);
  if (!node) { loadDir(path); return; }
  if (node.loading) { container.appendChild(h("div", { class: "ed-tree-note", style: { paddingLeft: `${10 + depth * 12}px` } }, t("action.loading"))); return; }
  if (node.error) { container.appendChild(h("div", { class: "ed-tree-note err" }, node.error)); return; }
  for (const entry of node.entries) {
    const isDir = entry.type === "dir";
    const open = session.expanded.has(entry.path);
    const key = isDir ? "" : `ws:${entry.path}`;
    const row = h("button", {
      class: `ed-tree-row ${isDir ? "dir" : "file"} ${key && key === session.state.active ? "on" : ""}`.trim(),
      type: "button", title: entry.path, dataset: { path: entry.path, type: entry.type },
      style: { paddingLeft: `${8 + depth * 12}px` },
      onclick: () => {
        if (isDir) {
          if (open) session.expanded.delete(entry.path); else session.expanded.add(entry.path);
          paintSide();
        } else openInEditor(session.ctx, { kind: "ws", path: entry.path }, { preview: true });
      },
      ondblclick: () => { if (!isDir) openInEditor(session.ctx, { kind: "ws", path: entry.path }); },
      onmousedown: (event) => { if (event.detail > 1) event.preventDefault(); },
    },
      isDir ? icon(open ? "chevron" : "chevronRight", "sm") : h("span", { class: "ed-tree-spacer" }),
      icon(isDir ? "folder" : tabIcon({ title: entry.name, path: entry.path, kind: "ws" }), "sm"),
      h("span", { class: "name" }, entry.name),
      key && findTab(session.state, key)?.dirty ? h("span", { class: "ed-tree-dirty", title: t("common.unsaved") }, "●") : null);
    container.appendChild(row);
    if (isDir && open) treeRows(entry.path, depth + 1, container);
  }
  if (node.truncated) container.appendChild(h("div", { class: "ed-tree-note" }, t("files.tree_truncated")));
}

let quickTimer = null;
function runQuick(text) {
  session.quick = text;
  clearTimeout(quickTimer);
  if (!text.trim()) { session.quickResults = null; paintSide(); return; }
  quickTimer = setTimeout(async () => {
    try {
      const data = await api.workspaceSearch(text.trim(), 60);
      if (session.quick !== text) return;
      session.quickResults = data.results || [];
    } catch (err) {
      session.quickResults = [];
      toast(err.message, "err");
    }
    paintSide(false);
  }, 160);
}

// The Explorer: with the workbench shell it is the "Explorer" view (in
// whichever sidebar/panel holds it); in the classic layout it is the
// Editor's own left column.
function explorerSide() {
  if (shellActive()) {
    const view = viewHost("explorer");
    if (!view) return null;
    let side = view.querySelector(":scope > .ed-side");
    if (!side) {
      side = h("div", { class: "ed-side wb-explorer", "aria-label": t("editor.explorer") });
      view.appendChild(side);
    }
    return side;
  }
  return session.host?.querySelector(".ed-side") || null;
}

function paintSide(rebuildInput = true) {
  const host = session.host;
  const side = explorerSide();
  if (!side) return;
  const classic = !shellActive();
  if (host && classic) host.classList.toggle("side-closed", !session.sideOpen);
  // Classic layout: the explorer column also offers the Changes view.
  if (classic && session.sideMode === "changes") {
    const view = viewHost("changes");
    clear(side);
    side.append(sideHead());
    if (view) {
      view.hidden = false;
      side.appendChild(view);
      viewShown("changes");
    }
    return;
  }
  const list = side.querySelector(".ed-tree");
  if (rebuildInput || !list || !side.querySelector(".ed-scope")) {
    clear(side);
    loadScopes();
    const input = h("input", {
      class: "input ed-quick", type: "search", placeholder: t("editor.quick_open"),
      "aria-label": t("editor.quick_open"), value: session.quick,
      oninput: (event) => runQuick(event.target.value),
      onkeydown: (event) => {
        if (event.key === "Enter") {
          const first = side.querySelector(".ed-tree-row.file");
          first?.click();
          if (first) first.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
        }
        if (event.key === "Escape") { event.target.value = ""; runQuick(""); }
      },
    });
    side.append(sideHead(), scopePicker(), input, h("div", { class: "ed-tree", role: "tree" }));
  }
  const tree = side.querySelector(".ed-tree");
  clear(tree);
  if (session.quickResults) {
    if (!session.quickResults.length) tree.appendChild(h("div", { class: "ed-tree-note" }, t("editor.no_matches")));
    for (const item of session.quickResults) {
      tree.appendChild(h("button", {
        class: "ed-tree-row file quick", type: "button", title: item.path, dataset: { path: item.path, type: "file" },
        onclick: () => openInEditor(session.ctx, { kind: "ws", path: item.path }, { preview: true }),
        ondblclick: () => openInEditor(session.ctx, { kind: "ws", path: item.path }),
      }, icon(tabIcon({ title: item.name, path: item.path, kind: "ws" }), "sm"),
        h("span", { class: "name" }, item.name), h("span", { class: "dir mono" }, item.path.split("/").slice(0, -1).join("/"))));
    }
    return;
  }
  treeRows("", 0, tree);
}

function sideHead() {
  const classic = !shellActive();
  const tab = (mode, label, iconName) => h("button", {
    class: `ed-side-tab ${session.sideMode === mode ? "on" : ""}`.trim(), type: "button",
    "aria-pressed": session.sideMode === mode ? "true" : "false", dataset: { sideMode: mode },
    onclick: () => { session.sideMode = mode; paintSide(); },
  }, icon(iconName, "sm"), t(label));
  return h("div", { class: "ed-side-head" },
    classic ? h("div", { class: "ed-side-tabs" }, tab("files", "editor.explorer", "folder"), tab("changes", "workbench.view.changes", "git"))
      : h("span", null, t("editor.explorer")),
    session.sideMode === "files" || !classic ? h("button", {
      class: "icon-btn", type: "button", title: t("action.refresh"), "aria-label": t("action.refresh"),
      onclick: () => { session.tree.clear(); paintSide(); },
    }, icon("refresh", "sm")) : null);
}

// Tab set: the project's own tabs or a task session's (Files › session scope).
function scopePicker() {
  const sessions = session.sessions || [];
  const options = [h("option", { value: "" }, t("files.project_scope"))];
  for (const item of sessions) {
    options.push(h("option", { value: item.id, selected: item.id === session.scope ? true : undefined },
      `${item.title}${item.status ? ` · ${item.status}` : ""}`));
  }
  if (session.scope && !sessions.some((item) => item.id === session.scope)) {
    options.push(h("option", { value: session.scope, selected: true }, session.scope));
  }
  const select = h("select", {
    class: "select ed-scope", "aria-label": t("files.session_scope"), title: t("files.scope_hint"),
    onchange: (event) => switchScope(event.target.value),
  }, options);
  select.value = session.scope;
  // With no agent sessions there is nothing to choose: keep the row (paintSide
  // looks for it) but hide it.
  return h("label", { class: "ed-scope-row", hidden: !sessions.length && !session.scope },
    icon(session.scope ? "play" : "folder", "sm"), select);
}

export function focusQuickOpen() {
  if (!explorerSide()?.querySelector(".ed-quick")) return;
  if (!editorExplorerVisible()) setExplorerVisible(true);
  explorerSide()?.querySelector(".ed-quick")?.focus();
}

// The Explorer is the Editor's left sidebar: its own corner button and the
// top-bar left layout toggle both drive it. It remembers its own state
// (ark.editor.side), separate from the filter rail of Research/Map.
// The event name lives in ../editor-entry.js, the lazy-loading entry point.
import { EXPLORER_CHANGED } from "../editor-entry.js";
export { EXPLORER_CHANGED };

export function editorExplorerVisible() {
  if (shellActive()) return isViewDisplayed("explorer");
  return Boolean(session.sideOpen);
}

export function setExplorerVisible(visible) {
  if (shellActive()) {
    showView("explorer", Boolean(visible));
    window.dispatchEvent(new Event(EXPLORER_CHANGED));
    return;
  }
  session.sideOpen = Boolean(visible);
  localStorage.setItem(SIDE_KEY, session.sideOpen ? "1" : "0");
  paintSide();
  window.dispatchEvent(new Event(EXPLORER_CHANGED));
}

// ---------------------------------------------------------------- render
export function renderEditor(host, ctx) {
  ensureProject(ctx);
  // On a phone the explorer overlays the viewer, so every visit starts with
  // it closed (the toggle in the corner opens it).
  if (isPhone()) session.sideOpen = false;
  const wanted = ctx.S.editorKey;
  if (wanted && wanted !== session.state.active) {
    // A deep link focuses a group that already shows the tab, else opens it
    // in the focused group.
    const holder = groupOrder(session.layout).find((id) => getGroup(session.layout, id).active === wanted)
      || groupOrder(session.layout).find((id) => findTab(getGroup(session.layout, id), wanted));
    if (holder) session.layout = updateGroup(focusGroup(session.layout, holder), holder, (group) => activateTab(group, wanted));
    else if (parseKey(wanted)) session.layout = openInGroup(session.layout, session.layout.focused, makeTab({ key: wanted }));
    persist();
  }
  syncRoute();
  const main = h("section", { class: "ed-main", "aria-label": t("nav.editor") }, h("div", { class: "ed-grid" }));
  let root;
  if (shellActive()) {
    // Workbench shell: the explorer is a sidebar view, not an Editor column.
    root = h("div", { class: "ed-shell no-side" }, main);
  } else {
    const toggle = h("button", {
      class: "icon-btn ed-side-toggle", type: "button", title: t("editor.toggle_explorer"), "aria-label": t("editor.toggle_explorer"),
      onclick: () => setExplorerVisible(!session.sideOpen),
    }, icon("panel-left", "sm"));
    root = h("div", { class: `ed-shell ${session.sideOpen ? "" : "side-closed"}`.trim() },
      h("aside", { class: "ed-side", "aria-label": t("editor.explorer") }), main, toggle);
  }
  if (!shellActive() && session.termOpen) requestAnimationFrame(() => setTerminalDrawer(true));
  session.host = root;
  host.appendChild(root);
  paintSide();
  paintView();
  refreshStrips();
}
