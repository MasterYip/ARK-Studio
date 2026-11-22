// Home → More prompts: the prompt library dialog (built-in and custom
// prompts, Copy, Preview, pins and their order) and the custom prompt
// editor.  Text building is in ../starter-prompts.js, list and pin logic in
// ../prompt-library-model.js; onboarding.js owns the library state.
import { h, clear, download } from "../dom.js";
import { t, getLang } from "../i18n.js";
import { icon } from "../icons.js";
import { openModal, confirmDialog, toast } from "../components.js";
import { promptLang } from "../starter-prompts.js";
import {
  libraryEntries, entryTitle, entryDesc, isPinned, togglePin, movePin, effectivePins, MAX_PINS,
  exportPrompts, parseImport, unknownPlaceholders,
} from "../prompt-library-model.js";

/**
 * lib: {state, pins, save(body) → Promise<state>, setPins(list), copy(entry, unit), preview(entry, unit),
 *       projectId}
 */
export function openPromptLibrary(lib) {
  const lang = promptLang(getLang());
  const units = new Map(); // entry id -> unit typed in the row
  const list = h("div", { class: "plib-list", "data-prompt-library": "" });
  const render = () => {
    clear(list);
    const entries = libraryEntries(lib.state);
    const pins = effectivePins(lib.pins);
    const groups = [
      ["builtin", entries.filter((e) => e.builtin)],
      ["personal", entries.filter((e) => e.scope === "personal")],
      ["project", entries.filter((e) => e.scope === "project")],
    ];
    for (const [group, items] of groups) {
      list.appendChild(h("h4", { class: "plib-group" }, t(`prompts.group.${group}`)));
      if (!items.length) {
        list.appendChild(h("p", { class: "muted plib-empty" }, t(group === "project" ? "prompts.empty_project" : "prompts.empty_personal")));
        continue;
      }
      for (const entry of items) list.appendChild(row(entry, pins));
    }
  };
  const row = (entry, pins) => {
    const pinned = pins.includes(entry.id);
    const at = pins.indexOf(entry.id);
    const canEdit = entry.custom && (entry.scope === "personal" || lib.state?.can_edit_project);
    const unitInput = entry.unit ? h("input", {
      class: "plib-unit", type: "text", placeholder: t("prompts.unit"), value: units.get(entry.id) || "",
      "aria-label": t("prompts.unit"), oninput: (e) => units.set(entry.id, e.target.value.trim()),
    }) : null;
    const unit = () => units.get(entry.id) || "";
    const setPins = (next) => { lib.setPins(next); render(); };
    return h("div", { class: `plib-row ${pinned ? "pinned" : ""}`.trim(), "data-library-prompt": entry.id },
      h("span", { class: "start-ic" }, icon(entry.icon || "pen", "sm")),
      h("div", { class: "plib-text" },
        h("b", null, entryTitle(entry, lang),
          entry.custom ? h("span", { class: "plib-scope" }, t(`prompts.scope.${entry.scope}`)) : null),
        h("span", { class: "muted" }, entryDesc(entry, lang) || t("prompts.no_desc")),
        unitInput),
      h("div", { class: "plib-actions" },
        h("button", { class: "btn sm primary", type: "button", "data-library-copy": entry.id, onclick: () => lib.copy(entry, unit()) },
          icon("copy", "sm"), t("start.copy")),
        h("button", { class: "btn sm ghost", type: "button", "data-library-preview": entry.id, onclick: () => lib.preview(entry, unit()) },
          t("start.preview")),
        h("button", {
          class: `btn sm ghost plib-pin ${pinned ? "on" : ""}`.trim(), type: "button", "data-library-pin": entry.id,
          "aria-pressed": pinned ? "true" : "false", title: t(pinned ? "prompts.unpin" : "prompts.pin"),
          disabled: !pinned && pins.length >= MAX_PINS,
          onclick: () => setPins(togglePin(lib.pins, entry.id)),
        }, icon("pin", "sm"), t(pinned ? "prompts.pinned" : "prompts.pin")),
        pinned ? h("button", { class: "icon-btn", type: "button", title: t("prompts.up"), "data-library-up": entry.id, disabled: at === 0,
          onclick: () => setPins(movePin(lib.pins, entry.id, -1)) }, icon("arrowUp", "sm")) : null,
        pinned ? h("button", { class: "icon-btn", type: "button", title: t("prompts.down"), "data-library-down": entry.id,
          disabled: at === pins.length - 1, onclick: () => setPins(movePin(lib.pins, entry.id, 1)) }, icon("arrowDown", "sm")) : null,
        canEdit ? h("button", { class: "icon-btn", type: "button", title: t("action.edit"), "data-library-edit": entry.id,
          onclick: () => openPromptEditor(lib, entry) }, icon("edit", "sm")) : null,
        canEdit ? h("button", { class: "icon-btn", type: "button", title: t("action.delete"), "data-library-delete": entry.id,
          onclick: async () => {
            if (!(await confirmDialog(t("prompts.delete_confirm", { title: entry.title }), { danger: true, confirmLabel: t("action.delete") }))) {
              openPromptLibrary(lib);
              return;
            }
            await saveOrToast(lib, { action: "delete", scope: entry.scope, id: entry.id }, t("prompts.deleted"));
            openPromptLibrary(lib);
          } }, icon("trash", "sm")) : null));
  };
  const file = h("input", { type: "file", accept: "application/json,.json", class: "plib-file", "data-library-import-file": "",
    onchange: async (e) => {
      const picked = e.target.files?.[0];
      e.target.value = "";
      if (!picked) return;
      let prompts;
      try { prompts = parseImport(await picked.text()); } catch (err) {
        toast(t("prompts.import_bad", { reason: err.message }), "err");
        return;
      }
      const reply = await saveOrToast(lib, { action: "import", scope: "personal", prompts }, null);
      if (reply) {
        toast(t("prompts.imported", { count: prompts.length }), "ok");
        render();
      }
    } });
  const exportAll = () => {
    const mine = lib.state?.personal || [];
    const theirs = lib.state?.project || [];
    if (!mine.length && !theirs.length) { toast(t("prompts.export_empty")); return; }
    download(`ark-prompts-${lib.projectId || "project"}.json`, exportPrompts([...mine, ...theirs], { project: lib.projectId }),
      "application/json");
  };
  const body = h("div", { class: "plib-body" },
    h("p", { class: "muted plib-hint" }, t(lib.offline ? "prompts.offline" : "prompts.hint")),
    list, file);
  const dialog = openModal({
    title: t("prompts.title"),
    wide: true,
    body,
    actions: [
      { label: t("prompts.add"), primary: true, disabled: lib.offline, close: false, onClick: () => { openPromptEditor(lib, null); } },
      { label: t("prompts.import"), ghost: true, disabled: lib.offline, close: false, onClick: () => { file.click(); } },
      { label: t("prompts.export"), ghost: true, close: false, onClick: exportAll },
      { label: t("prompts.reset_pins"), ghost: true, close: false, onClick: () => { lib.setPins(null); render(); } },
      { spacer: true },
      { label: t("action.close"), ghost: true },
    ],
  });
  dialog.modal.classList.add("plib-dialog");
  dialog.modal.dataset.promptLibraryDialog = "";
  lib.rerender = render;
  render();
  return dialog;
}

async function saveOrToast(lib, body, okMessage) {
  try {
    const reply = await lib.save(body);
    for (const warning of reply?.warnings || []) if (!/added,/.test(warning)) toast(warning);
    if (okMessage) toast(okMessage, "ok");
    return reply;
  } catch (err) {
    toast(t("prompts.save_failed", { reason: err?.message || "" }), "err");
    return null;
  }
}

/** Add (entry null) or edit a custom prompt; back to the library after. */
export function openPromptEditor(lib, entry) {
  const title = h("input", { type: "text", maxlength: 80, value: entry?.title || "", "data-prompt-field": "title" });
  const desc = h("input", { type: "text", maxlength: 200, value: entry?.desc || "", "data-prompt-field": "description" });
  const text = h("textarea", { rows: 12, maxlength: 8000, "data-prompt-field": "body", placeholder: t("prompts.body_placeholder") });
  text.value = entry?.body || "";
  const canProject = Boolean(lib.state?.can_edit_project);
  const scope = h("select", { "data-prompt-field": "scope", disabled: Boolean(entry) },
    h("option", { value: "personal", selected: (entry?.scope || "personal") === "personal" }, t("prompts.scope.personal")),
    canProject || entry?.scope === "project"
      ? h("option", { value: "project", selected: entry?.scope === "project" }, t("prompts.scope.project")) : null);
  const warn = h("p", { class: "muted plib-warn", "data-prompt-warn": "" });
  const check = () => {
    const unknown = unknownPlaceholders(text.value);
    warn.textContent = unknown.length ? t("prompts.unknown_placeholders", { names: unknown.map((n) => `{${n}}`).join(", ") }) : "";
  };
  text.addEventListener("input", check);
  check();
  const field = (label, input, hint = "") => h("label", { class: "plib-field" }, h("span", null, label), input,
    hint ? h("span", { class: "muted" }, hint) : null);
  openModal({
    title: t(entry ? "prompts.edit_title" : "prompts.add_title"),
    wide: true,
    body: h("div", { class: "plib-editor", "data-prompt-editor": "" },
      field(t("prompts.field.title"), title),
      field(t("prompts.field.description"), desc),
      field(t("prompts.field.body"), text, t("prompts.placeholders_hint")),
      warn,
      field(t("prompts.field.scope"), scope, canProject ? t("prompts.scope_hint_admin") : t("prompts.scope_hint"))),
    actions: [
      { label: t("action.cancel"), ghost: true, onClick: () => { setTimeout(() => openPromptLibrary(lib), 0); } },
      { spacer: true },
      {
        label: t("action.save"), primary: true, close: false,
        onClick: async (_e, close) => {
          if (!title.value.trim() || !text.value.trim()) { toast(t("prompts.need_title_body"), "err"); return false; }
          const reply = await saveOrToast(lib, {
            action: "save", scope: entry?.scope || scope.value,
            prompt: { id: entry?.id || undefined, title: title.value, description: desc.value, body: text.value },
          }, t("prompts.saved"));
          if (!reply) return false;
          close(true);
          openPromptLibrary(lib);
          return true;
        },
      },
    ],
  });
  title.focus();
}
