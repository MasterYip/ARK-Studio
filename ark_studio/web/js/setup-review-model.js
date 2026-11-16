// Pure paging logic for the setup "Review & confirm" dialog
// (views/setup-resources.js openSetupReview).  Page 1 is the project layer
// (admins confirm; others see it read-only), page 2 the caller's own layer,
// then a combined result.  No DOM, no API calls.

export const REVIEW_LAYERS = ["project", "user"];
const DONE = new Set(["confirmed", "verified"]);

function layerInfo(state, layer) {
  if (layer === "project") return state?.layers?.project || (state?.state ? { state: state.state } : null);
  return state?.layers?.user || null;
}

/**
 * What a page can do: "confirm" (proposed and this user may confirm),
 * "readonly" (proposed, a project admin confirms), "done" (already
 * confirmed), "pending" (nothing proposed yet) or "none" (no such layer).
 */
export function pageMode(state, layer) {
  const info = layerInfo(state, layer);
  if (!info) return "none";
  const value = info.state || "unset";
  if (DONE.has(value)) return "done";
  if (value === "proposed") return info.can_confirm === false ? "readonly" : "confirm";
  return "pending";
}

export function reviewPages(state) {
  return REVIEW_LAYERS.map((layer) => {
    const info = layerInfo(state, layer) || {};
    return {
      layer,
      mode: pageMode(state, layer),
      state: info.state || "unset",
      digest: info.digest || "",
      canConfirm: pageMode(state, layer) === "confirm",
    };
  });
}

/** Page to open first: the requested layer, else the first confirmable one. */
export function firstPage(pages, preferLayer = "") {
  const wanted = pages.findIndex((page) => page.layer === preferLayer);
  if (wanted >= 0) return wanted;
  const confirmable = pages.findIndex((page) => page.mode === "confirm");
  return confirmable >= 0 ? confirmable : 0;
}

/** Home shows "Review & confirm" when a layer awaits this user (or an admin's draft). */
export function needsReview(state) {
  const pages = reviewPages(state);
  if (pages.some((page) => page.mode === "confirm")) return true;
  const project = layerInfo(state, "project");
  return project?.state === "draft" && project.can_confirm !== false;
}

export function stepLabel(index, total) {
  return `${index + 1}/${total}`;
}

/** Next page after confirming page `index`: the following page, or the result. */
export function nextIndex(index, total) {
  return Math.min(index + 1, total);
}

/**
 * Combined result per layer: "confirmed" (in this dialog), "already",
 * "admin" (waiting for a project admin), "pending" (not proposed yet),
 * "skipped" (confirmable but not confirmed) or "none".
 */
export function reviewOutcome(pages, confirmed = new Set()) {
  return pages.map((page) => {
    if (confirmed.has(page.layer)) return { layer: page.layer, result: "confirmed" };
    const result = { done: "already", readonly: "admin", pending: "pending", none: "none", confirm: "skipped" }[page.mode];
    return { layer: page.layer, result };
  });
}
