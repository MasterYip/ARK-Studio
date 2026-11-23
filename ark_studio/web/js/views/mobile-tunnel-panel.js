// Settings › Mobile: let phones reach this computer through a Cloudflare
// Tunnel (local server only; server side in mobile_tunnel.go). Shows the
// switch, the tunnel state and address, pairing requests waiting for the
// 6-digit confirmation, the re-link QR after the address changed, and the
// named-tunnel options.

import { h, clear, copyText, relativeTime } from "../dom.js";
import { t } from "../i18n.js";
import { icon } from "../icons.js";
import { api } from "../api.js";
import { relinkNotice, tunnelBusy, tunnelStateView } from "../mobile-tunnel-model.js";

export function mobileTunnelPanel(ctx, { onUrlChange } = {}) {
  let status = null;
  let pending = [];
  let timer = null;
  let lastUrl = null;
  let saving = false;

  const enabledBox = h("input", { type: "checkbox", "data-tunnel-enabled": "" });
  const confirmBox = h("input", { type: "checkbox", "data-tunnel-confirm": "" });
  const pill = h("span", { class: "pill" });
  const statusHost = h("div", { class: "stack", style: { gap: "6px" } });
  const pendingHost = h("div", { class: "stack", style: { gap: "6px" } });
  const relinkHost = h("div", { class: "stack", style: { gap: "6px" } });
  const modeSelect = h("select", { class: "select", "aria-label": t("settings.tunnel.mode") },
    h("option", { value: "quick" }, t("settings.tunnel.mode_quick")),
    h("option", { value: "named" }, t("settings.tunnel.mode_named")));
  const hostInput = h("input", { class: "input", placeholder: "ark.example.com", autocapitalize: "off", spellcheck: "false" });
  const tokenInput = h("input", { class: "input", type: "password", autocomplete: "off", spellcheck: "false" });
  const pathInput = h("input", { class: "input", placeholder: t("settings.tunnel.binary_auto"), spellcheck: "false" });
  const namedHint = h("div", { class: "physics-hint" });
  const binaryHint = h("div", { class: "physics-hint" });
  const saveAdvanced = h("button", { class: "btn sm", type: "button" }, t("settings.tunnel.save_advanced"));
  const advanced = h("details", { class: "tunnel-advanced" },
    h("summary", null, t("settings.tunnel.advanced")),
    h("div", { class: "stack", style: { gap: "8px", marginTop: "8px" } },
      h("div", { class: "assistant-api-grid" },
        h("div", { class: "field" }, h("label", null, t("settings.tunnel.mode")), modeSelect),
        h("div", { class: "field" }, h("label", null, t("settings.tunnel.hostname")), hostInput),
        h("div", { class: "field" }, h("label", null, t("settings.tunnel.token")), tokenInput)),
      namedHint,
      h("div", { class: "field" }, h("label", null, t("settings.tunnel.binary")), pathInput),
      binaryHint,
      h("div", { class: "mobile-actions" }, saveAdvanced)));

  const el = h("div", { class: "stack mobile-tunnel hidden", style: { gap: "8px", marginTop: "10px" }, "data-mobile-tunnel": "" },
    h("div", { class: "tunnel-head" },
      h("label", null, enabledBox, h("b", null, t("settings.tunnel.switch"))), pill),
    h("p", { class: "settings-lead", style: { margin: 0 } }, t("settings.tunnel.lead")),
    h("div", { class: "settings-note" }, icon("alert", "sm"), h("div", null, t("settings.tunnel.network_note"))),
    statusHost,
    pendingHost,
    relinkHost,
    h("label", { class: "row tight", title: t("settings.tunnel.confirm_hint") }, confirmBox,
      h("span", null, t("settings.tunnel.confirm"))),
    h("div", { class: "physics-hint" }, t("settings.tunnel.confirm_hint")),
    advanced);

  async function save(body, { quiet = false } = {}) {
    if (saving) return;
    saving = true;
    try {
      status = await api.setMobileTunnel(body);
      render();
      if (!quiet) ctx.toast(t("settings.tunnel.saved"), "ok");
    } catch (err) {
      ctx.toast(err.message, "err");
      render();
    } finally {
      saving = false;
      schedule();
    }
  }

  enabledBox.addEventListener("change", () => save({ enabled: enabledBox.checked }, { quiet: true }));
  confirmBox.addEventListener("change", () => save({ require_confirm: confirmBox.checked }));
  saveAdvanced.addEventListener("click", () => {
    const body = { mode: modeSelect.value, hostname: hostInput.value.trim(), cloudflared_path: pathInput.value.trim() };
    if (tokenInput.value.trim()) body.token = tokenInput.value.trim();
    tokenInput.value = "";
    save(body);
  });

  function renderStatus() {
    clear(statusHost);
    const view = tunnelStateView(status);
    pill.textContent = t(view.key);
    pill.className = `pill ${view.tone}`.trim();
    if (!status.enabled) return;
    if (status.state === "downloading") {
      const dl = status.download || {};
      const mb = (n) => (n / 1048576).toFixed(1);
      statusHost.appendChild(h("div", { class: "physics-hint" }, h("span", { class: "spinner" }),
        t("settings.tunnel.downloading", { version: status.cloudflared?.version || "" }),
        dl.done ? ` ${mb(dl.done)}${dl.total ? ` / ${mb(dl.total)}` : ""} MB` : ""));
    } else if (status.state === "starting" || status.state === "restarting") {
      statusHost.appendChild(h("div", { class: "physics-hint" }, h("span", { class: "spinner" }), t("settings.tunnel.starting")));
    }
    if (status.url) {
      const copy = h("button", { class: "btn sm ghost", type: "button" }, icon("copy", "sm"), t("action.copy_link"));
      copy.onclick = async () => {
        const ok = await copyText(status.url);
        ctx.toast(t(ok ? "settings.mobile.link_copied" : "settings.clipboard_unavailable"), ok ? "ok" : "err");
      };
      statusHost.appendChild(h("div", { class: "row tight", style: { flexWrap: "wrap", gap: "8px" } },
        h("span", { class: "physics-name" }, t("settings.tunnel.address")),
        h("span", { class: "tunnel-url" }, status.url), copy));
      if (status.mode === "quick") {
        statusHost.appendChild(h("div", { class: "physics-hint" }, t("settings.tunnel.quick_changes")));
      }
    }
    if (status.error && status.state !== "running") {
      statusHost.appendChild(h("div", { class: "settings-note" }, icon("alert", "sm"),
        h("div", null, h("b", null, t("settings.tunnel.error")), " ", status.error)));
    }
    if ((status.log || []).length && status.state !== "running") {
      statusHost.appendChild(h("details", null, h("summary", null, t("settings.tunnel.log")),
        h("pre", { class: "mobile-tunnel-command tunnel-log" }, status.log.join("\n"))));
    }
  }

  function renderPending() {
    clear(pendingHost);
    for (const item of pending) {
      const who = [item.device, item.user_agent].filter(Boolean).join(" · ") || t("settings.tunnel.unknown_device");
      pendingHost.appendChild(h("div", { class: "tunnel-pending", "data-pending-claim": item.id },
        h("span", { class: "tunnel-pending-code" }, String(item.confirm_code).replace(/^(\d{3})(\d{3})$/, "$1 $2")),
        h("div", { class: "main" },
          h("div", { class: "t" }, t("settings.tunnel.pending_title")),
          h("div", { class: "d" }, `${who} · ${item.client_ip || ""} · ${relativeTime(item.created_at)}`)),
        h("button", { class: "btn sm primary", type: "button", onclick: () => decide(item.id, true) },
          icon("check", "sm"), t("settings.tunnel.approve")),
        h("button", { class: "btn sm danger", type: "button", onclick: () => decide(item.id, false) },
          t("settings.tunnel.reject"))));
    }
    if (pending.length) {
      pendingHost.appendChild(h("div", { class: "physics-hint" }, t("settings.tunnel.pending_hint")));
    }
  }

  async function decide(id, approve) {
    try {
      await api.confirmClaim(id, approve);
      pending = pending.filter((item) => item.id !== id);
      renderPending();
      ctx.toast(t(approve ? "settings.tunnel.approved" : "settings.tunnel.rejected"), "ok");
    } catch (err) {
      ctx.toast(err.message, "err");
    }
  }

  function renderRelink() {
    clear(relinkHost);
    const notice = relinkNotice(status);
    if (!notice) return;
    const qr = h("div", { class: "mobile-pair-qr" });
    qr.innerHTML = notice.qr; // generated by the server's own QR encoder
    relinkHost.appendChild(h("div", { class: "settings-note" }, icon("refresh", "sm"),
      h("div", null, h("b", null, t("settings.tunnel.relink_title")), " ", t("settings.tunnel.relink_lead"))));
    relinkHost.appendChild(h("div", { class: "tunnel-relink" }, qr,
      h("div", { class: "stack", style: { gap: "6px", flex: "1 1 200px" } },
        notice.code ? h("div", null, h("div", { class: "physics-name" }, t("settings.tunnel.relink_code")),
          h("div", { class: "mobile-pair-code" }, notice.code)) : null,
        h("div", { class: "physics-hint" }, t("settings.tunnel.relink_previous", { url: notice.previous })))));
  }

  function renderAdvanced() {
    if (document.activeElement && advanced.contains(document.activeElement)) return;
    modeSelect.value = status.mode || "quick";
    hostInput.value = status.hostname || "";
    tokenInput.placeholder = status.token_set ? t("settings.tunnel.token_saved") : t("settings.tunnel.token_placeholder");
    pathInput.value = status.cloudflared?.custom || "";
    namedHint.textContent = status.local_port && status.mode === "named"
      ? t("settings.tunnel.named_hint_port", { origin: `http://127.0.0.1:${status.local_port}` })
      : t("settings.tunnel.named_hint");
    const bin = status.cloudflared || {};
    binaryHint.textContent = bin.path
      ? t(`settings.tunnel.binary_${bin.source === "download" ? "downloaded" : bin.source === "custom" ? "custom" : "path"}`, { path: bin.path })
      : t("settings.tunnel.binary_none", { dir: bin.download_dir || "" });
  }

  function render() {
    if (!status) return;
    el.classList.toggle("hidden", !status.available);
    if (!status.available) return;
    enabledBox.checked = Boolean(status.enabled);
    confirmBox.checked = status.require_confirm !== false;
    renderStatus();
    renderPending();
    renderRelink();
    renderAdvanced();
    const url = status.state === "running" ? status.url || "" : "";
    if (lastUrl !== null && url !== lastUrl) onUrlChange?.(url);
    lastUrl = url;
  }

  function schedule() {
    if (timer) clearTimeout(timer);
    timer = null;
    if (!status?.available || !status.enabled) return;
    timer = setTimeout(async () => {
      timer = null;
      if (!el.isConnected) return; // Settings closed: stop polling
      await refresh();
    }, tunnelBusy(status) || pending.length || status.state === "running" ? 2000 : 5000);
  }

  async function refresh() {
    try {
      status = await api.mobileTunnel();
      pending = status.available && status.enabled ? (await api.pendingClaims()).pending || [] : [];
      render();
    } catch {
      // A phone or a remote server: no tunnel section.
      el.classList.add("hidden");
      status = null;
      return;
    }
    schedule();
  }

  return { el, refresh };
}
