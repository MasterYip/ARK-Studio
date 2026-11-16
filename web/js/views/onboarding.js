// Home "Start with an agent" panel: environment-setup status plus copy-ready
// prompts a new user pastes into DSH, Codex or Claude Code.  ARK keeps the
// record; the external agent does the work through the agent API.
import { h, clear } from "../dom.js";
import { t, getLang } from "../i18n.js";
import { icon } from "../icons.js";
import { api, appUrl } from "../api.js";
import { copyPromptWithToken } from "./agent-tokens.js";
import { openModal } from "../components.js";
import { openSetupReview } from "./setup-resources.js";
import { newArkSessionId } from "../session-context-model.js";
import { STARTER_PROMPTS, promptLang, starterPromptText } from "../starter-prompts.js";
import { needsReview } from "../setup-review-model.js";

const HIDE_KEY = "ark.onboarding.hidden";

// The prompt texts (English and Chinese) live in ../starter-prompts.js and
// follow the interface language.
function arkBase() {
  return appUrl("").replace(/\/+$/, "");
}

export function promptText(id, project, { token = "", sessionId = "", lang = promptLang(getLang()) } = {}) {
  return starterPromptText(id, project, { url: arkBase(), token, sessionId, lang });
}

const STATE_TONE = { unset: "warn", draft: "warn", proposed: "info", stale: "warn", confirmed: "ok", verified: "ok" };

// One preview dialog at a time: a second Preview click while it is open (or
// opening) only switches its prompt; nothing stacks.  Preview never mints a
// token; only Copy does, after the token confirmation.
let previewDialog = null;

export function openPromptPreview(id, project, { onCopy } = {}) {
  const item = STARTER_PROMPTS.find((p) => p.id === id);
  if (!item) return null;
  const sessionId = newArkSessionId();
  const text = promptText(id, project, { sessionId });
  if (previewDialog?.isOpen()) {
    previewDialog.show(id, text, sessionId);
    return previewDialog;
  }
  const title = h("span");
  const session = h("code");
  const pre = h("pre", { class: "start-preview", "data-start-preview-text": "" });
  let current = id;
  const dialog = openModal({
    title: "",
    wide: true,
    body: h("div", { class: "start-preview-body" },
      h("p", { class: "muted start-preview-hint" }, t("start.preview_hint")),
      h("div", { class: "start-session muted" }, icon("layers", "sm"), h("span", null, t("start.session_id")), session),
      pre),
    actions: [
      { spacer: true },
      { label: t("action.close"), ghost: true },
      { label: t("start.copy"), primary: true, onClick: () => { onCopy?.(current); } },
    ],
    onClose: () => { if (previewDialog === handle) previewDialog = null; },
  });
  dialog.modal.classList.add("start-preview-dialog");
  dialog.modal.dataset.startPreviewDialog = "";
  dialog.modal.querySelector(".modal-head h3")?.replaceChildren(title);
  const handle = {
    isOpen: () => dialog.backdrop.isConnected,
    close: () => dialog.close(false),
    show(nextId, nextText, nextSession) {
      current = nextId;
      dialog.modal.dataset.prompt = nextId;
      title.textContent = t(`start.${nextId}.title`);
      session.textContent = nextSession;
      pre.textContent = nextText; // plain text: no markdown, no re-render
      pre.scrollTop = 0;
    },
  };
  handle.show(id, text, sessionId);
  previewDialog = handle;
  return handle;
}

export function startPanel(ctx) {
  const project = ctx.S.project || {};
  // The user's own choice wins; otherwise a project that already has work
  // starts with the prompts folded to one row (Home leads with its state).
  const stored = localStorage.getItem(HIDE_KEY);
  const hidden = stored === "1" || (stored === null && (ctx.S.stats?.total || 0) >= 20);
  const panel = h("section", { class: `panel start-panel ${hidden ? "collapsed" : ""}`.trim() });
  const status = h("div", { class: "start-status" });
  let setupState = null;

  const toggle = () => {
    const now = !panel.classList.contains("collapsed");
    panel.classList.toggle("collapsed", now);
    localStorage.setItem(HIDE_KEY, now ? "1" : "0");
    toggleBtn.textContent = now ? t("start.show") : t("start.hide");
  };
  const toggleBtn = h("button", { class: "link", "data-start-toggle": "", onclick: toggle }, hidden ? t("start.show") : t("start.hide"));

  // One level: a title with one line of context, one setup element, and two
  // cards (title, one line, Copy, Preview).  Details live in the preview.
  panel.append(
    h("div", { class: "panel-head" },
      h("div", { class: "start-title" },
        h("h3", null, t("start.title")),
        h("span", { class: "muted start-sub" }, t("start.subtitle"))),
      h("div", { class: "row start-head-actions" }, status, toggleBtn)),
  );

  const copy = (id) => copyPromptWithToken((token) => promptText(id, project, { token, sessionId: newArkSessionId() }));
  const preview = (id) => openPromptPreview(id, project, { onCopy: (current) => copy(current) });
  const cards = h("div", { class: "start-grid start-grid-two" }, ...STARTER_PROMPTS.map((item) =>
    h("div", { class: `start-card start-card-${item.id}`, "data-prompt": item.id },
      h("div", { class: "start-card-head" },
        h("span", { class: "start-ic" }, icon(item.icon, "sm")),
        h("b", null, t(`start.${item.id}.title`))),
      h("p", { class: "start-desc" }, t(`start.${item.id}.desc`)),
      h("div", { class: "start-actions" },
        h("button", { class: "btn sm primary", type: "button", "data-copy-token": item.id, onclick: () => copy(item.id) },
          icon("copy", "sm"), t("start.copy")),
        h("button", { class: "btn sm ghost", type: "button", "data-start-preview": item.id, onclick: () => preview(item.id) },
          t("start.preview"))))));
  panel.append(cards);

  const renderStatus = () => {
    clear(status);
    if (!setupState) return;
    // One element: "Review & confirm" while a layer waits for this user,
    // otherwise the setup state (docs/operations/multi-user-setup.md).
    const chip = setupChip(setupState);
    const review = needsReview(setupState);
    status.appendChild(h("button", {
      class: review ? "btn sm primary start-setup" : `start-chip start-setup ${chip.tone}`.trim(),
      type: "button", "data-setup-chip": chip.id, ...(review ? { "data-setup-review": "" } : {}),
      title: review ? chip.text : t("start.chip_title", { host: setupState.host?.label || "" }),
      onclick: () => {
        if (review) { openSetupReview(setupState, "", load); return; }
        try { localStorage.setItem("ark.settingsTab", "setupResources"); localStorage.setItem("ark.settingsScope", "project"); } catch { /* ignore */ }
        ctx.navigate?.("settings");
      },
    }, review ? t("start.review") : chip.text));
    // Once setup is done the prompts fold to one row unless the user chose
    // to show them; the dashboard comes first.  "Show" keeps them reachable.
    if (setupState.readiness?.ready && localStorage.getItem(HIDE_KEY) === null && !panel.classList.contains("collapsed")) {
      panel.classList.add("collapsed");
      toggleBtn.textContent = t("start.show");
    }
  };
  const load = async () => {
    try {
      setupState = await api.setupState();
    } catch {
      setupState = null; // older server or no permission: prompts still work
    }
    renderStatus();
  };
  load();
  return panel;
}

export function setupChip(state) {
  const project = state.layers?.project?.state || state.state || "unset";
  if (project !== "confirmed" && project !== "verified") {
    const admin = state.layers?.project?.can_confirm !== false;
    const key = project === "proposed" && !admin ? "proposed_admin" : project;
    return { id: `project-${project}`, tone: STATE_TONE[project] || "", text: t(`start.state.${key}`) };
  }
  const mine = state.layers?.user?.state;
  if (mine === "draft" || mine === "proposed" || mine === "stale") {
    return { id: `mine-${mine}`, tone: "warn", text: t(`start.mine.${mine}`) };
  }
  const missing = (state.readiness?.needs_location || []).length;
  if (missing) return { id: "needs-location", tone: "warn", text: t("start.needs_location", { count: missing }) };
  return { id: "ready-here", tone: "ok", text: t("start.ready_here") };
}
