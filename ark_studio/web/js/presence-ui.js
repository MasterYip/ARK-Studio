// Agent presence in the UI around the graphs (T-0043): the overlay store
// adapter for GraphView, the "Agents active" chip of the Map and the
// "Being worked on by" line of the node details.
import { h, clear } from "./dom.js";
import { t } from "./i18n.js";
import { centreOnUnit, createPresenceOverlay, presenceNodeInView } from "./graph-presence.js";
import { presenceAgents, presenceEnabled, presenceForUnit, presenceModel, subscribePresence } from "./presence.js";

export const presenceStore = {
  model: presenceModel,
  subscribe: subscribePresence,
  enabled: presenceEnabled,
};

/** The overlay for one GraphView (graph.js _build). */
export function attachPresenceOverlay(view) {
  return createPresenceOverlay(view, presenceStore);
}

/** "writing" / "reading" / "working" / the action, translated. */
export function presenceActionLabel(entry) {
  const key = `presence.action.${entry.action}`;
  const text = t(key);
  if (text !== key) return text;
  return t(`presence.state.${entry.state}`);
}

export function presenceAgo(entry, now = Date.now()) {
  const seconds = Math.max(0, Math.round((now - entry.at) / 1000));
  if (seconds < 10) return t("presence.ago.now");
  if (seconds < 60) return t("presence.ago.s", { n: seconds });
  return t("presence.ago.min", { n: Math.round(seconds / 60) });
}

function dot(entry) {
  return h("span", { class: `presence-dot presence-${entry.state}`, style: { "--presence-color": entry.color } }, entry.initials);
}

/**
 * The Map's "Agents active: N" chip.  Clicking lists the agents; an agent
 * centres the view on its node and opens it.
 * @param {object} view  the GraphView
 */
export function createPresenceChip(view, { local = false } = {}) {
  const count = h("span", { class: "presence-chip-dots" });
  const label = h("span", { class: "presence-chip-label" });
  const list = h("div", { class: "presence-list hidden", role: "menu", "aria-label": t("presence.list_title") });
  const chip = h("button", {
    class: "presence-chip hidden", type: "button", "aria-haspopup": "menu", "aria-expanded": "false",
    onclick: (event) => { event.stopPropagation(); toggle(); },
  }, count, label);
  const wrap = h("div", { class: "presence-chip-wrap" }, list, chip);
  let open = false;

  const toggle = (force = null) => {
    open = force === null ? !open : force;
    list.classList.toggle("hidden", !open);
    chip.setAttribute("aria-expanded", String(open));
    if (open) renderList();
  };
  const outside = (event) => { if (open && !wrap.contains(event.target)) toggle(false); };
  document.addEventListener("pointerdown", outside, true);

  const activeInView = () => presenceAgents().flatMap((agent) => {
    const entry = local ? agent.entries.find((activity) => presenceNodeInView(view, activity.unit)) : agent.entries[0];
    return entry ? [{ ...agent, entry }] : [];
  });
  const renderList = (agents = activeInView()) => {
    clear(list);
    const now = Date.now();
    for (const agent of agents) {
      const entry = agent.entry;
      const node = presenceNodeInView(view, entry.unit);
      const where = entry.uid ? `${entry.uid} · ${entry.title || node?.title || ""}` : (entry.title || node?.title || "");
      list.appendChild(h("button", {
        class: "presence-row", type: "button", role: "menuitem", disabled: node ? undefined : true,
        title: node ? t("presence.centre") : t("presence.not_here"),
        onclick: () => {
          const shown = centreOnUnit(view, entry.unit);
          if (shown) { view.setSelected?.(shown.id); view.opts?.onSelect?.(shown, false); }
          toggle(false);
        },
      },
      dot(entry),
      h("span", { class: "presence-row-text" },
        h("b", null, agent.label),
        h("span", { class: "presence-row-where" }, where.trim().replace(/·\s*$/, "")),
        h("span", { class: "presence-row-meta" }, `${presenceActionLabel(entry)} · ${presenceAgo(entry, now)}`))));
    }
  };

  const update = () => {
    const agents = activeInView();
    chip.classList.toggle("hidden", !agents.length);
    if (!agents.length && open) toggle(false);
    clear(count);
    for (const agent of agents.slice(0, 4)) count.appendChild(dot(agent.entry));
    label.textContent = t("presence.active", { n: agents.length });
    chip.setAttribute("aria-label", label.textContent);
    chip.title = agents.map((agent) => agent.label).join(", ");
    if (open) renderList(agents);
  };
  const unsubscribe = subscribePresence(update);
  update();
  return {
    el: wrap, update,
    destroy() { unsubscribe(); document.removeEventListener("pointerdown", outside, true); wrap.remove(); },
  };
}

/**
 * The node details line "Being worked on by <agent> · <action> · <time>",
 * live while the element is in the page.
 */
export function presenceDetailsLine(unit) {
  const el = h("div", { class: "presence-details hidden", "aria-live": "polite" });
  let timer = 0;
  let armed = false;
  const render = () => {
    // Stops once the panel that showed it is gone (it is in the page from
    // the next task on).
    if (!el.isConnected) { if (armed) stop(); return; }
    const entries = presenceForUnit(unit);
    el.classList.toggle("hidden", !entries.length);
    clear(el);
    const now = Date.now();
    for (const entry of entries.slice(0, 3)) {
      el.appendChild(h("div", { class: "presence-details-row" },
        dot(entry),
        h("span", null, t("presence.worked_on_by", { agent: entry.label }),
          ` · ${presenceActionLabel(entry)} · ${presenceAgo(entry, now)}`,
          entry.note ? h("span", { class: "presence-note" }, ` · ${entry.note}`) : null)));
    }
    if (entries.length && !timer) timer = setInterval(render, 5000);
    if (!entries.length && timer) { clearInterval(timer); timer = 0; }
  };
  const unsubscribe = subscribePresence(render);
  const stop = () => { unsubscribe(); if (timer) clearInterval(timer); timer = 0; };
  render();
  setTimeout(() => { armed = true; render(); }, 0);
  return el;
}
