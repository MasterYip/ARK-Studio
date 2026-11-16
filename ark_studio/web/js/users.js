// Shared user directory: display names and avatars for ARK accounts.
//
// The username stays the stable ID everywhere (assignees, grants, activity
// actors, comment authors).  This module only decides how a username is
// *shown*: the account's display name, with the username kept as a tooltip or
// secondary text, and its avatar (an uploaded image or initials on a palette
// colour).  The directory arrives with /api/bootstrap and is refreshed after a
// profile save; every element rendered through here repaints in place, so a
// rename shows up without a reload.

import { h } from "./dom.js";
import { authenticatedUrl } from "./api.js";

const directory = new Map();
const painters = [];
let meName = "";

const key = (name) => String(name || "").trim().toLowerCase();

/** Replace the directory (bootstrap) and repaint visible names/avatars. */
export function setUserDirectory(users = [], profile = null) {
  if (!Array.isArray(users)) return;
  directory.clear();
  for (const card of users) if (card?.username) directory.set(key(card.username), card);
  if (profile?.username) {
    meName = profile.username;
    directory.set(key(profile.username), { ...(directory.get(key(profile.username)) || {}), ...profile });
  }
  repaintUsers();
}

/** Merge one account card (e.g. the saved profile) and repaint. */
export function upsertUser(card) {
  if (!card?.username) return;
  directory.set(key(card.username), { ...(directory.get(key(card.username)) || {}), ...card });
  repaintUsers();
}

/** Merge several cards (e.g. the admin user list) with one repaint. */
export function upsertUsers(cards = []) {
  for (const card of cards || []) {
    if (!card?.username) continue;
    const merged = { ...(directory.get(key(card.username)) || {}) };
    for (const field of ["username", "display_name", "avatar", "bio", "email", "disabled"]) {
      if (card[field] !== undefined) merged[field] = card[field];
    }
    directory.set(key(card.username), merged);
  }
  repaintUsers();
}

export function currentUsername() { return meName; }

export function userCard(username) {
  return directory.get(key(username)) || null;
}

/** Display name for a username, falling back to the username itself. */
export function displayName(username) {
  const name = String(username || "");
  const card = userCard(name);
  return String(card?.display_name || "").trim() || name;
}

/** "Display Name (username)" when they differ, for pickers and tooltips. */
export function userLabel(username) {
  const name = String(username || "");
  const shown = displayName(name);
  return shown && shown !== name ? `${shown} (${name})` : name;
}

export function userInitials(username) {
  const text = displayName(username);
  const parts = text.split(/[\s._@+-]+/).filter(Boolean);
  const chars = parts.length > 1
    ? [...parts[0]][0] + [...parts[1]][0]
    : [...text].slice(0, /[㐀-鿿]/.test(text) ? 1 : 2).join("");
  return (chars || "?").toUpperCase();
}

function hue(name = "") {
  let n = 0;
  for (const ch of String(name).toLowerCase()) n = (n * 31 + ch.codePointAt(0)) % 360;
  return n;
}

/** Avatar colour from the profile, or a stable hue for unknown names. */
export function userColor(username) {
  const color = userCard(username)?.avatar?.color;
  return /^#[0-9a-f]{6}$/i.test(String(color || "")) ? color : `hsl(${hue(username)} 55% 45%)`;
}

export function avatarImageUrl(username) {
  const avatar = userCard(username)?.avatar;
  return avatar?.kind === "image" && avatar.url ? authenticatedUrl(avatar.url) : "";
}

/** Fill an element with the avatar for a username (img or initials). */
export function paintAvatar(el, username) {
  const url = avatarImageUrl(username);
  const color = userColor(username);
  el.style.setProperty("--user-color", color);
  if (userCard(username)?.avatar?.color) el.style.setProperty("--assignee-color", color);
  el.classList.toggle("has-image", Boolean(url));
  if (url) {
    const current = el.querySelector("img");
    if (current?.getAttribute("src") === url) return;
    el.replaceChildren(h("img", { src: url, alt: "", loading: "lazy", decoding: "async",
      draggable: "false" }));
  } else {
    el.textContent = userInitials(username);
  }
}

/** Round avatar for a username.  size: "xs" | "sm" | "md" | "lg". */
export function userAvatar(username, { size = "sm", cls = "", title = true } = {}) {
  const el = h("span", {
    class: `user-avatar ${size} ${cls}`.trim(), "aria-hidden": "true",
    dataset: { userAvatar: String(username || "") },
    title: title ? userLabel(username) : undefined,
  });
  paintAvatar(el, username);
  return el;
}

/** Display-name text with the username as tooltip; repaints after a rename. */
export function userName(username, { tag = "span", cls = "" } = {}) {
  const name = String(username || "");
  return h(tag, {
    class: `user-name ${cls}`.trim(), title: name,
    dataset: { userName: name },
  }, displayName(name));
}

/** Avatar + display name (+ optional muted @username). */
export function userBadge(username, { size = "sm", showUsername = false, cls = "" } = {}) {
  const name = String(username || "");
  const shown = displayName(name);
  return h("span", { class: `user-badge ${cls}`.trim(), title: name },
    userAvatar(name, { size, title: false }),
    userName(name),
    showUsername && shown !== name ? h("span", { class: "user-handle" }, `@${name}`) : null);
}

/**
 * Register an in-place repaint for elements other modules render (for example
 * assignee chips).  fn(el) is called for each element matching selector.
 */
export function registerUserPainter(selector, fn) {
  painters.push({ selector, fn });
}

export function repaintUsers(root = typeof document !== "undefined" ? document : null) {
  if (!root?.querySelectorAll) return;
  for (const el of root.querySelectorAll("[data-user-avatar]")) {
    paintAvatar(el, el.dataset.userAvatar);
    if (el.title) el.title = userLabel(el.dataset.userAvatar);
  }
  for (const el of root.querySelectorAll("[data-user-name]")) {
    el.textContent = displayName(el.dataset.userName);
  }
  for (const el of root.querySelectorAll("option[data-user-option]")) {
    el.textContent = userLabel(el.dataset.userOption);
  }
  for (const { selector, fn } of painters) {
    for (const el of root.querySelectorAll(selector)) {
      try { fn(el); } catch { /* a stale element must not stop the others */ }
    }
  }
  try {
    window.dispatchEvent(new CustomEvent("ark:users-changed"));
  } catch { /* non-browser */ }
}
