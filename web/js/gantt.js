import { h, clear, typeColor, statusColor, fmtDate, todayISO, daysBetween, typeBadge, statusPill, dueState } from "./dom.js";
import { t, typeLabel, statusLabel } from "./i18n.js";
import { icon } from "./icons.js";
import { ganttSpan } from "./node-model.js";

const DAY = 86_400_000;

function parseDate(value) {
  return new Date(`${String(value).slice(0, 10)}T00:00:00`);
}

function isoDate(d) {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

export class GanttView {
  constructor(host, { items = [], groupBy = "type", onSelect = null, zoom = 1 } = {}) {
    this.host = host;
    this.items = items;
    this.groupBy = groupBy;
    this.onSelect = onSelect;
    this.zoom = zoom;
    this._build();
    this.render();
  }

  _build() {
    clear(this.host);
    this.panel = h("div", { class: "gantt-panel" });
    this.scroll = h("div", { class: "gantt-scroll" });
    this.chart = h("div", { class: "gantt-chart" });
    this.scroll.appendChild(this.chart);
    this.panel.appendChild(this.scroll);
    this.host.appendChild(this.panel);
  }

  setData(items, groupBy = this.groupBy) {
    this.items = items;
    this.groupBy = groupBy;
    this.render();
  }

  setGroup(groupBy) {
    this.groupBy = groupBy;
    this.render();
  }

  setZoom(zoom) {
    this.zoom = zoom;
    this.render();
  }

  render() {
    clear(this.chart);
    this.legend?.remove();
    this.legend = null;
    const now = todayISO();
    const items = (this.items || []).filter((u) => ganttSpan(u, now));
    if (!items.length) {
      this.chart.appendChild(h("div", { class: "empty", style: { margin: "16px" } }, t("gantt.no_dates")));
      return;
    }
    let min = null, max = null;
    for (const item of items) {
      const span = ganttSpan(item, now);
      const start = parseDate(span.start);
      const end = parseDate(span.end);
      if (!min || start < min) min = start;
      if (!max || end > max) max = end;
    }
    const today = parseDate(todayISO());
    if (today >= min && today <= max) { /* visible */ }
    min = new Date(min.getTime() - 3 * DAY);
    max = new Date(max.getTime() + 4 * DAY);
    const totalDays = Math.max(7, Math.round((max - min) / DAY));
    const dayW = Math.max(16, 22 * this.zoom);
    const width = totalDays * dayW;
    const grid = `210px ${width}px`;

    // Axis
    const axis = h("div", { class: "gantt-axis", style: { gridTemplateColumns: grid } });
    axis.appendChild(h("div", { class: "gantt-axis-label" }, `${items.length} ${t("common.results")}`));
    const scale = h("div", { class: "gantt-scale", style: { width: `${width}px` } });
    const monthRow = h("div", { class: "gantt-scale-row month" });
    const dayRow = h("div", { class: "gantt-scale-row day" });
    let lastMonth = null;
    const step = totalDays > 70 ? 7 : totalDays > 35 ? 3 : totalDays > 18 ? 2 : 1;
    for (let i = 0; i < totalDays; i++) {
      const d = new Date(min.getTime() + i * DAY);
      const left = i * dayW;
      const month = new Intl.DateTimeFormat(undefined, { month: "short", year: "2-digit" }).format(d);
      if (month !== lastMonth) {
        lastMonth = month;
        monthRow.appendChild(h("span", { class: "gantt-tick", style: { left: `${left}px`, width: `${dayW * 7}px` } }, month));
      }
      if (i % step === 0) {
        const weekend = d.getDay() === 0 || d.getDay() === 6;
        const isToday = isoDate(d) === todayISO();
        dayRow.appendChild(h("span", {
          class: `gantt-tick ${weekend ? "weekend" : ""} ${isToday ? "today" : ""}`.trim(),
          style: { left: `${left}px`, width: `${dayW * step}px` },
          title: isoDate(d),
        }, step === 1 ? `${d.getDate()}` : `${d.getMonth() + 1}/${d.getDate()}`));
      }
    }
    scale.append(monthRow, dayRow);
    axis.appendChild(scale);
    this.chart.appendChild(axis);

    // Group rows.
    const groups = new Map();
    for (const item of items) {
      let key;
      if (this.groupBy === "status") key = item.status;
      else if (this.groupBy === "agent") key = item.assignees?.[0]?.title || t("common.unassigned");
      else key = item.type;
      if (!groups.has(key)) groups.set(key, []);
      groups.get(key).push(item);
    }
    const typeOrder = ["task", "milestone", "experiment", "outreach", "story", "production", "artifact", "claim", "evidence"];
    const sortedGroups = [...groups.entries()].sort((a, b) => {
      const ai = typeOrder.indexOf(a[0]); const bi = typeOrder.indexOf(b[0]);
      return (ai < 0 ? 99 : ai) - (bi < 0 ? 99 : bi) || String(a[0]).localeCompare(String(b[0]));
    });
    for (const [group, groupItems] of sortedGroups) {
      const header = h("div", { class: "gantt-row head", style: { gridTemplateColumns: grid } });
      const color = this.groupBy === "status" ? statusColor(group) : this.groupBy === "agent" ? "#facc15" : typeColor(group);
      header.append(
        h("div", { class: "gantt-row-label", style: { color: "var(--sub)", fontWeight: "700", fontSize: "10px", letterSpacing: ".05em", textTransform: "uppercase" } },
          h("span", { class: "g-type", style: { background: color } }),
          this.groupBy === "status" ? statusLabel(group) : this.groupBy === "agent" ? group : typeLabel(group),
          h("span", { class: "n", style: { marginLeft: "auto", color: "var(--mute)" } }, groupItems.length)),
        h("div"));
      this.chart.appendChild(header);
      groupItems.sort((a, b) => (a.start_at || a.end_at || "").localeCompare(b.start_at || b.end_at || "") || a.title.localeCompare(b.title));
      for (const item of groupItems) this.chart.appendChild(this._row(item, min, totalDays, dayW, grid));
    }

    // Today line.
    const todayOffset = Math.round((today - min) / DAY);
    if (todayOffset >= 0 && todayOffset <= totalDays) {
      this.chart.appendChild(h("div", {
        class: "gantt-today-line",
        style: { left: `calc(210px + ${todayOffset * dayW}px)`, top: "50px" },
      }));
      this.chart.appendChild(h("div", {
        class: "gantt-today-label",
        style: { left: `calc(210px + ${todayOffset * dayW}px)`, top: "31px" },
      }, t("gantt.today")));
    }

    this.legend = h("div", { class: "gantt-legend" },
      [["task", typeColor("task")], ["milestone", typeColor("milestone")], ["experiment", typeColor("experiment")], ["outreach", typeColor("outreach")]]
        .map(([type, color]) => h("span", null, h("i", { style: { background: color } }), typeLabel(type))),
      h("span", { style: { marginLeft: "auto" } }, h("i", { style: { background: "transparent", border: "1px solid var(--red)", transform: "rotate(45deg)" } }), t("gantt.milestone")));
    this.panel.appendChild(this.legend);
    if (!this._scrolled) {
      this._scrolled = true;
      const todayIdx = Math.round((today - min) / DAY);
      if (todayIdx > 0) {
        requestAnimationFrame(() => { this.scroll.scrollLeft = Math.max(0, todayIdx * dayW - this.scroll.clientWidth * 0.35); });
      }
    }
  }

  _row(item, min, totalDays, dayW, grid) {
    const row = h("div", { class: "gantt-row", style: { gridTemplateColumns: grid }, dataset: { unitRef: item.uid || "" } });
    const label = h("div", { class: "gantt-row-label", title: `${item.uid} · ${item.title}` },
      h("span", { class: "g-type", style: { background: typeColor(item.type) } }),
      h("span", { style: { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" } }, item.title),
      h("span", { class: "g-uid" }, item.uid));
    const track = h("div", { class: "gantt-track", style: { width: `${totalDays * dayW}px` } });
    const span = ganttSpan(item, todayISO());
    const start = parseDate(span.start);
    const startIdx = Math.max(0, Math.round((start - min) / DAY));
    const isPoint = span.point;
    const left = startIdx * dayW;
    if (isPoint) {
      const point = h("button", {
        class: "gantt-point",
        style: { left: `${left}px`, background: typeColor(item.type), borderColor: "var(--lane)" },
        title: `${item.title} · ${fmtDate(item.start_at || item.end_at)}`,
        onclick: () => this.onSelect?.(item),
      });
      track.appendChild(point);
    } else {
      const end = parseDate(span.end);
      const days = Math.max(1, Math.round((end - start) / DAY) + 1);
      const bar = h("button", {
        class: "gantt-bar",
        style: {
          left: `${left}px`, width: `${Math.max(18, days * dayW - 2)}px`,
          background: item.status === "done" ? `color-mix(in srgb, ${typeColor(item.type)} 55%, #0f141c)` : typeColor(item.type),
          opacity: item.status === "done" ? "0.78" : "1",
          ...(dueState(item) === "overdue" ? { boxShadow: "0 0 0 1px var(--red)" } : {}),
          // Started, no end date yet: the bar fades out at today.
          ...(span.ongoing ? { maskImage: "linear-gradient(to right, #000 70%, transparent)",
            WebkitMaskImage: "linear-gradient(to right, #000 70%, transparent)" } : {}),
        },
        title: `${item.title}\n${item.start_at || "?"} → ${span.ongoing ? t("gantt.ongoing") : (item.end_at || "?")}`,
        onclick: () => this.onSelect?.(item),
      }, item.title);
      track.appendChild(bar);
    }
    row.append(label, track);
    return row;
  }
}

export function ganttControls(host, view, { group = "type", zoom = 1, onGroup = null, onZoom = null } = {}) {
  const groupSel = h("select", {
    class: "select", style: { width: "132px", height: "29px" }, value: group,
    onchange: (e) => { onGroup?.(e.target.value); view?.setGroup(e.target.value); },
  },
    h("option", { value: "type" }, `${t("gantt.group")}: ${t("gantt.group.type")}`),
    h("option", { value: "status" }, `${t("gantt.group")}: ${t("gantt.group.status")}`),
    h("option", { value: "agent" }, `${t("gantt.group")}: ${t("gantt.group.agent")}`));
  const zoomOut = h("button", { class: "btn sm ghost", title: t("graph.zoom_out"), onclick: () => { const z = Math.max(0.6, view.zoom - 0.2); view.setZoom(z); onZoom?.(z); } }, "−");
  const zoomIn = h("button", { class: "btn sm ghost", title: t("graph.zoom_in"), onclick: () => { const z = Math.min(2.2, view.zoom + 0.2); view.setZoom(z); onZoom?.(z); } }, "+");
  return [groupSel, zoomOut, zoomIn];
}
