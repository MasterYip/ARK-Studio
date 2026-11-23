import { h, clear, relativeTime } from "../dom.js";
import { t, serverErrorText } from "../i18n.js";
import { icon } from "../icons.js";
import { api } from "../api.js";
import { getShortcut, formatShortcut } from "../shortcuts.js";
import { createAssistantComposer } from "../assistant-composer.js";
import { assistantRichText, createAssistantResponse, followAssistantThread } from "../assistant-response.js";

// The harness label and note come from arkstudio/chat.py in English; show the
// known ones in the UI language and anything else as sent.
/* i18n-ignore-start */
const HARNESS_TEXT_KEYS = {
  "Local ARK harness": "chat.local_harness",
  "Writes through the project API; runs with the .ark folder as cwd.": "chat.harness_note.dsh",
  "ARK_DSH_COMMAND is configured but not on PATH; using the local harness.": "chat.harness_note.dsh_missing",
  "OpenAI-compatible API from Settings → Assistant API; server-side tool calling with reasoning.": "chat.harness_note.deepseek",
  "No DEEPSEEK_API_KEY / DSH command is configured, so this is the deterministic local tool parser, not a model.": "chat.harness_note.local",
};
/* i18n-ignore-end */

function localizeHarness(status) {
  const known = (text) => (HARNESS_TEXT_KEYS[text] ? t(HARNESS_TEXT_KEYS[text]) : text);
  return { ...status, label: known(status.label), note: known(status.note) };
}

export function renderChat(host, ctx) {
  clear(host);
  let messages = [];
  let harness = null;
  let assistantState = null;
  let busy = false;
  let loaded = false;
  const project = ctx.S.project || {};
  const phone = typeof window.matchMedia === "function"
    && window.matchMedia("(max-width: 760px)").matches;

  const page = h("div", { class: `page chat-shell ${phone ? "mobile-chat-page" : ""}`.trim() });
  const head = h("div", { class: "page-head chat-head" },
    h("div", null,
      h("div", { class: "eyebrow" }, `${t("nav.chat")} · ${t("chat.project_agent")}`),
      h("h1", { class: "page-title" }, t("chat.title")),
      h("p", { class: "page-sub" }, t("chat.subtitle")),
      h("p", { class: "page-sub assistant-scope", title: t("chat.scope_hint") }, icon("layers", "sm"), t("chat.scope"))),
    h("div", { class: "head-actions" },
      h("button", { class: "ghost-btn", onclick: () => navigateSettings(ctx) },
        icon("settings", "sm"), t("chat.settings")),
      h("button", { class: "ghost-btn", onclick: clearThread }, icon("trash", "sm"), t("chat.clear_history"))));

  const thread = h("div", { class: "chat-thread", role: "log", "aria-live": "polite" });
  const follow = followAssistantThread(thread);
  const composerUI = createAssistantComposer({
    placeholder: t("chat.placeholder"), onSubmit: () => send(),
  });
  const composer = composerUI.input;
  const composerBar = h("div", { class: "chat-composer chat-composer-modern" }, composerUI.el);

  const side = h("aside", { class: "chat-side" });
  const sideHarness = h("div", { class: "panel chat-side-card" });
  const sideTools = h("div", { class: "panel chat-side-card" });
  const sideSuggest = h("div", { class: "panel chat-side-card" });
  const sideMobileHead = h("div", { class: "chat-side-mobile-head" },
    h("b", null, t("chat.harness")),
    h("button", {
      class: "icon-btn", type: "button", title: t("action.close"),
      onclick: () => closeChatSide(),
    }, icon("x")));
  const sideActions = h("div", { class: "chat-side-actions" },
    h("button", { class: "btn sm ghost", onclick: () => navigateSettings(ctx) },
      icon("settings", "sm"), t("chat.settings")),
    h("button", { class: "btn sm ghost", onclick: () => clearThread() },
      icon("trash", "sm"), t("chat.clear")));
  side.append(sideMobileHead, sideActions, sideHarness, sideTools, sideSuggest);

  const mobileTitle = h("div", { class: "chat-mobile-title" },
    h("span", { class: "chat-mobile-eyebrow" }, t("chat.project_agent")),
    h("b", null, t("chat.title")));
  const mobileHead = h("div", { class: "chat-mobile-head" },
    h("button", {
      class: "sw-mobile-btn", type: "button", title: t("chat.open_status"),
      onclick: () => openChatSide(),
    }, icon("info", "sm"), t("chat.status")),
    mobileTitle,
    h("button", {
      class: "icon-btn", type: "button", title: t("chat.clear"),
      onclick: () => clearThread(),
    }, icon("trash", "sm")));
  const scrim = h("div", {
    class: "chat-scrim hidden", role: "presentation",
    onclick: () => closeChatSide(),
  });
  const body = h("div", { class: "chat-main" },
    h("section", { class: "panel chat-thread-panel" }, mobileHead, thread, composerBar),
    side);
  page.append(head, body, scrim);
  host.appendChild(page);

  function openChatSide() {
    side.classList.add("mobile-open");
    scrim.classList.remove("hidden");
  }

  function closeChatSide() {
    side.classList.remove("mobile-open");
    scrim.classList.add("hidden");
  }

  const setBusy = (value) => {
    busy = value;
    composerUI.setBusy(value);
  };

  function renderHarness() {
    clear(sideHarness);
    const status = localizeHarness(harness || { mode: "local", label: t("chat.local_harness"), available: true, note: "" });
    sideHarness.append(
      h("div", { class: "panel-head" }, h("h3", null, t("chat.harness")),
        h("span", { class: `pill ${status.mode === "dsh" ? "ok" : ""}` }, status.available ? t("chat.ready") : t("chat.offline"))),
      h("div", { class: "chat-harness-name" }, icon(status.mode === "dsh" ? "cpu" : "sparkles", "sm"), status.label),
      h("p", { class: "chat-side-note" }, status.note || t("chat.local_note")));
    if (status.command) sideHarness.append(h("pre", { class: "code-box chat-command" }, status.command));
    if (status.mode !== "dsh") sideHarness.append(h("p", { class: "chat-side-note muted" }, t("chat.dsh_hint")));
    if (assistantState?.personal_config_required) {
      sideHarness.append(h("div", { class: "chat-side-note" },
        icon("alert", "sm"),
        h("div", null,
          h("b", null, t("common.sentence_lead", { text: t("settings.personal_config_required") })),
          t("settings.personal_config_required_hint"),
          h("button", {
            class: "btn sm ghost", type: "button", style: { marginTop: "6px" },
            onclick: () => navigateSettings(ctx, "assistant"),
          }, icon("settings", "sm"), t("settings.personal_assistant")))));
    }
  }

  function renderTools() {
    clear(sideTools);
    sideTools.append(
      h("div", { class: "panel-head" }, h("h3", null, t("chat.tools"))),
      h("div", { class: "chat-tool-list" },
        h("div", null, icon("plus", "sm"), h("span", null, t("chat.tool.create"))),
        h("div", null, icon("link", "sm"), h("span", null, t("chat.tool.link"))),
        h("div", null, icon("edit", "sm"), h("span", null, t("chat.tool.update"))),
        h("div", null, icon("search", "sm"), h("span", null, t("chat.tool.search"))),
        h("div", null, icon("layers", "sm"), h("span", null, t("chat.tool.other_projects"))),
        h("div", null, icon("send", "sm"), h("span", null, t("chat.tool.dispatch")))));
  }

  function renderSuggestions() {
    clear(sideSuggest);
    const items = ["chat.suggest.1", "chat.suggest.2", "chat.suggest.3"];
    sideSuggest.append(
      h("div", { class: "panel-head" }, h("h3", null, t("chat.try")),
        h("span", { class: "kbd" }, formatShortcut(getShortcut("chat"), true))),
      h("div", { class: "chat-prompts" }, ...items.map((key) => h("button", {
        class: "chat-prompt",
        onclick: () => { composer.value = t(key); composer.focus(); },
      }, t(key)))));
  }

  function messageEl(message) {
    const assistant = message.role === "assistant";
    const wrap = h("div", { class: `chat-msg ${assistant ? "assistant" : "user"}` },
      h("div", { class: "chat-avatar" }, assistant ? icon("sparkles", "sm") : t("chat.you").slice(0, 1)),
      h("div", { class: "chat-bubble" },
        h("div", { class: "chat-meta" },
          h("b", null, assistant ? (message.meta?.harness || t("chat.assistant")) : t("chat.you")),
          h("span", null, relativeTime(message.at))),
        assistant ? createAssistantResponse(message).el
          : h("div", { class: "chat-user-text" }, assistantRichText(message.content || ""))));
    return wrap;
  }

  function renderThread() {
    clear(thread);
    // The empty thread shows the suggestions itself; the side "Try" card
    // only appears once a conversation exists (no duplicate list).
    sideSuggest.hidden = !messages.length;
    if (!messages.length) {
      thread.append(h("div", { class: "chat-empty" },
        h("div", { class: "chat-empty-icon" }, icon("sparkles")),
        h("h2", null, t("chat.empty_title")),
        h("p", null, t("chat.empty_hint")),
        h("div", { class: "chat-prompts" },
          ...["chat.suggest.1", "chat.suggest.2", "chat.suggest.3"].map((key) =>
            h("button", { class: "chat-prompt", onclick: () => { composer.value = t(key); send(); } }, t(key))))));
      return;
    }
    thread.append(...messages.map(messageEl));
    follow();
  }

  function liveAssistant(message) {
    const response = createAssistantResponse(message, { live: true, onPaint: follow });
    const bubble = h("div", { class: "chat-bubble" },
      h("div", { class: "chat-meta" }, h("b", null, harness?.label || t("chat.assistant")),
        h("span", { class: "chat-stream-badge" }, t("chat.streaming"))),
      response.el);
    const el = h("div", { class: "chat-msg assistant streaming" },
      h("div", { class: "chat-avatar" }, icon("sparkles", "sm")), bubble);
    thread.appendChild(el);
    follow(true);
    return { el, response };
  }

  async function send(text = null) {
    const content = String(text ?? composer.value).trim();
    if (!content || busy) return;
    composer.value = "";
    composerUI.resize();
    setBusy(true);
    const user = { role: "user", content, at: new Date().toISOString() };
    thread.querySelector(".chat-empty")?.remove();
    sideSuggest.hidden = false;
    const userEl = messageEl(user);
    thread.appendChild(userEl);
    const pending = { role: "assistant", content: "", meta: {} };
    const live = liveAssistant(pending);
    try {
      await api.chatStream(content, (event) => {
        if (event.type === "done") {
          messages.push(event.user || user, event.assistant);
          harness = event.harness || harness;
          live.response.finish(event.assistant);
          live.el.classList.remove("streaming");
          live.el.querySelector(".chat-stream-badge")?.remove();
          renderHarness();
          if (event.refresh) {
            api.bootstrap().then((boot) => ctx.applyBootstrap(boot)).catch(() => {});
          }
        } else if (event.type === "error") {
          throw new Error(serverErrorText(event, event.message) || "Chat stream failed");
        } else live.response.push(event);
      }, ctx.infoHubChatContext?.());
    } catch (err) {
      live.response.fail(err.message);
      live.el.classList.remove("streaming");
      live.el.querySelector(".chat-stream-badge")?.remove();
      messages.push(user, pending);
    } finally {
      setBusy(false);
    }
  }

  async function clearThread() {
    if (!messages.length) return;
    const { confirmDialog } = await import("../components.js");
    if (!(await confirmDialog(t("chat.clear_confirm"), { title: t("chat.clear_history"), danger: true, confirmLabel: t("chat.clear_history") }))) return;
    try {
      await api.clearChat();
      messages = [];
      renderThread();
      ctx.toast(t("chat.cleared"), "ok");
    } catch (err) { ctx.toast(err.message, "err"); }
  }

  function navigateSettings(context) {
    context.navigate("settings");
  }

  renderHarness();
  renderTools();
  renderSuggestions();
  renderThread();
  setTimeout(() => composer.focus(), 30);

  (async () => {
    try {
      const data = await api.chat();
      messages = data.messages || [];
      harness = data.harness || harness;
      assistantState = data.assistant || null;
      loaded = true;
      renderThread();
      renderHarness();
    } catch (err) {
      clear(thread);
      thread.appendChild(h("div", { class: "chat-error" }, icon("alert", "sm"), err.message));
    }
  })();

  return () => { host.classList.remove("chat-page"); };
}
