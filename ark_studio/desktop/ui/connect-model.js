// The message for a server that did not answer the connect check, from the
// reason the desktop shell reports (probe.rs): certificate, dns, refused,
// proxy, timeout, network.
const REASONS = new Set(["certificate", "dns", "refused", "proxy", "timeout"]);

export function unreachableMessage(res, fallbackUrl, t) {
  const url = res?.url || fallbackUrl || "";
  const reason = res?.reason;
  if (reason === "proxy" && !res.proxy) return t("remote.unreachable", { url });
  if (!REASONS.has(reason)) {
    const text = t("remote.unreachable", { url });
    return res?.error ? `${text} ${t("remote.detail", { detail: res.error })}` : text;
  }
  return t(`remote.unreachable.${reason}`, { url, proxy: res.proxy || "", detail: res.error || "" });
}
