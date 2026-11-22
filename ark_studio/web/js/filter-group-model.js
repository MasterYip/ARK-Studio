// Filter chip groups (filter rail: Types, Status, Importance, Assignee and
// each tag namespace; Needs you's filter bar): a chip's menu offers "Only
// this", "Select all" and "Invert"; Alt+click is "Only this".  Pure: the
// callers store the result like any other filter change.

export const GROUP_ACTIONS = ["only", "all", "invert"];
export const ONLY_THIS_CLICK = "Alt+Click";

/** Alt+click (without other modifiers) on a chip means "Only this". */
export function isOnlyThisClick(event) {
  return Boolean(event?.altKey) && !event.ctrlKey && !event.metaKey && !event.shiftKey;
}

/** The group's next selection, in `base` order. */
export function groupSelection(action, base, selected, value) {
  const all = [...new Set((base || []).map(String))];
  const on = new Set((selected || []).map(String));
  if (action === "only") return all.includes(String(value)) ? [String(value)] : all.filter((item) => on.has(item));
  if (action === "all") return all;
  if (action === "invert") return all.filter((item) => !on.has(item));
  return all.filter((item) => on.has(item));
}

const SCOPED = {
  types: { list: "types", none: "typesNone" },
  statuses: { list: "statuses", none: "statusesNone" },
  importance: { list: "importanceLevels", none: "importanceNone" },
};
const covers = (next, base) => base.every((item) => next.includes(item));
const tagNamespace = (tag) => (String(tag).includes(":") ? String(tag).split(":", 1)[0] : "tag");

/**
 * The values a rail group has switched on.  Types, Status and Importance:
 * an empty list is every value, `<group>None` is none.  Assignee and a tag
 * namespace (`tag:<ns>`) are include lists: empty is "no restriction",
 * shown as every chip on.
 */
export function railGroupSelected(filters, group, base) {
  const all = (base || []).map(String);
  const scoped = SCOPED[group];
  if (scoped) {
    if (filters?.[scoped.none]) return [];
    const list = filters?.[scoped.list] || [];
    return list.length ? list.map(String) : all;
  }
  const list = group === "assignees" ? (filters?.assignees || [])
    : (filters?.tags || []).filter((tag) => tagNamespace(tag) === group.slice(4));
  return list.length ? list.map(String) : all;
}

/** Whether `action` changes anything (the menu disables the rest). */
export function railGroupCan(filters, group, action, base, value) {
  const all = (base || []).map(String);
  const selected = railGroupSelected(filters, group, all);
  const next = groupSelection(action, all, selected, value);
  // Include lists cannot express "nothing": inverting "everything" is a no-op.
  if (!SCOPED[group] && !next.length) return false;
  return next.length !== selected.length || next.some((item, i) => item !== selected[i]);
}

/** Apply `action` to the rail group in place (S.filters); returns filters. */
export function applyRailGroup(filters, group, action, base, value) {
  const all = (base || []).map(String);
  const next = groupSelection(action, all, railGroupSelected(filters, group, all), value);
  const scoped = SCOPED[group];
  if (scoped) {
    filters[scoped.none] = next.length === 0;
    filters[scoped.list] = covers(next, all) ? [] : next;
    return filters;
  }
  const keep = !next.length || covers(next, all) ? [] : next;
  if (group === "assignees") {
    filters.assignees = keep;
    return filters;
  }
  const ns = group.slice(4);
  filters.tags = [...(filters.tags || []).filter((tag) => tagNamespace(tag) !== ns), ...keep];
  // "Only this" value of a namespace also drops its "unset" option.
  if (action === "only") filters.tagsUnset = (filters.tagsUnset || []).filter((item) => item !== ns);
  return filters;
}

/** Needs you's bar stores the hidden values per group; the same actions. */
export function needsGroupAction(filter, group, action, values, value) {
  const all = (values || []).map(String);
  const hidden = new Set((filter?.[group] || []).map(String));
  const next = groupSelection(action, all, all.filter((item) => !hidden.has(item)), value);
  return { ...filter, [group]: all.filter((item) => !next.includes(item)) };
}
