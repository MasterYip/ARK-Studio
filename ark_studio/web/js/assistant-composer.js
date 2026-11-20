import { h } from "./dom.js";
import { icon } from "./icons.js";
import { t } from "./i18n.js";

/**
 * Compact assistant composer used by both the Chat tab and the session dock.
 * The input is one line high by default and grows up to 140px with content.
 * The composer is a CSS size container (app.css .ai-composer): in a narrow
 * dock the access badge collapses to its icon (label kept as aria-label and
 * tooltip), the model name truncates and the send button stays visible.
 */
export function createAssistantComposer({ placeholder, onSubmit, model = "DeepSeek", onPlus = null }) {
  let busy = false;
  const input = h("textarea", {
    class: "ai-composer-input", rows: "1", placeholder,
    "aria-label": placeholder,
  });
  const resize = () => {
    input.style.height = "auto";
    input.style.height = `${Math.min(140, Math.max(30, input.scrollHeight))}px`;
  };
  const send = h("button", {
    class: "ai-composer-send", type: "button", title: t("chat.send"),
    "aria-label": t("chat.send"), onclick: () => { if (!busy) onSubmit?.(); },
  }, icon("send", "sm"));
  const plus = h("button", {
    class: "ai-composer-plus", type: "button", title: t("composer.add_attachment"),
    "aria-label": t("composer.add_attachment"), onclick: () => onPlus?.(),
  }, icon("plus", "sm"));
  const access = h("button", {
    class: "ai-composer-access", type: "button",
    title: `${t("composer.access_title")}: ${t("composer.full_access")}`,
    "aria-label": t("composer.full_access"),
  }, icon("alert", "sm"), h("span", { class: "ai-composer-access-label" }, t("composer.full_access")));
  const modelBtn = h("button", {
    class: "ai-composer-model", type: "button", title: `${t("composer.model_title")}: ${model}`,
  }, h("span", { class: "ai-composer-model-label" }, model), icon("chevron", "sm"));
  const el = h("div", { class: "ai-composer" },
    input,
    h("div", { class: "ai-composer-bar" },
      plus, access,
      h("span", { class: "spacer" }),
      modelBtn, send));
  input.addEventListener("input", resize);
  input.addEventListener("keydown", (event) => {
    if (event.key === "Enter" && !event.shiftKey && !event.isComposing && !busy) {
      event.preventDefault();
      onSubmit?.();
    }
  });
  // A graph Copy writes both a human-readable text payload and a small HTML
  // payload.  Intercept only the latter here so pasting a node reference into
  // Chat (or the session dock) inserts stable UIDs instead of raw HTML.
  input.addEventListener("paste", (event) => {
    const html = event.clipboardData?.getData("text/html") || "";
    const match = html.match(/data-ark-node-refs="([^"]+)"/);
    if (!match) return;
    let refs = [];
    try {
      const payload = JSON.parse(decodeURIComponent(match[1]));
      refs = (payload.items || []).filter((item) => item && item.uid);
    } catch { return; }
    if (!refs.length) return;
    event.preventDefault();
    const lines = refs.map((item) => `- ${item.uid}${item.type ? ` (${item.type})` : ""}${item.title ? ` — ${item.title}` : ""}`);
    const block = `Referenced ARK nodes:\n${lines.join("\n")}\n\n`; // i18n-ignore: message text sent to the assistant
    const start = input.selectionStart ?? input.value.length;
    const end = input.selectionEnd ?? start;
    const prefix = start > 0 && !input.value.slice(0, start).endsWith("\n") ? "\n" : "";
    input.setRangeText(prefix + block, start, end, "end");
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.focus();
  });
  const setBusy = (value) => {
    busy = Boolean(value);
    el.setAttribute("aria-busy", String(busy));
    send.disabled = value;
    plus.disabled = value;
    modelBtn.disabled = value;
  };
  return {
    el, input, send, setBusy,
    resize,
    clear() { input.value = ""; resize(); },
    focus() { input.focus(); },
  };
}
