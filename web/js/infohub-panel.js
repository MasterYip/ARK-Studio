import { h, clear } from "./dom.js";
import { icon } from "./icons.js";
import { api } from "./api.js";
import { t } from "./i18n.js";

const SIZE_KEY = "ark.infohubSize.v1";

const KIND_COLORS = {
  topic: "#3b82f6", todo: "#ef4444", event: "#a855f7", decision: "#f59e0b",
  question: "#eab308", announcement: "#14b8a6", resource: "#10b981",
  digest: "#64748b", note: "#94a3b8",
};

let panel = null;
let cleanup = null;
let current = null;
let sourcePath = "";
let expanded = false;
let detail = null;
let detailError = "";
let loadingDetail = false;
let smallRect = null;

export function closeInfoHubPreview() {
  cleanup?.();
  cleanup = null;
  panel?.remove();
  panel = null;
  current = null;
  detail = null;
  detailError = "";
  expanded = false;
  loadingDetail = false;
  smallRect = null;
}

export function openInfoHubPreview(node, anchor = null, path = "") {
  closeInfoHubPreview();
  if (!node) return;
  current = node;
  sourcePath = path || "";
  panel = h("aside", { class: "infohub-preview", role: "complementary", "aria-label": t("infohub.preview.aria") });
  try {
    const saved = JSON.parse(localStorage.getItem(SIZE_KEY) || "{}");
    if (Number(saved.width) >= 280) panel.style.width = `${saved.width}px`;
    if (Number(saved.height) >= 180) panel.style.height = `${saved.height}px`;
  } catch { /* storage can be unavailable */ }
  document.getElementById("overlay-root")?.appendChild(panel);
  render();
  position(panel, anchor);
  bindPanelMoveResize(panel);
  let resizeObserver = null;
  try {
    resizeObserver = new ResizeObserver(() => {
      if (!panel || expanded) return;
      const width = Math.round(panel.getBoundingClientRect().width);
      const height = Math.round(panel.getBoundingClientRect().height);
      if (width >= 280 && height >= 180) {
        localStorage.setItem(SIZE_KEY, JSON.stringify({ width, height }));
      }
    });
    resizeObserver.observe(panel);
  } catch { /* ResizeObserver may be unavailable */ }
  void loadDetail();

  const onDown = (event) => {
    if (panel && !panel.contains(event.target)) closeInfoHubPreview();
  };
  const onWheel = (event) => {
    if (panel && !panel.contains(event.target)) closeInfoHubPreview();
  };
  const onKey = (event) => {
    if (event.key === "Escape" && panel) closeInfoHubPreview();
  };
  document.addEventListener("pointerdown", onDown, true);
  document.addEventListener("wheel", onWheel, { capture: true, passive: true });
  document.addEventListener("keydown", onKey);
  cleanup = () => {
    resizeObserver?.disconnect();
    document.removeEventListener("pointerdown", onDown, true);
    document.removeEventListener("wheel", onWheel, true);
    document.removeEventListener("keydown", onKey);
  };
}

function kindColor(node) {
  return node?.color || KIND_COLORS[node?.kind] || "#94a3b8";
}

function bindPanelMoveResize(el) {
  let drag = null;
  el.addEventListener("pointerdown", (event) => {
    if (event.button !== 0) return;
    const head = event.target.closest(".infohub-preview-head");
    const resize = event.target.closest(".infohub-resize");
    if (!head && !resize) return;
    if (head && event.target.closest("button,input,textarea,select,a")) return;
    event.preventDefault();
    const rect = el.getBoundingClientRect();
    drag = {
      mode: resize ? "resize" : "move",
      startX: event.clientX, startY: event.clientY,
      left: rect.left, top: rect.top, width: rect.width, height: rect.height,
    };
    el.classList.add(resize ? "resizing" : "moving");
    const move = (ev) => {
      if (!drag) return;
      if (drag.mode === "move") {
        const left = Math.max(8, Math.min(window.innerWidth - rect.width - 8, drag.left + ev.clientX - drag.startX));
        const maxTop = Math.max(8, window.innerHeight - rect.height - 8);
        const top = Math.max(8, Math.min(maxTop, drag.top + ev.clientY - drag.startY));
        el.style.left = `${left}px`;
        el.style.top = `${top}px`;
      } else {
        const width = Math.max(280, Math.min(window.innerWidth - drag.left - 8, drag.width + ev.clientX - drag.startX));
        const height = Math.max(180, Math.min(window.innerHeight - drag.top - 8, drag.height + ev.clientY - drag.startY));
        el.style.width = `${width}px`;
        el.style.height = `${height}px`;
        el.style.maxHeight = "none";
      }
    };
    const up = () => {
      drag = null;
      el.classList.remove("moving", "resizing");
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  });
}

function render() {
  if (!panel || !current) return;
  const node = current;
  const d = detail || null;
  clear(panel);
  panel.className = `infohub-preview${expanded ? " expanded" : ""}`;
  panel.appendChild(h("div", { class: "infohub-preview-head" },
    h("span", { class: "infohub-preview-kind", style: { "--kind-color": kindColor(node) } }, node.kind || "unit"), // i18n-ignore: InfoHub kind value
    (d?.imported || node.imported) ? h("span", { class: "pill ok" }, t("ideas.infohub_imported")) : null,
    h("span", { class: "spacer" }),
    h("button", {
      class: `icon-btn infohub-expand-btn ${expanded ? "expanded" : ""}`.trim(),
      title: t(expanded ? "infohub.preview.collapse" : "infohub.preview.expand"),
      "aria-label": t(expanded ? "infohub.preview.collapse" : "infohub.preview.expand"),
      "aria-expanded": expanded ? "true" : "false",
      onclick: toggleExpand,
    }, icon("chevron", "sm")),
    h("button", { class: "icon-btn", title: t("action.close"), onclick: () => closeInfoHubPreview() }, icon("x", "sm"))));

  panel.appendChild(h("div", { class: "infohub-preview-body" },
    h("div", { class: "infohub-preview-title" }, d?.title || node.title || t("common.untitled")),
    (d?.summary || node.summary) ? h("div", { class: "infohub-preview-summary" }, d?.summary || node.summary) : null,
    h("div", { class: "infohub-preview-meta" },
      h("span", null, d?.status || node.status || "open"), // i18n-ignore: InfoHub status value
      (d?.for_me || node.for_me) ? h("span", { class: "warn" }, t("infohub.preview.for_me")) : null,
      h("span", null, d ? t("infohub.preview.subtopics_total", { n: d.n_children || 0, m: d.n_messages_total || 0 }) : t("infohub.preview.messages", { n: node.n_messages || 0 })),
      (d?.source || node.source) ? h("span", null, d?.source || node.source) : null,
      d?.priority ? h("span", null, t("infohub.preview.priority", { p: d.priority })) : null),
    d ? detailBody(d) : (loadingDetail ? loadingBody() : previewTags(node)),
    !expanded ? h("div", { class: "infohub-preview-note" }, t("infohub.preview.note")) : null));
}

function previewTags(node) {
  const tags = (node.tags || []).filter((tag) => !tag.startsWith("source:"));
  return tags.length
    ? h("div", { class: "infohub-preview-tags" }, ...tags.slice(0, 12).map((tag) => h("span", null, tag)))
    : null;
}

function loadingBody() {
  if (detailError) return h("div", { class: "infohub-detail-error" }, detailError);
  return h("div", { class: "infohub-detail-loading" }, h("span", { class: "spinner" }), t("infohub.preview.loading"));
}

function detailBody(d) {
  if (loadingDetail) return loadingBody();
  if (detailError) return h("div", { class: "infohub-detail-error" }, detailError);
  return h("div", { class: "infohub-detail" },
    detailSection(t("infohub.preview.tags"), d.tags?.length
      ? h("div", { class: "infohub-preview-tags" }, ...d.tags.map((tag) => h("span", null, tag)))
      : h("div", { class: "infohub-detail-empty" }, t("infohub.preview.no_tags"))),
    d.children?.length ? detailSection(t("infohub.preview.subtopics", { n: d.children.length }),
      h("div", { class: "ih-subtree" }, ...d.children.map((child, index) => subtopicEl(child, 0, index === 0)))) : null,
    d.messages?.length ? detailSection(t("infohub.preview.conversation", { n: d.messages.length }), transcriptEl(d.messages)) : null,
    h("div", { class: "infohub-detail-note" }, t("infohub.preview.detail_note")));
}

function detailSection(title, body) {
  return h("section", { class: "infohub-detail-section" },
    h("h4", null, title), body);
}

function transcriptEl(messages) {
  return h("div", { class: "ih-transcript" },
    ...(messages || []).map((message) => {
      const sender = message.sender_name || message.sender || t("common.unknown");
      return h("div", { class: "ih-transcript-line" },
        h("span", { class: "ih-transcript-time" }, shortTime(message.sent_at_iso || message.sent_at)),
        h("span", { class: "ih-transcript-sender" }, `${sender}:`),
        h("span", { class: "ih-transcript-text" }, message.text || t("infohub.preview.no_text")));
    }));
}

function shortTime(value) {
  if (value == null || value === "") return "";
  const ms = typeof value === "number"
    ? (value > 10_000_000_000 ? value : value * 1000)
    : Date.parse(value);
  if (!Number.isFinite(ms)) return "";
  return new Date(ms).toLocaleString([], { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" });
}

function subtopicEl(child, depth = 0, openInitially = false) {
  const details = h("details", { class: "ih-subcard", open: openInitially ? true : undefined },
    h("summary", { class: "ih-subcard-summary" },
      h("span", { class: "ih-subcard-kind", style: { "--kind-color": kindColor(child) } }, child.kind || "unit"), // i18n-ignore: InfoHub kind value
      h("span", { class: "ih-subcard-meta" }, t("infohub.preview.subtopics_total", { n: child.n_children || 0, m: child.n_messages_total || 0 })),
      h("span", { class: "ih-subcard-time" }, shortTime(child.last_activity_at_iso || child.updated_at_iso)),
      h("div", { class: "ih-subcard-title" }, child.title || t("common.untitled"))));
  const body = h("div", { class: "ih-subcard-body" });
  if (child.summary) body.appendChild(h("div", { class: "ih-subcard-text" }, child.summary));
  if (child.tags?.length) {
    body.appendChild(h("div", { class: "infohub-preview-tags" },
      ...child.tags.slice(0, 18).map((tag) => h("span", null, tag))));
  }
  const conversation = h("div", { class: "ih-subcard-conversation" });
  const nested = h("div", { class: "ih-subtree ih-subtree-nested" });
  details.appendChild(h("div", { class: "ih-subcard-content" }, body, conversation, nested));
  let loaded = false;
  let loading = false;
  async function load() {
    if (loaded || loading || depth >= 6) return;
    loading = true;
    try {
      const data = await api.infoHubUnit({ path: sourcePath, key: child.key });
      const nestedUnit = data?.unit || data;
      clear(conversation);
      if (nestedUnit.messages?.length) {
        conversation.appendChild(h("div", { class: "ih-mini-label" }, t("infohub.preview.conversation", { n: nestedUnit.messages.length })));
        conversation.appendChild(transcriptEl(nestedUnit.messages));
      }
      clear(nested);
      for (const item of nestedUnit.children || []) nested.appendChild(subtopicEl(item, depth + 1));
      loaded = true;
    } catch (err) {
      if (!conversation.childElementCount) conversation.textContent = err.message || t("infohub.preview.conversation_failed");
    } finally {
      loading = false;
    }
  }
  details.addEventListener("toggle", () => { if (details.open) load(); });
  if (openInitially) queueMicrotask(load);
  return details;
}

function messageEl(message) {
  const sender = message.sender_name || message.sender || t("common.unknown");
  const meta = [fmtTime(message.sent_at_iso || message.sent_at), message.channel_name || message.channel, message.kind]
    .filter(Boolean).join(" · ");
  return h("article", { class: "infohub-message" },
    h("div", { class: "infohub-message-head" },
      h("b", null, sender),
      message.from_me ? h("span", { class: "pill" }, t("infohub.preview.me")) : null,
      message.mentions_me ? h("span", { class: "pill warn" }, t("infohub.preview.mention")) : null,
      h("span", { class: "infohub-message-time" }, meta)),
    h("div", { class: "infohub-message-text" }, message.text || t("infohub.preview.no_text")),
    message.link ? linkEl(message.link) : null);
}

function linkEl(link) {
  const label = String(link);
  if (/^https?:\/\//i.test(label)) {
    return h("a", { class: "infohub-message-link", href: label, target: "_blank", rel: "noreferrer" }, label);
  }
  return h("div", { class: "infohub-message-link" }, label);
}

function relationRow(kind, item) {
  return h("div", { class: "infohub-relation" },
    h("span", { class: "infohub-relation-kind" }, kind),
    h("span", null, item.key || "—"),
    h("span", { class: "infohub-relation-title" }, item.title || t("common.untitled")),
    item.relation ? h("span", { class: "infohub-relation-name" }, item.relation) : null);
}

function fmtTime(value) {
  if (value == null || value === "") return "—";
  if (typeof value === "string") return value.replace("T", " ").replace(/\+00:00$/, " UTC");
  const ms = Number(value) > 10_000_000_000 ? Number(value) : Number(value) * 1000;
  if (!Number.isFinite(ms)) return "—";
  return new Date(ms).toLocaleString();
}

async function loadDetail() {
  if (!panel || detail || loadingDetail) return;
  loadingDetail = true;
  render();
  try {
    detail = await api.infoHubUnit({ path: sourcePath, key: current?.uid || current?.key || "" });
    detail = detail?.unit || detail;
  } catch (err) {
    detailError = err.message || t("infohub.preview.detail_failed");
  } finally {
    loadingDetail = false;
    render();
  }
}

async function toggleExpand() {
  if (!panel) return;
  expanded = !expanded;
  if (!expanded) {
    render();
    if (smallRect) {
      panel.style.width = `${smallRect.width}px`;
      panel.style.height = `${smallRect.height}px`;
      panel.style.left = smallRect.left;
      panel.style.top = smallRect.top;
      panel.style.maxHeight = "";
      smallRect = null;
    }
    return;
  }
  const rect = panel.getBoundingClientRect();
  smallRect = {
    width: rect.width,
    height: rect.height,
    left: panel.style.left || `${rect.left}px`,
    top: panel.style.top || `${rect.top}px`,
  };
  panel.style.width = "";
  panel.style.height = "";
  panel.style.maxHeight = "";
  render();
  void loadDetail();
}

let lastAnchor = null;

function position(el, anchor) {
  lastAnchor = anchor;
  if (!el || expanded) return;
  const rect = el.getBoundingClientRect();
  const width = rect.width || 360;
  const height = Math.min(rect.height || 420, window.innerHeight - 80);
  const x = anchor ? Math.max(12, Math.min(window.innerWidth - width - 12, anchor.x + 16)) : window.innerWidth - width - 16;
  const y = anchor ? Math.max(56, Math.min(window.innerHeight - height - 12, anchor.y - 20)) : 70;
  el.style.left = `${x}px`;
  el.style.top = `${y}px`;
}
