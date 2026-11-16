import assert from "node:assert/strict";
import test from "node:test";

import {
  SETTINGS_TABS, DEFAULT_SETTINGS_TAB, GRAPH_SECTION, resolveSettingsTab, settingsTabScope,
} from "../../ark_studio/web/js/settings-tabs.js";

// Section ids used as Settings tab ids before the regrouping, with the scope
// each lived in.  Every one must still resolve, and keep its scope.
const LEGACY = {
  appearance: "project", graph: "project", projectAgents: "project", projectAssistant: "project",
  setupResources: "project", skills: "project", providers: "project", data: "project",
  blackbox: "project", mobile: "project", remote: "project", gpu: "project", deleteProject: "project",
  profile: "personal", display: "personal", guidance: "personal", assistant: "personal",
  personalAgents: "personal", account: "personal", agentAccess: "personal", environment: "personal",
  shortcuts: "personal", about: "personal", admin: "admin",
};

test("settings are grouped into a few tabs per scope", () => {
  const count = (scope) => SETTINGS_TABS.filter((tab) => tab.scope === scope).length;
  assert.deepEqual(SETTINGS_TABS.map((tab) => tab.id),
    ["appearance", "agents", "resources", "access", "data", "profile", "display", "personalAI", "about", "admin"]);
  assert.equal(count("project"), 5);
  assert.equal(count("personal"), 4);
  assert.equal(count("admin"), 1);
  assert.ok(SETTINGS_TABS.some((tab) => tab.id === DEFAULT_SETTINGS_TAB));
  const sections = SETTINGS_TABS.flatMap((tab) => tab.sections);
  assert.equal(new Set(sections).size, sections.length, "each section lives in exactly one tab");
  // Sections added after the regrouping (no legacy tab id).
  assert.deepEqual([...sections].sort(), [...Object.keys(LEGACY), "appearanceOverride", "sessionUploads"].sort());
  assert.equal(resolveSettingsTab("sessionUploads").tab, "access");
  assert.equal(SETTINGS_TABS.find((tab) => tab.sections.includes(GRAPH_SECTION)).id, "appearance");
  assert.equal(SETTINGS_TABS.find((tab) => tab.id === "data").sections.at(-1), "deleteProject");
});

test("every old settings id maps to its new tab, keeps its scope and names the section to reveal", () => {
  const expected = {
    appearance: ["appearance", ""], graph: ["appearance", "graph"],
    projectAgents: ["agents", ""], projectAssistant: ["agents", "projectAssistant"],
    providers: ["agents", "providers"], skills: ["agents", "skills"],
    setupResources: ["resources", ""], gpu: ["resources", "gpu"], remote: ["access", "remote"],
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
  for (const id of ["", null, undefined, "nope", "constructor", "__proto__"]) assert.equal(resolveSettingsTab(id), null);
});
