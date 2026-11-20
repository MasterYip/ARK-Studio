// Pure logic for the setup "Review & confirm" dialog
// (views/setup-resources.js openSetupReview).  Page 1 is an overview of the
// project layer and the caller's own layer, page 2 the changes since each
// layer's last confirmation with the one Confirm action, which covers every
// proposed layer this user may confirm.  No DOM, no API calls.

export const REVIEW_LAYERS = ["project", "user"];
const DONE = new Set(["confirmed", "verified"]);

function layerInfo(state, layer) {
  if (layer === "project") return state?.layers?.project || (state?.state ? { state: state.state } : null);
  return state?.layers?.user || null;
}

/**
 * What a page can do: "confirm" (proposed and this user may confirm),
 * "readonly" (proposed, a project admin confirms), "done" (already
 * confirmed), "draft" (an agent saved a draft, or the confirmed setup
 * changed, and it has not been proposed yet), "pending" (nothing recorded
 * yet) or "none" (no such layer).
 */
export function pageMode(state, layer) {
  const info = layerInfo(state, layer);
  if (!info) return "none";
  const value = info.state || "unset";
  if (DONE.has(value)) return "done";
  if (value === "proposed") return info.can_confirm === false ? "readonly" : "confirm";
  if (value === "draft" || value === "stale") return "draft";
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

/**
 * The pages worth showing: a layer with nothing recorded (unset, or no
 * such layer) is left out, unless no page would remain.
 */
export function visiblePages(pages) {
  const shown = pages.filter((page) => page.mode !== "none" && page.state !== "unset");
  return shown.length ? shown : pages.slice(0, 1);
}

/** A layer waits for this user's confirmation (Home: "Review & confirm", Needs you). */
export function needsConfirm(state) {
  return reviewPages(state).some((page) => page.mode === "confirm");
}

/** Home opens the review for a layer awaiting this user, or an admin's draft. */
export function needsReview(state) {
  const pages = reviewPages(state);
  if (pages.some((page) => page.mode === "confirm")) return true;
  const project = layerInfo(state, "project");
  return project?.state === "draft" && project.can_confirm !== false;
}

export function stepLabel(index, total) {
  return `${index + 1}/${total}`;
}

/**
 * What one Confirm does: `confirm` the proposed layers this user may
 * confirm ([{layer, digest}], sent together), `admin` the proposed layers
 * only a project admin confirms, `drafts` the layers with changes that were
 * not sent for confirmation.  A confirmed layer needs nothing.
 */
export function confirmPlan(state) {
  const plan = { confirm: [], admin: [], drafts: [] };
  for (const page of reviewPages(state)) {
    if (page.mode === "confirm") plan.confirm.push({ layer: page.layer, digest: page.digest });
    else if (page.mode === "readonly") plan.admin.push(page.layer);
    else if (page.mode === "draft") plan.drafts.push(page.layer);
  }
  return plan;
}

/** The Confirm button's label key for a plan ("" when there is nothing to confirm). */
export function confirmLabelKey(plan) {
  const layers = plan.confirm.map((item) => item.layer);
  if (layers.length > 1) return "setupreview.confirm.both";
  if (layers[0] === "project") return "setupreview.confirm.project";
  if (layers[0] === "user") return "setupreview.confirm.user";
  return "";
}

/**
 * Combined result per layer: "confirmed" (in this dialog), "already",
 * "admin" (waiting for a project admin), "draft" (saved, not proposed),
 * "pending" (nothing recorded yet),
 * "skipped" (confirmable but not confirmed) or "none".
 */
export function reviewOutcome(pages, confirmed = new Set()) {
  return pages.map((page) => {
    if (confirmed.has(page.layer)) return { layer: page.layer, result: "confirmed" };
    const result = { done: "already", readonly: "admin", draft: "draft", pending: "pending", none: "none", confirm: "skipped" }[page.mode];
    return { layer: page.layer, result };
  });
}

// ------------------------------------------------------- plain summary
// The confirmation leads with what the agent may do, in plain words; the
// full technical profile (summary_markdown) stays behind "Details".
export const POLICY_ORDER = ["commit", "push", "merge", "destructive", "process_termination", "gpu_dispatch",
  "deployment", "data_transfer", "external_message", "purchase"];

function orderedKeys(keys) {
  const rank = (key) => { const i = POLICY_ORDER.indexOf(key); return i < 0 ? 99 : i; };
  return [...keys].sort((a, b) => rank(a) - rank(b) || a.localeCompare(b));
}

/**
 * What a layer says, as data for a plain-language summary:
 * `{objective, allow, ask, deny, resources: [{kind, names}], questions}`.
 * Project page: the project policies (allow / ask / deny) and the project
 * resources.  My page: my own resources and the rules I tightened.
 */
export function plainSummary(state, layer = "project") {
  const policies = (layer === "user" ? state?.policies?.user : state?.policies?.project || state?.policies?.effective) || {};
  const by = { allow: [], ask: [], deny: [] };
  for (const [key, value] of Object.entries(policies)) {
    if (by[value]) by[value].push(key);
  }
  const kinds = new Map();
  const me = state?.viewer?.user || "";
  for (const item of Array.isArray(state?.resources) ? state.resources : []) {
    if ((item.layer || "project") !== layer) continue;
    if (layer === "user" && me && item.owner && item.owner !== me) continue; // other members' layers
    if (!kinds.has(item.kind)) kinds.set(item.kind, []);
    kinds.get(item.kind).push(item.name || item.id);
  }
  return {
    objective: layer === "project" ? String(state?.profile?.objective || "").trim() : "",
    allow: orderedKeys(by.allow),
    ask: orderedKeys(by.ask),
    deny: orderedKeys(by.deny),
    resources: [...kinds].map(([kind, names]) => ({ kind, names })),
    questions: layer === "project" && Array.isArray(state?.questions) ? state.questions.length : 0,
  };
}

// ---------------------------------------------------------- changes
// layers.<layer>.changes (server setup.Changes): what changed since the
// layer's last confirmed version.

/**
 * Rows for the change list: resources (with the locations added to or
 * removed from them), project fields, policies, and locations of resources
 * that did not change themselves.  `names` maps resource keys to names.
 */
export function changeRows(changes, names = new Map()) {
  const c = changes || {};
  const rows = [];
  const byKey = new Map();
  for (const item of Array.isArray(c.resources) ? c.resources : []) {
    const row = { type: "resource", change: item.change, key: item.key, id: item.id, kind: item.kind,
      name: item.name || item.id, fields: Array.isArray(item.fields) ? item.fields : [], locations: [] };
    byKey.set(item.key, row);
    rows.push(row);
  }
  for (const item of Array.isArray(c.fields) ? c.fields : []) {
    rows.push({ type: "field", change: changeOf(item), field: item.field, old: item.old ?? "", new: item.new ?? "" });
  }
  for (const item of Array.isArray(c.policies) ? c.policies : []) {
    rows.push({ type: "policy", change: changeOf(item), field: item.field, old: item.old ?? "", new: item.new ?? "" });
  }
  for (const loc of Array.isArray(c.locations) ? c.locations : []) {
    const owner = byKey.get(loc.resource);
    const entry = { change: loc.change, resource: loc.resource, host: loc.host_label || loc.host_id || "",
      path: loc.path_or_ref || "", name: names.get(loc.resource) || String(loc.resource || "").replace(/^(project|u:[^/]+)\//, "") };
    if (owner && owner.change !== "removed") owner.locations.push(entry);
    else if (!owner) rows.push({ type: "location", ...entry });
  }
  return rows;
}

function changeOf(item) {
  if (!item.old && item.new) return "added";
  if (item.old && !item.new) return "removed";
  return "changed";
}

/**
 * The changes of each layer in the review, grouped by layer:
 * [{layer, state, mode, baseline, rows}].  Layers with nothing recorded are
 * left out.  `baseline` "none" means the layer was never confirmed, so all
 * of it is new and no rows are marked.
 */
export function changeGroups(state) {
  const names = new Map((Array.isArray(state?.resources) ? state.resources : []).map((item) => [item.key, item.name || item.id]));
  return visiblePages(reviewPages(state)).filter((page) => page.mode !== "none").map((page) => {
    const changes = state?.layers?.[page.layer]?.changes || null;
    const baseline = changes?.baseline || "none";
    return { layer: page.layer, state: page.state, mode: page.mode, baseline,
      rows: baseline === "none" ? [] : changeRows(changes, names) };
  });
}
