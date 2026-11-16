// Pure helpers for the "Assign to" menu (no DOM, no imports), so
// `node --test tests/js/assign_model.test.mjs` covers them.

export function sameName(a, b) {
  return String(a || "").toLowerCase() === String(b || "").toLowerCase();
}

/**
 * What the menu marks for `units`:
 *   known          every unit has a loaded `assignee` and they all agree
 *   own            that shared own value ("" = unassigned)
 *   effective      single unit only: the effective (maybe inherited) value
 *   inheritedFrom  single unit with no own value: title/uid it inherits from
 */
export function assignChoice(units = []) {
  const loaded = units.length > 0 && units.every((unit) => unit && "assignee" in unit);
  const owns = loaded ? new Set(units.map((unit) => String(unit.assignee || "").toLowerCase())) : new Set();
  const known = loaded && owns.size === 1;
  const own = known ? (units[0].assignee || "") : "";
  const single = units.length === 1 ? units[0] : null;
  const effective = (loaded && single?.assignee_effective) || "";
  const from = single?.assignee_from;
  const inheritedFrom = known && !own && effective && from ? String(from.title || from.uid || "") : "";
  return { known, own, effective, inheritedFrom };
}

/**
 * Menu names.  `members`: project members only, me first when I am one.
 * `former`: the current own/effective names that are not members (shown
 * disabled so the check mark / inherited hint stays visible, never offered).
 */
export function assignNames(users = [], me = "", meMember = false, choice = {}) {
  const members = [];
  const add = (list, name) => { if (name && !list.some((item) => sameName(item, name))) list.push(name); };
  const isMember = (name) => users.some((user) => sameName(user?.username, name));
  if (me && meMember && isMember(me)) add(members, me);
  for (const user of users) add(members, user?.username);
  const former = [];
  for (const name of [choice.own, choice.effective]) {
    if (name && !isMember(name)) add(former, name);
  }
  return { members, former };
}
