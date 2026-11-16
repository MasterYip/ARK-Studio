const STORAGE_KEY = "ark.shortcuts.v1";
const TAG_STORAGE_KEY = "ark.tagShortcuts.v1";
export const DEFAULT_SHORTCUTS = {
  palette: "Ctrl+K",
  chat: "Alt+J",
  newUnit: "N",
  editorSave: "Ctrl+S",
  editorClose: "Ctrl+W",
};

// Global actions shown in Settings → Personal → Shortcuts.  Contextual graph
// keys (Tab/Enter/arrows) remain documented in the graph reference because they
// only apply while the graph owns focus.  Labels and hints are i18n keys
// resolved with t() at render time, so a language switch takes effect.
export const SHORTCUT_ACTIONS = [
  { id: "palette", labelKey: "settings.palette_shortcut",
    hintKey: "settings.palette_shortcut_hint" },
  { id: "chat", labelKey: "settings.chat_shortcut",
    hintKey: "settings.chat_shortcut_hint" },
  { id: "newUnit", labelKey: "settings.new_shortcut",
    hintKey: "settings.new_shortcut_hint" },
  { id: "editorSave", labelKey: "settings.editor_save_shortcut",
    hintKey: "settings.editor_save_shortcut_hint" },
  { id: "editorClose", labelKey: "settings.editor_close_shortcut",
    hintKey: "settings.editor_close_shortcut_hint" },
];

const MODS = ["Ctrl", "Alt", "Shift", "Meta"];
const RESERVED = new Set([
  "Ctrl+J", "Ctrl+Shift+J", "Ctrl+L", "Ctrl+T", "Ctrl+W", "Ctrl+F",
  "Ctrl+S", "Ctrl+D", "Ctrl+R", "Alt+F4", "F5", "F12",
]);

// Browser keys that are normally reserved but mean exactly this action inside
// the Editor (VS Code muscle memory).  Only that action may claim them.
const RESERVED_ALLOWED = {
  editorSave: new Set(["Ctrl+S", "Meta+S"]),
  editorClose: new Set(["Ctrl+W", "Meta+W"]),
};

export function getShortcuts() {
  try {
    const parsed = JSON.parse(localStorage.getItem(STORAGE_KEY) || "{}");
    return { ...DEFAULT_SHORTCUTS, ...(parsed && typeof parsed === "object" ? parsed : {}) };
  } catch {
    return { ...DEFAULT_SHORTCUTS };
  }
}

export function getShortcut(name) {
  return getShortcuts()[name] || DEFAULT_SHORTCUTS[name] || "";
}

// UI that displays a shortcut (e.g. the search button hint) listens for this.
export const SHORTCUTS_CHANGED = "ark:shortcuts-changed";

function announce() {
  try { window.dispatchEvent(new CustomEvent(SHORTCUTS_CHANGED)); } catch { /* no window */ }
}

export function setShortcut(name, spec) {
  const shortcuts = { ...getShortcuts(), [name]: spec };
  localStorage.setItem(STORAGE_KEY, JSON.stringify(shortcuts));
  announce();
  return shortcuts;
}

export function resetShortcuts() {
  localStorage.removeItem(STORAGE_KEY);
  announce();
  return { ...DEFAULT_SHORTCUTS };
}

export function getTagShortcuts() {
  try {
    const parsed = JSON.parse(localStorage.getItem(TAG_STORAGE_KEY) || "{}");
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    return Object.fromEntries(Object.entries(parsed)
      .filter(([tag, spec]) => typeof tag === "string" && tag.trim() && typeof spec === "string" && spec)
      .map(([tag, spec]) => [tag.trim(), spec]));
  } catch {
    return {};
  }
}

export function setTagShortcut(tag, spec) {
  const value = String(tag || "").trim();
  if (!value) throw new Error("Tag is required");
  const next = { ...getTagShortcuts(), [value]: spec };
  localStorage.setItem(TAG_STORAGE_KEY, JSON.stringify(next));
  return next;
}

export function removeTagShortcut(tag) {
  const next = { ...getTagShortcuts() };
  delete next[tag];
  localStorage.setItem(TAG_STORAGE_KEY, JSON.stringify(next));
  return next;
}

export function formatShortcut(spec, spaces = false) {
  return String(spec || "").replaceAll("+", spaces ? " " : "+");
}

// Compact label for hints: "Ctrl K", "Alt J"; Meta shows as ⌘ on Apple devices.
export function shortcutLabel(spec) {
  const apple = typeof navigator !== "undefined"
    && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent || "");
  return String(spec || "").split("+").filter(Boolean)
    .map((part) => (part === "Meta" ? (apple ? "⌘" : "Win") : part)).join(" ");
}

export function shortcutFromEvent(event) {
  const key = String(event.key || "");
  if (!key || MODS.includes(key)) return null;
  const mods = [];
  if (event.ctrlKey) mods.push("Ctrl");
  if (event.altKey) mods.push("Alt");
  if (event.shiftKey) mods.push("Shift");
  if (event.metaKey) mods.push("Meta");
  if (!mods.length) {
    // Bare single keys are allowed for actions such as New unit (N).
    return /^[A-Za-z0-9]$/.test(key) ? key.toUpperCase() : null;
  }
  let name = key.length === 1 ? key.toUpperCase() : key;
  if (name === " ") name = "Space";
  return [...mods, name].join("+");
}

export function shortcutIsUsable(spec, name = "") {
  if (!spec) return false;
  if (RESERVED.has(spec) && !RESERVED_ALLOWED[name]?.has(spec)) return false;
  if (/^[A-Za-z0-9]$/.test(spec)) return true;
  return ["Ctrl", "Alt", "Shift", "Meta"].some((mod) => spec.startsWith(`${mod}+`));
}

export function shortcutConflict(name, spec) {
  const wanted = String(spec || "");
  for (const [id, current] of Object.entries(getShortcuts())) {
    if (id !== name && current === wanted) return id;
  }
  return "";
}

export function shortcutMatches(event, spec) {
  if (!spec) return false;
  const parts = String(spec).split("+").filter(Boolean);
  const key = parts.pop();
  if (!key) return false;
  const want = { Ctrl: false, Alt: false, Shift: false, Meta: false };
  for (const part of parts) {
    if (!(part in want)) return false;
    want[part] = true;
  }
  if (event.ctrlKey !== want.Ctrl) return false;
  if (event.altKey !== want.Alt) return false;
  if (event.shiftKey !== want.Shift) return false;
  if (event.metaKey !== want.Meta) return false;
  const actual = event.key === " " ? "Space" : event.key;
  return String(actual).toLowerCase() === String(key).toLowerCase();
}
