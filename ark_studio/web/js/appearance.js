// One token source for native themes, project palettes and reading preferences.
const HEX = /^#[0-9a-f]{6}$/i;
const STORAGE_KEY = "ark.display";
let palette = null;
let applied = new Set();
let display = readDisplay();

export function normalizeDisplay(value = {}) {
  const scale = Number(value.font_scale);
  return {
    font_scale: Number.isFinite(scale) ? Math.max(1, Math.min(1.5, scale)) : 1,
    high_contrast: value.high_contrast === true,
  };
}

function readDisplay() {
  try { return normalizeDisplay(JSON.parse(localStorage.getItem(STORAGE_KEY)) || {}); }
  catch { return normalizeDisplay(); }
}

export function displayPrefs() { return { ...display }; }

export function setDisplayPrefs(patch, { announce = true } = {}) {
  display = normalizeDisplay({ ...display, ...patch });
  try { localStorage.setItem(STORAGE_KEY, JSON.stringify(display)); } catch { /* private browsing */ }
  applyAppearance();
  if (announce) window.dispatchEvent(new CustomEvent("ark:pref-change", { detail: { display } }));
  window.dispatchEvent(new CustomEvent("ark:theme-applied"));
}

function rgb(hex) { return [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16)); }
function hex(values) { return "#" + values.map(v => Math.round(v).toString(16).padStart(2, "0")).join(""); }
export function mix(a, b, weight) { return hex(rgb(a).map((v, i) => v * (1 - weight) + rgb(b)[i] * weight)); }
export function luminance(color) {
  return rgb(color).map(v => v / 255).map(v => v <= .04045 ? v / 12.92 : ((v + .055) / 1.055) ** 2.4)
    .reduce((sum, v, i) => sum + v * [.2126, .7152, .0722][i], 0);
}
export function paletteMode(colors) { return luminance(colors.background || "#0f141c") > .179 ? "light" : "dark"; }
export function contrast(a, b) { const x = luminance(a), y = luminance(b); return (Math.max(x, y) + .05) / (Math.min(x, y) + .05); }

export function readable(color, surfaces, minimum = 4.5) {
  if (surfaces.every(bg => contrast(color, bg) >= minimum)) return color;
  const score = fg => Math.min(...surfaces.map(bg => contrast(fg, bg)));
  const target = score("#000000") > score("#ffffff") ? "#000000" : "#ffffff";
  for (let step = 1; step <= 100; step++) {
    const next = mix(color, target, step / 100);
    if (score(next) >= minimum) return next;
  }
  return target;
}

export function resolvePalette(value, mode) {
  if (!value?.enabled) return null;
  const colors = value[mode] || (paletteMode(value) === mode ? value : null);
  return colors && Object.values(colors).filter(v => typeof v === "string" && HEX.test(v)).length ? colors : null;
}

export function paletteTokens(colors, highContrast = false) {
  const c = Object.fromEntries(Object.entries(colors).filter(([, v]) => typeof v === "string" && HEX.test(v)));
  if (!["background", "surface", "text", "muted", "accent", "line"].every(key => c[key])) return {};
  const bg = c.background, surface = c.surface;
  const surfaces = [bg, surface];
  const ink = highContrast ? readable(c.text, surfaces, 10) : c.text;
  const sub = highContrast ? readable(c.muted, surfaces, 10) : c.muted;
  const line = highContrast ? readable(c.line, surfaces, 3) : c.line;
  const accent = c.accent;
  const soft = c.accent_soft || mix(surface, accent, .12);
  return {
    "--bg": bg, "--bg2": mix(bg, surface, .3), "--bg3": mix(surface, bg, .15),
    "--lane": surface, "--card": surface, "--card2": mix(surface, bg, .25),
    "--ink": ink, "--sub": sub, "--mute": sub, "--line": line,
    "--line-soft": highContrast ? line : mix(surface, line, .65),
    "--accent": accent, "--accent-2": readable(c.accent2 || accent, surfaces),
    "--accent-ink": readable("#ffffff", [accent]), "--accent-soft": soft,
    "--blue": accent, "--blue-soft": soft,
    "--green": c.success, "--amber": c.warning, "--red": c.danger,
    "--input-bg": mix(surface, bg, .2), "--input-border": line,
    "--btn": soft, "--btn-ink": readable(accent, [soft]), "--btn-hover": mix(soft, accent, .12),
    "--tag-border": line, "--tag-hover-border": accent, "--tag-hover-ink": readable(accent, surfaces),
    "--chip-bg": mix(surface, bg, .35), "--chip-ink": sub,
    "--scroll": mix(surface, ink, .3), "--scroll-hover": mix(surface, ink, .5),
    "--dash": line, "--stripe": mix(surface, bg, .5),
    "--grid-line": mix(bg, line, .45),
  };
}

// The last inputs applied: a remote refresh re-applies the same palette on
// every live-sync change, and re-setting every root token restyles the whole
// page each time.  An unchanged palette, theme and display do nothing.
let appliedFor = "";

export function applyProjectPalette(value) {
  palette = value;
  const root = typeof document !== "undefined" ? document.documentElement : null;
  const key = JSON.stringify([palette || null, root?.dataset.theme || "", display]);
  if (key === appliedFor) return;
  applyAppearance();
}

export function applyAppearance() {
  const root = document.documentElement;
  appliedFor = JSON.stringify([palette || null, root.dataset.theme || "", display]);
  const mode = root.dataset.theme === "dark" ? "dark" : "light";
  for (const key of applied) root.style.removeProperty(key);
  applied = new Set();
  root.style.colorScheme = mode;
  root.style.setProperty("--font-scale", String(display.font_scale));
  root.dataset.contrast = display.high_contrast ? "high" : "normal";
  root.dataset.textSize = display.font_scale > 1 ? "large" : "normal";
  const colors = resolvePalette(palette, mode);
  const tokens = colors ? paletteTokens(colors, display.high_contrast) : {};
  if (colors) root.dataset.projectPalette = colors.preset || "custom";
  else delete root.dataset.projectPalette;
  if (!colors) {
    const native = getComputedStyle(root);
    const get = key => native.getPropertyValue(key).trim();
    const accent = get("--accent");
    if (HEX.test(accent)) tokens["--accent-ink"] = readable("#ffffff", [accent]);
    const surfaces = [get("--bg"), get("--card")];
    if (display.high_contrast && surfaces.every(v => HEX.test(v))) {
      for (const key of ["--ink", "--sub", "--mute"]) tokens[key] = readable(get(key), surfaces, 10);
      for (const key of ["--line", "--line-soft", "--input-border"]) tokens[key] = readable(get(key), surfaces, 3);
    }
  }
  for (const [key, value] of Object.entries(tokens)) {
    if (value) { root.style.setProperty(key, value); applied.add(key); }
  }
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) meta.content = getComputedStyle(root).getPropertyValue("--bg").trim() || (mode === "dark" ? "#0f141c" : "#f5f6f8");
}
