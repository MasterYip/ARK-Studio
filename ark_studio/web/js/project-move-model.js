// Pure helpers for splitting and merging projects (project-move.js; server:
// store/move.go, store/clusters.go).  No DOM here so the rules are testable
// in Node.

// A cross-project reference in a body: [[prj_xxx/T-0042]].
const CROSS_REF = /^(prj_[A-Za-z0-9]+)\/([A-Za-z0-9][A-Za-z0-9_-]*)$/;

export function parseCrossRef(ref) {
  const m = CROSS_REF.exec(String(ref || ""));
  return m ? { project: m[1], uid: m[2] } : null;
}

export function formatBytes(bytes) {
  const n = Number(bytes) || 0;
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024, i = 0;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i += 1; }
  return `${v >= 10 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`;
}

// The projects this one can merge into: every listed project but itself.
export function moveTargets(projects, currentId) {
  return (projects || []).filter((p) => p && p.id && p.id !== currentId && !p.demo)
    .map((p) => ({ id: p.id, name: p.name || p.id }));
}

export function splitRequest({ roots, newName, crossLinks = "keep", planHash = "" }) {
  const body = { mode: "split", units: [...roots].map(String), cross_links: crossLinks, new_project: { name: String(newName || "").trim() } };
  if (planHash) body.plan_hash = planHash;
  return body;
}

export function mergeRequest({ target, planHash = "" }) {
  const body = { mode: "merge", target_project: target };
  if (planHash) body.plan_hash = planHash;
  return body;
}

// Split candidates: the clusters a person can tick, most separable first
// (fewest crossing links per unit), leaving out the one holding most of the
// project and the one-unit leftovers.  `weak` marks a cluster with at most
// a few crossing links.
export function splitCandidates(data, { minUnits = 3, weakLinks = 3 } = {}) {
  const clusters = (data?.clusters || []).map((c, index) => ({
    index, units: c.units || 0, cut: (c.cut_links || 0) + (c.hub_links || 0), roots: c.roots || [],
  }));
  const total = data?.units || clusters.reduce((n, c) => n + c.units, 0);
  return clusters
    .filter((c) => c.units >= minUnits && c.units * 2 <= total)
    .map((c) => ({ ...c, weak: c.cut <= weakLinks }))
    .sort((a, b) => a.cut / a.units - b.cut / b.units || b.units - a.units);
}

// The cluster that holds a unit's branch root, if any.
export function clusterOfRoot(candidates, uid) {
  return candidates.find((c) => c.roots.some((r) => r.uid === uid)) || null;
}

// The preview, as plain rows: [key, params] pairs for t(), plus the uid
// table and the cut links.  Notes only appear when they apply.
export function planRows(plan) {
  const p = plan || {};
  const rows = [
    ["project_move.sum_units", { n: p.unit_count || 0 }],
    [p.renumbered ? "project_move.sum_renumber" : "project_move.sum_keep", { n: p.renumbered || 0 }],
    ["project_move.sum_links", { n: p.links || 0 }],
    ["project_move.sum_media", { n: p.media || 0, files: p.files || 0, size: formatBytes(p.bytes) }],
    ["project_move.sum_comments", { n: p.comments || 0 }],
  ];
  const notes = [];
  if (p.media_borrowed) notes.push(["project_move.note_borrowed", { n: p.media_borrowed }]);
  if (p.text_history) notes.push(["project_move.note_history", { n: p.text_history }]);
  if (p.review_groups) notes.push(["project_move.note_review", { n: p.review_groups }]);
  if (p.files_missing) notes.push(["project_move.note_missing", { n: p.files_missing }]);
  const renamed = (p.units || []).filter((u) => u.uid !== u.new_uid);
  return { rows, notes, renamed, cross: p.cross_links || [] };
}

// Node details: where a node came from or went (moves, imports) and its
// links to nodes in other projects, as rows for project-move.js.
export function crossProjectRows(meta) {
  const m = meta && typeof meta === "object" ? meta : {};
  const rows = [];
  const add = (kind, item, extra = {}) => {
    if (item && item.project && item.uid) {
      rows.push({ kind, project: item.project, name: item.project_name || item.project, uid: item.uid, at: item.at || "", ...extra });
    }
  };
  add("moved_from", m.moved_from);
  add("moved_to", m.moved_to);
  add("imported_from", m.imported_from);
  for (const link of Array.isArray(m.external_links) ? m.external_links : []) {
    add("link", link, { relation: link.relation || "relates_to", direction: link.direction === "in" ? "in" : "out", title: link.title || "" });
  }
  return rows;
}

