import { h, clear, emptyState, typeColor, statusPill, tagChip, typeBadge, fmtDate, relativeTime, richText } from "../dom.js";
import { t, typeLabel, nsLabel, statusLabel } from "../i18n.js";
import { icon } from "../icons.js";
import { openImageLightbox } from "../image-viewer.js";
import { unitCard, openModal } from "../components.js";
import { api, mediaUrl } from "../api.js";

export function renderProduction(host, ctx) {
  const S = ctx.S;
  clear(host);
  const units = S.units || [];
  const production = units.filter((u) => u.type === "production");
  const assets = units.filter((u) => ["production", "artifact"].includes(u.type));
  const colors = collectTags(units, "color");
  const fonts = collectTags(units, "font");
  const roles = new Map();
  for (const unit of production) {
    const role = (unit.tags || []).find((x) => x.startsWith("role:"));
    const key = role ? role.split(":")[1] : "general";
    roles.set(key, (roles.get(key) || 0) + 1);
  }

  const paletteBody = colors.length
    ? h("div", { class: "palette" }, colors.map((color) => h("div", {
      class: "swatch", style: { background: color.value }, title: color.tag,
    }, color.value)))
    : h("div", { class: "empty sm" }, t("production.no_palette"));

  const fontBody = fonts.length
    ? h("div", { class: "font-list" }, fonts.map((font) => h("div", { class: "font-row" },
      h("span", { class: "sample", style: { fontFamily: font.value.replaceAll("-", " ") } }, "Aa"), // i18n-ignore: font sample glyphs
      h("span", null, font.value.replaceAll("-", " ")),
      h("span", { class: "muted", style: { fontSize: "9px" } }, font.tag))))
    : h("div", { class: "empty sm" }, t("common.none"));

  const roleBody = h("div", { class: "role-grid" }, [...roles.entries()].map(([role, n]) =>
    h("div", { class: "role-tile" }, h("div", { class: "k" }, role), h("div", { class: "v" }, n))));

  const assetBody = assets.length ? assetGrid(ctx, assets) : h("div", { class: "empty sm" }, t("production.empty"));

  const recent = h("div", null);
  for (const unit of production.slice(0, 6)) {
    recent.appendChild(h("div", { class: "action-item", dataset: { unitRef: unit.uid }, onclick: () => ctx.openUnit(unit.uid) },
      h("span", { class: "ic" }, icon("palette", "sm")),
      h("div", { class: "txt" },
        h("div", { class: "t" }, unit.title),
        h("div", { class: "d" }, `${statusLabel(unit.status)} · ${relativeTime(unit.updated_at)}`))));
  }

  const page = h("div", { class: "page" },
    h("div", { class: "page-head" },
      h("div", null,
        h("div", { class: "eyebrow" }, `${t("nav.production")} · ${t("stage.make")}`),
        h("h1", { class: "page-title" }, t("production.title")),
        h("p", { class: "page-sub" }, t("production.subtitle"))),
      h("div", { class: "head-actions" },
        h("button", { class: "ghost-btn", onclick: () => ctx.openAssistant() }, icon("sparkles", "sm"), t("assistant.title")),
        h("button", { class: "primary-btn", onclick: () => ctx.openCreate({ type: "production" }) }, icon("plus", "sm"), t("production.add")))),
    h("div", { class: "prod-grid" },
      h("div", { class: "stack" },
        h("section", { class: "panel" },
          h("div", { class: "panel-head" }, h("h3", null, t("production.palette")),
            h("span", { class: "muted", style: { fontSize: "10px" } }, `${colors.length}`)),
          paletteBody),
        h("div", { class: "style-board" },
          h("section", { class: "panel" },
            h("div", { class: "panel-head" }, h("h3", null, t("production.type"))),
            fontBody),
          h("section", { class: "panel" },
            h("div", { class: "panel-head" }, h("h3", null, t("production.versions"))),
            roleBody)),
        h("section", { class: "panel" },
          h("div", { class: "panel-head" }, h("h3", null, t("production.assets")),
            h("span", { class: "muted", style: { fontSize: "10px" } }, `${assets.length}`)),
          assetBody)),
      h("div", { class: "stack" },
        h("section", { class: "panel" },
          h("div", { class: "panel-head" }, h("h3", null, t("plan.story.title")),
            h("button", { class: "link", onclick: () => ctx.navigate("plan", "story") }, t("action.see_all"))),
          h("div", { class: "muted", style: { fontSize: "11px", lineHeight: "1.6" } }, t("production.story_hint"))),
        h("section", { class: "panel" },
          h("div", { class: "panel-head" }, h("h3", null, t("common.updated"))),
          recent))));
  host.appendChild(page);
}

function collectTags(units, ns) {
  const seen = new Map();
  for (const unit of units) {
    for (const tag of unit.tags || []) {
      if (!tag.startsWith(`${ns}:`)) continue;
      if (!seen.has(tag)) seen.set(tag, { tag, value: tag.slice(ns.length + 1) });
    }
  }
  return [...seen.values()];
}

function localMedia(unit) {
  return (unit.media || []).find((item) => item.url && !/^https?:\/\//i.test(item.url)) || null;
}

async function openUnitExternal(ctx, unit) {
  try {
    await api.openExternal({ unit: unit.uid });
    ctx.toast(t("toast.opened"), "ok");
  } catch (err) { ctx.toast(err.message, "err"); }
}

async function openReport(ctx, unit) {
  let full = unit;
  try {
    const data = await api.unit(unit.uid);
    full = data.unit || unit;
  } catch (err) {
    ctx.toast(err.message, "err");
    return;
  }
  const media = full.media || [];
  const hasLocal = Boolean(localMedia(full));
  const body = h("div", { class: "report-reader" },
    full.body ? richText(full.body) : h("div", { class: "empty sm" }, t("production.no_report_body")),
    media.length ? h("div", { class: "report-media-grid" }, media.map((item) =>
      item.is_image
        ? h("a", {
          class: "report-media", href: mediaUrl(item), target: "_blank", rel: "noreferrer noopener", title: t("viewer.open"),
          onclick: (event) => {
            if (event.ctrlKey || event.metaKey || event.shiftKey || event.button !== 0) return; // keep "open in new tab"
            event.preventDefault();
            openImageLightbox({ src: mediaUrl(item), alt: item.caption || full.title, title: item.caption || item.url });
          },
        },
          h("img", { src: mediaUrl(item), alt: item.caption || full.title, loading: "lazy" }),
          h("span", null, item.caption || item.url))
        : h("a", { class: "report-media file", href: mediaUrl(item), target: "_blank", rel: "noreferrer noopener" },
          icon("file", "sm"), item.caption || item.url))) : null);
  openModal({
    title: full.title || t("production.report"),
    wide: true,
    body,
    actions: [
      ...(hasLocal ? [{
        label: t("action.open_external"), ghost: true, close: false,
        onClick: async () => { await openUnitExternal(ctx, full); return false; },
      }] : []),
      { label: t("production.edit_node"), ghost: true, onClick: (event, close) => { close(); ctx.openUnit(full.uid); } },
      { label: t("editor.open_in_editor"), ghost: true, onClick: (event, close) => { close(); ctx.openUnitInEditor?.(full); } },
      { label: t("production.open_on_map"), ghost: true, onClick: (event, close) => { close(); ctx.openUnit(full.uid); ctx.navigate("map"); } },
      { label: t("action.close") },
    ],
  });
}

function assetGrid(ctx, assets) {
  const grid = h("div", { class: "asset-grid" });
  for (const unit of assets) {
    const media = (unit.media || []).find((m) => m.is_image) || null;
    const imageUrl = media ? mediaUrl(media) : "";
    const hasReport = Boolean(unit.body) || ["artifact", "resource"].includes(unit.type);
    const hasLocal = Boolean(localMedia(unit));
    grid.appendChild(h("div", {
      class: "asset", role: "button", tabindex: "0", dataset: { unitRef: unit.uid },
      onclick: () => hasReport ? openReport(ctx, unit) : ctx.openUnit(unit.uid),
      onkeydown: (event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          hasReport ? openReport(ctx, unit) : ctx.openUnit(unit.uid);
        }
      },
    },
      h("div", { class: "thumb" },
        imageUrl ? h("img", { src: imageUrl, alt: unit.title, loading: "lazy" })
          : h("div", { class: "ph" }, typeLabel(unit.type))),
      h("div", { class: "info" },
        h("div", { class: "t" }, unit.title),
        h("div", { class: "m" }, `${statusLabel(unit.status)} · ${(unit.tags || []).find((x) => x.startsWith("role:"))?.split(":")[1] || typeLabel(unit.type)}`),
        h("div", { class: "asset-actions" },
          hasReport ? h("button", {
            class: "link", type: "button",
            onclick: (event) => { event.stopPropagation(); openReport(ctx, unit); },
          }, t("production.view_report")) : null,
          hasLocal ? h("button", {
            class: "link", type: "button", title: t("action.open_external"),
            onclick: (event) => { event.stopPropagation(); openUnitExternal(ctx, unit); },
          }, t("action.open_external")) : null))));
  }
  return grid;
}
