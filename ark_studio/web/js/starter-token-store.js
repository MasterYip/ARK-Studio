// Older builds kept the Home starter token value in localStorage
// ("ark.starterToken:<user>:<project>").  The server now keeps it sealed and
// hands it out on Copy prompt (views/agent-tokens.js), so values found here
// are removed: on sign-out (api.js clearClientData) and when agent-tokens.js
// loads.  No DOM; `storage` is localStorage or a stand-in
// (tests/js/starter_token_store.test.mjs).

const PREFIX = "ark.starterToken:";

/** Forget one account's stored values (`user`), or all of them (no `user`). */
export function clearStarterTokens(storage, user = null) {
  if (!storage) return;
  const mine = user == null ? PREFIX : `${PREFIX}${user}:`;
  const found = [];
  try {
    for (let i = 0; i < storage.length; i++) {
      const key = storage.key(i);
      if (key && key.startsWith(mine)) found.push(key);
    }
  } catch { /* storage disabled */ }
  for (const key of found) {
    try { storage.removeItem(key); } catch { /* ignore */ }
  }
}