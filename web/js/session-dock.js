import { h, clear } from "./dom.js";
import { t, serverErrorText } from "./i18n.js";
import { icon } from "./icons.js";
import { api } from "./api.js";
import { S } from "./state.js";
import { clampInspectorWidth } from "./inspector-width.js";
import { createAssistantComposer } from "./assistant-composer.js";
import { createAssistantResponse, followAssistantThread } from "./assistant-response.js";
import { shellActive, viewHost, showView } from "./workbench.js";

let dock = null;
let messages = [];
let repaint = null;     // paints the mounted dock's thread

// "Clear history" (Assistant view menu): clears this user's chat history in
// this project on the server (DELETE /api/chat) after a confirmation.
export async function clearAssistantHistory(ctx) {
  const { confirmDialog, toast } = await import("./components.js");
  const ok = await confirmDialog(t("chat.clear_confirm"), {
    title: t("chat.clear_history"), danger: true, confirmLabel: t("chat.clear_history"),
  });
  if (!ok) return false;
  try {
    await api.clearChat();
    messages = [];
    loaded = true;
    repaint?.();
    toast(t("chat.cleared"), "ok");
    return true;
  } catch (err) {
    toast(err.message, "err");
    return false;
  }
}
let busy = false;
let loaded = false;

export function applyLayoutPrefs() {
  const root = document.documentElement;
  root.style.setProperty("--rail-w", `${Math.max(220, Math.min(460, Number(S?.railWidth) || 288))}px`);
  root.style.setProperty("--inspector-w", `${clampInspectorWidth(S?.inspectorWidth)}px`);
  root.style.setProperty("--session-h", `${Math.max(140, Math.min(620, Number(S?.sessionHeight) || 260))}px`);
}

// With the workbench shell the dock is the "Assistant" view: showing or
// hiding it opens/closes the container that holds it (see workbench.js).
export function toggleSessionDock(ctx, force = null) {
  const S = ctx.S;
  if (shellActive()) {
    const want = force == null ? !S.sessionDock : Boolean(force);
    showView("assistant", want);
    return want;
  }
  S.sessionDock = force == null ? !S.sessionDock : Boolean(force);
  localStorage.setItem("ark.sessionDock", S.sessionDock ? "1" : "0");
  document.body.classList.toggle("assistant-dock-open", S.sessionDock);
  if (S.sessionDock) mount(ctx);
  else unmount();
  return S.sessionDock;
}

// Place the dock where the current layout wants it: inside the Assistant
// view (kept alive while its container is hidden) or fixed at the bottom of
// the classic layout.
export function mountSessionDock(ctx) {
  const shell = shellActive();
  document.body.classList.toggle("assistant-dock-open", !shell && Boolean(ctx.S.sessionDock));
  const host = shell ? viewHost("assistant") : document.body;
  if (dock?.isConnected && dock.parentElement !== host) {
    if (shell || ctx.S.sessionDock) host.appendChild(dock);
    else unmount();
    return;
  }
  if (ctx.S.sessionDock) mount(ctx);
  else if (!shell) unmount();
}

// Rebuild the open dock after a language switch; keeps the thread and the draft.
export function relocalizeSessionDock(ctx) {
  if (!dock?.isConnected || busy) return;
  const draft = dock.querySelector("textarea")?.value || "";
  unmount();
  mount(ctx);
  const input = dock?.querySelector("textarea");
  if (input && draft) input.value = draft;
}

function unmount() {
  dock?.remove();
  dock = null;
}

function mount(ctx) {
  if (dock?.isConnected) return;
  const S = ctx.S;
  const thread = h("div", { class: "assistant-thread" });
  const follow = followAssistantThread(thread);
  const composerUI = createAssistantComposer({
    placeholder: t("chat.placeholder"), onSubmit: () => submit(),
  });
  const composer = composerUI.input;
  const head = h("div", { class: "assistant-head" },
    h("span", { style: { color: "var(--accent-2)" } }, icon("sparkles", "sm")),
    h("div", null, h("div", { class: "assistant-title" }, t("assistant.title")),
      h("div", { class: "assistant-sub" }, t("session.dock.subtitle"))),
    h("div", { class: "spacer" }),
    h("button", { class: "btn sm ghost", onclick: () => { toggleSessionDock(ctx, false); ctx.navigate("chat"); } }, t("session.dock.full_chat")),
    h("button", { class: "icon-btn", title: t("action.close"), onclick: () => toggleSessionDock(ctx, false) }, icon("x")));
  const resizer = h("div", { class: "assistant-resizer" });
  dock = h("aside", { class: "assistant-dock", "aria-label": t("assistant.title") },
    resizer, head, thread,
    h("div", { class: "assistant-composer" }, composerUI.el));
  ((shellActive() && viewHost("assistant")) || document.body).appendChild(dock);

  const setBusy = (value) => { busy = value; composerUI.setBusy(value); };
  repaint = () => { if (thread.isConnected) paint(); };
  function paint() {
    clear(thread);
    if (!messages.length) {
      thread.appendChild(h("div", { class: "assistant-empty" }, t("session.dock.empty")));
      return;
    }
    for (const message of messages.slice(-80)) {
      if (message.role === "tool") {
        thread.appendChild(h("div", { class: "assistant-msg tool" }, message.text || message.name || t("session.dock.tool")));
      } else {
        const el = h("div", { class: `assistant-msg ${message.role || "assistant"}` });
        const content = message.content || message.text || "";
        if (message.role === "assistant") el.appendChild(createAssistantResponse(message).el);
        else el.textContent = content;
        thread.appendChild(el);
      }
    }
    follow();
  }
  async function load() {
    if (loaded) { paint(); return; }
    loaded = true;
    try { messages = (await api.chat(80)).messages || []; }
    catch (err) { messages = [{ role: "assistant", content: err.message }]; }
    paint();
  }
  async function submit() {
    const text = composer.value.trim();
    if (!text || busy) return;
    composer.value = "";
    composerUI.resize();
    const live = { role: "assistant", content: "", meta: { reasoning: "" } };
    const user = { role: "user", content: text };
    messages.push(user);
    paint();
    const response = createAssistantResponse(live, { live: true, onPaint: follow });
    thread.appendChild(h("div", { class: "assistant-msg assistant" }, response.el));
    follow(true);
    setBusy(true);
    try {
      await api.chatStream(text, (event) => {
        if (event.type === "done") {
          Object.assign(user, event.user || {});
          Object.assign(live, event.assistant || {});
          response.finish(live);
          if (event.refresh) api.bootstrap().then((boot) => ctx.applyBootstrap(boot)).catch(() => {});
        } else if (event.type === "error") {
          throw new Error(serverErrorText(event, event.message) || t("session.dock.stream_failed"));
        } else response.push(event);
      }, ctx.infoHubChatContext?.());
    } catch (err) {
      response.fail(err.message);
    } finally {
      messages.push(live);
      setBusy(false);
    }
  }
  resizer.addEventListener("pointerdown", (e) => {
    e.preventDefault();
    const startY = e.clientY;
    const startH = Number(S.sessionHeight) || 260;
    const move = (ev) => {
      const height = Math.max(140, Math.min(620, startH + startY - ev.clientY));
      S.sessionHeight = Math.round(height);
      document.documentElement.style.setProperty("--session-h", `${S.sessionHeight}px`);
    };
    const up = () => {
      localStorage.setItem("ark.sessionHeight", String(S.sessionHeight));
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  });
  load();
}
