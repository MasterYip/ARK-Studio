// Research › Review (#/research/review): everything that needs you on one
// page, with the Home panel's filters, a checkbox per row and actions on the
// selection; History (#/research/review/history) lists the nodes that needed
// you before and what came of them.  Rows, filters and dialogs are shared
// with Home (needs-ui.js); the pure parts are in needs-model.js.
import { h, clear, fmtDateTime, relativeTime } from "../dom.js";
import { t, typeLabel } from "../i18n.js";
import { icon } from "../icons.js";
import { openModal, toast } from "../components.js";
import { api } from "../api.js";
import {
  filterNeeds, needsFacets, needsFilterActive, needsHistoryTags, needsOutcomeTags, needsValueOf, normalizeNeedsFilter,
  pruneSelection, reviewView, selectAll, selectedItems, selectionState, sortMineFirst, splitForReview, unitItems, groupNeeds,
} from "../needs-model.js";
import {
  NEEDS_CHANGED, applyToEach, closeNeedsFilters, needsEntryCard, needsFilterBar, needsFilterFor, needsProjectKey, saveNeedsFilter,
  watchNeedsSeen,
} from "../needs-ui.js";
import { currentUsername } from "../users.js";
import { assigneeChip } from "../assignee.js";
import { fillProgressively } from "../progressive-list.js";

// The selection survives redraws (a filter change, an accept elsewhere) but
// only for items still listed; it is per project.
let selection = { project: "", uids: new Set() };
let mounted = null; // { host, ctx } of the page on screen

export function renderReview(host, ctx) {
  mounted = { host, ctx };
  shownRows = 0;
  draw(host, ctx);
}

function draw(host, ctx) {
  const view = reviewView(ctx.S.reviewView);
  const projectKey = needsProjectKey(ctx);
  if (selection.project !== projectKey) selection = { project: projectKey, uids: new Set() };
  clear(host);
  const all = ctx.S.needsYou || [];
  const tab = (id, label, n) => h("button", {
    class: `seg-btn ${view === id ? "on" : ""}`.trim(), type: "button", dataset: { reviewView: id },
    "aria-pressed": String(view === id),
    onclick: () => { if (view !== id) ctx.navigateWorkspace("research", id === "history" ? "review/history" : "review"); },
  }, label, n == null ? null : h("span", { class: "review-tab-n" }, String(n)));
  const page = h("div", { class: "page review-page", dataset: { view } },
    h("div", { class: "page-head" },
      h("div", null,
        h("div", { class: "eyebrow" }, t("nav.research")),
        h("h1", { class: "page-title" }, t("review.title")),
        h("p", { class: "page-sub" }, view === "history" ? t("review.history_sub") : t("needs.hint"))),
      h("div", { class: "head-actions" },
        h("div", { class: "segmented review-switch", role: "group", "aria-label": t("review.title") },
          tab("waiting", t("needs.title"), all.length),
          tab("history", t("review.history"))))));
  host.appendChild(page);
  if (view === "history") historyView(page, ctx);
  else waitingView(page, ctx, all, projectKey);
}

// Redraw after an action here or on another page of this tab.
document.addEventListener(NEEDS_CHANGED, () => {
  if (!mounted?.host.isConnected) { mounted = null; return; }
  if (reviewView(mounted.ctx.S.reviewView) !== "waiting") return;
  const y = window.scrollY;
  draw(mounted.host, mounted.ctx);
  window.scrollTo({ top: y });
});

// ------------------------------------------------------------- waiting
function waitingView(page, ctx, all, projectKey) {
  const sorted = sortMineFirst(all, currentUsername());
  const filter = needsFilterFor(ctx, projectKey);
  const items = filterNeeds(sorted, filter);
  // Review groups are decided on their own card, not through the selection.
  const units = unitItems(items);
  // Rows hidden by a filter leave the selection: actions apply to what is shown.
  selection.uids = pruneSelection(selection.uids, units);
  const redraw = () => draw(mounted?.host || page.parentElement, ctx);
  const setFilter = (next) => { saveNeedsFilter(ctx, projectKey, next); redraw(); };
  if (!sorted.length) {
    page.appendChild(h("div", { class: "empty review-empty" },
      icon("check"), h("b", null, t("needs.empty")), h("span", { class: "muted" }, t("review.empty_hint")),
      h("button", { class: "link", type: "button", onclick: () => ctx.navigateWorkspace("research", "review/history") },
        t("review.show_history"))));
    return;
  }
  const state = selectionState(selection.uids, units);
  const allBox = h("input", {
    type: "checkbox", class: "review-select-all", checked: state.all, disabled: !items.length,
    "aria-label": t("review.select_all_n", { n: items.length }),
    onchange: (event) => {
      selection.uids = selectAll(selection.uids, units, event.target.checked);
      redraw();
      document.querySelector(".review-select-all")?.focus();
    },
  });
  allBox.indeterminate = state.some;
  page.appendChild(h("div", { class: "review-toolbar" },
    h("label", { class: "review-all" }, allBox, h("span", null, t("needs.bulk_all"))),
    h("span", { class: "muted review-shown", dataset: { shown: String(items.length), total: String(sorted.length) } },
      needsFilterActive(filter) ? t("needs.filter.shown", { n: items.length, total: sorted.length }) : t("review.count", { n: items.length })),
    needsFilterBar(needsFacets(sorted), filter, setFilter)));
  const chosen = selectedItems(selection.uids, units);
  if (chosen.length) page.appendChild(bulkBar(ctx, chosen, redraw));
  if (!items.length) {
    page.appendChild(h("div", { class: "empty review-empty" }, t("needs.filter.empty"),
      h("button", { class: "link needs-filter-clear", type: "button",
        onclick: () => { closeNeedsFilters(); setFilter(normalizeNeedsFilter({})); } }, t("filters.clear"))));
    return;
  }
  // Built as the list is scrolled (progressive-list.js); a redraw keeps as
  // many rows as were shown.
  const list = h("div", { class: "needs-list review-list" });
  page.appendChild(list);
  stopRows?.();
  // One card per item, per delivery of several units, per review group.
  stopRows = fillProgressively(list, groupNeeds(items), (entry) => needsEntryCard(ctx, entry, {
    select: {
      has: (uid) => selection.uids.has(uid),
      onChange: (uids, on) => {
        for (const uid of uids) if (on) selection.uids.add(uid); else selection.uids.delete(uid);
        redraw();
        // The page is redrawn: keep focus on the box that was toggled.
        document.querySelector(`.review-list .needs-row[data-unit-ref="${CSS.escape(uids[0])}"] .needs-select`)?.focus();
      },
    },
  }), { initial: REVIEW_ROWS, chunk: REVIEW_ROWS, previous: shownRows, onCount: (n) => { shownRows = n; } });
  watchNeedsSeen(ctx, list, list, groupNeeds(items), { filtered: needsFilterActive(filter) });
}

const REVIEW_ROWS = 25;
let shownRows = 0;
let stopRows = null;

// Sticky bar over the list while something is selected.
function bulkBar(ctx, chosen, redraw) {
  const { reviews, decisions } = splitForReview(chosen);
  const clearSelection = () => { selection.uids = new Set(); redraw(); };
  const skipped = decisions.length && reviews.length
    ? h("span", { class: "review-skip", dataset: { skipped: String(decisions.length) } },
      t("review.skip_decisions", { n: decisions.length }))
    : null;
  const onlyDecisions = !reviews.length;
  return h("div", { class: "review-bulk", role: "region", "aria-label": t("review.selection") },
    h("b", { class: "review-bulk-n", "aria-live": "polite" }, t("needs.bulk_selected", { n: chosen.length })),
    skipped,
    h("span", { class: "review-bulk-actions" },
      h("button", {
        class: "btn sm primary", type: "button", disabled: onlyDecisions, dataset: { bulk: "accept" },
        title: onlyDecisions ? t("review.only_decisions") : undefined,
        onclick: () => acceptSelected(ctx, reviews, decisions, clearSelection),
      }, icon("check", "sm"), t("needs.accept")),
      h("button", {
        class: "btn sm", type: "button", disabled: onlyDecisions, dataset: { bulk: "send-back" },
        title: onlyDecisions ? t("review.only_decisions") : undefined,
        onclick: () => sendBackSelected(ctx, reviews, decisions, clearSelection),
      }, icon("arrowLeft", "sm"), t("needs.send_back")),
      h("button", {
        class: "btn sm", type: "button", dataset: { bulk: "comment" },
        onclick: () => commentSelected(ctx, chosen, clearSelection),
      }, icon("message", "sm"), t("needs.comment")),
      h("button", { class: "link review-clear", type: "button", dataset: { bulk: "clear" }, onclick: clearSelection },
        t("review.clear"))));
}

// One dialog for Accept / Send back / Comment on the selection.
function selectionDialog({ title, hint, label, required, action, onRun }) {
  const input = h("textarea", { class: "textarea", style: { minHeight: "72px" }, placeholder: label, "aria-label": label });
  openModal({
    title,
    body: h("div", { class: "review-dialog" },
      hint ? h("p", { class: "muted" }, hint) : null,
      h("div", { class: "field" }, h("label", null, label), input)),
    actions: [
      { label: t("action.cancel"), ghost: true },
      {
        label: action, primary: true,
        onClick: async () => {
          const text = input.value.trim();
          if (required && !text) { toast(t("needs.reason_required"), "err"); input.focus(); return false; }
          await onRun(text);
        },
      },
    ],
  });
  setTimeout(() => input.focus(), 30);
}

function skipHint(decisions) {
  return decisions.length ? t("review.skip_decisions", { n: decisions.length }) : "";
}

function acceptSelected(ctx, reviews, decisions, done) {
  selectionDialog({
    title: t("review.accept_title", { n: reviews.length }), hint: skipHint(decisions),
    label: t("needs.bulk_note"), required: false, action: t("needs.bulk_action"),
    onRun: async (note) => {
      done();
      await applyToEach(ctx, reviews, { note, kind: "accepted", status: "done", doneKey: "needs.bulk_done", failedKey: "needs.bulk_failed" });
    },
  });
}

function sendBackSelected(ctx, reviews, decisions, done) {
  selectionDialog({
    title: t("review.send_back_title", { n: reviews.length }), hint: skipHint(decisions),
    label: t("review.reason_each"), required: true, action: t("needs.send_back"),
    onRun: async (note) => {
      done();
      await applyToEach(ctx, reviews, { note, kind: "sent_back", status: "active", doneKey: "review.sent_back_n", failedKey: "review.sent_back_failed" });
    },
  });
}

function commentSelected(ctx, items, done) {
  selectionDialog({
    title: t("review.comment_title", { n: items.length }), hint: "",
    label: t("review.comment_each"), required: true, action: t("needs.comment_save"),
    onRun: async (note) => {
      done();
      await applyToEach(ctx, items, { note, kind: "comment", doneKey: "review.commented_n", failedKey: "review.comment_failed" });
    },
  });
}

// ------------------------------------------------------------- history
// Nodes whose needs: tag records an outcome (accepted, sent back, decided,
// dismissed), newest first.  The same set, with the ones still waiting, is
// one click away on the Map.
const HISTORY_LIMIT = 200;

function historyView(page, ctx) {
  const openMap = h("button", { class: "link review-open-map", type: "button",
    onclick: () => ctx.navigate("map", null, { tags: needsHistoryTags() }) }, icon("map", "sm"), t("review.open_map"));
  const list = h("div", { class: "review-history" }, h("div", { class: "loading" }, h("span", { class: "spinner" }), t("action.loading")));
  page.appendChild(h("section", { class: "panel review-history-panel" },
    h("div", { class: "panel-head" }, h("h3", null, t("review.history")), openMap),
    list));
  api.units({ tags: needsOutcomeTags(), limit: HISTORY_LIMIT, slim: 1, sort: "updated" }).then((data) => {
    if (!list.isConnected) return;
    const units = data?.items || data?.units || [];
    clear(list);
    if (!units.length) {
      list.appendChild(h("div", { class: "empty sm" }, t("review.history_empty")));
      return;
    }
    list.append(...units.map((unit) => historyRow(ctx, unit)));
    const total = Number(data?.total || units.length);
    if (total > units.length) {
      list.appendChild(h("div", { class: "muted review-history-more" }, t("review.history_more", { n: units.length, total })));
    }
  }).catch((err) => {
    if (!list.isConnected) return;
    clear(list);
    list.appendChild(h("div", { class: "empty sm" }, t("error.load"), " ", err.message));
  });
}

function historyRow(ctx, unit) {
  const value = needsValueOf(unit.tags);
  return h("div", { class: "review-history-row", dataset: { unitRef: unit.uid, needs: value } },
    value ? h("span", { class: `needs-chip outcome-${value}` }, t(`needs.value.${value}`)) : null,
    h("button", { class: "needs-title", type: "button", title: `${unit.uid} · ${unit.title}`, onclick: () => ctx.openUnit(unit.uid) },
      h("span", { class: "needs-uid" }, unit.uid), " ", unit.title),
    h("span", { class: "review-history-type muted" }, typeLabel(unit.type)),
    assigneeChip(unit),
    h("span", { class: "needs-age", title: fmtDateTime(unit.updated_at) }, relativeTime(unit.updated_at)));
}
