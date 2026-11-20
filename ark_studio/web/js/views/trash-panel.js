// Settings > Project > Recycle bin: the nodes deleted in this project.
// Restore puts a node back with its links to nodes that still exist; Delete
// permanently and Empty recycle bin remove nodes for good (after a confirm).
import { h, clear, fmtDateTime, typeBadge } from "../dom.js";
import { t } from "../i18n.js";
import { icon } from "../icons.js";
import { api } from "../api.js";
import { confirmDialog, toast } from "../components.js";
import { trashActor, withoutUnits, canEditTrash } from "../trash-model.js";

export function trashPanel(ctx) {
  const list = h("div", { class: "trash-list", "data-trash-list": "" });
  const count = h("span", { class: "pill" });
  const editable = canEditTrash(ctx.S.session);
  let units = [];
  let busy = false;

  const emptyAll = h("button", {
    class: "ghost-btn danger sm", type: "button", "data-trash-empty": "",
    onclick: async () => {
      if (busy || !units.length) return;
      const ok = await confirmDialog(t("trash.empty_confirm", { n: units.length }),
        { title: t("trash.empty_all"), confirmLabel: t("trash.empty_all"), danger: true });
      if (!ok) return;
      await run(async () => {
        const data = await api.emptyTrash();
        units = [];
        toast(t("trash.emptied", { n: data.deleted || 0 }), "ok");
      });
    },
  }, icon("trash", "sm"), t("trash.empty_all"));

  async function run(fn) {
    busy = true;
    render();
    try { await fn(); } catch (err) { toast(err.message || String(err), "err"); }
    finally { busy = false; render(); }
  }

  function restore(unit) {
    return run(async () => {
      await api.restoreUnit(unit.uid);
      units = withoutUnits(units, unit.uid);
      toast(t("trash.restored", { title: unit.title }), "ok");
    });
  }

  async function purge(unit) {
    const ok = await confirmDialog(t("trash.purge_confirm", { title: unit.title }),
      { title: t("trash.purge"), confirmLabel: t("trash.purge"), danger: true });
    if (!ok) return;
    await run(async () => {
      await api.purgeUnit(unit.uid);
      units = withoutUnits(units, unit.uid);
      toast(t("trash.purged", { title: unit.title }), "ok");
    });
  }

  function row(unit) {
    const by = trashActor(unit.deleted_by);
    return h("div", { class: "trash-item", "data-trash-uid": unit.uid },
      h("div", { class: "trash-main" },
        typeBadge(unit.type),
        h("code", null, unit.uid),
        h("b", null, unit.title || t("common.untitled"))),
      h("div", { class: "trash-meta muted" },
        h("span", null, t("trash.deleted_at", { date: fmtDateTime(unit.deleted_at) })),
        by ? h("span", null, t("trash.deleted_by", { name: by })) : null,
        unit.links ? h("span", null, t("trash.links", { n: unit.links })) : null),
      editable ? h("div", { class: "trash-actions" },
        h("button", { class: "ghost-btn sm", type: "button", disabled: busy, "data-trash-restore": unit.uid,
          onclick: () => restore(unit) }, icon("refresh", "sm"), t("trash.restore")),
        h("button", { class: "ghost-btn danger sm", type: "button", disabled: busy, "data-trash-purge": unit.uid,
          onclick: () => purge(unit) }, icon("trash", "sm"), t("trash.purge"))) : null);
  }

  function render() {
    clear(list);
    count.textContent = t("trash.count", { n: units.length });
    emptyAll.disabled = busy || !units.length;
    if (!units.length) {
      list.appendChild(h("div", { class: "empty sm" }, t("trash.empty")));
      return;
    }
    for (const unit of units) list.appendChild(row(unit));
  }

  async function load() {
    try {
      units = (await api.trash()).units || [];
      render();
    } catch (err) {
      clear(list);
      list.appendChild(h("div", { class: "settings-note" }, icon("alert", "sm"), h("div", null, err.message)));
    }
  }

  const panel = h("section", { class: "panel settings-panel trash-panel", "data-trash-panel": "" },
    h("div", { class: "panel-head" }, h("h3", null, t("trash.title")), count),
    h("p", { class: "settings-lead" }, t("trash.lead")),
    editable ? h("div", { class: "assistant-api-actions" }, emptyAll) : null,
    list);
  list.appendChild(h("div", { class: "settings-lead" }, t("trash.loading")));
  load();
  return panel;
}
