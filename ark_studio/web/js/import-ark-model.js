// Pure helpers for importing into Ideas from another source (import-ark.js;
// server: routes_importark.go, store/importark.go).  No DOM here, so the
// rules are testable in Node.

import { emptyFilters, filtersToParams, normalizeFilters } from "./state.js";

export const INFOHUB_SOURCE = "infohub";

// A cross-project reference in a body: [[prj_xxx/T-0042]].
const CROSS_REF = /^(prj_[A-Za-z0-9]+)\/([A-Za-z0-9][A-Za-z0-9_-]*)$/;

export function parseCrossRef(ref) {
  const m = CROSS_REF.exec(String(ref || ""));
  return m ? { project: m[1], uid: m[2] } : null;
}

// The sources the Import pane offers: InfoHub (when shown) and every other
// project in the person's list (the list only has projects they can read).
export function importSources(projects, currentId, { infohub = true } = {}) {
  const out = infohub ? [{ id: INFOHUB_SOURCE, name: "InfoHub", kind: "infohub" }] : []; // i18n-ignore: product name
  for (const p of projects || []) {
    if (!p?.id || p.id === currentId || p.demo) continue;
    out.push({ id: p.id, name: p.name || p.id, kind: "ark" });
  }
  return out;
}

// The saved source if it is still offered, else the first one ("" if none).
export function pickImportSource(saved, sources) {
  if (saved && sources.some((s) => s.id === saved)) return saved;
  return sources[0]?.id || "";
}

export function isArkSource(id) { return Boolean(id) && id !== INFOHUB_SOURCE; }

// Query of GET /api/import-ark/graph: the ARK filter model, as /api/graph takes it.
export function importGraphParams(projectId, filters, { limit = 1000 } = {}) {
  const f = normalizeFilters(filters || emptyFilters());
  const params = filtersToParams(f, { project: projectId, limit });
  if (f.context) params.context = "1";
  return params;
}

export function importRequest({ project, uids, withChildren = false, lang = "en" }) {
  return { project, units: [...new Set((uids || []).map(String).filter(Boolean))], with_children: Boolean(withChildren), lang };
}

export function formatBytes(bytes) {
  const n = Number(bytes) || 0;
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB"];
  let v = n / 1024, i = 0;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i += 1; }
  return `${v >= 10 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`;
}

// The preview, as [i18n key, params] rows; notes only when they apply.
export function importSummaryRows(summary) {
  const s = summary || {};
  const rows = [["import_ark.sum_units", { n: s.unit_count || 0 }]];
  rows.push([s.renumbered ? "import_ark.sum_renumber" : "import_ark.sum_keep", { n: s.renumbered || 0 }]);
  if (s.links) rows.push(["import_ark.sum_links", { n: s.links }]);
  if (s.media) rows.push(["import_ark.sum_media", { n: s.media, files: s.files || 0, size: formatBytes(s.bytes) }]);
  if (s.skipped) rows.push(["import_ark.sum_skipped", { n: s.skipped }]);
  if (s.files_missing) rows.push(["import_ark.sum_missing", { n: s.files_missing }]);
  return rows;
}

// Source nodes that already have a copy in this project: greyed, with the
// copy's uid (GET /api/import-ark/graph "imported": {source uid: copy uid}).
export const IMPORTED_COLOR = "#a3acb9";

export function markImported(nodes, imported) {
  const map = imported && typeof imported === "object" ? imported : {};
  let count = 0;
  const out = (nodes || []).map((node) => {
    const copy = map[node.uid];
    if (!copy) return node;
    count += 1;
    return { ...node, importedAs: copy, customColor: IMPORTED_COLOR };
  });
  return { nodes: out, count };
}

