import { h, clear, fmtDate, copyText } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { api } from "./api.js";
import { confirmDialog, openModal } from "./components.js";
import { userBadge } from "./users.js";
import {
  INVITE_PERMISSIONS, INVITE_EXPIRY_DAYS, INVITE_MAX_USES, INVITE_DEFAULTS,
  permissionKey, maxUsesKey, expiryKey, inviteSummaryParts, sortInvites, joinModel,
} from "./invites-model.js";

// Project invite links: Settings › Project › Members (project admins) and
// the "Join project" dialog for <server>/#/join/<token>.  The server checks
// every action again (routes_invites.go); the token is only ever sent in a
// request body, never in a URL the server sees.

export { parseJoinHash } from "./invites-model.js";

const tk = ([key, params]) => (key ? t(key, params) : params.text || "");

/** Human label of a project permission. */
export function permissionLabel(permission) { return tk(permissionKey(permission)); }
const maxUsesLabel = (n) => tk(maxUsesKey(n));
const expiryLabel = (days) => tk(expiryKey(days));

/** One line describing an invite row of the list. */
function inviteSummary(invite) {
  return inviteSummaryParts(invite).map(([key, params]) => (key === "invite.expires_on"
    ? t(key, { date: fmtDate(params.date) }) : tk([key, params]))).join(" · ");
}

function pill(text, kind = "") {
  return h("span", { class: `pill ${kind}`.trim() }, text);
}

function selectOf(values, selected, label) {
  return h("select", { class: "select" }, ...values.map((value) => h("option", {
    value: String(value), selected: String(value) === String(selected),
  }, label(value))));
}

/** Settings › Project › Members: who has access and the invite links. */
export function renderMembersPanel(ctx) {
  const projectId = ctx.S.project?.id || "";
  const root = h("div", { class: "stack", "data-members-panel": "" });
  const membersHost = h("div", { class: "stack", style: { gap: "4px" } });
  const listHost = h("div", { class: "stack", style: { gap: "6px" }, "data-invite-list": "" });
  const createdHost = h("div", { "data-invite-created": "" });
  const signupNote = h("div");
  const state = { invites: [], settings: null };

  const permission = selectOf(INVITE_PERMISSIONS, INVITE_DEFAULTS.permission, permissionLabel);
  permission.dataset.invitePermission = "";
  const expires = selectOf(INVITE_EXPIRY_DAYS, INVITE_DEFAULTS.expires_days, expiryLabel);
  expires.dataset.inviteExpires = "";
  const maxUses = selectOf(INVITE_MAX_USES, INVITE_DEFAULTS.max_uses, maxUsesLabel);
  maxUses.dataset.inviteMaxUses = "";
  const allowSignup = h("input", { type: "checkbox", checked: INVITE_DEFAULTS.allow_signup, "data-invite-signup": "" });
  const label = h("input", { class: "input", maxlength: 80, placeholder: t("invite.label_placeholder"), "data-invite-label": "" });
  const createButton = h("button", { class: "primary-btn", type: "button", "data-invite-create": "" },
    icon("plus", "sm"), t("invite.create"));

  const field = (text, control) => h("div", { class: "field" }, h("label", null, text), control);

  function showCreated(url) {
    clear(createdHost);
    if (!url) return;
    const input = h("input", { class: "input", readonly: true, value: url, "data-invite-url": "",
      onfocus: (e) => e.target.select() });
    createdHost.append(h("div", { class: "settings-note", style: { display: "block" } },
      h("div", { style: { fontWeight: "600", marginBottom: "4px" } }, t("invite.created_title")),
      h("div", { class: "muted", style: { fontSize: "12px", marginBottom: "8px" } }, t("invite.created_hint")),
      h("div", { class: "row tight", style: { flexWrap: "nowrap" } }, input,
        h("button", {
          class: "primary-btn", type: "button", "data-invite-copy": "",
          onclick: async () => {
            const ok = await copyText(url);
            ctx.toast(t(ok ? "invite.copied" : "invite.copy_failed"), ok ? "ok" : "err");
          },
        }, icon("copy", "sm"), t("invite.copy")))));
  }

  function renderList() {
    clear(listHost);
    const invites = sortInvites(state.invites);
    if (!invites.length) {
      listHost.append(h("div", { class: "empty sm" }, t("invite.none")));
      return;
    }
    for (const invite of invites) {
      const usable = invite.state === "valid";
      listHost.append(h("div", {
        class: "row", "data-invite-id": invite.id,
        style: { alignItems: "center", gap: "8px", padding: "6px 0", borderTop: "1px solid var(--line-soft)" },
      },
        h("div", { style: { minWidth: "0", flex: "1" } },
          h("div", { style: { fontWeight: "600", fontSize: "12px" } },
            invite.label || t("invite.hint_label", { hint: invite.hint || "" })),
          h("div", { class: "physics-hint" }, inviteSummary(invite))),
        pill(t(`invite.state.${invite.state}`), usable ? "ok" : "warn"),
        usable ? h("button", {
          class: "ghost-btn sm", type: "button", "data-invite-revoke": invite.id,
          onclick: async () => {
            if (!await confirmDialog(t("invite.revoke_confirm"), { title: t("invite.revoke"),
              confirmLabel: t("invite.revoke"), danger: true })) return;
            try {
              apply(await api.revokeProjectInvite(projectId, invite.id));
              ctx.toast(t("invite.revoked_toast"), "ok");
            } catch (err) { ctx.toast(err.message, "err"); }
          },
        }, icon("ban", "sm"), t("invite.revoke")) : null));
    }
  }

  function renderSignupNote() {
    clear(signupNote);
    const off = state.settings && state.settings.signup_enabled === false;
    allowSignup.disabled = off;
    if (off) {
      signupNote.append(h("div", { class: "settings-note" }, icon("info", "sm"),
        h("div", null, t("invite.signup_off_org"))));
    }
  }

  function apply(data) {
    state.invites = data?.invites || [];
    state.settings = data?.settings || state.settings;
    renderList();
    renderSignupNote();
  }

  async function loadMembers() {
    clear(membersHost);
    try {
      const data = await api.orgProject(projectId);
      const grants = data.project?.grants || [];
      const teams = data.project?.team_grants || [];
      if (!grants.length && !teams.length) {
        membersHost.append(h("div", { class: "empty sm" }, t("admin.none")));
      }
      for (const grant of grants) {
        membersHost.append(h("div", { class: "row tight", style: { alignItems: "center", padding: "3px 0" } },
          userBadge(grant.username, { size: "xs", showUsername: true }), pill(permissionLabel(grant.permission))));
      }
      for (const grant of teams) {
        membersHost.append(h("div", { class: "row tight", style: { alignItems: "center", padding: "3px 0" } },
          icon("users", "sm"), h("span", null, grant.name), pill(permissionLabel(grant.permission))));
      }
    } catch (err) {
      membersHost.append(h("div", { class: "settings-note" }, icon("alert", "sm"), h("div", null, err.message)));
    }
  }

  async function load() {
    try {
      apply(await api.projectInvites(projectId));
    } catch (err) {
      clear(listHost);
      listHost.append(h("div", { class: "settings-note" }, icon("alert", "sm"), h("div", null, err.message)));
    }
  }

  createButton.addEventListener("click", async () => {
    createButton.disabled = true;
    try {
      const data = await api.createProjectInvite(projectId, {
        permission: permission.value,
        expires_days: Number(expires.value),
        max_uses: Number(maxUses.value),
        allow_signup: allowSignup.checked && !allowSignup.disabled,
        label: label.value.trim(),
      });
      apply(data);
      showCreated(data.url);
      label.value = "";
    } catch (err) {
      ctx.toast(err.message, "err");
    } finally {
      createButton.disabled = false;
    }
  });

  root.append(
    h("section", { class: "panel settings-panel" },
      h("div", { class: "panel-head" }, h("h3", null, t("invite.members_title"))),
      h("p", { class: "settings-lead" }, t("invite.members_lead")),
      membersHost),
    h("section", { class: "panel settings-panel", "data-invite-panel": "" },
      h("div", { class: "panel-head" }, h("h3", null, t("invite.panel_title"))),
      h("p", { class: "settings-lead" }, t("invite.panel_lead")),
      h("div", { class: "row", style: { gap: "10px", flexWrap: "wrap", alignItems: "flex-end" } },
        field(t("invite.access"), permission),
        field(t("invite.expires"), expires),
        field(t("invite.max_uses"), maxUses)),
      h("label", { class: "row tight", style: { alignItems: "center", gap: "6px", margin: "6px 0" } },
        allowSignup, h("span", null, t("invite.allow_signup"))),
      signupNote,
      field(t("invite.label"), label),
      h("div", { class: "assistant-api-actions" }, createButton),
      createdHost,
      h("div", { class: "settings-sub" }, t("invite.links")),
      listHost));
  loadMembers();
  load();
  return root;
}

/** Organization admin: the invite sign-up switch (owners change it). */
export function renderInviteSignupSetting(ctx) {
  const box = h("input", { type: "checkbox", "data-invite-signup-org": "" });
  const note = h("span", { class: "muted", style: { fontSize: "11px" } });
  const section = h("section", { class: "panel settings-panel" },
    h("div", { class: "panel-head" }, h("h3", null, t("admin.invite_signup"))),
    h("p", { class: "settings-lead" }, t("admin.invite_signup_hint")),
    h("label", { class: "row tight", style: { alignItems: "center", gap: "6px" } },
      box, h("span", null, t("admin.invite_signup_toggle"))),
    note);
  api.inviteSettings().then((data) => {
    const settings = data.settings || {};
    box.checked = settings.signup_enabled !== false;
    box.disabled = !settings.can_change;
    note.textContent = settings.can_change ? "" : t("admin.invite_signup_owner");
  }).catch(() => { section.classList.add("hidden"); });
  box.addEventListener("change", async () => {
    try {
      const data = await api.saveInviteSettings({ signup_enabled: box.checked });
      box.checked = data.settings?.signup_enabled !== false;
      ctx.toast(t("admin.invite_signup_saved"), "ok");
    } catch (err) {
      box.checked = !box.checked;
      ctx.toast(err.message, "err");
    }
  });
  return section;
}

/**
 * The Join project dialog for `#/join/<token>` in a signed-in workbench.
 * onJoined(project) opens the project.  Resolves when the dialog closes.
 */
export async function openJoinDialog(token, { onJoined, onDone } = {}) {
  let preview;
  try {
    preview = await api.invitePreview(token);
  } catch (err) {
    preview = { state: "invalid", error: err.message };
  }
  const model = joinModel(preview);
  const body = h("div", { class: "stack", "data-join-dialog": model.kind, style: { gap: "8px" } });
  const actions = [];
  if (model.kind === "unusable") {
    body.append(h("p", { class: "settings-lead" }, preview.error || t(model.message)));
    actions.push({ label: t("action.close"), primary: true, onClick: () => onDone?.() });
  } else {
    body.append(...[
      h("p", { style: { fontSize: "14px", margin: "0" } }, t("join.invited", { name: model.inviter, project: model.project })),
      h("div", { class: "row tight" }, pill(t("join.access", { perm: permissionLabel(model.permission) }), "ok")),
      model.user ? h("div", { class: "muted", style: { fontSize: "12px" } }, t("join.signed_in_as", { user: model.user })) : null,
      model.kind === "member" ? h("p", { class: "settings-lead" }, t("join.member")) : null].filter(Boolean));
    actions.push({ label: t("action.cancel"), ghost: true, onClick: () => onDone?.() });
    actions.push({
      label: model.kind === "member" ? t("join.open") : t("join.button"),
      primary: true,
      onClick: async () => {
        try {
          const result = model.kind === "member" ? { project: { id: model.projectId, name: model.project } }
            : await api.acceptInvite(token);
          await onJoined?.(result.project || { id: model.projectId, name: model.project }, result);
          onDone?.();
          return true;
        } catch (err) {
          clear(body);
          body.append(h("p", { class: "settings-lead" }, err.message));
          return false;
        }
      },
    });
  }
  openModal({
    title: model.kind === "unusable" ? t("join.unavailable_title") : t("join.title"),
    body,
    actions,
    onClose: () => onDone?.(),
  });
  return model;
}
