// Merging one person's devices (Settings → Mobile and the resources graph).
// A phone that signed in several times (an app reinstall, an older app that
// sends no device id) shows as several devices.  The server previews the
// merge first: the most recently used sign-in stays, the others are revoked.
// Nothing is revoked without the person's confirmation.

import { h, fmtDate } from "./dom.js";
import { t } from "./i18n.js";
import { api } from "./api.js";
import { confirmDialog } from "./components.js";

function deviceLine(pair) {
  const parts = [pair.label || t("settings.mobile.device_merge.phone")];
  if (pair.project) parts.push(t("settings.mobile.device_project", { project: pair.project }));
  parts.push(pair.last_seen_at ? t("settings.mobile.device_seen", { date: fmtDate(pair.last_seen_at) })
    : t("settings.mobile.pair_claimed", { date: fmtDate(pair.claimed_at) }));
  return parts.join(" · ");
}

// mergeDevices merges every active device of the same type as pairId
// (body {like}) or the given ids.  Returns the server reply (with the
// updated pairs) or null when the person cancelled.
export async function mergeDevices(target, { toast } = {}) {
  const body = Array.isArray(target) ? { ids: target } : { like: target };
  let plan;
  try {
    plan = await api.mergeDevices({ ...body, dry_run: true });
  } catch (err) {
    toast?.(err.message, "err");
    return null;
  }
  const revoke = plan.revoke || [];
  const message = h("div", null,
    h("p", null, t("settings.mobile.device_merge.keep")),
    h("ul", null, h("li", null, deviceLine(plan.keep))),
    h("p", null, t("settings.mobile.device_merge.revoke", { n: revoke.length })),
    h("ul", null, ...revoke.map((pair) => h("li", null, deviceLine(pair)))),
    h("p", null, t("settings.mobile.device_merge.note")));
  const ok = await confirmDialog(message, { title: t("settings.mobile.device_merge.title"),
    confirmLabel: t("settings.mobile.device_merge.action"), danger: true });
  if (!ok) return null;
  try {
    const done = await api.mergeDevices(body);
    toast?.(t("settings.mobile.device_merge.done", { n: revoke.length }), "ok");
    return done;
  } catch (err) {
    toast?.(err.message, "err");
    return null;
  }
}

// removeStaleDevices revokes the person's devices unseen for 30 days after
// a confirmation that lists them.
export async function removeStaleDevices({ toast } = {}) {
  let preview;
  try {
    preview = await api.staleDevices({ dry_run: true });
  } catch (err) {
    toast?.(err.message, "err");
    return null;
  }
  const stale = preview.stale || [];
  if (!stale.length) return preview;
  const message = h("div", null,
    h("p", null, t("settings.mobile.stale.confirm", { n: stale.length })),
    h("ul", null, ...stale.map((pair) => h("li", null, deviceLine(pair)))));
  const ok = await confirmDialog(message, { title: t("settings.mobile.stale.title"),
    confirmLabel: t("settings.mobile.stale.action", { n: stale.length }), danger: true });
  if (!ok) return null;
  try {
    const done = await api.staleDevices({});
    toast?.(t("settings.mobile.stale.done", { n: (done.stale || []).length }), "ok");
    return done;
  } catch (err) {
    toast?.(err.message, "err");
    return null;
  }
}

// duplicateGroups: the person's active phones grouped by type, only groups
// with more than one device (candidates for a merge).
export function duplicateGroups(pairs) {
  const groups = new Map();
  for (const pair of pairs || []) {
    if (pair.state !== "claimed" || pair.source === "desktop") continue;
    const key = pair.label || "";
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(pair);
  }
  return [...groups.values()].filter((group) => group.length > 1);
}
