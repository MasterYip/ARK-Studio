// The Sessions sidebar view: an agent session as a chat beside the work
// (VS Code's Chat view).  It docks like any workbench view and stays open
// while the main area moves between Map, Research, Board and node details.
//
// Data comes from the shared session stream (session-stream.js): the
// Sessions page and this view make one request per tick between them, and
// context attached here is the same list the page's composer shows.  The
// thread is a keyed list painted at most every PAINT_INTERVAL_MS and only
// while the view is visible; nothing outside this element is re-rendered.

import { h, clear, relativeTime, unitReference, copyText } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { api } from "./api.js";
import { assistantRichText } from "./assistant-response.js";
import { isViewDisplayed, WORKBENCH_CHANGED } from "./workbench.js";
import {
  sessionStream, contextItems, contextPayload, attachSessionContext, removeSessionContext, clearSessionContext,
  sendSessionMessage, stopAgentSession, startAgentSession, sendQueuedNow, fullToolItem, sessionImages, toolImage,
  renameAgentSession,
} from "./session-stream.js";
import { transcriptKey } from "./session-stream-model.js";
import { openContextMenu } from "./context-menu.js";
import { readImages, releaseImages, imageTray, bindImageInput, imagePicker, turnImages } from "./session-images.js";
import {
  threadBlocks, blockSignature, liveActivity, isActiveSession, shownStatus, toolGroupSummary, modelOptions,
  composerMode as composerModeEn, sessionTitle as sessionTitleEn,
} from "./session-model.js";
import { createActivityChips } from "./session-activity.js";
import { blockKeyFor } from "./session-activity-model.js";
import { fmtDuration, renderToolGroup as renderToolGroupRow, inlineRename } from "./session-thread-parts.js";
import {
  loadSidebarPrefs, saveSidebarPrefs, rememberSessionChoice, pickPreset, recentSessions, sidebarSelection,
  contextCandidates, pendingUnitContext, windowBlocks, composerAction, newSessionTitle, createPaintScheduler,
  BLOCK_WINDOW, NEW_SESSION, drawLimit, composerKeyAction, composerHintKey, sendNowMode, imageContext, turnRunning, stopLabelKey,
  loadAgentTabs, saveAgentTabs, openAgentTab as openAgentTabIn, closeAgentTab, activateAgentTab,
} from "./session-sidebar-model.js";

export const SESSIONS_VIEW = "sessions";
const UNIT_REF = /\b([A-Z]{1,4}-\d{3,5})\b/g;
const withT = { t };
const sessionTitle = (session) => sessionTitleEn(session, withT);

function statusText(status) {
  const key = `tasks.status.${status}`;
  return t(key) === key ? status : t(key);
}

// Plain text with T-0042 style references as links (they open in the main area).
function linkedText(text) {
  const value = String(text || "");
  const out = [];
  let last = 0;
  for (const match of value.matchAll(UNIT_REF)) {
    out.push(value.slice(last, match.index), unitReference(match[1]));
    last = match.index + match[0].length;
  }
  out.push(value.slice(last));
  return out;
}

export function createSessionSidebar(ctx) {
  let prefs = loadSidebarPrefs();
  let sessions = [];
  let listLoaded = false;
  let selectedId = prefs.sessionId;
  let transcript = null;
  let transcriptFor = "";
  let transcriptError = "";
  let limit = BLOCK_WINDOW;
  let drawn = 0;
  let stickToBottom = true;
  let presets = null;
  let presetsLoading = false;
  let busy = false;
  let pickerOpen = false;
  let newContext = [];
  let liveTimer = null;
  let headSignature = "";
  let composerSignature = "";
  const drafts = new Map();
  const blockNodes = new Map();
  const openTools = new Set();
  const choice = { platform: prefs.platform, model: prefs.model };

  // ------------------------------------------------------------------ DOM
  const switcher = h("select", {
    class: "sw-select ssb-switch", "aria-label": t("session.sidebar.switch"),
    onchange: (event) => select(event.target.value),
  });
  const statusLine = h("div", { class: "ssb-status" });
  // Sub-agent tabs of the open session, as a dropdown (the view is narrow).
  const tabSelect = h("select", {
    class: "sw-select ssb-tabs", "aria-label": t("session.tabs.label"), hidden: true,
    onchange: (event) => setTab(event.target.value),
  });
  const tabClose = h("button", {
    class: "icon-btn ssb-tab-x", type: "button", hidden: true, title: t("session.tabs.close_current"),
    "aria-label": t("session.tabs.close_current"),
    onclick: () => { saveAgentTabs(closeAgentTab(loadAgentTabs(), selectedId, activeTab())); tabChanged(); },
  }, icon("x", "sm"));
  const tabRow = h("div", { class: "ssb-tabrow", hidden: true }, tabSelect, tabClose);
  const head = h("div", { class: "ssb-head" }, switcher, statusLine, tabRow);
  const column = h("div", { class: "ssb-column" });
  const earlier = h("button", { class: "ssb-earlier", type: "button", hidden: true, onclick: showEarlier });
  const liveRow = h("div", { class: "ssb-live", role: "status", hidden: true });
  const thread = h("div", { class: "ssb-thread", role: "log", "aria-live": "polite" }, earlier, column, liveRow);
  const chips = h("div", { class: "ssb-chips" });
  const queueHost = h("div", { class: "ssb-queue" });
  const tray = h("div", { class: "ssb-tray" });
  const hint = h("div", { class: "sw-composer-hint", "data-composer-hint": "" });
  const picker = h("div", { class: "ssb-picker", hidden: true });
  const input = h("textarea", {
    class: "ssb-input", rows: "2", "aria-label": t("tasks.thread.composer_label"), title: t("tasks.thread.composer_keys"),
  });
  const sendButton = h("button", { class: "sw-send ssb-send", type: "button" });
  const options = h("div", { class: "ssb-options" });
  const foot = h("div", { class: "ssb-foot" }, options, h("span", { class: "spacer" }), sendButton);
  const imageInput = imagePicker((files) => addImages(files));
  const composer = h("div", { class: "ssb-composer" }, imageInput.input, picker, queueHost, chips, tray, input, hint, foot);
  bindImageInput(input, (files) => addImages(files));
  const activityChips = createActivityChips({
    onOpenItem: (itemId) => revealItem(itemId),
    onOpenSession: (id) => select(id),
    onOpenAgent: (entry) => openAgent(entry.itemId, entry.label),
  });
  // On Execute › Runs the view shows the session list (the page's own list,
  // moved here); elsewhere the conversation.
  const listSlot = h("div", { class: "ssb-list", hidden: true, dataset: { sessionList: "" } });
  const el = h("div", { class: "ssb", dataset: { sessionSidebar: "" } }, listSlot, head, thread, activityChips.el, composer);
  let listOwner = null;   // the element shown in the list slot
  const onRunsPage = () => ctx.S.tab === "tasks" && ctx.S.taskView === "sessions";
  function syncListMode() {
    const list = Boolean(listOwner?.isConnected && onRunsPage());
    el.classList.toggle("list-mode", list);
    listSlot.hidden = !list;
    for (const part of [head, thread, activityChips.el, composer]) part.classList.toggle("ssb-off", list);
  }
  window.addEventListener("ark:selection", syncListMode);
  window.addEventListener("ark:workbench-changed", syncListMode);

  const visible = () => el.isConnected && isViewDisplayed(SESSIONS_VIEW) && document.visibilityState !== "hidden";
  const scheduler = createPaintScheduler({
    paint: () => paintThread(),
    visible,
    frame: (fn) => requestAnimationFrame(fn),
  });

  function current() {
    return sessions.find((session) => session.id === selectedId) || null;
  }

  // ------------------------------------------------------------ the stream
  sessionStream.subscribeSessions((data) => {
    sessions = data.sessions || [];
    syncFollow();     // the view may have been hidden by a route change
    const first = !listLoaded;
    listLoaded = true;
    if (selectedId && !sessions.some((session) => session.id === selectedId)) {
      // The open session is gone (deleted, or another project): start a new one.
      selected(sidebarSelection(sessions, selectedId));
      return;
    }
    if (mainData) activityChips.update(mainData.activity || mainData.items || [], { session: current(), sessions });
    paintHead();
    paintComposer();
    if (first || (current() && !isActiveSession(current()))) scheduler.request();   // first paint; the live row ends
  }, { active: visible });

  sessionStream.subscribeContext((id) => {
    if (id === selectedId) paintComposer(true);
  });

  // Follow the open session (and its open sub-agent tab) while the view is
  // shown.  The main conversation feeds the activity chips.
  let mainData = null;
  const follows = new Map();   // key -> unsubscribe
  function activeTab() {
    return (selectedId && loadAgentTabs()[selectedId]?.active) || "";
  }
  function viewKey() {
    return selectedId ? transcriptKey(selectedId, activeTab()) : "";
  }
  function syncFollow() {
    const want = new Set(visible() ? [selectedId, viewKey()].filter(Boolean) : []);
    for (const [key, off] of follows) if (!want.has(key)) { off(); follows.delete(key); }
    for (const key of want) {
      if (follows.has(key)) continue;
      follows.set(key, sessionStream.subscribeTranscript(key, (data) => onTranscript(key, data)));
      if (!sessionStream.cachedTranscript(key)) {
        sessionStream.transcript(key).catch((err) => {
          if (key !== viewKey() || transcriptFor === selectedId) return;
          transcriptError = err.message;
          scheduler.request();
        });
      }
    }
  }

  function onTranscript(key, data) {
    if (!data) return;
    if (key === selectedId) {
      mainData = data;
      activityChips.update(data.activity || data.items || [], { session: current(), sessions });
      paintTabs();
    }
    if (key !== viewKey()) return;
    transcript = data;
    transcriptFor = selectedId;
    transcriptError = "";
    scheduler.request();
  }

  // ------------------------------------------------------------------ tabs
  function openAgent(itemId, label = "") {
    if (!selectedId || !itemId) return;
    saveAgentTabs(openAgentTabIn(loadAgentTabs(), selectedId, { id: itemId, label }));
    tabChanged();
  }

  function setTab(id) {
    saveAgentTabs(activateAgentTab(loadAgentTabs(), selectedId, id));
    tabChanged();
  }

  function tabChanged() {
    transcript = sessionStream.cachedTranscript(viewKey());
    transcriptFor = transcript ? selectedId : "";
    limit = BLOCK_WINDOW;
    stickToBottom = true;
    openTools.clear();
    blockNodes.clear();
    clear(column);
    paintTabs();
    composer.hidden = Boolean(activeTab());
    syncFollow();
    scheduler.request();
  }

  function paintTabs() {
    const entry = selectedId ? loadAgentTabs()[selectedId] : null;
    tabRow.hidden = !entry?.tabs?.length;
    tabClose.hidden = !activeTab();
    clear(tabSelect);
    if (tabRow.hidden) return;
    const statusOf = (id) => activityChips.activity.entries.find((item) => item.itemId === id)?.status || "";
    const mark = (status) => (status === "running" ? "● " : status === "failed" ? "✕ " : status ? "✓ " : "");
    tabSelect.append(h("option", { value: "", selected: !activeTab() }, t("session.tabs.main")),
      ...entry.tabs.map((tab) => h("option", { value: tab.id, selected: tab.id === activeTab() },
        `${mark(statusOf(tab.id))}${tab.label || t("session.activity.kind_subagent")}`)));
    tabSelect.hidden = false;
  }

  window.addEventListener(WORKBENCH_CHANGED, () => { syncFollow(); scheduler.flush(); });
  document.addEventListener("visibilitychange", () => { syncFollow(); scheduler.flush(); });
  window.addEventListener("ark:selection", () => paintComposer());

  // -------------------------------------------------------------- selection
  function select(id) {
    if ((id || NEW_SESSION) === selectedId) return;
    selected(id || NEW_SESSION);
    if (!selectedId) requestAnimationFrame(() => input.focus());
  }

  function selected(id) {
    drafts.set(selectedId, input.value);
    selectedId = id;
    prefs = saveSidebarPrefs({ sessionId: id });
    mainData = selectedId ? sessionStream.cachedTranscript(selectedId) : null;
    transcript = selectedId ? sessionStream.cachedTranscript(viewKey()) : null;
    transcriptFor = transcript ? selectedId : "";
    composer.hidden = Boolean(activeTab());
    paintTabs();
    transcriptError = "";
    limit = BLOCK_WINDOW;
    stickToBottom = true;
    pickerOpen = false;
    openTools.clear();
    blockNodes.clear();
    clear(column);
    input.value = drafts.get(selectedId) || "";
    autoGrow();
    headSignature = "";
    paintHead();
    paintComposer(true);
    syncFollow();
    scheduler.request();
  }

  function newSession() {
    select(NEW_SESSION);
    requestAnimationFrame(() => input.focus());
  }

  function openPage() {
    if (selectedId) ctx.S.selectedSessionId = selectedId;
    ctx.navigateWorkspace?.("execute", "sessions");
  }

  // ------------------------------------------------------------------ head
  function paintHead() {
    const list = recentSessions(sessions, undefined, selectedId);
    const signature = [selectedId, listLoaded, ...list.map((session) => `${session.id}:${shownStatus(session)}:${session.title || ""}`)].join("|");
    if (signature !== headSignature && document.activeElement !== switcher) {
      headSignature = signature;
      clear(switcher);
      switcher.appendChild(h("option", { value: NEW_SESSION, selected: !selectedId }, t("session.sidebar.new_option")));
      for (const session of list) {
        const mark = shownStatus(session) === "running" ? "● " : isActiveSession(session) ? "○ " : "";
        switcher.appendChild(h("option", { value: session.id, selected: session.id === selectedId },
          `${mark}${sessionTitle(session)}`));
      }
      switcher.value = selectedId;
    }
    paintStatus();
  }

  let statusSignature = "";
  function paintStatus() {
    const session = current();
    const signature = session ? [session.id, session.status, session.platform, session.model, session.task_ref,
      relativeTime(session.last_activity_at)].join("|") : `new|${listLoaded}`;
    if (signature === statusSignature) return;
    statusSignature = signature;
    clear(statusLine);
    if (!session) {
      statusLine.append(h("span", { class: "ssb-muted" }, t("session.sidebar.new_hint")));
      return;
    }
    const active = isActiveSession(session);
    statusLine.append(...[
      h("span", { class: `sw-status ${shownStatus(session)}`.trim() }, h("i"), statusText(shownStatus(session))),
      h("span", { class: "ssb-meta", title: [session.platform_label || session.platform, session.model].filter(Boolean).join(" · ") },
        [session.platform_label || session.platform, session.model].filter(Boolean).join(" · ")),
      session.task_ref ? unitReference(session.task_ref) : null,
      h("span", { class: "spacer" }),
      session.last_activity_at ? h("span", { class: "ssb-muted" }, relativeTime(session.last_activity_at)) : null,
      active ? h("button", {
        class: "btn sm danger ssb-stop", type: "button", "data-ssb-stop": "", onclick: stop,
        title: t(`${stopLabelKey(session)}_hint`),
      }, icon("stop", "sm"), t(stopLabelKey(session))) : null,
      h("button", {
        class: "icon-btn ssb-more", type: "button", title: t("session.menu.more"), "aria-label": t("session.menu.more"),
        "aria-haspopup": "menu", "data-session-menu": "",
        onclick: (event) => {
          const rect = event.currentTarget.getBoundingClientRect();
          openMenu(rect.left, rect.bottom + 4, event.currentTarget);
        },
      }, icon("more", "sm"))].filter(Boolean));
  }

  // The session's actions: "⋯", right-click on the header, Shift+F10.
  function openMenu(x, y, returnFocus = null) {
    const session = current();
    if (!session) return;
    const copy = async (text) => {
      const ok = text ? await copyText(text) : false;
      ctx.toast?.(ok ? t("action.copied") : t(text ? "action.copy" : "session.toast.no_replies"), ok ? "ok" : "err");
    };
    const replies = () => ((mainData || transcript)?.items || []).filter((item) => item.type === "message" && item.role !== "user")
      .map((item) => item.text || "").filter(Boolean).join("\n\n");
    openContextMenu({ x, y, returnFocus, items: [
      { label: t("session.menu.rename"), icon: "edit", onClick: () => rename(session) },
      { label: t("session.sidebar.open_page"), icon: "external", onClick: openPage },
      session.task_ref ? { label: t("tasks.open_task"), icon: "target", onClick: () => ctx.openUnit?.(session.task_ref) } : null,
      { label: t("session.copy_id"), icon: "copy", onClick: () => copy(session.id) },
      { label: t("session.copy_replies"), icon: "copy", onClick: () => copy(replies()) },
      isActiveSession(session) ? { label: t("tasks.stop"), icon: "x", danger: true, onClick: stop } : null,
    ].filter(Boolean) });
  }
  // Rename in place of the switcher.
  function rename(session) {
    const holder = h("div", { class: "ssb-rename" });
    switcher.replaceWith(holder);
    holder.appendChild(switcher);
    const restore = new MutationObserver(() => {
      if (holder.contains(switcher)) {
        restore.disconnect();
        holder.replaceWith(switcher);
      }
    });
    inlineRename(holder, session.title || "", async (title) => {
      try {
        await renameAgentSession(session.id, title);
        headSignature = "";
        paintHead();
      } catch (err) { ctx.toast?.(err.message, "err"); }
    }, { label: t("session.menu.rename") });
    restore.observe(holder, { childList: true });
  }

  switcher.addEventListener("contextmenu", (event) => {
    if (!current()) return;
    event.preventDefault();
    openMenu(event.clientX, event.clientY, switcher);
  });
  switcher.addEventListener("keydown", (event) => {
    if (event.key === "F2" && current()) { event.preventDefault(); rename(current()); }
  });

  head.addEventListener("contextmenu", (event) => {
    if (event.target.closest("select, input")) return;
    event.preventDefault();
    openMenu(event.clientX, event.clientY, head);
  });
  head.addEventListener("keydown", (event) => {
    if (event.key === "ContextMenu" || (event.shiftKey && event.key === "F10")) {
      event.preventDefault();
      const rect = head.getBoundingClientRect();
      openMenu(rect.left + 16, rect.bottom, head);
    }
  });

  // ---------------------------------------------------------------- thread
  function nearBottom() {
    return thread.scrollHeight - thread.scrollTop - thread.clientHeight < 60;
  }

  thread.addEventListener("scroll", () => { stickToBottom = nearBottom(); }, { passive: true });

  function showEarlier() {
    const before = thread.scrollHeight;
    const blocks = threadBlocks(transcript?.items || []);
    if (blocks.length <= limit && transcript?.offset > 0) {
      sessionStream.setTail(viewKey(), (transcript.items?.length || 0) + 400).catch(() => {});
    }
    limit += BLOCK_WINDOW;
    stickToBottom = false;
    paintThread();
    thread.scrollTop += thread.scrollHeight - before;
  }

  function stateNode(node) {
    blockNodes.clear();
    clear(column);
    earlier.hidden = true;
    liveRow.hidden = true;
    stopLive();
    column.appendChild(node);
  }

  function paintThread() {
    const session = current();
    if (!session) {
      activityChips.update([], { session: null });
      mainData = null;
      stateNode(h("div", { class: "ssb-empty" }, icon("message", "lg"),
        h("b", null, t("session.sidebar.empty_title")), h("div", null, t("session.sidebar.empty_hint"))));
      return;
    }
    if (transcriptFor !== selectedId || !transcript) {
      stateNode(transcriptError
        ? h("div", { class: "ssb-note err", role: "alert" }, icon("alert", "sm"), h("span", null, transcriptError))
        : h("div", { class: "ssb-empty" }, h("span", { class: "spinner" }), t("tasks.thread.loading")));
      return;
    }
    const items = transcript.items || [];
    const all = threadBlocks(items);
    if (!blockNodes.size) drawn = 0;
    drawn = drawLimit(drawn, limit);
    const { blocks, hidden: hiddenBlocks } = windowBlocks(all, drawn);
    if (Math.min(limit, all.length) > drawn) {
      const height = thread.scrollHeight;
      requestAnimationFrame(() => {
        const top = thread.scrollTop;
        const bottom = stickToBottom;
        paintThread();
        if (!bottom) thread.scrollTop = top + (thread.scrollHeight - height);
      });
    }
    const hidden = (hiddenBlocks ? Math.max(0, all.length - limit) : 0) + Number(transcript.offset || 0);
    earlier.hidden = !hidden;
    earlier.textContent = hidden ? t("session.sidebar.earlier", { n: hidden }) : "";
    if (!all.length) {
      stateNode(h("div", { class: "ssb-empty" }, h("div", null, t("tasks.thread.empty"))));
      paintLive(session, liveActivity(items, session));
      return;
    }
    if (column.firstChild && !blockNodes.size) clear(column);   // a loading or empty state
    const follow = stickToBottom;
    const desired = [];
    const seen = new Set();
    for (const block of blocks) {
      seen.add(block.key);
      const signature = `${blockSignature(block)}#${block.kind === "tools" && openTools.has(block.key) ? "open" : ""}`;
      let entry = blockNodes.get(block.key);
      if (!entry || entry.signature !== signature) {
        const node = renderBlock(block);
        if (entry?.node && entry.node.parentNode === column) entry.node.replaceWith(node);
        entry = { signature, node };
        blockNodes.set(block.key, entry);
      }
      desired.push(entry.node);
    }
    for (const key of [...blockNodes.keys()]) if (!seen.has(key)) blockNodes.delete(key);
    let ref = column.firstChild;
    for (const node of desired) {
      if (node === ref) ref = ref.nextSibling;
      else column.insertBefore(node, ref);
    }
    while (ref) {
      const next = ref.nextSibling;
      column.removeChild(ref);
      ref = next;
    }
    paintLive(session, activeTab() ? null : liveActivity(items, session));
    if (follow) thread.scrollTop = thread.scrollHeight;
  }

  // A sub-agent or background task chosen in the activity list.
  function revealItem(itemId) {
    if (activeTab()) setTab("");
    const all = threadBlocks(transcript?.items || []);
    const key = blockKeyFor(all, itemId);
    if (!key) return;
    const index = all.findIndex((block) => block.key === key);
    if (index >= 0 && all.length - index > limit) limit = all.length - index;
    if (key.startsWith("tools-")) openTools.add(key);
    blockNodes.delete(key);
    stickToBottom = false;
    paintThread();
    const node = blockNodes.get(key)?.node;
    if (!node) return;
    node.scrollIntoView({ block: "center", behavior: "smooth" });
    node.classList.add("sa-flash");
    setTimeout(() => node.classList.remove("sa-flash"), 1600);
  }

  function artifactMarkdown(text) {
    const id = selectedId;
    return String(text || "").replace(/!\[([^\]]*)\]\(([^)\s]+)\)/g, (match, alt, src) => (
      /^(https?:|data:|\/)/i.test(src) ? match : `![${alt}](${api.sessionArtifactUrl(id, src, true)})`));
  }

  function copyButton(getText) {
    return h("button", {
      class: "sw-act-btn", type: "button", title: t("tasks.thread.copy"), "aria-label": t("tasks.thread.copy"),
      onclick: async () => {
        const ok = await copyText(getText());
        ctx.toast?.(ok ? t("action.copied") : t("action.copy"), ok ? "ok" : "err");
      },
    }, icon("copy", "sm"));
  }

  function renderBlock(block) {
    const item = block.item || {};
    if (block.kind === "separator") {
      return h("div", { class: "ssb-sep" }, block.at ? relativeTime(block.at) : "");
    }
    if (block.kind === "user") {
      return h("div", { class: "ssb-msg ssb-user", dataset: { itemId: item.id } },
        turnImages(item.images, (path) => api.sessionArtifactUrl(selectedId, path, true),
          (src) => window.open(src, "_blank", "noopener")),
        h("div", { class: "sw-bubble" }, ...linkedText(item.text)));
    }
    if (block.kind === "assistant") {
      return h("div", { class: `ssb-msg ssb-assistant ${item.streaming ? "streaming" : ""}`.trim(), dataset: { itemId: item.id } },
        h("div", { class: "sw-prose" }, assistantRichText(artifactMarkdown(item.text))),
        h("div", { class: "ssb-msg-actions" }, copyButton(() => item.text || "")));
    }
    if (block.kind === "tools") return renderTools(block);
    if (block.kind === "reasoning") {
      return h("details", { class: `sw-act sw-thought ssb-act ${item.streaming ? "running" : ""}`.trim() },
        h("summary", { class: "sw-act-head" },
          h("span", { class: "sw-act-icon" }, icon("sparkles", "sm")),
          h("span", { class: "sw-act-label" }, item.streaming ? t("tasks.thread.thinking") : t("tasks.thread.thought")),
          h("span", { class: "sw-act-chev" }, icon("chevronRight", "sm"))),
        h("div", { class: "sw-act-body sw-thought-body" }, assistantRichText(item.text || "")));
    }
    if (block.kind === "error") {
      return h("div", { class: "ssb-note err", role: "alert" }, icon("alert", "sm"), h("span", null, ...linkedText(item.text)));
    }
    if (block.kind === "permission") {
      return h("div", { class: "ssb-note warn" }, icon("shield", "sm"),
        h("span", null, t("session.sidebar.approval")),
        h("button", { class: "link", type: "button", onclick: openPage }, t("session.sidebar.open_page")));
    }
    return h("div", { class: `ssb-note ${item.level || "info"}`.trim() }, icon("info", "sm"), h("span", null, ...linkedText(item.text)));
  }

  // Tool and terminal output: one collapsed row per run of tool calls (the
  // Sessions page draws the same rows).
  function renderTools(block) {
    return renderToolGroupRow(block, {
      openSet: openTools,
      summary: toolGroupSummary(block.items),
      extraClass: "ssb-act",
      onToggle: (open) => {
        const entry = blockNodes.get(block.key);
        if (entry) entry.signature = `${blockSignature(block)}#${open ? "open" : ""}`;
      },
      onFull: (itemId) => fullToolItem(selectedId, itemId),
      onImage: (itemId, n) => toolImage(selectedId, itemId, n),
      onOpenAgent: activeTab() ? null : (item) => openAgent(item.id, item.input?.description || item.input?.subagent_type || ""),
    });
  }

  function stopLive() {
    if (liveTimer) clearInterval(liveTimer);
    liveTimer = null;
  }

  function paintLive(session, live) {
    if (!live) {
      liveRow.hidden = true;
      stopLive();
      return;
    }
    liveRow.hidden = false;
    liveRow.dataset.phase = live.phase;
    const phase = live.phase === "running" || live.phase === "editing" ? "working" : live.phase;
    const tick = () => {
      if (!el.isConnected || liveRow.hidden) { stopLive(); return; }
      clear(liveRow);
      liveRow.append(h("span", { class: "sw-live-dot" }), h("span", { class: "sw-live-label" }, t(`tasks.thread.phase_${phase}`)),
        h("span", { class: "ssb-muted" }, live.since ? fmtDuration(live.since) : ""));
    };
    tick();
    if (!liveTimer) liveTimer = setInterval(() => { if (visible()) tick(); }, 1000);
  }

  // -------------------------------------------------------------- composer
  function autoGrow() {
    input.style.height = "auto";
    input.style.height = `${Math.min(input.scrollHeight || 0, 220)}px`;
  }

  function attached() {
    const session = current();
    return session ? contextItems(session.id) : newContext;
  }

  function candidates() {
    const S = ctx.S;
    const graphSelections = (ctx.graphs || []).map((graph) => [...(graph?.selectedNodes || [])]);
    return contextCandidates({
      detail: S.detail, drawerOpen: S.drawerOpen, selectedId: S.selectedId, units: S.units,
      editorKey: S.editorKey, tab: S.tab, graphSelections,
    });
  }

  async function attach(candidate) {
    const session = current();
    if (!session) {
      newContext = [...newContext.filter((item) => item.uid !== candidate.uid), pendingUnitContext(candidate)];
      paintComposer(true);
      return;
    }
    try {
      await attachSessionContext(session.id, { kind: "unit", uid: candidate.uid });
    } catch (err) {
      ctx.toast?.(err.message, "err");
    }
  }

  function detach(item) {
    const session = current();
    if (!session) {
      newContext = newContext.filter((entry) => entry.id !== item.id);
      paintComposer(true);
      return;
    }
    removeSessionContext(session.id, item.id);
  }

  function paintPicker() {
    clear(picker);
    picker.hidden = !pickerOpen;
    if (!pickerOpen) return;
    const taken = new Set(attached().map((item) => item.uid).filter(Boolean));
    const search = h("input", {
      class: "input ssb-picker-search", type: "search", placeholder: t("session.context.search_units_placeholder"),
      "aria-label": t("session.context.search_units"),
    });
    const list = h("div", { class: "ssb-picker-list" });
    const row = (unit, hint = "") => h("button", {
      class: "ssb-picker-row", type: "button", disabled: taken.has(unit.uid), title: `${unit.uid} · ${unit.title || ""}`,
      onclick: () => { pickerOpen = false; attach(unit); paintComposer(true); },
    }, icon(unit.type === "task" ? "tasks" : "target", "sm"), h("b", null, unit.uid),
      h("span", { class: "ssb-picker-title" }, unit.title || ""), hint ? h("span", { class: "ssb-muted" }, hint) : null);
    const fill = () => {
      clear(list);
      const query = search.value.trim().toLowerCase();
      const rows = query
        ? (ctx.S.units || []).filter((unit) => !unit.archived && `${unit.uid} ${unit.title || ""}`.toLowerCase().includes(query)).slice(0, 12)
        : candidates();
      if (!rows.length) list.appendChild(h("div", { class: "ssb-muted ssb-picker-empty" }, t(query ? "session.context.no_units" : "session.sidebar.no_selection")));
      for (const unit of rows) {
        list.appendChild(row(unit, query ? "" : t(unit.source === "open" ? "session.sidebar.ctx_open" : "session.sidebar.ctx_selected")));
      }
    };
    search.addEventListener("input", fill);
    search.addEventListener("keydown", (event) => {
      if (event.key === "Escape") { pickerOpen = false; paintComposer(true); input.focus(); }
    });
    fill();
    picker.append(h("div", { class: "ssb-picker-head" }, h("b", null, t("session.context.add")),
      h("span", { class: "spacer" }),
      h("button", { class: "icon-btn", type: "button", title: t("action.close"), onclick: () => { pickerOpen = false; paintComposer(true); } }, icon("x", "sm"))),
    search, list);
    requestAnimationFrame(() => search.focus());
  }

  function paintChips() {
    clear(chips);
    const items = attached();
    const taken = new Set(items.map((item) => item.uid).filter(Boolean));
    chips.appendChild(h("button", {
      class: `sw-chip ssb-add ${pickerOpen ? "on" : ""}`.trim(), type: "button", "data-ssb-add-context": "",
      title: t("session.context.add"), "aria-label": t("session.context.add"),
      onclick: () => { pickerOpen = !pickerOpen; paintComposer(true); },
    }, icon("plus", "sm")));
    for (const item of items) {
      chips.appendChild(h("span", { class: "sw-chip ssb-ctx", title: item.label }, icon(item.kind === "unit" ? "target" : "file", "sm"),
        h("span", { class: "ssb-ctx-label" }, item.label),
        h("button", {
          class: "ssb-ctx-x", type: "button", title: t("action.remove"), "aria-label": t("action.remove"), onclick: () => detach(item),
        }, icon("x", "sm"))));
    }
    // One click: the node the user is looking at (VS Code's "current file").
    const suggestion = candidates().find((candidate) => !taken.has(candidate.uid));
    if (suggestion) {
      chips.appendChild(h("button", {
        class: "sw-chip ssb-suggest", type: "button", "data-ssb-suggest": suggestion.uid,
        title: t("session.sidebar.attach_hint", { uid: suggestion.uid }),
        onclick: () => attach(suggestion),
      }, icon("plus", "sm"), h("span", { class: "ssb-ctx-label" }, suggestion.title ? `${suggestion.uid} · ${suggestion.title}` : suggestion.uid)));
    }
  }

  async function loadPresets() {
    if (presets || presetsLoading) return;
    presetsLoading = true;
    try {
      presets = ctx.S.sessionPresets?.length ? ctx.S.sessionPresets : (await api.sessionPresets()).presets || [];
    } catch {
      presets = [];
    } finally {
      presetsLoading = false;
      paintComposer(true);
    }
  }

  function paintOptions() {
    clear(options);
    const session = current();
    if (session) {
      options.append(h("span", { class: "ssb-muted ssb-mode" },
        t(isActiveSession(session) ? "session.sidebar.mode_steer" : "session.sidebar.mode_continue")));
      return;
    }
    if (!presets) {
      loadPresets();
      options.append(h("span", { class: "ssb-muted" }, t("session.composer.loading")));
      return;
    }
    const preset = pickPreset(presets, choice.platform);
    if (!preset) {
      options.append(h("span", { class: "ssb-muted" }, t("session.sidebar.no_provider")),
        h("button", { class: "link", type: "button", onclick: () => ctx.openSessionLaunch?.({}) }, t("session.sidebar.more")));
      return;
    }
    choice.platform = preset.id;
    const models = modelOptions(preset, preset.provider_capabilities || {});
    if (choice.model && !models.some((item) => item.id === choice.model)) models.unshift({ id: choice.model, label: choice.model });
    const provider = h("select", {
      class: "sw-select ssb-provider", "aria-label": t("session.provider"),
      onchange: (event) => { choice.platform = event.target.value; choice.model = ""; rememberSessionChoice(choice); paintComposer(true); },
    }, presets.filter((item) => item.available).map((item) => h("option", { value: item.id, selected: item.id === preset.id },
      item.label || item.id)));
    const model = h("select", {
      class: "sw-select ssb-model", "aria-label": t("session.composer.sel_model"),
      onchange: (event) => { choice.model = event.target.value; rememberSessionChoice(choice); },
    }, h("option", { value: "", selected: !choice.model }, t("tasks.thread.default_model")),
    models.map((item) => h("option", { value: item.id, selected: item.id === choice.model }, item.label || item.id)));
    options.append(provider, model, h("button", {
      class: "icon-btn ssb-more", type: "button", title: t("session.sidebar.more"), "aria-label": t("session.sidebar.more"),
      onclick: () => ctx.openSessionLaunch?.({ prompt: input.value.trim(), platform: choice.platform }),
    }, icon("settings", "sm")));
  }

  function paintSend() {
    const action = composerAction(current(), { hasContent: Boolean(input.value.trim() || attached().length || images().length) });
    const stop = action === "stop";
    const label = stop ? t("tasks.stop") : action === "steer" ? t("tasks.thread.send_steer") : t("tasks.thread.send");
    sendButton.classList.toggle("stop", stop);
    sendButton.classList.toggle("empty", action === "none");
    sendButton.disabled = busy;
    sendButton.title = label;
    sendButton.setAttribute("aria-label", label);
    clear(sendButton);
    sendButton.appendChild(busy ? h("span", { class: "spinner" }) : icon(stop ? "stop" : "arrowUp", "sm"));
  }

  function images() {
    return sessionImages.get(selectedId || "") || [];
  }

  async function addImages(files) {
    const { images: added, errors } = await readImages(files, images().length);
    for (const message of errors) ctx.toast?.(message, "err");
    if (!added.length) return;
    sessionImages.set(selectedId || "", [...images(), ...added]);
    paintComposer(true);
  }

  function paintTray() {
    clear(tray);
    const node = imageTray(images(), (image) => {
      releaseImages([image]);
      sessionImages.set(selectedId || "", images().filter((entry) => entry.id !== image.id));
      paintComposer(true);
    });
    if (node) tray.appendChild(node);
  }

  // Queued messages of a running turn, each with "Send now".
  function paintQueue() {
    clear(queueHost);
    const session = current();
    const queued = (session?.steer_queue || []).filter((item) => item.status === "queued");
    if (!queued.length) return;
    const interrupt = sendNowMode(session) === "interrupt" && isActiveSession(session);
    queueHost.append(h("div", { class: "ssb-queue-head" }, icon("send", "sm"), t("session.steer.queue_waiting", { n: queued.length })),
      ...queued.map((item) => h("div", { class: "ssb-queue-item" },
        h("span", { class: "ssb-queue-text", title: item.text }, item.text),
        h("button", {
          class: "btn sm ghost", type: "button", "data-steer-now": item.id,
          title: t(interrupt ? "session.steer.send_now_interrupt" : "session.steer.send_now_hint"),
          onclick: async (event) => {
            event.currentTarget.disabled = true;
            try {
              const data = await sendQueuedNow(session.id, item.id);
              ctx.toast?.(t(data.mode === "interrupt" ? "session.toast.sent_now_interrupt" : "session.toast.sent_now"), "ok");
              stickToBottom = true;
            } catch (err) { ctx.toast?.(err.message, "err"); }
          },
        }, t(interrupt ? "session.steer.send_now_interrupt_short" : "session.steer.send_now")))));
  }

  function paintComposer(force = false) {
    const session = current();
    const signature = [
      selectedId, session?.status || "", busy, pickerOpen, Boolean(presets), choice.platform,
      images().map((image) => image.id).join(","),
      (session?.steer_queue || []).map((item) => `${item.id}:${item.status}`).join(","),
      attached().map((item) => item.id).join(","), candidates().map((item) => item.uid).join(","),
    ].join("|");
    if (!force && signature === composerSignature) return;
    composerSignature = signature;
    const mode = session ? composerModeEn(session, withT) : null;
    input.placeholder = !session ? t("session.sidebar.placeholder_new")
      : mode.mode === "steer" ? t("session.sidebar.placeholder_steer") : t("session.sidebar.placeholder_continue");
    if (pickerOpen !== !picker.hidden || pickerOpen) paintPicker();
    hint.textContent = t(composerHintKey(turnRunning(session)));
    paintQueue();
    paintTray();
    paintChips();
    paintOptions();
    paintSend();
  }

  input.addEventListener("input", () => { autoGrow(); paintSend(); });
  input.addEventListener("keydown", (event) => {
    const action = composerKeyAction(event, { running: turnRunning(current()) });
    if (!action || action === "newline") return;
    // An empty new session starts from the button, not from a stray Enter.
    if (!current() && !input.value.trim() && !images().length) { event.preventDefault(); return; }
    event.preventDefault();
    send(action === "now");
  });
  sendButton.addEventListener("click", () => send());

  async function stop() {
    const session = current();
    if (!session) return;
    try {
      await stopAgentSession(session.id);
      ctx.toast?.(t("tasks.status.stopping"), "ok");
    } catch (err) { ctx.toast?.(err.message, "err"); }
  }

  async function send(now = false) {
    if (busy) return;
    const session = current();
    const text = input.value.trim();
    const action = composerAction(session, { hasContent: Boolean(text || attached().length || images().length) });
    if (action === "stop") { stop(); return; }
    if (action === "none") return;
    busy = true;
    paintComposer(true);
    try {
      if (action === "create") {
        const preset = pickPreset(presets || [], choice.platform);
        if (!preset) throw new Error(t("session.sidebar.no_provider"));
        const created = await startAgentSession({
          platform: preset.id, model: choice.model, title: newSessionTitle(text) || newSessionTitle(newContext[0]?.label || ""),
          prompt: text, context: [...newContext.map((item) => item.attachment), ...imageContext(images())],
        });
        releaseImages(images());
        sessionImages.delete("");
        rememberSessionChoice({ platform: preset.id, model: choice.model });
        newContext = [];
        drafts.delete(NEW_SESSION);
        input.value = "";
        if (created?.id) {
          sessions = [created, ...sessions.filter((item) => item.id !== created.id)];
          selected(created.id);
        }
      } else {
        const mode = composerModeEn(session, withT).mode;
        const data = await sendSessionMessage(session, text, {
          mode, context: [...contextPayload(session.id), ...imageContext(images())], now: now && mode === "steer",
        });
        releaseImages(images());
        sessionImages.delete(session.id);
        if (data.mode) {
          ctx.toast?.(t(data.mode === "interrupt" ? "session.toast.sent_now_interrupt" : "session.toast.sent_now"), "ok");
        } else if (mode === "steer") {
          const status = data.steer?.status;
          if (status === "failed") ctx.toast?.(t("session.toast.steer_failed"), "err");
          else if (status === "queued") ctx.toast?.(t("session.toast.steer_queued"), "ok");
        }
        clearSessionContext(session.id);
        drafts.delete(session.id);
        input.value = "";
        stickToBottom = true;
        sessionStream.transcript(viewKey()).catch(() => {});
      }
    } catch (err) {
      ctx.toast?.(err.message, "err");
    } finally {
      busy = false;
      autoGrow();
      paintComposer(true);
      input.focus();
    }
  }

  // ------------------------------------------------------------------ boot
  paintHead();
  paintComposer(true);

  return {
    el,
    newSession,
    openPage,
    // Execute › Runs: show `node` (the page's session list) in this view.
    showList(node) {
      if (node && node !== listOwner) {
        clear(listSlot);
        listSlot.appendChild(node);
      }
      if (node) listOwner = node;
      syncListMode();
    },
    get listMode() { return el.classList.contains("list-mode"); },
    // The view became visible (first show included).
    shown() {
      syncFollow();
      paintHead();
      paintComposer(true);
      scheduler.request();
      scheduler.flush();
      if (!listLoaded) sessionStream.refreshSessions().catch(() => {});
    },
    relocalize() {
      activityChips.relocalize();
      headSignature = "";
      statusSignature = "";
      blockNodes.clear();
      clear(column);
      switcher.setAttribute("aria-label", t("session.sidebar.switch"));
      input.setAttribute("aria-label", t("tasks.thread.composer_label"));
      input.title = t("tasks.thread.composer_keys");
      paintHead();
      paintComposer(true);
      scheduler.request();
    },
    get selectedId() { return selectedId; },
  };
}
