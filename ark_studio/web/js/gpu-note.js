import { h } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";

/**
 * Open Settings -> Resources at the Setup / Resources section, where a member
 * edits their own resources (the same route as Home's setup chip).
 */
export function openSetupResources(ctx) {
  try {
    localStorage.setItem("ark.settingsTab", "setupResources");
    localStorage.setItem("ark.settingsScope", "project");
  } catch { /* storage unavailable */ }
  if (ctx?.navigateWorkspace) ctx.navigateWorkspace("settings");
  else ctx?.navigate?.("settings");
}

/**
 * T-0018/T-0026: the GPU views' "not registered in this project" note.  The
 * server shows a project only the GPU servers its setup registers;
 * `registration` is the server's {registered, available, suggested, hint_code}.
 * GPU servers are registered in Setup, so the note links there; it never
 * lists the server names.  Returns null when the project registers a server.
 */
export function gpuRegistrationNote(registration, onOpenSetup = null) {
  if (!registration || registration.hint_code !== "not_registered") return null;
  const open = onOpenSetup ? h("button", {
    class: "ghost-btn sm gpu-register-open", type: "button", "data-gpu-open-setup": "",
    onclick: () => onOpenSetup(),
  }, icon("settings", "sm"), h("span", null, t("gpu.register.open_setup"))) : null;
  return h("div", { class: "settings-note gpu-register-note", "data-gpu-register-note": "" }, icon("info", "sm"),
    h("div", { class: "gpu-register-body" },
      h("b", null, t("gpu.register.title")), " ", t("gpu.register.hint"),
      open ? h("div", { class: "gpu-register-actions" }, open) : null));
}
