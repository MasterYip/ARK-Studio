// Settings > Project storage location (admin scope): where new projects are
// created.  Existing projects are never moved; the folders used before stay
// listed.  In the desktop app a native folder picker fills the path; in a
// browser the path is typed and checked by the server.  "Show in folder"
// exists when ARK runs on this computer (local mode).

import { h, clear, copyText } from "./dom.js";
import { t, hasKey } from "./i18n.js";
import { icon } from "./icons.js";
import { api } from "./api.js";
import { confirmDialog } from "./components.js";
import { desktopHub } from "./desktop-hub-model.js";
import { storageView, checkSummary, pathInputError, examplePath, canPickFolder } from "./project-storage-model.js";

/** A server error in the page language when its code is known. */
function errorText(err) {
  const code = String(err?.code || "");
  if (code && hasKey(`storage.code.${code}`)) return t(`storage.code.${code}`);
  return err?.message || String(err);
}

function pathCode(path) {
  return h("code", { class: "storage-path", title: path }, path);
}

export function projectStoragePanel(ctx) {
  const hub = desktopHub();
  const body = h("div", { class: "stack", style: { gap: "10px" } }, h("div", { class: "muted" }, t("action.loading")));
  const section = h("section", { class: "panel settings-panel", "data-project-storage": "" },
    h("div", { class: "panel-head" }, h("h3", null, t("storage.title"))),
    h("p", { class: "settings-lead" }, t("storage.lead")),
    body);

  const reveal = async (payload) => {
    try { await api.revealProject(payload); } catch (err) { ctx.toast(errorText(err), "err"); }
  };

  function render(storage) {
    const view = storageView(storage);
    clear(body);
    // Where new projects go now.
    const actions = h("div", { class: "row tight", style: { gap: "6px", flexWrap: "wrap" } });
    if (view.canReveal) {
      actions.append(h("button", { class: "btn sm", type: "button", "data-storage-reveal": "current",
        onclick: () => reveal({ path: view.current }) }, icon("folder", "sm"), t("storage.reveal")));
    }
    actions.append(h("button", { class: "btn sm ghost", type: "button",
      onclick: () => copyText(view.current).then(() => ctx.toast(t("storage.copied"), "ok")).catch(() => {}) },
    icon("copy", "sm"), t("action.copy")));
    body.append(h("div", { class: "field" },
      h("label", null, t("storage.current")),
      h("div", { class: "row tight", style: { gap: "8px", alignItems: "center", flexWrap: "wrap" } },
        pathCode(view.current),
        h("span", { class: `pill ${view.usesBuiltin ? "" : "ok"}`.trim() },
          t(view.usesBuiltin ? "storage.badge_default" : "storage.badge_custom")),
        view.freeText ? h("span", { class: `muted${view.lowSpace ? " warn-text" : ""}`, style: { fontSize: "11px" } },
          t(view.lowSpace ? "storage.free_low" : "storage.free", { size: view.freeText })) : null),
      actions));
    if (view.fallback) {
      body.append(h("div", { class: "settings-note warn", "data-storage-fallback": "" }, icon("alert", "sm"),
        h("div", null, t("storage.fallback", { configured: view.configured, builtin: view.builtin }))));
    }

    // Change it.
    const input = h("input", { class: "input", "data-storage-input": "", spellcheck: "false",
      placeholder: examplePath(view.platform), value: view.configured || "" });
    const result = h("div", { class: "muted", style: { fontSize: "11px", lineHeight: "1.6" }, "data-storage-check": "" });
    const save = h("button", { class: "btn primary sm", type: "button", "data-storage-save": "", disabled: true },
      t("storage.use"));
    let checked = null;
    const setResult = (text, bad = false) => {
      result.textContent = text;
      result.classList.toggle("err-text", bad);
    };
    async function check() {
      checked = null;
      save.disabled = true;
      const quick = pathInputError(input.value, view.platform);
      if (quick) { setResult(t(quick), true); return; }
      setResult(t("storage.checking"));
      try {
        const data = await api.checkProjectStorage(input.value.trim());
        const c = data.check || {};
        if (c.current) { setResult(t("storage.check.already")); return; }
        checked = c;
        setResult(checkSummary(c).map(([key, vars]) => t(key, vars)).join(" "));
        save.disabled = false;
      } catch (err) { setResult(errorText(err), true); }
    }
    input.addEventListener("input", () => { checked = null; save.disabled = true; setResult(""); });
    input.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); check(); } });
    save.addEventListener("click", async () => {
      if (!checked) return;
      const ok = await confirmDialog(t("storage.confirm", { path: checked.path }), {
        title: t("storage.title"), confirmLabel: t("storage.use") });
      if (!ok) return;
      try {
        const data = await api.saveProjectStorage({ default: checked.path });
        ctx.toast(t("storage.saved"), "ok");
        render(data.storage);
      } catch (err) { setResult(errorText(err), true); }
    });
    const pick = canPickFolder(hub) ? h("button", { class: "btn sm", type: "button", "data-storage-pick": "",
      onclick: async () => {
        try {
          const chosen = await hub.pickFolder(input.value.trim() || view.current);
          if (chosen) { input.value = chosen; check(); }
        } catch (err) { ctx.toast(errorText(err), "err"); }
      } }, icon("folder", "sm"), t("storage.pick")) : null;
    const reset = view.configured ? h("button", { class: "btn sm ghost", type: "button", "data-storage-reset": "",
      onclick: async () => {
        const ok = await confirmDialog(t("storage.reset_confirm", { path: view.builtin }), {
          title: t("storage.title"), confirmLabel: t("storage.reset") });
        if (!ok) return;
        try {
          const data = await api.saveProjectStorage({ default: "" });
          ctx.toast(t("storage.saved"), "ok");
          render(data.storage);
        } catch (err) { ctx.toast(errorText(err), "err"); }
      } }, t("storage.reset")) : null;
    body.append(h("div", { class: "field" },
      h("label", null, t("storage.change")),
      h("div", { class: "row", style: { flexWrap: "nowrap", gap: "6px" } }, input, pick,
        h("button", { class: "btn sm", type: "button", "data-storage-check-btn": "", onclick: check }, t("storage.check"))),
      result,
      h("div", { class: "row tight", style: { gap: "6px", marginTop: "6px" } }, save, reset)));
    body.append(h("div", { class: "settings-note" }, icon("info", "sm"),
      h("div", null, t("storage.existing_note"))));

    // Folders with projects.
    if (view.folders.length) {
      const list = h("div", { class: "storage-folders", "data-storage-folders": "" });
      for (const f of view.folders) {
        const tags = [];
        if (f.builtin) tags.push(t("storage.folder_builtin"));
        if (f.isDefault) tags.push(t("storage.folder_new"));
        const row = h("div", { class: "storage-folder", "data-storage-folder": f.path },
          h("div", { class: "storage-folder-main" },
            pathCode(f.path),
            h("div", { class: "muted", style: { fontSize: "11px" } },
              [t("storage.folder_count", { n: f.projects }), ...tags,
                ...(f.exists ? [] : [t("storage.folder_missing")])].join(" · "))),
          h("div", { class: "row tight", style: { gap: "4px" } },
            view.canReveal && f.exists ? h("button", { class: "btn sm ghost", type: "button", title: t("storage.reveal"),
              onclick: () => reveal({ path: f.path }) }, icon("folder", "sm"), t("storage.reveal")) : null,
            f.removable ? h("button", { class: "btn sm ghost", type: "button", "data-storage-remove": f.path,
              onclick: async () => {
                const ok = await confirmDialog(t("storage.remove_confirm", { path: f.path }), {
                  title: t("storage.remove"), confirmLabel: t("storage.remove") });
                if (!ok) return;
                try { render((await api.saveProjectStorage({ remove: f.path })).storage); } catch (err) { ctx.toast(errorText(err), "err"); }
              } }, t("storage.remove")) : null));
        list.append(row);
      }
      body.append(h("div", { class: "settings-sub" }, t("storage.folders")), list);
    }
  }

  api.projectStorage().then((data) => render(data.storage)).catch((err) => {
    clear(body);
    body.append(h("div", { class: "muted" }, errorText(err)));
  });
  return section;
}
