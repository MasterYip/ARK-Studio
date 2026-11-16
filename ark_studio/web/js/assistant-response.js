import { h, richText, unitReference, copyText } from "./dom.js";
import { icon } from "./icons.js";
import { t } from "./i18n.js";

const TOOL_KEYS = {
  create_unit: "chat.tool_step.create_unit", update_unit: "chat.tool_step.update_unit",
  link_units: "chat.tool_step.link_units", search_units: "action.search",
  dispatch_task: "assistant.dispatch_title", get_unit: "chat.tool.read",
  list_units: "chat.tool.list", get_context: "chat.tool.context",
  get_infohub_context: "chat.tool.infohub", search_infohub: "chat.tool.infohub",
  get_infohub_unit: "chat.tool.infohub",
};
const toolLabel = (name) => TOOL_KEYS[name] ? t(TOOL_KEYS[name]) : name;

export function assistantRichText(text) {
  const root = richText(text || "");
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode: (node) => node.parentElement?.closest("a, pre, .math-inline, .math-block, .mermaid-block")
      ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_ACCEPT,
  });
  const nodes = [];
  while (walker.nextNode()) nodes.push(walker.currentNode);
  for (const node of nodes) {
    const value = node.nodeValue || "";
    const pattern = /\b([A-Z]{1,4}-\d{3,5})\b/g;
    const matches = [...value.matchAll(pattern)];
    if (!matches.length) continue;
    const fragment = document.createDocumentFragment();
    let last = 0;
    for (const match of matches) {
      fragment.append(document.createTextNode(value.slice(last, match.index)), unitReference(match[1]));
      last = match.index + match[0].length;
    }
    fragment.append(document.createTextNode(value.slice(last)));
    const code = node.parentElement;
    if (code?.tagName === "CODE" && code.textContent === value) code.replaceWith(fragment);
    else node.replaceWith(fragment);
  }
  return root;
}

export function followAssistantThread(thread) {
  let pinned = true;
  thread.addEventListener("scroll", () => {
    pinned = thread.scrollHeight - thread.scrollTop - thread.clientHeight < 60;
  });
  return (force = false) => { if (force || pinned) thread.scrollTop = thread.scrollHeight; };
}

// Shared by the full chat and dock. Keep details and focus intact between deltas.
export function createAssistantResponse(message, { live = false, onPaint = () => {} } = {}) {
  let current = message, streaming = live, phase = "thinking", statusText = "", error = message.meta?.error || "";
  let manualReasoning = false, queued = false, lastContent = null, lastReasoning = null;
  const tools = new Map((message.tools || []).map((tool, i) => [String(i), tool]));
  const status = h("div", { class: "assistant-progress", role: "status" });
  const summary = h("summary", null, t("chat.reasoning"));
  const reasoningBody = h("div", { class: "chat-reasoning-body" });
  const reasoning = h("details", { class: "chat-reasoning" }, summary, reasoningBody);
  summary.addEventListener("click", () => { manualReasoning = true; });
  const toolBody = h("div", { class: "assistant-tool-list" });
  const toolSummary = h("summary");
  const toolDetails = h("details", { class: "assistant-tool-steps" }, toolSummary, toolBody);
  const answer = h("div", { class: "assistant-answer" });
  const failure = h("div", { class: "chat-error", role: "alert" });
  const copy = h("button", {
    class: "icon-btn assistant-copy", type: "button", title: t("action.copy"),
    "aria-label": t("action.copy"), onclick: () => copyText(current.content || ""),
  }, icon("copy", "sm"));
  const el = h("div", { class: "assistant-response" }, status, reasoning, toolDetails, answer, failure, copy);

  function paint() {
    queued = false;
    const text = current.content || "", thoughts = current.meta?.reasoning || "";
    el.classList.toggle("is-streaming", streaming);
    el.dataset.phase = streaming ? phase : error ? "error" : "done";
    status.replaceChildren(icon(error ? "alert" : streaming ? "clock" : "check", "sm"),
      document.createTextNode(error ? t("chat.interrupted") : streaming
        ? statusText || t(phase === "writing" ? "chat.writing" : "chat.thinking") : t("chat.finished")));
    reasoning.hidden = !thoughts;
    summary.textContent = streaming && phase === "thinking" && !text ? t("chat.thinking") : t("chat.reasoning");
    if (!manualReasoning) reasoning.open = streaming && phase === "thinking" && !text;
    if (lastReasoning !== thoughts) {
      const follow = reasoningBody.scrollHeight - reasoningBody.scrollTop - reasoningBody.clientHeight < 40;
      reasoningBody.textContent = thoughts;
      if (follow) reasoningBody.scrollTop = reasoningBody.scrollHeight;
      lastReasoning = thoughts;
    }
    if (lastContent !== text) {
      answer.replaceChildren(assistantRichText(text));
      lastContent = text;
    }
    answer.classList.toggle("has-caret", streaming && Boolean(text));
    toolDetails.hidden = !tools.size;
    toolSummary.textContent = t("chat.tool_steps", { n: tools.size });
    toolBody.replaceChildren(...[...tools.values()].map((tool) => {
      const label = toolLabel(tool.name), running = tool.state === "running";
      const target = tool.args?.ref || tool.args?.query || tool.args?.title || "";
      return h("div", { class: `assistant-tool-row ${running ? "running" : tool.ok === false ? "err" : ""}` },
        icon(running ? "clock" : tool.ok === false ? "alert" : "check", "sm"),
        h("span", null, label), target ? h("span", { class: "assistant-tool-target" }, String(target)) : null,
        tool.result?.uid ? unitReference(tool.result.uid) : null);
    }));
    failure.hidden = !error;
    failure.textContent = error;
    copy.hidden = streaming || !text;
    onPaint();
  }
  function schedule() {
    if (queued) return;
    queued = true;
    requestAnimationFrame(paint);
  }
  paint();
  return {
    el,
    push(event) {
      if (event.type === "status") {
        phase = event.phase || "thinking";
        statusText = event.text || "";
      } else if (event.type === "reasoning") {
        current.meta ||= {};
        current.meta.reasoning = (current.meta.reasoning || "") + (event.text || "");
        phase = "thinking"; statusText = "";
      } else if (event.type === "content") {
        current.content = (current.content || "") + (event.text || "");
        phase = "writing"; statusText = "";
      } else if (event.type === "tool") {
        tools.set(event.id, { ...tools.get(event.id), ...event });
        phase = "tool";
        statusText = event.state === "running" ? toolLabel(event.name) : t("chat.thinking");
      }
      schedule();
    },
    finish(final = current) {
      current = final; streaming = false; tools.clear();
      (final.tools || []).forEach((tool, i) => tools.set(String(i), tool));
      paint();
    },
    fail(text) {
      streaming = false; error = text;
      current.meta ||= {}; current.meta.error = text;
      paint();
    },
  };
}
