import { h, clear, fmtDate, fmtDateTime, relativeTime, typeColor, statusColor, richText, copyText, isImageUrl } from "./dom.js";
import { crossProjectSection } from "./project-move.js";
import { t, typeLabel, statusLabel, impLabel, relLabel, originLabel, logLabel, nsLabel } from "./i18n.js";
import { icon } from "./icons.js";
import { createImageViewer, openImageLightbox } from "./image-viewer.js";
import { attachImageContextMenu } from "./image-copy.js";
import { api, mediaUrl } from "./api.js";
import { confirmDialog, openLinkModal, openDispatchModal, openModal, toast, actorLabel, canRunWithAgent } from "./components.js";
import { mediaSource, mediaPreviewable } from "./editor-media.js";
import { draftFromUnit, mediaKinds } from "./node-model.js";
import { assigneePicker, assigneeLabel } from "./assignee.js";
import { commentsSection } from "./comments.js";
import { NEEDS_CHANGED, acceptUnit, reviewPanel, sendBackUnit, markNeedsSeen } from "./needs-ui.js";
import { markAssignedRead, needsUnitKey } from "./needs-model.js";
import { canDecideReview } from "./review-model.js";
import { openNodeHistory } from "./node-history.js";
import { openContextMenu } from "./context-menu.js";
import { unitMenuItems } from "./unit-menu.js";
import { openAddToTopic, topicSettings } from "./topic-actions.js";
import { floatingPosition } from "./floating-position.js";
import { INSPECTOR_WIDTH_KEY, clampInspectorWidth, dragInspectorWidth } from "./inspector-width.js";
import {
  UNIT_SAVED_EVENT, UNIT_RESTORE_CHECK_EVENT, saveUnitDraft, compressImage, fileToBase64, openMediaExternal,
} from "./unit-edit.js";
import { shellActive, viewHost, revealView, render as renderWorkbench } from "./workbench.js";
import { presenceDetailsLine } from "./presence-ui.js";
import { inspectorReadOnly, savedMode } from "./read-edit-mode.js";
import { markersFor, markerMenuChildren, removeMarkersChange, isMarkerTag } from "./markers.js";
import { saveMarkerChange } from "./marker-actions.js";

// With the workbench shell the docked inspector is the "Details" view and
// lives in whichever sidebar/panel holds it; floating drawers and the
// classic layout keep using #overlay-root.
function overlayRoot() {
  return document.getElementById("overlay-root");
}

export function dockRoot() {
  return (shellActive() && viewHost("details")) || overlayRoot();
}

export function drawerRoots() {
  return [overlayRoot(), viewHost("details")].filter(Boolean);
}

// Keep an open inspector in step with saves made elsewhere (Editor node
// tabs).  An inspector with pending edits is left alone; its own autosave
// will win and notify the Editor in turn.
let liveInspector = null;   // { ctx, unitId, isDirty }
let inspectorRequest = 0;

/**
 * The multi-selection inspector (inspector-multi.js) shares the request
 * counter: whichever panel was asked for last wins.
 */
export function claimInspector() {
  liveInspector = null;
  return ++inspectorRequest;
}
export function inspectorClaimed(request) { return request === inspectorRequest; }
window.addEventListener(UNIT_RESTORE_CHECK_EVENT, (event) => {
  if (liveInspector?.projectId === event.detail?.project && liveInspector?.unitId === event.detail?.id
      && liveInspector.isDirty()) event.preventDefault();
});
window.addEventListener(UNIT_SAVED_EVENT, (event) => {
  const { unit, origin } = event.detail || {};
  const live = liveInspector;
  if (!unit || origin === "inspector" || !live || live.unitId !== unit.id) return;
  if (!live.ctx.S.drawerOpen || live.isDirty()) return;
  if (origin === "remote" || origin === "markers") {
    // Newer server data for the open unit (cache revalidation, another
    // person's edit, a marker change from the graph menu or its undo):
    // repaint the docked or floating panel in place, but not
    // under a focused field; retry once the person moves on.
    const active = document.activeElement;
    if (active?.closest?.(".drawer") && (active.isContentEditable || /^(input|textarea|select)$/i.test(active.tagName))) {
      const tries = (event.detail.tries || 0) + 1;
      if (tries < 40) {
        setTimeout(() => window.dispatchEvent(new CustomEvent(UNIT_SAVED_EVENT,
          { detail: { ...event.detail, tries } })), 1500);
      }
      return;
    }
    const scrolled = drawerRoots().flatMap((root) => [...root.querySelectorAll(".drawer, .drawer *")])
      .map((el, index) => [index, el.scrollTop]).filter(([, top]) => top > 0);
    openInspector(live.ctx, unit.uid || unit.id, live.ctx.S.drawerAnchor).then(() => {
      const now = drawerRoots().flatMap((root) => [...root.querySelectorAll(".drawer, .drawer *")]);
      for (const [index, top] of scrolled) if (now[index]) now[index].scrollTop = top;
    });
    return;
  }
  if (!document.querySelector("#overlay-root .drawer:not(.inspector-empty)")) return;
  openInspector(live.ctx, unit.uid || unit.id, live.ctx.S.drawerAnchor);
});

// Floating inspector size is a browser preference. It is applied before the
// drawer is created so reopening (or reloading) uses the last dragged size.
try {
  const savedW = Number(localStorage.getItem("ark.inspectorFloatW") || 0);
  const savedH = Number(localStorage.getItem("ark.inspectorFloatH") || 0);
  if (savedW > 0) document.documentElement.style.setProperty("--inspector-float-w", `${savedW}px`);
  if (savedH > 0) document.documentElement.style.setProperty("--inspector-float-h", `${savedH}px`);
} catch { /* storage can be unavailable */ }

export async function openInspector(ctx, ref, anchor = null, options = {}) {
  if (!ref) return;
  const sameUnit = ctx.S.drawerOpen && [ctx.S.detail?.id, ctx.S.detail?.uid].some((id) => String(id) === String(ref));
  if (sameUnit && !anchor) anchor = ctx.S.drawerAnchor;
  if (sameUnit && !options.revealPane && options.forceFloating === undefined) options = { ...options, forceFloating: ctx.S.drawerFloating };
  const request = ++inspectorRequest;
  anchor = anchor && { ...anchor };
  ctx.S.multi = null;
  let data;
  try {
    data = await api.unit(ref);
  } catch (err) {
    // quiet: a restored inspector (project-state-model.js) whose unit is gone.
    if (request === inspectorRequest && !options.quiet) toast(err.message, "err");
    return;
  }
  if (request !== inspectorRequest) return;
  const unit = data.unit;
  ctx.S.selectedId = unit.id;
  ctx.S.detail = unit;
  ctx.S.drawerOpen = true;
  const seenKey = needsUnitKey(unit, ctx.S.needsAssigned);
  if (seenKey) markNeedsSeen(ctx, { items: [seenKey] });
  // Opening an assigned unit reads its notice (the server does the same).
  const assigned = markAssignedRead(ctx.S.needsAssigned || [], unit.uid);
  if (assigned !== ctx.S.needsAssigned && JSON.stringify(assigned) !== JSON.stringify(ctx.S.needsAssigned || [])) {
    ctx.S.needsAssigned = assigned;
    document.dispatchEvent(new CustomEvent(NEEDS_CHANGED, { detail: { ctx } }));
  }
  ctx.S.drawerAnchor = anchor || null;
  const isPhone = typeof window.matchMedia === "function"
    && window.matchMedia("(max-width: 760px)").matches;
  if (options.revealPane && !isPhone && ctx.S.inspectorPane === false) {
    ctx.S.inspectorPane = true;
    try { localStorage.setItem("ark.inspectorPane", "1"); } catch { /* storage may be disabled */ }
    ctx.renderTopbar?.();
  }
  const docked = !options.forceFloating && ctx.S.inspectorPane !== false && !isPhone;
  const editableGraphs = (ctx.graphs || []).filter((item) => !item.opts?.backdrop);
  const graph = editableGraphs.find((item) => item.selectedNodes?.has(unit.id))
    || editableGraphs.find((item) => item.byId?.has(unit.id))
    || null;
  // Read / Edit: the person's one remembered choice (the graphs share it);
  // Read until they switch, also on pages without a graph.
  const readOnly = inspectorReadOnly({
    readOnly: options.readOnly, sessionMode: ctx.S.graphMode, graphMode: graph?.mode, saved: savedMode(),
  });
  ctx.S.drawerFloating = Boolean(options.forceFloating);
  document.body.classList.toggle("inspector-pinned", docked);
  renderInspector(ctx, unit, {
    docked, anchor: ctx.S.drawerAnchor, readOnly, graph,
    mobileSheet: isPhone,
  });
  // A chat in the Sessions view keeps its place: the node waits in the Details tab.
  if (docked && shellActive()) revealView("details", { unless: ["localgraph", "sessions"] });
  renderWorkbench();
  window.dispatchEvent(new CustomEvent("ark:selection"));
}

export function closeInspector(ctx) {
  inspectorRequest++;
  ctx.S.multi = null;
  ctx.S.drawerOpen = false;
  ctx.S.detail = null;
  ctx.S.selectedId = null;
  document.body.classList.remove("inspector-pinned");
  for (const root of drawerRoots()) root.querySelectorAll(".drawer, .drawer-backdrop").forEach((el) => el.remove());
  if (ctx.S.inspectorPane) showInspectorPlaceholder(ctx);
  renderWorkbench();
  window.dispatchEvent(new CustomEvent("ark:selection"));
}

/** Edge handle for the docked pane; shared by the placeholder and a unit. */
export function inspectorResizer(ctx) {
  const handle = h("div", { class: "drawer-resizer", title: t("inspector.resize_docked") });
  handle.addEventListener("pointerdown", (e) => {
    e.preventDefault();
    const startX = e.clientX;
    const startW = clampInspectorWidth(ctx.S.inspectorWidth);
    const move = (ev) => {
      ctx.S.inspectorWidth = dragInspectorWidth(startW, startX, ev.clientX);
      document.documentElement.style.setProperty("--inspector-w", `${ctx.S.inspectorWidth}px`);
    };
    const up = () => {
      try { localStorage.setItem(INSPECTOR_WIDTH_KEY, String(ctx.S.inspectorWidth)); } catch { /* storage may be disabled */ }
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  });
  return handle;
}

/** Keep the attributes rail visible while its topbar toggle is active. */
export function showInspectorPlaceholder(ctx) {
  const root = dockRoot();
  if (!root) return;
  for (const item of drawerRoots()) item.querySelectorAll(".inspector-empty").forEach((el) => el.remove());
  // Tasks → Sessions owns the full width with its own details rail. Keep the
  // attributes placeholder out of the way until the user opens a real unit.
  if ((ctx.S.tab === "tasks" || ctx.S.tab === "editor") && !ctx.S.drawerOpen) {
    document.body.classList.remove("inspector-pinned");
    return;
  }
  // On a phone the attributes panel opens as a floating sheet only for a real
  // selection; an always-visible empty rail would eat the whole viewport.
  if (typeof window.matchMedia === "function"
      && window.matchMedia("(max-width: 760px)").matches) {
    document.body.classList.remove("inspector-pinned");
    return;
  }
  if (!ctx.S.inspectorPane || ctx.S.drawerOpen) {
    if (!ctx.S.drawerOpen) document.body.classList.remove("inspector-pinned");
    return;
  }
  document.body.classList.add("inspector-pinned");
  const drawer = h("aside", { class: "drawer docked inspector-empty", "aria-label": t("inspector.details") },
    h("div", { class: "drawer-head" },
      h("div", { class: "drawer-title-static" }, t("inspector.details")),
      h("div", { class: "spacer" }),
      h("span", { class: "pill" }, t("inspector.no_selection_pill"))),
    h("div", { class: "drawer-body" },
      h("div", { class: "inspector-empty-note" },
        h("div", { class: "inspector-empty-mark" }, "◎"),
        h("h3", null, t("inspector.no_selection_title")),
        h("p", null, t("inspector.no_selection_body")))));
  drawer.appendChild(inspectorResizer(ctx));
  root.appendChild(drawer);
}

export function placeFloating(drawer, anchor) {
  const topbar = parseFloat(getComputedStyle(document.documentElement).getPropertyValue("--topbar")) || 50;
  const rect = drawer.getBoundingClientRect();
  const width = rect.width || 430;
  const height = Math.min(rect.height || 380, window.innerHeight - topbar - 24);
  const { x, y } = floatingPosition(anchor, width, height, window.innerWidth, window.innerHeight, topbar);
  drawer.style.left = `${x}px`;
  drawer.style.top = `${y}px`;
}

export function createInspectorPreview(ctx, unit, anchor, onClose) {
  return renderInspector(ctx, unit, { docked: false, anchor, readOnly: true, preview: true, onClose });
}

function renderInspector(ctx, unit, {
  docked = true, anchor = null, readOnly = false, graph = null, mobileSheet = false,
  preview = false, onClose = null,
} = {}) {
  const root = docked && !mobileSheet ? dockRoot() : overlayRoot();
  if (!preview) {
    for (const item of drawerRoots()) item.querySelectorAll(".drawer, .drawer-backdrop, .modal-backdrop").forEach((el) => el.remove());
  }
  let dirty = false;
  let tags = (unit.tags || []).filter((tag) => !/^(type|status|importance):/.test(tag));
  let media = [...(unit.media || [])];

  const title = h("textarea", { class: "drawer-title", rows: "1", value: unit.title });
  const autoGrow = () => { title.style.height = "auto"; title.style.height = `${Math.min(120, title.scrollHeight)}px`; };
  const typeSel = h("select", { class: "input" }, (ctx.S.vocab?.types || []).map((item) =>
    h("option", { value: item.id, selected: item.id === unit.type ? true : undefined }, typeLabel(item.id))));
  typeSel.value = unit.type;
  const statusSel = h("select", { class: "input" }, (ctx.S.vocab?.statuses || []).map((item) =>
    h("option", { value: item.id, selected: item.id === unit.status ? true : undefined }, statusLabel(item.id))));
  statusSel.value = unit.status;
  const impSel = h("select", { class: "input" }, (ctx.S.vocab?.importance || []).map((item) =>
    h("option", { value: item.id, selected: item.id === unit.importance ? true : undefined }, t(`imp.long.${item.id}`))));
  impSel.value = unit.importance;
  const body = h("textarea", { class: "textarea", value: unit.body || "" });
  const start = h("input", { class: "input", type: "date", value: unit.start_at || "" });
  const end = h("input", { class: "input", type: "date", value: unit.end_at || "" });
  typeSel.addEventListener("change", () => {
    const local = (ctx.S.units || []).find((item) => item.id === unit.id);
    if (local) local.type = typeSel.value;
    unit.type = typeSel.value;
    for (const graph of ctx.graphs || []) graph.setNodeType?.(unit.id, typeSel.value);
  });

  let saveTimer = null;
  let saving = false;
  let revision = 0;
  const saveState = h("span", { class: "autosave-state" }, t("toast.saved"));
  const markDirty = () => {
    dirty = true;
    revision += 1;
    saveState.textContent = t("node.saving");
    saveState.classList.add("pending");
    clearTimeout(saveTimer);
    saveTimer = setTimeout(() => { saveTimer = null; save(); }, 550);
  };
  for (const el of [title, body, start, end]) el.addEventListener("input", markDirty);
  for (const el of [typeSel, statusSel, impSel]) el.addEventListener("change", markDirty);

  // Tags editor
  const tagWrap = h("div", { class: "tag-editor" });
  const tagInput = h("input", {
    placeholder: t("inspector.add_tag"),
    onkeydown: (e) => {
      if (e.key === "Enter" || e.key === ",") {
        e.preventDefault();
        const value = e.target.value.trim().replace(/,+$/, "");
        if (value && !tags.includes(value)) { tags.push(value); renderTags(); markDirty(); }
        e.target.value = "";
      } else if (e.key === "Backspace" && !e.target.value && tags.length) {
        tags.pop(); renderTags(); markDirty();
      }
    },
  });
  const renderTags = () => {
    clear(tagWrap);
    // Markers have their own row (markerWrap below).
    for (const tag of tags.filter((item) => !isMarkerTag(item))) {
      tagWrap.appendChild(h("span", { class: "tag" }, tag,
        h("button", { title: t("action.remove"), onclick: () => { tags = tags.filter((x) => x !== tag); renderTags(); markDirty(); } }, "×")));
    }
    tagWrap.appendChild(tagInput);
    if (tags.some((item) => !isMarkerTag(item))) {
      tagWrap.appendChild(h("button", {
        class: "tag-clear", type: "button", title: t("inspector.clear_tags"),
        "aria-label": t("inspector.clear_tags"),
        onclick: () => { tags = tags.filter(isMarkerTag); renderTags(); markDirty(); },
      }, icon("trash", "sm")));
    }
  };
  renderTags();

  // Markers: a chip removes its marker, + opens the marker menu (choosing a
  // marker the node has removes it; one marker per exclusive group).  Saved
  // at once as one batch, one Ctrl+Z step on the graph.
  const markerWrap = h("div", { class: "marker-editor", "data-markers": "" });
  const ownTagsOf = (saved) => (saved.tags || []).filter((tag) => !/^(type|status|importance):/.test(tag));
  const changeMarkers = async (change) => {
    try {
      if (dirty) { clearTimeout(saveTimer); saveTimer = null; await save(); }
      const result = await saveMarkerChange(ctx, [unit.id], change);
      const saved = result?.updated.find((item) => item.id === unit.id);
      if (saved) { tags = ownTagsOf(saved); renderTags(); renderMarkers(); }
      if (result?.undo && graph?._pushHistory) {
        graph._pushHistory({ undo: async () => graph._syncNodeTags?.(await result.undo()) });
      }
    } catch (err) {
      toast(err.message, "err");
    }
  };
  const markerChips = (editable) => markersFor({ tags }).map((marker) => h(editable ? "button" : "span", {
    class: "marker-chip", type: editable ? "button" : undefined, "data-marker": marker.tag,
    style: { "--marker-color": marker.color },
    title: editable ? t("inspector.remove_marker", { label: marker.label }) : marker.label,
    onclick: editable ? () => changeMarkers(removeMarkersChange([{ tags }], marker.tag)) : undefined,
  }, h("b", null, marker.glyph), marker.label,
  editable ? h("span", { class: "marker-chip-x", "aria-hidden": "true" }, "×") : null));
  function renderMarkers() {
    clear(markerWrap);
    markerWrap.append(...markerChips(true));
    markerWrap.appendChild(h("button", {
      class: "btn sm ghost", type: "button", "data-marker-add": "",
      onclick: (event) => {
        const rect = event.currentTarget.getBoundingClientRect();
        openContextMenu({ x: rect.left, y: rect.bottom + 4, returnFocus: event.currentTarget,
          items: markerMenuChildren([{ tags }], changeMarkers) });
      },
    }, icon("plus", "sm"), t("inspector.add_marker")));
  }
  renderMarkers();

  // Media
  let mediaPop = null;
  let mediaPopCleanup = null;
  const closeMediaPreview = () => {
    mediaPopCleanup?.();
    mediaPopCleanup = null;
    mediaPop?.__viewer?.destroy();
    mediaPop?.remove();
    mediaPop = null;
  };
  const openMediaPreview = (item, event) => {
    closeMediaPreview();
    const isImg = item.is_image || isImageUrl(item.url);
    const panel = h("aside", { class: "media-preview-pop", role: "dialog", "aria-label": item.caption || t("inspector.media_preview") });
    if (isImg) {
      const viewer = createImageViewer({ src: mediaUrl(item), alt: item.caption || "", className: "media-preview-viewer" });
      panel.appendChild(viewer.el);
      panel.__viewer = viewer;
    } else {
      const protocol = /^([a-z][a-z0-9+.-]*):/i.exec(item.url)?.[1] || "local";
      panel.append(
        h("div", { class: "media-preview-scheme" }, protocol),
        h("div", { class: "media-preview-title" }, item.caption || item.kind || t("editor.viewer.link")),
        h("div", { class: "media-preview-url" }, item.url),
        h("div", { class: "media-preview-note" }, t("inspector.media_no_iframe")));
    }
    panel.appendChild(h("div", { class: "media-preview-actions" },
      ctx.openInEditor ? h("button", { class: "btn ghost sm", type: "button", onclick: () => openMediaInEditor(item) },
        icon("edit", "sm"), t("editor.open_in_editor")) : null,
      h("button", { class: "btn primary sm", type: "button", onclick: async () => {
        try { await openMediaExternal(item); closeMediaPreview(); }
        catch (err) { toast(err.message, "err"); }
      } }, icon("external", "sm"), t("action.open")),
      h("button", { class: "btn ghost sm", type: "button", onclick: closeMediaPreview }, t("action.close"))));
    document.getElementById("overlay-root")?.appendChild(panel);
    const rect = panel.getBoundingClientRect();
    const x = Math.max(10, Math.min(window.innerWidth - rect.width - 10, (event?.clientX || window.innerWidth / 2) + 12));
    const y = Math.max(10, Math.min(window.innerHeight - rect.height - 10, (event?.clientY || 80) - 8));
    panel.style.left = `${x}px`;
    panel.style.top = `${y}px`;
    mediaPop = panel;
    const onDown = (ev) => { if (!panel.contains(ev.target)) closeMediaPreview(); };
    const onKey = (ev) => { if (ev.key === "Escape") closeMediaPreview(); };
    document.addEventListener("pointerdown", onDown, true);
    document.addEventListener("keydown", onKey);
    mediaPopCleanup = () => {
      document.removeEventListener("pointerdown", onDown, true);
      document.removeEventListener("keydown", onKey);
    };
  };
  const openMediaInEditor = (item, { preview = false } = {}) => {
    if (!ctx.openInEditor) return false;
    closeMediaPreview();
    ctx.openInEditor(mediaSource(item, unit), { preview });
    return true;
  };
  // Clicking a previewable attachment opens it in the Editor (as a preview
  // tab); links and unknown formats keep the small popover.
  const activateMedia = (item, event) => {
    if (mediaPreviewable(item) && openMediaInEditor(item, { preview: true })) return;
    openMediaPreview(item, event);
  };
  const mediaList = h("div", { class: "media-list" });
  const renderMedia = () => {
    clear(mediaList);
    if (!media.length) { mediaList.appendChild(h("div", { class: "muted", style: { fontSize: "10.5px" } }, t("inspector.no_media"))); return; }
    for (const item of media) {
      const isImg = item.is_image || isImageUrl(item.url);
      const thumb = h("div", { class: "thumb", onclick: (event) => activateMedia(item, event) }, isImg ? h("img", { src: mediaUrl(item), alt: item.caption || "" }) : icon("file", "sm"));
      if (isImg) attachImageContextMenu(thumb, () => ({ src: mediaUrl(item), kind: "image", caption: item.caption }));
      mediaList.appendChild(h("div", { class: "media-item" },
        thumb,
        h("div", { class: "txt" },
          h("button", { class: "t media-preview-trigger", type: "button", title: item.caption || item.kind || item.url,
            onclick: (event) => activateMedia(item, event) }, item.caption || item.kind || t("inspector.media")),
          h("div", { class: "u", title: item.url }, item.url)),
        ctx.openInEditor ? h("button", {
          class: "open-editor", type: "button", title: t("editor.open_in_editor"),
          "aria-label": t("editor.open_in_editor"), dataset: { mediaId: item.id },
          onclick: () => openMediaInEditor(item),
        }, icon("edit", "sm")) : null,
        h("button", {
          class: "open-external", type: "button", title: t("action.open_external"),
          onclick: () => openMediaExternal(item),
        }, icon("external", "sm")),
        readOnly ? null : h("button", {
          class: "x", title: t("action.remove"),
          onclick: async () => {
            if (!(await confirmDialog(t("action.remove") + "?", { danger: true }))) return;
            try {
              await api.deleteMedia(item.id);
              media = media.filter((m) => m.id !== item.id);
              unit.media = media;
              renderMedia();
              toast(t("toast.saved"), "ok");
            } catch (err) { toast(err.message, "err"); }
          },
        }, icon("x", "sm"))));
    }
  };
  renderMedia();
  const mediaUrlInput = h("input", {
    class: "input", placeholder: t("inspector.url"), style: { fontSize: "11px" },
  });
  const mediaKind = h("input", { class: "input", placeholder: t("inspector.kind"), style: { fontSize: "11px" } });
  const mediaCaption = h("input", { class: "input", placeholder: t("inspector.caption"), style: { fontSize: "11px" } });
  const mediaFileInput = h("input", { type: "file", class: "media-file-native" });
  let pendingFile = null;
  const fileChipName = h("span", { class: "media-file-name" });
  const fileChip = h("div", { class: "media-file-chip hidden" },
    icon("file", "sm"), fileChipName,
    h("button", {
      class: "media-file-clear", type: "button", title: t("action.remove"),
      onclick: (event) => { event.stopPropagation(); clearPendingFile(); },
    }, icon("x", "sm")));
  const chooseMediaBtn = h("button", { class: "btn sm ghost media-choose", type: "button" },
    icon("upload", "sm"), t("inspector.choose_file"));
  const addMediaBtn = h("button", { class: "btn sm primary", type: "button" },
    icon("plus", "sm"), t("inspector.add_media"));

  function clearPendingFile() {
    pendingFile = null;
    mediaFileInput.value = "";
    fileChip.classList.add("hidden");
    fileChipName.textContent = "";
    chooseMediaBtn.textContent = "";
    chooseMediaBtn.append(icon("upload", "sm"), t("inspector.choose_file"));
  }
  function setPendingFile(file) {
    pendingFile = file || null;
    if (!file) { clearPendingFile(); return; }
    mediaUrlInput.value = "";
    fileChipName.textContent = `${file.name} · ${(file.size / 1024).toFixed(0)} KB`;
    fileChip.classList.remove("hidden");
    chooseMediaBtn.textContent = "";
    chooseMediaBtn.append(icon("upload", "sm"), t("inspector.change_file"));
  }
  mediaUrlInput.addEventListener("input", () => {
    if (mediaUrlInput.value.trim()) clearPendingFile();
  });
  chooseMediaBtn.addEventListener("click", () => {
    mediaFileInput.value = "";
    mediaFileInput.click();
  });
  mediaFileInput.addEventListener("change", () => {
    setPendingFile(mediaFileInput.files?.[0] || null);
  });

  const embedFile = async (source) => {
    const file = await compressImage(source);
    if (!file) return;
    const data = await fileToBase64(file);
    await api.uploadMedia({
      unit: unit.id, filename: file.name, mime: file.type || "application/octet-stream",
      data, caption: mediaCaption.value.trim() || file.name,
    });
    const fresh = await api.unit(unit.id);
    unit = fresh.unit;
    ctx.S.detail = unit;
    media = fresh.unit.media || [];
    renderMedia();
    clearPendingFile();
    mediaCaption.value = "";
    toast(t("inspector.embedded_file", { name: file.name, size: (file.size / 1024).toFixed(0) }), "ok");
  };

  const addMedia = async () => {
    if (pendingFile) {
      addMediaBtn.disabled = true;
      try { await embedFile(pendingFile); }
      catch (err) { toast(err.message, "err"); }
      finally { addMediaBtn.disabled = false; }
      return;
    }
    const url = mediaUrlInput.value.trim();
    if (!url) {
      toast(t("inspector.media_source_required"), "err");
      return;
    }
    try {
      await api.addMedia({
        unit: unit.id, url,
        kind: mediaKind.value.trim() || (isImageUrl(url) ? "image" : "link"),
        caption: mediaCaption.value.trim(),
      });
      const fresh = await api.unit(unit.id);
      unit = fresh.unit;
      ctx.S.detail = unit;
      media = fresh.unit.media || [];
      renderMedia();
      mediaUrlInput.value = ""; mediaCaption.value = ""; mediaKind.value = "";
      toast(t("toast.saved"), "ok");
    } catch (err) { toast(err.message, "err"); }
  };
  addMediaBtn.addEventListener("click", addMedia);

  const uploadPastedImage = async (file) => {
    const compressed = await compressImage(file);
    if (!compressed) return null;
    const data = await fileToBase64(compressed);
    const uploaded = await api.uploadMedia({
      unit: unit.id, filename: compressed.name || "pasted.png",
      mime: compressed.type || "image/png", data,
      caption: compressed.name || "Pasted image", // i18n-ignore: stored data
    });
    const fresh = await api.unit(unit.id);
    unit = fresh.unit;
    ctx.S.detail = unit;
    media = fresh.unit.media || [];
    renderMedia();
    return `/api/media/${uploaded.id}/raw`;
  };
  body.addEventListener("paste", async (event) => {
    if (readOnly) return;
    const file = [...(event.clipboardData?.files || [])].find((item) => item.type.startsWith("image/"));
    if (!file) return;
    event.preventDefault();
    try {
      const src = await uploadPastedImage(file);
      if (!src) return;
      const markdown = `![${file.name || "pasted image"}](${src})`;
      const start = body.selectionStart ?? body.value.length;
      const end = body.selectionEnd ?? start;
      body.setRangeText(markdown, start, end, "end");
      body.dispatchEvent(new Event("input", { bubbles: true }));
      toast(t("inspector.image_embedded_body"), "ok");
    } catch (err) { toast(err.message, "err"); }
  });

  // Links
  const outgoing = unit.links_out || [];
  const incoming = unit.links_in || [];
  const linksOut = h("div", { class: "link-list" });
  const linksIn = h("div", { class: "link-list" });
  if (!outgoing.length) linksOut.appendChild(h("div", { class: "muted", style: { fontSize: "10.5px" } }, t("inspector.no_links")));
  if (!incoming.length) linksIn.appendChild(h("div", { class: "muted", style: { fontSize: "10.5px" } }, t("inspector.no_links")));
  for (const link of outgoing) {
    linksOut.appendChild(linkRow(ctx, unit, link, link.target, "out", readOnly));
  }
  for (const link of incoming) {
    linksIn.appendChild(linkRow(ctx, unit, link, link.source, "in", readOnly));
  }

  const act = h("div", { class: "unit-log" });
  for (const item of (unit.activity || []).slice(0, 12)) {
    act.appendChild(h("div", { class: "row" },
      h("span", { class: "at", title: fmtDateTime(item.at) }, relativeTime(item.at)),
      h("span", { class: "act", title: item.action },
        h("b", { style: { color: "var(--sub)", fontWeight: "600" } }, actorLabel(item.actor)), " ",
        logLabel(item.action), item.detail?.title ? ` · ${item.detail.title}` : "")));
  }
  if (!act.children.length) act.appendChild(h("div", { class: "muted", style: { fontSize: "10.5px" } }, t("inspector.no_activity")));

  async function save() {
    if (!title.value.trim()) {
      saveState.textContent = t("inspector.title_required");
      saveState.classList.remove("pending");
      dirty = true;
      return;
    }
    if (saving) return;
    saving = true;
    const rev = revision;
    saveState.textContent = t("node.saving");
    saveState.classList.add("pending");
    try {
      unit = await saveUnitDraft(ctx, unit.id, {
        ...draftFromUnit(unit),
        title: title.value, body: body.value, type: typeSel.value, status: statusSel.value,
        importance: impSel.value, tags, start_at: start.value, end_at: end.value,
      }, "inspector");
      ctx.S.detail = unit;
      ctx.S.selectedId = unit.id;
      dirty = revision !== rev;
      saveState.textContent = dirty ? t("inspector.unsaved") : t("toast.saved");
      saveState.classList.toggle("pending", dirty);
      if (dirty && !saveTimer) saveTimer = setTimeout(() => { saveTimer = null; save(); }, 400);
    } catch (err) {
      saveState.textContent = t("inspector.save_failed");
      saveState.classList.remove("pending");
      toast(err.message, "err");
    } finally {
      saving = false;
    }
  }

  const close = async (force = false) => {
    if (preview) { onClose?.(); return; }
    clearTimeout(saveTimer);
    saveTimer = null;
    if (dirty) await save();
    closeInspector(ctx);
  };

  const drawer = h("aside", {
    class: `drawer ${docked ? "docked" : "floating"} ${mobileSheet ? "mobile-sheet" : ""} ${preview ? "unit-ref-preview" : ""}`.trim(),
    role: "complementary", "aria-label": t("inspector.details"),
  });
  drawer.appendChild(inspectorResizer(ctx));
  if (!docked) {
    const floatResizer = h("div", { class: "drawer-float-resizer", title: t("inspector.resize_float") });
    floatResizer.addEventListener("pointerdown", (e) => {
      e.preventDefault();
      e.stopPropagation();
      const rect = drawer.getBoundingClientRect();
      const startX = e.clientX;
      const startY = e.clientY;
      const startW = rect.width;
      const startH = rect.height;
      const maxW = Math.max(320, window.innerWidth - rect.left - 12);
      const maxH = Math.max(260, window.innerHeight - rect.top - 12);
      const move = (ev) => {
        const width = Math.round(Math.max(320, Math.min(maxW, startW + ev.clientX - startX)));
        const height = Math.round(Math.max(260, Math.min(maxH, startH + ev.clientY - startY)));
        drawer.style.width = `${width}px`;
        drawer.style.height = `${height}px`;
        document.documentElement.style.setProperty("--inspector-float-w", `${width}px`);
        document.documentElement.style.setProperty("--inspector-float-h", `${height}px`);
      };
      const up = () => {
        try {
          const width = Math.round(drawer.getBoundingClientRect().width);
          const height = Math.round(drawer.getBoundingClientRect().height);
          localStorage.setItem("ark.inspectorFloatW", String(width));
          localStorage.setItem("ark.inspectorFloatH", String(height));
        } catch { /* storage can be unavailable */ }
        window.removeEventListener("pointermove", move);
        window.removeEventListener("pointerup", up);
      };
      window.addEventListener("pointermove", move);
      window.addEventListener("pointerup", up);
    });
    drawer.appendChild(floatResizer);
  }
  const pinBtn = h("button", {
    class: `icon-btn ${docked ? "on" : ""}`.trim(),
    title: docked ? t("inspector.unpin") : t("inspector.pin"),
    onclick: async () => {
      if (preview) {
        onClose?.();
        await ctx.openUnit(unit.uid, anchor, { revealPane: true });
        return;
      }
      ctx.S.inspectorPane = !docked;
      localStorage.setItem("ark.inspectorPane", ctx.S.inspectorPane ? "1" : "0");
      await openInspector(ctx, unit.uid, ctx.S.drawerAnchor, { forceFloating: false });
      ctx.renderTopbar?.();
    },
  }, icon("pin", "sm"));
  const enterEdit = () => {
    if (preview) {
      const rect = drawer.getBoundingClientRect();
      onClose?.();
      ctx.openUnit(unit.uid, { x: rect.left - 16, y: rect.top + 18 }, { forceFloating: true, readOnly: false });
      return;
    }
    if (graph?.readOnly) return;
    if (typeof ctx.syncInspectorMode === "function") {
      ctx.syncInspectorMode("edit");
      return;
    }
    graph?.setMode?.("edit");
    renderInspector(ctx, unit, { docked, anchor, readOnly: false, graph, mobileSheet });
  };
  const enterRead = () => {
    if (typeof ctx.syncInspectorMode === "function") {
      ctx.syncInspectorMode("read");
      return;
    }
    graph?.setMode?.("read");
    renderInspector(ctx, unit, { docked, anchor, readOnly: true, graph, mobileSheet });
  };
  const modeButton = readOnly
    ? h("button", {
      class: "btn sm ghost inspector-mode-btn", type: "button",
      title: t("inspector.edit_attributes"), "aria-label": t("inspector.edit_attributes"),
      onclick: enterEdit,
    }, icon("pen", "sm"), t("action.edit"))
    : h("button", {
      class: "btn sm ghost inspector-mode-btn", type: "button",
      title: t("inspector.read_attributes"), "aria-label": t("inspector.read_attributes"),
      onclick: enterRead,
    }, icon("book", "sm"), t("inspector.read"));
  const head = h("div", { class: "drawer-head" },
    h("div", { class: "drawer-identity" },
      h("span", { class: "type-badge", style: { "--type-color": typeColor(unit.type) } }, typeLabel(unit.type)),
      h("span", { class: "uid" }, unit.uid),
      h("span", { class: "status-pill", style: { "--status-color": statusColor(unit.status) } }, h("i"), statusLabel(unit.status))),
    h("div", { class: "drawer-actions" },
    modeButton,
    h("button", { class: "icon-btn", type: "button", title: t("history.title"), "aria-label": t("history.title"),
      onclick: () => openNodeHistory(unit, ctx) }, icon("clock", "sm")),
    ctx.openUnitInEditor ? h("button", {
      class: "icon-btn inspector-open-editor", type: "button",
      title: t("editor.open_in_editor"), "aria-label": t("editor.open_in_editor"),
      onclick: () => ctx.openUnitInEditor(unit),
    }, icon("file", "sm")) : null,
    pinBtn,
    // On the Map the node is already in view; elsewhere this opens the Map
    // with the node selected and centred.
    ctx.S.tab === "map" ? null : h("button", {
      class: "icon-btn", title: t("inspector.open_map"), "aria-label": t("inspector.open_map"),
      onclick: () => { if (preview) onClose?.(); else closeInspector(ctx); ctx.showOnMap ? ctx.showOnMap(unit.uid) : ctx.navigate("map"); },
    }, icon("map", "sm")),
    // ⋯: the node's menu (the same as a right click on its row or card).
    h("button", {
      class: "icon-btn inspector-more", type: "button", title: t("inspector.more"), "aria-label": t("inspector.more"), "aria-haspopup": "menu",
      onclick: (event) => {
        const rect = event.currentTarget.getBoundingClientRect();
        const items = unitMenuItems(ctx, unit).filter((item) => !item?.id || item.id !== "open-details");
        openContextMenu({ x: rect.right - 240, y: rect.bottom + 4, items, returnFocus: event.currentTarget });
      },
    }, icon("more", "sm")),
    h("button", { class: "icon-btn drawer-close", title: t("action.close"), onclick: () => close(false) }, icon("x"))));
  // Accept / Send back under the comment box for a unit in review.
  let mobilePermission = "";
  try { mobilePermission = localStorage.getItem("ark.mobile.permission") || ""; } catch { /* storage unavailable */ }
  const reopen = () => ctx.openUnit?.(unit.uid, null, { readOnly });
  const review = canDecideReview(unit, { permission: ctx.S.project?.permission, mobilePermission }) ? {
    accept: (text, clearInput) => acceptUnit(ctx, unit, { initial: text, onDone: () => { clearInput(); reopen(); } }),
    sendBack: (text, clearInput) => sendBackUnit(ctx, unit, { initial: text, onDone: () => { clearInput(); reopen(); } }),
  } : null;
  const comments = commentsSection(unit, {
    heading: "h4", className: readOnly ? "read-section" : "link-section", ctx, review,
    onChange: (list) => { if (ctx.S.detail?.id === unit.id) ctx.S.detail.comments = list; },
  });
  const readSection = (heading, content) => h("section", { class: "read-section" },
    heading ? h("h4", null, heading) : null, content);
  // Topics: a topic opens its subgraph; any node lists the topics showing it.
  const topicsBlock = () => h("div", { class: "inspector-topics" },
    ...(unit.topics || []).map((topic) => h("button", {
      class: "chip", type: "button", title: topic.uid,
      onclick: () => { close(false); ctx.openTopic?.(topic.uid); },
    }, icon("layers", "sm"), topic.title)),
    h("button", {
      class: "btn sm ghost", type: "button", onclick: () => openAddToTopic(ctx, [unit]),
    }, icon("plus", "sm"), t("topic.add_to")));
  const readFact = (label, value) => h("div", { class: "read-fact" },
    h("span", null, label), h("b", null, value));
  const readBody = h("div", { class: "drawer-body read-view" },
    h("div", { class: "read-title" }, unit.title),
    reviewPanel(ctx, unit),
    h("div", { class: "read-facts" },
      readFact(t("common.type"), typeLabel(unit.type)),
      readFact(t("common.status"), statusLabel(unit.status)),
      readFact(t("common.importance"), impLabel(unit.importance)),
      readFact(t("assignee.label"), assigneeLabel(unit)),
      unit.start_at ? readFact(t("common.start"), fmtDate(unit.start_at)) : null,
      unit.end_at ? readFact(t("common.end"), fmtDate(unit.end_at)) : null),
    unit.type === "topic" ? readSection(t("topic.settings.title"), topicSettings(ctx, unit)) : null,
    unit.source_ref || unit.source_title ? readSection(t("inspector.source"),
      h("div", { class: "read-source" },
        h("div", null, unit.source_title || unit.source_ref),
        unit.source_ref ? h("div", { class: "mono" }, unit.source_ref) : null,
        h("div", { class: "muted" }, `${t("inspector.origin")}: ${originLabel(unit.origin)} · ${t("common.created")} ${fmtDate(unit.created_at)}`))) : null,
    crossProjectSection(unit) ? readSection(t("cross_project.title"), crossProjectSection(unit)) : null,
    unit.body ? readSection(t("inspector.body"), h("div", { class: "read-body" }, inspectorRichBody(unit))) : null,
    readOnly ? comments : null,
    markersFor({ tags }).length ? readSection(t("inspector.markers"),
      h("div", { class: "marker-editor read-markers" }, markerChips(false))) : null,
    tags.some((tag) => !isMarkerTag(tag)) ? readSection(t("inspector.tags"),
      h("div", { class: "read-tags" }, tags.filter((tag) => !isMarkerTag(tag)).map((tag) => h("span", { class: "tag static" }, tag)))) : null,
    readSection(t("nav.topic"), topicsBlock()),
    readSection(t("inspector.connections"), h("div", { class: "read-links" },
      h("div", { class: "muted" }, t("inspector.outgoing")), linksOut,
      h("div", { class: "muted", style: { marginTop: "7px" } }, t("inspector.incoming")), linksIn)),
    readSection(t("inspector.media"), mediaList),
    readSection(t("inspector.activity"), act));
  // Read mode has no footer: Edit is the header's mode button (also the
  // node editor and the context menu) and the header closes the panel.
  const foot = readOnly
    ? null
    : h("div", { class: "drawer-foot" },
      h("button", { class: "btn ghost", onclick: archive, title: t("action.move_to_trash") }, icon("trash", "sm"), t("action.delete")),
      h("div", { class: "spacer" }),
      h("button", { class: "btn ghost drawer-close", onclick: () => close(false) }, t("action.close")),
      saveState);
  const bodyEl = readOnly ? readBody : h("div", { class: "drawer-body" },
    title,
    h("div", { class: "field-row three" },
      field(t("common.type"), typeSel),
      field(t("common.status"), statusSel),
      field(t("common.importance"), impSel)),
    field(t("assignee.label"), assigneePicker(ctx, unit)),
    unit.type === "topic" ? h("div", { class: "link-section" }, h("h4", null, t("topic.settings.title")), topicSettings(ctx, unit)) : null,
    unit.source_ref || unit.source_title ? h("div", { class: "provenance" },
      h("b", null, t("inspector.source")), ": ", unit.source_title || unit.source_ref,
      unit.source_ref ? h("div", { class: "mono", style: { fontSize: "9.5px", marginTop: "2px" } }, unit.source_ref) : null,
      h("div", { style: { marginTop: "3px" } }, `${t("inspector.origin")}: ${originLabel(unit.origin)} · ${t("common.created")} ${fmtDate(unit.created_at)}`)) : null,
    crossProjectSection(unit) ? h("div", { class: "provenance" }, h("b", null, t("cross_project.title")), crossProjectSection(unit)) : null,
    unit.body ? h("details", { class: "report-preview", open: ["artifact", "note"].includes(unit.type) ? true : undefined },
      h("summary", null, t("inspector.rendered_report")),
      inspectorRichBody(unit)) : null,
    field(t("inspector.body"), body, t("inspector.body_hint")),
    readOnly ? null : reviewPanel(ctx, unit),
    readOnly ? null : comments,
    field(t("inspector.markers"), markerWrap),
    field(t("inspector.tags"), tagWrap, t("inspector.tags_hint")),
    h("div", { class: "field-row" },
      field(t("common.start"), start), field(t("common.end"), end)),
    h("div", { class: "link-section" }, h("h4", null, t("nav.topic")), topicsBlock()),
    h("div", { class: "link-section" },
      h("h4", null, t("inspector.connections"),
        h("span", { style: { color: "var(--mute)", fontWeight: "400", textTransform: "none", letterSpacing: "0" } }, `${outgoing.length + incoming.length}`)),
      h("div", { style: { fontSize: "10px", color: "var(--mute)", marginBottom: "5px" } }, t("inspector.outgoing")),
      linksOut,
      h("div", { style: { fontSize: "10px", color: "var(--mute)", margin: "7px 0 5px" } }, t("inspector.incoming")),
      linksIn,
      h("button", { class: "btn sm ghost", style: { marginTop: "7px" }, onclick: () => openLinkModal(ctx, unit) }, icon("link", "sm"), t("inspector.add_connection"))),
    h("div", { class: "link-section" },
      h("h4", null, t("inspector.media"), h("span", { style: { color: "var(--mute)", fontWeight: "400", textTransform: "none", letterSpacing: "0" } }, media.length)),
      mediaList,
      h("div", { class: "media-composer" },
        fileChip,
        mediaUrlInput,
        h("div", { class: "media-source-row" }, chooseMediaBtn, addMediaBtn),
        h("div", { class: "field-row" }, mediaKind, mediaCaption),
        h("div", { class: "muted media-hint" }, t("inspector.media_hint")),
        mediaFileInput)),
    h("div", { class: "link-section" },
      h("h4", null, t("inspector.activity")),
      act),
    canRunWithAgent(unit) ? h("div", { style: { marginTop: "10px" } },
      h("button", { class: "btn", onclick: () => (ctx.dispatchTaskSession ? ctx.dispatchTaskSession(unit) : openDispatchModal(ctx, unit)) }, icon("send", "sm"), t("inspector.dispatch"))) : null);
  drawer.append(...[head, presenceDetailsLine(unit), bodyEl, foot].filter(Boolean));
  if (!docked) {
    head.addEventListener("pointerdown", (event) => {
      if (event.button !== 0 || event.target.closest("button,input,textarea,select,a")) return;
      event.preventDefault();
      const rect = drawer.getBoundingClientRect();
      const startX = event.clientX, startY = event.clientY;
      const startLeft = rect.left, startTop = rect.top;
      drawer.classList.add("dragging");
      const move = (ev) => {
        const left = Math.max(8, Math.min(window.innerWidth - rect.width - 8, startLeft + ev.clientX - startX));
        const maxTop = Math.max(8, window.innerHeight - rect.height - 8);
        const top = Math.max(8, Math.min(maxTop, startTop + ev.clientY - startY));
        drawer.style.left = `${left}px`;
        drawer.style.top = `${top}px`;
        drawer.style.right = "auto";
        drawer.style.bottom = "auto";
      };
      const up = () => {
        drawer.classList.remove("dragging");
        if (!preview) {
          const rect = drawer.getBoundingClientRect();
          ctx.S.drawerAnchor = { x: rect.left - 16, y: rect.top + 18 };
        }
        window.removeEventListener("pointermove", move);
        window.removeEventListener("pointerup", up);
      };
      window.addEventListener("pointermove", move);
      window.addEventListener("pointerup", up);
    });
  }
  root.append(drawer);
  if (!preview) liveInspector = { ctx, projectId: ctx.S.project?.id, unitId: unit.id, isDirty: () => dirty || saving || Boolean(saveTimer) };
  if (!docked && !mobileSheet) placeFloating(drawer, anchor);
  autoGrow();
  title.addEventListener("input", autoGrow);
  return drawer;

  async function archive() {
    const ok = await confirmDialog(t("inspector.archive_confirm"), { title: t("mind.delete"), confirmLabel: t("action.move_to_trash") });
    if (!ok) return;
    try {
      await api.archiveUnit(unit.id);
      toast(t("mind.deleted"), "ok");
      closeInspector(ctx);
      // A graph keeps its camera and positions; the node leaves in place.
      if (ctx.refreshData) ctx.refreshData(); else ctx.refresh();
    } catch (err) { toast(err.message, "err"); }
  }
}

function inspectorRichBody(unit) {
  const content = richText(unit.body, { mediaKinds: mediaKinds(unit.media) });
  for (const media of content.querySelectorAll("img.rich-img, video.rich-video, audio.rich-audio")) {
    const label = media.alt || media.title || unit.title;
    const image = media.tagName === "IMG";
    const wrap = h("span", { class: `inspector-body-media kind-${media.tagName.toLowerCase()}` });
    const open = () => {
      const src = media.currentSrc || media.src;
      if (image) {
        openImageLightbox({ src, alt: media.alt, title: label });
        return;
      }
      const playing = !media.paused;
      const position = media.currentTime;
      media.pause();
      const player = h(media.tagName.toLowerCase(), {
        class: "inspector-media-player", src, controls: true, playsinline: true, preload: "metadata",
      });
      player.volume = media.volume;
      player.muted = media.muted;
      player.playbackRate = media.playbackRate;
      const ready = () => {
        player.currentTime = position;
        if (playing) player.play().catch(() => {});
      };
      player.addEventListener("loadedmetadata", ready, { once: true });
      const preview = openModal({
        title: label, wide: true, body: player,
        onClose: () => {
          if (player.readyState >= 1) media.currentTime = player.currentTime;
          media.volume = player.volume;
          media.muted = player.muted;
          media.playbackRate = player.playbackRate;
          player.pause();
          player.removeEventListener("loadedmetadata", ready);
          player.removeAttribute("src");
          player.load();
          trigger.focus({ preventScroll: true });
        },
      });
      preview.modal.classList.add("media-preview-dialog", "inspector-media-dialog");
      preview.modal.setAttribute("role", "dialog");
      preview.modal.setAttribute("aria-modal", "true");
      preview.modal.setAttribute("aria-label", label);
      preview.backdrop.classList.add("media-preview-backdrop");
      preview.modal.querySelector(".modal-head button")?.focus({ preventScroll: true });
    };
    const title = t(image ? "viewer.open" : "inspector.expand_media");
    const trigger = h("button", {
      class: image ? "inspector-media-image" : "icon-btn inspector-media-expand", type: "button",
      title, "aria-label": `${title}: ${label}`, onclick: open,
    }, image ? null : icon("maximize", "sm"));
    media.replaceWith(wrap);
    if (image) {
      trigger.appendChild(media);
      wrap.appendChild(trigger);
      attachImageContextMenu(trigger, () => ({ src: media.currentSrc || media.src, kind: "image", caption: media.alt }));
    }
    else wrap.append(media, trigger);
  }
  return content;
}

function field(label, input, hint = null) {
  return h("div", { class: "field" }, h("label", null, label, hint ? h("span", { class: "hint" }, hint) : null), input);
}

function linkRow(ctx, unit, link, other, direction, readOnly = false) {
  return h("div", { class: "link-item" },
    h("span", { class: "rel" }, direction === "in" ? `← ${relLabel(link.relation)}` : relLabel(link.relation)),
    h("span", { class: "lnk", title: `${other.uid} · ${other.title}`, onclick: () => ctx.openUnit(other.uid) },
      h("span", { class: "uid", style: { marginRight: "5px" } }, other.uid), other.title),
    readOnly ? null : h("button", {
      class: "x", title: t("action.remove"),
      onclick: async () => {
        if (!(await confirmDialog(t("action.remove") + "?", { danger: true }))) return;
        try {
          await api.deleteLink(link.id);
          toast(t("toast.unlinked"), "ok");
          await ctx.reloadDetail();
          if (ctx.refreshData) ctx.refreshData(); else ctx.refresh();
        } catch (err) { toast(err.message, "err"); }
      },
    }, icon("x", "sm")));
}
