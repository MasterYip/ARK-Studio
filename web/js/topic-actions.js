// Topic actions shared by every graph, the inspector and the filter rail:
// create a topic, add nodes to one, save the current filters as one.
import { h, clear, typeColor } from "./dom.js";
import { api } from "./api.js";
import { t, typeLabel, statusLabel, relLabel } from "./i18n.js";
import { icon } from "./icons.js";
import { ruleParts } from "./topic-model.js";
import { openModal } from "./components.js";

function topicRow(topic, chosen, onPick) {
  return h("button", {
    type: "button",
    class: `topic-pick-row ${chosen ? "on" : ""}`.trim(),
    onclick: () => onPick(topic),
  },
    h("i", { class: "topic-pick-dot", style: { background: typeColor("topic") } }),
    h("span", { class: "topic-pick-title" }, topic.title),
    h("span", { class: "uid" }, topic.uid),
    h("span", { class: "muted" }, t("topic.members_n", { n: topic.member_count ?? 0 })));
}

/** Create a topic (optionally seeded with members/design) and open it. */
export async function createTopic(ctx, { title, body = "", members = [], design = null } = {}) {
  const payload = { type: "topic", title, body, members };
  if (design) payload.design = design;
  const created = await api.createUnit(payload);
  ctx.toast?.(t("topic.created", { title: created.unit.title }), "ok");
  ctx.openTopic?.(created.unit.uid);
  return created.unit;
}

/** Ask for a title, then create a topic.  Resolves to the unit or null. */
export function openNewTopic(ctx, { members = [], design = null, hint = "" } = {}) {
  return new Promise((resolve) => {
    const title = h("input", { class: "input", placeholder: t("topic.title_placeholder"), maxlength: 240 });
    const body = h("textarea", { class: "input", rows: 3, placeholder: t("topic.body_placeholder") });
    openModal({
      title: t("topic.new"),
      body: h("div", { class: "stack", style: { gap: "10px" } },
        hint ? h("p", { class: "muted", style: { margin: 0 } }, hint) : null,
        h("label", { class: "field" }, h("span", null, t("common.title")), title),
        h("label", { class: "field" }, h("span", null, t("topic.body_label")), body)),
      actions: [
        { label: t("action.cancel"), ghost: true, onClick: () => resolve(null) },
        {
          label: t("topic.create"), primary: true,
          onClick: async () => {
            const text = title.value.trim();
            if (!text) { title.focus(); return false; }
            try {
              resolve(await createTopic(ctx, { title: text, body: body.value, members, design }));
            } catch (err) {
              ctx.toast?.(err.message, "err");
              return false;
            }
            return true;
          },
        },
      ],
      onClose: () => resolve(null),
    });
    setTimeout(() => title.focus(), 40);
  });
}

/** Add nodes to an existing topic, or to a new one made from them. */
export async function openAddToTopic(ctx, nodes = []) {
  const units = (nodes || []).filter((node) => node && node.id != null);
  if (!units.length) return;
  const list = h("div", { class: "topic-pick-list" }, h("div", { class: "muted" }, t("topic.loading")));
  const search = h("input", { class: "input", placeholder: t("topic.search") });
  let topics = [];
  let chosen = null;
  let modalAction = null;
  const paint = () => {
    clear(list);
    const q = search.value.trim().toLowerCase();
    const ids = new Set(units.map((unit) => unit.id));
    const shown = topics.filter((topic) => !ids.has(topic.id)
      && (!q || `${topic.title} ${topic.uid}`.toLowerCase().includes(q)));
    if (!shown.length) list.appendChild(h("div", { class: "muted" }, t("topic.none_yet")));
    for (const topic of shown) {
      list.appendChild(topicRow(topic, chosen?.id === topic.id, (pick) => {
        chosen = pick;
        if (modalAction?._btn) modalAction._btn.disabled = false;
        paint();
      }));
    }
  };
  search.addEventListener("input", paint);
  modalAction = {
    label: t("topic.add_to_selected"), primary: true, disabled: true,
    onClick: async () => {
      if (!chosen) return false;
      try {
        const result = await api.addTopicMembers(chosen.id, units.map((unit) => unit.id));
        const added = result.report?.added?.length || 0;
        ctx.toast?.(t("topic.added", { n: added, title: chosen.title }), "ok");
        ctx.refresh?.();
      } catch (err) {
        ctx.toast?.(err.message, "err");
        return false;
      }
      return true;
    },
  };
  openModal({
    title: units.length === 1 ? t("topic.add_one", { title: units[0].title }) : t("topic.add_many", { n: units.length }),
    body: h("div", { class: "stack", style: { gap: "10px" } }, search, list),
    actions: [
      {
        label: t("topic.new_from_selection"), ghost: true,
        onClick: () => { openNewTopic(ctx, { members: units.map((unit) => unit.id) }); },
      },
      { spacer: true },
      { label: t("action.cancel"), ghost: true },
      modalAction,
    ],
  });
  setTimeout(() => search.focus(), 40);
  try {
    topics = (await api.topics()).items || [];
  } catch (err) {
    topics = [];
    ctx.toast?.(err.message, "err");
  }
  paint();
}

/** Graph context-menu entries for topics (see GraphView opts.nodeMenuExtras). */
export function topicMenuItems(ctx, nodes = [], node = null) {
  const items = [];
  if (node?.type === "topic" && nodes.length <= 1) {
    items.push({ label: t("topic.open"), icon: "layers", onClick: () => ctx.openTopic?.(node.uid || node.id) });
  }
  if (nodes.length) {
    items.push({ label: t("topic.add_to"), icon: "plus", onClick: () => openAddToTopic(ctx, nodes) });
  }
  return items;
}

/** Filter rail: keep the current filters as a topic's rule. */
export function saveFiltersAsTopic(ctx, rule) {
  if (!rule) {
    ctx.toast?.(t("topic.rule_empty"), "err");
    return;
  }
  openNewTopic(ctx, { design: { rule }, hint: t("topic.from_filters_hint") });
}

/** Edit a topic rule (a saved unit filter).  `onSave(rule | null)`. */
export function openRuleEditor(ctx, rule, onSave) {
  const S = ctx.S;
  const current = rule || {};
  const q = h("input", { class: "input", value: current.q || "", placeholder: t("topic.rule.q_placeholder") });
  const chipGroup = (values, selected, label) => {
    const set = new Set(selected || []);
    const wrap = h("div", { class: "topic-chip-group" });
    for (const [value, text] of values) {
      wrap.append(h("button", {
        type: "button", class: `chip ${set.has(value) ? "on" : ""}`.trim(), dataset: { value },
        onclick: (event) => {
          if (set.has(value)) set.delete(value); else set.add(value);
          event.currentTarget.classList.toggle("on", set.has(value));
        },
      }, text));
    }
    return { el: h("div", { class: "field" }, h("span", null, label), wrap), values: () => [...set] };
  };
  const types = chipGroup((S.vocab?.types || []).map((type) => [type.id, typeLabel(type.id)]), current.types, t("topic.rule.types"));
  const statuses = chipGroup((S.vocab?.statuses || []).map((status) => [status.id, statusLabel(status.id)]), current.statuses, t("topic.rule.statuses"));
  const tags = h("input", { class: "input", value: (current.tags || []).join(", "), placeholder: "topic:control, venue:icra" }); // i18n-ignore: tag syntax example
  const roots = h("input", { type: "checkbox", checked: Boolean(current.roots) });
  openModal({
    title: t("topic.rule.title"),
    wide: true,
    body: h("div", { class: "stack", style: { gap: "12px" } },
      h("p", { class: "muted", style: { margin: 0 } }, t("topic.rule.hint")),
      h("label", { class: "field" }, h("span", null, t("topic.rule.q")), q),
      types.el, statuses.el,
      h("label", { class: "field" }, h("span", null, t("topic.rule.tags")), tags),
      h("label", { class: "row", style: { gap: "8px" } }, roots, t("topic.rule.roots"))),
    actions: [
      rule ? { label: t("topic.rule.clear"), ghost: true, onClick: () => onSave(null) } : null,
      { spacer: true },
      { label: t("action.cancel"), ghost: true },
      {
        label: t("action.save"), primary: true,
        onClick: () => {
          const next = {
            ...current,
            q: q.value.trim(),
            types: types.values(),
            statuses: statuses.values(),
            tags: tags.value.split(",").map((tag) => tag.trim()).filter(Boolean),
            roots: roots.checked,
          };
          for (const key of Object.keys(next)) {
            if (next[key] === "" || next[key] === false || (Array.isArray(next[key]) && !next[key].length)) delete next[key];
          }
          return onSave(Object.keys(next).length ? next : null);
        },
      },
    ].filter(Boolean),
  });
  setTimeout(() => q.focus(), 40);
}

/**
 * The inspector's settings for a topic node: its rule and which links it
 * shows.  The topic page reads the same settings, so it re-renders after a
 * change here.
 */
export function topicSettings(ctx, unit) {
  const settings = unit.meta?.subgraph || {};
  const rule = settings.rule || null;
  const hidden = (settings.hidden_links || []).length;
  const relations = Array.isArray(settings.relations) ? settings.relations : null;
  const save = async (body) => {
    try {
      await api.designTopic(unit.id, body);
      ctx.toast?.(t("toast.saved"), "ok");
      // On the Topics page only the topic area reloads.
      if (ctx.topicPage?.mounted()) ctx.topicPage.reload();
      else ctx.refresh?.();
      await ctx.reloadDetail?.();
    } catch (err) {
      ctx.toast?.(err.message, "err");
    }
  };
  const linksMode = Boolean(ctx.S.topicLinksMode) && ctx.S.topicRef === unit.uid;
  const addNodes = async () => {
    try {
      const data = await api.topic(unit.uid);
      openMemberPicker(ctx, data, (ids) => save({ add: ids }));
    } catch (err) {
      ctx.toast?.(err.message, "err");
    }
  };
  return h("div", { class: "topic-settings" },
    h("div", { class: "topic-setting" },
      h("span", { class: "topic-setting-label" }, icon("layers", "sm"), t("topic.settings.nodes")),
      h("div", { class: "topic-setting-value" }, t("topic.members_n", { n: unit.member_count ?? 0 })),
      h("div", { class: "topic-setting-actions" },
        h("button", { class: "btn sm primary", type: "button", "data-open-topic": unit.uid,
          onclick: () => ctx.openTopic?.(unit.uid) }, t("topic.open")),
        h("button", { class: "btn sm ghost", type: "button", "data-topic-add-nodes": "1", onclick: addNodes },
          icon("plus", "sm"), t("topic.add_nodes")))),
    h("div", { class: "topic-setting" },
      h("span", { class: "topic-setting-label" }, icon("filter", "sm"), t("topic.settings.filter")),
      h("div", { class: "topic-setting-value" },
        rule
          ? ruleParts(rule).map(([key, value]) => h("span", { class: "pill" }, value ? `${t(key)}: ${value}` : t(key)))
          : h("span", { class: "muted" }, t("topic.rule.none"))),
      h("div", { class: "topic-setting-actions" },
        h("button", { class: "btn sm ghost", type: "button", "data-topic-rule": "1",
          onclick: () => openRuleEditor(ctx, rule, (next) => save({ rule: next })) }, t("action.edit")),
        rule ? h("button", { class: "btn sm ghost", type: "button", onclick: () => save({ rule: null }) }, t("topic.rule.clear")) : null)),
    h("div", { class: "topic-setting" },
      h("span", { class: "topic-setting-label" }, icon("link", "sm"), t("topic.settings.links")),
      h("div", { class: "topic-setting-value" },
        relations
          ? relations.map((relation) => h("span", { class: "pill" }, relLabel(relation)))
          : h("span", { class: "muted" }, t("topic.links_all")),
        hidden ? h("span", { class: "muted" }, t("topic.hidden_links", { n: hidden })) : null),
      h("div", { class: "topic-setting-actions" },
        h("button", {
          class: `btn sm ${linksMode ? "primary" : "ghost"}`.trim(), type: "button", "data-topic-links": "1",
          "aria-pressed": linksMode ? "true" : "false", title: t("topic.links_mode_hint"),
          onclick: () => {
            ctx.S.topicLinksMode = !linksMode;
            if (ctx.S.topicRef !== unit.uid || !ctx.topicPage?.mounted()) ctx.openTopic?.(unit.uid);
            else ctx.topicPage.setView();
            ctx.reloadDetail?.();
          },
        }, t("topic.links_mode")),
        relations || hidden ? h("button", { class: "btn sm ghost", type: "button",
          onclick: () => save({ relations: null, hidden_links: [] }) }, t("topic.links_reset")) : null)));
}

/** Search the project and add nodes to a topic.  `data` is the topic view. */
export async function openMemberPicker(ctx, data, onAdd) {
  const inTopic = new Set((data.members || []).map((unit) => unit.id));
  const chosen = new Map();
  const results = h("div", { class: "topic-pick-list" });
  const search = h("input", { class: "input", placeholder: t("topic.search_nodes") });
  const addBtn = { label: t("topic.add_n", { n: 0 }), primary: true, disabled: true, onClick: () => onAdd([...chosen.keys()]) };
  const sync = () => {
    if (!addBtn._btn) return;
    addBtn._btn.disabled = !chosen.size;
    addBtn._btn.textContent = t("topic.add_n", { n: chosen.size });
  };
  let seq = 0;
  const run = async () => {
    const mine = ++seq;
    let items = [];
    try {
      items = (await api.units({ q: search.value.trim(), limit: 40, facets: 0, sort: "updated" })).items || [];
    } catch (err) {
      ctx.toast(err.message, "err");
    }
    if (mine !== seq) return;
    clear(results);
    const shown = items.filter((unit) => unit.id !== data.topic.id && !inTopic.has(unit.id));
    if (!shown.length) results.append(h("div", { class: "muted" }, t("common.no_results")));
    for (const unit of shown) {
      const box = h("input", { type: "checkbox", checked: chosen.has(unit.id) });
      box.addEventListener("change", () => {
        if (box.checked) chosen.set(unit.id, unit); else chosen.delete(unit.id);
        sync();
      });
      results.append(h("label", { class: "topic-pick-row" }, box,
        h("i", { class: "topic-pick-dot", style: { background: typeColor(unit.type) } }),
        h("span", { class: "topic-pick-title" }, unit.title),
        h("span", { class: "uid" }, unit.uid),
        h("span", { class: "muted" }, typeLabel(unit.type))));
    }
  };
  let timer = null;
  search.addEventListener("input", () => { clearTimeout(timer); timer = setTimeout(run, 200); });
  openModal({
    title: t("topic.add_nodes_title", { title: data.topic.title }),
    wide: true,
    body: h("div", { class: "stack", style: { gap: "10px" } },
      h("p", { class: "muted", style: { margin: 0 } }, t("topic.add_nodes_hint")), search, results),
    actions: [{ label: t("action.cancel"), ghost: true }, addBtn],
  });
  setTimeout(() => search.focus(), 40);
  run();
}
