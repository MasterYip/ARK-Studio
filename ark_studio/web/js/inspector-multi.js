// The inspector for several selected nodes: the attributes they share (a
// field shows the common value, or "Mixed"), tags and topics on all / some
// of them, and the selected nodes as quick links.  An edit applies to every
// selected node in one POST /api/units/batch (one transaction, one activity
// entry per node with one batch id) and is one undo step: the panel's Undo
// button and Ctrl+Z on the graph the selection came from.  Title and body are
// per node and are not batch-edited.

import { h, clear, typeColor, statusColor } from "./dom.js";
import { t, typeLabel, statusLabel } from "./i18n.js";
import { icon } from "./icons.js";
import { api } from "./api.js";
import { toast } from "./components.js";
import { syncUnitLocally, announceUnitSaved } from "./unit-edit.js";
import { loadAssignees } from "./assignee.js";
import { displayName } from "./users.js";
import { canAssign } from "./assign-menu.js";
import { openAddToTopic } from "./topic-actions.js";
import { shellActive, revealView, render as renderWorkbench } from "./workbench.js";
import {
  claimInspector, inspectorClaimed, closeInspector, dockRoot, drawerRoots, inspectorResizer, placeFloating,
} from "./inspector.js";
import {
  sharedAttributes, tagPresence, topicPresence, batchFieldPayload, batchTagsPayload, undoPayload, changedBefore,
} from "./selection-model.js";

const isPhone = () => typeof window.matchMedia === "function" && window.matchMedia("(max-width: 760px)").matches;

/** Whether a graph selection should show here (the inspector is open or docked). */
export function multiInspectorWanted(S) {
  return Boolean(S.multi || S.drawerOpen || (S.inspectorPane !== false && !isPhone()));
}

/**
 * Show the multi-selection inspector for `refs` (unit ids or uids).
 * options: { graph, anchor, forceFloating }
 */
export async function openMultiInspector(ctx, refs, options = {}) {
  const list = [...new Set((refs || []).filter((ref) => ref !== null && ref !== undefined && ref !== ""))];
  if (list.length < 2) return;
  const request = claimInspector();
  let data;
  try {
    data = await api.unitsBatch(list);
  } catch (err) {
    if (inspectorClaimed(request)) toast(err.message, "err");
    return;
  }
  if (!inspectorClaimed(request)) return;
  const units = data.units || [];
  if (units.length < 2) {
    if (units.length === 1) await ctx.openUnit?.(units[0].uid);
    return;
  }
  const graph = options.graph || ctx.S.multi?.graph || null;
  ctx.S.multi = { refs: units.map((unit) => unit.id), graph };
  ctx.S.detail = null;
  ctx.S.selectedId = null;
  ctx.S.drawerOpen = true;
  if (options.anchor) ctx.S.drawerAnchor = { ...options.anchor };
  const docked = !options.forceFloating && ctx.S.inspectorPane !== false && !isPhone();
  ctx.S.drawerFloating = Boolean(options.forceFloating);
  document.body.classList.toggle("inspector-pinned", docked);
  renderMultiInspector(ctx, units, { docked, anchor: ctx.S.drawerAnchor, graph, mobileSheet: isPhone(), request });
  if (docked && shellActive()) revealView("details", { unless: ["localgraph", "sessions"] });
  renderWorkbench();
  window.dispatchEvent(new CustomEvent("ark:selection"));
}

function readOnlyFor(ctx, graph) {
  if (!canAssign(ctx.S)) return true;
  if (graph?.readOnly) return true;
  let saved = "read";
  try { saved = localStorage.getItem("ark.graphMode") === "edit" ? "edit" : "read"; } catch { /* storage disabled */ }
  const mode = ctx.S.graphMode || graph?.mode || saved;
  return mode !== "edit";
}

function renderMultiInspector(ctx, initial, { docked, anchor, graph, mobileSheet, request }) {
  const root = docked && !mobileSheet ? dockRoot() : document.getElementById("overlay-root");
  for (const item of drawerRoots()) item.querySelectorAll(".drawer, .drawer-backdrop").forEach((el) => el.remove());
  let units = initial.slice();
  let lastUndo = null;   // { before, opts, used }
  let busy = false;
  const readOnly = readOnlyFor(ctx, graph);
  const status = h("span", { class: "autosave-state multi-status", "data-multi-status": "" });
  const undoBtn = h("button", {
    class: "btn sm ghost hidden", type: "button", "data-multi-undo": "",
    onclick: () => undoLast(),
  }, icon("refresh", "sm"), t("multi.undo"));
  const body = h("div", { class: "drawer-body multi-body" });
  const countPill = h("span", { class: "pill", "data-multi-count": "" });

  const setStatus = (text, pending = false) => {
    status.textContent = text;
    status.classList.toggle("pending", pending);
  };
  const reportReply = (reply) => {
    const updated = reply.updated?.length || 0;
    const skipped = reply.skipped || [];
    const parts = [t("multi.updated", { n: updated })];
    if (skipped.length) parts.push(t("multi.skipped", { n: skipped.length }));
    setStatus(parts.join(" · "));
    status.title = skipped.map((item) => `${item.uid || item.unit}: ${item.error}`).join("\n");
    if (skipped.length) {
      toast(t("multi.skipped_detail", {
        n: skipped.length,
        list: skipped.slice(0, 3).map((item) => `${item.uid || item.unit} (${item.error})`).join(", "),
      }), "err");
    }
  };
  const merge = (fresh = []) => {
    const byId = new Map(fresh.map((unit) => [unit.id, unit]));
    units = units.map((unit) => (byId.has(unit.id) ? { ...unit, ...byId.get(unit.id) } : unit));
    for (const unit of fresh) {
      syncUnitLocally(ctx, unit);
      announceUnitSaved(unit, "batch");
    }
  };
  const send = async (payload, undoOpts) => {
    if (busy) return;
    busy = true;
    setStatus(t("node.saving"), true);
    const before = units.map((unit) => ({ ...unit, tags: [...(unit.tags || [])] }));
    try {
      const reply = await api.batchUpdate(payload);
      merge(reply.updated);
      reportReply(reply);
      const changed = changedBefore(before, reply);
      if (changed.length) {
        const entry = { before: changed, opts: undoOpts, used: false };
        lastUndo = entry;
        undoBtn.classList.remove("hidden");
        // One step in the graph's Ctrl+Z history too.
        graph?._pushHistory?.({ undo: () => runUndo(entry) });
      }
    } catch (err) {
      setStatus(t("inspector.save_failed"));
      toast(err.message, "err");
    } finally {
      busy = false;
      paint();
    }
  };
  const runUndo = async (entry) => {
    if (!entry || entry.used) return;
    entry.used = true;
    if (lastUndo === entry) {
      lastUndo = null;
      undoBtn.classList.add("hidden");
    }
    const reply = await api.batchUpdate(undoPayload(entry.before, entry.opts));
    merge(reply.updated);
    setStatus(t("multi.undone", { n: reply.updated?.length || 0 }));
    if (inspectorClaimed(request)) paint();
  };
  const undoLast = async () => {
    try { await runUndo(lastUndo); } catch (err) { toast(err.message, "err"); }
  };
  const setField = (field, value) => send(batchFieldPayload(units, field, value), { field });
  const setTags = (change) => send(batchTagsPayload(units, change), { tags: true });

  const mixedOption = (mixed) => (mixed ? h("option", { value: "", disabled: true, selected: true }, t("multi.mixed")) : null);
  const selectField = (field, options, shared) => {
    const select = h("select", { class: "input", "data-multi-field": field, disabled: readOnly ? true : undefined },
      mixedOption(shared.mixed),
      options.map(([value, label]) => h("option", { value, selected: !shared.mixed && value === shared.value ? true : undefined }, label)));
    if (!shared.mixed) select.value = shared.value;
    select.addEventListener("change", () => setField(field, select.value));
    return select;
  };
  const dateField = (field, shared) => {
    const input = h("input", {
      class: "input", type: "date", "data-multi-field": field, value: shared.mixed ? "" : shared.value,
      disabled: readOnly ? true : undefined,
      title: shared.mixed ? t("multi.mixed") : "",
    });
    input.addEventListener("change", () => setField(field, input.value));
    return input;
  };
  const field = (label, control, mixed = false) => h("div", { class: "field" },
    h("label", null, label, mixed ? h("span", { class: "multi-mixed-note" }, t("multi.mixed")) : null), control);

  let assignees = null;
  const assigneeField = (shared) => {
    const select = h("select", { class: "input", "data-multi-field": "assignee", disabled: readOnly ? true : undefined });
    const fill = () => {
      clear(select);
      const names = new Set((assignees?.users || []).map((user) => user.username));
      for (const unit of units) if (unit.assignee) names.add(unit.assignee);
      select.append(...[
        mixedOption(shared.mixed),
        h("option", { value: "" }, t("assignee.none")),
        ...[...names].sort().map((name) => h("option", { value: name }, displayName(name))),
      ].filter(Boolean));
      if (!shared.mixed) select.value = shared.value;
    };
    fill();
    if (!assignees) {
      loadAssignees(ctx.S.project?.id || "").then((data) => { assignees = data; fill(); }).catch(() => {});
    }
    select.addEventListener("change", () => setField("assignee", select.value));
    return select;
  };

  const tagsBlock = () => {
    const { all, some } = tagPresence(units);
    const wrap = h("div", { class: "tag-editor multi-tags", "data-multi-tags": "" });
    for (const tag of all) {
      wrap.appendChild(h("span", { class: "tag", "data-tag": tag, title: t("multi.tag_on_all") }, tag,
        readOnly ? null : h("button", {
          type: "button", title: t("multi.remove_from_all"), "aria-label": t("multi.remove_from_all"),
          onclick: () => setTags({ remove: [tag] }),
        }, "×")));
    }
    for (const { tag, count } of some) {
      wrap.appendChild(h("span", { class: "tag partial", "data-tag": tag, title: t("multi.tag_on_some", { n: count, total: units.length }) },
        tag, h("small", null, ` ${count}/${units.length}`),
        readOnly ? null : h("button", {
          type: "button", title: t("multi.add_to_all"), "aria-label": t("multi.add_to_all"),
          onclick: () => setTags({ add: [tag] }),
        }, "+"),
        readOnly ? null : h("button", {
          type: "button", title: t("multi.remove_from_all"), "aria-label": t("multi.remove_from_all"),
          onclick: () => setTags({ remove: [tag] }),
        }, "×")));
    }
    if (!readOnly) {
      wrap.appendChild(h("input", {
        placeholder: t("multi.add_tag"), "aria-label": t("multi.add_tag"), "data-multi-tag-input": "",
        onkeydown: (event) => {
          if (event.key !== "Enter" && event.key !== ",") return;
          event.preventDefault();
          const value = event.target.value.trim().replace(/,+$/, "");
          event.target.value = "";
          if (value) setTags({ add: [value] });
        },
      }));
    } else if (!all.length && !some.length) {
      wrap.appendChild(h("span", { class: "muted" }, t("multi.no_tags")));
    }
    return wrap;
  };

  const topicsBlock = () => {
    const { all, some } = topicPresence(units);
    const open = (topic) => () => ctx.openTopic?.(topic.uid);
    return h("div", { class: "inspector-topics", "data-multi-topics": "" },
      ...all.map((topic) => h("button", { class: "chip", type: "button", title: `${topic.uid} · ${t("multi.topic_on_all")}`, onclick: open(topic) },
        icon("layers", "sm"), topic.title)),
      ...some.map(({ topic, count }) => h("button", {
        class: "chip partial", type: "button", title: `${topic.uid} · ${t("multi.topic_on_some", { n: count, total: units.length })}`, onclick: open(topic),
      }, icon("layers", "sm"), topic.title, h("small", null, ` ${count}/${units.length}`))),
      readOnly ? null : h("button", {
        class: "btn sm ghost", type: "button",
        // The topic dialog refreshes this panel, not the whole page (that
        // would reset the graph selection).
        onclick: () => openAddToTopic({ ...ctx, refresh: () => reload() }, units),
      }, icon("plus", "sm"), t("multi.add_to_topic")));
  };

  const nodeList = () => h("div", { class: "multi-nodes", "data-multi-nodes": "" },
    units.map((unit) => h("button", {
      class: "multi-node", type: "button", title: `${unit.uid} · ${unit.title}`, "data-uid": unit.uid,
      onclick: () => {
        graph?.setSelected?.(unit.id);
        ctx.openUnit?.(unit.uid);
      },
    },
    h("i", { class: "multi-node-dot", style: { background: typeColor(unit.type) } }),
    h("span", { class: "uid" }, unit.uid),
    h("span", { class: "multi-node-title" }, unit.title),
    h("span", { class: "status-pill", style: { "--status-color": statusColor(unit.status) } }, h("i"), statusLabel(unit.status)))));

  const vocab = (key) => (ctx.S.vocab?.[key] || []);
  function paint() {
    countPill.textContent = t("multi.count", { n: units.length });
    const shared = sharedAttributes(units);
    clear(body);
    body.append(...[
      readOnly ? h("div", { class: "notice multi-read-note" }, icon("info", "sm"),
        h("div", null, canAssign(ctx.S) ? t("multi.read_hint") : t("multi.no_permission"))) : null,
      h("div", { class: "field-row three" },
        field(t("common.type"), selectField("type", vocab("types").map((item) => [item.id, typeLabel(item.id)]), shared.type)),
        field(t("common.status"), selectField("status", vocab("statuses").map((item) => [item.id, statusLabel(item.id)]), shared.status)),
        field(t("common.importance"), selectField("importance", vocab("importance").map((item) => [item.id, t(`imp.long.${item.id}`)]), shared.importance))),
      field(t("assignee.label"), assigneeField(shared.assignee)),
      h("div", { class: "field-row" },
        field(t("common.start"), dateField("start_at", shared.start_at), shared.start_at.mixed),
        field(t("common.end"), dateField("end_at", shared.end_at), shared.end_at.mixed)),
      field(t("inspector.tags"), tagsBlock()),
      h("div", { class: "link-section" }, h("h4", null, t("nav.topic")), topicsBlock()),
      h("div", { class: "link-section" },
        h("h4", null, t("multi.selected_nodes"), h("span", { class: "multi-count-inline" }, String(units.length))),
        h("div", { class: "muted multi-text-note" }, t("multi.text_note")),
        nodeList())].filter(Boolean));
  }
  async function reload() {
    try {
      const data = await api.unitsBatch(units.map((unit) => unit.id));
      if (!inspectorClaimed(request)) return;
      if ((data.units || []).length) units = data.units;
      paint();
    } catch (err) { toast(err.message, "err"); }
  }

  const drawer = h("aside", {
    class: `drawer ${docked ? "docked" : "floating"} ${mobileSheet ? "mobile-sheet" : ""} multi-inspector`.trim(),
    role: "complementary", "aria-label": t("multi.title", { n: initial.length }), "data-multi-inspector": "",
  });
  drawer.appendChild(inspectorResizer(ctx));
  const head = h("div", { class: "drawer-head" },
    h("div", { class: "drawer-identity" },
      h("span", { class: "type-badge multi-badge" }, icon("layers", "sm"), t("multi.badge")), countPill),
    h("div", { class: "spacer" }),
    canAssign(ctx.S) && !graph?.readOnly && typeof ctx.syncInspectorMode === "function" ? h("button", {
      class: "btn sm ghost inspector-mode-btn", type: "button", "data-multi-mode": readOnly ? "edit" : "read",
      title: t(readOnly ? "inspector.edit_attributes" : "inspector.read_attributes"),
      onclick: () => ctx.syncInspectorMode(readOnly ? "edit" : "read"),
    }, icon(readOnly ? "pen" : "book", "sm"), readOnly ? t("action.edit") : t("inspector.read")) : null,
    h("button", { class: "icon-btn drawer-close", type: "button", title: t("action.close"), "aria-label": t("action.close"),
      onclick: () => closeInspector(ctx) }, icon("x")));
  const foot = h("div", { class: "drawer-foot" }, undoBtn, h("div", { class: "spacer" }), status);
  drawer.append(head, body, foot);
  paint();
  root.append(drawer);
  if (!docked && !mobileSheet) placeFloating(drawer, anchor);
  return drawer;
}
