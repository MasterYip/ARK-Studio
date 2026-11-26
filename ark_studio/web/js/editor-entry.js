// The Editor (views/editor.js and its node, layout and file modules) loads on
// demand, so a first visit that starts elsewhere does not download it.  The
// rest of the app talks to it through these functions; until the module has
// loaded there are no Editor tabs, so the synchronous ones answer "nothing".
import { shellActive, isViewDisplayed } from "./workbench.js";

export const EXPLORER_CHANGED = "ark:editor-explorer";

let editor = null;
let loading = null;

/** Load the Editor module (once). */
export function loadEditor() {
  loading ||= import("./views/editor.js").then((module) => { editor = module; return module; });
  return loading;
}

export async function renderEditor(host, ctx) {
  const module = await loadEditor();
  return module.renderEditor(host, ctx);
}

export function openInEditor(ctx, source, options = {}) {
  return loadEditor().then((module) => module.openInEditor(ctx, source, options));
}

export function focusQuickOpen() {
  loadEditor().then((module) => module.focusQuickOpen());
}

export function editorHasDirty() {
  return editor ? editor.editorHasDirty() : false;
}

export function editorKeyDown(event) {
  return editor ? editor.editorKeyDown(event) : false;
}

export function editorExplorerVisible() {
  if (editor) return editor.editorExplorerVisible();
  return shellActive() ? isViewDisplayed("explorer") : false;
}

export function setExplorerVisible(visible) {
  loadEditor().then((module) => module.setExplorerVisible(visible));
}

// Ctrl+Shift+F in the Editor: the Search section of the Explorer.
export function focusEditorSearch(text = "") {
  loadEditor().then((module) => module.focusSearchSection(text));
}

export function editorFold(id) {
  return loadEditor().then((module) => module.editorFold(id));
}

export function revealActiveFile() {
  loadEditor().then((module) => module.revealActive());
}

export function editorAutoReveal() {
  if (editor) return editor.autoReveal();
  try { return localStorage.getItem("ark.editor.autoReveal") !== "0"; } catch { return true; }
}

export function setEditorAutoReveal(on) {
  loadEditor().then((module) => module.setAutoReveal(on));
}

/** Palette commands of the Editor (none until it has loaded). */
export function editorPaletteCommands() {
  return editor ? editor.editorPaletteCommands() : [];
}

export function workspaceFilesChanged(paths) {
  if (editor) editor.workspaceFilesChanged(paths);
}
