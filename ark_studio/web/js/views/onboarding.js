// Home "Start with an agent" panel: environment-setup status plus copy-ready
// prompts a new user pastes into DSH, Codex or Claude Code.  ARK keeps the
// record; the external agent does the work through the agent API.
import { h, clear } from "../dom.js";
import { t, getLang } from "../i18n.js";
import { icon } from "../icons.js";
import { api, appUrl } from "../api.js";
import { copyPromptWithToken } from "./agent-tokens.js";
import { openModal, toast } from "../components.js";
import { openSetupReview } from "./setup-resources.js";
import { newArkSessionId } from "../session-context-model.js";
import { BUILTIN_PROMPTS, promptLang, libraryPromptText } from "../starter-prompts.js";
import { libraryEntries, pinnedEntries, entryTitle, entryDesc, prunePins } from "../prompt-library-model.js";
import { openPromptLibrary } from "./prompt-library.js";
import { needsReview, needsConfirm } from "../setup-review-model.js";
import { connectionStatus } from "./agent-connect.js";
import { agentMessageButton } from "../agent-messages.js";

const HIDE_KEY = "ark.onboarding.hidden";

// The prompt texts (English and Chinese) live in ../starter-prompts.js and
// follow the interface language.
function arkBase() {
  return appUrl("").replace(/\/+$/, "");
}

// The project's and this person's composed agent rules (GET /api/agent-rules),
// fetched when the Home panel renders so that Copy stays synchronous inside
// the click.  Until they arrive (or on an older server) the prompt carries
// ARK's default rules.
let composedRules = { project: "", rules: null };
export function loadComposedRules(projectId = "") {
  return api.agentRules()
    .then((reply) => { composedRules = { project: projectId, rules: reply?.effective || null }; return composedRules.rules; })
    .catch(() => null);
}
export function setComposedRules(projectId, rules) { composedRules = { project: projectId, rules }; }

/** A library entry's text (built-in or custom) with the composed rules. */
export function entryPromptText(entry, project, { token = "", sessionId = "", unit = "", user = "", lang = promptLang(getLang()) } = {}) {
  const rules = composedRules.project === (project?.id || "") ? composedRules.rules : null;
  return libraryPromptText(entry, project, { url: arkBase(), token, sessionId, lang, rules, unit, user });
}

export function promptText(id, project, options = {}) {
  return entryPromptText(BUILTIN_PROMPTS.find((p) => p.id === id), project, options);
}

// The prompt library (Home → More prompts): this person's and the project's
// custom prompts and the Home pins (GET /api/prompts).  Pins are kept per
// person and project on the server; an older server keeps them in this
// browser.
const PINS_KEY = "ark.prompts.pins";
function localPins(projectId) {
  try {
    const raw = localStorage.getItem(`${PINS_KEY}:${projectId || "local"}`);
    return raw ? JSON.parse(raw) : null;
  } catch { return null; }
}
function storeLocalPins(projectId, pins) {
  try {
    if (pins === null) localStorage.removeItem(`${PINS_KEY}:${projectId || "local"}`);
    else localStorage.setItem(`${PINS_KEY}:${projectId || "local"}`, JSON.stringify(pins));
  } catch { /* storage unavailable */ }
}

// Setup chips are neutral: "not set up yet" or "not applicable" is not a
// warning (UX-09); only "waiting for you" stands out (info) and done is ok.
const STATE_TONE = { unset: "", draft: "", proposed: "info", stale: "info", confirmed: "ok", verified: "ok" };

// One setup-state request per Home render: the start panel and the Needs
// you row share it (both are built in the same pass).
let setupStateRequest = null;
export function loadSetupState() {
  if (!setupStateRequest) {
    setupStateRequest = api.setupState().finally(() => { setTimeout(() => { setupStateRequest = null; }, 0); });
  }
  return setupStateRequest;
}

// One preview dialog at a time: a second Preview click while it is open (or
// opening) only switches its prompt; nothing stacks.  Preview never mints a
// token; only Copy does, after the token confirmation.
let previewDialog = null;

export function openPromptPreview(idOrEntry, project, { onCopy, unit = "", user = "" } = {}) {
  const item = typeof idOrEntry === "string" ? BUILTIN_PROMPTS.find((p) => p.id === idOrEntry) : idOrEntry;
  if (!item) return null;
  const id = item.id;
  const sessionId = newArkSessionId();
  const text = entryPromptText(item, project, { sessionId, unit, user });
  const label = entryTitle(item, promptLang(getLang()));
  if (previewDialog?.isOpen()) {
    previewDialog.show(id, text, sessionId, label);
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
    show(nextId, nextText, nextSession, nextTitle) {
      current = nextId;
      dialog.modal.dataset.prompt = nextId;
      title.textContent = nextTitle;
      session.textContent = nextSession;
      pre.textContent = nextText; // plain text: no markdown, no re-render
      pre.scrollTop = 0;
    },
  };
  handle.show(id, text, sessionId, label);
  previewDialog = handle;
  return handle;
}

export function startPanel(ctx) {
  const project = ctx.S.project || {};
  loadComposedRules(project.id || "");
  // "Connect your agent" (UI line A, UX-03): open until an agent first wrote
  // to this project, then folded to one row with the connection state.  The
  // person's own Show/Hide for this project wins.  On an example project it
  // stays open ("try your agent on this example").
  const hideKey = `${HIDE_KEY}:${project.id || "local"}`;
  let stored = null;
  try { stored = localStorage.getItem(hideKey); } catch { /* storage unavailable */ }
  const hidden = stored === "1" || (stored === null && !project.demo && Boolean(ctx.S.overview?.agents?.first_at));
  const panel = h("section", { class: `panel start-panel ${hidden ? "collapsed" : ""}`.trim() });
  const status = h("div", { class: "start-status" });
  const connection = connectionStatus(ctx);
  let setupState = null;

  const toggle = () => {
    const now = !panel.classList.contains("collapsed");
    panel.classList.toggle("collapsed", now);
    try { localStorage.setItem(hideKey, now ? "1" : "0"); } catch { /* storage unavailable */ }
    toggleBtn.textContent = now ? t("start.show") : t("start.hide");
  };
  const toggleBtn = h("button", { class: "link", "data-start-toggle": "", onclick: toggle }, hidden ? t("start.show") : t("start.hide"));

  // One level: a title with one line of context, one setup element, and two
  // cards (title, one line, Copy, Preview).  Details live in the preview.
  panel.append(
    h("div", { class: "panel-head" },
      h("div", { class: "start-title" },
        h("h3", null, t(project.demo ? "home.connect.demo_title" : "home.connect.title")),
        connection.el,
        h("span", { class: "muted start-sub" }, t(project.id === "ark-demo" ? "home.connect.tutorial_sub"
          : project.demo ? "home.connect.demo_sub" : "start.subtitle"))),
      h("div", { class: "row start-head-actions" }, status, agentMessageButton(ctx), toggleBtn)),
  );

  // A stored starter token copies inside the click (agent-tokens.js); the
  // connection line starts waiting only after a copy, and only if it is not
  // waiting already (connectionStatus refresh()).
  const user = ctx.S.session?.user || "local";
  const lang = promptLang(getLang());
  const copyEntry = (entry, unit = "") => copyPromptWithToken(
    (token) => entryPromptText(entry, project, { token, sessionId: newArkSessionId(), unit, user }),
    { projectId: project.id || "", user: ctx.S.session?.user || "local" })
    .then((result) => { if (result?.copied) connection.refresh(); });
  const previewEntry = (entry, unit = "") => openPromptPreview(entry, project,
    { unit, user, onCopy: () => copyEntry(entry, unit) });

  // Home shows the pinned prompts (by default the two starter prompts) and
  // "More prompts" opens the library.
  const lib = {
    projectId: project.id || "",
    state: null,
    pins: localPins(project.id),
    offline: false,
    copy: copyEntry,
    preview: previewEntry,
    async save(body) {
      const reply = await api.promptsPost(body);
      lib.state = reply;
      lib.pins = reply.pins ?? null;
      if (body.action === "delete") {
        const pruned = prunePins(lib.pins, libraryEntries(reply));
        if (pruned && pruned.length !== lib.pins.length) lib.setPins(pruned);
      }
      renderCards();
      return reply;
    },
    setPins(pins) {
      lib.pins = pins;
      renderCards();
      if (lib.offline) { storeLocalPins(project.id, pins); return; }
      api.promptsPost({ action: "pins", pins }).then((reply) => {
        lib.state = reply;
      }).catch(() => {
        storeLocalPins(project.id, pins);
        toast(t("prompts.pins_local"));
      });
    },
  };
  const cards = h("div", { class: "start-grid start-grid-two" });
  const renderCards = () => {
    clear(cards);
    for (const item of pinnedEntries(lib.pins, libraryEntries(lib.state || {}))) {
      cards.appendChild(h("div", { class: `start-card start-card-${item.id}`, "data-prompt": item.id },
        h("div", { class: "start-card-head" },
          h("span", { class: "start-ic" }, icon(item.icon || "pen", "sm")),
          h("b", null, entryTitle(item, lang))),
        h("p", { class: "start-desc" }, entryDesc(item, lang)),
        h("div", { class: "start-actions" },
          h("button", { class: "btn sm primary", type: "button", "data-copy-token": item.id, onclick: () => copyEntry(item) },
            icon("copy", "sm"), t("start.copy")),
          h("button", { class: "btn sm ghost", type: "button", "data-start-preview": item.id, onclick: () => previewEntry(item) },
            t("start.preview")))));
    }
    if (!cards.childElementCount) cards.appendChild(h("p", { class: "muted start-none" }, t("prompts.none_pinned")));
  };
  renderCards();
  const more = h("div", { class: "start-more" },
    h("button", { class: "btn sm ghost", type: "button", "data-more-prompts": "", onclick: () => openPromptLibrary(lib) },
      icon("book", "sm"), t("prompts.more")));
  panel.append(cards, more);
  api.prompts().then((reply) => {
    lib.state = reply;
    lib.pins = reply?.pins ?? null;
    renderCards();
    lib.rerender?.();
  }).catch(() => { lib.offline = true; });

  const renderStatus = () => {
    clear(status);
    if (!setupState) return;
    // One element: "Review & confirm" while a layer waits for this user's
    // confirmation; otherwise the setup state as a neutral chip, which opens
    // the review for an admin's draft (it explains what to ask the agent)
    // and Settings → Setup / Resources otherwise
    // (docs/operations/multi-user-setup.md).
    const chip = setupChip(setupState);
    const confirm = needsConfirm(setupState);
    const review = needsReview(setupState);
    status.appendChild(h("button", {
      class: confirm ? "btn sm primary start-setup" : `start-chip start-setup ${chip.tone}`.trim(),
      type: "button", "data-setup-chip": chip.id, ...(review ? { "data-setup-review": "" } : {}),
      title: review ? chip.text : t("start.chip_title", { host: setupState.host?.label || "" }),
      onclick: () => {
        if (review) { openSetupReview(setupState, load); return; }
        try { localStorage.setItem("ark.settingsTab", "setupResources"); localStorage.setItem("ark.settingsScope", "project"); } catch { /* ignore */ }
        ctx.navigate?.("settings");
      },
    }, confirm ? t("start.review") : chip.text));
  };
  const load = async () => {
    try {
      setupState = await loadSetupState();
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
    return { id: `mine-${mine}`, tone: STATE_TONE[mine], text: t(`start.mine.${mine}`) };
  }
  // Local single-user mode: the agent registers paths under its own host
  // id, which differs from the browser's, so "needs a location here" would
  // only be noise after confirming.
  const missing = state.viewer?.token_kind === "local" ? 0 : (state.readiness?.needs_location || []).length;
  if (missing) return { id: "needs-location", tone: "", text: t("start.needs_location", { count: missing }) };
  return { id: "ready-here", tone: "ok", text: t("start.ready_here") };
}

/**
 * Home "Needs you": a row while a setup layer waits for this user's
 * confirmation (an agent proposed it).  Empty until the state arrives;
 * `onShown()` lets the panel adjust its count and empty message.
 */
export function setupNeedsRow({ onShown, onChange } = {}) {
  const slot = h("div", { class: "needs-setup-slot" });
  const fill = async () => {
    let state;
    try { state = await loadSetupState(); } catch { return; }
    clear(slot);
    if (!needsConfirm(state)) return;
    const project = state.layers?.project?.state === "proposed" && state.layers.project.can_confirm !== false;
    slot.appendChild(h("div", { class: "needs-setup", "data-needs-setup": project ? "project" : "user" },
      h("span", { class: "start-ic" }, icon("settings", "sm")),
      h("div", { class: "needs-setup-text" },
        h("b", null, t(project ? "needs.setup.title" : "needs.setup.title_mine")),
        h("span", null, t("needs.setup.hint"))),
      h("button", { class: "btn sm primary", type: "button", "data-setup-review": "",
        onclick: () => openSetupReview(state, () => { fill(); onChange?.(); }) }, t("start.review"))));
    onShown?.();
  };
  fill();
  return slot;
}
