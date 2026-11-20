// Five steps, one per stage of the core loop: capture (and connect your
// agent), plan, evidence, execute, review and accept.  Graph layouts, filters
// and phone pairing are taught where they are used (the tutorial chapters,
// Settings), not here.  `target` is the element the spotlight frames.
export const GUIDANCE_STEPS = [
  { id: "capture", workspace: "home", target: ".start-panel" },
  { id: "plan", workspace: "research" },
  { id: "evidence", workspace: "map" },
  { id: "execute", workspace: "execute" },
  { id: "review", workspace: "home", target: ".needs-panel" },
];

// The tutorial project (ARK Studio Demo).
export const TUTORIAL_PROJECT_ID = "ark-demo";

/**
 * What finishing the last step does: in the tutorial project it opens the
 * tutorial topic graph ("tutorial-topic"); elsewhere the guide just ends ("done").
 */
export function guidanceFinish(project) {
  return project?.id === TUTORIAL_PROJECT_ID ? "tutorial-topic" : "done";
}

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
