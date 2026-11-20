// Settings → Personal → Agent access: per-account agent tokens.
// A personal token lets the user's own coding agent call /api/agent/v1/* as
// that user.  The server stores only a hash; the value is shown once here.
// Also: the legacy shared server token (Settings → Admin) and the helper the
// Home "Copy with my token" action uses to mint a prompt token on request.
import { h, clear, fmtDateTime, relativeTime, copyText } from "../dom.js";
import { t } from "../i18n.js";
import { icon } from "../icons.js";
import { api } from "../api.js";
import { openModal, toast } from "../components.js";
import {
  forgetStarterTokenId, loadStarterToken, saveStarterToken, starterTokenListed,
} from "../starter-token-store.js";

function storage() {
  try { return globalThis.localStorage || null; } catch { return null; }
}

// Stored token label (data, not UI): kept English so it can be recognised.
/* i18n-ignore-start */
export const HOME_PROMPT_LABEL = "Home starter prompt";
const LABEL_SEPARATOR = " – ";
/* i18n-ignore-end */
const EXPIRY_CHOICES = [0, 30, 90];

function tokenDate(value) {
  return value ? fmtDateTime(value) : "";
}

function expiryText(item) {
  if (item.expired) return t("tokens.expired");
  return item.expires_at ? tokenDate(item.expires_at) : t("tokens.never_expires");
}

function revealBox(secret, onDone) {
  const input = h("input", { class: "input", type: "text", value: secret, readonly: true,
    "data-agent-token": "1", onfocus: (e) => e.target.select() });
  return h("div", { class: "agent-token-reveal notice" },
    icon("alert", "sm"),
    h("div", { class: "stack" },
      h("b", null, t("tokens.once")),
      h("div", { class: "token-row" }, input,
        h("button", {
          class: "primary-btn", type: "button",
          onclick: async () => {
            const ok = await copyText(secret);
            toast(ok ? t("tokens.copied") : t("start.copy_failed"), ok ? "ok" : "err");
          },
        }, icon("copy", "sm"), t("tokens.copy")),
        h("button", { class: "ghost-btn", type: "button", onclick: onDone }, t("tokens.done")))));
}

function confirmRevoke(item, onRevoked) {
  openModal({
    title: t("tokens.revoke_title"),
    body: h("p", null, t("tokens.revoke_body", { label: item.label, prefix: item.prefix })),
    actions: [
      { label: t("action.cancel"), ghost: true },
      {
        label: t("tokens.revoke"), primary: true,
        onClick: async () => {
          try {
            const data = await api.revokeAgentToken(item.id);
            const store = storage();
            if (store) forgetStarterTokenId(store, item.id);
            toast(t("tokens.revoked"), "ok");
            onRevoked(data);
          } catch (err) { toast(err.message, "err"); return false; }
        },
      },
    ],
  });
}

export function agentTokensPanel(ctx) {
  const user = ctx.S.session?.user || "local";
  const label = h("input", { class: "input", maxlength: 80, placeholder: t("tokens.label_placeholder") });
  const scope = h("select", { class: "select" },
    h("option", { value: "write" }, t("tokens.scope.write")),
    h("option", { value: "read" }, t("tokens.scope.read")));
  const expiry = h("select", { class: "select" },
    ...EXPIRY_CHOICES.map((days) => h("option", { value: String(days) }, t(`tokens.expiry.${days}`))));
  const reveal = h("div");
  const list = h("div", { class: "agent-token-list" });
  const count = h("span", { class: "pill" });
  let state = { tokens: [], limit: 10 };

  const render = () => {
    clear(list);
    const active = state.tokens.filter((item) => !item.expired).length;
    count.textContent = t("tokens.count", { count: active, limit: state.limit });
    if (!state.tokens.length) {
      list.appendChild(h("div", { class: "empty sm" }, t("tokens.empty")));
      return;
    }
    for (const item of state.tokens) {
      list.appendChild(h("div", { class: `agent-token-item ${item.expired ? "expired" : ""}`.trim(),
        "data-token-id": item.id },
        h("div", { class: "agent-token-main" },
          h("b", null, item.label),
          h("code", null, `${item.prefix}…`),
          h("span", { class: `pill ${item.scope === "write" ? "ok" : ""}`.trim() },
            t(`tokens.scope.${item.scope}`))),
        h("div", { class: "agent-token-meta muted" },
          h("span", null, t("tokens.created_at", { date: tokenDate(item.created_at) })),
          h("span", null, t("tokens.last_used", {
            date: item.last_used_at ? relativeTime(item.last_used_at) : t("tokens.never_used") })),
          h("span", { class: item.expired ? "bad" : "" }, t("tokens.expires", { date: expiryText(item) }))),
        h("button", {
          class: "ghost-btn danger sm", type: "button",
          onclick: () => confirmRevoke(item, (data) => { state = data; render(); }),
        }, icon("ban", "sm"), t("tokens.revoke"))));
    }
  };
  const load = async () => {
    try { state = await api.agentTokens(); render(); }
    catch (err) { clear(list); list.appendChild(h("div", { class: "notice" }, icon("alert"), h("div", null, err.message))); }
  };
  const create = h("button", {
    class: "primary-btn", type: "button",
    onclick: async () => {
      create.disabled = true;
      try {
        const data = await api.createAgentToken({
          label: label.value.trim(), scope: scope.value, expires_days: Number(expiry.value),
        });
        state = data;
        label.value = "";
        render();
        clear(reveal);
        reveal.appendChild(revealBox(data.token, () => clear(reveal)));
      } catch (err) { toast(err.message, "err"); }
      finally { create.disabled = false; }
    },
  }, icon("plus", "sm"), t("tokens.create"));

  const panel = h("section", { class: "panel settings-panel agent-tokens-panel" },
    h("div", { class: "panel-head" }, h("h3", null, t("tokens.title")), count),
    h("p", { class: "settings-lead" }, t("tokens.lead", { user })),
    h("div", { class: "assistant-api-grid" },
      h("div", { class: "field" }, h("label", null, t("tokens.label")), label),
      h("div", { class: "field" }, h("label", null, t("tokens.scope")), scope),
      h("div", { class: "field" }, h("label", null, t("tokens.expiry")), expiry)),
    h("div", { class: "assistant-api-actions" }, create),
    reveal,
    list,
    h("div", { class: "settings-note" }, icon("info", "sm"), h("div", null, t("tokens.whoami_hint"))));
  load();
  return panel;
}

// Settings → Admin: the shared server token, kept for older agents/scripts.
export function legacyServerTokenPanel() {
  const body = h("div", { class: "stack" });
  const reveal = h("button", {
    class: "ghost-btn", type: "button",
    onclick: async () => {
      try {
        const { token } = await api.agentToken();
        clear(body);
        const input = h("input", { class: "input", type: "password", value: token, readonly: true });
        let shown = false;
        const toggle = h("button", {
          class: "ghost-btn", type: "button",
          onclick: () => {
            shown = !shown;
            input.type = shown ? "text" : "password";
            toggle.textContent = shown ? t("assistant.hide") : t("assistant.reveal");
          },
        }, t("assistant.reveal"));
        body.appendChild(h("div", { class: "token-row" }, input, toggle,
          h("button", {
            class: "ghost-btn", type: "button",
            onclick: async () => {
              const ok = await copyText(token);
              toast(ok ? t("tokens.copied") : t("start.copy_failed"), ok ? "ok" : "err");
            },
          }, icon("copy", "sm"), t("tokens.copy"))));
      } catch (err) { toast(err.message, "err"); }
    },
  }, icon("eye", "sm"), t("assistant.reveal"));
  body.appendChild(h("div", { class: "assistant-api-actions" }, reveal));
  return h("section", { class: "panel settings-panel legacy-token-panel" },
    h("div", { class: "panel-head" }, h("h3", null, t("tokens.legacy.title")),
      h("span", { class: "pill warn" }, t("tokens.legacy.pill"))),
    h("p", { class: "settings-lead" }, t("tokens.legacy.hint")),
    body);
}

// ---------------------------------------------------------- Home prompts
// The server labels a project's starter token "Home starter prompt (<id>) –
// <date>" and rotates them (ark_studio/server/internal/org/admin.go).  Tokens
// labelled "Home starter prompt – <date>" come from before that (no project).
export function homePromptLabel(date = new Date(), projectId = "") {
  const day = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
  return `${HOME_PROMPT_LABEL}${projectId ? ` (${projectId})` : ""}${LABEL_SEPARATOR}${day}`;
}

/** Starter tokens minted before rotation existed: no project, never rotated by the server. */
export function isLegacyPromptToken(item) {
  return !item?.expired && String(item?.label || "").startsWith(`${HOME_PROMPT_LABEL}${LABEL_SEPARATOR}`);
}

function manualCopy(text) {
  const area = h("textarea", { class: "input agent-token-manual", readonly: true, rows: 10 });
  area.value = text;
  openModal({
    title: t("start.token_manual_title"),
    wide: true,
    body: h("div", { class: "stack" }, h("p", { class: "muted" }, t("start.token_manual_hint")), area),
    actions: [{ label: t("tokens.done"), primary: true }],
  });
  setTimeout(() => area.select(), 0);
}

// Copy prompt (Home card, prompt preview).  The server keeps only a hash of
// a token, so a value can be handed out again only from this browser: the
// starter token minted here is stored per account and project
// (starter-token-store.js).  With a stored, unexpired token the click copies
// at once: the text is built and clipboard.writeText is called inside the
// click, with no request, no dialog and no rotation.  The token list is
// checked afterwards; a token the server no longer lists (revoked, expired,
// rotated away by a copy in another browser) is forgotten and the next Copy
// mints a new one.
//
// Without a stored token: ask, then mint.  Nothing is created without the
// click on "Create token and copy".  The server rotates (purpose: "starter"):
// each mint replaces this project's older starter tokens and keeps the
// previous one active, so a running agent is not cut off and copying never
// reaches the per-account limit.  Starter tokens from before rotation (no
// project in the label) can be revoked here; that happens before the new
// token is created, so a full account still gets its copy.
// One token flow at a time: further Copy clicks while the token list is
// loading or the confirmation is open are ignored (no stacked dialogs, no
// duplicate requests, never two tokens for one copy).
// Resolves to { reused, copied } (copied: false when cancelled or failed).
let tokenFlow = null;

export function copyPromptWithToken(buildText, { projectId = "", user = "local" } = {}) {
  if (tokenFlow) return tokenFlow;
  const store = storage();
  const kept = store && projectId ? loadStarterToken(store, user, projectId) : null;
  if (kept) {
    // Synchronous up to clipboard.writeText: still inside the user's click.
    const text = buildText(kept.token);
    tokenFlow = copyText(text).then((ok) => {
      if (ok) toast(t("start.token_copied"), "ok");
      else manualCopy(text);
      checkKeptToken(store, kept.id);
      return { reused: true, copied: true };
    }).finally(() => { tokenFlow = null; });
    return tokenFlow;
  }
  tokenFlow = new Promise((resolve) => {
    openTokenFlow(buildText, { projectId, user, store }, resolve)
      .catch(() => resolve({ reused: false, copied: false }));
  }).finally(() => { tokenFlow = null; });
  return tokenFlow;
}

// After a reuse: is the token still listed?  If not, forget it and say so.
async function checkKeptToken(store, id) {
  let tokens = null;
  try { tokens = (await api.agentTokens()).tokens || []; } catch { return; }
  if (starterTokenListed(tokens, id)) return;
  forgetStarterTokenId(store, id);
  toast(t("start.token_gone"), "err");
}

async function openTokenFlow(buildText, { projectId, user, store }, done) {
  let legacy = [];
  try {
    legacy = ((await api.agentTokens()).tokens || []).filter(isLegacyPromptToken);
  } catch (err) { toast(err.message, "err"); done({ reused: false, copied: false }); return; }
  const label = homePromptLabel(new Date(), projectId);
  const revokeOld = h("input", { type: "checkbox" });
  revokeOld.checked = true;
  let minting = false;
  let copied = false;
  openModal({
    onClose: () => done({ reused: false, copied }),
    title: t("start.token_title"),
    body: h("div", { class: "stack" },
      h("p", null, t("start.token_body", { label })),
      projectId ? h("p", { class: "muted", "data-token-rotation": "" }, t("start.token_rotation")) : null,
      legacy.length ? h("p", { class: "muted" }, t("start.token_existing", { count: legacy.length })) : null,
      legacy.length ? h("label", { class: "row tight agent-token-revoke-old" }, revokeOld,
        h("span", null, t("start.token_revoke_old"))) : null,
      h("div", { class: "notice" }, icon("alert", "sm"), h("div", null, t("start.token_warning")))),
    actions: [
      { label: t("action.cancel"), ghost: true },
      {
        label: t("start.token_confirm"), primary: true,
        onClick: async () => {
          if (minting) return false;
          minting = true;
          if (legacy.length && revokeOld.checked) {
            for (const old of legacy) {
              try { await api.revokeAgentToken(old.id); } catch { /* already gone */ }
            }
            legacy = [];
          }
          let data;
          try {
            data = await api.createAgentToken({ label, scope: "write", expires_days: 90,
              ...(projectId ? { purpose: "starter", project: projectId } : {}) });
          } catch (err) { minting = false; toast(err.message, "err"); return false; }
          if (store && projectId) {
            saveStarterToken(store, user, projectId,
              { token: data.token, id: data.item?.id, expires_at: data.item?.expires_at });
          }
          const text = buildText(data.token);
          const ok = await copyText(text);
          copied = true;
          if (ok) toast(t("start.token_copied"), "ok");
          if (!ok) manualCopy(text);
          return true;
        },
      },
    ],
  });
}
