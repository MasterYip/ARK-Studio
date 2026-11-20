// Shared pieces of "Needs you" (Home panel, views/overview.js) and the
// Research › Review page (views/review.js): the item row, the category
// filter bar, and the dialogs that accept, send back, decide or comment.
// The pure parts live in needs-model.js.
import { h, fmtDate, fmtDateTime, relativeTime, richText, typeColor } from "./dom.js";
import { t, typeLabel } from "./i18n.js";
import { icon } from "./icons.js";
import { openModal, toast } from "./components.js";
import { api, authenticatedUrl, mediaUrl } from "./api.js";
import { openMediaLightbox } from "./image-viewer.js";
import { attachImageContextMenu } from "./image-copy.js";
import {
  borrowedSources, decisionOptions, groupExplain, groupNeeds, needsFilterActive, needsKind, needsLightboxItems,
  normalizeNeedsFilter, previewKind, runBatch, setNeedsGroupAll, toggleNeedsValue, NEEDS_FILTER_GROUPS,
} from "./needs-model.js";
import {
  anchorLabel, anchorUnit, changeSummary, changesText, clockLabel, diffBlocks, keyNumber, noteParts, reviewCardText,
} from "./review-model.js";
import { authorName, threadComments } from "./comments-model.js";
import { displayName } from "./users.js";
import { assigneeChip } from "./assignee.js";

/** Fired on `document` after the needs list was reloaded (refreshNeeds). */
export const NEEDS_CHANGED = "ark:needs-changed";

export function needsProjectKey(ctx) {
  return String(ctx.S.project?.id || ctx.S.project?.name || "local");
}

// Reload the list from the server, update the topbar badge, then let each
// view that shows it redraw only itself (no full route change, so the page
// keeps its scroll position).
export async function refreshNeeds(ctx) {
  try {
    const data = await api.project();
    ctx.S.needsYou = data.needs_you || [];
    if (data.summary && ctx.S.project) ctx.S.project = { ...ctx.S.project, ...data.summary };
    document.dispatchEvent(new CustomEvent(NEEDS_CHANGED, { detail: { ctx } }));
    ctx.syncNeedsBadge?.();
  } catch (err) { toast(err.message, "err"); }
}

// ------------------------------------------------------------- filters
// Category filters (type / kind / assignee), remembered per project and
// shared by Home and Review.  Each group is a drop-down of capsule chips
// with an "All" switch, like the Map filter rail.
let openGroup = "";

export function needsFilterFor(ctx, projectKey = needsProjectKey(ctx)) {
  const byProject = ctx.S.homeNeedsFilter ||= {};
  if (!(projectKey in byProject)) {
    let raw = {};
    try { raw = JSON.parse(localStorage.getItem(`ark.needsFilter:${projectKey}`) || "{}"); } catch { raw = {}; }
    byProject[projectKey] = normalizeNeedsFilter(raw);
  }
  return byProject[projectKey];
}

export function saveNeedsFilter(ctx, projectKey, next) {
  (ctx.S.homeNeedsFilter ||= {})[projectKey] = next;
  try { localStorage.setItem(`ark.needsFilter:${projectKey}`, JSON.stringify(next)); } catch { /* storage unavailable */ }
}

/** Closes any open filter drop-down on the next redraw. */
export function closeNeedsFilters() { openGroup = ""; }

function valueLabel(group, value) {
  if (group === "type") return typeLabel(value);
  if (group === "kind") return t(`needs.kind.${value}`);
  return value ? displayName(value) : t("assignee.none");
}

/** The filter bar; `onChange(next)` stores and redraws. */
export function needsFilterBar(facets, filter, onChange) {
  const groupLabel = { type: t("filters.types"), kind: t("needs.filter.kind"), assignee: t("needs.filter.assignee") };
  const bar = h("div", { class: "needs-filters", role: "group", "aria-label": t("needs.filter.label") });
  for (const group of NEEDS_FILTER_GROUPS) {
    const values = facets[group] || [];
    if (!values.length) continue;
    const hidden = (filter[group] || []).filter((value) => values.some((item) => item.value === value));
    const shown = values.length - hidden.length;
    const apply = (next) => { openGroup = group; onChange(next); };
    const allToggle = h("input", {
      type: "checkbox", checked: hidden.length === 0,
      "aria-label": `${t("filters.all")} ${groupLabel[group]}`,
      onchange: (event) => apply(setNeedsGroupAll(filter, group, event.target.checked, values.map((item) => item.value))),
    });
    allToggle.indeterminate = hidden.length > 0 && shown > 0;
    const chips = h("div", { class: "type-filter" }, values.map(({ value, n }) => {
      const on = !hidden.includes(value);
      return h("button", {
        type: "button", class: `type-chip ${on ? "on" : ""}`.trim(), "aria-pressed": String(on),
        dataset: { group, value },
        onclick: () => apply(toggleNeedsValue(filter, group, value)),
      }, group === "type" ? h("i", { style: { "--dot": typeColor(value) } }) : null,
      valueLabel(group, value), h("span", { class: "n" }, String(n)));
    }));
    const details = h("details", {
      class: `needs-filter ${hidden.length ? "active" : ""}`.trim(), dataset: { group },
      open: openGroup === group ? true : undefined,
    },
    h("summary", null, h("span", null, groupLabel[group]),
      h("b", null, hidden.length ? `${shown}/${values.length}` : t("filters.all")), icon("chevron", "sm")),
    h("div", { class: "needs-filter-pop" },
      h("label", { class: "type-all-toggle" }, allToggle, t("filters.all")),
      chips));
    details.addEventListener("toggle", () => {
      if (details.open) {
        openGroup = group;
        // Keep the drop-down on screen: open it leftwards near the right edge.
        const pop = details.querySelector(".needs-filter-pop");
        pop.classList.remove("align-end");
        if (pop.getBoundingClientRect().right > document.documentElement.clientWidth - 8) pop.classList.add("align-end");
        bar.querySelectorAll("details[open]").forEach((other) => { if (other !== details) other.open = false; });
      } else if (openGroup === group) openGroup = "";
    });
    bar.appendChild(details);
  }
  if (needsFilterActive(filter)) {
    bar.appendChild(h("button", { class: "link needs-filter-clear", type: "button",
      onclick: () => { openGroup = ""; onChange(normalizeNeedsFilter({})); } },
    t("filters.clear")));
  }
  return bar;
}

// Close an open filter drop-down on an outside click or Escape.
document.addEventListener("pointerdown", (event) => {
  const open = document.querySelector(".needs-filter[open]");
  if (open && !open.contains(event.target)) { open.open = false; openGroup = ""; }
});
document.addEventListener("keydown", (event) => {
  if (event.key !== "Escape") return;
  const open = document.querySelector(".needs-filter[open]");
  if (!open) return;
  event.stopPropagation();
  open.open = false; openGroup = ""; open.querySelector("summary")?.focus();
}, true);

// ---------------------------------------------------------------- row
// One compact row per item: kind + title + age, a two-line excerpt, small
// media thumbnails, then actions.  "Comment" leaves a note the agent reads
// without changing the status.  `select` adds a leading checkbox:
// `{ checked, onChange(checked) }`.
export function needsRow(ctx, item, { select = null } = {}) {
  const kind = needsKind(item);
  // Opens the unit, at the first place the review note points to in it.
  const first = (item.review_anchors || []).find((a) => a.ok !== false && a.kind === "heading" && (!a.unit || a.unit === item.uid));
  const open = () => (first ? revealAnchor(ctx, first, item.uid) : ctx.openUnit(item.uid));
  const comment = h("button", { class: "btn sm ghost", type: "button", onclick: () => commentUnit(ctx, item) },
    icon("message", "sm"), t("needs.comment"));
  const actions = kind === "decision"
    ? [
      h("button", { class: "btn sm primary", type: "button", onclick: () => decideUnit(ctx, item) }, icon("check", "sm"), t("needs.decide")),
      comment,
      h("button", { class: "btn sm ghost", type: "button", onclick: open }, t("needs.open")),
    ]
    : [
      h("button", { class: "btn sm primary", type: "button", onclick: () => acceptUnit(ctx, item) }, icon("check", "sm"), t("needs.accept")),
      h("button", { class: "btn sm ghost", type: "button", onclick: () => sendBackUnit(ctx, item) }, icon("arrowLeft", "sm"), t("needs.send_back")),
      comment,
      h("button", { class: "btn sm ghost", type: "button", onclick: open }, t("needs.open")),
    ];
  const media = (item.media || []).filter((m) => previewKind(m));
  // Unresolved user comments: what the agent still has to act on.
  const notes = item.comments_open || 0;
  // Previews from linked nodes are labelled; the node itself has none.
  const borrowed = !item.own_media;
  const noMedia = borrowed ? h("span", { class: "needs-no-media", title: t("needs.no_media_hint") },
    icon("image", "sm"), t("needs.no_media"),
    borrowedSources(media).length ? ` · ${t("needs.previews_from_links")}` : "") : null;
  const check = select ? h("input", {
    type: "checkbox", class: "needs-select", checked: Boolean(select.checked),
    "aria-label": t("review.select_item", { uid: item.uid, title: item.title || "" }),
    onchange: (event) => select.onChange(event.target.checked),
  }) : null;
  return h("article", {
    class: `needs-row kind-${kind} ${select?.checked ? "selected" : ""}`.trim(),
    dataset: { unitRef: item.uid, needsKind: kind },
  },
    check,
    h("span", { class: `needs-chip kind-${kind}` }, t(`needs.kind.${kind}`)),
    h("div", { class: "needs-main" },
      h("div", { class: "needs-line" },
        h("button", { class: "needs-title", type: "button", onclick: open, title: `${item.uid} · ${item.title}` },
          h("span", { class: "needs-uid" }, item.uid), " ", item.title),
        assigneeChip(item),
        notes ? h("span", { class: "needs-notes", title: t("needs.comments_open", { n: notes }), dataset: { open: notes } },
          icon("message", "sm"), String(notes)) : null,
        kind === "review" && item.review_round ? h("span", { class: "needs-round", dataset: { round: String(item.review_round) } },
          t("review_req.round", { n: item.review_round })) : null,
        h("span", { class: "needs-age", title: fmtDateTime(item.review_since || item.updated_at) },
          relativeTime(item.review_since || item.updated_at))),
      kind === "review" && "review_note" in item ? reviewFocus(ctx, item, open)
        : item.excerpt ? h("div", { class: "needs-excerpt", onclick: open }, item.excerpt_plain || stripMd(item.excerpt)) : null,
      kind === "review" ? previousRoundLine(item.review_prev) : null,
      kind === "review" ? changesToggle(ctx, item) : null,
      h("div", { class: "needs-foot" },
        media.length || noMedia ? h("div", { class: "needs-media-col" },
          media.length ? needsStrip(ctx, item, media) : null,
          noMedia) : null,
        h("div", { class: "needs-actions" }, actions))));
}

export function stripMd(text) {
  return String(text || "")
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/!\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/^\s{0,3}(#{1,6}\s+|>\s?|[-*+]\s+|\d+[.)]\s+)/gm, "")
    .replace(/(\*\*|__|`|~~)/g, "")
    .replace(/\s+/g, " ").trim();
}

// Every preview, newest first, in one horizontal strip.  The mouse wheel
// scrolls it sideways; clicking a thumbnail opens the media lightbox the
// graph uses for card thumbnails (zoomable images, video player, prev/next
// across every preview of the card, borrowed ones included, in strip order).
// Images show the server's small thumbnail (thumb_url), videos their poster
// frame (poster_url) as an image: no <video> element until the lightbox
// plays one.  Without a poster, a video element loads its first frame only
// once it is on screen (a hidden or scrolled-away row loads nothing): every
// review of a project with hundreds of videos each starting a media player
// froze the page.
function needsStrip(ctx, item, media) {
  const preview = (index) => openMediaLightbox({
    items: needsLightboxItems(media, (uid) => t("needs.from", { uid })),
    index, title: item.title || item.uid || "", srcOf: mediaUrl,
  });
  const strip = h("div", { class: "needs-media" }, media.map((m, index) => needsThumb(ctx, m, index, preview)));
  strip.addEventListener("wheel", (event) => {
    if (strip.scrollWidth <= strip.clientWidth + 1) return; // nothing to scroll: page scrolls
    const delta = Math.abs(event.deltaX) > Math.abs(event.deltaY) ? event.deltaX : event.deltaY;
    if (!delta) return;
    const max = strip.scrollWidth - strip.clientWidth;
    // At either end, let the page scroll on instead of trapping the wheel.
    if ((delta < 0 && strip.scrollLeft <= 0) || (delta > 0 && strip.scrollLeft >= max - 1)) return;
    event.preventDefault();
    strip.scrollLeft = Math.max(0, Math.min(max, strip.scrollLeft + delta));
  }, { passive: false });
  for (const video of strip.querySelectorAll("video[data-src]")) observeVideo(video);
  return strip;
}

// One observer for every lazy video on the page (root: the viewport, so a
// thumbnail clipped by its strip or in a hidden row does not count).
let videoObserver = null;
function observeVideo(video) {
  if (typeof IntersectionObserver === "undefined") { loadVideo(video); return; }
  videoObserver ||= new IntersectionObserver((entries) => {
    for (const entry of entries) {
      if (!entry.isIntersecting) continue;
      videoObserver.unobserve(entry.target);
      loadVideo(entry.target);
    }
  }, { rootMargin: "200px 160px" });
  videoObserver.observe(video);
}

function loadVideo(video) {
  if (!video.dataset.src) return;
  video.src = video.dataset.src;
  video.removeAttribute("data-src");
}

/** The image a thumbnail shows: thumb_url / poster_url when the server has one. */
export function thumbSource(media, kind = previewKind(media)) {
  const own = kind === "image" ? media.thumb_url : media.poster_url;
  if (own && own !== media.raw_url) return authenticatedUrl(own);
  return kind === "image" ? mediaUrl(media) : "";
}

function needsThumb(ctx, media, index, preview) {
  const kind = previewKind(media);
  const src = mediaUrl(media);
  const label = media.caption || media.from?.title || media.kind || "";
  const from = media.from ? t("needs.from", { uid: media.from.uid }) : "";
  const title = [label, from].filter(Boolean).join(" · ");
  const still = thumbSource(media, kind);
  const lazyVideo = () => h("video", { "data-src": `${src}#t=0.1`, muted: true, playsinline: true, preload: "metadata" });
  let stage;
  if (kind === "image") {
    stage = h("img", { src: still, alt: label, loading: "lazy", decoding: "async" });
    // A thumbnail the server could not make: the original.
    if (still !== src) stage.addEventListener("error", () => { if (stage.getAttribute("src") !== src) stage.src = src; }, { once: true });
  } else {
    const poster = still ? h("img", { src: still, alt: label, loading: "lazy", decoding: "async" }) : lazyVideo();
    stage = h("span", { class: "needs-video" }, poster, h("span", { class: "needs-play", "aria-hidden": "true" }, icon("play", "sm")));
    if (still) {
      poster.addEventListener("error", () => {
        const video = lazyVideo();
        poster.replaceWith(video);
        observeVideo(video);
      }, { once: true });
    }
  }
  const thumb = h("button", {
    class: `needs-thumb kind-${kind}${media.new ? " is-new" : ""}`, type: "button",
    title: media.new ? `${title} · ${t("review_req.new_media")}` : title,
    "aria-label": kind === "video" ? `${t("needs.play_video")}: ${title}` : title,
    onclick: () => preview(index),
  }, stage, media.new ? h("span", { class: "needs-new-badge" }, t("review_req.new_badge")) : null,
  h("span", { class: "needs-thumb-cap" }, label || from));
  if (kind === "image") attachImageContextMenu(thumb, () => ({ src: mediaUrl(media), kind: "image", caption: label }));
  if (!media.from) return thumb;
  // Borrowed from a linked node: say which, and link to it.
  return h("span", { class: "needs-thumb-wrap borrowed" }, thumb,
    h("a", {
      class: "needs-from", href: `?unit=${encodeURIComponent(media.from.uid)}#/home`,
      title: t("needs.from_title", { uid: media.from.uid, title: media.from.title || "" }),
      dataset: { unitRef: media.from.uid },
      onclick: (event) => { event.preventDefault(); ctx.openUnit(media.from.uid); },
    }, from));
}

// ------------------------------------------------------------ dialogs
// Each action writes one comment row on the unit (kind comment / sent_back /
// decided / accepted) and, except a plain comment, then sets the status.
// Comments are their own entries on the node, so agents rewriting the body
// cannot lose them; agents read the unresolved ones before they resume.
async function earlierComments(item) {
  try { return (await api.unit(item.uid)).unit?.comments || []; } catch { return []; }
}

export function noteDialog(ctx, item, {
  title, action, label, kind, status, done, options = [], history = [], placeholder = "", optional = false, initial = "", onDone = null,
}) {
  const input = h("textarea", { class: "textarea", style: { minHeight: "72px" }, placeholder: placeholder || label, "aria-label": label });
  input.value = initial;
  const threads = threadComments(history).slice(-5);
  const line = (c) => h("div", { class: `needs-history-item kind-${c.kind}${c.resolved ? " resolved" : ""}` },
    h("b", null, `${t(`comments.kind.${c.kind}`)} · ${displayName(authorName(c)) || t(`comments.author.${c.kind === "agent_reply" ? "agent" : "user"}`)} · ${fmtDate(c.created_at)}`),
    " ", c.body);
  const past = threads.length ? h("div", { class: "needs-history" },
    h("div", { class: "muted" }, t("needs.previous_notes")),
    threads.map((thread) => h("div", { class: "needs-history-thread" },
      line(thread.comment),
      thread.replies.map((reply) => h("div", { class: "needs-history-reply" }, line(reply)))))) : null;
  const picks = options.length ? h("div", { class: "needs-options" }, options.map((option) => h("button", {
    class: "btn sm ghost", type: "button",
    onclick: () => { input.value = option; input.focus(); },
  }, option))) : null;
  openModal({
    title,
    body: h("div", null,
      past,
      h("div", { class: "field" }, h("label", null, label), picks, input)),
    actions: [
      { label: t("action.cancel"), ghost: true },
      {
        label: action, primary: true,
        onClick: async () => {
          const text = input.value.trim();
          if (!text && !optional) { toast(t("needs.reason_required"), "err"); input.focus(); return false; }
          try {
            if (text) await api.addComment(item.uid, { body: text, kind });
            if (status) await api.updateUnit(item.uid, { status });
            toast(t(done, { uid: item.uid }), "ok");
          } catch (err) { toast(err.message, "err"); return false; }
          onDone?.();
          refreshNeeds(ctx);
        },
      },
    ],
  });
}

// Accept closes the node; the note is optional.
export async function acceptUnit(ctx, item, { initial = "", onDone = null } = {}) {
  noteDialog(ctx, item, {
    title: t("needs.accept_title", { uid: item.uid }), action: t("needs.accept"), label: t("needs.accept_note"),
    kind: "accepted", status: "done", done: "needs.accepted", optional: true, history: await earlierComments(item),
    initial, onDone,
  });
}

export async function sendBackUnit(ctx, item, { initial = "", onDone = null } = {}) {
  noteDialog(ctx, item, {
    title: t("needs.send_back_title", { uid: item.uid }), action: t("needs.send_back"), label: t("needs.send_back_reason"),
    kind: "sent_back", status: "active", done: "needs.sent_back", history: await earlierComments(item),
    initial, onDone,
  });
}

export async function decideUnit(ctx, item) {
  let unit = null;
  try { unit = (await api.unit(item.uid)).unit; } catch { /* quick picks are optional */ }
  noteDialog(ctx, item, {
    title: t("needs.decide_title", { uid: item.uid }), action: t("needs.decide"), label: t("needs.decide_answer"),
    kind: "decided", status: "done", done: "needs.decided", options: decisionOptions(unit?.body),
    history: unit?.comments || [],
  });
}

// A comment keeps the status.
export async function commentUnit(ctx, item) {
  noteDialog(ctx, item, {
    title: t("needs.comment_title", { uid: item.uid }), action: t("needs.comment_save"), label: t("needs.comment_label"),
    kind: "comment", status: null, done: "needs.commented", history: await earlierComments(item),
    placeholder: t("needs.comment_placeholder"),
  });
}

/**
 * Applies one note (comment kind `kind`) and optionally a status to each item,
 * a few at a time, then shows a toast: `doneKey`/`failedKey` with {n, failed}.
 * With a status, the toast offers Undo, which puts each previous status back.
 * Resolves to runBatch's `{ done, failed }`.
 */
export async function applyToEach(ctx, items, { note = "", kind = "comment", status = null, doneKey, failedKey }) {
  const result = await runBatch(items, async (item) => {
    if (note) await api.addComment(item.uid, { body: note, kind });
    if (status) await api.updateUnit(item.uid, { status });
  });
  const { done, failed } = result;
  const undo = status && done.length ? {
    label: t("home.start.undo"),
    onClick: async () => {
      await runBatch(done, (item) => api.updateUnit(item.uid, { status: item.status || "review" }));
      refreshNeeds(ctx);
    },
  } : null;
  if (failed.length) toast(t(failedKey, { n: done.length, failed: failed.length }), "err", { action: undo, duration: 8000 });
  else toast(t(doneKey, { n: done.length }), "ok", { action: undo, duration: 8000 });
  refreshNeeds(ctx);
  return result;
}

// Accept several reviews at once (Home): all shown reviews start selected;
// an optional note goes to each.  Undo puts the previous statuses back.
export function bulkAcceptDialog(ctx, items) {
  const boxes = items.map((item) => h("input", { type: "checkbox", checked: true, value: item.uid }));
  const count = h("span", { class: "muted needs-bulk-count" });
  const all = h("input", { type: "checkbox", checked: true });
  const selected = () => items.filter((_, i) => boxes[i].checked);
  const sync = () => {
    const n = selected().length;
    count.textContent = t("needs.bulk_selected", { n });
    all.checked = n === items.length;
    all.indeterminate = n > 0 && n < items.length;
  };
  boxes.forEach((box) => box.addEventListener("change", sync));
  all.addEventListener("change", () => { boxes.forEach((box) => { box.checked = all.checked; }); sync(); });
  const note = h("textarea", { class: "textarea", style: { minHeight: "56px" }, placeholder: t("needs.bulk_note"), "aria-label": t("needs.bulk_note") });
  sync();
  openModal({
    title: t("needs.bulk_title"),
    body: h("div", { class: "needs-bulk-dialog" },
      h("p", { class: "muted" }, t("needs.bulk_hint")),
      h("div", { class: "needs-bulk-head" }, h("label", null, all, t("needs.bulk_all")), count),
      h("div", { class: "needs-bulk-list" }, items.map((item, i) => h("label", { class: "needs-bulk-row", title: `${item.uid} · ${item.title}` },
        boxes[i], h("span", { class: "needs-uid" }, item.uid), h("span", { class: "needs-bulk-title" }, item.title)))),
      h("div", { class: "field" }, h("label", null, t("needs.bulk_note")), note)),
    actions: [
      { label: t("action.cancel"), ghost: true },
      {
        label: t("needs.bulk_action"), primary: true,
        onClick: async () => {
          const chosen = selected();
          if (!chosen.length) { toast(t("needs.bulk_none"), "err"); return false; }
          await applyToEach(ctx, chosen, {
            note: note.value.trim(), kind: "accepted", status: "done", doneKey: "needs.bulk_done", failedKey: "needs.bulk_failed",
          });
        },
      },
    ],
  });
}

// ------------------------------------------------------ review requests
// What to look at (the review note with its anchors, else the newest part of
// the body), the previous round's feedback and the changes since the
// person last decided (server: store/reviewrounds.go).

function noteBlock(ctx, note, selfUid, anchors = []) {
  const byRaw = new Map((anchors || []).map((a) => [a.raw, a]));
  return noteParts(note).map((part) => {
    if (part.text != null) return document.createTextNode(part.text);
    const resolved = { ...part.anchor, ...(byRaw.get(part.anchor.raw) || {}) };
    return anchorChip(ctx, resolved, selfUid);
  });
}

export function anchorChip(ctx, anchor, selfUid = "") {
  const broken = anchor.ok === false;
  const label = anchorLabel(anchor, selfUid);
  return h("button", {
    class: `review-anchor kind-${anchor.kind}${broken ? " broken" : ""}`, type: "button",
    title: broken ? `${anchor.raw} · ${anchor.problem || t("review_req.anchor_missing")}` : anchor.raw,
    dataset: { anchor: anchor.raw },
    onclick: (event) => { event.stopPropagation(); revealAnchor(ctx, anchor, selfUid); },
  }, icon(anchor.kind === "media" ? (anchor.start != null ? "play" : "image") : "link", "sm"), label);
}

function reviewFocus(ctx, item, open) {
  const text = reviewCardText(item, t);
  if (text.note) {
    return h("div", { class: "needs-note", dataset: { note: "1" } },
      h("b", { class: "needs-note-label" }, text.lead),
      h("span", { class: "needs-note-text" }, noteBlock(ctx, text.note, item.uid, item.review_anchors)));
  }
  const focus = text.focus || stripMd(item.excerpt);
  return h("div", { class: "needs-note missing", dataset: { note: "0" } },
    h("span", { class: "needs-note-label" }, text.lead),
    focus ? h("span", { class: "needs-excerpt", onclick: open }, focus) : null);
}

function previousRoundLine(prev) {
  if (!prev || (!prev.outcome && !(prev.comments || []).length)) return null;
  const outcome = prev.outcome ? t(`needs.value.${prev.outcome}`) : "";
  const comments = (prev.comments || []).slice(-3);
  return h("div", { class: "needs-prev", dataset: { open: String(prev.open || 0) } },
    h("span", { class: "needs-prev-head" },
      prev.round ? t("review_req.prev_round", { n: prev.round }) : t("review_req.prev_feedback"),
      outcome ? ` · ${outcome}` : ""),
    comments.map((c) => h("span", {
      class: `needs-prev-item ${c.resolved ? "resolved" : "open"}`,
      title: `${displayName(c.author) || ""} · ${fmtDateTime(c.created_at)}`,
    }, h("i", { "aria-label": c.resolved ? t("review_req.resolved") : t("review_req.unresolved") },
      c.resolved ? "✓" : "●"), " ", stripMd(c.body))));
}

function changesLabel(changes, base) { return changesText(changes, base, t); }

function clearEl(el) { while (el.firstChild) el.firstChild.remove(); }

// A toggle under the card: loads the line diff on first open.
function changesToggle(ctx, item) {
  const sum = changeSummary(item.review_changes);
  if (!sum) return null;
  const panel = h("div", { class: "needs-diff", hidden: true });
  let loaded = false;
  const button = h("button", {
    class: `link needs-changes ${sum.empty ? "empty" : ""}`.trim(), type: "button", "aria-expanded": "false",
    disabled: sum.empty, dataset: { added: String(sum.added), removed: String(sum.removed), media: String(sum.media) },
    onclick: async () => {
      const open = panel.hidden;
      panel.hidden = !open;
      button.setAttribute("aria-expanded", String(open));
      if (!open || loaded) return;
      loaded = true;
      panel.append(h("div", { class: "loading" }, h("span", { class: "spinner" }), t("action.loading")));
      try {
        const info = await api.unitReview(item.uid);
        clearEl(panel);
        panel.append(diffView(ctx, info));
      } catch (err) {
        clearEl(panel);
        panel.append(h("div", { class: "empty sm" }, t("error.load"), " ", err.message));
        loaded = false;
      }
    },
  }, icon("clock", "sm"), changesLabel(item.review_changes, item.review_base));
  return h("div", { class: "needs-changes-wrap" }, button, panel);
}

/** The line diff and the new attachments of a review (GET .../review). */
export function diffView(ctx, info) {
  const blocks = diffBlocks(info?.diff?.hunks);
  const media = (info?.media_new || []).filter((m) => previewKind(m));
  const body = h("div", { class: "review-diff" }, blocks.map((block) => {
    if (block.op === "skip") return h("div", { class: "review-diff-skip" }, t("review_req.unchanged_lines", { n: block.n }));
    if (block.op === "add") return h("div", { class: "review-diff-add" }, richText(block.text));
    if (block.op === "del") return h("div", { class: "review-diff-del" }, block.text);
    return h("div", { class: "review-diff-same" }, block.text);
  }));
  if (!blocks.length) body.append(h("div", { class: "empty sm" }, t("review_req.no_text_changes")));
  return h("div", { class: "review-diff-wrap" }, body,
    media.length ? h("div", { class: "review-diff-media" },
      h("div", { class: "muted" }, t("review_req.new_files", { n: media.length })),
      needsStrip(ctx, { uid: info.uid, title: info.title }, media)) : null);
}

// ----------------------------------------------------- anchor navigation
// Opens the unit an anchor points to and scrolls its panel to the heading
// or {#id} block, with a short highlight; media anchors open the lightbox
// (videos at the given time, paused at the end of a range).
export async function revealAnchor(ctx, anchor, selfUid = "") {
  if (anchor.kind === "media") { openMediaAnchor(anchor); return; }
  const uid = anchorUnit(anchor, selfUid);
  if (!uid) return;
  await ctx.openUnit(uid, null, { readOnly: true });
  if (anchor.kind !== "heading" || !anchor.slug) return;
  const deadline = Date.now() + 2500;
  const find = () => [...document.querySelectorAll(".drawer [data-anchor]")].find((el) => el.dataset.anchor === anchor.slug);
  let target = find();
  while (!target && Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, 80));
    target = find();
  }
  if (!target) { toast(t("review_req.anchor_missing"), "err"); return; }
  for (let el = target.parentElement; el; el = el.parentElement) if (el.tagName === "DETAILS") el.open = true;
  target.scrollIntoView({ block: "center", behavior: "smooth" });
  target.classList.remove("anchor-flash");
  void target.offsetWidth;
  target.classList.add("anchor-flash");
  setTimeout(() => target.classList.remove("anchor-flash"), 2000);
}

function openMediaAnchor(anchor) {
  const item = anchor.media_item;
  if (!item) { toast(t("review_req.anchor_missing"), "err"); return; }
  const kind = item.preview_kind || previewKind(item) || (anchor.start != null ? "video" : "image");
  openMediaLightbox({ items: [{ ...item, kind }], index: 0, title: item.caption || item.unit || "", srcOf: mediaUrl });
  if (kind !== "video" || anchor.start == null) return;
  const seek = (tries = 0) => {
    const videos = [...document.querySelectorAll("video")].filter((v) => v.closest("[class*='lightbox']"));
    const video = videos[videos.length - 1];
    if (!video) { if (tries < 30) setTimeout(() => seek(tries + 1), 60); return; }
    const start = () => { video.currentTime = anchor.start; video.play?.()?.catch?.(() => {}); };
    if (video.readyState >= 1) start(); else video.addEventListener("loadedmetadata", start, { once: true });
    if (anchor.end != null) {
      const stop = () => {
        if (video.currentTime >= anchor.end) { video.pause(); video.removeEventListener("timeupdate", stop); }
      };
      video.addEventListener("timeupdate", stop);
    }
    video.title = `${clockLabel(anchor.start)}${anchor.end != null ? `–${clockLabel(anchor.end)}` : ""}`;
  };
  seek();
}

// ------------------------------------------------------------ cards
// The list as cards: single items, units of one delivery shown together
// (needs-model.js groupNeeds) and review groups agents submitted.
// `select` (Review page): `{ has(uid), onChange(uids, on) }`.
export function needsCards(ctx, items, { select = null } = {}) {
  return groupNeeds(items).map((entry) => needsEntryCard(ctx, entry, { select }));
}

/** The card for one groupNeeds() entry. */
export function needsEntryCard(ctx, entry, { select = null } = {}) {
  if (entry.kind === "group") return deliveryCard(ctx, entry, { select });
  if (entry.item.kind === "review_group") return reviewGroupCard(ctx, entry.item);
  return needsRow(ctx, entry.item, select ? {
    select: { checked: select.has(entry.item.uid), onChange: (on) => select.onChange([entry.item.uid], on) },
  } : {});
}

// One compact row per member: uid, the claim, its headline number, the
// independent review verdict, what changed; clicking opens the member at
// the first place its note points to.
function memberTable(ctx, members, { outcome = false } = {}) {
  return h("div", { class: "needs-members", role: "table" }, members.map((m) => {
    const anchors = m.review_anchors || m.anchors || [];
    const first = anchors.find((a) => a.ok !== false && a.kind === "heading" && (!a.unit || a.unit === m.uid));
    const open = () => (first ? revealAnchor(ctx, first, m.uid) : ctx.openUnit(m.uid));
    const changes = changeSummary(m.review_changes || m.changes);
    const number = keyNumber(m.title);
    const note = m.note || m.review_note || "";
    return h("div", { class: `needs-member${m.outcome ? ` outcome-${m.outcome}` : ""}`, role: "row", dataset: { unitRef: m.uid } },
      h("button", { class: "needs-member-open", type: "button", onclick: open, title: `${m.uid} · ${m.title}` },
        h("span", { class: "needs-uid" }, m.uid),
        outcome && m.outcome ? h("span", { class: `needs-chip outcome-${m.outcome}` }, t(`needs.value.${m.outcome}`)) : null,
        h("span", { class: "needs-member-title" }, m.title)),
      h("span", { class: "needs-member-number", title: number ? t("review_req.key_number") : undefined }, number),
      h("span", { class: "needs-member-verdict", title: m.verdict ? t("review_req.verdict") : undefined }, m.verdict || ""),
      h("span", { class: "needs-member-changes", title: changes ? changesLabel(m.review_changes || m.changes, m.review_base || m.base) : undefined },
        changes && !changes.empty ? `+${changes.added} −${changes.removed}${changes.media ? ` · ${t("review_req.new_files", { n: changes.media })}` : ""}` : ""),
      note ? h("span", { class: "needs-member-note" }, noteBlock(ctx, note, m.uid, anchors)) : null);
  }));
}

function explainLine(group) {
  const why = groupExplain(group);
  const parts = [];
  if (why.parents.length) parts.push(t("review_req.why_parent", { uid: why.parents.join(", ") }));
  if (why.linked) parts.push(t("review_req.why_linked"));
  if (why.delivery) parts.push(t("review_req.why_delivery", { name: why.delivery.replace(/^(branch|commit):/, "") }));
  else parts.push(t("review_req.why_window", { n: Math.max(1, why.minutes) }));
  return h("div", { class: "needs-group-why", title: t("review_req.why_hint") }, icon("info", "sm"), parts.join(" · "));
}

function groupCountLabel(members) {
  const types = new Set(members.map((m) => m.type));
  return types.size === 1
    ? t("review_req.n_of_type", { n: members.length, type: typeLabel([...types][0]) })
    : t("review_req.n_units", { n: members.length });
}

// Units of one delivery that each went to review on their own.
function deliveryCard(ctx, group, { select = null } = {}) {
  const members = group.members;
  const uids = members.map((m) => m.uid);
  const expanded = h("div", { class: "needs-group-rows", hidden: true });
  let built = false;
  const label = h("span", null, t("review_req.one_by_one"));
  const toggle = h("button", {
    class: "btn sm ghost needs-group-expand", type: "button", "aria-expanded": "false",
    onclick: () => {
      const open = expanded.hidden;
      expanded.hidden = !open;
      toggle.setAttribute("aria-expanded", String(open));
      label.textContent = open ? t("review_req.collapse") : t("review_req.one_by_one");
      if (open && !built) {
        built = true;
        expanded.append(...members.map((m) => needsRow(ctx, m, select ? {
          select: { checked: select.has(m.uid), onChange: (on) => select.onChange([m.uid], on) },
        } : {})));
      }
    },
  }, icon("list", "sm"), label);
  const all = select ? h("input", {
    type: "checkbox", class: "needs-select", checked: uids.every((uid) => select.has(uid)),
    "aria-label": t("review.select_item", { uid: group.title, title: groupCountLabel(members) }),
    onchange: (event) => select.onChange(uids, event.target.checked),
  }) : null;
  if (all) all.indeterminate = !all.checked && uids.some((uid) => select.has(uid));
  const newest = members.reduce((max, m) => ((m.review_since || m.updated_at || "") > max ? (m.review_since || m.updated_at) : max), "");
  return h("article", {
    class: "needs-row needs-group kind-review", dataset: { unitRef: group.primary.uid, needsKind: "review", group: group.key },
  },
  all,
  h("span", { class: "needs-chip kind-review" }, t("review_req.delivery")),
  h("div", { class: "needs-main" },
    h("div", { class: "needs-line" },
      h("button", {
        class: "needs-title", type: "button", title: group.parent ? `${group.parent.uid} · ${group.parent.title}` : group.title,
        onclick: () => ctx.openUnit(group.parent?.uid || group.primary.uid),
      }, group.title, " ", h("span", { class: "needs-group-count" }, `· ${groupCountLabel(members)}`)),
      h("span", { class: "needs-age", title: fmtDateTime(newest) }, relativeTime(newest))),
    explainLine(group),
    memberTable(ctx, members),
    h("div", { class: "needs-foot" },
      h("div", { class: "needs-actions" },
        h("button", { class: "btn sm primary", type: "button", onclick: () => groupDecideDialog(ctx, { title: group.title, members, accept: true }) },
          icon("check", "sm"), t("review_req.accept_all")),
        h("button", { class: "btn sm ghost", type: "button", onclick: () => groupDecideDialog(ctx, { title: group.title, members, accept: false }) },
          icon("arrowLeft", "sm"), t("needs.send_back")),
        toggle)),
    expanded));
}

// Accept / send back several members (a fallback group): the checked ones
// take the action; the others stay as they are.
function groupDecideDialog(ctx, { title, members, accept }) {
  const boxes = members.map((m) => h("input", { type: "checkbox", checked: true, value: m.uid }));
  const label = accept ? t("needs.bulk_note") : t("review.reason_each");
  const note = h("textarea", { class: "textarea", style: { minHeight: "56px" }, placeholder: label, "aria-label": label });
  openModal({
    title: `${accept ? t("review_req.accept_all") : t("needs.send_back")} · ${title}`,
    body: h("div", { class: "needs-bulk-dialog" },
      h("p", { class: "muted" }, accept ? t("review_req.accept_all_hint") : t("review_req.send_back_hint")),
      h("div", { class: "needs-bulk-list" }, members.map((m, i) => h("label", { class: "needs-bulk-row", title: `${m.uid} · ${m.title}` },
        boxes[i], h("span", { class: "needs-uid" }, m.uid), h("span", { class: "needs-bulk-title" }, m.title)))),
      h("div", { class: "field" }, h("label", null, label), note)),
    actions: [
      { label: t("action.cancel"), ghost: true },
      {
        label: accept ? t("needs.bulk_action") : t("needs.send_back"), primary: true,
        onClick: async () => {
          const chosen = members.filter((_, i) => boxes[i].checked);
          const text = note.value.trim();
          if (!chosen.length) { toast(t("needs.bulk_none"), "err"); return false; }
          if (!accept && !text) { toast(t("needs.reason_required"), "err"); note.focus(); return false; }
          await applyToEach(ctx, chosen, accept
            ? { note: text, kind: "accepted", status: "done", doneKey: "needs.bulk_done", failedKey: "needs.bulk_failed" }
            : { note: text, kind: "sent_back", status: "active", doneKey: "review.sent_back_n", failedKey: "review.sent_back_failed" });
        },
      },
    ],
  });
}

// A review group an agent submitted (store/reviewgroups.go).
function reviewGroupCard(ctx, group) {
  const members = group.members || [];
  const pending = members.filter((m) => !m.outcome);
  const comments = (group.comments || []).filter((c) => c.kind !== "agent_reply");
  return h("article", {
    class: "needs-row needs-group review-group kind-review", dataset: { unitRef: group.id, needsKind: "review", reviewGroup: group.id },
  },
  h("span", { class: "needs-chip kind-review" }, t("review_req.group")),
  h("div", { class: "needs-main" },
    h("div", { class: "needs-line" },
      h("span", { class: "needs-title", title: group.title }, group.title, " ",
        h("span", { class: "needs-group-count" }, `· ${groupCountLabel(members)}`)),
      group.comments_open ? h("span", { class: "needs-notes", title: t("needs.comments_open", { n: group.comments_open }) },
        icon("message", "sm"), String(group.comments_open)) : null,
      h("span", { class: "needs-round" }, t("review_req.round", { n: group.round || 1 })),
      h("span", { class: "needs-age", title: fmtDateTime(group.opened_at) }, relativeTime(group.opened_at))),
    group.summary ? h("div", { class: "needs-group-summary" }, stripMd(group.summary)) : null,
    group.review_note
      ? h("div", { class: "needs-note" }, h("b", { class: "needs-note-label" }, t("review_req.look_here")),
        h("span", { class: "needs-note-text" }, noteBlock(ctx, group.review_note, "", group.anchors)))
      : h("div", { class: "needs-note missing" }, h("span", { class: "needs-note-label" }, t("review_req.no_note"))),
    comments.length ? h("div", { class: "needs-prev" }, comments.slice(-3).map((c) => h("span", {
      class: `needs-prev-item ${c.resolved ? "resolved" : "open"}`, title: `${displayName(c.author) || ""} · ${fmtDateTime(c.created_at)}`,
    }, h("i", null, c.resolved ? "✓" : "●"), " ", stripMd(c.body)))) : null,
    memberTable(ctx, members, { outcome: true }),
    h("div", { class: "needs-foot" },
      h("div", { class: "needs-actions" },
        h("button", { class: "btn sm primary", type: "button", disabled: !pending.length,
          onclick: () => reviewGroupDialog(ctx, group, true) }, icon("check", "sm"), t("needs.accept")),
        h("button", { class: "btn sm ghost", type: "button", disabled: !pending.length,
          onclick: () => reviewGroupDialog(ctx, group, false) }, icon("arrowLeft", "sm"), t("needs.send_back")),
        h("button", { class: "btn sm ghost", type: "button", onclick: () => reviewGroupComment(ctx, group) },
          icon("message", "sm"), t("needs.comment"))))));
}

// Accept: checked members are accepted, unchecked ones sent back (with the
// note).  Send back: every pending member goes back.
function reviewGroupDialog(ctx, group, accept) {
  const pending = (group.members || []).filter((m) => !m.outcome);
  const boxes = pending.map((m) => h("input", { type: "checkbox", checked: true, value: m.uid }));
  const label = accept ? t("needs.accept_note") : t("needs.send_back_reason");
  const note = h("textarea", { class: "textarea", style: { minHeight: "56px" }, placeholder: label, "aria-label": label });
  openModal({
    title: `${accept ? t("needs.accept") : t("needs.send_back")} · ${group.title}`,
    body: h("div", { class: "needs-bulk-dialog" },
      h("p", { class: "muted" }, accept ? t("review_req.group_accept_hint") : t("review_req.group_send_back_hint")),
      accept ? h("div", { class: "needs-bulk-list" }, pending.map((m, i) => h("label", { class: "needs-bulk-row", title: `${m.uid} · ${m.title}` },
        boxes[i], h("span", { class: "needs-uid" }, m.uid), h("span", { class: "needs-bulk-title" }, m.title)))) : null,
      h("div", { class: "field" }, h("label", null, label), note)),
    actions: [
      { label: t("action.cancel"), ghost: true },
      {
        label: accept ? t("needs.accept") : t("needs.send_back"), primary: true,
        onClick: async () => {
          const text = note.value.trim();
          const back = accept ? pending.filter((_, i) => !boxes[i].checked) : pending;
          if (back.length && !text) { toast(t("review_req.reason_for_back"), "err"); note.focus(); return false; }
          const members = {};
          back.forEach((m) => { members[m.uid] = "send_back"; });
          try {
            const out = await api.decideReviewGroup(group.id, { action: accept ? "accept" : "send_back", note: text, members });
            toast(out?.closed ? t("review_req.group_closed", { title: group.title }) : t("review_req.group_decided", { title: group.title }), "ok");
          } catch (err) { toast(err.message, "err"); return false; }
          refreshNeeds(ctx);
        },
      },
    ],
  });
}

function reviewGroupComment(ctx, group) {
  const input = h("textarea", { class: "textarea", style: { minHeight: "72px" }, placeholder: t("needs.comment_placeholder"), "aria-label": t("needs.comment_label") });
  const past = (group.comments || []).slice(-5);
  openModal({
    title: t("review_req.group_comment_title", { title: group.title }),
    body: h("div", null,
      past.length ? h("div", { class: "needs-history" }, past.map((c) => h("div", { class: `needs-history-item kind-${c.kind}${c.resolved ? " resolved" : ""}` },
        h("b", null, `${displayName(c.author) || c.author} · ${fmtDate(c.created_at)}`), " ", c.body))) : null,
      h("div", { class: "field" }, h("label", null, t("needs.comment_label")), input)),
    actions: [
      { label: t("action.cancel"), ghost: true },
      {
        label: t("needs.comment_save"), primary: true,
        onClick: async () => {
          const text = input.value.trim();
          if (!text) { toast(t("needs.reason_required"), "err"); input.focus(); return false; }
          try {
            await api.reviewGroupComment(group.id, { body: text });
            toast(t("needs.commented", { uid: group.id }), "ok");
          } catch (err) { toast(err.message, "err"); return false; }
          refreshNeeds(ctx);
        },
      },
    ],
  });
}

// ------------------------------------------------------- unit panel
// The inspector's review section for a unit in review: round, note with
// anchors, previous round, changes since the last review, and the note's
// editor (a person can say what to look at too).
// Research › Review with the group's card in view.
async function revealGroupCard(ctx, id) {
  ctx.navigateWorkspace?.("research", "review");
  for (let i = 0; i < 40; i++) {
    const card = document.querySelector(`.review-list [data-review-group="${CSS.escape(id)}"]`);
    if (card) {
      card.scrollIntoView({ block: "center", behavior: "smooth" });
      card.classList.add("anchor-flash");
      card.dataset.anchor = id;
      setTimeout(() => card.classList.remove("anchor-flash"), 2000);
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
}

export function reviewPanel(ctx, unit) {
  if (unit?.status !== "review" && !(unit?.review_groups || []).length) return null;
  const host = h("section", { class: "read-section review-panel", dataset: { unit: unit.uid } },
    h("h4", null, t("review_req.panel_title")),
    h("div", { class: "loading" }, h("span", { class: "spinner" }), t("action.loading")));
  api.unitReview(unit.uid).then((info) => {
    if (!info.in_review) {
      if (!(info.groups || []).length) { host.remove(); return; }
      host.replaceChildren(h("h4", null, t("review_req.panel_title")));
      info.groups.forEach((g) => host.append(h("div", { class: "review-panel-group" },
        h("button", { class: "link", type: "button", dataset: { reviewGroup: g.id }, onclick: () => revealGroupCard(ctx, g.id) },
          t("review_req.in_group", { title: g.title, id: g.id })))));
      return;
    }
    host.replaceChildren(h("h4", null, t("review_req.panel_title"),
      info.round ? h("span", { class: "needs-round" }, t("review_req.round", { n: info.round })) : null));
    host.append(info.note
      ? h("div", { class: "needs-note" }, h("b", { class: "needs-note-label" }, t("review_req.look_here")),
        h("span", { class: "needs-note-text" }, noteBlock(ctx, info.note, unit.uid, info.anchors)))
      : h("div", { class: "needs-note missing" }, h("span", { class: "needs-note-label" }, t("review_req.no_note"))));
    host.append(h("button", { class: "link review-note-edit", type: "button", onclick: () => editNoteDialog(ctx, unit, info) },
      t("review_req.edit_note")));
    const prev = previousRoundLine(info.previous);
    if (prev) host.append(prev);
    (info.groups || []).forEach((g) => host.append(h("div", { class: "review-panel-group" },
      h("button", {
        class: "link", type: "button", dataset: { reviewGroup: g.id },
        onclick: () => revealGroupCard(ctx, g.id),
      }, t("review_req.in_group", { title: g.title, id: g.id })))));
    if (info.diff) {
      const changes = { added: info.diff.added, removed: info.diff.removed, media: (info.media_new || []).length };
      const sum = changeSummary(changes);
      const panel = diffView(ctx, info);
      panel.hidden = true;
      const button = h("button", {
        class: "link needs-changes", type: "button", "aria-expanded": "false", disabled: sum.empty,
        onclick: () => { panel.hidden = !panel.hidden; button.setAttribute("aria-expanded", String(!panel.hidden)); },
      }, icon("clock", "sm"), changesLabel(changes, info.base));
      host.append(h("div", { class: "needs-changes-wrap" }, button, panel));
    }
  }).catch(() => host.remove());
  return host;
}

function editNoteDialog(ctx, unit, info) {
  const input = h("textarea", { class: "textarea", style: { minHeight: "72px" }, maxlength: 500,
    placeholder: t("review_req.note_placeholder"), "aria-label": t("review_req.look_here") });
  input.value = info.note || "";
  openModal({
    title: t("review_req.edit_note_title", { uid: unit.uid }),
    body: h("div", null, h("p", { class: "muted" }, t("review_req.note_hint")), input),
    actions: [
      { label: t("action.cancel"), ghost: true },
      {
        label: t("action.save"), primary: true,
        onClick: async () => {
          try {
            const out = await api.updateUnit(unit.uid, { review_note: input.value.trim() });
            const warnings = out?.review_warnings || [];
            if (warnings.length) toast(t("review_req.anchor_warnings", { n: warnings.length, first: warnings[0].anchor }), "err", { duration: 6000 });
            else toast(t("review_req.note_saved"), "ok");
          } catch (err) { toast(err.message, "err"); return false; }
          refreshNeeds(ctx);
          ctx.openUnit?.(unit.uid, null, { readOnly: true });
        },
      },
    ],
  });
}
