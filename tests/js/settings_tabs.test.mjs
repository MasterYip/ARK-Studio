import assert from "node:assert/strict";
import test from "node:test";

import {
  SETTINGS_TABS, DEFAULT_SETTINGS_TAB, GRAPH_SECTION, resolveSettingsTab, settingsTabScope, settingsTarget,
} from "../../ark_studio/web/js/settings-tabs.js";

// Section ids used as Settings tab ids before the regrouping, with the scope
// each lived in.  Every one must still resolve, and keep its scope.
const LEGACY = {
  appearance: "project", graph: "project", projectAgents: "project", projectAssistant: "project",
  setupResources: "project", skills: "project", providers: "project", data: "project",
  blackbox: "project", mobile: "project", gpu: "project", deleteProject: "project",
  // Remote access is stored in this browser: it moved to Personal › This browser.
  remote: "personal",
  profile: "personal", display: "personal", guidance: "personal", assistant: "personal",
  personalAgents: "personal", account: "personal", agentAccess: "personal", environment: "personal",
  // About moved from Personal to the top level, next to the overview.
  shortcuts: "personal", about: "overview", admin: "admin",
};

test("settings are grouped into a few tabs per scope", () => {
  const count = (scope) => SETTINGS_TABS.filter((tab) => tab.scope === scope).length;
  assert.deepEqual(SETTINGS_TABS.map((tab) => tab.id),
    ["overview", "app", "about", "appearance", "lines", "schedules", "agents", "data", "trash", "members", "resources", "access", "profile", "display", "personalAI", "plugins", "browser", "storage", "admin"]);
  assert.equal(count("overview"), 3);
  assert.equal(count("project"), 9);
  assert.equal(count("personal"), 5);
  assert.equal(count("admin"), 2);
  // Settings opens on the overview; Resources and Access are listed as Advanced.
  assert.equal(DEFAULT_SETTINGS_TAB, "overview");
  assert.deepEqual(SETTINGS_TABS.filter((tab) => tab.advanced).map((tab) => tab.id), ["resources", "access"]);
  const sections = SETTINGS_TABS.flatMap((tab) => tab.sections);
  assert.equal(new Set(sections).size, sections.length, "each section lives in exactly one tab");
  // Sections added after the regrouping (no legacy tab id).
  assert.deepEqual([...sections].sort(), [...Object.keys(LEGACY), "appearanceOverride", "sessionUploads",
    "overview", "advanced", "infohubSource", "graphOverride", "plugins", "trash",
    "agentRules", "personalAgentRules", "members", "projectStorage", "app", "markers", "lines", "schedules",
    "notifications"].sort());
  // Session alerts (session-notices-panel.js) sit with the profile and account.
  assert.deepEqual(resolveSettingsTab("notifications"), { tab: "profile", section: "notifications" });
  // Browser-local settings no longer sit under the Project scope.
  assert.equal(settingsTabScope(resolveSettingsTab("infohubSource").tab), "personal");
  assert.equal(resolveSettingsTab("sessionUploads").tab, "access");
  assert.equal(SETTINGS_TABS.find((tab) => tab.sections.includes(GRAPH_SECTION)).id, "appearance");
  assert.equal(SETTINGS_TABS.find((tab) => tab.id === "data").sections.at(-1), "deleteProject");
  // The recycle bin is its own project tab.
  assert.deepEqual(resolveSettingsTab("trash"), { tab: "trash", section: "" });
  assert.equal(settingsTabScope("trash"), "project");
  // Members and invite links (project admins) are their own project tab.
  assert.deepEqual(resolveSettingsTab("members"), { tab: "members", section: "" });
  assert.equal(settingsTabScope("members"), "project");
});

test("every old settings id maps to its new tab, keeps its scope and names the section to reveal", () => {
  const expected = {
    appearance: ["appearance", ""], graph: ["appearance", "graph"],
    projectAgents: ["agents", ""], projectAssistant: ["agents", "projectAssistant"],
    providers: ["agents", "providers"], skills: ["agents", "skills"],
    setupResources: ["resources", ""], gpu: ["resources", "gpu"], remote: ["browser", "remote"],
    mobile: ["access", ""], data: ["data", ""], blackbox: ["data", "blackbox"],
    deleteProject: ["data", "deleteProject"], profile: ["profile", ""], account: ["profile", "account"],
    environment: ["profile", "environment"], display: ["display", ""], shortcuts: ["display", "shortcuts"],
    guidance: ["display", "guidance"], assistant: ["personalAI", ""],
    personalAgents: ["personalAI", "personalAgents"], agentAccess: ["personalAI", "agentAccess"],
    about: ["about", ""], admin: ["admin", ""],
  };
  for (const [id, scope] of Object.entries(LEGACY)) {
    const [tab, section] = expected[id];
    assert.deepEqual(resolveSettingsTab(id), { tab, section }, id);
    assert.equal(settingsTabScope(tab), scope, `${id} keeps the ${scope} scope`);
  }
});

test("new tab ids resolve to themselves and unknown ids are rejected", () => {
  for (const tab of SETTINGS_TABS) assert.equal(resolveSettingsTab(tab.id).tab, tab.id);
  assert.deepEqual(resolveSettingsTab("agents"), { tab: "agents", section: "" });
  assert.deepEqual(resolveSettingsTab("personalAI"), { tab: "personalAI", section: "" });
  // "resources" was the merged Resources & access tab; it keeps naming Resources.
  assert.deepEqual(resolveSettingsTab("resources"), { tab: "resources", section: "" });
  assert.deepEqual(resolveSettingsTab("access"), { tab: "access", section: "" });
  assert.deepEqual(resolveSettingsTab("appearanceOverride"), { tab: "display", section: "appearanceOverride" });
  // T-0036: graph settings by graph type; personal overrides sit next to
  // the personal palette, and the old browser-only place keeps a pointer.
  assert.deepEqual(resolveSettingsTab("graphOverride"), { tab: "display", section: "graphOverride" });
  assert.deepEqual(resolveSettingsTab("graph"), { tab: "appearance", section: "graph" });
  for (const id of ["", null, undefined, "nope", "constructor", "__proto__"]) assert.equal(resolveSettingsTab(id), null);
});

test("About is a top-level page next to the overview; old links and remembered tabs open it there", () => {
  assert.deepEqual(SETTINGS_TABS.filter((tab) => tab.scope === "overview").map((tab) => tab.id), ["overview", "app", "about"]);
  // App: the desktop app's own settings (the view drops it outside the app).
  assert.deepEqual(settingsTarget("app"), { tab: "app", section: "", scope: "overview" });
  assert.equal(SETTINGS_TABS.some((tab) => tab.scope === "personal" && tab.sections.includes("about")), false);
  // ?settings=about and a remembered ark.settingsTab "about" (from Personal).
  assert.deepEqual(resolveSettingsTab("about"), { tab: "about", section: "" });
  assert.deepEqual(settingsTarget("about"), { tab: "about", section: "", scope: "overview" });
  assert.deepEqual(settingsTarget("shortcuts"), { tab: "display", section: "shortcuts", scope: "personal" });
  assert.equal(settingsTarget("nope"), null);
});
