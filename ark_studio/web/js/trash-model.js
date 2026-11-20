// Recycle bin model (Settings > Project > Recycle bin, views/trash-panel.js).
// Deleting a node moves it to the bin (GET /api/trash lists it); restoring
// brings it back with its links, deleting permanently removes it.  Pure
// helpers so tests can check them without a DOM.

/**
 * Display name of the actor who deleted a node: "human:alice" -> "alice",
 * "agent:ana/claude" -> "claude"; the local owner, task agents and unnamed
 * actors -> "" (not shown).
 */
export function trashActor(actor) {
  const text = String(actor || "");
  const colon = text.indexOf(":");
  const kind = colon >= 0 ? text.slice(0, colon) : "";
  let name = colon >= 0 ? text.slice(colon + 1) : text;
  if (kind === "task") return "";
  if (kind === "agent") name = name.split("/").pop();
  return ["", "ui", "local", "api", "user"].includes(name) ? "" : name;
}

/** The bin's units without the given uids (after a restore or permanent delete). */
export function withoutUnits(units, uids) {
  const drop = new Set((Array.isArray(uids) ? uids : [uids]).map(String));
  return (units || []).filter((unit) => !drop.has(String(unit.uid)));
}

/** Whether this session may restore or permanently delete (write access). */
export function canEditTrash(session) {
  return session?.project_permission !== "read";
}
