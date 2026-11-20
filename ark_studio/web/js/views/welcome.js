// First-run Welcome (UI line A, UX-04): once per user, three ways in instead
// of landing in the PredActor example with its dashboard:
//   1. my research: a new project; the question becomes its first Question
//   2. the 10-minute tutorial: ARK Studio Demo, chapter 1
//   3. the full example: PredActor
// Shown only where it helps (home-model.js shouldShowWelcome); any choice,
// Escape or the close button marks it seen for this user in this browser.
import { h } from "../dom.js";
import { t, getLang } from "../i18n.js";
import { icon } from "../icons.js";
import { api } from "../api.js";
import { shouldShowWelcome, tutorialChapters } from "../home-model.js";

const TUTORIAL_ID = "ark-demo";

export function welcomeKey(user = "local") {
  return `ark.welcome.v1:${encodeURIComponent(String(user || "local"))}`;
}

function seen(key) {
  try { return localStorage.getItem(key); } catch { return "unavailable"; }
}

function exampleProject(ctx) {
  return (ctx.S.projects || []).find((p) => p.demo && p.id !== TUTORIAL_ID && /predactor/i.test(p.name || ""))
    || (ctx.S.projects || []).find((p) => p.demo && p.id !== TUTORIAL_ID) || null;
}

async function openFirstChapter(ctx) {
  try {
    const data = await api.units({ tags: "tutorial:chapter", limit: 40, slim: 1 });
    const first = tutorialChapters(data?.items || data?.units || [], getLang())[0];
    if (first) ctx.openUnit(first.uid, null, { readOnly: true, revealPane: true });
  } catch { /* the Home banner lists the chapters */ }
}

/** Show the Welcome if this user has not seen it; returns whether it shows. */
export function maybeShowWelcome(ctx) {
  const key = welcomeKey(ctx.S.session?.user);
  if (!shouldShowWelcome({ stored: seen(key), project: ctx.S.project, stats: ctx.S.stats })) return false;
  openWelcome(ctx, key);
  return true;
}

export function openWelcome(ctx, key = welcomeKey(ctx.S.session?.user)) {
  document.querySelector(".welcome-layer")?.remove();
  const previous = document.activeElement;
  let busy = false;
  const close = () => {
    try { localStorage.setItem(key, new Date().toISOString()); } catch { /* storage unavailable */ }
    layer.remove();
    document.removeEventListener("keydown", onKey, true);
    if (previous?.isConnected) previous.focus({ preventScroll: true });
    ctx.guidance?.invite?.();
  };
  const onKey = (event) => {
    if (event.key !== "Escape") return;
    event.preventDefault();
    event.stopImmediatePropagation();
    close();
  };
  const run = async (button, action) => {
    if (busy) return;
    busy = true;
    button.disabled = true;
    try {
      await action();
      close();
    } catch (err) {
      ctx.toast(err.message, "err");
    } finally {
      busy = false;
      button.disabled = false;
    }
  };

  // 1. My research.
  const name = h("input", { class: "input", id: "welcome-name", maxlength: "80",
    placeholder: t("welcome.mine.name_placeholder"), "aria-label": t("welcome.mine.name") });
  const question = h("textarea", { class: "textarea", id: "welcome-question", rows: "2", maxlength: "300",
    placeholder: t("welcome.mine.question_placeholder"), "aria-label": t("welcome.mine.question") });
  const create = h("button", { class: "btn primary", type: "button", "data-welcome": "mine" },
    icon("plus", "sm"), t("welcome.mine.create"));
  create.onclick = () => run(create, async () => {
    const text = question.value.trim();
    const title = name.value.trim() || text.split(/[?？.。\n]/)[0].trim().slice(0, 60);
    if (!title) { name.focus(); throw new Error(t("welcome.mine.required")); }
    const data = await api.createProject({ name: title, description: text, template: "blank" });
    ctx.applyBootstrap(data);
    if (text) {
      // The question is the project's first node; Home's checklist ticks it.
      await api.createUnit({ type: "question", title: text.slice(0, 200), status: "open" });
      ctx.applyBootstrap(await api.bootstrap({ fresh: true }));
      ctx.refresh();
    }
    ctx.toast(t("project.toast.created", { name: title }), "ok");
  });
  for (const field of [name, question]) {
    field.addEventListener("keydown", (event) => {
      if (event.key === "Enter" && (field === name || event.ctrlKey || event.metaKey)) { event.preventDefault(); create.click(); }
    });
  }

  // 2. The tutorial; 3. the full example.
  const tutorial = h("button", { class: "btn", type: "button", "data-welcome": "tutorial" },
    icon("play", "sm"), t("welcome.tutorial.open"));
  tutorial.onclick = () => run(tutorial, async () => {
    await ctx.switchProject({ id: TUTORIAL_ID, name: t("welcome.tutorial.name") });
    openFirstChapter(ctx);
  });
  const example = exampleProject(ctx);
  const exampleBtn = example ? h("button", { class: "btn", type: "button", "data-welcome": "example" },
    icon("map", "sm"), t("welcome.example.open")) : null;
  if (exampleBtn) {
    exampleBtn.onclick = () => run(exampleBtn, async () => {
      if (example.id !== ctx.S.project?.id) await ctx.switchProject(example);
    });
  }

  const card = (id, ic, title, body, ...content) => h("section", { class: `welcome-card welcome-${id}` },
    h("div", { class: "welcome-card-head" }, h("span", { class: "welcome-ic" }, icon(ic, "sm")), h("h2", null, title)),
    h("p", { class: "muted" }, body), ...content);

  const layer = h("div", { class: "welcome-layer", role: "dialog", "aria-modal": "true", "aria-labelledby": "welcome-title" },
    h("div", { class: "welcome-panel" },
      h("button", { class: "icon-btn welcome-close", type: "button", title: t("welcome.skip"), "aria-label": t("welcome.skip"),
        onclick: close }, icon("x", "sm")),
      h("div", { class: "welcome-head" },
        h("h1", { id: "welcome-title" }, t("welcome.title")),
        h("p", { class: "muted" }, t("welcome.lead"))),
      h("div", { class: "welcome-cards" },
        card("mine", "bulb", t("welcome.mine.title"), t("welcome.mine.body"),
          h("div", { class: "welcome-fields" }, name, question), h("div", { class: "welcome-actions" }, create)),
        card("tutorial", "route", t("welcome.tutorial.title"), t("welcome.tutorial.body"),
          h("div", { class: "welcome-actions" }, tutorial)),
        example ? card("example", "layers", t("welcome.example.title"), t("welcome.example.body", { name: example.name }),
          h("div", { class: "welcome-actions" }, exampleBtn)) : null),
      h("div", { class: "welcome-foot muted" }, t("welcome.foot"))));
  document.body.appendChild(layer);
  document.addEventListener("keydown", onKey, true);
  setTimeout(() => name.focus({ preventScroll: true }), 0);
  return { close };
}
