// The Home starter token this browser minted, per account and project
// (views/agent-tokens.js copyPromptWithToken).  The server keeps only a
// hash, so the value can be reused only from here: a later Copy prompt
// copies at once (no request, no dialog, no rotation).  Removed on sign-out
// (api.js clearClientData), when the token is revoked in Settings, and when
// the server no longer lists it.  No DOM; `storage` is localStorage or a
// stand-in (tests/js/starter_token_store.test.mjs).

const PREFIX = "ark.starterToken:";

export function starterTokenKey(user, projectId) {
  return `${PREFIX}${user || "local"}:${projectId || ""}`;
}

/** The stored token, or null when none, unreadable or expired at `now`. */
export function loadStarterToken(storage, user, projectId, now = Date.now()) {
  let item = null;
  try { item = JSON.parse(storage.getItem(starterTokenKey(user, projectId)) || "null"); } catch { return null; }
  if (!item || typeof item.token !== "string" || !item.token || !item.id) return null;
  if (item.expires_at) {
    const end = Date.parse(item.expires_at);
    if (!Number.isFinite(end) || end <= now) return null;
  }
  return item;
}

export function saveStarterToken(storage, user, projectId, { token, id, expires_at = null }) {
  if (!token || !id) return;
  try {
    storage.setItem(starterTokenKey(user, projectId), JSON.stringify({ token, id, expires_at: expires_at || null }));
  } catch { /* storage full or disabled */ }
}

function keys(storage) {
  const out = [];
  try {
    for (let i = 0; i < storage.length; i++) {
      const key = storage.key(i);
      if (key && key.startsWith(PREFIX)) out.push(key);
    }
  } catch { /* storage disabled */ }
  return out;
}

/** Forget one account's tokens (`user`), or every stored token (no `user`). */
export function clearStarterTokens(storage, user = null) {
  const mine = user == null ? PREFIX : `${PREFIX}${user}:`;
  for (const key of keys(storage)) {
    if (key.startsWith(mine)) { try { storage.removeItem(key); } catch { /* ignore */ } }
  }
}

/** Forget the stored token with this server id (revoked in Settings). */
export function forgetStarterTokenId(storage, id) {
  if (!id) return;
  for (const key of keys(storage)) {
    let item = null;
    try { item = JSON.parse(storage.getItem(key) || "null"); } catch { /* unreadable: drop below */ }
    if (!item || item.id === id) { try { storage.removeItem(key); } catch { /* ignore */ } }
  }
}

/**
 * After a reuse: is the stored token still usable according to the server's
 * list (GET /api/personal/agent-tokens items)?  Unknown list: assume yes.
 */
export function starterTokenListed(tokens, id) {
  if (!Array.isArray(tokens)) return true;
  const item = tokens.find((entry) => entry?.id === id);
  return Boolean(item && !item.expired);
}
