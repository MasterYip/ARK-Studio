const STORAGE_KEY = "ark.shortcuts.v1";
const TAG_STORAGE_KEY = "ark.tagShortcuts.v1";
export const DEFAULT_SHORTCUTS = {
  // Ctrl+K is the prefix of the folding chords, so the palette has its own key.
  palette: "Ctrl+P",
  chat: "Alt+J",
  sync: "Alt+R",
  locateMap: "Alt+M",
  newUnit: "N",
  editorSave: "Ctrl+S",
  editorClose: "Ctrl+W",
  // Folding in the Editor (VS Code keys): two-key chords start with Ctrl+K.
  editorFoldAll: "Ctrl+K Ctrl+0",
  editorUnfoldAll: "Ctrl+K Ctrl+J",
  editorFoldLevel1: "Ctrl+K Ctrl+1",
  editorFoldLevel2: "Ctrl+K Ctrl+2",
  editorFoldLevel3: "Ctrl+K Ctrl+3",
  editorFoldLevel4: "Ctrl+K Ctrl+4",
  editorFoldLevel5: "Ctrl+K Ctrl+5",
  editorFoldLevel6: "Ctrl+K Ctrl+6",
  editorFoldLevel7: "Ctrl+K Ctrl+7",
  editorFold: "Ctrl+Shift+[",
  editorUnfold: "Ctrl+Shift+]",
  // Files open for reading: edit / view, and the Editor filling the window.
  editorEdit: "Ctrl+E",
  editorFullscreen: "F11",
};

// The Editor's folding commands, in the order Settings and the palette list them.
export const FOLD_ACTIONS = [
  { id: "editorFoldAll", labelKey: "editor.fold.all" },
  { id: "editorUnfoldAll", labelKey: "editor.fold.unfold_all" },
  ...[1, 2, 3, 4, 5, 6, 7].map((n) => ({ id: `editorFoldLevel${n}`, labelKey: "editor.fold.level", labelParams: { n }, level: n })),
  { id: "editorFold", labelKey: "editor.fold.current" },
  { id: "editorUnfold", labelKey: "editor.fold.unfold_current" },
];

// Global actions shown in Settings → Personal → Shortcuts.  Contextual graph
// keys (Tab/Enter/arrows) remain documented in the graph reference because they
// only apply while the graph owns focus.  Labels and hints are i18n keys
// resolved with t() at render time, so a language switch takes effect.
export const SHORTCUT_ACTIONS = [
  { id: "palette", labelKey: "settings.palette_shortcut",
    hintKey: "settings.palette_shortcut_hint" },
  { id: "chat", labelKey: "settings.chat_shortcut",
    hintKey: "settings.chat_shortcut_hint" },
  { id: "sync", labelKey: "settings.sync_shortcut",
    hintKey: "settings.sync_shortcut_hint" },
  { id: "locateMap", labelKey: "settings.locate_shortcut",
    hintKey: "settings.locate_shortcut_hint" },
  { id: "newUnit", labelKey: "settings.new_shortcut",
    hintKey: "settings.new_shortcut_hint" },
  { id: "editorSave", labelKey: "settings.editor_save_shortcut",
    hintKey: "settings.editor_save_shortcut_hint" },
  { id: "editorClose", labelKey: "settings.editor_close_shortcut",
    hintKey: "settings.editor_close_shortcut_hint" },
  { id: "editorEdit", labelKey: "settings.editor_edit_shortcut",
    hintKey: "settings.editor_edit_shortcut_hint" },
  { id: "editorFullscreen", labelKey: "settings.editor_fullscreen_shortcut",
    hintKey: "settings.editor_fullscreen_shortcut_hint" },
  // Folding: "Editor: …" like the other Editor keys; the first row explains them.
  ...FOLD_ACTIONS.map((action, index) => ({
    ...action, prefixKey: "settings.editor_prefix", hintKey: index === 0 ? "settings.editor_fold_shortcut_hint" : "",
  })),
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
    const saved = parsed && typeof parsed === "object" ? { ...parsed } : {};
    // Saved settings copy every key; an old palette "Ctrl+K" is the former default.
    if (saved.palette === "Ctrl+K" || saved.palette === "Meta+K") delete saved.palette;
    return { ...DEFAULT_SHORTCUTS, ...saved };
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
// A chord ("Ctrl+K Ctrl+0") reads "Ctrl K, Ctrl 0".
export function shortcutLabel(spec) {
  const apple = typeof navigator !== "undefined"
    && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent || "");
  return chordParts(spec).map((combo) => combo.split("+").filter(Boolean)
    .map((part) => (part === "Meta" ? (apple ? "⌘" : "Win") : part)).join(" ")).join(", ");
}

// Shift changes event.key for punctuation and digits ("{" for "["): these
// keys are recorded and matched by their physical key.
const CODE_KEYS = {
  BracketLeft: "[", BracketRight: "]", Backslash: "\\", Minus: "-", Equal: "=", Comma: ",", Period: ".",
  Slash: "/", Semicolon: ";", Quote: "'", Backquote: "`",
  Digit0: "0", Digit1: "1", Digit2: "2", Digit3: "3", Digit4: "4", Digit5: "5", Digit6: "6", Digit7: "7", Digit8: "8", Digit9: "9",
};
const KEY_CODES = Object.fromEntries(Object.entries(CODE_KEYS).map(([code, key]) => [key, code]));

/** The key combos of a shortcut: one, or two for a chord ("Ctrl+K Ctrl+0"). */
export function chordParts(spec) {
  return String(spec || "").trim().split(/\s+/).filter(Boolean);
}

export function isChord(spec) {
  return chordParts(spec).length > 1;
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
    if (/^F([1-9]|1[0-2])$/.test(key)) return key;
    return /^[A-Za-z0-9]$/.test(key) ? key.toUpperCase() : null;
  }
  let name = key.length === 1 ? key.toUpperCase() : key;
  if (CODE_KEYS[event.code]) name = CODE_KEYS[event.code];
  if (name === " ") name = "Space";
  return [...mods, name].join("+");
}

export function shortcutIsUsable(spec, name = "") {
  if (!spec) return false;
  const parts = chordParts(spec);
  if (parts.length > 2) return false;
  // A chord's keys each need a modifier (Ctrl+K Ctrl+0); reserved browser
  // keys are fine as its second key.
  if (parts.length === 2) return parts.every((part) => ["Ctrl", "Alt", "Meta"].some((mod) => part.startsWith(`${mod}+`)));
  if (RESERVED.has(spec) && !RESERVED_ALLOWED[name]?.has(spec)) return false;
  if (/^[A-Za-z0-9]$/.test(spec)) return true;
  // Function keys alone (F11: the Editor full screen); F5 and F12 stay the browser's.
  if (/^F([1-9]|1[0-2])$/.test(spec)) return true;
  return ["Ctrl", "Alt", "Shift", "Meta"].some((mod) => spec.startsWith(`${mod}+`));
}

export function shortcutConflict(name, spec) {
  const wanted = String(spec || "");
  for (const [id, current] of Object.entries(getShortcuts())) {
    if (id !== name && current === wanted) return id;
  }
  return "";
}

// One key combo (not a chord): see matchChord for two-key shortcuts.
export function shortcutMatches(event, spec) {
  if (!spec || isChord(spec)) return false;
  return comboMatches(event, spec);
}

function comboMatches(event, spec) {
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
  if (KEY_CODES[key] && event.code === KEY_CODES[key]) return true;
  const actual = event.key === " " ? "Space" : event.key;
  return String(actual).toLowerCase() === String(key).toLowerCase();
}

/**
 * Two-key shortcuts.  `specs` maps action ids to shortcuts; `pending` is the
 * first combo of a chord already pressed ("" when none).  Returns
 * { type: "match", id } for a complete shortcut, { type: "prefix", combo }
 * when the key starts a chord, or { type: "none" }.
 */
export function matchChord(event, specs, pending = "") {
  const entries = Object.entries(specs || {});
  if (pending) {
    for (const [id, spec] of entries) {
      const parts = chordParts(spec);
      if (parts.length === 2 && parts[0] === pending && comboMatches(event, parts[1])) return { type: "match", id };
    }
    return { type: "none" };
  }
  for (const [id, spec] of entries) {
    const parts = chordParts(spec);
    if (parts.length === 1 && comboMatches(event, parts[0])) return { type: "match", id };
  }
  for (const [, spec] of entries) {
    const parts = chordParts(spec);
    if (parts.length === 2 && comboMatches(event, parts[0])) return { type: "prefix", combo: parts[0] };
  }
  return { type: "none" };
}
