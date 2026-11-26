// A tree of tag toggles (line tags "a/b/c"; model: tag-tree-model.js).
// One row per tag: fold chevron, checkbox, the last part of the path (the
// whole path as tooltip) and the count.  A parent row switches its subtree
// and shows a mixed checkbox while only part of it is on.  Used by the
// filter rail's line section (components.js) and Needs you's line filter
// (needs-ui.js).

import { h } from "./dom.js";
import { icon } from "./icons.js";
import { t } from "./i18n.js";
import { initialFolded, loadFolded, nodeState, saveFolded, toggleFold, toggleTarget, visibleRows } from "./tag-tree-model.js";

/**
 * tagTree({ roots, isOn, onToggle, foldKey, all, extra, rowMenu, prefix })
 *   roots      buildTagTree(...)
 *   isOn       (path) => whether the value is on
 *   onToggle   (paths, on, item) => switch these values on or off
 *   foldKey    where the folded rows are remembered (tag-tree-model.js)
 *   all        { label, checked, indeterminate, onChange(on) }: the row on top
 *   extra      [{ key, label, title, n, on, toggle() }]: plain rows below the tree
 *   rowMenu    (row element, item) => attach a context menu (optional)
 *   prefix     shown before the path in the tooltip ("branch:")
 */
export function tagTree({ roots = [], isOn, onToggle, foldKey = "", all = null, extra = [], rowMenu = null, prefix = "" }) {
  let folded = initialFolded(roots, foldKey ? loadFolded(foldKey) : null);
  const host = h("div", { class: "tag-tree", role: "tree" });

  const checkbox = (state, label, onChange, focus) => {
    const input = h("input", { type: "checkbox", checked: state === "on", "aria-label": label,
      dataset: { focusKey: `tree:${foldKey}:${focus}` }, onchange: (event) => onChange(event.target.checked) });
    input.indeterminate = state === "mixed";
    return input;
  };

  const row = (item) => {
    const state = nodeState(item, isOn);
    const parent = item.children.length > 0;
    const open = parent && !folded.has(item.path);
    const fold = parent ? h("button", {
      type: "button", class: "tag-tree-fold", "aria-label": t(open ? "files.collapse" : "files.expand"),
      title: t(open ? "files.collapse" : "files.expand"), "aria-expanded": String(open),
      dataset: { focusKey: `tree-fold:${foldKey}:${item.path}` },
      onclick: (event) => {
        event.preventDefault();
        folded = toggleFold(folded, item.path);
        if (foldKey) saveFolded(foldKey, folded);
        paint();
        [...host.querySelectorAll(".tag-tree-fold")].find((button) => button.closest(".tag-tree-row")?.dataset.path === item.path)?.focus();
      },
    }, icon("chevronRight", "sm")) : h("span", { class: "tag-tree-fold-gap", "aria-hidden": "true" });
    const toggle = () => {
      const { paths, on } = toggleTarget(item, isOn);
      if (paths.length) onToggle(paths, on, item);
    };
    const el = h("div", {
      class: `tag-tree-row ${state !== "off" ? "on" : ""} ${parent ? "parent" : ""}`.replace(/\s+/g, " ").trim(),
      role: "treeitem", "aria-level": String(item.depth + 1), "aria-expanded": parent ? String(open) : undefined,
      style: { "--depth": item.depth }, dataset: { path: item.path },
    },
    fold,
    h("label", { class: "tag-tree-label", title: `${prefix}${item.path}` },
      checkbox(state, item.path, toggle, `p:${item.path}`),
      h("span", { class: "tag-tree-name" }, item.label)),
    h("span", { class: "n" }, String(item.total)));
    rowMenu?.(el, item);
    return el;
  };

  const paint = () => {
    const rows = [];
    if (all) {
      rows.push(h("div", { class: "tag-tree-row tag-tree-all", style: { "--depth": 0 } },
        h("span", { class: "tag-tree-fold-gap", "aria-hidden": "true" }),
        h("label", { class: "tag-tree-label" },
          checkbox(all.checked ? "on" : all.indeterminate ? "mixed" : "off", all.label, (on) => all.onChange(on), "all"),
          h("span", { class: "tag-tree-name" }, all.label))));
    }
    for (const item of visibleRows(roots, folded)) rows.push(row(item));
    for (const item of extra) {
      rows.push(h("div", { class: `tag-tree-row tag-tree-extra ${item.on ? "on" : ""}`.trim(), style: { "--depth": 0 }, dataset: { path: item.key } },
        h("span", { class: "tag-tree-fold-gap", "aria-hidden": "true" }),
        h("label", { class: "tag-tree-label", title: item.title || item.label },
          checkbox(item.on ? "on" : "off", item.label, () => item.toggle(), `x:${item.key}`),
          h("span", { class: "tag-tree-name" }, item.label)),
        item.n == null ? null : h("span", { class: "n" }, String(item.n))));
    }
    host.replaceChildren(...rows);
  };
  paint();
  return host;
}
