// Cloudflare Tunnel pairing: pure helpers shared by Settings › Mobile and the
// phone's claim flow (no DOM, no imports; tests/js/mobile_tunnel_model.test.mjs).

// Tunnel states from GET /api/mobile/tunnel → i18n key suffix and pill tone.
const STATES = {
  off: ["off", ""],
  starting: ["starting", "warn"],
  downloading: ["downloading", "warn"],
  restarting: ["restarting", "warn"],
  running: ["running", "ok"],
  error: ["error", "bad"],
  stopped: ["off", ""],
};

export function tunnelStateView(status) {
  const [key, tone] = STATES[status?.state] || STATES.off;
  return { key: `settings.tunnel.state_${key}`, tone };
}

// True while Settings should poll the tunnel (it is changing on its own).
export function tunnelBusy(status) {
  return Boolean(status?.enabled) && ["starting", "downloading", "restarting"].includes(status?.state);
}

// The re-link notice: shown when the tunnel's address changed since a phone
// may have paired (the previous address is known and differs).
export function relinkNotice(status) {
  if (!status || status.state !== "running" || !status.url || !status.previous_url) return null;
  if (status.previous_url === status.url) return null;
  return { previous: status.previous_url, url: status.url, code: status.relink_code || "", qr: status.relink_qr_svg || "" };
}

// A short re-link code ("able-baker-charlie-delta") or a pasted address →
// the tunnel's base URL, or "" when it is neither.
export function relinkBaseFromInput(input) {
  const text = String(input || "").trim().toLowerCase();
  if (!text) return "";
  if (/^[a-z0-9]+(?:-[a-z0-9]+)+$/.test(text)) return `https://${text}.trycloudflare.com/`;
  try {
    const url = new URL(text.includes("://") ? text : `https://${text}`);
    if (url.protocol !== "https:" || !url.hostname.includes(".")) return "";
    return `${url.origin}/`;
  } catch {
    return "";
  }
}

// Waits for the computer to confirm a pending claim (202 from
// /api/mobile/pair/claim). post(path, body) performs one request and returns
// the parsed reply; onPending(code, secondsLeft) updates the UI; sleep and
// isCancelled are injectable for tests. Resolves with the final claim reply
// (mobile_token) or throws the server's error.
export async function awaitConfirmedClaim(first, code, { post, onPending, sleep, isCancelled }) {
  let reply = first;
  const cancel = () => {
    post("api/mobile/pair/claim/status", { claim_id: first.claim_id, claim_secret: first.claim_secret, cancel: true })
      .catch(() => {});
    return Object.assign(new Error("cancelled"), { cancelled: true });
  };
  const pause = sleep || ((ms) => new Promise((resolve) => setTimeout(resolve, ms)));
  const deadline = Date.now() + Math.max(30, Number(first?.expires_in) || 180) * 1000;
  while (reply?.pending) {
    onPending?.(reply.confirm_code || first.confirm_code || "", Math.max(0, Number(reply.expires_in) || 0));
    if (isCancelled?.()) throw cancel();
    if (Date.now() > deadline) throw Object.assign(new Error("expired"), { code: "PAIRING_CONFIRM_EXPIRED" });
    await pause(Math.max(1, Number(reply.poll_after) || 2) * 1000);
    if (isCancelled?.()) throw cancel();
    reply = await post("api/mobile/pair/claim/status",
      { code, claim_id: first.claim_id, claim_secret: first.claim_secret });
  }
  return reply;
}
