// Splitting and merging projects.  Agents propose plans through the agent
// API; a person opens a plan from its decision node ("?move_plan=<id>"
// link), checks the summary and approves, approves and moves, or rejects.
// The same view offers Undo once the plan was applied.  People can also
// start a split from a node's menu or a merge from the command palette: that
// makes a plan and opens the same view.
// Server: /api/project-move/{clusters,plans,...} (routes_projectmove.go).

import { t } from "./i18n.js";
import { h, unitReference } from "./dom.js";
import { api } from "./api.js";
import { openModal, toast } from "./components.js";
import { S } from "./state.js";
import { clusterOfRoot, crossProjectRows, mergeRequest, moveTargets, planRows, splitCandidates, splitRequest } from "./project-move-model.js";

const PLAN_PARAM = "move_plan";

// Unit menu: split off the branch this unit starts.
export function projectSplitMenuItem(ctx, unit) {
  const uid = String(unit?.uid || unit?.id || "");
  if (!uid || !S.project?.id || S.project.demo) return null;
  return { label: t("project_move.split_menu"), icon: "folder", onClick: () => openProjectSplit(ctx, { uid }) };
}

// Command palette entries.
export function projectMoveCommands(ctx) {
  if (!S.project?.id || S.project.demo) return [];
  return [
    { id: "project-split", label: t("project_move.split_title"), cat: t("palette.commands"), run: () => openProjectSplit(ctx) },
    { id: "project-merge", label: t("project_move.merge_title"), cat: t("palette.commands"), run: () => openProjectMerge(ctx) },
  ];
}

// "?move_plan=<id>" links (in a plan's decision node) open the plan view
// instead of reloading the page; so does such a URL at start.
export function installMovePlanLinks(ctx) {
  document.addEventListener("click", (event) => {
    const link = event.target instanceof Element ? event.target.closest("a[href]") : null;
    if (!link || event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey) return;
    let url;
    try { url = new URL(link.getAttribute("href"), location.href); } catch { return; }
    const id = url.origin === location.origin ? url.searchParams.get(PLAN_PARAM) : null;
    if (!id) return;
    event.preventDefault(); event.stopPropagation();
    openMovePlan(ctx, id);
  }, true);
  const id = new URLSearchParams(location.search).get(PLAN_PARAM);
  if (id) setTimeout(() => openMovePlan(ctx, id), 120);
}

function planList(plan) {
  const { rows, notes, renamed, cross } = planRows(plan.summary);
  const list = (items) => h("ul", null, ...items.map(([key, params]) => h("li", null, t(key, params))));
  const parts = [
    h("p", null, plan.mode === "merge"
      ? t("project_move.plan_merge", { name: plan.request?.target_name || "" })
      : plan.mode === "import"
        ? t("project_move.plan_import", { name: plan.request?.source_name || "" })
        : t("project_move.plan_split", { name: plan.new_project?.name || "" })),
    list(rows),
  ];
  if (plan.note) parts.unshift(h("p", { class: "hint" }, plan.note));
  if (renamed.length) {
    parts.push(h("details", null, h("summary", null, t("project_move.uids", { n: renamed.length })),
      plan.state === "proposed" || plan.state === "approved" ? h("p", { class: "hint" }, t("project_move.uids_note")) : null,
      h("ul", null, ...renamed.slice(0, 200).map((u) => h("li", null, `${u.uid} → ${u.new_uid} · ${u.title}`)))));
  }
  if (cross.length) {
    parts.push(h("details", { open: cross.length <= 10 },
      h("summary", null, t(plan.request?.cross_links === "drop" ? "project_move.cross_dropped" : "project_move.cross_kept", { n: cross.length })),
      h("ul", null, ...cross.slice(0, 200).map((c) => h("li", null,
        c.direction === "out" ? `${c.uid} —${c.relation}→ ${c.other_uid} · ${c.other_title}`
          : `${c.other_uid} · ${c.other_title} —${c.relation}→ ${c.uid}`)))));
  }
  if (notes.length) parts.push(list(notes));
  return parts;
}

// What made a plan stale (GET .../plans/<id> "stale").
function staleNotice(stale) {
  const units = (list) => (list || []).slice(0, 10).join(", ") + ((list || []).length > 10 ? " …" : "");
  return h("div", { class: "hint err", "data-move-stale": "" },
    h("p", null, t("project_move.stale")),
    h("ul", null, ...(stale.reasons || []).map((r) => h("li", null,
      t(`project_move.stale.${r.code}`, { units: units(r.units), detail: r.message || "" })))));
}

// The plan view: summary, then the actions its state allows.
export async function openMovePlan(ctx, id) {
  let plan;
  try { ({ plan } = await api.movePlan(id)); } catch (err) { toast(err.message, "err"); return; }
  const act = (action, body, done) => async (event, close) => {
    try {
      const out = await api.movePlanAction(plan.id, action, body);
      close(true);
      ctx.closeUnit?.(); ctx.refresh?.();
      done?.(out);
    } catch (err) { toast(err.message, "err"); }
    return false;
  };
  const moved = (out) => toast(t("project_move.done", { n: out.move?.unit_count || 0, name: out.move?.target_name || "" }), "ok", {
    duration: 15000, action: { label: t("project_move.undo"), onClick: () => openMovePlan(ctx, plan.id) },
  });
  const actions = [{ label: t("action.close"), ghost: true }, { spacer: true }];
  const refresh = async (event, close) => {
    try {
      await api.movePlanAction(plan.id, "refresh", {});
      close(true);
      toast(t("project_move.refreshed"), "ok");
      ctx.refresh?.();
      openMovePlan(ctx, plan.id);
    } catch (err) { toast(err.message, "err"); }
    return false;
  };
  if (plan.stale && (plan.state === "proposed" || plan.state === "approved")) {
    if (plan.state === "proposed") {
      actions.push({ label: t("project_move.reject"), danger: true, close: false, onClick: act("reject", {}, () => toast(t("project_move.rejected"), "ok")) });
    }
    actions.push({ label: t("project_move.refresh"), primary: true, close: false, onClick: refresh });
  } else if (plan.state === "proposed") {
    actions.push(
      { label: t("project_move.reject"), danger: true, close: false, onClick: act("reject", {}, () => toast(t("project_move.rejected"), "ok")) },
      { label: t("project_move.approve"), close: false, onClick: act("approve", {}, () => toast(t("project_move.approved"), "ok")) },
      { label: t("project_move.approve_apply"), primary: true, close: false, onClick: act("approve", { apply: true }, moved) });
  } else if (plan.state === "approved") {
    actions.push({ label: t("project_move.apply"), primary: true, close: false, onClick: act("apply", {}, moved) });
  } else if (plan.state === "applied") {
    actions.push({ label: t("project_move.undo"), danger: true, close: false, onClick: act("undo", {}, () => toast(t("project_move.undone"), "ok")) });
  }
  openModal({
    title: t("project_move.plan_title"), wide: true,
    body: h("div", { class: "project-move" },
      h("p", null, h("span", { class: "pill" }, t(`project_move.state.${plan.state}`)),
        plan.created_by ? h("small", null, ` ${t("project_move.by", { who: plan.created_by })}`) : null),
      plan.stale ? staleNotice(plan.stale) : null,
      ...planList(plan),
      plan.error && !plan.stale ? h("p", { class: "hint err" }, plan.error) : null),
    actions,
  });
}

// Secondary path for people: pick groups, then review the plan as above.
export async function openProjectSplit(ctx, { uid = "" } = {}) {
  let data;
  try { data = await api.projectMoveClusters(); } catch (err) { toast(err.message, "err"); return; }
  const candidates = splitCandidates(data);
  const picked = new Set();
  const preset = uid ? clusterOfRoot(candidates, uid) : null;
  const extra = uid && !preset ? [uid] : [];
  if (preset) picked.add(preset.index);
  const name = h("input", { class: "input", type: "text", placeholder: t("project_move.new_name") });
  const cross = h("select", { class: "input" },
    h("option", { value: "keep" }, t("project_move.cross_keep")),
    h("option", { value: "drop" }, t("project_move.cross_drop")));
  const roots = () => [...extra, ...candidates.filter((c) => picked.has(c.index)).flatMap((c) => c.roots.map((r) => r.uid))];
  const rows = candidates.map((c) => {
    const box = h("input", { type: "checkbox", checked: picked.has(c.index) });
    box.addEventListener("change", () => { if (box.checked) picked.add(c.index); else picked.delete(c.index); });
    const head = c.roots.slice(0, 3).map((r) => `${r.uid} ${r.title}`).join(" · ") + (c.roots.length > 3 ? " …" : "");
    return h("label", { class: "check project-move-cluster" }, box,
      h("span", null, h("b", null, t("project_move.cluster", { n: c.units, k: c.cut })),
        c.weak ? h("span", { class: "pill ok" }, t("project_move.weak")) : null, h("br"), h("small", null, head)));
  });
  openModal({
    title: t("project_move.split_title"), wide: true,
    body: h("div", { class: "project-move" },
      h("p", { class: "hint" }, t("project_move.split_hint", { n: data.units || 0, k: candidates.filter((c) => c.weak).length })),
      extra.length ? h("p", null, t("project_move.split_unit", { uid })) : null,
      rows.length ? h("div", { class: "project-move-clusters" }, ...rows) : h("p", { class: "hint" }, t("project_move.no_clusters")),
      h("label", { class: "field" }, h("span", null, t("project_move.new_name")), name),
      h("label", { class: "field" }, h("span", null, t("project_move.cross_mode")), cross)),
    actions: [{ label: t("action.cancel"), ghost: true }, { spacer: true }, {
      label: t("project_move.preview"), primary: true, close: false,
      onClick: async (event, close) => {
        if (!roots().length) { toast(t("project_move.need_branch"), "err"); return false; }
        if (!name.value.trim()) { toast(t("project_move.need_name"), "err"); return false; }
        try {
          const { plan } = await api.movePlanCreate(splitRequest({ roots: roots(), newName: name.value, crossLinks: cross.value }));
          close(true);
          openMovePlan(ctx, plan.id);
        } catch (err) { toast(err.message, "err"); }
        return false;
      },
    }],
  });
}

export function openProjectMerge(ctx) {
  const targets = moveTargets(S.projects, S.project?.id);
  const select = h("select", { class: "input" }, ...targets.map((p) => h("option", { value: p.id }, p.name)));
  openModal({
    title: t("project_move.merge_title"), wide: true,
    body: h("div", { class: "project-move" },
      h("p", { class: "hint" }, t("project_move.merge_hint", { name: S.project?.name || "" })),
      h("label", { class: "field" }, h("span", null, t("project_move.target")), select)),
    actions: [{ label: t("action.cancel"), ghost: true }, { spacer: true }, {
      label: t("project_move.preview"), primary: true, close: false,
      onClick: async (event, close) => {
        if (!select.value) { toast(t("project_move.need_target"), "err"); return false; }
        try {
          const { plan } = await api.movePlanCreate(mergeRequest({ target: select.value }));
          close(true);
          openMovePlan(ctx, plan.id);
        } catch (err) { toast(err.message, "err"); }
        return false;
      },
    }],
  });
}

// Node details: "Other projects" (moved from / to, imported from, links
// to nodes in other projects); null when there is nothing to show.
export function crossProjectSection(unit) {
  const rows = crossProjectRows(unit?.meta);
  if (!rows.length) return null;
  return h("div", { class: "cross-project", "data-cross-project": "" }, ...rows.map((row) => {
    const ref = unitReference(`${row.project}/${row.uid}`);
    const label = row.kind === "link"
      ? t(row.direction === "out" ? "cross_project.link_out" : "cross_project.link_in", { relation: row.relation, name: row.name })
      : t(`cross_project.${row.kind}`, { name: row.name });
    return h("div", { class: "cross-project-row" }, h("span", { class: "muted" }, label), " ", ref,
      row.title ? h("span", null, ` ${row.title}`) : null);
  }));
}

