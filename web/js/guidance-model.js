export const GUIDANCE_STEPS = [
  { id: "home", workspace: "home" },
  { id: "prompts", workspace: "home", target: ".start-panel" },
  { id: "research", workspace: "research" },
  { id: "map", workspace: "map" },
  ...["force", "tree", "mind", "node"].map((id) => ({ id, surface: true, layout: id })),
  { id: "edit", surface: true, layout: "mind", practice: true },
  { id: "filter", surface: true, layout: "mind", practice: true },
  { id: "relations", surface: true, layout: "mind" },
  { id: "execute", workspace: "execute" },
  { id: "output", workspace: "output" },
  { id: "editor", workspace: "editor" },
  { id: "assistant", workspace: "assistant" },
  { id: "settings", workspace: "settings" },
  { id: "mobile", workspace: "settings", settingsTab: "access", target: ".android-apps" },
];

const STATUSES = new Set(["new", "paused", "dismissed", "completed"]);

export function guidanceKey(user = "local") {
  return `ark.guidance.v3:${encodeURIComponent(String(user || "local"))}`;
}

export function guidanceState(value) {
  if (!value || typeof value !== "object" || !STATUSES.has(value.status)) return { status: "new", step: 0 };
  return { status: value.status, step: Number.isInteger(value.step)
    ? Math.max(0, Math.min(GUIDANCE_STEPS.length - 1, value.step)) : 0 };
}

export function readGuidance(storage, key) {
  try { return guidanceState(JSON.parse(storage.getItem(key))); }
  catch { return guidanceState(null); }
}

export function saveGuidance(storage, key, state) {
  const value = guidanceState(state);
  try { storage.setItem(key, JSON.stringify(value)); } catch { /* private browsing */ }
  return value;
}
