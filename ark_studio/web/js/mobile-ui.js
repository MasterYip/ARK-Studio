// DOM-side mobile/PWA helpers: service worker registration, install prompt and
// the pairing gate shown when a phone opens ARK without a live session token.

import { h, clear } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import {
  isEditableTarget, isStandalone, keyboardTracker, pwaState,
} from "./mobile-core.js";

let installEvent = null;
let pwaPromise = null;
const initialSecure = typeof window !== "undefined" ? Boolean(window.isSecureContext) : false;
const initialStandalone = typeof window !== "undefined"
  && Boolean(window.matchMedia?.("(display-mode: standalone)")?.matches);
let lastState = {
  ...pwaState({
    secureContext: initialSecure,
    hostname: typeof location !== "undefined" ? location.hostname : "",
    standalone: initialStandalone,
    serviceWorker: false,
    manifest: true,
  }),
  registered: false,
  secure: initialSecure,
};

export function pwaSnapshot() {
  return { ...lastState };
}

function emitState() {
  try {
    window.dispatchEvent(new CustomEvent("ark:pwa-state", { detail: { ...lastState } }));
  } catch { /* non-browser */ }
}

export function captureInstallPrompt() {
  if (typeof window === "undefined") return;
  window.addEventListener("beforeinstallprompt", (event) => {
    event.preventDefault();
    installEvent = event;
    lastState = { ...lastState, canInstall: true, reason: "browser-install" };
    emitState();
  });
  window.addEventListener("appinstalled", () => {
    installEvent = null;
    lastState = { ...lastState, canInstall: false, installed: true, reason: "installed" };
    emitState();
  });
}

export async function promptInstall() {
  if (!installEvent) return false;
  try {
    installEvent.prompt();
    const choice = await installEvent.userChoice;
    const accepted = choice?.outcome === "accepted";
    installEvent = null;
    lastState = {
      ...lastState,
      installed: accepted,
      canInstall: false,
      reason: accepted ? "installed" : "browser-install",
    };
    emitState();
    return accepted;
  } catch {
    return false;
  }
}

export async function registerServiceWorker() {
  if (typeof window === "undefined") return pwaSnapshot();
  captureInstallPrompt();
  const secure = Boolean(window.isSecureContext);
  const displayMode = window.matchMedia?.("(display-mode: standalone)")?.matches ? "standalone" : "browser";
  let registered = false;
  if (secure && "serviceWorker" in navigator) {
    try {
      // Relative registration keeps the scope inside a path-prefixed tunnel.
      // updateViaCache:none ensures a new sw.js is fetched, not reused from the
      // HTTP cache.  A new version never reloads the page by itself: it
      // announces "ark:update-ready" and the app offers a reload, so an
      // update cannot interrupt work in progress.
      // On a first visit the page has no controller yet; that first takeover
      // is the initial install, not an update.  Every later one is.
      let initialClaim = !navigator.serviceWorker.controller;
      const registration = await navigator.serviceWorker.register("./sw.js", {
        scope: "./", updateViaCache: "none",
      });
      registered = Boolean(registration);
      navigator.serviceWorker.addEventListener("controllerchange", () => {
        if (initialClaim) { initialClaim = false; return; }
        window.dispatchEvent(new CustomEvent("ark:update-ready"));
      });
      registration.addEventListener("updatefound", () => {
        const worker = registration.installing;
        if (!worker) return;
        worker.addEventListener("statechange", () => {
          if (worker.state === "installed" && navigator.serviceWorker.controller) {
            worker.postMessage({ type: "SKIP_WAITING" });
          }
        });
      });
      registration.update().catch(() => {});
    } catch {
      registered = false;
    }
  }
  lastState = {
    ...pwaState({
      secureContext: secure,
      hostname: location.hostname,
      standalone: isStandalone(displayMode),
      serviceWorker: registered,
      manifest: Boolean(document.querySelector('link[rel="manifest"]')),
    }),
    registered,
    secure,
  };
  document.documentElement.dataset.pwa = lastState.installed ? "installed"
    : lastState.registered ? "ready"
      : lastState.canCache ? "no-worker" : "insecure";
  emitState();
  return pwaSnapshot();
}

export function initPwa() {
  if (!pwaPromise) pwaPromise = registerServiceWorker();
  return pwaPromise;
}

// Phone shell: while the on-screen keyboard is open the fixed bottom tab bar
// would sit on top of it (or be pushed off-screen when the layout viewport
// shrinks), so <html> gets .keyboard-open and mobile.css hides the bar; it
// comes back as soon as the keyboard closes or the field loses focus.
let keyboardWatching = false;
export function watchKeyboard() {
  if (keyboardWatching || typeof window === "undefined") return;
  keyboardWatching = true;
  const root = document.documentElement;
  const vv = window.visualViewport;
  const sample = keyboardTracker();
  let frame = 0;
  const update = () => {
    frame = 0;
    const open = sample({
      width: window.innerWidth,
      visibleHeight: vv ? vv.height * (vv.scale || 1) : window.innerHeight,
      layoutHeight: window.innerHeight,
      editableFocused: isEditableTarget(document.activeElement),
    });
    if (root.classList.contains("keyboard-open") !== open) root.classList.toggle("keyboard-open", open);
  };
  const schedule = () => { if (!frame) frame = requestAnimationFrame(update); };
  vv?.addEventListener("resize", schedule);
  window.addEventListener("resize", schedule);
  window.addEventListener("orientationchange", schedule);
  document.addEventListener("focusin", schedule);
  // focusout fires before the next element gains focus: wait a tick.
  document.addEventListener("focusout", () => setTimeout(schedule, 60));
  update();
}

// Update the browser chrome colour when the user switches theme.
export function syncThemeColor(theme) {
  const meta = document.querySelector('meta[name="theme-color"]:not([media])');
  if (meta) meta.setAttribute("content", theme === "light" ? "#f5f6f8" : "#0f141c");
}

export function renderPairGate(host, { session, api, onConnected, error = "", busy = false } = {}) {
  clear(host);
  const project = session?.project?.name || "ARK Studio";
  const reason = session?.reason || "";
  const message = reason === "invalid_or_expired"
    ? t("mobile.gate.code_used")
    : reason === "wrong_project"
      ? t("mobile.gate.wrong_project")
      : t("mobile.gate.needs_code");
  const input = h("input", {
    class: "input pair-gate-input", type: "text", inputmode: "text",
    autocomplete: "one-time-code", autocapitalize: "off", spellcheck: "false",
    placeholder: t("mobile.gate.code_placeholder"),
    "aria-label": t("mobile.gate.code_label"),
  });
  const status = h("div", { class: "pair-gate-status", role: "status" });
  const submit = h("button", { class: "primary-btn pair-gate-submit", type: "submit" }, icon("link", "sm"), t("mobile.gate.connect"));
  const form = h("form", {
    class: "pair-gate-form",
    onsubmit: async (event) => {
      event.preventDefault();
      const token = input.value.trim();
      if (!token) {
        status.textContent = t("mobile.gate.code_missing");
        status.className = "pair-gate-status err";
        return;
      }
      submit.disabled = true;
      status.textContent = t("mobile.gate.checking");
      status.className = "pair-gate-status";
      try {
        const prompt = confirmCodeOverlay();
        let data;
        try {
          data = await api.claimPairing(token, prompt);
        } finally {
          prompt.close();
        }
        await onConnected?.(data);
      } catch (err) {
        status.textContent = err.message || t("mobile.gate.failed");
        status.className = "pair-gate-status err";
      } finally {
        submit.disabled = false;
      }
    },
  }, input, submit);
  const card = h("section", { class: "pair-gate-card" },
    h("div", { class: "pair-gate-brand" },
      h("img", { src: "./brand/ark-mark.svg", alt: "", width: 34, height: 34 }),
      h("div", null, h("b", null, "ARK"), h("span", null, "Studio"))),
    h("div", { class: "pair-gate-eyebrow" }, icon("shield", "sm"), t("mobile.gate.eyebrow")),
    h("h1", null, t("mobile.gate.title", { project })),
    h("p", { class: "pair-gate-lead" }, message),
    error ? h("div", { class: "pair-gate-status err" }, error) : null,
    busy ? h("div", { class: "pair-gate-status" }, h("span", { class: "spinner" }), t("mobile.gate.connecting")) : form,
    h("p", { class: "pair-gate-hint" }, t("mobile.gate.hint")),
    h("div", { class: "pair-gate-facts" },
      h("span", null, icon("clock", "sm"), t("mobile.gate.fact_expire")),
      h("span", null, icon("shield", "sm"), t("mobile.gate.fact_read_only")),
      h("span", null, icon("link", "sm"), t("mobile.gate.fact_tunnel"))),
    h("p", { class: "pair-gate-foot" }, t("mobile.gate.foot")),
    status);
  host.appendChild(h("div", { class: "pair-gate-wrap" }, card));
}

// Small honest status row used by Settings → Mobile.
export function pwaStatusRow() {
  const row = h("div", { class: "pwa-status" });
  const refresh = () => {
    const state = pwaSnapshot();
    row.className = `pwa-status ${state.installed ? "ok" : state.canCache ? "warn" : "bad"}`;
    const text = t(state.installed ? "mobile.pwa.installed"
      : !state.secure ? "mobile.pwa.insecure"
        : state.registered ? "mobile.pwa.registered"
          : state.canCache ? "mobile.pwa.menu" : "mobile.pwa.no_sw");
    clear(row);
    row.append(icon(state.installed ? "check" : "info", "sm"), h("span", null, text));
  };
  refresh();
  if (typeof window !== "undefined") window.addEventListener("ark:pwa-state", refresh);
  return row;
}

// While a claim through a Cloudflare Tunnel waits for the computer, the phone
// shows the 6-digit code to compare with the one in Settings › Mobile there.
export function confirmCodeOverlay() {
  let cancelled = false;
  const digits = h("div", { class: "pair-confirm-code", "aria-live": "polite" });
  const left = h("div", { class: "pair-confirm-left" });
  const el = h("div", { class: "pair-confirm", role: "dialog", "aria-modal": "true" },
    h("div", { class: "pair-confirm-card" },
      h("div", { class: "pair-gate-eyebrow" }, icon("shield", "sm"), t("mobile.confirm.eyebrow")),
      h("h2", null, t("mobile.confirm.title")),
      digits,
      h("p", { class: "pair-confirm-lead" }, t("mobile.confirm.lead")),
      left,
      h("button", { class: "btn ghost", type: "button", onclick: () => { cancelled = true; close(); } },
        t("action.cancel"))));
  function close() { el.remove(); }
  return {
    onPending(code, seconds) {
      if (!el.isConnected) document.body.appendChild(el);
      digits.textContent = String(code || "").replace(/^(\d{3})(\d{3})$/, "$1 $2");
      left.textContent = seconds ? t("mobile.confirm.left", { n: Math.ceil(seconds) }) : "";
    },
    isCancelled: () => cancelled,
    close,
  };
}

