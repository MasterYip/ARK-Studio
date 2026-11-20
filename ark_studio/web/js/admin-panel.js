import { h, clear } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { api } from "./api.js";
import { upsertUsers, userBadge, userLabel } from "./users.js";
import { confirmDialog } from "./components.js";

// Organization administration: users, teams, project grants and Linux-account
// mappings.  Every action calls the existing /api/org/* endpoints; the server
// enforces the owner/admin role even if this panel is bypassed.
export function renderAdminPanel(ctx) {
  const state = {
    org: null, users: [], teams: [], linux: {},
    projects: [], selectedProject: "", detail: null, capability: null,
  };
  const root = h("div", { class: "stack" });
  const status = h("span", { class: "settings-status" });
  const errorNote = h("div", { class: "settings-note" });
  const usersHost = h("div", { class: "stack" });
  const teamsHost = h("div", { class: "stack" });
  const grantsHost = h("div", { class: "stack" });
  const projectSelect = h("select", { class: "select" });

  const row = (...children) => h("div", {
    style: { display: "flex", gap: "8px", alignItems: "center",
             flexWrap: "wrap", padding: "7px 0",
             borderBottom: "1px solid var(--line-soft)" },
  }, ...children);
  const pill = (text, kind = "") => h("span", { class: `pill ${kind}`.trim() }, text);

  function fail(err) {
    clear(errorNote);
    errorNote.append(icon("alert", "sm"), h("div", null, err.message || String(err)));
  }
  async function run(action) {
    try {
      clear(errorNote);
      await action();
      await loadAll();
    } catch (err) { fail(err); }
  }

  function roleSelect(user) {
    return h("select", { class: "select", style: { width: "auto" } },
      ...["owner", "admin", "member", "viewer"].map((role) => h("option", { // i18n-ignore: option values; labels via t()
        value: role, selected: user.role === role,
      }, t(`admin.role.${role}`))));
  }

  const sectionButtons = new Map();
  const nav = h("div", {
    class: "admin-tabs",
    style: { display: "flex", gap: "6px", flexWrap: "wrap",
             padding: "4px", background: "var(--card2)",
             border: "1px solid var(--line)", borderRadius: "10px" },
  });
  for (const [id, label, iconName] of [
    ["users", t("admin.users"), "user"],
    ["teams", t("admin.teams"), "users"],
    ["grants", t("admin.grants"), "folder"],
  ]) {
    const button = h("button", {
      class: "ghost-btn sm", type: "button",
      onclick: () => selectSection(id),
    }, icon(iconName, "sm"), label);
    sectionButtons.set(id, button);
    nav.appendChild(button);
  }
  function selectSection(id) {
    const active = ["users", "teams", "grants"].includes(id) ? id : "users";
    for (const [key, button] of sectionButtons) {
      const on = key === active;
      button.classList.toggle("active", on);
      button.style.background = on ? "var(--accent)" : "";
      button.style.color = on ? "#fff" : "";
    }
    usersHost.classList.toggle("hidden", active !== "users");
    teamsHost.classList.toggle("hidden", active !== "teams");
    grantsHost.classList.toggle("hidden", active !== "grants");
  }

  function renderUsers() {
    clear(usersHost);
    const createName = h("input", { class: "input", placeholder: t("admin.username") });
    const createPassword = h("input", { class: "input", type: "password",
      autocomplete: "new-password", placeholder: t("admin.password") });
    const createRole = h("select", { class: "select" },
      ...["member", "viewer", "admin", "owner"].map((role) => // i18n-ignore: option values; labels via t()
        h("option", { value: role }, t(`admin.role.${role}`))));
    usersHost.append(h("section", { class: "panel settings-panel" },
      h("div", { class: "panel-head" }, h("h3", null, t("admin.users"))),
      h("p", { class: "settings-lead" }, t("admin.users_hint")),
      row(createName, createPassword, createRole,
        h("button", {
          class: "primary-btn", type: "button",
          onclick: () => run(() => api.createOrgUser({
            username: createName.value.trim(),
            password: createPassword.value,
            role: createRole.value,
          })),
        }, icon("plus", "sm"), t("admin.create_user")))));
    // The server refuses to disable or delete yourself or the last owner;
    // say so up front instead of offering the buttons.  Local mode has no
    // passwords, so Reset password is hidden there.
    const me = ctx.S.session?.user || "";
    const owners = state.users.filter((item) => item.role === "owner" && !item.disabled).length;
    const local = Boolean(ctx.S.session?.local);
    for (const user of state.users) {
      const role = roleSelect(user);
      const self = user.username === me;
      const lastOwner = user.role === "owner" && owners <= 1;
      const lockReason = self ? t("admin.self_locked") : lastOwner ? t("admin.last_owner_locked") : "";
      const linux = state.linux[user.username] || [];
      const assignInput = h("input", {
        class: "input", placeholder: t("admin.linux_account"),
        style: { width: "150px" },
      });
      const capability = state.capability;
      const linuxDetails = h("details", { class: "admin-user-linux" },
        h("summary", null, `${t("admin.linux_account")}: `,
          linux.length ? linux.join(", ") : t("admin.none")),
        capability?.linux === false
          ? h("div", { class: "settings-note" }, icon("info", "sm"),
            h("div", null, t("admin.linux_only")))
          : h("div", { class: "stack", style: { gap: "6px", marginTop: "8px" } },
            linux.length ? h("div", { class: "row tight" }, ...linux.map((account) =>
              h("button", {
                class: "ghost-btn sm", type: "button",
                onclick: () => run(() => api.revokeLinuxAccount(user.id, account)),
              }, `${account} ×`))) : null,
            row(assignInput,
              h("button", {
                class: "ghost-btn sm", type: "button",
                onclick: () => run(() => api.assignLinuxAccount(user.id,
                  assignInput.value.trim())),
              }, icon("plus", "sm"), t("admin.assign_linux")))));
      usersHost.append(h("section", { class: "panel settings-panel" },
        h("div", { class: "panel-head" },
          h("h3", { class: "admin-user-title" },
            userBadge(user.username, { size: "md", showUsername: true })),
          h("div", { class: "row tight" },
            pill(t(`admin.role.${user.role}`), user.role === "owner" ? "ok" : ""),
            user.disabled ? pill(t("admin.disabled"), "warn") : null)),
        row(role,
          h("button", {
            class: "ghost-btn", type: "button",
            onclick: () => run(() => api.updateOrgUser(user.id, { role: role.value })),
          }, icon("save", "sm"), t("admin.save_role")),
          h("button", {
            class: "ghost-btn", type: "button",
            disabled: Boolean(lockReason) && !user.disabled, title: !user.disabled ? lockReason || undefined : undefined,
            onclick: async () => {
              if (!user.disabled && !await confirmDialog(t("admin.disable_confirm", { name: user.username }),
                { title: t("admin.disable"), confirmLabel: t("admin.disable"), danger: true })) return;
              run(() => api.updateOrgUser(user.id, { disabled: !user.disabled }));
            },
          }, icon(user.disabled ? "check" : "ban", "sm"),
          user.disabled ? t("admin.enable") : t("admin.disable")),
          local ? null : h("button", {
            class: "ghost-btn", type: "button",
            onclick: () => {
              const value = window.prompt(t("admin.new_password_for") + " " + user.username);
              if (value) run(() => api.resetOrgPassword(user.id, { new_password: value }));
            },
          }, icon("shield", "sm"), t("admin.reset_password")),
          h("button", {
            class: "ghost-btn danger", type: "button",
            disabled: Boolean(lockReason), title: lockReason || undefined,
            onclick: async () => {
              if (await confirmDialog(t("admin.delete_confirm", { name: user.username }),
                { title: t("admin.delete_title"), confirmLabel: t("admin.delete"), danger: true })) {
                run(() => api.deleteOrgUser(user.id));
              }
            },
          }, icon("trash", "sm"), t("admin.delete")),
          lockReason ? h("span", { class: "muted", style: { fontSize: "11px" } }, lockReason) : null),
        linuxDetails));
    }
  }

  function renderTeams() {
    clear(teamsHost);
    const teamName = h("input", { class: "input", placeholder: t("admin.team_name") });
    teamsHost.append(h("section", { class: "panel settings-panel" },
      h("div", { class: "panel-head" }, h("h3", null, t("admin.teams"))),
      row(teamName, h("button", {
        class: "primary-btn", type: "button",
        onclick: () => run(() => api.createOrgTeam({ name: teamName.value.trim() })),
      }, icon("plus", "sm"), t("admin.create_team")))));
    for (const team of state.teams) {
      const members = team.members || [];
      const memberIds = new Set(members.map((member) => member.id));
      const candidates = state.users.filter((user) => !memberIds.has(user.id));
      const addSelect = h("select", { class: "select" },
        ...candidates.map((user) => h("option", { value: user.username,
          dataset: { userOption: user.username } }, userLabel(user.username))));
      teamsHost.append(h("section", { class: "panel settings-panel" },
        h("div", { class: "panel-head" },
          h("h3", null, team.name),
          h("div", { class: "row tight" },
            pill(`${members.length} ${t("admin.members")}`),
            h("button", {
              class: "ghost-btn danger", type: "button",
              onclick: () => {
                if (window.confirm(`${t("admin.delete")} ${team.name}?`)) {
                  run(() => api.deleteOrgTeam(team.id));
                }
              },
            }, icon("trash", "sm"), t("admin.delete")))),
        h("div", { class: "row tight" },
          ...members.map((member) => h("button", {
            class: "ghost-btn", type: "button",
            title: t("admin.remove_member"),
            onclick: () => run(() => api.removeTeamMember(team.id, member.id)),
          }, userBadge(member.username, { size: "xs" }), " ×"))),
        candidates.length
          ? row(addSelect, h("button", {
            class: "ghost-btn", type: "button",
            onclick: () => run(() => api.addTeamMember(team.id, addSelect.value)),
          }, icon("plus", "sm"), t("admin.add_member")))
          : null));
    }
  }

  async function loadProject() {
    state.detail = null;
    if (!state.selectedProject) { renderGrants(); return; }
    try {
      const data = await api.orgProject(state.selectedProject);
      state.detail = data.project;
    } catch (err) { fail(err); }
    renderGrants();
  }

  function renderGrants() {
    clear(grantsHost);
    const projects = state.projects || [];
    clear(projectSelect);
    for (const project of projects) {
      projectSelect.append(h("option", {
        value: project.id, selected: project.id === state.selectedProject,
      }, project.name || project.id));
    }
    projectSelect.onchange = () => {
      state.selectedProject = projectSelect.value;
      loadProject();
    };
    const detail = state.detail;
    const grantTarget = h("select", { class: "select" },
      ...state.users.map((user) => h("option", { value: user.id },
        `${t("admin.user")}: ${user.username}`)),
      ...state.teams.map((team) => h("option", { value: `team:${team.id}` },
        `${t("admin.team")}: ${team.name}`)));
    const grantPermission = h("select", { class: "select" },
      ...["read", "write", "admin"].map((permission) => // i18n-ignore: option values; labels via t()
        h("option", { value: permission }, t(`admin.permission.${permission}`))));
    grantsHost.append(h("section", { class: "panel settings-panel" },
      h("div", { class: "panel-head" }, h("h3", null, t("admin.grants"))),
      h("p", { class: "settings-lead" }, t("admin.grants_hint")),
      row(projectSelect)));
    if (!detail) {
      grantsHost.append(h("div", { class: "empty sm" }, t("admin.select_project")));
      return;
    }
    grantsHost.append(h("section", { class: "panel settings-panel" },
      h("div", { class: "panel-head" },
        h("h3", null, detail.name || detail.id),
        pill(detail.permission || "")),
      h("div", { class: "settings-lead" }, h("b", null, t("admin.direct_grants"))),
      ...(detail.grants || []).map((grant) => row(
        userBadge(grant.username, { size: "xs", showUsername: true }), pill(grant.permission, "ok"),
        h("button", {
          class: "ghost-btn", type: "button",
          onclick: () => run(() => api.revokeProjectGrant(detail.id,
            { user: grant.username })),
        }, icon("trash", "sm"), t("admin.revoke")))),
      (detail.grants || []).length ? null : h("div", { class: "empty sm" },
        t("admin.none")),
      h("div", { class: "settings-lead" }, h("b", null, t("admin.team_grants"))),
      ...(detail.team_grants || []).map((grant) => row(
        h("span", null, grant.name), pill(grant.permission, "ok"),
        h("button", {
          class: "ghost-btn", type: "button",
          onclick: () => run(() => api.revokeProjectGrant(detail.id,
            { team: grant.team_id })),
        }, icon("trash", "sm"), t("admin.revoke")))),
      (detail.team_grants || []).length ? null : h("div", { class: "empty sm" },
        t("admin.none")),
      row(grantTarget, grantPermission, h("button", {
        class: "primary-btn", type: "button",
        onclick: () => {
          const value = grantTarget.value || "";
          const body = value.startsWith("team:")
            ? { team: value.slice(5), permission: grantPermission.value }
            : { user: value, permission: grantPermission.value };
          run(() => api.grantProject(detail.id, body));
        },
      }, icon("plus", "sm"), t("admin.grant")))));
  }

  async function loadAll() {
    status.textContent = t("admin.loading");
    try {
      const [overview, usersData, teamsData, environment] = await Promise.all([
        api.orgOverview(), api.orgUsers(), api.orgTeams(),
        api.personalEnvironment().catch(() => null),
      ]);
      state.org = overview;
      state.users = usersData.users || [];
      upsertUsers(state.users);
      state.linux = usersData.linux_accounts || {};
      state.teams = teamsData.teams || [];
      state.capability = environment?.settings?.capability || null;
      state.projects = overview.all_projects || overview.projects || [];
      if (!state.selectedProject || !state.projects.some(
        (project) => project.id === state.selectedProject)) {
        state.selectedProject = state.projects[0]?.id || "";
      }
      renderUsers();
      renderTeams();
      await loadProject();
      status.textContent = "";
    } catch (err) {
      status.textContent = "";
      fail(err);
    }
  }

  root.append(
    h("section", { class: "panel settings-panel" },
      h("div", { class: "panel-head" },
        h("h3", null, t("admin.title")),
        h("div", { class: "row tight" }, status,
          h("button", {
            class: "ghost-btn", type: "button", onclick: () => loadAll(),
          }, icon("refresh", "sm"), t("admin.refresh")))),
      h("p", { class: "settings-lead" }, t("admin.hint")),
      h("div", { class: "settings-note" }, icon("shield", "sm"),
        h("div", null, t("admin.guard_hint")))),
    errorNote, nav, usersHost, teamsHost, grantsHost);
  selectSection("users");
  loadAll();
  return root;
}
