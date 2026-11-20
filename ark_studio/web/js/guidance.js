import { h, clear } from "./dom.js";
import { t, getLang } from "./i18n.js";
import { icon } from "./icons.js";
import { api } from "./api.js";
import { GUIDANCE_STEPS, guidanceFinish, guidanceKey, readGuidance, saveGuidance } from "./guidance-model.js";
import { tutorialTopic } from "./home-model.js";

// The interactive guide: five steps around the core loop (guidance-model.js).
// Each step opens a workspace and frames the whole page or one element of it.
export function mountGuidance(ctx) {
  const key = guidanceKey(ctx.S.session?.user);
  let storage;
  try { storage = localStorage; } catch { storage = null; }
  let state = readGuidance(storage, key);
  let root = null, coach = null, spotlight = null, next = null, feedback = null;
  let timer = null, step = 0;
  let previousFocus = null, invitation = null;
  let pending = false;
  let promptPanel = null, promptWasCollapsed = false, framed = null;

  const save = (status) => { state = saveGuidance(storage, key, { status, step }); };
  const button = (label, symbol, action, className = "btn") => h("button", {
    type: "button", class: className, onclick: action,
  }, symbol ? icon(symbol, "sm") : null, label);
  const tool = (label, symbol, action) => h("button", {
    type: "button", class: "icon-btn", title: label, "aria-label": label, onclick: action,
  }, icon(symbol, "sm"));

  function close(status = "paused") {
    if (!root) return;
    save(status);
    clearInterval(timer);
    timer = null;
    document.removeEventListener("keydown", onKey, true);
    restorePrompts();
    root.remove();
    root = null;
    if (previousFocus?.isConnected) previousFocus.focus({ preventScroll: true });
    if (status === "paused") invite();
  }

  function onKey(event) {
    if (event.key !== "Escape") return;
    event.preventDefault();
    event.stopImmediatePropagation();
    close();
  }

  function invite() {
    invitation?.remove();
    // The first-run Welcome screen speaks first; the guide waits for it.
    if (document.querySelector(".welcome-layer")) return;
    const resume = state.status === "paused";
    invitation = h("aside", { class: "guidance-invite", "aria-label": t("tour.title") },
      h("div", { class: "guidance-invite-copy" }, h("b", null, t("tour.title")),
        h("p", null, t(resume ? "tour.resume_hint" : "tour.loop.welcome"))),
      h("div", { class: "guidance-actions" },
        button(t("tour.later"), null, () => { if (!resume) save("dismissed"); invitation.remove(); }, "btn ghost"),
        button(t(resume ? "tour.resume" : "tour.start"), "play", () => start(), "btn primary")));
    document.body.appendChild(invitation);
  }

  function visibleTarget() {
    const item = GUIDANCE_STEPS[step];
    if (ctx.S.workspace !== item.workspace) return null;
    if (item.target) {
      const el = document.querySelector(item.target);
      if (!el) return null;
      if (el !== framed) {
        restorePrompts();
        framed = el;
        // The agent card may be folded; open it for this step only.
        if (el.matches(".start-panel")) {
          promptPanel = el;
          promptWasCollapsed = el.classList.contains("collapsed");
          el.classList.remove("collapsed");
        }
        el.scrollIntoView({ block: "start" });
      }
      return el;
    }
    const content = document.querySelector(".workspace-view-host");
    if (!content?.children.length) return null;
    return content;
  }

  function restorePrompts() {
    if (promptPanel?.isConnected) promptPanel.classList.toggle("collapsed", promptWasCollapsed);
    promptPanel = null;
    framed = null;
  }

  function tick() {
    if (!root) return;
    const target = visibleTarget();
    next.disabled = !target;
    feedback.textContent = t(target ? "tour.explore" : "tour.return_hint");
    spotlight.hidden = !target;
    if (!target) return;
    const r = target.getBoundingClientRect();
    const top = Math.max(58, r.top), left = Math.max(6, r.left);
    Object.assign(spotlight.style, {
      left: `${left}px`, top: `${top}px`,
      width: `${Math.max(0, Math.min(innerWidth - 6, r.right) - left)}px`,
      height: `${Math.max(0, Math.min(innerHeight - 6, r.bottom) - top)}px`,
    });
  }

  function renderStep() {
    restorePrompts();
    const item = GUIDANCE_STEPS[step];
    save("paused");
    ctx.navigateWorkspace(item.workspace);
    root.dataset.step = item.id;
    clear(coach);
    feedback = h("p", { class: "guidance-feedback", role: "status", "aria-live": "polite" }, t("tour.explore"));
    const last = step === GUIDANCE_STEPS.length - 1;
    const finish = guidanceFinish(ctx.S.project) === "tutorial-topic" ? "tour.finish_tutorial" : "tour.finish";
    next = button(t(last ? finish : "tour.next"),
      "arrowRight", () => advance(), "btn primary");
    next.dataset.tourNext = "";
    next.disabled = true;
    const back = tool(t("tour.back"), "arrowLeft", async () => {
      if (step > 0 && await ctx.canStartGuidance()) { step--; renderStep(); }
    });
    back.disabled = step === 0;
    coach.append(
      h("div", { class: "guidance-coach-head" },
        h("span", null, t("tour.progress", { current: step + 1, total: GUIDANCE_STEPS.length })),
        tool(t("tour.pause"), "x", () => close())),
      h("progress", { max: GUIDANCE_STEPS.length, value: step + 1, "aria-label": t("tour.title") }),
      h("h2", { id: "guidance-heading" }, t(`tour.loop.${item.id}.title`)),
      h("p", { class: "guidance-task" }, t(`tour.loop.${item.id}.body`)),
      feedback,
      h("div", { class: "guidance-actions" }, back,
        button(t("tour.open_tab"), "external", () => ctx.navigateWorkspace(item.workspace), "btn ghost"),
        next));
    tick();
    coach.focus({ preventScroll: true });
  }

  async function advance() {
    if (ctx.canStartGuidance && !await ctx.canStartGuidance()) return;
    if (step < GUIDANCE_STEPS.length - 1) { step++; renderStep(); }
    else {
      close("completed");
      ctx.toast(t("tour.finished"), "ok");
      if (guidanceFinish(ctx.S.project) === "tutorial-topic") await openTutorialTopic();
    }
  }

  // In the tutorial project the guide ends on the tutorial as a topic graph.
  async function openTutorialTopic() {
    try {
      const data = await api.units({ tags: "tutorial:topic", limit: 10, slim: 1 });
      const topic = tutorialTopic(data?.items || data?.units || [], getLang());
      if (topic) ctx.openTopic(topic.uid);
    } catch { /* the guide is complete either way */ }
  }

  async function start({ restart = false } = {}) {
    if (pending) return;
    pending = true;
    // Leave dirty Editor buffers open until the user saves them.
    try {
      if (ctx.canStartGuidance && !await ctx.canStartGuidance()) return;
      if (root) close("paused");
      invitation?.remove();
      step = restart || state.status !== "paused" ? 0 : state.step;
      previousFocus = document.activeElement;
      spotlight = h("div", { class: "guidance-spotlight", "aria-hidden": "true" });
      coach = h("section", { class: "guidance-coach", tabindex: "-1", role: "region",
        "aria-labelledby": "guidance-heading" });
      root = h("div", { class: "guidance-layer", "data-tour": "" }, spotlight, coach);
      document.body.appendChild(root);
      document.addEventListener("keydown", onKey, true);
      renderStep();
      timer = setInterval(tick, 200);
    } finally { pending = false; }
  }

  if (ctx.S.project && (state.status === "new" || state.status === "paused")) invite();
  return { start, pause: () => close(), invite: () => { if (!root && (state.status === "new" || state.status === "paused")) invite(); },
    get active() { return !!root; },
    get step() { return GUIDANCE_STEPS[step].id; },
    relocalize() { if (root) renderStep(); else if (invitation?.isConnected) invite(); } };
}
