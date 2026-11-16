import { h, clear, typeColor, statusColor, impColor, fmtDate, fmtDateTime, relativeTime, dueState, typeBadge, statusPill, impDot, tagChip, avatarFor, copyText, linksLabel } from "./dom.js";
import { t, typeLabel, statusLabel, impLabel, relLabel, nsLabel } from "./i18n.js";
import { icon } from "./icons.js";
import { api } from "./api.js";
import { sortNeedsValues } from "./needs-model.js";
import { S, baseTypes, selectedTypes, selectedStatuses, selectedImportance, activeFilterCount, filterDiffFromDefault, hasFilterDefault } from "./state.js";
import { openContextMenu } from "./context-menu.js";
import { assigneeChip, assigneeFilterSection } from "./assignee.js";
import { displayName } from "./users.js";

const KIND_COLORS = {topic: "#3b82f6", todo: "#ef4444", event: "#a855f7", decision: "#f59e0b",
  question: "#eab308", announcement: "#14b8a6", resource: "#10b981", digest: "#64748b", note: "#94a3b8"};

const OVERLAY = () => document.getElementById("overlay-root");
const TOASTS = () => document.getElementById("toast-root");

export function toast(message, kind = "") {
  const root = TOASTS();
  if (!root) return;
  const el = h("div", { class: `toast ${kind}`.trim() }, message);
  root.appendChild(el);
  setTimeout(() => { el.style.opacity = "0"; el.style.transform = "translateY(6px)"; }, 2600);
  setTimeout(() => el.remove(), 3000);
}

export function openModal({ title, body, actions = [], wide = false, onClose = null, dismissable = true, enterConfirm = false }) {
  // A modal can sit above an open inspector; only replace other modals.
  const root = OVERLAY();
  // A replaced modal also drops its keyboard handler (otherwise every
  // replaced dialog kept listening for Escape / Enter on the document).
  root?.querySelectorAll(":scope > .modal-backdrop").forEach((el) => { el._disposeModal?.(); el.remove(); });
  const modal = h("div", { class: `modal ${wide ? "wide" : ""}`.trim(), onclick: (e) => e.stopPropagation() });
  const head = h("div", { class: "modal-head" }, h("h3", null, title),
    h("button", { class: "icon-btn", title: t("action.close"), onclick: () => close(true) }, icon("x")));
  const bodyEl = h("div", { class: "modal-body" }, body);
  const foot = h("div", { class: "modal-foot" });
  const backdrop = h("div", { class: "modal-backdrop", onclick: () => { if (dismissable) close(false); } }, modal);
  modal.append(head, bodyEl);
  if (actions.length) {
    for (const action of actions) {
      if (action.spacer) { foot.appendChild(h("div", { class: "spacer" })); continue; }
      if (action.hint) { foot.appendChild(h("span", { class: "hint" }, action.hint)); continue; }
      const btn = h("button", {
        class: `btn ${action.primary ? "primary" : ""} ${action.danger ? "danger" : ""} ${action.ghost ? "ghost" : ""}`.trim(),
        disabled: action.disabled,
        onclick: async (e) => {
          if (action.onClick) {
            const result = await action.onClick(e, close);
            if (result === false) return;
          }
          if (action.close !== false) close(true);
        },
      }, action.label);
      action._btn = btn;
      foot.appendChild(btn);
    }
    modal.appendChild(foot);
  }
  root.appendChild(backdrop);
  const key = (e) => {
    if (e.key === "Escape" && dismissable) {
      e.preventDefault();
      e.stopPropagation();
      close(false);
      return;
    }
    if (enterConfirm && e.key === "Enter" && !e.shiftKey) {
      const target = actions.find((a) => a.primary && a.onClick)
        || actions.find((a) => a.danger && a.onClick);
      if (target) {
        e.preventDefault();
        target._btn?.click();
      }
    }
    if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
      const primary = actions.find((a) => a.primary && a.onClick);
      primary?._btn?.click();
    }
  };
  document.addEventListener("keydown", key);
  // Replacement only detaches the listener; close() still runs onClose later
  // if an action closes this modal after opening the next one.
  backdrop._disposeModal = () => document.removeEventListener("keydown", key);
  let closed = false;
  function close(ok) {
    if (closed) return;
    closed = true;
    document.removeEventListener("keydown", key);
    backdrop.remove();
    onClose?.(ok);
  }
  setTimeout(() => {
    const focusable = modal.querySelector("input,textarea,select,button.primary");
    focusable?.focus();
  }, 20);
  return { close, body: bodyEl, modal, backdrop, actions };
}

export function closeOverlay() {
  const root = OVERLAY();
  if (!root) return;
  const active = root.querySelector(".drawer, .modal-backdrop, .palette-backdrop");
  if (active) clear(root);
}

export function confirmDialog(message, { title = t("dialog.confirm_title"), danger = false, confirmLabel = t("action.continue") } = {}) {
  return new Promise((resolve) => {
    openModal({
      title,
      body: h("div", { style: { fontSize: "13px", color: "var(--sub)", lineHeight: "1.6" } }, message),
      actions: [
        { label: t("action.cancel"), ghost: true, onClick: () => resolve(false) },
        { label: confirmLabel, primary: !danger, danger, onClick: () => resolve(true) },
      ],
      onClose: () => resolve(false),
      enterConfirm: true,
    });
  });
}

// ---------------------------------------------------------------- project UI
// One row per project; used by the project modal and the brand project menu.
// Rows are buttons so they work from the keyboard.
export function projectRows(ctx, projects, { beforeOpen = null } = {}) {
  return (projects || []).map((project) => {
    const current = project.id === ctx.S.project?.id;
    return h("button", {
      class: `project-row ${current ? "current" : ""}`.trim(), type: "button",
      "data-project-id": project.id, title: project.path || "",
      "aria-current": current ? "true" : undefined,
      onclick: async () => {
        try {
          const data = await api.openProject({ id: project.id });
          beforeOpen?.(project);
          ctx.applyBootstrap(data);
          ctx.toast(t("project.toast.opened", { name: project.name }), "ok");
        } catch (err) { ctx.toast(err.message, "err"); }
      },
    },
      h("span", { class: "main" },
        h("span", { class: "t" }, h("span", { class: "n" }, project.name),
          project.demo ? h("span", { class: "demo-badge" }, t("common.demo")) : null,
          current ? h("span", { class: "pill" }, t("project.current")) : null),
        h("span", { class: "d" }, project.description || project.path || "")),
      icon("chevronRight", "sm"));
  });
}

export async function openProjectModal(ctx) {
  let projects = ctx.S.projects || [];
  try { projects = (await api.projects()).projects || projects; } catch { /* keep cached list */ }
  const list = h("div", { class: "project-list" });
  if (!projects.length) list.appendChild(h("div", { class: "empty sm" }, t("project.empty")));
  list.append(...projectRows(ctx, projects, { beforeOpen: () => close() }));
  const name = h("input", { class: "input", placeholder: t("project.name_placeholder"), id: "new-project-name" });
  const desc = h("textarea", { class: "textarea", style: { minHeight: "58px" }, placeholder: t("project.desc_placeholder") });
  const template = h("select", { class: "select" },
    h("option", { value: "blank" }, t("common.blank")),
    h("option", { value: "paper" }, t("common.paper")),
    h("option", { value: "demo" }, `${t("common.demo")} · PredActor`)); // i18n-ignore: demo project name
  const openPath = h("input", { class: "input", placeholder: "…/MyResearch.ark" }); // i18n-ignore: example path
  const body = h("div", null,
    h("div", { class: "section-head", style: { marginTop: 0 } }, h("h2", null, t("project.recent"))),
    list,
    h("div", { class: "divider" }),
    h("div", { class: "field" }, h("label", null, t("project.new")),
      h("div", { class: "stack", style: { gap: "7px" } }, name, desc, template)),
    h("div", { class: "field" }, h("label", null, t("project.open_path")),
      h("div", { class: "row", style: { flexWrap: "nowrap" } }, openPath,
        h("button", {
          class: "btn", onclick: async () => {
            try {
              const data = await api.openProject({ path: openPath.value.trim() });
              ctx.applyBootstrap(data);
              close();
            } catch (err) { ctx.toast(err.message, "err"); }
          },
        }, t("action.open"))),
      h("div", { class: "muted", style: { fontSize: "10px", marginTop: "4px" } }, t("project.path_hint"))));
  const modal = openModal({
    title: t("project.title"),
    body,
    wide: true,
    actions: [
      { label: t("action.cancel"), ghost: true },
      {
        label: t("project.create"), primary: true,
        onClick: async () => {
          const title = name.value.trim();
          if (!title) { ctx.toast(t("error.required"), "err"); return false; }
          try {
            const data = await api.createProject({ name: title, description: desc.value.trim(), template: template.value });
            ctx.applyBootstrap(data);
            ctx.toast(t("project.toast.created", { name: title }), "ok");
            close();
          } catch (err) { ctx.toast(err.message, "err"); return false; }
        },
      },
    ],
  });
  function close() { modal.close(); }
}

export function openCreateUnit(ctx, defaults = {}) {
  const vocab = ctx.S.vocab;
  const type = defaults.type || defaultTypeForTab(ctx.S.tab, ctx.S.sub);
  const title = h("input", { class: "input", placeholder: t("common.title"), id: "create-title" });
  const typeSel = h("select", { class: "select" },
    (vocab?.types || []).map((item) => h("option", { value: item.id, selected: item.id === type ? true : undefined }, typeLabel(item.id))));
  typeSel.value = type;
  const statusSel = h("select", { class: "select" },
    (vocab?.statuses || []).map((item) => h("option", { value: item.id, selected: item.id === (defaults.status || "open") ? true : undefined }, statusLabel(item.id))));
  const impSel = h("select", { class: "select" },
    (vocab?.importance || []).map((item) => h("option", { value: item.id, selected: item.id === (defaults.importance || "p2") ? true : undefined }, impLabel(item.id))));
  const body = h("textarea", { class: "textarea", style: { minHeight: "110px" }, placeholder: `${t("inspector.body")}…` });
  const tags = h("input", { class: "input", placeholder: "topic:control, stage:capture" }); // i18n-ignore: example tag values
  const start = h("input", { class: "input", type: "date", value: defaults.start_at || "" });
  const end = h("input", { class: "input", type: "date", value: defaults.end_at || "" });
  const form = h("div", null,
    h("div", { class: "field" }, h("label", null, t("inspector.title")), title),
    h("div", { class: "field-row three" },
      h("div", { class: "field" }, h("label", null, t("common.type")), typeSel),
      h("div", { class: "field" }, h("label", null, t("common.status")), statusSel),
      h("div", { class: "field" }, h("label", null, t("common.importance")), impSel)),
    h("div", { class: "field" }, h("label", null, t("inspector.body")), body),
    h("div", { class: "field" }, h("label", null, t("common.tags")), tags),
    h("div", { class: "field-row" },
      h("div", { class: "field" }, h("label", null, t("common.start")), start),
      h("div", { class: "field" }, h("label", null, t("common.end")), end)));
  const modal = openModal({
    title: t("action.new"),
    body: form,
    wide: false,
    actions: [
      { label: t("action.cancel"), ghost: true },
      {
        label: t("action.create"), primary: true,
        onClick: async () => {
          const text = title.value.trim();
          if (!text) { ctx.toast(t("error.required"), "err"); return false; }
          const tagList = tags.value.split(",").map((x) => x.trim()).filter(Boolean);
          try {
            const data = await api.createUnit({
              title: text, type: typeSel.value, status: statusSel.value, importance: impSel.value,
              body: body.value, tags: tagList, start_at: start.value || null, end_at: end.value || null,
            });
            modal.close();
            ctx.toast(t("toast.created", { uid: data.unit.uid }), "ok");
            await ctx.refresh();
            ctx.openUnit(data.unit.uid);
          } catch (err) { ctx.toast(err.message, "err"); return false; }
        },
      },
    ],
  });
  setTimeout(() => title.focus(), 40);
}

function defaultTypeForTab(tab, sub) {
  if (tab === "ideas") return "idea";
  if (tab === "tasks") return "task";
  if (tab === "production") return "production";
  if (tab === "distribution") return "outreach";
  if (tab === "plan") return { story: "story", related: "related_work", method: "method", experiment: "experiment", topic: "topic" }[sub] || "story";
  return "note";
}

export async function openLinkModal(ctx, sourceUnit, targetUnit = null) {
  let target = targetUnit;
  const results = h("div", { class: "link-results hidden" });
  const search = h("input", { class: "input", placeholder: t("inspector.select_target"), value: target ? `${target.uid} · ${target.title}` : "" });
  const relationSel = h("select", { class: "select" },
    (ctx.S.vocab?.relations || []).map((rel) => h("option", { value: rel.id }, relLabel(rel.id))));
  const chosen = h("div", { class: "muted", style: { fontSize: "11px", minHeight: "16px" } }, target ? `${target.uid} · ${target.title}` : t("inspector.connect_hint"));
  const doSearch = async () => {
    const q = search.value.trim();
    if (target && q === `${target.uid} · ${target.title}`) return;
    target = null;
    chosen.textContent = t("action.loading");
    try {
      const data = await api.units({ q, limit: 8, facets: 0 });
      clear(results);
      for (const unit of data.items) {
        results.appendChild(h("button", {
          type: "button",
          onclick: () => {
            target = unit;
            search.value = `${unit.uid} · ${unit.title}`;
            chosen.textContent = `${unit.uid} · ${unit.title}`;
            results.classList.add("hidden");
          },
        }, h("span", { class: "u" }, unit.uid), unit.title));
      }
      results.classList.toggle("hidden", !data.items.length);
      chosen.textContent = data.items.length ? t("inspector.select_target") : t("common.no_results");
    } catch (err) { chosen.textContent = err.message; }
  };
  let timer = null;
  search.addEventListener("input", () => { clearTimeout(timer); timer = setTimeout(doSearch, 220); });
  const body = h("div", null,
    h("div", { class: "field" }, h("label", null, t("inspector.select_target")),
      h("div", { class: "link-search" }, search, results)),
    h("div", { class: "field-row" },
      h("div", { class: "field" }, h("label", null, t("inspector.relation")), relationSel),
      h("div", { class: "field" }, h("label", null, t("common.source")), h("div", { class: "input", style: { opacity: ".8" } }, `${sourceUnit.uid} · ${sourceUnit.title}`))),
    h("div", { class: "muted", style: { fontSize: "10.5px" } }, t("inspector.connect_hint")));
  const modal = openModal({
    title: `${t("inspector.add_connection")} · ${sourceUnit.uid}`,
    body,
    actions: [
      { label: t("action.cancel"), ghost: true },
      {
        label: t("action.connect"), primary: true,
        onClick: async () => {
          if (!target) { ctx.toast(t("inspector.select_target"), "err"); return false; }
          try {
            await api.createLink({ source: sourceUnit.id, target: target.id, relation: relationSel.value });
            modal.close();
            ctx.toast(t("toast.linked"), "ok");
            await ctx.refresh();
            if (ctx.S.drawerOpen && ctx.S.detail?.id) await ctx.reloadDetail();
          } catch (err) { ctx.toast(err.message, "err"); return false; }
        },
      },
    ],
  });
  setTimeout(() => search.focus(), 40);
}

export async function openBulkEdit(ctx, nodes) {
  const picked = (nodes || []).filter((n) => n?.uid);
  if (!picked.length) return;
  const tagInput = h("input", { class: "input", placeholder: t("bulk.tags_placeholder") });
  const statusSel = h("select", { class: "select" },
    h("option", { value: "" }, t("bulk.keep")),
    (ctx.S.vocab?.statuses || []).map((s) => h("option", { value: s.id }, statusLabel(s.id))));
  const importanceSel = h("select", { class: "select" },
    h("option", { value: "" }, t("bulk.keep")),
    ["p0", "p1", "p2", "p3"].map((p) => h("option", { value: p }, impLabel(p))));
  const chips = h("div", { class: "tag-list" },
    ...picked.slice(0, 8).map((n) => h("span", { class: "pill" }, n.uid)),
    picked.length > 8 ? h("span", { class: "pill" }, `+${picked.length - 8}`) : null);
  const body = h("div", null,
    h("div", { class: "notice" }, icon("layers", "sm"),
      h("div", null, t("bulk.hint", { n: picked.length }))),
    h("div", { class: "chips", style: { marginBottom: "10px" } }, chips),
    h("div", { class: "field" },
      h("label", null, t("bulk.add_tags")), tagInput,
      h("div", { class: "muted", style: { fontSize: "10px", marginTop: "4px" } }, t("bulk.tags_hint"))),
    h("div", { class: "field-row" },
      h("div", { class: "field" }, h("label", null, t("common.status")), statusSel),
      h("div", { class: "field" }, h("label", null, t("common.importance")), importanceSel)));
  const modal = openModal({
    title: t("bulk.title", { n: picked.length }),
    body,
    actions: [
      { label: t("action.cancel"), ghost: true },
      {
        label: t("bulk.apply"), primary: true,
        onClick: async () => {
          const tags = [...new Set(tagInput.value.split(/[,，\n]/).map((x) => x.trim()).filter(Boolean))]
            .filter((tag) => !/^(type|status|importance):/.test(tag));
          const status = statusSel.value, importance = importanceSel.value;
          if (!tags.length && !status && !importance) {
            ctx.toast(t("bulk.nothing"), "err");
            return false;
          }
          let changed = 0;
          try {
            for (const node of picked) {
              const detail = (await api.unit(node.uid)).unit;
              const merged = [...new Set([...(detail.tags || []), ...tags])];
              const patch = { tags: merged };
              if (status) patch.status = status;
              if (importance) patch.importance = importance;
              await api.updateUnit(node.uid, patch);
              changed += 1;
            }
            modal.close();
            ctx.toast(t("bulk.updated", { n: changed }), "ok");
            await ctx.refresh();
          } catch (err) {
            ctx.toast(err.message, "err");
            return false;
          }
        },
      },
    ],
  });
  setTimeout(() => tagInput.focus(), 40);
}

export async function openRelationModal(ctx, edge, source, target) {
  const relationSel = h("select", { class: "select" },
    (ctx.S.vocab?.relations || []).map((rel) =>
      h("option", { value: rel.id, selected: rel.id === edge.relation ? true : undefined }, relLabel(rel.id))));
  relationSel.value = edge.relation || "unset";
  const body = h("div", null,
    h("div", { class: "notice" }, icon("link", "sm"), h("div", null, t("edge.change_hint"))),
    h("div", { class: "link-trace" },
      h("span", null, `${source?.uid || "?"} · ${source?.title || ""}`),
      icon("arrowRight", "sm"),
      h("span", null, `${target?.uid || "?"} · ${target?.title || ""}`)),
    h("div", { class: "field" }, h("label", null, t("edge.relation")), relationSel));
  const modal = openModal({
    title: t("edge.relation"),
    body,
    actions: [
      { label: t("action.cancel"), ghost: true },
      {
        label: t("action.save"), primary: true,
        onClick: async () => {
          try {
            await api.updateLink(edge.id, { relation: relationSel.value });
            modal.close();
            ctx.toast(t("edge.saved"), "ok");
            // Update the local edge only: a full route refresh would throw
            // away graph camera/selection just for a relation label change.
            ctx.updateEdgeRelation?.(edge, relationSel.value);
          } catch (err) { ctx.toast(err.message, "err"); return false; }
        },
      },
    ],
  });
}

export async function openDispatchModal(ctx, unit) {
  let data;
  try { data = await api.dispatch(unit.uid || unit.id); } catch (err) { ctx.toast(err.message, "err"); return; }
  const prompt = h("pre", { class: "prompt-box" }, data.prompt);
  const trace = h("div", { class: "stack", style: { gap: "5px", marginBottom: "10px" } },
    traceRow(t("plan.experiment.title"), data.experiment),
    traceRow(t("type.claim"), data.claim),
    traceRow(t("type.agent"), data.agent),
    traceRow(t("type.host"), data.host));
  const body = h("div", null,
    h("div", { class: "notice" }, icon("info"), h("div", null, t("tasks.dispatch_hint"))),
    trace, prompt);
  const modal = openModal({
    title: `${t("tasks.dispatch")} · ${unit.uid}`,
    body,
    wide: true,
    actions: [
      { label: t("action.close"), ghost: true },
      {
        label: t("tasks.copy_prompt"),
        onClick: async () => { await copyText(data.prompt); ctx.toast(t("toast.copied"), "ok"); },
      },
      {
        label: unit.status === "active" ? t("action.close") : t("tasks.mark_active"), primary: unit.status !== "active",
        onClick: async () => {
          if (unit.status === "active") return;
          try {
            await api.updateUnit(unit.uid, { status: "active" });
            ctx.toast(t("toast.status", { status: statusLabel("active") }), "ok");
            modal.close();
            await ctx.refresh();
          } catch (err) { ctx.toast(err.message, "err"); return false; }
        },
      },
    ],
  });
}

function traceRow(label, value) {
  return h("div", { class: "row", style: { fontSize: "11px", alignItems: "baseline" } },
    h("span", { style: { width: "104px", color: "var(--mute)", flex: "0 0 auto" } }, label),
    value ? h("span", null, `${value.uid} · ${value.title}`) : h("span", { style: { color: "var(--red)" } }, t("common.none")));
}

// ------------------------------------------------------------------ import UI
export async function openImportModal(ctx) {
  const body = h("div", { class: "loading" }, h("span", { class: "spinner" }), t("action.loading"));
  const modal = openModal({
    title: t("import.title"), body, wide: true,
    actions: [{ label: t("action.close"), ghost: true }],
  });
  let sources = [];
  try {
    sources = (await api.importSources()).sources || [];
  } catch (err) { ctx.toast(err.message, "err"); }
  let preview = null;
  const pathInput = h("input", {
    class: "input",
    value: ctx.S.infoHubPath || sources[0]?.path || "",
    placeholder: "…/info-hub/data/hub.db", // i18n-ignore: example path
  });
  const previewBox = h("div", null);
  const mode = h("select", { class: "select" },
    h("option", { value: "topics" }, t("import.topics")),
    h("option", { value: "all" }, t("import.all")));
  const update = h("input", { type: "checkbox" });
  const loadPreview = async () => {
    const path = pathInput.value.trim();
    if (!path) { ctx.toast(t("import.no_source"), "err"); return; }
    clear(previewBox);
    previewBox.appendChild(h("div", { class: "loading" }, h("span", { class: "spinner" }), t("action.loading")));
    try {
      preview = await api.previewImport(path);
      clear(previewBox);
      previewBox.append(
        h("div", { class: "row", style: { gap: "12px", marginBottom: "8px" } },
          h("span", { class: "pill" }, t("import.found", { n: preview.units })),
          h("span", { class: "pill ok" }, t("import.topics_found", { n: preview.topics })),
          h("span", { class: "pill" }, t("import.links_found", { n: preview.links })),
          h("span", { class: "muted", style: { fontSize: "10px" } }, preview.path)),
        preview.sample?.length ? h("div", { class: "stack", style: { gap: "3px" } },
          h("div", { class: "rail-label", style: { marginTop: "6px" } }, t("import.sample")),
          ...preview.sample.map((item) => h("div", { class: "row", style: { fontSize: "11px", alignItems: "baseline" } },
            h("span", { class: "pill" }, item.kind || "?"),
            h("span", { style: { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" } }, item.title)))) : null,
        h("div", { class: "muted", style: { fontSize: "10px", marginTop: "8px" } }, t("import.tags_preserved")));
    } catch (err) {
      clear(previewBox);
      previewBox.appendChild(h("div", { class: "notice" }, icon("alert"), h("div", null, err.message)));
    }
  };
  const form = h("div", null,
    h("div", { class: "notice" }, icon("info"), h("div", null, t("import.subtitle"))),
    h("div", { class: "field" }, h("label", null, t("import.source")),
      h("div", { class: "row", style: { flexWrap: "nowrap" } }, pathInput,
        h("button", { class: "btn", onclick: loadPreview }, t("import.preview")))),
    h("div", { class: "field-row" },
      h("div", { class: "field" }, h("label", null, t("import.mode")), mode),
      h("div", { class: "field" }, h("label", null, t("import.options")),
        h("label", { class: "row", style: { height: "32px", gap: "6px", fontSize: "11px", color: "var(--sub)", textTransform: "none", letterSpacing: "0", fontWeight: "400" } }, update, t("import.update")))),
    previewBox);
  clear(body);
  body.appendChild(form);
  if (sources.length) {
    loadPreview();
  } else {
    previewBox.appendChild(h("div", { class: "muted", style: { fontSize: "10.5px" } }, t("import.no_source")));
  }
  // Replace footer with the import action now that the body exists.
  modal.modal.querySelector(".modal-foot")?.append(
    h("button", {
      class: "btn primary",
      onclick: async () => {
        if (!preview) { ctx.toast(t("import.preview"), "err"); return; }
        try {
          const result = await api.importInfoHub({ path: pathInput.value.trim(), mode: mode.value, update: update.checked });
          ctx.toast(t("import.done", result), "ok");
          modal.close();
          await ctx.refresh();
        } catch (err) { ctx.toast(err.message, "err"); }
      },
    }, t("import.import")));
}

// ------------------------------------------------------------------ extract UI
export function openExtractModal(ctx) {
  const text = h("textarea", {
    class: "textarea", style: { minHeight: "180px" }, placeholder: t("ideas.extract_placeholder"),
  });
  const body = h("div", null,
    h("div", { class: "notice" }, icon("info", "sm"), h("div", null, t("ideas.extract_hint"))),
    h("div", { class: "field" }, h("label", null, t("ideas.extract_title")), text));
  const modal = openModal({
    title: t("ideas.extract"),
    body,
    wide: true,
    actions: [
      { label: t("action.cancel"), ghost: true },
      {
        label: t("ideas.extract_button"), primary: true,
        onClick: async () => {
          if (!text.value.trim()) { ctx.toast(t("error.required"), "err"); return false; }
          try {
            const data = await api.extract({ text: text.value });
            ctx.toast(t("ideas.extracted", { n: data.created_count }), "ok");
            modal.close();
            await ctx.refresh();
            if (data.created?.[0]) ctx.openUnit(data.created[0].uid);
          } catch (err) { ctx.toast(err.message, "err"); return false; }
        },
      },
    ],
  });
  setTimeout(() => text.focus(), 40);
}

// ------------------------------------------------------------------ assistant
export async function openAssistant(ctx) {
  closeOverlay();
  const root = OVERLAY();
  const body = h("div", { class: "drawer-body" });
  const head = h("div", { class: "drawer-head" },
    h("span", { style: { width: "26px", height: "26px", borderRadius: "7px", display: "grid", placeItems: "center", background: "var(--accent-soft)", color: "var(--accent-2)" } }, icon("sparkles", "sm")),
    h("div", null, h("div", { style: { fontSize: "13px", fontWeight: "700" } }, t("assistant.title")),
      h("div", { style: { fontSize: "10px", color: "var(--mute)" } }, t("assistant.local"))),
    h("div", { class: "spacer" }),
    h("button", { class: "icon-btn", title: t("action.close"), onclick: () => closeAssistant() }, icon("x")));
  const tabs = h("div", { class: "tabs-inline", style: { padding: "0 14px 9px" } });
  const drawer = h("div", { class: "drawer" }, head, tabs, body);
  const backdrop = h("div", { class: "drawer-backdrop", onclick: () => closeAssistant() });
  root.append(backdrop, drawer);
  function closeAssistant() { drawer.remove(); backdrop.remove(); }
  const show = async (which = "insights") => {
    clear(tabs);
    tabs.append(
      h("button", { class: which === "insights" ? "on" : "", onclick: () => show("insights") }, t("assistant.insights")),
      h("button", { class: which === "api" ? "on" : "", onclick: () => show("api") }, t("assistant.api")));
    clear(body);
    if (which === "insights") {
      body.appendChild(h("div", { class: "loading" }, h("span", { class: "spinner" }), t("action.loading")));
      try {
        const data = await api.insights();
        clear(body);
        body.append(
          h("div", { style: { fontSize: "11.5px", color: "var(--sub)", marginBottom: "10px", lineHeight: "1.55" } }, t("assistant.subtitle")),
          h("div", { class: "row", style: { marginBottom: "10px" } },
            h("button", { class: "btn sm ghost", onclick: () => show("insights") }, icon("refresh", "sm"), t("assistant.refresh"))));
        if (!data.insights.length) {
          body.appendChild(h("div", { class: "empty sm" }, t("assistant.no_insights")));
        } else {
          const list = h("div", { class: "stack", style: { gap: "7px" } });
          for (const insight of data.insights) {
            list.appendChild(h("div", { class: "panel", style: { padding: "9px 10px" } },
              h("div", { class: "row", style: { gap: "6px", marginBottom: "4px" } },
                h("span", { class: `pill ${insight.severity === "high" ? "bad" : insight.severity === "medium" ? "warn" : ""}`.trim() }, t(`assistant.severity.${insight.severity}`)),
                h("span", { class: "muted", style: { fontSize: "9.5px", textTransform: "uppercase", letterSpacing: ".05em" } },
                  t(`assistant.kind.${insight.kind}`) === `assistant.kind.${insight.kind}` ? insight.kind.replaceAll("-", " ") : t(`assistant.kind.${insight.kind}`))),
              h("div", { style: { fontSize: "12px", fontWeight: "600", lineHeight: "1.45" } }, insight.title),
              h("div", { style: { fontSize: "10.5px", color: "var(--mute)", marginTop: "3px", lineHeight: "1.5" } },
                t(`assistant.detail.${insight.kind}`) === `assistant.detail.${insight.kind}`
                  ? insight.detail
                  : t(`assistant.detail.${insight.kind}`)),
              h("div", { class: "row", style: { marginTop: "7px" } },
                insight.ops?.length ? h("button", {
                  class: "btn sm", onclick: async () => {
                    try { await api.applyInsight(insight); ctx.toast(t("toast.applied"), "ok"); await ctx.refresh(); show("insights"); }
                    catch (err) { ctx.toast(err.message, "err"); }
                  },
                }, t("assistant.apply")) : null,
                insight.unit_ids?.[0] ? h("button", {
                  class: "btn sm ghost", onclick: () => { closeAssistant(); ctx.openUnit(insight.unit_ids[0]); },
                }, t("assistant.open")) : null)));
          }
          body.appendChild(list);
        }
      } catch (err) {
        clear(body);
        body.appendChild(h("div", { class: "notice" }, icon("alert"), h("div", null, err.message)));
      }
    } else {
      body.appendChild(h("div", { class: "loading" }, h("span", { class: "spinner" }), t("action.loading")));
      try {
        const { token } = await api.agentToken();
        clear(body);
        const input = h("input", { class: "input", type: "password", value: token, readonly: true, style: { fontFamily: "var(--mono)", fontSize: "10px" } });
        let shown = false;
        const toggle = h("button", {
          class: "btn sm ghost", onclick: () => {
            shown = !shown;
            input.type = shown ? "text" : "password";
            toggle.textContent = shown ? t("assistant.hide") : t("assistant.reveal");
          },
        }, t("assistant.reveal"));
        const endpoints = [
          "GET  /api/agent/v1/context",
          "GET  /api/agent/v1/units?type=task&status=active",
          "POST /api/agent/v1/units",
          "PATCH /api/agent/v1/units/{id}",
          "POST /api/agent/v1/links",
        ].join("\n");
        body.append(
          h("div", { style: { fontSize: "11.5px", color: "var(--sub)", lineHeight: "1.55", marginBottom: "12px" } }, t("assistant.api_hint")),
          h("div", { class: "settings-note" }, icon("info", "sm"), h("div", null, t("assistant.api_personal_hint"))),
          h("div", { class: "field" }, h("label", null, t("assistant.token_legacy")),
            h("div", { class: "token-row" }, input, toggle)),
          h("div", { class: "field" }, h("label", null, t("assistant.endpoints")),
            h("pre", { class: "code-box" }, endpoints),
            h("div", { class: "row", style: { marginTop: "7px" } },
              h("button", {
                class: "btn sm", onclick: async () => {
                  await copyText(token); ctx.toast(t("toast.copied"), "ok");
                },
              }, t("assistant.copy")),
              h("button", {
                class: "btn sm ghost", onclick: async () => {
                  const curl = `curl -H "Authorization: Bearer ${token}" http://127.0.0.1:8890/api/agent/v1/context`;
                  await copyText(curl); ctx.toast(t("toast.copied"), "ok");
                },
              }, "cURL"))));
      } catch (err) {
        clear(body);
        body.appendChild(h("div", { class: "notice" }, icon("alert"), h("div", null, err.message)));
        body.appendChild(h("div", { class: "settings-note" }, icon("info", "sm"), h("div", null, t("assistant.api_personal_hint"))));
      }
    }
  };
  show("insights");
}

// -------------------------------------------------------------------- rail
export function renderFilterRail(host, ctx) {
  const { S: state } = ctx;
  const facets = state.facets || { types: {}, statuses: {}, importance: {}, namespaces: {} };
  const base = baseTypes(state.tab, state.sub);
  clear(host);
  if (state.tab === "ideas") {
    host.appendChild(railTabs(state, ctx));
    if (state.filterTab === "infohub") {
      renderInfoHubRail(host, ctx);
      return;
    }
  }
  const diff = filterDiffFromDefault(state.filters);
  const savedDefault = hasFilterDefault();
  const head = h("div", { class: "rail-head" },
    h("div", { class: "rail-title" }, t("action.filters"),
      diff.length ? h("span", {
        class: "rail-diff-dot", role: "status",
        title: t(savedDefault ? "filters.differs_saved" : "filters.differs_builtin", { n: diff.length }),
        "aria-label": t(savedDefault ? "filters.differs_saved" : "filters.differs_builtin", { n: diff.length }),
      }) : null),
    h("div", { class: "row tight" },
      activeFilterCount(state.filters) ? h("span", { class: "pill" }, t("filters.active", { n: activeFilterCount(state.filters) })) : null,
      activeFilterCount(state.filters) ? h("button", { class: "rail-clear-btn", type: "button", title: t("filters.clear"), onclick: () => ctx.clearFilters() }, t("filters.clear")) : null,
      h("button", { class: "icon-btn", title: t("action.close"), style: { display: window.innerWidth <= 980 ? "grid" : "none" }, onclick: () => { state.railOpen = false; host.classList.remove("open"); ctx.closeFilters?.(); } }, icon("x"))));
  host.appendChild(head);
  host.appendChild(h("div", { class: "rail-defaults" },
    h("button", {
      class: "rail-default-btn rail-reset", type: "button", disabled: !diff.length,
      title: t(savedDefault ? "filters.reset_saved_hint" : "filters.reset_builtin_hint"), onclick: () => ctx.resetFilters(),
    }, icon("refresh", "sm"), t("filters.reset")),
    h("button", {
      class: "rail-default-btn rail-set-default", type: "button", disabled: savedDefault && !diff.length,
      title: t("filters.set_default_hint"), onclick: () => ctx.saveFiltersAsDefault(),
    }, icon("pin", "sm"), t("filters.set_default")),
    savedDefault ? h("span", { class: "rail-default-tag", title: t("filters.saved_default_hint") }, t("filters.saved_default")) : null,
    h("button", {
      class: "icon-btn rail-default-more", type: "button", title: t("filters.more"), "aria-label": t("filters.more"), "aria-haspopup": "menu",
      onclick: (event) => {
        const rect = event.currentTarget.getBoundingClientRect();
        openContextMenu({
          x: rect.left, y: rect.bottom + 4, items: [
            { label: t("filters.restore_builtin"), icon: "refresh", disabled: !savedDefault, onClick: () => ctx.clearFiltersDefault() },
            { label: t("filters.clear"), icon: "x", disabled: !activeFilterCount(state.filters), onClick: () => ctx.clearFilters() },
            { separator: true },
            { label: t("topic.save_filters"), icon: "layers", onClick: () => ctx.saveFiltersAsTopic?.() },
          ],
        });
      },
    }, icon("more", "sm"))));

  const search = h("input", {
    class: "filter-input", value: state.filters.q || "", placeholder: t("filters.keyword"),
    onkeydown: (e) => {
      if (e.key === "Enter") { ctx.setFilter("q", e.target.value.trim()); }
    },
  });
  host.appendChild(h("div", { class: "rail-section" }, search));

  // Types
  // Only count types that are in this view's type scope.  A facet may also
  // contain off-scope types (for example Agent on the Map); showing those
  // makes the group total disagree with the visible unit total.
  const typeBase = [...base];
  if (typeBase.length) {
    const selected = selectedTypes(state.filters, typeBase);
    const allSelected = typeBase.every((type) => selected.includes(type));
    const allToggle = h("input", {
      type: "checkbox", checked: allSelected,
      "aria-label": t("map.all_types"),
      onchange: (event) => ctx.toggleAllTypes(event.target.checked),
    });
    allToggle.indeterminate = selected.length > 0 && !allSelected;
    const typeWrap = h("div", { class: "type-filter" });
    for (const type of typeBase) {
      const on = selected.includes(type);
      typeWrap.appendChild(h("button", {
        class: `type-chip ${on ? "on" : ""}`.trim(),
        "aria-pressed": String(on),
        onclick: () => ctx.toggleFilterValue("types", type),
      }, h("i", { style: { "--dot": typeColor(type) } }), typeLabel(type),
        h("span", { class: "n" }, facets.types?.[type] ?? 0)));
    }
    host.appendChild(section([
      h("span", null, t("filters.types")),
      h("label", { class: "type-all-toggle", title: t("map.all_types") }, allToggle, t("filters.all")),
    ], typeWrap));
  }

  // Status & importance: same chip/OR behavior as types.  Each field has its
  // own group; status and importance groups combine with AND in the backend.
  const statusBase = [...new Set([
    ...(ctx.S.vocab?.statuses || []).map((item) => item.id),
    ...Object.keys(facets.statuses || {}),
  ])];
  if (statusBase.length) {
    const selected = selectedStatuses(state.filters, statusBase);
    const allSelected = statusBase.every((value) => selected.includes(value));
    const allToggle = h("input", {
      type: "checkbox", checked: allSelected,
      "aria-label": `${t("filters.all")} ${t("filters.status")}`,
      onchange: (event) => ctx.toggleAllStatuses(event.target.checked),
    });
    allToggle.indeterminate = selected.length > 0 && !allSelected;
    const statusWrap = h("div", { class: "type-filter" });
    for (const id of statusBase) {
      const on = selected.includes(id);
      statusWrap.appendChild(h("button", {
        type: "button", class: `type-chip ${on ? "on" : ""}`.trim(),
        "aria-pressed": String(on),
        onclick: () => ctx.toggleStatus(id, statusBase),
      }, h("i", { style: { "--dot": statusColor(id) } }), statusLabel(id),
        h("span", { class: "n" }, facets.statuses?.[id] ?? 0)));
    }
    host.appendChild(section([
      h("span", null, t("filters.status")),
      h("label", { class: "type-all-toggle" }, allToggle, t("filters.all")),
    ], statusWrap));
  }

  const importanceBase = [...new Set([
    ...(ctx.S.vocab?.importance || []).map((item) => item.id),
    ...Object.keys(facets.importance || {}),
  ])];
  if (importanceBase.length) {
    const selected = selectedImportance(state.filters, importanceBase);
    const allSelected = importanceBase.every((value) => selected.includes(value));
    const allToggle = h("input", {
      type: "checkbox", checked: allSelected,
      "aria-label": `${t("filters.all")} ${t("filters.importance")}`,
      onchange: (event) => ctx.toggleAllImportance(event.target.checked),
    });
    allToggle.indeterminate = selected.length > 0 && !allSelected;
    const importanceWrap = h("div", { class: "type-filter" });
    for (const id of importanceBase) {
      const on = selected.includes(id);
      importanceWrap.appendChild(h("button", {
        type: "button", class: `type-chip ${on ? "on" : ""}`.trim(),
        "aria-pressed": String(on),
        onclick: () => ctx.toggleImportance(id, importanceBase),
      }, h("i", { style: { "--dot": impColor(id) } }), impLabel(id),
        h("span", { class: "n" }, facets.importance?.[id] ?? 0)));
    }
    host.appendChild(section([
      h("span", null, t("filters.importance")),
      h("label", { class: "type-all-toggle" }, allToggle, t("filters.all")),
    ], importanceWrap));
  }

  host.appendChild(assigneeFilterSection(ctx, facets));

  // Time and graph options live above the tag cloud; tags are the longest
  // section and should not push these controls off-screen.
  host.appendChild(section(t("filters.time"), timeFilterControl(state, ctx, facets)));
  host.appendChild(section(t("filters.graph_options"), h("div", { class: "filter-check-list" },
    h("label", { class: "filter-check" },
      h("input", { type: "checkbox", checked: state.filters.roots ? true : undefined,
        onchange: (e) => ctx.setFilter("roots", e.target.checked) }),
      h("span", null, t("filters.top_level"))),
    h("label", { class: "filter-check" },
      h("input", { type: "checkbox", checked: state.filters.context ? true : undefined,
        onchange: (e) => ctx.setFilter("context", e.target.checked) }),
      h("span", null, t("map.context")),
      h("span", { class: "filter-check-hint" }, t("filters.graph_hint"))))));

  // Selected tags (including virtual "unset" values)
  if (state.filters.tags.length || state.filters.tagsUnset?.length) {
    const chips = h("div", { class: "chips", style: { marginBottom: "4px" } },
      state.filters.tags.map((tag) => h("span", {
        class: "chip on", title: t("action.remove"), onclick: () => ctx.toggleFilterValue("tags", tag),
      }, tag.startsWith("needs:") ? `${t("needs.group")}: ${t(`needs.value.${tag.slice(6)}`)}` : tag, h("span", { style: { opacity: ".8" } }, "×"))),
      (state.filters.tagsUnset || []).map((ns) => h("span", {
        class: "chip on unset-chip", title: t("action.remove"),
        onclick: () => ctx.toggleFilterValue("tagsUnset", ns),
      }, ns === "needs" ? `∅ ${t("needs.group")}` : `∅ ${ns}`, h("span", { style: { opacity: ".8" } }, "×"))));
    host.appendChild(section(t("filters.tags"), chips));
  }

  // Tag namespaces
  host.appendChild(h("div", { class: "tag-filter-hint" }, t("filters.tag_logic_hint")));
  const namespaces = facets.namespaces || {};
  const groupNames = Object.keys(namespaces).sort((a, b) => {
    // "needs" (Needs you, set by ARK) leads; then the usual research namespaces.
    const priority = ["needs", "topic", "stage", "role", "component", "protocol", "outcome", "channel", "venue", "quality", "risk", "gate", "system"];
    const ai = priority.indexOf(a), bi = priority.indexOf(b);
    return (ai < 0 ? 50 : ai) - (bi < 0 ? 50 : bi) || a.localeCompare(b);
  });
  for (const ns of groupNames) {
    const isNeeds = ns === "needs";
    const values = isNeeds ? sortNeedsValues(namespaces[ns] || []) : (namespaces[ns] || []);
    if (!values.length) continue;
    const selectedInNs = values.some((value) => state.filters.tags.includes(value.tag));
    const unsetOn = (state.filters.tagsUnset || []).includes(ns);
    // Small groups stay expanded. Namespaces with more than 10 values start
    // collapsed so the filter rail remains usable; a selected value or the
    // unset option keeps that group open.
    const expanded = values.length <= 10 || selectedInNs || unsetOn;
    const details = h("details", { class: "tag-filter-wrap", open: expanded ? true : undefined });
    if (isNeeds) details.dataset.ns = "needs";
    details.appendChild(h("summary", null, `${isNeeds ? t("needs.group") : nsLabel(ns)} (${values.length})`));
    const list = h("div", { class: "tag-filter-cloud" });
    for (const value of values.slice(0, 20)) {
      list.appendChild(h("button", {
        class: `tag-filter ${state.filters.tags.includes(value.tag) ? "on" : ""}`.trim(),
        title: value.tag,
        onclick: (event) => ctx.toggleFilterValue("tags", value.tag, event.currentTarget),
      }, h("span", { class: "tag-value" }, isNeeds ? t(`needs.value.${value.value}`) : value.value),
      h("span", { class: "n" }, value.count)));
    }
    const unsetCount = facets.unset?.[ns] ?? 0;
    list.appendChild(h("button", {
      class: `tag-filter unset-filter ${unsetOn ? "on" : ""}`.trim(),
      title: t("filters.unset_hint"),
      onclick: () => ctx.toggleFilterValue("tagsUnset", ns),
    }, h("span", { class: "tag-value" }, isNeeds ? `∅ ${t("needs.unset")}` : `∅ ${t("filters.unset")}`),
    h("span", { class: "n" }, unsetCount)));
    details.appendChild(list);
    host.appendChild(details);
  }



}

function timeFilterControl(state, ctx, facets) {
  const mode = ["all", "timeline", "recent", "overdue"].includes(state.filters.timeMode)
    ? state.filters.timeMode : "all";
  const modeButton = (key, label) => h("button", {
    type: "button", class: `time-mode-btn ${mode === key ? "on" : ""}`.trim(),
    "aria-pressed": String(mode === key),
    onclick: () => ctx.setTimeMode?.(key),
  }, label);
  const head = h("div", { class: "time-mode-row" },
    modeButton("all", t("filters.time_mode.all")),
    modeButton("timeline", t("filters.time_mode.timeline")),
    modeButton("recent", t("filters.time_mode.recent")),
    modeButton("overdue", t("filters.time_mode.overdue")));
  let body;
  if (mode === "timeline") {
    body = timeRangeControl(state, ctx, facets);
  } else if (mode === "recent") {
    const recent = [1, 3, 7, 30];
    body = h("div", { class: "recent-days" },
      h("span", { class: "recent-days-label" }, t("filters.recent_days")),
      recent.map((days) => h("button", {
        type: "button",
        class: `recent-day-btn ${Number(state.filters.recentDays) === days ? "on" : ""}`.trim(),
        "aria-pressed": String(Number(state.filters.recentDays) === days),
        onclick: () => ctx.setRecentDays?.(days),
      }, days === 1 ? t("filters.one_day") : `${days} ${t("filters.days")}`)));
  } else if (mode === "overdue") {
    body = h("div", { class: "time-range-empty" }, t("filters.overdue_hint"));
  } else {
    body = h("div", { class: "time-range-empty" }, t("filters.all_time"));
  }
  return h("div", { class: "time-filter" }, head, body);
}

function timeRangeControl(state, ctx, facets) {
  const info = facets.time || {};
  const HOUR = 3_600_000;
  const asMs = (value, endOfDay = false) => {
    const text = String(value || "").trim();
    if (!text) return NaN;
    if (/^\d{4}-\d{2}-\d{2}$/.test(text)) {
      const [year, month, day] = text.split("-").map(Number);
      // Date-only ARK values are calendar dates in the viewer's timezone, not
      // UTC midnight.  Keeping them local makes the slider labels intuitive.
      return new Date(year, month - 1, day,
        endOfDay ? 23 : 0, endOfDay ? 59 : 0, endOfDay ? 59 : 0,
        endOfDay ? 999 : 0).getTime();
    }
    return Date.parse(text);
  };
  let minMs = asMs(info.min, false);
  let maxMs = asMs(info.max, true);
  if (!Number.isFinite(minMs) || !Number.isFinite(maxMs)) {
    minMs = Infinity; maxMs = -Infinity;
    for (const unit of state.units || []) {
      const s = asMs(unit.start_at, false);
      const e = asMs(unit.end_at, true);
      if (Number.isFinite(s)) minMs = Math.min(minMs, s);
      if (Number.isFinite(e)) maxMs = Math.max(maxMs, e);
      if (Number.isFinite(s) && !Number.isFinite(e)) maxMs = Math.max(maxMs, s);
      if (Number.isFinite(e) && !Number.isFinite(s)) minMs = Math.min(minMs, e);
    }
  }
  if (!Number.isFinite(minMs) || !Number.isFinite(maxMs)) {
    return h("div", { class: "time-range-empty" }, t("filters.no_dates"));
  }
  if (maxMs < minMs) [minMs, maxMs] = [maxMs, minMs];

  // Keep the full data extent, but expose it at hourly resolution.  The
  // slider is in epoch hours, not days, so dragging can select e.g. 14:00.
  const minHour = Math.floor(minMs / HOUR);
  const maxHour = Math.ceil(maxMs / HOUR);
  const span = Math.max(1, maxHour - minHour);
  const clamp = (value) => Math.max(minHour, Math.min(maxHour, Number(value) || minHour));
  const hourOf = (value) => {
    const ms = asMs(value, false);
    return Number.isFinite(ms) ? Math.floor(ms / HOUR) : minHour;
  };
  const endHourOf = (value) => {
    const ms = asMs(value, true);
    return Number.isFinite(ms) ? Math.ceil(ms / HOUR) : maxHour;
  };
  let startHour = clamp(state.filters.from ? hourOf(state.filters.from) : minHour);
  let endHour = clamp(state.filters.to ? endHourOf(state.filters.to) : maxHour);
  if (endHour < startHour) endHour = startHour;

  const fill = h("div", { class: "time-range-fill" });
  const fromLabel = h("span", { class: "time-range-value" });
  const toLabel = h("span", { class: "time-range-value" });
  const fromInput = h("input", {
    type: "range", class: "time-range-input from", min: String(minHour), max: String(maxHour),
    step: "1", value: String(startHour), "aria-label": t("filters.time"),
  });
  const toInput = h("input", {
    type: "range", class: "time-range-input to", min: String(minHour), max: String(maxHour),
    step: "1", value: String(endHour), "aria-label": t("filters.time"),
  });
  let timer = 0;
  let lastLiveRun = 0;
  const emit = (from, to, live) => {
    clearTimeout(timer);
    const run = () => ctx.setTimeRange?.(from, to, { repaint: false, repaintRail: !live });
    if (!live) {
      run();
      return;
    }
    // Throttle, do not debounce: while the pointer is moving we still paint
    // immediately, then at most once per 50 ms.  A debounce would wait until
    // the drag pauses or the pointer is released.
    const now = performance.now();
    if (now - lastLiveRun >= 50) {
      lastLiveRun = now;
      run();
    } else {
      timer = setTimeout(() => {
        lastLiveRun = performance.now();
        run();
      }, 50);
    }
  };
  const sync = (apply = false, source = null, live = false) => {
    startHour = clamp(fromInput.value);
    endHour = clamp(toInput.value);
    if (source === fromInput && startHour > endHour) endHour = startHour;
    if (source === toInput && endHour < startHour) startHour = endHour;
    fromInput.value = String(startHour);
    toInput.value = String(endHour);
    const left = ((startHour - minHour) / span) * 100;
    const right = ((maxHour - endHour) / span) * 100;
    fill.style.left = `${left}%`;
    fill.style.right = `${right}%`;
    const fromDate = new Date(startHour * HOUR);
    const toDate = new Date(endHour * HOUR);
    fromLabel.textContent = fmtDateTime(fromDate.toISOString());
    toLabel.textContent = fmtDateTime(toDate.toISOString());
    if (apply) {
      const from = startHour <= minHour ? "" : fromDate.toISOString();
      const to = endHour >= maxHour ? "" : new Date(endHour * HOUR + HOUR - 1).toISOString();
      emit(from, to, live);
    }
  };
  fromInput.addEventListener("input", () => sync(true, fromInput, true));
  toInput.addEventListener("input", () => sync(true, toInput, true));
  fromInput.addEventListener("change", () => sync(true, fromInput, false));
  toInput.addEventListener("change", () => sync(true, toInput, false));
  const reset = h("button", {
    class: "time-range-reset", type: "button", title: t("filters.any_time"),
    onclick: () => {
      clearTimeout(timer);
      startHour = minHour; endHour = maxHour;
      sync(false);
      ctx.setTimeRange?.("", "", { repaint: true, repaintRail: true });
    },
  }, t("filters.all_time"));
  sync(false);
  return h("div", { class: "time-range" },
    h("div", { class: "time-range-labels" }, fromLabel, h("span", { class: "time-range-dash" }, "–"), toLabel),
    h("div", { class: "time-range-track" },
      h("div", { class: "time-range-rail" }), fill, fromInput, toInput),
    h("div", { class: "time-range-foot" },
      h("span", { class: "time-range-hint" }, info.undated ? `${info.undated} ${t("filters.undated")}` : ""),
      reset));
}

function railTabs(state, ctx) {
  const count = state.infoHubData?.total;
  return h("div", { class: "rail-tabs" },
    h("button", {
      class: `rail-tab ${state.filterTab !== "infohub" ? "on" : ""}`.trim(),
      onclick: () => ctx.setFilterTab("ark"),
    }, "ARK"),
    h("button", {
      class: `rail-tab ${state.filterTab === "infohub" ? "on" : ""}`.trim(),
      onclick: () => ctx.setFilterTab("infohub"),
    }, "InfoHub", count != null ? h("span", { class: "n" }, count) : null)); // i18n-ignore: product name
}

function renderInfoHubRail(host, ctx) {
  const state = ctx.S;
  const filters = state.infoHubFilters || (state.infoHubFilters = {
    q: "", kinds: ["topic"], tags: [], status: "", days: 3, mine: false,
    roots: false, context: false, connected: false, order: "activity",
  });
  let data = state.infoHubData;
  const refresh = () => ctx.refresh();
  const updateGraph = () => {
    const top = host.scrollTop;
    if (typeof ctx.updateInfoHubGraph === "function") {
      ctx.updateInfoHubGraph();
      requestAnimationFrame(() => { host.scrollTop = top; });
    } else {
      ctx.refresh();
    }
  };
  const head = h("div", { class: "ih-head" },
    h("div", null,
      h("div", { class: "ih-title" }, t("filters.infohub.title")),
      h("div", { class: "ih-path" }, data?.path ? data.path.split(/[\\/]/).slice(-2).join("/") : t("filters.infohub.read_only_source"))),
    h("div", { class: "row tight" },
      h("span", { class: "pill" }, data ? `${data.total}` : "-"),
      h("button", {
        class: "icon-btn", title: t("action.close"),
        style: { display: window.innerWidth <= 980 ? "grid" : "none" },
        onclick: () => { state.railOpen = false; host.classList.remove("open"); ctx.closeFilters?.(); },
      }, icon("x"))));
  host.appendChild(head);
  host.appendChild(h("div", { class: "ih-hint" }, t("ideas.infohub_hint")));

  const search = h("input", {
    class: "filter-input", value: filters.q, placeholder: t("filters.keyword"),
    onkeydown: (e) => { if (e.key === "Enter") { filters.q = e.target.value.trim(); updateGraph(); } },
  });
  const kinds = h("div", { class: "ih-kinds" });
  const tagWrap = h("div", { class: "ih-tag-filter" });
  const status = h("div", { class: "ih-loading" });

  function renderKinds() {
    clear(kinds);
    kinds.appendChild(h("button", {
      class: `ih-kind ${filters.kinds.length ? "" : "on"}`.trim(),
      onclick: () => { filters.kinds = []; renderKinds(); updateGraph(); },
    }, t("action.all")));
    for (const item of data?.kinds || []) {
      const on = filters.kinds.includes(item.kind);
      kinds.appendChild(h("button", {
        class: `ih-kind ${on ? "on" : ""}`.trim(),
        style: { "--kind-color": KIND_COLORS[item.kind] || "#94a3b8" },
        onclick: () => {
          filters.kinds = on ? filters.kinds.filter((kind) => kind !== item.kind) : [...filters.kinds, item.kind];
          renderKinds();
          updateGraph();
        },
      }, h("i", { class: "ih-kind-dot" }), item.kind, h("span", { class: "n" }, item.count)));
    }
  }

  function renderTags() {
    const tagScroll = tagWrap.scrollTop;
    clear(tagWrap);
    const selected = filters.tags || [];
    const toggleTag = (tag) => {
      filters.tags = selected.includes(tag)
        ? selected.filter((value) => value !== tag)
        : [...selected, tag];
      renderTags();
      updateGraph();
    };
    if (selected.length) {
      tagWrap.appendChild(h("details", { class: "ih-tag-group", open: true },
        h("summary", { class: "ih-tag-ns" }, t("filters.infohub.selected"), h("span", { class: "n" }, selected.length)),
        h("div", { class: "ih-tag-row" }, ...selected.map((tag) => h("button", {
          class: "ih-tag on", title: t("filters.infohub.remove_tag", { tag }),
          onclick: () => { filters.tags = selected.filter((value) => value !== tag); renderTags(); updateGraph(); },
        }, tag, h("span", { class: "n" }, "×"))))));
    }
    const groups = new Map();
    for (const item of data?.tags || []) {
      const namespace = item.tag.split(":", 1)[0] || "tag";
      if (!groups.has(namespace)) groups.set(namespace, []);
      groups.get(namespace).push(item);
    }
    for (const [namespace, items] of groups) {
      const groupSelected = items.some((item) => selected.includes(item.tag));
      tagWrap.appendChild(h("details", {
        class: "ih-tag-group",
        open: groupSelected ? true : undefined,
      },
        h("summary", { class: "ih-tag-ns", title: namespace === "source" ? t("filters.infohub.source_hint") : namespace },
          `${namespace}:`, h("span", { class: "n" }, items.length)),
        h("div", { class: "ih-tag-row" }, ...items.map((item) => {
          const on = selected.includes(item.tag);
          const value = item.tag.startsWith(namespace + ":") ? item.tag.slice(namespace.length + 1) : item.tag;
          return h("button", {
            class: `ih-tag ${on ? "on" : ""}`.trim(), title: item.tag,
            "aria-pressed": on ? "true" : "false",
            onclick: () => toggleTag(item.tag),
          }, value, h("span", { class: "n" }, item.count));
        }))));
    }
    tagWrap.scrollTop = tagScroll;
  }

  const statusSel = h("select", { class: "select", onchange: (e) => { filters.status = e.target.value; updateGraph(); } },
    h("option", { value: "" }, t("filters.infohub.open_done")),
    h("option", { value: "open", selected: filters.status === "open" ? true : undefined }, t("status.open")),
    h("option", { value: "done", selected: filters.status === "done" ? true : undefined }, t("status.done")),
    h("option", { value: "dropped", selected: filters.status === "dropped" ? true : undefined }, t("filters.infohub.ignored")),
    h("option", { value: "all", selected: filters.status === "all" ? true : undefined }, t("filters.infohub.all_statuses")));
  statusSel.value = filters.status || "";
  const maxDays = Math.max(1, Number(state.infoHubMaxDays) || Number(data?.max_days) || 365);
  const daysValue = h("span", { class: "ih-days-value" }, filters.days ? t("filters.infohub.n_days", { n: filters.days }) : t("filters.any_time"));
  const daysRange = h("input", {
    type: "range", min: "0", max: String(maxDays), step: "0.5", value: String(Math.max(0, Math.min(maxDays, Number(filters.days) || 0))),
    oninput: (e) => {
      const value = Number(e.target.value);
      daysNumber.value = String(value);
      daysValue.textContent = value ? t("filters.infohub.n_days", { n: value }) : t("filters.any_time");
    },
    onchange: (e) => { filters.days = Number(e.target.value) || 0; updateGraph(); },
  });
  const daysNumber = h("input", {
    type: "number", min: "0", max: String(maxDays), step: "0.1", value: String(Number(filters.days) || 0),
    onchange: (e) => {
      const value = Math.max(0, Math.min(maxDays, Number(e.target.value) || 0));
      e.target.value = String(value);
      daysRange.value = String(value);
      daysValue.textContent = value ? t("filters.infohub.n_days", { n: value }) : t("filters.any_time");
      filters.days = value;
      updateGraph();
    },
  });
  const scopeSel = h("select", { class: "select", onchange: (e) => { filters.mine = e.target.value === "mine"; updateGraph(); } },
    h("option", { value: "all", selected: filters.mine ? undefined : true }, t("filters.infohub.everything")),
    h("option", { value: "mine", selected: filters.mine ? true : undefined }, t("filters.infohub.for_me")));
  scopeSel.value = filters.mine ? "mine" : "all";
  const orderSel = h("select", { class: "select", onchange: (e) => { filters.order = e.target.value; updateGraph(); } },
    h("option", { value: "activity", selected: filters.order === "activity" ? true : undefined }, t("filters.infohub.order_activity")),
    h("option", { value: "oldest", selected: filters.order === "oldest" ? true : undefined }, t("filters.infohub.order_oldest")),
    h("option", { value: "due", selected: filters.order === "due" ? true : undefined }, t("filters.infohub.order_due")),
    h("option", { value: "created", selected: filters.order === "created" ? true : undefined }, t("filters.infohub.order_created")),
    h("option", { value: "title", selected: filters.order === "title" ? true : undefined }, t("filters.infohub.order_title")));
  orderSel.value = filters.order || "activity";

  const onlyLinked = h("input", { type: "checkbox", checked: filters.connected !== false ? true : undefined,
    onchange: (e) => { filters.connected = e.target.checked; updateGraph(); } });
  const rootsOnly = h("input", { type: "checkbox", checked: filters.roots ? true : undefined,
    onchange: (e) => { filters.roots = e.target.checked; updateGraph(); } });
  const withContext = h("input", { type: "checkbox", checked: filters.context ? true : undefined,
    onchange: (e) => { filters.context = e.target.checked; updateGraph(); } });

  async function load() {
    status.textContent = t("action.loading");
    try {
      state.infoHubData = await api.infoHubBrowse({
        path: state.infoHubPath, q: filters.q, kind: (filters.kinds || []).join(","),
        tags: (filters.tags || []).join(","), status: filters.status,
        days: filters.days, mine: filters.mine ? 1 : 0, roots: filters.roots ? 1 : 0,
        order: filters.order, limit: 1,
      });
      state.infoHubPath = state.infoHubData.path;
      data = state.infoHubData;
      if (data.max_days) {
        state.infoHubMaxDays = data.max_days;
        daysRange.max = String(data.max_days);
        daysNumber.max = String(data.max_days);
      }
      status.textContent = `${data.total} ${t("common.units")}`;
      renderKinds(); renderTags();
    } catch (err) {
      status.textContent = err.message;
    }
  }

  host.appendChild(h("div", { class: "rail-section" }, search));
  host.appendChild(h("div", { class: "rail-section" }, kinds,
    h("div", { class: "ih-selects" }, statusSel, scopeSel, orderSel),
    h("div", { class: "ih-days" }, h("div", { class: "rail-label" }, t("filters.infohub.activity_window")), daysValue,
      h("div", { class: "ih-days-controls" }, daysRange, daysNumber)),
    h("label", { class: "ih-only" }, onlyLinked, t("filters.infohub.only_linked")),
    h("label", { class: "ih-only" }, rootsOnly, t("filters.infohub.roots_only")),
    h("label", { class: "ih-only" }, withContext, t("filters.infohub.one_hop"))));
  host.appendChild(h("div", { class: "rail-section" },
    h("div", { class: "rail-label" }, t("common.tags")), tagWrap));
  host.appendChild(h("div", { class: "rail-section" }, status));
  host.appendChild(h("div", { class: "rail-foot" },
    h("button", { class: "btn ghost", onclick: () => ctx.openImport() }, t("ideas.import")),
    h("button", { class: "btn", onclick: refresh }, t("action.refresh"))));
  renderKinds(); renderTags();
  load();
}

function section(title, body) {
  return h("div", { class: "rail-section" },
    h("div", { class: "rail-label" }, title), body);
}

// -------------------------------------------------------------------- cards
export function unitCard(unit, ctx, { selected = false, onTag = null, showActions = true } = {}) {
  const rawTags = unit.tags || [];
  const venue = rawTags.find((tag) => tag.startsWith("venue:"))?.slice(6);
  const year = rawTags.find((tag) => tag.startsWith("year:"))?.slice(5);
  const tags = rawTags.filter((tag) => !/^(type|status|importance|venue|year):/.test(tag));
  const due = dueState(unit);
  const card = h("article", {
    class: `card click ${selected ? "selected" : ""}`.trim(),
    style: { "--type-color": typeColor(unit.type) },
    dataset: { unitRef: unit.uid || unit.id },
    onclick: (e) => {
      if (e.target.closest("button,a")) return;
      ctx.openUnit(unit.uid || unit.id, { x: e.clientX, y: e.clientY });
    },
  },
    h("span", { class: "type-accent" }),
    h("div", { class: "card-top" },
      typeBadge(unit.type), h("span", { class: "uid" }, unit.uid),
      h("span", { class: "right" }, statusPill(unit.status), impDot(unit.importance))),
    h("h3", { class: "card-title" }, unit.title),
    unit.excerpt || unit.body ? h("p", { class: "excerpt" }, (unit.excerpt || unit.body || "").slice(0, 240)) : null,
    unit.source_title ? h("div", { style: { marginTop: "5px" } },
      h("span", { class: "source-chip", title: unit.source_title }, icon("database", "sm"), unit.source_title)) : null,
    venue || year ? h("div", { class: "card-meta", style: { marginTop: "6px" } },
      icon("book", "sm"), [venue, year].filter(Boolean).join(" · ")) : null,
    tags.length ? h("div", { class: "tags" }, tags.slice(0, 7).map((tag) => tagChip(tag, { onClick: (e) => { e.stopPropagation(); onTag?.(tag); } }))) : null,
    h("div", { class: "card-meta" },
      unit.end_at ? h("span", { class: due === "overdue" ? "overdue" : "" }, `${t("common.due")} ${fmtDate(unit.end_at)}`) : null,
      unit.start_at && !unit.end_at ? h("span", null, fmtDate(unit.start_at)) : null,
      unit.out_count + unit.in_count ? h("span", null, linksLabel(unit.out_count + unit.in_count)) : null,
      unit.media?.length ? h("span", { class: "row tight" }, icon("image", "sm"), unit.media.length) : null,
      unit.assignees?.length ? h("span", { class: "assignees" }, unit.assignees.map((a) => avatarFor(a))) : null,
      assigneeChip(unit),
      h("span", { class: "right", style: { fontSize: "10px", color: "var(--mute)" } }, relativeTime(unit.updated_at))),
    showActions ? h("div", { class: "card-actions" },
      h("button", { class: "btn sm", onclick: (e) => { e.stopPropagation(); ctx.openUnit(unit.uid || unit.id); } }, t("action.open")),
      h("button", { class: "btn sm ghost", onclick: (e) => { e.stopPropagation(); ctx.openLink(unit); } }, icon("link", "sm"), t("action.connect")),
      unit.type === "task" ? h("button", { class: "btn sm ghost", onclick: (e) => { e.stopPropagation(); (ctx.dispatchTaskSession ? ctx.dispatchTaskSession(unit) : openDispatchModal(ctx, unit)); } }, t("tasks.dispatch")) : null) : null);
  return card;
}

export function viewToggle(value, options, onChange) {
  return h("div", { class: "view-toggle" },
    options.map(([id, label, ic]) => h("button", {
      class: value === id ? "on" : "", onclick: () => onChange(id), title: label,
    }, ic ? icon(ic, "sm") : null, label)));
}

export function typeLegend(units) {
  const counts = new Map();
  for (const u of units) counts.set(u.type, (counts.get(u.type) || 0) + 1);
  return h("div", { class: "chips" }, [...counts.entries()].sort((a, b) => b[1] - a[1]).map(([type, n]) =>
    h("span", { class: "chip", style: { cursor: "default" } },
      h("i", { style: { width: "6px", height: "6px", borderRadius: "2px", background: typeColor(type), display: "inline-block" } }),
      typeLabel(type), h("span", { style: { color: "var(--mute)" } }, n))));
}

export function actorLabel(actor = "") {
  if (actor.startsWith("agent:")) return actor.slice(6);
  if (actor === "human:ui") return t("common.you");
  if (actor === "system" || actor.startsWith("system:")) return "ARK";
  if (actor.startsWith("import:")) return actor.slice(7);
  return displayName(actor);
}

export function attachGraphHeight(wrap, ctx, selector = ".graph-panel") {
  const target = wrap.querySelector(selector);
  if (!target) return wrap;
  const saved = Number(localStorage.getItem("ark.graphHeight"));
  if (saved >= 280) wrap.style.setProperty("--graph-h", `${saved}px`);
  const handle = h("div", {
    class: "graph-resize-handle",
    title: t("graph.resize_handle"),
    role: "separator",
    "aria-orientation": "horizontal",
  }, h("span", { class: "graph-resize-grip" }));
  let state = null;
  handle.addEventListener("pointerdown", (event) => {
    if (event.button !== 0) return;
    event.preventDefault();
    event.stopPropagation();
    state = { id: event.pointerId, y: event.clientY, height: target.getBoundingClientRect().height };
    handle.classList.add("active");
    try { handle.setPointerCapture(event.pointerId); } catch { /* ignore */ }
  });
  handle.addEventListener("pointermove", (event) => {
    if (!state || state.id !== event.pointerId) return;
    const max = Math.max(360, window.innerHeight - 120);
    const height = Math.max(300, Math.min(max, state.height + event.clientY - state.y));
    wrap.style.setProperty("--graph-h", `${height}px`);
  });
  const finish = (event) => {
    if (!state || state.id !== event.pointerId) return;
    const height = Math.round(target.getBoundingClientRect().height);
    state = null;
    handle.classList.remove("active");
    ctx.S.graphHeight = height;
    localStorage.setItem("ark.graphHeight", String(height));
  };
  handle.addEventListener("pointerup", finish);
  handle.addEventListener("pointercancel", finish);
  wrap.appendChild(handle);
  return wrap;
}
