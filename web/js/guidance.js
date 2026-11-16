import { h, clear } from "./dom.js";
import { t, typeLabel, statusLabel } from "./i18n.js";
import { icon } from "./icons.js";
import { GraphView } from "./graph.js";
import { GUIDANCE_STEPS, guidanceKey, readGuidance, saveGuidance } from "./guidance-model.js";

export function mountGuidance(ctx) {
  const key = guidanceKey(ctx.S.session?.user);
  let storage;
  try { storage = localStorage; } catch { storage = null; }
  let state = readGuidance(storage, key);
  let root = null, coach = null, spotlight = null, next = null, feedback = null;
  let practice = null, practiceHost = null, timer = null, step = 0, done = false;
  let previousFocus = null, invitation = null, filterBar = null;
  let sampleNodes = [], sampleEdges = [], renamed = false, filtered = false, sampleSeq = 0;
  let pending = false;
  let promptPanel = null, promptWasCollapsed = false;
  let settingsRestore = null, settingsTarget = null, settingsViewport = "";
  const roots = ["tour-root-a", "tour-root-b"];

  const save = (status) => { state = saveGuidance(storage, key, { status, step }); };
  const button = (label, symbol, action, className = "btn") => h("button", {
    type: "button", class: className, onclick: action,
  }, symbol ? icon(symbol, "sm") : null, label);
  const tool = (label, symbol, action) => h("button", {
    type: "button", class: "icon-btn", title: label, "aria-label": label, onclick: action,
  }, icon(symbol, "sm"));

  function destroyPractice() {
    practice?.destroy();
    practice = null;
    practiceHost?.remove();
    practiceHost = null;
    filterBar = null;
  }

  function close(status = "paused") {
    if (!root) return;
    save(status);
    clearInterval(timer);
    timer = null;
    document.removeEventListener("keydown", onKey, true);
    destroyPractice();
    restorePrompts();
    restoreSettings();
    document.body.classList.remove("guidance-mobile");
    document.body.style.removeProperty("--guidance-coach-h");
    root.remove();
    root = null;
    if (previousFocus?.isConnected) previousFocus.focus({ preventScroll: true });
    if (status === "paused") invite();
  }

  function onKey(event) {
    if (event.key !== "Escape") return;
    if (event.target.closest?.(".graph-mind-editor")) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    close();
  }

  function invite() {
    invitation?.remove();
    const resume = state.status === "paused";
    invitation = h("aside", { class: "guidance-invite", "aria-label": t("tour.title") },
      h("div", { class: "guidance-invite-copy" }, h("b", null, t("tour.title")),
        h("p", null, t(resume ? "tour.resume_hint" : "tour.welcome"))),
      h("div", { class: "guidance-actions" },
        button(t("tour.later"), null, () => { if (!resume) save("dismissed"); invitation.remove(); }, "btn ghost"),
        button(t(resume ? "tour.resume" : "tour.start"), "play", () => start(), "btn primary")));
    document.body.appendChild(invitation);
  }

  function makePractice() {
    if (practice) return;
    const graphHost = h("div", { class: "guidance-graph graph-host", "data-tour-practice": "" });
    practiceHost = h("section", { class: "guidance-practice", "aria-label": t("tour.practice") },
      h("div", { class: "guidance-practice-head" },
        h("b", null, t("tour.practice")), h("span", null, t("tour.practice_note"))), graphHost);
    root.appendChild(practiceHost);
    // These callbacks only update the practice graph, never project units.
    practice = new GraphView(graphHost, {
      layout: "mind", mode: "edit", persistMode: false, persistLayout: false,
      cameraKey: "guidance-practice", edgeFilterKey: "ark.guidance.edges.v2",
      onRename: () => { renamed = true; },
      onAddNode: (parent, where) => {
        const edge = practice.edges.find((item) => item.source === parent.id && item.relation === "part_of");
        return { unit: newSample(), parentId: where === "child" ? parent.id : edge?.target };
      },
      onAddOrphan: () => ({ unit: newSample() }),
      onConnect: () => ({ link: { id: `tour-link-${++sampleSeq}` }, relation: "part_of" }),
    });
    // The shared context menu includes project assignment commands.
    graphHost.addEventListener("contextmenu", (event) => {
      event.preventDefault(); event.stopImmediatePropagation();
    }, true);
    graphHost.addEventListener("keydown", (event) => {
      if (event.key !== "Tab" || event.target.closest("input,textarea,select,button")) return;
      if (!event.shiftKey && practice.mode === "edit" && practice.selectedNodes.size) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      (next?.disabled ? coach.querySelector("button:not(:disabled)") : next)?.focus();
    }, true);
    filterBar = h("div", { class: "guidance-filter-bar", hidden: true });
    practiceHost.insertBefore(filterBar, graphHost);
  }

  function newSample() {
    const id = `tour-added-${++sampleSeq}`;
    return { id, uid: `P-${sampleSeq + 4}`, title: t("common.untitled"), type: "note", status: "open" };
  }

  function showFilters() {
    clear(filterBar);
    filterBar.hidden = GUIDANCE_STEPS[step].id !== "filter";
    if (filterBar.hidden) return;
    const query = h("input", { class: "input", type: "search", "aria-label": t("tour.filter.search"),
      placeholder: t("tour.filter.search"), "data-tour-filter": "query" });
    const types = h("select", { class: "input", "aria-label": t("common.type"), "data-tour-filter": "type" },
      h("option", { value: "" }, t("tour.filter.all_types")),
      [...new Set(sampleNodes.map((n) => n.type))].map((type) => h("option", { value: type }, typeLabel(type))));
    const statuses = h("select", { class: "input", "aria-label": t("common.status"), "data-tour-filter": "status" },
      h("option", { value: "" }, t("tour.filter.all_statuses")),
      [...new Set(sampleNodes.map((n) => n.status))].map((status) => h("option", { value: status }, statusLabel(status))));
    const apply = () => {
      const q = query.value.trim().toLowerCase();
      const nodes = sampleNodes.filter((n) => (!q || `${n.title} ${n.uid}`.toLowerCase().includes(q))
        && (!types.value || n.type === types.value) && (!statuses.value || n.status === statuses.value));
      const ids = new Set(nodes.map((n) => n.id));
      practice.setData(nodes, sampleEdges.filter((e) => ids.has(e.source) && ids.has(e.target)), { layout: practice.layout });
      practice.fit();
      if (nodes.length > 0 && nodes.length < sampleNodes.length) filtered = true;
    };
    query.addEventListener("input", apply);
    types.addEventListener("change", apply);
    statuses.addEventListener("change", apply);
    filterBar.append(query, types, statuses,
      tool(t("tour.filter.clear"), "x", () => { query.value = types.value = statuses.value = ""; apply(); }));
  }

  function resetPractice() {
    makePractice();
    renamed = filtered = false;
    sampleNodes = [
      { id: roots[0], uid: "P-1", title: t("tour.sample.question"), type: "question", status: "open" },
      { id: "tour-child-a", uid: "P-2", title: t("tour.sample.evidence"), type: "evidence", status: "done", tags: ["marker:star-yellow"] },
      { id: roots[1], uid: "P-3", title: t("tour.sample.plan"), type: "task", status: "active" },
      { id: "tour-child-b", uid: "P-4", title: t("tour.sample.result"), type: "artifact", status: "review" },
    ];
    practice.collapsed.clear();
    practice._setSelection([]);
    sampleEdges = [
      { id: "tour-edge-a", source: "tour-child-a", target: roots[0], relation: "part_of" },
      { id: "tour-edge-b", source: "tour-child-b", target: roots[1], relation: "part_of" },
      { id: "tour-edge-c", source: "tour-child-a", target: "tour-child-b", relation: "supports" },
    ];
    const item = GUIDANCE_STEPS[step];
    practice.setLayout(item.layout);
    practice.setMode(item.id === "filter" || item.id === "relations" ? "read" : "edit", { persist: false });
    practice.setData(sampleNodes, sampleEdges, { layout: item.layout });
    practice._toggleEdgeFilter(item.id === "relations");
    showFilters();
    practice.fit();
    practice._draw();
  }

  function completed() {
    const id = GUIDANCE_STEPS[step].id;
    return id === "edit" ? renamed : id === "filter" && filtered;
  }

  function visibleTarget() {
    const item = GUIDANCE_STEPS[step];
    if (item.surface) return practiceHost;
    if (ctx.S.workspace !== item.workspace) return null;
    if (item.settingsTab) {
      const scope = document.querySelector('.settings-scope-btn[data-settings-scope="project"]');
      const tab = document.querySelector(`[data-settings-tab="${item.settingsTab}"]`);
      if (!scope || !tab) return null;
      if (!settingsRestore) {
        settingsRestore = {
          scope: document.querySelector('.settings-scope-btn[aria-selected="true"]')?.dataset.settingsScope,
          tab: document.querySelector('.settings-nav-item.active')?.dataset.settingsTab,
        };
        try {
          settingsRestore.savedScope = storage?.getItem("ark.settingsScope");
          settingsRestore.savedTab = storage?.getItem("ark.settingsTab");
        } catch { /* preferences can be unavailable */ }
      }
      if (scope.getAttribute("aria-selected") !== "true") scope.click();
      if (!tab.classList.contains("active")) tab.click();
      const target = document.querySelector(item.target);
      const viewport = `${innerWidth}:${innerHeight}:${target?.offsetHeight}`;
      if (target && (target !== settingsTarget || viewport !== settingsViewport)) {
        settingsTarget = target; settingsViewport = viewport;
        target.scrollIntoView({ block: "start", behavior: "instant" });
      }
      return target;
    }
    if (item.id === "prompts") {
      const panel = document.querySelector(item.target);
      if (!panel) return null;
      if (panel !== promptPanel) {
        restorePrompts();
        promptPanel = panel;
        promptWasCollapsed = panel.classList.contains("collapsed");
        panel.scrollIntoView({ block: "start" });
      }
      panel.classList.remove("collapsed");
      const toggle = panel.querySelector("[data-start-toggle]");
      if (toggle) toggle.textContent = t("start.hide");
      return panel;
    }
    const content = document.querySelector(".workspace-view-host");
    if (!content?.children.length) return null;
    return content;
  }

  function restorePrompts() {
    if (promptPanel?.isConnected) {
      promptPanel.classList.toggle("collapsed", promptWasCollapsed);
      const toggle = promptPanel.querySelector("[data-start-toggle]");
      if (toggle) toggle.textContent = t(promptWasCollapsed ? "start.show" : "start.hide");
    }
    promptPanel = null;
  }

  function restoreSettings() {
    if (!settingsRestore) return;
    const previous = settingsRestore;
    document.querySelector(`.settings-scope-btn[data-settings-scope="${previous.scope}"]`)?.click();
    document.querySelector(`[data-settings-tab="${previous.tab}"]`)?.click();
    try {
      for (const [key, value] of [["ark.settingsScope", previous.savedScope], ["ark.settingsTab", previous.savedTab]]) {
        if (value == null) storage?.removeItem(key); else storage?.setItem(key, value);
      }
    } catch { /* preferences can be unavailable */ }
    settingsRestore = settingsTarget = null;
    settingsViewport = "";
  }

  function tick() {
    if (!root) return;
    const item = GUIDANCE_STEPS[step];
    if (item.settingsTab) document.body.style.setProperty("--guidance-coach-h", `${coach.offsetHeight}px`);
    const target = visibleTarget();
    if (!item.practice) {
      next.disabled = !target;
      feedback.textContent = t(target ? "tour.explore" : "tour.return_hint");
    } else if (!done && completed()) {
      done = true;
      next.disabled = false;
      feedback.textContent = t("tour.success");
      feedback.classList.add("success");
    }
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
    restoreSettings();
    const item = GUIDANCE_STEPS[step];
    document.body.classList.toggle("guidance-mobile", item.id === "mobile");
    save("paused");
    done = false;
    if (item.surface) resetPractice();
    else {
      destroyPractice();
      ctx.navigateWorkspace(item.workspace);
    }
    root.dataset.step = item.id;
    root.classList.toggle("has-practice", !!item.surface);
    clear(coach);
    feedback = h("p", { class: "guidance-feedback", role: "status", "aria-live": "polite" },
      t(item.practice ? "tour.try" : "tour.explore"));
    next = button(t(step === GUIDANCE_STEPS.length - 1 ? "tour.finish" : "tour.next"),
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
      h("h2", { id: "guidance-heading" }, t(`tour.${item.id}.title`)),
      h("p", { class: "guidance-task" }, t(`tour.${item.id}.body`)),
      feedback,
      h("div", { class: "guidance-actions" }, back,
        item.surface
          ? button(t("tour.reset"), "refresh", () => { done = false; resetPractice(); next.disabled = !!item.practice;
            feedback.textContent = t(item.practice ? "tour.try" : "tour.explore"); feedback.classList.remove("success"); }, "btn ghost")
          : button(t("tour.open_tab"), "external", () => ctx.navigateWorkspace(item.workspace), "btn ghost"),
        item.practice ? button(t("tour.skip"), null, () => advance(), "btn ghost") : null,
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
    }
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
  return { start, pause: () => close(), get active() { return !!root; },
    get step() { return GUIDANCE_STEPS[step].id; },
    relocalize() { if (root) renderStep(); else if (invitation?.isConnected) invite(); } };
}
