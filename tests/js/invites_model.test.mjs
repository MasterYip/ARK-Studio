// Project invite links: the Members panel model, the Join dialog states and
// the login page's invite texts, in English and Chinese.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  INVITE_DEFAULTS, INVITE_EXPIRY_DAYS, INVITE_MAX_USES, INVITE_PERMISSIONS, JOIN_STATES,
  canManageInvites, expiryKey, inviteSummaryParts, joinModel, maxUsesKey, parseJoinHash, permissionKey, sortInvites,
} from "../../ark_studio/web/js/invites-model.js";
import { EN } from "../../ark_studio/web/js/i18n-en.js";
import { ZH } from "../../ark_studio/web/js/i18n-zh.js";

const TOKEN = "AbCdEfGhIjKlMnOpQrStUvWxYz012345";

test("join links are read from the hash only", () => {
  assert.equal(parseJoinHash(`#/join/${TOKEN}`), TOKEN);
  assert.equal(parseJoinHash(`#join/${TOKEN}`), TOKEN);
  assert.equal(parseJoinHash(`#/join/${TOKEN}?x=1`), TOKEN);
  for (const hash of ["", "#/home", "#/join/", "#/join/short", `#/join/${TOKEN}<script>`, `#/join/${"a".repeat(129)}`, null]) {
    assert.equal(parseJoinHash(hash), "", String(hash));
  }
});

test("defaults: edit access, one person, seven days, sign-up allowed", () => {
  assert.deepEqual(INVITE_DEFAULTS, { permission: "write", expires_days: 7, max_uses: 1, allow_signup: true });
  assert.ok(INVITE_PERMISSIONS.includes(INVITE_DEFAULTS.permission));
  assert.ok(INVITE_EXPIRY_DAYS.includes(INVITE_DEFAULTS.expires_days));
  assert.ok(INVITE_MAX_USES.includes(INVITE_DEFAULTS.max_uses));
  assert.deepEqual(INVITE_EXPIRY_DAYS, [1, 7, 30, 90], "the server accepts exactly these");
  assert.deepEqual(maxUsesKey(0), ["invite.uses_unlimited", {}]);
  assert.deepEqual(maxUsesKey(1), ["invite.uses_one", {}]);
  assert.deepEqual(maxUsesKey(5), ["invite.uses_n", { n: 5 }]);
  assert.deepEqual(expiryKey(1), ["invite.day1", {}]);
  assert.deepEqual(expiryKey(30), ["invite.days", { n: 30 }]);
  assert.deepEqual(permissionKey("write"), ["invite.perm.write", {}]);
  assert.deepEqual(permissionKey("owner"), ["", { text: "owner" }]);
});

test("an invite row says access, uses, expiry, sign-up and creator", () => {
  const row = { permission: "admin", uses: 2, max_uses: 5, state: "valid", expires_at: "2026-11-27T00:00:00+00:00",
    allow_signup: false, created_by: { username: "ann", display_name: "Ann" } };
  assert.deepEqual(inviteSummaryParts(row).map(([key]) => key),
    ["invite.perm.admin", "invite.used", "invite.expires_on", "invite.no_signup_badge", "invite.by"]);
  assert.deepEqual(inviteSummaryParts(row)[1][1], { uses: 2, max: 5 });
  const ended = inviteSummaryParts({ permission: "read", uses: 3, max_uses: 0, state: "expired", allow_signup: true });
  assert.deepEqual(ended.map(([key]) => key), ["invite.perm.read", "invite.used_unlimited"]);
});

test("active links come first, newest first", () => {
  const list = [
    { id: "a", state: "revoked", created_at: "2026-11-20T03:00:00+00:00" },
    { id: "b", state: "valid", created_at: "2026-11-19T00:00:00+00:00" },
    { id: "c", state: "valid", created_at: "2026-11-20T01:00:00+00:00" },
    { id: "d", state: "used_up", created_at: "2026-11-18T00:00:00+00:00" },
  ];
  assert.deepEqual(sortInvites(list).map((x) => x.id), ["c", "b", "a", "d"]);
  assert.deepEqual(sortInvites(null), []);
});

test("join dialog states", () => {
  for (const state of JOIN_STATES) assert.deepEqual(joinModel({ state }), { kind: "unusable", message: `join.state.${state}` });
  assert.deepEqual(joinModel({ state: "weird" }), { kind: "unusable", message: "join.state.invalid" });
  assert.deepEqual(joinModel(null), { kind: "unusable", message: "join.state.invalid" });
  const valid = { state: "valid", project: { id: "prj_1", name: "Team" }, inviter: { username: "ann", display_name: "Ann" },
    permission: "write", signup: true };
  assert.equal(joinModel({ ...valid, signed_in: false }).kind, "signin");
  const join = joinModel({ ...valid, signed_in: true, viewer: { username: "bob", permission: "read", member: false } });
  assert.deepEqual(join, { project: "Team", projectId: "prj_1", inviter: "Ann", permission: "write", signup: true, kind: "join", user: "bob" });
  assert.equal(joinModel({ ...valid, signed_in: true, viewer: { username: "own", permission: "admin", member: true } }).kind, "member");
});

test("the Members tab is for project admins on a server with accounts", () => {
  assert.equal(canManageInvites({ project_permission: "admin" }, { id: "prj_1" }), true);
  assert.equal(canManageInvites({ project_permission: "write" }, { id: "prj_1" }), false);
  assert.equal(canManageInvites({ project_permission: "admin", local: true }, { id: "prj_1" }), false);
  assert.equal(canManageInvites({ project_permission: "admin" }, { id: "ark-demo" }), false);
  assert.equal(canManageInvites({ project_permission: "admin" }, { id: "x", demo: true }), false);
  assert.equal(canManageInvites(null, null), false);
});

test("every invite text exists in English and Chinese", () => {
  const keys = [
    "settings.tab.members", "settings.tab.members_hint", "invite.members_title", "invite.members_lead",
    "invite.panel_title", "invite.panel_lead", "invite.access", "invite.expires", "invite.max_uses", "invite.allow_signup",
    "invite.signup_off_org", "invite.label", "invite.label_placeholder", "invite.create", "invite.created_title",
    "invite.created_hint", "invite.copy", "invite.copied", "invite.copy_failed", "invite.links", "invite.none",
    "invite.hint_label", "invite.revoke", "invite.revoke_confirm", "invite.revoked_toast",
    "join.title", "join.unavailable_title", "join.invited", "join.access", "join.signed_in_as", "join.button",
    "join.open", "join.member", "join.joined",
    "admin.invite_signup", "admin.invite_signup_hint", "admin.invite_signup_toggle", "admin.invite_signup_owner",
    "admin.invite_signup_saved",
    ...INVITE_PERMISSIONS.map((p) => permissionKey(p)[0]),
    ...INVITE_MAX_USES.map((n) => maxUsesKey(n)[0]),
    ...INVITE_EXPIRY_DAYS.map((d) => expiryKey(d)[0]),
    ...["valid", ...JOIN_STATES.slice(1)].map((s) => `invite.state.${s}`),
    ...JOIN_STATES.map((s) => `join.state.${s}`),
    "invite.used", "invite.used_unlimited", "invite.expires_on", "invite.no_signup_badge", "invite.by",
    "log.project.invite.create", "log.project.invite.revoke", "log.project.invite.use", "log.project.member.join",
  ];
  for (const key of keys) {
    assert.ok(EN[key], `EN ${key}`);
    assert.ok(ZH[key], `ZH ${key}`);
    assert.notEqual(EN[key], ZH[key], `ZH ${key} is translated`);
    const vars = (text) => [...text.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort();
    assert.deepEqual(vars(ZH[key]), vars(EN[key]), `${key} placeholders`);
  }
});

test("the login page shows invite states and the sign-up form in both languages", async () => {
  const source = await readFile(new URL("../../ark_studio/web/js/login.js", import.meta.url), "utf8");
  const block = (name) => {
    const start = source.indexOf(`  ${name}: {`);
    assert.ok(start >= 0, name);
    return source.slice(start, source.indexOf("\n  },", start));
  };
  const en = block("en"), zh = block("zh");
  const keys = ["invite.loading", "invite.title", "invite.invited", "invite.access", "invite.lead_signup", "invite.lead_signin",
    "invite.tab_signin", "invite.tab_signup", "perm.read", "perm.write", "perm.admin",
    ...JOIN_STATES.map((s) => `state.${s}`),
    "signup.username", "signup.display", "signup.password", "signup.password_hint", "signup.confirm", "signup.mismatch",
    "signup.short", "signup.submit", "signup.busy", "signup.done", "signup.failed"];
  for (const key of keys) {
    assert.ok(en.includes(`"${key}":`), `login EN ${key}`);
    assert.ok(zh.includes(`"${key}":`), `login ZH ${key}`);
  }
  // Server messages the sign-up form can show have Chinese text.
  for (const text of ["This username is taken.", "This invite link has expired. Ask for a new one.",
    "Too many invite attempts. Wait a few minutes and try again."]) {
    assert.ok(block("server_zh").includes(JSON.stringify(text)), text);
  }
  // The token goes in POST bodies only.
  assert.match(source, /fetch\("api\/auth\/invite\/preview", \{\s*method: "POST"/);
  assert.match(source, /fetch\("api\/auth\/invite\/signup", \{\s*method: "POST"/);
  assert.doesNotMatch(source, /invite\/[a-z]+\?token=/);
});
