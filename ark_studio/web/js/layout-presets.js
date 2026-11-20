// Workbench layout presets: the top-bar Layout menu and palette commands.
// Built-ins (Default, Focus/Zen, Research, Writing, Review) plus layouts the
// user saves.  Saved presets follow the account (PATCH /api/me
// prefs.workbench, validated server-side); browser storage is the cache and
// the fallback when there is no savable profile.

import { h } from "./dom.js";
import { t } from "./i18n.js";
import { api } from "./api.js";
import { openContextMenu } from "./context-menu.js";
import { openModal, confirmDialog, toast } from "./components.js";
import {
  BUILTIN_PRESETS, PRESET_STORE_KEY, normalizePresetStore, savePreset, renamePreset, deletePreset, setDefaultPreset,
  withCurrentLayout, accountLayoutIsNewer, defaultLayout,
} from "./workbench-model.js";
import {
  applyPresetLayout, setPresetStore, currentPresetStore, setLayoutSync, applyAccountLayout, resetViews,
} from "./workbench.js";

let persist = false;

function readLocal() {
  try { return JSON.parse(localStorage.getItem(PRESET_STORE_KEY) || "null"); } catch { return null; }
}

export function initLayoutPresets(profile) {
  persist = Boolean(profile && !profile.permissions?.read_only);
  const saved = persist && profile.prefs?.workbench ? profile.prefs.workbench : readLocal();
  const clean = normalizePresetStore(saved);
  setPresetStore(clean);
  // The arrangement follows the account.  Another account's layout left in
  // this browser is never inherited: an account without a saved layout
  // starts from its default preset (built-in Default unless it chose one).
  const user = String(profile?.username || "");
  const otherOwner = user && localLayoutOwner() !== user;
  if (persist && clean.layout && (otherOwner || accountLayoutIsNewer(clean, localLayoutAt()))) {
    applyAccountLayout(clean.layout);
    setLocalLayoutAt(clean.layout_at || Date.now());
  } else if (persist && !clean.layout && otherOwner) {
    applyAccountLayout(defaultLayout(clean));
    setLocalLayoutAt(0);
  }
  if (user) setLocalLayoutOwner(user);
  setLayoutSync(persist ? queueLayoutSync : null);
}

const LAYOUT_AT_KEY = "ark.workbench.layoutAt";
const LAYOUT_OWNER_KEY = "ark.workbench.owner";

function localLayoutOwner() {
  try { return localStorage.getItem(LAYOUT_OWNER_KEY) || ""; } catch { return ""; }
}

function setLocalLayoutOwner(user) {
  try { localStorage.setItem(LAYOUT_OWNER_KEY, user); } catch { /* storage disabled */ }
}

function localLayoutAt() {
  try { return Number(localStorage.getItem(LAYOUT_AT_KEY)) || 0; } catch { return 0; }
}

function setLocalLayoutAt(at) {
  try { localStorage.setItem(LAYOUT_AT_KEY, String(at)); } catch { /* storage disabled */ }
}

// Every layout change (drag & drop, moves, resizes, presets) is saved to
// the account after a short pause, so one drag sequence is one request.
const LAYOUT_SYNC_DELAY = 1500;
let syncTimer = 0;
let syncLayout = null;

function queueLayoutSync(layout) {
  syncLayout = layout;
  const at = Date.now();
  setLocalLayoutAt(at);
  clearTimeout(syncTimer);
  syncTimer = setTimeout(() => {
    const next = syncLayout;
    syncLayout = null;
    if (next) store(withCurrentLayout(currentPresetStore(), next, undefined, at), { quiet: true });
  }, LAYOUT_SYNC_DELAY);
}

async function store(next, { quiet = false } = {}) {
  const previous = currentPresetStore();
  const clean = normalizePresetStore(next);
  setPresetStore(clean);
  try { localStorage.setItem(PRESET_STORE_KEY, JSON.stringify(clean)); } catch { /* storage disabled */ }
  if (!persist) return true;
  try {
    const empty = !clean.presets.length && !clean.default && !clean.layout;
    const data = await api.updateMe({ prefs: { workbench: empty ? null : clean } });
    setPresetStore(normalizePresetStore(data.profile?.prefs?.workbench || {}));
    return true;
  } catch (err) {
    setPresetStore(previous);
    // A failed background layout save keeps the browser copy; no toast.
    if (!quiet) toast(err.message, "err");
    return false;
  }
}

export function presetName(id) {
  if (BUILTIN_PRESETS.includes(id)) return t(`workbench.preset.${id}`);
  return currentPresetStore().presets.find((item) => item.id === id)?.name || id;
}

export function applyPreset(id) {
  applyPresetLayout(id);
}

function askName(title, initial = "") {
  return new Promise((resolve) => {
    let done = false;
    const finish = (value) => { if (!done) { done = true; resolve(value); } };
    const input = h("input", { class: "input", value: initial, maxlength: "60", "aria-label": t("workbench.preset.name") });
    openModal({
      title,
      body: h("label", { class: "field" }, h("label", null, t("workbench.preset.name")), input),
      enterConfirm: true,
      onClose: () => finish(null),
      actions: [
        { label: t("action.cancel"), ghost: true, onClick: () => finish(null) },
        { label: t("action.save"), primary: true, onClick: () => {
          const value = input.value.trim();
          if (!value) { input.focus(); return false; }
          finish(value);
          return true;
        } },
      ],
    });
    requestAnimationFrame(() => { input.focus(); input.select(); });
  });
}

export async function saveCurrentLayoutAs() {
  const name = await askName(t("workbench.preset.save_as"));
  if (!name) return;
  const { workbenchLayout } = await import("./workbench.js");
  const result = savePreset(currentPresetStore(), name, workbenchLayout());
  if (!result.id) { toast(t("workbench.preset.full"), "err"); return; }
  if (await store(result.store)) toast(t("workbench.preset.saved", { name }), "ok");
}

async function rename(id) {
  const name = await askName(t("workbench.preset.rename"), presetName(id));
  if (name) await store(renamePreset(currentPresetStore(), id, name));
}

async function remove(id) {
  const ok = await confirmDialog(t("workbench.preset.delete_confirm", { name: presetName(id) }),
    { danger: true, confirmLabel: t("action.delete") });
  if (ok) await store(deletePreset(currentPresetStore(), id));
}

async function makeDefault(id) {
  await store(setDefaultPreset(currentPresetStore(), id));
}

// The Layout menu (top bar, next to the panel toggles).
export function openLayoutMenu(anchor) {
  const presets = currentPresetStore();
  const def = presets.default || "default";
  const rect = anchor.getBoundingClientRect();
  const label = (id) => `${presetName(id)}${id === def && def !== "default" ? ` · ${t("workbench.preset.default_badge")}` : ""}`;
  const items = [
    { heading: t("workbench.preset.builtin") },
    ...BUILTIN_PRESETS.map((id) => ({ label: label(id), icon: "layers", onClick: () => applyPreset(id) })),
  ];
  if (presets.presets.length) {
    items.push({ heading: t("workbench.preset.mine") });
    for (const preset of presets.presets) {
      items.push({
        label: label(preset.id), icon: "layers",
        children: [
          { label: t("workbench.preset.apply"), icon: "check", onClick: () => applyPreset(preset.id) },
          { label: t("workbench.preset.rename"), icon: "pen", onClick: () => rename(preset.id) },
          { label: t("workbench.preset.set_default"), icon: "pin", disabled: preset.id === def, onClick: () => makeDefault(preset.id) },
          { label: t("action.delete"), icon: "trash", danger: true, onClick: () => remove(preset.id) },
        ],
      });
    }
  }
  items.push({ separator: true },
    { label: t("workbench.preset.save_as"), icon: "save", onClick: () => saveCurrentLayoutAs() },
    { label: t("workbench.preset.set_default"), icon: "pin",
      children: [...BUILTIN_PRESETS, ...presets.presets.map((item) => item.id)].map((id) => ({
        label: presetName(id), checked: id === def, onClick: () => makeDefault(id),
      })) },
    { label: t("workbench.reset_views"), icon: "refresh", onClick: () => resetViews() });
  openContextMenu({ x: rect.right - 240, y: rect.bottom + 4, items, className: "wb-layout-menu" });
}

// Command palette entries.
export function presetCommands(category) {
  const presets = currentPresetStore();
  return [
    ...BUILTIN_PRESETS.map((id) => ({ id: `layout-${id}`, label: t("workbench.preset.apply_named", { name: presetName(id) }), cat: category, run: () => applyPreset(id) })),
    ...presets.presets.map((item) => ({ id: `layout-${item.id}`, label: t("workbench.preset.apply_named", { name: item.name }), cat: category, run: () => applyPreset(item.id) })),
    { id: "layout-save", label: t("workbench.preset.save_as"), cat: category, run: () => saveCurrentLayoutAs() },
    { id: "layout-reset", label: t("workbench.reset_views"), cat: category, run: () => resetViews() },
  ];
}
