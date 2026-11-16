const STORAGE_KEY = "ark.mind.v1";
export const MIND_DEFAULTS = { align: "left", autoLayout: true, markerScale: 1.35, markerGap: 0.55, markerArc: 0.4, mindMarkerScale: 0.7, showMindImages: true, nodeTransitions: true };

export function getMindSettings() {
  try {
    const parsed = JSON.parse(localStorage.getItem(STORAGE_KEY) || "{}");
    const scale = Number(parsed?.markerScale);
    const gap = Number(parsed?.markerGap);
    const arc = Number(parsed?.markerArc);
    const mindScale = Number(parsed?.mindMarkerScale);
    return {
      ...MIND_DEFAULTS,
      ...(parsed && typeof parsed === "object" ? parsed : {}),
      align: parsed?.align === "center" ? "center" : "left",
      autoLayout: parsed?.autoLayout !== false,
      markerScale: Number.isFinite(scale) ? Math.max(0.5, Math.min(2.5, scale)) : MIND_DEFAULTS.markerScale,
      markerGap: Number.isFinite(gap) ? Math.max(0, Math.min(2, gap)) : MIND_DEFAULTS.markerGap,
      markerArc: Number.isFinite(arc) ? Math.max(0.1, Math.min(1, arc)) : MIND_DEFAULTS.markerArc,
      mindMarkerScale: Number.isFinite(mindScale) ? Math.max(0.3, Math.min(2, mindScale)) : MIND_DEFAULTS.mindMarkerScale,
      showMindImages: parsed?.showMindImages !== false,
      nodeTransitions: parsed?.nodeTransitions !== false,
    };
  } catch {
    return { ...MIND_DEFAULTS };
  }
}

export function setMindSettings(patch) {
  const next = { ...getMindSettings(), ...(patch || {}) };
  next.align = next.align === "center" ? "center" : "left";
  next.autoLayout = next.autoLayout !== false;
  const scale = Number(next.markerScale);
  const gap = Number(next.markerGap);
  const arc = Number(next.markerArc);
  const mindScale = Number(next.mindMarkerScale);
  next.markerScale = Number.isFinite(scale) ? Math.max(0.5, Math.min(2.5, scale)) : MIND_DEFAULTS.markerScale;
  next.markerGap = Number.isFinite(gap) ? Math.max(0, Math.min(2, gap)) : MIND_DEFAULTS.markerGap;
  next.markerArc = Number.isFinite(arc) ? Math.max(0.1, Math.min(1, arc)) : MIND_DEFAULTS.markerArc;
  next.mindMarkerScale = Number.isFinite(mindScale) ? Math.max(0.3, Math.min(2, mindScale)) : MIND_DEFAULTS.mindMarkerScale;
  next.showMindImages = next.showMindImages !== false;
  next.nodeTransitions = next.nodeTransitions !== false;
  localStorage.setItem(STORAGE_KEY, JSON.stringify(next));
  return next;
}
