import { t, hasKey } from "./i18n.js";

// Node markers: the small icons drawn next to a node.  A marker on a node is
// the tag "marker:<id>".  Markers come in groups (project setting
// settings.markers, edited in Settings › Appearance › Node markers; without
// one the defaults below, which ark_studio/server/internal/store/markers keeps
// too).  In an exclusive group a node has at most one marker: choosing one
// replaces the others of that group.  Choosing a marker the node already has
// removes it.  The server applies the same rules to every write.

export const MARKER_NS = "marker";

const m = (id, glyph, color) => ({ id, glyph, color, label: "" });

export const DEFAULT_MARKER_GROUPS = [
  { id: "star", name: "", exclusive: true, markers: [
    m("star-yellow", "★", "#f59e0b"), m("star-red", "★", "#ef4444"),
    m("star-blue", "★", "#3b82f6"), m("star-green", "★", "#22c55e")] },
  { id: "flag", name: "", exclusive: true, markers: [
    m("flag-red", "⚑", "#ef4444"), m("flag-orange", "⚑", "#f97316"),
    m("flag-blue", "⚑", "#3b82f6"), m("flag-green", "⚑", "#22c55e")] },
  { id: "priority", name: "", exclusive: true, markers: [
    m("priority-1", "①", "#ef4444"), m("priority-2", "②", "#f97316"), m("priority-3", "③", "#3b82f6")] },
  { id: "progress", name: "", exclusive: true, markers: [
    m("progress-0", "○", "#64748b"), m("progress-25", "◔", "#0ea5e9"), m("progress-50", "◑", "#0ea5e9"),
    m("progress-75", "◕", "#0ea5e9"), m("progress-100", "●", "#22c55e")] },
  { id: "symbol", name: "", exclusive: false, markers: [
    m("check-green", "✓", "#22c55e"), m("question", "?", "#38bdf8"), m("priority", "!", "#fb923c")] },
];

// Limits and patterns of internal/store/markers (Parse).
export const MARKER_LIMITS = { groups: 12, markers: 16, name: 40, glyph: 3 };
export const MARKER_ID_RE = /^[a-z0-9][a-z0-9-]{0,39}$/;
const COLOR_RE = /^#[0-9a-fA-F]{6}$/;

const builtinKey = (id) => `markers.${id.replaceAll("-", "_")}`;

/** Shown name of a group: its own name, else the built-in one, else the id. */
export function groupName(group) {
  if (group?.name) return group.name;
  const key = `markers.group.${group?.id}`;
  return hasKey(key) ? t(key) : String(group?.id || "");
}

/** Shown label of a marker: its own label, else the built-in one, else the id. */
export function markerLabel(marker) {
  if (marker?.label) return marker.label;
  const key = builtinKey(String(marker?.id || ""));
  return hasKey(key) ? t(key) : String(marker?.id || "");
}

const clone = (groups) => groups.map((group) => ({
  ...group, markers: group.markers.map((marker) => ({ ...marker })),
}));

/** The groups a project uses: its setting ({groups}) or the defaults. */
export function effectiveMarkerGroups(setting) {
  const groups = Array.isArray(setting?.groups) && setting.groups.length ? setting.groups : DEFAULT_MARKER_GROUPS;
  return clone(groups);
}

function build(groups) {
  const byTag = new Map();
  const resolved = groups.map((group) => {
    const out = { id: group.id, name: group.name || "", exclusive: Boolean(group.exclusive), markers: [] };
    Object.defineProperty(out, "label", { get() { return groupName(out); }, enumerable: false });
    for (const raw of group.markers || []) {
      const marker = {
        id: raw.id, tag: `${MARKER_NS}:${raw.id}`, glyph: raw.glyph, color: raw.color, own: raw.label || "", group: out,
        // A getter, so menus built later pick up the current language.
        get label() { return markerLabel({ id: this.id, label: this.own }); },
      };
      out.markers.push(marker);
      byTag.set(marker.tag, marker);
    }
    return out;
  });
  return { groups: resolved, byTag, key: JSON.stringify(groups) };
}

let active = build(DEFAULT_MARKER_GROUPS);

/** Use a project's setting; true when the groups changed. */
export function setMarkerGroups(setting) {
  const next = build(effectiveMarkerGroups(setting));
  if (next.key === active.key) return false;
  active = next;
  return true;
}

/** The active groups ({id, label, exclusive, markers: [{id, tag, glyph, color, label, group}]}). */
export function markerGroups() {
  return active.groups;
}

/** Every marker of the active groups, in group order. */
export function allMarkers() {
  return active.groups.flatMap((group) => group.markers);
}

export function markerForTag(tag) {
  return active.byTag.get(tag) || null;
}

export function isMarkerTag(tag) {
  return String(tag || "").startsWith(`${MARKER_NS}:`);
}

/**
 * The markers drawn for a node, in tag order.  A node saved before groups
 * existed may hold several markers of one exclusive group: it shows one
 * (the one that sorts last, the one the server keeps on its next write).
 */
export function markersFor(node) {
  const list = (node?.tags || []).map(markerForTag).filter(Boolean);
  const last = new Map();
  for (const marker of list) if (marker.group.exclusive) last.set(marker.group.id, marker);
  const seen = new Set();
  return list.filter((marker) => {
    if (seen.has(marker.tag)) return false;
    seen.add(marker.tag);
    return !marker.group.exclusive || last.get(marker.group.id) === marker;
  });
}

/** "all" | "some" | "none": whether the nodes show `tag`. */
export function markerState(nodes, tag) {
  const list = nodes || [];
  const count = list.filter((node) => markersFor(node).some((marker) => marker.tag === tag)).length;
  if (!count) return "none";
  return count === list.length ? "all" : "some";
}

/**
 * The tag change for choosing `tag` on `nodes`: when every node shows it,
 * it is removed; otherwise it is added to all of them, and in an exclusive
 * group the group's other markers are removed.
 * @returns {{ add: string[], remove: string[] }}
 */
export function toggleMarkerChange(nodes, tag) {
  const marker = markerForTag(tag);
  if (!marker) return { add: [], remove: [] };
  if (markerState(nodes, tag) === "all") return { add: [], remove: [tag] };
  const remove = marker.group.exclusive
    ? marker.group.markers.map((item) => item.tag).filter((other) => other !== tag
      && (nodes || []).some((node) => (node.tags || []).includes(other)))
    : [];
  return { add: [tag], remove };
}

/**
 * The tag change removing every marker of the nodes, or only `tag` (with,
 * for an exclusive group, the hidden extra markers of that group a node
 * saved before the rule may still hold).
 */
export function removeMarkersChange(nodes, tag = null) {
  const present = new Set();
  for (const node of nodes || []) for (const item of node.tags || []) if (isMarkerTag(item)) present.add(item);
  if (!tag) return { add: [], remove: [...present] };
  const group = markerForTag(tag)?.group;
  const remove = [...present].filter((item) => item === tag
    || (group?.exclusive && markerForTag(item)?.group === group));
  return { add: [], remove };
}

/**
 * Context-menu items for markers on `nodes` (graph menu, inspector picker):
 * a heading per group, a check on markers every node has (choosing one
 * again removes it; another of an exclusive group replaces it), then Remove
 * marker and Clear markers.  `apply(change)` saves a change.
 */
export function markerMenuChildren(nodes, apply) {
  const children = [];
  for (const group of markerGroups()) {
    children.push({ heading: group.label });
    for (const marker of group.markers) {
      const state = markerState(nodes, marker.tag);
      children.push({
        label: `${marker.glyph} ${marker.label}`,
        checked: state === "all",
        hint: state === "some" ? t("graph.menu.marker_some") : "",
        onClick: () => apply(toggleMarkerChange(nodes, marker.tag)),
      });
    }
  }
  const present = presentMarkers(nodes);
  children.push({ separator: true });
  children.push({
    label: t("graph.menu.remove_marker"), icon: "x", disabled: !present.length,
    children: present.length ? present.map((marker) => ({
      label: `${marker.glyph} ${marker.label}`,
      onClick: () => apply(removeMarkersChange(nodes, marker.tag)),
    })) : undefined,
  });
  children.push({
    label: t("graph.menu.clear_markers"), icon: "trash", disabled: !present.length,
    onClick: () => apply(removeMarkersChange(nodes)),
  });
  return children;
}

/** `tags` after a change (the server applies the same). */
export function applyMarkerChange(tags, { add = [], remove = [] } = {}) {
  const drop = new Set(remove);
  const out = (tags || []).filter((tag) => !drop.has(tag));
  for (const tag of add) if (!out.includes(tag)) out.push(tag);
  return out;
}

/** Markers present on any of the nodes, in group order. */
export function presentMarkers(nodes) {
  return allMarkers().filter((marker) => markerState(nodes, marker.tag) !== "none");
}

/**
 * Check a draft of groups for the settings editor; returns the clean
 * {groups} to save or throws an Error naming the problem (the server checks
 * the same, internal/store/markers Parse).
 */
export function cleanMarkerGroups(groups) {
  const list = groups || [];
  if (list.length > MARKER_LIMITS.groups) throw new Error(t("markers.error.too_many_groups", { n: MARKER_LIMITS.groups }));
  const groupIds = new Set();
  const markerIds = new Set();
  return {
    groups: list.map((group) => {
      const id = String(group.id || "").trim();
      if (!MARKER_ID_RE.test(id)) throw new Error(t("markers.error.group_id", { id }));
      if (groupIds.has(id)) throw new Error(t("markers.error.duplicate_group", { id }));
      groupIds.add(id);
      const name = String(group.name || "").trim();
      if ([...name].length > MARKER_LIMITS.name) throw new Error(t("markers.error.name_long"));
      const markers = group.markers || [];
      if (!markers.length) throw new Error(t("markers.error.no_markers", { group: groupName({ id, name }) }));
      if (markers.length > MARKER_LIMITS.markers) throw new Error(t("markers.error.too_many_markers", { n: MARKER_LIMITS.markers }));
      return {
        id, name, exclusive: Boolean(group.exclusive),
        markers: markers.map((marker) => {
          const mid = String(marker.id || "").trim();
          if (!MARKER_ID_RE.test(mid)) throw new Error(t("markers.error.marker_id", { id: mid }));
          if (markerIds.has(mid)) throw new Error(t("markers.error.duplicate_marker", { id: mid }));
          markerIds.add(mid);
          const glyph = String(marker.glyph || "").trim();
          if (!glyph || [...glyph].length > MARKER_LIMITS.glyph || /[\s\p{Cc}]/u.test(glyph)) {
            throw new Error(t("markers.error.glyph", { id: mid }));
          }
          const color = String(marker.color || "").trim();
          if (!COLOR_RE.test(color)) throw new Error(t("markers.error.color", { id: mid }));
          const label = String(marker.label || "").trim();
          if ([...label].length > MARKER_LIMITS.name) throw new Error(t("markers.error.name_long"));
          return { id: mid, glyph, color: color.toLowerCase(), label };
        }),
      };
    }),
  };
}

/** A free id like `${base}-2` that is not in `taken`. */
export function freeMarkerId(base, taken) {
  const used = new Set(taken);
  const stem = String(base || "marker").toLowerCase().replace(/[^a-z0-9-]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 30) || "marker";
  if (!used.has(stem)) return stem;
  for (let i = 2; ; i += 1) if (!used.has(`${stem}-${i}`)) return `${stem}-${i}`;
}

/**
 * Circular-node markers: an upper-right arc, clockwise in ~20° steps. The
 * marker size is derived from the node radius so the angular spacing stays
 * visually meaningful at every zoom level rather than collapsing to one blob.
 */
export function markerArc(markers, cx, cy, radius, start = -Math.PI / 4, step = Math.PI / 9) {
  return (markers || []).map((marker, index) => {
    const angle = start + index * step;
    return {
      marker,
      x: cx + Math.cos(angle) * radius,
      y: cy + Math.sin(angle) * radius,
    };
  });
}
