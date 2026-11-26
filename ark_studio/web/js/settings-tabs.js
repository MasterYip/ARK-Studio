// Settings navigation model: which sections each Settings tab stacks, and how
// the section ids used before the regrouping (deep links ?settings=<id>,
// localStorage ark.settingsTab, guidance steps) resolve to a tab + section.
// Pure data so tests can check it without a DOM.

// Scopes: "overview" is the first screen (what lives where, plus shortcuts),
// App (the ARK Studio desktop app's own settings; only inside the app) and
// About ARK Studio (version, updates, links; it used to sit under Personal:
// old links and remembered tabs open it here);
// "project" is shared with every collaborator; "personal" follows your
// account, except the "browser" tab, which is stored only in this browser.
// Graph display settings live under Appearance by graph type (T-0036): the
// project defaults in the "graph" section, a person's overrides in
// "graphOverride".  Node marker groups (project) are the "markers" section.
// Tabs marked `advanced` are listed under an "Advanced" heading.
export const SETTINGS_TABS = [
  { id: "overview", scope: "overview", sections: ["overview"] },
  { id: "app", scope: "overview", sections: ["app"] },
  { id: "about", scope: "overview", sections: ["about"] },
  { id: "appearance", scope: "project", sections: ["appearance", "graph", "markers"] },
  // Lines of work (branch:<name> tags) and scheduled Assistant tasks.
  { id: "lines", scope: "project", sections: ["lines"] },
  { id: "schedules", scope: "project", sections: ["schedules"] },
  { id: "agents", scope: "project", sections: ["projectAgents", "agentRules", "projectAssistant", "providers", "skills"] },
  { id: "data", scope: "project", sections: ["data", "blackbox", "deleteProject"] },
  { id: "trash", scope: "project", sections: ["trash"] },
  { id: "members", scope: "project", sections: ["members"] },
  { id: "resources", scope: "project", advanced: true, sections: ["setupResources", "gpu"] },
  { id: "access", scope: "project", advanced: true, sections: ["mobile", "sessionUploads"] },
  { id: "profile", scope: "personal", sections: ["profile", "account", "notifications", "environment"] },
  { id: "display", scope: "personal", sections: ["display", "advanced", "appearanceOverride", "graphOverride", "shortcuts", "guidance"] },
  { id: "personalAI", scope: "personal", sections: ["assistant", "personalAgents", "personalAgentRules", "agentAccess"] },
  { id: "plugins", scope: "personal", sections: ["plugins"] },
  { id: "browser", scope: "personal", sections: ["infohubSource", "remote"] },
  { id: "storage", scope: "admin", sections: ["projectStorage"] },
  { id: "admin", scope: "admin", sections: ["admin"] },
];

export const DEFAULT_SETTINGS_TAB = "overview";

// The section that owns the graph Save/Discard/Reset controls.
export const GRAPH_SECTION = "graph";

const SECTION_TAB = new Map(SETTINGS_TABS.flatMap((tab) => tab.sections.map((section) => [section, tab.id])));
const TAB_IDS = new Set(SETTINGS_TABS.map((tab) => tab.id));

/**
 * Resolve a requested id (new tab id or a pre-regrouping section id) to
 * `{ tab, section }`.  `section` is set when the id named a section that is
 * not the first one of its tab, so the caller can scroll it into view.
 * Unknown ids resolve to `null`.
 */
export function resolveSettingsTab(id) {
  const key = String(id || "");
  if (!key) return null;
  const tabId = SECTION_TAB.get(key) || (TAB_IDS.has(key) ? key : "");
  if (!tabId) return null;
  const tab = SETTINGS_TABS.find((item) => item.id === tabId);
  const section = SECTION_TAB.has(key) && tab.sections[0] !== key ? key : "";
  return { tab: tabId, section };
}

export function settingsTabScope(id) {
  return SETTINGS_TABS.find((tab) => tab.id === id)?.scope || "";
}

/**
 * Where to open Settings for `id` (a tab or section id): `{ tab, scope, section }`
 * for ark.settingsTab / ark.settingsScope, or null for unknown ids.
 */
export function settingsTarget(id) {
  const resolved = resolveSettingsTab(id);
  return resolved ? { ...resolved, scope: settingsTabScope(resolved.tab) } : null;
}
