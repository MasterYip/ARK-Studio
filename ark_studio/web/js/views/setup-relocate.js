// Settings > Setup > Devices: move recorded paths (server: routes_taskspace.go).
// "Move paths" rewrites an old folder prefix to a new one in unit bodies,
// attachment links and setup locations; "Use task: paths" turns absolute
// paths inside the task space into task: links.  Both preview first; an
// applied change can be undone from the toast.  Files are not moved here.
import { h } from "../dom.js";
import { t } from "../i18n.js";
import { api } from "../api.js";
import { openModal, toast } from "../components.js";

function previewView(data) {
  const tot = data.totals || {};
  const units = (data.units || []).slice(0, 40);
  return h("div", { class: "relocate-preview" },
    h("p", null, t("relocate.summary", { units: tot.units || 0, paths: tot.paths || 0, media: tot.media || 0,
      locations: tot.locations || 0 })),
    (data.locations || []).length ? h("div", { class: "stack tight" }, h("b", null, t("relocate.locations")),
      ...(data.locations || []).map((l) => h("div", { class: "muted" }, h("code", null, l.before), " → ", h("code", null, l.after)))) : null,
    units.length ? h("div", { class: "stack tight" }, h("b", null, t("relocate.units")),
      ...units.map((u) => h("div", { class: "relocate-unit" },
        h("div", null, h("b", null, u.uid), " ", u.title, h("span", { class: "muted" }, ` · ${u.paths}`)),
        ...(u.lines || []).map((line) => h("pre", { class: "code-box", style: { whiteSpace: "pre-wrap", wordBreak: "break-all", margin: "2px 0" } },
          `- ${line.before}\n+ ${line.after}`))))) : null,
    (data.units || []).length > units.length ? h("p", { class: "muted" }, t("relocate.more", { n: data.units.length - units.length })) : null,
    h("p", { class: "muted" }, t("relocate.note")));
}

async function applyWithUndo(body, reload) {
  const data = await api.setupAction("relocate", { ...body, apply: true });
  const id = data.relocation;
  const conflicts = (data.conflicts || []).length;
  toast(t(conflicts ? "relocate.applied_conflicts" : "relocate.applied", { n: data.written || 0, loc: data.locations_written || 0, conflicts }),
    conflicts ? "err" : "ok", {
      duration: 12000,
      action: id ? {
        label: t("home.start.undo"),
        onClick: async () => {
          try {
            const undone = await api.setupAction("relocate", { undo: id });
            toast(t("relocate.undone", { n: undone.written || 0 }), "ok");
            reload?.();
          } catch (err) { toast(err.message, "err"); }
        },
      } : null,
    });
  reload?.();
}

/** Move paths of one device (from: its current task-space path, if any). */
export function openRelocateDialog({ device = null, from = "", reload = null } = {}) {
  const fromInput = h("input", { class: "input", value: from, placeholder: "C:/old/tasks", "aria-label": t("relocate.from") });
  const toInput = h("input", { class: "input", placeholder: "C:/new/tasks", "aria-label": t("relocate.to") });
  const all = h("input", { type: "checkbox" });
  const out = h("div", { class: "relocate-out" });
  let preview = null;
  const body = () => ({ from: fromInput.value.trim(), to: toInput.value.trim(), host_id: device?.id || "", all_devices: all.checked });
  const actions = [
    { label: t("action.cancel"), ghost: true },
    {
      label: t("relocate.preview"), close: false,
      onClick: async () => {
        try {
          preview = await api.setupAction("relocate", body());
          out.replaceChildren(previewView(preview));
          if (applyAction._btn) applyAction._btn.disabled = !((preview.totals?.paths || 0) + (preview.totals?.locations || 0));
        } catch (err) { toast(err.message, "err"); }
        return false;
      },
    },
  ];
  const applyAction = {
    label: t("relocate.apply"), primary: true, disabled: true,
    onClick: async () => {
      try { await applyWithUndo(body(), reload); } catch (err) { toast(err.message, "err"); return false; }
      return true;
    },
  };
  actions.push(applyAction);
  for (const input of [fromInput, toInput]) input.addEventListener("input", () => { if (applyAction._btn) applyAction._btn.disabled = true; });
  all.addEventListener("change", () => { if (applyAction._btn) applyAction._btn.disabled = true; });
  openModal({
    title: device ? t("relocate.title_device", { device: device.label || device.id }) : t("relocate.title"),
    wide: true,
    body: h("div", null,
      h("p", { class: "muted", style: { marginTop: "0" } }, t("relocate.lead")),
      h("div", { class: "field" }, h("label", null, t("relocate.from")), fromInput),
      h("div", { class: "field" }, h("label", null, t("relocate.to")), toInput),
      h("label", { class: "row tight" }, all, t("relocate.all_devices")),
      out),
    actions,
  });
}

/** Absolute paths inside the task space -> task: links (whole project). */
export function openLogicalDialog({ reload = null } = {}) {
  const out = h("div", { class: "relocate-out" }, h("p", { class: "muted" }, t("relocate.loading")));
  const applyAction = {
    label: t("relocate.apply"), primary: true, disabled: true,
    onClick: async () => {
      try { await applyWithUndo({ logical: true }, reload); } catch (err) { toast(err.message, "err"); return false; }
      return true;
    },
  };
  openModal({
    title: t("relocate.logical_title"), wide: true,
    body: h("div", null, h("p", { class: "muted", style: { marginTop: "0" } }, t("relocate.logical_lead")), out),
    actions: [{ label: t("action.cancel"), ghost: true }, applyAction],
  });
  api.setupAction("relocate", { logical: true }).then((data) => {
    out.replaceChildren(previewView(data));
    if (applyAction._btn) applyAction._btn.disabled = !(data.totals?.paths || 0);
  }).catch((err) => out.replaceChildren(h("p", { class: "bad" }, err.message)));
}
