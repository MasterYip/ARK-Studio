// Project invite links: pure helpers for Settings › Project › Members and
// the Join dialog (invites.js).  Labels are returned as i18n keys with
// parameters, so the model needs no DOM and no loaded dictionary.

export const INVITE_PERMISSIONS = ["read", "write", "admin"];
export const INVITE_EXPIRY_DAYS = [1, 7, 30, 90];
export const INVITE_MAX_USES = [1, 5, 10, 25, 0]; // 0 = no limit
export const INVITE_DEFAULTS = { permission: "write", expires_days: 7, max_uses: 1, allow_signup: true };
export const JOIN_STATES = ["invalid", "expired", "revoked", "used_up"];

const JOIN_RE = /^#\/?join\/([A-Za-z0-9_-]{16,128})(?:[/?&].*)?$/;

/** The invite token of a `#/join/<token>` hash, or "". */
export function parseJoinHash(hash) {
  const match = JOIN_RE.exec(String(hash || ""));
  return match ? match[1] : "";
}

/** [key, params] of a permission label. */
export function permissionKey(permission) {
  return INVITE_PERMISSIONS.includes(permission) ? [`invite.perm.${permission}`, {}] : ["", { text: String(permission || "") }];
}

/** [key, params] of a max-uses choice. */
export function maxUsesKey(n) {
  const value = Number(n) || 0;
  if (!value) return ["invite.uses_unlimited", {}];
  return value === 1 ? ["invite.uses_one", {}] : ["invite.uses_n", { n: value }];
}

/** [key, params] of an expiry choice. */
export function expiryKey(days) {
  return Number(days) === 1 ? ["invite.day1", {}] : ["invite.days", { n: Number(days) }];
}

/** The parts of one invite row's summary line, as [key, params]. */
export function inviteSummaryParts(invite) {
  const parts = [permissionKey(invite.permission)];
  const uses = Number(invite.uses) || 0;
  const max = Number(invite.max_uses) || 0;
  parts.push(max ? ["invite.used", { uses, max }] : ["invite.used_unlimited", { uses }]);
  if (invite.state === "valid" && invite.expires_at) parts.push(["invite.expires_on", { date: invite.expires_at }]);
  if (invite.allow_signup === false) parts.push(["invite.no_signup_badge", {}]);
  const by = invite.created_by?.display_name || invite.created_by?.username;
  if (by) parts.push(["invite.by", { name: by }]);
  return parts;
}

/** Usable links first, newest first within each group. */
export function sortInvites(list) {
  return [...(list || [])].sort((a, b) => (a.state === "valid" ? 0 : 1) - (b.state === "valid" ? 0 : 1)
    || String(b.created_at || "").localeCompare(String(a.created_at || "")));
}

/**
 * What the Join dialog shows for a preview reply.
 * kind: "unusable" (message key), "member" (already has access), "join"
 * (signed in, can join) or "signin" (not signed in).
 */
export function joinModel(preview) {
  const state = preview?.state || "invalid";
  if (state !== "valid") {
    return { kind: "unusable", message: `join.state.${JOIN_STATES.includes(state) ? state : "invalid"}` }; // i18n-ignore: state id
  }
  const project = preview.project || {};
  const base = {
    project: project.name || project.id || "",
    projectId: project.id || "",
    inviter: preview.inviter?.display_name || preview.inviter?.username || "",
    permission: preview.permission || "",
    signup: Boolean(preview.signup),
  };
  if (!preview.signed_in) return { ...base, kind: "signin" };
  if (preview.viewer?.member) return { ...base, kind: "member", user: preview.viewer.username || "" };
  return { ...base, kind: "join", user: preview.viewer?.username || "" };
}

/** Whether Settings shows the Members tab for this session and project. */
export function canManageInvites(session, project) {
  if (!session || session.local) return false;
  if (project?.demo || project?.id === "ark-demo") return false;
  return session.project_permission === "admin";
}
