// Self-service account profile: Settings -> Personal -> Profile, the top-bar
// account menu, and per-account language/theme preferences.
//
// The username is the stable sign-in ID and is shown read-only.  Display
// name, avatar, bio, email and preferences are saved through PATCH /api/me;
// an uploaded avatar is square-cropped and downscaled here (<= 256 px,
// <= 200 KB) before POST /api/me/avatar.  After a save the shared user
// directory (users.js) repaints every visible name and avatar in place.

import { h } from "./dom.js";
import { t, getLang } from "./i18n.js";
import { icon } from "./icons.js";
import { api, authenticatedUrl } from "./api.js";
import { S, setLanguage, setTheme } from "./state.js";
import { openContextMenu } from "./context-menu.js";
import { setDisplayPrefs } from "./appearance.js";
import { appearanceOverrides, setAppearanceOverrides } from "./appearance-overrides.js";
import { LEGACY_MIND_KEY, LEGACY_MIND_DONE_KEY, migrateLegacyMind } from "./graph-display.js";
import { localLayoutBackend } from "./mind-node-state.js";
import {
  homeFilterEntry, homeFilterFromPrefs, homeFilterIsDefault, homeFilterParams, normalizeHomeFilter,
} from "./home-filter-model.js";
import {
  currentUsername, displayName, upsertUser, upsertUsers, userAvatar,
} from "./users.js";

const THEME_PREF_KEY = "ark.themePref";
const AVATAR_SIZE = 256;
const AVATAR_MAX_BYTES = 200 * 1024;
const AVATAR_TYPES = ["image/png", "image/jpeg", "image/webp"];
const MIN_PASSWORD = 8;

const prefsState = { profile: null, persist: false, applying: 0, pending: {}, timer: 0, media: null };

// ------------------------------------------------------------ preferences
function systemTheme() {
  try { return window.matchMedia?.("(prefers-color-scheme: dark)")?.matches ? "dark" : "light"; } catch { return "light"; }
}

export function themePref() {
  try {
    const value = localStorage.getItem(THEME_PREF_KEY);
    if (["system", "light", "dark"].includes(value)) return value;
  } catch { /* storage unavailable */ }
  return S.theme === "dark" ? "dark" : "light";
}

function quietly(fn) {
  prefsState.applying += 1;
  try { return fn(); } finally { prefsState.applying -= 1; }
}

/** Apply "system" | "light" | "dark" without echoing it back to the server. */
export function applyThemePref(pref) {
  const value = ["system", "light", "dark"].includes(pref) ? pref : "light";
  try { localStorage.setItem(THEME_PREF_KEY, value); } catch { /* ignore */ }
  const media = window.matchMedia?.("(prefers-color-scheme: dark)");
  if (prefsState.media) {
    prefsState.media.removeEventListener?.("change", onSystemTheme);
    prefsState.media = null;
  }
  if (value === "system" && media) {
    prefsState.media = media;
    media.addEventListener?.("change", onSystemTheme);
  }
  quietly(() => setTheme(value === "system" ? systemTheme() : value));
  for (const select of document.querySelectorAll("[data-theme-pref]")) select.value = value;
}

function onSystemTheme() {
  if (themePref() !== "system") return;
  quietly(() => setTheme(systemTheme()));
  window.dispatchEvent(new CustomEvent("ark:theme-applied"));
}

function queuePersist(patch) {
  if (!prefsState.persist) return;
  Object.assign(prefsState.pending, patch);
  clearTimeout(prefsState.timer);
  prefsState.timer = setTimeout(async () => {
    const prefs = prefsState.pending;
    prefsState.pending = {};
    try {
      const data = await api.updateMe({ prefs });
      prefsState.profile = data.profile;
    } catch { /* offline or read-only: localStorage still has it */ }
  }, 400);
}

function onPrefChange(event) {
  if (prefsState.applying) return;
  const detail = event.detail || {};
  if (detail.theme) {
    // A direct toggle is an explicit choice, never "system".
    try { localStorage.setItem(THEME_PREF_KEY, detail.theme); } catch { /* ignore */ }
    for (const select of document.querySelectorAll("[data-theme-pref]")) select.value = detail.theme;
    queuePersist({ theme: detail.theme });
  }
  if (detail.lang) queuePersist({ lang: detail.lang });
  if (detail.display) queuePersist({ display: detail.display });
}

let listening = false;

/**
 * Boot: adopt the account's saved language/theme (they follow the user across
 * devices) before the first render, then persist later toggles.  A ?lang= or
 * ?theme= query keeps priority, like before.
 */
export function initProfilePrefs(profile) {
  prefsState.profile = profile || null;
  prefsState.persist = Boolean(profile && !profile.permissions?.read_only);
  if (!listening) {
    window.addEventListener("ark:pref-change", onPrefChange);
    listening = true;
  }
  const query = new URLSearchParams(location.search);
  const prefs = profile?.prefs || {};
  if (prefs.display) setDisplayPrefs(prefs.display, { announce: false });
  // A savable account profile is the source of truth for appearance
  // overrides; without one (no account, read-only device) the browser copy is.
  if (prefsState.persist) setAppearanceOverrides(prefs.appearance || {}, { replace: true });
  if (prefsState.persist) refreshAccountPrefs();
  else migrateLegacyGraphDisplay();
  let loginTheme;
  try {
    loginTheme = localStorage.getItem("ark.loginTheme");
    localStorage.removeItem("ark.loginTheme");
  } catch { /* storage unavailable */ }
  if (!query.get("lang") && prefs.lang && prefs.lang !== getLang()) {
    quietly(() => setLanguage(prefs.lang));
  }
  if (!query.get("theme")) {
    const explicitLogin = loginTheme === "light" || loginTheme === "dark";
    const pref = (explicitLogin && loginTheme) || prefs.theme || (() => {
      try { return localStorage.getItem(THEME_PREF_KEY); } catch { return null; }
    })();
    if (pref) applyThemePref(pref);
    if (explicitLogin && prefsState.persist) queuePersist({ theme: loginTheme });
  }
}

/**
 * Save a personal appearance override patch (`{palette|graph|arrows: value|null}`).
 * Signed-in accounts store it with their profile and adopt the server's
 * normalized copy; otherwise it stays in this browser.  Rejects on a server
 * validation error without changing the active override.
 */
export async function saveAppearanceOverride(patch) {
  if (!prefsState.persist) return setAppearanceOverrides(patch);
  const data = await api.updateMe({ prefs: { appearance: patch } });
  prefsState.profile = data.profile;
  return setAppearanceOverrides(data.profile?.prefs?.appearance || {}, { replace: true });
}

// ------------------------------------------- graph display migration (T-0036)
// The Mind and marker settings used to live in this browser only
// ("ark.mind.v1").  On the first load after the move, the values this person
// had changed become their personal override (account settings already
// saved elsewhere win), saved to the account once; the browser copy is then
// renamed so it is never read again.  A failed save leaves it in place and
// the next load tries again; the look is applied right away either way.
function storageGet(key) { try { return localStorage.getItem(key); } catch { return null; } }

// The bootstrap reply that carried `profile` may come from the browser's
// cache (shown at once, revalidated later), so it can predate a change saved
// a moment ago, on this device or another.  Re-read the account from the
// server (bypassing the cached GET /api/me) and adopt its appearance before
// migrating anything into it: otherwise a later save would build on the
// stale copy and drop values.
export const PROFILE_REFRESHED = "ark:profile-refreshed";
let refreshing = null;
export function refreshAccountPrefs() {
  refreshing ||= api.me({ fresh: true }).then((data) => {
    if (!data?.profile) return;
    prefsState.profile = data.profile;
    setAppearanceOverrides(data.profile.prefs?.appearance || {}, { replace: true });
    window.dispatchEvent(new CustomEvent(PROFILE_REFRESHED, { detail: data.profile }));
  }).catch(() => { /* offline: the cached profile stays */ })
    .then(() => migrateLegacyGraphDisplay())
    .finally(() => { refreshing = null; });
  return refreshing;
}

export function migrateLegacyGraphDisplay() {
  const raw = storageGet(LEGACY_MIND_KEY);
  if (raw === null) return null;
  const retire = () => {
    try {
      localStorage.setItem(LEGACY_MIND_DONE_KEY, raw);
      localStorage.removeItem(LEGACY_MIND_KEY);
    } catch { /* storage unavailable */ }
  };
  const { override, changed } = migrateLegacyMind(raw, appearanceOverrides().graph_display);
  if (!changed) { retire(); return null; }
  setAppearanceOverrides({ graph_display: override });
  if (!prefsState.persist) { retire(); return Promise.resolve(override); }
  return api.updateMe({ prefs: { appearance: { graph_display: override } } }).then((data) => {
    prefsState.profile = data.profile;
    setAppearanceOverrides(data.profile?.prefs?.appearance || {}, { replace: true });
    retire();
    return override;
  }).catch(() => null);
}

// ------------------------------------------------ per-project Mind layout
// Card widths and pinned images (mind-node-state.js): on the account profile
// (prefs.mind_layout) when it can be saved, in this browser otherwise.
const localLayout = localLayoutBackend(typeof localStorage === "undefined" ? null : localStorage);

export function mindLayoutEntry(projectId) {
  if (!prefsState.persist) return localLayout.loadEntry(projectId);
  return prefsState.profile?.prefs?.mind_layout?.[projectId] || null;
}

export function saveMindLayoutEntry(projectId, entry) {
  if (!projectId) return;
  if (!prefsState.persist) { localLayout.saveEntry(projectId, entry); return; }
  api.updateMe({ prefs: { mind_layout: { [projectId]: entry } } })
    .then((data) => { prefsState.profile = data.profile; })
    .catch(() => localLayout.saveEntry(projectId, entry));
}

// ------------------------------------------------- Home shows (per project)
// What the Home header graph draws (home-filter-model.js): on the account
// profile (prefs.home_filter), where the server reads it for the overview;
// a read-only device keeps it in this browser and asks for it explicitly.
const HOME_FILTER_KEY = "ark.homeFilter:";

export function homeFilterFor(projectId) {
  if (!prefsState.persist) {
    try {
      const raw = localStorage.getItem(HOME_FILTER_KEY + projectId);
      if (raw) return normalizeHomeFilter(JSON.parse(raw));
    } catch { /* storage unavailable */ }
  }
  return homeFilterFromPrefs(prefsState.profile?.prefs, projectId);
}

/** Query parameters GET /api/project/overview needs (none: the server knows). */
export function homeFilterQuery(projectId) {
  if (prefsState.persist || !projectId) return {};
  const filter = homeFilterFor(projectId);
  return homeFilterIsDefault(filter) ? {} : homeFilterParams(filter);
}

export async function saveHomeFilter(projectId, filter) {
  if (!projectId) return homeFilterFor(projectId);
  const entry = homeFilterEntry(filter);
  if (!prefsState.persist) {
    try {
      if (entry) localStorage.setItem(HOME_FILTER_KEY + projectId, JSON.stringify(entry));
      else localStorage.removeItem(HOME_FILTER_KEY + projectId);
    } catch { /* storage unavailable */ }
    return homeFilterFor(projectId);
  }
  const data = await api.updateMe({ prefs: { home_filter: { [projectId]: entry } } });
  prefsState.profile = data.profile;
  return homeFilterFor(projectId);
}

// ----------------------------------------------------------- account menu
function goToProfile(ctx) {
  try {
    localStorage.setItem("ark.settingsScope", "personal");
    localStorage.setItem("ark.settingsTab", "profile");
  } catch { /* ignore */ }
  if (S.tab === "settings") ctx.refresh?.();
  else ctx.navigateWorkspace?.("settings");
}

/** Avatar button: who is signed in, Profile, Sign out.  `side` places it at
 *  the bottom of the activity bar: the menu then opens to the right of the
 *  bar and upward (openContextMenu flips it above the avatar's bottom edge
 *  when it would run off the screen); otherwise it drops below the top bar. */
export function accountMenuButton(ctx, { side = false } = {}) {
  const username = currentUsername() || S.session?.user || "";
  if (!username) return null;
  const button = h("button", {
    class: `${side ? "wb-act" : "icon-btn"} account-btn`, type: "button",
    title: t("profile.account_menu"), "aria-label": t("profile.account_menu"),
    "aria-haspopup": "menu",
    onclick: (event) => {
      const rect = event.currentTarget.getBoundingClientRect();
      const local = Boolean(S.session?.local);
      const shown = displayName(username);
      openContextMenu({
        ...(side ? { x: rect.right + 6, y: rect.bottom } : { x: rect.right - 240, y: rect.bottom + 6 }),
        returnFocus: event.currentTarget,
        items: [
          { label: shown === username ? shown : `${shown} · @${username}`, icon: "user", disabled: true },
          { separator: true },
          { label: t("profile.title"), icon: "settings", onClick: () => goToProfile(ctx) },
          local ? null : {
            label: t("settings.sign_out"), icon: "external",
            onClick: async () => {
              try { await api.logout(); } catch { /* cookie may already be gone */ }
              location.replace("./login");
            },
          },
        ],
      });
    },
  }, userAvatar(username, { size: "md", title: false }));
  button.dataset.accountUser = username;
  return button;
}

// ------------------------------------------------------------ avatar crop
function readAsDataUrl(blob) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result || ""));
    reader.onerror = () => reject(reader.error || new Error("read failed"));
    reader.readAsDataURL(blob);
  });
}

function canvasBlob(canvas, type, quality) {
  return new Promise((resolve) => canvas.toBlob(resolve, type, quality));
}

/** Square-crop and downscale an image file; resolves {blob, dataUrl}. */
export async function prepareAvatar(file) {
  const type = String(file?.type || "").toLowerCase();
  if (!type.startsWith("image/") || type.includes("svg")) {
    throw new Error(t("profile.avatar_type_error"));
  }
  const url = URL.createObjectURL(file);
  try {
    const img = await new Promise((resolve, reject) => {
      const el = new Image();
      el.onload = () => resolve(el);
      el.onerror = () => reject(new Error(t("profile.avatar_read_error")));
      el.src = url;
    });
    const side = Math.min(img.naturalWidth, img.naturalHeight);
    if (!side) throw new Error(t("profile.avatar_read_error"));
    const size = Math.min(AVATAR_SIZE, side);
    const canvas = document.createElement("canvas");
    canvas.width = size;
    canvas.height = size;
    const g = canvas.getContext("2d");
    g.imageSmoothingQuality = "high";
    g.drawImage(img, (img.naturalWidth - side) / 2, (img.naturalHeight - side) / 2, side, side,
      0, 0, size, size);
    let blob = null;
    for (const [format, quality] of [["image/webp", 0.9], ["image/webp", 0.75], ["image/jpeg", 0.85],
      ["image/jpeg", 0.7], ["image/jpeg", 0.5]]) {
      const candidate = await canvasBlob(canvas, format, quality);
      if (candidate && AVATAR_TYPES.includes(candidate.type) && candidate.size <= AVATAR_MAX_BYTES) {
        blob = candidate;
        break;
      }
    }
    if (!blob) throw new Error(t("profile.avatar_size_error"));
    return { blob, dataUrl: await readAsDataUrl(blob) };
  } finally {
    URL.revokeObjectURL(url);
  }
}

// --------------------------------------------------------- settings panel
const EMAIL_RE = /^[^@\s<>"'(),;:\\]+@[^@\s<>"'(),;:\\]+\.[^@\s<>"'(),;:\\]+$/;

function field(label, input, { hint = null, error = null, full = false, counter = null } = {}) {
  return h("div", { class: `field ${full ? "full" : ""}`.trim() },
    h("label", null, h("span", null, label), counter),
    input,
    hint ? h("div", { class: "profile-hint" }, hint) : null,
    error);
}

function errorLine() {
  return h("div", { class: "profile-error", role: "alert", hidden: true });
}

function setError(el, text) {
  el.textContent = text || "";
  el.hidden = !text;
}

/**
 * Settings -> Personal -> Profile.  Returns [profilePanel, passwordPanel].
 * Everything is staged in a draft and written by the Save button.
 */
export function renderProfilePanels(ctx) {
  const state = { profile: null, options: null, draft: null, saving: false };
  const status = h("span", { class: "settings-status" });
  const username = h("code", { class: "profile-username" });
  const nameInput = h("input", { class: "input", type: "text", autocomplete: "name", maxlength: "80",
    "data-profile-field": "display_name" });
  const bioInput = h("input", { class: "input", type: "text", maxlength: "160",
    placeholder: t("profile.bio_placeholder"), "data-profile-field": "bio" });
  const emailInput = h("input", { class: "input", type: "email", autocomplete: "email", maxlength: "254",
    placeholder: t("profile.email_placeholder"), "data-profile-field": "email" });
  const langSelect = h("select", { class: "select", "data-profile-field": "lang" },
    h("option", { value: "en" }, t("profile.lang_en")),
    h("option", { value: "zh" }, t("profile.lang_zh")));
  const themeSelect = h("select", { class: "select", "data-profile-field": "theme", "data-theme-pref": "" },
    h("option", { value: "system" }, t("profile.theme_system")),
    h("option", { value: "light" }, t("profile.theme_light")),
    h("option", { value: "dark" }, t("profile.theme_dark")));
  const nameError = errorLine();
  const bioError = errorLine();
  const emailError = errorLine();
  const avatarError = errorLine();
  const nameCount = h("span", { class: "hint" });
  const bioCount = h("span", { class: "hint" });
  const preview = h("span", { class: "user-avatar xl profile-preview-avatar", "aria-hidden": "true" });
  const previewName = h("b", { class: "profile-preview-name" });
  const previewBio = h("span", { class: "profile-preview-bio" });
  const swatches = h("div", { class: "profile-swatches", role: "radiogroup", "aria-label": t("profile.avatar_color") });
  const fileInput = h("input", { type: "file", accept: AVATAR_TYPES.join(","), hidden: true,
    "data-profile-field": "avatar_file" });
  const uploadBtn = h("button", { class: "ghost-btn", type: "button", onclick: () => fileInput.click() },
    icon("upload", "sm"), t("profile.avatar_upload"));
  const removeBtn = h("button", { class: "ghost-btn", type: "button" }, icon("trash", "sm"), t("profile.avatar_remove"));
  const saveBtn = h("button", { class: "primary-btn", type: "button", "data-profile-save": "1" },
    icon("save", "sm"), t("profile.save"));
  const readOnlyNote = h("div", { class: "settings-note", hidden: true }, icon("info", "sm"),
    h("div", null, t("profile.read_only_device")));

  const draftColor = () => state.draft?.avatar?.color || state.profile?.avatar?.color || "#4f46e5";

  function paintPreview() {
    if (!state.draft) return;
    const avatar = state.draft.avatar;
    preview.style.setProperty("--user-color", draftColor());
    const image = avatar.kind === "image" ? (avatar.dataUrl || avatar.url) : "";
    preview.classList.toggle("has-image", Boolean(image));
    if (image) {
      preview.replaceChildren(h("img", { src: avatar.dataUrl || avatar.authUrl, alt: "" }));
    } else {
      const text = nameInput.value.trim() || state.profile?.username || "?";
      const parts = text.split(/[\s._@+-]+/).filter(Boolean);
      preview.textContent = (parts.length > 1 ? [...parts[0]][0] + [...parts[1]][0]
        : [...text].slice(0, /[㐀-鿿]/.test(text) ? 1 : 2).join("")).toUpperCase();
    }
    previewName.textContent = nameInput.value.trim() || state.profile?.username || "";
    previewBio.textContent = bioInput.value.trim();
    previewBio.hidden = !previewBio.textContent;
    for (const sw of swatches.children) {
      const on = avatar.kind === "initials" && sw.dataset.color === draftColor();
      sw.classList.toggle("on", on);
      sw.setAttribute("aria-checked", on ? "true" : "false");
    }
    removeBtn.hidden = avatar.kind !== "image";
    nameCount.textContent = `${nameInput.value.trim().length}/80`;
    bioCount.textContent = `${bioInput.value.trim().length}/160`;
  }

  function validate() {
    const name = nameInput.value.trim();
    const email = emailInput.value.trim();
    setError(nameError, !name ? t("profile.error_name_required")
      : name.length > 80 ? t("profile.error_too_long", { n: 80 }) : "");
    setError(bioError, bioInput.value.trim().length > 160 ? t("profile.error_too_long", { n: 160 }) : "");
    setError(emailError, email && !EMAIL_RE.test(email) ? t("profile.error_email") : "");
    return nameError.hidden && bioError.hidden && emailError.hidden;
  }

  function load(data) {
    state.profile = data.profile;
    state.options = data.options || state.options;
    const p = state.profile;
    username.textContent = p.username;
    nameInput.value = p.display_name || "";
    bioInput.value = p.bio || "";
    emailInput.value = p.email || "";
    langSelect.value = p.prefs?.lang || getLang();
    themeSelect.value = p.prefs?.theme || themePref();
    state.draft = {
      avatar: p.avatar?.kind === "image"
        ? { kind: "image", url: p.avatar.url, authUrl: userAvatarUrl(p), color: p.avatar.color }
        : { kind: "initials", color: p.avatar?.color },
    };
    swatches.replaceChildren(...(state.options?.palette || []).map((color) => h("button", {
      class: "profile-swatch", type: "button", role: "radio", title: color,
      "aria-label": `${t("profile.avatar_color")} ${color}`,
      style: { "--swatch": color }, dataset: { color },
      onclick: () => {
        state.draft.avatar = { kind: "initials", color };
        setError(avatarError, "");
        paintPreview();
      },
    })));
    const readOnly = Boolean(p.permissions?.read_only);
    for (const el of [nameInput, bioInput, emailInput, langSelect, themeSelect, uploadBtn, removeBtn, saveBtn]) {
      el.disabled = readOnly;
    }
    for (const sw of swatches.children) sw.disabled = readOnly;
    readOnlyNote.hidden = !readOnly;
    validate();
    paintPreview();
  }

  function userAvatarUrl(profile) {
    const url = profile?.avatar?.url;
    return url ? authenticatedUrl(url) : "";
  }

  for (const input of [nameInput, bioInput, emailInput]) {
    input.addEventListener("input", () => { validate(); paintPreview(); status.textContent = ""; });
  }
  fileInput.addEventListener("change", async () => {
    const file = fileInput.files?.[0];
    fileInput.value = "";
    if (!file) return;
    setError(avatarError, "");
    try {
      const prepared = await prepareAvatar(file);
      state.draft.avatar = { kind: "image", blob: prepared.blob, dataUrl: prepared.dataUrl, color: draftColor() };
      paintPreview();
    } catch (err) {
      setError(avatarError, err.message || String(err));
    }
  });
  removeBtn.addEventListener("click", () => {
    state.draft.avatar = { kind: "initials", color: draftColor() };
    paintPreview();
  });

  // Resolves true when saved; Settings calls it from the tab save bar.
  async function save() {
    if (state.saving || !state.profile) return false;
    if (!validate()) {
      status.replaceChildren(h("span", { class: "pill warn" }, t("profile.fix_errors")));
      return false;
    }
    state.saving = true;
    saveBtn.disabled = true;
    status.replaceChildren(h("span", { class: "pill" }, t("profile.saving")));
    const before = state.profile;
    try {
      const avatar = state.draft.avatar;
      let data = null;
      if (avatar.kind === "image" && avatar.blob) {
        data = await api.uploadAvatar({ content_type: avatar.blob.type, data: avatar.dataUrl });
      }
      const body = {
        display_name: nameInput.value.trim(),
        bio: bioInput.value.trim(),
        email: emailInput.value.trim(),
        // Theme has one picker (Personal › Display); keep whatever it set.
        prefs: { lang: langSelect.value, theme: themePref() },
      };
      if (avatar.kind === "initials"
          && (before.avatar?.kind !== "initials" || before.avatar?.color !== avatar.color)) {
        body.avatar = { kind: "initials", color: avatar.color };
      }
      data = await api.updateMe(body);
      prefsState.profile = data.profile;
      if (data.users) upsertUsers(data.users);
      upsertUser(data.profile);
      load(data);
      status.replaceChildren(h("span", { class: "pill ok" }, t("profile.saved")));
      ctx.toast?.(t("profile.saved"), "ok");
      ctx.renderTopbar?.();
      const theme = data.profile.prefs?.theme;
      if (theme && theme !== themePref()) {
        applyThemePref(theme);
        ctx.renderTopbar?.();
        for (const graph of ctx.graphs || []) graph._draw?.();
      }
      const lang = data.profile.prefs?.lang;
      if (lang && lang !== getLang()) {
        prefsState.applying += 1;
        const pending = ctx.changeLanguage?.(lang);
        prefsState.applying -= 1;
        await pending;
      }
      return true;
    } catch (err) {
      status.replaceChildren(h("span", { class: "pill warn" }, err.message || String(err)));
      if (/avatar/i.test(err.serverMessage || "")) setError(avatarError, err.message);
      return false;
    } finally {
      state.saving = false;
      saveBtn.disabled = Boolean(state.profile?.permissions?.read_only);
    }
  }
  saveBtn.addEventListener("click", save);

  const profilePanel = h("section", { class: "panel settings-panel profile-panel", "data-profile-panel": "1" },
    h("div", { class: "panel-head" },
      h("h3", null, t("profile.title")),
      h("div", { class: "row tight" }, status, saveBtn)),
    h("p", { class: "settings-lead" }, t("profile.lead_prefix"), " ", username, " ", t("profile.lead_suffix")),
    readOnlyNote,
    h("div", { class: "profile-identity" },
      h("div", { class: "profile-preview" }, preview,
        h("div", { class: "profile-preview-copy" }, previewName, previewBio)),
      h("div", { class: "profile-avatar-controls" },
        h("div", { class: "profile-label" }, t("profile.avatar")),
        swatches,
        h("div", { class: "row tight" }, uploadBtn, removeBtn, fileInput),
        h("div", { class: "profile-hint" }, t("profile.avatar_hint")),
        avatarError)),
    h("div", { class: "assistant-api-grid profile-grid" },
      field(t("profile.display_name"), nameInput, { error: nameError, counter: nameCount }),
      field(t("profile.email"), emailInput, { error: emailError, hint: t("profile.email_hint") }),
      field(t("profile.bio"), bioInput, { error: bioError, counter: bioCount, full: true }),
      field(t("profile.language"), langSelect, { hint: t("profile.prefs_hint") })));

  const passwordPanel = renderPasswordPanel(ctx);
  const boot = prefsState.profile;
  api.me().then(load).catch((err) => {
    status.replaceChildren(h("span", { class: "pill warn" }, err.message || String(err)));
    if (boot) load({ profile: boot, options: { palette: [] } });
  });
  // Settings saves the profile from its tab save bar instead of the panel button.
  profilePanel.arkSave = save;
  // Restore every field, the avatar draft and errors from the last loaded profile.
  profilePanel.arkDiscard = () => {
    fileInput.value = "";
    for (const el of [nameError, emailError, bioError, avatarError]) setError(el, "");
    status.replaceChildren();
    if (state.profile) load({ profile: state.profile, options: state.options });
  };
  return [profilePanel, passwordPanel];
}

function renderPasswordPanel(ctx) {
  const session = S.session || {};
  const panel = h("section", { class: "panel settings-panel profile-password", "data-password-panel": "1" },
    h("div", { class: "panel-head" }, h("h3", null, t("profile.change_password"))));
  if (session.local) {
    panel.append(h("div", { class: "settings-note" }, icon("info", "sm"),
      h("div", null, t("profile.password_local"))));
    return panel;
  }
  if (prefsState.profile?.permissions?.device) {
    panel.append(h("div", { class: "settings-note" }, icon("info", "sm"),
      h("div", null, t("profile.password_device"))));
    return panel;
  }
  const current = h("input", { class: "input", type: "password", autocomplete: "current-password",
    "data-password-field": "current" });
  const next = h("input", { class: "input", type: "password", autocomplete: "new-password",
    "data-password-field": "new" });
  const confirm = h("input", { class: "input", type: "password", autocomplete: "new-password",
    "data-password-field": "confirm" });
  const error = errorLine();
  const status = h("span", { class: "settings-status" });
  // Personal agent tokens survive a password change unless this is ticked.
  const revokeTokens = h("input", { type: "checkbox", "data-password-revoke-tokens": "1" });
  const button = h("button", { class: "primary-btn", type: "button", "data-password-save": "1" },
    icon("shield", "sm"), t("profile.change_password"));
  const check = () => {
    if (next.value && next.value.length < MIN_PASSWORD) return t("profile.password_short", { n: MIN_PASSWORD });
    if (confirm.value && next.value !== confirm.value) return t("profile.password_mismatch");
    return "";
  };
  for (const input of [next, confirm]) input.addEventListener("input", () => setError(error, check()));
  button.addEventListener("click", async () => {
    const problem = !current.value || !next.value || !confirm.value ? t("profile.password_required") : check();
    setError(error, problem);
    if (problem) return;
    button.disabled = true;
    try {
      const data = await api.changeOwnPassword({
        current_password: current.value, new_password: next.value, confirm_password: confirm.value,
        revoke_agent_tokens: revokeTokens.checked,
      });
      current.value = next.value = confirm.value = "";
      revokeTokens.checked = false;
      let message = t("profile.password_changed", {
        n: (data.sessions_revoked || 0) + (data.devices_revoked || 0),
      });
      if (data.agent_tokens_revoked) message += ` ${t("tokens.password_revoked", { n: data.agent_tokens_revoked })}`;
      status.replaceChildren(h("span", { class: "pill ok" }, message));
      ctx.toast?.(message, "ok");
    } catch (err) {
      setError(error, err.status === 429 ? t("profile.password_rate_limited") : (err.message || String(err)));
    } finally {
      button.disabled = false;
    }
  });
  panel.append(
    h("p", { class: "settings-lead" }, t("profile.password_lead")),
    h("div", { class: "assistant-api-grid" },
      field(t("profile.current_password"), current, { full: true }),
      field(t("profile.new_password"), next, { hint: t("profile.password_short", { n: MIN_PASSWORD }) }),
      field(t("profile.confirm_password"), confirm)),
    h("label", { class: "row tight profile-revoke-tokens" }, revokeTokens,
      h("span", null, t("tokens.password_revoke"))),
    error,
    h("div", { class: "assistant-api-actions" }, button, status));
  return panel;
}
