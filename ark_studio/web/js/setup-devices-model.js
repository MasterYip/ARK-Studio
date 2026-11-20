// The person's devices in Settings > Resources (GET /api/skills/setup/state
// `devices`, internal/setup/devices.go): setup is repeatable per person and
// per device; each device has its own paths.  Pure, no DOM.

/** Device rows: current device first, then by last seen; counts of paths set and missing. */
export function deviceRows(state) {
  const list = Array.isArray(state?.devices) ? state.devices : [];
  return list.map((device) => {
    const paths = Array.isArray(device.paths) ? device.paths : [];
    const missing = new Set(Array.isArray(device.missing) ? device.missing : []);
    return {
      id: String(device.id || ""),
      label: String(device.label || device.id || ""),
      lastSeen: device.last_seen || "",
      current: device.current === true,
      paths: paths.map((p) => ({ ...p, missing: missing.has(p.resource) })),
      set: paths.filter((p) => p.path_or_ref).length,
      missing: missing.size,
    };
  }).sort((a, b) => (a.current !== b.current ? (a.current ? -1 : 1) : String(b.lastSeen).localeCompare(String(a.lastSeen))));
}

/** How long ago, as { n, unit } with unit "min", "h" or "d"; null when unknown. */
export function since(iso, now = Date.now()) {
  const t = Date.parse(iso || "");
  if (!Number.isFinite(t)) return null;
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 3600) return { n: Math.max(1, Math.round(s / 60)), unit: "min" };
  if (s < 86400) return { n: Math.round(s / 3600), unit: "h" };
  return { n: Math.round(s / 86400), unit: "d" };
}
