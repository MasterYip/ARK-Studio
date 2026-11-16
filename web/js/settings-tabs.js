// Settings navigation model: which sections each Settings tab stacks, and how
// the section ids used before the regrouping (deep links ?settings=<id>,
// localStorage ark.settingsTab, guidance steps) resolve to a tab + section.
// Pure data so tests can check it without a DOM.

export const SETTINGS_TABS = [
  { id: "appearance", scope: "project", sections: ["appearance", "graph"] },
  { id: "agents", scope: "project", sections: ["projectAgents", "projectAssistant", "providers", "skills"] },
  { id: "resources", scope: "project", sections: ["setupResources", "gpu"] },
  { id: "access", scope: "project", sections: ["mobile", "remote", "sessionUploads"] },
  { id: "data", scope: "project", sections: ["data", "blackbox", "deleteProject"] },
  { id: "profile", scope: "personal", sections: ["profile", "account", "environment"] },
  { id: "display", scope: "personal", sections: ["display", "appearanceOverride", "shortcuts", "guidance"] },
  { id: "personalAI", scope: "personal", sections: ["assistant", "personalAgents", "agentAccess"] },
  { id: "about", scope: "personal", sections: ["about"] },
  { id: "admin", scope: "admin", sections: ["admin"] },
];

export const DEFAULT_SETTINGS_TAB = "appearance";

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
