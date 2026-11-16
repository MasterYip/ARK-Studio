export const MAP_LIST_SORTS = [
  ["updated", "common.updated", "desc"], ["created", "common.created", "desc"],
  ["start", "common.start", "asc"], ["end", "common.due", "asc"],
  ["connections", "map.list.connections", "desc"],
  ["incoming", "map.list.incoming", "desc"], ["outgoing", "map.list.outgoing", "desc"],
  ["type", "common.type", "asc"], ["status", "common.status", "asc"],
  ["importance", "common.importance", "asc"], ["assignee", "assignee.label", "asc"],
  ["title", "common.title", "asc"], ["uid", "map.list.uid", "asc"],
];
export const MAP_LIST_GROUPS = ["none", "type", "status", "importance", "assignee"];
const TYPE_ORDER = ["question", "claim", "evidence", "experiment", "method", "idea", "story", "task", "decision"];
const STATUS_ORDER = ["inbox", "open", "active", "review", "blocked", "done", "abandoned", "archived"];
const DATE_FIELDS = { updated: "updated_at", created: "created_at", start: "start_at", end: "end_at" };

export function normalizeMapListPrefs(value = {}) {
  const sort = MAP_LIST_SORTS.some(([key]) => key === value?.sort) ? value.sort : "title";
  return {
    sort,
    direction: ["asc", "desc"].includes(value?.direction) ? value.direction : defaultMapListDirection(sort),
    group: MAP_LIST_GROUPS.includes(value?.group) ? value.group : "type",
  };
}

export function defaultMapListDirection(sort) {
  return MAP_LIST_SORTS.find(([key]) => key === sort)?.[2] || "asc";
}

function assignee(unit) { return unit.assignee_effective || unit.assignee || ""; }
function count(value) { const number = Number(value); return Number.isFinite(number) ? Math.max(0, number) : 0; }
function valueFor(unit, key) {
  if (DATE_FIELDS[key]) {
    const at = Date.parse(unit[DATE_FIELDS[key]] || "");
    return Number.isFinite(at) ? at : null;
  }
  if (key === "connections") return count(unit.in_count) + count(unit.out_count);
  if (key === "incoming") return count(unit.in_count);
  if (key === "outgoing") return count(unit.out_count);
  if (key === "assignee") return assignee(unit) || null;
  return String(unit[key] || "") || null;
}

function comparisons({ locale, types = [], statuses = [] } = {}) {
  const collator = new Intl.Collator(locale, { numeric: true, sensitivity: "base" });
  const ranks = {
    type: new Map([...new Set([...TYPE_ORDER, ...types])].map((key, i) => [key, i])),
    status: new Map([...new Set([...statuses, ...STATUS_ORDER])].map((key, i) => [key, i])),
    importance: new Map(["p0", "p1", "p2", "p3"].map((key, i) => [key, i])),
  };
  const compare = (a, b, key = "") => {
    const rank = ranks[key];
    if (rank) {
      const difference = (rank.get(a) ?? rank.size) - (rank.get(b) ?? rank.size);
      if (difference) return difference;
    }
    return typeof a === "number" && typeof b === "number" ? a - b : collator.compare(String(a), String(b));
  };
  return compare;
}

export function sortMapUnits(units, preferences, options = {}) {
  const { sort, direction } = normalizeMapListPrefs(preferences);
  const compare = comparisons(options), sign = direction === "desc" ? -1 : 1;
  // Calculate dates and degrees once per node, rather than in every comparison.
  return units.map((unit, index) => ({ unit, index, value: valueFor(unit, sort) }))
    .sort((a, b) => {
      if ((a.value == null) !== (b.value == null)) return a.value == null ? 1 : -1;
      return (a.value == null ? 0 : sign * compare(a.value, b.value, sort))
        || compare(a.unit.title || "", b.unit.title || "")
        || compare(a.unit.uid || "", b.unit.uid || "")
        || count(a.unit.id) - count(b.unit.id) || a.index - b.index;
    }).map(({ unit }) => unit);
}

export function groupMapUnits(units, group, options = {}) {
  if (!MAP_LIST_GROUPS.includes(group) || group === "none") return [{ key: "", items: units }];
  const groups = new Map(), compare = comparisons(options);
  for (const unit of units) {
    const key = String(group === "assignee" ? assignee(unit) : unit[group] || "");
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(unit);
  }
  return [...groups].sort(([a], [b]) => !a !== !b ? (a ? -1 : 1) : compare(a, b, group))
    .map(([key, items]) => ({ key, items }));
}

export function mapListPage(groups, page = 0, size = 100) {
  const total = groups.reduce((sum, group) => sum + group.items.length, 0);
  const pages = Math.max(1, Math.ceil(total / size));
  const current = Math.max(0, Math.min(pages - 1, Math.floor(Number(page) || 0)));
  const start = current * size, end = Math.min(total, start + size);
  let offset = 0;
  const visible = [];
  for (const group of groups) {
    const items = group.items.slice(Math.max(0, start - offset), Math.max(0, end - offset));
    if (items.length) visible.push({ ...group, items, total: group.items.length });
    offset += group.items.length;
    if (offset >= end) break;
  }
  return { groups: visible, page: current, pages, start: total ? start + 1 : 0, end, total };
}
