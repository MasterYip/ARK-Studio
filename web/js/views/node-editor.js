// Node tabs ("unit:<uid>") in the Editor workspace: a wide, document-like
// editor for one ARK unit.  Same data, rules and APIs as the inspector side
// panel (node-model.js / unit-edit.js), laid out as a page:
//   header (title, type, status, importance, dates, tags, assignees)
//   body as Source | Split | Preview with live rich preview
//   media gallery (players, lightbox, captions, add/remove)
//   links (incoming/outgoing) and an activity strip.
// Tab state (dirty dot, preview/pinned, close confirm, persistence) is owned
// by editor.js through the `host` callbacks passed to renderNodeTab().

import { h, clear, richText, typesetMath, typesetMermaid, typeColor, statusColor, fmtDate, fmtDateTime, relativeTime, avatarFor, debounce } from "../dom.js";
import { t, typeLabel, statusLabel, relLabel, logLabel } from "../i18n.js";
import { icon } from "../icons.js";
import { api, mediaUrl } from "../api.js";
import { confirmDialog, openLinkModal, toast, actorLabel } from "../components.js";
import { getShortcut, shortcutLabel } from "../shortcuts.js";
import {
  draftFromUnit, draftChanged, validateDraft, addTag, normalizeMode, linkRows,
  mediaMarkdown, mediaKinds, galleryKind, BODY_MODES,
} from "../node-model.js";
import { hashText } from "../rich-model.js";
import { openImageLightbox } from "../image-viewer.js";
import {
  UNIT_SAVED_EVENT, saveUnitDraft, uploadUnitFile, addUnitMediaUrl, openMediaExternal,
} from "../unit-edit.js";
import { mediaSource } from "../editor-media.js";
import { assigneePicker } from "../assignee.js";
import { commentsSection } from "../comments.js";
import { openNodeHistory } from "../node-history.js";
import { UNIT_RESTORE_CHECK_EVENT } from "../unit-edit.js";
import { takeNodeReveal, lineRange } from "../node-reveal.js";
import { registerSurface, caretLine, caretLineOf, previewAnchors, textareaAnchors } from "../scroll-sync.js";

const MODE_KEY = "ark.editor.nodeMode";
const PREVIEW_DELAY = 220;
const nodes = new Map();      // tab key -> node state
let host = null;              // callbacks from editor.js (last render)
window.addEventListener(UNIT_RESTORE_CHECK_EVENT, (event) => {
  if ([...nodes.values()].some((state) => state.projectId === event.detail?.project
      && state.unit?.id === event.detail?.id && (isDirty(state) || state.saving))) {
    event.preventDefault();
  }
});

function isPhone() {
  return Boolean(globalThis.matchMedia?.("(max-width: 760px)")?.matches);
}

function storedMode() {
  try { return localStorage.getItem(MODE_KEY) || ""; } catch { return ""; }
}

export function dropNodeTab(key) { nodes.delete(key); }

function stateFor(tab) {
  let state = nodes.get(tab.key);
  if (!state) {
    state = { key: tab.key, uid: tab.uid, projectId: host?.ctx?.S?.project?.id, status: "loading", mode: "" };
    nodes.set(tab.key, state);
    load(state);
  }
  return state;
}

async function load(state, { keepDraft = false } = {}) {
  state.status = state.unit ? state.status : "loading";
  try {
    const data = await api.unit(state.uid);
    applyUnit(state, data.unit, { keepDraft });
    state.status = "ready";
    state.error = "";
  } catch (err) {
    state.status = state.unit ? "ready" : "error";
    state.error = err.message || String(err);
    if (state.unit) toast(state.error, "err");
  }
  host?.rename(state.key, state.unit?.title);
  repaintIf(state);
}

function applyUnit(state, unit, { keepDraft = false } = {}) {
  state.unit = unit;
  state.base = draftFromUnit(unit);
  state.updatedAt = unit.updated_at || "";
  if (!keepDraft || !state.draft) state.draft = draftFromUnit(unit);
  state.conflict = false;
  syncDirty(state);
}

function isDirty(state) {
  return Boolean(state.draft && state.base && draftChanged(state.draft, state.base));
}

function syncDirty(state) {
  host?.setDirty(state.key, isDirty(state));
}

function repaintIf(state) {
  if (host?.isActive(state.key)) host.repaint();
}

// Saves made in the inspector (or another Editor tab) update clean tabs and
// flag a conflict on tabs with local edits.
window.addEventListener(UNIT_SAVED_EVENT, (event) => {
  const { unit, origin } = event.detail || {};
  if (!unit) return;
  for (const state of nodes.values()) {
    // A tab that is saving applies its own result (see saveNodeTab).
    if (state.status !== "ready" || state.saving || state.unit?.id !== unit.id) continue;
    if (origin === "assign") {
      // "Assign to" (right-click menu) only changes ownership, which is not
      // part of the text draft: apply it without flagging a conflict.
      state.unit = { ...state.unit, assignee: unit.assignee, assignee_effective: unit.assignee_effective,
        assignee_from: unit.assignee_from, updated_at: unit.updated_at };
      state.updatedAt = unit.updated_at || state.updatedAt;
      repaintIf(state);
      continue;
    }
    if (isDirty(state)) {
      state.conflict = true;
      state.remote = unit;
    } else {
      applyUnit(state, { ...state.unit, ...unit });
      host?.rename(state.key, unit.title);
    }
    repaintIf(state);
  }
});

// Another person changed open units (live-sync delta; ids null = unknown):
// revalidate them; newer data comes back through UNIT_SAVED_EVENT "remote".
window.addEventListener("ark:units-touched", (event) => {
  const ids = event.detail?.ids;
  for (const state of nodes.values()) {
    if (state.status !== "ready" || state.saving || !state.unit) continue;
    if (!ids || ids.includes(state.unit.id)) api.unit(state.uid).catch(() => {});
  }
});

// ------------------------------------------------------------------ saving
export async function saveNodeTab(key, { force = false } = {}) {
  const state = nodes.get(key);
  if (!state || state.status !== "ready" || state.saving) return false;
  const errors = validateDraft(state.draft);
  if (errors.length) {
    state.errors = errors;
    toast(t(errors[0].key), "err");
    repaintIf(state);
    return false;
  }
  state.errors = [];
  if (!isDirty(state)) return true;
  state.saving = true;
  paintSaveState(state);
  try {
    if (!force) {
      // Same unit edited elsewhere since we loaded it?  Ask instead of
      // silently overwriting (the API itself is last-write-wins).
      const fresh = await api.unit(state.uid, { fresh: true });
      if ((fresh.unit.updated_at || "") !== state.updatedAt && draftChanged(draftFromUnit(fresh.unit), state.base)) {
        state.conflict = true;
        state.remote = fresh.unit;
        return false;
      }
    }
    const saved = await saveUnitDraft(host?.ctx, state.unit.id, state.draft, "editor");
    // Apply the canonical result and keep any text typed while saving.
    const typed = state.draft;
    applyUnit(state, { ...state.unit, ...saved });
    if (draftChanged(typed, state.base)) state.draft = typed;
    syncDirty(state);
    host?.rename(state.key, saved.title);
    toast(t("node.saved", { name: saved.uid }), "ok");
    return true;
  } catch (err) {
    toast(err.message || String(err), "err");
    return false;
  } finally {
    state.saving = false;
    repaintIf(state);
  }
}

async function resolveConflict(state, action) {
  if (action === "reload") {
    if (isDirty(state) && !(await confirmDialog(t("node.reload_confirm"), { danger: true, confirmLabel: t("node.reload") }))) return;
    state.draft = null;
    await load(state);
    return;
  }
  state.conflict = false;
  // Overwrite: rebase onto the remote version, then save our draft over it.
  if (state.remote) {
    state.base = draftFromUnit(state.remote);
    state.updatedAt = state.remote.updated_at || "";
  }
  await saveNodeTab(state.key, { force: true });
}

// ------------------------------------------------------------------ render
// Bindings of the mounted node pages. With split editor groups the same node
// can be shown twice; both pages edit one shared draft (state.draft).
const views = new Set();

function liveViews(state) {
  for (const item of [...views]) if (!item.saveBtn.isConnected) views.delete(item);
  return [...views].filter((item) => item.state === state);
}

function paintSaveState(state) {
  const dirty = isDirty(state);
  for (const view of liveViews(state)) {
    view.saveBtn.disabled = !dirty || Boolean(state.saving);
    view.saveBtn.classList.toggle("primary", dirty);
    view.saveState.textContent = state.saving ? t("node.saving") : dirty ? t("node.unsaved") : t("node.all_saved");
    view.saveState.classList.toggle("pending", dirty || Boolean(state.saving));
  }
}

// Editing marks the tab dirty, which also pins a preview tab (editor-model);
// other groups showing the same node refresh from the shared draft.
function edited(state) {
  syncDirty(state);
  paintSaveState(state);
  host?.edited?.(state.key);
}

export function renderNodeTab(main, tab, editorHost) {
  host = editorHost;
  const state = stateFor(tab);
  if (state.status === "loading") {
    main.appendChild(h("div", { class: "ed-loading" }, h("span", { class: "spinner" }), t("action.loading")));
    return;
  }
  if (state.status === "error") {
    main.appendChild(h("div", { class: "ed-info" },
      h("div", { class: "ed-info-icon" }, icon("alert", "lg")),
      h("h3", null, tab.uid),
      h("p", null, `${t("node.not_found")} ${state.error || ""}`),
      h("div", { class: "ed-info-actions" },
        h("button", { class: "btn sm", type: "button", onclick: () => { state.status = "loading"; load(state); host.repaint(); } },
          icon("refresh", "sm"), t("node.reload")))));
    return;
  }
  // The body mode is per editor group, so the same node can show its
  // Source in one group and its Preview in another (scroll-synced).
  const group = main.closest?.(".ed-group")?.dataset.group || "";
  state.modes ||= {};
  const pane = { mode: normalizeMode(state.modes[group] || state.mode || storedMode(), { phone: isPhone() }), lastMode: "" };
  state.modes[group] = pane.mode;
  const unit = state.unit;
  if (unit.title && tab.title !== unit.title) host.rename(tab.key, unit.title);
  const draft = state.draft;
  const ctx = host.ctx;

  // ---- toolbar
  const saveBtn = h("button", {
    class: "btn sm ed-save nd-save", type: "button", title: shortcutLabel(getShortcut("editorSave")),
    onclick: () => saveNodeTab(state.key),
  }, icon("save", "sm"), t("files.save"));
  const saveState = h("span", { class: "nd-save-state" });
  const modeSeg = h("div", { class: "ed-seg nd-mode", role: "group", "aria-label": t("node.body") },
    BODY_MODES.map((id) => h("button", {
      class: pane.mode === id ? "on" : "", type: "button", "aria-pressed": pane.mode === id ? "true" : "false",
      dataset: { nodeMode: id },
      onclick: () => {
        pane.mode = id;
        state.modes[group] = id;
        try { localStorage.setItem(MODE_KEY, id); } catch { /* ignore */ }
        setMode(id);
      },
    }, t(`node.${id}`))));
  const toolbar = h("div", { class: "ed-toolbar nd-toolbar" },
    h("span", { class: "ed-crumb" }, icon("node", "sm"),
      h("span", { class: "type-badge", style: { "--type-color": typeColor(unit.type) } }, typeLabel(unit.type)),
      h("span", { class: "leaf mono" }, unit.uid)),
    tab.preview ? h("span", { class: "ed-badge preview" }, t("editor.preview_tab")) : null,
    h("span", { class: "spacer" }),
    saveState,
    modeSeg,
    h("button", { class: "icon-btn", type: "button", title: t("history.title"), "aria-label": t("history.title"),
      onclick: () => openNodeHistory(unit, ctx) }, icon("clock", "sm")),
    h("button", {
      class: "icon-btn", type: "button", title: t("node.open_details"), "aria-label": t("node.open_details"),
      onclick: () => ctx.openUnit?.(unit.uid),
    }, icon("panel-right", "sm")),
    h("button", {
      class: "icon-btn", type: "button", title: t("node.reload"), "aria-label": t("node.reload"),
      onclick: () => resolveConflict(state, "reload"),
    }, icon("refresh", "sm")),
    saveBtn);
  main.appendChild(toolbar);

  if (state.conflict) {
    main.appendChild(h("div", { class: "ed-banners" }, h("div", { class: "ed-banner warn nd-conflict" },
      icon("alert", "sm"), t("node.conflict"),
      h("button", { class: "btn sm", type: "button", onclick: () => resolveConflict(state, "reload") }, t("node.reload")),
      h("button", { class: "btn sm danger", type: "button", onclick: () => resolveConflict(state, "overwrite") }, t("node.overwrite")))));
  }
  const errorFor = (field) => (state.errors || []).find((item) => item.field === field);

  // ---- header
  const title = h("textarea", {
    class: `nd-title ${errorFor("title") ? "invalid" : ""}`.trim(), rows: "1", value: draft.title,
    placeholder: t("common.title"), "aria-label": t("common.title"), maxlength: "240",
  });
  const grow = () => { title.style.height = "auto"; title.style.height = `${Math.min(160, title.scrollHeight)}px`; };
  title.addEventListener("input", () => { draft.title = title.value; grow(); edited(state); });
  title.addEventListener("keydown", (event) => { if (event.key === "Enter") { event.preventDefault(); source.focus(); } });

  const select = (field, items, label) => {
    const el = h("select", { class: "input", "aria-label": t(`node.field.${field}`), dataset: { field } },
      (items || []).map((item) => h("option", { value: item.id }, label(item.id))));
    el.value = draft[field];
    el.addEventListener("change", () => { draft[field] = el.value; edited(state); });
    return el;
  };
  const vocab = ctx.S?.vocab || {};
  const typeSel = select("type", vocab.types, typeLabel);
  const statusSel = select("status", vocab.statuses, statusLabel);
  const impSel = select("importance", vocab.importance, (id) => t(`imp.long.${id}`));
  const dateInput = (field) => {
    const el = h("input", { class: `input ${errorFor(field) ? "invalid" : ""}`.trim(), type: "date", value: draft[field] || "", dataset: { field } });
    el.addEventListener("input", () => { draft[field] = el.value; edited(state); });
    return el;
  };

  const tagWrap = h("div", { class: "tag-editor nd-tags" });
  const tagInput = h("input", {
    placeholder: t("inspector.add_tag"), "aria-label": t("inspector.tags"),
    onkeydown: (event) => {
      if (event.key === "Enter" || event.key === ",") {
        event.preventDefault();
        draft.tags = addTag(draft.tags, event.target.value);
        event.target.value = "";
        renderTags(); edited(state);
      } else if (event.key === "Backspace" && !event.target.value && draft.tags.length) {
        draft.tags = draft.tags.slice(0, -1);
        renderTags(); edited(state);
      }
    },
  });
  const renderTags = () => {
    clear(tagWrap);
    for (const tag of draft.tags) {
      tagWrap.appendChild(h("span", { class: "tag" }, tag,
        h("button", {
          type: "button", title: t("action.remove"), "aria-label": `${t("action.remove")} ${tag}`,
          onclick: () => { draft.tags = draft.tags.filter((x) => x !== tag); renderTags(); edited(state); },
        }, "×")));
    }
    tagWrap.appendChild(tagInput);
  };
  renderTags();

  const assignees = unit.assignees || [];
  const field = (label, control, cls = "") => h("label", { class: `nd-field ${cls}`.trim() }, h("span", null, label), control);
  const header = h("header", { class: "nd-head" },
    title,
    h("div", { class: "nd-meta" },
      field(t("common.type"), typeSel),
      field(t("common.status"), statusSel),
      field(t("common.importance"), impSel),
      field(t("common.start"), dateInput("start_at")),
      field(t("common.end"), dateInput("end_at"), errorFor("end_at") ? "invalid" : "")),
    h("div", { class: "nd-meta-row" },
      field(t("inspector.tags"), tagWrap, "wide"),
      h("div", { class: "nd-field nd-owner" }, h("span", null, t("assignee.label")),
        assigneePicker(ctx, unit, { onSaved: (saved) => {
          // Assignee is not part of the text draft: apply it without a conflict.
          state.unit = { ...state.unit, assignee: saved.assignee, assignee_effective: saved.assignee_effective,
            assignee_from: saved.assignee_from, updated_at: saved.updated_at };
          state.updatedAt = saved.updated_at || state.updatedAt;
        } })),
      h("div", { class: "nd-field nd-assignees" }, h("span", null, t("node.assignees")),
        h("div", { class: "nd-assignee-list" }, assignees.length
          ? assignees.map((person) => h("button", {
            class: "nd-chip", type: "button", title: `${person.uid} · ${person.title}`,
            onclick: (event) => openLinked(person.uid, person.title, event),
          }, avatarFor(person, "sm"), h("span", null, person.title)))
          : h("span", { class: "muted" }, t("node.no_assignees"))))),
    h("div", { class: "nd-facts" },
      h("span", { class: "status-pill", style: { "--status-color": statusColor(unit.status) } }, h("i"), statusLabel(unit.status)),
      h("span", null, `${t("common.created")} ${fmtDate(unit.created_at)}`),
      unit.updated_at ? h("span", { title: fmtDateTime(unit.updated_at) }, t("node.updated", { time: relativeTime(unit.updated_at) })) : null,
      unit.source_title || unit.source_ref ? h("span", { class: "nd-source", title: unit.source_ref || "" }, icon("database", "sm"), unit.source_title || unit.source_ref) : null));

  // ---- body: source / split / preview
  const source = h("textarea", {
    class: "nd-source-input", value: draft.body, spellcheck: "false",
    placeholder: t("node.body_placeholder"), "aria-label": t("node.body"),
  });
  const previewPane = h("div", { class: "nd-preview", "aria-live": "off" });
  const bodyWrap = h("section", { class: `nd-body mode-${pane.mode}` },
    h("div", { class: "nd-pane nd-pane-source" }, source),
    h("div", { class: "nd-pane nd-pane-preview" }, previewPane));
  const kinds = mediaKinds(unit.media);
  const paintPreview = () => {
    const fresh = richText(draft.body, { mediaKinds: kinds, vizScope: state.key, sourceLines: true });
    if (!draft.body.trim()) fresh.appendChild(h("p", { class: "muted nd-empty" }, t("node.empty_preview")));
    patchPreview(previewPane, fresh);
    previewSync?.observe(previewPane.firstElementChild);
  };
  let previewSync = null;
  const schedulePreview = debounce(() => { if (previewPane.isConnected) paintPreview(); }, PREVIEW_DELAY);
  source.addEventListener("input", () => { draft.body = source.value; edited(state); schedulePreview(); });
  source.addEventListener("paste", (event) => pasteImage(state, source, event));
  source.addEventListener("keydown", (event) => {
    if (event.key === "Tab" && !event.shiftKey && !event.ctrlKey && !event.altKey) {
      event.preventDefault();
      source.setRangeText("  ", source.selectionStart, source.selectionEnd, "end");
      source.dispatchEvent(new Event("input"));
    }
  });
  // Clicking a project link in the preview opens it in the Editor.
  previewPane.addEventListener("click", (event) => {
    const link = event.target.closest?.("a[data-ws-path], a[data-media-id]");
    if (!link) {
      const img = event.target.closest?.("img.rich-img");
      if (img) { event.preventDefault(); openLightbox(img.src, img.alt); }
      return;
    }
    event.preventDefault();
    if (link.dataset.wsPath) host.open({ kind: "ws", path: link.dataset.wsPath }, { preview: !(event.ctrlKey || event.metaKey) });
    else {
      const item = (unit.media || []).find((m) => String(m.id) === link.dataset.mediaId);
      if (item) host.open(mediaSource(item, unit), { preview: !(event.ctrlKey || event.metaKey) });
    }
  });
  const setMode = (mode) => {
    // Keep the place in the body when switching Source / Split / Preview.
    const from = pane.lastMode && pane.lastMode !== "preview" ? sourceSync : previewSync;
    const keepLine = from?.active() ? from.lineAt() : null;
    pane.lastMode = mode;
    bodyWrap.className = `nd-body mode-${mode}`;
    modeSeg.querySelectorAll("button").forEach((button) => {
      const on = button.dataset.nodeMode === mode;
      button.classList.toggle("on", on);
      button.setAttribute("aria-pressed", on ? "true" : "false");
    });
    if (mode !== "source") paintPreview();
    if (keepLine) {
      requestAnimationFrame(() => {
        const to = mode === "source" ? sourceSync : previewSync;
        to.ignoreUntil = performance.now() + 200;
        to.scrollToLine(keepLine);
      });
    }
  };

  // ---- media gallery
  const gallery = h("div", { class: "nd-gallery" });
  const mediaSection = mediaGallery(state, gallery);

  // ---- comments (own rows on the unit; saving the body never touches them)
  const commentSection = commentsSection(unit, {
    heading: "h3", className: "nd-section nd-comments", ctx: host?.ctx,
    onChange: (list) => { state.unit = { ...state.unit, comments: list }; },
  });

  // ---- links
  const linksSection = linksPanel(state);

  // ---- activity
  const activity = (unit.activity || []).slice(0, 16);
  const activitySection = h("section", { class: "nd-section nd-activity" },
    h("h3", null, t("node.activity"), h("span", { class: "count" }, String((unit.activity || []).length))),
    activity.length
      ? h("ol", { class: "nd-activity-strip" }, activity.map((item) => h("li", { title: fmtDateTime(item.at) },
        h("b", null, actorLabel(item.actor)), " ", logLabel(item.action),
        item.detail?.fields?.length ? h("span", { class: "muted" }, ` · ${item.detail.fields.join(", ")}`) : null,
        h("time", null, relativeTime(item.at)))))
      : h("p", { class: "muted" }, t("node.no_activity")));

  const page = h("div", { class: "nd-page" },
    header,
    h("section", { class: "nd-section nd-body-section" },
      h("h3", null, t("node.body")), bodyWrap),
    commentSection, mediaSection, linksSection, activitySection);
  const scroller = h("div", { class: "ed-body nd-scroll", dataset: { unit: unit.uid } }, page);
  main.appendChild(scroller);
  const status = h("div", { class: "ed-statusbar" },
    h("span", null, t("editor.viewer.node")),
    h("span", null, unit.uid),
    h("span", { class: "nd-status-chars" }, t("node.chars", { n: draft.body.length.toLocaleString() })),
    h("span", { class: "spacer" }),
    h("span", null, shortcutLabel(getShortcut("editorSave"))));
  main.appendChild(status);

  views.add({ state, saveBtn, saveState, source });
  // Scroll sync: this tab's source and preview (Split mode) and the same
  // node open in other groups.  In Preview mode the page scrolls.
  pane.lastMode = pane.mode;
  const sourceSync = registerSurface({
    doc: state.key, group, kind: "source", scrollEl: () => source,
    anchors: () => textareaAnchors(source), active: () => pane.mode !== "preview",
  });
  previewSync = registerSurface({
    doc: state.key, group, kind: "preview",
    scrollEl: () => (pane.mode === "preview" ? scroller : previewPane), listen: [scroller, previewPane],
    watch: previewPane.firstElementChild || previewPane, active: () => pane.mode !== "source",
    anchors: () => previewAnchors(pane.mode === "preview" ? scroller : previewPane, previewPane, draft.body.split(/\r\n|\r|\n/).length),
  });
  for (const name of ["keyup", "click"]) source.addEventListener(name, () => caretLine(state.key, caretLineOf(source)));
  paintSaveState(state);
  if (pane.mode !== "source") paintPreview();
  if (state.scrollTop) scroller.scrollTop = state.scrollTop;
  scroller.addEventListener("scroll", () => { state.scrollTop = scroller.scrollTop; }, { passive: true });
  requestAnimationFrame(grow);
  // Opened from a Search result: bring the matching field (and body line) into view.
  const reveal = takeNodeReveal(state.key);
  if (reveal) requestAnimationFrame(() => revealMatch(reveal, { scroller, title, tagWrap, source, previewPane, bodyWrap, commentSection, mode: pane.mode }));
}

function revealMatch(reveal, { scroller, title, tagWrap, source, previewPane, bodyWrap, commentSection, mode }) {
  const into = (el) => {
    if (!el) return;
    const top = el.getBoundingClientRect().top - scroller.getBoundingClientRect().top + scroller.scrollTop;
    scroller.scrollTop = Math.max(0, top - scroller.clientHeight / 4);
  };
  if (reveal.field === "title") {
    into(title);
    title.focus({ preventScroll: true });
    title.select();
  } else if (reveal.field === "tags") {
    into(tagWrap);
  } else if (reveal.field === "comment") {
    const row = reveal.commentId !== undefined && reveal.commentId !== null
      ? commentSection.querySelector(`[data-comment-id="${CSS.escape(String(reveal.commentId))}"]`) : null;
    into(row || commentSection);
    row?.classList.add("nd-flash");
  } else {
    into(bodyWrap);
    const range = lineRange(source.value, reveal.line || 1);
    const lines = Math.max(1, source.value.split(/\r\n|\r|\n/).length);
    const fraction = Math.max(0, ((reveal.line || 1) - 1) / lines);
    if (mode !== "preview" && range) {
      source.focus({ preventScroll: true });
      source.setSelectionRange(range.start, range.end);
      const lineHeight = parseFloat(getComputedStyle(source).lineHeight) || 20;
      source.scrollTop = Math.max(0, ((reveal.line || 1) - 1) * lineHeight - source.clientHeight / 3);
    }
    if (mode !== "source") previewPane.scrollTop = Math.max(0, fraction * previewPane.scrollHeight - previewPane.clientHeight / 3);
  }
}

// Replace preview content block by block, keeping unchanged top-level blocks
// (rendered math, Mermaid SVG, running visualisation frames) in place so a
// keystroke does not make the whole preview flicker or reload iframes.
function blockKey(el) {
  if (el.dataset.mermaid !== undefined) return `m:${hashText(el.dataset.mermaid)}`;
  if (el.classList.contains("viz-block")) return `v:${hashText(el.querySelector(".viz-source")?.textContent || "")}`;
  return `e:${hashText(el.outerHTML)}`;
}

function patchPreview(pane, fresh) {
  const old = pane.firstElementChild;
  if (!old) {
    for (const child of fresh.children) child.dataset.rk = blockKey(child);
    pane.appendChild(fresh);
    return;
  }
  const newKids = [...fresh.children];
  for (const child of newKids) child.dataset.rk = blockKey(child);
  const oldKids = [...old.children];
  const count = Math.max(oldKids.length, newKids.length);
  for (let i = 0; i < count; i += 1) {
    const before = oldKids[i];
    const after = newKids[i];
    if (before && after && before.dataset.rk === after.dataset.rk) continue;
    if (before && after) {
      // A diagram whose source changed keeps showing the previous SVG until
      // the new one is ready.
      if (after.dataset.mermaid !== undefined && !after.classList.contains("rendered")
          && before.dataset.mermaid !== undefined && before.classList.contains("rendered")) {
        after.innerHTML = before.innerHTML;
        after.classList.add("stale");
      }
      old.replaceChild(after, before);
    } else if (after) old.appendChild(after);
    else before.remove();
  }
  // richText typeset the detached root; finish math/diagrams in place.
  typesetMath(old);
  typesetMermaid(old);
}

async function pasteImage(state, source, event) {
  const file = [...(event.clipboardData?.files || [])].find((item) => item.type.startsWith("image/"));
  if (!file) return;
  event.preventDefault();
  try {
    const uploaded = await uploadUnitFile(state.unit.id, file, file.name || "Pasted image"); // i18n-ignore: stored file name
    if (!uploaded) return;
    source.setRangeText(`![${file.name || "pasted image"}](media:${uploaded.id})`, source.selectionStart, source.selectionEnd, "end");
    state.draft.body = source.value;
    await refreshMedia(state);
    edited(state);
  } catch (err) { toast(err.message, "err"); }
}

async function refreshMedia(state) {
  try {
    const data = await api.unit(state.uid);
    state.unit = { ...state.unit, media: data.unit.media || [], links_out: data.unit.links_out, links_in: data.unit.links_in, activity: data.unit.activity };
    if (state.unit.id === host?.ctx?.S?.detail?.id) host.ctx.S.detail = { ...host.ctx.S.detail, media: data.unit.media };
  } catch (err) { toast(err.message, "err"); }
  repaintIf(state);
}

// ------------------------------------------------------------- media gallery
function mediaGallery(state, gallery) {
  const unit = state.unit;
  const media = unit.media || [];
  for (const item of media) gallery.appendChild(mediaCard(state, item));
  if (!media.length) gallery.appendChild(h("p", { class: "muted nd-empty" }, t("node.no_media")));

  const urlInput = h("input", { class: "input", placeholder: t("inspector.url"), "aria-label": t("inspector.url") });
  const captionInput = h("input", { class: "input", placeholder: t("inspector.caption"), "aria-label": t("inspector.caption") });
  const fileInput = h("input", { type: "file", class: "media-file-native", multiple: true });
  const upload = async (files) => {
    for (const file of files) {
      try {
        const done = await uploadUnitFile(unit.id, file, captionInput.value.trim() || file.name);
        if (done) toast(`${file.name} · ${(file.size / 1024).toFixed(0)} KB`, "ok");
      } catch (err) { toast(err.message, "err"); }
    }
    captionInput.value = "";
    await refreshMedia(state);
  };
  fileInput.addEventListener("change", () => { const files = [...(fileInput.files || [])]; fileInput.value = ""; if (files.length) upload(files); });
  const addUrl = async () => {
    const url = urlInput.value.trim();
    if (!url) { toast(t("inspector.media_source_required"), "err"); return; }
    try {
      await addUnitMediaUrl(unit.id, url, { caption: captionInput.value.trim() });
      urlInput.value = ""; captionInput.value = "";
      toast(t("toast.saved"), "ok");
      await refreshMedia(state);
    } catch (err) { toast(err.message, "err"); }
  };
  urlInput.addEventListener("keydown", (event) => { if (event.key === "Enter") { event.preventDefault(); addUrl(); } });
  const composer = h("div", { class: "nd-media-add" },
    urlInput, captionInput,
    h("button", { class: "btn sm", type: "button", onclick: addUrl }, icon("plus", "sm"), t("inspector.add_media")),
    h("button", { class: "btn sm ghost", type: "button", onclick: () => fileInput.click() }, icon("upload", "sm"), t("node.upload")),
    fileInput);
  const section = h("section", { class: "nd-section nd-media" },
    h("h3", null, t("node.media"), h("span", { class: "count" }, String(media.length))),
    gallery, composer,
    h("p", { class: "muted nd-hint" }, t("node.drop_hint")));
  section.addEventListener("dragover", (event) => {
    if (![...(event.dataTransfer?.types || [])].includes("Files")) return;
    event.preventDefault();
    section.classList.add("drop");
  });
  section.addEventListener("dragleave", () => section.classList.remove("drop"));
  section.addEventListener("drop", (event) => {
    const files = [...(event.dataTransfer?.files || [])];
    if (!files.length) return;
    event.preventDefault();
    section.classList.remove("drop");
    upload(files);
  });
  return section;
}

function mediaCard(state, item) {
  const kind = galleryKind(item);
  const src = mediaUrl(item);
  const label = item.caption || item.kind || t("inspector.media");
  let stage;
  if (kind === "image") {
    stage = h("button", { class: "nd-thumb", type: "button", title: t("viewer.open"), onclick: () => openLightbox(src, label) },
      h("img", { src, alt: label, loading: "lazy" }));
  } else if (kind === "video") {
    stage = h("video", { class: "nd-video", src, controls: true, preload: "metadata", playsinline: true, dataset: { mediaId: item.id } });
  } else if (kind === "audio") {
    stage = h("div", { class: "nd-audio" }, icon("play", "lg"), h("audio", { src, controls: true, preload: "metadata" }));
  } else {
    const glyph = { pdf: "book", html: "graph", link: "link" }[kind] || "file"; // i18n-ignore: icon names
    stage = h("button", {
      class: `nd-file-card kind-${kind}`, type: "button",
      onclick: () => (kind === "link" ? openMediaExternal(item) : host.open(mediaSource(item, state.unit), { preview: true })),
    }, icon(glyph, "lg"), h("span", { class: "nd-file-kind" }, kind === "html" ? `HTML · ${t("viz.sandboxed")}` : kind.toUpperCase()));
  }
  const caption = h("div", { class: "nd-caption", title: item.url }, label);
  const editCaption = () => {
    const input = h("input", { class: "input nd-caption-input", value: item.caption || "", "aria-label": t("node.caption") });
    const finish = async (commitIt) => {
      if (!input.isConnected) return;
      const value = input.value.trim();
      input.replaceWith(caption);
      if (!commitIt || value === (item.caption || "")) return;
      try {
        await api.updateMedia(item.id, { caption: value });
        item.caption = value;
        caption.textContent = value || item.kind || t("inspector.media");
        toast(t("node.caption_saved"), "ok");
      } catch (err) { toast(err.message, "err"); }
    };
    input.addEventListener("keydown", (event) => {
      if (event.key === "Enter") { event.preventDefault(); finish(true); }
      if (event.key === "Escape") { event.preventDefault(); finish(false); }
    });
    input.addEventListener("blur", () => finish(true));
    caption.replaceWith(input);
    input.focus();
    input.select();
  };
  const actions = h("div", { class: "nd-card-actions" },
    kind !== "link" ? h("button", {
      class: "icon-btn nd-open-editor", type: "button", title: t("editor.open_in_editor"), "aria-label": t("editor.open_in_editor"),
      onclick: () => host.open(mediaSource(item, state.unit), { preview: false }),
    }, icon("edit", "sm")) : null,
    h("button", {
      class: "icon-btn", type: "button", title: t("node.edit_caption"), "aria-label": t("node.edit_caption"), onclick: editCaption,
    }, icon("message", "sm")),
    h("button", {
      class: "icon-btn", type: "button", title: t("node.insert_into_body"), "aria-label": t("node.insert_into_body"),
      onclick: () => {
        const area = liveViews(state).find((item) => item.source.isConnected)?.source;
        if (!area) return;
        const text = `${mediaMarkdown(item)}\n`;
        area.focus();
        area.setRangeText(text, area.selectionStart, area.selectionEnd, "end");
        area.dispatchEvent(new Event("input"));
      },
    }, icon("plus", "sm")),
    h("button", {
      class: "icon-btn", type: "button", title: t("action.open_external"), "aria-label": t("action.open_external"),
      onclick: () => openMediaExternal(item),
    }, icon("external", "sm")),
    h("button", {
      class: "icon-btn danger", type: "button", title: t("action.remove"), "aria-label": t("action.remove"),
      onclick: async () => {
        if (!(await confirmDialog(t("node.remove_confirm", { label }), { danger: true }))) return;
        try {
          await api.deleteMedia(item.id);
          toast(t("toast.saved"), "ok");
          await refreshMedia(state);
        } catch (err) { toast(err.message, "err"); }
      },
    }, icon("trash", "sm")));
  return h("figure", { class: `nd-card kind-${kind}`, dataset: { mediaId: item.id, kind } },
    stage, h("figcaption", null, caption, actions));
}

function openLightbox(src, alt = "") {
  openImageLightbox({ src, alt, title: alt, className: "nd-lightbox" });
}

// --------------------------------------------------------------------- links
function openLinked(uid, title, event) {
  const pinned = Boolean(event && (event.ctrlKey || event.metaKey || event.button === 1));
  host.open({ kind: "unit", uid, title }, { preview: !pinned });
}

function linksPanel(state) {
  const unit = state.unit;
  const rows = linkRows(unit);
  const list = (direction) => {
    const items = rows.filter((row) => row.direction === direction);
    if (!items.length) return h("p", { class: "muted nd-empty" }, t("node.no_links"));
    return h("ul", { class: "nd-link-list" }, items.map((row) => h("li", { class: "nd-link", dataset: { unitRef: row.uid } },
      h("span", { class: "nd-rel" }, direction === "in" ? `← ${relLabel(row.relation)}` : `${relLabel(row.relation)} →`),
      h("a", {
        href: `#/editor/${encodeURIComponent(`unit:${row.uid}`)}`, class: "nd-link-target", title: t("node.open_new_tab_hint"),
        onclick: (event) => { event.preventDefault(); openLinked(row.uid, row.title, event); },
        onauxclick: (event) => { if (event.button === 1) { event.preventDefault(); openLinked(row.uid, row.title, event); } },
      },
        row.type ? h("span", { class: "type-dot", style: { background: typeColor(row.type) } }) : null,
        h("span", { class: "uid" }, row.uid), h("span", { class: "nd-link-title" }, row.title)),
      h("button", {
        class: "icon-btn danger", type: "button", title: t("action.remove"), "aria-label": t("action.remove"),
        onclick: async () => {
          if (!(await confirmDialog(t("node.remove_link_confirm"), { danger: true }))) return;
          try {
            await api.deleteLink(row.id);
            toast(t("toast.unlinked"), "ok");
            await refreshMedia(state);
          } catch (err) { toast(err.message, "err"); }
        },
      }, icon("x", "sm")))));
  };
  return h("section", { class: "nd-section nd-links" },
    h("h3", null, t("node.links"), h("span", { class: "count" }, String(rows.length)),
      h("button", {
        class: "btn sm ghost", type: "button",
        onclick: async () => { await openLinkModal(host.ctx, unit); },
      }, icon("link", "sm"), t("inspector.add_connection"))),
    h("div", { class: "nd-link-cols" },
      h("div", null, h("h4", null, t("node.outgoing")), list("out")),
      h("div", null, h("h4", null, t("node.incoming")), list("in"))));
}
