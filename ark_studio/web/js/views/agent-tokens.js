// Settings → Personal → Agent access: per-account agent tokens.
// A personal token lets the user's own coding agent call /api/agent/v1/* as
// that user.  The server stores only a hash; the value is shown once here.
// Also: the legacy shared server token (Settings → Admin) and the helper the
// Home "Copy prompt" action uses for the project's starter token.
import { h, clear, fmtDateTime, relativeTime, copyText } from "../dom.js";
import { t } from "../i18n.js";
import { icon } from "../icons.js";
import { api } from "../api.js";
import { openModal, toast } from "../components.js";
import { clearStarterTokens } from "../starter-token-store.js";

try { clearStarterTokens(globalThis.localStorage); } catch { /* storage disabled */ }

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
          onclick: () => confirmRevoke(item, (data) => { state = data; render(); paintCleanup(); }),
        }, icon("ban", "sm"), t("tokens.revoke"))));
    }
  };
  // Home starter-prompt tokens that Copy prompt no longer hands out (every
  // project's current one is kept): one action revokes them all.
  const cleanup = h("div", { class: "assistant-api-actions", "data-starter-cleanup": "" });
  const paintCleanup = async () => {
    let old = 0;
    try { old = (await api.starterToken({ mode: "peek" })).old_tokens || 0; } catch { /* hide */ }
    clear(cleanup);
    if (!old) return;
    cleanup.appendChild(h("button", {
      class: "ghost-btn danger sm", type: "button",
      onclick: () => openModal({
        title: t("tokens.starter_cleanup_title"),
        body: h("p", null, t("tokens.starter_cleanup_body", { count: old })),
        actions: [
          { label: t("action.cancel"), ghost: true },
          {
            label: t("tokens.starter_cleanup_go", { count: old }), danger: true,
            onClick: async () => {
              try { state = await api.starterToken({ mode: "cleanup" }); }
              catch (err) { toast(err.message, "err"); return false; }
              toast(t("tokens.revoked"), "ok");
              render();
              paintCleanup();
            },
          },
        ],
      }),
    }, icon("ban", "sm"), t("tokens.starter_cleanup", { count: old })));
  };
  const load = async () => {
    try { state = await api.agentTokens(); render(); paintCleanup(); }
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
    cleanup,
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
// <date>" (ark_studio/server/internal/org/starter_tokens.go).  Tokens labelled
// "Home starter prompt – <date>" have no project.
export function homePromptLabel(date = new Date(), projectId = "") {
  const day = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
  return `${HOME_PROMPT_LABEL}${projectId ? ` (${projectId})` : ""}${LABEL_SEPARATOR}${day}`;
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

// Copies the text a request resolves to.  ClipboardItem takes the pending
// text inside the click, so the copy still counts as the user's gesture after
// the round trip; without it (or when it is refused) the text is copied once
// it arrives.  Request errors are rethrown.  Resolves to { ok, text }.
async function copyFromRequest(makeText) {
  const pending = makeText();
  let text = null;
  if (globalThis.ClipboardItem && navigator.clipboard?.write) {
    const blob = pending.then((value) => { text = value; return new Blob([value], { type: "text/plain" }); });
    try {
      await navigator.clipboard.write([new ClipboardItem({ "text/plain": blob })]);
      return { ok: true, text };
    } catch { /* refused or failed: the request result decides below */ }
  }
  text = await pending;
  return { ok: await copyText(text), text };
}

// Copy prompt (Home card, prompt preview).  The server keeps one starter
// token per account and project and hands its value out again (mode "copy",
// ark_studio/server/internal/org/starter_tokens.go), so a copy reuses it and
// a new token is minted only when there is none, it expired or was revoked,
// or the person chooses "New token" (which revokes the previous one).  The
// dialog opens on every Copy; it says which token goes into the prompt.  At
// the 10-token limit it offers to revoke this project's older starter tokens
// (revoke_old) and copy.  One flow at a time: further Copy clicks while it is
// open are ignored.  Resolves to { reused, copied }.
let tokenFlow = null;

export function copyPromptWithToken(buildText, { projectId = "" } = {}) {
  if (tokenFlow) return tokenFlow;
  tokenFlow = new Promise((resolve) => {
    openTokenFlow(buildText, projectId, resolve).catch(() => resolve({ reused: false, copied: false }));
  }).finally(() => { tokenFlow = null; });
  return tokenFlow;
}

async function openTokenFlow(buildText, projectId, done) {
  let peek = null;
  if (projectId) {
    try { peek = await api.starterToken({ project: projectId, mode: "peek" }); }
    catch (err) { toast(err.message, "err"); done({ reused: false, copied: false }); return; }
  }
  const current = peek?.current || null;
  const limit = peek?.limit || 10;
  const label = homePromptLabel(new Date(), projectId);
  const extra = h("div", { class: "stack", "data-token-extra": "" });
  let busy = false;
  let result = { reused: false, copied: false };
  let dialog = null;

  const run = async (mode, revokeOld = false) => {
    if (busy) return;
    busy = true;
    const request = projectId
      ? () => api.starterToken({ project: projectId, mode, ...(revokeOld ? { revoke_old: true } : {}) })
      : () => api.createAgentToken({ label, scope: "write", expires_days: 90 });
    let reply = null;
    let copy;
    try {
      copy = await copyFromRequest(async () => { reply = await request(); return buildText(reply.token); });
    } catch (err) {
      busy = false;
      if (err?.code === "AGENT_TOKEN_LIMIT" && projectId) await showLimit(mode);
      else toast(err?.message || String(err), "err");
      return;
    }
    result = { reused: Boolean(reply?.reused), copied: true };
    dialog.close(true);
    if (copy.ok) toast(t("start.token_copied"), "ok");
    else manualCopy(copy.text);
  };

  const showLimit = async (mode) => {
    let old = 0;
    try { old = (await api.starterToken({ project: projectId, mode: "peek" })).old_tokens || 0; } catch { /* keep 0 */ }
    clear(extra);
    extra.appendChild(h("div", { class: "notice", "data-token-limit": "" }, icon("alert", "sm"),
      h("div", { class: "stack" },
        h("p", null, old ? t("start.token_limit_old", { limit, count: old }) : t("start.token_limit", { limit })),
        old ? h("div", { class: "row tight" },
          h("button", { class: "btn primary", type: "button", onclick: () => run(mode, true) },
            t("start.token_limit_revoke", { count: old }))) : null)));
  };

  const confirmNew = () => {
    clear(extra);
    extra.appendChild(h("div", { class: "notice", "data-token-new": "" }, icon("alert", "sm"),
      h("div", { class: "stack" },
        h("p", null, t("start.token_new_confirm", { label: current.label })),
        h("div", { class: "row tight" },
          h("button", { class: "btn danger", type: "button", onclick: () => run("new") }, t("start.token_new_go")),
          h("button", { class: "btn ghost", type: "button", onclick: () => clear(extra) }, t("action.cancel"))))));
    return false;
  };

  const old = peek?.old_tokens || 0;
  dialog = openModal({
    onClose: () => done(result),
    title: t("start.token_title"),
    body: h("div", { class: "stack" },
      current
        ? h("p", null, t("start.token_body_reuse", { label: current.label, expires: expiryText(current) }))
        : h("p", null, t(projectId ? "start.token_body" : "start.token_body_once", { label })),
      old ? h("p", { class: "muted" }, t("start.token_existing", { count: old })) : null,
      h("div", { class: "notice" }, icon("alert", "sm"), h("div", null, t("start.token_warning"))),
      extra),
    actions: [
      { label: t("action.cancel"), ghost: true },
      ...(current ? [{ label: t("start.token_new"), ghost: true, onClick: confirmNew }] : []),
      {
        label: t(current ? "start.token_copy" : "start.token_confirm"), primary: true,
        onClick: () => { run("copy"); return false; },
      },
    ],
  });
}
