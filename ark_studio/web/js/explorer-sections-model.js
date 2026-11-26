// The Editor's Explorer is one panel with stacked, collapsible sections
// (VS Code style): Files, Outline, Timeline and Search.  This module keeps
// their state (collapsed, sizes, the maximized one) and the resize math;
// pure and DOM-free (tests/js/explorer_sections_model.test.mjs).
// `sectionsModel(spec)` makes the same model for another explorer (Research ›
// Topics uses it for Topics, Lines and Outline).

export const SECTION_IDS = ["files", "outline", "timeline", "search"];
export const SECTIONS_KEY = "ark.editor.sections.v1";
export const SECTION_MIN = 72;          // px of an expanded section while resizing

/**
 * The section model for one explorer.  `spec`: { ids, sizes: {id: px},
 * collapsed: {id: bool} } (defaults).
 */
export function sectionsModel({ ids, sizes: defaultSizes = {}, collapsed: defaultCollapsed = {} }) {
  const IDS = [...ids];

  /** A valid state from anything stored (unknown ids and bad values dropped). */
  function normalizeSections(raw) {
    const src = raw && typeof raw === "object" ? raw : {};
    const collapsed = {};
    const sizes = {};
    for (const id of IDS) {
      const c = src.collapsed?.[id];
      collapsed[id] = typeof c === "boolean" ? c : Boolean(defaultCollapsed[id]);
      const size = Number(src.sizes?.[id]);
      sizes[id] = Number.isFinite(size) && size >= 20 && size <= 5000 ? Math.round(size) : (defaultSizes[id] || 240);
    }
    const max = IDS.includes(src.max) ? src.max : "";
    return { collapsed, sizes, max };
  }

  function parseSections(text) {
    try { return normalizeSections(JSON.parse(text || "{}")); } catch { return normalizeSections(null); }
  }

  /** Collapse or expand one section.  Collapsing the maximized one restores the others. */
  function toggleSection(state, id, open = state.collapsed[id]) {
    if (!IDS.includes(id)) return state;
    const next = { ...state, collapsed: { ...state.collapsed, [id]: !open } };
    if (!open && state.max === id) next.max = "";
    if (open && state.max && state.max !== id) next.max = "";
    return next;
  }

  /** Maximize a section (the others show only their headers), or restore. */
  function toggleMaximized(state, id) {
    if (!IDS.includes(id)) return state;
    if (state.max === id) return { ...state, max: "" };
    return { ...state, max: id, collapsed: { ...state.collapsed, [id]: false } };
  }

  /** Show a section: expanded, and the maximized one restored when it is another. */
  function revealSection(state, id) {
    if (!IDS.includes(id)) return state;
    const next = { ...state, collapsed: { ...state.collapsed, [id]: false } };
    if (state.max && state.max !== id) next.max = "";
    return next;
  }

  /** What each section shows: "open" (with a flex weight), or "head" (header only). */
  function sectionLayout(state) {
    return IDS.map((id) => {
      const open = state.max ? state.max === id : !state.collapsed[id];
      return { id, open, weight: state.max ? 1 : state.sizes[id] };
    });
  }

  /**
   * Drag the sash above section `below`: the nearest open section above it
   * and the first open section from `below` on share their heights `heights`
   * (px, by id).  Returns the new sizes, or null when nothing can move.
   */
  function resizeAt(state, below, heights, delta, min = SECTION_MIN) {
    const layout = sectionLayout(state);
    const index = IDS.indexOf(below);
    if (index <= 0 || state.max) return null;
    const up = layout.slice(0, index).filter((item) => item.open).pop();
    const down = layout.slice(index).find((item) => item.open);
    if (!up || !down) return null;
    const a = Number(heights[up.id]) || 0;
    const b = Number(heights[down.id]) || 0;
    const pair = a + b;
    if (pair < 2 * min) return null;
    const first = Math.max(min, Math.min(pair - min, a + delta));
    return { ...state.sizes, [up.id]: Math.round(first), [down.id]: Math.round(pair - first) };
  }

  return { ids: IDS, normalizeSections, parseSections, toggleSection, toggleMaximized, revealSection, sectionLayout, resizeAt };
}

// The Editor's Explorer.
const editor = sectionsModel({
  ids: SECTION_IDS,
  sizes: { files: 320, outline: 220, timeline: 160, search: 300 },
  collapsed: { files: false, outline: false, timeline: false, search: true },
});
export const {
  normalizeSections, parseSections, toggleSection, toggleMaximized, revealSection, sectionLayout, resizeAt,
} = editor;
