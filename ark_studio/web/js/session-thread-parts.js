// Thread pieces shared by the Sessions page (session-workspace.js) and the
// Sessions sidebar view (session-sidebar.js): tool activity rows and their
// one-line summaries.

import { h } from "./dom.js";
import { openImageLightbox } from "./image-viewer.js";
import { attachImageContextMenu } from "./image-copy.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { toolCommand, toolKind, toolPaths, toolGroupSummary, tabLabel as tabLabelEn } from "./session-model.js";
import { taskBadge, toolActivityKind } from "./session-activity-model.js";

const tabLabel = (path) => tabLabelEn(path, { t });

export const TOOL_ICONS = { command: "terminal", edit: "edit", search: "search", read: "file", other: "command" };

export function fmtDuration(start, end) {
  if (!start) return "—";
  const from = new Date(start).getTime();
  const to = end ? new Date(end).getTime() : Date.now();
  if (!Number.isFinite(from) || !Number.isFinite(to)) return "—";
  const seconds = Math.max(0, Math.round((to - from) / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  const hours = Math.floor(minutes / 60);
  return `${hours}h ${minutes % 60}m`;
}

export function textValue(value) {
  if (value === null || value === undefined) return "";
  if (typeof value === "string") return value;
  try { return JSON.stringify(value, null, 2); } catch { return String(value); }
}

export function toolStatusLabel(status) {
  return t(status === "running" ? "tasks.status.running" : status === "error" ? "session.tool.failed" : "session.tool.ok");
}

export function capFirst(text, lower = false) {
  const value = String(text || "");
  if (!value) return value;
  return (lower ? value.charAt(0).toLowerCase() : value.charAt(0).toUpperCase()) + value.slice(1);
}

/**
 * Inline rename: `host` shows an input with `current`; Enter or leaving the
 * field saves (onSave(text)), Esc cancels.  Returns the input.
 */
export function inlineRename(host, current, onSave, { label = "" } = {}) {
  const before = [...host.childNodes];
  const input = h("input", { class: "input sw-rename", value: current, "aria-label": label, maxlength: "120" });
  let done = false;
  const finish = (save) => {
    if (done) return;
    done = true;
    host.replaceChildren(...before);
    if (save && input.value.trim() !== current.trim()) onSave(input.value.trim());
  };
  input.addEventListener("keydown", (event) => {
    event.stopPropagation();
    if (event.key === "Enter") { event.preventDefault(); finish(true); }
    if (event.key === "Escape") { event.preventDefault(); finish(false); }
  });
  input.addEventListener("click", (event) => event.stopPropagation());
  input.addEventListener("blur", () => finish(true));
  host.replaceChildren(input);
  input.focus();
  input.select();
  return input;
}

// Text drawn for a tool output; longer output waits behind "Show all".
export const OUTPUT_DRAW_CHARS = 4000;

export function toolSummaryText(summary, items = []) {
  const agents = items.filter((item) => toolActivityKind(item) === "subagent");
  if (agents.length && agents.length < items.length) {
    // A run with sub-agents and other tools: "2 sub-agents, 1 running · Ran 3 commands".
    const others = items.filter((item) => toolActivityKind(item) !== "subagent");
    return `${toolSummaryText(toolGroupSummary(agents), agents)} · ${toolSummaryText(toolGroupSummary(others), others)}`;
  }
  if (agents.length && agents.length === items.length) {
    const running = agents.filter((item) => taskBadge(item)?.status === "running" || item.status === "running").length;
    if (agents.length === 1) {
      const input = agents[0].input && typeof agents[0].input === "object" ? agents[0].input : {};
      return t(running ? "tasks.thread.subagent_running" : "tasks.thread.subagent_done",
        { label: String(input.description || input.subagent_type || agents[0].name || "").slice(0, 80) });
    }
    return t(running ? "tasks.thread.subagents_running_n" : "tasks.thread.subagents_n", { n: agents.length, running });
  }
  const background = items.filter((item) => toolActivityKind(item) === "background" && taskBadge(item)?.status === "running").length;
  if (background && !summary.running) return t("tasks.thread.running_background", { n: background });
  if (summary.running) {
    return t({
      command: "tasks.thread.running_command", edit: "tasks.thread.editing_files",
      search: "tasks.thread.searching", read: "tasks.thread.reading",
    }[summary.kind] || "tasks.thread.running_tool");
  }
  const { counts } = summary;
  const parts = [];
  if (counts.edit) {
    const n = summary.edited;
    parts.push(t(n === 1 ? "tasks.thread.edited_one" : "tasks.thread.edited_n", { n }));
  }
  if (counts.command) parts.push(t(counts.command === 1 ? "tasks.thread.ran_one" : "tasks.thread.ran_n", { n: counts.command }));
  if (counts.search) parts.push(t(counts.search === 1 ? "tasks.thread.searched_one" : "tasks.thread.searched_n", { n: counts.search }));
  if (counts.read) parts.push(t(counts.read === 1 ? "tasks.thread.read_one" : "tasks.thread.read_n", { n: counts.read }));
  if (counts.other) parts.push(t(counts.other === 1 ? "tasks.thread.used_one" : "tasks.thread.used_n", { n: counts.other }));
  return parts.map((part, index) => capFirst(part, index > 0)).join(", ");
}

export function toolPreview(tools) {
  const last = tools[tools.length - 1] || {};
  const command = toolCommand(last);
  if (command) return command.split("\n")[0];
  const input = last.input && typeof last.input === "object" ? last.input : {};
  const query = input.pattern ?? input.query ?? input.q;
  if (toolKind(last) === "search" && typeof query === "string") return query;
  const paths = toolPaths(last);
  if (paths.length) return paths.map((path) => tabLabel(path)).join(", ");
  return last.name || "";
}

export function patchLines(text) {
  const pre = h("pre", { class: "sw-act-out sw-act-diff" });
  for (const line of String(text || "").split("\n")) {
    const cls = line.startsWith("+") && !line.startsWith("+++") ? "add"
      : line.startsWith("-") && !line.startsWith("---") ? "del"
        : line.startsWith("@@") || line.startsWith("***") ? "hunk" : "";
    pre.appendChild(h("span", { class: cls }, `${line}\n`));
  }
  return pre;
}

// Output text, drawn up to OUTPUT_DRAW_CHARS; "Show all" draws the rest, or
// fetches the whole kept output first (onFull) when the page held a preview.
function outputBlock(item, onFull) {
  const pre = h("pre", { class: "sw-act-out" });
  const full = textValue(item.output);
  const total = Number(item.output_chars) || full.length;
  const more = full.length > OUTPUT_DRAW_CHARS || (item.output_preview && onFull);
  pre.textContent = full.length > OUTPUT_DRAW_CHARS ? full.slice(0, OUTPUT_DRAW_CHARS) : full;
  if (!more) return pre;
  const button = h("button", {
    class: "link sw-act-more", type: "button",
    onclick: async () => {
      button.disabled = true;
      let text = full;
      if (item.output_preview && onFull) {
        try {
          const got = await onFull(item.id);
          if (got?.output != null) text = textValue(got.output);
        } catch { /* keep the preview */ }
      }
      pre.textContent = text;
      button.remove();
    },
  }, t("tasks.thread.show_all_output", { n: total.toLocaleString() }));
  return h("div", { class: "sw-act-outwrap" }, pre, button);
}

// The task line of a sub-agent or background call: status, last progress.
function taskLine(item, onOpenAgent) {
  const badge = taskBadge(item);
  if (!badge) return null;
  const isAgent = badge.kind === "subagent";
  if (!isAgent && !badge.background) return null;
  const status = badge.status || (item.status === "error" ? "failed" : "completed");
  return h("div", { class: `sw-act-task ${status}`.trim() },
    h("i", { class: `sa-dot ${status}` }),
    h("span", { class: "sw-act-task-kind" }, t(isAgent ? "session.activity.kind_subagent" : "session.activity.background_task")),
    h("span", { class: "sw-act-task-state" }, t(`session.activity.status_${status}`)),
    badge.text ? h("span", { class: "sw-act-task-text" }, badge.text) : null,
    badge.tokens ? h("span", { class: "sw-act-task-tokens" }, t("session.activity.tokens", { n: badge.tokens.toLocaleString() })) : null,
    isAgent && onOpenAgent ? h("button", {
      class: "link sw-act-open", type: "button",
      onclick: (event) => { event.stopPropagation(); onOpenAgent(item); },
    }, icon("external", "sm"), t("session.tabs.open_agent")) : null);
}

// Images a tool returned (a Read of a png): fetched one by one when the row
// opens, shown as thumbnails that open in the lightbox.
function toolImages(item, onImage) {
  const count = Number(item.images) || 0;
  if (!count || !onImage) return null;
  const wrap = h("div", { class: "sw-act-images" });
  for (let n = 1; n <= Math.min(count, 12); n += 1) {
    const img = h("img", { alt: t("session.images.tool_image", { n }), decoding: "async" });
    const button = h("button", {
      class: "sw-turn-image sw-act-image", type: "button", title: t("session.images.tool_image", { n }), disabled: true,
      onclick: () => openImageLightbox({ src: img.src, alt: img.alt, title: img.alt }),
    }, img);
    attachImageContextMenu(button, () => (img.src ? { src: img.src, kind: "image", caption: img.alt } : null));
    onImage(item.id, n).then((src) => {
      if (!src) return;
      img.src = src;
      button.disabled = false;
    }).catch(() => {});
    wrap.appendChild(button);
  }
  return wrap;
}

export function renderToolEntry(item, { onFull = null, onOpenAgent = null, onImage = null } = {}) {
  const status = item.status || "running";
  const kind = toolKind(item);
  const command = toolCommand(item);
  const paths = toolPaths(item);
  const input = item.input && typeof item.input === "object" ? item.input : null;
  let head;
  if (command) head = h("code", { class: "sw-act-cmd" }, h("span", { class: "sw-act-prompt" }, "$"), command);
  else head = h("code", { class: "sw-act-cmd" }, h("b", null, item.name || t("session.approval.kind_tool")), paths.length ? ` ${paths.join(", ")}` : "");
  let detail = null;
  if (!command && kind === "edit" && typeof input?.patch === "string") detail = patchLines(input.patch);
  else if (!command && item.input !== null && item.input !== undefined && !paths.length) {
    const inputText = textValue(item.input);
    detail = h("pre", { class: "sw-act-out sw-act-input" },
      inputText.length > OUTPUT_DRAW_CHARS ? `${inputText.slice(0, OUTPUT_DRAW_CHARS)}…` : inputText);
  }
  const hasOutput = item.output !== null && item.output !== undefined && textValue(item.output) !== "";
  return h("div", { class: `sw-act-item ${status}`.trim() },
    h("div", { class: "sw-act-item-head" }, head,
      h("span", { class: "spacer" }),
      status !== "ok" ? h("span", { class: `sw-act-status ${status}`.trim() }, toolStatusLabel(status)) : null,
      item.at && item.completed_at ? h("span", { class: "sw-msg-time" }, fmtDuration(item.at, item.completed_at)) : null),
    taskLine(item, onOpenAgent),
    detail,
    toolImages(item, onImage),
    hasOutput ? outputBlock(item, onFull)
      : status === "running" ? h("div", { class: "sw-act-wait" }, t("tasks.thread.waiting_output")) : null);
}

/**
 * A run of tool calls as one collapsed row: "Ran 2 commands  pytest -q",
 * "Running in the background", "Sub-agent: Survey session code".  The
 * entries are built only when the row is opened.  `openSet` remembers open
 * rows by block key; `onToggle(open)` runs after a toggle.
 */
export function renderToolGroup(block, { openSet, summary, onToggle = null, onFull = null, onOpenAgent = null, onImage = null, extraClass = "" } = {}) {
  const body = h("div", { class: "sw-act-body" });
  const fill = () => {
    if (body.childNodes.length) return;
    for (const item of block.items) body.appendChild(renderToolEntry(item, { onFull, onOpenAgent, onImage }));
  };
  const open = openSet.has(block.key);
  if (open) fill();
  const taskRunning = block.items.some((item) => toolActivityKind(item) && taskBadge(item)?.status === "running");
  const running = summary.running || taskRunning;
  const kind = summary.running ? summary.kind : summary.primary;
  const onlyAgents = block.items.every((item) => toolActivityKind(item) === "subagent");
  const iconName = onlyAgents ? "sparkles" : TOOL_ICONS[kind] || "command";
  const agent = onlyAgents && block.items.length === 1 ? block.items[0] : null;
  const agentLine = agent ? taskBadge(agent)?.text : "";
  const others = block.items.filter((item) => toolActivityKind(item) !== "subagent");
  const openAgent = (event) => {
    event.preventDefault();
    event.stopPropagation();
    onOpenAgent(agent);
  };
  return h("details", {
    class: `sw-act ${extraClass} ${running ? "running" : ""} ${summary.failed ? "failed" : ""}`.trim(),
    open: open ? true : undefined,
    ontoggle: (event) => {
      if (event.target.open) { openSet.add(block.key); fill(); } else openSet.delete(block.key);
      onToggle?.(event.target.open);
    },
  },
    h("summary", { class: "sw-act-head" },
      h("span", { class: "sw-act-icon" }, icon(iconName, "sm")),
      h("span", { class: "sw-act-label" }, toolSummaryText(summary, block.items)),
      h("span", { class: "sw-act-preview" }, agentLine || toolPreview(others.length ? others : block.items)),
      summary.failed ? h("span", { class: "sw-act-fail" }, t("tasks.thread.failed_n", { n: summary.failed })) : null,
      agent && onOpenAgent ? h("span", {
        class: "sw-act-open-tab", role: "button", tabindex: "0", title: t("session.tabs.open_agent"),
        "aria-label": t("session.tabs.open_agent"), dataset: { openAgent: agent.id },
        onclick: openAgent,
        onkeydown: (event) => { if (event.key === "Enter" || event.key === " ") openAgent(event); },
      }, icon("external", "sm")) : null,
      h("span", { class: "sw-act-chev" }, icon("chevronRight", "sm"))),
    body);
}
