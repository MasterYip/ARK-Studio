// Source control / review (formerly Execute › Files › Review): the project's
// git changes against a base ref (HEAD, HEAD~1, a branch …) with +/− stats.
// Clicking a file opens its diff as an Editor tab.  It is the workbench
// "Changes" view.

import { h, clear } from "./dom.js";
import { t, localizeServer } from "./i18n.js";
import { icon } from "./icons.js";
import { api, cachedThenFresh, qs } from "./api.js";
import { diffTotalLabel, fileChangeLabel, fileChangeClass, normalizeBaseRef } from "./files-model.js";

const BASE_KEY = "ark.changes.base.v1";

function storedBase() {
  try { return normalizeBaseRef(localStorage.getItem(BASE_KEY)); } catch { return "HEAD"; }
}

export function createChangesPanel({ onOpenDiff, onCount } = {}) {
  let base = storedBase();
  let changes = { for: "", loading: false, error: "", data: null };
  let info = null;
  let pending = false;
  let activePath = "";
  const root = h("div", { class: "wb-changes" });

  async function loadInfo() {
    try { info = await api.workspaceInfo(); } catch { info = null; }
    paint();
  }

  async function load(force = false) {
    if (changes.loading) { if (force) pending = true; return; }
    if (!force && changes.for === base && changes.data) return;
    changes = { for: base, loading: true, error: "", data: changes.data };
    paint();
    try {
      // The last list for this base shows at once; the current one follows.
      const want = base;
      await cachedThenFresh(`/api/workspace/changes${qs({ base: want })}`,
        (options) => api.workspaceChanges(want, options), (data, { cached }) => {
          if (base !== want) return;
          changes = { for: want, loading: cached, error: "", data };
          if (cached) { onCount?.(data?.files?.length || 0); paint(); }
        });
    } catch (err) {
      changes = { for: base, loading: false, error: err.message, data: null };
    }
    onCount?.(changes.data?.files?.length || 0);
    paint();
    if (pending) { pending = false; load(true); }
  }

  function applyBase(value) {
    const next = normalizeBaseRef(value);
    if (next === base) return;
    base = next;
    try { localStorage.setItem(BASE_KEY, base); } catch { /* storage disabled */ }
    changes = { for: "", loading: false, error: "", data: null };
    load(true);
  }

  function paint() {
    clear(root);
    const git = info?.git || null;
    const branch = git?.branch || "";
    const baseInput = h("input", {
      class: "input fw-base-input", type: "text", value: base, spellcheck: false,
      "aria-label": t("files.base"), title: t("files.base_hint"), list: "wb-base-refs",
    });
    baseInput.addEventListener("keydown", (event) => {
      if (event.key === "Enter") { event.preventDefault(); applyBase(baseInput.value); }
    });
    root.append(
      h("div", { class: "wb-changes-head" },
        git?.is_repo
          ? h("span", { class: "fw-git-badge", title: git.root || "" }, icon("git", "sm"),
            branch || t("files.git_detached"), git.head ? `@${git.head}` : "")
          : h("span", { class: "fw-git-badge muted", title: localizeServer(git?.reason || "") }, icon("git", "sm"), t("files.no_git_badge")),
        h("span", { class: "spacer" }),
        changes.data ? h("span", { class: "fw-muted" }, diffTotalLabel(changes.data.totals, { t })) : null,
        h("button", { class: "icon-btn", type: "button", title: t("action.refresh"), "aria-label": t("action.refresh"), onclick: () => { loadInfo(); load(true); } },
          icon("refresh", "sm"))),
      h("label", { class: "fw-base wb-changes-base" }, h("span", null, t("files.base")), baseInput,
        h("button", { class: "btn sm ghost", type: "button", onclick: () => applyBase(baseInput.value) }, t("files.base_apply")),
        h("datalist", { id: "wb-base-refs" },
          ["HEAD", "HEAD~1", "HEAD~2", branch].filter(Boolean).map((ref) => h("option", { value: ref })))), // i18n-ignore: git refs
    );
    const list = h("div", { class: "fw-side-scroll wb-changes-list" });
    if (changes.loading && !changes.data) {
      list.appendChild(h("div", { class: "fw-tree-loading" }, h("span", { class: "spinner" }), t("action.loading")));
    } else if (changes.error) {
      list.appendChild(h("div", { class: "fw-tree-error" }, icon("alert", "sm"), changes.error));
    } else if (changes.data?.git === false) {
      list.appendChild(h("div", { class: "fw-side-empty" },
        h("b", null, t("files.no_git_title")), h("div", null, localizeServer(changes.data.reason) || t("files.no_git_hint"))));
    } else if (!changes.data?.files?.length) {
      list.appendChild(h("div", { class: "fw-side-empty" }, t("files.no_changes", { base })));
    } else {
      list.appendChild(h("div", { class: "fw-review-hint" }, t("files.review_hint")));
      for (const item of changes.data.files) {
        list.appendChild(h("button", {
          class: `fw-review-row ${item.path === activePath ? "on" : ""} ${fileChangeClass(item.status)}`.trim(),
          type: "button", title: `${item.path} · ${fileChangeLabel(item.status, { t })}`,
          dataset: { path: item.path },
          onclick: () => { activePath = item.path; paint(); onOpenDiff?.(item.path, base); },
        },
          h("span", { class: `fw-change-badge ${fileChangeClass(item.status)}` }, item.status || "M"),
          h("span", { class: "fw-review-path" }, item.path),
          h("span", { class: "fw-review-stat" },
            item.binary ? t("files.binary_short") : item.additions === null ? "±" : `+${item.additions ?? 0} −${item.deletions ?? 0}`)));
      }
      if (changes.data.truncated) list.appendChild(h("div", { class: "fw-tree-empty" }, t("files.changes_truncated")));
    }
    root.append(list, h("div", { class: "fw-side-foot" },
      h("span", { class: "mono" }, t("files.base_label", { ref: base })), h("span", { class: "spacer" }),
      changes.data?.files?.length ? h("span", { class: "mono" }, t("files.n_files", { n: changes.data.files.length })) : null));
  }

  paint();
  return {
    el: root,
    // First show (and every later show) refreshes the list.
    shown() {
      if (!info) loadInfo();
      load(true);
    },
    count: () => changes.data?.files?.length || 0,
    refresh: () => load(true),
  };
}
